package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real handler, SQLite and stream observer without a TCP listener.
// Socket behavior remains covered by the separate HTTP integration suite.
type contractTransport func(*http.Request) (*http.Response, error)

func (f contractTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func contractApp(t *testing.T, upstream contractTransport) *App {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := &memorySecrets{values: map[string]string{"administrator": "test-administrator", "source": "test-source-secret"}}
	c := Config{Listen: "127.0.0.1:5569", MaxBody: 8192, MaxResponse: 16384, MaxEvent: 1024, MaxConcurrent: 1, HeaderTimeout: 2, IdleTimeout: 1, TotalTimeout: 5, RetentionDays: 7, Codex: CodexConfig{AuthBaseURL: "http://upstream.invalid", ClientID: "synthetic-client"}}
	a, err := New(c, s, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		if response := syntheticIdentityResponse(r); response != nil {
			return response, nil
		}
		if upstream == nil {
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			return nil, errors.New("unexpected upstream request")
		}
		return upstream(r)
	})
	src := Source{ID: "source", Name: "Contract source", Kind: "api_key", BaseURL: "http://upstream.invalid", Enabled: true, Version: 1, Generation: 1, CredentialRef: "source", Configured: true, AuthStatus: "configured", Models: []string{"fixture-model"}, Verification: Verification{Status: "untested", Capabilities: []string{}}, Quota: map[string]any{"status": "unknown"}}
	if err = s.saveSource(src); err != nil {
		t.Fatal(err)
	}
	k := ClientKey{ID: "key", Name: "Contract client", SourceID: src.ID}
	if _, err = s.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", k.ID, digest("test-client-key"), src.ID, encode(k)); err != nil {
		t.Fatal(err)
	}
	a.sessions[digest("test-session")] = time.Now().Add(time.Hour)
	t.Cleanup(func() {
		a.CloseAdmission()
		a.CancelAll()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.WaitOwnedTasks(ctx); err != nil {
			t.Errorf("owned tasks did not stop: %v", err)
			return
		}
		_ = s.DB.Close()
	})
	return a
}

func contractRequest(method, path, body, bearerToken string) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1:5569"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:5569")
	r.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		r.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return r
}

func contractCall(a *App, method, path, body, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.ServeHTTP(w, contractRequest(method, path, body, token))
	return w
}

const contractJSON = `{"id":"response-contract","status":"completed","model":"fixture-model","usage":{"input_tokens":12,"output_tokens":3}}`
const contractBody = `{"model":"fixture-model","input":"test","stream":false}`
const contractStream = `{"model":"fixture-model","input":"test","stream":true}`

