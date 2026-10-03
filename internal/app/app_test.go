package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memorySecrets struct {
	mu         sync.Mutex
	values     map[string]string
	failPut    bool
	failDelete bool
}

func (v *memorySecrets) Health() error { return nil }

func (v *memorySecrets) Get(k string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.values[k]
	if !ok {
		return "", errors.New("missing credential")
	}
	return s, nil
}
func (v *memorySecrets) Put(k, s string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failPut {
		return errors.New("vault unavailable")
	}
	v.values[k] = s
	return nil
}
func (v *memorySecrets) Delete(k string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.failDelete {
		return errors.New("cleanup failed")
	}
	delete(v.values, k)
	return nil
}

type fixture struct {
	a       *App
	server  *httptest.Server
	vault   *memorySecrets
	source  Source
	key     string
	session string
	t       *testing.T
}

func newFixture(t *testing.T, handler http.HandlerFunc) *fixture {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if response := syntheticIdentityResponse(r); response != nil {
			defer response.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.Copy(w, response.Body)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(up.Close)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := &memorySecrets{values: map[string]string{"administrator": "test-administrator", "source": "SENTINEL_UPSTREAM_SECRET"}}
	server := httptest.NewUnstartedServer(nil)
	c := Config{Listen: server.Listener.Addr().String(), DataDir: dir, MaxBody: 8 << 20, MaxResponse: 16 << 20, MaxEvent: 1 << 20, MaxConcurrent: 8, HeaderTimeout: 2, IdleTimeout: 2, TotalTimeout: 10, RetentionDays: 7, AllowPaidFallback: true, SubscriptionQuotaThreshold: 5, Codex: CodexConfig{BaseURL: up.URL, AuthBaseURL: up.URL, ClientID: "synthetic-client", ClientVersion: "test"}}
	a, err := New(c, store, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = a
	server.Start()
	t.Cleanup(func() {
		a.CloseAdmission()
		a.CancelAll()
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.WaitOwnedTasks(ctx); err != nil {
			t.Errorf("owned tasks did not stop: %v", err)
			return
		}
		_ = store.DB.Close()
	})
	src := Source{ID: "source", Name: "Synthetic source", Kind: "api_key", BaseURL: up.URL, Enabled: true, Version: 1, Generation: 1, CredentialRef: "source", Configured: true, AuthStatus: "configured", Models: []string{"fixture-model"}, Verification: Verification{Status: "untested", Capabilities: []string{}}, Quota: map[string]any{"status": "unknown"}, Continuation: true}
	if err = store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	key := ClientKey{ID: "key", Name: "Fixture client", Fingerprint: "fake-fingerprint", SourceID: src.ID}
	if _, err = store.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", key.ID, digest("SENTINEL_DOWNSTREAM_KEY"), src.ID, encode(key)); err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: a, server: server, vault: v, source: src, key: "SENTINEL_DOWNSTREAM_KEY", t: t}
	request, _ := http.NewRequest("POST", server.URL+"/admin/browser-tickets", nil)
	request.Header.Set("Authorization", "Bearer test-administrator")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if json.NewDecoder(resp.Body).Decode(&ticket) != nil || resp.StatusCode != 201 {
		t.Fatal("ticket creation failed")
	}
	resp.Body.Close()
	resp, b := f.request("POST", "/admin/session", encode(map[string]string{"ticket": ticket.Ticket}), false)
	var session struct {
		Token string `json:"session_token"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(b, &session) != nil || session.Token == "" || len(resp.Cookies()) != 0 {
		t.Fatal("admin session contract failed")
	}
	f.session = session.Token
	return f
}
func (f *fixture) request(method, path, body string, admin bool) (*http.Response, []byte) {
	f.t.Helper()
	r, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if admin {
		r.Header.Set("Authorization", "Bearer "+f.session)
	}
	r.Header.Set("Origin", f.server.URL)
	if strings.HasPrefix(path, "/v1/") {
		r.Header.Set("Authorization", "Bearer "+f.key)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}
func completed(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q,"model":"reported-model","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":2}}}`, id)
}
func waitRecords(t *testing.T, a *App, count int) []Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := a.listRecords(nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		complete := len(rows) == count
		for _, r := range rows {
			if r.Ended == nil {
				complete = false
			}
		}
		if complete {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("records did not finish")
	return nil
}

func TestAdminBoundaryAndSecretIsolation(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, "r") })
	req, _ := http.NewRequest("POST", f.server.URL+"/admin/sources", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+f.session)
	req.Header.Set("Origin", "https://evil.invalid")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-site write accepted")
	}
	resp, _ = f.request("GET", "/admin/sources", "", false)
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated read accepted")
	}
	resp, b := f.request("POST", "/admin/accounts", `{"provider":"openai_compatible","auth_type":"api_key","name":"New"}`, true)
	var account Account
	if resp.StatusCode != 201 || json.Unmarshal(b, &account) != nil {
		t.Fatal("account create failed")
	}
	resp, b = f.request("POST", "/admin/accounts/"+account.ID+"/credential", `{"version":1,"secret":"SENTINEL_CREATED_SECRET"}`, true)
	if resp.StatusCode != 200 {
		t.Fatal("credential publication failed")
	}
	resp, b = f.request("POST", "/admin/sources", encode(map[string]any{"name": "New", "account_id": account.ID, "base_url": "https://example.invalid/v1", "models": []string{"m"}}), true)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, b)
	}
	if bytes.Contains(b, []byte("SENTINEL_CREATED_SECRET")) {
		t.Fatal("source response leaked credential")
	}
	_, b = f.request("POST", "/admin/diagnostics/export", `{}`, true)
	if bytes.Contains(b, []byte("SENTINEL")) {
		t.Fatal("diagnostics leaked secrets")
	}
	paths, _ := filepath.Glob(filepath.Join(f.a.Config.DataDir, "gatt.db*"))
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte("SENTINEL_CREATED_SECRET")) || bytes.Contains(b, []byte(f.key)) {
			t.Fatal("database leaked secret")
		}
	}
}
func TestNativeJSONAndOpaqueToolHistory(t *testing.T) {
	var got []byte
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SENTINEL_UPSTREAM_SECRET" || r.Header.Get("Cookie") != "" {
			t.Error("credential separation failed")
		}
		got, _ = io.ReadAll(r.Body)
		completed(w, "resp-1")
	})
	src, _ := f.a.Store.source("source")
	src.NativeOperations = []string{"compact"}
	if e := f.a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	seedNativeOpaque(t, f.a, "source", "key", "fixture-model", "unchanged")
	body := `{"model":"fixture-model","stream":false,"input":[{"type":"reasoning","id":"opaque","encrypted_content":"unchanged"},{"role":"assistant","phase":"commentary","content":"Hi"},{"type":"function_call","call_id":"c1","namespace":"ns","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"result"}],"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"tool","parameters":{"type":"object"}}]}],"future_optional":"preserve"}`
	resp, b := f.request("POST", "/v1/responses", body, false)
	if resp.StatusCode != 200 || !bytes.Contains(b, []byte(`"status":"completed"`)) {
		t.Fatalf("response %d %s", resp.StatusCode, b)
	}
	if string(got) != body {
		t.Fatal("native body mutated")
	}
	rows := waitRecords(t, f.a, 2)
	if rows[0].Status != "succeeded" || rows[0].Completeness != "complete" || *rows[0].Usage.Input != 12 || rows[0].ReportedModel != "reported-model" {
		t.Fatalf("bad record: %+v", rows[0])
	}
}
func TestSSEArbitraryChunksAndFunctionCycle(t *testing.T) {
	var calls atomic.Int32
	wire := ": heartbeat\r\n\r\nevent: vendor.unknown\r\ndata: {\"text\":\"你好\"}\r\n\r\nevent: response.created\ndata: {\"response\":\ndata: {\"id\":\"resp-tool\",\"status\":\"in_progress\"}}\n\nevent: response.function_call_arguments.delta\ndata: {\"item_id\":\"fc1\",\"delta\":\"{\\\"x\\\":\"}\n\nevent: response.function_call_arguments.delta\ndata: {\"item_id\":\"fc1\",\"delta\":\"1}\"}\n\nevent: response.completed\ndata: {\"response\":{\"id\":\"resp-tool\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"c1\",\"name\":\"add\",\"arguments\":\"{\\\"x\\\":1}\"}],\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}}\n\n"
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 2 {
			b, _ := io.ReadAll(r.Body)
			if !bytes.Contains(b, []byte(`"call_id":"c1"`)) {
				t.Error("tool link lost")
			}
			completed(w, "resp-follow")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, b := range []byte(wire) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	})
	resp, b := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"call add","stream":true}`, false)
	if resp.StatusCode != 200 || string(b) != wire {
		t.Fatalf("SSE bytes changed: %d, %d/%d", resp.StatusCode, len(b), len(wire))
	}
	resp, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","input":[{"type":"function_call_output","call_id":"c1","output":"2"}],"previous_response_id":"resp-tool"}`, false)
	if resp.StatusCode != 200 {
		t.Fatal("continuation failed")
	}
	rows := waitRecords(t, f.a, 2)
	for _, v := range rows {
		if v.Status != "succeeded" {
			t.Fatalf("status %s", v.Status)
		}
	}
}
func TestContinuationIsolationAndURLGeneration(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completed(w, "resp-bound") })
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","previous_response_id":"external","input":"hi"}`, false)
	if r.StatusCode != 409 {
		t.Fatal("external ID allowed")
	}
	r, _ = f.request("POST", "/admin/sources/source/credential", `{"version":1,"credential":"replacement-fake"}`, true)
	if r.StatusCode != 200 {
		t.Fatal("replace failed")
	}
	r, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","previous_response_id":"resp-bound","input":"hi"}`, false)
	if r.StatusCode != 409 || calls.Load() != 1 {
		t.Fatal("old identity forwarded")
	}
	s, _ := f.a.Store.source("source")
	r, _ = f.request("PATCH", "/admin/sources/source", fmt.Sprintf(`{"version":%d,"base_url":"https://elsewhere.invalid/v1"}`, s.Version), true)
	if r.StatusCode != 400 {
		t.Fatal("cross origin reused credential")
	}
	r, _ = f.request("PATCH", "/admin/sources/source", fmt.Sprintf(`{"version":%d,"base_url":%q}`, s.Version, s.BaseURL+"/v2"), true)
	if r.StatusCode != 200 {
		t.Fatal("url patch failed")
	}
	next, _ := f.a.Store.source("source")
	if next.Generation != s.Generation+1 {
		t.Fatal("URL did not increment generation")
	}
}
func TestUpstreamErrorsNoRetriesAndRedaction(t *testing.T) {
	for _, code := range []int{400, 401, 429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "17")
				w.Header().Set("Set-Cookie", "upstream=no")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				fmt.Fprint(w, `{"error":{"type":"source_error","message":"SENTINEL_UPSTREAM_SECRET rejected"}}`)
			})
			r, b := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
			if r.StatusCode != code || r.Header.Get("Retry-After") != "17" || r.Header.Get("Set-Cookie") != "" || bytes.Contains(b, []byte("SENTINEL_UPSTREAM_SECRET")) || calls.Load() != 1 {
				t.Fatal("error contract broken")
			}
			rows := waitRecords(t, f.a, 1)
			if rows[0].Status != "failed" || rows[0].Usage.Input != nil || rows[0].Cost != nil {
				t.Fatal("error usage fabricated")
			}
		})
	}
}
func TestDisconnectNotSuccessAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"delta\":\"partial\"}\n\n")
		w.(http.Flusher).Flush()
	})
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi","stream":true}`, false)
	rows := waitRecords(t, f.a, 1)
	if rows[0].Status != "failed" || calls.Load() != 1 {
		t.Fatal("broken stream passed or replayed")
	}
}
func TestCancellationDisableRevokeAndDelete(t *testing.T) {
	began := make(chan struct{})
	cancelled := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ready\n\n")
		w.(http.Flusher).Flush()
		close(began)
		<-r.Context().Done()
		close(cancelled)
	})
	req, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"fixture-model","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	<-began
	r, _ := f.request("DELETE", "/admin/sources/source", "", true)
	if r.StatusCode != 409 {
		t.Fatal("active source deleted")
	}
	r, _ = f.request("PATCH", "/admin/sources/source", `{"version":1,"enabled":false}`, true)
	if r.StatusCode != 200 {
		t.Fatal("disable failed")
	}
	select {
	case <-cancelled:
		t.Fatal("disable cancelled an active request")
	default:
	}
	r, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 503 {
		t.Fatal("disabled source accepted")
	}
	resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream not cancelled")
	}
	rows := waitRecords(t, f.a, 1)
	if rows[0].Status != "cancelled" {
		t.Fatalf("cancel status %s", rows[0].Status)
	}
	f.request("DELETE", "/admin/client-keys/key", "", true)
	r, _ = f.request("DELETE", "/admin/sources/source", "", true)
	if r.StatusCode != 200 {
		t.Fatal("source deletion failed")
	}
}
func TestRedirectAndLocalRejection(t *testing.T) {
	var targetCalls, calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Redirect(w, r, target.URL, 307) })
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 502 || targetCalls.Load() != 0 {
		t.Fatal("redirect followed")
	}
	for _, body := range []string{`{"model":"fixture-model","background":true}`, `{"model":"fixture-model","conversation":"c"}`, `{"model":"fixture-model","tools":[{"type":"web_search"}]}`, `{"model":"fixture-model","input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`, `{"model":"unknown"}`} {
		r, _ = f.request("POST", "/v1/responses", body, false)
		expected := 400
		if strings.Contains(body, "web_search") || strings.Contains(body, "background") || strings.Contains(body, "conversation") {
			expected = 422
		}
		if r.StatusCode != expected {
			t.Fatalf("unsupported capability status=%d expected=%d body=%s", r.StatusCode, expected, body)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("rejected request hit upstream")
	}
}
func TestUsageDecimalsUnknownAndCurrencies(t *testing.T) {
	num := func(v int64) *int64 { return &v }
	u := Usage{Input: num(100), Output: num(20), Cached: num(30), Reasoning: num(5)}
	p := &Price{Currency: "USD", Input: "2", Cached: "0.5", Output: "8"}
	cost := estimate(u, p)
	if cost == nil || *cost != "0.000315000000" {
		t.Fatalf("bad cost %v", cost)
	}
	if estimate(Usage{Input: num(10)}, p) != nil || estimate(Usage{Input: num(0), Output: num(0)}, p) != nil {
		t.Fatal("missing usage treated as zero")
	}
	if usageCompleteness(Usage{}) != "unknown" || usageCompleteness(Usage{Input: num(0)}) != "partial" {
		t.Fatal("bad unknown semantics")
	}
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, "usage") })
	src := f.source
	src.Price = p
	if e := f.a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	src.Price = &Price{Currency: "EUR", Input: "1", Cached: "1", Output: "1"}
	if e := f.a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	waitRecords(t, f.a, 2)
	_, b := f.request("GET", "/admin/usage", "", true)
	var result struct {
		Costs map[string]string `json:"estimated_cost_by_currency"`
	}
	if json.Unmarshal(b, &result) != nil || len(result.Costs) != 2 {
		t.Fatalf("currency aggregation %s", b)
	}
}
func TestCrashRecoveryAndStorageFailure(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("should not dispatch") })
	rec := Record{ID: "orphan", SourceID: f.source.ID, Status: "dispatching", Started: time.Now().UTC(), Completeness: "unknown"}
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	// Reopen a separate database connection as startup would do, with no active calls.
	reopened, err := OpenStore(f.a.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.DB.Close()
	rows, e := f.a.listRecords(nil, 0)
	if e != nil || rows[0].Status != "interrupted" {
		t.Fatal("interrupted request hidden")
	}
	if _, err = f.a.Store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 503 {
		t.Fatal("dispatch despite database failure")
	}
}
func TestCredentialTransactionRollback(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, "ok") })
	if _, err := f.a.Store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	r, _ := f.request("POST", "/admin/sources/source/credential", `{"version":1,"credential":"new-fake-secret"}`, true)
	if r.StatusCode != 503 {
		t.Fatal("should fail")
	}
	src, _ := f.a.Store.source("source")
	secret, _ := f.vault.Get(src.CredentialRef)
	if secret != "SENTINEL_UPSTREAM_SECRET" {
		t.Fatal("old credential lost")
	}
	f.vault.mu.Lock()
	defer f.vault.mu.Unlock()
	if len(f.vault.values) != 2 {
		t.Fatal("orphan credential not removed")
	}
}
func fakeJWT(account, subject string, identity ...string) string {
	issuer, nonce := "http://upstream.invalid", ""
	if len(identity) > 0 {
		issuer = identity[0]
	}
	if len(identity) > 1 {
		nonce = identity[1]
	}
	return signedTestClaims(map[string]any{"iss": issuer, "aud": "synthetic-client", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "sub": subject, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
}
func TestConcurrentRefreshAndUnknownRotation(t *testing.T) {
	var refreshes, calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			refreshes.Add(1)
			time.Sleep(30 * time.Millisecond)
			writeJSON(w, 200, map[string]any{"access_token": "new-test-access", "refresh_token": "rotated-test-refresh", "id_token": fakeJWT("account", "subject", "http://"+r.Host), "expires_in": 3600})
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer new-test-access" || r.Header.Get("Chatgpt-Account-Id") != "account" {
			t.Error("incorrect refreshed identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"response\":{\"id\":\"r%d\",\"status\":\"completed\"}}\n\n", calls.Load())
	})
	src := f.source
	src.Kind = "codex_subscription"
	src.AuthStatus = "logged_in"
	f.a.Store.saveSource(src)
	c := Credential{Access: "old", Refresh: "old-refresh", Account: "account", Subject: "subject", Expires: time.Now().Add(-time.Hour)}
	f.vault.Put(src.CredentialRef, encode(c))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","stream":true,"input":"hi"}`, false)
			if r.StatusCode != 200 {
				t.Errorf("status %d", r.StatusCode)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 || calls.Load() != 4 {
		t.Fatal("refresh race")
	}
	waitRecords(t, f.a, 4)
	before := calls.Load()
	for _, body := range []string{`{"model":"fixture-model","input":"hi"}`, `{"model":"fixture-model","stream":true,"store":true}`, `{"model":"fixture-model","stream":true,"previous_response_id":"x"}`} {
		r, _ := f.request("POST", "/v1/responses", body, false)
		if r.StatusCode != 400 {
			t.Fatal("subscription semantics silently changed")
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid subscription call dispatched")
	}
	src, _ = f.a.Store.source("source")
	src.AuthStatus = "refreshing"
	f.a.Store.saveSource(src)
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","stream":true,"input":"hi"}`, false)
	if r.StatusCode != 503 || refreshes.Load() != 1 {
		t.Fatal("unknown rotation retried")
	}
}
func TestRefreshLostResponseNeverReused(t *testing.T) {
	var refreshes atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			refreshes.Add(1)
			h := w.(http.Hijacker)
			conn, _, _ := h.Hijack()
			conn.Close()
			return
		}
		t.Error("unexpected model call")
	})
	s := f.source
	s.Kind = "codex_subscription"
	s.AuthStatus = "logged_in"
	f.a.Store.saveSource(s)
	f.vault.Put(s.CredentialRef, encode(Credential{Access: "old", Refresh: "old", Account: "a", Subject: "s", Expires: time.Now().Add(-time.Hour)}))
	for i := 0; i < 2; i++ {
		r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","stream":true,"input":"hi"}`, false)
		if r.StatusCode != 503 {
			t.Fatal("refresh uncertainty allowed")
		}
	}
	if refreshes.Load() != 1 {
		t.Fatal("old rotating token reused")
	}
}
func callbackConfig(t *testing.T, f *fixture) {
	t.Helper()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	f.a.Config.Codex.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)
	t.Cleanup(f.a.CloseAdmission)
}
func startBrowserLogin(t *testing.T, f *fixture) (Login, *url.URL) {
	t.Helper()
	s, err := f.a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	r, b := f.request("POST", "/admin/sources/source/login", encode(map[string]int{"version": s.Version}), true)
	if r.StatusCode != 201 {
		t.Fatalf("start login %d %s", r.StatusCode, b)
	}
	var op Login
	if e := json.Unmarshal(b, &op); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(op.AuthorizationURL)
	if e != nil {
		t.Fatal(e)
	}
	return op, u
}
func TestBrowserOAuthPKCEStateAndSave(t *testing.T) {
	var expectedChallenge, expectedNonce string
	var exchanges atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Error("unexpected OAuth endpoint")
			return
		}
		exchanges.Add(1)
		_ = r.ParseForm()
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(hash[:]) != expectedChallenge || r.Form.Get("code") != "test-code" {
			t.Error("PKCE exchange mismatch")
		}
		writeJSON(w, 200, map[string]any{"access_token": "test-access", "refresh_token": "test-refresh", "id_token": fakeJWT("account", "subject", "http://"+r.Host, expectedNonce), "expires_in": 3600})
	})
	callbackConfig(t, f)
	s := f.source
	s.Kind = "codex_subscription"
	s.AuthStatus = "logged_out"
	s.Configured = false
	s.CredentialRef = ""
	f.a.Store.saveSource(s)
	op, u := startBrowserLogin(t, f)
	expectedChallenge = u.Query().Get("code_challenge")
	expectedNonce = u.Query().Get("nonce")
	if op.Method != "browser_oauth" || u.Query().Get("code_challenge_method") != "S256" || expectedChallenge == "" {
		t.Fatal("browser PKCE contract missing")
	}
	r, b := f.request("POST", "/admin/sources/source/login", encode(map[string]int{"version": s.Version}), true)
	var same Login
	json.Unmarshal(b, &same)
	if r.StatusCode != 200 || same.ID != op.ID {
		t.Fatal("duplicate browser login")
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := client.Get(f.a.Config.Codex.RedirectURI + "?state=wrong&code=test-code")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 400 || exchanges.Load() != 0 {
		t.Fatal("wrong state accepted")
	}
	r, e = client.Get(f.a.Config.Codex.RedirectURI + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"test-code"}}.Encode())
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Set-Cookie") != "" {
		t.Fatal("callback must return completion page without management identity")
	}
	s, _ = f.a.Store.source("source")
	if !s.Configured || s.AuthStatus != "logged_in" || s.Verification.Status != "untested" || exchanges.Load() != 1 {
		t.Fatal("login incorrectly saved or advertised as verified")
	}
	_, b = f.request("GET", "/admin/sources/source/login", "", true)
	if bytes.Contains(b, []byte(expectedChallenge)) || bytes.Contains(b, []byte("test-refresh")) {
		t.Fatal("finished login retained transient/secret material")
	}
}
func TestLoginCancelLateResultCannotResurrect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			close(started)
			<-release
			writeJSON(w, 200, map[string]any{"access_token": "test", "refresh_token": "test", "id_token": fakeJWT("a", "s"), "expires_in": 3600})
		}
	})
	callbackConfig(t, f)
	s := f.source
	s.Kind = "codex_subscription"
	s.AuthStatus = "logged_out"
	s.Configured = false
	s.CredentialRef = ""
	f.a.Store.saveSource(s)
	_, u := startBrowserLogin(t, f)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r, e := http.Get(f.a.Config.Codex.RedirectURI + "?" + url.Values{"state": {u.Query().Get("state")}, "code": {"code"}}.Encode())
		if e == nil {
			r.Body.Close()
		}
	}()
	<-started
	f.request("DELETE", "/admin/sources/source/login", "", true)
	f.request("POST", "/admin/sources/source/logout", `{}`, true)
	close(release)
	<-done
	s, _ = f.a.Store.source("source")
	if s.Configured || s.AuthStatus != "logged_out" {
		t.Fatal("late OAuth result resurrected login")
	}
}
func TestBrowserTicketSingleUse(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, "ok") })
	req, _ := http.NewRequest("POST", f.server.URL+"/admin/browser-tickets", nil)
	req.Header.Set("Authorization", "Bearer test-administrator")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var result struct {
		Ticket string `json:"ticket"`
	}
	json.Unmarshal(b, &result)
	if r.StatusCode != 201 || result.Ticket == "" {
		t.Fatal("ticket creation")
	}
	body := encode(map[string]string{"ticket": result.Ticket})
	r, _ = f.request("POST", "/admin/session", body, false)
	if r.StatusCode != 200 || len(r.Cookies()) != 0 {
		t.Fatal("ticket exchange failed")
	}
	r, _ = f.request("POST", "/admin/session", body, false)
	if r.StatusCode != 401 {
		t.Fatal("ticket replay accepted")
	}
}
func TestAdminTestsMeteredAndContinuationVerified(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, fmt.Sprintf("admin-%d", calls.Add(1))) })
	src := f.source
	src.Continuation = false
	f.a.Store.saveSource(src)
	r, b := f.request("POST", "/admin/sources/source/test", `{"continuation":true}`, true)
	if r.StatusCode != 200 {
		t.Fatalf("test failed %s", b)
	}
	rows := waitRecords(t, f.a, 2)
	for _, v := range rows {
		if v.Origin != "admin_test" || v.Usage.Input == nil {
			t.Fatal("unmetered test")
		}
	}
	src, _ = f.a.Store.source("source")
	if !src.Continuation {
		t.Fatal("continuation not verified")
	}
}
func TestBoundedObserverOversizeAndUTF8(t *testing.T) {
	var out bytes.Buffer
	observed := 0
	wire := []byte("data: " + strings.Repeat("你", 1000) + "\n\nevent: custom\ndata: {}\n\n")
	s := newSSE(100, func(b []byte) error { observed++; _, e := out.Write(b); return e }, func(b []byte) error { _, e := out.Write(b); return e })
	for _, b := range wire {
		if e := s.Feed([]byte{b}); e != nil {
			t.Fatal(e)
		}
	}
	s.End()
	if !s.Skipped || observed != 1 || !bytes.Equal(wire, out.Bytes()) || cap(s.buf) > 200 {
		t.Fatal("observer unbounded or lossy")
	}
}
func TestEightStreamsCapacityAndCancellation(t *testing.T) {
	var active atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": start\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	responses := []*http.Response{}
	for i := 0; i < 8; i++ {
		r, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"fixture-model","stream":true,"input":"hi"}`))
		r.Header.Set("Authorization", "Bearer "+f.key)
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		responses = append(responses, resp)
	}
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","stream":true,"input":"hi"}`, false)
	if r.StatusCode != 429 || active.Load() != 8 {
		t.Fatal("capacity failed")
	}
	for _, r := range responses {
		r.Body.Close()
	}
	waitRecords(t, f.a, 8)
}
func TestRetentionDeletesBindings(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { completed(w, "old") })
	rec := Record{ID: "old-record", SourceID: "source", Status: "succeeded", Started: time.Now().UTC().Add(-10 * 24 * time.Hour), KeyID: "key", Model: "fixture-model", Generation: 1}
	f.a.Store.record(rec)
	f.a.Store.bind(rec, "old")
	if e := f.a.Store.cleanup(7); e != nil {
		t.Fatal(e)
	}
	if f.a.Store.continuation("old", ClientKey{ID: "key"}, f.source, "fixture-model") {
		t.Fatal("expired binding retained")
	}
}
func TestMissingVaultCannotDispatch(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("missing credential dispatched") })
	f.vault.Delete("source")
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 503 {
		t.Fatal("missing credential accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.a.StartMaintenance(ctx)
	if err := f.a.WaitOwnedTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHeaderAndIdleTimeoutNoReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%v", stream), func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, ": heartbeat\n\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			r, _ := f.request("POST", "/v1/responses", fmt.Sprintf(`{"model":"fixture-model","stream":%v,"input":"hi"}`, stream), false)
			if !stream && r.StatusCode != 504 {
				t.Fatalf("expected header 504, got %d", r.StatusCode)
			}
			rows := waitRecords(t, f.a, 1)
			if rows[0].Status != "failed" || calls.Load() != 1 {
				t.Fatalf("timeout misreported or replayed: %+v", rows[0])
			}
			if stream && rows[0].ErrorStage != "timeout" {
				t.Fatal("idle timeout reported as user cancellation")
			}
		})
	}
}
func TestBodyLimitsRejectBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.Repeat("x", 1024))
	})
	f.a.Config.MaxBody = 100
	f.a.Config.MaxResponse = 100
	r, _ := f.request("POST", "/v1/responses", strings.Repeat("x", 101), false)
	if r.StatusCode != 413 || calls.Load() != 0 {
		t.Fatal("oversized request dispatched")
	}
	r, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 502 || calls.Load() != 1 {
		t.Fatal("response size limit failed")
	}
}
func TestPostDispatchStorageFailureStopsAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[]}`)
	})
	done := make(chan int, 1)
	go func() {
		r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
		done <- r.StatusCode
	}()
	<-entered
	if _, err := f.a.Store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if <-done != 200 {
		t.Fatal("completed result changed due to record failure")
	}
	deadline := time.Now().Add(time.Second)
	for !f.a.storageFailed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	r, _ := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	if r.StatusCode != 503 || calls.Load() != 1 {
		t.Fatal("admitted new unrecordable call")
	}
}
func TestBindingsRejectAnotherKeyAndSource(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completed(w, "owned") })
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi"}`, false)
	waitRecords(t, f.a, 1)
	r, b := f.request("POST", "/admin/client-keys", `{"name":"another","source_id":"source"}`, true)
	if r.StatusCode != 201 {
		t.Fatal("key create")
	}
	var fresh struct {
		Secret string `json:"secret"`
	}
	json.Unmarshal(b, &fresh)
	original := f.key
	f.key = fresh.Secret
	r, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","previous_response_id":"owned","input":"hi"}`, false)
	if r.StatusCode != 409 {
		t.Fatal("other key continued response")
	}
	f.key = original
	s := f.source
	s.ID = "second"
	s.Name = "Second"
	f.a.Store.saveSource(s)
	f.request("PATCH", "/admin/client-keys/key", `{"source_id":"second"}`, true)
	r, _ = f.request("POST", "/v1/responses", `{"model":"fixture-model","previous_response_id":"owned","input":"hi"}`, false)
	if r.StatusCode != 409 || calls.Load() != 1 {
		t.Fatal("other source continued response")
	}
}
func TestPartialPriceDoesNotClaimTotal(t *testing.T) {
	n := int64(20)
	p := &Price{Currency: "USD", Input: "2", Cached: "0.5", Output: "8"}
	u := Usage{Output: &n}
	partial := estimatePartial(u, p)
	if estimate(u, p) != nil || partial == nil || *partial != "0.000160000000" {
		t.Fatal("partial estimate incorrect")
	}
}

