package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type App struct {
	backupQuiescing    bool
	backupOwners       int
	backupChanged      chan struct{}
	stagedAdmission    bool
	observations       *runtimeObservations
	Config             Config
	Store              *Store
	Secrets            Secrets
	HTTP               *http.Client
	networkMu          sync.Mutex
	networkClients     map[string]*http.Client
	clientMu           sync.Mutex
	mu                 sync.Mutex // Admission/config/credential publication share one short lifecycle boundary.
	sessions           map[string]time.Time
	tickets            map[string]time.Time
	failures           []time.Time
	logins             map[string]*Login
	refreshes          map[string]*credentialRefresh
	running            map[string]context.CancelFunc
	runningSources     map[string]string
	runningKeys        map[string]string
	operationCancels   map[string]context.CancelFunc
	rateBuckets        map[string]*rateBucket
	tpmWindows         map[string]map[string]tokenReservation
	keyActive          map[string]int
	accountActive      map[string]int
	routeActive        map[string]int
	webSocketActive    int
	realtimeActive     int
	realtimeBudgetRefs map[string]int
	quotaObserver      quotaObservationState
	policyState        PolicyState
	routeWeights       map[string]map[string]int
	queued             map[string]queuedRequest
	admissionChanged   chan struct{}
	pageCursors        map[string]requestCursor
	slots              chan struct{}
	storageFailed      atomic.Bool
	maintenanceError   string
	stopping           bool
	ownedTasks         sync.WaitGroup // Add under mu, only while admission is open.
	drained            chan struct{}
	maintenanceCancel  context.CancelFunc
	adminDigest        string
	handler            http.Handler
}

