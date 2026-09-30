package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Regression cases for the independent Spec v1.2 review.
func TestReviewDisabledNativeModelCannotDispatch(t *testing.T) {
	for _, tc := range []struct{ name, protocol, provider, path, request, response string }{
		{"Responses", "responses", "openai_compatible", "/v1/responses", contractBody, contractJSON},
		{"Chat", "chat_completions", "openai_compatible", "/v1/chat/completions", `{"model":"fixture-model","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chatcmpl-review","object":"chat.completion","created":1,"model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`},
		{"Messages", "messages", "anthropic", "/v1/messages", `{"model":"fixture-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, `{"id":"msg_review","type":"message","role":"assistant","model":"fixture-model","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return contractResponse(tc.response, "application/json"), nil
			})
			src, err := a.Store.source("source")
			if err != nil {
				t.Fatal(err)
			}
			src.NativeProtocol, src.Provider = tc.protocol, tc.provider
			if err = a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			baseline := contractCall(a, "POST", tc.path, tc.request, "test-client-key")
			if baseline.Code != 200 || calls.Load() != 1 {
				t.Fatalf("baseline: %d %s calls=%d", baseline.Code, baseline.Body.String(), calls.Load())
			}
			models, err := a.Store.models("source")
			if err != nil || len(models) != 1 {
				t.Fatalf("fixture model: %v count=%d", err, len(models))
			}
			m := models[0]
			changed := lifecycleAdmin(a, "PATCH", "/admin/models/"+m.ID, encode(map[string]any{"version": m.Version, "enabled": false}), "")
			if changed.Code != 200 {
				t.Fatalf("disable: %d %s", changed.Code, changed.Body.String())
			}
			listed := contractCall(a, "GET", "/v1/models", "", "test-client-key")
			var catalog struct {
				Data []any `json:"data"`
			}
			if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &catalog) != nil || len(catalog.Data) != 0 {
				t.Fatalf("disabled model still listed: %d %s", listed.Code, listed.Body.String())
			}
			after := contractCall(a, "POST", tc.path, tc.request, "test-client-key")
			wantStatus := 422
			if tc.protocol == "responses" {
				wantStatus = 400 // Preserve Responses' existing request-validation contract.
			}
			if after.Code != wantStatus || calls.Load() != 1 {
				t.Fatalf("disabled model accepted: status=%d upstream_calls_before=1 upstream_calls_after=%d catalog_count=%d", after.Code, calls.Load(), len(catalog.Data))
			}
		})
	}
}

func TestReviewAdminBodyDoesNotHoldGatewayMutex(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(contractJSON, "application/json"), nil
	})
	hold := &contractHeldBody{entered: make(chan struct{}), release: make(chan struct{})}
	r := contractRequest("POST", "/admin/client-keys", "", "test-session")
	r.Body = hold
	adminDone := make(chan struct{})
	go func() { defer close(adminDone); a.ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-hold.entered:
	case <-time.After(time.Second):
		close(hold.release)
		<-adminDone
		t.Fatal("admin did not begin reading body")
	}
	available := a.mu.TryLock()
	if available {
		a.mu.Unlock()
	}
	modelDone := make(chan int, 1)
	go func() { modelDone <- contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key").Code }()
	blocked := false
	select {
	case status := <-modelDone:
		if status != 200 {
			t.Errorf("model status=%d", status)
		}
	case <-time.After(150 * time.Millisecond):
		blocked = true
	}
	close(hold.release)
	select {
	case <-adminDone:
	case <-time.After(time.Second):
		t.Fatal("admin did not finish")
	}
	if blocked {
		select {
		case status := <-modelDone:
			if status != 200 {
				t.Errorf("model status after release=%d", status)
			}
		case <-time.After(time.Second):
			t.Fatal("model did not finish after releasing admin upload")
		}
	}
	if !available || blocked {
		t.Fatalf("admin body read holds global mutex=%t; unrelated model request blocked until body release=%t", !available, blocked)
	}
}

