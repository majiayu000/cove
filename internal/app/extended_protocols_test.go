package app

import (
	"bytes"
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

const geminiTextRequest = `{"contents":[{"role":"user","parts":[{"text":"synthetic input"}]}],"generationConfig":{"temperature":0}}`
const geminiTextResponse = `{"responseId":"provider-response","modelVersion":"fixture-model","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"synthetic result"}]},"finishReason":"STOP","safetyRatings":[]}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3,"cachedContentTokenCount":2}}`
const signedGeminiPart = `{"functionCall":{"id":"call-one","name":"lookup","args":{"q":"one"}},"thoughtSignature":"SYNTHETIC_SIGNATURE"}`

func extendedApp(t *testing.T, upstream contractTransport) *App {
	t.Helper()
	a := contractApp(t, upstream)
	if err := initializeExtendedProtocols(a.Store.DB); err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.Config.DataDir = dir
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.NativeProtocol = "gemini"
	src.Provider = "gemini"
	src.NativeOperations = []string{"generate", "count_tokens"}
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	candidate, err := a.candidateFor(src, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	candidate.Model.OpaqueHistory = true
	candidate.Model.NativeServerTools = []string{"googleSearch", "codeExecution"}
	candidate.Model.AdapterVersion = "synthetic-card-1"
	if _, err = a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(candidate.Model), candidate.Model.ID); err != nil {
		t.Fatal(err)
	}
	return a
}
func extendedGeminiCall(a *App, method, path, body, key string, headers map[string]string) *httptest.ResponseRecorder {
	r := contractRequest(method, path, body, key)
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	a.geminiData(w, r)
	return w
}
func extendedRecord(t *testing.T, a *App, w *httptest.ResponseRecorder) Record {
	t.Helper()
	var raw string
	if err := a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", w.Header().Get("X-Gateway-Request-Id")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var rec Record
	if json.Unmarshal([]byte(raw), &rec) != nil {
		t.Fatal("record malformed")
	}
	return rec
}
func extendedStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
}

func TestSpecGeminiNativeAuthAndPermissions(t *testing.T) {
	var calls atomic.Int32
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.RawQuery != "" || r.Header.Get("X-Goog-Api-Key") != "test-source-secret" || r.Header.Get("Authorization") != "" {
			t.Fatal("client/upstream credential isolation failed")
		}
		if r.URL.Path != "/v1beta/models/fixture-model:generateContent" {
			t.Fatal(r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte("test-client-key")) {
			t.Fatal("client key forwarded")
		}
		return contractResponse(geminiTextResponse, "application/json"), nil
	})
	path := "/v1beta/models/fixture-model:generateContent"
	w := extendedGeminiCall(a, "POST", path, geminiTextRequest, "", map[string]string{"X-Goog-Api-Key": "test-client-key"})
	extendedStatus(t, w, 200)
	if w.Body.String() != geminiTextResponse {
		t.Fatal("native response modified")
	}
	rec := extendedRecord(t, a, w)
	if rec.Status != "succeeded" || rec.Usage.Cached == nil || *rec.Usage.Cached != 2 {
		t.Fatalf("usage/status: %+v", rec)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path+"?key=NEVER_ECHO_QUERY_SECRET", geminiTextRequest, "test-client-key", nil), 400)
	w = extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", map[string]string{"X-Goog-Api-Key": "other"})
	extendedStatus(t, w, 401)
	extendedStatus(t, extendedGeminiCall(a, "POST", path, `{"model":"wrong","contents":[{"parts":[{"text":"x"}]}]}`, "test-client-key", nil), 400)
	if calls.Load() != 1 {
		t.Fatal("rejected requests dispatched")
	}
	key, err := a.Store.keyByDigest(digest("test-client-key"))
	if err != nil {
		t.Fatal(err)
	}
	key.OperationAllowlist = []string{"count_tokens"}
	key.ProtocolAllowlist = []string{"gemini"}
	if _, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil), 403)
	if calls.Load() != 1 {
		t.Fatal("generation allowed by count-only key")
	}
}

