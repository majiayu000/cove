package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestSpecGrokConfigPreservesOtherModelsLoginAndLaterEdits(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "config.toml")
	before := "# keep user comment\n[models]\ndefault = \"native\"\nweb_search = \"native-search\"\n[model.native]\nmodel = \"original\"\napi_key = \"SYNTHETIC_ORIGINAL_KEY\"\n[mcp_servers.original]\ncommand = \"original-server\"\n"
	writeClient(t, path, before)
	auth := filepath.Join(root, "auth.json")
	writeClient(t, auth, "SYNTHETIC_OFFICIAL_LOGIN")
	p := clientPreviewFor(t, f.a, "grok", "user", root, path)
	if p.Status != "ready" || p.RequiredSecret["env_name"] != "COVE_API_KEY" || strings.Contains(encode(p), "SYNTHETIC") {
		t.Fatal("Grok config blocked or leaked original credentials", p.Status)
	}
	c := clientApply(t, f.a, p)
	var data map[string]any
	if err := toml.Unmarshal(readClient(t, path), &data); err != nil {
		t.Fatal(err)
	}
	entry := data["model"].(map[string]any)["cove"].(map[string]any)
	if entry["api_backend"] != "responses" || entry["env_key"] != "COVE_API_KEY" || entry["api_key"] != nil || data["models"].(map[string]any)["default"] != "cove" {
		t.Fatal("incorrect native Grok BYOK configuration")
	}
	changed := strings.Replace(string(readClient(t, path)), `model = 'coding'`, `model = 'user-after'`, 1)
	// editTOML uses JSON-quoted strings.
	changed = strings.Replace(changed, `model = "coding"`, `model = "user-after"`, 1)
	writeClient(t, path, changed)
	rp := clientRestorePreviewFor(t, f.a, c)
	if rp.Status != "conflict" {
		t.Fatal("Grok later model edit did not produce a conflict")
	}
	clientRestore(t, f.a, c, rp, map[string]string{"model.cove.model": "keep_current"})
	content := string(readClient(t, path))
	if !strings.Contains(content, "user-after") || !strings.Contains(content, "# keep user comment") || !strings.Contains(content, "SYNTHETIC_ORIGINAL_KEY") || !strings.Contains(content, "native-search") || string(readClient(t, auth)) != "SYNTHETIC_OFFICIAL_LOGIN" {
		t.Fatal("Grok restore lost existing models, comments, login or later edits")
	}
	if err := toml.Unmarshal([]byte(content), &data); err != nil || data["models"].(map[string]any)["default"] != "native" {
		t.Fatal("original default model was not restored", err)
	}
}

func TestSpecGrokConfigCASCredentialsScopeAndVersion(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	path := filepath.Join(root, "config.toml")
	p := clientPreviewFor(t, f.a, "grok", "user", root, path)
	writeClient(t, path, "# later user file\n")
	status, _ := clientRequest(t, f.a, "/admin/client-changes", clientApplyInput{PreviewID: p.ID, BaseHashes: map[string]string{path: p.Files[0].BaseHash}, SecretDelivery: "env_reference"})
	if status != 409 || string(readClient(t, path)) != "# later user file\n" {
		t.Fatal("Grok stale preview overwrote a user file")
	}
	for _, content := range []string{"[model.cove]\napi_key=\"SYNTHETIC_KEY\"\n", "[models]\n[models.extra_headers]\nAuthorization=\"SYNTHETIC_AUTH\"\n"} {
		writeClient(t, path, content)
		status, raw := clientRequest(t, f.a, "/admin/clients/grok/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
		if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "blocked" || strings.Contains(string(raw), "SYNTHETIC") || string(readClient(t, path)) != content {
			t.Fatalf("Grok existing credential conflict was ignored or disclosed: status=%d response=%s", status, raw)
		}
	}
	for _, input := range []clientConfigInput{{Scope: "project", Root: root, Path: filepath.Join(root, ".grok", "config.toml"), Model: "coding"}, {Scope: "user", Root: root, Path: filepath.Join(root, "auth.json"), Model: "coding"}} {
		status, _ = clientRequest(t, f.a, "/admin/clients/grok/preview", input)
		if status != 400 {
			t.Fatal("Grok project model or auth-file mutation was accepted")
		}
	}
	writeClient(t, path, "")
	bin := t.TempDir()
	installClientVersionFixture(t, bin, "grok", "grok 9.9.9 (unknown)")
	t.Setenv("PATH", bin)
	status, raw := clientRequest(t, f.a, "/admin/clients/grok/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding"})
	if status != 200 || json.Unmarshal(raw, &p) != nil || p.Status != "blocked" {
		t.Fatal("unknown Grok version was accepted")
	}
}

