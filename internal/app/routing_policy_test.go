package app

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func policyCandidate(id, model, account string) RoutingPolicyCandidate {
	context, output := int64(10000), int64(100)
	return RoutingPolicyCandidate{Source: Source{ID: id, AccountID: account, AccountGeneration: 1, Generation: 1, Kind: "api_key", NativeProtocol: "responses", Enabled: true, Configured: true, Models: []string{model}}, Model: SourceModel{ID: id + "_model", SourceID: id, UpstreamModel: model, Enabled: true, Version: 1, ContextLimit: &context, MaxOutput: &output}, Member: RouteMember{ModelID: id + "_model", Priority: 0, Weight: 1}}
}
func policyInput() RoutingPolicyInput {
	return RoutingPolicyInput{Key: ClientKey{ID: "key_a", RouteID: "route"}, PublicModel: "coding", Protocol: "responses", Operation: "generate", ClientRaw: []byte(`{"model":"coding","input":"hello","max_output_tokens":20}`), MaxResponse: 1 << 20}
}
func policyTime() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
func policyHasCandidate(v RoutingPolicyResult, id string) bool {
	for _, c := range v.Candidates {
		if c.Model.ID == id {
			return true
		}
	}
	return false
}
func mustPolicyFilter(t *testing.T, state *PolicyState, policy RoutePolicy, strategy string, in RoutingPolicyInput, candidates []RoutingPolicyCandidate, at time.Time) RoutingPolicyResult {
	t.Helper()
	result, err := state.Filter(policy, strategy, in, candidates, at)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestSpecHealthCooldownAccountModelAndSourceScopes(t *testing.T) {
	now := policyTime()
	a := policyCandidate("a", "gpt-a", "shared")
	b := policyCandidate("b", "gpt-a", "shared")
	c := policyCandidate("c", "gpt-a", "separate")
	in := policyInput()
	var state PolicyState
	state.Observe(PolicyObservation{Candidate: a, Kind: "auth"}, now)
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, b, c}, now)
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "c" {
		t.Fatalf("authentication failure escaped its account scope %+v", result.Reasons)
	}
	if !a.Source.Enabled || !b.Source.Enabled || !c.Source.Enabled {
		t.Fatal("health observation changed enabled")
	}
	a.Source.AccountGeneration++
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, c}, now)
	if len(result.Candidates) != 2 {
		t.Fatal("old credential-generation failure blocked new identity")
	}
	state = PolicyState{}
	other := policyCandidate("other", "gpt-b", "shared")
	state.Observe(PolicyObservation{Candidate: b, Kind: "model_unsupported"}, now)
	result = mustPolicyFilter(t, &state, RoutePolicy{AllowCrossModel: true}, "priority", in, []RoutingPolicyCandidate{b, other, c}, now)
	if len(result.Candidates) != 3 {
		t.Fatal("unconfirmed model failure became evidence")
	}
	state.Observe(PolicyObservation{Candidate: b, Kind: "model_unsupported", Confirmed: true}, now)
	result = mustPolicyFilter(t, &state, RoutePolicy{AllowCrossModel: true}, "priority", in, []RoutingPolicyCandidate{b, other, c}, now)
	if policyHasCandidate(result, b.Model.ID) || !policyHasCandidate(result, other.Model.ID) || !policyHasCandidate(result, c.Model.ID) {
		t.Fatalf("model failure used wrong scope %+v", result.Reasons)
	}
	state = PolicyState{}
	for i := 0; i < 3; i++ {
		state.Observe(PolicyObservation{Candidate: b, Kind: "transient"}, now)
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{b, c}, now)
	if policyHasCandidate(result, b.Model.ID) || !policyHasCandidate(result, c.Model.ID) {
		t.Fatal("source cooldown became a global outage")
	}
}
func TestSpecHealthCooldownSingleHalfOpenProbe(t *testing.T) {
	now := policyTime()
	a := policyCandidate("a", "gpt-a", "shared")
	var state PolicyState
	for i := 0; i < 3; i++ {
		state.Observe(PolicyObservation{Candidate: a, Kind: "transient"}, now)
	}
	if state.ClaimProbe(a, now) {
		t.Fatal("cooldown allowed premature dispatch")
	}
	after := now.Add(2 * time.Second)
	for i := 0; i < 2; i++ {
		result := mustPolicyFilter(t, &state, RoutePolicy{}, "priority", policyInput(), []RoutingPolicyCandidate{a}, after)
		if len(result.Candidates) != 1 {
			t.Fatal("preview advanced half-open state")
		}
	}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			if state.ClaimProbe(a, after) {
				admitted.Add(1)
			}
		}()
	}
	close(gate)
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("half-open admitted %d simultaneous probes", admitted.Load())
	}
	if _, err := state.Filter(RoutePolicy{}, "priority", policyInput(), []RoutingPolicyCandidate{a}, after); err == nil {
		t.Fatal("another request bypassed occupied half-open probe")
	}
	state.Observe(PolicyObservation{Candidate: a, Kind: "success"}, after)
	if !state.ClaimProbe(a, after) || !state.ClaimProbe(a, after) {
		t.Fatal("observed success did not restore ordinary admission")
	}
}
func TestSpecHealthCooldownRetryAfterAndUnverifiedQuota(t *testing.T) {
	now := policyTime()
	a := policyCandidate("a", "gpt-a", "shared")
	b := policyCandidate("b", "gpt-b", "shared")
	c := policyCandidate("c", "gpt-a", "separate")
	var state PolicyState
	state.Observe(PolicyObservation{Candidate: a, Kind: "quota", Scope: "account", RetryAfter: "120"}, now)
	result := mustPolicyFilter(t, &state, RoutePolicy{AllowCrossModel: true}, "priority", policyInput(), []RoutingPolicyCandidate{a, b, c}, now.Add(119*time.Second))
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "c" {
		t.Fatal("verified account quota did not affect sibling models")
	}
	if !state.ClaimProbe(a, now.Add(120*time.Second)) {
		t.Fatal("Retry-After was not respected exactly")
	}
	state = PolicyState{}
	state.ObserveAttempt(a, Record{HTTPStatus: 429, Status: "failed"}, "30", "", false, now)
	result = mustPolicyFilter(t, &state, RoutePolicy{AllowCrossModel: true}, "priority", policyInput(), []RoutingPolicyCandidate{a, b, c}, now)
	if policyHasCandidate(result, a.Model.ID) || !policyHasCandidate(result, b.Model.ID) {
		t.Fatal("unknown quota scope was guessed as an account limit")
	}
	state = PolicyState{}
	state.ObserveAttempt(a, Record{HTTPStatus: 404, Status: "failed"}, "", "", false, now)
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", policyInput(), []RoutingPolicyCandidate{a, c}, now)
	if len(result.Candidates) != 2 {
		t.Fatal("generic HTTP 404 was treated as model evidence")
	}
	date := now.Add(45 * time.Second).Format(http.TimeFormat)
	if until := retryAfterUntil(date, now); until == nil || !until.Equal(now.Add(45*time.Second)) {
		t.Fatalf("HTTP-date Retry-After %v", until)
	}
	if retryAfterUntil("provider-private-unrecognized-value", now) != nil {
		t.Fatal("invented Retry-After")
	}
}
func TestSpecAffinity(t *testing.T) {
	now := policyTime()
	a, b := policyCandidate("a", "gpt-a", "shared"), policyCandidate("b", "gpt-a", "other")
	in := policyInput()
	in.SessionID = "same-client-session"
	var state PolicyState
	state.BindSession(RoutePolicy{}, in, b, now)
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, b}, now)
	if result.PreferredModelID != b.Model.ID {
		t.Fatalf("session hint lost %+v", result)
	}
	otherKey := in
	otherKey.Key.ID = "key_b"
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", otherKey, []RoutingPolicyCandidate{a, b}, now)
	if result.PreferredModelID != "" {
		t.Fatal("session affinity crossed Key ownership")
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, b}, now.Add(30*time.Minute))
	if result.PreferredModelID != "" {
		t.Fatal("expired default TTL remained sticky")
	}
	bound := in
	bound.AuthoritativeBinding = true
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", bound, []RoutingPolicyCandidate{a, b}, now)
	if result.PreferredModelID != "" || result.Snapshot.Affinity != "authoritative_binding_precedes_session_hint" {
		t.Fatal("session hint overrode resource authority")
	}
	b.Source.Generation++
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, b}, now)
	if result.PreferredModelID != "" {
		t.Fatal("affinity survived a source generation change")
	}
	disabled := 0
	state = PolicyState{}
	state.BindSession(RoutePolicy{AffinityTTLSeconds: &disabled}, in, a, now)
	if len(state.affinity) != 0 {
		t.Fatal("disabled affinity retained session entries")
	}
}
func TestSpecRouteEligibilityDryCapabilityAndBudgetChecks(t *testing.T) {
	now := policyTime()
	native := policyCandidate("native", "claude-model", "native-account")
	native.Source.NativeProtocol = "messages"
	converter := policyCandidate("converter", "claude-model", "converter-account")
	converter.Member.Priority = -1
	in := policyInput()
	in.Protocol = "messages"
	in.ClientRaw = []byte(`{"model":"coding","max_tokens":20,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://fixture.invalid/image.png"}}]}]}`)
	var state PolicyState
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{native, converter}, now)
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "native" {
		t.Fatalf("incompatible conversion selected before checking request %+v", result.Reasons)
	}
	in = policyInput()
	a, b := policyCandidate("a", "gpt-a", "a"), policyCandidate("b", "gpt-a", "b")
	calls := 0
	in.Eligibility = func(source Source, _ map[string]json.RawMessage) error {
		calls++
		if source.ID == "a" {
			return fmt.Errorf("strict budget lacks a proven upper bound")
		}
		return nil
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a, b}, now)
	if calls != 2 || len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "b" {
		t.Fatal("budget-ineligible candidate was selected")
	}
}
func TestSpecRouteEligibilityExplicitCrossModel(t *testing.T) {
	now := policyTime()
	a, b := policyCandidate("a", "gpt-a", "a"), policyCandidate("b", "gpt-b", "b")
	a.ExcludedReason = "primary source unavailable"
	var state PolicyState
	result, err := state.Filter(RoutePolicy{}, "priority", policyInput(), []RoutingPolicyCandidate{a, b}, now)
	if err == nil || len(result.Candidates) != 0 || !strings.Contains(result.Reasons[1].Reason, "跨模型") {
		t.Fatalf("implicit cross-model fallback %+v %v", result, err)
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{AllowCrossModel: true}, "priority", policyInput(), []RoutingPolicyCandidate{a, b}, now)
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "b" {
		t.Fatal("explicit alias mapping was not honored")
	}
}
func TestSpecAdvancedRoutingCostsAndUnknownMetadata(t *testing.T) {
	now := policyTime()
	a, b, u := policyCandidate("a", "gpt-a", "a"), policyCandidate("b", "gpt-a", "b"), policyCandidate("unknown", "gpt-a", "unknown")
	price := func(currency, rate string) *Price {
		return &Price{Currency: currency, Input: rate, Output: rate, AsOf: now.Add(-time.Hour).Format(time.RFC3339Nano)}
	}
	a.Model.Price = price("USD", "2")
	b.Model.Price = price("USD", "1")
	var state PolicyState
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "cost", policyInput(), []RoutingPolicyCandidate{a, b, u}, now)
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "b" {
		t.Fatalf("unknown price sorted as zero %+v", result)
	}
	b.Model.Price = price("EUR", "1")
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "cost", policyInput(), []RoutingPolicyCandidate{a, b, u}, now)
	if len(result.Candidates) != 3 || !strings.Contains(result.Snapshot.Notice, "cost_unavailable") {
		t.Fatal("different currencies were compared")
	}
	a.Model.Price = nil
	b.Model.Price = nil
	u.Source.Kind = "codex_subscription"
	u.Model.Price = price("USD", "0")
	subscriptionInput := policyInput()
	subscriptionInput.ClientRaw = []byte(`{"model":"coding","input":"hello","stream":true}`)
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "cost", subscriptionInput, []RoutingPolicyCandidate{a, b, u}, now)
	if len(result.Candidates) != 3 || !strings.Contains(result.Snapshot.Notice, "cost_unavailable") {
		t.Fatal("subscription API equivalent became free billing")
	}
	a.Model.Price = price("USD", "1")
	a.Model.Price.AsOf = now.Add(time.Hour).Format(time.RFC3339Nano)
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "cost", policyInput(), []RoutingPolicyCandidate{a, b}, now)
	if len(result.Candidates) != 2 || result.Snapshot.Candidates[0].EstimatedCost != nil {
		t.Fatal("future price became effective early")
	}
}
func TestSpecAdvancedRoutingContextBoundsAndUnknown(t *testing.T) {
	now := policyTime()
	a := policyCandidate("a", "gpt-a", "a")
	in := policyInput()
	body, _, _, _, _, _, err := candidateInput(in.ClientRaw, in.Protocol, a.Source, a.Model.UpstreamModel, in.MaxResponse)
	if err != nil {
		t.Fatal(err)
	}
	tokens, known := policyTextInput(body)
	if !known {
		t.Fatal("text estimate unavailable")
	}
	exact := tokens + 20
	a.Model.ContextLimit = &exact
	var state PolicyState
	result := mustPolicyFilter(t, &state, RoutePolicy{StrictContext: true}, "context_fit", in, []RoutingPolicyCandidate{a}, now)
	if len(result.Candidates) != 1 || result.Snapshot.Candidates[0].EstimateProvenance == "" {
		t.Fatal("exact context boundary rejected")
	}
	exact--
	if _, err = state.Filter(RoutePolicy{StrictContext: true}, "context_fit", in, []RoutingPolicyCandidate{a}, now); err == nil {
		t.Fatal("oversized context selected")
	}
	a.Model.ContextLimit = nil
	if _, err = state.Filter(RoutePolicy{StrictContext: true}, "priority", in, []RoutingPolicyCandidate{a}, now); err == nil {
		t.Fatal("unknown context called guaranteed fit")
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{a}, now)
	if len(result.Candidates) != 1 {
		t.Fatal("basic routing treated unknown context as zero")
	}
	a.Source.NativeProtocol = "messages"
	in.Protocol = "messages"
	in.ClientRaw = []byte(`{"model":"coding","max_tokens":20,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://fixture.invalid/image.png"}}]}]}`)
	large := int64(100000)
	a.Model.ContextLimit = &large
	if _, err = state.Filter(RoutePolicy{StrictContext: true}, "priority", in, []RoutingPolicyCandidate{a}, now); err == nil {
		t.Fatal("image used text tokenizer to guarantee context")
	}
}
func TestSpecAdvancedRoutingLatencySampleWindowAndEWMA(t *testing.T) {
	now := policyTime()
	a, b := policyCandidate("a", "gpt-a", "a"), policyCandidate("b", "gpt-a", "b")
	var state PolicyState
	for i := 0; i < 5; i++ {
		ttft := time.Duration(100*(i+1)) * time.Millisecond
		state.Observe(PolicyObservation{Candidate: a, Kind: "success", AttemptID: fmt.Sprintf("a_%d", i), Operation: "generate", TTFT: &ttft}, now.Add(time.Duration(i)*time.Second))
		slow := 600 * time.Millisecond
		state.Observe(PolicyObservation{Candidate: b, Kind: "success", AttemptID: fmt.Sprintf("b_%d", i), Operation: "generate", TTFT: &slow}, now.Add(time.Duration(i)*time.Second))
	}
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{a, b}, now.Add(5*time.Second))
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "a" || result.Snapshot.Candidates[0].LatencyCount != 5 || math.Abs(*result.Snapshot.Candidates[0].LatencyEWMA-263.84) > 0.0001 {
		t.Fatalf("bad latency observation %+v", result.Snapshot)
	}
	for i := 0; i < 3; i++ {
		_ = mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{a, b}, now.Add(5*time.Second))
	}
	if len(state.samples[policySampleKey{"a", "gpt-a", "generate", 1, 1}]) != 5 {
		t.Fatal("preview changed observed statistics")
	}
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{a, b}, now.Add(6*time.Minute))
	if len(result.Candidates) != 2 || !strings.Contains(result.Snapshot.Notice, "latency_unavailable") {
		t.Fatal("expired latency samples used")
	}
	a.Source.AccountGeneration++
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{a, b}, now.Add(5*time.Second))
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != "b" {
		t.Fatal("old identity TTFT leaked into new generation")
	}
}
func TestSpecAdvancedRoutingLatencyMinimumAndBoundedSamples(t *testing.T) {
	now := policyTime()
	a, b := policyCandidate("a", "gpt-a", "a"), policyCandidate("b", "gpt-a", "b")
	var state PolicyState
	for i := 0; i < 120; i++ {
		fast := time.Millisecond
		state.Observe(PolicyObservation{Candidate: a, Kind: "success", AttemptID: fmt.Sprintf("a_%d", i), Operation: "other", TTFT: &fast}, now)
	}
	slow := 200 * time.Millisecond
	for i := 0; i < 4; i++ {
		state.Observe(PolicyObservation{Candidate: b, Kind: "success", AttemptID: fmt.Sprintf("b_%d", i), Operation: "generate", TTFT: &slow}, now)
	}
	state.Observe(PolicyObservation{Candidate: b, Kind: "success", AttemptID: "b_3", Operation: "generate", TTFT: &slow}, now)
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{a, b}, now)
	if len(result.Candidates) != 2 || result.Snapshot.Candidates[1].LatencyCount != 4 {
		t.Fatal("insufficient/other-operation samples ranked as observed latency")
	}
	if len(state.samples[policySampleKey{"a", "gpt-a", "other", 1, 1}]) != 100 {
		t.Fatal("latency samples were not bounded")
	}
}

