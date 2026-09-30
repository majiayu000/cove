package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Qualification is pure: route previews never call the provider or reserve money.
// This contract covers default-tier OpenAI Responses text and client functions.
func strictResponsesBudgetInput(src Source, body map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	reject := func() (map[string]json.RawMessage, error) {
		return nil, &accountingError{422, "budget", strictBudgetReason}
	}
	if src.Kind != "api_key" || src.Provider != "openai" || src.NativeProtocol != "responses" || strings.TrimRight(src.BaseURL, "/") != "https://api.openai.com/v1" {
		return reject()
	}
	var tier string
	var cap int64
	if json.Unmarshal(body["service_tier"], &tier) != nil || tier != "default" || json.Unmarshal(body["max_output_tokens"], &cap) != nil || cap <= 0 {
		return reject()
	}
	for _, field := range []string{"previous_response_id", "conversation", "context_management"} {
		if _, ok := body[field]; ok {
			return reject()
		}
	}
	if v, ok := body["background"]; ok && string(v) != "false" {
		return reject()
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if v, ok := body["tools"]; ok {
		if json.Unmarshal(v, &tools) != nil {
			return reject()
		}
		for _, tool := range tools {
			if tool.Type != "function" {
				return reject()
			}
		}
	}
	var text string
	if json.Unmarshal(body["input"], &text) != nil {
		var items []map[string]json.RawMessage
		if json.Unmarshal(body["input"], &items) != nil {
			return reject()
		}
		for _, item := range items {
			var kind string
			_ = json.Unmarshal(item["type"], &kind)
			switch kind {
			case "", "message":
				if json.Unmarshal(item["content"], &text) != nil {
					var parts []struct {
						Type string  `json:"type"`
						Text *string `json:"text"`
					}
					if json.Unmarshal(item["content"], &parts) != nil {
						return reject()
					}
					for _, part := range parts {
						if (part.Type != "input_text" && part.Type != "output_text") || part.Text == nil {
							return reject()
						}
					}
				}
			case "function_call":
				if json.Unmarshal(item["arguments"], &text) != nil {
					return reject()
				}
			case "function_call_output":
				if json.Unmarshal(item["output"], &text) != nil {
					return reject()
				}
			default:
				return reject()
			}
		}
	}
	if _, _, err := strictBudgetRates(src.Price); err != nil {
		return nil, err
	}
	count := map[string]json.RawMessage{}
	for _, field := range []string{"model", "input", "instructions", "parallel_tool_calls", "personality", "reasoning", "text", "tool_choice", "tools", "truncation"} {
		if v, ok := body[field]; ok {
			count[field] = v
		}
	}
	return count, nil
}

func strictBudgetRates(price *Price) (input, output *big.Rat, err error) {
	failure := func() (*big.Rat, *big.Rat, error) {
		return nil, nil, &accountingError{422, "budget", strictBudgetReason}
	}
	if price == nil || !validPrice(*price) || price.CacheCreation != "" {
		return failure()
	}
	rates := map[string]*big.Rat{}
	if len(price.Units) > 0 {
		for _, unit := range price.Units {
			if unit.Dimension != "input_token" && unit.Dimension != "output_token" && unit.Dimension != "cached_input_token" {
				return failure()
			}
			amount, _ := new(big.Rat).SetString(unit.Amount)
			per, _ := new(big.Rat).SetString(unit.Per)
			rates[unit.Dimension] = new(big.Rat).Quo(amount, per)
		}
	} else {
		for _, v := range []struct{ dimension, value string }{{"input_token", price.Input}, {"output_token", price.Output}, {"cached_input_token", price.Cached}} {
			if v.value != "" {
				rate, e := accountingRat(v.value)
				if e != nil {
					return nil, nil, e
				}
				rates[v.dimension] = new(big.Rat).Quo(rate, big.NewRat(1000000, 1))
			}
		}
	}
	input, output = rates["input_token"], rates["output_token"]
	if input == nil || output == nil {
		return failure()
	}
	if cached := rates["cached_input_token"]; cached != nil && cached.Cmp(input) > 0 {
		input = cached
	}
	return input, output, nil
}

func strictBudgetAmount(price *Price, input, output int64) (string, error) {
	in, out, err := strictBudgetRates(price)
	if err != nil {
		return "", err
	}
	value := new(big.Rat).Add(new(big.Rat).Mul(in, big.NewRat(input, 1)), new(big.Rat).Mul(out, big.NewRat(output, 1)))
	// Round UP at the ledger's eighteen-decimal precision. Rounding to nearest
	// would turn tiny positive prices into zero or under-reserve repeating ratios.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	scaled := new(big.Rat).Mul(value, new(big.Rat).SetInt(scale))
	units, remainder := new(big.Int), new(big.Int)
	units.QuoRem(scaled.Num(), scaled.Denom(), remainder)
	if remainder.Sign() > 0 {
		units.Add(units, big.NewInt(1))
	}
	return accountingMoney(new(big.Rat).SetFrac(units, scale)), nil
}

// Called and returned with a.mu held. The counting request does not hold the
// admission lock or consume a generation/account slot. Revalidate its lease
// before converting a preliminary plan into a reservable upper bound.
func (a *App) completeStrictAccounting(ctx context.Context, key ClientKey, src Source, body map[string]json.RawMessage, secret, requestID string, admin bool, plan *AccountingPlan) error {
	if plan == nil || !plan.StrictInputCountRequired {
		return nil
	}
	count := plan.strictInput
	if count == nil {
		return &accountingError{422, "budget", strictBudgetReason}
	}
	var model string
	_ = json.Unmarshal(body["model"], &model)
	before, err := a.candidateFor(src, model)
	if err != nil {
		return err
	}
	routeVersion := 0
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e != nil {
			return e
		}
		routeVersion = route.Version
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeEndpoint(src.BaseURL, "/responses/input_tokens"), bytes.NewReader([]byte(encode(count))))
	if err != nil {
		return err
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	var tokens *int64
	a.mu.Unlock()
	func() {
		resp, e := a.doUpstream(req, src)
		if e != nil {
			err = e
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			err = errors.New("input count rejected")
			return
		}
		raw, e := readLimited(resp.Body, 4096)
		if e != nil {
			err = e
			return
		}
		var v struct {
			Object string `json:"object"`
			Input  *int64 `json:"input_tokens"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Object != "response.input_tokens" || v.Input == nil || *v.Input < 0 {
			err = errors.New("input count invalid")
			return
		}
		tokens = v.Input
	}()
	a.mu.Lock()
	if ctx.Err() != nil {
		return &accountingError{504, "budget", "输入计数已取消或超过请求总期限；未派发生成"}
	}
	if err != nil {
		return &accountingError{502, "budget", "提供方输入计数失败；未派发生成"}
	}
	if a.stopping || a.storageFailed.Load() {
		return &accountingError{503, "budget", "服务正在关闭或存储异常；未派发生成"}
	}
	if !admin {
		var currentKey ClientKey
		var raw string
		err = a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", key.ID).Scan(&raw)
		if err != nil || json.Unmarshal([]byte(raw), &currentKey) != nil || !keyValid(currentKey, time.Now()) || currentKey.Version != key.Version {
			return &accountingError{409, "budget", "输入计数期间Key已变化，请重新发送请求"}
		}
	}
	current, err := a.Store.source(src.ID)
	if err != nil || !current.Enabled || current.Deleted || current.Version != src.Version || current.Generation != src.Generation || current.AccountGeneration != src.AccountGeneration || current.CredentialRef != src.CredentialRef || !current.Configured || current.AuthStatus == "rejected" || current.AuthStatus == "needs_reauth" || current.AuthStatus == "logged_out" {
		return &accountingError{409, "budget", "输入计数期间来源或账号已变化，请重新发送请求"}
	}
	after, err := a.candidateFor(current, model)
	if err != nil || after.Model.ID != before.Model.ID || after.Model.Version != before.Model.Version || encode(after.Source.Price) != encode(src.Price) {
		return &accountingError{409, "budget", "输入计数期间模型或价格已变化，请重新发送请求"}
	}
	ownAccount := 0
	if a.runningKeys[requestID] == key.ID {
		var accountID string
		if err = a.Store.DB.QueryRow("SELECT json_extract(data,'$.account_id') FROM requests WHERE id=?", requestID).Scan(&accountID); err != nil {
			return err
		}
		if accountID == current.AccountID {
			ownAccount = 1
		}
	}
	if current.MaxConcurrent != nil && a.accountActive[current.AccountID]-ownAccount >= *current.MaxConcurrent {
		return &accountingError{429, "budget", "输入计数后账号并发已满；未派发生成"}
	}
	if blocked, _ := quotaDispatchBlocked(current, model, "generate", time.Now()); blocked {
		return &accountingError{429, "budget", "输入计数后来源额度不可用；未派发生成"}
	}
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e != nil || !route.Enabled || route.Version != routeVersion {
			return &accountingError{409, "budget", "输入计数期间路由已变化，请重新发送请求"}
		}
		ownRoute := 0
		if a.runningKeys[requestID] == key.ID {
			ownRoute = 1
		}
		if route.MaxConcurrent != nil && a.routeActive[route.ID]-ownRoute >= *route.MaxConcurrent {
			return &accountingError{429, "budget", "输入计数后路由并发已满；未派发生成"}
		}
	}
	fresh, err := a.prepareAccounting(key, after.Source, body)
	if err != nil {
		return err
	}
	if fresh == nil || !fresh.StrictInputCountRequired || len(fresh.Reservations) != len(plan.Reservations) {
		return &accountingError{409, "budget", "输入计数期间预算已变化，请重新发送请求"}
	}
	versions := map[string]int{}
	for _, r := range plan.Reservations {
		versions[r.BudgetID] = r.BudgetVersion
	}
	for _, r := range fresh.Reservations {
		if versions[r.BudgetID] != r.BudgetVersion {
			return &accountingError{409, "budget", "输入计数期间预算已变化，请重新发送请求"}
		}
	}
	if *tokens > (1<<63-1)-fresh.EstimatedOutputTokens {
		return &accountingError{422, "budget", "输入计数与输出上限合计超出可计量范围；未派发生成"}
	}
	amount, err := strictBudgetAmount(after.Source.Price, *tokens, fresh.EstimatedOutputTokens)
	if err != nil {
		return err
	}
	fresh.EstimatedInputTokens = *tokens
	fresh.StrictInputCountRequired = false
	fresh.strictCountVerified = true
	fresh.EstimateProvenance = "strict local upper bound; OpenAI responses/input_tokens; max_output_tokens; configured default-tier token prices"
	for i := range fresh.Reservations {
		fresh.Reservations[i].Amount = amount
	}
	*plan = *fresh
	return nil
}
