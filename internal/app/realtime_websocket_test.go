package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wsFixture struct {
	App         *App
	Server      *httptest.Server
	Up          *httptest.Server
	Connections chan *websocket.Conn
	Received    chan []byte
	Posts       atomic.Int32
}

func newWSFixture(t *testing.T, realtime bool) *wsFixture {
	t.Helper()
	f := &wsFixture{Connections: make(chan *websocket.Conn, 8), Received: make(chan []byte, 64)}
	f.Up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			f.Posts.Add(1)
			w.WriteHeader(500)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-source-secret" {
			t.Error("downstream/upstream key isolation failed")
			w.WriteHeader(401)
			return
		}
		if r.URL.RawQuery != "" && (!realtime || r.URL.Query().Get("model") != "fixture-model") {
			t.Error("unexpected upstream query")
		}
		c, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		f.Connections <- c
		for {
			_, raw, e := c.ReadMessage()
			if e != nil {
				return
			}
			f.Received <- raw
		}
	}))
	t.Cleanup(f.Up.Close)
	a := contractApp(t, nil)
	f.App = a
	a.Config.MaxConcurrent = 4
	a.Config.IdleTimeout = 5
	a.Config.TotalTimeout = 20
	a.slots = make(chan struct{}, 4)
	a.HTTP = &http.Client{Transport: &http.Transport{}}
	source, _ := a.Store.source("source")
	source.BaseURL = f.Up.URL + "/v1"
	source.Provider = "openai"
	source.NativeProtocol = "responses"
	source.NativeOperations = []string{"responses_websocket", "compact"}
	source.Continuation = true
	card := wsPublicCard
	if realtime {
		source.NativeProtocol = "realtime_websocket"
		source.NativeOperations = []string{"realtime_websocket"}
		card = wsRealtimeCard
	}
	if err := a.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	candidate, err := a.candidateFor(source, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(encode(candidate.Model)), &raw)
	raw["websocket_adapter"] = json.RawMessage(encode(card))
	raw["websocket_warmup"] = json.RawMessage("false")
	raw["websocket_steering"] = json.RawMessage("false")
	if _, err = a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(raw), candidate.Model.ID); err != nil {
		t.Fatal(err)
	}
	a.EnableBackupGate()
	// The shared fixture configures a fixed listen address. Route through the
	// real data/admin handlers while using this test's temporary listener.
	a.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			a.admin(w, r)
			return
		}
		a.data(w, r)
	})
	f.Server = httptest.NewUnstartedServer(a)
	a.Config.Listen = f.Server.Listener.Addr().String()
	f.Server.Start()
	t.Cleanup(f.Server.Close)
	return f
}
func (f *wsFixture) dial(t *testing.T, path string) *websocket.Conn {
	t.Helper()
	c, r, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+path, http.Header{"Authorization": {"Bearer test-client-key"}})
	if err != nil {
		if r != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body.Close()
			t.Fatalf("dial status=%d body=%s: %v", r.StatusCode, body, err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func wsSend(t *testing.T, c *websocket.Conn, raw string) {
	t.Helper()
	if err := c.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}
func wsRead(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func wsReceive(t *testing.T, f *wsFixture) []byte {
	t.Helper()
	select {
	case raw := <-f.Received:
		return raw
	case <-time.After(3 * time.Second):
		t.Fatal("native create not dispatched")
		return nil
	}
}
func wsUp(t *testing.T, f *wsFixture) *websocket.Conn {
	t.Helper()
	select {
	case c := <-f.Connections:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("native upstream not connected")
		return nil
	}
}
func wsRecords(t *testing.T, a *App, ended int) []Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := a.Store.DB.Query("SELECT data FROM requests ORDER BY started,id")
		if err != nil {
			t.Fatal(err)
		}
		var records []Record
		n := 0
		for rows.Next() {
			var raw string
			rows.Scan(&raw)
			var rec Record
			json.Unmarshal([]byte(raw), &rec)
			records = append(records, rec)
			if rec.Ended != nil {
				n++
			}
		}
		rows.Close()
		if n >= ended {
			return records
		}
		if time.Now().After(deadline) {
			t.Fatalf("records did not finish: %+v", records)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func wsDone(t *testing.T, c *websocket.Conn, lane, id string) {
	t.Helper()
	body := map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{}, "usage": map[string]any{"input_tokens": 11, "output_tokens": 3}}}
	if lane != "" {
		body["stream_id"] = lane
	}
	wsSend(t, c, encode(body))
}

func wsAdmin(t *testing.T, f *wsFixture, method, path string, body any) (int, map[string]json.RawMessage) {
	t.Helper()
	r, err := http.NewRequest(method, f.Server.URL+path, strings.NewReader(encode(body)))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer test-session")
	r.Header.Set("Origin", f.Server.URL)
	r.Header.Set("Content-Type", "application/json")
	response, err := f.App.HTTP.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("admin response status=%d body=%s", response.StatusCode, raw)
	}
	return response.StatusCode, result
}

func wsBoundRequest(t *testing.T, a *App, responseID string) string {
	t.Helper()
	var requestID string
	if err := a.Store.DB.QueryRow("SELECT request_id FROM bindings WHERE response_id=?", responseID).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	return requestID
}

func wsCancelled(t *testing.T, up *websocket.Conn, lane, responseID string) {
	t.Helper()
	wsSend(t, up, encode(map[string]any{"type": "response.failed", "stream_id": lane,
		"response": map[string]any{"id": responseID, "status": "cancelled", "output": []any{},
			"usage": map[string]any{"input_tokens": 11, "output_tokens": 3}}}))
}

func TestSpecWebSocketAdminCancellationTargetsOnlyRequestedLane(t *testing.T) {
	f := newWSFixture(t, false)
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","stream_id":"a","model":"fixture-model","input":"cancel me"}`)
	up := wsUp(t, f)
	wsReceive(t, f)
	wsSend(t, down, `{"type":"response.create","stream_id":"b","model":"fixture-model","input":"keep me"}`)
	wsReceive(t, f)
	wsSend(t, up, `{"type":"response.created","stream_id":"a","response":{"id":"cancel-a","status":"in_progress"}}`)
	wsRead(t, down)
	wsSend(t, up, `{"type":"response.created","stream_id":"b","response":{"id":"keep-b","status":"in_progress"}}`)
	wsRead(t, down)
	requestID := wsBoundRequest(t, f.App, "cancel-a")
	status, result := wsAdmin(t, f, "POST", "/admin/requests/"+requestID+"/cancel", map[string]any{"version": 1})
	if status != 202 || string(result["state"]) != `"cancelling"` || string(result["completed"]) == "true" {
		t.Fatal("active native cancellation falsely returned complete", status, result)
	}
	var cancel map[string]json.RawMessage
	json.Unmarshal(wsReceive(t, f), &cancel)
	if string(cancel["type"]) != `"response.cancel"` || string(cancel["stream_id"]) != `"a"` || string(cancel["response_id"]) != `"cancel-a"` {
		t.Fatal("admin cancel crossed native lane/response scope", cancel)
	}
	// A duplicate admin action coalesces; it does not create another generation.
	wsAdmin(t, f, "POST", "/admin/requests/"+requestID+"/cancel", map[string]any{"version": 1})
	wsCancelled(t, up, "a", "cancel-a")
	wsRead(t, down)
	wsDone(t, up, "b", "keep-b")
	wsRead(t, down)
	down.Close()
	records := wsRecords(t, f.App, 2)
	for _, rec := range records {
		want := "succeeded"
		if rec.ID == requestID {
			want = "cancelled"
		}
		if rec.Status != want || rec.Usage.Input == nil || *rec.Usage.Input != 11 {
			t.Fatal("cancel fabricated success or discarded observed usage", rec)
		}
	}
	select {
	case extra := <-f.Received:
		t.Fatal("duplicate cancellation dispatched native extra frame", string(extra))
	default:
	}
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if len(f.App.running) != 0 || len(f.App.runningSources) != 0 {
		t.Fatal("native cancellation callback leaked")
	}
}

func TestSpecWebSocketKeyCancelActiveUsesNativeController(t *testing.T) {
	f := newWSFixture(t, false)
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","stream_id":"a","model":"fixture-model","input":"one"}`)
	up := wsUp(t, f)
	wsReceive(t, f)
	wsSend(t, down, `{"type":"response.create","stream_id":"b","model":"fixture-model","input":"two"}`)
	wsReceive(t, f)
	for _, lane := range []string{"a", "b"} {
		wsSend(t, up, encode(map[string]any{"type": "response.created", "stream_id": lane,
			"response": map[string]any{"id": "key-cancel-" + lane, "status": "in_progress"}}))
		wsRead(t, down)
	}
	key, _ := f.App.Store.keyByDigest(digest("test-client-key"))
	status, result := wsAdmin(t, f, "DELETE", "/admin/client-keys/"+key.ID,
		map[string]any{"version": key.Version, "cancel_active": true})
	var requested []string
	json.Unmarshal(result["cancel_requested"], &requested)
	if status != 200 || len(requested) != 2 || string(result["upstream_execution"]) != `"unknown"` {
		t.Fatal("Key cancel_active omitted WS turns or claimed provider stopped", status, result)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		var event struct {
			Type       string `json:"type"`
			Lane       string `json:"stream_id"`
			ResponseID string `json:"response_id"`
		}
		json.Unmarshal(wsReceive(t, f), &event)
		if event.Type != "response.cancel" || event.ResponseID != "key-cancel-"+event.Lane {
			t.Fatal("Key cancel did not target owned native response", event)
		}
		seen[event.Lane] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatal("Key cancel ignored an active lane")
	}
	for _, lane := range []string{"a", "b"} {
		wsCancelled(t, up, lane, "key-cancel-"+lane)
		wsRead(t, down)
	}
	down.Close()
	for _, rec := range wsRecords(t, f.App, 2) {
		if rec.Status != "cancelled" || rec.Usage.Output == nil || *rec.Usage.Output != 3 {
			t.Fatal("Key cancel changed native terminal observation", rec)
		}
	}
}

func TestSpecWebSocketRealtimeAdminSessionCancellation(t *testing.T) {
	f := newWSFixture(t, true)
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	wsUp(t, f)
	requestID := wsRecords(t, f.App, 0)[0].ID
	status, _ := wsAdmin(t, f, "POST", "/admin/requests/"+requestID+"/cancel", map[string]any{"version": 1})
	if status != 202 {
		t.Fatal("Realtime session cancellation ignored", status)
	}
	down.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := down.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
		t.Fatal("session cancel did not close dedicated transport", err)
	}
	if records := wsRecords(t, f.App, 1); records[0].Status == "succeeded" || records[0].Cost != nil {
		t.Fatal("cancelled session fabricated successful billed generation", records)
	}
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if len(f.App.running) != 0 || len(f.App.runningSources) != 0 || f.App.realtimeActive != 0 {
		t.Fatal("session cancellation registration leaked")
	}
}

