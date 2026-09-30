package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeFixture(t *testing.T, transport contractTransport) *App {
	t.Helper()
	a := contractApp(t, transport)
	a.Config.DataDir = t.TempDir()
	a.Config.MaxEvent = 1 << 20
	a.Config.MaxResponse = 1 << 20
	// Size-limit fixtures transfer 32 MiB under race instrumentation; cancellation is driven explicitly.
	a.Config.HeaderTimeout = 30
	a.Config.IdleTimeout = 30
	a.Config.TotalTimeout = 120
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.NativeOperations = []string{"images.generate", "images.edit", "audio.transcribe", "audio.translate", "audio.speech", "embeddings", "rerank", "compact"}
	src.RerankPath = "/v2/rerank"
	src.NativeProtocol = "responses"
	src.Provider = "openai_compatible"
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	return a
}
func nativeCall(a *App, path, contentType string, body io.Reader, credential string, ctx context.Context) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:5569"+path, body)
	r.Header.Set("Authorization", "Bearer "+credential)
	r.Header.Set("Content-Type", contentType)
	if ctx != nil {
		r = r.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	if !a.nativeOperationsAPI(w, r) {
		panic("not a native operation")
	}
	return w
}
func nativeJSONCall(a *App, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	return nativeCall(a, path, "application/json", bytes.NewReader(raw), "test-client-key", nil)
}
func nativeRecord(t *testing.T, a *App, w *httptest.ResponseRecorder) Record {
	t.Helper()
	var raw string
	var record Record
	if err := a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", w.Header().Get("X-Gateway-Request-Id")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(raw), &record) != nil {
		t.Fatal("record invalid")
	}
	return record
}
func nativeResponse(raw []byte, kind string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{kind}}, Body: io.NopCloser(bytes.NewReader(raw))}
}
func nativeMultipart(t *testing.T, operation, model string) ([]byte, string, []byte) {
	t.Helper()
	var out bytes.Buffer
	writer := multipart.NewWriter(&out)
	writer.WriteField("model", model)
	writer.WriteField("prompt", "synthetic edit")
	writer.WriteField("response_format", "text")
	name, kind := "image[]", "image/png"
	image, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==")
	media := image
	if operation == "audio" {
		name, kind = "file", "audio/wav"
		media = append([]byte("RIFF\x26\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data\x02\x00\x00\x00"), 0, 0)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="`+name+`"; filename="../../synthetic.bin"`)
	header.Set("Content-Type", kind)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	part.Write(media)
	writer.Close()
	return out.Bytes(), writer.FormDataContentType(), media
}

type nativeCountingBody struct {
	reads  int
	bytes  int
	Reader io.Reader
}

func (b *nativeCountingBody) Read(p []byte) (int, error) {
	b.reads++
	n, err := b.Reader.Read(p)
	b.bytes += n
	return n, err
}
func (b *nativeCountingBody) Close() error { return nil }
func TestSpecExtendedOperationsPermissionsBeforeBodyAndDispatch(t *testing.T) {
	var calls atomic.Int32
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nativeResponse([]byte(`{"data":[{"url":"https://synthetic.invalid/image"}]}`), "application/json"), nil
	})
	for _, token := range []string{"", "test-administrator", "test-session", "invalid"} {
		body := &nativeCountingBody{Reader: strings.NewReader(`{"model":"fixture-model","prompt":"synthetic"}`)}
		w := nativeCall(a, "/v1/images/generations", "application/json", body, token, nil)
		if w.Code != 401 || body.reads != 0 {
			t.Fatalf("unauthorized body consumed code=%d reads=%d", w.Code, body.reads)
		}
	}
	key, err := a.Store.keyByDigest(digest("test-client-key"))
	if err != nil {
		t.Fatal(err)
	}
	key.OperationAllowlist = []string{}
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	body := &nativeCountingBody{Reader: strings.NewReader(`{"model":"fixture-model","prompt":"synthetic"}`)}
	w := nativeCall(a, "/v1/images/generations", "application/json", body, "test-client-key", nil)
	if w.Code != 403 || body.reads != 0 {
		t.Fatal("operation restriction consumed body")
	}
	key.OperationAllowlist = nil
	key.ModelAllowlist = []string{"allowed-only"}
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	if w = nativeJSONCall(a, "/v1/images/generations", map[string]any{"model": "fixture-model", "prompt": "synthetic"}); w.Code != 403 {
		t.Fatalf("model restriction ignored %d %s", w.Code, w.Body.String())
	}
	body = &nativeCountingBody{Reader: strings.NewReader(`{"model":"fixture-model","prompt":"` + strings.Repeat("x", 2<<20) + `"}`)}
	w = nativeCall(a, "/v1/images/generations", "application/json", body, "test-client-key", nil)
	if w.Code != 403 || body.bytes > int(nativeAdmissionLimit) {
		t.Fatal("model denial read large body")
	}
	key.ModelAllowlist = nil
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	body = &nativeCountingBody{Reader: strings.NewReader(`{"prompt":"` + strings.Repeat("x", 2<<20) + `","model":"fixture-model"}`)}
	w = nativeCall(a, "/v1/images/generations", "application/json", body, "test-client-key", nil)
	if w.Code != 422 || body.bytes > int(nativeAdmissionLimit)+1 {
		t.Fatal("late model consumed unauthenticated media")
	}
	if calls.Load() != 0 {
		t.Fatal("permission denial made an upstream call")
	}
	if len(a.slots) != 0 || a.keyActive["key"] != 0 {
		t.Fatal("rejected request leaked capacity")
	}
}
func TestSpecImagesJSONMultipartNativeBytesAndUnknownCost(t *testing.T) {
	var calls atomic.Int32
	var expectedImage []byte
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-source-secret" {
			t.Fatal("wrong credential namespace")
		}
		if r.URL.Path == "/images/edits" && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(part)
				if part.FormName() == "image[]" {
					if !bytes.Equal(raw, expectedImage) {
						t.Fatal("transparent image bytes transformed")
					}
					found = true
				}
			}
			if !found {
				t.Fatal("image part missing")
			}
		}
		return nativeResponse([]byte(`{"data":[{"b64_json":"bmF0aXZl"}],"usage":{"input_tokens":4,"output_tokens":8}}`), "application/json"), nil
	})
	w := nativeJSONCall(a, "/v1/images/generations", map[string]any{"model": "fixture-model", "prompt": "synthetic", "background": "transparent", "unknown_native_extension": map[string]int{"value": 1}})
	if w.Code != 200 {
		t.Fatalf("image generation %d %s", w.Code, w.Body.String())
	}
	record := nativeRecord(t, a, w)
	if record.Status != "succeeded" || record.Operation != "images.generate" || record.Cost != nil || record.RequestBytes == 0 || record.ResponseBytes == 0 {
		t.Fatalf("media observation fabricated cost or incomplete %+v", record)
	}
	raw, kind, image := nativeMultipart(t, "image", "fixture-model")
	expectedImage = image
	w = nativeCall(a, "/v1/images/edits", kind, bytes.NewReader(raw), "test-client-key", nil)
	if w.Code != 200 {
		t.Fatalf("multipart edit %d %s", w.Code, w.Body.String())
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
	w = nativeJSONCall(a, "/v1/images/edits", map[string]any{"model": "fixture-model", "prompt": "synthetic edit", "images": []map[string]string{{"image_url": dataURL}}})
	if w.Code != 200 {
		t.Fatalf("JSON edit %d %s", w.Code, w.Body.String())
	}
	before := calls.Load()
	for _, body := range []map[string]any{{"model": "fixture-model", "prompt": "edit", "images": []map[string]string{{"file_id": "foreign-file"}}}, {"model": "fixture-model", "prompt": "edit", "images": []map[string]string{{"image_url": "data:image/png;base64,invalid?"}}}} {
		w = nativeJSONCall(a, "/v1/images/edits", body)
		if w.Code != 422 {
			t.Fatalf("unsafe edit accepted %d", w.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid media triggered call")
	}
	entries, _ := os.ReadDir(a.Config.DataDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".native-operation-") {
			t.Fatal("temporary media retained")
		}
	}
}
func TestSpecAudioRealtimeNonRealtimeBinarySubtitlesSSE(t *testing.T) {
	speech := []byte{0xff, 0xfb, 0x90, 0x00, 0x80, 0x00, 0xff}
	sse := []byte("event: speech.audio.delta\ndata: {\"type\":\"speech.audio.delta\",\"audio\":\"/w==\"}\n\nevent: speech.audio.done\ndata: {\"type\":\"speech.audio.done\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3}}\n\n")
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/audio/transcriptions" || r.URL.Path == "/audio/translations" {
			return nativeResponse([]byte("1\n00:00:00,000 --> 00:00:02,000\nsynthetic subtitle\n"), "text/plain"), nil
		}
		raw, _ := io.ReadAll(r.Body)
		if bytes.Contains(raw, []byte(`"stream_format":"sse"`)) {
			return nativeResponse(sse, "text/event-stream"), nil
		}
		return nativeResponse(speech, "audio/mpeg"), nil
	})
	w := nativeJSONCall(a, "/v1/audio/speech", map[string]any{"model": "fixture-model", "input": "synthetic speech", "voice": "alloy", "response_format": "mp3"})
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), speech) || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatal("speech binary was converted")
	}
	record := nativeRecord(t, a, w)
	if record.Completeness != "unknown" || record.Cost != nil {
		t.Fatal("missing speech usage fabricated")
	}
	w = nativeJSONCall(a, "/v1/audio/speech", map[string]any{"model": "fixture-model", "input": "synthetic", "voice": "alloy", "stream_format": "sse"})
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), sse) || nativeRecord(t, a, w).Status != "succeeded" {
		t.Fatalf("SSE audio converted or no terminal %s", w.Body.String())
	}
	raw, kind, _ := nativeMultipart(t, "audio", "fixture-model")
	for _, path := range []string{"/v1/audio/transcriptions", "/v1/audio/translations"} {
		w = nativeCall(a, path, kind, bytes.NewReader(raw), "test-client-key", nil)
		if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "1\n00:00") || w.Header().Get("Content-Type") != "text/plain" {
			t.Fatalf("subtitle wrapped as JSON %d %s", w.Code, w.Body.String())
		}
	}
}
func TestSpecVectorsRerankBoundsScoresOrderingAndRedaction(t *testing.T) {
	var invalid atomic.Bool
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/embeddings" {
			if invalid.Load() {
				return nativeResponse([]byte(`{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[3,4]}],"usage":{"prompt_tokens":9}}`), "application/json"), nil
			}
			return nativeResponse([]byte(`{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[3,4]}],"usage":{"prompt_tokens":9,"total_tokens":9}}`), "application/json"), nil
		}
		return nativeResponse([]byte(`{"id":"rank","results":[{"index":2,"relevance_score":1.2,"document":"PRIVATE_PROVIDER_DOCUMENT"},{"index":0,"relevance_score":1.2},{"index":1,"relevance_score":-0.2}],"meta":{"billed_units":{"search_units":1}}}`), "application/json"), nil
	})
	w := nativeJSONCall(a, "/v1/embeddings", map[string]any{"model": "fixture-model", "input": []string{"one", "two"}, "dimensions": 2})
	if w.Code != 200 {
		t.Fatalf("embeddings failed %d %s", w.Code, w.Body.String())
	}
	record := nativeRecord(t, a, w)
	if record.Usage.Input == nil || *record.Usage.Input != 9 || record.Usage.Output != nil || record.Cost != nil {
		t.Fatal("embedding usage guessed")
	}
	invalid.Store(true)
	w = nativeJSONCall(a, "/v1/embeddings", map[string]any{"model": "fixture-model", "input": []string{"one", "two"}, "dimensions": 2})
	if w.Code != 502 || nativeRecord(t, a, w).Usage.Input == nil {
		t.Fatal("malformed vectors accepted or actual usage discarded")
	}
	w = nativeJSONCall(a, "/v1/rerank", map[string]any{"model": "fixture-model", "query": "synthetic", "documents": []string{"duplicate", "other", "duplicate"}})
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_PROVIDER_DOCUMENT") || strings.Contains(w.Body.String(), "duplicate") {
		t.Fatalf("rerank false return_documents leaks body %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Results []struct {
			Index int `json:"index"`
		} `json:"results"`
		Usage struct {
			Input *int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Results) != 3 || result.Results[0].Index != 0 || result.Results[1].Index != 2 || result.Results[2].Index != 1 || result.Usage.Input != nil {
		t.Fatal("rerank ties/order/unknown tokens wrong")
	}
}
func TestSpecExtendedOperationsLimitsCapabilityBudgetAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nativeResponse([]byte(`{"data":[{"url":"https://synthetic.invalid"}]}`), "application/json"), nil
	})
	huge := strings.NewReader(`{"model":"fixture-model","prompt":"` + strings.Repeat("a", int(nativeMediaLimit)) + `"}`)
	w := nativeCall(a, "/v1/images/generations", "application/json", huge, "test-client-key", nil)
	if w.Code != 413 || calls.Load() != 0 {
		t.Fatal("oversize image dispatched")
	}
	source, _ := a.Store.source("source")
	source.NativeOperations = []string{}
	a.Store.saveSource(source)
	w = nativeJSONCall(a, "/v1/images/generations", map[string]any{"model": "fixture-model", "prompt": "test"})
	if w.Code != 422 || calls.Load() != 0 {
		t.Fatal("directory implied native capability")
	}
	source.NativeOperations = []string{"images.generate"}
	a.Store.saveSource(source)
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	limit := 100
	key.Limits.TPM = &limit
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	w = nativeJSONCall(a, "/v1/images/generations", map[string]any{"model": "fixture-model", "prompt": "test"})
	if w.Code != 422 || calls.Load() != 0 {
		t.Fatal("unknown media tokens bypassed TPM")
	}
	key.Limits.TPM = nil
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	budget := Budget{ID: "budget", Name: "soft", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "100", Mode: "soft", Period: BudgetPeriod{Kind: "monthly", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	a.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES(?,?,?)", budget.ID, "instance", encode(budget))
	w = nativeJSONCall(a, "/v1/images/generations", map[string]any{"model": "fixture-model", "prompt": "test"})
	if w.Code != 422 || calls.Load() != 0 {
		t.Fatal("media bypassed unknown fee budget")
	}
}
func TestSpecCompactNativeWindowAndOpaqueOwnership(t *testing.T) {
	var calls atomic.Int32
	output := []byte(`{"id":"cmp_native","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"retained tool history"}]},{"type":"compaction","encrypted_content":"opaque-native-secret-bytes"}],"usage":{"input_tokens":5,"output_tokens":2}}`)
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Path != "/responses/compact" {
			t.Fatal("compact became ordinary generation")
		}
		return nativeResponse(output, "application/json"), nil
	})
	w := nativeJSONCall(a, "/v1/responses/compact", map[string]any{"model": "fixture-model", "input": []map[string]any{{"role": "user", "content": "synthetic history"}}})
	if w.Code != 200 || !bytes.Equal(output, w.Body.Bytes()) {
		t.Fatalf("compact window changed %d %s", w.Code, w.Body.String())
	}
	record := nativeRecord(t, a, w)
	if record.Operation != "compact" || record.Usage.Input == nil {
		t.Fatal("compact not an independent billable attempt")
	}
	input := map[string]any{"model": "fixture-model", "input": []map[string]any{{"type": "compaction", "encrypted_content": "opaque-native-secret-bytes"}}}
	w = nativeJSONCall(a, "/v1/responses/compact", input)
	if w.Code != 200 {
		t.Fatalf("same owner opaque refused %d %s", w.Code, w.Body.String())
	}
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	src, _ := a.Store.source(key.SourceID)
	history, _ := json.Marshal(input["input"])
	if err := a.validateNativeOpaqueHistory(map[string]json.RawMessage{"input": history}, key, src, "fixture-model"); err != nil {
		t.Fatal(err)
	}
	if err := a.validateNativeOpaqueHistory(map[string]json.RawMessage{"input": history}, key, src, "other-model"); err == nil {
		t.Fatal("ordinary Responses opaque hook lost model ownership")
	}
	if err := a.validateNativeOpaqueHistory(map[string]json.RawMessage{"tools": json.RawMessage(`[{"encrypted_content":"not-history"}]`)}, key, src, "fixture-model"); err != nil {
		t.Fatal("tool schema treated as opaque history")
	}
	key.ID = "otherkey"
	a.Store.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", key.ID, digest("other-secret"), key.SourceID, encode(key))
	raw, _ := json.Marshal(input)
	before := calls.Load()
	w = nativeCall(a, "/v1/responses/compact", "application/json", bytes.NewReader(raw), "other-secret", nil)
	if w.Code != 409 || calls.Load() != before {
		t.Fatal("opaque history crossed Keys")
	}
	var stored string
	a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", record.ID).Scan(&stored)
	if strings.Contains(stored, "opaque-native-secret-bytes") || strings.Contains(stored, "retained tool history") {
		t.Fatal("opaque/body persisted in record")
	}
}
func TestSpecExtendedOperationsCancellationReleasesSharedCapacity(t *testing.T) {
	started := make(chan struct{})
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- nativeCall(a, "/v1/audio/speech", "application/json", strings.NewReader(`{"model":"fixture-model","input":"synthetic","voice":"alloy"}`), "test-client-key", ctx)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native request did not start")
	}
	cancel()
	var response *httptest.ResponseRecorder
	select {
	case response = <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop native operation")
	}
	record := nativeRecord(t, a, response)
	if record.Status != "cancelled" || record.Submission != "possible" || record.Cost != nil {
		t.Fatal("cancelled submitted operation lost unknown fee")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.slots) != 0 || len(a.running) != 0 || a.keyActive["key"] != 0 {
		t.Fatal("shared lifecycle capacity leaked")
	}
}
func TestSpecExtendedOperationsOutputLimitAndMalformedMultipart(t *testing.T) {
	var calls atomic.Int32
	a := nativeFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"audio/mpeg"}}, Body: io.NopCloser(io.MultiReader(bytes.NewReader([]byte{0xff, 0xfb}), io.LimitReader(&nativeRepeatReader{}, nativeMediaLimit)))}, nil
	})
	w := nativeJSONCall(a, "/v1/audio/speech", map[string]any{"model": "fixture-model", "input": "synthetic", "voice": "alloy"})
	if w.Code != 502 || nativeRecord(t, a, w).ErrorStage != "response_limit" {
		t.Fatal("oversize output presented as complete audio")
	}
	before := calls.Load()
	w = nativeCall(a, "/v1/audio/transcriptions", "multipart/form-data; boundary=bad", strings.NewReader("broken"), "test-client-key", nil)
	if w.Code != 400 || calls.Load() != before {
		t.Fatal("malformed multipart dispatched")
	}
	if err := validateNativeMedia([]byte("not png"), "image/png", false); err == nil {
		t.Fatal("incorrect media magic accepted")
	}
	if err := validateNativeMedia([]byte("\x89PNG\r\n\x1a\n"), "image/jpeg", false); err == nil {
		t.Fatal("incorrect media MIME accepted")
	}
	if _, err := embeddingInputCount(json.RawMessage(`[[1,2],["not token"]]`)); err == nil {
		t.Fatal("mixed token arrays accepted")
	}
	if _, err := embeddingInputCount(json.RawMessage(`[]`)); err == nil {
		t.Fatal("empty embeddings accepted")
	}
	if _, err := nativePath(Source{Kind: "api_key", NativeOperations: []string{"rerank"}, RerankPath: "//outside.invalid/path"}, nativeOperationSpec{Name: "rerank"}); err == nil {
		t.Fatal("rerank endpoint escaped configured origin")
	}
}

type nativeRepeatReader struct{}

func (*nativeRepeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