func TestSpecAdvancedRoutingSemanticTTFTEvidence(t *testing.T) {
	for _, fixture := range []struct {
		protocol, event, body string
		semantic              bool
	}{
		{"responses", "", `{"type":"response.created","response":{"id":"fixture"}}`, false},
		{"responses", "", `{"type":"response.output_text.delta","delta":""}`, false},
		{"responses", "", `{"type":"response.output_text.delta","delta":"hi"}`, true},
		{"responses", "", `{"type":"response.output_item.added","item":{"type":"function_call","name":"weather"}}`, false},
		{"chat_completions", "", `{"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{"chat_completions", "", `{"choices":[{"delta":{"content":"hi"}}]}`, true},
		{"messages", "content_block_start", `{"content_block":{"type":"text","text":""}}`, false},
		{"messages", "content_block_delta", `{"delta":{"type":"text_delta","text":"hi"}}`, true},
		{"messages", "content_block_start", `{"content_block":{"type":"tool_use","name":"weather"}}`, false},
	} {
		if got := policySemanticOutput([]byte(fixture.body), fixture.event, fixture.protocol); got != fixture.semantic {
			t.Fatalf("semantic output=%t for %s", got, fixture.body)
		}
	}
	now := policyTime()
	candidate := policyCandidate("a", "gpt-a", "a")
	var state PolicyState
	for i := 0; i < 5; i++ {
		metadata := now.Add(time.Millisecond)
		started := now.Add(time.Second)
		content := started.Add(100 * time.Millisecond)
		state.ObserveAttempt(candidate, Record{AttemptID: fmt.Sprintf("real_%d", i), Started: now, AttemptStarted: started, FirstEvent: &metadata, FirstContentAt: &content, Status: "succeeded"}, "", "", false, now.Add(2*time.Second))
	}
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "latency", policyInput(), []RoutingPolicyCandidate{candidate}, now.Add(2*time.Second))
	if result.Snapshot.Candidates[0].LatencyEWMA == nil || *result.Snapshot.Candidates[0].LatencyEWMA != 100 {
		t.Fatal("TTFT used logical request start or metadata frame")
	}
}

func TestSpecHealthCooldownLateTerminalDoesNotReplaceNewerEvidence(t *testing.T) {
	now := policyTime()
	c := policyCandidate("a", "gpt-a", "a")
	var state PolicyState
	first := now
	second := now.Add(time.Second)
	state.ObserveAttempt(c, Record{AttemptID: "old_success", Ended: &first, Status: "succeeded"}, "", "", false, first)
	state.ObserveAttempt(c, Record{AttemptID: "new_auth_failure", Ended: &second, HTTPStatus: 401, Status: "failed"}, "", "", false, second)
	state.ObserveAttempt(c, Record{AttemptID: "old_success", Ended: &first, Status: "succeeded"}, "", "", false, now.Add(10*time.Second))
	if state.ClaimProbe(c, now.Add(10*time.Second)) {
		t.Fatal("duplicate old success erased newer authentication failure")
	}
	state.ObserveAttempt(c, Record{AttemptID: "new_recovery", Ended: &second, Status: "succeeded"}, "", "", false, second)
	state.ObserveAttempt(c, Record{AttemptID: "late_old_failure", Ended: &first, HTTPStatus: 401, Status: "failed"}, "", "", false, now.Add(10*time.Second))
	if !state.ClaimProbe(c, now.Add(10*time.Second)) {
		t.Fatal("late old failure replaced newer recovery")
	}
}
func TestSpecAdvancedRoutingServerToolFeesRemainUnknown(t *testing.T) {
	now := policyTime()
	c := policyCandidate("native", "claude-model", "account")
	c.Source.NativeProtocol = "messages"
	c.Model.Price = &Price{Currency: "USD", Input: "1", Output: "1", AsOf: now.Add(-time.Hour).Format(time.RFC3339)}
	in := policyInput()
	in.Protocol = "messages"
	in.ClientRaw = []byte(`{"model":"coding","max_tokens":20,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	var state PolicyState
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "cost", in, []RoutingPolicyCandidate{c}, now)
	if result.Snapshot.Candidates[0].EstimatedCost != nil || !strings.Contains(result.Snapshot.Notice, "cost_unavailable") {
		t.Fatal("server-tool fee inferred from token-only prices")
	}
}