func TestSpecManualClientsNeverWriteModelConfig(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	for _, card := range manualClientCards() {
		status, raw := clientRequest(t, f.a, "/admin/clients/"+card.Kind+"/preview", clientConfigInput{Scope: "user", Root: root, Path: filepath.Join(root, "settings.json"), Model: "coding"})
		if status != 422 || !strings.Contains(string(raw), card.ManualSetup) {
			t.Fatalf("manual client %s offered an unsupported file write", card.Kind)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("manual client changed config")
	}
}

func TestSpecGrokSelectedSourceUsesNativeProtocol(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		t.Run(protocol, func(t *testing.T) {
			f := clientFixture(t)
			f.source.NativeProtocol = protocol
			if err := f.a.Store.saveSource(f.source); err != nil {
				t.Fatal(err)
			}
			root := clientRoot(t)
			path := filepath.Join(root, "config.toml")
			status, raw := clientRequest(t, f.a, "/admin/clients/grok/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding", KeyID: "key"})
			var preview clientPreview
			if status != 200 || json.Unmarshal(raw, &preview) != nil || preview.Status != "ready" {
				t.Fatal("selected-source Grok preview failed", status)
			}
			change := clientApply(t, f.a, preview)
			doc, err := parseClientDocument("grok", path, readClient(t, path), true)
			if err != nil {
				t.Fatal(err)
			}
			backend, _, err := doc.field([]string{"model", "cove", "api_backend"})
			if err != nil || backend != protocol {
				t.Fatal("Grok did not use the selected source's native protocol", backend, err)
			}
			clientRestore(t, f.a, change, clientRestorePreviewFor(t, f.a, change), nil)
			var keyData string
			if err := f.a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id='key'").Scan(&keyData); err != nil {
				t.Fatal(err)
			}
			var key ClientKey
			if err := json.Unmarshal([]byte(keyData), &key); err != nil {
				t.Fatal(err)
			}
			key.ProtocolAllowlist = []string{"messages"}
			if _, err := f.a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id='key'", encode(key)); err != nil {
				t.Fatal(err)
			}
			status, raw = clientRequest(t, f.a, "/admin/clients/grok/preview", clientConfigInput{Scope: "user", Root: root, Path: path, Model: "coding", KeyID: "key"})
			if status != 200 || json.Unmarshal(raw, &preview) != nil || preview.Status != "blocked" {
				t.Fatal("Grok preview ignored the selected Key's protocol restrictions")
			}
		})
	}
}

