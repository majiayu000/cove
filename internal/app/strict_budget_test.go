package app

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const strictTestBody = `{"model":"fixture-model","input":"strict synthetic input","max_output_tokens":50,"service_tier":"default","stream":false}`

func strictBudgetFixture(t *testing.T, transport contractTransport) (*fixture, Budget) {
	t.Helper()
	a := contractApp(t, transport)
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Provider = "openai"
	src.NativeProtocol = "responses"
	src.BaseURL = "https://api.openai.com/v1"
	src.Price = &Price{Currency: "USD", Input: "1", Cached: "0.5", Output: "1"}
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	src, err = a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: a, source: src, t: t}
	budget := testBudget(t, f, BudgetScope{Kind: "instance"}, "0.0002")
	budget.Mode = "strict"
	tx, err := a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = saveBudget(tx, budget); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return f, budget
}

func TestStrictBudgetOfficialCountReservationAndSettlement(t *testing.T) {
	var counts, generations atomic.Int32
	f, budget := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.openai.com" || r.Header.Get("Authorization") != "Bearer test-source-secret" {
			t.Error("count/generation escaped provider credential boundary")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/v1/responses/input_tokens":
			counts.Add(1)
			if _, ok := body["max_output_tokens"]; ok {
				t.Error("generation-only parameter reached count endpoint")
			}
			if string(body["input"]) != `"strict synthetic input"` || string(body["model"]) != `"fixture-model"` {
				t.Error("count differs from generation input")
			}
			return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
		case "/v1/responses":
			generations.Add(1)
			if string(body["max_output_tokens"]) != "50" || string(body["service_tier"]) != `"default"` {
				t.Error("output cap/pricing tier lost")
			}
			return contractResponse(`{"id":"strict-result","status":"completed","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens":7}}`, "application/json"), nil
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			return nil, errors.New("unexpected endpoint")
		}
	})
	first := contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key")
	if first.Code != 200 {
		t.Fatalf("qualified strict operation rejected: %d %s", first.Code, first.Body.String())
	}
	assertBudgetAmount(t, f, budget, "0.000127", "0", "0")
	second := contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key")
	if second.Code != 429 || generations.Load() != 1 || counts.Load() != 2 {
		t.Fatalf("over-budget generation dispatched: %d count=%d generate=%d", second.Code, counts.Load(), generations.Load())
	}
	var provenance string
	if err := f.a.Store.DB.QueryRow("SELECT allocation_json FROM reservations WHERE budget_id=?", budget.ID).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provenance, "strict") || !strings.Contains(provenance, "input_tokens") {
		t.Fatal("strict provider count provenance missing", provenance)
	}
}

func TestStrictBudgetCountFailureNeverGenerates(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		status     int
		network    bool
	}{
		{"provider_rejected", `{"error":"SYNTHETIC_PRIVATE_ERROR"}`, 401, false},
		{"missing_count", `{"object":"response.input_tokens"}`, 200, false},
		{"wrong_object", `{"object":"estimate","input_tokens":10}`, 200, false},
		{"negative", `{"object":"response.input_tokens","input_tokens":-1}`, 200, false},
		{"fractional", `{"object":"response.input_tokens","input_tokens":1.5}`, 200, false},
		{"network", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var generations atomic.Int32
			f, _ := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/responses/input_tokens" {
					generations.Add(1)
				}
				if tc.network {
					return nil, errors.New("SYNTHETIC_PRIVATE_ERROR")
				}
				resp := contractResponse(tc.wire, "application/json")
				resp.StatusCode = tc.status
				return resp, nil
			})
			result := contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key")
			if result.Code != 502 || generations.Load() != 0 || strings.Contains(result.Body.String(), "SYNTHETIC_PRIVATE_ERROR") {
				t.Fatalf("count failure violated admission/privacy: %d %s generation=%d", result.Code, result.Body.String(), generations.Load())
			}
		})
	}
}

