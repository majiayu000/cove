package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const compatFinal = `{"id":"resp_compat","status":"completed","model":"fixture-model","output":[{"type":"message","content":[{"type":"output_text","text":"你好"}]},{"type":"function_call","call_id":"call_read","name":"read_file","arguments":"{\"path\":\"a.txt\"}"}],"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":4}}}`
const compatChatBody = `{"model":"fixture-model","messages":[{"role":"system","content":"Be precise"},{"role":"assistant","content":null,"tool_calls":[{"id":"old_call","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b.txt\"}"}}]},{"role":"tool","tool_call_id":"old_call","content":"old result"},{"role":"user","content":[{"type":"text","text":"Next"}]}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"tool_choice":{"type":"function","function":{"name":"read_file"}},"max_completion_tokens":128}`
const compatMessagesBody = `{"model":"fixture-model","system":[{"type":"text","text":"Be precise"}],"max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"old_call","name":"read_file","input":{"path":"b.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"old_call","content":[{"type":"text","text":"old result"}],"is_error":true},{"type":"text","text":"Next"}]}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"tool_choice":{"type":"tool","name":"read_file","disable_parallel_tool_use":true}}`

func compatCall(a *App, path, body, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := contractRequest("POST", path, body, "")
	if path == "/v1/messages" {
		r.Header.Set("X-Api-Key", token)
		r.Header.Set("Anthropic-Version", "2023-06-01")
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	a.ServeHTTP(w, r)
	return w
}

func TestCompatJSONToolsHistoryAndAccounting(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "messages"} {
		for _, transport := range []struct{ name, wire, contentType string }{{"json", compatFinal, "application/json"}, {"completed_items", compatDoneWire(), "text/event-stream"}} {
			t.Run(protocol+"/"+transport.name, func(t *testing.T) {
				path, body := "/v1/chat/completions", compatChatBody
				if protocol == "messages" {
					path, body = "/v1/messages", compatMessagesBody
				}
				var calls atomic.Int32
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-source-secret" || r.Header.Get("X-Api-Key") != "" || r.GetBody != nil {
						t.Error("upstream auth/path/replay contract changed")
					}
					b, _ := io.ReadAll(r.Body)
					var v map[string]json.RawMessage
					if json.Unmarshal(b, &v) != nil || transport.contentType == "application/json" && string(v["max_output_tokens"]) != "128" || transport.contentType == "text/event-stream" && v["max_output_tokens"] != nil || string(v["store"]) != "false" || v["messages"] != nil || !bytes.Contains(v["input"], []byte(`"call_id":"old_call"`)) || !bytes.Contains(v["input"], []byte("old result")) {
						t.Errorf("history not translated: %s", b)
					}
					if protocol == "messages" && (!bytes.Contains(v["input"], []byte(`\"is_error\":true`)) || string(v["parallel_tool_calls"]) != "false") {
						t.Error("tool failure or parallel constraint lost")
					}
					return contractResponse(transport.wire, transport.contentType), nil
				})
				if transport.contentType == "text/event-stream" {
					s, _ := a.Store.source("source")
					s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
					s.AllowParameterAdjustment = true
					if err := a.Store.saveSource(s); err != nil {
						t.Fatal(err)
					}
					if err := a.Secrets.Put(s.CredentialRef, encode(Credential{Access: "test-source-secret", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
						t.Fatal(err)
					}
				}
				w := compatCall(a, path, body, "test-client-key")
				if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
				}
				var out map[string]any
				if json.Unmarshal(w.Body.Bytes(), &out) != nil {
					t.Fatal("invalid JSON")
				}
				usage := out["usage"].(map[string]any)
				if protocol == "messages" {
					content := out["content"].([]any)
					if len(content) != 2 || out["type"] != "message" || out["stop_reason"] != "tool_use" || content[0].(map[string]any)["text"] != "你好" || content[1].(map[string]any)["id"] != "call_read" || usage["input_tokens"] != float64(8) || usage["cache_read_input_tokens"] != float64(4) {
						t.Fatalf("incorrect Messages response: %s", w.Body.String())
					}
				} else {
					choice := out["choices"].([]any)[0].(map[string]any)
					message := choice["message"].(map[string]any)
					if out["object"] != "chat.completion" || choice["finish_reason"] != "tool_calls" || message["content"] != "你好" || usage["prompt_tokens"] != float64(12) || usage["total_tokens"] != float64(15) {
						t.Fatalf("incorrect Chat response: %s", w.Body.String())
					}
				}
				r := waitRecords(t, a, 1)[0]
				if calls.Load() != 1 || r.Protocol != protocol || r.Status != "succeeded" || r.Usage.Input == nil || *r.Usage.Input != 12 {
					t.Fatalf("duplicate dispatch or incorrect accounting: %+v", r)
				}
			})
		}
	}
}