func TestReviewBudgetKeyCanRotate(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(contractJSON, "application/json"), nil
	})
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Price = &Price{Currency: "USD", Input: "1", Output: "1"}
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	created := lifecycleAdmin(a, "POST", "/admin/client-keys", `{"name":"review-budget-key","source_id":"source","budget":{"name":"review-soft-budget","currency":"USD","amount_limit":"10","mode":"soft","enabled":true,"period":{"kind":"calendar_month","timezone":"UTC"}}}`, "")
	var result struct {
		Key    ClientKey `json:"key"`
		Secret string    `json:"secret"`
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &result) != nil || result.Key.BudgetID == "" {
		t.Fatalf("budget key setup failed status=%d", created.Code)
	}
	// Leave both settled spend and an in-flight reservation before rotation.
	if w := contractCall(a, "POST", "/v1/responses", contractBody, result.Secret); w.Code != 200 {
		t.Fatalf("old Key call: %d %s", w.Code, w.Body.String())
	}
	budget, err := readBudget(a.Store.DB, result.Key.BudgetID)
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := budgetBounds(budget.Period, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pending := Record{ID: "rotation-in-flight", KeyID: result.Key.ID, SourceID: src.ID, AttemptID: "rotation-attempt", Sequence: 1, Status: "dispatching", Started: time.Now().UTC(), Price: src.Price, Completeness: "unknown", Accounting: &AccountingPlan{Reservations: []PlannedReservation{{BudgetID: budget.ID, BudgetVersion: budget.Version, PeriodStart: start, PeriodEnd: end, Amount: "9.99", Currency: "USD"}}}}
	if err = a.Store.record(pending); err != nil {
		t.Fatal(err)
	}
	unresolved := pending
	unresolved.ID, unresolved.AttemptID = "rotation-unresolved", "rotation-unresolved-attempt"
	unresolved.Accounting = &AccountingPlan{Reservations: []PlannedReservation{{BudgetID: budget.ID, BudgetVersion: budget.Version, PeriodStart: start, PeriodEnd: end, Amount: "0.001", Currency: "USD"}}}
	if err = a.Store.record(unresolved); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	unresolved.Status, unresolved.Ended = "interrupted", &ended
	if err = a.Store.record(unresolved); err != nil {
		t.Fatal(err)
	}
	before, err := summarizeBudget(a.Store.DB, budget, time.Now())
	if err != nil || mustRat(t, before.Pending).Cmp(mustRat(t, "0.001")) != 0 {
		t.Fatalf("pending reconciliation setup: %+v %v", before, err)
	}
	current := result
	revokeAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	for i := 0; i < 2; i++ {
		input := map[string]any{"version": current.Key.Version}
		if i == 0 {
			input["revoke_at"] = revokeAt
		}
		rotated := lifecycleAdmin(a, "POST", "/admin/client-keys/"+current.Key.ID+"/rotate", encode(input), "")
		if rotated.Code != 201 || json.Unmarshal(rotated.Body.Bytes(), &current) != nil {
			t.Fatalf("budget Key rotation: %d %s", rotated.Code, rotated.Body.String())
		}
		if current.Key.BudgetID != budget.ID || current.Key.budgetScopeID() != result.Key.ID || current.Secret == result.Secret {
			t.Fatal("rotation changed budget ownership or reused secret")
		}
		if w := contractCall(a, "POST", "/v1/responses", contractBody, current.Secret); w.Code != 200 {
			t.Fatalf("rotated Key call: %d %s", w.Code, w.Body.String())
		}
	}
	old, err := a.Store.keyByID(result.Key.ID)
	if err != nil || old.RevokeAt == nil || !old.RevokeAt.Equal(revokeAt) || !keyValid(old, revokeAt.Add(-time.Second)) || keyValid(old, revokeAt) || !keyValid(current.Key, revokeAt) {
		t.Fatalf("rotation window changed Key validity: %v", err)
	}
	// Existing in-flight work settles against the same budget after rotation.
	now := time.Now().UTC()
	cost := "9.9999"
	pending.Status, pending.Ended, pending.Completeness, pending.Cost = "succeeded", &now, "complete", &cost
	if err = a.Store.record(pending); err != nil {
		t.Fatal(err)
	}
	after, err := summarizeBudget(a.Store.DB, budget, time.Now())
	if err != nil || mustRat(t, after.Settled).Cmp(mustRat(t, before.Settled)) <= 0 || after.Pending != before.Pending || mustRat(t, after.Reserved).Cmp(mustRat(t, before.Pending)) != 0 {
		t.Fatalf("old reservation failed to settle: %+v %v", after, err)
	}
	// All generations are rejected by the original budget once it is exhausted.
	for _, secret := range []string{result.Secret, current.Secret} {
		if w := contractCall(a, "POST", "/v1/responses", contractBody, secret); w.Code != 429 {
			t.Fatalf("rotation bypassed spent budget: %d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("over-budget requests reached upstream: %d", calls.Load())
	}
	foreign := lifecycleAdmin(a, "POST", "/admin/client-keys", encode(map[string]any{"name": "Foreign", "source_id": src.ID, "budget_id": budget.ID}), "")
	if foreign.Code != 409 {
		t.Fatalf("unrelated Key acquired rotation budget: %d", foreign.Code)
	}
	view := lifecycleAdmin(a, "GET", "/admin/client-keys/"+current.Key.ID, "", "")
	var decoded clientKeyView
	if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &decoded) != nil || len(decoded.BudgetSummary) != 1 || decoded.BudgetSummary[0].ID != budget.ID || decoded.BudgetSummary[0].Settled != after.Settled {
		t.Fatalf("rotated Key summary lost shared ledger: %d", view.Code)
	}
}

func TestReviewImplicitBudgetFollowsRotation(t *testing.T) {
	a := contractApp(t, nil)
	created := lifecycleAdmin(a, "POST", "/admin/client-keys", `{"name":"Implicit budget","source_id":"source"}`, "")
	var result struct {
		Key ClientKey `json:"key"`
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &result) != nil {
		t.Fatal("create failed")
	}
	budget := lifecycleAdmin(a, "POST", "/admin/budgets", encode(map[string]any{"name": "Key scope", "scope": BudgetScope{"key", result.Key.ID}, "currency": "USD", "amount_limit": "10", "mode": "soft", "period": BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}}), "")
	if budget.Code != 201 {
		t.Fatalf("budget create: %d", budget.Code)
	}
	owner := result.Key.ID
	rotated := lifecycleAdmin(a, "POST", "/admin/client-keys/"+owner+"/rotate", encode(map[string]any{"version": result.Key.Version}), "")
	if rotated.Code != 201 || json.Unmarshal(rotated.Body.Bytes(), &result) != nil {
		t.Fatal("rotation failed")
	}
	if result.Key.BudgetID != "" || result.Key.budgetScopeID() != owner {
		t.Fatal("implicit budget ownership changed")
	}
	newBudget := lifecycleAdmin(a, "POST", "/admin/budgets", encode(map[string]any{"name": "New generation scope", "scope": BudgetScope{"key", result.Key.ID}, "currency": "USD", "amount_limit": "5", "mode": "soft", "period": BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}}), "")
	if newBudget.Code != 201 {
		t.Fatalf("new generation budget create: %d", newBudget.Code)
	}
	for _, keyID := range []string{owner, result.Key.ID} {
		view := lifecycleAdmin(a, "GET", "/admin/client-keys/"+keyID, "", "")
		var decoded clientKeyView
		if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &decoded) != nil || len(decoded.BudgetSummary) != 2 {
			t.Fatalf("budget scope lost on %s: %d", keyID, view.Code)
		}
	}
}

