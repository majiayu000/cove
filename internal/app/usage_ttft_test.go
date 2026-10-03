package app

import (
	"encoding/json"
	"math"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUsageTTFTUsesSemanticContentAndExcludesUnknown(t *testing.T) {
	a := contractApp(t, nil)
	started := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	metadata := started.Add(10 * time.Millisecond)
	content := started.Add(2 * time.Second)
	mean := 2000.0
	for _, record := range []Record{
		{ID: "content", Started: started, FirstEvent: &metadata, FirstContentAt: &content},
		{ID: "metadata_only", Started: started, FirstEvent: &metadata},
		{ID: "no_events", Started: started},
	} {
		record.Status = "succeeded"
		record.SourceID = "source"
		record.KeyID = "key"
		record.Origin = "client"
		record.Protocol = "responses"
		record.Model = record.ID
		record.Completeness = "unknown"
		if err := a.Store.record(record); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		name, query string
		known       int
		mean        *float64
	}{
		{"mixed", "", 1, &mean},
		{"metadata only", "?model=metadata_only", 0, nil},
		{"no events", "?model=no_events", 0, nil},
		{"empty interval", "?from=" + started.Add(time.Hour).Format(time.RFC3339), 0, nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.usageAPI(w, httptest.NewRequest("GET", "/admin/usage"+fixture.query, nil))
			if w.Code != 200 {
				t.Fatalf("usage status %d: %s", w.Code, w.Body.String())
			}
			var usage struct {
				Known int      `json:"ttft_known_requests"`
				Mean  *float64 `json:"mean_ttft_ms"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil {
				t.Fatal(err)
			}
			if usage.Known != fixture.known || (usage.Mean == nil) != (fixture.mean == nil) {
				t.Fatalf("metadata or missing content counted as TTFT: %s", w.Body.String())
			}
			// SQLite julianday arithmetic has sub-millisecond rounding error.
			if fixture.mean != nil && math.Abs(*usage.Mean-*fixture.mean) > 1 {
				t.Fatalf("TTFT mean %f, want %f ms", *usage.Mean, *fixture.mean)
			}
		})
	}
}