func compatWire() string {
	return "event: response.created\ndata: {\"response\":{\"id\":\"resp_compat\",\"model\":\"fixture-model\",\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"你\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"好\"}\n\n" +
		"event: response.output_item.added\ndata: {\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_read\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"output_index\":1,\"delta\":\"{\\\"path\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"output_index\":1,\"delta\":\"\\\"a.txt\\\"}\"}\n\n" +
		"event: response.completed\ndata: {\"response\":" + compatFinal + "}\n\n"
}

// Codex emits completed items separately and an empty terminal output array.
func compatDoneWire() string {
	prefix, _, _ := strings.Cut(compatWire(), "event: response.completed")
	var response responseOutput
	_ = json.Unmarshal([]byte(compatFinal), &response)
	for index, item := range response.Output {
		prefix += "event: response.output_item.done\ndata: " + encode(map[string]any{"output_index": index, "item": item}) + "\n\n"
	}
	response.Output = []responseItem{}
	return prefix + "event: response.completed\ndata: " + encode(map[string]any{"response": response}) + "\n\n"
}

type compatFragments struct{ io.Reader }

func (r compatFragments) Read(b []byte) (int, error) {
	if len(b) > 3 {
		b = b[:3]
	}
	return r.Reader.Read(b)
}

func readCompatFrames(t *testing.T, b []byte) ([]map[string]any, bool) {
	t.Helper()
	frames := []map[string]any{}
	done := false
	s := newSSE(1<<20, func(frame []byte) error {
		data, _ := sseData(frame)
		if string(data) == "[DONE]" {
			done = true
			return nil
		}
		var v map[string]any
		if json.Unmarshal(data, &v) != nil {
			t.Errorf("bad converted event: %s", frame)
		} else {
			frames = append(frames, v)
		}
		return nil
	}, func([]byte) error { t.Error("truncated converted event"); return nil })
	if err := s.Feed(b); err != nil {
		t.Fatal(err)
	}
	if err := s.End(); err != nil {
		t.Fatal(err)
	}
	return frames, done
}

