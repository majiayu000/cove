package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecClinePreservesProviderCredentialsAndLaterEdits(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "settings", "providers.json")
	before := `{"version":1,"modes":{},"lastUsedProvider":"cline","providers":{"cline":{"settings":{"provider":"cline","auth":{"accessToken":"SYNTHETIC_LOGIN"}},"updatedAt":"2026-01-01T00:00:00Z"},"openai-compatible":{"settings":{"provider":"openai-compatible","model":"before","baseUrl":"https://old.invalid/v1","headers":{"X-User":"keep"}},"updatedAt":"2026-01-02T00:00:00Z"}}}`
	writeClient(t, path, before)
	p := clientPreviewFor(t, f.a, "cline", "user", root, path)
	if p.RequiredSecret["env_name"] != "OPENAI_API_KEY" || strings.Contains(encode(p), "SYNTHETIC") {
		t.Fatal("Cline secret delivery incorrect or preview leaked credentials")
	}
	c := clientApply(t, f.a, p)
	var applied map[string]any
	if err := json.Unmarshal(readClient(t, path), &applied); err != nil {
		t.Fatal(err)
	}
	providers := applied["providers"].(map[string]any)
	entry := providers["openai-compatible"].(map[string]any)
	settings := entry["settings"].(map[string]any)
	if settings["apiKey"] != nil || entry["updatedAt"] != "2026-01-02T00:00:00Z" || settings["model"] != "coding" {
		t.Fatal("Cline application changed credentials or unrelated metadata")
	}
	settings["model"] = "user-after"
	settings["contextWindow"] = 12345
	writeClient(t, path, encode(applied))
	rp := clientRestorePreviewFor(t, f.a, c)
	if rp.Status != "conflict" {
		t.Fatal("Later model change did not create a restore conflict")
	}
	clientRestore(t, f.a, c, rp, map[string]string{"providers.openai-compatible.settings.model": "keep_current"})
	var restored map[string]any
	if err := json.Unmarshal(readClient(t, path), &restored); err != nil {
		t.Fatal(err)
	}
	entry = restored["providers"].(map[string]any)["openai-compatible"].(map[string]any)
	settings = entry["settings"].(map[string]any)
	if restored["lastUsedProvider"] != "cline" || settings["model"] != "user-after" || settings["baseUrl"] != "https://old.invalid/v1" || settings["contextWindow"] != float64(12345) || settings["apiKey"] != nil || !strings.Contains(string(readClient(t, path)), "SYNTHETIC_LOGIN") {
		t.Fatal("Cline restore lost original provider credentials or later edits")
	}
}

func TestSpecClineNewFileCASAndRestore(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "settings", "providers.json")
	p := clientPreviewFor(t, f.a, "cline", "user", root, path)
	writeClient(t, path, `{"version":1,"modes":{},"providers":{},"user":"later"}`)
	current := string(readClient(t, path))
	status, _ := clientRequest(t, f.a, "/admin/client-changes", clientApplyInput{PreviewID: p.ID, BaseHashes: map[string]string{path: p.Files[0].BaseHash}, SecretDelivery: "env_reference"})
	if status != 409 || string(readClient(t, path)) != current {
		t.Fatal("Cline stale preview overwrote user file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p = clientPreviewFor(t, f.a, "cline", "user", root, path)
	c := clientApply(t, f.a, p)
	var data map[string]any
	if json.Unmarshal(readClient(t, path), &data) != nil || data["version"] != float64(1) {
		t.Fatal("New Cline config lacks official schema version")
	}
	entry := data["providers"].(map[string]any)["openai-compatible"].(map[string]any)
	if _, ok := entry["updatedAt"].(string); !ok {
		t.Fatal("New Cline provider lacks required metadata")
	}
	assertPrivateTestPermissions(t, path, 0600)
	clientRestore(t, f.a, c, clientRestorePreviewFor(t, f.a, c), nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Unmodified newly created Cline config was not removed")
	}
}

func TestSpecClineRefusesInvalidSchemaPathAndEnvironmentOverride(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "settings", "providers.json")
	for _, content := range []string{`{"version":2,"providers":{}}`, `{"providers":{}}`, `{"version":1,"providers":{"openai-compatible":{"updatedAt":123}}}`} {
		writeClient(t, path, content)
		status, _ := clientRequest(t, f.a, "/admin/clients/cline/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
		if status != 422 || string(readClient(t, path)) != content {
			t.Fatal("Unsupported Cline format was accepted or modified")
		}
	}
	status, _ := clientRequest(t, f.a, "/admin/clients/cline/preview", clientConfigInput{Scope: "user", Root: root, Path: filepath.Join(root, "auth.json"), Model: "coding"})
	if status != 400 {
		t.Fatal("Cline arbitrary credential file path accepted")
	}
	writeClient(t, path, `{"version":1,"providers":{}}`)
	for _, credentials := range []string{`"apiKey":"SYNTHETIC_OLD_KEY"`, `"auth":{"accessToken":"SYNTHETIC_OLD_LOGIN"}`} {
		content := `{"version":1,"providers":{"openai-compatible":{"settings":{"provider":"openai-compatible",` + credentials + `},"updatedAt":"2026-01-01T00:00:00Z"}}}`
		writeClient(t, path, content)
		status, raw := clientRequest(t, f.a, "/admin/clients/cline/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
		var p clientPreview
		if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "blocked" || strings.Contains(string(raw), "SYNTHETIC") || string(readClient(t, path)) != content {
			t.Fatal("Cline persisted authentication conflict was missed or altered")
		}
	}
	writeClient(t, path, `{"version":1,"providers":{}}`)
	t.Setenv("CLINE_PROVIDER_SETTINGS_PATH", "other-provider-settings.json")
	status, raw := clientRequest(t, f.a, "/admin/clients/cline/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
	var p clientPreview
	if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "blocked" || !strings.Contains(encode(p.Blockers), "CLINE_PROVIDER_SETTINGS_PATH") {
		t.Fatal("Cline higher priority provider path was not disclosed and blocked")
	}
}