func TestSpecWebSocketRealtimeSilentAuthorityTightening(t *testing.T) {
	for _, change := range []string{"model_permission", "source_generation", "account_generation", "card"} {
		t.Run(change, func(t *testing.T) {
			f := newWSFixture(t, true)
			down := f.dial(t, "/v1/realtime?model=fixture-model")
			up := wsUp(t, f)
			wsSend(t, up, `{"type":"response.created","response":{"id":"observed-before-change","status":"in_progress","usage":{"input_tokens":5,"output_tokens":2}}}`)
			wsRead(t, down)
			source, _ := f.App.Store.source("source")
			status := 0
			switch change {
			case "model_permission":
				key, _ := f.App.Store.keyByDigest(digest("test-client-key"))
				status, _ = wsAdmin(t, f, "PATCH", "/admin/client-keys/"+key.ID,
					map[string]any{"version": key.Version, "model_allowlist": []string{"different-model"}})
			case "source_generation":
				status, _ = wsAdmin(t, f, "PATCH", "/admin/sources/"+source.ID,
					map[string]any{"version": source.Version, "native_protocol": "responses"})
			case "account_generation":
				account, _, err := f.App.Store.account(source.AccountID)
				if err != nil {
					t.Fatal(err)
				}
				status, _ = wsAdmin(t, f, "POST", "/admin/accounts/"+account.ID+"/credential",
					map[string]any{"version": account.Version, "secret": "another-synthetic-secret"})
			case "card":
				candidate, _ := f.App.candidateFor(source, "fixture-model")
				status, _ = wsAdmin(t, f, "PATCH", "/admin/models/"+candidate.Model.ID,
					map[string]any{"version": candidate.Model.Version, "websocket_adapter": ""})
			}
			if status != 200 {
				t.Fatal("authority tightening setup failed", status)
			}
			// No further client control/audio/create is sent. The watch must still
			// stop automatic provider generation under the changed authority.
			down.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _, err := down.ReadMessage()
			if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
				t.Fatal("silent session kept stale generation/model/card authority", err)
			}
			for _, rec := range wsRecords(t, f.App, 2) {
				if rec.Operation == "realtime" && (rec.Status != "interrupted" || rec.Generation != source.Generation || rec.Usage.Input == nil || *rec.Usage.Input != 5) {
					t.Fatal("authority close discarded original-owner observed usage", rec)
				}
			}
		})
	}
}