func TestStrictBudgetSlowCountRevalidatesWithoutLock(t *testing.T) {
	for _, change := range []string{"key", "model", "source", "budget", "capacity"} {
		t.Run(change, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var generations atomic.Int32
			f, budget := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v1/responses/input_tokens" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
				}
				generations.Add(1)
				return contractResponse(contractJSON, "application/json"), nil
			})
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("strict count not dispatched")
			}
			defer close(release)
			mutated := make(chan error, 1)
			go func() {
				f.a.mu.Lock()
				defer f.a.mu.Unlock()
				var err error
				switch change {
				case "key":
					var k ClientKey
					k, err = f.a.Store.keyByDigest(digest("test-client-key"))
					k.Version++
					if err == nil {
						_, err = f.a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID)
					}
				case "source":
					var s Source
					s, err = f.a.Store.source(f.source.ID)
					s.Version++
					if err == nil {
						err = f.a.Store.saveSource(s)
					}
				case "model":
					var m SourceModel
					candidate, candidateErr := f.a.candidateFor(f.source, "fixture-model")
					m, err = candidate.Model, candidateErr
					m.Enabled = false
					m.Version++
					if err == nil {
						_, err = f.a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID)
					}
				case "budget":
					budget.Version++
					_, err = f.a.Store.DB.Exec("UPDATE budgets SET data=? WHERE id=?", encode(budget), budget.ID)
				case "capacity":
					limit := 1
					s, _ := f.a.Store.source(f.source.ID)
					s.MaxConcurrent = &limit
					err = f.a.Store.saveSource(s)
					f.a.accountActive[s.AccountID] = 1
				}
				mutated <- err
			}()
			select {
			case err := <-mutated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("slow count held global admission lock")
			}
			// Cleanup releases the blocked transport even if any assertion fails.
			release <- struct{}{}
			select {
			case got := <-result:
				if got.Code != 409 && got.Code != 429 {
					t.Fatalf("changed admission accepted: %d %s", got.Code, got.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("count waiter did not finish")
			}
			if generations.Load() != 0 {
				t.Fatal("stale count dispatched generation")
			}
		})
	}
}

func TestStrictBudgetAmountRoundsUpAndCoversCache(t *testing.T) {
	for _, tc := range []struct {
		name          string
		price         Price
		input, output int64
		want          string
	}{
		{"tiny_positive", Price{Currency: "USD", Input: "0.000000000000000001", Output: "0"}, 1, 0, "0.000000000000000001"},
		{"repeating_unit", Price{Currency: "USD", Units: []PriceUnit{{Dimension: "input_token", Amount: "1", Per: "3"}, {Dimension: "output_token", Amount: "0", Per: "1"}}}, 1, 0, "0.333333333333333334"},
		{"higher_cache_price", Price{Currency: "USD", Input: "1", Cached: "2", Output: "1"}, 1, 50, "0.000052"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			amount, err := strictBudgetAmount(&tc.price, tc.input, tc.output)
			if err != nil || mustRat(t, amount).Cmp(mustRat(t, tc.want)) != 0 {
				t.Fatalf("bound %s %v wanted %s", amount, err, tc.want)
			}
		})
	}
}

