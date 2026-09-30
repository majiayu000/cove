package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const accountingSchema = `CREATE TABLE IF NOT EXISTS budgets(id TEXT PRIMARY KEY, scope_kind TEXT NOT NULL CHECK(scope_kind IN ('instance','key','route')), scope_id TEXT, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS budgets_scope ON budgets(scope_kind,scope_id);
CREATE TABLE IF NOT EXISTS reservations(request_id TEXT NOT NULL REFERENCES requests(id), budget_id TEXT NOT NULL REFERENCES budgets(id), period_start TEXT NOT NULL, period_end TEXT NOT NULL, reserved_amount TEXT NOT NULL, allocation_json TEXT NOT NULL, settled_amount TEXT NOT NULL, pending_amount TEXT NOT NULL, currency TEXT NOT NULL, budget_version INTEGER NOT NULL, status TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(request_id,budget_id,period_start));
CREATE INDEX IF NOT EXISTS reservation_period ON reservations(budget_id,period_start,status);
CREATE TABLE IF NOT EXISTS accounting_audit(id TEXT PRIMARY KEY, request_id TEXT REFERENCES requests(id), created_at TEXT NOT NULL, data TEXT NOT NULL);`

type BudgetScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}
type BudgetPeriod struct {
	Kind     string     `json:"kind"`
	Timezone string     `json:"timezone"`
	StartAt  *time.Time `json:"start_at,omitempty"`
	EndAt    *time.Time `json:"end_at,omitempty"`
}
type Budget struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Scope       BudgetScope  `json:"scope"`
	Currency    string       `json:"currency"`
	AmountLimit string       `json:"amount_limit"`
	Mode        string       `json:"mode"`
	Period      BudgetPeriod `json:"period"`
	Enabled     bool         `json:"enabled"`
	Version     int          `json:"version"`
	CreatedAt   time.Time    `json:"created_at"`
}
type BudgetSummary struct {
	Budget
	PeriodStart   time.Time `json:"period_start"`
	PeriodEnd     time.Time `json:"period_end"`
	Settled       string    `json:"settled"`
	Reserved      string    `json:"reserved"`
	Pending       string    `json:"pending"`
	Available     string    `json:"available"`
	Eligibility   string    `json:"eligibility"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
}
type PlannedReservation struct {
	BudgetID      string
	BudgetVersion int
	PeriodStart   time.Time
	PeriodEnd     time.Time
	Amount        string
	Currency      string
}
type AccountingPlan struct {
	Reservations             []PlannedReservation
	EstimateProvenance       string
	EstimatedInputTokens     int64
	EstimatedOutputTokens    int64
	StrictInputCountRequired bool
	strictCountVerified      bool
	strictInput              map[string]json.RawMessage
}
type accountingError struct {
	Status  int
	Field   string
	Message string
}

func (e *accountingError) Error() string { return e.Message }
func accountingFailure(w http.ResponseWriter, err error) {
	var e *accountingError
	if errors.As(err, &e) {
		fail(w, e.Status, e.Message, e.Field)
	} else {
		fail(w, 503, storageError().Error(), "")
	}
}
func accountingRat(s string) (*big.Rat, error) {
	if !decimalPattern.MatchString(s) {
		return nil, fmt.Errorf("金额必须为非负十进制字符串")
	}
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("无效金额")
	}
	return n, nil
}

// Token prices have at most twelve decimals and are quoted per million; eighteen
// fractional places preserve the exact result without SQLite REAL conversion.
func accountingMoney(n *big.Rat) string {
	s := n.FloatString(18)
	for strings.HasSuffix(s, "0") && len(s)-strings.IndexByte(s, '.')-1 > 12 {
		s = strings.TrimSuffix(s, "0")
	}
	return s
}
func ledgerRat(s string) (*big.Rat, error) {
	n, ok := new(big.Rat).SetString(s)
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("invalid stored accounting amount")
	}
	return n, nil
}
func initializeAccounting(db *sql.DB) error {
	if _, err := db.Exec(accountingSchema); err != nil {
		return err
	}
	// Startup recovery preserves every reservation. It never interprets an
	// interrupted observation as a zero-cost provider execution.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.request_id,r.budget_id,r.period_start,r.allocation_json FROM reservations r JOIN requests q ON q.id=r.request_id WHERE q.status='interrupted' AND r.status IN ('pending','pending_reconciliation')`)
	if err != nil {
		return err
	}
	type recovery struct{ request, budget, start, raw string }
	var pending []recovery
	for rows.Next() {
		var v recovery
		if err = rows.Scan(&v.request, &v.budget, &v.start, &v.raw); err != nil {
			break
		}
		pending = append(pending, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range pending {
		var allocations map[string]attemptAllocation
		if err = json.Unmarshal([]byte(v.raw), &allocations); err != nil {
			return err
		}
		for k, a := range allocations {
			if a.State == "pending" {
				a.State = "pending_reconciliation"
				allocations[k] = a
			}
		}
		if err = saveAllocations(tx, v.request, v.budget, v.start, v.raw, allocations); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func budgetBounds(p BudgetPeriod, now time.Time) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("选择有效 IANA 时区")
	}
	local := now.In(loc)
	switch p.Kind {
	case "calendar_day":
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		return start.UTC(), start.AddDate(0, 0, 1).UTC(), nil
	case "calendar_month":
		start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
		return start.UTC(), start.AddDate(0, 1, 0).UTC(), nil
	case "fixed":
		if p.StartAt == nil || p.EndAt == nil || !p.StartAt.Before(*p.EndAt) {
			return time.Time{}, time.Time{}, fmt.Errorf("固定周期必须提供 start_at < end_at")
		}
		return p.StartAt.UTC(), p.EndAt.UTC(), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("period.kind 无效")
}
func validateBudget(v Budget) error {
	if strings.TrimSpace(v.Name) == "" || len(v.Name) > 100 {
		return &accountingError{400, "name", "预算名称需要 1 到 100 字符"}
	}
	if !currencyPattern.MatchString(v.Currency) {
		return &accountingError{400, "currency", "币种需要三位大写代码"}
	}
	if _, err := accountingRat(v.AmountLimit); err != nil {
		return &accountingError{400, "amount_limit", err.Error()}
	}
	if v.Mode != "soft" && v.Mode != "strict" {
		return &accountingError{400, "mode", "mode 必须为 soft 或 strict"}
	}
	if (v.Scope.Kind == "instance" && v.Scope.ID != "") || (v.Scope.Kind != "instance" && v.Scope.Kind != "key" && v.Scope.Kind != "route") || (v.Scope.Kind != "instance" && v.Scope.ID == "") {
		return &accountingError{400, "scope", "scope 必须为实例或有效 Key/路由 ID"}
	}
	if _, _, err := budgetBounds(v.Period, time.Now()); err != nil {
		return &accountingError{400, "period", err.Error()}
	}
	return nil
}

type accountingQuery interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func readBudget(q accountingQuery, bid string) (Budget, error) {
	var v Budget
	var raw string
	err := q.QueryRow("SELECT data FROM budgets WHERE id=?", bid).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v)
	}
	return v, err
}
func saveBudget(tx *sql.Tx, v Budget) error {
	var scopeID any
	if v.Scope.ID != "" {
		scopeID = v.Scope.ID
	}
	_, err := tx.Exec("INSERT INTO budgets(id,scope_kind,scope_id,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, v.Scope.Kind, scopeID, encode(v))
	return err
}
func validateBudgetScope(tx *sql.Tx, v Budget) error {
	var count int
	switch v.Scope.Kind {
	case "key":
		if err := tx.QueryRow("SELECT count(*) FROM client_keys WHERE id=?", v.Scope.ID).Scan(&count); err != nil {
			return err
		}
	case "route":
		if err := tx.QueryRow("SELECT count(*) FROM routes WHERE id=?", v.Scope.ID).Scan(&count); err != nil {
			return err
		}
	default:
		count = 1
	}
	if count == 0 {
		return &accountingError{409, "scope", "预算作用域不存在"}
	}
	return nil
}

// linkKeyBudget runs in the same transaction that creates or updates the Key.
// The caller must insert a new Key before invoking this helper for an inline budget.
func linkKeyBudget(tx *sql.Tx, k *ClientKey, budgetID string, budget *Budget) error {
	if budgetID != "" && budget != nil {
		return &accountingError{400, "budget", "budget 与 budget_id 不能同时提交"}
	}
	if budget != nil {
		v := *budget
		if v.Scope.Kind != "" && (v.Scope.Kind != "key" || v.Scope.ID != "" && v.Scope.ID != k.ID && v.Scope.ID != k.budgetScopeID()) {
			return &accountingError{409, "budget.scope", "内联预算只能属于当前 Key"}
		}
		v.ID, v.Scope, v.Version, v.CreatedAt = id("budget"), BudgetScope{"key", k.budgetScopeID()}, 1, time.Now().UTC()
		v.Enabled = true
		if err := validateBudget(v); err != nil {
			return err
		}
		if err := saveBudget(tx, v); err != nil {
			return err
		}
		budgetID = v.ID
	}
	if budgetID != "" {
		v, err := readBudget(tx, budgetID)
		if err == sql.ErrNoRows {
			return &accountingError{409, "budget_id", "预算不存在"}
		}
		if err != nil {
			return err
		}
		if !v.Enabled || v.Scope.Kind == "key" && v.Scope.ID != k.budgetScopeID() || v.Scope.Kind == "route" && v.Scope.ID != k.RouteID {
			return &accountingError{409, "budget_id", "预算未启用或不属于当前 Key/路由"}
		}
	}
	k.BudgetID = budgetID
	_, err := tx.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID)
	return err
}
func matchingBudgets(q accountingQuery, keyID, routeID string) ([]Budget, error) {
	rows, err := q.Query("SELECT data FROM budgets WHERE scope_kind='instance' OR (scope_kind='key' AND scope_id=?) OR (scope_kind='route' AND scope_id=?) ORDER BY id", keyID, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Budget{}
	for rows.Next() {
		var raw string
		var v Budget
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}

const strictBudgetReason = "严格预算不适用：仅支持官方OpenAI Responses的无状态文本/函数工具、明确default服务层、正max_output_tokens与完整token价格；其他操作没有可信完整计费上界，tokenizer估算不能作为计费上界"

func (a *App) prepareAccounting(key ClientKey, src Source, body map[string]json.RawMessage) (*AccountingPlan, error) {
	budgets, err := matchingBudgets(a.Store.DB, key.budgetScopeID(), key.RouteID)
	if err != nil {
		return nil, err
	}
	if key.BudgetID != "" {
		v, e := readBudget(a.Store.DB, key.BudgetID)
		if e != nil {
			return nil, e
		}
		if v.Scope.Kind == "key" && v.Scope.ID != key.budgetScopeID() || v.Scope.Kind == "route" && v.Scope.ID != key.RouteID {
			return nil, &accountingError{409, "budget_id", "Key 的预算作用域无效"}
		}
	}
	if len(budgets) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	plan := &AccountingPlan{EstimateProvenance: "soft estimate; o200k_base; tokenizer-0.8.1; uncached JSON input; output cap or 4096 soft default"}
	for _, v := range budgets {
		if v.Mode == "strict" && !plan.StrictInputCountRequired {
			count, e := strictResponsesBudgetInput(src, body)
			if e != nil {
				return nil, e
			}
			plan.strictInput = count
			plan.StrictInputCountRequired = true
		}
		if src.Price == nil || !validPrice(*src.Price) {
			return nil, &accountingError{422, "budget", "软限制无法计量：所选模型没有有效的用户配置价格；费用保持未知"}
		}
		if src.Price.Currency != v.Currency {
			return nil, &accountingError{422, "budget.currency", "所选模型价格币种与预算不同；未配置汇率换算"}
		}
		start, end, e := budgetBounds(v.Period, now)
		if e != nil {
			return nil, e
		}
		if now.Before(start) || !now.Before(end) {
			return nil, &accountingError{429, "budget.period", "固定预算周期尚未开始或已结束"}
		}
		plan.Reservations = append(plan.Reservations, PlannedReservation{BudgetID: v.ID, BudgetVersion: v.Version, PeriodStart: start, PeriodEnd: end, Currency: v.Currency})
	}
	input := map[string]json.RawMessage{}
	for _, field := range []string{"input", "messages", "instructions", "system", "tools", "text", "response_format"} {
		if raw, ok := body[field]; ok {
			input[field] = raw
		}
	}
	if !plan.StrictInputCountRequired {
		plan.EstimatedInputTokens, err = estimateTokens(encode(input))
		if err != nil {
			return nil, err
		}
	}
	plan.EstimatedOutputTokens = 4096
	for _, field := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if raw, ok := body[field]; ok {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n <= 0 {
				return nil, &accountingError{400, field, "输出 token 上限必须为正整数"}
			}
			plan.EstimatedOutputTokens = n
			break
		}
	}
	price := *src.Price
	price.Cached = "" // Cache hits and creation are not assumed for a soft reservation.
	price.CacheCreation = ""
	if len(price.Units) > 0 {
		units := []PriceUnit{}
		for _, unit := range price.Units {
			if unit.Dimension == "input_token" || unit.Dimension == "output_token" {
				units = append(units, unit)
			} else if unit.Dimension != "cached_input_token" && unit.Dimension != "cache_creation_token" {
				return nil, &accountingError{422, "budget", "当前操作有尚未纳入token软预留的计费维度"}
			}
		}
		price.Units = units
	}
	var amount *string
	if plan.StrictInputCountRequired {
		bound, e := strictBudgetAmount(src.Price, 0, plan.EstimatedOutputTokens)
		if e != nil {
			return nil, e
		}
		amount = &bound
	} else {
		amount = estimate(Usage{Input: &plan.EstimatedInputTokens, Output: &plan.EstimatedOutputTokens}, &price)
	}
	if amount == nil {
		return nil, &accountingError{422, "budget", "当前输入无法生成费用估算"}
	}
	for i := range plan.Reservations {
		plan.Reservations[i].Amount = *amount
	}
	return plan, nil
}

type attemptAllocation struct {
	Reserved              string  `json:"reserved"`
	Actual                *string `json:"actual,omitempty"`
	Partial               *string `json:"known_partial_cost,omitempty"`
	State                 string  `json:"state"`
	Provenance            string  `json:"provenance"`
	ReservationProvenance string  `json:"reservation_provenance,omitempty"`
	InputTokensBound      *int64  `json:"input_tokens_bound,omitempty"`
	OutputTokensBound     *int64  `json:"output_tokens_bound,omitempty"`
}

func budgetTotals(q accountingQuery, bid, start string) (settled, reserved, pending *big.Rat, err error) {
	settled, reserved, pending = new(big.Rat), new(big.Rat), new(big.Rat)
	rows, err := q.Query("SELECT settled_amount,reserved_amount,pending_amount FROM reservations WHERE budget_id=? AND period_start=?", bid, start)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ss, rs, ps string
		if err = rows.Scan(&ss, &rs, &ps); err != nil {
			return nil, nil, nil, err
		}
		for _, p := range []struct {
			raw string
			sum *big.Rat
		}{{ss, settled}, {rs, reserved}, {ps, pending}} {
			n, e := ledgerRat(p.raw)
			if e != nil {
				return nil, nil, nil, e
			}
			p.sum.Add(p.sum, n)
		}
	}
	return settled, reserved, pending, rows.Err()
}
func reserveAccounting(tx *sql.Tx, v Record) error {
	if v.Accounting == nil {
		return nil
	}
	for _, p := range v.Accounting.Reservations {
		start := p.PeriodStart.UTC().Format(time.RFC3339Nano)
		raw := ""
		allocations := map[string]attemptAllocation{}
		err := tx.QueryRow("SELECT allocation_json FROM reservations WHERE request_id=? AND budget_id=? AND period_start=?", v.ID, p.BudgetID, start).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if err = json.Unmarshal([]byte(raw), &allocations); err != nil {
				return err
			}
			if _, exists := allocations[v.AttemptID]; exists {
				continue
			}
		}
		budget, err := readBudget(tx, p.BudgetID)
		if err != nil {
			return err
		}
		if v.Origin != "provider_vad" && (!budget.Enabled || budget.Version != p.BudgetVersion) {
			return &accountingError{409, "budget", "预算已变化，请重新准入"}
		}
		keyScopeID := v.KeyID
		if v.Origin != "provider_vad" && budget.Scope.Kind == "key" {
			if err = tx.QueryRow("SELECT COALESCE(NULLIF(json_extract(data,'$.budget_scope_id'),''),id) FROM client_keys WHERE id=?", v.KeyID).Scan(&keyScopeID); err != nil {
				return err
			}
		}
		if v.Origin != "provider_vad" && (budget.Scope.Kind == "key" && budget.Scope.ID != keyScopeID || budget.Scope.Kind == "route" && budget.Scope.ID != v.RouteID) {
			return &accountingError{409, "budget", "预算作用域与请求不符"}
		}
		if budget.Mode == "strict" && v.Origin != "provider_vad" && (!v.Accounting.strictCountVerified || v.Accounting.StrictInputCountRequired) {
			return &accountingError{422, "budget", strictBudgetReason}
		}
		if p.Currency != budget.Currency {
			return &accountingError{422, "budget.currency", "预留币种不符"}
		}
		settled, reserved, _, err := budgetTotals(tx, p.BudgetID, start)
		if err != nil {
			return err
		}
		amount, err := ledgerRat(p.Amount)
		if err != nil {
			return err
		}
		limit, err := accountingRat(budget.AmountLimit)
		if err != nil {
			return err
		}
		occupied := new(big.Rat).Add(settled, reserved)
		occupied.Add(occupied, amount)
		if occupied.Cmp(limit) > 0 && v.Origin != "provider_vad" {
			return &accountingError{429, "budget", fmt.Sprintf("预算 %s 的本地可用额不足（已结算 + 未释放预留 + 本次预留超过 %s %s）", budget.Name, budget.AmountLimit, budget.Currency)}
		}
		allocations[v.AttemptID] = attemptAllocation{Reserved: p.Amount, State: "pending", Provenance: v.Accounting.EstimateProvenance, ReservationProvenance: v.Accounting.EstimateProvenance}
		if v.Accounting.strictCountVerified {
			allocation := allocations[v.AttemptID]
			input, output := v.Accounting.EstimatedInputTokens, v.Accounting.EstimatedOutputTokens
			allocation.InputTokensBound, allocation.OutputTokensBound = &input, &output
			allocations[v.AttemptID] = allocation
		}

		if raw == "" {
			_, err = tx.Exec("INSERT INTO reservations(request_id,budget_id,period_start,period_end,reserved_amount,allocation_json,settled_amount,pending_amount,currency,budget_version,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", v.ID, p.BudgetID, start, p.PeriodEnd.UTC().Format(time.RFC3339Nano), p.Amount, encode(allocations), "0", "0", p.Currency, p.BudgetVersion, "pending", time.Now().UTC().Format(time.RFC3339Nano))
		} else {
			err = saveAllocations(tx, v.ID, p.BudgetID, start, raw, allocations)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func saveAllocations(tx *sql.Tx, request, budget, start, previous string, allocations map[string]attemptAllocation) error {
	settled, reserved, pending := new(big.Rat), new(big.Rat), new(big.Rat)
	status := "settled"
	for _, a := range allocations {
		switch a.State {
		case "settled":
			if a.Actual == nil {
				return fmt.Errorf("settled allocation lacks actual amount")
			}
			n, e := ledgerRat(*a.Actual)
			if e != nil {
				return e
			}
			settled.Add(settled, n)
		case "pending", "pending_reconciliation":
			n, e := ledgerRat(a.Reserved)
			if e != nil {
				return e
			}
			reserved.Add(reserved, n)
			if a.State == "pending_reconciliation" {
				pending.Add(pending, n)
				status = "pending_reconciliation"
			} else if status != "pending_reconciliation" {
				status = "pending"
			}
		default:
			return fmt.Errorf("invalid allocation state")
		}
	}
	result, err := tx.Exec("UPDATE reservations SET reserved_amount=?,settled_amount=?,pending_amount=?,allocation_json=?,status=?,updated_at=? WHERE request_id=? AND budget_id=? AND period_start=? AND allocation_json=?", accountingMoney(reserved), accountingMoney(settled), accountingMoney(pending), encode(allocations), status, time.Now().UTC().Format(time.RFC3339Nano), request, budget, start, previous)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed != 1 {
		err = fmt.Errorf("accounting allocation changed concurrently")
	}
	return err
}
func settleAccounting(tx *sql.Tx, v Record) error {
	if v.Ended == nil {
		return nil
	}
	rows, err := tx.Query("SELECT budget_id,period_start,currency,allocation_json FROM reservations WHERE request_id=?", v.ID)
	if err != nil {
		return err
	}
	type reservation struct{ budget, start, currency, raw string }
	var items []reservation
	for rows.Next() {
		var item reservation
		if err = rows.Scan(&item.budget, &item.start, &item.currency, &item.raw); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		var allocations map[string]attemptAllocation
		if err = json.Unmarshal([]byte(item.raw), &allocations); err != nil {
			return err
		}
		allocation, exists := allocations[v.AttemptID]
		if !exists || allocation.State != "pending" {
			continue
		}
		allocation.State = "pending_reconciliation"
		allocation.Partial = v.PartialCost
		if v.Submission == "not_sent" || v.ErrorStage == "prepare" {
			zero := "0"
			allocation.Actual = &zero
			allocation.State = "settled"
			allocation.Provenance = "confirmed_not_submitted"
		} else if v.Cost != nil && v.Price != nil && v.Price.Currency == item.currency && v.Completeness == "complete" && v.ObservationStatus != "partial" {
			if _, err = ledgerRat(*v.Cost); err != nil {
				return err
			}
			allocation.Actual = v.Cost
			allocation.State = "settled"
			allocation.Provenance = "observed_usage_price_estimate"
		}
		allocations[v.AttemptID] = allocation
		if err = saveAllocations(tx, v.ID, item.budget, item.start, item.raw, allocations); err != nil {
			return err
		}
	}
	return nil
}
func summarizeBudget(q accountingQuery, v Budget, now time.Time) (BudgetSummary, error) {
	start, end, err := budgetBounds(v.Period, now)
	if err != nil {
		return BudgetSummary{}, err
	}
	settled, reserved, pending, err := budgetTotals(q, v.ID, start.Format(time.RFC3339Nano))
	if err != nil {
		return BudgetSummary{}, err
	}
	limit, err := accountingRat(v.AmountLimit)
	if err != nil {
		return BudgetSummary{}, err
	}
	available := new(big.Rat).Sub(limit, settled)
	available.Sub(available, reserved)
	out := BudgetSummary{Budget: v, PeriodStart: start, PeriodEnd: end, Settled: accountingMoney(settled), Reserved: accountingMoney(reserved), Pending: accountingMoney(pending), Available: accountingMoney(available), Eligibility: "requires_priced_source"}
	if v.Mode == "strict" {
		out.Eligibility = "requires_qualified_operation"
	}
	if available.Sign() <= 0 {
		out.BlockedReason = "本地预算已用尽或已占用超过新上限"
	}
	if !v.Enabled {
		out.BlockedReason = "预算已停用"
	}
	return out, nil
}
func (a *App) budgetsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 2 && len(parts) != 3 {
		fail(w, 404, "预算接口不存在", "")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(parts) == 2 && r.Method == "GET" {
		rows, err := a.Store.DB.Query("SELECT data FROM budgets ORDER BY id")
		if err != nil {
			accountingFailure(w, err)
			return
		}
		var budgets []Budget
		for rows.Next() {
			var raw string
			var v Budget
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &v); err != nil {
				break
			}
			budgets = append(budgets, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			accountingFailure(w, err)
			return
		}
		items := []BudgetSummary{}
		for _, v := range budgets {
			summary, e := summarizeBudget(a.Store.DB, v, time.Now())
			if e != nil {
				accountingFailure(w, e)
				return
			}
			items = append(items, summary)
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil})
		return
	}
	var current Budget
	if len(parts) == 3 {
		var err error
		current, err = readBudget(a.Store.DB, parts[2])
		if err == sql.ErrNoRows {
			fail(w, 404, "预算不存在", "")
			return
		}
		if err != nil {
			accountingFailure(w, err)
			return
		}
		if r.Method == "GET" {
			summary, e := summarizeBudget(a.Store.DB, current, time.Now())
			if e != nil {
				accountingFailure(w, e)
				return
			}
			writeJSON(w, 200, summary)
			return
		}
		if r.Method == "DELETE" {
			var refs int
			err = a.Store.DB.QueryRow("SELECT (SELECT count(*) FROM client_keys WHERE json_extract(data,'$.budget_id')=?)+(SELECT count(*) FROM reservations WHERE budget_id=?)", current.ID, current.ID).Scan(&refs)
			if err != nil {
				accountingFailure(w, err)
				return
			}
			if refs > 0 || a.realtimeBudgetRefs[current.ID] > 0 {
				fail(w, 409, "预算仍被 Key、活动 Realtime 会话或账务记录引用；可以停用，历史账目保留", "")
				return
			}
			if _, err = a.Store.DB.Exec("DELETE FROM budgets WHERE id=?", current.ID); err != nil {
				accountingFailure(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"id": current.ID, "deleted": true})
			return
		}
	}
	if len(parts) == 2 && r.Method != "POST" || len(parts) == 3 && r.Method != "PATCH" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Name     *string       `json:"name"`
		Scope    *BudgetScope  `json:"scope"`
		Currency *string       `json:"currency"`
		Amount   *string       `json:"amount_limit"`
		Mode     *string       `json:"mode"`
		Period   *BudgetPeriod `json:"period"`
		Enabled  *bool         `json:"enabled"`
		Version  int           `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	create := len(parts) == 2
	v := current
	if create {
		v = Budget{ID: id("budget"), Enabled: true, Version: 1, CreatedAt: time.Now().UTC()}
	} else {
		if in.Version != v.Version {
			fail(w, 409, "预算版本已变化", "version")
			return
		}
		if in.Scope != nil && *in.Scope != v.Scope || in.Currency != nil && *in.Currency != v.Currency || in.Mode != nil && *in.Mode != v.Mode || in.Period != nil && encode(*in.Period) != encode(v.Period) {
			fail(w, 409, "作用域、币种、模式和周期在创建时冻结；请创建新的预算", "")
			return
		}
		_, end, e := budgetBounds(v.Period, time.Now())
		if e != nil {
			accountingFailure(w, e)
			return
		}
		if v.Period.Kind == "fixed" && !time.Now().Before(end) {
			fail(w, 409, "已关闭预算周期只读", "")
			return
		}
		v.Version++
	}
	if in.Name != nil {
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Scope != nil {
		v.Scope = *in.Scope
		if create && v.Scope.Kind == "key" {
			key, err := a.Store.keyByID(v.Scope.ID)
			if err == sql.ErrNoRows {
				fail(w, 409, "预算作用域不存在", "scope")
				return
			}
			if err != nil {
				accountingFailure(w, err)
				return
			}
			v.Scope.ID = key.budgetScopeID()
		}
	}
	if in.Currency != nil {
		v.Currency = *in.Currency
	}
	if in.Amount != nil {
		v.AmountLimit = *in.Amount
	}
	if in.Mode != nil {
		v.Mode = *in.Mode
	}
	if in.Period != nil {
		v.Period = *in.Period
	}
	if in.Enabled != nil {
		v.Enabled = *in.Enabled
	}
	if err := validateBudget(v); err != nil {
		accountingFailure(w, err)
		return
	}
	tx, err := a.Store.DB.Begin()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	defer tx.Rollback()
	if err = validateBudgetScope(tx, v); err == nil {
		err = saveBudget(tx, v)
	}
	if err == nil {
		_, err = tx.Exec("INSERT INTO accounting_audit(id,request_id,created_at,data) VALUES(?,NULL,?,?)", id("audit"), time.Now().UTC().Format(time.RFC3339Nano), encode(map[string]any{"kind": "budget_change", "budget_id": v.ID, "before": current, "after": v}))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		accountingFailure(w, err)
		return
	}
	summary, err := summarizeBudget(a.Store.DB, v, time.Now())
	if err != nil {
		accountingFailure(w, err)
		return
	}
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, summary)
}

type ReconcileAttemptCost struct {
	AttemptID   string `json:"attempt_id"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
	Reason      string `json:"reason"`
	EvidenceRef string `json:"evidence_ref"`
}

// reconcileAccountingAPI must be reached through the authenticated admin handler.
func (a *App) reconcileAccountingAPI(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Version      int                    `json:"version"`
		AttemptCosts []ReconcileAttemptCost `json:"attempt_costs"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || len(in.AttemptCosts) == 0 {
		fail(w, 400, "提供账务版本与至少一个待核对 attempt", "attempt_costs")
		return
	}
	seen := map[string]bool{}
	for _, c := range in.AttemptCosts {
		if c.AttemptID == "" || seen[c.AttemptID] || !currencyPattern.MatchString(c.Currency) || strings.TrimSpace(c.Reason) == "" || strings.TrimSpace(c.EvidenceRef) == "" || len(c.Reason) > 1000 || len(c.EvidenceRef) > 2000 {
			fail(w, 400, "每条核对需要唯一 attempt_id、币种、reason 与 evidence_ref", "attempt_costs")
			return
		}
		if _, err := accountingRat(c.Amount); err != nil {
			fail(w, 400, err.Error(), "amount")
			return
		}
		seen[c.AttemptID] = true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tx, err := a.Store.DB.Begin()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	defer tx.Rollback()
	var revision int
	if err = tx.QueryRow("SELECT count(*)+1 FROM accounting_audit WHERE request_id=?", requestID).Scan(&revision); err != nil {
		accountingFailure(w, err)
		return
	}
	if revision != in.Version {
		fail(w, 409, "账务版本已变化", "version")
		return
	}
	rows, err := tx.Query("SELECT budget_id,period_start,currency,allocation_json FROM reservations WHERE request_id=?", requestID)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	type row struct{ budget, start, currency, raw string }
	var items []row
	for rows.Next() {
		var item row
		if err = rows.Scan(&item.budget, &item.start, &item.currency, &item.raw); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	matched := map[string]bool{}
	before, after := map[string]any{}, map[string]any{}
	for _, item := range items {
		var allocations map[string]attemptAllocation
		if err = json.Unmarshal([]byte(item.raw), &allocations); err != nil {
			accountingFailure(w, err)
			return
		}
		changed := false
		for _, c := range in.AttemptCosts {
			allocation, ok := allocations[c.AttemptID]
			if !ok {
				continue
			}
			if allocation.State != "pending_reconciliation" || item.currency != c.Currency {
				fail(w, 409, "仅可核对待确认费用，且币种必须匹配", "attempt_costs")
				return
			}
			before[item.budget+":"+item.start+":"+c.AttemptID] = allocation
			allocation.Actual = &c.Amount
			allocation.State = "settled"
			allocation.Provenance = "manual_reported"
			allocations[c.AttemptID] = allocation
			after[item.budget+":"+item.start+":"+c.AttemptID] = allocation
			changed = true
			matched[c.AttemptID] = true
		}
		if changed {
			if err = saveAllocations(tx, requestID, item.budget, item.start, item.raw, allocations); err != nil {
				accountingFailure(w, err)
				return
			}
		}
	}
	if len(matched) != len(in.AttemptCosts) {
		fail(w, 409, "attempt 不属于此请求的待确认账务", "attempt_costs")
		return
	}
	audit := map[string]any{"kind": "manual_reconciliation", "version": revision + 1, "attempt_costs": in.AttemptCosts, "before": before, "after": after, "provenance": "manual_reported"}
	if _, err = tx.Exec("INSERT INTO accounting_audit(id,request_id,created_at,data) VALUES(?,?,?,?)", id("audit"), requestID, time.Now().UTC().Format(time.RFC3339Nano), encode(audit)); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		accountingFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"request_id": requestID, "accounting_version": revision + 1, "provenance": "manual_reported"})
}

// accountingDetails exposes ledger revisions separately from immutable observations.
func (s *Store) accountingDetails(requestID string) (map[string]any, error) {
	rows, err := s.DB.Query("SELECT budget_id,period_start,period_end,reserved_amount,settled_amount,pending_amount,currency,status,allocation_json FROM reservations WHERE request_id=? ORDER BY period_start,budget_id", requestID)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for rows.Next() {
		var budget, start, end, reserved, settled, pending, currency, state, allocation string
		if err = rows.Scan(&budget, &start, &end, &reserved, &settled, &pending, &currency, &state, &allocation); err != nil {
			break
		}
		var allocations map[string]attemptAllocation
		if err = json.Unmarshal([]byte(allocation), &allocations); err != nil {
			break
		}
		items = append(items, map[string]any{"budget_id": budget, "period_start": start, "period_end": end, "reserved": reserved, "settled": settled, "pending": pending, "currency": currency, "status": state, "allocations": allocations})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	var version int
	if err = s.DB.QueryRow("SELECT count(*)+1 FROM accounting_audit WHERE request_id=?", requestID).Scan(&version); err != nil {
		return nil, err
	}
	return map[string]any{"accounting_version": version, "reservations": items}, nil
}
