package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatGPTIndependentRegistrationAndPermission(t *testing.T) {
	for _, permission := range []bool{true, false} {
		t.Run(map[bool]string{true: "direct-granted", false: "identity-only"}[permission], func(t *testing.T) {
			var nonce string
			var exchanges atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				exchanges.Add(1)
				_ = r.ParseForm()
				if r.URL.Path != "/api/accounts/oauth/token" || r.Form.Get("client_id") != "issued-cove-client" || r.Form.Get("resource") != chatGPTResource || r.Form.Get("client_secret") != "" {
					t.Error("independent token exchange contract changed")
				}
				scope := "openid profile email offline_access resource.invoke"
				if permission {
					scope += " " + chatGPTDirectScope
				}
				identity := signedTestClaims(map[string]any{"iss": "http://" + r.Host, "aud": "issued-cove-client", "exp": time.Now().Add(time.Hour).Unix(), "sub": "verified-subject", "nonce": nonce})
				writeJSON(w, 200, map[string]any{"access_token": "test-access", "refresh_token": "test-refresh", "id_token": identity, "expires_in": 3600, "scope": scope})
			})
			callbackConfig(t, f)
			f.a.Config.Codex.ClientID = "dynamic_agent_client"
			s := f.source
			s.Kind, s.Provider, s.BaseURL, s.AuthStatus = "codex_subscription", "codex", chatGPTResource, "logged_out"
			s.Configured, s.CredentialRef = false, ""
			if err := f.a.Store.saveSource(s); err != nil {
				t.Fatal(err)
			}
			_, auth := startBrowserLogin(t, f)
			q := auth.Query()
			nonce = q.Get("nonce")
			if auth.Path != "/api/accounts/authorize" || q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "Cove" || !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") || q.Get("resource") != chatGPTResource || !strings.Contains(q.Get("scope"), chatGPTDirectScope) || q.Get("codex_cli_simplified_flow") != "" {
				t.Fatal("Cove registration used the official Codex identity or missing direct permission")
			}
			callback := func(values url.Values) int {
				r, err := http.Get(f.a.Config.Codex.RedirectURI + "?" + values.Encode())
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				return r.StatusCode
			}
			if callback(url.Values{"state": {"wrong"}, "code": {"test-code"}, "client_id": {"issued-cove-client"}}) != 400 || exchanges.Load() != 0 {
				t.Fatal("wrong state accepted")
			}
			if callback(url.Values{"state": {q.Get("state")}, "code": {"test-code"}}) != 400 || exchanges.Load() != 0 {
				t.Fatal("missing issued ID accepted")
			}
			if callback(url.Values{"state": {q.Get("state")}, "code": {"test-code"}, "client_id": {"issued-cove-client"}}) != 200 {
				t.Fatal("registration callback failed")
			}
			s, _ = f.a.Store.source("source")
			account, _, readErr := f.a.Store.account(s.AccountID)
			if readErr != nil || !s.Configured || s.AuthStatus != "logged_in" || account.Identity["plan_usage_enabled"] != permission {
				t.Fatal("saved account identity and plan permission conflated")
			}
			f.a.mu.Lock()
			credential, err := f.a.subscriptionCredential(context.Background(), &s)
			f.a.mu.Unlock()
			if permission && (err != nil || credential.ClientID != "issued-cove-client" || credential.Subject != "verified-subject") || !permission && err == nil {
				t.Fatal("permission not enforced before inference")
			}
			_, returning := startBrowserLogin(t, f)
			if returning.Query().Get("client_id") != "issued-cove-client" || returning.Query().Get("ext_agent_host_id") != q.Get("ext_agent_host_id") || returning.Query().Get("agent_name_hint") != "" {
				t.Fatal("reauthorization re-registered the host/account")
			}
			if callback(url.Values{"state": {returning.Query().Get("state")}, "code": {"test-code"}, "client_id": {"other-client"}}) != 400 || exchanges.Load() != 1 {
				t.Fatal("callback replaced selected registration")
			}
			f.a.mu.Lock()
			f.a.cancelLogin(s.ID)
			f.a.mu.Unlock()
		})
	}
}

