package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionSchedulingPersistenceAndCAS(t *testing.T) {
	a := contractApp(t, nil)
	patch := func(version, threshold int, paid bool) *httptest.ResponseRecorder {
		return lifecycleAdmin(a, "PATCH", "/admin/settings", encode(map[string]any{"version": version, "changes": map[string]any{"allow_paid_fallback": paid, "subscription_quota_threshold": threshold}}), "")
	}
	mustOperationsStatus(t, patch(1, 20, false), 200)
	if a.Config.AllowPaidFallback || a.Config.SubscriptionQuotaThreshold != 20 {
		t.Fatal("settings not applied immediately")
	}
	mustOperationsStatus(t, patch(1, 5, true), 409)
	mustOperationsStatus(t, patch(2, 101, true), 400)
	mustOperationsStatus(t, patch(2, -1, true), 400)
	state, err := a.Store.readRuntimeSettings()
	if err != nil || state.Version != 2 {
		t.Fatal("rejected changes affected persisted version", err)
	}
	restored := Config{AllowPaidFallback: true, SubscriptionQuotaThreshold: 5}
	state.Current.apply(&restored)
	if restored.AllowPaidFallback || restored.SubscriptionQuotaThreshold != 20 {
		t.Fatal("saved changes were lost on startup application")
	}
	pack, err := a.collectConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changes := pack.Settings["runtime_settings"]
	if changes.AllowPaidFallback == nil || *changes.AllowPaidFallback || changes.SubscriptionQuotaThreshold == nil || *changes.SubscriptionQuotaThreshold != 20 {
		t.Fatal("configuration export omitted scheduling settings")
	}
	if err := validatePortableSettings(changes); err != nil {
		t.Fatal(err)
	}
	bad := 101
	changes.SubscriptionQuotaThreshold = &bad
	if validatePortableSettings(changes) == nil {
		t.Fatal("import accepted invalid threshold")
	}
}

func TestSubscriptionSchedulingPreviewAndRequestSelection(t *testing.T) {
	a, sub, _ := quotaTestApp(t, nil)
	paid := Source{ID: "paid", Name: "Paid API", Kind: "api_key", Enabled: true, Version: 1, Generation: 1, CredentialRef: "source", BaseURL: "http://upstream.invalid", Configured: true, AuthStatus: "configured", Models: []string{"fixture-model"}}
	if err := a.Store.saveSource(paid); err != nil {
		t.Fatal(err)
	}
	models, err := a.Store.models("")
	if err != nil {
		t.Fatal(err)
	}
	route := Route{ID: "schedule", Name: "Coding", Enabled: true, Version: 1, Strategy: "priority", MaxAttempts: 2}
	for _, m := range models {
		priority := 0
		if m.SourceID == sub.ID {
			priority = 10
		}
		route.Members = append(route.Members, RouteMember{ModelID: m.ID, Priority: priority, Weight: 1})
	}
	if _, err = a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?)", route.ID, encode(route)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", "coding", route.ID, encode(Alias{PublicModel: "coding", RouteID: route.ID, Version: 1})); err != nil {
		t.Fatal(err)
	}
	key := ClientKey{ID: "key", RouteID: route.ID}
	if _, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	setQuota := func(used int, stale bool) {
		t.Helper()
		q, e := parseCodexQuota([]byte(quotaWire(time.Now(), used)), sub, time.Now())
		if e != nil {
			t.Fatal(e)
		}
		if stale {
			q.ExpiresAt = timePointer(time.Now().Add(-time.Second))
		}
		if e = json.Unmarshal([]byte(encode(q)), &sub.Quota); e != nil {
			t.Fatal(e)
		}
		if e = a.Store.saveSource(sub); e != nil {
			t.Fatal(e)
		}
	}
	cases := []struct {
		name            string
		used, threshold int
		stale, paid     bool
		want            string
	}{
		{"subscription before higher priority paid", 50, 5, false, true, sub.ID},
		{"boundary skips subscription", 95, 5, false, true, paid.ID},
		{"threshold change admits subscription", 90, 5, false, true, sub.ID},
		{"threshold change skips subscription", 90, 20, false, true, paid.ID},
		{"disabled paid fallback", 100, 5, false, false, ""},
		{"stale quota is not exhaustion", 100, 5, true, false, sub.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setQuota(tc.used, tc.stale)
			a.Config.AllowPaidFallback, a.Config.SubscriptionQuotaThreshold = tc.paid, tc.threshold
			w := lifecycleAdmin(a, "POST", "/admin/routes/"+route.ID+"/preview", encode(map[string]any{"model": "coding", "protocol": "responses"}), "")
			mustOperationsStatus(t, w, 200)
			var preview struct {
				Selected *struct {
					Source string `json:"source_id"`
				} `json:"selected_candidate"`
				Error string `json:"error"`
				Calls int    `json:"upstream_calls"`
			}
			if json.Unmarshal(w.Body.Bytes(), &preview) != nil {
				t.Fatal(w.Body.String())
			}
			if tc.want == "" {
				if preview.Selected != nil || preview.Error == "" {
					t.Fatal("blocked preview chose a candidate", w.Body.String())
				}
				reject := contractCall(a, "POST", "/v1/responses", `{"model":"coding","input":"hello","stream":true}`, "test-client-key")
				if reject.Code != 429 {
					t.Fatal("disabled fallback did not reject without calling upstream", reject.Code, reject.Body.String())
				}
			} else if preview.Selected == nil || preview.Selected.Source != tc.want {
				t.Fatal("preview disagreed with scheduling", w.Body.String())
			}
			if preview.Calls != 0 {
				t.Fatal("preview contacted provider")
			}
			// Evaluate the production request plan as well as the configuration-only preview.
			in := policyInput()
			in.ClientRaw = []byte(`{"model":"coding","input":"hello","stream":true}`)
			a.mu.Lock()
			selected, _, reasons, e := a.selectSourceExcluding(key, "coding", "responses", true, nil, in)
			a.mu.Unlock()
			if tc.want == "" {
				if e == nil {
					t.Fatal("request plan bypassed disabled fallback")
				}
			} else if e != nil || selected.ID != tc.want {
				t.Fatal("request and preview differed", selected.ID, e, encode(reasons))
			}
		})
	}
	// Fixed-source keys keep their explicit source even with global paid fallback disabled.
	a.Config.AllowPaidFallback = false
	selected, _, _, err := a.selectSource(ClientKey{SourceID: paid.ID}, "fixture-model", "responses", true)
	if err != nil || selected.ID != paid.ID {
		t.Fatal("global route policy changed fixed-source selection", err)
	}
}

