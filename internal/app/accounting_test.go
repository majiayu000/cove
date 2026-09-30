package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func accountingFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("accounting fixture must not call upstream") })
	if err := initializeAccounting(f.a.Store.DB); err != nil {
		t.Fatal(err)
	}
	return f
}
func testBudget(t *testing.T, f *fixture, scope BudgetScope, limit string) Budget {
	t.Helper()
	v := Budget{ID: id("budget"), Name: "Local test budget", Scope: scope, Currency: "USD", AmountLimit: limit, Mode: "soft", Period: BudgetPeriod{Kind: "calendar_day", Timezone: "UTC"}, Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = saveBudget(tx, v); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return v
}
func testAccountingRecord(t *testing.T, f *fixture, name, amount string, budgets ...Budget) Record {
	t.Helper()
	now := time.Now().UTC()
	plan := &AccountingPlan{EstimateProvenance: "fixture soft estimate"}
	for _, b := range budgets {
		start, end, err := budgetBounds(b.Period, now)
		if err != nil {
			t.Fatal(err)
		}
		plan.Reservations = append(plan.Reservations, PlannedReservation{BudgetID: b.ID, BudgetVersion: b.Version, PeriodStart: start, PeriodEnd: end, Amount: amount, Currency: b.Currency})
	}
	src, err := f.a.Store.source(f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	return Record{ID: name, KeyID: "key", SourceID: src.ID, AccountID: src.AccountID, AttemptID: name + "_attempt_1", Sequence: 1, Status: "dispatching", Started: now, Price: &Price{Currency: "USD", Input: "1", Output: "1"}, Completeness: "unknown", Accounting: plan}
}
func mustRat(t *testing.T, s string) *big.Rat {
	t.Helper()
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatal(s)
	}
	return n
}
func assertBudgetAmount(t *testing.T, f *fixture, b Budget, settled, reserved, pending string) {
	t.Helper()
	summary, err := summarizeBudget(f.a.Store.DB, b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ actual, want string }{{summary.Settled, settled}, {summary.Reserved, reserved}, {summary.Pending, pending}} {
		if mustRat(t, pair.actual).Cmp(mustRat(t, pair.want)) != 0 {
			t.Fatalf("summary %+v, wanted settled %s reserved %s pending %s", summary, settled, reserved, pending)
		}
	}
}
func TestSpecBudgetReservation(t *testing.T) {
	f := accountingFixture(t)
	instance := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	key := testBudget(t, f, BudgetScope{Kind: "key", ID: "key"}, "1")
	var admitted atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		rec := testAccountingRecord(t, f, fmt.Sprintf("parallel_%d", i), "0.1", instance, key)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := f.a.Store.record(rec)
			if err == nil {
				admitted.Add(1)
				return
			}
			var e *accountingError
			if !errors.As(err, &e) || e.Status != 429 {
				t.Errorf("unexpected admission error %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("admitted %d; want exactly 10 of 20", admitted.Load())
	}
	assertBudgetAmount(t, f, instance, "0", "1", "0")
	assertBudgetAmount(t, f, key, "0", "1", "0")
	var requests, attempts, reservations int
	for _, q := range []struct {
		query string
		to    *int
	}{{"SELECT count(*) FROM requests", &requests}, {"SELECT count(*) FROM attempts", &attempts}, {"SELECT count(*) FROM reservations", &reservations}} {
		if err := f.a.Store.DB.QueryRow(q.query).Scan(q.to); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 10 || attempts != 10 || reservations != 20 {
		t.Fatalf("partial transactions: request=%d attempt=%d reservation=%d", requests, attempts, reservations)
	}
}
func TestSpecBudgetReservationNoHalfReservation(t *testing.T) {
	f := accountingFixture(t)
	large := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	small := testBudget(t, f, BudgetScope{Kind: "key", ID: "key"}, "0.01")
	err := f.a.Store.record(testAccountingRecord(t, f, "rejected", "0.1", large, small))
	var e *accountingError
	if !errors.As(err, &e) || e.Status != 429 {
		t.Fatal(err)
	}
	assertBudgetAmount(t, f, large, "0", "0", "0")
	assertBudgetAmount(t, f, small, "0", "0", "0")
	var count int
	if err = f.a.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected admission persisted: %d %v", count, err)
	}
}
func TestSpecAccountingUnknownAndManualReconciliation(t *testing.T) {
	f := accountingFixture(t)
	b := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	rec := testAccountingRecord(t, f, "unknown", "0.5", b)
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC()
	partial := "0.2"
	rec.Ended = &end
	rec.Status = "interrupted"
	rec.PartialCost = &partial
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	assertBudgetAmount(t, f, b, "0", "0.5", "0.5")
	zero := "0"
	duplicate := rec
	duplicate.Cost = &zero
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = settleAccounting(tx, duplicate); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertBudgetAmount(t, f, b, "0", "0.5", "0.5")
	body := fmt.Sprintf(`{"version":1,"attempt_costs":[{"attempt_id":%q,"currency":"USD","amount":"0.3","reason":"Manual invoice comparison","evidence_ref":"fixture://invoice/1"}]}`, rec.AttemptID)
	request := httptest.NewRequest("POST", "/admin/requests/unknown/reconcile", bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	f.a.reconcileAccountingAPI(response, request, rec.ID)
	if response.Code != 200 {
		t.Fatalf("reconcile %d %s", response.Code, response.Body.String())
	}
	assertBudgetAmount(t, f, b, "0.3", "0", "0")
	var raw string
	if err = f.a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", rec.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original Record
	if err = json.Unmarshal([]byte(raw), &original); err != nil || original.Cost != nil {
		t.Fatalf("manual correction rewrote provider observation: %s %v", raw, err)
	}
	var audit string
	if err = f.a.Store.DB.QueryRow("SELECT data FROM accounting_audit WHERE request_id=?", rec.ID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(audit), []byte("manual_reported")) || !bytes.Contains([]byte(audit), []byte("fixture://invoice/1")) {
		t.Fatal("missing manual audit evidence")
	}
	response = httptest.NewRecorder()
	f.a.reconcileAccountingAPI(response, httptest.NewRequest("POST", "/admin/requests/unknown/reconcile", bytes.NewBufferString(body)), rec.ID)
	if response.Code != 409 {
		t.Fatalf("repeated reconcile must conflict, got %d", response.Code)
	}
}
func TestSpecAccountingRestartPreservesUnknownReservation(t *testing.T) {
	f := accountingFixture(t)
	b := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	if err := f.a.Store.record(testAccountingRecord(t, f, "crashed", "0.4", b)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(f.a.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	if err = initializeAccounting(reopened.DB); err != nil {
		t.Fatal(err)
	}
	summary, err := summarizeBudget(reopened.DB, b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if mustRat(t, summary.Reserved).Cmp(mustRat(t, "0.4")) != 0 || summary.Reserved != summary.Pending {
		t.Fatalf("lost restarted reservation %+v", summary)
	}
	var state string
	if err = reopened.DB.QueryRow("SELECT status FROM reservations WHERE request_id='crashed'").Scan(&state); err != nil || state != "pending_reconciliation" {
		t.Fatalf("recovery state %s %v", state, err)
	}
}
func TestSpecAccountingAttemptAllocationAndCrossPeriod(t *testing.T) {
	f := accountingFixture(t)
	b := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	rec := testAccountingRecord(t, f, "retry", "0.4", b)
	firstStart := rec.Accounting.Reservations[0].PeriodStart
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	rec.Ended = &ended
	rec.Status = "failed"
	rec.Submission = "not_sent"
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	assertBudgetAmount(t, f, b, "0", "0", "0")
	rec.AttemptID = "retry_attempt_2"
	rec.Sequence = 2
	rec.Ended = nil
	rec.Status = "dispatching"
	rec.Submission = ""
	rec.Accounting.Reservations[0].PeriodStart = firstStart.AddDate(0, 0, 1)
	rec.Accounting.Reservations[0].PeriodEnd = firstStart.AddDate(0, 0, 2)
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	actual := "0.2"
	rec.Cost = &actual
	rec.Ended = &ended
	rec.Status = "succeeded"
	rec.Completeness = "complete"
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	rows, err := f.a.Store.DB.Query("SELECT period_start,settled_amount,reserved_amount FROM reservations WHERE request_id='retry' ORDER BY period_start")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []string{}
	for rows.Next() {
		var start, settled, reserved string
		if err = rows.Scan(&start, &settled, &reserved); err != nil {
			t.Fatal(err)
		}
		if mustRat(t, reserved).Sign() != 0 {
			t.Fatal("reservation not released")
		}
		got = append(got, settled)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || mustRat(t, got[0]).Sign() != 0 || mustRat(t, got[1]).Cmp(mustRat(t, "0.2")) != 0 {
		t.Fatalf("attempt charged to each cycle %v", got)
	}
}
func TestSpecBudgetEligibility(t *testing.T) {
	f := accountingFixture(t)
	b := testBudget(t, f, BudgetScope{Kind: "key", ID: "key"}, "1")
	src := f.source
	src.Price = &Price{Currency: "USD", Input: "1", Output: "1"}
	key := ClientKey{ID: "key", SourceID: src.ID}
	body := map[string]json.RawMessage{"input": json.RawMessage(`"hello"`), "max_output_tokens": json.RawMessage(`10`)}
	plan, err := f.a.prepareAccounting(key, src, body)
	if err != nil || plan == nil || len(plan.Reservations) != 1 || plan.EstimateProvenance == "" {
		t.Fatalf("soft plan %v %v", plan, err)
	}
	b.Mode = "strict"
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = saveBudget(tx, b); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"api_key", "codex_subscription"} {
		src.Kind = kind
		_, err = f.a.prepareAccounting(key, src, body)
		var e *accountingError
		if !errors.As(err, &e) || e.Status != 422 || e.Field != "budget" {
			t.Fatalf("strict %s accepted: %v", kind, err)
		}
	}
	b.Mode = "soft"
	b.Currency = "EUR"
	tx, err = f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = saveBudget(tx, b); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = f.a.prepareAccounting(key, src, body)
	var e *accountingError
	if !errors.As(err, &e) || e.Field != "budget.currency" {
		t.Fatalf("currency conversion invented: %v", err)
	}
}
func TestSpecBudgetPeriodsDSTAndMonth(t *testing.T) {
	for _, fixture := range []struct {
		at    string
		kind  string
		hours int
		days  int
	}{{"2026-03-08T12:00:00Z", "calendar_day", 23, 0}, {"2026-11-01T12:00:00Z", "calendar_day", 25, 0}, {"2026-02-15T12:00:00Z", "calendar_month", 0, 28}, {"2026-03-15T12:00:00Z", "calendar_month", 0, 31}} {
		at, err := time.Parse(time.RFC3339, fixture.at)
		if err != nil {
			t.Fatal(err)
		}
		start, end, err := budgetBounds(BudgetPeriod{Kind: fixture.kind, Timezone: "America/New_York"}, at)
		if err != nil {
			t.Fatal(err)
		}
		if fixture.hours > 0 && end.Sub(start) != time.Duration(fixture.hours)*time.Hour {
			t.Fatalf("DST day = %s", end.Sub(start))
		}
		if fixture.days > 0 {
			loc, _ := time.LoadLocation("America/New_York")
			expected := start.In(loc).AddDate(0, 0, fixture.days)
			if !end.Equal(expected) {
				t.Fatalf("month %s to %s", start, end)
			}
		}
	}
}
func TestSpecBudgetKeyScopeAndDecimalPrecision(t *testing.T) {
	f := accountingFixture(t)
	foreign := testBudget(t, f, BudgetScope{Kind: "key", ID: "other_key"}, "1")
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	key := ClientKey{ID: "key", SourceID: f.source.ID}
	err = linkKeyBudget(tx, &key, foreign.ID, nil)
	var e *accountingError
	if !errors.As(err, &e) || e.Status != 409 {
		t.Fatalf("shared foreign key budget %v", err)
	}
	one := int64(1)
	zero := int64(0)
	amount := estimate(Usage{Input: &one, Output: &zero}, &Price{Currency: "USD", Input: "0.000000000001", Output: "0"})
	if amount == nil || mustRat(t, *amount).Cmp(big.NewRat(1, 1000000000000000000)) != 0 {
		t.Fatalf("small exact amount rounded: %v", amount)
	}
}
func TestSpecAccountingBudgetUpdateDoesNotBreakInFlightSettlement(t *testing.T) {
	f := accountingFixture(t)
	b := testBudget(t, f, BudgetScope{Kind: "instance"}, "1")
	rec := testAccountingRecord(t, f, "changed", "0.5", b)
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	b.Version++
	b.AmountLimit = "0.1"
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = saveBudget(tx, b); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC()
	amount := "0.4"
	rec.Ended = &end
	rec.Status = "succeeded"
	rec.Completeness = "complete"
	rec.Cost = &amount
	if err = f.a.Store.record(rec); err != nil {
		t.Fatalf("budget change blocked settlement %v", err)
	}
	assertBudgetAmount(t, f, b, "0.4", "0", "0")
	if err = f.a.Store.record(testAccountingRecord(t, f, "over_new_limit", "0.01", b)); err == nil {
		t.Fatal("new admission bypassed lowered budget")
	}
}
func TestSpecAccountingUsageSQLFiltersAndUnknown(t *testing.T) {
	f := accountingFixture(t)
	from := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Second)
	nums := func(v int64) *int64 { return &v }
	for i, at := range []time.Time{from.Add(-time.Nanosecond), from, from.Add(time.Nanosecond), to} {
		rec := testAccountingRecord(t, f, fmt.Sprintf("usage_%d", i), "0")
		rec.Accounting = nil
		rec.Started = at
		rec.Status = "succeeded"
		rec.Completeness = "complete"
		rec.Usage = Usage{Input: nums(3), Output: nums(2)}
		rec.Completeness = "complete"
		cost := "0.000000000000000001"
		rec.Cost = &cost
		rec.Price = &Price{Currency: "USD", Input: "1", Output: "1"}
		if i == 2 {
			rec.Origin = "admin_test"
			rec.Usage = Usage{}
			rec.Completeness = "unknown"
			rec.Cost = nil
		}
		if err := f.a.Store.record(rec); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest("GET", "/admin/usage?from="+from.Format(time.RFC3339Nano)+"&to="+to.Format(time.RFC3339Nano), nil)
	response := httptest.NewRecorder()
	f.a.usageAPI(response, request)
	if response.Code != 200 {
		t.Fatalf("usage %d %s", response.Code, response.Body.String())
	}
	var v struct {
		Requests int               `json:"requests"`
		Attempts int               `json:"attempts"`
		Unknown  int               `json:"usage_unknown_requests"`
		Tests    int               `json:"admin_test_requests"`
		Input    int               `json:"known_input_tokens"`
		Costs    map[string]string `json:"estimated_cost_by_currency"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Requests != 2 || v.Attempts != 2 || v.Unknown != 1 || v.Tests != 1 || v.Input != 3 || mustRat(t, v.Costs["USD"]).Cmp(big.NewRat(1, 1000000000000000000)) != 0 {
		t.Fatalf("interval or aggregation mismatch %s", response.Body.String())
	}
	response = httptest.NewRecorder()
	f.a.usageAPI(response, httptest.NewRequest("GET", "/admin/usage?from=invalid", nil))
	if response.Code != 400 {
		t.Fatalf("invalid bounds %d", response.Code)
	}
}

func TestSpecBudgetsAPI(t *testing.T) {
	f := accountingFixture(t)
	invoke := func(method, path, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		f.a.budgetsAPI(response, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		return response
	}
	create := `{"name":"Monthly","scope":{"kind":"instance"},"currency":"USD","amount_limit":"1","mode":"soft","period":{"kind":"calendar_month","timezone":"America/New_York"}}`
	response := invoke("POST", "/admin/budgets", create)
	if response.Code != 201 {
		t.Fatalf("create %d %s", response.Code, response.Body.String())
	}
	var b BudgetSummary
	if err := json.Unmarshal(response.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	rec := testAccountingRecord(t, f, "api_reserved", "0.5", b.Budget)
	if err := f.a.Store.record(rec); err != nil {
		t.Fatal(err)
	}
	response = invoke("PATCH", "/admin/budgets/"+b.ID, `{"version":1,"amount_limit":"0.1"}`)
	if response.Code != 200 {
		t.Fatalf("lower limit %d %s", response.Code, response.Body.String())
	}
	var lowered BudgetSummary
	if err := json.Unmarshal(response.Body.Bytes(), &lowered); err != nil {
		t.Fatal(err)
	}
	if mustRat(t, lowered.Available).Sign() >= 0 || lowered.BlockedReason == "" {
		t.Fatalf("lowering ignored occupied amount %+v", lowered)
	}
	response = invoke("PATCH", "/admin/budgets/"+b.ID, `{"version":2,"period":{"kind":"calendar_month","timezone":"UTC"}}`)
	if response.Code != 409 {
		t.Fatalf("timezone was not frozen: %d", response.Code)
	}
	response = invoke("DELETE", "/admin/budgets/"+b.ID, "")
	if response.Code != 409 {
		t.Fatalf("referenced budget deletion %d", response.Code)
	}
	response = invoke("PATCH", "/admin/budgets/"+b.ID, `{"version":2,"enabled":false}`)
	if response.Code != 200 {
		t.Fatalf("disable %d %s", response.Code, response.Body.String())
	}
	response = invoke("GET", "/admin/budgets", "")
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte("0.500000000000")) {
		t.Fatalf("disabled budget erased reservation %s", response.Body.String())
	}
	response = invoke("POST", "/admin/budgets", `{"name":"Foreign","scope":{"kind":"key","id":"missing"},"currency":"USD","amount_limit":"1","mode":"soft","period":{"kind":"calendar_day","timezone":"UTC"}}`)
	if response.Code != 409 {
		t.Fatalf("missing scope %d", response.Code)
	}
}

func TestSpecAccountingInlineKeyBudgetAtomic(t *testing.T) {
	f := accountingFixture(t)
	tx, err := f.a.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	key := ClientKey{ID: "inline_key", Name: "Inline key", SourceID: f.source.ID}
	if _, err = tx.Exec("INSERT INTO client_keys(id,digest,source_id,data) VALUES(?,?,?,?)", key.ID, digest("synthetic-inline-key"), key.SourceID, encode(key)); err != nil {
		t.Fatal(err)
	}
	b := &Budget{Name: "Key only", Currency: "USD", AmountLimit: "2", Mode: "soft", Period: BudgetPeriod{Kind: "calendar_month", Timezone: "UTC"}}
	if err = linkKeyBudget(tx, &key, "", b); err != nil {
		t.Fatal(err)
	}
	if key.BudgetID == "" {
		t.Fatal("inline budget not linked")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = f.a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", key.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored ClientKey
	if err = json.Unmarshal([]byte(raw), &stored); err != nil || stored.BudgetID != key.BudgetID {
		t.Fatal("key and budget were not atomically linked")
	}
	budget, err := readBudget(f.a.Store.DB, key.BudgetID)
	if err != nil || budget.Scope.Kind != "key" || budget.Scope.ID != key.ID {
		t.Fatalf("wrong inline scope %+v %v", budget, err)
	}
}

func TestSpecAccountingCacheCreationPriceDimensions(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	usage := Usage{Input: n(100), Output: n(20), Cached: n(30), CacheCreation: n(10), Reasoning: n(5)}
	price := &Price{Currency: "USD", Input: "2", Output: "8", Cached: "0.5", CacheCreation: "2.5"}
	amount := estimate(usage, price)
	if amount == nil || mustRat(t, *amount).Cmp(mustRat(t, "0.000320")) != 0 {
		t.Fatalf("cache creation charged as normal input: %v", amount)
	}
	price.CacheCreation = ""
	if estimate(usage, price) != nil {
		t.Fatal("missing cache creation rate claimed a full cost")
	}
	partial := estimatePartial(usage, price)
	if partial == nil || mustRat(t, *partial).Cmp(mustRat(t, "0.000295")) != 0 {
		t.Fatalf("wrong known partial creation cost %v", partial)
	}
	price.CacheCreation = "2.5"
	usage.CacheCreation = nil
	if estimate(usage, price) != nil {
		t.Fatal("unknown creation count treated as zero")
	}
}
