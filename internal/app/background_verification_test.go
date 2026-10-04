package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const backgroundVerificationCompleted = `{"id":"resp-verification","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":2}}`

func backgroundVerificationFixture(t *testing.T, transport contractTransport) (*App, Source, SourceModel, ClientKey) {
	t.Helper()
	a := resourceFixture(t, transport)
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Verification = Verification{Status: "untested", Capabilities: []string{}}
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	src, err = a.Store.source(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	models, err := a.Store.models(src.ID)
	if err != nil || len(models) != 1 {
		t.Fatal("missing selected model fixture", err)
	}
	key, err := a.Store.keyByDigest(digest("test-client-key"))
	if err != nil {
		t.Fatal(err)
	}
	key.ProtocolAllowlist = []string{"responses"}
	key.OperationAllowlist = []string{"generate", "background"}
	key.ModelAllowlist = []string{models[0].UpstreamModel}
	if _, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	return a, src, models[0], key
}

func backgroundVerificationAssertIdle(t *testing.T, a *App) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.slots) != 0 || len(a.running) != 0 {
		t.Fatal("verification leaked the shared executor")
	}
	for _, active := range a.keyActive {
		if active != 0 {
			t.Fatal("verification leaked Key admission")
		}
	}
	for _, active := range a.accountActive {
		if active != 0 {
			t.Fatal("verification leaked account admission")
		}
	}
}

func TestSpecBackgroundVerificationBootstrapExistingKeyAndModelPrice(t *testing.T) {
	var posts, gets atomic.Int32
	var received map[string]json.RawMessage
	a, src, model, key := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			posts.Add(1)
			body, _ := io.ReadAll(r.Body)
			if json.Unmarshal(body, &received) != nil || r.URL.Path != "/responses" {
				t.Error("invalid create envelope")
			}
			return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
		}
		gets.Add(1)
		if r.Method != "GET" || r.URL.Path != "/responses/resp-verification" {
			t.Error("poll changed owned native ID or replayed a mutation")
		}
		return resourceResponse(backgroundVerificationCompleted), nil
	})
	model.Price = &Price{Currency: "USD", Input: "3", Output: "7"}
	if _, err := a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(model), model.ID); err != nil {
		t.Fatal(err)
	}
	resourceBudget(t, a, "soft")
	record, passed, err := a.verifyBackground(context.Background(), src, model, key)
	if err != nil || !passed || record == nil || record.Status != "succeeded" || record.Origin != "verification" {
		t.Fatalf("background proof failed: passed=%v record=%v err=%v", passed, record != nil, err)
	}
	if posts.Load() != 1 || gets.Load() != 1 || string(received["input"]) != `"OK"` || string(received["max_output_tokens"]) != "32" || string(received["store"]) != "true" || string(received["background"]) != "true" {
		t.Fatal("verification did not use one bounded explicit background creation")
	}
	if record.Price == nil || record.Price.Input != "3" || record.Cost == nil || *record.Cost != "0.000029000000" {
		cost := "unknown"
		if record.Cost != nil {
			cost = *record.Cost
		}
		t.Fatalf("verification ignored the selected model price: price=%v cost=%s", record.Price, cost)
	}
	var status string
	var keys, requests, attempts int
	a.Store.DB.QueryRow("SELECT status FROM reservations WHERE request_id=?", record.ID).Scan(&status)
	a.Store.DB.QueryRow("SELECT count(*) FROM client_keys").Scan(&keys)
	a.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&requests)
	a.Store.DB.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts)
	if status != "settled" || keys != 1 || requests != 1 || attempts != 1 {
		t.Fatal("verification bypassed shared finances or minted a hidden Key")
	}
	current, _ := a.Store.source(src.ID)
	if current.Verification.Status == "passed" || slices.Contains(current.Verification.Capabilities, "background") {
		t.Fatal("helper published capability outside the API's final CAS transaction")
	}
	backgroundVerificationAssertIdle(t, a)
}

