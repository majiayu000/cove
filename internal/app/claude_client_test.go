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
)

// The installed CLI uses --bare and its own config directory; no subscription login.
func TestClaudeCLIToolLoop(t *testing.T) {
	if os.Getenv("GATT_CLAUDE_E2E") != "1" {
		t.Skip("set GATT_CLAUDE_E2E=1 to exercise installed Claude Code")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	toolReturned := false
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/count_tokens") {
			writeJSON(w, 200, map[string]int{"input_tokens": 10})
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("unexpected Claude request path: %s", r.URL.Path)
			http.Error(w, "unsupported", 404)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
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
			var content []struct {
				Type    string          `json:"type"`
				ID      string          `json:"tool_use_id"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(message.Content, &content) == nil {
				for _, block := range content {
					if block.Type == "tool_result" && block.ID == "toolu_cli" && strings.Contains(string(block.Content), "GATT_CLAUDE_TOOL_OK") {
						toolReturned = true
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event string, payload map[string]any) {
			payload["type"] = event
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		}
		emit("message_start", map[string]any{"message": map[string]any{"id": fmt.Sprintf("msg_cli_%d", calls), "type": "message", "role": "assistant", "model": "fixture-model", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0}}})
		if calls == 1 {
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_cli", "name": "Bash", "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"printf GATT_CLAUDE_TOOL_OK","description":"Print the synthetic acceptance marker"}`}})
		} else {
			emit("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "GATT_CLAUDE_LOOP_COMPLETE"}})
		}
		emit("content_block_stop", map[string]any{"index": 0})
		stop := "end_turn"
		if calls == 1 {
			stop = "tool_use"
		}
		emit("message_delta", map[string]any{"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
		emit("message_stop", map[string]any{})
	})
	src, err := f.a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.NativeProtocol = "messages"
	src.Provider = "anthropic"
	if err = f.a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	models, err := f.a.Store.models(src.ID)
	if err != nil || len(models) != 1 {
		t.Fatalf("models: %v", err)
	}
	model := models[0]
	for _, beta := range strings.Split("claude-code-20250219,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24", ",") {
		model.Features = append(model.Features, "anthropic_beta:"+beta)
	}
	if _, err = f.a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(model), model.ID); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "claude-config")
	if err = os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--bare", "--print", "--no-session-persistence", "--strict-mcp-config", "--tools", "Bash", "--allowedTools", "Bash(printf *)", "--model", "fixture-model", "Run the provided synthetic printf tool and report its result.")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + config, "ANTHROPIC_BASE_URL=" + f.server.URL, "ANTHROPIC_API_KEY=" + f.key, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "NO_COLOR=1"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude client failed: %v\n%s", err, redact(string(output), f.key))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || !toolReturned || !strings.Contains(string(output), "GATT_CLAUDE_LOOP_COMPLETE") {
		t.Fatalf("Claude tool loop incomplete: calls=%d returned=%v output=%s", calls, toolReturned, redact(string(output), f.key))
	}
	response, raw := f.request("GET", "/admin/requests", "", true)
	var recorded struct {
		Items []Record `json:"items"`
	}
	if response.StatusCode != 200 || json.Unmarshal(raw, &recorded) != nil {
		t.Fatal("cannot read CLI request records")
	}
	records := recorded.Items
	if len(records) != 2 {
		t.Fatalf("record count=%d", len(records))
	}
	for _, record := range records {
		if record.Status != "succeeded" || record.Usage.Input == nil || *record.Usage.Input != 10 || record.Usage.Output == nil || *record.Usage.Output != 20 {
			t.Fatalf("CLI usage not recorded correctly: %+v", record.Usage)
		}
	}
	t.Log("Claude Code: two native Messages streams, Bash result returned, both usage records settled")
}

func TestNativeMessagesBetaCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, beta string
		features             []string
		status               int
	}{
		{"declared_native", "messages", "test-beta-2026", []string{"anthropic_beta:test-beta-2026"}, 200},
		{"undeclared_native", "messages", "test-beta-2026", nil, 422},
		{"partially_declared", "messages", "test-beta-2026,other-beta", []string{"anthropic_beta:test-beta-2026"}, 422},
		{"converted", "responses", "test-beta-2026", []string{"anthropic_beta:test-beta-2026"}, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Anthropic-Beta") != tc.beta {
					t.Error("declared beta header was lost")
				}
				if r.Header.Get("X-Api-Key") != "test-source-secret" || r.Header.Get("Authorization") != "" {
					t.Error("native credential boundary changed")
				}
				return contractResponse(`{"id":"msg_beta","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`, "application/json"), nil
			})
			src, err := a.Store.source("source")
			if err != nil {
				t.Fatal(err)
			}
			src.NativeProtocol = tc.protocol
			src.Provider = "anthropic"
			if err = a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			models, err := a.Store.models(src.ID)
			if err != nil || len(models) != 1 {
				t.Fatalf("models: %v", err)
			}
			model := models[0]
			model.Features = tc.features
			if _, err = a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(model), model.ID); err != nil {
				t.Fatal(err)
			}
			req := contractRequest("POST", "/v1/messages", `{"model":"fixture-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, "test-client-key")
			req.Header.Set("Anthropic-Beta", tc.beta)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
			wantCalls := 0
			if tc.status == 200 {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("upstream calls=%d want=%d", calls, wantCalls)
			}
		})
	}
}