func TestCompatFragmentedStreamsAndToolArguments(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "messages"} {
		for _, transport := range []struct{ name, wire string }{{"terminal_output", compatWire()}, {"completed_items", compatDoneWire()}} {
			t.Run(protocol+"/"+transport.name, func(t *testing.T) {
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					response := contractResponse("", "text/event-stream")
					response.Body = io.NopCloser(compatFragments{strings.NewReader(transport.wire)})
					return response, nil
				})
				path, body := "/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"stream":true,"stream_options":{"include_usage":true}}`
				if protocol == "messages" {
					path, body = "/v1/messages", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":128,"stream":true}`
				}
				w := compatCall(a, path, body, "test-client-key")
				if w.Code != 200 {
					t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
				}
				frames, done := readCompatFrames(t, w.Body.Bytes())
				var text, args string
				starts, stops := 0, 0
				gotID, gotUsage, gotFinish := false, false, false
				for _, v := range frames {
					if protocol == "chat_completions" {
						if v["object"] != "chat.completion.chunk" {
							t.Fatalf("native protocol escaped: %v", v)
						}
						choices := v["choices"].([]any)
						if len(choices) == 0 {
							gotUsage = v["usage"].(map[string]any)["prompt_tokens"] == float64(12)
							continue
						}
						choice := choices[0].(map[string]any)
						delta := choice["delta"].(map[string]any)
						if part, ok := delta["content"].(string); ok {
							text += part
						}
						if calls, ok := delta["tool_calls"].([]any); ok {
							for _, raw := range calls {
								call := raw.(map[string]any)
								if call["index"] != float64(0) {
									t.Error("wrong tool index")
								}
								if call["id"] == "call_read" {
									gotID = true
								}
								if fn, ok := call["function"].(map[string]any); ok {
									if part, ok := fn["arguments"].(string); ok {
										args += part
									}
								}
							}
						}
						if choice["finish_reason"] == "tool_calls" {
							gotFinish = true
						}
					} else {
						switch v["type"] {
						case "content_block_start":
							starts++
							b := v["content_block"].(map[string]any)
							if b["id"] == "call_read" {
								gotID = true
							}
						case "content_block_stop":
							stops++
						case "content_block_delta":
							delta := v["delta"].(map[string]any)
							if part, ok := delta["text"].(string); ok {
								text += part
							}
							if part, ok := delta["partial_json"].(string); ok {
								args += part
							}
						case "message_delta":
							gotFinish = v["delta"].(map[string]any)["stop_reason"] == "tool_use"
							estimate, err := estimateTokens("你好" + encode([]any{map[string]any{"id": "call_read", "type": "function", "function": map[string]string{"name": "read_file", "arguments": `{"path":"a.txt"}`}}}))
							gotUsage = err == nil && v["usage"].(map[string]any)["output_tokens"] == float64(estimate)
						}
					}
				}
				if text != "你好" || args != `{"path":"a.txt"}` || !gotID || !gotUsage || !gotFinish {
					t.Fatalf("stream lost/duplicated output: text=%q args=%q wire=%s", text, args, w.Body.String())
				}
				if protocol == "chat_completions" && !done || protocol == "messages" && (done || starts != 2 || stops != 2 || frames[0]["type"] != "message_start" || frames[len(frames)-1]["type"] != "message_stop") {
					t.Fatal("wrong stream termination")
				}
				if waitRecords(t, a, 1)[0].Status != "succeeded" {
					t.Fatal("stream not recorded as success")
				}
			})
		}
	}
}

func TestCompatSubscriptionChatNonstream(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		if !bytes.Contains(b, []byte(`"stream":true`)) || !bytes.Contains(b, []byte(`"store":false`)) {
			t.Error("subscription transport changed")
		}
		return contractResponse(compatWire(), "text/event-stream"), nil
	})
	s, _ := a.Store.source("source")
	s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
	if err := a.Store.saveSource(s); err != nil {
		t.Fatal(err)
	}
	if err := a.Secrets.Put(s.CredentialRef, encode(Credential{Access: "synthetic", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
		t.Fatal(err)
	}
	w := compatCall(a, "/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}]}`, "test-client-key")
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "event:") || !strings.Contains(w.Body.String(), `"object":"chat.completion"`) {
		t.Fatalf("nonstream subscription output: %d %s", w.Code, w.Body.String())
	}
}

func TestCompatRejectedBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(compatFinal, "application/json"), nil
	})
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":"hi"}],"n":2}`, 422},
		{"/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}]}`, 422},
		{"/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":"hi"}],"stop":["STOP"]}`, 422},
		{"/v1/messages", `{"model":"fixture-model","messages":[{"role":"user","content":"hi"}]}`, 400},
		{"/v1/messages", `{"model":"fixture-model","max_tokens":128,"messages":[{"role":"user","content":[{"type":"thinking","thinking":"private","signature":"signed"}]}]}`, 422},
		{"/v1/messages", `{"model":"fixture-model","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"system":[{"type":"text","text":"secret","cache_control":{"type":"ephemeral"}}]}`, 422},
	} {
		w := compatCall(a, tc.path, tc.body, "test-client-key")
		if w.Code != tc.status {
			t.Fatalf("unsupported request accepted: %d %s", w.Code, tc.body)
		}
		if tc.path == "/v1/messages" && !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) {
			t.Fatal("wrong Messages error shape")
		}
	}
	if calls.Load() != 0 || len(a.slots) != 0 {
		t.Fatal("invalid requests dispatched or leaked admission")
	}
	s, _ := a.Store.source("source")
	s.Kind = "codex_subscription"
	if err := a.Store.saveSource(s); err != nil {
		t.Fatal(err)
	}
	w := compatCall(a, "/v1/messages", compatMessagesBody, "test-client-key")
	if w.Code != 422 || !strings.Contains(w.Body.String(), "max_tokens") || calls.Load() != 0 {
		t.Fatal("subscription limit silently dropped")
	}
}

