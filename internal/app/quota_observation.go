package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Only normalized observations are retained; raw bodies and provider identities
// are never archived. Embed CodexModelMetadata in SourceModel at integration.
const QuotaObservationSchema = `CREATE TABLE IF NOT EXISTS quota_snapshots(account_id TEXT NOT NULL REFERENCES accounts(id),dimension TEXT NOT NULL,observed_at TEXT NOT NULL,expires_at TEXT,account_generation INTEGER NOT NULL,status TEXT NOT NULL,value_json TEXT NOT NULL,PRIMARY KEY(account_id,dimension,observed_at));
CREATE INDEX IF NOT EXISTS quota_history ON quota_snapshots(account_id,observed_at DESC);`

const codexObservationAdapter = "codex-50d9c5de-observation-v1"
const quotaSQLTime = "2006-01-02T15:04:05.000000000Z"

type CodexModelMetadata struct {
	CodexCatalog *codexCatalogMetadata `json:"codex_catalog,omitempty"`
}
type codexCatalogMetadata struct {
	MaxContextWindow  *int64   `json:"max_context_window"`
	InputModalities   []string `json:"input_modalities"`
	ReasoningLevels   []string `json:"supported_reasoning_levels"`
	SupportedInAPI    *bool    `json:"supported_in_api"`
	AdapterVersion    string   `json:"adapter_version"`
	ClientVersion     string   `json:"client_version"`
	SourceGeneration  int      `json:"source_generation"`
	AccountGeneration int      `json:"account_generation"`
}
type codexQuotaScope struct {
	Account   string `json:"account"`
	Model     string `json:"model,omitempty"`
	Operation string `json:"operation,omitempty"`
}
type codexQuotaWindow struct {
	Dimension      string          `json:"dimension"`
	Unit           string          `json:"unit"`
	Scope          codexQuotaScope `json:"scope"`
	Name           string          `json:"limit_name"`
	MeteredFeature string          `json:"metered_feature,omitempty"`
	Status         string          `json:"status"`
	UsedPercent    *float64        `json:"used_percent"`
	Remaining      *float64        `json:"remaining"`
	Limit          *float64        `json:"limit"`
	LimitSeconds   *int64          `json:"limit_window_seconds"`
	WindowStart    *time.Time      `json:"window_start"`
	ResetAt        *time.Time      `json:"reset_at"`
	ObservedAt     time.Time       `json:"observed_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	Confidence     string          `json:"confidence"`
	AccountWide    bool            `json:"account_wide"`
	Errors         []string        `json:"errors,omitempty"`
}
type codexQuotaLimit struct {
	Name           string            `json:"limit_name"`
	MeteredFeature string            `json:"metered_feature,omitempty"`
	Allowed        *bool             `json:"allowed"`
	LimitReached   *bool             `json:"limit_reached"`
	Primary        *codexQuotaWindow `json:"primary_window"`
	Secondary      *codexQuotaWindow `json:"secondary_window"`
}
type codexQuotaCredits struct {
	Dimension  string  `json:"dimension"`
	Unit       string  `json:"unit"`
	Balance    *string `json:"balance"`
	HasCredits *bool   `json:"has_credits"`
	Unlimited  *bool   `json:"unlimited"`
}
type codexQuotaSnapshot struct {
	Status            string             `json:"status"`
	Provider          string             `json:"provider"`
	Scope             codexQuotaScope    `json:"scope"`
	SourceGeneration  int                `json:"source_generation"`
	AccountGeneration int                `json:"account_generation"`
	ObservedAt        *time.Time         `json:"observed_at"`
	ExpiresAt         *time.Time         `json:"expires_at"`
	AttemptedAt       time.Time          `json:"attempted_at"`
	NextAttempt       time.Time          `json:"next_attempt_at"`
	Failures          int                `json:"refresh_failures"`
	Confidence        string             `json:"confidence"`
	AdapterVersion    string             `json:"adapter_version"`
	Limits            []codexQuotaLimit  `json:"limits"`
	Windows           []codexQuotaWindow `json:"windows"`
	Credits           *codexQuotaCredits `json:"credits"`
	LastError         string             `json:"last_error,omitempty"`
}
type quotaObservationState struct {
	Flights map[string]*quotaObservationFlight
	// Caller holds a.mu. Root forwards to the shared subscriptionCredential
	// owner with rejectedAccess; a missing callback fails explicitly on 401.
	RefreshRejected func(context.Context, *Source, string) (Credential, error)
}
type quotaObservationFlight struct {
	Operation Operation
	Source    Source
	Kind      string
	Config    Config
}
type codexCatalogModel struct {
	ID       string
	Name     string
	Context  *int64
	Output   *int64
	Metadata codexCatalogMetadata
}
type codexCatalogCache struct {
	ETag              string    `json:"etag"`
	SourceGeneration  int       `json:"source_generation"`
	AccountGeneration int       `json:"account_generation"`
	ClientVersion     string    `json:"client_version"`
	ObservedAt        time.Time `json:"observed_at"`
}

func fixedCodexObservationSource(src Source) bool {
	return src.Kind == "codex_subscription" && src.Provider == "codex" && strings.TrimRight(src.BaseURL, "/") == "https://chatgpt.com/backend-api/codex"
}

func chatGPTDirectSource(src Source) bool {
	return src.Kind == "codex_subscription" && src.Provider == "codex" && strings.TrimRight(src.BaseURL, "/") == chatGPTResource
}
func quotaSnapshot(src Source) codexQuotaSnapshot {
	var q codexQuotaSnapshot
	b, _ := json.Marshal(src.Quota)
	_ = json.Unmarshal(b, &q)
	return q
}

// The persisted observation remains intact; readers must label an expired
// provider value stale even before the next scheduled refresh runs.
func expireQuotaObservation(src *Source, now time.Time) {
	q := quotaSnapshot(*src)
	if q.Status == "available" && q.ExpiresAt != nil && !q.ExpiresAt.After(now) {
		src.Quota["status"] = "stale"
	}
}

// Account DTOs project the same observation used by source reads and dispatch;
// no second persisted quota status needs to be synchronized.
func accountQuotaStatus(account Account, sources []Source) string {
	status := account.QuotaStatus
	var newest time.Time
	for _, src := range sources {
		if src.AccountID != account.ID || src.AccountGeneration != account.Generation {
			continue
		}
		q := quotaSnapshot(src)
		if q.ObservedAt == nil || q.Scope.Account != account.ID {
			continue
		}
		observed := *q.ObservedAt
		if q.AttemptedAt.After(observed) {
			observed = q.AttemptedAt
		}
		if !newest.IsZero() && !observed.After(newest) {
			continue
		}
		newest = observed
		status = q.Status
		if q.AccountGeneration != account.Generation || q.SourceGeneration != src.Generation {
			status = "stale"
		}
	}
	return status
}
func quotaJitter() time.Duration {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return time.Duration(b[0]%31) * time.Second
}
func quotaBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	if failures > 5 {
		failures = 5
	}
	return []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute}[failures-1]
}

// The existing source/model handlers already hold a.mu and enforce admin auth.
func (a *App) codexQuotaRefreshAPI(w http.ResponseWriter, r *http.Request, src Source, state *quotaObservationState) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	op, err := a.startCodexObservationLocked(src, "quota", state, time.Now().UTC())
	if err != nil {
		quotaObservationFail(w, err)
		return
	}
	if op == nil {
		writeJSON(w, 200, map[string]any{"source": src, "cached": true, "next_attempt_at": quotaSnapshot(src).NextAttempt})
		return
	}
	writeJSON(w, 202, op)
}
func (a *App) codexModelDiscoveryAPI(w http.ResponseWriter, r *http.Request, src Source, state *quotaObservationState) {
	op, err := a.startCodexObservationLocked(src, "models", state, time.Now().UTC())
	if err != nil {
		quotaObservationFail(w, err)
		return
	}
	writeJSON(w, 202, op)
}
func quotaObservationFail(w http.ResponseWriter, err error) {
	var e *accountingError
	if errors.As(err, &e) {
		fail(w, e.Status, e.Message, e.Field)
		return
	}
	fail(w, 503, "额度或模型观测暂不可用；原观测保留", "")
}

func (a *App) startCodexObservationLocked(src Source, kind string, state *quotaObservationState, now time.Time) (*Operation, error) {
	if !fixedCodexObservationSource(src) && !(kind == "models" && chatGPTDirectSource(src)) {
		if kind == "quota" && chatGPTDirectSource(src) {
			return nil, &accountingError{Status: 422, Field: "source", Message: "ChatGPT 独立授权没有公开额度 GET 合同；请查看 https://chatgpt.com/settings/usage，不能沿用 Codex 私有额度接口"}
		}
		return nil, &accountingError{Status: 422, Field: "source", Message: "此来源没有固定版本的独立模型/额度 GET 合同"}
	}
	if !src.Enabled || src.Deleted || !src.Configured || src.AuthStatus != "logged_in" {
		return nil, &accountingError{Status: 409, Message: "请先启用并登录此 Codex 来源"}
	}
	if a.stagedAdmission || a.stopping {
		return nil, &accountingError{Status: 503, Message: "服务尚未开放观测准入"}
	}
	if kind == "models" && !chatGPTDirectSource(src) && (a.Config.Codex.ClientVersion == "" || len(a.Config.Codex.ClientVersion) > 128 || strings.ContainsAny(a.Config.Codex.ClientVersion, "\r\n\x00")) {
		return nil, &accountingError{Status: 422, Field: "client_version", Message: "先配置实际声明的 Codex 客户端版本，不能猜测版本"}
	}
	if state.Flights == nil {
		state.Flights = map[string]*quotaObservationFlight{}
	}
	key := src.AccountID + ":" + kind
	if kind == "models" {
		key = src.ID + ":models"
	}
	if f := state.Flights[key]; f != nil {
		copy := f.Operation
		return &copy, nil
	}
	q := quotaSnapshot(src)
	if kind == "quota" && q.AccountGeneration == src.AccountGeneration && q.NextAttempt.After(now) {
		return nil, nil
	}
	if len(state.Flights) >= 8 {
		return nil, &accountingError{Status: 503, Message: "观测任务已满，请等待已有任务结束"}
	}
	release, err := a.beginBackupOwnerLocked()
	if err != nil {
		return nil, err
	}
	cfg := a.Config
	timeout := time.Duration(cfg.HeaderTimeout) * time.Second
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	op := Operation{ID: id("op"), Kind: "codex_" + kind + "_observation", ObjectID: src.ID, State: "running", Version: 1, CreatedAt: now, UpdatedAt: now}
	if _, err = a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES(?,?)", op.ID, encode(op)); err != nil {
		cancel()
		a.mu.Unlock()
		release()
		a.mu.Lock()
		return nil, storageError()
	}
	f := &quotaObservationFlight{Operation: op, Source: src, Kind: kind, Config: cfg}
	state.Flights[key] = f
	a.accountActive[src.AccountID]++
	a.operationCancels[op.ID] = cancel
	a.ownedTasks.Add(1)
	go func() {
		defer a.ownedTasks.Done()
		defer release()
		defer cancel()
		a.runCodexObservation(ctx, state, f)
		a.mu.Lock()
		delete(state.Flights, key)
		delete(a.operationCancels, op.ID)
		a.accountActive[src.AccountID]--
		a.signalAdmission()
		a.mu.Unlock()
	}()
	return &op, nil
}

func (a *App) codexObservationGET(ctx context.Context, state *quotaObservationState, f *quotaObservationFlight) ([]byte, http.Header, int, error) {
	src := f.Source
	a.mu.Lock()
	c, err := a.subscriptionCredential(ctx, &src)
	if err == nil {
		f.Source = src
	} // Only the shared refresh owner can rotate this lease's credential epoch.
	a.mu.Unlock()
	if err != nil {
		return nil, nil, 401, errors.New("订阅认证不可用")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return nil, nil, 0, ctx.Err()
		}
		address := "https://chatgpt.com/backend-api/wham/usage"
		cache := codexCatalogCache{}
		if f.Kind == "models" {
			address = "https://chatgpt.com/backend-api/codex/models?client_version=" + url.QueryEscape(f.Config.Codex.ClientVersion)
			if chatGPTDirectSource(src) {
				address = chatGPTResource + "/models"
			}
			b, _ := json.Marshal(f.Source.Quota["codex_model_catalog"])
			_ = json.Unmarshal(b, &cache)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
		if err != nil {
			return nil, nil, 0, errors.New("观测请求初始化失败")
		}
		req.Header.Set("Authorization", "Bearer "+c.Access)
		if !chatGPTDirectSource(src) {
			req.Header.Set("ChatGPT-Account-Id", c.Account)
		}
		req.Header.Set("Accept", "application/json")
		if cache.ETag != "" && cache.AccountGeneration == src.AccountGeneration && cache.SourceGeneration == src.Generation && cache.ClientVersion == f.Config.Codex.ClientVersion {
			req.Header.Set("If-None-Match", cache.ETag)
		}
		resp, err := a.doUpstream(req, src)
		if err != nil {
			return nil, nil, 0, errors.New("观测网络请求失败")
		}
		max := f.Config.MaxResponse
		if max > 4<<20 {
			max = 4 << 20
		}
		raw, readErr := readLimited(resp.Body, max)
		resp.Body.Close()
		if readErr != nil {
			return nil, resp.Header, resp.StatusCode, errors.New("观测响应超过限制或读取失败")
		}
		if resp.StatusCode == 401 && attempt == 0 {
			a.mu.Lock()
			current, e := a.Store.source(src.ID)
			if e != nil || !sameQuotaOwner(f.Source, current) {
				err = errors.New("来源或账号变化，旧观测已丢弃")
			} else if state.RefreshRejected == nil {
				err = errors.New("共享 401 刷新 owner 尚未接入")
			} else {
				src = current
				c, err = state.RefreshRejected(ctx, &src, c.Access)
				if err == nil {
					f.Source = src
				}
			}
			a.mu.Unlock()
			if err != nil {
				return nil, resp.Header, 401, errors.New("订阅认证刷新失败，需要重新登录")
			}
			continue
		}
		if resp.StatusCode == 200 {
			// Selected labels are retained, but credentials and verified upstream
			// identities cannot become names, model IDs, or cache header values.
			secrets := []string{c.Access, c.Refresh, c.IDToken, c.Account, c.Subject}
			raw = codexRedactObservation(raw, secrets)
			for _, secret := range secrets {
				if len(secret) > 3 && strings.Contains(resp.Header.Get("ETag"), secret) {
					resp.Header.Del("ETag")
				}
			}
		}
		return raw, resp.Header, resp.StatusCode, nil
	}
	return nil, nil, 401, errors.New("订阅认证失败")
}
func codexRedactObservation(raw []byte, secrets []string) []byte {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return raw
	}
	var visit func(any) any
	visit = func(v any) any {
		switch x := v.(type) {
		case string:
			for _, s := range secrets {
				if len(s) > 3 {
					x = strings.ReplaceAll(x, s, "[redacted]")
				}
			}
			return x
		case []any:
			for i, v := range x {
				x[i] = visit(v)
			}
		case map[string]any:
			for k, v := range x {
				x[k] = visit(v)
			}
		}
		return v
	}
	b, err := json.Marshal(visit(value))
	if err != nil {
		return raw
	}
	return b
}
func sameQuotaOwner(old, current Source) bool {
	return !current.Deleted && old.ID == current.ID && old.AccountID == current.AccountID && old.Generation == current.Generation && old.AccountGeneration == current.AccountGeneration && old.Version == current.Version && old.BaseURL == current.BaseURL && (fixedCodexObservationSource(current) || chatGPTDirectSource(current))
}
func codexObservationError(status int, err error) string {
	switch status {
	case 401:
		return "认证失败，需要重新登录"
	case 403:
		return "观测接口资格或权限不足"
	case 429:
		return "观测接口限速，稍后重试"
	default:
		return "观测接口返回未知或失败状态，旧数值保留"
	}
}

func (a *App) runCodexObservation(ctx context.Context, state *quotaObservationState, f *quotaObservationFlight) {
	raw, headers, status, err := a.codexObservationGET(ctx, state, f)
	now := time.Now().UTC()
	var q codexQuotaSnapshot
	var models []codexCatalogModel
	if err == nil && status == 200 {
		if f.Kind == "quota" {
			q, err = parseCodexQuota(raw, f.Source, now)
			if err == nil {
				if ttl, ok := quotaCacheTTL(headers.Get("Cache-Control")); ok {
					expires := now.Add(ttl)
					q.ExpiresAt = &expires
					q.NextAttempt = expires.Add(quotaJitter())
					for i := range q.Windows {
						q.Windows[i].ExpiresAt = expires
					}
					for i := range q.Limits {
						if q.Limits[i].Primary != nil {
							q.Limits[i].Primary.ExpiresAt = expires
						}
						if q.Limits[i].Secondary != nil {
							q.Limits[i].Secondary.ExpiresAt = expires
						}
					}
				}
			}
		} else {
			models, err = parseCodexCatalog(raw, f.Config.Codex.ClientVersion, f.Source)
		}
	} else if err == nil && !(f.Kind == "models" && status == 304) {
		err = errors.New("provider 观测未成功")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, readErr := a.Store.source(f.Source.ID)
	if readErr != nil || !sameQuotaOwner(f.Source, current) {
		err = errors.New("来源或账号变化，旧观测已丢弃")
		status = 409
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	op := f.Operation
	if status != 409 && readErr == nil && sameQuotaOwner(f.Source, current) {
		if f.Kind == "quota" {
			if err == nil && q.Status == "available" {
				present := map[string]bool{}
				for _, w := range q.Windows {
					present[w.Dimension] = true
				}
				previous := quotaSnapshot(current)
				if previous.Scope.Account == current.AccountID {
					for _, w := range previous.Windows {
						if !present[w.Dimension] {
							if len(q.Windows) >= 260 {
								break
							} // At most one current and one prior bounded set.
							w.Status = "unknown"
							w.Confidence = "unknown"
							w.UsedPercent = nil
							w.Remaining = nil
							w.Limit = nil
							w.Errors = []string{"本次观测未返回此前窗口，不能据此认定恢复"}
							q.Windows = append(q.Windows, w)
						}
					}
				}
			}
			if err == nil && q.Status == "unknown" {
				previous := quotaSnapshot(current)
				if previous.ObservedAt != nil {
					q.Windows = previous.Windows
					q.ObservedAt = previous.ObservedAt
					q.ExpiresAt = previous.ExpiresAt
				}
			}
			if err != nil {
				q = quotaSnapshot(current)
				q.Status = "stale"
				if q.ObservedAt == nil {
					q.Status = "unknown"
				}
				q.Failures++
				q.AttemptedAt = now
				q.LastError = codexObservationError(status, err)
				q.NextAttempt = now.Add(quotaBackoff(q.Failures) + quotaJitter())
				if status == 429 {
					if d := quotaRetryAfter(headers.Get("Retry-After"), now); d > 0 && now.Add(d).After(q.NextAttempt) {
						q.NextAttempt = now.Add(d)
					}
				}
			}
			if e := a.publishCodexQuotaLocked(ctx, current, q, status == 200 && err == nil, now); e != nil {
				err = e
			}
		} else if err == nil {
			if e := a.publishCodexCatalogLocked(current, models, headers.Get("ETag"), f.Config.Codex.ClientVersion, status == 304, now); e != nil {
				err = e
			}
		}
		if status == 401 && err != nil && ctx.Err() == nil {
			account, ref, e := a.Store.account(current.AccountID)
			if e == nil && account.Generation == current.AccountGeneration {
				account.AuthState = "needs_reauth"
				if _, e = a.Store.DB.Exec("UPDATE accounts SET data=? WHERE id=? AND generation=? AND credential_ref=?", encode(account), account.ID, account.Generation, ref); e != nil {
					err = e
				}
			}
		}
	}
	op.Version++
	op.UpdatedAt = now
	op.State = "succeeded"
	result := map[string]any{"call_verified": false, "account_generation": f.Source.AccountGeneration, "source_generation": f.Source.Generation, "observed_at": now, "not_modified": status == 304}
	if f.Kind == "models" {
		result["discovered_count"] = len(models)
	}
	op.Result = result
	if err != nil {
		op.State = "failed"
		op.Error = codexObservationError(status, err)
		if ctx.Err() != nil {
			op.State = "cancelled"
		}
	}
	if _, e := a.Store.DB.Exec("UPDATE operations SET data=? WHERE id=?", encode(op), op.ID); e != nil {
		a.markStorageFailure()
	}
}
func quotaRetryAfter(value string, now time.Time) time.Duration {
	if n, e := strconv.ParseInt(strings.TrimSpace(value), 10, 64); e == nil && n >= 0 {
		if n > 900 {
			n = 900
		}
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(value); e == nil && t.After(now) {
		d := t.Sub(now)
		if d > 15*time.Minute {
			d = 15 * time.Minute
		}
		return d
	}
	return 0
}
func quotaCacheTTL(header string) (time.Duration, bool) {
	var ttl time.Duration
	found := false
	for _, part := range strings.Split(header, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 || !strings.EqualFold(pair[0], "max-age") {
			continue
		}
		n, err := strconv.ParseInt(strings.Trim(pair[1], "\""), 10, 64)
		if found || err != nil || n < 0 || n > math.MaxInt64/int64(time.Second) {
			return 0, false
		}
		ttl = time.Duration(n) * time.Second
		found = true
	}
	return ttl, found
}

type codexWireWindow struct {
	Used    json.RawMessage `json:"used_percent"`
	Seconds json.RawMessage `json:"limit_window_seconds"`
	Reset   json.RawMessage `json:"reset_at"`
	After   json.RawMessage `json:"reset_after_seconds"`
}
type codexWireLimit struct {
	Allowed   *bool            `json:"allowed"`
	Reached   *bool            `json:"limit_reached"`
	Primary   *codexWireWindow `json:"primary_window"`
	Secondary *codexWireWindow `json:"secondary_window"`
}

func parseCodexQuota(raw []byte, src Source, now time.Time) (codexQuotaSnapshot, error) {
	q := codexQuotaSnapshot{Status: "available", Provider: "codex", Scope: codexQuotaScope{Account: src.AccountID}, SourceGeneration: src.Generation, AccountGeneration: src.AccountGeneration, ObservedAt: &now, AttemptedAt: now, Confidence: "provider_reported", AdapterVersion: codexObservationAdapter, Windows: []codexQuotaWindow{}, Limits: []codexQuotaLimit{}}
	expires := now.Add(5 * time.Minute)
	q.ExpiresAt = &expires
	q.NextAttempt = expires.Add(quotaJitter())
	var data struct {
		Rate       *codexWireLimit `json:"rate_limit"`
		Additional []struct {
			Name    string          `json:"limit_name"`
			Feature string          `json:"metered_feature"`
			Rate    *codexWireLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
		Credits *struct {
			Balance   *string `json:"balance"`
			Has       *bool   `json:"has_credits"`
			Unlimited *bool   `json:"unlimited"`
		} `json:"credits"`
	}
	if json.Unmarshal(raw, &data) != nil || len(data.Additional) > 64 {
		return q, errors.New("额度 JSON 或限制数量无效")
	}
	add := func(name, feature string, rate *codexWireLimit, accountWide bool) {
		limit := codexQuotaLimit{Name: name, MeteredFeature: feature}
		if rate != nil {
			limit.Allowed = rate.Allowed
			limit.LimitReached = rate.Reached
			limit.Primary = codexWindow(rate.Primary, src, name, feature, "primary", accountWide, now, expires)
			limit.Secondary = codexWindow(rate.Secondary, src, name, feature, "secondary", accountWide, now, expires)
		}
		for _, w := range []*codexQuotaWindow{limit.Primary, limit.Secondary} {
			if w != nil {
				q.Windows = append(q.Windows, *w)
			}
		}
		q.Limits = append(q.Limits, limit)
	}
	add("subscription", "", data.Rate, true)
	seen := map[string]bool{}
	for _, extra := range data.Additional {
		if !quotaLabel(extra.Name, 200) || !quotaLabel(extra.Feature, 200) {
			return q, errors.New("附加限制标识无效")
		}
		key := extra.Name + "\x00" + extra.Feature
		if seen[key] {
			return q, errors.New("重复附加额度限制标识")
		}
		seen[key] = true
		add(extra.Name, extra.Feature, extra.Rate, false)
	}
	if len(q.Windows) == 0 {
		q.Status = "unknown"
		q.Confidence = "unknown"
		q.LastError = "未返回可识别额度窗口，缺失字段保持未知"
	}
	if data.Credits != nil {
		q.Credits = &codexQuotaCredits{Dimension: "provider_credit", Unit: "provider_credit", HasCredits: data.Credits.Has, Unlimited: data.Credits.Unlimited}
		if data.Credits.Balance != nil && quotaDecimal(*data.Credits.Balance) {
			q.Credits.Balance = data.Credits.Balance
		}
	}
	return q, nil
}
func quotaLabel(s string, max int) bool {
	return s != "" && len(s) <= max && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00")
}
func quotaDecimal(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	dots := 0
	digits := 0
	for _, r := range s {
		if r == '.' {
			dots++
			if dots > 1 {
				return false
			}
		} else if r >= '0' && r <= '9' {
			digits++
		} else {
			return false
		}
	}
	v, ok := new(big.Rat).SetString(s)
	return digits > 0 && ok && v.Sign() >= 0
}
func codexWindow(raw *codexWireWindow, src Source, name, feature, which string, accountWide bool, now, expires time.Time) *codexQuotaWindow {
	if raw == nil {
		return nil
	}
	dimension := "subscription:" + which
	if !accountWide {
		dimension = "pool:" + digest(name+"\x00"+feature) + ":" + which
	}
	w := &codexQuotaWindow{Dimension: dimension, Unit: "percent", Scope: codexQuotaScope{Account: src.AccountID}, Name: name, MeteredFeature: feature, Status: "unknown", ObservedAt: now, ExpiresAt: expires, Confidence: "unknown", AccountWide: accountWide}
	var used float64
	if len(raw.Used) > 0 && string(raw.Used) != "null" && json.Unmarshal(raw.Used, &used) == nil && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 0 && used <= 100 {
		remaining, limit := 100-used, 100.0
		w.UsedPercent = &used
		w.Remaining = &remaining
		w.Limit = &limit
		w.Status = "available"
		w.Confidence = "provider_reported"
	} else {
		w.Errors = append(w.Errors, "使用百分比缺失或无效")
	}
	var seconds int64
	if len(raw.Seconds) > 0 && string(raw.Seconds) != "null" {
		if json.Unmarshal(raw.Seconds, &seconds) == nil && seconds > 0 && seconds <= math.MaxInt64/int64(time.Second) {
			w.LimitSeconds = &seconds
		} else {
			w.Errors = append(w.Errors, "窗口秒数无效")
		}
	}
	var epoch int64
	if len(raw.Reset) > 0 && string(raw.Reset) != "null" {
		if json.Unmarshal(raw.Reset, &epoch) == nil && epoch > 0 && epoch <= 253402300799 {
			reset := time.Unix(epoch, 0).UTC()
			w.ResetAt = &reset
		} else {
			w.Errors = append(w.Errors, "重置 epoch 秒无效")
		}
	} else if len(raw.After) > 0 && string(raw.After) != "null" {
		var after int64
		if json.Unmarshal(raw.After, &after) == nil && after >= 0 && after <= math.MaxInt64/int64(time.Second) {
			reset := now.Add(time.Duration(after) * time.Second)
			if reset.Year() < 10000 {
				w.ResetAt = &reset
			}
		} else {
			w.Errors = append(w.Errors, "相对重置秒数无效")
		}
	}
	if w.ResetAt != nil && w.LimitSeconds != nil {
		start := w.ResetAt.Add(-time.Duration(*w.LimitSeconds) * time.Second)
		if start.Year() > 0 {
			w.WindowStart = &start
		}
	}
	return w
}

func (a *App) publishCodexQuotaLocked(ctx context.Context, src Source, q codexQuotaSnapshot, success bool, now time.Time) error {
	sources, err := a.Store.sources()
	if err != nil {
		return err
	}
	tx, err := a.Store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, current := range sources {
		if current.AccountID != src.AccountID || current.AccountGeneration != src.AccountGeneration || !fixedCodexObservationSource(current) {
			continue
		}
		copy := q
		copy.SourceGeneration = current.Generation
		copy.AccountGeneration = current.AccountGeneration
		copy.Scope.Account = current.AccountID
		if current.Quota == nil {
			current.Quota = map[string]any{}
		}
		delete(current.Quota, "last_error")
		b, _ := json.Marshal(copy)
		var normalized map[string]any
		if json.Unmarshal(b, &normalized) != nil {
			return storageError()
		}
		for k, v := range normalized {
			current.Quota[k] = v
		}
		if _, err = tx.Exec("UPDATE sources SET data=? WHERE id=? AND account_id=?", encode(current), current.ID, src.AccountID); err != nil {
			return err
		}
	}
	if success {
		var expiry any
		if q.ExpiresAt != nil {
			expiry = q.ExpiresAt.UTC().Format(quotaSQLTime)
		}
		if _, err = tx.Exec("INSERT INTO quota_snapshots(account_id,dimension,observed_at,expires_at,account_generation,status,value_json) VALUES(?,?,?,?,?,?,?)", src.AccountID, "codex.windows", now.UTC().Format(quotaSQLTime), expiry, src.AccountGeneration, q.Status, encode(q)); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM quota_snapshots WHERE account_id=? AND (observed_at<? OR observed_at NOT IN (SELECT observed_at FROM quota_snapshots WHERE account_id=? ORDER BY observed_at DESC LIMIT 100))", src.AccountID, now.AddDate(0, 0, -30).UTC().Format(quotaSQLTime), src.AccountID); err != nil {
			return err
		}
	}
	if ctx.Err() != nil && success {
		return ctx.Err()
	}
	return tx.Commit()
}

func parseCodexCatalog(raw []byte, version string, src Source) ([]codexCatalogModel, error) {
	var data struct {
		Models []struct {
			Slug       string   `json:"slug"`
			Visibility string   `json:"visibility"`
			Name       string   `json:"display_name"`
			Context    *int64   `json:"context_window"`
			MaxContext *int64   `json:"max_context_window"`
			Output     *int64   `json:"max_output_tokens"`
			Modalities []string `json:"input_modalities"`
			Reasoning  []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Supported *bool `json:"supported_in_api"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Models == nil || len(data.Models) > 2000 {
		return nil, errors.New("Codex models 目录无效")
	}
	items := []codexCatalogModel{}
	seen := map[string]bool{}
	for _, m := range data.Models {
		if chatGPTDirectSource(src) && m.Visibility != "list" {
			continue
		}
		if !quotaLabel(m.Slug, 200) || strings.Contains(m.Slug, "[redacted]") || seen[m.Slug] || len(m.Name) > 512 || strings.ContainsAny(m.Name, "\r\n\x00") {
			return nil, errors.New("Codex 目录模型 ID 无效或重复")
		}
		seen[m.Slug] = true
		for _, limit := range []*int64{m.Context, m.MaxContext, m.Output} {
			if limit != nil && *limit <= 0 {
				return nil, errors.New("模型限制值无效")
			}
		}
		if len(m.Modalities) > 32 || len(m.Reasoning) > 64 {
			return nil, errors.New("模型声明数量无效")
		}
		for _, modality := range m.Modalities {
			if !quotaLabel(modality, 64) {
				return nil, errors.New("模型 modality 无效")
			}
		}
		reasoning := []string{}
		for _, level := range m.Reasoning {
			if !quotaLabel(level.Effort, 64) {
				return nil, errors.New("模型 reasoning level 无效")
			}
			reasoning = append(reasoning, level.Effort)
		}
		items = append(items, codexCatalogModel{ID: m.Slug, Name: m.Name, Context: m.Context, Output: m.Output, Metadata: codexCatalogMetadata{MaxContextWindow: m.MaxContext, InputModalities: m.Modalities, ReasoningLevels: reasoning, SupportedInAPI: m.Supported, AdapterVersion: codexObservationAdapter, ClientVersion: version, SourceGeneration: src.Generation, AccountGeneration: src.AccountGeneration}})
		if chatGPTDirectSource(src) {
			items[len(items)-1].Metadata.AdapterVersion = "chatgpt-direct"
		}
	}
	return items, nil
}
func (a *App) publishCodexCatalogLocked(src Source, items []codexCatalogModel, etag, version string, notModified bool, now time.Time) error {
	var cache codexCatalogCache
	if b, e := json.Marshal(src.Quota["codex_model_catalog"]); e == nil {
		_ = json.Unmarshal(b, &cache)
	}
	if notModified && (cache.ETag == "" || cache.SourceGeneration != src.Generation || cache.AccountGeneration != src.AccountGeneration || cache.ClientVersion != version) {
		return errors.New("304 没有同归属目录缓存")
	}
	if etag != "" && !quotaLabel(etag, 1024) {
		return errors.New("ETag 无效")
	}
	if etag == "" && notModified {
		etag = cache.ETag
	}
	tx, err := a.Store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !notModified {
		models, err := modelsInTx(tx, src.ID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for order, item := range items {
			seen[item.ID] = true
			m := models[item.ID]
			if m.ID == "" {
				m = SourceModel{ID: id("model"), SourceID: src.ID, UpstreamModel: item.ID, DisplayName: item.ID, Version: 1, Discovery: "discovered", Verification: "unverified", CreatedAt: now}
			} else {
				m.Version++
			}
			m.DiscoveredAt = &now
			if chatGPTDirectSource(src) {
				m.CatalogOrder = &order
			}
			if m.MetadataReason == "" || m.MetadataReason == "provider discovery" || m.MetadataReason == "codex provider discovery" {
				m.ContextLimit = item.Context
				m.MaxOutput = item.Output
				m.Modalities = item.Metadata.InputModalities
				m.MetadataReason = "codex provider discovery"
				if item.Name != "" {
					m.DisplayName = item.Name
				}
			}
			if m.Discovery != "manual" {
				m.Discovery = "discovered"
			}
			b, _ := json.Marshal(m)
			var obj map[string]any
			if json.Unmarshal(b, &obj) != nil {
				return storageError()
			}
			obj["codex_catalog"] = item.Metadata
			if _, err = tx.Exec("INSERT INTO source_models(id,source_id,upstream_model,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", m.ID, src.ID, item.ID, encode(obj)); err != nil {
				return err
			}
			delete(models, item.ID)
		}
		for _, m := range models {
			if m.Discovery == "discovered" {
				m.Discovery = "stale"
				if _, err = tx.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID); err != nil {
					return err
				}
			}
		}
	}
	if src.Quota == nil {
		src.Quota = map[string]any{}
	}
	src.Quota["codex_model_catalog"] = codexCatalogCache{ETag: etag, SourceGeneration: src.Generation, AccountGeneration: src.AccountGeneration, ClientVersion: version, ObservedAt: now}
	if _, err = tx.Exec("UPDATE sources SET data=? WHERE id=? AND account_id=?", encode(src), src.ID, src.AccountID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) codexQuotaHistoryAPI(w http.ResponseWriter, r *http.Request, src Source) {
	if r.Method != "GET" {
		fail(w, 405, "方法不支持", "")
		return
	}
	rows, err := a.Store.DB.QueryContext(r.Context(), "SELECT value_json FROM quota_snapshots WHERE account_id=? AND observed_at>=? ORDER BY observed_at DESC LIMIT 100", src.AccountID, time.Now().UTC().AddDate(0, 0, -30).Format(quotaSQLTime))
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer rows.Close()
	items := []codexQuotaSnapshot{}
	for rows.Next() {
		var raw string
		var q codexQuotaSnapshot
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &q) != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if q.AccountGeneration != src.AccountGeneration {
			q.Status = "stale"
		}
		items = append(items, q)
	}
	if rows.Err() != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, map[string]any{"data": items, "account_generation": src.AccountGeneration})
}

