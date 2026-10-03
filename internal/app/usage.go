package app

import (
	"database/sql"
	"encoding/json"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var decimalPattern = regexp.MustCompile(`^\d+(\.\d{1,18})?$`)
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func validPrice(p Price) bool {
	if len(p.Units) > 0 {
		return currencyPattern.MatchString(p.Currency) && validPriceUnits(p.Units)
	}
	return currencyPattern.MatchString(p.Currency) && decimalPattern.MatchString(p.Input) && decimalPattern.MatchString(p.Output) && (p.Cached == "" || decimalPattern.MatchString(p.Cached)) && (p.CacheCreation == "" || decimalPattern.MatchString(p.CacheCreation))
}
func mergeUsage(u *Usage, b []byte) {
	var v struct {
		Input        *int64 `json:"input_tokens"`
		Output       *int64 `json:"output_tokens"`
		InputDetails struct {
			Cached        *int64 `json:"cached_tokens"`
			CacheCreation *int64 `json:"cache_creation_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Reasoning *int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	for _, pair := range []struct {
		to   **int64
		from *int64
	}{{&u.Input, v.Input}, {&u.Output, v.Output}, {&u.Cached, v.InputDetails.Cached}, {&u.CacheCreation, v.InputDetails.CacheCreation}, {&u.Reasoning, v.OutputDetails.Reasoning}} {
		if pair.from != nil && *pair.from >= 0 {
			*pair.to = pair.from
		}
	}
}
func usageCompleteness(u Usage) string {
	if u.Input != nil && u.Output != nil {
		return "complete"
	}
	if u.Input != nil || u.Output != nil || u.Cached != nil || u.CacheCreation != nil || u.Reasoning != nil {
		return "partial"
	}
	return "unknown"
}
func estimate(u Usage, p *Price) *string {
	if p != nil && len(p.Units) > 0 && validPrice(*p) {
		return estimateUnits(u, p, false)
	}
	if p == nil || !validPrice(*p) || u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 {
		return nil
	}
	input := *u.Input
	cached, created := int64(0), int64(0)
	for _, dimension := range []struct {
		count    *int64
		rate     string
		dest     *int64
		creation bool
	}{{u.Cached, p.Cached, &cached, false}, {u.CacheCreation, p.CacheCreation, &created, true}} {
		if dimension.rate != "" && dimension.count == nil {
			return nil
		}
		if dimension.count != nil {

			if *dimension.count < 0 || *dimension.count > input {
				return nil
			}
			*dimension.dest = *dimension.count
			input -= *dimension.count
			if dimension.rate == "" && *dimension.count > 0 {
				return nil
			}
		}
	}
	total := new(big.Rat)
	for _, v := range []struct {
		tokens int64
		rate   string
	}{{input, p.Input}, {*u.Output, p.Output}, {cached, p.Cached}, {created, p.CacheCreation}} {
		if v.rate == "" {
			continue
		}
		rate, _ := new(big.Rat).SetString(v.rate)
		total.Add(total, rate.Mul(rate, big.NewRat(v.tokens, 1000000)))
	}
	value := accountingMoney(total)
	return &value
}
func estimatePartial(u Usage, p *Price) *string {
	if p != nil && len(p.Units) > 0 && validPrice(*p) {
		return estimateUnits(u, p, true)
	}
	if p == nil || !validPrice(*p) {
		return nil
	}
	total := new(big.Rat)
	known := false
	add := func(n int64, price string) {
		if n < 0 || price == "" {
			return
		}
		rate, _ := new(big.Rat).SetString(price)
		total.Add(total, rate.Mul(rate, big.NewRat(n, 1000000)))
		known = true
	}
	if u.Output != nil {
		add(*u.Output, p.Output)
	}
	if u.Input != nil && *u.Input >= 0 {
		input := *u.Input
		available := true
		for _, dimension := range []struct {
			count    *int64
			rate     string
			creation bool
		}{{u.Cached, p.Cached, false}, {u.CacheCreation, p.CacheCreation, true}} {

			if dimension.count == nil {
				if dimension.rate != "" {
					available = false
				}
				continue
			}
			if *dimension.count < 0 || *dimension.count > input {
				available = false
				continue
			}
			input -= *dimension.count
			add(*dimension.count, dimension.rate)
		}
		if available {
			add(input, p.Input)
		}
	}
	if !known {
		return nil
	}
	value := accountingMoney(total)
	return &value
}

// usageWhere validates report bounds before constructing the shared request filter.
// Padding fractional UTC seconds keeps the interval exact at RFC3339Nano boundaries.
func usageWhere(q map[string][]string) (string, []any, bool, error) {
	where := "1=1"
	args := []any{}
	for _, field := range []struct{ param, expr string }{
		{"source_id", "source_id"}, {"status", "status"}, {"client_key_id", "json_extract(data,'$.client_key_id')"},
		{"client_name", "json_extract(data,'$.client_name')"}, {"account_id", "json_extract(data,'$.account_id')"},
		{"route_id", "json_extract(data,'$.route_id')"}, {"model", "json_extract(data,'$.requested_model')"},
		{"protocol", "json_extract(data,'$.protocol')"}, {"origin", "json_extract(data,'$.origin')"},
	} {
		if values := q[field.param]; len(values) > 0 && values[0] != "" {
			where += " AND " + field.expr + "=?"
			args = append(args, values[0])
		}
	}
	var from, to *time.Time
	for _, field := range []struct {
		name, op string
		dest     **time.Time
	}{{"from", ">=", &from}, {"to", "<", &to}} {
		if values := q[field.name]; len(values) > 0 && values[0] != "" {
			parsed, err := time.Parse(time.RFC3339Nano, values[0])
			if err != nil {
				return "", nil, true, &accountingError{400, field.name, "时间范围需要 RFC3339 时间"}
			}
			*field.dest = &parsed
			timestamp := "(substr(started,1,19)||'.'||substr(CASE WHEN substr(started,20,1)='.' THEN substr(started,21,length(started)-21) ELSE '' END||'000000000',1,9)||'Z')"
			where += " AND " + timestamp + field.op + "?"
			args = append(args, parsed.UTC().Format("2006-01-02T15:04:05.000000000Z"))
		}
	}
	if from != nil && to != nil && !from.Before(*to) {
		return "", nil, true, &accountingError{400, "to", "时间范围必须 from < to"}
	}
	includesTests := true
	if values := q["include_admin_tests"]; len(values) > 0 {
		switch values[0] {
		case "true":
		case "false":
			includesTests = false
			where += " AND json_extract(data,'$.origin')!='admin_test'"
		default:
			return "", nil, true, &accountingError{400, "include_admin_tests", "include_admin_tests 必须为 true 或 false"}
		}
	}
	return where, args, includesTests, nil
}
func (a *App) usageAPI(w http.ResponseWriter, r *http.Request) {
	where, args, includesTests, err := usageWhere(r.URL.Query())
	if err != nil {
		accountingFailure(w, err)
		return
	}
	query := `SELECT status,count(*),coalesce(sum(json_extract(data,'$.usage.input_tokens')),0),coalesce(sum(json_extract(data,'$.usage.output_tokens')),0),sum(json_extract(data,'$.usage.input_tokens') IS NOT NULL),sum(json_extract(data,'$.usage.output_tokens') IS NOT NULL),sum(json_extract(data,'$.usage_completeness')='unknown'),sum(json_extract(data,'$.usage_completeness')='partial'),sum(json_extract(data,'$.estimated_cost') IS NULL OR json_extract(data,'$.price_snapshot.currency') IS NULL),sum(json_extract(data,'$.origin')='admin_test'),coalesce(sum(json_extract(data,'$.duration_ms')),0) FROM requests WHERE ` + where + ` GROUP BY status`
	rows, err := a.Store.DB.Query(query, args...)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	states := map[string]int64{}
	var requests, input, output, knownInput, knownOutput, unknown, partial, unknownCost, tests, duration int64
	for rows.Next() {
		var state string
		var count, i, o, ki, ko, u, p, uc, t, d int64
		if err = rows.Scan(&state, &count, &i, &o, &ki, &ko, &u, &p, &uc, &t, &d); err != nil {
			break
		}
		states[state] = count
		requests += count
		input += i
		output += o
		knownInput += ki
		knownOutput += ko
		unknown += u
		partial += p
		unknownCost += uc
		tests += t
		duration += d
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	var attempts int64
	err = a.Store.DB.QueryRow("SELECT count(*) FROM attempts WHERE request_id IN (SELECT id FROM requests WHERE "+where+")", args...).Scan(&attempts)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	// Currency and decimal amount are the only fee columns loaded. Identical decimal
	// values are counted in SQL, then combined exactly with big.Rat in a streaming scan.
	rows, err = a.Store.DB.Query(`SELECT json_extract(data,'$.price_snapshot.currency'),json_extract(data,'$.estimated_cost'),json_extract(data,'$.partial_estimated_cost'),count(*) FROM requests WHERE `+where+` GROUP BY json_extract(data,'$.price_snapshot.currency'),json_extract(data,'$.estimated_cost'),json_extract(data,'$.partial_estimated_cost')`, args...)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	costs, partialCosts := map[string]*big.Rat{}, map[string]*big.Rat{}
	for rows.Next() {
		var currency, cost, partialCost sql.NullString
		var count int64
		if err = rows.Scan(&currency, &cost, &partialCost, &count); err != nil {
			break
		}
		if !currency.Valid {
			continue
		}
		value, target := cost, costs
		if !cost.Valid {
			value, target = partialCost, partialCosts
		}
		if !value.Valid {
			continue
		}
		amount, parseErr := ledgerRat(value.String)
		if parseErr != nil {
			err = parseErr
			break
		}
		amount.Mul(amount, big.NewRat(count, 1))
		if target[currency.String] == nil {
			target[currency.String] = new(big.Rat)
		}
		target[currency.String].Add(target[currency.String], amount)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	currencies, partialCurrencies := map[string]string{}, map[string]string{}
	for c, n := range costs {
		currencies[c] = accountingMoney(n)
	}
	for c, n := range partialCosts {
		partialCurrencies[c] = accountingMoney(n)
	}
	var totalDuration any
	if requests > 0 {
		totalDuration = float64(duration) / float64(requests)
	}
	var ttft sql.NullFloat64
	var ttftKnown int64
	// Request-level TTFT includes queueing and retries, and ends at semantic
	// content. Metadata/keepalive-only requests have no sample.
	err = a.Store.DB.QueryRow(`SELECT avg((julianday(json_extract(data,'$.first_content_at'))-julianday(started))*86400000),count(json_extract(data,'$.first_content_at')) FROM requests WHERE `+where, args...).Scan(&ttft, &ttftKnown)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	var ttftMS any
	if ttft.Valid {
		ttftMS = ttft.Float64
	}
	// Reservations include unknown costs that remain occupied after restart. These
	// sums are exact decimal arithmetic and are distinct from provider quota.
	reservations := map[string]map[string]string{}
	rows, err = a.Store.DB.Query(`SELECT budget_id,currency,reserved_amount,pending_amount FROM reservations WHERE request_id IN (SELECT id FROM requests WHERE `+where+`)`, args...)
	if err != nil {
		accountingFailure(w, err)
		return
	}
	reservedTotals, pendingTotals := map[string]*big.Rat{}, map[string]*big.Rat{}
	for rows.Next() {
		var budgetID, currency, reserved, pending string
		if err = rows.Scan(&budgetID, &currency, &reserved, &pending); err != nil {
			break
		}
		reservations[budgetID] = map[string]string{"currency": currency}
		for _, pair := range []struct {
			raw    string
			target map[string]*big.Rat
		}{{reserved, reservedTotals}, {pending, pendingTotals}} {
			n, e := ledgerRat(pair.raw)
			if e != nil {
				err = e
				break
			}
			if pair.target[budgetID] == nil {
				pair.target[budgetID] = new(big.Rat)
			}
			pair.target[budgetID].Add(pair.target[budgetID], n)
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	for budgetID, n := range reservedTotals {
		reservations[budgetID]["reserved"] = accountingMoney(n)
		reservations[budgetID]["pending"] = accountingMoney(pendingTotals[budgetID])
	}
	notice := "已知 token 是部分观测总量；未知不等于零。估算费用不是订阅账单。"
	if !includesTests {
		notice += " 本次已排除管理测试。"
	}
	writeJSON(w, 200, map[string]any{"requests": requests, "attempts": attempts, "states": states, "known_input_tokens": input, "known_output_tokens": output, "known_input_requests": knownInput, "known_output_requests": knownOutput, "usage_unknown_requests": unknown, "usage_partial_requests": partial, "cost_unknown_requests": unknownCost, "estimated_cost_by_currency": currencies, "partial_estimated_cost_by_currency": partialCurrencies, "admin_test_requests": tests, "includes_admin_tests": includesTests, "mean_duration_ms": totalDuration, "mean_ttft_ms": ttftMS, "ttft_known_requests": ttftKnown, "budget_reservations_by_budget": reservations, "notice": strings.TrimSpace(notice)})
}
