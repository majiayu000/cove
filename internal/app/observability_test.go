package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpecTelemetryWireTopologyPrivacyAndShutdownFlush(t *testing.T) {
	var payload []byte
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://collector.example.test/v1/traces" || r.Header.Get("Authorization") != "Bearer SYNTHETIC_COLLECTOR_SECRET" {
			t.Fatal("collector endpoint or private authorization missing")
		}
		var err error
		payload, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		calls.Add(1)
		return contractResponse(`{}`, "application/json"), nil
	})
	ref := "synthetic-collector-header"
	if err := a.Secrets.Put(ref, `{"Authorization":"Bearer SYNTHETIC_COLLECTOR_SECRET"}`); err != nil {
		t.Fatal(err)
	}
	a.observations.mu.Lock()
	a.observations.settings = telemetrySettings{Enabled: true, SampleRate: 1, Endpoint: "https://collector.example.test/v1/traces", HeaderRef: ref}
	a.observations.mu.Unlock()
	now := time.Now().UTC()
	r := Record{ID: "PRIVATE_REQUEST_ID", AttemptID: "PRIVATE_ATTEMPT_ID", KeyID: "PRIVATE_KEY_ID", SourceID: "PRIVATE_SOURCE_ID", Model: "PRIVATE_MODEL", Protocol: "responses", Operation: "generate", Status: "succeeded", Started: now.Add(-time.Second), AttemptStarted: now.Add(-time.Second), Ended: &now, QueueMS: 100, TraceParent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", RefreshTiming: credentialRefreshTiming{Start: now.Add(-2 * time.Second), End: now.Add(-1500 * time.Millisecond)}}
	a.observeExecution(r, Source{Provider: "openai"}, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.telemetryLoop(ctx)
	if calls.Load() != 1 || len(a.observations.queue) != 0 {
		t.Fatal("shutdown did not flush one bounded batch")
	}
	for _, secret := range []string{"PRIVATE_", "SYNTHETIC_COLLECTOR_SECRET", "prompt", "credential", "key_id", "source_id"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("private telemetry content: %s", secret)
		}
	}
	var decoded struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ResourceSpans) != 1 || len(decoded.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatal("invalid OTLP JSON envelope")
	}
	spans := decoded.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 4 {
		t.Fatalf("spans=%d", len(spans))
	}
	var root map[string]any
	for _, span := range spans {
		if span["name"] == "gateway.request" {
			root = span
		}
	}
	if root == nil || root["parentSpanId"] != "0123456789abcdef" {
		t.Fatal("incoming trace parent lost")
	}
	names := map[string]bool{}
	for _, span := range spans {
		names[span["name"].(string)] = true
		if span["traceId"] != "0123456789abcdef0123456789abcdef" {
			t.Fatal("trace ID changed")
		}
		if span["name"] != "gateway.request" && span["parentSpanId"] != root["spanId"] {
			t.Fatal("child parent mismatch")
		}
		if span["startTimeUnixNano"].(string) < root["startTimeUnixNano"].(string) || span["endTimeUnixNano"].(string) > root["endTimeUnixNano"].(string) {
			t.Fatal("child outside root interval")
		}
	}
	for _, name := range []string{"gateway.request", "gateway.attempt", "gateway.queue", "gateway.refresh"} {
		if !names[name] {
			t.Fatal("missing", name)
		}
	}
}

func TestSpecObservabilityTerminalDedupAndPrivacy(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(contractJSON, "application/json"), nil
	})
	if w := contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	rows := waitRecords(t, a, 1)
	source, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 100; i++ {
		a.observeExecution(rows[0], source, true)
	}
	w := lifecycleAdmin(a, "GET", "/admin/metrics", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `cove_requests_total{protocol="responses",operation="generate",outcome="completed"} 1`) || !strings.Contains(w.Body.String(), `cove_attempts_total{provider="openai_compatible",outcome="completed"} 1`) || !strings.Contains(w.Body.String(), "cove_ttft_seconds_count 0") {
		t.Fatal(w.Body.String())
	}
	for _, secret := range []string{"test-client-key", "test-source-secret", "test-session", "response-contract", "Contract source"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private data in metrics")
		}
	}
	if _, e = a.Store.DB.Exec("DELETE FROM requests"); e != nil {
		t.Fatal(e)
	}
	w = lifecycleAdmin(a, "GET", "/admin/metrics", "", "")
	if !strings.Contains(w.Body.String(), `outcome="completed"} 1`) {
		t.Fatal("counter declined when retention removed history")
	}
}

