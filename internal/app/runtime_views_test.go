package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeClientApp(t *testing.T) *App {
	t.Helper()
	a := contractApp(t, nil)
	bin := t.TempDir()
	for name, version := range map[string]string{"codex": "codex-cli 0.158.0", "claude": "2.1.281 (Claude Code)", "opencode": "1.18.33"} {
		installClientVersionFixture(t, bin, name, version)
	}
	t.Setenv("PATH", bin)
	for _, name := range []string{"CODEX_HOME", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
	return a
}
func runtimeClientCard(t *testing.T, a *App, kind string) clientCard {
	t.Helper()
	w := lifecycleAdmin(a, "GET", "/admin/clients", "", "")
	mustOperationsStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "SYNTHETIC_PRIVATE_SECRET") {
		t.Fatal("client status leaked private field values")
	}
	var result struct {
		Items []clientCard `json:"items"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	for _, c := range result.Items {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatal("missing card", kind)
	return clientCard{}
}

func TestV13ClientConfigurationCheckedFieldsAndRestore(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			a := runtimeClientApp(t)
			if runtimeClientCard(t, a, kind).Configuration.State != "not_configured" {
				t.Fatal("installation inferred configuration")
			}
			root := clientRoot(t)
			path := filepath.Join(root, "config.toml")
			initial := "model = \"before\"\nsecret = \"SYNTHETIC_PRIVATE_SECRET\"\n"
			if kind == "claude" {
				path = filepath.Join(root, ".claude", "settings.local.json")
				initial = `{"model":"before","secret":"SYNTHETIC_PRIVATE_SECRET"}`
			}
			if kind == "opencode" {
				path = filepath.Join(root, "opencode.json")
				initial = `{"model":"before","secret":"SYNTHETIC_PRIVATE_SECRET"}`
			}
			writeClient(t, path, initial)
			scope := "project"
			if kind == "codex" {
				scope = "user"
			}
			preview := clientPreviewFor(t, a, kind, scope, root, path)
			change := clientApply(t, a, preview)
			shown := runtimeClientCard(t, a, kind).Configuration
			if shown.State != "configured" || shown.Model != "coding" || shown.Path != path || shown.Scope != scope {
				t.Fatal("applied configuration was not checked", encode(shown))
			}
			before := readClient(t, path)
			// Unrelated user fields do not invalidate Cove's selected configuration.
			changed := string(before)
			if kind == "codex" {
				changed += "\nunrelated = true\n"
			} else {
				changed = strings.TrimSpace(changed)
				changed = strings.TrimSuffix(changed, "}") + `,"unrelated":true}`
			}
			writeClient(t, path, changed)
			if runtimeClientCard(t, a, kind).Configuration.State != "configured" {
				t.Fatal("unrelated fields invalidated configuration")
			}
			changed = strings.ReplaceAll(changed, `"coding"`, `"other-model"`)
			if kind == "opencode" {
				changed = strings.ReplaceAll(changed, `"cove/coding"`, `"cove/other-model"`)
			}
			writeClient(t, path, changed)
			shown = runtimeClientCard(t, a, kind).Configuration
			if shown.State != "modified" || shown.Model != "" {
				t.Fatal("changed file reported as applied", encode(shown))
			}
			writeClient(t, path, string(before))
			restore := clientRestorePreviewFor(t, a, change)
			clientRestore(t, a, change, restore, nil)
			if runtimeClientCard(t, a, kind).Configuration.State != "restored" {
				t.Fatal("restored file remained configured")
			}
		})
	}
}

func TestV13ClientConfigurationNoopAndUnsafeTarget(t *testing.T) {
	a := runtimeClientApp(t)
	root := clientRoot(t)
	path := filepath.Join(root, "config.toml")
	preview := clientPreviewFor(t, a, "codex", "user", root, path)
	clientApply(t, a, preview)
	// A preview with no changed fields must still remember the selected model.
	preview = clientPreviewFor(t, a, "codex", "user", root, path)
	if len(preview.Files[0].Diff) != 0 {
		t.Fatal("expected no-op preview")
	}
	clientApply(t, a, preview)
	if runtimeClientCard(t, a, "codex").Configuration.Model != "coding" {
		t.Fatal("no-op lost model metadata")
	}
	before := readClient(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if runtimeClientCard(t, a, "codex").Configuration.State != "modified" {
		t.Fatal("removed file remained configured")
	}
	outside := filepath.Join(clientRoot(t), "outside.toml")
	writeClient(t, outside, string(before))
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	shown := runtimeClientCard(t, a, "codex").Configuration
	if shown.State != "unavailable" || shown.Model != "" {
		t.Fatal("followed an unsafe replaced path", encode(shown))
	}

}

func runtimeRoute(t *testing.T, a *App) (Source, Route) {
	t.Helper()
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	src.MaxConcurrent = &one
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	models, err := a.Store.models(src.ID)
	if err != nil || len(models) != 1 {
		t.Fatal(err)
	}
	route := Route{ID: "runtime-route", Name: "runtime route", Enabled: true, Version: 1, Strategy: "priority", MaxAttempts: 1, MaxConcurrent: &one, Members: []RouteMember{{ModelID: models[0].ID, Weight: 1}}}
	if _, err = a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?)", route.ID, encode(route)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", "coding", route.ID, encode(Alias{PublicModel: "coding", RouteID: route.ID, Version: 1})); err != nil {
		t.Fatal(err)
	}
	return src, route
}
func runtimeRouteRead(t *testing.T, a *App, route Route) map[string]json.RawMessage {
	t.Helper()
	w := lifecycleAdmin(a, "GET", "/admin/routes/"+route.ID, "", "")
	mustOperationsStatus(t, w, 200)
	var body map[string]json.RawMessage
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal(w.Body.String())
	}
	var runtime map[string]json.RawMessage
	if json.Unmarshal(body["runtime"], &runtime) != nil {
		t.Fatal("missing runtime")
	}
	return runtime
}

func TestV13RoutingRuntimeCountsAndHealthUseRealAdmission(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return contractResponse(contractJSON, "application/json"), nil
	})
	src, route := runtimeRoute(t, a)
	key := ClientKey{ID: "key", RouteID: route.ID}
	if _, err := a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	// Reading the list/individual runtime does not advance the existing weights.
	a.routeWeights[route.ID] = map[string]int{"sentinel": 7}
	w := lifecycleAdmin(a, "GET", "/admin/routes", "", "")
	mustOperationsStatus(t, w, 200)
	if a.routeWeights[route.ID]["sentinel"] != 7 {
		t.Fatal("runtime read mutated scheduling")
	}
	done := make(chan int, 1)
	go func() {
		done <- contractCall(a, "POST", "/v1/responses", `{"model":"coding","input":"test","stream":false}`, "test-client-key").Code
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached upstream")
	}
	status := lifecycleAdmin(a, "GET", "/admin/status", "", "")
	mustOperationsStatus(t, status, 200)
	var summary struct {
		Active    int            `json:"active_requests"`
		Accounts  map[string]int `json:"account_active"`
		Routes    map[string]int `json:"route_active"`
		Accepting bool           `json:"accepting_requests"`
	}
	if json.Unmarshal(status.Body.Bytes(), &summary) != nil || summary.Active != 1 || summary.Accounts[src.AccountID] != 1 || summary.Routes[route.ID] != 1 || !summary.Accepting {
		t.Fatal("actual admission occupancy was not exposed", status.Body.String())
	}
	runtime := runtimeRouteRead(t, a, route)
	if string(runtime["active_requests"]) != "1" {
		t.Fatal("route occupancy", encode(runtime))
	}
	if !strings.Contains(string(runtime["candidates"]), "账号并发已满") {
		t.Fatal("busy account was shown healthy", encode(runtime))
	}
	close(release)
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal("request failed", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	runtime = runtimeRouteRead(t, a, route)
	if string(runtime["active_requests"]) != "0" {
		t.Fatal("released capacity not visible", encode(runtime))
	}
	candidate, err := a.candidateFor(src, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	a.policyState.Observe(PolicyObservation{Candidate: candidate, Kind: "auth"}, time.Now())
	runtime = runtimeRouteRead(t, a, route)
	if !strings.Contains(string(runtime["candidates"]), "账号认证被上游拒绝") {
		t.Fatal("auth health exclusion not reflected", encode(runtime))
	}
	a.SetStagedAdmission(true)
	status = lifecycleAdmin(a, "GET", "/admin/status", "", "")
	if json.Unmarshal(status.Body.Bytes(), &summary) != nil || summary.Accepting {
		t.Fatal("staged admission shown ready", status.Body.String())
	}
}