func TestCompatErrorsNoRetryAndNoSuccessfulTerminator(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				response := contractResponse(`{"error":{"message":"test-source-secret rejected"}}`, "application/json")
				response.StatusCode = 429
				response.Header.Set("Retry-After", "9")
				return response, nil
			})
			body := `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":128}`
			if w := compatCall(a, path, body, "wrong-key"); w.Code != 401 {
				t.Fatal("wrong key accepted")
			}
			w := compatCall(a, path, body, "test-client-key")
			if w.Code != 429 || w.Header().Get("Retry-After") != "9" || strings.Contains(w.Body.String(), "test-source-secret") || calls.Load() != 1 {
				t.Fatal("upstream error/retry/redaction contract changed")
			}
			if path == "/v1/messages" && !strings.Contains(w.Body.String(), `"type":"rate_limit_error"`) {
				t.Fatal("wrong Messages error type")
			}
			a.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
				return contractResponse("event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n", "text/event-stream"), nil
			})
			body = `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":128,"stream":true}`
			var response *httptest.ResponseRecorder
			func() {
				defer func() {
					if p := recover(); p != nil && p != http.ErrAbortHandler {
						panic(p)
					}
				}()
				response = httptest.NewRecorder()
				a.ServeHTTP(response, contractRequest("POST", path, body, "test-client-key"))
			}()
			if strings.Contains(response.Body.String(), "[DONE]") || strings.Contains(response.Body.String(), "message_stop") || !strings.Contains(response.Body.String(), `"error"`) {
				t.Fatal("interrupted stream claimed success")
			}
			rows := waitRecords(t, a, 2)
			if rows[0].Status == "succeeded" || rows[1].Status == "succeeded" {
				t.Fatal("failed requests metered as success")
			}
		})
	}
}

func TestCompatToolResultRoundTrip(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				b, _ := io.ReadAll(r.Body)
				if calls.Add(1) == 1 {
					return contractResponse(compatFinal, "application/json"), nil
				}
				var request struct {
					Input []map[string]any `json:"input"`
				}
				if json.Unmarshal(b, &request) != nil {
					t.Fatal("invalid upstream body")
				}
				found := false
				for _, v := range request.Input {
					if v["type"] == "function_call_output" && v["call_id"] == "call_read" && v["output"] == "file result" {
						found = true
					}
				}
				if !found {
					t.Error("tool ID/result did not round trip")
				}
				return contractResponse(`{"id":"resp_second","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Done"}]}],"usage":{"input_tokens":12,"output_tokens":3}}`, "application/json"), nil
			})
			first := compatCall(a, path, `{"model":"fixture-model","messages":[{"role":"user","content":"read file"}],"max_tokens":128}`, "test-client-key")
			var output map[string]any
			if json.Unmarshal(first.Body.Bytes(), &output) != nil || first.Code != 200 {
				t.Fatal("first call failed")
			}
			messages := []any{map[string]any{"role": "user", "content": "read file"}}
			if path == "/v1/messages" {
				messages = append(messages, map[string]any{"role": "assistant", "content": output["content"]}, map[string]any{"role": "user", "content": []any{map[string]string{"type": "tool_result", "tool_use_id": "call_read", "content": "file result"}}})
			} else {
				message := output["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
				messages = append(messages, message, map[string]string{"role": "tool", "tool_call_id": "call_read", "content": "file result"})
			}
			second := compatCall(a, path, encode(map[string]any{"model": "fixture-model", "messages": messages, "max_tokens": 128}), "test-client-key")
			if second.Code != 200 || !strings.Contains(second.Body.String(), "Done") || calls.Load() != 2 {
				t.Fatalf("tool continuation failed: %d %s", second.Code, second.Body.String())
			}
		})
	}
}