func TestSpecWebSocketRealtimeBudgetDeletionOwnsLiveSessionPlan(t *testing.T) {
	f := newWSFixture(t, true)
	source, _ := f.App.Store.source("source")
	source.Price = &Price{Currency: "USD", Units: []PriceUnit{{Dimension: "request", Amount: "1", Per: "1"}}}
	if err := f.App.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	budget := Budget{ID: "live-vad-budget", Name: "VAD soft", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "100", Mode: "soft", Period: BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	if _, err := f.App.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES(?,?,?)", budget.ID, "instance", encode(budget)); err != nil {
		t.Fatal(err)
	}
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	up := wsUp(t, f)
	var reservations int
	f.App.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE budget_id=?", budget.ID).Scan(&reservations)
	if reservations != 0 {
		t.Fatal("session connection invented generation reservation")
	}
	status, _ := wsAdmin(t, f, "DELETE", "/admin/budgets/"+budget.ID, nil)
	if status != 409 {
		t.Fatal("live VAD dispatch plan lost its budget FK", status)
	}
	wsSend(t, up, `{"type":"response.created","response":{"id":"vad-after-budget-delete","status":"in_progress"}}`)
	wsRead(t, down)
	wsSend(t, up, `{"type":"response.done","response":{"id":"vad-after-budget-delete","status":"completed","usage":{"input_tokens":2,"output_tokens":3}}}`)
	wsRead(t, down)
	down.Close()
	for _, rec := range wsRecords(t, f.App, 2) {
		if rec.Origin == "provider_vad" && (rec.Status != "succeeded" || rec.Cost == nil) {
			t.Fatal("VAD attempt fact dropped after delete refusal", rec)
		}
	}
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if len(f.App.realtimeBudgetRefs) != 0 || len(f.App.running) != 0 || f.App.realtimeActive != 0 {
		t.Fatal("session dispatch references/cancellation callbacks leaked")
	}
}
func TestSpecWebSocketNativeMultiplexFIFOAndContinuation(t *testing.T) {
	f := newWSFixture(t, false)
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","stream_id":"a","model":"fixture-model","input":"first","store":false}`)
	up := wsUp(t, f)
	first := wsReceive(t, f)
	if !bytes.Contains(first, []byte(`"stream_id":"a"`)) {
		t.Fatal(string(first))
	}
	wsSend(t, down, `{"type":"response.create","stream_id":"b","model":"fixture-model","input":"parallel","store":false}`)
	wsReceive(t, f)
	wsSend(t, down, `{"type":"response.create","stream_id":"a","model":"fixture-model","input":"second","store":false}`)
	select {
	case <-f.Received:
		t.Fatal("same lane overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	native := `{ "type":"response.output_text.delta", "stream_id":"b", "delta":"PRIVATE_BODY_NOT_LOGGED", "opaque":{"native":[1,2]} }`
	wsSend(t, up, native)
	if string(wsRead(t, down)) != native {
		t.Fatal("native event was rewritten")
	}
	wsDone(t, up, "a", "ra")
	wsRead(t, down)
	next := wsReceive(t, f)
	if !bytes.Contains(next, []byte(`"input":"second"`)) {
		t.Fatal("FIFO changed")
	}
	wsDone(t, up, "b", "rb")
	wsRead(t, down)
	wsDone(t, up, "a", "ra2")
	wsRead(t, down)
	wsSend(t, down, `{"type":"response.create","stream_id":"fork","model":"fixture-model","input":"branch","previous_response_id":"ra2"}`)
	wsReceive(t, f)
	wsDone(t, up, "fork", "rfork")
	wsRead(t, down)
	wsSend(t, down, `{"type":"response.create","stream_id":"foreign","model":"fixture-model","input":"branch","previous_response_id":"not-owned"}`)
	errWire := wsRead(t, down)
	if !bytes.Contains(errWire, []byte(`"status":409`)) {
		t.Fatal(string(errWire))
	}
	records := wsRecords(t, f.App, 4)
	if len(records) != 4 || f.Posts.Load() != 0 {
		t.Fatal("per-create attempts/POST bridge", len(records), f.Posts.Load())
	}
	for _, rec := range records {
		if rec.Status != "succeeded" || rec.Usage.Input == nil || *rec.Usage.Input != 11 {
			t.Fatal(rec)
		}
		if strings.Contains(encode(rec), "PRIVATE_BODY_NOT_LOGGED") {
			t.Fatal("body persisted")
		}
	}
}
func TestSpecWebSocketHandshakeAndUnsupportedCards(t *testing.T) {
	f := newWSFixture(t, false)
	for _, test := range []struct {
		Path    string
		Headers http.Header
		Status  int
	}{
		{"/v1/responses?key=NO_SECRET", http.Header{"Authorization": {"Bearer test-client-key"}}, 400},
		{"/v1/responses", http.Header{}, 401},
		{"/v1/responses", http.Header{"Authorization": {"Bearer test-client-key"}, "Origin": {"https://evil.example"}}, 403},
	} {
		c, r, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+test.Path, test.Headers)
		if c != nil {
			c.Close()
		}
		if e == nil || r == nil || r.StatusCode != test.Status {
			t.Fatalf("status %v want %d err %v", r, test.Status, e)
		}
		r.Body.Close()
	}
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"a","stream":false}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"status":422`)) {
		t.Fatal("HTTP-only stream swallowed")
	}
	source, _ := f.App.Store.source("source")
	source.Provider = "openai_compatible"
	f.App.Store.saveSource(source)
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"a"}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"status":422`)) {
		t.Fatal("compatible inherited native card")
	}
	if f.Posts.Load() != 0 {
		t.Fatal("fallback POST")
	}
}
func TestSpecWebSocketGenerationBindingDisconnect(t *testing.T) {
	f := newWSFixture(t, false)
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"first"}`)
	up := wsUp(t, f)
	wsReceive(t, f)
	source, _ := f.App.Store.source("source")
	source.Generation++
	f.App.Store.saveSource(source)
	wsSend(t, down, `{"type":"response.create","stream_id":"new","model":"fixture-model","input":"second"}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"status":409`)) {
		t.Fatal("account generation allowed new create")
	}
	up.Close()
	down.Close()
	records := wsRecords(t, f.App, 1)
	if records[0].Status != "interrupted" || records[0].Cost != nil || records[0].Usage.Input != nil {
		t.Fatal("disconnect fabricated success/usage", records[0])
	}
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if len(f.App.slots) != 0 || f.App.accountActive[source.AccountID] != 0 {
		t.Fatal("capacity leaked")
	}
}
func TestSpecWebSocketNativeWarmupAndQueueBounds(t *testing.T) {
	f := newWSFixture(t, false)
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","generate":false,"input":"warmup"}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"status":422`)) {
		t.Fatal("warmup implicitly enabled")
	}
	source, _ := f.App.Store.source("source")
	candidate, _ := f.App.candidateFor(source, "fixture-model")
	var model map[string]json.RawMessage
	json.Unmarshal([]byte(encode(candidate.Model)), &model)
	model["websocket_warmup"] = json.RawMessage("true")
	f.App.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(model), candidate.Model.ID)
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","generate":false,"input":"warmup"}`)
	up := wsUp(t, f)
	if !bytes.Contains(wsReceive(t, f), []byte(`"generate":false`)) {
		t.Fatal("warmup replaced")
	}
	wsDone(t, up, "", "rwarm")
	wsRead(t, down)
	records := wsRecords(t, f.App, 1)
	if records[0].Operation != "warmup" {
		t.Fatal("warmup charged as generation")
	}
	q := newWSWriteQueue()
	if q.Put(wsFrame{Data: make([]byte, wsBufferLimit)}) != nil || q.Put(wsFrame{Data: []byte{1}}) == nil {
		t.Fatal("4MiB bound ineffective")
	}
	s := &wsSession{Lanes: map[string]*wsLane{}}
	for i := 0; i < 32; i++ {
		body := map[string]json.RawMessage{"stream_id": json.RawMessage(encode("lane" + strconv.Itoa(i)))}
		if err := s.enqueue(body, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if s.enqueue(map[string]json.RawMessage{"stream_id": json.RawMessage(`"overflow"`)}, []byte("{}")) == nil {
		t.Fatal("lane/pending bound ineffective")
	}
}
func TestSpecWebSocketRealtimeNativeAudioVADAndSessionScope(t *testing.T) {
	f := newWSFixture(t, true)
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	up := wsUp(t, f)
	created := `{"type":"session.created","session":{"id":"local-session","model":"fixture-model"}}`
	wsSend(t, up, created)
	if string(wsRead(t, down)) != created {
		t.Fatal("session rewritten")
	}
	audio := base64.StdEncoding.EncodeToString(make([]byte, 24000*2*2))
	input := encode(map[string]any{"type": "input_audio_buffer.append", "audio": audio})
	wsSend(t, down, input)
	if string(wsReceive(t, f)) != input {
		t.Fatal("2-second base64 audio changed")
	}
	wsSend(t, down, `{"type":"input_audio_buffer.commit"}`)
	wsReceive(t, f)
	wsSend(t, up, `{"type":"response.created","response":{"id":"vad-response","status":"in_progress"}}`)
	wsRead(t, down)
	output := `{"type":"response.output_audio.delta","response_id":"vad-response","item_id":"item","delta":"AAABBBCCC"}`
	wsSend(t, up, output)
	if string(wsRead(t, down)) != output {
		t.Fatal("audio converted to text")
	}
	wsSend(t, down, `{"type":"response.cancel","response_id":"vad-response"}`)
	if !bytes.Contains(wsReceive(t, f), []byte(`"response.cancel"`)) {
		t.Fatal("cancel not relayed")
	}
	wsSend(t, down, `{"type":"conversation.item.truncate","item_id":"item","content_index":0,"audio_end_ms":1500}`)
	if !bytes.Contains(wsReceive(t, f), []byte(`"audio_end_ms":1500`)) {
		t.Fatal("playback position guessed")
	}
	wsSend(t, up, `{"type":"response.done","response":{"id":"vad-response","status":"cancelled","usage":{"input_tokens":12,"output_tokens":7,"input_token_details":{"audio_tokens":10,"text_tokens":2},"output_token_details":{"audio_tokens":6,"text_tokens":1}}}}`)
	wsRead(t, down)
	wsSend(t, down, `{"type":"session.update","session":{"model":"not-allowed"}}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"param":"session.model"`)) {
		t.Fatal("session expanded model scope")
	}
	down.Close()
	records := wsRecords(t, f.App, 2)
	var response Record
	for _, rec := range records {
		if rec.Operation == "realtime" {
			response = rec
		}
	}
	if response.Origin != "provider_vad" || response.Status != "cancelled" || response.UsageDimensions["input_audio_token"] != "10" {
		t.Fatal("VAD accounting lost", response)
	}
	for _, rec := range records {
		if rec.Operation == "realtime.session" && rec.Usage.Input != nil {
			t.Fatal("session usage double counted")
		}
	}
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if len(f.App.slots) != 0 || f.App.realtimeActive != 0 {
		t.Fatal("nested session slot/leak")
	}
}
func TestSpecWebSocketOwnedShutdownAndBackupLease(t *testing.T) {
	f := newWSFixture(t, true)
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	wsUp(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if release, err := f.App.quiesceBackup(ctx); err == nil {
		release()
		t.Fatal("full backup skipped open WS owned lease")
	}
	f.App.mu.Lock()
	if f.App.backupQuiescing {
		t.Fatal("failed backup did not resume")
	}
	f.App.mu.Unlock()
	f.App.CloseAdmission()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := f.App.WaitOwnedTasks(ctx2); err != nil {
		t.Fatal("native read goroutines did not drain", err)
	}
	down.Close()
}
func TestSpecWebSocketRealtimeStrictTPM(t *testing.T) {
	f := newWSFixture(t, true)
	key, _ := f.App.Store.keyByDigest(digest("test-client-key"))
	limit := 100
	key.Limits.TPM = &limit
	f.App.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	c, r, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+"/v1/realtime?model=fixture-model", http.Header{"Authorization": {"Bearer test-client-key"}})
	if c != nil {
		c.Close()
	}
	if e == nil || r == nil || r.StatusCode != 422 {
		t.Fatal("opaque media bypassed TPM", r, e)
	}
	r.Body.Close()
	key.Limits.TPM = nil
	f.App.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
	budget := Budget{ID: "strict-ws", Name: "strict", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "100", Mode: "strict", Period: BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	f.App.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES(?,?,?)", budget.ID, "instance", encode(budget))
	c, r, e = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+"/v1/realtime?model=fixture-model", http.Header{"Authorization": {"Bearer test-client-key"}})
	if c != nil {
		c.Close()
	}
	if e == nil || r == nil || r.StatusCode != 422 {
		t.Fatal("automatic VAD allowed strict budget", r, e)
	}
	r.Body.Close()
}