func New(c Config, s *Store, secrets Secrets, assets fs.FS) (*App, error) {
	var adminHash string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key='admin_digest'").Scan(&adminHash)
	if err != nil {
		// Existing metadata must not silently reset access when the vault is unavailable.
		var count int
		if e := s.DB.QueryRow("SELECT count(*) FROM settings WHERE key='admin_digest'").Scan(&count); e != nil {
			return nil, e
		}
		if count != 0 {
			return nil, err
		}
		secret, e := secrets.Get("administrator")
		if e != nil {
			secret = token()
			if e = secrets.Put("administrator", secret); e != nil {
				return nil, e
			}
		}
		adminHash = digest(secret)
		if _, e = s.DB.Exec("INSERT INTO settings(key,value) VALUES('admin_digest',?)", adminHash); e != nil {
			return nil, e
		}
	}
	var retention int
	if e := s.DB.QueryRow("SELECT value FROM settings WHERE key='retention_days'").Scan(&retention); e == nil && retention > 0 {
		c.RetentionDays = retention
	}
	runtime, settingsErr := s.readRuntimeSettings()
	if settingsErr != nil {
		return nil, settingsErr
	}
	runtime.Current.apply(&c)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = time.Duration(c.HeaderTimeout) * time.Second
	tr.DisableCompression = true
	a := &App{Config: c, Store: s, Secrets: secrets, HTTP: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, sessions: map[string]time.Time{}, tickets: map[string]time.Time{}, logins: map[string]*Login{}, refreshes: map[string]*credentialRefresh{}, running: map[string]context.CancelFunc{}, runningSources: map[string]string{}, runningKeys: map[string]string{}, slots: make(chan struct{}, c.MaxConcurrent), adminDigest: adminHash}
	a.observations = newRuntimeObservations()
	a.quotaObserver.RefreshRejected = func(ctx context.Context, src *Source, rejected string) (Credential, error) {
		return a.subscriptionCredential(ctx, src, rejected)
	}
	if e := a.loadTelemetry(); e != nil {
		return nil, e
	}
	a.operationCancels = map[string]context.CancelFunc{}
	a.rateBuckets = map[string]*rateBucket{}
	a.tpmWindows = map[string]map[string]tokenReservation{}
	a.keyActive = map[string]int{}
	a.accountActive = map[string]int{}
	a.routeActive = map[string]int{}
	a.routeWeights = map[string]map[string]int{}
	a.pageCursors = map[string]requestCursor{}
	a.queued = map[string]queuedRequest{}
	a.admissionChanged = make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "pid": os.Getpid(), "build_id": BuildID, "version": Version})
	})
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("/v1/", a.data)
	mux.HandleFunc("/v1beta/", a.geminiData)
	mux.HandleFunc("/admin/", a.admin)
	if assets != nil {
		mux.Handle("/", http.FileServerFS(assets))
	}
	a.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != c.Listen {
			fail(w, 403, "Host 与监听地址不符", "")
			return
		}
		mux.ServeHTTP(w, r)
	})
	if err := s.cleanup(c.RetentionDays); err != nil {
		a.markStorageFailure()
	}
	if c.DataDir != "" {
		if err := a.cleanupOperationArtifacts(context.Background()); err != nil {
			a.maintenanceError = err.Error()
		}
	}
	if c.DataDir != "" {
		if err := a.cleanupResourceSpools(context.Background()); err != nil {
			return nil, err
		}
	}
	a.EnableBackupGate()
	return a, nil
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		// Probes remain observable while the listener drains. They cannot start
		// owned work; ready returns before touching storage once admission closes.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
			a.handler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/messages" {
			w = &messagesErrors{ResponseWriter: w}
		}
		fail(w, 503, "服务尚未开放请求准入", "")
		return
	}
	var release func()
	if !backupGateExempt(r) {
		var err error
		release, err = a.beginBackupOwnerLocked()
		if err != nil {
			a.mu.Unlock()
			fail(w, 503, err.Error(), "")
			return
		}
	}
	if a.stagedAdmission && (r.Method != "GET" || strings.HasPrefix(r.URL.Path, "/v1/") || strings.HasPrefix(r.URL.Path, "/v1beta/")) {
		a.mu.Unlock()
		if release != nil {
			release()
		}
		fail(w, 503, "恢复实例尚未激活", "")
		return
	}
	a.ownedTasks.Add(1)
	a.mu.Unlock()
	defer a.ownedTasks.Done()
	if release != nil {
		defer release()
	}
	a.handler.ServeHTTP(w, r)
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	// New completes initialization before publishing the App to the listener.
	a.mu.Lock()
	stopping := a.stopping || a.stagedAdmission || a.backupQuiescing
	a.mu.Unlock()
	if stopping {
		fail(w, 503, "服务尚未开放请求准入", "")
		return
	}
	if a.storageFailed.Load() {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if a.Secrets.Health() != nil {
		fail(w, 503, "本地凭据存储异常；请重启后检查账号状态", "")
		return
	}
	// Bound the probe when the single SQLite connection is busy. A cancelled
	// probe does not establish a persistent storage fault or change admission.
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.Store.DB.PingContext(ctx); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ready"})
}

func (a *App) ReadyForSwitch(ctx context.Context) error {
	a.mu.Lock()
	closing := a.stopping
	a.mu.Unlock()
	if closing || a.storageFailed.Load() {
		return storageError()
	}
	if err := a.Secrets.Health(); err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return a.Store.DB.PingContext(probe)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message, param string) {
	requestID := w.Header().Get("X-Gateway-Request-Id")
	if requestID == "" {
		requestID = id("req")
		w.Header().Set("X-Gateway-Request-Id", requestID)
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": "gateway_error", "param": param, "field": param}, "request_id": requestID})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "请求 JSON 无效或超出大小限制", "")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "只接受一个 JSON 对象", "")
		return false
	}
	return true
}
func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	w = &managementErrors{ResponseWriter: w}
	if r.URL.Path == "/admin/browser-tickets" && r.Method == "POST" {
		a.browserTicket(w, r)
		return
	}
	if r.Method != "GET" && r.Header.Get("Origin") != "http://"+a.Config.Listen {
		fail(w, 403, "管理写请求的 Origin 不匹配", "")
		return
	}
	if r.Method == "GET" && r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+a.Config.Listen {
		fail(w, 403, "跨来源读取被拒绝", "")
		return
	}
	if r.URL.Path == "/admin/session" && r.Method == "POST" {
		a.loginAdmin(w, r)
		return
	}
	session := bearer(r)
	a.mu.Lock()
	valid := session != "" && a.sessions[digest(session)].After(time.Now())
	a.mu.Unlock()

	if !valid {
		fail(w, 401, "本机连接已失效，请刷新页面重新连接", "")
		return
	}

	if r.URL.Path == "/admin/session" && r.Method == "DELETE" {
		a.mu.Lock()
		delete(a.sessions, digest(session))
		a.mu.Unlock()
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	a.withAdminAction(w, r, a.adminDispatch)
}

