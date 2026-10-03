package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RoutePolicy is anonymously embedded in Route so its JSON fields remain flat.
// Strategy remains the existing route field.
type RoutePolicy struct {
	AllowCrossModel    bool `json:"allow_cross_model"`
	AffinityTTLSeconds *int `json:"affinity_ttl_seconds,omitempty"`
	StrictContext      bool `json:"strict_context"`
}

func validateRoutePolicy(p RoutePolicy) error {
	if p.AffinityTTLSeconds != nil && (*p.AffinityTTLSeconds < 0 || int64(*p.AffinityTTLSeconds) > math.MaxInt64/int64(time.Second)) {
		return fmt.Errorf("affinity_ttl_seconds 必须是可表示的非负秒数；0 表示关闭")
	}
	return nil
}
func policyTTL(p RoutePolicy) time.Duration {
	seconds := 1800
	if p.AffinityTTLSeconds != nil {
		seconds = *p.AffinityTTLSeconds
	}
	return time.Duration(seconds) * time.Second
}
func validRouteStrategy(s string) bool {
	return s == "priority" || s == "weighted_round_robin" || s == "cost" || s == "latency" || s == "context_fit"
}

type RoutingPolicyCandidate struct {
	Source         Source
	Model          SourceModel
	Member         RouteMember
	ExcludedReason string // Existing enabled/auth/account-slot checks run first.
}
type RoutingPolicyInput struct {
	SubscriptionFirst         bool // Global scheduling is applied by the production route caller.
	AllowPaidFallback         bool
	IncludeBusySources        bool
	OwnedRequestID            string // Only a registered running request may reuse its own account slot.
	SnapshotOut               *RoutingPolicySnapshot
	Key                       ClientKey
	PublicModel               string
	Protocol                  string
	Operation                 string
	ClientRaw                 []byte
	SessionID                 string
	AuthoritativeBinding      bool
	BoundSourceID, BoundModel string
	ManualTest                bool
	MaxResponse               int64
	// The production caller supplies prepareAccounting here. It is a dry eligibility
	// check: this callback must not create reservations or contact the provider.
	Eligibility func(Source, map[string]json.RawMessage) error
}
type CandidatePolicySnapshot struct {
	ModelID            string     `json:"model_id"`
	EstimateProvenance string     `json:"estimate_provenance,omitempty"`
	InputTokens        *int64     `json:"estimated_input_tokens,omitempty"`
	OutputReserve      *int64     `json:"output_reserve,omitempty"`
	EstimatedCost      *string    `json:"estimated_cost,omitempty"`
	Currency           string     `json:"currency,omitempty"`
	PriceVersion       string     `json:"price_version,omitempty"`
	ContextLimit       *int64     `json:"context_limit,omitempty"`
	LatencyCount       int        `json:"latency_count"`
	LatencyEWMA        *float64   `json:"latency_ewma_ms,omitempty"`
	LatencyFrom        *time.Time `json:"latency_from,omitempty"`
	LatencyTo          *time.Time `json:"latency_to,omitempty"`
	Reason             string     `json:"reason"`
}
type RoutingPolicySnapshot struct {
	Algorithm   string                    `json:"algorithm"`
	EvaluatedAt time.Time                 `json:"evaluated_at"`
	Notice      string                    `json:"notice,omitempty"`
	Affinity    string                    `json:"affinity,omitempty"`
	Candidates  []CandidatePolicySnapshot `json:"candidates"`
}
type RoutingPolicyResult struct {
	Candidates       []RoutingPolicyCandidate // Equally ranked candidates; existing smooth weights break ties.
	Reasons          []CandidateReason
	PreferredModelID string // Nonempty only for an eligible, current-generation session hint.
	Snapshot         RoutingPolicySnapshot
}
type policyScope struct {
	Kind, ID, Model string
	Generation      int
}
type policyHealth struct {
	ObservedAt time.Time
	AttemptID  string
	Failures   int
	Until      time.Time
	Probing    bool
	Blocked    bool
	Reason     string
}
type policySample struct {
	AttemptID string
	At        time.Time
	TTFT      time.Duration
}
type policySampleKey struct {
	SourceID, Model, Operation          string
	SourceGeneration, AccountGeneration int
}
type policySessionKey struct {
	KeyID, RouteID string
	Session        [32]byte
}
type policyAffinity struct {
	ModelID, SourceID                   string
	SourceGeneration, AccountGeneration int
	Expires                             time.Time
}

