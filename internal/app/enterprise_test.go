package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type enterpriseFixture struct {
	e      *Enterprise
	server *httptest.Server
	admin  *http.Client
	t      *testing.T
}

func newEnterpriseFixture(t *testing.T) *enterpriseFixture {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Listen: server.Listener.Addr().String(), DataDir: dir, MaxBody: 8 << 20, MaxResponse: 16 << 20, MaxEvent: 1 << 20, MaxConcurrent: 8, HeaderTimeout: 2, IdleTimeout: 2, TotalTimeout: 10, RetentionDays: 7, SubscriptionQuotaThreshold: 5, Enterprise: &EnterpriseConfig{PublicOrigin: "http://" + server.Listener.Addr().String()}}
	if err = InitializeEnterprise(s, "platform", "synthetic-platform-password"); err != nil {
		t.Fatal(err)
	}
	v := &memorySecrets{values: map[string]string{}}
	root, err := New(c, s, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEnterprise(root, nil, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f := &enterpriseFixture{e: e, server: server, t: t}
	server.Config.Handler = e
	server.Start()
	t.Cleanup(func() {
		server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Close(ctx); err != nil {
			t.Error(err)
		}
		root.CloseAdmission()
		root.CancelAll()
		if root.WaitOwnedTasks(ctx) == nil {
			s.DB.Close()
		}
	})
	f.admin = f.login("platform", "synthetic-platform-password")
	return f
}
func (f *enterpriseFixture) call(client *http.Client, method, path string, body any, want int) map[string]any {
	f.t.Helper()
	var data io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		data = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.server.URL+path, data)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	if res.StatusCode != want {
		f.t.Fatalf("%s %s got %d, wanted %d", method, path, res.StatusCode, want)
	}
	var value map[string]any
	if err = json.Unmarshal(b, &value); err != nil {
		f.t.Fatalf("non JSON result from %s", path)
	}
	return value
}
func (f *enterpriseFixture) login(username, password string) *http.Client {
	f.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	f.call(client, "POST", "/enterprise/session", map[string]any{"username": username, "password": password}, 200)
	return client
}
func (f *enterpriseFixture) tenant(name string) string {
	f.t.Helper()
	return f.call(f.admin, "POST", "/enterprise/tenants", map[string]any{"name": name}, 201)["id"].(string)
}
func (f *enterpriseFixture) addUser(username string) string {
	f.t.Helper()
	return f.call(f.admin, "POST", "/enterprise/users", map[string]any{"username": username, "password": "synthetic-member-password"}, 201)["id"].(string)
}
func TestEnterpriseIsolationAndRoles(t *testing.T) {
	f := newEnterpriseFixture(t)
	tenantA := f.tenant("Alpha")
	tenantB := f.tenant("Beta")
	prefixA := "/t/" + tenantA + "/admin/"
	prefixB := "/t/" + tenantB + "/admin/"
	adminID := f.addUser("alpha-admin")
	viewerID := f.addUser("alpha-viewer")
	for user, role := range map[string]string{adminID: "tenant_admin", viewerID: "viewer"} {
		f.call(f.admin, "PUT", "/enterprise/tenants/"+tenantA+"/members/"+user, map[string]any{"role": role, "version": 0}, 200)
	}
	admin := f.login("alpha-admin", "synthetic-member-password")
	viewer := f.login("alpha-viewer", "synthetic-member-password")
	f.call(http.DefaultClient, "POST", prefixA+"session", map[string]any{}, 401)
	f.call(admin, "GET", prefixB+"status", nil, 403)
	f.call(viewer, "GET", prefixA+"usage", nil, 200)
	f.call(viewer, "POST", prefixA+"client-keys", map[string]any{}, 403)
	f.call(viewer, "GET", prefixA+"accounts", nil, 403)
	f.call(admin, "POST", "/enterprise/tenants", map[string]any{"name": "forbidden"}, 403)
	for _, path := range []string{"clients/codex/preview", "config-extensions/mcp/preview", "backups", "restore-preview", "diagnostics/export", "telemetry", "settings", "doctor", "artifacts/unknown", "browser-tickets"} {
		f.call(admin, "POST", prefixA+path, map[string]any{}, 403)
	}
	for _, auth := range []string{"aws_profile", "google_adc", "codex_subscription"} {
		f.call(admin, "POST", prefixA+"accounts", map[string]any{"auth_type": auth, "provider": "bedrock"}, 422)
	}
	f.call(admin, "GET", prefixA+"settings", nil, 200)
	account := f.call(admin, "POST", prefixA+"accounts", map[string]any{"provider": "openai_compatible", "auth_type": "api_key", "name": "Alpha provider"}, 201)
	accountID := account["id"].(string)
	f.call(admin, "POST", prefixA+"accounts/"+accountID+"/credential", map[string]any{"version": account["version"], "secret": "synthetic-upstream-only"}, 200)
	source := f.call(admin, "POST", prefixA+"sources", map[string]any{"account_id": accountID, "name": "Alpha model", "base_url": "https://synthetic.invalid/v1", "models": []string{"same-model"}}, 201)
	key := f.call(admin, "POST", prefixA+"client-keys", map[string]any{"source_id": source["id"], "name": "Alpha Key"}, 201)["secret"].(string)
	for tenant, want := range map[string]int{tenantA: 200, tenantB: 401} {
		req, _ := http.NewRequest("GET", f.server.URL+"/t/"+tenant+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("tenant Key isolation got %d wanted %d", res.StatusCode, want)
		}
	}
	b := f.call(f.admin, "GET", prefixB+"accounts", nil, 200)
	if len(b["items"].([]any)) != 0 {
		t.Fatal("tenant B has A accounts")
	}
	if f.e.tenants[tenantA].Config.DataDir == f.e.tenants[tenantB].Config.DataDir {
		t.Fatal("shared tenant directory")
	}
	for _, tenant := range []string{tenantA, tenantB} {
		if _, err := os.Stat(filepath.Join(f.e.tenants[tenant].Config.DataDir, "gatt.db")); err != nil {
			t.Fatal(err)
		}
	}
	f.call(f.admin, "PUT", "/enterprise/tenants/"+tenantA+"/members/"+viewerID, map[string]any{"role": "tenant_admin", "version": 0}, 409)
	f.call(f.admin, "DELETE", "/enterprise/tenants/"+tenantA+"/members/"+viewerID, map[string]any{"version": 1}, 200)
	f.call(viewer, "GET", prefixA+"usage", nil, 403)
	f.call(f.admin, "PATCH", "/enterprise/users/"+adminID, map[string]any{"version": 1, "enabled": false}, 200)
	f.call(admin, "GET", prefixA+"status", nil, 401)
	f.call(f.admin, "PATCH", "/enterprise/users/"+adminID, map[string]any{"version": 2, "enabled": true}, 200)
	f.call(admin, "GET", prefixA+"status", nil, 401)
	f.call(f.admin, "PATCH", "/enterprise/tenants/"+tenantB, map[string]any{"version": 1, "enabled": false}, 200)
	f.call(f.admin, "GET", prefixB+"status", nil, 403)
	f.call(f.admin, "PATCH", "/enterprise/tenants/"+tenantB, map[string]any{"version": 1, "enabled": true}, 409)
	f.call(f.admin, "PATCH", "/enterprise/tenants/"+tenantB, map[string]any{"version": 2, "enabled": true}, 200)
	f.call(f.admin, "GET", prefixB+"status", nil, 200)
	var stored string
	if err := f.e.root.Store.DB.QueryRow("SELECT password FROM enterprise_users WHERE id=?", adminID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "synthetic") || !enterprisePasswordMatches(stored, "synthetic-member-password") {
		t.Fatal("password hash contract")
	}
	audit := f.call(f.admin, "GET", "/enterprise/audit", nil, 200)
	raw := encode(audit)
	if strings.Contains(raw, "password") || strings.Contains(raw, "synthetic-upstream") {
		t.Fatal("audit exposes secrets")
	}
}
func TestEnterpriseLoginPasswordAndOrigin(t *testing.T) {
	f := newEnterpriseFixture(t)
	for i := 0; i < 5; i++ {
		f.call(http.DefaultClient, "POST", "/enterprise/session", map[string]any{"username": "platform", "password": "synthetic-wrong-password"}, 401)
	}
	f.call(http.DefaultClient, "POST", "/enterprise/session", map[string]any{"username": "platform", "password": "synthetic-platform-password"}, 429)
	// Rate limits are attached to the real TCP peer, never spoofable forwarding headers.
	req, _ := http.NewRequest("POST", f.server.URL+"/enterprise/session", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://hostile.invalid")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("wrong Origin accepted")
	}
	f.call(f.admin, "POST", "/enterprise/password", map[string]any{"current_password": "wrong-password", "new_password": "synthetic-new-password"}, 401)
	f.call(f.admin, "POST", "/enterprise/password", map[string]any{"current_password": "synthetic-platform-password", "new_password": "synthetic-new-password"}, 200)
	f.call(f.admin, "GET", "/enterprise/session", nil, 401)
	f.e.mu.Lock()
	f.e.failures = map[string][]time.Time{}
	f.e.mu.Unlock()
	client := f.login("platform", "synthetic-new-password")
	current := f.call(client, "GET", "/enterprise/session", nil, 200)
	f.call(client, "PATCH", "/enterprise/users/"+current["id"].(string), map[string]any{"version": current["version"], "enabled": false}, 409)
	f.call(client, "DELETE", "/enterprise/session", nil, 200)
	f.call(client, "GET", "/enterprise/session", nil, 401)
	address, _ := url.Parse(f.server.URL)
	if len(client.Jar.Cookies(address)) != 0 {
		t.Fatal("logout cookie retained")
	}
}

