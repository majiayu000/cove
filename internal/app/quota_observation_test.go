package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quotaTestApp(t *testing.T, transport contractTransport) (*App, Source, *quotaObservationState) {
	t.Helper()
	a := contractApp(t, transport)
	a.Config.Codex.ClientVersion = "0.158.0" // Explicit synthetic declaration, never inferred from a local install.
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.Kind = "codex_subscription"
	src.Provider = "codex"
	src.BaseURL = "https://chatgpt.com/backend-api/codex"
	src.AccountID = id("quota-account")
	src.AuthStatus = "logged_in"
	src.Configured = true
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	src, err = a.Store.source(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := Credential{Access: "SYNTHETIC_QUOTA_ACCESS", Refresh: "SYNTHETIC_QUOTA_REFRESH", Account: "synthetic-account", Subject: "synthetic-subject", Expires: time.Now().Add(time.Hour)}
	if err = a.Secrets.Put(src.CredentialRef, encode(c)); err != nil {
		t.Fatal(err)
	}
	state := &quotaObservationState{RefreshRejected: func(ctx context.Context, src *Source, rejected string) (Credential, error) {
		return a.subscriptionCredential(ctx, src, rejected)
	}}
	if _, err = a.Store.DB.Exec(QuotaObservationSchema); err != nil {
		t.Fatal(err)
	}
	return a, src, state
}

func TestSpecQuotaExpiredReadsPreserveValuesWithoutBlocking(t *testing.T) {
	a, src, _ := quotaTestApp(t, nil)
	now := time.Now().UTC()
	q, err := parseCodexQuota([]byte(`{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":1999999999}}}`), src, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	q.ExpiresAt = timePointer(now.Add(-time.Second))
	raw, _ := json.Marshal(q)
	if json.Unmarshal(raw, &src.Quota) != nil {
		t.Fatal("quota fixture")
	}
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	current, err := a.Store.source(src.ID)
	if err != nil || current.Quota["status"] != "stale" || len(quotaSnapshot(current).Windows) == 0 {
		t.Fatal("expired values were labelled current or erased", err)
	}
	if blocked, _ := quotaDispatchBlocked(current, "synthetic-model", "generate", now); blocked {
		t.Fatal("expired observation blocked dispatch")
	}
	sources, err := a.Store.sources()
	if err != nil || len(sources) != 1 || sources[0].Quota["status"] != "stale" {
		t.Fatal("source list did not mark expiration", err)
	}
	var persisted string
	if err = a.Store.DB.QueryRow("SELECT data FROM sources WHERE id=?", src.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	var original Source
	if json.Unmarshal([]byte(persisted), &original) != nil || original.Quota["status"] != "available" {
		t.Fatal("read changed persisted observation")
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestSpecQuotaAccountReadsUseCurrentGenerationAndTTL(t *testing.T) {
	a, src, _ := quotaTestApp(t, nil)
	now := time.Now().UTC()
	q, err := parseCodexQuota([]byte(quotaWire(now, 50)), src, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, want                 string
		expired, changedGeneration bool
	}{{"fresh", "available", false, false}, {"expired", "stale", true, false}, {"old-generation", "stale", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			copy := q
			if tc.expired {
				copy.ExpiresAt = timePointer(now.Add(-time.Second))
			}
			if tc.changedGeneration {
				copy.AccountGeneration--
			}
			if _, err := a.Store.DB.Exec("UPDATE sources SET data=json_set(data,'$.quota',json(?)) WHERE id=?", encode(copy), src.ID); err != nil {
				t.Fatal(err)
			}
			list := lifecycleAdmin(a, "GET", "/admin/accounts", "", "")
			var envelope struct {
				Items []Account `json:"items"`
			}
			if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &envelope) != nil {
				t.Fatalf("account list disagreed with quota observation: %s", list.Body.String())
			}
			found := false
			for _, account := range envelope.Items {
				if account.ID == src.AccountID {
					found = account.QuotaStatus == tc.want
				}
			}
			if !found {
				t.Fatalf("bound account quota was absent or stale: %s", list.Body.String())
			}
			detail := lifecycleAdmin(a, "GET", "/admin/accounts/"+src.AccountID, "", "")
			var account Account
			if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &account) != nil || account.QuotaStatus != tc.want {
				t.Fatalf("account detail disagreed with quota observation: %s", detail.Body.String())
			}
		})
	}
}

func quotaStart(t *testing.T, a *App, state *quotaObservationState, src Source, kind string) *Operation {
	t.Helper()
	a.mu.Lock()
	op, err := a.startCodexObservationLocked(src, kind, state, time.Now().UTC())
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if op == nil {
		t.Fatal("unexpected cached observation")
	}
	return op
}
func quotaWait(t *testing.T, a *App, state *quotaObservationState, op *Operation) Operation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var raw string
		var current Operation
		if err := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", op.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal([]byte(raw), &current) != nil {
			t.Fatal("bad operation")
		}
		a.mu.Lock()
		active := false
		for _, f := range state.Flights {
			active = active || f.Operation.ID == op.ID
		}
		a.mu.Unlock()
		if current.State != "running" && !active {
			return current
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("observation did not finish")
	return Operation{}
}
func quotaWire(now time.Time, used int) string {
	return encode(map[string]any{"rate_limit": map[string]any{"allowed": used < 100, "limit_reached": used == 100, "primary_window": map[string]any{"used_percent": used, "limit_window_seconds": 3600, "reset_at": now.Add(time.Hour).Unix()}, "secondary_window": nil}, "additional_rate_limits": []any{map[string]any{"limit_name": "model pool", "metered_feature": "specific-model-feature", "rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": 95, "limit_window_seconds": 7200, "reset_after_seconds": 1200}}}}, "credits": map[string]any{"balance": "12.50", "has_credits": true, "unlimited": false}, "private_error": "SYNTHETIC_QUOTA_ACCESS"})
}