func TestChatGPTIssuedClientSurvivesFailedExchange(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	callbackConfig(t, f)
	f.a.Config.Codex.ClientID = "dynamic_agent_client"
	s := f.source
	s.Kind, s.Provider, s.BaseURL, s.AuthStatus = "codex_subscription", "codex", chatGPTResource, "logged_out"
	s.Configured, s.CredentialRef = false, ""
	if err := f.a.Store.saveSource(s); err != nil {
		t.Fatal(err)
	}
	_, auth := startBrowserLogin(t, f)
	r, err := http.Get(f.a.Config.Codex.RedirectURI + "?" + url.Values{"state": {auth.Query().Get("state")}, "code": {"test-code"}, "client_id": {"issued-cove-client"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	_, again := startBrowserLogin(t, f)
	if again.Query().Get("client_id") != "issued-cove-client" {
		t.Fatal("failed exchange lost issued registration")
	}
}

func TestChatGPTPublicCatalogOrderAndTransport(t *testing.T) {
	a := contractApp(t, nil)
	src, _ := a.Store.source("source")
	src.Kind, src.Provider, src.BaseURL = "codex_subscription", "codex", chatGPTResource
	items, err := parseCodexCatalog([]byte(`{"models":[{"slug":"second","display_name":"Second","visibility":"list"},{"slug":"hidden","visibility":"hide"},{"slug":"first","display_name":"First","visibility":"list"}]}`), "", src)
	if err != nil || len(items) != 2 || items[0].ID != "second" || items[1].ID != "first" {
		t.Fatal("catalog visibility/order changed")
	}
	if err := a.publishCodexCatalogLocked(src, items, "", "", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	models, err := a.Store.models(src.ID)
	if err != nil || len(models) < 2 || models[0].UpstreamModel != "second" || models[1].UpstreamModel != "first" {
		t.Fatal("published catalog reordered")
	}
	_, err = a.startCodexObservationLocked(src, "quota", &quotaObservationState{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "没有公开额度") {
		t.Fatal("private quota contract reused")
	}
	input, _, err := compatInput([]byte(`{"model":"fixture-model","max_tokens":128,"system":"instruction","messages":[{"role":"user","content":"hi"}]}`), "messages", Source{Kind: src.Kind, Provider: src.Provider, BaseURL: src.BaseURL, AllowParameterAdjustment: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(input["input"]), `"role":"developer"`) {
		t.Fatal("public direct route received unsupported system role")
	}
	req, err := prepareUpstream(context.Background(), src, input, []byte(encode(input)), "test-access", "issued-cove-client", true, CodexConfig{})
	if err != nil || req.URL.String() != chatGPTResource+"/responses" || req.Header.Get("Authorization") != "Bearer test-access" || req.Header.Get("ChatGPT-Account-Id") != "" || req.Header.Get("originator") != "" {
		t.Fatal("private Codex transport reused for independent app")
	}
}

func TestChatGPTLogoutRevokesOnlyRegisteredSession(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, result string
		statuses               []int
	}{
		{"confirmed", "http://upstream.invalid/revoke", "confirmed", []int{200}},
		{"retry_server_error", "http://upstream.invalid/revoke", "confirmed", []int{503, 200}},
		{"unconfirmed", "http://upstream.invalid/revoke", "unknown", []int{400}},
		{"different_origin", "http://other.invalid/revoke", "unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := contractApp(t, nil)
			src, _ := a.Store.source("source")
			src.Kind, src.Provider, src.BaseURL, src.AuthStatus = "codex_subscription", "codex", chatGPTResource, "logged_in"
			if err := a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			if err := a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "synthetic-access", Refresh: "synthetic-refresh", ClientID: "issued-client"})); err != nil {
				t.Fatal(err)
			}
			_ = a.Secrets.Put("chatgpt-client-"+src.AccountID, "issued-client")
			calls := 0
			a.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/.well-known/openid-configuration" {
					return contractResponse(encode(map[string]string{"issuer": a.Config.Codex.AuthBaseURL, "revocation_endpoint": tc.endpoint}), "application/json"), nil
				}
				if len(tc.statuses) <= calls {
					t.Fatal("credential sent to an unapproved revocation destination")
				}
				_ = r.ParseForm()
				if r.Method != "POST" || r.Form.Get("client_id") != "issued-client" || r.Form.Get("token") != "synthetic-refresh" || r.Form.Get("token_type_hint") != "refresh_token" || r.Header.Get("Authorization") != "" {
					t.Error("wrong registered-session revocation contract")
				}
				current, _ := a.Store.source(src.ID)
				if current.AuthStatus != "logged_out" || current.Configured {
					t.Error("local admission remained open during revocation")
				}
				resp := contractResponse("", "application/json")
				resp.StatusCode = tc.statuses[calls]
				calls++
				return resp, nil
			})
			w := httptest.NewRecorder()
			a.sourcesAPI(w, contractRequest("POST", "/admin/sources/source/logout", `{"version":1}`, "test-session"))
			var result map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["upstream_revocation"] != tc.result || calls != len(tc.statuses) {
				t.Fatalf("logout result %d %s", w.Code, w.Body.String())
			}
			if _, err := a.Secrets.Get(src.CredentialRef); err == nil {
				t.Fatal("logout retained bearer credentials")
			}
			if client, err := a.Secrets.Get("chatgpt-client-" + src.AccountID); err != nil || client != "issued-client" {
				t.Fatal("logout discarded registered client mapping")
			}
		})
	}
}