func TestSpecGeminiCountTokensIndependent(t *testing.T) {
	var calls int
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, ":countTokens") {
			t.Fatal(r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"model":"models/fixture-model"`)) {
			t.Fatal(string(raw))
		}
		return contractResponse(`{"totalTokens":37,"cachedContentTokenCount":3}`, "application/json"), nil
	})
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	key.OperationAllowlist = []string{"count_tokens"}
	if _, err := a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	path := "/v1beta/models/fixture-model:countTokens"
	body := `{"generateContentRequest":{"model":"models/fixture-model","contents":[{"parts":[{"text":"count synthetic"}]}]}}`
	w := extendedGeminiCall(a, "POST", path, body, "test-client-key", nil)
	extendedStatus(t, w, 200)
	rec := extendedRecord(t, a, w)
	if rec.Operation != "count_tokens" || rec.CountedTokens == nil || *rec.CountedTokens != 37 || rec.Usage.Input != nil || rec.Usage.Output != nil {
		t.Fatalf("count was generation usage: %+v", rec)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path, strings.Replace(body, "models/fixture-model", "models/wrong", 1), "test-client-key", nil), 400)
	key.OperationAllowlist = []string{"generate"}
	_, _ = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	extendedStatus(t, extendedGeminiCall(a, "POST", path, body, "test-client-key", nil), 403)
	if calls != 1 {
		t.Fatal("count permission or conflicting model dispatched")
	}
}

func TestSpecGeminiSignedHistoryIsolation(t *testing.T) {
	var calls int
	response := `{"candidates":[{"index":0,"content":{"role":"model","parts":[` + signedGeminiPart + `]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"second"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5}}`
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return contractResponse(response, "application/json"), nil
	})
	path := "/v1beta/models/fixture-model:generateContent"
	w := extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if extendedRecord(t, a, w).Completeness != "partial" {
		t.Fatal("partial usage invented")
	}
	history := `{"contents":[{"role":"model","parts":[` + signedGeminiPart + `]},{"role":"user","parts":[{"functionResponse":{"id":"call-one","name":"lookup","response":{"value":"one"}}}]}]}`
	w = extendedGeminiCall(a, "POST", path, history, "test-client-key", nil)
	extendedStatus(t, w, 200)
	changed := strings.Replace(history, `"q":"one"`, `"q":"edited"`, 1)
	extendedStatus(t, extendedGeminiCall(a, "POST", path, changed, "test-client-key", nil), 409)
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	key.ID = "other-key"
	_, err := a.Store.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", key.ID, digest("other-client"), key.SourceID, encode(key))
	if err != nil {
		t.Fatal(err)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path, history, "other-client", nil), 409)
	src, _ := a.Store.source("source")
	src.AccountGeneration++
	_, err = a.Store.DB.Exec("UPDATE accounts SET generation=?,data=json_set(data,'$.generation',?) WHERE id=?", src.AccountGeneration, src.AccountGeneration, src.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path, history, "test-client-key", nil), 409)
	if calls != 2 {
		t.Fatal("unbound signatures dispatched")
	}
	var stored string
	_ = a.Store.DB.QueryRow("SELECT group_concat(part_hash) FROM gemini_history_bindings").Scan(&stored)
	if strings.Contains(stored, "SYNTHETIC_SIGNATURE") {
		t.Fatal("raw signature persisted")
	}
}

func TestSpecGeminiParallelToolsAndStream(t *testing.T) {
	stream := "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"A\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"index\":1,\"content\":{\"parts\":[{\"text\":\"B\"}]}},{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n" +
		"data: {\"candidates\":[{\"index\":1,\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":2}}\n\n"
	var calls int
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("alt") != "sse" {
			t.Fatal("SSE parameter missing")
		}
		return contractResponse(stream, "text/event-stream"), nil
	})
	a.Config.MaxEvent = 4096
	body := `{"contents":[{"role":"model","parts":[{"functionCall":{"id":"first","name":"lookup","args":{"q":"a"}}},{"functionCall":{"id":"second","name":"lookup","args":{"q":"b"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"second","name":"lookup","response":{"v":"b"}}},{"functionResponse":{"id":"first","name":"lookup","response":{"v":"a"}}}]}]}`
	path := "/v1beta/models/fixture-model:streamGenerateContent?alt=sse"
	w := extendedGeminiCall(a, "POST", path, body, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if w.Body.String() != stream {
		t.Fatal("events were rebuilt or candidate contents overwritten")
	}
	rec := extendedRecord(t, a, w)
	if rec.Status != "succeeded" || rec.Usage.Input == nil || *rec.Usage.Input != 11 {
		t.Fatalf("stream terminal: %+v", rec)
	}
	unsafe := strings.ReplaceAll(strings.ReplaceAll(body, `"id":"first",`, ""), `"id":"second",`, "")
	extendedStatus(t, extendedGeminiCall(a, "POST", path, unsafe, "test-client-key", nil), 400)
	if calls != 1 {
		t.Fatal("ambiguous name-only parallel results dispatched")
	}
}

func TestSpecGeminiSafetyAndIncompleteStream(t *testing.T) {
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":8}}`, "application/json"), nil
	})
	w := extendedGeminiCall(a, "POST", "/v1beta/models/fixture-model:generateContent", geminiTextRequest, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if extendedRecord(t, a, w).UpstreamStatus != "safety_blocked" {
		t.Fatal("native safety block dropped")
	}
	a.HTTP.Transport = contractTransport(func(r *http.Request) (*http.Response, error) {
		return contractResponse("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n", "text/event-stream"), nil
	})
	w = extendedGeminiCall(a, "POST", "/v1beta/models/fixture-model:streamGenerateContent", geminiTextRequest, "test-client-key", nil)
	if extendedRecord(t, a, w).Status == "succeeded" {
		t.Fatal("EOF accepted without candidate terminal")
	}
}

func enableExtendedCache(t *testing.T, a *App) {
	t.Helper()
	settings, _ := a.Store.responseCacheSettings()
	settings.SourceIDs = []string{"source"}
	_, err := a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('response_cache',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(settings))
	if err != nil {
		t.Fatal(err)
	}
}
func TestSpecServerToolsCacheGeminiOptInAndReplay(t *testing.T) {
	var calls atomic.Int32
	a := extendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			return contractResponse("data: "+geminiTextResponse+"\n\n", "text/event-stream"), nil
		}
		return contractResponse(geminiTextResponse, "application/json"), nil
	})
	path := "/v1beta/models/fixture-model:generateContent"
	for i := 0; i < 2; i++ {
		extendedStatus(t, extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil), 200)
	}
	if calls.Load() != 2 {
		t.Fatal("default cache saved/replayed output")
	}
	var entries int
	_ = a.Store.DB.QueryRow("SELECT count(*) FROM response_cache").Scan(&entries)
	if entries != 0 {
		t.Fatal("default privacy changed")
	}
	enableExtendedCache(t, a)
	w := extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if w.Header().Get("X-Cove-Cache") != "miss" {
		t.Fatal(w.Header())
	}
	first := extendedRecord(t, a, w)
	w = extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if calls.Load() != 3 || w.Header().Get("X-Cove-Cache") != "hit" {
		t.Fatal("identical eligible request failed to replay")
	}
	rec := extendedRecord(t, a, w)
	if !rec.CacheHit || rec.Usage.Input != nil || rec.CachedResultUsage == nil || rec.CachedResultUsage.Input == nil || *rec.CachedResultUsage.Input != 12 || rec.Cost == nil || *rec.Cost != "0" || rec.UpstreamStatus != "not_called" || rec.CacheOriginRequestID != first.ID || !cachedResponseID(rec.ResponseID) {
		t.Fatalf("replay accounting: %+v", rec)
	}
	if !strings.Contains(w.Body.String(), `"promptTokenCount":12`) {
		t.Fatal("provider wire usage was replaced")
	}
	w = extendedGeminiCall(a, "POST", "/v1beta/models/fixture-model:streamGenerateContent", geminiTextRequest, "test-client-key", nil)
	extendedStatus(t, w, 200)
	if calls.Load() != 4 || w.Header().Get("X-Cove-Cache") == "hit" || !strings.HasPrefix(w.Body.String(), "data: ") {
		t.Fatal("stream request replayed nonstream cache")
	}
	files, err := os.ReadDir(filepath.Join(a.Config.DataDir, ".response-cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatal("cache index/directory mismatch")
	}
	assertPrivateTestPermissions(t, filepath.Join(a.Config.DataDir, ".response-cache", files[0].Name()), 0600)
	toolBody := strings.TrimSuffix(geminiTextRequest, "}") + `,"tools":[{"googleSearch":{}}]}`
	extendedStatus(t, extendedGeminiCall(a, "POST", path, toolBody, "test-client-key", nil), 200)
	if calls.Load() != 5 {
		t.Fatal("tool request reused cache")
	}
	if err = os.WriteFile(filepath.Join(a.Config.DataDir, ".response-cache", files[0].Name()), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	extendedStatus(t, extendedGeminiCall(a, "POST", path, geminiTextRequest, "test-client-key", nil), 200)
	if calls.Load() != 6 {
		t.Fatal("corruption was served or caused extra attempts")
	}
	a.mu.Lock()
	err = a.clearResponseCache()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	files, _ = os.ReadDir(filepath.Join(a.Config.DataDir, ".response-cache"))
	_ = a.Store.DB.QueryRow("SELECT count(*) FROM response_cache").Scan(&entries)
	if len(files) != 0 || entries != 0 {
		t.Fatal("clear left bodies/index")
	}
}

func TestSpecServerToolsCacheWireBytesAndRevocation(t *testing.T) {
	pretty := []byte("{\n  \"responseId\":\"pretty\", \"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"<汉字>&\"}]}}]\n}")
	a := extendedApp(t, nil)
	enableExtendedCache(t, a)
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	src, _ := a.Store.source("source")
	candidate, _ := a.candidateFor(src, "fixture-model")
	rec := Record{ID: "original-pretty", Origin: "client", KeyID: key.ID, SourceID: src.ID, Generation: src.Generation, AccountGeneration: src.AccountGeneration, Protocol: "gemini", Operation: "generate", Model: "fixture-model", SentModel: "fixture-model", ConfigVersion: 1, Status: "succeeded", DeliveryStatus: "completed"}
	d, err := a.responseCacheDescriptor(key, src, candidate.Model, rec, []byte(geminiTextRequest), http.Header{})
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if err = a.storeResponseCache(d, pretty, "application/json", rec); err != nil {
		t.Fatal(err)
	}
	entry, err := a.lookupResponseCache(d)
	if err != nil || entry == nil || !bytes.Equal(entry.Body, pretty) {
		t.Fatal("cache envelope changed whitespace/Unicode/body hash", err)
	}
	key.Revoked = true
	_, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err = a.lookupResponseCache(d)
	if err != nil || entry != nil {
		t.Fatal("revoked Key replayed saved result", err)
	}
}

func TestSpecServerToolsCacheIsolationAndEligibility(t *testing.T) {
	a := extendedApp(t, nil)
	enableExtendedCache(t, a)
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	src, _ := a.Store.source("source")
	candidate, _ := a.candidateFor(src, "fixture-model")
	rec := Record{Protocol: "gemini", Operation: "generate", Model: "public", SentModel: "fixture-model", ConfigVersion: 1, Price: src.Price}
	base, err := a.responseCacheDescriptor(key, src, candidate.Model, rec, []byte(geminiTextRequest), http.Header{})
	if err != nil || base == nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ClientKey, *Source, *SourceModel, *Record){func(k *ClientKey, s *Source, m *SourceModel, r *Record) { k.ID = "different" }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { s.AccountGeneration++ }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { s.Generation++ }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { r.SentModel = "other" }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { r.Model = "other-public" }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { m.AdapterVersion = "new" }, func(k *ClientKey, s *Source, m *SourceModel, r *Record) { r.ConfigVersion++ }} {
		k, s, m, r := key, src, candidate.Model, rec
		mutate(&k, &s, &m, &r)
		d, e := a.responseCacheDescriptor(k, s, m, r, []byte(geminiTextRequest), http.Header{})
		if e != nil || d == nil || d.CacheKey == base.CacheKey {
			t.Fatal("cache isolation collapsed")
		}
	}
	for _, body := range []string{strings.Replace(geminiTextRequest, `"temperature":0`, `"temperature":0.1`, 1), strings.TrimSuffix(geminiTextRequest, "}") + `,"tools":[]}`, strings.TrimSuffix(geminiTextRequest, "}") + `,"cachedContent":"foreign"}`, strings.Replace(geminiTextRequest, `{"text":"synthetic input"}`, signedGeminiPart, 1), strings.TrimSuffix(geminiTextRequest, "}") + `,"unknown":true}`} {
		if responseCacheEligible([]byte(body), "gemini", "generate") {
			t.Fatal("unsafe body cached: " + body)
		}
	}
	if responseCacheEligible([]byte(geminiTextRequest), "gemini", "count_tokens") {
		t.Fatal("count operation cached as generation")
	}
	aJSON, err := canonicalCacheJSON([]byte(`{"b":[1.0,2],"a":"汉字"}`))
	if err != nil {
		t.Fatal(err)
	}
	bJSON, _ := canonicalCacheJSON([]byte(`{"a":"汉字","b":[1.0,2]}`))
	if !bytes.Equal(aJSON, bJSON) || !bytes.Contains(aJSON, []byte("1.0")) {
		t.Fatal("canonical JSON changed number/text semantics")
	}
	if _, err = canonicalCacheJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate keys cached")
	}
}