func contractResponse(body, kind string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestContractManagementBearerIsolation(t *testing.T) {
	a := contractApp(t, nil)
	r := contractRequest("POST", "/admin/browser-tickets", "", "test-administrator")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("browser-origin native ticket was accepted")
	}
	r.Header.Del("Origin")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &ticket) != nil {
		t.Fatal("native ticket failed")
	}
	w = contractCall(a, "POST", "/admin/session", encode(map[string]string{"ticket": ticket.Ticket}), "")
	var session struct {
		Token string `json:"session_token"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Token == "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("explicit session contract failed")
	}
	if contractCall(a, "POST", "/admin/session", encode(map[string]string{"ticket": ticket.Ticket}), "").Code != 401 {
		t.Fatal("ticket replay accepted")
	}
	for _, token := range []string{"", "test-administrator", "test-client-key"} {
		if contractCall(a, "GET", "/admin/status", "", token).Code != 401 {
			t.Fatal("wrong identity accepted by admin")
		}
	}
	r = contractRequest("GET", "/admin/status", "", "")
	r.AddCookie(&http.Cookie{Name: "gatt_session", Value: session.Token})
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambient cookie authenticated")
	}
	r = contractRequest("GET", "/admin/status", "", session.Token)
	r.Header.Set("Origin", "http://127.0.0.1:5570")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("other-port origin accepted")
	}
	if contractCall(a, "GET", "/admin/status", "", session.Token).Code != 200 {
		t.Fatal("session rejected")
	}
	if contractCall(a, "POST", "/v1/responses", contractBody, session.Token).Code != 401 {
		t.Fatal("admin session accepted as model key")
	}
	if contractCall(a, "DELETE", "/admin/session", "", session.Token).Code != 200 {
		t.Fatal("logout failed")
	}
	if contractCall(a, "GET", "/admin/status", "", session.Token).Code != 401 {
		t.Fatal("logged-out session survived")
	}
}

type contractHeldBody struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	reads   atomic.Int32
}

func (b *contractHeldBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	b.once.Do(func() { close(b.entered); <-b.release })
	return 0, io.EOF
}
func (b *contractHeldBody) Close() error { return nil }

func TestContractAdmissionBeforeBody(t *testing.T) {
	var dispatches atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		dispatches.Add(1)
		return contractResponse(contractJSON, "application/json"), nil
	})
	hold := &contractHeldBody{entered: make(chan struct{}), release: make(chan struct{})}
	r := contractRequest("POST", "/v1/responses", "", "test-client-key")
	r.Body = hold
	done := make(chan struct{})
	go func() { defer close(done); a.ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-hold.entered:
	case <-time.After(time.Second):
		t.Fatal("first upload not admitted")
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"wrong-key", 401}, {"test-client-key", 429}} {
		body := &contractHeldBody{}
		r := contractRequest("POST", "/v1/responses", "", tc.token)
		r.Body = body
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.status || body.reads.Load() != 0 {
			t.Fatal("read body before auth/capacity rejection")
		}
	}
	close(hold.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upload did not release slot")
	}
	if len(a.slots) != 0 || dispatches.Load() != 0 {
		t.Fatal("invalid upload leaked slot or dispatched")
	}
	if contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key").Code != 200 || dispatches.Load() != 1 {
		t.Fatal("slot not reusable")
	}
}

func TestContractOversizeTerminalIsUnverified(t *testing.T) {
	wire := "event: response.completed\ndata: " + strings.TrimSuffix(contractJSON, "}") + `,"padding":"` + strings.Repeat("x", 4096) + "\"}\n\n"
	a := contractApp(t, func(r *http.Request) (*http.Response, error) { return contractResponse(wire, "text/event-stream"), nil })
	w := contractCall(a, "POST", "/v1/responses", contractStream, "test-client-key")
	if w.Code != 200 || w.Body.String() != wire {
		t.Fatal("oversize bytes changed")
	}
	rows := waitRecords(t, a, 1)
	r := rows[0]
	if r.Status != "unverified" || r.UpstreamStatus != "unknown" || r.DeliveryStatus != "completed" || r.ObservationStatus != "partial" || r.Usage.Input != nil || r.Cost != nil {
		t.Fatalf("unobserved terminal claimed a known outcome: %+v", r)
	}
}

type contractFailedWriter struct{ *httptest.ResponseRecorder }

func (w contractFailedWriter) Write(b []byte) (int, error) { return 0, io.ErrClosedPipe }

func TestContractDeliveryFailureKeepsUpstreamUsage(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(encode(streaming), func(t *testing.T) {
			wire, kind, body := contractJSON, "application/json", contractBody
			if streaming {
				wire, kind, body = "event: response.completed\ndata: "+contractJSON+"\n\n", "text/event-stream", contractStream
			}
			a := contractApp(t, func(r *http.Request) (*http.Response, error) { return contractResponse(wire, kind), nil })
			func() {
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				a.ServeHTTP(contractFailedWriter{httptest.NewRecorder()}, contractRequest("POST", "/v1/responses", body, "test-client-key"))
			}()
			r := waitRecords(t, a, 1)[0]
			if r.Status != "failed" || r.UpstreamStatus != "completed" || r.DeliveryStatus != "failed" || r.ErrorStage != "downstream_write" || r.Usage.Input == nil || *r.Usage.Input != 12 {
				t.Fatalf("upstream result lost or delivery claimed success: %+v", r)
			}
		})
	}
}

type contractSlowWriter struct {
	*httptest.ResponseRecorder
	once sync.Once
}

func (w *contractSlowWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { time.Sleep(1200 * time.Millisecond) })
	return w.ResponseRecorder.Write(b)
}

func TestContractBackpressureNotUpstreamIdle(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse("event: response.completed\ndata: "+contractJSON+"\n\n", "text/event-stream"), nil
	})
	w := &contractSlowWriter{ResponseRecorder: httptest.NewRecorder()}
	a.ServeHTTP(w, contractRequest("POST", "/v1/responses", contractStream, "test-client-key"))
	r := waitRecords(t, a, 1)[0]
	if r.Status != "succeeded" || r.ErrorStage != "" {
		t.Fatalf("downstream wait triggered upstream idle: %+v", r)
	}
}