// Root calls this before direct and route admission. Additional named pools
// retain their identity but cannot block all models without a proven scope.
func quotaDispatchBlocked(src Source, model, operation string, now time.Time) (bool, string) {
	return quotaBelowThreshold(src, model, operation, now, 0)
}

// Unknown, expired or different-generation observations never imply exhaustion.
func quotaBelowThreshold(src Source, model, operation string, now time.Time, threshold int) (bool, string) {
	q := quotaSnapshot(src)
	if q.Status != "available" || q.Scope.Account != src.AccountID || q.ObservedAt == nil || q.ExpiresAt == nil || !q.ExpiresAt.After(now) || q.ObservedAt.After(now.Add(time.Minute)) || q.AccountGeneration != src.AccountGeneration || q.SourceGeneration != src.Generation {
		return false, ""
	}
	for _, w := range q.Windows {
		if !w.AccountWide || w.Scope.Account != src.AccountID || w.Status != "available" || w.UsedPercent == nil || *w.UsedPercent < float64(100-threshold) || w.ResetAt == nil || !w.ResetAt.After(now) || !w.ExpiresAt.After(now) {
			continue
		}
		if w.Scope.Model != "" && w.Scope.Model != model || w.Scope.Operation != "" && w.Scope.Operation != operation {
			continue
		}
		if *w.UsedPercent == 100 {
			return true, "已观测订阅窗口耗尽；等待新观测或额度状态过期"
		}
		return true, fmt.Sprintf("已观测订阅窗口剩余额度不高于 %d%% 调度阈值", threshold)
	}
	return false, ""
}
func (a *App) quotaObservationTick(state *quotaObservationState, now time.Time) error {
	a.mu.Lock()
	var release func()
	defer func() {
		a.mu.Unlock()
		if release != nil {
			release()
		}
	}()
	if a.stopping || a.stagedAdmission || a.backupQuiescing {
		return nil
	}
	var err error
	release, err = a.beginBackupOwnerLocked()
	if err != nil {
		return nil
	}
	if _, err = a.Store.DB.Exec("DELETE FROM quota_snapshots WHERE observed_at<?", now.AddDate(0, 0, -30).UTC().Format(quotaSQLTime)); err != nil {
		return err
	}
	sources, err := a.Store.sources()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, src := range sources {
		if seen[src.AccountID] || !fixedCodexObservationSource(src) || src.Deleted || !src.Enabled || !src.Configured || src.AuthStatus != "logged_in" {
			continue
		}
		seen[src.AccountID] = true
		if _, err = a.startCodexObservationLocked(src, "quota", state, now); err != nil {
			var e *accountingError
			if errors.As(err, &e) && e.Status == 503 {
				break
			}
			return err
		}
	}
	return nil
}
func (a *App) quotaObservationLoop(ctx context.Context, state *quotaObservationState) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := a.quotaObservationTick(state, now.UTC()); err != nil {
				a.mu.Lock()
				a.maintenanceError = "额度观测维护失败，旧数值保留"
				a.mu.Unlock()
			}
		}
	}
}