func TestEnterprisePasswordChangeRejectsInFlightLogin(t *testing.T) {
	f := newEnterpriseFixture(t)
	userID := f.addUser("changing-password")
	fresh, err := enterprisePassword("synthetic-replacement-password")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.server.URL+"/enterprise/session", strings.NewReader(`{"username":"changing-password","password":"synthetic-member-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		response, callErr := http.DefaultClient.Do(req)
		if callErr != nil {
			requestError <- callErr
			return
		}
		done <- response
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(f.e.passwordSlots) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("login never began password verification")
		}
		time.Sleep(time.Millisecond)
	}
	// Hold the publication/revocation lock while changing the persisted
	// credential generation, as a password-change commit does. Whether the
	// login has already read the old hash or not, it must not publish afterward.
	f.e.mu.Lock()
	_, err = f.e.root.Store.DB.Exec("UPDATE enterprise_users SET password=?,version=version+1 WHERE id=?", fresh, userID)
	f.e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-done:
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized || len(response.Cookies()) != 0 {
			t.Fatalf("stale login published a session: HTTP %d", response.StatusCode)
		}
	case err := <-requestError:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("in-flight login did not finish")
	}
	f.login("changing-password", "synthetic-replacement-password")
}

func TestEnterpriseRestartRetainsDirectoryAndIsolatesSession(t *testing.T) {
	f := newEnterpriseFixture(t)
	tenant := f.tenant("Persistent")
	a := f.e.tenants[tenant]
	f.call(f.admin, "POST", "/t/"+tenant+"/admin/budgets", map[string]any{"name": "Tenant budget", "scope": map[string]any{"kind": "instance"}, "currency": "USD", "amount_limit": "100", "mode": "soft", "enabled": true, "period": map[string]any{"kind": "calendar_month", "timezone": "UTC"}}, 201)
	if _, err := a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('enterprise_restart_sentinel','kept')"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	next, err := NewEnterprise(f.e.root, nil, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx)
	var value string
	if err = next.tenants[tenant].Store.DB.QueryRow("SELECT value FROM settings WHERE key='enterprise_restart_sentinel'").Scan(&value); err != nil || value != "kept" {
		t.Fatal("tenant data not retained")
	}
	var budgetCount int
	if err = next.tenants[tenant].Store.DB.QueryRow("SELECT count(*) FROM budgets WHERE scope_kind='instance'").Scan(&budgetCount); err != nil || budgetCount != 1 {
		t.Fatal("tenant budget not retained")
	}
	if len(next.sessions) != 0 {
		t.Fatal("session survived restart")
	}
	if err = InitializeEnterprise(f.e.root.Store, "replacement", "synthetic-password"); err == nil {
		t.Fatal("administrator overwritten")
	}
}
func TestEnterpriseOriginAndPersonalDirectoryGuard(t *testing.T) {
	for _, origin := range []string{"http://public.invalid", "https://user:password@public.invalid", "https://public.invalid/path", "https://public.invalid?query=yes"} {
		if (EnterpriseConfig{PublicOrigin: origin}).validate() == nil {
			t.Fatalf("unsafe origin accepted %s", origin)
		}
	}
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if InitializeEnterprise(f.a.Store, "platform", "synthetic-password") == nil {
		t.Fatal("personal data reused as enterprise directory")
	}
}