func TestSubscriptionSSEWithoutContentType(t *testing.T) {
	complete := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_missing_header\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	for _, tc := range []struct {
		name, kind, contentType, body string
		want                          int
	}{
		{"subscription_event", "codex_subscription", "", complete, 200},
		{"subscription_data", "codex_subscription", "", strings.TrimPrefix(complete, "event: response.completed\n"), 200},
		{"subscription_heartbeat", "codex_subscription", "", ": heartbeat\n\n" + complete, 200},
		{"subscription_json", "codex_subscription", "", `{"status":"completed"}`, 502},
		{"subscription_html", "codex_subscription", "", "<html>not a stream</html>", 502},
		{"subscription_empty", "codex_subscription", "", "", 502},
		{"wrong_explicit_type", "codex_subscription", "text/html", complete, 502},
		{"wrong_type_suffix", "codex_subscription", "text/event-stream-invalid", complete, 502},
		{"api_missing_type", "api_key", "", complete, 502},
		{"api_valid_type", "api_key", "text/event-stream; charset=utf-8", complete, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header()["Content-Type"] = nil // Disable net/http content sniffing.
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				w.WriteHeader(200)
				for _, b := range []byte(tc.body) {
					w.Write([]byte{b})
					w.(http.Flusher).Flush()
				}
			})
			if tc.kind == "codex_subscription" {
				s := f.source
				s.Kind, s.AuthStatus = tc.kind, "logged_in"
				if err := f.vault.Put(s.CredentialRef, encode(Credential{Access: "synthetic-access", Account: "synthetic-account", Expires: time.Now().Add(time.Hour)})); err != nil {
					t.Fatal(err)
				}
				if err := f.a.Store.saveSource(s); err != nil {
					t.Fatal(err)
				}
			}
			r, body := f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hello","stream":true}`, false)
			if r.StatusCode != tc.want {
				t.Fatalf("status %d, wanted %d", r.StatusCode, tc.want)
			}
			rows := waitRecords(t, f.a, 1)
			if calls.Load() != 1 {
				t.Fatal("request replayed")
			}
			if tc.want == 200 {
				if string(body) != tc.body || rows[0].Status != "succeeded" || rows[0].Usage.Input == nil || *rows[0].Usage.Input != 2 {
					t.Fatal("stream bytes, completion or usage lost")
				}
			} else if rows[0].Status != "failed" || rows[0].ErrorStage != "content_type" {
				t.Fatal("invalid stream reported as success")
			}
		})
	}
}

func TestTotalTimeoutWithActiveHeartbeats(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				fmt.Fprint(w, ": heartbeat\n\n")
				w.(http.Flusher).Flush()
			}
		}
	})
	f.a.Config.TotalTimeout = 1
	f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"hi","stream":true}`, false)
	rows := waitRecords(t, f.a, 1)
	if calls.Load() != 1 || rows[0].Status != "failed" || rows[0].ErrorStage != "timeout" {
		t.Fatal("total timeout replayed or reported as success")
	}
}

func TestClientResendRecordsSeparateAttempts(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completed(w, id("resp")) })
	for range 2 {
		f.request("POST", "/v1/responses", `{"model":"fixture-model","input":"same payload"}`, false)
	}
	rows := waitRecords(t, f.a, 2)
	var attempts int
	if err := f.a.Store.DB.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || attempts != 2 || rows[0].ID == rows[1].ID {
		t.Fatal("client resends were merged or replayed")
	}
}