func TestContractOldResultDoesNotVerifyChangedSource(t *testing.T) {
	for _, change := range []string{"generation", "version"} {
		t.Run(change, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				close(started)
				<-release
				return contractResponse(contractJSON, "application/json"), nil
			})
			done := make(chan struct{})
			go func() { defer close(done); contractCall(a, "POST", "/admin/sources/source/test", `{}`, "test-session") }()
			<-started
			a.mu.Lock()
			s, err := a.Store.source("source")
			if err == nil {
				if change == "generation" {
					s.Generation++
				} else {
					s.Version++
				}
				err = a.Store.saveSource(s)
			}
			a.mu.Unlock()
			close(release)
			<-done
			if err != nil {
				t.Fatal(err)
			}
			s, err = a.Store.source("source")
			if err != nil || s.Verification.Status != "untested" || s.Quota["call_health"] != nil {
				t.Fatal("stale result changed current source")
			}
			if waitRecords(t, a, 1)[0].Status != "succeeded" {
				t.Fatal("historical result not retained")
			}
		})
	}
}

func TestContractPostBodiesNotReplayable(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "POST" || r.GetBody != nil {
			t.Error("model/token POST is replayable")
		}
		return nil, errors.New("synthetic ambiguous connection failure")
	})
	if contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key").Code != 502 {
		t.Fatal("model error changed")
	}
	a.Config.Codex.AuthBaseURL = "http://upstream.invalid"
	_, _ = a.exchange(context.Background(), map[string][]string{"grant_type": {"refresh_token"}})
	if calls.Load() != 2 {
		t.Fatal("ambiguous model/token POST was retried")
	}
}

func TestContractSourceTestPreservesRejection(t *testing.T) {
	a := contractApp(t, nil)
	w := contractCall(a, "POST", "/admin/sources/source/test", `{"model":"unsupported"}`, "test-session")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "request_id") {
		t.Fatal("admin test hid original rejection")
	}
}

func TestContractLogoutDurableBeforeCleanup(t *testing.T) {
	a := contractApp(t, nil)
	s, _ := a.Store.source("source")
	s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
	if err := a.Store.saveSource(s); err != nil {
		t.Fatal(err)
	}
	a.Secrets.(*memorySecrets).failDelete = true
	w := contractCall(a, "POST", "/admin/sources/source/logout", `{}`, "test-session")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "cleanup_warning") {
		t.Fatal("local logout failed")
	}
	if _, err := a.Secrets.Get("source"); err != nil {
		t.Fatal("cleanup failure not injected")
	}
	// Reopen the real database while the orphaned secret remains present.
	var file string
	if err := a.Store.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(file)
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	s, err = reopened.source("source")
	if err != nil || s.CredentialRef != "" || s.Configured || s.AuthStatus != "logged_out" {
		t.Fatal("residual secret resurrected source")
	}
}

