package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type queuedRequest struct {
	ID      string    `json:"id"`
	KeyID   string    `json:"client_key_id"`
	RouteID string    `json:"route_id"`
	Version int       `json:"version"`
	Started time.Time `json:"queued_at"`
	Stage   string    `json:"stage"`
	cancel  context.CancelFunc
}

func (a *App) signalAdmission() {
	if a.admissionChanged != nil {
		close(a.admissionChanged)
	}
	a.admissionChanged = make(chan struct{})
}
func (a *App) queueView() []queuedRequest {
	out := []queuedRequest{}
	for _, q := range a.queued {
		out = append(out, q)
	}
	return out
}

// Called and returned with mu held. Waiting never reserves an account slot or
// charges RPM/budgets. Body reads remain bounded by the ingress slot limit.
func (a *App) waitCapacity(w http.ResponseWriter, ctx context.Context, k ClientKey, s *Source, requestID, stage string) (bool, time.Duration) {
	started := time.Now()
	route, err := a.Store.route(k.RouteID)
	if err != nil || !route.Enabled || route.QueueLimit <= 0 {
		fail(w, 429, "调用容量已满，当前目标未开启排队", "")
		return false, 0
	}
	count := 0
	for _, queued := range a.queued {
		if queued.RouteID == route.ID {
			count++
		}
	}
	if count >= route.QueueLimit {
		fail(w, 429, "此路由等待队列已满", "")
		return false, 0
	}
	deadline := route.QueueTimeoutMS
	if deadline == 0 {
		deadline = 5000
	}
	queueCtx, cancel := context.WithTimeout(ctx, time.Duration(deadline)*time.Millisecond)
	a.queued[requestID] = queuedRequest{ID: requestID, KeyID: k.ID, RouteID: route.ID, Version: 1, Started: started.UTC(), Stage: stage, cancel: cancel}
	defer func() { cancel(); delete(a.queued, requestID); a.signalAdmission() }()
	for {
		if a.stopping || a.storageFailed.Load() {
			fail(w, 503, "服务已停止准入", "")
			return false, time.Since(started)
		}
		current, keyErr := a.Store.keyByID(k.ID)
		if keyErr != nil || !keyValid(current, time.Now()) {
			fail(w, 401, "排队中的 Key 已到期、停用或撤销", "")
			return false, time.Since(started)
		}
		if current.Version != k.Version {
			fail(w, 409, "排队期间 Key 已修改，请重新发送", "")
			return false, time.Since(started)
		}
		latest, routeErr := a.Store.route(route.ID)
		if routeErr != nil || !latest.Enabled || latest.Version != route.Version {
			fail(w, 409, "排队期间路由已修改", "")
			return false, time.Since(started)
		}
		ready := true
		if stage == "ingress" {
			ready = len(a.slots) < a.Config.MaxConcurrent && (k.Limits.MaxConcurrent == nil || a.keyActive[k.ID] < *k.Limits.MaxConcurrent)
		}
		if s != nil {
			source, sourceErr := a.Store.source(s.ID)
			if sourceErr != nil || source.Deleted || !source.Enabled || source.Generation != s.Generation || source.AccountGeneration != s.AccountGeneration || source.Version != s.Version {
				fail(w, 409, "排队期间来源已修改", "")
				return false, time.Since(started)
			}
			ready = ready && (s.MaxConcurrent == nil || a.accountActive[s.AccountID] < *s.MaxConcurrent)
		}
		ready = ready && (latest.MaxConcurrent == nil || a.routeActive[route.ID] < *latest.MaxConcurrent)
		if ready {
			return true, time.Since(started)
		}
		changed := a.admissionChanged
		a.mu.Unlock()
		select {
		case <-queueCtx.Done():
		case <-changed:
		}
		a.mu.Lock()
		if queueCtx.Err() != nil {
			status := 429
			message := "队列等待超时，未派发上游"
			if ctx.Err() != nil || queueCtx.Err() == context.Canceled {
				status = 499
				message = "队列已取消，未派发上游"
			}
			fail(w, status, message, "")
			return false, time.Since(started)
		}
	}
}
func (s *Store) keyByID(keyID string) (ClientKey, error) {
	var k ClientKey
	var raw string
	err := s.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", keyID).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &k)
	}
	return k, err
}

// The bounded background discriminator and the ordinary runner share one
// ingress lease. This prevents prefix buffering outside the global/key limits.
type dataIngressLease struct {
	KeyID, RequestID string
	Config           Config
	ConfigVersion    int
	QueuedFor        time.Duration
	Started          time.Time
}
type dataIngressContextKey struct{}

func dataIngress(r *http.Request) *dataIngressLease {
	value, _ := r.Context().Value(dataIngressContextKey{}).(*dataIngressLease)
	return value
}
func (a *App) acquireResponsesIngress(w http.ResponseWriter, r *http.Request) (*http.Request, func(), bool) {
	started := time.Now().UTC()
	a.mu.Lock()
	cfg := a.Config
	key, err := a.Store.keyByDigest(digest(bearer(r)))
	reject := func(code int, message string) (*http.Request, func(), bool) {
		a.mu.Unlock()
		fail(w, code, message, "")
		return r, nil, false
	}
	if err != nil || !keyValid(key, time.Now()) {
		return reject(401, "客户端 Key 无效或已撤销")
	}
	if !allowed(key.ProtocolAllowlist, "responses") {
		return reject(403, "Key 无此协议权限")
	}
	if a.stopping || a.storageFailed.Load() {
		return reject(503, "服务正在关闭或存储异常")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.TotalTimeout)*time.Second)
	requestID := id("req")
	var queuedFor time.Duration
	version := 1
	if settings, e := a.Store.readRuntimeSettings(); e == nil {
		version = settings.Version
	}
	if len(a.slots) >= a.Config.MaxConcurrent || key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent {
		if ok, wait := a.waitCapacity(w, ctx, key, nil, requestID, "ingress"); !ok {
			a.mu.Unlock()
			cancel()
			return r, nil, false
		} else {
			queuedFor = wait
		}
	}
	a.slots <- struct{}{}
	a.keyActive[key.ID]++
	lease := &dataIngressLease{KeyID: key.ID, RequestID: requestID, Config: cfg, ConfigVersion: version, QueuedFor: queuedFor, Started: started}
	a.mu.Unlock()
	release := func() {
		cancel()
		a.mu.Lock()
		<-a.slots
		a.keyActive[key.ID]--
		a.signalAdmission()
		a.mu.Unlock()
	}
	return r.WithContext(context.WithValue(ctx, dataIngressContextKey{}, lease)), release, true
}