func TestProtocolEntrySharesExecution(t *testing.T) {
	for _, tc := range []struct{ path, protocol, body string }{
		{"/v1/responses", "responses", contractBody},
		{"/v1/chat/completions", "chat_completions", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}]}`},
		{"/v1/messages", "messages", `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":128}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			var dispatches atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				dispatches.Add(1)
				if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-source-secret" || r.GetBody != nil {
					t.Error("shared source dispatch contract changed")
				}
				return contractResponse(compatFinal, "application/json"), nil
			})
			for _, auth := range []bool{false, true} {
				body := &contractHeldBody{}
				key := "wrong"
				status := 401
				if auth {
					key = "test-client-key"
					status = 429
					a.slots <- struct{}{}
				}
				req := contractRequest("POST", tc.path, "", key)
				req.Body = body
				w := httptest.NewRecorder()
				a.ServeHTTP(w, req)
				if auth {
					<-a.slots
				}
				if w.Code != status || body.reads.Load() != 0 {
					t.Fatal("auth/capacity did not reject before body read")
				}
			}
			if w := compatCall(a, tc.path, tc.body, "test-client-key"); w.Code != 200 {
				t.Fatalf("valid request failed: %d %s", w.Code, w.Body.String())
			}
			rows := waitRecords(t, a, 1)
			var attempts int
			if err := a.Store.DB.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if dispatches.Load() != 1 || attempts != 1 || len(rows) != 1 || rows[0].KeyID != "key" || rows[0].SourceID != "source" || rows[0].Generation != 1 || rows[0].Protocol != tc.protocol {
				t.Fatal("entry forked dispatch/record/source identity")
			}
			if w := compatCall(a, tc.path, strings.Replace(tc.body, "fixture-model", "not-configured", 1), "test-client-key"); w.Code != 400 {
				t.Fatal("unconfigured model accepted")
			}
			src, _ := a.Store.source("source")
			src.Enabled = false
			if err := a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			if w := compatCall(a, tc.path, tc.body, "test-client-key"); w.Code != 503 {
				t.Fatal("disabled source accepted")
			}
			key, _ := a.Store.keyByDigest(digest("test-client-key"))
			key.Revoked = true
			if _, err := a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
				t.Fatal(err)
			}
			if w := compatCall(a, tc.path, tc.body, "test-client-key"); w.Code != 401 {
				t.Fatal("revoked key accepted")
			}
			if dispatches.Load() != 1 || len(a.slots) != 0 {
				t.Fatal("rejected call dispatched or leaked capacity")
			}
		})
		t.Run(tc.protocol+"_cancel", func(t *testing.T) {
			entered := make(chan struct{})
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				close(entered)
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.ServeHTTP(httptest.NewRecorder(), contractRequest("POST", tc.path, tc.body, "test-client-key").WithContext(ctx))
			}()
			<-entered
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancel did not stop upstream")
			}
			rows := waitRecords(t, a, 1)
			if calls.Load() != 1 || rows[0].Status != "cancelled" || rows[0].Protocol != tc.protocol || len(a.slots) != 0 {
				t.Fatal("entry changed cancellation/record contract")
			}
		})
	}
}

type compatReadFunc func([]byte) (int, error)

func (f compatReadFunc) Read(b []byte) (int, error) { return f(b) }

