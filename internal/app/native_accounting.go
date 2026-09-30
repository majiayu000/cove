package app

import (
	"encoding/json"
	"math/big"
	"strconv"
	"time"
)

// Media prices retain their own units. These estimates are soft reservations;
// neither requested image counts nor a local tokenizer prove a billing bound.
func (a *App) prepareNativeAccounting(key ClientKey, src Source, input nativeInput, op nativeOperationSpec) (*AccountingPlan, error) {
	budgets, err := matchingBudgets(a.Store.DB, key.budgetScopeID(), key.RouteID)
	if err != nil {
		return nil, err
	}
	if len(budgets) == 0 {
		return nil, nil
	}
	if src.Price == nil || len(src.Price.Units) == 0 || !validPrice(*src.Price) {
		return nil, &accountingError{422, "budget", "此原生操作需要有明确计费维度的模型价格"}
	}
	counts := map[string]string{"request": "1"}
	provenance := "soft native operation estimate"
	if op.Name == "embeddings" {
		raw := input.Body["input"]
		n, err := estimateTokens(string(raw))
		if err != nil {
			return nil, err
		}
		counts["input_token"] = strconv.FormatInt(n, 10)
		counts["output_token"] = "0"
		provenance += "; o200k_base input estimate"
	}
	if op.Name == "images.generate" || op.Name == "images.edit" {
		n := int64(1)
		if raw, ok := input.Body["n"]; ok && json.Unmarshal(raw, &n) != nil {
			return nil, &accountingError{400, "n", "n 必须是正整数"}
		}
		if n <= 0 {
			return nil, &accountingError{400, "n", "n 必须是正整数"}
		}
		counts["image"] = strconv.FormatInt(n, 10)
		provenance += "; requested image count"
	}
	amount := nativeDimensionCost(counts, src.Price, false)
	if amount == nil {
		return nil, &accountingError{422, "budget", "此原生操作存在未知的费用预留维度，请配置适用价格后重试"}
	}
	now := time.Now().UTC()
	plan := &AccountingPlan{EstimateProvenance: provenance}
	for _, v := range budgets {
		if v.Mode == "strict" {
			return nil, &accountingError{422, "budget", strictBudgetReason}
		}
		if v.Currency != src.Price.Currency {
			return nil, &accountingError{422, "budget.currency", "原生操作价格与预算币种不同"}
		}
		start, end, err := budgetBounds(v.Period, now)
		if err != nil {
			return nil, err
		}
		if now.Before(start) || !now.Before(end) {
			return nil, &accountingError{429, "budget.period", "预算周期尚未开始或已经结束"}
		}
		plan.Reservations = append(plan.Reservations, PlannedReservation{BudgetID: v.ID, BudgetVersion: v.Version, PeriodStart: start, PeriodEnd: end, Amount: *amount, Currency: v.Currency})
	}
	return plan, nil
}

func nativeDimensionCost(counts map[string]string, price *Price, partial bool) *string {
	if price == nil || !validPrice(*price) || len(price.Units) == 0 {
		return nil
	}
	total := new(big.Rat)
	known := false
	for _, unit := range price.Units {
		count, ok := counts[unit.Dimension]
		if !ok {
			if !partial {
				return nil
			}
			continue
		}
		n, valid := new(big.Rat).SetString(count)
		if !valid || n.Sign() < 0 {
			return nil
		}
		amount, _ := new(big.Rat).SetString(unit.Amount)
		per, _ := new(big.Rat).SetString(unit.Per)
		total.Add(total, new(big.Rat).Mul(n, new(big.Rat).Quo(amount, per)))
		known = true
	}
	if !known {
		return nil
	}
	value := accountingMoney(total)
	return &value
}

func finalizeNativeCost(record *Record, op nativeOperationSpec) {
	if record.Submission == "not_sent" {
		return
	}
	if op.Name == "compact" {
		record.Cost = estimate(record.Usage, record.Price)
		if record.Cost == nil {
			record.PartialCost = estimatePartial(record.Usage, record.Price)
		}
		return
	}
	counts := record.UsageDimensions
	if counts == nil {
		counts = map[string]string{}
	}
	if record.Status == "succeeded" {
		counts["request"] = "1"
	}
	for _, pair := range []struct {
		name string
		n    *int64
	}{{"input_token", record.Usage.Input}, {"output_token", record.Usage.Output}, {"cached_input_token", record.Usage.Cached}, {"cache_creation_token", record.Usage.CacheCreation}} {
		if pair.n != nil && *pair.n >= 0 {
			counts[pair.name] = strconv.FormatInt(*pair.n, 10)
		}
	}
	if record.Usage.Input != nil {
		n := *record.Usage.Input
		for _, cached := range []*int64{record.Usage.Cached, record.Usage.CacheCreation} {
			if cached != nil {
				n -= *cached
			}
		}
		if n >= 0 {
			counts["input_token"] = strconv.FormatInt(n, 10)
		} else {
			delete(counts, "input_token")
		}
	}
	record.UsageDimensions = counts
	record.Cost = nativeDimensionCost(counts, record.Price, false)
	if record.Cost == nil {
		record.PartialCost = nativeDimensionCost(counts, record.Price, true)
	}
}
