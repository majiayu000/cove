package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// An adapter is an explicit native transport capability, never inferred from
// an HTTP text test or a provider name. The two subscription/public cards differ.
type WebSocketModelCapabilities struct {
	WebSocketAdapter  string `json:"websocket_adapter"`
	WebSocketWarmup   bool   `json:"websocket_warmup"`
	WebSocketSteering bool   `json:"websocket_steering"`
}

const (
	wsPublicCard         = "openai_responses_websocket_v1"
	wsCodexCard          = "codex_responses_websocket_0_158_0"
	wsRealtimeCard       = "openai_realtime_websocket_v1"
	wsMessageLimit int64 = 8 << 20
	wsMediaLimit   int64 = 2 << 20
	wsBufferLimit  int64 = 4 << 20
)

type wsFailure struct {
	Status         int
	Field, Message string
}

func (e *wsFailure) Error() string                    { return e.Message }
func wsError(status int, field, message string) error { return &wsFailure{status, field, message} }
func wsErrorDetails(err error) (int, string, string) {
	var e *wsFailure
	if errors.As(err, &e) {
		return e.Status, e.Field, e.Message
	}
	var b *accountingError
	if errors.As(err, &b) {
		return b.Status, b.Field, b.Message
	}
	var selection *selectionError
	if errors.As(err, &selection) {
		return selection.Status, "model", selection.Message
	}
	return 503, "", "本机存储或上游连接不可用"
}
func wsOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		return true
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Host != r.Host {
		return false
	}
	host := u.Hostname()
	if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
		return false
	}
	return u.Scheme == "http" && r.TLS == nil || u.Scheme == "https" && r.TLS != nil
}
func wsAuth(r *http.Request) (string, error) {
	if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || bearer(r) == "" {
		return "", wsError(401, "Authorization", "WebSocket 需要唯一 Bearer Cove Key")
	}
	if r.Header.Get("X-Goog-Api-Key") != "" || r.Header.Get("X-Api-Key") != "" {
		return "", wsError(401, "Authorization", "此 WebSocket 卡只接受 Bearer Cove Key")
	}
	return digest(bearer(r)), nil
}
func wsProtocol(realtime bool) string {
	if realtime {
		return "realtime_websocket"
	}
	return "responses"
}
func wsCapability(src Source, cap WebSocketModelCapabilities, realtime bool) error {
	if cap.WebSocketSteering {
		return wsError(422, "websocket_steering", "当前卡尚未实现 mid-turn steering；不能伪装成第二个POST")
	}
	operation := "responses_websocket"
	if realtime {
		operation = "realtime_websocket"
	}
	if !slices.Contains(src.NativeOperations, operation) {
		return wsError(422, "native_operations", "来源没有明确启用此原生 WebSocket operation")
	}
	if realtime {
		if cap.WebSocketAdapter != wsRealtimeCard || src.Kind != "api_key" || src.Provider != "openai" || src.NativeProtocol != "realtime_websocket" || cap.WebSocketWarmup {
			return wsError(422, "websocket_adapter", "Realtime 只支持明确的公开原生 WebSocket 卡")
		}
	} else {
		switch cap.WebSocketAdapter {
		case wsPublicCard:
			if src.Kind != "api_key" || src.Provider != "openai" || src.NativeProtocol != "responses" {
				return wsError(422, "websocket_adapter", "公开 Responses WS 卡与来源身份/协议不符")
			}
		case wsCodexCard:
			if src.Kind != "codex_subscription" || src.Provider != "codex" || src.NativeProtocol != "responses" || cap.WebSocketWarmup || chatGPTDirectSource(src) {
				return wsError(422, "websocket_adapter", "Codex 0.158.0 WS 卡与来源身份/协议不符")
			}
		default:
			return wsError(422, "websocket_adapter", "模型未启用已实现的原生 WebSocket 卡")
		}
	}
	return nil
}
func wsLaneID(body map[string]json.RawMessage) (string, error) {
	raw, ok := body["stream_id"]
	if !ok {
		return "", nil
	}
	var lane string
	if json.Unmarshal(raw, &lane) != nil || len(lane) < 1 || len(lane) > 256 {
		return "", wsError(400, "stream_id", "stream_id 需要1到256个明确字符")
	}
	for _, c := range lane {
		if c > 127 || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return "", wsError(400, "stream_id", "stream_id 字符无效")
		}
	}
	return lane, nil
}
func wsJSON(raw []byte) (map[string]json.RawMessage, string, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, "", wsError(400, "event", "需要完整JSON对象")
	}
	var kind string
	if json.Unmarshal(body["type"], &kind) != nil || kind == "" {
		return nil, "", wsError(400, "type", "事件缺少type")
	}
	return body, kind, nil
}
func wsLocalError(realtime bool, lane string, err error) []byte {
	status, field, message := wsErrorDetails(err)
	errorBody := map[string]any{"type": "invalid_request_error", "code": "cove_admission", "message": message, "param": field}
	value := map[string]any{"type": "error", "event_id": id("event"), "error": errorBody, "status": status}
	if !realtime {
		if lane != "" {
			value["stream_id"] = lane
		}
	}
	return []byte(encode(value))
}

type wsFrame struct {
	DeliveryID string
	Final      bool
	Kind       int
	Data       []byte
	Upstream   bool
	Err        error
}
type wsWriteQueue struct {
	mu      sync.Mutex
	bytes   int64
	frames  chan wsFrame
	changed chan struct{}
}