// PolicyState belongs to App; it contains no provider credentials or persisted state.
// Its zero value is usable. Every preview path reads without advancing this state.
type PolicyState struct {
	mu       sync.Mutex
	health   map[policyScope]policyHealth
	samples  map[policySampleKey][]policySample
	affinity map[policySessionKey]policyAffinity
}

func candidateScopes(c RoutingPolicyCandidate) []policyScope {
	return []policyScope{{Kind: "account", ID: c.Source.AccountID, Generation: c.Source.AccountGeneration}, {Kind: "model", ID: c.Source.AccountID, Generation: c.Source.AccountGeneration, Model: c.Model.UpstreamModel}, {Kind: "source", ID: c.Source.ID, Generation: c.Source.Generation}}
}
func (s *PolicyState) candidateHealth(c RoutingPolicyCandidate, now time.Time) string {
	for _, scope := range candidateScopes(c) {
		if state, ok := s.health[scope]; ok {
			if state.Blocked {
				return state.Reason
			}
			if state.Until.After(now) {
				return state.Reason + "；冷却至 " + state.Until.UTC().Format(time.RFC3339Nano)
			}
			if !state.Until.IsZero() && state.Probing {
				return "恢复探测已由另一条真实请求占用"
			}
		}
	}
	return ""
}

// ClaimProbe runs immediately before admission/dispatch; false means another real
// request claimed the half-open slot. Calling it for a preview would be a bug.
func (s *PolicyState) ClaimProbe(c RoutingPolicyCandidate, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.candidateHealth(c, now) != "" {
		return false
	}
	for _, scope := range candidateScopes(c) {
		state, ok := s.health[scope]
		if ok && !state.Until.IsZero() {
			state.Probing = true
			s.health[scope] = state
		}
	}
	return true
}
func (s *PolicyState) ReleaseProbe(c RoutingPolicyCandidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, scope := range candidateScopes(c) {
		if state, ok := s.health[scope]; ok {
			state.Probing = false
			s.health[scope] = state
		}
	}
}

type PolicyObservation struct {
	Candidate  RoutingPolicyCandidate
	Kind       string // success, auth, model_unsupported, transient, quota; provider facts only.
	Scope      string // quota scope supplied by a verified adapter: account/model/source.
	Confirmed  bool   // Required for model_unsupported; HTTP 404 alone is insufficient.
	RetryAfter string
	AttemptID  string
	Operation  string
	TTFT       *time.Duration
	ObservedAt time.Time // Terminal observation time, distinct from callback arrival.
}