func TestCompatSemanticBoundaries(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		body := `{"model":"fixture-model","messages":[{"role":"user","content":"test"}],"max_tokens":128,"stream":true}`
		for _, tc := range []struct {
			name, wire string
			limit      int64
		}{
			{"conflicting_final", strings.Replace(compatWire(), `"text":"你好"`, `"text":"再见"`, 1), 0},
			{"oversize_event", "event: response.completed\ndata: {\"response\":" + strings.TrimSuffix(compatFinal, "}") + `,"padding":"` + strings.Repeat("x", 2048) + "\"}}\n\n", 0},
			{"converted_output_limit", compatWire(), 80},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					return contractResponse(tc.wire, "text/event-stream"), nil
				})
				if tc.limit > 0 {
					a.Config.MaxResponse = tc.limit
				}
				w := httptest.NewRecorder()
				func() {
					defer func() {
						if p := recover(); p != nil && p != http.ErrAbortHandler {
							panic(p)
						}
					}()
					a.ServeHTTP(w, contractRequest("POST", path, body, "test-client-key"))
				}()
				if strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "message_stop") {
					t.Fatal("unconvertible stream advertised success")
				}
				row := waitRecords(t, a, 1)[0]
				if row.Status == "succeeded" || row.ErrorStage != "protocol_conversion" {
					t.Fatalf("wrong conversion failure record: %+v", row)
				}
			})
		}
		t.Run(path+"/incremental_before_terminal", func(t *testing.T) {
			w := httptest.NewRecorder()
			prefix, terminal, _ := strings.Cut(compatWire(), "event: response.completed")
			checked := false
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				response := contractResponse("", "text/event-stream")
				final := strings.NewReader("event: response.completed" + terminal)
				response.Body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), compatReadFunc(func(b []byte) (int, error) {
					if !checked {
						checked = true
						if !w.Flushed || !strings.Contains(w.Body.String(), "你") || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "message_stop") {
							t.Error("stream buffered deltas until terminal")
						}
					}
					return final.Read(b)
				})))
				return response, nil
			})
			a.ServeHTTP(w, contractRequest("POST", path, body, "test-client-key"))
			if !checked || waitRecords(t, a, 1)[0].Status != "succeeded" {
				t.Fatal("incremental stream failed")
			}
		})
		t.Run(path+"/missing_usage_and_output_limit", func(t *testing.T) {
			final := `{"id":"resp_limit","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`
			a := contractApp(t, func(r *http.Request) (*http.Response, error) { return contractResponse(final, "application/json"), nil })
			w := compatCall(a, path, strings.Replace(body, `"stream":true`, `"stream":false`, 1), "test-client-key")
			reason := `"finish_reason":"length"`
			usage := `"usage":null`
			if path == "/v1/messages" {
				if w.Code != 502 || strings.Contains(w.Body.String(), `"input_tokens":0`) {
					t.Fatalf("unknown wire usage advertised as SDK compatible: %s", w.Body.String())
				}
				if waitRecords(t, a, 1)[0].Usage.Input != nil {
					t.Fatal("missing usage fabricated")
				}
				return
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), reason) || !strings.Contains(w.Body.String(), usage) {
				t.Fatalf("output limit/unknown usage lost: %s", w.Body.String())
			}
			if waitRecords(t, a, 1)[0].Usage.Input != nil {
				t.Fatal("missing usage fabricated")
			}
		})
	}
}