func TestSpecQuotaObservationGETScopeWindowsAndDispatch(t *testing.T) {
	now := time.Now().UTC()
	var gets atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		gets.Add(1)
		if r.Method != "GET" || r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Authorization") != "Bearer SYNTHETIC_QUOTA_ACCESS" || r.Header.Get("ChatGPT-Account-Id") != "synthetic-account" {
			t.Fatal("wrong read-only usage path/ownership")
		}
		return contractResponse(quotaWire(now, 100), "application/json"), nil
	})
	op := quotaStart(t, a, state, src, "quota")
	if result := quotaWait(t, a, state, op); result.State != "succeeded" {
		t.Fatal(result)
	}
	current, err := a.Store.source(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := quotaSnapshot(current)
	if q.Status != "available" || len(q.Windows) != 2 || q.Limits[0].Secondary != nil || q.Windows[0].Remaining == nil || *q.Windows[0].Remaining != 0 || q.Windows[1].ResetAt == nil || q.Credits == nil || q.Credits.Balance == nil || *q.Credits.Balance != "12.50" {
		t.Fatal("quota fields conflated/missing", encode(q))
	}
	if q.Windows[1].AccountWide || q.Windows[0].Unit != "percent" || q.Windows[0].ResetAt.Location() != time.UTC {
		t.Fatal("scope/unit/UTC mapping")
	}
	if blocked, _ := quotaDispatchBlocked(current, "any-model", "generate", time.Now().UTC()); !blocked {
		t.Fatal("fresh exhausted account window not blocked")
	}
	if blocked, _ := quotaDispatchBlocked(current, "any-model", "generate", q.ExpiresAt.Add(time.Second)); blocked {
		t.Fatal("expired snapshot permanently blocked")
	}
	current.AccountGeneration++
	if blocked, _ := quotaDispatchBlocked(current, "any-model", "generate", time.Now()); blocked {
		t.Fatal("old account generation blocked new account")
	}
	var persisted string
	if err = a.Store.DB.QueryRow("SELECT value_json FROM quota_snapshots WHERE account_id=?", src.AccountID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "SYNTHETIC_QUOTA_ACCESS") || strings.Contains(persisted, "synthetic-account") || gets.Load() != 1 {
		t.Fatal("provider credentials/identity leaked or extra calls")
	}
	var requests int
	if err = a.Store.DB.QueryRow("SELECT count(*) FROM requests").Scan(&requests); err != nil || requests != 0 {
		t.Fatal("GET observation created a chargeable request")
	}
}