func TestSpecBackgroundVerificationPermissionsZeroCallsAndHeaderIsolation(t *testing.T) {
	var calls atomic.Int32
	a, src, model, key := backgroundVerificationFixture(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return resourceResponse(backgroundVerificationCompleted), nil
	})
	cases := []struct {
		name string
		edit func(*Source, *SourceModel, *ClientKey)
	}{
		{"foreign Key", func(_ *Source, _ *SourceModel, k *ClientKey) { k.SourceID = "other" }},
		{"route Key", func(_ *Source, _ *SourceModel, k *ClientKey) { k.RouteID = "route" }},
		{"revoked Key", func(_ *Source, _ *SourceModel, k *ClientKey) { k.Revoked = true }},
		{"missing generate", func(_ *Source, _ *SourceModel, k *ClientKey) { k.OperationAllowlist = []string{"background"} }},
		{"missing background", func(_ *Source, _ *SourceModel, k *ClientKey) { k.OperationAllowlist = []string{"generate"} }},
		{"wrong protocol", func(_ *Source, _ *SourceModel, k *ClientKey) { k.ProtocolAllowlist = []string{"messages"} }},
		{"wrong model", func(_ *Source, _ *SourceModel, k *ClientKey) { k.ModelAllowlist = []string{} }},
		{"disabled model", func(_ *Source, m *SourceModel, _ *ClientKey) { m.Enabled = false }},
		{"disabled source", func(s *Source, _ *SourceModel, _ *ClientKey) { s.Enabled = false }},
		{"unconfigured source", func(s *Source, _ *SourceModel, _ *ClientKey) { s.Configured = false }},
		{"undeclared operation", func(s *Source, _ *SourceModel, _ *ClientKey) { s.NativeOperations = nil }},
		{"subscription", func(s *Source, _ *SourceModel, _ *ClientKey) { s.Kind = "codex_subscription" }},
		{"cloud", func(s *Source, _ *SourceModel, _ *ClientKey) { s.Provider = "bedrock" }},
		{"non-native Responses", func(s *Source, _ *SourceModel, _ *ClientKey) { s.NativeProtocol = "chat_completions" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s, m, k := src, model, key
			test.edit(&s, &m, &k)
			r, passed, err := a.verifyBackground(context.Background(), s, m, k)
			if err == nil || passed || r != nil || calls.Load() != 0 {
				t.Fatal("invalid verification dispatched")
			}
		})
	}
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.Header.Set("X-Cove-Verification", key.ID)
	r.Header.Set("X-Cove-Client-Key-ID", key.ID)
	if _, err := a.resourceCurrentKey(r); err == nil || resourceVerificationOrigin(r.Context()) != "client" {
		t.Fatal("public headers selected trusted verification credentials")
	}
	r.Header.Set("Authorization", "Bearer test-client-key")
	selected, err := a.resourceCurrentKey(r)
	if err != nil || selected.ID != key.ID {
		t.Fatal("ordinary resource bearer contract changed")
	}
	var requests int
	a.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&requests)
	if requests != 0 {
		t.Fatal("denied preflight fabricated a financial request")
	}
}

func TestSpecBackgroundVerificationUnknownUsageAndTerminalFailures(t *testing.T) {
	cases := []struct {
		name, terminal string
		passed         bool
	}{
		{"unknown usage", `{"id":"resp-verification","status":"completed","output":[{"content":[{"type":"output_text","text":"OK"}]}]}`, true},
		{"empty output", `{"id":"resp-verification","status":"completed","output":[]}`, false},
		{"failed", `{"id":"resp-verification","status":"failed","output":[]}`, false},
		{"unknown state", `{"id":"resp-verification","status":"provider_new_state"}`, false},
		{"wrong ID", `{"id":"resp-other","status":"completed"}`, false},
		{"malformed", `{"id":`, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var posts, gets atomic.Int32
			a, src, model, key := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" {
					posts.Add(1)
					return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
				}
				gets.Add(1)
				return resourceResponse(test.terminal), nil
			})
			resourceBudget(t, a, "soft")
			record, passed, err := a.verifyBackground(context.Background(), src, model, key)
			if passed != test.passed || (test.passed && err != nil) || (!test.passed && err == nil) || record == nil || record.Cost != nil || posts.Load() != 1 || gets.Load() != 1 {
				t.Fatalf("unproven evidence accepted or cost invented: passed=%v err=%v", passed, err)
			}
			var status string
			a.Store.DB.QueryRow("SELECT status FROM reservations WHERE request_id=?", record.ID).Scan(&status)
			if status != "pending" && status != "pending_reconciliation" {
				t.Fatal("unknown expenditure became reusable balance")
			}
			backgroundVerificationAssertIdle(t, a)
		})
	}
}