func TestChatGPTRefreshPreservesRegistrationAndEnforcesPermission(t *testing.T) {
	for _, permission := range []bool{true, false} {
		t.Run(map[bool]string{true: "granted", false: "permission_lost"}[permission], func(t *testing.T) {
			calls := atomic.Int32{}
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = r.ParseForm()
				if r.URL.Path != "/api/accounts/oauth/token" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "issued-client" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("resource") != chatGPTResource || r.Form.Get("scope") != "" {
					t.Error("refresh used a different registration or grant")
				}
				scope := "openid profile email offline_access"
				if permission {
					scope += " " + chatGPTDirectScope
				}
				identity := signedTestClaims(map[string]any{"iss": "http://" + r.Host, "aud": "issued-client", "exp": time.Now().Add(time.Hour).Unix(), "sub": "verified-subject"})
				writeJSON(w, 200, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "id_token": identity, "expires_in": 3600, "scope": scope})
			})
			src := f.source
			src.Kind, src.Provider, src.BaseURL, src.AuthStatus = "codex_subscription", "codex", chatGPTResource, "logged_in"
			if err := f.a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			old := Credential{Access: "old-access", Refresh: "old-refresh", Account: "issued-client", Subject: "verified-subject", ClientID: "issued-client", Scopes: []string{chatGPTDirectScope}, Expires: time.Now().Add(-time.Minute)}
			src, _ = f.a.Store.source(src.ID)
			if err := f.a.Secrets.Put(src.CredentialRef, encode(old)); err != nil {
				t.Fatal(err)
			}
			f.a.mu.Lock()
			fresh, err := f.a.subscriptionCredential(context.Background(), &src)
			f.a.mu.Unlock()
			if calls.Load() != 1 || permission && (err != nil || fresh.Refresh != "new-refresh" || fresh.ClientID != old.ClientID || fresh.Subject != old.Subject) || !permission && err == nil {
				t.Fatal("refresh rotation or permission boundary failed")
			}
			current, _ := f.a.Store.source(src.ID)
			if !permission && current.AuthStatus != "needs_reauth" {
				t.Fatal("lost grant remained usable")
			}
		})
	}
}