func TestSpecWebSocketRealtimeSessionLimit(t *testing.T) {
	f := newWSFixture(t, true)
	f.App.Config.MaxConcurrent = 8
	f.App.slots = make(chan struct{}, 8)
	var sockets []*websocket.Conn
	for i := 0; i < 4; i++ {
		sockets = append(sockets, f.dial(t, "/v1/realtime?model=fixture-model"))
		wsUp(t, f)
	}
	c, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+"/v1/realtime?model=fixture-model", http.Header{"Authorization": {"Bearer test-client-key"}})
	if c != nil {
		c.Close()
	}
	if err == nil || response == nil || response.StatusCode != 429 {
		t.Fatal("fifth Realtime session bypassed fixed limit", response, err)
	}
	response.Body.Close()
	for _, c := range sockets {
		c.Close()
	}
	wsRecords(t, f.App, 4)
	f.App.mu.Lock()
	defer f.App.mu.Unlock()
	if f.App.realtimeActive != 0 || len(f.App.slots) != 0 {
		t.Fatal("Realtime session limit leaked admission")
	}
}

func TestSpecWebSocketRealtimeVADDispatchBudgetSnapshot(t *testing.T) {
	f := newWSFixture(t, true)
	source, _ := f.App.Store.source("source")
	source.Price = &Price{Currency: "USD", Units: []PriceUnit{{Dimension: "request", Amount: "1", Per: "1"}}}
	if err := f.App.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	budget := Budget{ID: "vad-snapshot", Name: "VAD soft", Scope: BudgetScope{Kind: "instance"}, Currency: "USD", AmountLimit: "100", Mode: "soft", Period: BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	if _, err := f.App.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES(?,?,?)", budget.ID, "instance", encode(budget)); err != nil {
		t.Fatal(err)
	}
	budget.AmountLimit = "0"
	f.App.Store.DB.Exec("UPDATE budgets SET data=? WHERE id=?", encode(budget), budget.ID)
	denied, denial, denialError := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.Server.URL, "http")+"/v1/realtime?model=fixture-model", http.Header{"Authorization": {"Bearer test-client-key"}})
	if denied != nil {
		denied.Close()
	}
	if denialError == nil || denial == nil || denial.StatusCode != 429 {
		t.Fatal("exhausted soft budget admitted automatic generation", denial, denialError)
	}
	denial.Body.Close()
	budget.AmountLimit = "100"
	f.App.Store.DB.Exec("UPDATE budgets SET data=? WHERE id=?", encode(budget), budget.ID)
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	up := wsUp(t, f)
	// Generation was authorized by the soft session plan. Current budget
	// scope/version/mode/amount edits must not block its observed ledger fact.
	budget.Version = 2
	budget.Mode = "strict"
	budget.AmountLimit = "0"
	budget.Scope = BudgetScope{Kind: "key", ID: "another-key"}
	if _, err := f.App.Store.DB.Exec("UPDATE budgets SET scope_kind=?,scope_id=?,data=? WHERE id=?", budget.Scope.Kind, budget.Scope.ID, encode(budget), budget.ID); err != nil {
		t.Fatal(err)
	}
	wsSend(t, up, `{"type":"response.created","response":{"id":"already-dispatched-vad","status":"in_progress"}}`)
	wsRead(t, down)
	wsSend(t, up, `{"type":"response.done","response":{"id":"already-dispatched-vad","status":"completed","usage":{"input_tokens":2,"output_tokens":3}}}`)
	wsRead(t, down)
	down.Close()
	records := wsRecords(t, f.App, 2)
	var generation Record
	for _, rec := range records {
		if rec.Origin == "provider_vad" {
			generation = rec
		}
	}
	if generation.Status != "succeeded" || generation.Cost == nil || *generation.Cost != "1.000000000000" {
		t.Fatal("already dispatched VAD fact rejected or fabricated free", generation)
	}
	var version int
	var status, settled string
	if err := f.App.Store.DB.QueryRow("SELECT budget_version,status,settled_amount FROM reservations WHERE request_id=? AND budget_id=?", generation.ID, budget.ID).Scan(&version, &status, &settled); err != nil {
		t.Fatal(err)
	}
	if version != 1 || status != "settled" || settled != "1.000000000000" {
		t.Fatal("VAD replaced the original dispatch budget version", version, status, settled)
	}
}

