package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpecContinueSubscriptionOptionsAndRestore(t *testing.T) {
	f := clientFixture(t)
	f.source.Kind = "codex_subscription"
	if err := f.a.Store.saveSource(f.source); err != nil {
		t.Fatal(err)
	}
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	before := "name: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n  - name: Cove\n    provider: openai\n    model: before\n    useResponsesApi: true\n    requestOptions:\n      timeout: 1234 # preserve timeout\n      extraBodyProperties:\n        temperature: 0.5\n        original: keep\n"
	writeClient(t, path, before)
	status, raw := clientRequest(t, f.a, "/admin/clients/continue/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding", KeyID: "key"})
	var p clientPreview
	if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "ready" || !strings.Contains(encode(p.Warnings), "null") {
		t.Fatal("Continue subscription adjustment not disclosed", status, string(raw))
	}
	c := clientApply(t, f.a, p)
	doc, err := parseClientDocument("continue", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range clientDesiredFields("continue", "coding", "http://"+f.a.Config.Listen, true) {
		value, present, err := doc.field(field.Path)
		if err != nil || !present || value != field.Ours {
			t.Fatal("Continue typed subscription field not applied", field.Path, value, err)
		}
	}
	if !strings.Contains(string(readClient(t, path)), "timeout: 1234 # preserve timeout") || !strings.Contains(string(readClient(t, path)), "original: keep") {
		t.Fatal("Continue subscription application changed unrelated request options")
	}
	clientRestore(t, f.a, c, clientRestorePreviewFor(t, f.a, c), nil)
	doc, err = parseClientDocument("continue", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	value, present, err := doc.field([]string{"models", "Cove", "requestOptions", "extraBodyProperties", "temperature"})
	if err != nil || !present || value != "0.5" || strings.Contains(string(readClient(t, path)), "max_completion_tokens") {
		t.Fatal("Continue restore lost original typed request options")
	}
	// A newly created model must not leave empty requestOptions after restore.
	writeClient(t, path, "name: Existing\nversion: 1.0.0\nschema: v1\n")
	status, raw = clientRequest(t, f.a, "/admin/clients/continue/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding", KeyID: "key"})
	if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "ready" {
		t.Fatal("New subscription model preview failed")
	}
	c = clientApply(t, f.a, p)
	writeClient(t, path, string(readClient(t, path))+"later: user field\n")
	clientRestore(t, f.a, c, clientRestorePreviewFor(t, f.a, c), nil)
	if string(readClient(t, path)) != "name: Existing\nversion: 1.0.0\nschema: v1\nlater: user field\n" {
		t.Fatal("Continue restore left owned nested options or lost later user field")
	}
}

func TestSpecContinueSelectedModelAndThreeWayRestore(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	before := "# user's config\nname: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n  - name: Local # keep this exactly\n    provider: ollama\n    model: local-model\n# keep rules comment\nrules:\n  - Existing rule\n"
	writeClient(t, path, before)
	secretPath := filepath.Join(root, ".continue", ".env")
	writeClient(t, secretPath, "SENTINEL_NOT_A_REAL_CREDENTIAL")
	p := clientPreviewFor(t, f.a, "continue", "user", root, path)
	if p.RequiredSecret["mode"] != "continue_secret" || p.RequiredSecret["status"] != "needs_secret" {
		t.Fatal("Continue secret incorrectly treated as a delivered process secret")
	}
	c := clientApply(t, f.a, p)
	applied := string(readClient(t, path))
	if !strings.Contains(applied, "${{ secrets.COVE_API_KEY }}") || !strings.Contains(applied, "- chat") || !strings.Contains(applied, "# keep rules comment\nrules:") || !strings.Contains(applied, "- name: Local # keep this exactly\n    provider: ollama\n    model: local-model") {
		t.Fatal("Continue model edit changed unrelated bytes or secret syntax")
	}
	writeClient(t, path, applied+"extra: later user edit\n")
	rp := clientRestorePreviewFor(t, f.a, c)
	clientRestore(t, f.a, c, rp, nil)
	restored := string(readClient(t, path))
	if restored != before+"extra: later user edit\n" || string(readClient(t, secretPath)) != "SENTINEL_NOT_A_REAL_CREDENTIAL" {
		t.Fatal("Restore changed an unrelated model, comment, later field or secret file")
	}
}