func (a *App) adminDispatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/audit" {
		a.auditAPI(w, r)
		return
	}
	if a.notificationsAPI(w, r) {
		return
	}
	if a.resourceConsoleAPI(w, r) {
		return
	}
	if a.extendedProtocolsAPI(w, r) {
		return
	}
	if r.URL.Path == "/admin/telemetry" {
		a.telemetryAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/metrics" {
		a.runtimeMetrics(w, r)
		return
	}
	if r.URL.Path == "/admin/prices" {
		a.pricesAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/config-extensions" || strings.HasPrefix(r.URL.Path, "/admin/config-extensions/") {
		a.configExtensionsAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/retention-cleanup" {
		a.retentionCleanupAPI(w, r)
		return
	}
	if a.operationsExtraAPI(w, r) {
		return
	}
	if r.URL.Path == "/admin/status" && r.Method == "GET" {
		a.mu.Lock()
		secretsHealthy := a.Secrets.Health() == nil
		out := map[string]any{"version": Version, "build_id": BuildID, "listen": a.Config.Listen, "data_dir": a.Config.DataDir, "storage_healthy": !a.storageFailed.Load(), "maintenance_error": a.maintenanceError, "active_requests": len(a.running), "queued_requests": len(a.queued), "queue": a.queueView(), "account_active": maps.Clone(a.accountActive), "route_active": maps.Clone(a.routeActive), "secrets_healthy": secretsHealthy, "accepting_requests": !a.stopping && !a.stagedAdmission && !a.backupQuiescing && !a.storageFailed.Load() && secretsHealthy, "subscription_status": "experimental_unverified", "codex_client_version": a.Config.Codex.ClientVersion}
		a.mu.Unlock()
		for k, v := range a.operationsStatus() {
			if a.Config.PublicAPIBase != "" && (k == "data_dir" || k == "config_path" || k == "runtime" || k == "platform") {
				continue
			}
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
		if a.Config.PublicAPIBase != "" {
			out["api_base_url"] = a.Config.PublicAPIBase
			delete(out, "data_dir")
		}
		writeJSON(w, 200, out)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/sources") {
		a.sourcesAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/clients" || strings.HasPrefix(r.URL.Path, "/admin/clients/") {
		a.clientsAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/client-changes" || strings.HasPrefix(r.URL.Path, "/admin/client-changes/") {
		a.clientChangesAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/budgets") {
		a.budgetsAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/models") {
		a.modelsAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/accounts") {
		a.accountsAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/operations") {
		a.operationsAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/routes") {
		a.routesAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/model-aliases") {
		a.aliasesAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/client-keys") {
		a.keysAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/requests") {
		a.requestsAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/usage" && r.Method == "GET" {
		a.usageAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/settings" {
		a.settingsAPI(w, r)
		return
	}
	if r.URL.Path == "/admin/diagnostics/export" && r.Method == "POST" {
		a.diagnostics(w, r)
		return
	}
	fail(w, 404, "管理接口不存在", "")
}
func bearer(r *http.Request) string {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return value
}

