package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type telemetrySettings struct {
	Version    int     `json:"version"`
	Enabled    bool    `json:"enabled"`
	Endpoint   string  `json:"endpoint"`
	SampleRate float64 `json:"sample_rate"`
	HeaderRef  string  `json:"-"`
}
type telemetryStored struct {
	telemetrySettings
	HeaderRef string `json:"header_ref,omitempty"`
}
type observedHistogram struct {
	Buckets [10]int64
	Count   int64
	Sum     float64
}

var observationBuckets = [10]float64{.1, .25, .5, 1, 2, 5, 10, 30, 60, 120}

type requestTrace struct {
	id, root, parent string
	sampled          bool
	started          time.Time
}

type runtimeObservations struct {
	traces                                   map[string]requestTrace
	storageErrors                            atomic.Int64
	credentialRefreshFailures                atomic.Int64
	mu                                       sync.Mutex
	Requests, Attempts                       map[string]int64
	Seen                                     map[string]bool
	Order                                    []string
	Duration, TTFT                           observedHistogram
	UpstreamBytes, InputTokens, OutputTokens int64
	Started                                  time.Time
	dropped                                  atomic.Int64
	queue                                    chan map[string]any
	cancel                                   context.CancelFunc
	settings                                 telemetrySettings
}

func newRuntimeObservations() *runtimeObservations {
	return &runtimeObservations{traces: map[string]requestTrace{}, Requests: map[string]int64{}, Attempts: map[string]int64{}, Seen: map[string]bool{}, Started: time.Now().UTC(), queue: make(chan map[string]any, 2048), settings: telemetrySettings{Version: 1, SampleRate: .1}}
}
func (h *observedHistogram) add(v float64) {
	for i, b := range observationBuckets {
		if v <= b {
			h.Buckets[i]++
		}
	}
	h.Count++
	h.Sum += v
}
func observationOutcome(r Record) string {
	if r.Status == "succeeded" {
		return "completed"
	}
	return metricLabel(r.Status, "failed", "cancelled", "interrupted", "rejected", "unverified")
}