func TestCompatCompletedItemsRequireConsistentTerminal(t *testing.T) {
	prefix, terminal, _ := strings.Cut(compatDoneWire(), "event: response.completed")
	lastDone := strings.LastIndex(prefix, "event: response.output_item.done")
	cases := []struct {
		name, wire string
	}{
		{"missing_terminal", prefix},
		{"missing_completed_item", prefix[:lastDone] + "event: response.completed" + terminal},
		{"duplicate_completed_item", prefix + prefix[lastDone:] + "event: response.completed" + terminal},
		{"conflicting_full_terminal", prefix + "event: response.completed\ndata: {\"response\":" + strings.Replace(compatFinal, `"text":"你好"`, `"text":"再见"`, 1) + "}\n\n"},
		{"conflicting_delta", strings.Replace(compatDoneWire(), `"delta":"你"`, `"delta":"错"`, 1)},
		{"negative_index", strings.Replace(compatDoneWire(), `"output_index":1,"item"`, `"output_index":-1,"item"`, 1)},
		{"missing_index", strings.Replace(compatDoneWire(), `"output_index":1,`, "", 1)},
		{"sparse_index", strings.Replace(compatDoneWire(), `"output_index":1,"item"`, `"output_index":1000000,"item"`, 1)},
		{"late_completed_item", compatDoneWire() + prefix[lastDone:]},
	}
	for _, protocol := range []string{"chat_completions", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range cases {
				// A non-streaming response consumes authoritative completed items, not
				// speculative deltas. Streaming must also verify already-delivered bytes.
				if tc.name == "conflicting_delta" && !stream {
					continue
				}
				t.Run(protocol+"/"+encode(stream)+"/"+tc.name, func(t *testing.T) {
					var calls atomic.Int32
					a := contractApp(t, func(r *http.Request) (*http.Response, error) {
						calls.Add(1)
						return contractResponse(tc.wire, "text/event-stream"), nil
					})
					s, _ := a.Store.source("source")
					s.Kind, s.AuthStatus = "codex_subscription", "logged_in"
					s.AllowParameterAdjustment = true
					if err := a.Store.saveSource(s); err != nil {
						t.Fatal(err)
					}
					if err := a.Secrets.Put(s.CredentialRef, encode(Credential{Access: "synthetic", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
						t.Fatal(err)
					}
					path := "/v1/chat/completions"
					if protocol == "messages" {
						path = "/v1/messages"
					}
					body := encode(map[string]any{"model": "fixture-model", "messages": []any{map[string]string{"role": "user", "content": "test"}}, "max_tokens": 128, "stream": stream})
					w := httptest.NewRecorder()
					func() {
						defer func() {
							if p := recover(); p != nil && p != http.ErrAbortHandler {
								panic(p)
							}
						}()
						a.ServeHTTP(w, contractRequest("POST", path, body, "test-client-key"))
					}()
					if !stream && w.Code != 502 {
						t.Fatalf("unconvertible JSON advertised success: HTTP %d %s", w.Code, w.Body.String())
					}
					if stream && (strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "message_stop")) {
						t.Fatal("unconvertible stream advertised success")
					}
					row := waitRecords(t, a, 1)[0]
					if calls.Load() != 1 || row.Status == "succeeded" || row.ErrorStage != "protocol_conversion" {
						t.Fatalf("conversion failure/replay contract changed: %+v", row)
					}
				})
			}
		}
	}
}