func TestSpecContinueExistingModelCommentsAndFieldConflict(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	before := "name: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n  # Cove comment\n  - name: Cove\n    provider: openai\n    model: old-model # owned comment\n    apiBase: http://old.invalid/v1\n    apiKey: old-test-secret-reference\n    roles: [chat, edit]\n    requestOptions:\n      timeout: 1234 # user's timeout\n  # Next model comment\n  - name: Local\n    provider: ollama\n    model: local-model\n"
	writeClient(t, path, before)
	p := clientPreviewFor(t, f.a, "continue", "user", root, path)
	c := clientApply(t, f.a, p)
	applied := string(readClient(t, path))
	for _, keep := range []string{"# Cove comment", "# owned comment", "# user's timeout", "# Next model comment", "timeout: 1234"} {
		if strings.Count(applied, keep) != 1 {
			t.Fatal("Continue lost or duplicated comment/unknown field", keep)
		}
	}
	writeClient(t, path, strings.Replace(applied, "model: coding", "model: user-after", 1))
	rp := clientRestorePreviewFor(t, f.a, c)
	if rp.Status != "conflict" {
		t.Fatal("Subsequent Cove model edit was not detected")
	}
	clientRestore(t, f.a, c, rp, map[string]string{"models.Cove.model": "keep_current"})
	doc, err := parseClientDocument("continue", path, readClient(t, path), true)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"model": "user-after", "apiBase": "http://old.invalid/v1", "apiKey": "old-test-secret-reference", "roles": `["chat","edit"]`} {
		got, present, err := doc.field([]string{"models", "Cove", field})
		if err != nil || !present || got != want {
			t.Fatal("Restore lost original or selected current field", field)
		}
	}
}

func TestSpecContinueRefusesAmbiguousFormatAndUninstalledExtension(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	for _, models := range []string{
		"  - name: Cove\n    provider: openai\n    model: a\n  - name: Cove\n    provider: openai\n    model: b\n",
		"  - &saved {name: Cove, provider: openai, model: a}\n  - *saved\n",
		"  - uses: owner/unresolved-model\n",
	} {
		before := "name: Existing\nversion: 1.0.0\nschema: v1\nmodels:\n" + models
		writeClient(t, path, before)
		status, _ := clientRequest(t, f.a, "/admin/clients/continue/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
		if status != 422 || string(readClient(t, path)) != before {
			t.Fatal("Ambiguous YAML was modified")
		}
	}
	writeClient(t, path, "name: Existing\nversion: 1.0.0\nschema: v1\n")
	bin := t.TempDir()
	installClientVersionFixture(t, bin, "code", "unrelated.extension@1.0.0")
	t.Setenv("PATH", bin)
	status, raw := clientRequest(t, f.a, "/admin/clients/continue/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
	var p clientPreview
	if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "blocked" || len(p.Blockers) == 0 {
		t.Fatal("Uninstalled extension was treated as configured")
	}
}

func TestSpecContinueNewFileRestoreAndCAS(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	p := clientPreviewFor(t, f.a, "continue", "user", root, path)
	c := clientApply(t, f.a, p)
	rp := clientRestorePreviewFor(t, f.a, c)
	writeClient(t, path, string(readClient(t, path))+"extra: new user field\n")
	status, _ := clientRequest(t, f.a, "/admin/client-changes/"+c.ID+"/restore", clientRestoreInput{CurrentHash: rp.CurrentHash})
	if status != 409 {
		t.Fatal("Stale restore replaced a modified file")
	}
	rp = clientRestorePreviewFor(t, f.a, c)
	clientRestore(t, f.a, c, rp, nil)
	if !strings.Contains(string(readClient(t, path)), "extra: new user field") || strings.Contains(string(readClient(t, path)), "models:") {
		t.Fatal("New file restoration removed user changes or retained the owned model")
	}
	root = clientRoot(t)
	path = filepath.Join(root, ".continue", "config.yaml")
	p = clientPreviewFor(t, f.a, "continue", "user", root, path)
	c = clientApply(t, f.a, p)
	rp = clientRestorePreviewFor(t, f.a, c)
	clientRestore(t, f.a, c, rp, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Unmodified new config file was not removed")
	}
}

func TestSpecContinueNoFinalNewlineAndModifiedItemPreserved(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, ".continue", "config.yaml")
	writeClient(t, path, "name: Existing\nversion: 1.0.0\nschema: v1")
	p := clientPreviewFor(t, f.a, "continue", "user", root, path)
	c := clientApply(t, f.a, p)
	applied := string(readClient(t, path))
	modified := strings.Replace(applied, "    provider: openai", "    requestOptions:\n      timeout: 1234\n    provider: openai", 1)
	writeClient(t, path, modified)
	rp := clientRestorePreviewFor(t, f.a, c)
	status, _ := clientRequest(t, f.a, "/admin/client-changes/"+c.ID+"/restore", clientRestoreInput{CurrentHash: rp.CurrentHash})
	if status != 422 || string(readClient(t, path)) != modified {
		t.Fatal("Modified new model item was partially removed or silently reported restored")
	}
}