func TestSpecWebSocketRealtimeManualToolLoopAndNativeMediaError(t *testing.T) {
	f := newWSFixture(t, true)
	down := f.dial(t, "/v1/realtime?model=fixture-model")
	up := wsUp(t, f)
	media := `{"type":"session.update","event_id":"bad-media","session":{"audio":{"input":{"format":{"type":"audio/unsupported"}}}}}`
	wsSend(t, down, media)
	if string(wsReceive(t, f)) != media {
		t.Fatal("native media validation was replaced")
	}
	nativeError := `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_audio_format","event_id":"bad-media","message":"unsupported audio format"}}`
	wsSend(t, up, nativeError)
	if string(wsRead(t, down)) != nativeError {
		t.Fatal("native MIME error rewritten")
	}
	wsSend(t, down, `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"foreign-call","output":"forged"}}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"status":409`)) {
		t.Fatal("foreign tool output passed scope")
	}
	create := `{"type":"response.create","event_id":"manual-create","response":{"tools":[{"type":"function","name":"sum","parameters":{"type":"object"}}]}}`
	wsSend(t, down, create)
	if string(wsReceive(t, f)) != create {
		t.Fatal("manual native create rewritten")
	}
	wsSend(t, up, `{"type":"response.created","response":{"id":"manual-tool","status":"in_progress"}}`)
	wsRead(t, down)
	wsSend(t, up, `{"type":"response.done","response":{"id":"manual-tool","status":"completed","output":[{"id":"tool-item","type":"function_call","call_id":"owned-call","name":"sum","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":4}}}`)
	wsRead(t, down)
	output := `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"owned-call","output":"2"}}`
	wsSend(t, down, output)
	if string(wsReceive(t, f)) != output {
		t.Fatal("owned client tool result lost")
	}
	wsSend(t, down, `{"type":"response.create","response":{}}`)
	wsReceive(t, f)
	wsSend(t, up, `{"type":"response.created","response":{"id":"after-tool","status":"in_progress"}}`)
	wsRead(t, down)
	wsSend(t, up, `{"type":"response.done","response":{"id":"after-tool","status":"completed","usage":{"input_tokens":7,"output_tokens":2}}}`)
	wsRead(t, down)
	down.Close()
	records := wsRecords(t, f.App, 3)
	manual := 0
	for _, rec := range records {
		if rec.Operation == "realtime" {
			manual++
			if rec.Origin != "client" || rec.Status != "succeeded" {
				t.Fatal("manual tool generation not separately accounted", rec)
			}
		}
	}
	if manual != 2 || f.Posts.Load() != 0 {
		t.Fatal("manual generations used HTTP or session billing", manual, f.Posts.Load())
	}
}