func newWSWriteQueue() *wsWriteQueue {
	return &wsWriteQueue{frames: make(chan wsFrame, 128), changed: make(chan struct{})}
}
func (q *wsWriteQueue) Put(frame wsFrame) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+int64(len(frame.Data)) > wsBufferLimit {
		return wsError(503, "buffer", "WebSocket 待发送缓冲达到4MiB")
	}
	select {
	case q.frames <- frame:
		q.bytes += int64(len(frame.Data))
		return nil
	default:
		return wsError(503, "buffer", "WebSocket 待发送事件达到上限")
	}
}
func (q *wsWriteQueue) Done(frame wsFrame) {
	q.mu.Lock()
	q.bytes -= int64(len(frame.Data))
	close(q.changed)
	q.changed = make(chan struct{})
	q.mu.Unlock()
}
func (q *wsWriteQueue) Wait(ctx context.Context) error {
	for {
		q.mu.Lock()
		if q.bytes == 0 {
			q.mu.Unlock()
			return nil
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func wsReader(ctx context.Context, conn *websocket.Conn, upstream bool, input *wsWriteQueue, wg *sync.WaitGroup) {
	events := input.frames
	defer wg.Done()
	conn.SetReadLimit(wsMessageLimit)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case events <- wsFrame{Upstream: upstream, Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if e := input.Put(wsFrame{Kind: kind, Data: data, Upstream: upstream}); e != nil {
			select {
			case events <- wsFrame{Upstream: upstream, Err: e}:
			case <-ctx.Done():
			}
			return
		}
	}
}
func wsWriter(ctx context.Context, conn *websocket.Conn, queue *wsWriteQueue, upstream bool, events chan<- wsFrame, wg *sync.WaitGroup) {
	defer wg.Done()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-queue.frames:
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			err := conn.WriteMessage(frame.Kind, frame.Data)
			if !upstream && frame.DeliveryID != "" {
				select {
				case events <- wsFrame{DeliveryID: frame.DeliveryID, Final: frame.Final, Err: err}:
				case <-ctx.Done():
					queue.Done(frame)
					return
				}
			}
			queue.Done(frame)
			if err != nil {
				select {
				case events <- wsFrame{Upstream: upstream, Err: err}:
				case <-ctx.Done():
				}
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(30*time.Second)); err != nil {
				select {
				case events <- wsFrame{Upstream: upstream, Err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}
}

type wsCreate struct {
	Body map[string]json.RawMessage
	Raw  []byte
	Lane string
	At   time.Time
}
type wsTurn struct {
	Record          Record
	Key             ClientKey
	Source          Source
	Body            map[string]json.RawMessage
	Wire            []byte
	ResponseID      string
	LastProgress    time.Time
	Acquired        bool
	EventID         string
	CancelRequested bool
}
type wsLane struct {
	Active *wsTurn
	Queue  []*wsCreate
}
type wsSession struct {
	CancelMu               sync.Mutex
	CancelPending          map[string]bool
	CancelWake             chan struct{}
	Completed              map[string]*wsTurn
	Items                  map[string]bool
	Calls                  map[string]bool
	Expires                time.Time
	GracefulFlush          bool
	App                    *App
	Cfg                    Config
	KeyDigest              string
	InitialKey             ClientKey
	Realtime               bool
	ID                     string
	Source                 *Source
	PublicModel, SentModel string
	ProviderModel          string
	Cap                    WebSocketModelCapabilities
	SessionRecord          *Record
	SessionPlan            *AccountingPlan
	SessionHeld            bool
	ResponseUsageObserved  bool
	SessionUsage           json.RawMessage
	Lanes                  map[string]*wsLane
	LaneOrder              []string
	Named                  int
	Pending                int
	Down, Up               *websocket.Conn
	DownQ, UpQ             *wsWriteQueue
	ReadQ                  *wsWriteQueue
	Events                 chan wsFrame
	Context                context.Context
	Cancel                 context.CancelFunc
	Workers                sync.WaitGroup
	CloseCode              int
	CloseReason            string
}

// This handler stays on App.ServeHTTP's owned/backup lease until all native
// socket workers and final SQL have finished. It must not run detached from it.
func (a *App) webSocketAPI(w http.ResponseWriter, r *http.Request) bool {
	realtime := r.URL.Path == "/v1/realtime"
	if !realtime && (r.URL.Path != "/v1/responses" || !websocket.IsWebSocketUpgrade(r)) {
		return false
	}
	if r.Method != "GET" || !websocket.IsWebSocketUpgrade(r) {
		fail(w, 405, "接口需要原生 WebSocket Upgrade", "")
		return true
	}
	if !wsOrigin(r) {
		fail(w, 403, "WebSocket Origin 不允许", "Origin")
		return true
	}
	for name, values := range r.URL.Query() {
		if !realtime || name != "model" || len(values) != 1 {
			fail(w, 400, "WebSocket query只允许Realtime的单一model；禁止query secret", "query")
			return true
		}
	}
	keyDigest, err := wsAuth(r)
	if err != nil {
		status, field, message := wsErrorDetails(err)
		fail(w, status, message, field)
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Minute)
	s := &wsSession{App: a, KeyDigest: keyDigest, Realtime: realtime, ID: id("ws"), Lanes: map[string]*wsLane{}, DownQ: newWSWriteQueue(), UpQ: newWSWriteQueue(), Events: make(chan wsFrame, 64), Context: ctx, Cancel: cancel, CloseCode: websocket.CloseNormalClosure}
	s.ReadQ = newWSWriteQueue()
	s.Events = s.ReadQ.frames
	s.Completed = map[string]*wsTurn{}
	s.Items = map[string]bool{}
	s.Calls = map[string]bool{}
	s.CancelPending = map[string]bool{}
	s.CancelWake = make(chan struct{}, 1)
	a.mu.Lock()
	s.Cfg = a.Config
	key, keyErr := a.Store.keyByDigest(keyDigest)
	if a.stopping || a.backupQuiescing || a.stagedAdmission {
		err = wsError(503, "", "服务正在排空、备份或尚未激活")
	} else if a.storageFailed.Load() {
		err = wsError(503, "", storageError().Error())
	} else if keyErr != nil || !keyValid(key, time.Now()) {
		err = wsError(401, "Authorization", "Cove Key无效或已到期")
	} else if !allowed(key.ProtocolAllowlist, wsProtocol(realtime)) || realtime && !allowed(key.OperationAllowlist, "realtime") || !realtime && !allowed(key.OperationAllowlist, "generate") && !allowed(key.OperationAllowlist, "warmup") {
		err = wsError(403, "protocol", "Key没有此WebSocket协议/operation权限")
	} else if a.webSocketActive >= a.Config.MaxConcurrent*4 {
		err = wsError(429, "concurrency", "本机WebSocket连接配额已满")
	}
	s.InitialKey = key
	if err == nil && realtime {
		s.PublicModel = r.URL.Query().Get("model")
		if s.PublicModel == "" {
			err = wsError(400, "model", "Realtime query model必填")
		} else {
			err = s.startRealtimeLocked()
		}
	}
	if err == nil {
		a.webSocketActive++
		if a.operationCancels == nil {
			a.operationCancels = map[string]context.CancelFunc{}
		}
		a.operationCancels[s.ID] = cancel
		a.ownedTasks.Add(1)
	}
	a.mu.Unlock()
	if err != nil {
		cancel()
		status, field, message := wsErrorDetails(err)
		fail(w, status, message, field)
		return true
	}
	defer func() {
		if s.GracefulFlush && ctx.Err() == nil {
			flushCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			_ = s.DownQ.Wait(flushCtx)
			stop()
		}
		if s.Down != nil && ctx.Err() == nil {
			_ = s.Down.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(s.CloseCode, s.CloseReason), time.Now().Add(time.Second))
		}
		cancel()
		if s.Down != nil {
			_ = s.Down.Close()
		}
		if s.Up != nil {
			_ = s.Up.Close()
		}
		s.Workers.Wait()
		s.finishAll("interrupted", "socket_closed", "连接已关闭；未重放任何未确认请求")
		a.mu.Lock()
		delete(a.operationCancels, s.ID)
		a.webSocketActive--
		a.signalAdmission()
		a.mu.Unlock()
		a.ownedTasks.Done()
	}()
	if realtime {
		if err = s.connect(nil, r); err != nil {
			s.CloseReason = "upstream_handshake"
			status, field, message := wsErrorDetails(err)
			fail(w, status, message, field)
			return true
		}
	}
	upgrader := websocket.Upgrader{CheckOrigin: wsOrigin, HandshakeTimeout: time.Duration(s.Cfg.HeaderTimeout) * time.Second, EnableCompression: false}
	s.Down, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.CloseReason = "downstream_upgrade"
		return true
	}
	s.startWorkers(s.Down, false, s.DownQ)
	if s.Up != nil {
		s.startWorkers(s.Up, true, s.UpQ)
	}
	s.loop(r)
	return true
}
func (s *wsSession) startWorkers(conn *websocket.Conn, upstream bool, queue *wsWriteQueue) {
	s.Workers.Add(2)
	go wsReader(s.Context, conn, upstream, s.ReadQ, &s.Workers)
	go wsWriter(s.Context, conn, queue, upstream, s.Events, &s.Workers)
}
func (s *wsSession) keyLocked() (ClientKey, error) {
	a := s.App
	if a.stopping || a.backupQuiescing || a.stagedAdmission {
		return ClientKey{}, wsError(503, "", "服务排空或尚未激活")
	}
	if a.storageFailed.Load() {
		return ClientKey{}, wsError(503, "", storageError().Error())
	}
	key, err := a.Store.keyByDigest(s.KeyDigest)
	if err != nil || !keyValid(key, time.Now()) {
		return key, wsError(401, "Authorization", "Key已撤销、停用或到期")
	}
	if key.ID != s.InitialKey.ID || key.SourceID != s.InitialKey.SourceID || key.RouteID != s.InitialKey.RouteID {
		return key, wsError(409, "target", "Key目标已变化；需要新连接")
	}
	if !allowed(key.ProtocolAllowlist, wsProtocol(s.Realtime)) {
		return key, wsError(403, "protocol", "Key协议权限已变化")
	}
	return key, nil
}
func (s *wsSession) selectLocked(key ClientKey, public, previous, operation string) (Source, string, WebSocketModelCapabilities, error) {
	var cap WebSocketModelCapabilities
	if !allowed(key.ModelAllowlist, public) {
		return Source{}, "", cap, wsError(403, "model", "Key不允许此模型")
	}
	plan := RoutingPolicyInput{IncludeBusySources: true}

	if previous != "" {
		var boundSource, boundModel string
		err := s.App.Store.DB.QueryRow("SELECT source_id,model FROM bindings WHERE response_id=? AND key_id=?", previous, key.ID).Scan(&boundSource, &boundModel)
		if err != nil || s.Source != nil && boundSource != s.Source.ID {
			return Source{}, "", cap, wsError(409, "previous_response_id", "前序响应不属于此Key/固定账号；请发送完整历史")
		}
		plan.AuthoritativeBinding, plan.BoundSourceID, plan.BoundModel = true, boundSource, boundModel
	}
	// Selection sees the same fixed provider/account on later turns. It cannot
	// switch sockets after dispatch. Public aliases are the only model rewrite.
	protocol := wsProtocol(s.Realtime)
	excluded := map[string]bool{}
	for {
		src, sent, _, err := s.App.selectSourceExcluding(key, public, protocol, false, excluded, plan)
		if err != nil {
			return src, sent, cap, wsError(422, "model", "没有符合原生WebSocket卡的来源/模型")
		}
		if sent == "" {
			sent = public
		}
		if s.Source != nil && src.ID != s.Source.ID && key.RouteID != "" {
			excluded[src.ID] = true
			continue
		}
		if s.Source != nil && (src.ID != s.Source.ID || src.AccountID != s.Source.AccountID || src.Generation != s.Source.Generation || src.AccountGeneration != s.Source.AccountGeneration || src.BaseURL != s.Source.BaseURL || src.Provider != s.Source.Provider || src.NativeProtocol != s.Source.NativeProtocol) {
			return src, sent, cap, wsError(409, "target", "连接账号、来源代次或原生endpoint已变化")
		}
		if src.Deleted || !src.Enabled || !src.Configured || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.AuthStatus == "rejected" {
			return src, sent, cap, wsError(503, "target", "固定来源不可用或需要重新授权")
		}
		candidate, err := s.App.candidateFor(src, sent)
		if err != nil || !candidate.Model.Enabled {
			return src, sent, cap, wsError(422, "model", "模型能力卡不存在或已停用")
		}
		src = candidate.Source
		var raw string
		if err = s.App.Store.DB.QueryRow("SELECT data FROM source_models WHERE id=?", candidate.Model.ID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &cap) != nil {
			return src, sent, cap, storageError()
		}
		if err = wsCapability(src, cap, s.Realtime); err != nil {
			if s.Source != nil || key.RouteID == "" {
				return src, sent, cap, err
			}
			excluded[src.ID] = true
			continue
		}
		if src.Kind == "codex_subscription" && src.Provider == "codex" {
			if blocked, reason := quotaDispatchBlocked(src, sent, operation, time.Now()); blocked {
				return src, sent, cap, wsError(429, "quota", reason)
			}
		}
		if previous != "" && (!src.Continuation || !s.App.Store.continuation(previous, key, src, sent)) {
			return src, sent, cap, wsError(409, "previous_response_id", "前序响应账号/模型/代次归属无效")
		}
		return src, sent, cap, nil
	}
}
func (s *wsSession) sessionCapacityLocked(key ClientKey, src Source) error {
	a := s.App
	if key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent {
		return wsError(429, "concurrency", "Key并发已满")
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		return wsError(429, "concurrency", "账号并发已满")
	}
	if key.RouteID != "" {
		route, err := a.Store.route(key.RouteID)
		if err != nil || !route.Enabled {
			return wsError(503, "target", "路由不可用")
		}
		if route.MaxConcurrent != nil && a.routeActive[key.RouteID] >= *route.MaxConcurrent {
			return wsError(429, "concurrency", "路由并发已满")
		}
	}
	select {
	case a.slots <- struct{}{}:
		return nil
	default:
		return wsError(429, "concurrency", "全局生成/会话并发已满")
	}
}
func (s *wsSession) newRecord(key ClientKey, src Source, public, sent, operation string) Record {
	now := time.Now().UTC()
	rec := Record{ID: id("req"), AttemptID: id("att"), Sequence: 1, Origin: "client", KeyID: key.ID, ClientName: key.Name, Fingerprint: key.Fingerprint, KeyVersion: key.Version, SourceID: src.ID, SourceName: src.Name, SourceVersion: src.Version, Generation: src.Generation, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, RouteID: key.RouteID, Model: public, SentModel: sent, Protocol: wsProtocol(s.Realtime), Operation: operation, Started: now, AttemptStarted: now, Status: "dispatching", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", Completeness: "unknown", Submission: "not_sent", Price: src.Price, Version: 1}
	if candidate, err := s.App.candidateFor(src, sent); err == nil {
		rec.ModelID = candidate.Model.ID
	}
	if settings, err := s.App.Store.readRuntimeSettings(); err == nil {
		rec.ConfigVersion = settings.Version
	}
	if route, err := s.App.Store.route(key.RouteID); err == nil {
		rec.RouteVersion = route.Version
	}
	return rec
}
func (s *wsSession) realtimeAccountingLocked(key ClientKey, src Source, body map[string]json.RawMessage) (*AccountingPlan, error) {
	budgets, err := matchingBudgets(s.App.Store.DB, key.budgetScopeID(), key.RouteID)
	if err != nil {
		return nil, err
	}
	for _, budget := range budgets {
		if budget.Mode == "strict" {
			return nil, wsError(422, "budget", "自动VAD可在客户端create之前生成；Realtime仅支持soft预算，不能兑现strict上界")
		}
	}
	plan, err := s.App.prepareNativeAccounting(key, src, nativeInput{Body: body}, nativeOperationSpec{Name: "realtime_websocket"})
	if err != nil || plan == nil {
		return plan, err
	}
	// A connection admits automatic generation; refuse an already exhausted
	// budget before opening it. Actual VAD attempts reserve their original plan
	// after observation even when unavoidable soft overrun has occurred.
	for _, reservation := range plan.Reservations {
		budget, err := readBudget(s.App.Store.DB, reservation.BudgetID)
		if err != nil {
			return nil, err
		}
		settled, reserved, _, err := budgetTotals(s.App.Store.DB, budget.ID, reservation.PeriodStart.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		amount, err := ledgerRat(reservation.Amount)
		if err != nil {
			return nil, err
		}
		limit, err := accountingRat(budget.AmountLimit)
		if err != nil {
			return nil, err
		}
		occupied := settled.Add(settled, reserved)
		occupied.Add(occupied, amount)
		if occupied.Cmp(limit) > 0 {
			return nil, wsError(429, "budget", "Realtime的本地soft预算可用额不足")
		}
	}
	return plan, nil
}
func (s *wsSession) startRealtimeLocked() error {
	a := s.App
	key := s.InitialKey
	if !allowed(key.OperationAllowlist, "realtime") {
		return wsError(403, "operation", "Key不允许Realtime")
	}
	if key.Limits.TPM != nil {
		return wsError(422, "limits.tpm", "Realtime自动音频/VAD无法提供可信的派发前token估算；不能静默跳过TPM")
	}
	if a.realtimeActive >= 4 {
		return wsError(429, "concurrency", "最多4个活动Realtime会话")
	}
	src, sent, cap, err := s.selectLocked(key, s.PublicModel, "", "realtime")
	if err != nil {
		return err
	}
	plan, err := s.realtimeAccountingLocked(key, src, map[string]json.RawMessage{})
	if err != nil {
		return err
	}
	if err = s.sessionCapacityLocked(key, src); err != nil {
		return err
	}
	s.SessionHeld = true
	a.realtimeActive++
	a.keyActive[key.ID]++
	a.accountActive[src.AccountID]++
	if key.RouteID != "" {
		a.routeActive[key.RouteID]++
	}
	s.Source = &src
	s.SentModel = sent
	s.Cap = cap
	s.SessionPlan = plan
	if plan != nil {
		if a.realtimeBudgetRefs == nil {
			a.realtimeBudgetRefs = map[string]int{}
		}
		for _, reservation := range plan.Reservations {
			a.realtimeBudgetRefs[reservation.BudgetID]++
		}
	}
	rec := s.newRecord(key, src, s.PublicModel, sent, "realtime.session")
	rec.Price = nil // Session connection is not an extra billed generation.
	rec.Accounting = nil
	s.SessionRecord = &rec
	if err = a.Store.record(rec); err != nil {
		s.releaseSessionLocked()
		return storageError()
	}
	s.registerCancellationLocked(rec.ID, src.ID, key.ID)
	return nil
}
func (s *wsSession) releaseSessionLocked() {
	if !s.SessionHeld {
		return
	}
	a := s.App
	if s.SessionPlan != nil {
		for _, reservation := range s.SessionPlan.Reservations {
			a.realtimeBudgetRefs[reservation.BudgetID]--
			if a.realtimeBudgetRefs[reservation.BudgetID] == 0 {
				delete(a.realtimeBudgetRefs, reservation.BudgetID)
			}
		}
	}
	a.realtimeActive--
	a.keyActive[s.InitialKey.ID]--
	a.accountActive[s.Source.AccountID]--
	if s.InitialKey.RouteID != "" {
		a.routeActive[s.InitialKey.RouteID]--
	}
	<-a.slots
	s.SessionHeld = false
	a.signalAdmission()
}
func (s *wsSession) connect(turn *wsTurn, r *http.Request) error {
	if s.Up != nil {
		return nil
	}
	a := s.App
	a.mu.Lock()
	src := *s.Source
	var secret, account string
	var err error
	if src.Kind == "codex_subscription" {
		var c Credential
		c, err = a.subscriptionCredential(s.Context, &src)
		secret, account = c.Access, c.Account
	} else {
		secret, err = a.Secrets.Get(src.CredentialRef)
	}
	if err == nil {
		// Subscription refresh can release App.mu. Recheck fixed ownership,
		// permissions, capability card, and fresh quota before opening native WS.
		var key ClientKey
		key, err = s.keyLocked()
		if err == nil {
			public, expectedSent, operation, previous := s.PublicModel, s.SentModel, "realtime", ""
			if turn != nil {
				public, expectedSent, operation = turn.Record.Model, turn.Record.SentModel, turn.Record.Operation
				_ = json.Unmarshal(turn.Body["previous_response_id"], &previous)
			}
			var sent string
			_, sent, _, err = s.selectLocked(key, public, previous, operation)
			if err == nil && sent != expectedSent {
				err = wsError(409, "model", "原生WebSocket派发前模型映射已变化")
			}
		}
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	endpoint, err := url.Parse(safeEndpoint(src.BaseURL, "/responses"))
	if s.Realtime {
		endpoint, err = url.Parse(safeEndpoint(src.BaseURL, "/realtime"))
		if err == nil {
			values := url.Values{"model": {s.SentModel}}
			endpoint.RawQuery = values.Encode()
		}
	}
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return wsError(422, "base_url", "此卡需要受控HTTP(S) base endpoint")
	}
	if endpoint.Scheme == "http" {
		endpoint.Scheme = "ws"
	} else {
		endpoint.Scheme = "wss"
	}
	headers := http.Header{"Authorization": {"Bearer " + secret}}
	if src.Kind == "codex_subscription" {
		headers.Set("Chatgpt-Account-Id", account)
		headers.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
		headers.Set("originator", "codex_cli_rs")
		headers.Set("Version", s.Cfg.Codex.ClientVersion)
		if state := r.Header.Get("X-Codex-Turn-State"); state != "" {
			if turn == nil || !a.Store.continuation("ws_turnstate_"+digest(state), turn.Key, src, turn.Record.SentModel) {
				return wsError(409, "x-codex-turn-state", "turn-state不属于当前Key/账号代次/model")
			}
			headers.Set("X-Codex-Turn-State", state)
		}
	}
	dialer := websocket.Dialer{HandshakeTimeout: time.Duration(s.Cfg.HeaderTimeout) * time.Second, EnableCompression: false}
	if tr, ok := a.HTTP.Transport.(*http.Transport); ok {
		dialer.Proxy = tr.Proxy
		dialer.NetDialContext = tr.DialContext
		dialer.NetDialTLSContext = tr.DialTLSContext
		dialer.TLSClientConfig = tr.TLSClientConfig
	}
	if src.ProxyURL != nil {
		if err = validProxy(*src.ProxyURL); err != nil {
			return err
		}
		dialer.Proxy = nil
		if *src.ProxyURL != "" {
			proxyURL, _ := url.Parse(*src.ProxyURL)
			dialer.Proxy = http.ProxyURL(proxyURL)
		}
	}
	var response *http.Response
	s.Up, response, err = dialer.DialContext(s.Context, endpoint.String(), headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			if response.StatusCode == 401 {
				return wsError(503, "upstream_auth", "上游WebSocket握手认证失败；未派发create")
			}
		}
		return wsError(502, "upstream_handshake", "原生WebSocket握手失败；不会改走HTTP POST")
	}
	if turn != nil && response != nil {
		if state := response.Header.Get("X-Codex-Turn-State"); state != "" {
			if err = a.Store.bind(turn.Record, "ws_turnstate_"+digest(state)); err != nil {
				return storageError()
			}
		}
	}
	if s.SessionRecord != nil {
		s.SessionRecord.Submission = "confirmed"
		s.SessionRecord.Status = "streaming"
		s.SessionRecord.UpstreamStatus = "session_open"
		if err = a.Store.record(*s.SessionRecord); err != nil {
			return storageError()
		}
	}
	return nil
}

// Admin cancellation callbacks execute while App.mu is held. They only enqueue
// an ID and wake the socket controller, never inspect mutable native lane state
// or block on a socket/channel. Duplicate requests coalesce without buffer growth.
func (s *wsSession) registerCancellationLocked(requestID, sourceID, keyID string) {
	s.App.running[requestID] = func() {
		s.CancelMu.Lock()
		if s.Context.Err() == nil {
			s.CancelPending[requestID] = true
		}
		s.CancelMu.Unlock()
		select {
		case s.CancelWake <- struct{}{}:
		default:
		}
	}
	s.App.runningSources[requestID] = sourceID
	s.App.runningKeys[requestID] = keyID
}

func (s *wsSession) cancelRequested() bool {
	s.CancelMu.Lock()
	pending := s.CancelPending
	s.CancelPending = map[string]bool{}
	s.CancelMu.Unlock()
	if s.SessionRecord != nil && pending[s.SessionRecord.ID] {
		s.CloseCode = websocket.CloseGoingAway
		s.CloseReason = "admin_session_cancel"
		return false
	}
	for _, laneID := range s.LaneOrder {
		turn := s.Lanes[laneID].Active
		if turn == nil || !pending[turn.Record.ID] || turn.CancelRequested {
			continue
		}
		turn.CancelRequested = true
		turn.Record.UpstreamStatus = "cancellation_requested"
		turn.Record.ErrorSummary = "用户已请求原生取消；是否停止生成及费用等待上游观测"
		if err := s.App.Store.record(turn.Record); err != nil {
			s.App.markStorageFailure()
			return false
		}
		wire := map[string]any{"type": "response.cancel"}
		if laneID != "" {
			wire["stream_id"] = laneID
		}
		if turn.ResponseID != "" {
			wire["response_id"] = turn.ResponseID
		}
		if s.UpQ.Put(wsFrame{Kind: websocket.TextMessage, Data: []byte(encode(wire))}) != nil {
			s.CloseCode = websocket.CloseTryAgainLater
			s.CloseReason = "buffer_limit"
			return false
		}
	}
	return true
}

func wsRejectResources(body map[string]json.RawMessage) error {
	var value any
	if json.Unmarshal([]byte(encode(body)), &value) != nil {
		return wsError(400, "event", "JSON结构无效")
	}
	var walk func(any, string) error
	walk = func(value any, path string) error {
		switch v := value.(type) {
		case map[string]any:
			for k, child := range v {
				if slices.Contains([]string{"file_id", "file_ids", "container_id", "vector_store_ids", "conversation_id", "prompt"}, k) && child != nil {
					return wsError(422, path+"."+k, "此资源未接入当前Key/账号归属适配")
				}
				if err := walk(child, path+"."+k); err != nil {
					return err
				}
			}
		case []any:
			for i, child := range v {
				if err := walk(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, "event")
}
func (s *wsSession) createTurnLocked(create *wsCreate, observed bool) (*wsTurn, error) {
	a := s.App
	key, err := s.keyLocked()
	if err != nil && !observed {
		return nil, err
	}
	if observed && err != nil {
		key = s.InitialKey
	}
	operation := "generate"
	body := map[string]json.RawMessage{}
	for k, v := range create.Body {
		body[k] = v
	}
	delete(body, "type")
	delete(body, "stream_id")
	public := s.PublicModel
	sent := s.SentModel
	var src Source
	var cap WebSocketModelCapabilities
	if s.Realtime {
		operation = "realtime"
		src = *s.Source
		cap = s.Cap
		if !observed && !allowed(key.OperationAllowlist, operation) {
			return nil, wsError(403, "operation", "Key没有Realtime operation权限")
		}
		var scoped map[string]json.RawMessage
		if raw, ok := body["response"]; ok {
			if json.Unmarshal(raw, &scoped) != nil {
				return nil, wsError(400, "response", "Realtime response需要对象")
			}
			body = scoped
		}
		if raw, ok := body["model"]; ok {
			var requested string
			if json.Unmarshal(raw, &requested) != nil {
				return nil, wsError(400, "response.model", "model需要字符串")
			}
			if requested != s.PublicModel && requested != s.SentModel {
				return nil, wsError(409, "response.model", "模型变更需要新Realtime session")
			}
		}
		if !observed {
			current, currentSent, currentCap, e := s.selectLocked(key, public, "", operation)
			if e != nil {
				return nil, e
			}
			if currentSent != s.SentModel {
				return nil, wsError(409, "model", "Realtime模型映射已变化")
			}
			src, cap = current, currentCap
		}
	} else {
		if _, ok := body["stream"]; ok {
			return nil, wsError(422, "stream", "Responses WebSocket不用HTTP stream字段")
		}
		if _, ok := body["background"]; ok {
			return nil, wsError(422, "background", "Responses WebSocket不用HTTP background字段")
		}
		var e error
		if raw, ok := body["generate"]; ok {
			var generate bool
			if json.Unmarshal(raw, &generate) != nil {
				return nil, wsError(400, "generate", "generate需要布尔值")
			}
			if !generate {
				operation = "warmup"
			}
		}
		if !allowed(key.OperationAllowlist, operation) {
			return nil, wsError(403, "operation", "Key没有此create operation权限")
		}
		if json.Unmarshal(body["model"], &public) != nil || public == "" {
			return nil, wsError(400, "model", "response.create model必填")
		}
		previous := ""
		if raw, ok := body["previous_response_id"]; ok && string(raw) != "null" {
			if json.Unmarshal(raw, &previous) != nil {
				return nil, wsError(400, "previous_response_id", "前序ID需要字符串或null")
			}
		}
		src, sent, cap, e = s.selectLocked(key, public, previous, operation)
		if e != nil {
			return nil, e
		}
		if operation == "warmup" && (!cap.WebSocketWarmup || cap.WebSocketAdapter != wsPublicCard) {
			return nil, wsError(422, "generate", "此卡没有明确启用native warmup")
		}
		if cap.WebSocketAdapter == wsCodexCard && create.Lane != "" {
			return nil, wsError(422, "stream_id", "Codex0.158.0初始卡只支持默认单lane")
		}
		if cap.WebSocketAdapter == wsCodexCard {
			for _, lane := range s.Lanes {
				if lane.Active != nil {
					return nil, wsError(422, "stream_id", "订阅卡不允许第二个并发create")
				}
			}
		}
	}
	if err = wsRejectResources(body); err != nil {
		return nil, err
	}
	if !s.Realtime {
		if err = a.validateNativeOpaqueHistory(body, key, src, sent); err != nil {
			return nil, wsError(409, "input", err.Error())
		}
		candidate, e := a.candidateFor(src, sent)
		if e != nil {
			return nil, storageError()
		}
		if err = validateExtendedServerTools(body, src, candidate.Model); err != nil {
			return nil, wsError(422, "tools", err.Error())
		}
	} else if err = wsRealtimeTools(body); err != nil {
		return nil, err
	}
	rec := s.newRecord(key, src, public, sent, operation)
	rec.RequestBytes = int64(len(create.Raw))
	rec.QueueMS = time.Since(create.At).Milliseconds()
	if !s.Realtime {
		body["model"] = json.RawMessage(encode(sent))
		estimateBody := map[string]json.RawMessage{}
		for k, v := range body {
			estimateBody[k] = v
		}
		delete(estimateBody, "generate")
		rec.ReservedTokens, rec.TokenReservationSource, err = reservationEstimate(estimateBody, create.Raw)
		if err != nil && key.Limits.TPM != nil {
			return nil, wsError(422, "limits.tpm", err.Error())
		}
		if ok, _ := a.checkTPM(key, rec.ReservedTokens, time.Now()); !ok {
			return nil, wsError(429, "limits.tpm", "本地token估算窗口已满")
		}
		rec.Accounting, err = a.prepareAccounting(key, src, estimateBody)
	} else if observed {
		// Automatic generation was authorized when this upstream session was
		// opened. A subsequent budget edit cannot replace that dispatch snapshot
		// or prevent persistence of a provider generation that already happened.
		rec.Origin = "provider_vad"
		rec.Submission = "confirmed"
		rec.Accounting = s.SessionPlan
		err = nil
	} else {
		rec.Accounting, err = s.realtimeAccountingLocked(key, src, body)
	}
	if err != nil {
		return nil, err
	}
	acquired := false
	if !s.Realtime {
		if err = s.sessionCapacityLocked(key, src); err != nil {
			return nil, err
		}
		acquired = true
	}
	if !observed {
		if ok, _ := a.consumeRPM(key, time.Now()); !ok {
			if acquired {
				<-a.slots
			}
			return nil, wsError(429, "limits.rpm", "Key请求速率已满")
		}
	}
	if hasExtendedServerTools(body) {
		rec.ToolCostStatus = "unknown"
	}
	if err = a.Store.record(rec); err != nil {
		if acquired {
			<-a.slots
		}
		if !observed {
			a.refundRPM(key)
		}
		return nil, err
	}
	if acquired {
		a.keyActive[key.ID]++
		a.accountActive[src.AccountID]++
		if key.RouteID != "" {
			a.routeActive[key.RouteID]++
		}
		a.reserveTPM(key, rec.ID, rec.ReservedTokens, rec.Started)
	}
	if s.Source == nil {
		snapshot := src
		s.Source = &snapshot
		s.Cap = cap
	}
	wire := map[string]json.RawMessage{}
	for k, v := range create.Body {
		wire[k] = v
	}
	if !s.Realtime {
		wire["model"] = json.RawMessage(encode(sent))
	}
	if !s.Realtime && create.Lane != "" {
		wire["stream_id"] = json.RawMessage(encode(create.Lane))
	}
	if s.Realtime {
		if response, ok := wire["response"]; ok {
			var options map[string]json.RawMessage
			_ = json.Unmarshal(response, &options)
			if _, ok = options["model"]; ok {
				options["model"] = json.RawMessage(encode(sent))
				wire["response"] = json.RawMessage(encode(options))
			}
		}
	}
	turn := &wsTurn{Record: rec, Key: key, Source: src, Body: wire, Wire: create.Raw, LastProgress: time.Now(), Acquired: acquired}
	if public != sent {
		turn.Wire = []byte(encode(wire))
	}
	s.registerCancellationLocked(rec.ID, src.ID, key.ID)
	_ = json.Unmarshal(create.Body["event_id"], &turn.EventID)
	return turn, nil
}
func wsRealtimeTools(body map[string]json.RawMessage) error {
	if raw, ok := body["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(raw, &tools) != nil {
			return wsError(400, "tools", "tools需要数组")
		}
		for _, tool := range tools {
			var typ string
			_ = json.Unmarshal(tool["type"], &typ)
			if typ != "function" {
				return wsError(422, "tools", "Realtime卡仅开放客户端function；未接入server MCP/搜索资源")
			}
		}
	}
	return nil
}
func (s *wsSession) sendError(lane string, err error) bool {
	if e := s.DownQ.Put(wsFrame{Kind: websocket.TextMessage, Data: wsLocalError(s.Realtime, lane, err)}); e != nil {
		s.CloseCode = websocket.CloseTryAgainLater
		s.CloseReason = "buffer_limit"
		return false
	}
	return true
}
func (s *wsSession) enqueue(body map[string]json.RawMessage, raw []byte) error {
	laneID, err := wsLaneID(body)
	if err != nil {
		return err
	}
	if s.Realtime && laneID != "" {
		return wsError(422, "stream_id", "Realtime不能继承Responses lane合同")
	}
	lane := s.Lanes[laneID]
	if lane == nil {
		if laneID != "" {
			if s.Named >= 32 {
				return wsError(429, "stream_id", "每连接最多32个命名lane")
			}
			s.Named++
		}
		lane = &wsLane{}
		s.Lanes[laneID] = lane
		s.LaneOrder = append(s.LaneOrder, laneID)
	}
	if !s.Realtime && s.Cap.WebSocketAdapter == wsCodexCard && lane.Active != nil {
		return wsError(422, "stream_id", "订阅单lane卡不接受第二个并发create")
	}
	if s.Pending >= 32 {
		return wsError(429, "queue", "每连接最多32个等待create")
	}
	lane.Queue = append(lane.Queue, &wsCreate{Body: body, Raw: raw, Lane: laneID, At: time.Now()})
	s.Pending++
	return nil
}
func (s *wsSession) pump(r *http.Request) bool {
	for _, name := range s.LaneOrder {
		lane := s.Lanes[name]
		if lane.Active != nil || len(lane.Queue) == 0 {
			continue
		}
		create := lane.Queue[0]
		if time.Since(create.At) > time.Duration(s.Cfg.TotalTimeout)*time.Second {
			lane.Queue = lane.Queue[1:]
			s.Pending--
			if !s.sendError(name, wsError(429, "queue", "本地create等待期限已到；未派发")) {
				return false
			}
			continue
		}
		s.App.mu.Lock()
		turn, err := s.createTurnLocked(create, false)
		s.App.mu.Unlock()
		if err != nil {
			status, field, _ := wsErrorDetails(err)
			if status == 429 && field == "concurrency" {
				continue
			}
			lane.Queue = lane.Queue[1:]
			s.Pending--
			if !s.sendError(name, err) {
				return false
			}
			continue
		}
		lane.Active = turn
		lane.Queue = lane.Queue[1:]
		s.Pending--
		if s.Up == nil {
			if err = s.connect(turn, r); err != nil {
				s.finishTurn(turn, "failed", "upstream_handshake", err.Error())
				lane.Active = nil
				if !s.sendError(name, err) {
					return false
				}
				s.GracefulFlush = true
				return false
			}
			s.startWorkers(s.Up, true, s.UpQ)
		}
		// Durable possible dispatch precedes the writer queue. A partial failed
		// write stays unknown; no create is ever replayed onto a replacement socket.
		turn.Record.Submission = "possible"
		if err = s.App.Store.record(turn.Record); err != nil {
			s.App.markStorageFailure()
			return false
		}
		if err = s.UpQ.Put(wsFrame{Kind: websocket.TextMessage, Data: turn.Wire}); err != nil {
			s.CloseCode = websocket.CloseTryAgainLater
			s.CloseReason = "buffer_limit"
			return false
		}
	}
	return true
}
func (s *wsSession) loop(r *http.Request) {
	first := time.NewTimer(10 * time.Second)
	defer first.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	seen := s.Realtime
	for {
		if !s.pump(r) {
			return
		}
		select {
		case <-s.Context.Done():
			s.CloseCode = websocket.CloseGoingAway
			s.CloseReason = "cancelled_or_session_limit"
			return
		case <-s.CancelWake:
			if !s.cancelRequested() {
				return
			}
		case <-first.C:
			if !seen {
				s.CloseCode = websocket.ClosePolicyViolation
				s.CloseReason = "first_create_timeout"
				return
			}
		case <-tick.C:
			now := time.Now()
			if !s.Expires.IsZero() && !now.Before(s.Expires) {
				s.CloseCode = websocket.CloseGoingAway
				s.CloseReason = "provider_session_limit"
				return
			}
			active := false
			for _, lane := range s.Lanes {
				if lane.Active != nil {
					active = true
					if now.Sub(lane.Active.Record.Started) > time.Duration(s.Cfg.TotalTimeout)*time.Second || now.Sub(lane.Active.LastProgress) > time.Duration(s.Cfg.IdleTimeout)*time.Second {
						s.CloseCode = websocket.CloseGoingAway
						s.CloseReason = "generation_timeout"
						return
					}
				}
			}
			s.App.mu.Lock()
			key, err := s.keyLocked()
			if err == nil && s.Realtime {
				_, sent, _, currentErr := s.selectLocked(key, s.PublicModel, "", "realtime")
				err = currentErr
				if err == nil && sent != s.SentModel {
					err = wsError(409, "model", "Realtime模型映射已变化；需要新session")
				}
			}
			s.App.mu.Unlock()
			if err != nil {
				status, _, _ := wsErrorDetails(err)
				if s.Realtime || !active || status == 503 {
					s.CloseCode = websocket.CloseGoingAway
					s.CloseReason = "key_or_admission_changed"
					return
				}
			}
			if s.Realtime && !allowed(key.OperationAllowlist, "realtime") {
				s.CloseCode = websocket.ClosePolicyViolation
				s.CloseReason = "permission_changed"
				return
			}
		case frame := <-s.Events:
			s.ReadQ.Done(frame)
			if frame.DeliveryID != "" {
				s.delivery(frame)
				if frame.Err == nil {
					continue
				}
			}
			if frame.Err != nil {
				if _, field, _ := wsErrorDetails(frame.Err); field == "buffer" {
					s.CloseCode = websocket.CloseTryAgainLater
					s.CloseReason = "buffer_limit"
					return
				}
				s.CloseCode = websocket.CloseGoingAway
				s.CloseReason = "client_disconnect"
				if frame.Upstream {
					s.CloseReason = "provider_disconnect"
				}
				var closeError *websocket.CloseError
				if errors.As(frame.Err, &closeError) && frame.Upstream && closeError.Code == websocket.CloseNormalClosure {
					s.GracefulFlush = true
				}
				if errors.As(frame.Err, &closeError) && closeError.Code == websocket.CloseMessageTooBig {
					s.CloseCode = websocket.CloseMessageTooBig
					s.CloseReason = "message_limit"
				}
				if strings.Contains(frame.Err.Error(), "read limit") {
					s.CloseCode = websocket.CloseMessageTooBig
					s.CloseReason = "message_limit"
				}
				return
			}
			if frame.Upstream {
				if !s.upstream(frame) {
					return
				}
			} else {
				if frame.Kind != websocket.TextMessage {
					if !s.sendError("", wsError(422, "frame", "此native卡客户端使用JSON文本事件；binary不是HTTP/SSE")) {
						return
					}
					continue
				}
				body, kind, err := wsJSON(frame.Data)
				if err != nil {
					if !s.sendError("", err) {
						return
					}
					continue
				}
				if s.Realtime && int64(len(frame.Data)) > wsMediaLimit {
					s.CloseCode = websocket.CloseMessageTooBig
					s.CloseReason = "media_frame_limit"
					return
				}
				if kind == "response.create" {
					if err = s.enqueue(body, frame.Data); err != nil {
						lane, _ := wsLaneID(body)
						if !s.sendError(lane, err) {
							return
						}
					}
					seen = true
				} else if s.Realtime {
					if err = s.realtimeControl(body, kind, frame.Data); err != nil {
						if !s.sendError("", err) {
							return
						}
					}
				} else if kind == "response.cancel" {
					if err = s.responsesCancel(body, frame.Data); err != nil {
						if !s.sendError("", err) {
							return
						}
					}
				} else {
					if !s.sendError("", wsError(422, "type", "此卡尚未声明该client事件；不能扩展为HTTP调用")) {
						return
					}
				}
			}
		}
	}
}
func (s *wsSession) responsesCancel(body map[string]json.RawMessage, raw []byte) error {
	laneID, err := wsLaneID(body)
	if err != nil {
		return err
	}
	lane := s.Lanes[laneID]
	if lane == nil || lane.Active == nil || s.Up == nil {
		return wsError(409, "response_id", "此lane没有活动生成")
	}
	if idRaw, ok := body["response_id"]; ok {
		var responseID string
		if json.Unmarshal(idRaw, &responseID) != nil || responseID != lane.Active.ResponseID {
			return wsError(409, "response_id", "cancel不属于当前lane/Key")
		}
	}
	return s.UpQ.Put(wsFrame{Kind: websocket.TextMessage, Data: raw})
}
func (s *wsSession) realtimeControl(body map[string]json.RawMessage, kind string, raw []byte) error {
	a := s.App
	a.mu.Lock()
	key, err := s.keyLocked()
	if err == nil && !allowed(key.OperationAllowlist, "realtime") {
		err = wsError(403, "operation", "Key已无Realtime权限")
	}
	if err == nil {
		_, sent, _, e := s.selectLocked(key, s.PublicModel, "", "realtime")
		err = e
		if err == nil && sent != s.SentModel {
			err = wsError(409, "model", "Realtime模型映射已变化")
		}
	}
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err = wsRejectResources(body); err != nil {
		return err
	}
	a.mu.Lock()
	_, err = s.realtimeAccountingLocked(key, *s.Source, map[string]json.RawMessage{})
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if kind == "response.cancel" {
		if rawID, ok := body["response_id"]; ok {
			var responseID string
			_ = json.Unmarshal(rawID, &responseID)
			lane := s.Lanes[""]
			if lane == nil || lane.Active == nil || lane.Active.ResponseID != responseID {
				return wsError(409, "response_id", "cancel不属于此Key/session")
			}
		}
	}
	if strings.HasPrefix(kind, "conversation.item.") && kind != "conversation.item.create" {
		var itemID string
		_ = json.Unmarshal(body["item_id"], &itemID)
		if itemID == "" || !s.Items[itemID] {
			return wsError(409, "item_id", "item不属于当前Key/session")
		}
	}
	if kind == "conversation.item.create" {
		var item map[string]json.RawMessage
		if json.Unmarshal(body["item"], &item) != nil {
			return wsError(400, "item", "item需要对象")
		}
		var typ, callID string
		_ = json.Unmarshal(item["type"], &typ)
		_ = json.Unmarshal(item["call_id"], &callID)
		if typ == "function_call_output" && !s.Calls[callID] {
			return wsError(409, "item.call_id", "工具结果不属于此session")
		}
		if strings.HasPrefix(typ, "mcp") {
			return wsError(422, "item.type", "此卡没有server MCP权限")
		}
	}
	switch kind {
	case "session.update":
		var session map[string]json.RawMessage
		if json.Unmarshal(body["session"], &session) != nil {
			return wsError(400, "session", "session需要对象")
		}
		if rawModel, ok := session["model"]; ok {
			var model string
			if json.Unmarshal(rawModel, &model) != nil {
				return wsError(400, "session.model", "model需要字符串")
			}
			if model != s.PublicModel && model != s.SentModel {
				return wsError(409, "session.model", "更换模型需要新session")
			}
			if model != s.SentModel {
				session["model"] = json.RawMessage(encode(s.SentModel))
				body["session"] = json.RawMessage(encode(session))
				raw = []byte(encode(body))
			}
		}
		if err = wsRealtimeTools(session); err != nil {
			return err
		}
		// Inline transcription is a distinct model/operation and billing path.
		// This card cannot turn it on under the generation permission alone.
		var value any
		_ = json.Unmarshal(body["session"], &value)
		var transcription func(any) bool
		transcription = func(value any) bool {
			if object, ok := value.(map[string]any); ok {
				for k, v := range object {
					if (k == "transcription" || k == "input_audio_transcription") && v != nil {
						return true
					}
					if transcription(v) {
						return true
					}
				}
			}
			return false
		}
		if transcription(value) {
			return wsError(422, "session.audio.input.transcription", "inline转录需要独立模型、权限与计量适配；此卡未声明")
		}
	case "input_audio_buffer.append", "input_audio_buffer.commit", "input_audio_buffer.clear", "conversation.item.create", "conversation.item.truncate", "conversation.item.delete", "conversation.item.retrieve", "response.cancel":
		// Native provider validates media, playback offset, format and voice. Cove
		// never executes tools, decodes audio into text, or guesses playback time.
	default:
		return wsError(422, "type", "此Realtime客户端事件尚未声明支持")
	}
	return s.UpQ.Put(wsFrame{Kind: websocket.TextMessage, Data: raw})
}
func (s *wsSession) upstream(frame wsFrame) bool {
	if frame.Kind != websocket.TextMessage {
		for _, lane := range s.Lanes {
			if lane.Active != nil {
				lane.Active.Record.ObservationStatus = "partial"
				lane.Active.LastProgress = time.Now()
			}
		}
		if s.DownQ.Put(frame) != nil {
			s.CloseCode = websocket.CloseTryAgainLater
			s.CloseReason = "buffer_limit"
			return false
		}
		return true
	}
	body, kind, err := wsJSON(frame.Data)
	if err != nil {
		s.CloseCode = websocket.CloseUnsupportedData
		s.CloseReason = "provider_json_invalid"
		return false
	}
	if s.Realtime {
		var item struct {
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Type   string `json:"type"`
		}
		_ = json.Unmarshal(body["item"], &item)
		if item.ID != "" {
			s.Items[item.ID] = true
		}
		if item.Type == "function_call" && item.CallID != "" {
			s.Calls[item.CallID] = true
		}
		var itemID string
		_ = json.Unmarshal(body["item_id"], &itemID)
		if itemID != "" {
			s.Items[itemID] = true
		}
		if kind == "session.created" || kind == "session.updated" {
			var session struct {
				Model   string          `json:"model"`
				Expires int64           `json:"expires_at"`
				Usage   json.RawMessage `json:"usage"`
			}
			_ = json.Unmarshal(body["session"], &session)
			if session.Model != "" {
				if s.ProviderModel != "" && session.Model != s.ProviderModel {
					s.CloseCode = websocket.ClosePolicyViolation
					s.CloseReason = "provider_model_changed"
					return false
				}
				// A native alias can resolve to a dated model. The first server
				// value is observation, not a new authorization or routing target.
				s.ProviderModel = session.Model
			}
			if session.Expires > 0 {
				s.Expires = time.Unix(session.Expires, 0)
				if !s.Expires.After(time.Now()) {
					s.CloseCode = websocket.CloseGoingAway
					s.CloseReason = "provider_session_limit"
					return false
				}
			}
			if len(session.Usage) > 0 {
				s.SessionUsage = session.Usage
			}
		}
		if kind == "session.usage" {
			s.SessionUsage = body["usage"]
		}
	}
	laneID, err := wsLaneID(body)
	if err != nil {
		s.CloseReason = "provider_lane_invalid"
		return false
	}
	lane := s.Lanes[laneID]
	var response struct {
		ID, Status string
		Usage      json.RawMessage `json:"usage"`
		Model      string
		Output     []map[string]json.RawMessage `json:"output"`
	}
	if raw, ok := body["response"]; ok {
		_ = json.Unmarshal(raw, &response)
	}
	if s.Realtime {
		for _, item := range response.Output {
			var itemID, callID, itemType string
			_ = json.Unmarshal(item["id"], &itemID)
			_ = json.Unmarshal(item["call_id"], &callID)
			_ = json.Unmarshal(item["type"], &itemType)
			if itemID != "" {
				s.Items[itemID] = true
			}
			if itemType == "function_call" && callID != "" {
				s.Calls[callID] = true
			}
		}
	}
	if s.Realtime && kind == "response.created" && (lane == nil || lane.Active == nil) {
		// Automatic VAD has already dispatched. Keep the provider attempt even if
		// the Key/budget was changed in the unavoidable observation race.
		s.App.mu.Lock()
		turn, e := s.createTurnLocked(&wsCreate{Body: map[string]json.RawMessage{"type": json.RawMessage(`"response.create"`)}, At: time.Now()}, true)
		s.App.mu.Unlock()
		if e != nil {
			s.App.markStorageFailure()
			s.CloseReason = "vad_record_failed"
			return false
		}
		if lane == nil {
			lane = &wsLane{}
			s.Lanes[laneID] = lane
			s.LaneOrder = append(s.LaneOrder, laneID)
		}
		lane.Active = turn
	}
	if lane != nil && lane.Active != nil {
		turn := lane.Active
		frame.DeliveryID = turn.Record.ID
		turn.LastProgress = time.Now()
		rec := &turn.Record
		if rec.FirstEvent == nil {
			now := time.Now().UTC()
			rec.FirstEvent = &now
		}
		rec.UpstreamBytes += int64(len(frame.Data))
		if response.ID != "" {
			if turn.ResponseID != "" && turn.ResponseID != response.ID {
				s.CloseReason = "response_identity_changed"
				return false
			}
			turn.ResponseID = response.ID
			rec.ResponseID = response.ID
			rec.Submission = "confirmed"
			rec.UpstreamStatus = "generating"
			rec.Status = "streaming"
			if err = s.App.Store.bind(*rec, response.ID); err != nil {
				s.App.markStorageFailure()
				return false
			}
		}
		if kind == "response.created" || kind == "response.in_progress" {
			rec.Status = "streaming"
		}
		if strings.HasSuffix(kind, ".delta") && rec.FirstContentAt == nil {
			now := time.Now().UTC()
			rec.FirstContentAt = &now
		}
		if s.Realtime {
			wsMergeRealtimeUsage(rec, response.Usage)
		} else {
			mergeUsage(&rec.Usage, response.Usage)
		}
		if err = s.bindOpaqueOutputs(*rec, response.Output); err != nil {
			s.App.markStorageFailure()
			return false
		}
		terminal := kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed" || s.Realtime && kind == "response.done"
		if kind == "error" {
			var upstreamError struct {
				EventID string `json:"event_id"`
			}
			_ = json.Unmarshal(body["error"], &upstreamError)
			if !s.Realtime || turn.EventID != "" && upstreamError.EventID == turn.EventID {
				frame.Final = true
				s.Completed[rec.ID] = turn
				s.finishTurn(turn, "failed", "upstream_error", "上游返回原生request错误；未重试")
				lane.Active = nil
			}
		} else if terminal {
			status := "failed"
			if kind == "response.completed" || s.Realtime && response.Status == "completed" {
				status = "succeeded"
			}
			if response.Status == "cancelled" {
				status = "cancelled"
			}
			if len(response.Usage) > 0 && s.Realtime {
				s.ResponseUsageObserved = true
			}
			rec.UpstreamStatus = response.Status
			if rec.UpstreamStatus == "" {
				rec.UpstreamStatus = strings.TrimPrefix(kind, "response.")
			}
			frame.Final = true
			s.Completed[rec.ID] = turn
			s.finishTurn(turn, status, "", "")
			lane.Active = nil
		}
	}
	if err = s.DownQ.Put(frame); err != nil {
		s.CloseCode = websocket.CloseTryAgainLater
		s.CloseReason = "buffer_limit"
		return false
	}
	if kind == "error" && !s.Realtime {
		var nativeError struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body["error"], &nativeError)
		// This documented native error ends the entire transport. Other error
		// events remain per-request errors and keep their original wire shape.
		if nativeError.Code == "websocket_connection_limit_reached" {
			s.CloseCode = websocket.CloseGoingAway
			s.CloseReason = "provider_connection_limit"
			s.GracefulFlush = true
			return false
		}
	}
	return true
}
func (s *wsSession) bindOpaqueOutputs(rec Record, output []map[string]json.RawMessage) error {
	for _, item := range output {
		var opaque string
		_ = json.Unmarshal(item["encrypted_content"], &opaque)
		if opaque != "" {
			if err := s.App.Store.bind(rec, opaqueBinding(opaque, *s.Source)); err != nil {
				return err
			}
		}
	}
	return nil
}
func wsMergeRealtimeUsage(rec *Record, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	mergeUsage(&rec.Usage, raw)
	var usage struct {
		InputDetails  map[string]json.RawMessage `json:"input_token_details"`
		OutputDetails map[string]json.RawMessage `json:"output_token_details"`
	}
	_ = json.Unmarshal(raw, &usage)
	if rec.UsageDimensions == nil {
		rec.UsageDimensions = map[string]string{}
	}
	for direction, details := range map[string]map[string]json.RawMessage{"input": usage.InputDetails, "output": usage.OutputDetails} {
		for _, field := range []string{"audio_tokens", "text_tokens", "image_tokens", "cached_tokens"} {
			var n int64
			if v, ok := details[field]; ok && json.Unmarshal(v, &n) == nil && n >= 0 {
				rec.UsageDimensions[direction+"_"+strings.TrimSuffix(field, "s")] = strconv.FormatInt(n, 10)
			}
		}
	}
}
func (s *wsSession) finishTurn(turn *wsTurn, status, stage, message string) {
	rec := &turn.Record
	if rec.Ended != nil {
		return
	}
	now := time.Now().UTC()
	rec.Ended = &now
	rec.DurationMS = now.Sub(rec.Started).Milliseconds()
	rec.Status = status
	rec.ErrorStage = stage
	rec.ErrorSummary = message
	rec.Completeness = usageCompleteness(rec.Usage)
	if rec.ObservationStatus == "partial" && rec.Completeness == "complete" {
		rec.Completeness = "partial"
	}
	if rec.Submission == "not_sent" {
		zero := "0"
		rec.Cost = &zero
	} else if s.Realtime {
		finalizeNativeCost(rec, nativeOperationSpec{Name: "realtime_websocket"})
		if rec.Completeness == "unknown" {
			rec.Cost = nil
		}
	} else {
		rec.Cost = estimate(rec.Usage, rec.Price)
		if rec.Cost == nil {
			rec.PartialCost = estimatePartial(rec.Usage, rec.Price)
		}
		finalizeExtendedCost(rec)
	}
	if err := s.App.Store.record(*rec); err != nil {
		s.App.markStorageFailure()
	}
	s.App.mu.Lock()
	delete(s.App.running, rec.ID)
	delete(s.App.runningSources, rec.ID)
	delete(s.App.runningKeys, rec.ID)
	if turn.Acquired {
		s.App.keyActive[turn.Key.ID]--
		s.App.accountActive[turn.Source.AccountID]--
		if turn.Key.RouteID != "" {
			s.App.routeActive[turn.Key.RouteID]--
		}
		<-s.App.slots
		turn.Acquired = false
		s.App.settleTPM(turn.Key, *rec)
		s.App.signalAdmission()
	}
	s.App.mu.Unlock()
	s.App.observeExecution(*rec, turn.Source, true)
}
func (s *wsSession) delivery(frame wsFrame) {
	turn := s.Completed[frame.DeliveryID]
	if turn == nil {
		for _, lane := range s.Lanes {
			if lane.Active != nil && lane.Active.Record.ID == frame.DeliveryID {
				turn = lane.Active
				break
			}
		}
	}
	if turn == nil {
		return
	}
	rec := &turn.Record
	if frame.Err != nil {
		rec.DeliveryStatus = "failed"
		rec.ErrorStage = "downstream_write"
		rec.ErrorSummary = "原生输出已观察，但客户端接收失败"
	} else if frame.Final {
		rec.DeliveryStatus = "completed"
	} else if rec.DeliveryStatus != "completed" {
		rec.DeliveryStatus = "streaming"
	}
	if rec.Ended != nil {
		if err := s.App.Store.record(*rec); err != nil {
			s.App.markStorageFailure()
		}
		if frame.Final && frame.Err == nil {
			delete(s.Completed, frame.DeliveryID)
		}
	}
}
func (s *wsSession) finishAll(status, stage, message string) {
	for {
		select {
		case frame := <-s.Events:
			if frame.DeliveryID != "" {
				s.delivery(frame)
			}
		default:
			goto drained
		}
	}
drained:
	for _, turn := range s.Completed {
		if turn.Record.DeliveryStatus != "completed" {
			turn.Record.DeliveryStatus = "failed"
			turn.Record.ErrorStage = "downstream_write"
			turn.Record.ErrorSummary = "原生终态未确认写入客户端"
			if err := s.App.Store.record(turn.Record); err != nil {
				s.App.markStorageFailure()
			}
		}
	}
	for _, lane := range s.Lanes {
		if lane.Active != nil {
			s.finishTurn(lane.Active, status, stage, message)
			lane.Active = nil
		}
		lane.Queue = nil
	}
	if rec := s.SessionRecord; rec != nil && rec.Ended == nil {
		now := time.Now().UTC()
		rec.Ended = &now
		rec.DurationMS = now.Sub(rec.Started).Milliseconds()
		rec.Status = "interrupted"
		rec.ErrorStage = stage
		rec.ErrorSummary = message
		if len(s.SessionUsage) > 0 && !s.ResponseUsageObserved {
			rec.Price = s.Source.Price
			wsMergeRealtimeUsage(rec, s.SessionUsage)
			rec.ObservationStatus = "session_indivisible"
			finalizeNativeCost(rec, nativeOperationSpec{Name: "realtime_websocket"})
			rec.Completeness = usageCompleteness(rec.Usage)
		} else {
			rec.Price = nil
			rec.Cost = nil
			rec.ObservationStatus = "connection_only"
			rec.Completeness = "unknown"
		}
		if err := s.App.Store.record(*rec); err != nil {
			s.App.markStorageFailure()
		}
	}
	s.App.mu.Lock()
	if s.SessionRecord != nil {
		delete(s.App.running, s.SessionRecord.ID)
		delete(s.App.runningSources, s.SessionRecord.ID)
		delete(s.App.runningKeys, s.SessionRecord.ID)
	}
	s.releaseSessionLocked()
	s.App.mu.Unlock()
}