func TestCompatCompletedItemsBounded(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c := compatOutput{stream: stream, max: 128, blocks: make(map[string]*compatBlock)}
		for index := 0; index < 2; index++ {
			frame := []byte("event: response.output_item.done\ndata: " + encode(map[string]any{"output_index": index, "item": map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": strings.Repeat("x", 32)}}}}) + "\n\n")
			if _, err := c.event(frame); (err != nil) != (index == 1) {
				t.Fatalf("completed item aggregate size bound: index=%d err=%v", index, err)
			}
		}
	}
}

func TestCompatNativeConversionFailurePreservesObservedUsage(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			name := path + "/json"
			wire := `{"id":"msg_usage","model":"fixture-model","content":[{"type":"thinking","thinking":"PRIVATE_THINKING","signature":"opaque"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":17,"output_tokens":25}}`
			contentType := "application/json"
			if stream {
				name = path + "/stream"
				contentType = "text/event-stream"
				wire = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"model\":\"fixture-model\",\"usage\":{\"input_tokens\":17}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"PRIVATE_THINKING\",\"signature\":\"opaque\"}}\n\n"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return contractResponse(wire, contentType), nil
				})
				src, err := a.Store.source("source")
				if err != nil {
					t.Fatal(err)
				}
				src.NativeProtocol = "messages"
				src.Provider = "anthropic"
				if err = a.Store.saveSource(src); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"model": "fixture-model", "max_tokens": 64, "messages": []map[string]string{{"role": "user", "content": "test"}}, "stream": stream}
				if path == "/v1/responses" {
					delete(body, "messages")
					delete(body, "max_tokens")
					body["input"] = "test"
					body["max_output_tokens"] = 64
				}
				w := httptest.NewRecorder()
				func() {
					defer func() {
						if p := recover(); p != nil && p != http.ErrAbortHandler {
							panic(p)
						}
					}()
					a.ServeHTTP(w, contractRequest("POST", path, encode(body), "test-client-key"))
				}()
				row := waitRecords(t, a, 1)[0]
				if calls.Load() != 1 || row.Status != "failed" || row.ErrorStage != "protocol_conversion" || row.Usage.Input == nil || *row.Usage.Input != 17 {
					t.Fatalf("conversion failure lost known input or changed error contract: %+v", row)
				}
				if stream {
					if row.Usage.Output != nil || row.UpstreamStatus != "unknown" || row.Completeness != "partial" || row.ObservationStatus != "partial" {
						t.Fatalf("unobserved terminal became known: %+v", row)
					}
				} else if w.Code != 502 || row.UpstreamStatus != "completed" || row.Usage.Output == nil || *row.Usage.Output != 25 || row.Completeness != "complete" {
					t.Fatalf("JSON terminal usage lost: %+v", row)
				}
				if strings.Contains(encode(row), "PRIVATE_THINKING") || strings.Contains(w.Body.String(), "PRIVATE_THINKING") {
					t.Fatal("opaque content leaked")
				}
			})
		}
	}
}

func TestCompatMessagesStreamCostRequiresObservedTerminal(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/completed=%t", path, completed), func(t *testing.T) {
				wire := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial\",\"model\":\"fixture-model\",\"usage\":{\"input_tokens\":17,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"
				if completed {
					wire += "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				var calls atomic.Int32
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					return contractResponse(wire, "text/event-stream"), nil
				})
				src, err := a.Store.source("source")
				if err != nil {
					t.Fatal(err)
				}
				src.NativeProtocol, src.Provider = "messages", "anthropic"
				src.Price = &Price{Currency: "USD", Input: "1", Output: "1"}
				if err = a.Store.saveSource(src); err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"model": "fixture-model", "max_tokens": 64, "messages": []map[string]string{{"role": "user", "content": "test"}}, "stream": true}
				if path == "/v1/responses" {
					delete(body, "messages")
					delete(body, "max_tokens")
					body["input"], body["max_output_tokens"] = "test", 64
				}
				func() {
					defer func() {
						if p := recover(); p != nil && p != http.ErrAbortHandler {
							panic(p)
						}
					}()
					a.ServeHTTP(httptest.NewRecorder(), contractRequest("POST", path, encode(body), "test-client-key"))
				}()
				row := waitRecords(t, a, 1)[0]
				if calls.Load() != 1 || row.Usage.Input == nil || *row.Usage.Input != 17 || row.Usage.Output == nil {
					t.Fatalf("observed usage lost or request replayed: %+v", row)
				}
				if completed {
					if row.Status != "succeeded" || row.UpstreamStatus != "completed" || row.Completeness != "complete" || *row.Usage.Output != 4 || row.Cost == nil {
						t.Fatalf("completed stream lost final accounting: %+v", row)
					}
				} else if row.Status == "succeeded" || row.UpstreamStatus != "unknown" || row.Completeness != "partial" || row.ObservationStatus != "partial" || *row.Usage.Output != 0 || row.Cost != nil || row.PartialCost == nil {
					t.Fatalf("stream without terminal fabricated complete accounting: %+v", row)
				}
			})
		}
	}
}