func TestSpecObservabilityTTFTIncludesQueueAndEarlierAttempts(t *testing.T) {
	a := contractApp(t, nil)
	started := time.Now().UTC().Add(-4 * time.Second)
	ended := started.Add(4 * time.Second)
	content := started.Add(3 * time.Second)
	r := Record{ID: "logical-request", AttemptID: "last-attempt", Protocol: "responses", Operation: "generate", Status: "succeeded", Started: started, AttemptStarted: started.Add(2 * time.Second), FirstContentAt: &content, Ended: &ended, QueueMS: 500}
	a.observeExecution(r, Source{Provider: "openai"}, true)
	// The runtime metric uses the same request start as the request detail and
	// usage report. The last attempt's start excludes time the user waited.
	w := lifecycleAdmin(a, "GET", "/admin/metrics", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "cove_ttft_seconds_count 1\n") || !strings.Contains(w.Body.String(), "cove_ttft_seconds_sum 3\n") {
		t.Fatal(w.Code, w.Body.String())
	}
	beforeStart := started.Add(-time.Millisecond)
	r.ID, r.AttemptID, r.FirstContentAt = "invalid-content-time", "other-attempt", &beforeStart
	a.observeExecution(r, Source{Provider: "openai"}, true)
	w = lifecycleAdmin(a, "GET", "/admin/metrics", "", "")
	if !strings.Contains(w.Body.String(), "cove_ttft_seconds_count 1\n") {
		t.Fatal("invalid content timestamp produced a TTFT sample", w.Body.String())
	}
}
func TestSpecObservabilityBoundedOfflineTelemetry(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("synthetic collector offline")
	})
	w := lifecycleAdmin(a, "PUT", "/admin/telemetry", `{"version":1,"enabled":true,"endpoint":"https://collector.example.test/v1/traces","sample_rate":1,"headers":{"Authorization":"Bearer SYNTHETIC_TELEMETRY_SECRET"}}`, "synthetic-telemetry-action")
	if w.Code != 200 || strings.Contains(w.Body.String(), "SYNTHETIC_TELEMETRY_SECRET") {
		t.Fatal(w.Code, w.Body.String())
	}
	now := time.Now().UTC()
	record := Record{ID: "test", AttemptID: "att", Operation: "generate", Protocol: "responses", Status: "succeeded", Submission: "possible", Started: now.Add(-time.Second), Ended: &now, AttemptStarted: now.Add(-time.Second)}
	source := Source{Provider: "openai_compatible"}
	for i := 0; i < 3000; i++ {
		record.ID = id("r")
		record.AttemptID = id("a")
		a.observeExecution(record, source, true)
	}
	if len(a.observations.queue) != 2048 || a.observations.dropped.Load() < 900 {
		t.Fatal("telemetry queue unbounded or blocking")
	}
	for len(a.observations.queue) > 0 {
		span := <-a.observations.queue
		if raw := encode(span); strings.Contains(raw, "SYNTHETIC") || strings.Contains(raw, "test-source-secret") || strings.Contains(raw, "key_id") || strings.Contains(raw, "prompt") {
			t.Fatal("sensitive telemetry attributes")
		}
	}
	var persisted string
	if e := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", "action_"+digest("synthetic-telemetry-action")).Scan(&persisted); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(persisted, "body_hash") || strings.Contains(persisted, "SYNTHETIC_TELEMETRY_SECRET") {
		t.Fatal("telemetry credential hash or secret in audit")
	}
	a.CloseAdmission()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := a.WaitOwnedTasks(ctx); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("setting or queue filling sent unsolicited collector request")
	}
}