// Optional installed-client verification uses a disposable OS home as well as
// GROK_HOME. No account login, real API key or external model is used.
func TestGrokCLIConfigAndExtensions(t *testing.T) {
	if os.Getenv("GATT_GROK_E2E") != "1" {
		t.Skip("set GATT_GROK_E2E=1 to exercise installed Grok Build")
	}
	binary, err := exec.LookPath("grok")
	if err != nil {
		t.Fatal(err)
	}
	root := clientRoot(t)
	grokHome := filepath.Join(root, "independent-grok-home")
	if err := os.Mkdir(grokHome, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(grokHome, "config.toml")
	doc, err := parseClientDocument("grok", path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	config, err := doc.edit(clientDesiredFields("grok", "coding", "http://127.0.0.1:5573"), nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err = extensionEditMCP("grok", "cove-fixture", config, true, map[string]any{"command": "must-not-be-executed"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, path, string(config))
	skill := filepath.Join(grokHome, "skills", "cove-fixture", "SKILL.md")
	writeClient(t, skill, "---\nname: cove-fixture\ndescription: isolated Cove acceptance fixture\n---\nReply with COVE_GROK_FIXTURE.\n")
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GROK_HOME=" + grokHome}
	for _, group := range []string{"CLAUDE", "CURSOR"} {
		for _, item := range []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS"} {
			env = append(env, "GROK_"+group+"_"+item+"_ENABLED=0")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "inspect", "--json")
	cmd.Dir, cmd.Env = root, env
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal("Grok inspect failed", err)
	}
	var result struct {
		Skills []struct {
			Name   string
			Source struct{ Path string }
		}
	}
	if json.Unmarshal(raw, &result) != nil || !strings.Contains(string(raw), "config.toml") {
		t.Fatal("Grok did not discover production model config, MCP and Skill")
	}
	foundSkill := false
	for _, discovered := range result.Skills {
		if discovered.Name == "cove-fixture" && filepath.Clean(discovered.Source.Path) == filepath.Clean(skill) {
			foundSkill = true
		}
	}
	if !foundSkill {
		t.Fatal("Grok did not discover the Skill at the selected independent GROK_HOME path")
	}
	cmd = exec.CommandContext(ctx, binary, "models")
	cmd.Dir, cmd.Env = root, env
	raw, err = cmd.Output()
	if err != nil || !strings.Contains(string(raw), "cove (default)") {
		t.Fatal("Grok did not select production Cove BYOK model", err)
	}
	t.Log("Official Grok Build accepted production config and discovered isolated MCP/Skill; no model request")
}

func TestSpecGrokUserExtensionsShareSelectedHome(t *testing.T) {
	f := clientFixture(t)
	root := clientRoot(t)
	in := extensionInput{Client: "grok", ClientVersion: "1.0.46", Scope: "user", Root: root, Path: filepath.Join(root, "config.toml"), Name: "cove-fixture", Transport: "stdio", Command: "must-not-be-executed", SecretDelivery: "env_reference"}
	p := extensionPreviewFor(t, f.a, "mcp", in)
	change := extensionApplyFor(t, f.a, p, "env_reference")
	if !strings.Contains(string(readClient(t, in.Path)), "mcp_servers") {
		t.Fatal("Grok user MCP was not written in selected GROK_HOME")
	}
	before := extensionRestorePreviewFor(t, f.a, change)
	extensionRestoreFor(t, f.a, change, before, nil)
	card := extensionCard{}
	for _, candidate := range extensionCards() {
		if candidate.Kind == "grok" {
			card = candidate
		}
	}
	if card.SkillUser != "skills" {
		t.Fatal("Grok user Skills must share the selected GROK_HOME")
	}
}

func TestGrokCLIMCPToolLoop(t *testing.T) {
	if os.Getenv("GATT_GROK_E2E") != "1" {
		t.Skip("set GATT_GROK_E2E=1 to exercise installed Grok Build")
	}
	binary, err := exec.LookPath("grok")
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls, returned := 0, false
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "synthetic endpoint only supports Responses", 404)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var body struct {
			Tools []struct{ Name string } `json:"tools"`
			Input []struct {
				Type   string          `json:"type"`
				Output json.RawMessage `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		for _, item := range body.Input {
			if item.Type == "function_call_output" && strings.Contains(string(item.Output), "COVE_MCP_RESULT_7") {
				returned = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sequence := 0
		emit := func(kind string, payload map[string]any) {
			payload["type"] = kind
			payload["sequence_number"] = sequence
			sequence++
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encode(payload))
			w.(http.Flusher).Flush()
		}
		response := map[string]any{"id": fmt.Sprintf("resp_grok_%d", calls), "object": "response", "created_at": time.Now().Unix(), "model": "fixture-model", "status": "in_progress", "output": []any{}}
		emit("response.created", map[string]any{"response": response})
		var item map[string]any
		if calls <= 2 {
			name, arguments := "search_tool", `{"query":"covefixture cove_add"}`
			if calls == 2 {
				name, arguments = "use_tool", `{"tool_name":"covefixture__cove_add","tool_input":{"a":3,"b":4}}`
				found := false
				for _, input := range body.Input {
					found = found || input.Type == "function_call_output" && strings.Contains(string(input.Output), "cove_add")
				}
				if !found {
					t.Errorf("Grok MCP discovery did not return cove_add: %s", raw)
				}
			}
			found := false
			for _, tool := range body.Tools {
				found = found || tool.Name == name
			}
			if !found {
				t.Errorf("Grok did not advertise the MCP dispatcher %s", name)
				return
			}
			item = map[string]any{"id": fmt.Sprintf("fc_grok_%d", calls), "type": "function_call", "call_id": fmt.Sprintf("call_grok_%d", calls), "name": name, "arguments": ""}
			emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
			emit("response.function_call_arguments.delta", map[string]any{"item_id": item["id"], "output_index": 0, "delta": arguments})
			item["arguments"] = arguments
			emit("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": 0, "arguments": item["arguments"]})
		} else {
			item = map[string]any{"id": "msg_grok", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "COVE_GROK_LOOP_COMPLETE", "annotations": []any{}}}}
			emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
			emit("response.output_text.delta", map[string]any{"item_id": "msg_grok", "output_index": 0, "content_index": 0, "delta": "COVE_GROK_LOOP_COMPLETE"})
		}
		emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		response["status"], response["output"] = "completed", []any{item}
		response["usage"] = map[string]any{"input_tokens": 10, "output_tokens": 20, "total_tokens": 30, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}
		emit("response.completed", map[string]any{"response": response})
	})
	root := clientRoot(t)
	home := filepath.Join(root, ".grok")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(root, "mcp.py")
	writeClient(t, server, `import json,sys
for line in sys.stdin:
 message=json.loads(line)
 if "id" not in message: continue
 method=message.get("method")
 if method=="initialize": result={"protocolVersion":message["params"]["protocolVersion"],"capabilities":{"tools":{}},"serverInfo":{"name":"covefixture","version":"1.0"}}
 elif method=="tools/list": result={"tools":[{"name":"cove_add","description":"Add two integers in an isolated acceptance fixture","inputSchema":{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]},"annotations":{"readOnlyHint":True}}]}
 elif method=="tools/call":
  args=message["params"]["arguments"]; result={"content":[{"type":"text","text":"COVE_MCP_RESULT_"+str(args["a"]+args["b"])}]}
 else: result={}
 print(json.dumps({"jsonrpc":"2.0","id":message["id"],"result":result}),flush=True)
`)
	path := filepath.Join(home, "config.toml")
	doc, err := parseClientDocument("grok", path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	config, err := doc.edit(clientDesiredFields("grok", "fixture-model", f.server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err = extensionEditMCP("grok", "covefixture", config, true, map[string]any{"command": python, "args": []string{server}}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, path, string(config))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-p", "Use cove_add for 3+4 and report the result.", "--output-format", "streaming-json", "--max-turns", "4", "--no-subagents", "--disable-web-search", "--allow", "covefixture__cove_add")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GROK_HOME=" + home, "COVE_API_KEY=" + f.key, "NO_PROXY=127.0.0.1,localhost", "NO_COLOR=1"}
	for _, group := range []string{"CLAUDE", "CURSOR"} {
		for _, item := range []string{"SKILLS", "RULES", "AGENTS", "MCPS", "HOOKS"} {
			cmd.Env = append(cmd.Env, "GROK_"+group+"_"+item+"_ENABLED=0")
		}
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Grok tool loop failed: %v\n%s", err, redact(string(output), f.key))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 3 || !returned || !strings.Contains(string(output), "COVE_GROK_LOOP_COMPLETE") {
		t.Fatalf("Grok tool loop incomplete: calls=%d returned=%v output=%s", calls, returned, redact(string(output), f.key))
	}
	t.Logf("Official Grok Build: production BYOK config, %d Cove Responses requests, deferred MCP discovery and actual stdio MCP result returned; synthetic upstream only", calls)
}

func TestQoderCLIConfigExtensions(t *testing.T) {
	if os.Getenv("GATT_QODER_E2E") != "1" {
		t.Skip("set GATT_QODER_E2E=1 to exercise installed Qoder CLI")
	}
	binary, err := exec.LookPath("qodercli")
	if err != nil {
		t.Fatal(err)
	}
	root := clientRoot(t)
	userRoot := filepath.Join(root, "user-config")
	if err := os.Mkdir(userRoot, 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	path := filepath.Join(project, ".qoder", "settings.json")
	config, err := extensionEditMCP("qodercli", "cove-fixture", nil, false, map[string]any{"type": "stdio", "command": "/usr/bin/false"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, path, string(config))
	writeClient(t, filepath.Join(project, ".qoder", "skills", "cove-fixture", "SKILL.md"), "---\nname: cove-fixture\ndescription: isolated Cove acceptance fixture\n---\nCOVE_QODER_SKILL_FIXTURE\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, action := range []string{"mcp", "skills"} {
		cmd := exec.CommandContext(ctx, binary, "--config-dir", userRoot, "--cwd", project, action, "list")
		cmd.Dir, cmd.Env = project, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "NO_COLOR=1"}
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "cove-fixture") {
			t.Fatalf("Qoder did not discover isolated %s config: %v\n%s", action, err, output)
		}
	}
	t.Log("Official Qoder CLI discovered production-generated MCP and isolated Skill; false server intentionally does not connect, zero model requests")
}

func TestGrokCLIHTTPMCPAuthentication(t *testing.T) {
	if os.Getenv("GATT_GROK_E2E") != "1" {
		t.Skip("set GATT_GROK_E2E=1 to exercise installed Grok Build")
	}
	binary, err := exec.LookPath("grok")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	authenticated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authenticated = authenticated || r.Header.Get("Authorization") == "Bearer synthetic-token" && r.Header.Get("X-Cove-Fixture") == "synthetic-header"
		mu.Unlock()
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if len(message.ID) == 0 {
			w.WriteHeader(202)
			return
		}
		result := map[string]any{}
		switch message.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": message.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "covefixture", "version": "1.0"}}
		case "tools/list":
			result["tools"] = []any{map[string]any{"name": "cove_fixture", "description": "Read-only acceptance fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
	}))
	defer server.Close()
	root := clientRoot(t)
	home := filepath.Join(root, ".grok")
	definition, err := extensionDefinition(extensionInput{Client: "grok", Transport: "http", URL: server.URL, BearerEnv: "COVE_MCP_FIXTURE_TOKEN", Headers: map[string]string{"X-Cove-Fixture": "${COVE_MCP_FIXTURE_HEADER}"}, SecretDelivery: "env_reference"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := extensionEditMCP("grok", "covefixture", nil, false, definition, true, false)
	if err != nil {
		t.Fatal(err)
	}
	writeClient(t, filepath.Join(home, "config.toml"), string(config))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp", "doctor", "covefixture", "--json")
	cmd.Dir, cmd.Env = root, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GROK_HOME=" + home, "COVE_MCP_FIXTURE_TOKEN=synthetic-token", "COVE_MCP_FIXTURE_HEADER=synthetic-header"}
	output, err := cmd.CombinedOutput()
	mu.Lock()
	defer mu.Unlock()
	if err != nil || !authenticated {
		t.Fatalf("Grok HTTP MCP did not deliver native bearer/header references: authenticated=%v error=%v output=%s", authenticated, err, output)
	}
	t.Log("Official Grok HTTP MCP delivered both generated headers and bearer_token_env_var to isolated server; zero model requests")
}
