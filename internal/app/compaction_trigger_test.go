package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSpecCodexStreamCompactionOwnerAndSingleItem(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			calls := 0
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"compaction_trigger"`) {
					t.Error("native trigger removed")
				}
				frames := `event: response.created` + "\ndata: " + `{"type":"response.created","response":{"id":"cmp-codex","status":"in_progress"}}` + "\n\n"
				for i := 0; i < count; i++ {
					frames += "event: response.output_item.done\ndata: " + encode(map[string]any{"type": "response.output_item.done", "output_index": i, "item": map[string]any{"type": "compaction", "encrypted_content": "synthetic-opaque"}}) + "\n\n"
				}
				frames += "event: response.completed\ndata: " + `{"type":"response.completed","response":{"id":"cmp-codex","status":"completed","usage":{"input_tokens":8,"output_tokens":2}}}` + "\n\n"
				return contractResponse(frames, "text/event-stream"), nil
			})
			src, _ := a.Store.source("source")
			src.Kind = "codex_subscription"
			src.AuthStatus = "logged_in"
			src.NativeProtocol = "responses"
			src.NativeOperations = []string{"compact"}
			if e := a.Store.saveSource(src); e != nil {
				t.Fatal(e)
			}
			if e := a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "synthetic-access", Account: "synthetic-account", Subject: "synthetic-subject", Expires: time.Now().Add(time.Hour)})); e != nil {
				t.Fatal(e)
			}
			w := httptest.NewRecorder()
			func() {
				defer func() {
					if e := recover(); e != nil && e != http.ErrAbortHandler {
						panic(e)
					}
				}()
				a.ServeHTTP(w, contractRequest("POST", "/v1/responses", `{"model":"fixture-model","stream":true,"input":[{"role":"user","content":"history"},{"type":"compaction_trigger"}]}`, "test-client-key"))
			}()
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			rec := extendedRecord(t, a, w)
			if rec.Operation != "compact" || calls != 1 {
				t.Fatal(encode(rec), calls)
			}
			key, _ := a.Store.keyByDigest(digest("test-client-key"))
			src, _ = a.Store.source(src.ID)
			history := map[string]json.RawMessage{"input": json.RawMessage(`[{"type":"compaction","encrypted_content":"synthetic-opaque"}]`)}
			if count == 1 {
				if rec.Status != "succeeded" || a.validateNativeOpaqueHistory(history, key, src, "fixture-model") != nil {
					t.Fatal(encode(rec))
				}
				key.ID = "another-key"
				if a.validateNativeOpaqueHistory(history, key, src, "fixture-model") == nil {
					t.Fatal("opaque binding crossed keys")
				}
			} else if rec.Status == "succeeded" || a.validateNativeOpaqueHistory(history, key, src, "fixture-model") == nil {
				t.Fatal("invalid terminal qualified opaque", encode(rec))
			}
		})
	}
}