func retryAfterUntil(value string, now time.Time) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= math.MaxInt64/int64(time.Second) {
		until := now.Add(time.Duration(seconds) * time.Second)
		return &until
	}
	if at, err := http.ParseTime(value); err == nil {
		if at.Before(now) {
			at = now
		}
		return &at
	}
	return nil
}
func policyBackoff(scope policyScope, count int) time.Duration {
	exponent := count - 3
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 6 {
		exponent = 6
	}
	base := time.Second * time.Duration(1<<exponent)
	if base > 60*time.Second {
		base = 60 * time.Second
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(scope.Kind + scope.ID + scope.Model + strconv.Itoa(count)))
	// Jitter remains below the documented sixty-second local backoff cap.
	jitter := time.Duration(hash.Sum32()%201) * base / 1000
	if base+jitter > 60*time.Second {
		return 60 * time.Second
	}
	return base + jitter
}
func (s *PolicyState) Observe(o PolicyObservation, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := o.Candidate
	scopes := candidateScopes(c)
	at := o.ObservedAt
	if at.IsZero() {
		at = now
	}
	if s.health == nil {
		s.health = map[policyScope]policyHealth{}
	}
	if o.Kind == "success" {
		for _, scope := range scopes {
			previous := s.health[scope]
			if previous.ObservedAt.After(at) || o.AttemptID != "" && previous.AttemptID == o.AttemptID {
				continue
			}
			s.health[scope] = policyHealth{ObservedAt: at, AttemptID: o.AttemptID}
		}
		if o.TTFT == nil || *o.TTFT < 0 || o.AttemptID == "" {
			return
		}
		if s.samples == nil {
			s.samples = map[policySampleKey][]policySample{}
		}
		key := policySampleKey{c.Source.ID, c.Model.UpstreamModel, o.Operation, c.Source.Generation, c.Source.AccountGeneration}
		samples := s.samples[key]
		fresh := make([]policySample, 0, 100)
		for _, sample := range samples {
			if sample.AttemptID == o.AttemptID {
				return
			}
			if !sample.At.Before(now.Add(-5 * time.Minute)) {
				fresh = append(fresh, sample)
			}
		}
		fresh = append(fresh, policySample{o.AttemptID, at, *o.TTFT})
		slices.SortStableFunc(fresh, func(a, b policySample) int { return a.At.Compare(b.At) })
		if len(fresh) > 100 {
			fresh = fresh[len(fresh)-100:]
		}
		s.samples[key] = fresh
		return
	}
	var scope policyScope
	reason := ""
	switch o.Kind {
	case "auth":
		scope = scopes[0]
		reason = "账号认证被上游拒绝"
	case "model_unsupported":
		if !o.Confirmed {
			return
		}
		scope = scopes[1]
		reason = "上游已明确不支持此账号/模型"
	case "transient":
		scope = scopes[2]
		reason = "来源端点连续发生可恢复错误"
	case "quota":
		switch o.Scope {
		case "account":
			scope = scopes[0]
		case "model":
			scope = scopes[1]
		case "source":
			scope = scopes[2]
		default:
			return
		}
		reason = "已观测到此作用域的上游限流或额度拒绝"
	default:
		return
	}
	if s.health == nil {
		s.health = map[policyScope]policyHealth{}
	}
	state := s.health[scope]
	if state.ObservedAt.After(at) || o.AttemptID != "" && state.AttemptID == o.AttemptID {
		return
	}
	state.ObservedAt, state.AttemptID = at, o.AttemptID
	state.Failures++
	state.Probing = false
	state.Reason = reason
	if o.Kind == "auth" || o.Kind == "model_unsupported" {
		state.Blocked = true
	} else if until := retryAfterUntil(o.RetryAfter, now); until != nil {
		state.Until = *until
	} else if state.Failures >= 3 {
		state.Until = now.Add(policyBackoff(scope, state.Failures))
	}
	s.health[scope] = state
	// A half-open attempt can also fail in a different scope. Release its old probe
	// slots, then preserve the newly observed scope's failure and cooldown.
	for _, other := range scopes {
		if other == scope {
			continue
		}
		if state, ok := s.health[other]; ok {
			state.Probing = false
			s.health[other] = state
		}
	}
}
func (s *PolicyState) ObserveAttempt(c RoutingPolicyCandidate, r Record, retryAfter, quotaScope string, modelUnsupportedConfirmed bool, now time.Time) {
	o := PolicyObservation{Candidate: c, AttemptID: r.AttemptID, Operation: r.Operation, RetryAfter: retryAfter}
	if o.Operation == "" {
		o.Operation = "generate"
	}
	if r.Ended != nil {
		o.ObservedAt = *r.Ended
	}
	switch {
	case r.UpstreamStatus == "completed" || r.Status == "succeeded":
		o.Kind = "success"
		started := r.AttemptStarted
		if started.IsZero() {
			started = r.Started
		}
		if r.FirstContentAt != nil && !r.FirstContentAt.Before(started) {
			ttft := r.FirstContentAt.Sub(started)
			o.TTFT = &ttft
		}
	case r.HTTPStatus == 401:
		o.Kind = "auth"
	case r.HTTPStatus == 404 && modelUnsupportedConfirmed:
		o.Kind = "model_unsupported"
		o.Confirmed = true
	case r.HTTPStatus == 429 && quotaScope != "":
		o.Kind = "quota"
		o.Scope = quotaScope
	case r.HTTPStatus == 429:
		o.Kind = "transient" // Unknown quota scope stays at this endpoint.
	case r.HTTPStatus >= 500 || (r.ErrorStage == "connect_or_headers" || r.ErrorStage == "connect"):
		o.Kind = "transient"
	default:
		s.ReleaseProbe(c)
		return
	}
	s.Observe(o, now)
}
func (s *PolicyState) BindSession(policy RoutePolicy, in RoutingPolicyInput, c RoutingPolicyCandidate, now time.Time) {
	if in.SessionID == "" || in.Key.ID == "" || in.AuthoritativeBinding || policyTTL(policy) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.affinity == nil {
		s.affinity = map[policySessionKey]policyAffinity{}
	}
	for key, entry := range s.affinity {
		if !entry.Expires.After(now) {
			delete(s.affinity, key)
		}
	}
	// Bound memory without retaining the client's literal session header.
	if len(s.affinity) >= 4096 {
		var earliest policySessionKey
		var expiry time.Time
		for key, entry := range s.affinity {
			if expiry.IsZero() || entry.Expires.Before(expiry) {
				earliest, expiry = key, entry.Expires
			}
		}
		delete(s.affinity, earliest)
	}
	key := policySessionKey{in.Key.ID, in.Key.RouteID, sha256.Sum256([]byte(in.SessionID))}
	s.affinity[key] = policyAffinity{c.Model.ID, c.Source.ID, c.Source.Generation, c.Source.AccountGeneration, now.Add(policyTTL(policy))}
}
func policyModelAnchor(candidates []RoutingPolicyCandidate) string {
	all := slices.Clone(candidates)
	slices.SortFunc(all, func(a, b RoutingPolicyCandidate) int {
		if a.Member.Priority < b.Member.Priority {
			return -1
		}
		if a.Member.Priority > b.Member.Priority {
			return 1
		}
		return strings.Compare(a.Model.ID, b.Model.ID)
	})
	for _, c := range all {
		if c.Model.UpstreamModel != "" {
			return c.Model.UpstreamModel
		}
	}
	return ""
}
func policyTextInput(body map[string]json.RawMessage) (int64, bool) {
	values := map[string]json.RawMessage{}
	var multimodal func(any) bool
	multimodal = func(v any) bool {
		switch v := v.(type) {
		case []any:
			for _, item := range v {
				if multimodal(item) {
					return true
				}
			}
		case map[string]any:
			if kind, _ := v["type"].(string); kind == "image" || kind == "input_image" || kind == "image_url" || kind == "audio" || kind == "input_audio" || kind == "video" || kind == "document" {
				return true
			}
			for key, item := range v {
				if key == "image_url" || key == "input_audio" {
					return true
				}
				if multimodal(item) {
					return true
				}
			}
		}
		return false
	}
	for _, key := range []string{"input", "messages", "instructions", "system", "tools", "text", "response_format"} {
		if raw, ok := body[key]; ok {
			var value any
			if json.Unmarshal(raw, &value) != nil || key != "tools" && key != "text" && key != "response_format" && multimodal(value) {
				return 0, false
			}
			values[key] = raw
		}
	}
	n, err := estimateTokens(encode(values))
	return n, err == nil
}
func policyOutputReserve(body map[string]json.RawMessage, m SourceModel) (int64, bool) {
	for _, key := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if raw, ok := body[key]; ok {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n <= 0 {
				return 0, false
			}
			return n, true
		}
	}
	if m.MaxOutput != nil && *m.MaxOutput > 0 {
		return *m.MaxOutput, true
	}
	return 0, false
}
func (s *PolicyState) latencySnapshot(c RoutingPolicyCandidate, operation string, now time.Time) (int, *float64, *time.Time, *time.Time) {
	key := policySampleKey{c.Source.ID, c.Model.UpstreamModel, operation, c.Source.Generation, c.Source.AccountGeneration}
	count := 0
	ewma := float64(0)
	var first, last time.Time
	for _, sample := range s.samples[key] {
		if sample.At.Before(now.Add(-5*time.Minute)) || sample.At.After(now) {
			continue
		}
		value := float64(sample.TTFT) / float64(time.Millisecond)
		if count == 0 {
			first = sample.At
			ewma = value
		} else {
			ewma = .2*value + .8*ewma
		}
		last = sample.At
		count++
	}
	if count < 5 {
		return count, nil, nil, nil
	}
	return count, &ewma, &first, &last
}
func preferSubscriptions(candidates []RoutingPolicyCandidate, reasons []CandidateReason, allowPaid bool) []RoutingPolicyCandidate {
	hasSubscription := slices.ContainsFunc(candidates, func(c RoutingPolicyCandidate) bool { return c.Source.Kind == "codex_subscription" })
	return slices.DeleteFunc(candidates, func(c RoutingPolicyCandidate) bool {
		if c.Source.Kind == "codex_subscription" || c.Source.Kind == "none" || allowPaid && !hasSubscription {
			return false
		}
		for i := range reasons {
			if reasons[i].ModelID == c.Model.ID {
				reasons[i].Eligible = false
				if !allowPaid {
					reasons[i].Reason = "全局调度已关闭付费 API 回退"
				} else {
					reasons[i].Reason = "有可用订阅候选，付费 API 仅作为回退"
				}
			}
		}
		return true
	})
}

