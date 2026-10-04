package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clientFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("client config must never call upstream")
		w.WriteHeader(500)
	})
	if _, err := f.a.Store.DB.Exec(ClientConfigSchema); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	versions := map[string]string{"codex": "codex-cli 0.158.0", "claude": "2.1.281 (Claude Code)", "opencode": "1.18.33"}
	for name, version := range versions {
		installClientVersionFixture(t, bin, name, version)
	}
	t.Setenv("PATH", bin)
	for _, key := range []string{"CODEX_HOME", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"} {
		t.Setenv(key, "")
	}
	return f
}
func clientRequest(t *testing.T, a *App, path string, in any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	w := httptest.NewRecorder()
	if strings.HasPrefix(path, "/admin/clients/") {
		a.clientsAPI(w, r)
	} else {
		a.clientChangesAPI(w, r)
	}
	return w.Code, w.Body.Bytes()
}
func clientRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSpecClientConfigVerifiedCodex0160(t *testing.T) {
	f := clientFixture(t)
	bin := t.TempDir()
	installClientVersionFixture(t, bin, "codex", "codex-cli 0.160.0")
	t.Setenv("PATH", bin)
	root := clientRoot(t)
	path := filepath.Join(root, "config.toml")
	preview := clientPreviewFor(t, f.a, "codex", "user", root, path)
	if preview.Status != "ready" || preview.Version != "codex-cli 0.160.0" {
		t.Fatal("verified current CLI was blocked", preview)
	}
	change := clientApply(t, f.a, preview)
	if change.State != "applied" || !strings.Contains(string(readClient(t, path)), "model_provider") {
		t.Fatal("current client configuration was not applied")
	}
}
func clientPreviewFor(t *testing.T, a *App, kind, scope, root, path string) clientPreview {
	t.Helper()
	status, b := clientRequest(t, a, "/admin/clients/"+kind+"/preview", clientConfigInput{Scope: scope, Root: root, Path: path, Model: "coding"})
	if status != 200 {
		t.Fatalf("preview %d: %s", status, b)
	}
	var p clientPreview
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "ready" {
		t.Fatalf("blocked preview: %s", b)
	}
	return p
}
func clientApply(t *testing.T, a *App, p clientPreview) clientChange {
	t.Helper()
	status, b := clientRequest(t, a, "/admin/client-changes", clientApplyInput{PreviewID: p.ID, BaseHashes: map[string]string{p.Files[0].Path: p.Files[0].BaseHash}, SecretDelivery: "env_reference"})
	if status != 201 {
		t.Fatalf("apply %d: %s", status, b)
	}
	var c clientChange
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func clientRestorePreviewFor(t *testing.T, a *App, c clientChange) clientRestorePreview {
	t.Helper()
	status, b := clientRequest(t, a, "/admin/client-changes/"+c.ID+"/restore-preview", map[string]any{})
	if status != 200 {
		t.Fatalf("restore preview %d: %s", status, b)
	}
	var p clientRestorePreview
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func clientRestore(t *testing.T, a *App, c clientChange, p clientRestorePreview, resolutions map[string]string) {
	t.Helper()
	status, b := clientRequest(t, a, "/admin/client-changes/"+c.ID+"/restore", clientRestoreInput{CurrentHash: p.CurrentHash, Resolutions: resolutions})
	if status != 200 {
		t.Fatalf("restore %d: %s", status, b)
	}
}
func readClient(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func writeClient(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSpecClientConfig(t *testing.T) {
	cases := []struct{ kind, scope, path, before string }{
		{"codex", "user", "config.toml", "# user comment\nmodel = 'before' # model comment\nmodel_provider = \"openai\"\n[model_providers.other]\nname = \"unknown provider\"\nbase_url = \"https://example.test\"\n[mcp_servers.user]\ncommand = \"unchanged\"\n"},
		{"claude", "project", ".claude/settings.local.json", "{\n  \"model\": \"before\",\n  \"env\": {\"USER_ENV\": \"preserve\"},\n  \"permissions\": {\"allow\": [\"Read\"]},\n  \"unknown_secret\": \"SENTINEL_PRIVATE_FIELD\"\n}\n"},
		{"opencode", "project", "opencode.jsonc", "// user comment\n{\n  \"model\": \"other/before\", // model comment\n  \"provider\": {\"other\": {\"name\": \"unknown provider\"}},\n  \"mcp\": {\"user\": {\"type\": \"local\", \"command\": [\"unchanged\"]}},\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			f := clientFixture(t)
			root := clientRoot(t)
			path := filepath.Join(root, tc.path)
			writeClient(t, path, tc.before)
			auth := filepath.Join(root, "auth.json")
			writeClient(t, auth, "SENTINEL_OFFICIAL_LOGIN_UNTOUCHED")
			p := clientPreviewFor(t, f.a, tc.kind, tc.scope, root, path)
			if p.Files[0].BaseHash != digest(tc.before) || len(p.Files[0].Diff) == 0 {
				t.Fatalf("invalid hash/diff: %+v", p)
			}
			encoded := encode(p)
			if strings.Contains(encoded, "SENTINEL_PRIVATE_FIELD") {
				t.Fatal("unknown secret leaked into preview")
			}
			if string(readClient(t, path)) != tc.before {
				t.Fatal("preview mutated file")
			}
			c := clientApply(t, f.a, p)
			b := readClient(t, path)
			doc, err := parseClientDocument(tc.kind, path, b, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range clientDesiredFields(tc.kind, "coding", "http://"+f.a.Config.Listen) {
				value, present, err := doc.field(field.Path)
				if err != nil || !present || value != field.Ours {
					t.Fatalf("field %v not applied: %q %v", field.Path, value, err)
				}
			}
			assertPrivateTestPermissions(t, path, 0600)
			if tc.kind != "claude" && (!strings.Contains(string(b), "user comment") || !strings.Contains(string(b), "model comment")) {
				t.Fatal("comments lost")
			}
			rp := clientRestorePreviewFor(t, f.a, c)
			clientRestore(t, f.a, c, rp, nil)
			restored := readClient(t, path)
			doc, err = parseClientDocument(tc.kind, path, restored, true)
			if err != nil {
				t.Fatal(err)
			}
			model, present, err := doc.field([]string{"model"})
			if err != nil || !present || !(model == "before" || model == "other/before") {
				t.Fatalf("original model not restored: %q", model)
			}
			if tc.kind == "codex" {
				_, present, err = doc.lookup([]string{"model_providers", "cove"})
				if err != nil || present {
					t.Fatal("new Cove provider was not removed")
				}
			}
			if !strings.Contains(string(restored), "unchanged") && tc.kind != "claude" {
				t.Fatal("unknown MCP lost")
			}
			if string(readClient(t, auth)) != "SENTINEL_OFFICIAL_LOGIN_UNTOUCHED" {
				t.Fatal("official auth changed")
			}
			rows, err := f.a.Store.DB.Query("SELECT data FROM client_previews UNION ALL SELECT data FROM client_changes")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var data string
				if err = rows.Scan(&data); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(data, "SENTINEL_PRIVATE_FIELD") {
					t.Fatal("secret leaked into metadata DB")
				}
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		})
	}
}

func TestSpecClientConfigCASAndExpiry(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "opencode.json")
	writeClient(t, path, "{\"model\":\"before\"}\n")
	p := clientPreviewFor(t, f.a, "opencode", "project", root, path)
	newer := "{\"model\":\"user-new\",\"permission\":\"preserve\"}\n"
	writeClient(t, path, newer)
	status, b := clientRequest(t, f.a, "/admin/client-changes", clientApplyInput{PreviewID: p.ID, BaseHashes: map[string]string{path: p.Files[0].BaseHash}, SecretDelivery: "env_reference"})
	if status != 409 || string(readClient(t, path)) != newer {
		t.Fatalf("stale apply did not preserve user file: %d %s", status, b)
	}
	p = clientPreviewFor(t, f.a, "opencode", "project", root, path)
	if _, err := f.a.Store.DB.Exec("UPDATE client_previews SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), p.ID); err != nil {
		t.Fatal(err)
	}
	status, b = clientRequest(t, f.a, "/admin/client-changes", clientApplyInput{PreviewID: p.ID, BaseHashes: map[string]string{path: p.Files[0].BaseHash}, SecretDelivery: "env_reference"})
	if status != 409 {
		t.Fatalf("expired apply %d %s", status, b)
	}
}

func TestSpecClientRestorePreservesUserChanges(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".claude", "settings.local.json")
	writeClient(t, path, "{\"model\":\"original\",\"permissions\":{\"allow\":[\"Read\"]}}\n")
	c := clientApply(t, f.a, clientPreviewFor(t, f.a, "claude", "project", root, path))
	doc, err := parseClientDocument("claude", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := doc.edit([]clientField{{Path: []string{"model"}, OursPresent: true, Ours: "user-later"}, {Path: []string{"new_user_field"}, OursPresent: true, Ours: "keep"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, path, string(edited))
	p := clientRestorePreviewFor(t, f.a, c)
	if p.Status != "conflict" {
		t.Fatalf("expected model conflict: %+v", p)
	}
	status, _ := clientRequest(t, f.a, "/admin/client-changes/"+c.ID+"/restore", clientRestoreInput{CurrentHash: p.CurrentHash})
	if status != 409 || string(readClient(t, path)) != string(edited) {
		t.Fatal("unresolved conflict changed file")
	}
	clientRestore(t, f.a, c, p, map[string]string{"model": "keep_current"})
	restored, err := parseClientDocument("claude", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path  []string
		value string
	}{{[]string{"model"}, "user-later"}, {[]string{"new_user_field"}, "keep"}} {
		v, present, err := restored.field(entry.path)
		if err != nil || !present || v != entry.value {
			t.Fatalf("user edit lost: %v", entry.path)
		}
	}
	if _, present, _ := restored.lookup([]string{"env", "ANTHROPIC_BASE_URL"}); present {
		t.Fatal("non-conflicting Cove field not restored")
	}
	// A second restore must not turn a prior keep_current decision into a new conflict.
	rp := clientRestorePreviewFor(t, f.a, c)
	if len(rp.Fields) != 0 {
		t.Fatal("resolved restore reapplied")
	}
}

func TestSpecClientRestoreExplicitBeforeAndRestoreCAS(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "config.toml")
	writeClient(t, path, "model = \"original\"\n")
	c := clientApply(t, f.a, clientPreviewFor(t, f.a, "codex", "user", root, path))
	doc, err := parseClientDocument("codex", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.edit([]clientField{{Path: []string{"model"}, OursPresent: true, Ours: "user-later"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, path, string(b))
	p := clientRestorePreviewFor(t, f.a, c)
	writeClient(t, path, string(b)+"# user unrelated comment\n")
	status, _ := clientRequest(t, f.a, "/admin/client-changes/"+c.ID+"/restore", clientRestoreInput{CurrentHash: p.CurrentHash, Resolutions: map[string]string{"model": "restore_before"}})
	if status != 409 {
		t.Fatal("stale restore accepted")
	}
	p = clientRestorePreviewFor(t, f.a, c)
	clientRestore(t, f.a, c, p, map[string]string{"model": "restore_before"})
	result := string(readClient(t, path))
	if !strings.Contains(result, "original") || !strings.Contains(result, "user unrelated comment") {
		t.Fatal("explicit before did not preserve unrelated comment")
	}
}

func TestSpecClientConfigNewFileRestore(t *testing.T) {
	for _, userAdds := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete_unchanged", true: "preserve_user_addition"}[userAdds], func(t *testing.T) {
			f := clientFixture(t)
			root := clientRoot(t)
			path := filepath.Join(root, "opencode.jsonc")
			c := clientApply(t, f.a, clientPreviewFor(t, f.a, "opencode", "project", root, path))
			if userAdds {
				doc, err := parseClientDocument("opencode", path, readClient(t, path), true)
				if err != nil {
					t.Fatal(err)
				}
				b, err := doc.edit([]clientField{{Path: []string{"user_added"}, OursPresent: true, Ours: "preserve"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				writeClient(t, path, string(b))
			}
			p := clientRestorePreviewFor(t, f.a, c)
			clientRestore(t, f.a, c, p, nil)
			if !userAdds {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("unchanged generated file not removed")
				}
			} else {
				doc, err := parseClientDocument("opencode", path, readClient(t, path), true)
				if err != nil {
					t.Fatal(err)
				}
				value, present, err := doc.field([]string{"user_added"})
				if err != nil || !present || value != "preserve" {
					t.Fatal("user addition removed")
				}
				if _, present, _ := doc.lookup([]string{"provider"}); present {
					t.Fatal("empty generated provider containers retained")
				}
			}
		})
	}
}

func TestSpecClientConfigRejectsSymlinkEscapeAndFormat(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	outside := clientRoot(t)
	path := filepath.Join(root, "opencode.json")
	destination := filepath.Join(outside, "opencode.json")
	writeClient(t, destination, "{\"model\":\"outside\"}")
	if err := os.Symlink(destination, path); err != nil {
		t.Fatal(err)
	}
	status, _ := clientRequest(t, f.a, "/admin/clients/opencode/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
	if status != 400 {
		t.Fatalf("symlink accepted: %d", status)
	}
	if string(readClient(t, destination)) != "{\"model\":\"outside\"}" {
		t.Fatal("outside file changed")
	}
	os.Remove(path)
	status, _ = clientRequest(t, f.a, "/admin/clients/opencode/preview", clientConfigInput{Scope: "project", Root: root, Path: destination, Model: "coding"})
	if status != 400 {
		t.Fatal("outside target accepted")
	}
	if err := os.Symlink(outside, filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	status, _ = clientRequest(t, f.a, "/admin/clients/claude/preview", clientConfigInput{Scope: "project", Root: root, Path: filepath.Join(root, ".claude", "settings.local.json"), Model: "coding"})
	if status != 400 {
		t.Fatal("symlink directory accepted")
	}
	for _, content := range []string{"{broken}", "{\"model\":\"one\",\"model\":\"two\"}"} {
		writeClient(t, path, content)
		status, _ = clientRequest(t, f.a, "/admin/clients/opencode/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
		if status != 422 || string(readClient(t, path)) != content {
			t.Fatal("invalid config mutated or accepted")
		}
	}
}

func TestSpecClientConfigBlocksOverridesAndUnknownVersion(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".claude", "settings.local.json")
	writeClient(t, path, "{\"env\":{\"ANTHROPIC_API_KEY\":\"SENTINEL_SENSITIVE_CONFLICT\"}}")
	status, b := clientRequest(t, f.a, "/admin/clients/claude/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
	if status != 200 || !strings.Contains(string(b), "blocked") || strings.Contains(string(b), "SENTINEL_SENSITIVE_CONFLICT") {
		t.Fatalf("config auth conflict: %d %s", status, b)
	}
	writeClient(t, path, "{}")
	t.Setenv("ANTHROPIC_MODEL", "SENTINEL_ENV_VALUE")
	status, b = clientRequest(t, f.a, "/admin/clients/claude/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
	if status != 200 || !strings.Contains(string(b), "blocked") || strings.Contains(string(b), "SENTINEL_ENV_VALUE") {
		t.Fatal("environment blocker leaked or missed")
	}
	t.Setenv("ANTHROPIC_MODEL", "")
	installClientVersionFixture(t, os.Getenv("PATH"), "claude", "99.0.0")
	status, b = clientRequest(t, f.a, "/admin/clients/claude/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
	if status != 200 || !strings.Contains(string(b), "unsupported_version") {
		t.Fatal("unknown version applied template")
	}
}

func TestSpecClientConfigPrivateStoreFailurePreservesFile(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "opencode.json")
	writeClient(t, path, "{}")
	f.vault.failPut = true
	status, _ := clientRequest(t, f.a, "/admin/clients/opencode/preview", clientConfigInput{Scope: "project", Root: root, Path: path, Model: "coding"})
	if status != 503 || string(readClient(t, path)) != "{}" {
		t.Fatal("failed private record changed client")
	}
}