func TestSpecQuotaObservationSharedFlightAndFailurePreservesWindows(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return contractResponse(quotaWire(time.Now().UTC(), 85), "application/json"), nil
		}
		resp := contractResponse(`{"error":"SYNTHETIC_PROVIDER_SECRET"}`, "application/json")
		resp.StatusCode = 503
		return resp, nil
	})
	other := src
	other.ID = "same-account-source"
	other.Name = "Same account"
	if err := a.Store.saveSource(other); err != nil {
		t.Fatal(err)
	}
	other, _ = a.Store.source(other.ID)
	first := quotaStart(t, a, state, src, "quota")
	<-started
	second := quotaStart(t, a, state, other, "quota")
	if first.ID != second.ID || calls.Load() != 1 {
		t.Fatal("same account quota flight duplicated")
	}
	once.Do(func() { close(release) })
	quotaWait(t, a, state, first)
	current, _ := a.Store.source(src.ID)
	other, _ = a.Store.source(other.ID)
	old := quotaSnapshot(current)
	if quotaSnapshot(other).ObservedAt == nil {
		t.Fatal("shared account result missing from second source")
	}
	a.mu.Lock()
	op, err := a.startCodexObservationLocked(current, "quota", state, time.Now())
	a.mu.Unlock()
	if err != nil || op != nil {
		t.Fatal("manual refresh bypassed read cooldown", err)
	}
	a.mu.Lock()
	op, err = a.startCodexObservationLocked(current, "quota", state, old.NextAttempt.Add(time.Second))
	a.mu.Unlock()
	if err != nil || op == nil {
		t.Fatal(err)
	}
	result := quotaWait(t, a, state, op)
	current, _ = a.Store.source(src.ID)
	q := quotaSnapshot(current)
	if result.State != "failed" || q.Status != "stale" || q.ObservedAt == nil || !q.ObservedAt.Equal(*old.ObservedAt) || len(q.Windows) != len(old.Windows) || *q.Windows[0].UsedPercent != 85 || q.Failures != 1 || strings.Contains(encode(q), "SYNTHETIC_PROVIDER_SECRET") {
		t.Fatal("failure erased/leaked old observation", encode(q), result)
	}
	if blocked, _ := quotaDispatchBlocked(current, "model", "generate", time.Now()); blocked {
		t.Fatal("stale observation blocked")
	}
}

func TestSpecQuotaObservation401SharedForcedRefresh(t *testing.T) {
	var refreshes, oldGets, newGets atomic.Int32
	twoOld, refreshStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/token" {
			refreshes.Add(1)
			close(refreshStarted)
			<-release
			return contractResponse(encode(map[string]any{"access_token": "SYNTHETIC_FRESH_ACCESS", "refresh_token": "SYNTHETIC_ROTATED_REFRESH", "id_token": fakeJWT("synthetic-account", "synthetic-subject"), "expires_in": 3600}), "application/json"), nil
		}
		if r.Method != "GET" {
			t.Fatal("paid request replayed by read retry")
		}
		if r.Header.Get("Authorization") == "Bearer SYNTHETIC_QUOTA_ACCESS" {
			if oldGets.Add(1) == 2 {
				close(twoOld)
			}
			// Both reads must capture the old credential before either 401 starts
			// the shared refresh; otherwise the second legitimately joins it first.
			select {
			case <-twoOld:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			resp := contractResponse(`{"error":"synthetic401"}`, "application/json")
			resp.StatusCode = 401
			return resp, nil
		}
		newGets.Add(1)
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC_FRESH_ACCESS" {
			t.Fatal("stale token retry")
		}
		if strings.Contains(r.URL.Path, "/models") {
			return contractResponse(`{"models":[{"slug":"synthetic-model","context_window":8192}]}`, "application/json"), nil
		}
		return contractResponse(quotaWire(time.Now().UTC(), 40), "application/json"), nil
	})
	quota := quotaStart(t, a, state, src, "quota")
	models := quotaStart(t, a, state, src, "models")
	select {
	case <-twoOld:
	case <-time.After(time.Second):
		t.Fatal("concurrent reads did not reach401")
	}
	<-refreshStarted
	once.Do(func() { close(release) })
	if q := quotaWait(t, a, state, quota); q.State != "succeeded" {
		t.Fatal(q)
	}
	if m := quotaWait(t, a, state, models); m.State != "succeeded" {
		t.Fatal(m)
	}
	if refreshes.Load() != 1 || oldGets.Load() != 2 || newGets.Load() != 2 {
		t.Fatal("refresh owner duplicated or GET retry count", refreshes.Load(), oldGets.Load(), newGets.Load())
	}
}