func TestSubscriptionSchedulingRespectsRequestEligibilityAndBinding(t *testing.T) {
	sub := policyCandidate("sub", "model", "sub_account")
	sub.Source.Kind, sub.Source.Provider = "codex_subscription", "codex"
	paid := policyCandidate("paid", "model", "paid_account")
	paid.Member.Priority = -10
	in := policyInput()
	in.ClientRaw = []byte(`{"model":"coding","input":"hello","stream":true}`)
	in.SubscriptionFirst, in.AllowPaidFallback = true, true
	var state PolicyState
	result := mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{sub, paid}, policyTime())
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != sub.Source.ID {
		t.Fatal("paid priority defeated subscription preference")
	}
	// A subscription unable to fit the actual request must not suppress an eligible API.
	tiny := int64(1)
	sub.Model.ContextLimit = &tiny
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{sub, paid}, policyTime())
	if len(result.Candidates) != 1 || result.Candidates[0].Source.ID != paid.Source.ID {
		t.Fatal("ineligible subscription prevented fallback")
	}
	sub.Model.ContextLimit = paid.Model.ContextLimit
	sub.ExcludedReason = "权威前序响应绑定到另一来源或模型"
	in.AuthoritativeBinding = true
	result = mustPolicyFilter(t, &state, RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{sub, paid}, policyTime())
	if result.Candidates[0].Source.ID != paid.Source.ID {
		t.Fatal("subscription preference diverted an authoritative binding")
	}
	in.AllowPaidFallback = false
	result, err := state.Filter(RoutePolicy{}, "priority", in, []RoutingPolicyCandidate{sub, paid}, policyTime())
	if err == nil || len(result.Candidates) != 0 || !strings.Contains(encode(result.Reasons), "关闭付费") {
		t.Fatal("binding bypassed paid authorization")
	}
}

func TestSubscriptionQuotaThresholdUsesOnlyCurrentScopedObservations(t *testing.T) {
	src := Source{Kind: "codex_subscription", ID: "sub", AccountID: "account", Generation: 1, AccountGeneration: 1}
	now := time.Now().UTC()
	baseline, err := parseCodexQuota([]byte(quotaWire(now, 95)), src, now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*codexQuotaSnapshot)
		want   bool
	}{
		{"current account window", func(q *codexQuotaSnapshot) {}, true},
		{"unknown snapshot", func(q *codexQuotaSnapshot) { q.Status = "unknown" }, false},
		{"expired snapshot", func(q *codexQuotaSnapshot) { q.ExpiresAt = timePointer(now.Add(-time.Second)) }, false},
		{"old source generation", func(q *codexQuotaSnapshot) { q.SourceGeneration++ }, false},
		{"old account generation", func(q *codexQuotaSnapshot) { q.AccountGeneration++ }, false},
		{"different account", func(q *codexQuotaSnapshot) { q.Scope.Account = "other" }, false},
		{"unknown window", func(q *codexQuotaSnapshot) { q.Windows[0].UsedPercent = nil }, false},
		{"different model", func(q *codexQuotaSnapshot) { q.Windows[0].Scope.Model = "other" }, false},
		{"different operation", func(q *codexQuotaSnapshot) { q.Windows[0].Scope.Operation = "compact" }, false},
		{"named pool is not account quota", func(q *codexQuotaSnapshot) { q.Windows[0].AccountWide = false }, false},
		{"past reset", func(q *codexQuotaSnapshot) { q.Windows[0].ResetAt = timePointer(now.Add(-time.Second)) }, false},
		{"future observation", func(q *codexQuotaSnapshot) { q.ObservedAt = timePointer(now.Add(time.Hour)) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var q codexQuotaSnapshot
			if err := json.Unmarshal([]byte(encode(baseline)), &q); err != nil {
				t.Fatal(err)
			}
			tc.change(&q)
			current := src
			if err := json.Unmarshal([]byte(encode(q)), &current.Quota); err != nil {
				t.Fatal(err)
			}
			if blocked, reason := quotaBelowThreshold(current, "model", "generate", now, 5); blocked != tc.want {
				t.Fatal(blocked, reason)
			}
			if blocked, _ := quotaDispatchBlocked(current, "model", "generate", now); blocked {
				t.Fatal("direct-source exhaustion check inherited route threshold")
			}
		})
	}
}