func expiredContractSource(t *testing.T, a *App) Source {
	t.Helper()
	s, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
	a.Config.Codex.AuthBaseURL = "http://upstream.invalid"
	if err := a.Store.saveSource(s); err != nil {
		t.Fatal(err)
	}
	c := Credential{Access: "expired", Refresh: "refresh-old", Account: "account", Subject: "subject", Expires: time.Now().Add(-time.Hour)}
	if err := a.Secrets.Put(s.CredentialRef, encode(c)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContractSharedRefreshSurvivesWaiterCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var refreshes atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		refreshes.Add(1)
		close(started)
		<-release
		if r.Context().Err() != nil {
			t.Error("request cancellation killed shared refresh")
		}
		return contractResponse(encode(map[string]any{"access_token": "fresh", "refresh_token": "rotated", "id_token": fakeJWT("account", "subject"), "expires_in": 3600}), "application/json"), nil
	})
	s := expiredContractSource(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		a.mu.Lock()
		copy := s
		_, err := a.subscriptionCredential(ctx, &copy)
		a.mu.Unlock()
		first <- err
	}()
	<-started
	// A management call must complete while the token endpoint is still stalled.
	status := make(chan int, 1)
	go func() { status <- contractCall(a, "GET", "/admin/status", "", "test-session").Code }()
	select {
	case code := <-status:
		if code != 200 {
			t.Fatal("management failed")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh blocked the global lifecycle lock")
	}
	second := make(chan error, 1)
	go func() {
		a.mu.Lock()
		copy, _ := a.Store.source(s.ID)
		c, err := a.subscriptionCredential(context.Background(), &copy)
		a.mu.Unlock()
		if err == nil && c.Access != "fresh" {
			err = errors.New("wrong credential")
		}
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled waiter did not return")
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	current, _ := a.Store.source(s.ID)
	if refreshes.Load() != 1 || current.Generation != s.Generation || current.AuthStatus != "logged_in" {
		t.Fatal("refresh duplicated or changed identity generation")
	}
}

func TestContractLogoutRejectsLateRefresh(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release // Simulate a transport returning after cancellation.
		return contractResponse(encode(map[string]any{"access_token": "late", "refresh_token": "late-rotated", "id_token": fakeJWT("account", "subject"), "expires_in": 3600}), "application/json"), nil
	})
	s := expiredContractSource(t, a)
	done := make(chan error, 1)
	go func() {
		a.mu.Lock()
		copy := s
		_, err := a.subscriptionCredential(context.Background(), &copy)
		a.mu.Unlock()
		done <- err
	}()
	<-started
	if w := contractCall(a, "POST", "/admin/sources/source/logout", `{}`, "test-session"); w.Code != 200 {
		t.Fatal("logout blocked by refresh")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late refresh published")
	}
	current, _ := a.Store.source(s.ID)
	if current.AuthStatus != "logged_out" || current.CredentialRef != "" {
		t.Fatal("logout resurrected")
	}
}

type contractBlockedWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func (w *contractBlockedWriter) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.unblock
	return 0, io.ErrClosedPipe
}
func (w *contractBlockedWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.unblock) })
	}
	return nil
}

func TestContractCancellationWakesBlockedWriter(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				return contractResponse("event: response.completed\ndata: "+compatFinal+"\n\n", "text/event-stream"), nil
			})
			w := &contractBlockedWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), unblock: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := contractStream
			if path != "/v1/responses" {
				body = `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"stream":true,"max_tokens":128}`
			}
			r := contractRequest("POST", path, body, "test-client-key").WithContext(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				a.ServeHTTP(w, r)
			}()
			<-w.entered
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancel did not wake downstream writer")
			}
			record := waitRecords(t, a, 1)[0]
			if record.Status != "cancelled" || record.UpstreamStatus != "completed" || record.Usage.Input == nil || len(a.slots) != 0 {
				t.Fatal("cancel lost upstream evidence or leaked capacity")
			}
		})
	}
}