func TestSpecQuotaObservationCatalogETagUnknownLimitsAndNoCapabilityGrant(t *testing.T) {
	var calls atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/backend-api/codex/models" || r.URL.Query().Get("client_version") != "0.158.0" {
			t.Fatal("catalog path/version mismatch")
		}
		if calls.Add(1) == 1 {
			resp := contractResponse(`{"models":[{"slug":"observed-model","display_name":"Observed","context_window":8192,"max_context_window":16384,"input_modalities":["text","image"],"supported_reasoning_levels":[{"effort":"high","description":"private"}],"supported_in_api":true},{"slug":"unknown-model"}]}`, "application/json")
			resp.Header.Set("ETag", `"synthetic-etag"`)
			return resp, nil
		}
		if r.Header.Get("If-None-Match") != `"synthetic-etag"` {
			t.Fatal("ETag not preserved")
		}
		resp := contractResponse("", "application/json")
		resp.StatusCode = 304
		return resp, nil
	})
	if op := quotaWait(t, a, state, quotaStart(t, a, state, src, "models")); op.State != "succeeded" {
		t.Fatal(op)
	}
	var raw string
	if err := a.Store.DB.QueryRow("SELECT data FROM source_models WHERE source_id=? AND upstream_model='observed-model'", src.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m SourceModel
	var declared struct{ CodexModelMetadata }
	if json.Unmarshal([]byte(raw), &m) != nil || json.Unmarshal([]byte(raw), &declared) != nil {
		t.Fatal("invalid stored model")
	}
	if m.ContextLimit == nil || *m.ContextLimit != 8192 || m.MaxOutput != nil || m.Verification != "unverified" || declared.CodexCatalog == nil || *declared.CodexCatalog.MaxContextWindow != 16384 || len(declared.CodexCatalog.ReasoningLevels) != 1 {
		t.Fatal("metadata treated as call proof or invented output limit", raw)
	}
	if err := a.Store.DB.QueryRow("SELECT data FROM source_models WHERE source_id=? AND upstream_model='unknown-model'", src.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(raw), &m)
	if m.ContextLimit != nil || m.MaxOutput != nil {
		t.Fatal("missing model limits filled with defaults")
	}
	current, _ := a.Store.source(src.ID)
	if op := quotaWait(t, a, state, quotaStart(t, a, state, current, "models")); op.State != "succeeded" {
		t.Fatal(op)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected catalog GET count")
	}
}

func TestSpecQuotaObservationLateGenerationCancelAndBackupBarrier(t *testing.T) {
	for _, mode := range []string{"generation", "cancel", "backup"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
				close(started)
				<-release
				return contractResponse(quotaWire(time.Now().UTC(), 10), "application/json"), nil
			})
			a.EnableBackupGate()
			op := quotaStart(t, a, state, src, "quota")
			<-started
			a.mu.Lock()
			active := a.accountActive[src.AccountID]
			owners := a.backupOwners
			a.mu.Unlock()
			if active != 1 || owners < 1 {
				t.Fatal("account/backup owner not held")
			}
			switch mode {
			case "generation":
				a.mu.Lock()
				account, _, err := a.Store.account(src.AccountID)
				if err != nil {
					t.Fatal(err)
				}
				account.Generation++
				_, err = a.Store.DB.Exec("UPDATE accounts SET generation=?,data=? WHERE id=?", account.Generation, encode(account), account.ID)
				a.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				a.CloseAdmission()
				a.CancelAll()
			case "backup":
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				resume, err := a.quiesceBackup(ctx)
				cancel()
				if resume != nil {
					resume()
				}
				if err == nil {
					t.Fatal("backup ignored active observation owner")
				}
			}
			once.Do(func() { close(release) })
			result := quotaWait(t, a, state, op)
			var count int
			if err := a.Store.DB.QueryRow("SELECT count(*) FROM quota_snapshots").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if mode != "backup" && (count != 0 || result.State == "succeeded") {
				t.Fatal("late result published after ownership/cancel change", result, count)
			}
			a.mu.Lock()
			active = a.accountActive[src.AccountID]
			owners = a.backupOwners
			a.mu.Unlock()
			if active != 0 || owners != 0 {
				t.Fatal("observation lease leaked")
			}
		})
	}
}

