package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resourceFixture(t *testing.T, transport contractTransport) *App {
	t.Helper()
	a := contractApp(t, transport)
	a.Config.DataDir = t.TempDir()
	a.Config.HeaderTimeout = 30
	a.Config.IdleTimeout = 30
	a.Config.TotalTimeout = 120
	a.Config.MaxResponse = 8 << 20
	if _, e := a.Store.DB.Exec(ResourceOperationsSchema); e != nil {
		t.Fatal(e)
	}
	src, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	src.NativeOperations = []string{"files", "background", "batch", "compact"}
	src.NativeProtocol = "responses"
	src.Provider = "openai_compatible"
	src.Verification = Verification{Status: "passed", Capabilities: []string{"background"}}
	src.Price = &Price{Currency: "USD", Input: "1", Output: "1"}
	if e = a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	return a
}

func TestSpecResourcesKeyRevocationCancelsOnlyOwnedAdmissions(t *testing.T) {
	a := contractApp(t, nil)
	a.Config.MaxConcurrent = 2
	a.slots = make(chan struct{}, 2)
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	key, err := a.Store.keyByDigest(digest("test-client-key"))
	if err != nil {
		t.Fatal(err)
	}
	other := key
	other.ID = "other-key"
	if _, err = a.Store.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", other.ID, digest("synthetic-other-key"), src.ID, encode(other)); err != nil {
		t.Fatal(err)
	}
	admit := func(key ClientKey, token string) (context.Context, func()) {
		r := httptest.NewRequest("GET", "/v1/responses/synthetic", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		_, ctx, release, err := a.resourceAdmit(r, key, src, "background")
		if err != nil {
			t.Fatal(err)
		}
		return ctx, release
	}
	owned, releaseOwned := admit(key, "test-client-key")
	defer releaseOwned()
	unrelated, releaseOther := admit(other, "synthetic-other-key")
	defer releaseOther()
	w := lifecycleAdmin(a, "DELETE", "/admin/client-keys/"+key.ID, encode(map[string]any{"version": key.Version, "cancel_active": true}), "")
	if w.Code != 200 || !errors.Is(owned.Err(), context.Canceled) || unrelated.Err() != nil {
		t.Fatalf("Key revocation failed or cancelled another owner: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Requested []string `json:"cancel_requested"`
		Execution string   `json:"upstream_execution"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Requested) != 1 || result.Execution != "unknown" {
		t.Fatalf("revocation misrepresented provider execution: %s", w.Body.String())
	}
}
func resourceResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func resourceRequest(a *App, method, path, contentType, token string, reader io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:5569"+path, reader)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	if !a.resourceOperationsAPI(w, r) {
		panic("unknown resource fixture path")
	}
	return w
}
func resourceUploadBody(t *testing.T, purpose, content, kind string) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	writer.WriteField("purpose", purpose)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="../../synthetic.txt"`)
	header.Set("Content-Type", kind)
	part, e := writer.CreatePart(header)
	if e != nil {
		t.Fatal(e)
	}
	part.Write([]byte(content))
	writer.Close()
	return buffer.Bytes(), writer.FormDataContentType()
}
func resourceUploadFixture(t *testing.T, a *App, purpose, content string) string {
	t.Helper()
	body, kind := resourceUploadBody(t, purpose, content, "text/plain")
	w := resourceRequest(a, "POST", "/v1/files", kind, "test-client-key", bytes.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	var value map[string]any
	json.Unmarshal(w.Body.Bytes(), &value)
	public, _ := value["id"].(string)
	if !strings.HasPrefix(public, "file-cove-") {
		t.Fatal("native file ID exposed")
	}
	return public
}
func resourceOtherKey(t *testing.T, a *App) ClientKey {
	t.Helper()
	key, e := a.Store.keyByDigest(digest("test-client-key"))
	if e != nil {
		t.Fatal(e)
	}
	key.ID = "other-key"
	if _, e = a.Store.DB.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", key.ID, digest("other-secret"), key.SourceID, encode(key)); e != nil {
		t.Fatal(e)
	}
	return key
}
func resourceBudget(t *testing.T, a *App, mode string) {
	t.Helper()
	budget := Budget{ID: "resource-budget", Name: "synthetic budget", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "10", Mode: mode, Period: BudgetPeriod{Kind: "calendar_day", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	tx, e := a.Store.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if e = saveBudget(tx, budget); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
}
func resourceBackgroundCall(a *App, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://127.0.0.1:5569/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-client-key")
	var input map[string]json.RawMessage
	json.Unmarshal([]byte(body), &input)
	w := httptest.NewRecorder()
	a.backgroundResponses(w, r, input)
	return w
}

func TestSpecFilesPermissionsAndUnsupportedNoBodyOrProvider(t *testing.T) {
	var calls atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return resourceResponse(`{"id":"file-native"}`), nil
	})
	for _, token := range []string{"", "invalid", "test-administrator", "test-session"} {
		body := &nativeCountingBody{Reader: strings.NewReader("private synthetic")}
		w := resourceRequest(a, "POST", "/v1/files", "multipart/form-data", token, body)
		if w.Code != 401 || body.bytes != 0 {
			t.Fatal("unauthorized upload consumed body")
		}
	}
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	key.OperationAllowlist = []string{}
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	body := &nativeCountingBody{Reader: strings.NewReader("private synthetic")}
	w := resourceRequest(a, "POST", "/v1/files", "multipart/form-data", "test-client-key", body)
	if w.Code != 403 || body.bytes != 0 {
		t.Fatal("operation denied after body")
	}
	key.OperationAllowlist = nil
	a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	src, _ := a.Store.source("source")
	src.Kind = "codex_subscription"
	a.Store.saveSource(src)
	body = &nativeCountingBody{Reader: strings.NewReader("private synthetic")}
	w = resourceRequest(a, "POST", "/v1/files", "multipart/form-data", "test-client-key", body)
	if w.Code != 422 || body.bytes != 0 || calls.Load() != 0 {
		t.Fatal("subscription inherited native files")
	}
}
func TestSpecFilesCRUDMappingRangeAndNoPathSpool(t *testing.T) {
	var posts, deletes atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			posts.Add(1)
			_, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if e != nil {
				t.Fatal(e)
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			found := false
			for {
				p, e := reader.NextPart()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				raw, _ := io.ReadAll(p)
				if p.FormName() == "file" {
					found = string(raw) == "synthetic file bytes"
				}
			}
			if !found {
				t.Fatal("file altered")
			}
			return resourceResponse(`{"id":"file-native-1","object":"file","bytes":20,"filename":"synthetic.txt","purpose":"user_data","status":"processed"}`), nil
		case r.Method == "DELETE":
			deletes.Add(1)
			return resourceResponse(`{"id":"file-native-1","deleted":true}`), nil
		case strings.HasSuffix(r.URL.Path, "/content"):
			if r.Header.Get("Range") != "" {
				return &http.Response{StatusCode: 206, Header: http.Header{"Content-Type": []string{"application/octet-stream"}, "Content-Range": []string{"bytes 0-2/20"}}, Body: io.NopCloser(strings.NewReader("syn"))}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: io.NopCloser(strings.NewReader("synthetic file bytes"))}, nil
		default:
			return resourceResponse(`{"id":"file-native-1","object":"file","purpose":"user_data","status":"processed"}`), nil
		}
	})
	public := resourceUploadFixture(t, a, "user_data", "synthetic file bytes")
	w := resourceRequest(a, "GET", "/v1/files?purpose=user_data&limit=1", "", "test-client-key", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "file-native-1") {
		t.Fatal("list fetched/leaked native account inventory")
	}
	w = resourceRequest(a, "GET", "/v1/files/"+public, "", "test-client-key", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "file-native-1") {
		t.Fatal("get native ID not mapped")
	}
	w = resourceRequest(a, "GET", "/v1/files/"+public+"/content", "", "test-client-key", nil)
	if w.Code != 200 || w.Body.String() != "synthetic file bytes" {
		t.Fatal("content binary changed")
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:5569/v1/files/"+public+"/content", nil)
	r.Header.Set("Authorization", "Bearer test-client-key")
	r.Header.Set("Range", "bytes=0-2")
	w = httptest.NewRecorder()
	a.resourceOperationsAPI(w, r)
	if w.Code != 206 || w.Body.String() != "syn" {
		t.Fatal("native Range not forwarded")
	}
	w = resourceRequest(a, "DELETE", "/v1/files/"+public, "", "test-client-key", nil)
	if w.Code != 200 || deletes.Load() != 1 || posts.Load() != 1 {
		t.Fatal("CRUD counts wrong")
	}
	entries, _ := os.ReadDir(a.Config.DataDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".resource-spool-") {
			t.Fatal("upload retained private file")
		}
	}
}
func TestSpecFilesOwnershipRestartAndAccountGeneration(t *testing.T) {
	var calls atomic.Int32
	provider := contractTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return resourceResponse(`{"id":"file-owner","status":"processed","purpose":"user_data"}`), nil
	})
	a := resourceFixture(t, provider)
	public := resourceUploadFixture(t, a, "user_data", "synthetic")
	resourceOtherKey(t, a)
	before := calls.Load()
	for _, method := range []string{"GET", "DELETE"} {
		w := resourceRequest(a, method, "/v1/files/"+public, "", "other-secret", nil)
		if w.Code != 404 || calls.Load() != before {
			t.Fatal("other Key learned or reached resource")
		}
	}
	w := resourceRequest(a, "GET", "/v1/files", "", "other-secret", nil)
	if strings.Contains(w.Body.String(), public) {
		t.Fatal("cross Key file listed")
	}
	var seq int
	var name, path string
	if e := a.Store.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	a.Store.DB.Close()
	store, e := OpenStore(filepath.Dir(path))
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	restored, e := New(a.Config, store, a.Secrets, nil)
	if e != nil {
		t.Fatal(e)
	}
	restored.HTTP.Transport = provider
	w = resourceRequest(restored, "GET", "/v1/files/"+public, "", "test-client-key", nil)
	if w.Code != 200 {
		t.Fatalf("resource vanished on restart %d %s", w.Code, w.Body.String())
	}
	src, _ := restored.Store.source("source")
	restored.Store.DB.Exec("UPDATE accounts SET data=json_set(data,'$.generation',generation+1),generation=generation+1 WHERE id=?", src.AccountID)
	before = calls.Load()
	w = resourceRequest(restored, "GET", "/v1/files/"+public, "", "test-client-key", nil)
	if w.Code != 409 || calls.Load() != before {
		t.Fatal("resource crossed account generation")
	}
}
func TestSpecFilesLimitsMIMEAndOrphanBindingFailure(t *testing.T) {
	var posts, deletes atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			deletes.Add(1)
			return resourceResponse(`{"deleted":true}`), nil
		}
		posts.Add(1)
		return resourceResponse(`{"id":"file-orphan"}`), nil
	})
	w := resourceRequest(a, "POST", "/v1/files", "multipart/form-data", "test-client-key", strings.NewReader(strings.Repeat("x", int(nativeMediaLimit)+1)))
	if w.Code != 413 || posts.Load() != 0 {
		t.Fatal("oversize upload reached provider")
	}
	body, kind := resourceUploadBody(t, "user_data", "not a PNG", "image/png")
	w = resourceRequest(a, "POST", "/v1/files", kind, "test-client-key", bytes.NewReader(body))
	if w.Code != 400 || posts.Load() != 0 {
		t.Fatal("MIME mismatch uploaded")
	}
	if _, e := a.Store.DB.Exec("CREATE TRIGGER resource_binding_failure BEFORE INSERT ON resources BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END"); e != nil {
		t.Fatal(e)
	}
	body, kind = resourceUploadBody(t, "user_data", "synthetic", "text/plain")
	w = resourceRequest(a, "POST", "/v1/files", kind, "test-client-key", bytes.NewReader(body))
	if w.Code != 503 || posts.Load() != 1 || deletes.Load() != 1 || !a.storageFailed.Load() {
		t.Fatalf("orphan not reported/cleaned %d posts=%d deletes=%d", w.Code, posts.Load(), deletes.Load())
	}
}
func TestSpecBackgroundReadonlyPollRestartCASBudget(t *testing.T) {
	var posts, gets atomic.Int32
	provider := contractTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			posts.Add(1)
			return resourceResponse(`{"id":"resp-background","status":"queued"}`), nil
		}
		gets.Add(1)
		return resourceResponse(`{"id":"resp-background","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}`), nil
	})
	a := resourceFixture(t, provider)
	resourceBudget(t, a, "soft")
	w := resourceBackgroundCall(a, `{"model":"fixture-model","input":"synthetic","background":true,"max_output_tokens":8}`)
	if w.Code != 200 || posts.Load() != 1 {
		t.Fatalf("background create %d %s", w.Code, w.Body.String())
	}
	request := w.Header().Get("X-Gateway-Request-Id")
	record, e := resourceReadRecord(a.Store.DB, request)
	if e != nil || record.Ended != nil || record.Status != "queued" {
		t.Fatal("queued treated as terminal success")
	}
	var status string
	a.Store.DB.QueryRow("SELECT status FROM reservations WHERE request_id=?", request).Scan(&status)
	if status != "pending" {
		t.Fatal("background reservation released on HTTP end")
	}
	var seq int
	var name, path string
	a.Store.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path)
	a.Store.DB.Close()
	store, e := OpenStore(filepath.Dir(path))
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	restored, e := New(a.Config, store, a.Secrets, nil)
	if e != nil {
		t.Fatal(e)
	}
	restored.HTTP.Transport = provider
	restored.Store.DB.Exec("UPDATE jobs SET next_poll_at=?", time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano))
	if e = restored.pollResourceJobs(context.Background()); e != nil {
		t.Fatal(e)
	}
	record, e = resourceReadRecord(store.DB, request)
	if e != nil || record.Status != "succeeded" || record.Cost == nil {
		t.Fatalf("restart final import %v %#v", e, record)
	}
	store.DB.QueryRow("SELECT status FROM reservations WHERE request_id=?", request).Scan(&status)
	if status != "settled" {
		t.Fatal("provider final evidence did not settle restarted reservation")
	}
	firstCost := *record.Cost
	w = resourceRequest(restored, "GET", "/v1/responses/resp-background", "", "test-client-key", nil)
	if w.Code != 200 {
		t.Fatalf("repeat observe %d %s", w.Code, w.Body.String())
	}
	again, _ := resourceReadRecord(store.DB, request)
	if again.Cost == nil || *again.Cost != firstCost || again.Version != record.Version || posts.Load() != 1 || gets.Load() != 2 {
		t.Fatal("poll imported same terminal twice or replayed POST")
	}
	resourceOtherKey(t, restored)
	before := gets.Load()
	w = resourceRequest(restored, "GET", "/v1/responses/resp-background", "", "other-secret", nil)
	if w.Code != 404 || gets.Load() != before {
		t.Fatal("background crossed Key")
	}
}
func TestSpecBackgroundUnknownUsageCancelOnceAndVerifiedCapability(t *testing.T) {
	var submits, cancels atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
			return resourceResponse(`{"id":"resp-cancel","status":"cancelled"}`), nil
		}
		submits.Add(1)
		return resourceResponse(`{"id":"resp-cancel","status":"in_progress"}`), nil
	})
	resourceBudget(t, a, "soft")
	src, _ := a.Store.source("source")
	src.Verification.Status = "untested"
	a.Store.saveSource(src)
	w := resourceBackgroundCall(a, `{"model":"fixture-model","input":"synthetic","background":true}`)
	if w.Code != 422 || submits.Load() != 0 {
		t.Fatal("configured unverified background passed")
	}
	src.Verification.Status = "passed"
	a.Store.saveSource(src)
	w = resourceBackgroundCall(a, `{"model":"fixture-model","input":"synthetic","background":true,"store":false}`)
	if w.Code != 422 || submits.Load() != 0 {
		t.Fatal("background privacy setting silently changed")
	}
	w = resourceBackgroundCall(a, `{"model":"fixture-model","input":"synthetic","background":true}`)
	request := w.Header().Get("X-Gateway-Request-Id")
	for i := 0; i < 2; i++ {
		w = resourceRequest(a, "POST", "/v1/responses/resp-cancel/cancel", "", "test-client-key", nil)
		if w.Code != 200 {
			t.Fatalf("cancel %d %s", w.Code, w.Body.String())
		}
	}
	record, _ := resourceReadRecord(a.Store.DB, request)
	if record.Status != "cancelled" || record.Cost != nil || cancels.Load() != 1 || submits.Load() != 1 {
		t.Fatal("cancel reissued or unknown cost zeroed")
	}
	var status string
	a.Store.DB.QueryRow("SELECT status FROM reservations WHERE request_id=?", request).Scan(&status)
	if status != "pending_reconciliation" {
		t.Fatal("cancel unknown expense refunded")
	}
}
func TestSpecBatchOwnershipOutOfOrderAndImportExactlyOnce(t *testing.T) {
	var uploads, submits, reads atomic.Int32
	var observedInput string
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			uploads.Add(1)
			return resourceResponse(`{"id":"file-batch-input","purpose":"batch"}`), nil
		case r.Method == "POST" && r.URL.Path == "/batches":
			submits.Add(1)
			raw, _ := io.ReadAll(r.Body)
			observedInput = string(raw)
			return resourceResponse(`{"id":"batch-native","status":"validating","input_file_id":"file-batch-input","output_file_id":null,"error_file_id":null}`), nil
		case r.URL.Path == "/batches/batch-native":
			reads.Add(1)
			return resourceResponse(`{"id":"batch-native","status":"completed","input_file_id":"file-batch-input","output_file_id":"file-batch-output","error_file_id":null}`), nil
		case r.URL.Path == "/files/file-batch-output/content":
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/jsonl"}}, Body: io.NopCloser(strings.NewReader("{\"custom_id\":\"second\",\"response\":{\"status_code\":200,\"body\":{\"id\":\"resp-item-2\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n{\"custom_id\":\"first\",\"response\":{\"status_code\":400,\"body\":{\"error\":{\"message\":\"synthetic failed\"}}}}\n"))}, nil
		default:
			t.Errorf("unexpected provider %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected")
		}
	})
	resourceBudget(t, a, "soft")
	input := `{"custom_id":"first","method":"POST","url":"/v1/responses","body":{"model":"fixture-model","input":"synthetic private one","max_output_tokens":8}}
{"custom_id":"second","method":"POST","url":"/v1/responses","body":{"model":"fixture-model","input":"synthetic private two","max_output_tokens":8}}
{"custom_id":"missing","method":"POST","url":"/v1/responses","body":{"model":"fixture-model","input":"synthetic private three","max_output_tokens":8}}
`
	file := resourceUploadFixture(t, a, "batch", input)
	body := `{"input_file_id":"` + file + `","endpoint":"/v1/responses","completion_window":"24h"}`
	w := resourceRequest(a, "POST", "/v1/batches", "application/json", "test-client-key", strings.NewReader(body))
	if w.Code != 200 {
		t.Fatalf("batch submit %d %s", w.Code, w.Body.String())
	}
	var wire map[string]any
	json.Unmarshal(w.Body.Bytes(), &wire)
	batch, _ := wire["id"].(string)
	if !strings.HasPrefix(batch, "batch-cove-") || strings.Contains(w.Body.String(), "file-batch-input") || !strings.Contains(observedInput, "file-batch-input") {
		t.Fatal("batch IDs not mapped exactly")
	}
	var reserved int
	a.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE status='pending'").Scan(&reserved)
	if reserved != 3 {
		t.Fatal("not all item budgets reserved before POST")
	}
	for i := 0; i < 2; i++ {
		w = resourceRequest(a, "GET", "/v1/batches/"+batch, "", "test-client-key", nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "file-batch-output") {
			t.Fatalf("batch observe/map %d %s", w.Code, w.Body.String())
		}
	}
	var requests, attempts, settled, pending int
	a.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&requests)
	a.Store.DB.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts)
	a.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE status='settled'").Scan(&settled)
	a.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE status='pending_reconciliation'").Scan(&pending)
	if requests != 5 || attempts != 5 || settled != 1 || pending != 2 || submits.Load() != 1 || uploads.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("batch imports duplicated or lost unknown rows req=%d attempts=%d settled=%d pending=%d", requests, attempts, settled, pending)
	}
	var metadata string
	a.Store.DB.QueryRow("SELECT metadata_json FROM resources WHERE id=?", file).Scan(&metadata)
	if strings.Contains(metadata, "synthetic private") {
		t.Fatal("batch prompt retained in metadata")
	}
	resourceOtherKey(t, a)
	before := reads.Load()
	w = resourceRequest(a, "GET", "/v1/batches/"+batch, "", "other-secret", nil)
	if w.Code != 404 || reads.Load() != before {
		t.Fatal("Batch crossed key")
	}
}
func TestSpecBackgroundConcurrentTerminalCAS(t *testing.T) {
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		return resourceResponse(`{"id":"resp-cas","status":"queued"}`), nil
	})
	resourceBudget(t, a, "soft")
	w := resourceBackgroundCall(a, `{"model":"fixture-model","input":"synthetic","background":true}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	j, e := a.resourceReadJob("resp-cas", "key", "background")
	if e != nil {
		t.Fatal(e)
	}
	var wire map[string]json.RawMessage
	json.Unmarshal([]byte(`{"id":"resp-cas","status":"completed","usage":{"input_tokens":5,"output_tokens":2}}`), &wire)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); results <- a.observeBackgroundJob(j, wire) }()
	}
	group.Wait()
	close(results)
	success, conflicts := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else {
			var re *resourceError
			if !errors.As(e, &re) || re.Code != 409 {
				t.Fatal(e)
			}
			conflicts++
		}
	}
	var fingerprint sql.NullString
	a.Store.DB.QueryRow("SELECT terminal_fingerprint FROM jobs WHERE id=?", j.ID).Scan(&fingerprint)
	if success != 1 || conflicts != 1 || !fingerprint.Valid {
		t.Fatal("same terminal imported more than once")
	}
	var amount string
	a.Store.DB.QueryRow("SELECT settled_amount FROM reservations WHERE request_id=?", j.RequestID).Scan(&amount)
	if amount != "0.000007000000" {
		t.Fatalf("CAS accounting amount=%s", amount)
	}
}
func TestSpecBatchValidationStrictBudgetAndDuplicateResults(t *testing.T) {
	var uploads, submits atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			uploads.Add(1)
			return resourceResponse(`{"id":"file-input-duplicates","purpose":"batch"}`), nil
		case r.Method == "POST" && r.URL.Path == "/batches":
			submits.Add(1)
			return resourceResponse(`{"id":"batch-duplicates","status":"validating"}`), nil
		case r.URL.Path == "/batches/batch-duplicates":
			return resourceResponse(`{"id":"batch-duplicates","status":"completed","output_file_id":"file-duplicate-output"}`), nil
		default:
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/jsonl"}}, Body: io.NopCloser(strings.NewReader("{\"custom_id\":\"one\",\"response\":{\"status_code\":200,\"body\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}}\n{\"custom_id\":\"one\",\"response\":{\"status_code\":200,\"body\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}}\n{\"custom_id\":\"two\",\"response\":{\"status_code\":200,\"body\":{\"usage\":{\"input_tokens\":-1,\"output_tokens\":1}}}}\n"))}, nil
		}
	})
	bad := `{"custom_id":"same","method":"POST","url":"https://untrusted.invalid/v1/responses","body":{"model":"fixture-model","input":"synthetic"}}
`
	body, kind := resourceUploadBody(t, "batch", bad, "text/plain")
	w := resourceRequest(a, "POST", "/v1/files", kind, "test-client-key", bytes.NewReader(body))
	if w.Code != 400 || uploads.Load() != 0 || submits.Load() != 0 {
		t.Fatal("invalid endpoint submitted")
	}
	input := `{"custom_id":"one","method":"POST","url":"/v1/responses","body":{"model":"fixture-model","input":"synthetic"}}
{"custom_id":"two","method":"POST","url":"/v1/responses","body":{"model":"fixture-model","input":"synthetic"}}
`
	file := resourceUploadFixture(t, a, "batch", input)
	resourceBudget(t, a, "strict")
	create := `{"input_file_id":"` + file + `","endpoint":"/v1/responses","completion_window":"24h"}`
	w = resourceRequest(a, "POST", "/v1/batches", "application/json", "test-client-key", strings.NewReader(create))
	if w.Code != 422 || submits.Load() != 0 {
		t.Fatal("strict unsupported budget dispatched")
	}
	budget, _ := readBudget(a.Store.DB, "resource-budget")
	budget.Mode = "soft"
	budget.Version++
	tx, _ := a.Store.DB.Begin()
	saveBudget(tx, budget)
	tx.Commit()
	w = resourceRequest(a, "POST", "/v1/batches", "application/json", "test-client-key", strings.NewReader(create))
	if w.Code != 200 {
		t.Fatalf("soft batch %d %s", w.Code, w.Body.String())
	}
	var wire map[string]any
	json.Unmarshal(w.Body.Bytes(), &wire)
	batch := wire["id"].(string)
	w = resourceRequest(a, "GET", "/v1/batches/"+batch, "", "test-client-key", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var unknown int
	a.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE status='pending_reconciliation'").Scan(&unknown)
	if unknown != 2 {
		t.Fatal("duplicate/invalid usage was settled or zeroed")
	}
	rows, e := a.Store.DB.Query("SELECT data FROM requests WHERE json_extract(data,'$.operation')='batch.item'")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var record Record
		json.Unmarshal([]byte(raw), &record)
		if record.Cost != nil || record.ObservationStatus != "partial" {
			t.Fatal("invalid results treated as complete actual usage")
		}
	}
	rows.Close()
}
func TestSpecResourcesStartupSpoolCleanupBoundaries(t *testing.T) {
	a := resourceFixture(t, nil)
	old := filepath.Join(a.Config.DataDir, ".resource-spool-old")
	recent := filepath.Join(a.Config.DataDir, ".resource-spool-recent")
	foreign := t.TempDir()
	for _, dir := range []string{old, recent} {
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(dir, "request"), []byte("synthetic private"), 0600)
	}
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	symlink := filepath.Join(a.Config.DataDir, ".resource-spool-link")
	if e := os.Symlink(foreign, symlink); e != nil {
		t.Fatal(e)
	}
	if e := a.cleanupResourceSpools(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("expired private spool survived startup cleanup")
	}
	if _, e := os.Stat(recent); e != nil {
		t.Fatal("recent spool removed")
	}
	if _, e := os.Lstat(symlink); e != nil {
		t.Fatal("symlink removed")
	}
	if _, e := os.Stat(foreign); e != nil {
		t.Fatal("foreign directory touched")
	}
}
func TestSpecBackgroundEntryOneSlotAndNormalBodyReplay(t *testing.T) {
	var posts atomic.Int32
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		posts.Add(1)
		return resourceResponse(`{"id":"resp-entry","status":"queued"}`), nil
	})
	normal := `{"model":"fixture-model","input":"synthetic normal"}`
	r := httptest.NewRequest("POST", "http://127.0.0.1:5569/v1/responses", strings.NewReader(normal))
	r.Header.Set("Authorization", "Bearer test-client-key")
	w := httptest.NewRecorder()
	if a.backgroundResponsesEntry(w, r) {
		t.Fatal("normal request consumed by background entry")
	}
	replayed, _ := io.ReadAll(r.Body)
	if string(replayed) != normal || posts.Load() != 0 {
		t.Fatal("normal prefix not replayed exactly")
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:5569/v1/responses", strings.NewReader(`{"background":true,"model":"fixture-model","input":"synthetic"}`))
	r.Header.Set("Authorization", "Bearer test-client-key")
	w = httptest.NewRecorder()
	if !a.backgroundResponsesEntry(w, r) || w.Code != 200 || posts.Load() != 1 {
		t.Fatalf("one-slot background deadlock/replay %d %s", w.Code, w.Body.String())
	}
	a.mu.Lock()
	if len(a.slots) != 0 || a.keyActive["key"] != 0 || len(a.running) != 0 {
		t.Fatal("entry leaked shared slot")
	}
	a.mu.Unlock()
	before := posts.Load()
	r = httptest.NewRequest("POST", "http://127.0.0.1:5569/v1/responses", strings.NewReader(`{"background":true,"input":"`+strings.Repeat("x", 100<<10)+`","model":"fixture-model"}`))
	r.Header.Set("Authorization", "Bearer test-client-key")
	w = httptest.NewRecorder()
	if !a.backgroundResponsesEntry(w, r) || w.Code != 422 || posts.Load() != before {
		t.Fatal("late background model read large unauthenticated body")
	}
}
func TestSpecResourcesFileReferencePathsAndContentCancel(t *testing.T) {
	var filePublic string
	var backgroundPosts atomic.Int32
	started := make(chan struct{})
	blocked := &resourceBlockedBody{Closed: make(chan struct{}), Started: started}
	a := resourceFixture(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/files":
			return resourceResponse(`{"id":"file-reference","purpose":"user_data"}`), nil
		case r.Method == "POST":
			backgroundPosts.Add(1)
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"file_id":"file-reference"`) || !strings.Contains(string(raw), `"text":"file-cove-literal-text"`) {
				t.Fatal("resource path not mapped selectively")
			}
			return resourceResponse(`{"id":"resp-file-reference","status":"queued"}`), nil
		case strings.HasSuffix(r.URL.Path, "/input_items"):
			return resourceResponse(`{"object":"list","data":[{"type":"message","content":[{"type":"input_file","file_id":"file-reference"},{"type":"input_text","text":"file-reference"}]}]}`), nil
		default:
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: blocked}, nil
		}
	})
	filePublic = resourceUploadFixture(t, a, "user_data", "synthetic")
	w := resourceBackgroundCall(a, `{"model":"fixture-model","background":true,"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"`+filePublic+`"},{"type":"input_text","text":"file-cove-literal-text"}]}]}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = resourceRequest(a, "GET", "/v1/responses/resp-file-reference/input_items", "", "test-client-key", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"file_id":"`+filePublic+`"`) || !strings.Contains(w.Body.String(), `"text":"file-reference"`) {
		t.Fatal("input_items mapping rewrote user text or leaked native file reference")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "http://127.0.0.1:5569/v1/files/"+filePublic+"/content", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-client-key")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); a.resourceOperationsAPI(w, r); done <- w }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("file download did not start")
	}
	cancel()
	select {
	case w = <-done:
	case <-time.After(time.Second):
		t.Fatal("file cancellation did not close reader")
	}
	record, e := resourceReadRecord(a.Store.DB, w.Header().Get("X-Gateway-Request-Id"))
	if e != nil || record.Status != "cancelled" || record.Cost != nil || record.DeliveryStatus != "failed" {
		t.Fatal("cancelled binary marked complete/free")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.slots) != 0 || len(a.running) != 0 {
		t.Fatal("content cancellation leaked slot")
	}
	if backgroundPosts.Load() != 1 {
		t.Fatal("file continuation replayed generation")
	}
}

type resourceBlockedBody struct {
	Closed, Started      chan struct{}
	CloseOnce, StartOnce sync.Once
}

func (b *resourceBlockedBody) Read(p []byte) (int, error) {
	b.StartOnce.Do(func() { close(b.Started) })
	<-b.Closed
	return 0, errors.New("synthetic interrupted body")
}
func (b *resourceBlockedBody) Close() error { b.CloseOnce.Do(func() { close(b.Closed) }); return nil }