func TestStrictBudgetUnsupportedDimensionsAndOperations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Source, map[string]json.RawMessage)
	}{
		{"subscription", func(s *Source, _ map[string]json.RawMessage) { s.Kind = "codex_subscription" }},
		{"compatible_proxy", func(s *Source, _ map[string]json.RawMessage) { s.BaseURL = "https://proxy.invalid/v1" }},
		{"unknown_tier", func(_ *Source, b map[string]json.RawMessage) { delete(b, "service_tier") }},
		{"priority_tier", func(_ *Source, b map[string]json.RawMessage) { b["service_tier"] = json.RawMessage(`"priority"`) }},
		{"missing_output_cap", func(_ *Source, b map[string]json.RawMessage) { delete(b, "max_output_tokens") }},
		{"stateful", func(_ *Source, b map[string]json.RawMessage) { b["previous_response_id"] = json.RawMessage(`"opaque"`) }},
		{"media", func(_ *Source, b map[string]json.RawMessage) {
			b["input"] = json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"https://fixture.invalid/a.png"}]}]`)
		}},
		{"server_tool", func(_ *Source, b map[string]json.RawMessage) { b["tools"] = json.RawMessage(`[{"type":"web_search"}]`) }},
		{"request_price", func(s *Source, _ map[string]json.RawMessage) {
			s.Price = &Price{Currency: "USD", Units: []PriceUnit{{Dimension: "request", Amount: "1", Per: "1"}, {Dimension: "input_token", Amount: "1", Per: "1000000"}, {Dimension: "output_token", Amount: "1", Per: "1000000"}}}
		}},
		{"cache_creation", func(s *Source, _ map[string]json.RawMessage) { s.Price.CacheCreation = "2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := strictBudgetFixture(t, nil)
			src := f.source
			var body map[string]json.RawMessage
			_ = json.Unmarshal([]byte(strictTestBody), &body)
			tc.change(&src, body)
			key, _ := f.a.Store.keyByDigest(digest("test-client-key"))
			_, err := f.a.prepareAccounting(key, src, body)
			var e *accountingError
			if !errors.As(err, &e) || e.Status != 422 || e.Field != "budget" {
				t.Fatalf("unsupported strict qualification %v", err)
			}
		})
	}
}

func TestStrictBudgetConcurrentAdmissionAndUnknownRetention(t *testing.T) {
	const n = 8
	entered, release := make(chan struct{}, n), make(chan struct{})
	var generations atomic.Int32
	f, budget := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
		}
		generations.Add(1)
		return contractResponse(`{"id":"unknown-strict-result","status":"completed","output":[]}`, "application/json"), nil
	})
	f.a.Config.MaxConcurrent = n
	f.a.slots = make(chan struct{}, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() { done <- contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key").Code }()
	}
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("concurrent counters did not reach provider")
		}
	}
	close(release)
	admitted := 0
	for i := 0; i < n; i++ {
		select {
		case status := <-done:
			if status == 200 {
				admitted++
			} else if status != 429 {
				t.Fatal("unexpected concurrent admission", status)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent admission did not end")
		}
	}
	if admitted != 1 || generations.Load() != 1 {
		t.Fatal("strict budget over-admitted", admitted, generations.Load())
	}
	assertBudgetAmount(t, f, budget, "0", "0.000173", "0.000173")
	var allocationJSON string
	if err := f.a.Store.DB.QueryRow("SELECT allocation_json FROM reservations WHERE budget_id=?", budget.ID).Scan(&allocationJSON); err != nil {
		t.Fatal(err)
	}
	var allocations map[string]attemptAllocation
	if err := json.Unmarshal([]byte(allocationJSON), &allocations); err != nil {
		t.Fatal(err)
	}
	for _, a := range allocations {
		if a.InputTokensBound == nil || *a.InputTokensBound != 123 || a.OutputTokensBound == nil || *a.OutputTokensBound != 50 {
			t.Fatal("bound token evidence lost", allocationJSON)
		}
	}
}

func TestStrictBudgetRoutePreviewAndFunctionWire(t *testing.T) {
	var counts, generations atomic.Int32
	const tools = `[{"type":"function","name":"add","parameters":{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]}}]`
	f, _ := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if string(body["model"]) != `"fixture-model"` || string(body["tools"]) != tools || string(body["instructions"]) != `"Use the local function"` {
			t.Error("count/generation function context differs", encode(body))
		}
		if r.URL.Path == "/v1/responses/input_tokens" {
			counts.Add(1)
			return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
		}
		generations.Add(1)
		return contractResponse(`{"id":"strict-function","status":"completed","usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":0},"output_tokens":5}}`, "application/json"), nil
	})
	candidate, err := f.a.candidateFor(f.source, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	routeResult := contractCall(f.a, "POST", "/admin/routes", encode(map[string]any{"name": "Strict route", "strategy": "priority", "max_attempts": 2, "members": []map[string]any{{"model_id": candidate.Model.ID, "priority": 0, "weight": 1}}}), "test-session")
	if routeResult.Code != 201 {
		t.Fatal("route create", routeResult.Code, routeResult.Body.String())
	}
	var route Route
	if err = json.Unmarshal(routeResult.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	alias := contractCall(f.a, "POST", "/admin/model-aliases", encode(map[string]any{"public_model": "strict-public", "route_id": route.ID}), "test-session")
	if alias.Code != 201 {
		t.Fatal("alias create", alias.Code, alias.Body.String())
	}
	key, _ := f.a.Store.keyByDigest(digest("test-client-key"))
	bind := contractCall(f.a, "PATCH", "/admin/client-keys/key", encode(map[string]any{"version": key.Version, "target": map[string]any{"kind": "route", "id": route.ID}}), "test-session")
	if bind.Code != 200 {
		t.Fatal("key bind", bind.Code, bind.Body.String())
	}
	body := `{"model":"strict-public","input":"strict synthetic input","instructions":"Use the local function","tools":` + tools + `,"max_output_tokens":50,"service_tier":"default","stream":false}`
	preview := contractCall(f.a, "POST", "/admin/routes/"+route.ID+"/preview", encode(map[string]any{"model": "strict-public", "protocol": "responses", "request": json.RawMessage(body)}), "test-session")
	if preview.Code != 200 || counts.Load() != 0 || generations.Load() != 0 {
		t.Fatal("route preview called provider", preview.Code, preview.Body.String(), counts.Load(), generations.Load())
	}
	result := contractCall(f.a, "POST", "/v1/responses", body, "test-client-key")
	if result.Code != 200 || counts.Load() != 1 || generations.Load() != 1 {
		t.Fatal("strict route/function admission", result.Code, result.Body.String(), counts.Load(), generations.Load())
	}
}

func TestStrictBudgetSafeDialFailoverSharedAccount(t *testing.T) {
	var counts, generations atomic.Int32
	f, budget := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			counts.Add(1)
			return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
		}
		if generations.Add(1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic no-send dial failure")}
		}
		return contractResponse(`{"id":"strict-after-safe-failover","status":"completed","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens":7}}`, "application/json"), nil
	})
	limit := 1
	first := f.source
	first.MaxConcurrent = &limit
	if err := f.a.Store.saveSource(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second-strict-source"
	second.Name = "Shared account second source"
	if err := f.a.Store.saveSource(second); err != nil {
		t.Fatal(err)
	}
	c1, err := f.a.candidateFor(first, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := f.a.candidateFor(second, "fixture-model")
	if err != nil {
		t.Fatal(err)
	}
	result := contractCall(f.a, "POST", "/admin/routes", encode(map[string]any{"name": "Strict safe failover", "strategy": "priority", "max_attempts": 2, "members": []map[string]any{{"model_id": c1.Model.ID, "priority": 0, "weight": 1}, {"model_id": c2.Model.ID, "priority": 1, "weight": 1}}}), "test-session")
	if result.Code != 201 {
		t.Fatal(result.Code, result.Body.String())
	}
	var route Route
	if err = json.Unmarshal(result.Body.Bytes(), &route); err != nil {
		t.Fatal(err)
	}
	alias := contractCall(f.a, "POST", "/admin/model-aliases", encode(map[string]any{"public_model": "strict-safe", "route_id": route.ID}), "test-session")
	if alias.Code != 201 {
		t.Fatal(alias.Code, alias.Body.String())
	}
	key, _ := f.a.Store.keyByDigest(digest("test-client-key"))
	tpm := 173
	key.Limits.TPM = &tpm
	if _, err = f.a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	bind := contractCall(f.a, "PATCH", "/admin/client-keys/key", encode(map[string]any{"version": key.Version, "target": map[string]any{"kind": "route", "id": route.ID}}), "test-session")
	if bind.Code != 200 {
		t.Fatal(bind.Code, bind.Body.String())
	}
	response := contractCall(f.a, "POST", "/v1/responses", strings.Replace(strings.Replace(strictTestBody, "fixture-model", "strict-safe", 1), "strict synthetic", strings.Repeat("synthetic input ", 300), 1), "test-client-key")
	if response.Code != 200 || counts.Load() != 2 || generations.Load() != 2 {
		t.Fatalf("owned shared-account slot blocked safe failover: %d %s count=%d generation=%d", response.Code, response.Body.String(), counts.Load(), generations.Load())
	}
	assertBudgetAmount(t, f, budget, "0.000127", "0", "0")
	var attempts int
	if err = f.a.Store.DB.QueryRow("SELECT count(*) FROM attempts").Scan(&attempts); err != nil || attempts != 2 {
		t.Fatal("failover attempts not retained", attempts, err)
	}
}

func TestStrictBudgetCountSharesDeadlineAndAdminScope(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		var generations atomic.Int32
		f, _ := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v1/responses/input_tokens" {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			generations.Add(1)
			return contractResponse(contractJSON, "application/json"), nil
		})
		f.a.Config.TotalTimeout = 1
		started := time.Now()
		result := contractCall(f.a, "POST", "/v1/responses", strictTestBody, "test-client-key")
		if result.Code != 504 || generations.Load() != 0 || time.Since(started) > 2*time.Second {
			t.Fatal("count escaped common deadline", result.Code, generations.Load(), time.Since(started))
		}
	})
	t.Run("admin_test", func(t *testing.T) {
		f, budget := strictBudgetFixture(t, func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v1/responses/input_tokens" {
				return contractResponse(`{"object":"response.input_tokens","input_tokens":123}`, "application/json"), nil
			}
			return contractResponse(`{"id":"strict-admin","status":"completed","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens":7}}`, "application/json"), nil
		})
		w := httptest.NewRecorder()
		record := f.a.forward(w, contractRequest("POST", "/v1/responses", strictTestBody, "test-session"), f.source.ID)
		if w.Code != 200 || record == nil || record.Origin != "admin_test" {
			t.Fatal("admin strict budget rejected/misclassified", w.Code, w.Body.String())
		}
		assertBudgetAmount(t, f, budget, "0.000127", "0", "0")
	})
}