func TestContractAccountChangeConfirmation(t *testing.T) {
	for _, outcome := range []string{"confirm", "cancel", "source_changed", "same_identity"} {
		t.Run(outcome, func(t *testing.T) {
			account := "new-account"
			if outcome == "same_identity" {
				account = "old-account"
			}
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				return contractResponse(encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "id_token": fakeJWT(account, "subject", "http://upstream.invalid", "test-nonce"), "expires_in": 3600}), "application/json"), nil
			})
			s, _ := a.Store.source("source")
			s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
			if err := a.Store.saveSource(s); err != nil {
				t.Fatal(err)
			}
			if err := a.Secrets.Put(s.CredentialRef, encode(Credential{Account: "old-account", Subject: "subject", Access: "old-access"})); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			op := &Login{ID: "test-operation", Status: "pending", Expires: time.Now().Add(time.Minute), version: s.Version, generation: s.Generation, state: "test-state", verifier: "test-verifier", nonce: "test-nonce", cancel: cancel}
			canonical, _ := a.Store.source(s.ID)
			op.accountGeneration = canonical.AccountGeneration
			a.logins[canonical.AccountID] = op
			if outcome == "source_changed" {
				changed := s
				changed.Version++
				if err := a.Store.saveSource(changed); err != nil {
					t.Fatal(err)
				}
			}
			callback, _ := url.Parse("http://localhost:1455/auth/callback")
			w := httptest.NewRecorder()
			a.oauthCallback(w, httptest.NewRequest("GET", callback.String()+"?state=test-state&code=test-code", nil), ctx, s.ID, op, callback)
			if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "test-code") {
				t.Fatal("callback leaked identity or code")
			}
			current, _ := a.Store.source(s.ID)
			if outcome == "source_changed" {
				if op.Status != "failed" || current.CredentialRef != s.CredentialRef {
					t.Fatal("stale login published")
				}
				return
			}
			if outcome == "same_identity" {
				if op.Status != "succeeded" || current.CredentialRef == s.CredentialRef {
					t.Fatal("same identity did not publish")
				}
				return
			}
			if op.Status != "awaiting_confirmation" || current.CredentialRef != s.CredentialRef || strings.Contains(encode(op), "new-access") {
				t.Fatal("different identity published or leaked before confirmation")
			}
			if outcome == "cancel" {
				contractCall(a, "DELETE", "/admin/sources/source/login", "", "test-session")
				current, _ = a.Store.source(s.ID)
				if op.pending != nil || current.CredentialRef != s.CredentialRef {
					t.Fatal("cancellation lost old identity")
				}
				return
			}
			body := encode(map[string]any{"version": s.Version + 1, "operation_id": op.ID})
			if contractCall(a, "POST", "/admin/sources/source/login/confirm", body, "test-session").Code != 409 {
				t.Fatal("wrong-version confirmation accepted")
			}
			body = encode(map[string]any{"version": s.Version, "operation_id": op.ID})
			if contractCall(a, "POST", "/admin/sources/source/login/confirm", body, "test-session").Code != 200 {
				t.Fatal("confirmation failed")
			}
			current, _ = a.Store.source(s.ID)
			if current.CredentialRef == s.CredentialRef || current.Generation != s.Generation+1 || current.AuthStatus != "logged_in" || op.pending != nil {
				t.Fatal("confirmation not committed")
			}
			if contractCall(a, "POST", "/admin/sources/source/login/confirm", body, "test-session").Code != 409 {
				t.Fatal("confirmation replayed")
			}
		})
	}
}

func TestContractSourceRules(t *testing.T) {
	t.Run("native_opaque_body", func(t *testing.T) {
		body := `{"model":"fixture-model","input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"call","namespace":"ns","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call","output":"result"}],"future_optional":"preserve"}`
		a := contractApp(t, func(r *http.Request) (*http.Response, error) {
			got, _ := io.ReadAll(r.Body)
			if string(got) != body || r.GetBody != nil || r.Header.Get("Authorization") != "Bearer test-source-secret" {
				t.Error("native payload/auth/no-replay changed")
			}
			return contractResponse(contractJSON, "application/json"), nil
		})
		src, _ := a.Store.source("source")
		src.NativeOperations = []string{"compact"}
		if e := a.Store.saveSource(src); e != nil {
			t.Fatal(e)
		}
		seedNativeOpaque(t, a, "source", "key", "fixture-model", "opaque")
		if w := compatCall(a, "/v1/responses", body, "test-client-key"); w.Code != 200 || w.Body.String() != contractJSON {
			t.Fatal("native response changed")
		}
	})
	for _, tc := range []struct {
		name, kind, contentType, prefix string
		want                            int
	}{
		{"subscription_no_header", "codex_subscription", "", "", 200},
		{"subscription_heartbeat", "codex_subscription", "", ": ping\n\n", 200},
		{"api_requires_header", "api_key", "", "", 502},
		{"subscription_explicit_invalid", "codex_subscription", "text/html", "", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.prefix + "event: response.completed\ndata: {\"response\":" + contractJSON + "}\n\n"
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				if tc.kind == "codex_subscription" && (r.Header.Get("Chatgpt-Account-Id") != "account" || r.Header.Get("Originator") != "codex_cli_rs") {
					t.Error("subscription identity headers missing")
				}
				return contractResponse(wire, tc.contentType), nil
			})
			if tc.kind == "codex_subscription" {
				s, _ := a.Store.source("source")
				s.Kind, s.AuthStatus = tc.kind, "logged_in"
				if err := a.Store.saveSource(s); err != nil {
					t.Fatal(err)
				}
				if err := a.Secrets.Put(s.CredentialRef, encode(Credential{Access: "synthetic", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
					t.Fatal(err)
				}
			}
			w := compatCall(a, "/v1/responses", contractStream, "test-client-key")
			if w.Code != tc.want || tc.want == 200 && w.Body.String() != wire {
				t.Fatal("source SSE classification or native bytes changed")
			}
		})
	}
}
