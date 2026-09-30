package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func notificationFixture(t *testing.T) *App {
	t.Helper()
	a := contractApp(t, nil)
	if _, e := a.Store.DB.Exec(NotificationSchema); e != nil {
		t.Fatal(e)
	}
	return a
}
func notificationRequest(a *App, method, path, body string) *httptest.ResponseRecorder {
	r := contractRequest(method, path, body, "test-session")
	w := httptest.NewRecorder()
	if path == "/admin/alerts" || strings.HasPrefix(path, "/admin/alerts/") {
		a.notificationsAlertsAPI(w, r)
	} else if !a.notificationsAPI(w, r) {
		panic("fixture path")
	}
	return w
}
func notificationSet(t *testing.T, a *App, enabled bool) notificationSettings {
	t.Helper()
	s, e := a.Store.readNotifications(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	w := notificationRequest(a, "PUT", "/admin/notifications", encode(map[string]any{"version": s.Version, "enabled": enabled, "url": "https://notifications.example.test/hook", "signature": true, "secret": "SYNTHETIC_WEBHOOK_SECRET", "headers": map[string]string{"Authorization": "Bearer SYNTHETIC_WEBHOOK_AUTH"}, "event_kinds": notificationKinds}))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, e = a.Store.readNotifications(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func notificationAccountState(t *testing.T, a *App, state string) {
	t.Helper()
	var aid, raw string
	var generation int
	if e := a.Store.DB.QueryRow("SELECT id,generation,data FROM accounts LIMIT 1").Scan(&aid, &generation, &raw); e != nil {
		t.Fatal(e)
	}
	var account Account
	if e := json.Unmarshal([]byte(raw), &account); e != nil {
		t.Fatal(e)
	}
	account.AuthState = state
	if _, e := a.Store.DB.Exec("UPDATE accounts SET data=? WHERE id=?", encode(account), aid); e != nil {
		t.Fatal(e)
	}
}
func notificationCount(t *testing.T, a *App, query string, args ...any) int {
	t.Helper()
	var n int
	if e := a.Store.DB.QueryRow(query, args...).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestSpecNotificationsDedupDismissRecoveryAndGenerations(t *testing.T) {
	a := notificationFixture(t)
	notificationSet(t, a, true)
	notificationAccountState(t, a, "needs_reauth")
	now := time.Now().UTC()
	var alerts []localAlert
	var e error
	for i := 0; i < 100; i++ {
		alerts, e = a.reconcileNotifications(context.Background(), now.Add(time.Duration(i)*time.Second))
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(alerts) != 1 || alerts[0].Count != 100 || notificationCount(t, a, "SELECT count(*) FROM alert_deliveries") != 1 {
		t.Fatal("fault did not deduplicate", alerts)
	}
	fault := alerts[0]
	w := notificationRequest(a, "POST", "/admin/alerts/"+fault.ID+"/dismiss", encode(map[string]any{"version": fault.Version}))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(101*time.Second))
	if e != nil || len(alerts) != 1 || alerts[0].State != "dismissed" {
		t.Fatal("dismiss modified fault", e, alerts)
	}
	stale := notificationRequest(a, "POST", "/admin/alerts/"+fault.ID+"/dismiss", encode(map[string]any{"version": fault.Version}))
	if stale.Code != 409 {
		t.Fatal("stale dismissal accepted")
	}
	notificationAccountState(t, a, "configured")
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(102*time.Second))
	if e != nil || alerts[0].ResolvedAt == nil || alerts[0].State != "resolved" {
		t.Fatal("recovery missing", e, alerts)
	}
	notificationAccountState(t, a, "needs_reauth")
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(103*time.Second))
	if e != nil || len(alerts) != 2 || alerts[0].ID == fault.ID || alerts[0].Period == fault.Period {
		t.Fatal("new fault cycle missing", e, alerts)
	}
	if notificationCount(t, a, "SELECT count(*) FROM alert_deliveries") != 3 {
		t.Fatal("same observation created duplicate event")
	}
	var accountRaw, aid string
	a.Store.DB.QueryRow("SELECT id,data FROM accounts LIMIT 1").Scan(&aid, &accountRaw)
	var acct Account
	json.Unmarshal([]byte(accountRaw), &acct)
	acct.Generation++
	if _, e = a.Store.DB.Exec("UPDATE accounts SET generation=?,data=? WHERE id=?", acct.Generation, encode(acct), aid); e != nil {
		t.Fatal(e)
	}
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(104*time.Second))
	if e != nil || notificationCount(t, a, "SELECT count(*) FROM alerts WHERE resolved_at IS NULL") != 1 || len(alerts) != 3 {
		t.Fatal("generation did not close old identity", e, alerts)
	}
	r := contractRequest("GET", "/admin/alerts?state=resolved", "", "test-session")
	w = httptest.NewRecorder()
	a.notificationsAlertsAPI(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "resolved_at") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSpecNotificationsQuotaBudgetHealthJobUpdateAndUnknown(t *testing.T) {
	a := notificationFixture(t)
	now := time.Now().UTC()
	src, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	src.Quota = map[string]any{"status": "available", "observed_at": now.Add(-time.Minute), "expires_at": now.Add(time.Hour), "windows": []any{map[string]any{"dimension": "requests", "unit": "count", "remaining": 1, "limit": 100, "reset_at": now.Add(time.Hour)}}}
	if e = a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	a.policyState.health = map[policyScope]policyHealth{{Kind: "source", ID: src.ID, Generation: src.Generation}: {Failures: 3, ObservedAt: now, Until: now.Add(time.Minute)}}
	budget := Budget{ID: "notification-budget", Name: "SYNTHETIC_BUDGET_NAME", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "0", Enabled: true, Version: 1, Mode: "soft", Period: BudgetPeriod{Kind: "calendar_day", Timezone: "UTC"}, CreatedAt: now}
	if _, e = a.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES(?,?,?)", budget.ID, "instance", encode(budget)); e != nil {
		t.Fatal(e)
	}
	if e = a.noteNotificationUpdate(context.Background(), 1, true); e != nil {
		t.Fatal(e)
	}
	record := Record{ID: "notification-request", AttemptID: "notification-attempt", Started: now, AttemptStarted: now, SourceID: src.ID, KeyID: "key", Status: "dispatching"}
	if e = a.Store.record(record); e != nil {
		t.Fatal(e)
	}
	account, _, e := a.Store.account(src.AccountID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Store.DB.Exec("INSERT INTO jobs(id,request_id,native_id,kind,key_id,source_id,account_id,source_generation,account_generation,state,created_at,updated_at) VALUES(?,?,'native','background','key',?,?,?,?,?,?,?)", "notification-job", record.ID, src.ID, src.AccountID, src.Generation, account.Generation, "pending_reconciliation", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	a.storageFailed.Store(true)
	alerts, e := a.reconcileNotifications(context.Background(), now)
	if e != nil {
		t.Fatal(e)
	}
	kinds := map[string]bool{}
	for _, al := range alerts {
		kinds[al.Kind] = true
	}
	for _, k := range []string{"quota", "budget", "source_health", "job", "storage", "update"} {
		if !kinds[k] {
			t.Fatal("missing fact", k, alerts)
		}
	}
	if notificationCount(t, a, "SELECT count(*) FROM alert_deliveries") != 0 {
		t.Fatal("default off sent event")
	}
	src.Quota = map[string]any{"status": "unknown"}
	a.Store.saveSource(src)
	a.policyState.health = nil
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(time.Second))
	if e != nil {
		t.Fatal(e)
	}
	for _, al := range alerts {
		if (al.Kind == "quota" || al.Kind == "source_health") && al.ResolvedAt != nil {
			t.Fatal("unknown claimed recovery")
		}
	}
	src.Quota = map[string]any{"status": "available", "observed_at": now, "expires_at": now.Add(time.Hour), "windows": []any{map[string]any{"dimension": "requests", "unit": "count", "used_percent": 20, "reset_at": now.Add(time.Hour)}}}
	a.Store.saveSource(src)
	a.policyState.health = map[policyScope]policyHealth{{Kind: "source", ID: src.ID, Generation: src.Generation}: {ObservedAt: now.Add(2 * time.Second)}}
	a.storageFailed.Store(false)
	if e = a.noteNotificationUpdate(context.Background(), 1, false); e != nil {
		t.Fatal(e)
	}
	if e = a.noteNotificationUpdate(context.Background(), 2, false); e != nil {
		t.Fatal(e)
	}
	if e = a.noteNotificationUpdate(context.Background(), 1, true); e == nil {
		t.Fatal("stale update generation overwrote result")
	}
	a.Store.DB.Exec("UPDATE jobs SET settled_at=? WHERE id='notification-job'", now.Format(time.RFC3339Nano))
	a.Store.DB.Exec("DELETE FROM budgets WHERE id=?", budget.ID)
	alerts, e = a.reconcileNotifications(context.Background(), now.Add(3*time.Second))
	if e != nil {
		t.Fatal(e)
	}
	for _, al := range alerts {
		if al.ResolvedAt == nil {
			t.Fatal("known recovery omitted", al)
		}
	}
	for _, q := range []map[string]any{{"status": "unknown"}, {"status": "available", "observed_at": now, "expires_at": now.Add(-time.Second), "windows": []any{}}, {"status": "available", "observed_at": now, "expires_at": now.Add(time.Hour), "windows": []any{map[string]any{"dimension": "requests", "unit": "count", "used_percent": 101, "reset_at": now.Add(time.Hour)}}}} {
		src.Quota = q
		fs, known := notificationQuotaFacts(src, now)
		if len(fs) != 0 || known {
			t.Fatal("invalid quota inferred amount")
		}
	}
}
func TestSpecNotificationsPersistentRetryHMACPrivacyAndNoModelRetry(t *testing.T) {
	a := notificationFixture(t)
	s := notificationSet(t, a, true)
	now := time.Now().UTC()
	notificationAccountState(t, a, "needs_reauth")
	if _, e := a.reconcileNotifications(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	var payload, event string
	if e := a.Store.DB.QueryRow("SELECT event_id,redacted_payload_json FROM alert_deliveries").Scan(&event, &payload); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(payload, "SYNTHETIC") || strings.Contains(payload, "Contract") || strings.Contains(payload, "upstream") || strings.Contains(payload, "prompt") {
		t.Fatal("payload privacy", payload)
	}
	var calls atomic.Int32
	send := func(ctx context.Context, settings notificationSettings, creds notificationCredentials, body string, at time.Time) (int, string, error) {
		calls.Add(1)
		if body != payload {
			t.Fatal("retry changed exact body")
		}
		req, e := notificationSignedRequest(ctx, settings, creds, body, at)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(req.Body)
		if e != nil {
			t.Fatal(e)
		}
		mac := hmac.New(sha256.New, []byte("SYNTHETIC_WEBHOOK_SECRET"))
		mac.Write([]byte(req.Header.Get("X-Cove-Timestamp") + "."))
		mac.Write(raw)
		if req.Header.Get("X-Cove-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || req.Header.Get("X-Cove-Event-ID") != event || req.GetBody != nil {
			t.Fatal("bad signature or replay body")
		}
		return 429, "9999", errors.New("SYNTHETIC_ERROR_CONTAINING_SECRET")
	}
	if e := a.deliverNotificationQueue(context.Background(), now, send); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry in same tick")
	}
	var state, next, errJSON string
	var attempts int
	a.Store.DB.QueryRow("SELECT state,attempt_count,next_attempt_at,last_error_json FROM alert_deliveries").Scan(&state, &attempts, &next, &errJSON)
	due, e := time.Parse(time.RFC3339Nano, next)
	if e != nil || due.Sub(now) != 5*time.Minute || attempts != 1 || state != "pending" || strings.Contains(errJSON, "SYNTHETIC") {
		t.Fatal(state, attempts, next, errJSON)
	}
	// Construct a new App over the same database and vault: pending SQL, not an
	// in-memory queue, is the restart boundary. No real network transport is used.
	restarted := &App{Store: a.Store, Secrets: a.Secrets, running: map[string]context.CancelFunc{}, backupChanged: make(chan struct{})}
	if e = restarted.deliverNotificationQueue(context.Background(), now.Add(5*time.Minute), send); e != nil {
		t.Fatal(e)
	}
	if e = restarted.deliverNotificationQueue(context.Background(), now.Add(10*time.Minute), send); e != nil {
		t.Fatal(e)
	}
	a.Store.DB.QueryRow("SELECT state,attempt_count,last_error_json FROM alert_deliveries").Scan(&state, &attempts, &errJSON)
	if state != "delivery_failed" || attempts != 3 || calls.Load() != 3 {
		t.Fatal(state, attempts, calls.Load())
	}
	if notificationCount(t, a, "SELECT count(*) FROM requests") != 0 {
		t.Fatal("notification retried inference")
	}
	ref, e := notificationRequiredSecret(a.Store.DB)
	if e != nil || ref != s.HeaderRef {
		t.Fatal("full backup secret closure", e, ref)
	}
	for _, key := range []string{"webhook"} {
		var raw string
		a.Store.DB.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
		if strings.Contains(raw, "SYNTHETIC") {
			t.Fatal("secret persisted in settings")
		}
	}
	rows, e := a.Store.DB.Query("SELECT value FROM settings WHERE key LIKE 'notification_audit_%'")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		if strings.Contains(raw, "SYNTHETIC") || strings.Contains(raw, "example.test") || strings.Contains(raw, "header_ref") {
			t.Fatal("audit privacy")
		}
	}
}
func TestSpecNotificationsQueueBoundCASRestartExpiryAndDisable(t *testing.T) {
	a := notificationFixture(t)
	s := notificationSet(t, a, true)
	now := time.Now().UTC()
	f := notificationFact{localAlert: localAlert{ID: "alert-bound", Kind: "storage", EntityID: "local", State: "active"}, EntityKind: "instance"}
	a.Store.DB.Exec("INSERT INTO alerts(id,dedup_key,kind,entity_kind,entity_id,severity,first_seen_at,last_seen_at,redacted_detail_json) VALUES('alert-bound','bound','storage','instance','local','warning',?,?,'{}')", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	tx, e := a.Store.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 300; i++ {
		if e = notificationEnqueue(tx, s, f, "healthy", now); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if notificationCount(t, a, "SELECT count(*) FROM alert_deliveries WHERE state='pending'") != notificationQueueLimit || notificationCount(t, a, "SELECT count(*) FROM alert_deliveries WHERE state='delivery_failed'") != 44 {
		t.Fatal("queue unbounded")
	}
	var idsMu sync.Mutex
	seen := map[string]int{}
	sender := func(ctx context.Context, s notificationSettings, c notificationCredentials, payload string, at time.Time) (int, string, error) {
		var p notificationPayload
		json.Unmarshal([]byte(payload), &p)
		idsMu.Lock()
		seen[p.EventID]++
		idsMu.Unlock()
		return 204, "", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := a.deliverNotificationQueue(context.Background(), now, sender); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	for _, n := range seen {
		if n != 1 {
			t.Fatal("CAS sent same event concurrently")
		}
	}
	if _, e = a.Store.DB.Exec("UPDATE alert_deliveries SET expires_at=? WHERE state='pending'", now.Add(-time.Second).Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	if e = a.deliverNotificationQueue(context.Background(), now, sender); e != nil {
		t.Fatal(e)
	}
	if notificationCount(t, a, "SELECT count(*) FROM alert_deliveries WHERE state IN ('pending','sending')") != 0 {
		t.Fatal("expired queue not terminated")
	}
	notificationSet(t, a, false)
	if e = a.deliverNotificationQueue(context.Background(), now, func(context.Context, notificationSettings, notificationCredentials, string, time.Time) (int, string, error) {
		t.Fatal("disabled performed send")
		return 0, "", nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestSpecNotificationsSettingsAtomicFailureAndExplicitSample(t *testing.T) {
	a := notificationFixture(t)
	w := notificationRequest(a, "GET", "/admin/notifications", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, raw := range []string{`{"version":1,"enabled":true,"url":"https://127.0.0.1/hook","event_kinds":["auth"]}`, `{"version":1,"enabled":true,"url":"https://example.test/hook","signature":true,"event_kinds":["auth"]}`, `{"version":1,"enabled":true,"url":"https://example.test/hook","event_kinds":["invented"]}`, `{"version":1,"enabled":false,"url":"https://example.test/hook","event_kinds":["auth"],"headers":{"Host":"private"}}`} {
		w = notificationRequest(a, "PUT", "/admin/notifications", raw)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s := notificationSet(t, a, true)
	ref := s.HeaderRef
	if _, e := a.Store.DB.Exec("CREATE TRIGGER reject_notification_audit BEFORE INSERT ON settings WHEN NEW.key LIKE 'notification_audit_%' BEGIN SELECT RAISE(ABORT,'synthetic'); END"); e != nil {
		t.Fatal(e)
	}
	w = notificationRequest(a, "PUT", "/admin/notifications", encode(map[string]any{"version": s.Version, "enabled": false, "url": s.URL, "signature": true, "event_kinds": notificationKinds, "secret": "SYNTHETIC_REPLACEMENT"}))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, e := a.Store.readNotifications(context.Background())
	if e != nil || after.Version != s.Version || after.HeaderRef != ref || !after.Enabled {
		t.Fatal("failed publish changed settings")
	}
	vault := a.Secrets.(*memorySecrets)
	vault.mu.Lock()
	for k, v := range vault.values {
		if k != ref && strings.Contains(v, "SYNTHETIC_REPLACEMENT") {
			t.Fatal("failed publish leaked new private secret")
		}
	}
	vault.mu.Unlock()
	a.Store.DB.Exec("DROP TRIGGER reject_notification_audit")
	w = notificationRequest(a, "POST", "/admin/notifications/preview", encode(map[string]any{"version": s.Version, "confirm_external_send": false}))
	if w.Code != 422 {
		t.Fatal("implicit sample send accepted")
	}
	w = notificationRequest(a, "POST", "/admin/notifications/preview", encode(map[string]any{"version": s.Version - 1, "confirm_external_send": true}))
	if w.Code != 409 {
		t.Fatal("sample stale version accepted")
	}
	// A malicious URL already stored by an older process is still validated at
	// delivery. Sample is explicit, synthetic, one attempt, no inference traffic.
	s.URL = "https://169.254.169.254/hook"
	a.Store.DB.Exec("UPDATE settings SET value=? WHERE key='webhook'", encode(notificationStored{s, s.HeaderRef}))
	w = notificationRequest(a, "POST", "/admin/notifications/preview", encode(map[string]any{"version": s.Version, "confirm_external_send": true}))
	if w.Code != 502 || strings.Contains(w.Body.String(), "169.254") {
		t.Fatal(w.Code, w.Body.String())
	}
	if notificationCount(t, a, "SELECT count(*) FROM requests") != 0 {
		t.Fatal("sample sent model")
	}
	for _, path := range []string{"/admin/notifications", "/admin/notifications/preview"} {
		req := contractRequest("POST", path, `{}`, "wrong-session")
		w = httptest.NewRecorder()
		a.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal("admin boundary", w.Code)
		}
	}
}
func TestSpecNotificationsSSRFPinningAndRedirect(t *testing.T) {
	for _, raw := range []string{"http://example.test/hook", "https://user:secret@example.test/hook", "https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/", "https://10.1.1.1/", "https://example.test/hook?secret=synthetic", "https://:443/"} {
		if _, e := notificationURL(raw); e == nil {
			t.Fatal("unsafe URL accepted", raw)
		}
	}
	for _, ip := range []string{"0.0.0.0", "100.100.100.200", "192.168.1.1", "198.18.1.1", "::ffff:127.0.0.1", "fd00::1", "fe80::1", "64:ff9b::7f00:1"} {
		if notificationPublicIP(netip.MustParseAddr(ip)) {
			t.Fatal("unsafe IP accepted", ip)
		}
	}
	u, _ := url.Parse("https://notifications.example.test/hook")
	var dialCalls atomic.Int32
	_, e := notificationPinnedClient(context.Background(), u, func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errors.New("unexpected")
	})
	if e == nil || dialCalls.Load() != 0 {
		t.Fatal("mixed DNS private record permitted")
	}
	var gotHost string
	var serverCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls.Add(1)
		gotHost = r.Host
		w.Header().Set("Location", "https://127.0.0.1/private")
		w.WriteHeader(302)
	}))
	defer server.Close()
	serverAddress := strings.TrimPrefix(server.URL, "https://")
	lookups := 0
	client, e := notificationPinnedClient(context.Background(), u, func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCalls.Add(1)
		if address != "8.8.8.8:443" {
			t.Error("DNS name was re-resolved", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	})
	if e != nil {
		t.Fatal(e)
	}
	// The production TLS policy remains verified; only this synthetic fixture
	// trusts the fixture certificate while retaining hostname pinning assertions.
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test fixture only
	defer client.CloseIdleConnections()
	resp, e := client.Post(u.String(), "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || lookups != 1 || serverCalls.Load() != 1 || gotHost != u.Host {
		t.Fatal("redirect followed or hostname changed")
	}
	if notificationRetryAfter("9999", time.Now()) != 5*time.Minute || notificationRetryAfter("-1", time.Now()) != 0 {
		t.Fatal("unbounded Retry-After")
	}
}
func TestSpecNotificationsOwnedTickGateAndCancellation(t *testing.T) {
	a := notificationFixture(t)
	notificationAccountState(t, a, "needs_reauth")
	if e := a.notificationsTick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if notificationCount(t, a, "SELECT count(*) FROM alerts") != 1 {
		t.Fatal("periodic alert missing")
	}
	a.mu.Lock()
	a.stagedAdmission = true
	a.mu.Unlock()
	if e := a.notificationsTick(context.Background()); e == nil {
		t.Fatal("staged restore began notification work")
	}
	a.mu.Lock()
	a.stagedAdmission = false
	a.mu.Unlock()
	a.mu.Lock()
	owners := a.backupOwners
	running := len(a.running)
	a.backupQuiescing = true
	a.mu.Unlock()
	if owners != 0 || running != 0 {
		t.Fatal("tick leaked owner")
	}
	if e := a.notificationsTick(context.Background()); e == nil {
		t.Fatal("quiescing did not reject tick")
	}
	a.mu.Lock()
	a.backupQuiescing = false
	a.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := a.notificationsTick(ctx); e == nil {
		t.Fatal("cancelled tick wrote success")
	}
	a.mu.Lock()
	if a.backupOwners != 0 || len(a.running) != 0 {
		t.Fatal("cancel leaked owner")
	}
	a.mu.Unlock()
}
func TestSpecNotificationsDeliveryRecoveryAndPermanentRedirect(t *testing.T) {
	a := notificationFixture(t)
	notificationSet(t, a, true)
	now := time.Now().UTC()
	notificationAccountState(t, a, "needs_reauth")
	if _, e := a.reconcileNotifications(context.Background(), now); e != nil {
		t.Fatal(e)
	}
	// One network attempt was charged before the previous process stopped.
	if _, e := a.Store.DB.Exec("UPDATE alert_deliveries SET state='sending',attempt_count=1,next_attempt_at=?", now.Add(-time.Second).Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	var calls int
	if e := a.deliverNotificationQueue(context.Background(), now, func(context.Context, notificationSettings, notificationCredentials, string, time.Time) (int, string, error) {
		calls++
		return 302, "", nil
	}); e != nil {
		t.Fatal(e)
	}
	var state, raw string
	var count int
	if e := a.Store.DB.QueryRow("SELECT state,attempt_count,last_error_json FROM alert_deliveries").Scan(&state, &count, &raw); e != nil {
		t.Fatal(e)
	}
	if calls != 1 || count != 2 || state != "delivery_failed" || !strings.Contains(raw, "redirect_blocked") {
		t.Fatal("redirect retried", state, count, raw)
	}
}
