package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSpecGeminiHTTPConversions(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		t.Run("GeminiTo"+protocol, func(t *testing.T) {
			response := `{"id":"resp-test","status":"completed","model":"fixture-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":12,"output_tokens":3}}`
			path := "/responses"
			if protocol == "chat_completions" {
				path = "/chat/completions"
				response = `{"id":"chat-test","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`
			}
			if protocol == "messages" {
				path = "/messages"
				response = `{"id":"msg-test","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":3}}`
			}
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != path {
					t.Errorf("upstream path %s want %s", r.URL.Path, path)
				}
				raw, _ := io.ReadAll(r.Body)
				if strings.Contains(string(raw), "contents") || strings.Contains(string(raw), "test-client-key") {
					t.Error("wrong protocol or leaked credential")
				}
				return contractResponse(response, "application/json"), nil
			})
			src, _ := a.Store.source("source")
			src.NativeProtocol = protocol
			if err := a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":32}}`
			w := contractCall(a, "POST", "/v1beta/models/fixture-model:generateContent", body, "test-client-key")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"hello"`) {
				t.Fatal(w.Code, w.Body.String())
			}
			rec := extendedRecord(t, a, w)
			if rec.Status != "succeeded" || rec.Protocol != "gemini" || rec.Usage.Input == nil || *rec.Usage.Input != 12 {
				t.Fatal(encode(rec))
			}
		})
		t.Run(protocol+"ToGemini", func(t *testing.T) {
			a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(r.Body)
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) != nil || len(body["contents"]) == 0 || len(body["model"]) > 0 || len(body["stream"]) > 0 {
					t.Error("bad Gemini wire", string(raw))
				}
				if r.URL.Path != "/v1beta/models/fixture-model:generateContent" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Goog-Api-Key") != "test-source-secret" {
					t.Error("wrong endpoint/auth")
				}
				return contractResponse(geminiTextResponse, "application/json"), nil
			})
			path := "/v1/responses"
			body := `{"model":"fixture-model","input":"hello","max_output_tokens":32}`
			if protocol == "chat_completions" {
				path = "/v1/chat/completions"
				body = `{"model":"fixture-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`
			}
			if protocol == "messages" {
				path = "/v1/messages"
				body = `{"model":"fixture-model","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`
			}
			w := contractCall(a, "POST", path, body, "test-client-key")
			if w.Code != 200 || !strings.Contains(w.Body.String(), "synthetic result") {
				t.Fatal(w.Code, w.Body.String())
			}
			rec := extendedRecord(t, a, w)
			if rec.Status != "succeeded" || rec.Protocol != protocol || rec.Usage.Output == nil || *rec.Usage.Output != 3 {
				t.Fatal(encode(rec))
			}
		})
	}
}
