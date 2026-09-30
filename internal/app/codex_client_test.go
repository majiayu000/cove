package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is the installed client, a synthetic provider and an isolated home. No account access.
func TestCodexCLIToolLoop(t *testing.T) {
	if os.Getenv("GATT_CODEX_E2E") != "1" {
		t.Skip("set GATT_CODEX_E2E=1 to exercise installed Codex CLI")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	toolReturned := false
	shapes := []string{}
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		defer mu.Unlock()
		calls++
		var tools []map[string]json.RawMessage
		_ = json.Unmarshal(body["tools"], &tools)
		var inputs []map[string]json.RawMessage
		_ = json.Unmarshal(body["input"], &inputs)
		for _, item := range inputs {
			var kind string
			_ = json.Unmarshal(item["type"], &kind)
			if kind == "additional_tools" {
				var more []map[string]json.RawMessage
				_ = json.Unmarshal(item["tools"], &more)
				tools = append(tools, more...)
			}
		}
		execAdvertised := false
		for _, tool := range tools {
			var kind, name string
			_ = json.Unmarshal(tool["type"], &kind)
			_ = json.Unmarshal(tool["name"], &name)
			shapes = append(shapes, kind+":"+name)
			if kind == "namespace" && name == "functions" {
				var nested []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				}
				_ = json.Unmarshal(tool["tools"], &nested)
				for _, n := range nested {
					shapes = append(shapes, n.Type+":"+n.Name)
					if n.Type == "custom" && n.Name == "exec" {
						execAdvertised = true
					}
				}
			}
		}
		w.Header()["Content-Type"] = nil // Reproduce the subscription endpoint without a MIME header.
		emit := func(kind string, payload map[string]any) {
			payload["type"] = kind
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encode(payload))
			w.(http.Flusher).Flush()
		}
		responseID := fmt.Sprintf("resp_cli_%d", calls)
		emit("response.created", map[string]any{"response": map[string]any{"id": responseID, "object": "response", "status": "in_progress", "output": []any{}}})
		if calls == 1 {
			if !execAdvertised {
				t.Errorf("Luna client did not advertise functions.exec: %v", shapes)
				return
			}
			code := `const r = await tools.exec_command({cmd:"printf GATT_TOOL_OK",max_output_tokens:100}); text(r);`
			item := map[string]any{"type": "custom_tool_call", "id": "ctc_cli", "call_id": "call_cli", "name": "exec", "namespace": "functions", "input": ""}
			emit("response.output_item.added", map[string]any{"output_index": 0, "item": item})
			emit("response.custom_tool_call_input.delta", map[string]any{"output_index": 0, "item_id": "ctc_cli", "delta": code})
			item["input"] = code
			emit("response.custom_tool_call_input.done", map[string]any{"output_index": 0, "item_id": "ctc_cli", "input": code})
			emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
			emit("response.completed", map[string]any{"response": map[string]any{"id": responseID, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
			return
		}
		var input []map[string]json.RawMessage
		_ = json.Unmarshal(body["input"], &input)
		for _, item := range input {
			var kind, call string
			_ = json.Unmarshal(item["type"], &kind)
			_ = json.Unmarshal(item["call_id"], &call)
			output := string(item["output"])
			if kind == "custom_tool_call_output" && call == "call_cli" && strings.Contains(output, "GATT_TOOL_OK") {
				toolReturned = true
			}
		}
		item := map[string]any{"id": "msg_cli", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "GATT_TOOL_LOOP_COMPLETE", "annotations": []any{}}}}
		emit("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_cli", "type": "message", "role": "assistant", "content": []any{}}})
		emit("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_cli", "delta": "GATT_TOOL_LOOP_COMPLETE"})
		emit("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		emit("response.completed", map[string]any{"response": map[string]any{"id": responseID, "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 7, "total_tokens": 27}}})
	})
	f.a.mu.Lock()
	src := f.source
	src.Models = []string{"gpt-5.6-luna"}
	src.Kind, src.AuthStatus = "codex_subscription", "logged_in"
	if err := f.vault.Put(src.CredentialRef, encode(Credential{Access: "synthetic-access", Account: "synthetic-account", Expires: time.Now().Add(time.Hour)})); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	f.a.mu.Unlock()
	dir := t.TempDir()
	original := f.a.handler
	f.a.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(b)))
			var input struct {
				Tools []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"tools"`
			}
			_ = json.Unmarshal(b, &input)
			t.Logf("client tool declaration shapes: %+v", input.Tools)
			var shapes struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(b, &shapes)
			for _, item := range shapes.Input {
				var kind, role string
				_ = json.Unmarshal(item["type"], &kind)
				_ = json.Unmarshal(item["role"], &role)
				keys := []string{}
				for k := range item {
					keys = append(keys, k)
				}
				t.Logf("input shape type=%s role=%s keys=%v", kind, role, keys)
				if kind == "additional_tools" {
					var declarations []map[string]json.RawMessage
					_ = json.Unmarshal(item["tools"], &declarations)
					for _, tool := range declarations {
						var kind, name string
						_ = json.Unmarshal(tool["type"], &kind)
						_ = json.Unmarshal(tool["name"], &name)
						t.Logf("declaration type=%s name=%s", kind, name)
						var nested []struct {
							Type string `json:"type"`
							Name string `json:"name"`
						}
						_ = json.Unmarshal(tool["tools"], &nested)
						t.Logf("namespace children=%+v", nested)
					}
				}
			}
		}
		original.ServeHTTP(w, r)
	})
	home := filepath.Join(dir, "codex-home")
	if err = os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`model_provider = "gatt"
model = "gpt-5.6-luna"
web_search = "disabled"
model_context_window = 128000
model_auto_compact_token_limit = 120000
cli_auth_credentials_store = "ephemeral"
[features]
apply_patch_freeform = false
[model_providers.gatt]
name = "Gatt fixture"
base_url = %q
env_key = "GATT_TEST_CLIENT_KEY"
wire_api = "responses"
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`, f.server.URL+"/v1")
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "exec", "--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only", "--cd", dir, "Run the provided synthetic printf tool and report its result.")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "CODEX_HOME=" + home, "GATT_TEST_CLIENT_KEY=" + f.key, "NO_COLOR=1"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex client failed: %v\n%s", err, redact(string(output), f.key))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || !toolReturned || !strings.Contains(string(output), "GATT_TOOL_LOOP_COMPLETE") {
		t.Fatalf("tool loop incomplete: calls=%d returned=%v\n%s", calls, toolReturned, output)
	}
	t.Logf("Codex CLI: 2 HTTP Responses requests; function output returned; advertised tools: %v", shapes)
}
