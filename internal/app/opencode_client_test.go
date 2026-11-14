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

// The installed CLI reads the production-generated project opencode.json; its
// home is isolated and the upstream is synthetic. No account access.
func TestOpenCodeCLIGeneratedConfigToolLoop(t *testing.T) {
	if os.Getenv("GATT_OPENCODE_E2E") != "1" {
		t.Skip("set GATT_OPENCODE_E2E=1 to exercise installed OpenCode")
	}
	binary, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	toolReturned := false
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected OpenCode request path: %s", r.URL.Path)
			http.Error(w, "unsupported", 404)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role       string          `json:"role"`
				ToolCallID string          `json:"tool_call_id"`
				Content    json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(raw, &body) != nil {
			t.Error("invalid client JSON")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		for _, message := range body.Messages {
			if message.Role == "tool" && message.ToolCallID == "call_cli" && strings.Contains(string(message.Content), "GATT_OPENCODE_TOOL_OK") {
				toolReturned = true
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta map[string]any, finish any, usage any) {
			chunk := map[string]any{"id": fmt.Sprintf("chatcmpl_cli_%d", calls), "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
			if usage != nil {
				chunk["usage"] = usage
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		usage := map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
		if !toolReturned {
			arguments := `{"command":"printf GATT_OPENCODE_TOOL_OK","description":"Print the synthetic acceptance marker"}`
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_cli", "type": "function", "function": map[string]any{"name": "bash", "arguments": arguments}}}}, nil, nil)
			emit(map[string]any{}, "tool_calls", usage)
		} else {
			emit(map[string]any{"role": "assistant", "content": "GATT_OPENCODE_LOOP_COMPLETE"}, nil, nil)
			emit(map[string]any{}, "stop", usage)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	src, err := f.a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.NativeProtocol = "chat_completions"
	if err = f.a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Permission and update settings belong to the user's own file; Cove only adds its fields.
	before := `{"$schema":"https://opencode.ai/config.json","autoupdate":false,"share":"disabled","permission":{"bash":"allow"}}`
	configPath := filepath.Join(dir, "opencode.json")
	document, err := parseClientDocument("opencode", configPath, []byte(before), true)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := document.edit(clientDesiredFields("opencode", "fixture-model", f.server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(configPath, generated, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "Run the provided synthetic printf tool and report its result.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, ".config"), "XDG_DATA_HOME=" + filepath.Join(dir, ".data"), "XDG_CACHE_HOME=" + filepath.Join(dir, ".cache"), "XDG_STATE_HOME=" + filepath.Join(dir, ".state"), "COVE_API_KEY=" + f.key, "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "NO_COLOR=1"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenCode client failed: %v\n%s", err, redact(string(output), f.key))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 || !toolReturned || !strings.Contains(string(output), "GATT_OPENCODE_LOOP_COMPLETE") {
		t.Fatalf("OpenCode tool loop incomplete: calls=%d returned=%v output=%s", calls, toolReturned, redact(string(output), f.key))
	}
	t.Logf("OpenCode: %d Chat Completions streams through Cove, bash result returned", calls)
}
