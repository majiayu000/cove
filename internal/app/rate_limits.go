package app

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

type tokenReservation struct {
	At     time.Time
	Tokens int64
}

func reservationEstimate(body map[string]json.RawMessage, raw []byte) (int64, string, error) {
	n, err := estimateTokens(string(raw))
	if err != nil {
		return 0, "", err
	}
	output := int64(4096)
	kind := "estimated;o200k_base;soft_output_4096"
	for _, field := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if value, ok := body[field]; ok && string(value) != "null" {
			if json.Unmarshal(value, &output) != nil || output < 1 || output > math.MaxInt64-n {
				return 0, "", errors.New("输出上限必须是正整数")
			}
			kind = "estimated;o200k_base;explicit_output_limit"
			break
		}
	}
	return n + output, kind, nil
}

// All window updates share the same admission mutex. Windows reset on restart.
func (a *App) checkTPM(k ClientKey, amount int64, now time.Time) (bool, int) {
	if k.Limits.TPM == nil {
		return true, 0
	}
	window := a.tpmWindows[k.ID]
	total := int64(0)
	for id, item := range window {
		if !item.At.Add(time.Minute).After(now) {
			delete(window, id)
			continue
		}
		total += item.Tokens
	}
	if amount <= int64(*k.Limits.TPM)-total {
		return true, 0
	}
	if amount > int64(*k.Limits.TPM) {
		return false, 0
	}
	// Wait for enough reservations to expire, rather than just the oldest one.
	items := make([]tokenReservation, 0, len(window))
	for _, item := range window {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].At.Before(items[j].At) })
	for _, item := range items {
		total -= item.Tokens
		if amount <= int64(*k.Limits.TPM)-total {
			return false, int(math.Ceil(item.At.Add(time.Minute).Sub(now).Seconds()))
		}
	}
	return false, 0
}
func (a *App) reserveTPM(k ClientKey, id string, n int64, at time.Time) {
	if k.Limits.TPM == nil {
		return
	}
	if a.tpmWindows[k.ID] == nil {
		a.tpmWindows[k.ID] = map[string]tokenReservation{}
	}
	a.tpmWindows[k.ID][id] = tokenReservation{At: at, Tokens: n}
}
func (a *App) settleTPM(k ClientKey, r Record) {
	window := a.tpmWindows[k.ID]
	item, ok := window[r.ID]
	if !ok {
		return
	}
	if r.Submission == "not_sent" {
		delete(window, r.ID)
		return
	}
	if r.Usage.Input != nil && r.Usage.Output != nil {
		item.Tokens = *r.Usage.Input + *r.Usage.Output
		window[r.ID] = item
	}
}
func (a *App) refundRPM(k ClientKey) {
	if b := a.rateBuckets[k.ID]; b != nil {
		b.Tokens = math.Min(float64(b.RPM), b.Tokens+1)
	}
}