// Call only after the terminal SQL transaction commits. Retry attempt completions
// use logical=false; the last completion contributes one logical request.
func (a *App) observeExecution(r Record, src Source, logical bool) {
	o := a.observations
	if o == nil || r.Ended == nil {
		return
	}
	outcome := observationOutcome(r)
	o.mu.Lock()
	attemptKey := "attempt:" + r.AttemptID
	requestKey := "request:" + r.ID
	newAttempt := !o.Seen[attemptKey]
	newLogical := logical && !o.Seen[requestKey]
	if !newAttempt && !newLogical {
		o.mu.Unlock()
		return
	}
	remember := func(k string) {
		o.Seen[k] = true
		o.Order = append(o.Order, k)
		if len(o.Order) > 65536 {
			delete(o.Seen, o.Order[0])
			o.Order = o.Order[1:]
		}
	}
	if newAttempt {
		remember(attemptKey)
		provider := metricLabel(src.Provider, "openai", "openai_compatible", "codex", "anthropic", "gemini", "azure", "bedrock", "vertex", "local", "ollama")
		o.Attempts[provider+"\x00"+outcome]++
		o.UpstreamBytes += r.UpstreamBytes
		if r.Submission != "not_sent" && r.Operation != "cache_hit" {
			if r.Usage.Input != nil {
				o.InputTokens += *r.Usage.Input
			}
			if r.Usage.Output != nil {
				o.OutputTokens += *r.Usage.Output
			}
		}
	}
	if newLogical {
		remember(requestKey)
		protocol := metricLabel(r.Protocol, "responses", "chat_completions", "messages", "gemini", "realtime_websocket")
		operationName := r.Operation
		if strings.HasPrefix(operationName, "files.") {
			operationName = "files"
		}
		operation := metricLabel(operationName, "generate", "compact", "images.generate", "images.edit", "audio.transcribe", "audio.translate", "audio.speech", "embeddings", "rerank", "background", "batch", "files", "count_tokens", "warmup", "realtime", "cache_hit")
		o.Requests[protocol+"\x00"+operation+"\x00"+outcome]++
		o.Duration.add(r.Ended.Sub(r.Started).Seconds())
		if r.FirstContentAt != nil && !r.FirstContentAt.Before(r.AttemptStarted) {
			o.TTFT.add(r.FirstContentAt.Sub(r.AttemptStarted).Seconds())
		}
	}
	settings := o.settings
	trace, found := o.traces[r.ID]
	if !found {
		trace = requestTrace{id: randomTraceID(16), root: randomTraceID(8)}
		if tid, parent, ok := validatedTraceParent(r.TraceParent); ok {
			trace.id, trace.parent = tid, parent
		}
		var selection [2]byte
		if _, err := rand.Read(selection[:]); err == nil {
			trace.sampled = float64(uint16(selection[0])<<8|uint16(selection[1]))/65536 < settings.SampleRate
		}
		if len(o.traces) >= 32768 {
			for old := range o.traces {
				delete(o.traces, old)
				break
			}
		}
	}
	start := r.Started.Add(-time.Duration(r.QueueMS) * time.Millisecond)
	if !r.RefreshTiming.Start.IsZero() && r.RefreshTiming.Start.Before(start) {
		start = r.RefreshTiming.Start
	}
	if trace.started.IsZero() || start.Before(trace.started) {
		trace.started = start
	}
	o.traces[r.ID] = trace
	o.mu.Unlock()
	if !settings.Enabled || !trace.sampled {
		return
	}
	attrs := []any{map[string]any{"key": "protocol", "value": map[string]string{"stringValue": r.Protocol}}, map[string]any{"key": "operation", "value": map[string]string{"stringValue": r.Operation}}, map[string]any{"key": "outcome", "value": map[string]string{"stringValue": outcome}}}
	status := 1
	if outcome != "completed" {
		status = 2
	}
	span := func(name, id, parent string, start, end time.Time, kind int) map[string]any {
		value := map[string]any{"traceId": trace.id, "spanId": id, "name": name, "kind": kind, "startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(end.UnixNano(), 10), "attributes": attrs, "status": map[string]int{"code": status}}
		if parent != "" {
			value["parentSpanId"] = parent
		}
		return value
	}
	if newAttempt {
		start := r.AttemptStarted
		if start.IsZero() {
			start = r.Started
		}
		o.enqueueSpan(span("gateway.attempt", randomTraceID(8), trace.root, start, *r.Ended, 3))
		if !r.RefreshTiming.Start.IsZero() && !r.RefreshTiming.End.Before(r.RefreshTiming.Start) {
			o.enqueueSpan(span("gateway.refresh", randomTraceID(8), trace.root, r.RefreshTiming.Start, r.RefreshTiming.End, 1))
		}
	}
	if newLogical {
		o.enqueueSpan(span("gateway.request", trace.root, trace.parent, trace.started, *r.Ended, 2))
		if r.QueueMS > 0 {
			o.enqueueSpan(span("gateway.queue", randomTraceID(8), trace.root, r.Started.Add(-time.Duration(r.QueueMS)*time.Millisecond), r.Started, 1))
		}
	}

}
func (o *runtimeObservations) enqueueSpan(span map[string]any) {
	select {
	case o.queue <- span:
	default:
		o.dropped.Add(1)
	}
}
func validatedTraceParent(raw string) (string, string, bool) {
	if len(raw) != 55 || raw[:3] != "00-" || raw[35] != '-' || raw[52] != '-' {
		return "", "", false
	}
	for _, part := range []string{raw[3:35], raw[36:52], raw[53:]} {
		if _, err := hex.DecodeString(part); err != nil || strings.ToLower(part) != part {
			return "", "", false
		}
	}
	if raw[3:35] == strings.Repeat("0", 32) || raw[36:52] == strings.Repeat("0", 16) {
		return "", "", false
	}
	return raw[3:35], raw[36:52], true
}
func (a *App) markStorageFailure() {
	a.storageFailed.Store(true)
	if a.observations != nil {
		a.observations.storageErrors.Add(1)
	}
}
func randomTraceID(n int) string {
	v := make([]byte, n)
	if _, e := rand.Read(v); e != nil {
		return ""
	}
	return hex.EncodeToString(v)
}

func (a *App) runtimeMetrics(w http.ResponseWriter, r *http.Request) {
	o := a.observations
	o.mu.Lock()
	var b strings.Builder
	b.WriteString("# TYPE cove_requests_total counter\n")
	write := func(name string, values map[string]int64, labels []string) {
		keys := []string{}
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := strings.Split(k, "\x00")
			parts := []string{}
			for i, l := range labels {
				parts = append(parts, l+"="+strconv.Quote(p[i]))
			}
			fmt.Fprintf(&b, "%s{%s} %d\n", name, strings.Join(parts, ","), values[k])
		}
	}
	write("cove_requests_total", o.Requests, []string{"protocol", "operation", "outcome"})
	b.WriteString("# TYPE cove_attempts_total counter\n")
	write("cove_attempts_total", o.Attempts, []string{"provider", "outcome"})
	for _, v := range []struct {
		Name      string
		Histogram observedHistogram
	}{{"cove_request_duration_seconds", o.Duration}, {"cove_ttft_seconds", o.TTFT}} {
		fmt.Fprintf(&b, "# TYPE %s histogram\n", v.Name)
		for i, limit := range observationBuckets {
			fmt.Fprintf(&b, "%s_bucket{le=%q} %d\n", v.Name, strconv.FormatFloat(limit, 'f', -1, 64), v.Histogram.Buckets[i])
		}
		fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n%s_count %d\n%s_sum %g\n", v.Name, v.Histogram.Count, v.Name, v.Histogram.Count, v.Name, v.Histogram.Sum)
	}
	fmt.Fprintf(&b, "# TYPE cove_upstream_bytes_total counter\ncove_upstream_bytes_total %d\n# TYPE cove_actual_input_tokens_total counter\ncove_actual_input_tokens_total %d\n# TYPE cove_actual_output_tokens_total counter\ncove_actual_output_tokens_total %d\ncove_telemetry_dropped_total %d\ncove_process_start_time_seconds %d\n", o.UpstreamBytes, o.InputTokens, o.OutputTokens, o.dropped.Load(), o.Started.Unix())
	fmt.Fprintf(&b, "# TYPE cove_storage_errors_total counter\ncove_storage_errors_total %d\n# TYPE cove_credential_refresh_failures_total counter\ncove_credential_refresh_failures_total %d\n", o.storageErrors.Load(), o.credentialRefreshFailures.Load())
	o.mu.Unlock()
	a.mu.Lock()
	fmt.Fprintf(&b, "cove_active_requests %d\ncove_queued_requests %d\ncove_storage_fault %d\n", len(a.running), len(a.queued), boolNumber(a.storageFailed.Load()))
	a.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = io.WriteString(w, b.String())
}
func boolNumber(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (a *App) telemetryAPI(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.observations
	o.mu.Lock()
	current := o.settings
	o.mu.Unlock()
	if r.Method == "GET" {
		writeJSON(w, 200, map[string]any{"settings": current, "queue_capacity": cap(o.queue), "queued_spans": len(o.queue), "dropped_spans": o.dropped.Load(), "scope": "since_process_start"})
		return
	}
	if r.Method != "PUT" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		telemetrySettings
		Headers map[string]string `json:"headers"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version != current.Version {
		fail(w, 409, "观测设置已修改", "version")
		return
	}
	if in.Enabled && (validateURL(in.Endpoint) != nil || !strings.HasPrefix(in.Endpoint, "https://") || in.SampleRate < 0 || in.SampleRate > 1) {
		fail(w, 400, "需 HTTPS OTLP endpoint 与0到1采样率", "")
		return
	}
	for name, value := range in.Headers {
		if name != "Authorization" && name != "X-API-Key" {
			fail(w, 400, "观测凭据header不支持", "headers")
			return
		}
		if strings.ContainsAny(value, "\r\n") || len(value) > 65536 {
			fail(w, 400, "凭据header无效", "headers")
			return
		}
	}
	next := in.telemetrySettings
	next.Version = current.Version + 1
	next.HeaderRef = current.HeaderRef
	if in.Headers != nil {
		next.HeaderRef = id("telemetry")
		if e := a.Secrets.Put(next.HeaderRef, encode(in.Headers)); e != nil {
			fail(w, 503, "观测凭据保存失败", "")
			return
		}
	}
	stored := telemetryStored{next, next.HeaderRef}
	if _, e := a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('telemetry',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(stored)); e != nil {
		if next.HeaderRef != current.HeaderRef {
			a.cleanupSecret(next.HeaderRef)
		}
		fail(w, 503, storageError().Error(), "")
		return
	}
	if current.HeaderRef != next.HeaderRef {
		a.cleanupSecret(current.HeaderRef)
	}
	o.mu.Lock()
	o.settings = next
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	if next.Enabled && !a.stopping {
		ctx, cancel := context.WithCancel(context.Background())
		o.cancel = cancel
		a.ownedTasks.Add(1)
		go func() { defer a.ownedTasks.Done(); a.telemetryLoop(ctx) }()
	}
	o.mu.Unlock()
	writeJSON(w, 200, map[string]any{"settings": next})
}
func (a *App) loadTelemetry() error {
	var raw string
	e := a.Store.DB.QueryRow("SELECT value FROM settings WHERE key='telemetry'").Scan(&raw)
	if e == sql.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	var stored telemetryStored
	if e = json.Unmarshal([]byte(raw), &stored); e != nil {
		return e
	}
	stored.telemetrySettings.HeaderRef = stored.HeaderRef
	a.observations.settings = stored.telemetrySettings
	return nil
}
func (a *App) telemetryLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	flushing := false
	for {
		if !flushing {
			select {
			case <-ctx.Done():
				flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer flushCancel()
				ctx = flushCtx
				flushing = true
			case <-ticker.C:
			}
		}
		if ctx.Err() != nil {
			a.observations.dropped.Add(int64(len(a.observations.queue)))
			return
		}
		batch := []any{}
		for len(batch) < 128 {
			select {
			case span := <-a.observations.queue:
				batch = append(batch, span)
			default:
				goto ready
			}
		}
	ready:
		if len(batch) == 0 {
			if flushing {
				return
			}
			continue
		}
		a.observations.mu.Lock()
		settings := a.observations.settings
		a.observations.mu.Unlock()
		if !settings.Enabled {
			continue
		}
		payload := []byte(encode(map[string]any{"resourceSpans": []any{map[string]any{"resource": map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]string{"stringValue": "cove"}}}}, "scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "cove", "version": Version}, "spans": batch}}}}}))
		var headers map[string]string
		if settings.HeaderRef != "" {
			secret, e := a.Secrets.Get(settings.HeaderRef)
			if e != nil {
				a.observations.dropped.Add(int64(len(batch)))
				continue
			}
			if json.Unmarshal([]byte(secret), &headers) != nil {
				a.observations.dropped.Add(int64(len(batch)))
				continue
			}
		}
		delivered := false
		for attempt := 0; attempt < 3; attempt++ {
			sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			req, e := http.NewRequestWithContext(sendCtx, "POST", settings.Endpoint, bytes.NewReader(payload))
			if e == nil {
				req.Header.Set("Content-Type", "application/json")
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				req.GetBody = nil
				client := &http.Client{Transport: a.HTTP.Transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				resp, e := client.Do(req)
				if e == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
					resp.Body.Close()
					delivered = resp.StatusCode >= 200 && resp.StatusCode < 300
				}
			}
			cancel()
			if delivered || ctx.Err() != nil || flushing {
				break
			}
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
			}
			timer.Stop()
		}
		if !delivered {
			a.observations.dropped.Add(int64(len(batch)))
		}
	}
}

// This reader counts provider wire bytes before any protocol conversion.
type observedResponseBody struct {
	io.ReadCloser
	count *int64
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	*b.count += int64(n)
	return n, e
}