// Caller holds a.mu. Both native-ticket and browser-session failures share this limit.
func (a *App) allowAdminLogin(w http.ResponseWriter) bool {
	now := time.Now()
	recent := a.failures[:0]
	for _, t := range a.failures {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	a.failures = recent
	if len(a.failures) >= 5 {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "登录失败过多，请一分钟后重试", "")
		return false
	}
	return true
}

func (a *App) browserTicket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		fail(w, 403, "仅本机启动器可申请管理票据", "")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.allowAdminLogin(w) {
		return
	}
	h := digest(bearer(r))
	if subtle.ConstantTimeCompare([]byte(h), []byte(a.adminDigest)) != 1 {
		a.failures = append(a.failures, time.Now())
		fail(w, 401, "管理员凭据不正确", "")
		return
	}
	now := time.Now()
	for k, expires := range a.tickets {
		if !expires.After(now) {
			delete(a.tickets, k)
		}
	}
	if len(a.tickets) >= 16 {
		fail(w, 429, "打开请求过多，请一分钟后重试", "")
		return
	}
	ticket := token()
	expires := now.Add(time.Minute)
	a.tickets[digest(ticket)] = expires
	writeJSON(w, 201, map[string]any{"ticket": ticket, "expires_at": expires})
}

func (a *App) loginAdmin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Ticket string `json:"ticket"`
	}
	if !decode(w, r, &input) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if input.Ticket != "" {
		if !a.allowAdminLogin(w) {
			return
		}
		key := digest(input.Ticket)
		valid := a.tickets[key].After(time.Now())
		delete(a.tickets, key)
		if !valid {
			a.failures = append(a.failures, time.Now())
			fail(w, 401, "本机票据已失效", "")
			return
		}
	}
	// Exact Host and Origin were checked before reaching this local-only endpoint.
	// The browser obtains a short-lived token automatically, without a password.
	if session := bearer(r); session != "" && a.sessions[digest(session)].After(time.Now()) {
		writeJSON(w, 200, map[string]any{"session_token": session, "expires_at": a.sessions[digest(session)]})
		return
	}
	a.issueAdminSession(w)
}

// Caller holds a.mu.
func (a *App) issueAdminSession(w http.ResponseWriter) {
	now := time.Now()
	for k, expires := range a.sessions {
		if !expires.After(now) {
			delete(a.sessions, k)
		}
	}
	if len(a.sessions) >= 32 {
		var oldest string
		for key, expires := range a.sessions {
			if oldest == "" || expires.Before(a.sessions[oldest]) {
				oldest = key
			}
		}
		delete(a.sessions, oldest)
	}
	v := token()
	expires := now.Add(12 * time.Hour)
	a.sessions[digest(v)] = expires
	a.failures = nil
	writeJSON(w, 200, map[string]any{"session_token": v, "expires_at": expires})
}
func (a *App) CloseAdmission() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return
	}
	a.stopping = true
	a.observations.mu.Lock()
	if a.observations.cancel != nil {
		a.observations.cancel()
	}
	a.observations.mu.Unlock()
	a.signalAdmission()
	if a.maintenanceCancel != nil {
		a.maintenanceCancel()
	}
	for _, op := range a.logins {
		op.cancel()
	}
	for _, task := range a.refreshes {
		task.cancel()
	}
	for _, cancel := range a.operationCancels {
		cancel()
	}
	a.drained = make(chan struct{})
	go func() {
		a.ownedTasks.Wait()
		close(a.drained)
	}()
}