func TestSpecQuotaObservationInvalidMissingAndHistoryBounds(t *testing.T) {
	now := time.Now().UTC()
	src := Source{ID: "local", AccountID: "local-account", Generation: 1, AccountGeneration: 2}
	q, err := parseCodexQuota([]byte(`{"rate_limit":{"primary_window":{"used_percent":150,"limit_window_seconds":-1,"reset_at":-10},"secondary_window":null},"credits":{"balance":"private-invalid"}}`), src, now)
	if err != nil || q.Windows[0].UsedPercent != nil || q.Windows[0].Remaining != nil || q.Windows[0].ResetAt != nil || q.Windows[0].Status != "unknown" || q.Limits[0].Secondary != nil || q.Credits.Balance != nil {
		t.Fatal("invalid/missing values treated as zero or balance", encode(q), err)
	}
	q, err = parseCodexQuota([]byte(`{"rate_limit":null}`), src, now)
	if err != nil || q.Status != "unknown" || len(q.Windows) != 0 || q.Limits[0].Primary != nil {
		t.Fatal(err, encode(q))
	}
	a, current, _ := quotaTestApp(t, nil)
	a.mu.Lock()
	for i := 0; i < 105; i++ {
		when := now.Add(time.Duration(i-105) * time.Minute)
		q, err = parseCodexQuota([]byte(quotaWire(when, 10)), current, when)
		if err == nil {
			err = a.publishCodexQuotaLocked(context.Background(), current, q, true, when)
		}
		if err != nil {
			a.mu.Unlock()
			t.Fatal(err)
		}
	}
	a.mu.Unlock()
	var count int
	if err = a.Store.DB.QueryRow("SELECT count(*) FROM quota_snapshots WHERE account_id=?", current.AccountID).Scan(&count); err != nil || count != 100 {
		t.Fatal("history not bounded", count, err)
	}
	w := httptest.NewRecorder()
	a.codexQuotaHistoryAPI(w, httptest.NewRequest("GET", "/quota-history", nil), current)
	if w.Code != 200 || strings.Count(w.Body.String(), `"adapter_version"`) != 100 {
		t.Fatal("history read failed", w.Code)
	}
	if _, err = a.Store.DB.Exec("INSERT INTO quota_snapshots(account_id,dimension,observed_at,account_generation,status,value_json) VALUES(?,?,?,?,?,?)", current.AccountID, "codex.windows", now.AddDate(0, 0, -40).Format(quotaSQLTime), current.AccountGeneration, "available", encode(q)); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	current.Enabled = false
	if err = a.Store.saveSource(current); err != nil {
		a.mu.Unlock()
		t.Fatal(err)
	}
	a.mu.Unlock()
	if err = a.quotaObservationTick(&quotaObservationState{}, now); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.DB.QueryRow("SELECT count(*) FROM quota_snapshots WHERE account_id=?", current.AccountID).Scan(&count); err != nil || count != 100 {
		t.Fatal("periodic cleanup retained expired account history", count, err)
	}
}

func TestSpecQuotaObservationUnsupportedAndResponseFailuresNoCalls(t *testing.T) {
	var calls atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("unexpected") })
	for _, mutate := range []func(*Source){func(s *Source) { s.Provider = "claude_subscription" }, func(s *Source) { s.BaseURL = "https://evil.example/backend-api/codex" }, func(s *Source) { s.Kind = "api_key" }} {
		copy := src
		mutate(&copy)
		a.mu.Lock()
		_, err := a.startCodexObservationLocked(copy, "quota", state, time.Now())
		a.mu.Unlock()
		var boundary *accountingError
		if !errors.As(err, &boundary) || boundary.Status != 422 {
			t.Fatal("unsupported provider inherited Codex quota", err)
		}
	}
	a.Config.Codex.ClientVersion = ""
	a.mu.Lock()
	_, err := a.startCodexObservationLocked(src, "models", state, time.Now())
	a.mu.Unlock()
	if err == nil || calls.Load() != 0 {
		t.Fatal("invented version or unsupported provider contacted")
	}
	for _, status := range []int{401, 403, 429, 500, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
				count.Add(1)
				resp := contractResponse(`{"error":"SYNTHETIC_PRIVATE_BODY"}`, "application/json")
				resp.StatusCode = status
				resp.Header.Set("Retry-After", "999999")
				resp.Header.Set("Location", "https://evil.example/private")
				return resp, nil
			})
			state.RefreshRejected = nil
			result := quotaWait(t, a, state, quotaStart(t, a, state, src, "quota"))
			current, _ := a.Store.source(src.ID)
			q := quotaSnapshot(current)
			if result.State != "failed" || q.Status != "unknown" || count.Load() != 1 || strings.Contains(encode(q), "SYNTHETIC_PRIVATE_BODY") {
				t.Fatal("invalid success/retry/error privacy", result, count.Load(), encode(q))
			}
			if status == 401 && current.AuthStatus != "needs_reauth" {
				t.Fatal("401 did not require reauth")
			}
			if status == 429 && q.NextAttempt.Sub(q.AttemptedAt) > 15*time.Minute {
				t.Fatal("unbounded Retry-After")
			}
		})
	}
}