func TestSpecBackgroundVerificationCancellationAndEpochChangeKeepJob(t *testing.T) {
	for _, change := range []string{"cancel", "source generation", "model version", "Key version"} {
		t.Run(change, func(t *testing.T) {
			var posts, gets atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var a *App
			a, src, model, key := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" {
					posts.Add(1)
					return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
				}
				gets.Add(1)
				if change == "cancel" {
					cancel()
					return nil, r.Context().Err()
				}
				a.mu.Lock()
				switch change {
				case "source generation":
					s, _ := a.Store.source("source")
					s.Generation++
					a.Store.saveSource(s)
				case "model version":
					models, _ := a.Store.models("source")
					m := models[0]
					m.Version++
					a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID)
				case "Key version":
					k, _ := a.Store.keyByDigest(digest("test-client-key"))
					k.Version++
					a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID)
				}
				a.mu.Unlock()
				return resourceResponse(backgroundVerificationCompleted), nil
			})
			record, passed, err := a.verifyBackground(ctx, src, model, key)
			if err == nil || passed || record == nil || posts.Load() != 1 || gets.Load() != 1 {
				t.Fatal("changed or cancelled observation published proof or replayed POST")
			}
			var native string
			a.Store.DB.QueryRow("SELECT native_id FROM jobs WHERE request_id=?", record.ID).Scan(&native)
			if native != "resp-verification" {
				t.Fatal("cancellation discarded the persistent remote job")
			}
			backgroundVerificationAssertIdle(t, a)
		})
	}
}

func TestSpecBackgroundVerificationSharedCapacityRateAndBudget(t *testing.T) {
	for _, mode := range []string{"concurrent", "RPM", "TPM", "strict budget", "exhausted soft budget"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, src, model, key := backgroundVerificationFixture(t, func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return resourceResponse(backgroundVerificationCompleted), nil
			})
			one := 1
			switch mode {
			case "concurrent":
				a.mu.Lock()
				a.slots <- struct{}{}
				a.mu.Unlock()
				defer func() { a.mu.Lock(); <-a.slots; a.mu.Unlock() }()
			case "RPM":
				key.Limits.RPM = &one
				a.rateBuckets[key.ID] = &rateBucket{RPM: 1, Tokens: 0, At: time.Now()}
			case "TPM":
				key.Limits.TPM = &one
			case "strict budget":
				resourceBudget(t, a, "strict")
			case "exhausted soft budget":
				resourceBudget(t, a, "soft")
				b, _ := readBudget(a.Store.DB, "resource-budget")
				b.AmountLimit = "0.000001"
				tx, _ := a.Store.DB.Begin()
				if err := saveBudget(tx, b); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
				t.Fatal(err)
			}
			record, passed, err := a.verifyBackground(context.Background(), src, model, key)
			if err == nil || passed || record != nil || calls.Load() != 0 {
				t.Fatal("verification bypassed shared rate, capacity or budget admission")
			}
			if mode != "concurrent" {
				backgroundVerificationAssertIdle(t, a)
			}
		})
	}
}