// HTTP shutdown alone does not wait for forced-closed handlers or OAuth/refresh
// work. Close storage only after this barrier succeeds. A timeout is not a drain.
func (a *App) WaitOwnedTasks(ctx context.Context) error {
	a.CloseAdmission()
	select {
	case <-a.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *App) CancelAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.running {
		cancel()
	}
}
func (a *App) StartMaintenance(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping || a.maintenanceCancel != nil {
		return
	}
	a.observations.mu.Lock()
	if !a.stagedAdmission && a.observations.settings.Enabled && a.observations.cancel == nil {
		tc, cancel := context.WithCancel(ctx)
		a.observations.cancel = cancel
		a.ownedTasks.Add(1)
		go func() { defer a.ownedTasks.Done(); a.telemetryLoop(tc) }()
	}
	a.observations.mu.Unlock()
	ctx, a.maintenanceCancel = context.WithCancel(ctx)
	a.ownedTasks.Add(1)
	go func() { defer a.ownedTasks.Done(); a.quotaObservationLoop(ctx, &a.quotaObserver) }()
	a.ownedTasks.Add(1)
	go func() { defer a.ownedTasks.Done(); a.notificationsLoop(ctx) }()
	a.ownedTasks.Add(1)
	go func() {
		defer a.ownedTasks.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			a.mu.Lock()
			if a.stagedAdmission {
				a.mu.Unlock()
				continue
			}
			release, err := a.beginBackupOwnerLocked()
			a.mu.Unlock()
			if err != nil {
				continue
			}
			if err := a.pollResourceJobs(ctx); err != nil {
				a.mu.Lock()
				a.maintenanceError = "资源任务轮询失败；请检查本地任务状态"
				a.mu.Unlock()
			}
			release()
		}
	}()
	a.ownedTasks.Add(1)
	go func() {
		defer a.ownedTasks.Done()
		a.maintain(ctx)
	}()
}

func (a *App) maintain(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.mu.Lock()
			if a.stopping {
				a.mu.Unlock()
				return
			}
			if a.stagedAdmission {
				a.mu.Unlock()
				continue
			}
			release, gateErr := a.beginBackupOwnerLocked()
			if gateErr != nil {
				a.mu.Unlock()
				continue
			}
			err := a.Store.cleanup(a.Config.RetentionDays)
			if err != nil {
				a.markStorageFailure()
			}
			a.mu.Unlock()
			if err := a.maintainResponseCache(); err != nil {
				a.mu.Lock()
				a.maintenanceError = err.Error()
				a.mu.Unlock()
			}
			if err := a.cleanupOperationArtifacts(ctx); err != nil {
				a.mu.Lock()
				a.maintenanceError = err.Error()
				a.mu.Unlock()
			}
			release()
		}
	}
}
func readLimited(body io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("response too large")
	}
	return b, nil
}

// A controlled launcher performs switching outside the HTTP process it stops.
func (a *App) SetStagedAdmission(closed bool) {
	a.mu.Lock()
	a.stagedAdmission = closed
	if !closed && a.maintenanceCancel != nil && !a.stopping {
		a.observations.mu.Lock()
		if a.observations.settings.Enabled && a.observations.cancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			a.observations.cancel = cancel
			a.ownedTasks.Add(1)
			go func() { defer a.ownedTasks.Done(); a.telemetryLoop(ctx) }()
		}
		a.observations.mu.Unlock()
	}
	a.signalAdmission()
	a.mu.Unlock()
}
func (a *App) StagedAdmission() bool                              { a.mu.Lock(); defer a.mu.Unlock(); return a.stagedAdmission }
func (a *App) DrainForSwitch(ctx context.Context) (func(), error) { return a.quiesceBackup(ctx) }

type managementErrors struct {
	http.ResponseWriter
	status int
}

func (w *managementErrors) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *managementErrors) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *managementErrors) Write(body []byte) (int, error) {
	if w.status >= 400 {
		var value map[string]any
		if json.Unmarshal(body, &value) == nil {
			if err, ok := value["error"].(map[string]any); ok {
				if _, present := err["code"]; !present {
					err["code"] = "management_error"
				}
				converted := []byte(encode(value) + "\n")
				_, e := w.ResponseWriter.Write(converted)
				if e != nil {
					return 0, e
				}
				return len(body), nil
			}
		}
	}
	return w.ResponseWriter.Write(body)
}
