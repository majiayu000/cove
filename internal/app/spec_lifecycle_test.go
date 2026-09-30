package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func lifecycleAdmin(a *App, method, path, body, action string) *httptest.ResponseRecorder {
	r := contractRequest(method, path, body, "test-session")
	if action != "" {
		r.Header.Set("X-Cove-Action-Id", action)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func TestSpecAccountPublicationAndGeneration(t *testing.T) {
	a := contractApp(t, nil)
	w := lifecycleAdmin(a, "POST", "/admin/accounts", `{"provider":"openai_compatible","auth_type":"api_key","name":"Independent"}`, "")
	var acct Account
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &acct) != nil {
		t.Fatalf("account create %d %s", w.Code, w.Body.String())
	}
	w = lifecycleAdmin(a, "POST", "/admin/accounts/"+acct.ID+"/credential", encode(map[string]any{"version": acct.Version, "secret": "SYNTHETIC_ACCOUNT_SECRET"}), "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "SYNTHETIC_ACCOUNT_SECRET") {
		t.Fatalf("publication %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &acct)
	w = lifecycleAdmin(a, "POST", "/admin/sources", encode(map[string]any{"kind": "api_key", "name": "Linked", "account_id": acct.ID, "provider": "openai_compatible", "base_url": "https://api.example.test/v1", "models": []string{"m"}}), "")
	var src Source
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &src) != nil {
		t.Fatalf("link %d %s", w.Code, w.Body.String())
	}
	src, e := a.Store.source(src.ID)
	if e != nil {
		t.Fatal(e)
	}
	src.Verification.Status = "passed"
	src.Continuation = true
	if e := a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	w = lifecycleAdmin(a, "PATCH", "/admin/accounts/"+acct.ID, encode(map[string]any{"version": acct.Version, "name": "Renamed"}), "")
	var named Account
	json.Unmarshal(w.Body.Bytes(), &named)
	if w.Code != 200 || named.Generation != acct.Generation {
		t.Fatal("rename altered credential generation")
	}
	w = lifecycleAdmin(a, "POST", "/admin/accounts/"+acct.ID+"/credential", encode(map[string]any{"version": named.Version, "secret": "SYNTHETIC_ACCOUNT_NEW"}), "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	current, e := a.Store.source(src.ID)
	if e != nil || current.AccountGeneration != acct.Generation+1 || current.Verification.Status != "stale" || current.Continuation {
		t.Fatalf("stale account evidence reused: %+v %v", current, e)
	}
	w = lifecycleAdmin(a, "DELETE", "/admin/accounts/"+acct.ID, encode(map[string]any{"version": named.Version + 1}), "")
	if w.Code != 409 {
		t.Fatal("referenced account deleted")
	}
}

func TestSpecAdminActionSecretLossAndOrdinaryCAS(t *testing.T) {
	a := contractApp(t, nil)
	body := `{"name":"Once","target":{"kind":"source","id":"source"}}`
	w := lifecycleAdmin(a, "POST", "/admin/client-keys", body, "synthetic-action-1")
	var created struct {
		Key    ClientKey `json:"key"`
		Secret string    `json:"secret"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Secret == "" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = lifecycleAdmin(a, "POST", "/admin/client-keys", body, "synthetic-action-1")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "secret_not_recoverable") || strings.Contains(w.Body.String(), created.Secret) {
		t.Fatal("secret creation replayed", w.Code)
	}
	var raw string
	if e := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", "action_"+digest("synthetic-action-1")).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, created.Secret) || strings.Contains(raw, "\"response\"") {
		t.Fatal("one-time key persisted in action")
	}
	patch := encode(map[string]any{"version": created.Key.Version, "name": "Changed"})
	w = lifecycleAdmin(a, "PATCH", "/admin/client-keys/"+created.Key.ID, patch, "ordinary-action")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = lifecycleAdmin(a, "PATCH", "/admin/client-keys/"+created.Key.ID, patch, "ordinary-action")
	if w.Code != 200 || w.Header().Get("X-Cove-Action-Replayed") != "true" {
		t.Fatal("ordinary replay lost result")
	}
	w = lifecycleAdmin(a, "PATCH", "/admin/client-keys/"+created.Key.ID, `{"version":2,"name":"Other"}`, "ordinary-action")
	if w.Code != 409 {
		t.Fatal("different action body accepted")
	}
}

func TestSpecPriceImmutableHistory(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(contractJSON, "application/json"), nil
	})
	models, e := a.Store.models("source")
	if e != nil || len(models) != 1 {
		t.Fatal(e)
	}
	create := func(at time.Time, rate string) PriceVersion {
		t.Helper()
		w := lifecycleAdmin(a, "POST", "/admin/prices", encode(map[string]any{"model_id": models[0].ID, "currency": "USD", "effective_at": at.UTC(), "units": []PriceUnit{{"input_token", rate, "1000000"}, {"output_token", "0", "1"}}, "provenance": map[string]any{"kind": "manual", "observed_at": at.UTC()}}), "")
		var v PriceVersion
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return v
	}
	first := create(time.Now().Add(-time.Hour), "1")
	if w := contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	second := create(time.Now().Add(-time.Minute), "2")
	if w := contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	records := waitRecords(t, a, 2)
	if records[1].Price.ID != first.ID || records[0].Price.ID != second.ID || records[1].Cost == nil || *records[1].Cost != "0.000012000000" || *records[0].Cost != "0.000024000000" {
		t.Fatalf("historical prices changed: %+v", records)
	}
}

func TestSpecRuntimeLimitSnapshotAndCAS(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return contractResponse(contractJSON, "application/json"), nil
	})
	results := make(chan int, 2)
	go func() { results <- contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key").Code }()
	<-entered
	w := lifecycleAdmin(a, "PATCH", "/admin/settings", `{"version":1,"changes":{"max_concurrent":2,"total_timeout_seconds":1}}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	go func() { results <- contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key").Code }()
	<-entered
	w = lifecycleAdmin(a, "PATCH", "/admin/settings", `{"version":2,"changes":{"max_concurrent":1}}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := contractCall(a, "POST", "/v1/responses", contractBody, "test-client-key"); w.Code != 429 {
		t.Fatal("lower limit admitted third request")
	}
	w = lifecycleAdmin(a, "PATCH", "/admin/settings", `{"version":1,"changes":{"max_concurrent":3}}`, "")
	if w.Code != 409 {
		t.Fatal("settings stale CAS accepted")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if code := <-results; code != 200 {
			t.Fatal("limit edit interrupted admitted request", code)
		}
	}
	a.mu.Lock()
	active := len(a.slots)
	a.mu.Unlock()
	if active != 0 || calls.Load() != 2 {
		t.Fatal("resized slots leaked")
	}
	records := waitRecords(t, a, 2)
	if records[0].ConfigVersion == records[1].ConfigVersion {
		t.Fatal("configuration snapshots not recorded")
	}
}

func seedNativeOpaque(t *testing.T, a *App, sourceID, keyID, model, opaque string) {
	t.Helper()
	src, e := a.Store.source(sourceID)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	r := Record{ID: id("history"), SourceID: src.ID, SourceName: src.Name, KeyID: keyID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, Model: model, SentModel: model, Started: now.Add(-time.Second), Ended: &now, Status: "succeeded", Origin: "verification", Submission: "possible", Operation: "compact"}
	if e = a.Store.record(r); e != nil {
		t.Fatal(e)
	}
	if e = a.Store.bind(r, opaqueBinding(opaque, src)); e != nil {
		t.Fatal(e)
	}
}