func TestSpecBackgroundVerificationManagementOperationPublishesOnlyProof(t *testing.T) {
	for _, text := range []bool{true, false} {
		t.Run(map[bool]string{true: "passed", false: "empty"}[text], func(t *testing.T) {
			var posts, gets atomic.Int32
			a, src, model, key := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "POST" {
					posts.Add(1)
					return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
				}
				gets.Add(1)
				if text {
					return resourceResponse(backgroundVerificationCompleted), nil
				}
				return resourceResponse(`{"id":"resp-verification","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}`), nil
			})
			body := map[string]any{"protocol": "responses", "features": []string{"background"}, "expected_source_generation": src.Generation}
			denied := lifecycleAdmin(a, "POST", "/admin/models/"+model.ID+"/verify", encode(body), "")
			if denied.Code != 400 || posts.Load() != 0 {
				t.Fatal("management minted an implicit Key")
			}
			body["client_key_id"] = key.ID
			w := lifecycleAdmin(a, "POST", "/admin/models/"+model.ID+"/verify", encode(body), "")
			op := operationDone(t, a, w)
			if op.State != "succeeded" {
				t.Fatal("management operation failed", op.Error)
			}
			current, _ := a.Store.source(src.ID)
			latest, _ := a.Store.model(model.ID)
			if slices.Contains(current.Verification.Capabilities, "background") != text || len(latest.VerificationResults) != 1 || (latest.VerificationResults[0].Status == "passed") != text || posts.Load() != 1 || gets.Load() != 1 {
				t.Fatal("API published unsupported qualification or dispatched twice")
			}
			records := waitRecords(t, a, 1)
			if records[0].Origin != "verification" || len(latest.VerificationResults[0].RequestIDs) != 1 || latest.VerificationResults[0].RequestIDs[0] != records[0].ID {
				t.Fatal("qualification lacks its real verification record")
			}
		})
	}
}

func TestSpecBackgroundVerificationImmediateCompleteStillRetrievesAndDeadline(t *testing.T) {
	var posts, gets atomic.Int32
	a, src, model, key := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" {
			posts.Add(1)
		} else {
			gets.Add(1)
		}
		return resourceResponse(backgroundVerificationCompleted), nil
	})
	record, passed, err := a.verifyBackground(context.Background(), src, model, key)
	if err != nil || !passed || record == nil || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("immediate completion skipped retrieval proof")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, passed, err = a.verifyBackground(ctx, src, model, key)
	if err == nil || passed || posts.Load() != 1 {
		t.Fatal("cancelled verification issued a mutation")
	}
	if resourceVerificationOrigin(context.Background()) != "client" {
		t.Fatal("ordinary origin changed")
	}
	if strings.Contains(encode(record), "test-source-secret") || errors.Is(err, io.EOF) {
		t.Fatal("private credentials entered verification record")
	}
	backgroundVerificationAssertIdle(t, a)
	t.Run("bounded pending observation", func(t *testing.T) {
		var mutations, observations atomic.Int32
		pending, source, selectedModel, selectedKey := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
			if r.Method == "POST" {
				mutations.Add(1)
				// Creation may consume the observation deadline. A known pending
				// response must remain owned without requiring a GET after expiry.
				time.Sleep(150 * time.Millisecond)
			} else {
				observations.Add(1)
			}
			return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		kept, qualified, err := pending.verifyBackground(ctx, source, selectedModel, selectedKey)
		if !errors.Is(err, context.DeadlineExceeded) || qualified || mutations.Load() > 1 || observations.Load() > mutations.Load() || (mutations.Load() == 1 && (kept == nil || kept.Ended != nil)) {
			t.Fatalf("deadline state: err=%v qualified=%t kept=%t ended=%t creations=%d observations=%d", err, qualified, kept != nil, kept != nil && kept.Ended != nil, mutations.Load(), observations.Load())
		}
		backgroundVerificationAssertIdle(t, pending)
	})
	t.Run("cancel after first pending observation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mutations, observations atomic.Int32
		pending, source, selectedModel, selectedKey := backgroundVerificationFixture(t, func(r *http.Request) (*http.Response, error) {
			if r.Method == "POST" {
				mutations.Add(1)
			} else {
				observations.Add(1)
				cancel()
			}
			return resourceResponse(`{"id":"resp-verification","status":"queued"}`), nil
		})
		kept, qualified, err := pending.verifyBackground(ctx, source, selectedModel, selectedKey)
		if !errors.Is(err, context.Canceled) || qualified || kept == nil || kept.Ended != nil || mutations.Load() != 1 || observations.Load() != 1 {
			t.Fatalf("cancellation state: err=%v qualified=%t kept=%t ended=%t creations=%d observations=%d", err, qualified, kept != nil, kept != nil && kept.Ended != nil, mutations.Load(), observations.Load())
		}
		backgroundVerificationAssertIdle(t, pending)
	})
}