func (s *PolicyState) Filter(policy RoutePolicy, strategy string, in RoutingPolicyInput, candidates []RoutingPolicyCandidate, now time.Time) (RoutingPolicyResult, error) {
	result := RoutingPolicyResult{Candidates: []RoutingPolicyCandidate{}, Reasons: []CandidateReason{}, Snapshot: RoutingPolicySnapshot{Algorithm: strategy, EvaluatedAt: now.UTC(), Candidates: []CandidatePolicySnapshot{}}}
	if err := validateRoutePolicy(policy); err != nil {
		return result, err
	}
	if !validRouteStrategy(strategy) {
		return result, fmt.Errorf("路由策略无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	anchor := policyModelAnchor(candidates)
	estimates := map[string]CandidatePolicySnapshot{}
	for _, candidate := range candidates {
		c := candidate
		reason := CandidateReason{ModelID: c.Model.ID, SourceID: c.Source.ID, AccountID: c.Source.AccountID, UpstreamModel: c.Model.UpstreamModel}
		snapshot := CandidatePolicySnapshot{ModelID: c.Model.ID, ContextLimit: c.Model.ContextLimit}
		switch {
		case c.ExcludedReason != "":
			reason.Reason = c.ExcludedReason
		case !policy.AllowCrossModel && c.Model.UpstreamModel != anchor:
			reason.Reason = "跨模型切换未明确启用；此成员与别名主模型不同"
		case !in.ManualTest && s.candidateHealth(c, now) != "":
			reason.Reason = s.candidateHealth(c, now)
		default:
			if !policy.AllowCrossModel {
				snapshot.Reason = "同一上游模型的显式别名成员"
			}
			if c.Model.Price != nil {
				price := *c.Model.Price
				c.Source.Price = &price
			}
			if !policy.AllowCrossModel && c.Model.UpstreamModel == "" {
				reason.Reason = "模型映射未知"
				break
			}
			body, _, _, _, _, _, err := candidateInput(in.ClientRaw, in.Protocol, c.Source, c.Model.UpstreamModel, in.MaxResponse)
			if err != nil {
				reason.Reason = "请求能力不兼容：" + err.Error()
				break
			}
			if in.Eligibility != nil {
				if err = in.Eligibility(c.Source, body); err != nil {
					reason.Reason = "准入策略不适用：" + err.Error()
					break
				}
			}
			input, knownInput := policyTextInput(body)
			output, knownOutput := policyOutputReserve(body, c.Model)
			if knownInput {
				snapshot.InputTokens = &input
				snapshot.EstimateProvenance = "o200k_base; tokenizer-0.8.1; serialized text/tool estimate"
			}
			if knownOutput {
				snapshot.OutputReserve = &output
			}
			if knownInput && c.Model.ContextLimit != nil && input > *c.Model.ContextLimit {
				reason.Reason = "估算输入已超过已配置 context_limit"
				break
			}
			if knownInput && knownOutput && c.Model.ContextLimit != nil {
				if input > *c.Model.ContextLimit || output > *c.Model.ContextLimit-input {
					reason.Reason = "估算输入与输出预留超过已配置 context_limit"
					break
				}
			} else if policy.StrictContext || strategy == "context_fit" {
				reason.Reason = "严格上下文资格未知：需要文本估算、输出预留和已配置 context_limit"
				break
			}
			if c.Source.Kind == "api_key" && knownInput && knownOutput && c.Model.Price != nil && policyTokenOnlyBilling(body) {
				price := *c.Model.Price
				asOf, e := time.Parse(time.RFC3339Nano, price.AsOf)
				if e == nil && !asOf.After(now) && validPrice(price) {
					price.Cached = ""
					price.CacheCreation = ""
					snapshot.EstimatedCost = estimate(Usage{Input: &input, Output: &output}, &price)
					snapshot.Currency = price.Currency
					snapshot.PriceVersion = "model-v" + strconv.Itoa(c.Model.Version) + "@" + price.AsOf
				}
			}
			snapshot.LatencyCount, snapshot.LatencyEWMA, snapshot.LatencyFrom, snapshot.LatencyTo = s.latencySnapshot(c, in.Operation, now)
			reason.Eligible = true
			reason.Reason = "请求转换与准入检查通过；未知 provider 能力仍需真实验证"
			result.Candidates = append(result.Candidates, c)
		}
		snapshot.Reason = reason.Reason
		estimates[c.Model.ID] = snapshot
		result.Reasons = append(result.Reasons, reason)
		result.Snapshot.Candidates = append(result.Snapshot.Candidates, snapshot)
	}
	if in.SubscriptionFirst {
		result.Candidates = preferSubscriptions(result.Candidates, result.Reasons, in.AllowPaidFallback)
		for i := range result.Snapshot.Candidates {
			result.Snapshot.Candidates[i].Reason = result.Reasons[i].Reason
		}
	}
	priority := math.MaxInt
	for _, c := range result.Candidates {
		priority = min(priority, c.Member.Priority)
	}
	result.Candidates = slices.DeleteFunc(result.Candidates, func(c RoutingPolicyCandidate) bool { return c.Member.Priority != priority })
	slices.SortFunc(result.Candidates, func(a, b RoutingPolicyCandidate) int { return strings.Compare(a.Model.ID, b.Model.ID) })
	if len(result.Candidates) == 0 {
		return result, fmt.Errorf("没有符合请求能力和路由策略的候选，请查看排除原因")
	}
	switch strategy {
	case "cost":
		currency := ""
		var best *big.Rat
		var winners []RoutingPolicyCandidate
		mixed := false
		for _, c := range result.Candidates {
			snap := estimates[c.Model.ID]
			if snap.EstimatedCost == nil {
				continue
			}
			if currency != "" && currency != snap.Currency {
				mixed = true
				break
			}
			currency = snap.Currency
			n, ok := new(big.Rat).SetString(*snap.EstimatedCost)
			if !ok {
				continue
			}
			if best == nil || n.Cmp(best) < 0 {
				best = n
				winners = []RoutingPolicyCandidate{c}
			} else if n.Cmp(best) == 0 {
				winners = append(winners, c)
			}
		}
		if mixed || len(winners) == 0 {
			result.Snapshot.Notice = "cost_unavailable：价格未知或存在不同币种；使用基础优先级与权重，不换算币种"
		} else {
			result.Candidates = winners
		}
	case "latency":
		best := math.Inf(1)
		var winners []RoutingPolicyCandidate
		for _, c := range result.Candidates {
			snap := estimates[c.Model.ID]
			if snap.LatencyEWMA == nil {
				continue
			}
			if *snap.LatencyEWMA < best {
				best = *snap.LatencyEWMA
				winners = []RoutingPolicyCandidate{c}
			} else if *snap.LatencyEWMA == best {
				winners = append(winners, c)
			}
		}
		if len(winners) == 0 {
			result.Snapshot.Notice = "latency_unavailable：最近 5 分钟成功 TTFT 样本少于 5；使用基础优先级与权重"
		} else {
			result.Candidates = winners
		}
	case "context_fit":
		result.Snapshot.Notice = "按已配置上下文与带来源说明的文本估算筛选；未改写或裁剪请求"
	}
	if in.SessionID != "" && in.Key.ID != "" && !in.AuthoritativeBinding && policyTTL(policy) > 0 {
		key := policySessionKey{in.Key.ID, in.Key.RouteID, sha256.Sum256([]byte(in.SessionID))}
		hint, ok := s.affinity[key]
		if ok && hint.Expires.After(now) {
			for _, c := range result.Candidates {
				if c.Model.ID == hint.ModelID && c.Source.ID == hint.SourceID && c.Source.Generation == hint.SourceGeneration && c.Source.AccountGeneration == hint.AccountGeneration {
					result.PreferredModelID = c.Model.ID
					result.Snapshot.Affinity = "key_scoped_session_hint"
					break
				}
			}
		}
	}
	if in.AuthoritativeBinding {
		result.Snapshot.Affinity = "authoritative_binding_precedes_session_hint"
	}
	return result, nil
}

// policySemanticOutput recognizes wire evidence of output, excluding metadata,
// role-only Chat chunks, empty content starts and keepalive frames. The caller
// timestamps the first true result before doing client protocol conversion.
func policySemanticOutput(raw []byte, event, protocol string) bool {
	var frame struct {
		Type  string          `json:"type"`
		Delta json.RawMessage `json:"delta"`
		Item  struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"item"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"content_block"`
		Choices []struct {
			Delta struct {
				Content   string            `json:"content"`
				Refusal   string            `json:"refusal"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	if frame.Type == "" {
		frame.Type = event
	}
	if protocol == "chat_completions" {
		for _, choice := range frame.Choices {
			if choice.Delta.Content != "" || choice.Delta.Refusal != "" {
				return true
			}
		}
		return false
	}
	if protocol == "messages" {
		if frame.Type == "content_block_start" {
			return frame.ContentBlock.Type == "text" && frame.ContentBlock.Text != ""
		}
		if frame.Type != "content_block_delta" {
			return false
		}
		var delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
			JSON     string `json:"partial_json"`
		}
		if json.Unmarshal(frame.Delta, &delta) != nil {
			return false
		}
		return delta.Type == "text_delta" && delta.Text != "" || delta.Type == "thinking_delta" && delta.Thinking != "" || delta.Type == "input_json_delta" && delta.JSON != ""
	}
	switch frame.Type {
	case "response.output_text.delta", "response.refusal.delta", "response.function_call_arguments.delta", "response.reasoning_summary_text.delta":
		var delta string
		return json.Unmarshal(frame.Delta, &delta) == nil && delta != ""
	case "response.output_item.added":
		return false
	}
	return false
}

func policyTokenOnlyBilling(body map[string]json.RawMessage) bool {
	var tools []struct {
		Type  string          `json:"type"`
		Tools json.RawMessage `json:"tools"`
	}
	raw := body["tools"]
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		switch tool.Type {
		case "", "function", "custom":
		case "namespace":
			if !policyTokenOnlyBilling(map[string]json.RawMessage{"tools": tool.Tools}) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