func TestSpecWebSocketRevocationAndNativeFatalError(t *testing.T) {
	t.Run("revocation allows dispatched completion", func(t *testing.T) {
		f := newWSFixture(t, false)
		down := f.dial(t, "/v1/responses")
		wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"already authorized"}`)
		up := wsUp(t, f)
		wsReceive(t, f)
		key, _ := f.App.Store.keyByDigest(digest("test-client-key"))
		key.Revoked = true
		f.App.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID)
		wsSend(t, down, `{"type":"response.create","stream_id":"new","model":"fixture-model","input":"unauthorized"}`)
		if !bytes.Contains(wsRead(t, down), []byte(`"status":401`)) {
			t.Fatal("revoked Key admitted a new lane")
		}
		wsDone(t, up, "", "authorized-response")
		wsRead(t, down)
		down.Close()
		if records := wsRecords(t, f.App, 1); records[0].Status != "succeeded" {
			t.Fatal("revocation changed already dispatched result", records)
		}
	})
	t.Run("native connection limit", func(t *testing.T) {
		f := newWSFixture(t, false)
		down := f.dial(t, "/v1/responses")
		wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"first"}`)
		up := wsUp(t, f)
		wsReceive(t, f)
		nativeError := `{"type":"error","error":{"type":"invalid_request_error","code":"websocket_connection_limit_reached","message":"60 minute connection limit"},"status":400}`
		wsSend(t, up, nativeError)
		if string(wsRead(t, down)) != nativeError {
			t.Fatal("connection-level error wire lost")
		}
		_, _, err := down.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseGoingAway) {
			t.Fatal("fatal connection error did not close native transport", err)
		}
		if records := wsRecords(t, f.App, 1); records[0].Status != "failed" || records[0].Cost != nil {
			t.Fatal("fatal native error fabricated usage", records)
		}
	})
}

