package app

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpecTokenizer(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Text  string `json:"text"`
			Count int64  `json:"count"`
		} `json:"cases"`
	}
	b, err := os.ReadFile("tokenizer-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Cases {
		n, e := estimateTokens(v.Text)
		if e != nil || n != v.Count {
			t.Fatalf("encoding differs from official reference: %q got %d want %d (%v)", v.Text, n, v.Count, e)
		}
	}
}
func TestSpecSchemaIsolation(t *testing.T) {
	dir := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(dir, "gatt.db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE important(value TEXT);INSERT INTO important VALUES('preserve');PRAGMA user_version=1"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if _, e = OpenStore(dir); e == nil || !strings.Contains(e.Error(), "独立 data_dir") {
		t.Fatalf("old schema silently accepted: %v", e)
	}
	db, e = sql.Open("sqlite3", filepath.Join(dir, "gatt.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var value string
	if e = db.QueryRow("SELECT value FROM important").Scan(&value); e != nil || value != "preserve" {
		t.Fatal("old data changed")
	}
}
func TestSpecModels(t *testing.T) {
	var generated atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer SENTINEL_UPSTREAM_SECRET" {
				t.Error("wrong discovery auth")
			}
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"id": "discovered-model"}}})
			return
		}
		generated.Add(1)
		writeJSON(w, 200, map[string]any{"id": "r", "status": "completed"})
	})
	r, b := f.request("POST", "/admin/sources/source/models/discover", `{}`, true)
	if r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	var op Operation
	json.Unmarshal(b, &op)
	r.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for op.State == "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		r, b = f.request("GET", "/admin/operations/"+op.ID, "", true)
		json.Unmarshal(b, &op)
		r.Body.Close()
	}
	if op.State != "succeeded" || generated.Load() != 0 {
		t.Fatalf("discovery generated or failed: %+v", op)
	}
	models, e := f.a.Store.models("source")
	if e != nil || len(models) != 2 {
		t.Fatalf("models %v %v", models, e)
	}
	for _, m := range models {
		if m.UpstreamModel == "discovered-model" && (m.Enabled || m.Verification != "unverified") {
			t.Fatal("discovery claimed routable/verified")
		}
	}
}
func TestSpecKeys(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(contractJSON, "application/json"), nil
	})
	k, e := a.Store.keyByDigest(digest("test-client-key"))
	if e != nil {
		t.Fatal(e)
	}
	k.ModelAllowlist = []string{}
	k.Version = 1
	if _, e = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID); e != nil {
		t.Fatal(e)
	}
	if w := compatCall(a, "/v1/responses", contractBody, "test-client-key"); w.Code != 403 {
		t.Fatalf("empty allowlist accepted: %d", w.Code)
	}
	k.ModelAllowlist = nil
	k.ProtocolAllowlist = []string{"responses"}
	expired := time.Now().Add(-time.Second)
	k.ExpiresAt = &expired
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID)
	if w := compatCall(a, "/v1/responses", contractBody, "test-client-key"); w.Code != 401 {
		t.Fatal("expired Key accepted")
	}
	k.ExpiresAt = nil
	k.Limits.RPM = new(int)
	*k.Limits.RPM = 1
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID)
	if w := compatCall(a, "/v1/responses", contractBody, "test-client-key"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := compatCall(a, "/v1/responses", contractBody, "test-client-key"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("RPM not enforced")
	}
	if calls.Load() != 1 {
		t.Fatalf("denied requests dispatched %d", calls.Load())
	}
}
func TestSpecRouting(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(contractJSON, "application/json"), nil
	})
	src, _ := a.Store.source("source")
	src.Models = append(src.Models, "other-model")
	if e := a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	models, _ := a.Store.models(src.ID)
	route := Route{RoutePolicy: RoutePolicy{AllowCrossModel: true}, ID: "route", Enabled: true, Version: 1, Strategy: "weighted_round_robin", MaxAttempts: 1}
	for _, m := range models {
		weight := 1
		if m.UpstreamModel == "other-model" {
			weight = 3
		}
		route.Members = append(route.Members, RouteMember{ModelID: m.ID, Weight: weight})
	}
	a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?)", route.ID, encode(route))
	a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", "coding", route.ID, encode(Alias{PublicModel: "coding", RouteID: route.ID, Version: 1}))
	key := ClientKey{RouteID: route.ID}
	counts := map[string]int{}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := 0; i < 10; i++ {
		if _, _, _, e := a.selectSource(key, "coding", "responses", true); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 40; i++ {
		_, model, _, e := a.selectSource(key, "coding", "responses", false)
		if e != nil {
			t.Fatal(e)
		}
		counts[model]++
	}
	if counts["fixture-model"] != 10 || counts["other-model"] != 30 {
		t.Fatalf("smooth weights not exact: %v", counts)
	}
}
func TestSpecProviderContracts(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			var calls int
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if strings.Contains(string(b), "SENTINEL_") {
					t.Error("credentials leaked to model body")
				}
				if protocol == "messages" {
					if r.URL.Path != "/messages" || r.Header.Get("X-Api-Key") != "SENTINEL_UPSTREAM_SECRET" || r.Header.Get("Authorization") != "" {
						t.Error("wrong native Messages transport")
					}
					writeJSON(w, 200, map[string]any{"id": "msg", "type": "message", "model": "fixture-model", "stop_reason": "end_turn", "content": []any{map[string]string{"type": "text", "text": "OK"}}, "usage": map[string]int{"input_tokens": 7, "output_tokens": 2}})
				} else {
					if r.URL.Path != "/chat/completions" {
						t.Error("Chat-only source got Responses")
					}
					writeJSON(w, 200, map[string]any{"id": "chat", "object": "chat.completion", "model": "fixture-model", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "OK"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 2}})
				}
			})
			src, _ := f.a.Store.source("source")
			src.NativeProtocol = protocol
			if e := f.a.Store.saveSource(src); e != nil {
				t.Fatal(e)
			}
			body := `{"model":"fixture-model","input":"Hi","max_output_tokens":20}`
			resp, b := f.request("POST", "/v1/responses", body, false)
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(b), `"status":"completed"`) {
				t.Fatalf("cross native failed %d %s", resp.StatusCode, b)
			}
			nativePath := "/v1/chat/completions"
			nativeBody := `{"model":"fixture-model","messages":[{"role":"user","content":"Hi"}],"provider_extension":{"x":true}}`
			if protocol == "messages" {
				nativePath = "/v1/messages"
				nativeBody = `{"model":"fixture-model","max_tokens":20,"messages":[{"role":"user","content":"Hi"}],"provider_extension":{"x":true}}`
			}
			resp, b = f.request("POST", nativePath, nativeBody, false)
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(string(b), "OK") || calls != 2 {
				t.Fatalf("native failed %d %s", resp.StatusCode, b)
			}
			rows := waitRecords(t, f.a, 2)
			for _, row := range rows {
				if row.Usage.Input == nil || *row.Usage.Input != 7 || row.Status != "succeeded" {
					t.Fatalf("native accounting lost %+v", row)
				}
			}
		})
	}
}
func TestSpecNativeStreams(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			wire := `data: {"id":"chat","model":"fixture-model","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n" + `data: {"id":"chat","model":"fixture-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}` + "\n\ndata: [DONE]\n\n"
			if protocol == "messages" {
				wire = "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg","model":"fixture-model","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\nevent: content_block_start\ndata: " + `{"index":0,"content_block":{"type":"text","text":""}}` + "\n\nevent: content_block_delta\ndata: " + `{"index":0,"delta":{"type":"text_delta","text":"Hi"}}` + "\n\nevent: content_block_stop\ndata: {\"index\":0}\n\nevent: message_delta\ndata: " + `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: {}\n\n"
			}
			a := contractApp(t, func(r *http.Request) (*http.Response, error) { return contractResponse(wire, "text/event-stream"), nil })
			src, _ := a.Store.source("source")
			src.NativeProtocol = protocol
			a.Store.saveSource(src)
			w := compatCall(a, "/v1/responses", `{"model":"fixture-model","input":"Hi","stream":true,"max_output_tokens":20}`, "test-client-key")
			if !strings.Contains(w.Body.String(), "response.output_text.delta") || !strings.Contains(w.Body.String(), "response.completed") {
				t.Fatalf("native stream lost canonical events: %s", w.Body.String())
			}
			row := waitRecords(t, a, 1)[0]
			if row.Status != "succeeded" || row.Usage.Output == nil || *row.Usage.Output != 1 {
				t.Fatalf("wrong native terminal %+v", row)
			}
		})
	}
}
func TestSpecCodexAdjustments(t *testing.T) {
	src := Source{Kind: "codex_subscription", AllowParameterAdjustment: false}
	body := `{"model":"fixture-model","max_tokens":8,"messages":[{"role":"user","content":"Hi"}]}`
	if _, _, e := compatInput([]byte(body), "messages", src); e == nil {
		t.Fatal("adjustment not opt-in")
	}
	src.AllowParameterAdjustment = true
	out, adapter, e := compatInput([]byte(body), "messages", src)
	if e != nil || out["max_output_tokens"] != nil || !strings.Contains(encode(adapter.adjustments), "output_limit_not_enforced") {
		t.Fatalf("wrong disclosed adjustment %v %v", out, e)
	}
	if _, _, e = compatInput([]byte(strings.Replace(body, `"max_tokens":8`, `"max_tokens":0`, 1)), "messages", src); e == nil {
		t.Fatal("cache warmup became generation")
	}
}

var _ = httptest.NewRecorder