func TestSpecQuotaObservationTTLUnknownPreservesAndBoundedInvalidBody(t *testing.T) {
	var calls atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			resp := contractResponse(quotaWire(time.Now().UTC(), 90), "application/json")
			resp.Header.Set("Cache-Control", "private, max-age=60")
			return resp, nil
		case 2:
			return contractResponse(`{"rate_limit":{"primary_window":null,"secondary_window":null}}`, "application/json"), nil
		case 3:
			return contractResponse(quotaWire(time.Now().UTC(), 20)+"TRAILING_PRIVATE_GARBAGE", "application/json"), nil
		default:
			return contractResponse(strings.Repeat("x", 8192), "application/json"), nil
		}
	})
	quotaWait(t, a, state, quotaStart(t, a, state, src, "quota"))
	current, _ := a.Store.source(src.ID)
	q := quotaSnapshot(current)
	old := q
	if q.ExpiresAt.Sub(*q.ObservedAt) != time.Minute || q.Windows[0].ExpiresAt.Sub(q.Windows[0].ObservedAt) != time.Minute {
		t.Fatal("provider TTL ignored", encode(q))
	}
	for step := 2; step <= 4; step++ {
		a.mu.Lock()
		if step == 4 {
			a.Config.MaxResponse = 1024
		}
		op, err := a.startCodexObservationLocked(current, "quota", state, q.NextAttempt.Add(time.Second))
		a.mu.Unlock()
		if err != nil || op == nil {
			t.Fatal(err)
		}
		result := quotaWait(t, a, state, op)
		current, _ = a.Store.source(src.ID)
		q = quotaSnapshot(current)
		if q.ObservedAt == nil || !q.ObservedAt.Equal(*old.ObservedAt) || len(q.Windows) == 0 || *q.Windows[0].UsedPercent != 90 {
			t.Fatal("missing/invalid reply erased old numeric observation", encode(q))
		}
		if step == 2 {
			if q.Status != "unknown" || result.State != "succeeded" || q.Limits[0].Primary != nil {
				t.Fatal("missing window treated as current 0%", result, encode(q))
			}
		} else if q.Status != "stale" || result.State != "failed" {
			t.Fatal("bad body became success", result, encode(q))
		}
		if blocked, _ := quotaDispatchBlocked(current, "model", "generate", time.Now()); blocked {
			t.Fatal("unknown/stale observation blocked generation")
		}
	}
}

func TestSpecQuotaObservationMissingPoolCannotAssertRecovery(t *testing.T) {
	var calls atomic.Int32
	a, src, state := quotaTestApp(t, func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return contractResponse(quotaWire(time.Now().UTC(), 90), "application/json"), nil
		}
		return contractResponse(encode(map[string]any{"rate_limit": map[string]any{"primary_window": map[string]any{"used_percent": 20, "reset_at": time.Now().Add(time.Hour).Unix()}}}), "application/json"), nil
	})
	quotaWait(t, a, state, quotaStart(t, a, state, src, "quota"))
	current, _ := a.Store.source(src.ID)
	q := quotaSnapshot(current)
	a.mu.Lock()
	op, err := a.startCodexObservationLocked(current, "quota", state, q.NextAttempt.Add(time.Second))
	a.mu.Unlock()
	if err != nil || op == nil {
		t.Fatal(err)
	}
	quotaWait(t, a, state, op)
	current, _ = a.Store.source(src.ID)
	q = quotaSnapshot(current)
	if len(q.Windows) != 2 || q.Windows[1].Status != "unknown" || q.Windows[1].UsedPercent != nil || q.Windows[1].Name != "model pool" {
		t.Fatal("omitted pool silently interpreted as recovered", encode(q))
	}
	if _, complete := notificationQuotaFacts(current, time.Now().UTC()); complete {
		t.Fatal("omitted known pool allowed alert recovery")
	}
}