func TestSpecWebSocketNativeWireLimitsAndCards(t *testing.T) {
	t.Run("8MiB native message", func(t *testing.T) {
		f := newWSFixture(t, false)
		down := f.dial(t, "/v1/responses")
		_ = down.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", int(wsMessageLimit+1))))
		_, _, err := down.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
			t.Fatal("native message limit did not close 1009", err)
		}
	})
	t.Run("2MiB Realtime media", func(t *testing.T) {
		f := newWSFixture(t, true)
		down := f.dial(t, "/v1/realtime?model=fixture-model")
		wsUp(t, f)
		raw := encode(map[string]any{"type": "input_audio_buffer.append", "audio": strings.Repeat("A", int(wsMediaLimit))})
		_ = down.WriteMessage(websocket.TextMessage, []byte(raw))
		_, _, err := down.ReadMessage()
		if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
			t.Fatal("Realtime media limit did not close 1009", err)
		}
		wsRecords(t, f.App, 1)
	})
	public := Source{Kind: "api_key", Provider: "openai", NativeProtocol: "responses", NativeOperations: []string{"responses_websocket"}}
	codex := Source{Kind: "codex_subscription", Provider: "codex", NativeProtocol: "responses", NativeOperations: []string{"responses_websocket"}}
	if err := wsCapability(codex, WebSocketModelCapabilities{WebSocketAdapter: wsCodexCard}, false); err != nil {
		t.Fatal("explicit fixed Codex card rejected", err)
	}
	for _, tc := range []struct {
		Source Source
		Card   WebSocketModelCapabilities
	}{
		{public, WebSocketModelCapabilities{WebSocketAdapter: wsCodexCard}},
		{codex, WebSocketModelCapabilities{WebSocketAdapter: wsPublicCard}},
		{codex, WebSocketModelCapabilities{WebSocketAdapter: wsCodexCard, WebSocketWarmup: true}},
		{public, WebSocketModelCapabilities{WebSocketAdapter: wsPublicCard, WebSocketSteering: true}},
	} {
		if err := wsCapability(tc.Source, tc.Card, false); err == nil {
			t.Fatal("provider/subscription/card capabilities were inferred")
		}
	}
}