func TestSpecServerToolsCacheResponseShapes(t *testing.T) {
	for _, item := range []struct{ protocol, body string }{
		{"responses", `{"model":"m","input":"hello","temperature":0,"store":false}`},
		{"chat_completions", `{"model":"m","messages":[{"role":"user","content":"hello"}],"temperature":0}`},
		{"messages", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"temperature":0,"max_tokens":10}`},
	} {
		if !responseCacheEligible([]byte(item.body), item.protocol, "generate") {
			t.Fatal("eligible text rejected: " + item.protocol)
		}
		for _, field := range []string{`,"tools":[]`, `,"stream":true`, `,"previous_response_id":"old"`, `,"background":true`, `,"conversation":"state"`} {
			body := strings.TrimSuffix(item.body, "}") + field + "}"
			if responseCacheEligible([]byte(body), item.protocol, "generate") {
				t.Fatal("unsafe field cached: " + field)
			}
		}
	}
	for _, item := range []struct{ protocol, body string }{
		{"responses", `{"status":"completed","output":[{"type":"function_call","name":"run"}]}`},
		{"gemini", `{"candidates":[{"finishReason":"STOP","content":{"parts":[` + signedGeminiPart + `]}}]}`},
		{"messages", `{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"call"}]}`},
		{"chat_completions", `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{}]}}]}`},
	} {
		if responseCacheOutputEligible([]byte(item.body), item.protocol) {
			t.Fatal("opaque/tool output cached")
		}
	}
}

func TestSpecServerToolsCacheSettingsTTLAndLRU(t *testing.T) {
	a := extendedApp(t, nil)
	call := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := contractRequest(method, "/admin/response-cache", body, "test-session")
		if !a.extendedProtocolsAPI(w, r) {
			t.Fatal("handler not selected")
		}
		return w
	}
	extendedStatus(t, call("PUT", `{"version":1,"source_ids":["source"],"route_ids":[],"ttl_minutes":5}`), 400)
	extendedStatus(t, call("PUT", `{"version":1,"source_ids":["source"],"route_ids":[],"ttl_minutes":5,"save_output_consent":true}`), 200)
	extendedStatus(t, call("PUT", `{"version":1,"source_ids":[],"route_ids":[],"ttl_minutes":5}`), 409)
	extendedStatus(t, call("PUT", `{"version":2,"source_ids":[],"route_ids":[],"ttl_minutes":61}`), 400)
	root, err := a.responseCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	now := time.Now().UTC()
	for i, name := range []string{"cache_old.json", "cache_new.json", "cache_expired.json"} {
		if err = root.WriteFile(name, []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
		expiry := now.Add(time.Hour)
		if i == 2 {
			expiry = now.Add(-time.Minute)
		}
		_, err = a.Store.DB.Exec("INSERT INTO response_cache(cache_key,filename,size,expires_at,last_access) VALUES(?,?,?,?,?)", name, name, responseCacheLimit/2, expiry.Format(cacheTimeFormat), now.Add(time.Duration(i)*time.Second).Format(cacheTimeFormat))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = a.pruneResponseCache(root, now, 1); err != nil {
		t.Fatal(err)
	}
	var count int
	_ = a.Store.DB.QueryRow("SELECT count(*) FROM response_cache").Scan(&count)
	if count != 1 {
		t.Fatalf("TTL/LRU rows=%d", count)
	}
	if _, err = root.Stat("cache_new.json"); err != nil {
		t.Fatal("newest LRU entry removed")
	}
	extendedStatus(t, call("DELETE", ""), 200)
	files, _ := os.ReadDir(filepath.Join(a.Config.DataDir, ".response-cache"))
	if len(files) != 0 {
		t.Fatal("clear directory mismatch")
	}
}

func TestSpecServerToolsCacheNativeCapabilityCards(t *testing.T) {
	var body map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"tools":[{"type":"web_search","search_context_size":"low"}]}`), &body)
	src := Source{Kind: "api_key", Provider: "openai", NativeProtocol: "responses"}
	m := SourceModel{}
	m.NativeServerTools = []string{"web_search"}
	if err := validateExtendedServerTools(body, src, m); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file_search", "code_interpreter", "unknown_search"} {
		_ = json.Unmarshal([]byte(`{"tools":[{"type":"`+kind+`"}]}`), &body)
		if validateExtendedServerTools(body, src, m) == nil {
			t.Fatal("resource/unknown tool accepted")
		}
	}
	_ = json.Unmarshal([]byte(`{"tools":[{"type":"web_search"}]}`), &body)
	src.Kind = "codex_subscription"
	if validateExtendedServerTools(body, src, m) == nil {
		t.Fatal("subscription inherited API tool card")
	}
	src.Kind = "api_key"
	m.NativeServerTools = nil
	if validateExtendedServerTools(body, src, m) == nil {
		t.Fatal("missing model capability accepted")
	}
	input, output := int64(1), int64(2)
	rate := Price{Currency: "USD", Input: "1", Output: "2"}
	rec := Record{Usage: Usage{Input: &input, Output: &output}, Price: &rate}
	rec.Cost = estimate(rec.Usage, rec.Price)
	rec.ToolCostStatus = "unknown"
	finalizeExtendedCost(&rec)
	if rec.Cost != nil || rec.PartialCost == nil {
		t.Fatal("unknown tool cost reported free/complete")
	}
}