func TestReviewSlowKeyMutationRechecksVersion(t *testing.T) {
	for _, operation := range []struct{ method, suffix string }{{"PATCH", ""}, {"POST", "/rotate"}, {"DELETE", ""}} {
		t.Run(operation.method+operation.suffix, func(t *testing.T) {
			a := contractApp(t, nil)
			created := lifecycleAdmin(a, "POST", "/admin/client-keys", `{"name":"CAS Key","source_id":"source"}`, "")
			var result struct {
				Key ClientKey `json:"key"`
			}
			if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &result) != nil {
				t.Fatal("create failed")
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			r := contractRequest(operation.method, "/admin/client-keys/"+result.Key.ID+operation.suffix, "", "test-session")
			r.Body, r.ContentLength = reader, 100
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); a.ServeHTTP(w, r); done <- w }()
			// A partial object proves the handler has entered the upload.
			if _, err := io.Copy(writer, strings.NewReader(`{"version":`)); err != nil {
				t.Fatal(err)
			}
			changed := lifecycleAdmin(a, "PATCH", "/admin/client-keys/"+result.Key.ID, encode(map[string]any{"version": result.Key.Version, "name": "Updated while uploading"}), "")
			if changed.Code != 200 {
				t.Fatalf("concurrent mutation: %d", changed.Code)
			}
			if _, err := io.Copy(writer, strings.NewReader(encode(result.Key.Version)+`}`)); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			select {
			case w := <-done:
				if w.Code != 409 {
					t.Fatalf("stale mutation accepted: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(time.Second):
				t.Fatal("slow mutation did not finish")
			}
		})
	}
}

func TestReviewNonCloudSourceEditPreservesEndpoint(t *testing.T) {
	a := contractApp(t, nil)
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.BaseURL = "https://synthetic.invalid/v1"
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	edited := lifecycleAdmin(a, "PATCH", "/admin/sources/source", encode(map[string]any{"version": src.Version, "name": "Renamed source", "base_url": src.BaseURL, "cloud_config": map[string]any{}}), "")
	if edited.Code != 200 {
		t.Fatalf("non-cloud name edit rejected: %d %s", edited.Code, edited.Body.String())
	}
	after, err := a.Store.source(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BaseURL != src.BaseURL || after.Generation != src.Generation {
		t.Fatal("name-only edit changed endpoint or credential binding")
	}
	changed := lifecycleAdmin(a, "PATCH", "/admin/sources/source", encode(map[string]any{"version": after.Version, "base_url": "https://different-origin.invalid/v1", "cloud_config": map[string]any{}}), "")
	if changed.Code != 400 {
		t.Fatalf("cross-origin change without new credential: %d", changed.Code)
	}
	final, err := a.Store.source(src.ID)
	if err != nil || final.BaseURL != src.BaseURL {
		t.Fatal("rejected edit changed the persisted endpoint")
	}
}