func TestSpecWebSocketCodexObservedQuotaAdmission(t *testing.T) {
	f := newWSFixture(t, false)
	source, err := f.App.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	source.Kind, source.Provider = "codex_subscription", "codex"
	if err := f.App.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	candidate, err := f.App.candidateFor(source, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	candidate.Model.WebSocketAdapter = wsCodexCard
	if _, err := f.App.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(candidate.Model), candidate.Model.ID); err != nil {
		t.Fatal(err)
	}
	key, err := f.App.Store.keyByDigest(digest("test-client-key"))
	if err != nil {
		t.Fatal(err)
	}
	session := &wsSession{App: f.App}
	now, expires := time.Now().UTC(), time.Now().UTC().Add(time.Minute)
	hundred := float64(100)
	proof := func() codexQuotaSnapshot {
		return codexQuotaSnapshot{Status: "available", Scope: codexQuotaScope{Account: source.AccountID},
			SourceGeneration: source.Generation, AccountGeneration: source.AccountGeneration,
			ObservedAt: &now, ExpiresAt: &expires,
			Windows: []codexQuotaWindow{{Status: "available", AccountWide: true, UsedPercent: &hundred,
				Scope:   codexQuotaScope{Account: source.AccountID, Model: "fixture-model", Operation: "generate"},
				ResetAt: &expires, ExpiresAt: expires}}}
	}
	for _, name := range []string{"fresh_full", "unknown", "expired", "old_account_generation", "old_source_generation", "unproven_accountwide", "other_model", "other_operation"} {
		t.Run(name, func(t *testing.T) {
			q := proof()
			operation := "generate"
			switch name {
			case "unknown":
				q.Status = "unknown"
			case "expired":
				old := now.Add(-time.Minute)
				q.ExpiresAt = &old
			case "old_account_generation":
				q.AccountGeneration--
			case "old_source_generation":
				q.SourceGeneration--
			case "unproven_accountwide":
				q.Windows[0].AccountWide = false
			case "other_model":
				q.Windows[0].Scope.Model = "other-model"
			case "other_operation":
				operation = "warmup"
			}
			if err := json.Unmarshal([]byte(encode(q)), &source.Quota); err != nil {
				t.Fatal(err)
			}
			if err := f.App.Store.saveSource(source); err != nil {
				t.Fatal(err)
			}
			f.App.mu.Lock()
			_, _, _, err := session.selectLocked(key, "fixture-model", "", operation)
			f.App.mu.Unlock()
			if name == "fresh_full" {
				status, field, _ := wsErrorDetails(err)
				if status != 429 || field != "quota" {
					t.Fatal("fresh proven account-wide limit bypassed", err)
				}
			} else if err != nil {
				t.Fatal("unknown/stale/unrelated observation became a dispatch ban", err)
			}
		})
	}
	// The real native create admission applies the same proof before credential
	// refresh, reservation, native connection, or any upstream dispatch.
	if err := json.Unmarshal([]byte(encode(proof())), &source.Quota); err != nil {
		t.Fatal(err)
	}
	if err := f.App.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"quota denied"}`)
	if !bytes.Contains(wsRead(t, down), []byte(`"param":"quota"`)) {
		t.Fatal("native create did not report the quota gate")
	}
	select {
	case <-f.Connections:
		t.Fatal("quota rejected create opened native upstream")
	default:
	}
	var records int
	if err := f.App.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&records); err != nil || records != 0 {
		t.Fatal("quota denial created an unexecuted usage fact", records, err)
	}
}

func TestSpecWebSocketCodexQuotaPublishedDuringCredentialRefresh(t *testing.T) {
	f := newWSFixture(t, false)
	source, err := f.App.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	source.Kind, source.Provider, source.AuthStatus = "codex_subscription", "codex", "logged_in"
	if err := f.App.Store.saveSource(source); err != nil {
		t.Fatal(err)
	}
	candidate, err := f.App.candidateFor(source, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	candidate.Model.WebSocketAdapter = wsCodexCard
	if _, err := f.App.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(candidate.Model), candidate.Model.ID); err != nil {
		t.Fatal(err)
	}
	credential := Credential{Access: "test-source-secret", Account: "synthetic-account", Subject: "synthetic-subject", Expires: time.Now().Add(time.Hour)}
	if err := f.App.Secrets.Put(source.CredentialRef, encode(credential)); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	refresh := &credentialRefresh{done: make(chan struct{}), cancel: cancel}
	f.App.mu.Lock()
	f.App.refreshes[source.AccountID] = refresh
	f.App.mu.Unlock()
	down := f.dial(t, "/v1/responses")
	wsSend(t, down, `{"type":"response.create","model":"fixture-model","input":"wait for refresh"}`)
	// A record proves the initial admission succeeded. The held refresh owns
	// no network activity and releases App.mu while the controller waits.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := f.App.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial native admission did not reach held refresh")
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.App.mu.Lock()
	now, expires, full := time.Now().UTC(), time.Now().Add(time.Minute).UTC(), float64(100)
	q := codexQuotaSnapshot{Status: "available", Scope: codexQuotaScope{Account: source.AccountID},
		SourceGeneration: source.Generation, AccountGeneration: source.AccountGeneration, ObservedAt: &now, ExpiresAt: &expires,
		Windows: []codexQuotaWindow{{Status: "available", Scope: codexQuotaScope{Account: source.AccountID},
			AccountWide: true, UsedPercent: &full, ResetAt: &expires, ExpiresAt: expires}}}
	_ = json.Unmarshal([]byte(encode(q)), &source.Quota)
	err = f.App.Store.saveSource(source)
	delete(f.App.refreshes, source.AccountID)
	close(refresh.done)
	f.App.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(wsRead(t, down), []byte(`"param":"quota"`)) {
		t.Fatal("credential wait used an obsolete quota snapshot")
	}
	for _, rec := range wsRecords(t, f.App, 1) {
		if rec.Submission != "not_sent" || rec.Cost == nil || *rec.Cost != "0" || rec.Status != "failed" {
			t.Fatal("pre-dispatch quota rejection fabricated provider usage", rec)
		}
	}
	select {
	case <-f.Connections:
		t.Fatal("refreshed full quota opened a native provider connection")
	default:
	}
}