func responsesExtendedApp(t *testing.T, upstream contractTransport) *App {
	t.Helper()
	a := extendedApp(t, upstream)
	src, _ := a.Store.source("source")
	src.NativeProtocol = "responses"
	src.Provider = "openai"
	if err := a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	candidate, _ := a.candidateFor(src, "fixture-model")
	candidate.Model.NativeServerTools = []string{"web_search"}
	candidate.Model.Price = &Price{Currency: "USD", Input: "1", Output: "2"}
	if _, err := a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(candidate.Model), candidate.Model.ID); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestSpecServerToolsCacheResponsesGatewayIntegration(t *testing.T) {
	var calls int
	text := `{"id":"original","status":"completed","model":"fixture-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"cached text","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":3}}`
	a := responsesExtendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return contractResponse(text, "application/json"), nil
	})
	enableExtendedCache(t, a)
	body := `{"model":"fixture-model","input":"cache this synthetic","temperature":0,"store":false}`
	w := contractCall(a, "POST", "/v1/responses", body, "test-client-key")
	extendedStatus(t, w, 200)
	w = contractCall(a, "POST", "/v1/responses", body, "test-client-key")
	extendedStatus(t, w, 200)
	if calls != 1 || w.Header().Get("X-Cove-Cache") != "hit" {
		t.Fatalf("Responses hook missing: calls=%d header=%v", calls, w.Header())
	}
	rec := extendedRecord(t, a, w)
	if !rec.CacheHit || rec.Cost == nil || *rec.Cost != "0" || rec.Usage.Input != nil {
		t.Fatalf("cache recharged usage: %+v", rec)
	}
	w = contractCall(a, "POST", "/v1/responses", `{"model":"fixture-model","input":"next","previous_response_id":"`+rec.ResponseID+`"}`, "test-client-key")
	extendedStatus(t, w, 409)
	if calls != 1 || !strings.Contains(w.Body.String(), "缓存响应") {
		t.Fatal("cached response continued or error did not explain replay")
	}
}
func TestSpecServerToolsCacheResponsesNativeToolIntegration(t *testing.T) {
	var calls int
	response := `{"id":"native-search","status":"completed","model":"fixture-model","output":[{"type":"web_search_call","id":"ws","status":"completed","action":{"type":"search","query":"synthetic"}},{"type":"message","content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://synthetic.invalid","title":"synthetic"}]}]}],"usage":{"input_tokens":12,"output_tokens":3}}`
	a := responsesExtendedApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"type":"web_search"`)) {
			t.Fatal("native tool altered")
		}
		return contractResponse(response, "application/json"), nil
	})
	enableExtendedCache(t, a)
	body := `{"model":"fixture-model","input":"synthetic search","temperature":0,"tools":[{"type":"web_search","search_context_size":"low"}]}`
	for i := 0; i < 2; i++ {
		w := contractCall(a, "POST", "/v1/responses", body, "test-client-key")
		extendedStatus(t, w, 200)
		if w.Body.String() != response {
			t.Fatal("native citations/tool result altered")
		}
		rec := extendedRecord(t, a, w)
		if rec.ToolCostStatus != "unknown" || rec.Cost != nil || rec.PartialCost == nil {
			t.Fatalf("tool fee omitted: %+v", rec)
		}
	}
	if calls != 2 {
		t.Fatal("server tool result cached")
	}
	candidate, _ := a.candidateFor(mustExtendedSource(t, a), "fixture-model")
	candidate.Model.NativeServerTools = nil
	_, _ = a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(candidate.Model), candidate.Model.ID)
	extendedStatus(t, contractCall(a, "POST", "/v1/responses", body, "test-client-key"), 422)
	if calls != 2 {
		t.Fatal("missing capability dispatched")
	}
}
func mustExtendedSource(t *testing.T, a *App) Source {
	t.Helper()
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	return src
}
