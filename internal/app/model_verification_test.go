package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSpecLocalOperationInitialSnapshot(t *testing.T) {
	a := contractApp(t, nil)
	a.Config.DataDir = t.TempDir()
	for i := 0; i < 64; i++ {
		w := httptest.NewRecorder()
		a.startLocalOperation(w, httptest.NewRequest("POST", "/admin/models/model/verify", nil), "model_verification", func(context.Context, string) (any, error) {
			if i%2 == 0 {
				return map[string]any{"passed": true}, nil
			}
			return nil, errors.New("synthetic immediate failure")
		})
		var initial Operation
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.State != "running" || initial.Version != 1 || initial.Result != nil {
			t.Fatalf("initial operation response changed during completion: %s", w.Body.String())
		}
		a.ownedTasks.Wait()
		var raw string
		if err := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", initial.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var completed Operation
		if json.Unmarshal([]byte(raw), &completed) != nil || completed.Version != 2 || i%2 == 0 && completed.State != "succeeded" || i%2 == 1 && completed.State != "failed" {
			t.Fatalf("terminal operation state was lost: %s", raw)
		}
	}
}

func TestSpecModelVerificationExplicitAndScoped(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(`{"id":"verification-response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`, "application/json"), nil
	})
	a.Config.DataDir = t.TempDir()
	models, err := a.Store.models("source")
	if err != nil || len(models) != 1 {
		t.Fatal("model fixture")
	}
	model := models[0]
	src, _ := a.Store.source("source")
	w := lifecycleAdmin(a, "POST", "/admin/models/"+model.ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "features": []string{"text_json"}}), "")
	op := operationDone(t, a, w)
	if op.State != "succeeded" {
		t.Fatal(op.Error)
	}
	model, _ = a.Store.model(model.ID)
	if model.Verification != "passed" || calls.Load() != 1 || len(model.VerificationResults) != 1 || model.VerificationResults[0].Feature != "text_json" {
		t.Fatal("verification claimed too much or dispatched twice")
	}
	records := waitRecords(t, a, 1)
	if records[0].Origin != "verification" {
		t.Fatal("missing verification origin")
	}
	src.Generation++
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	model, _ = a.Store.model(model.ID)
	if model.Verification != "stale" {
		t.Fatal("old result survived changed source generation")
	}
}

func TestSpecModelVerificationRejectsUnavailableFeaturesAndEmptyOutput(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(contractJSON, "application/json"), nil
	})
	a.Config.DataDir = t.TempDir()
	models, _ := a.Store.models("source")
	src, _ := a.Store.source("source")
	w := lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "features": []string{"audio"}}), "")
	if w.Code != 422 || calls.Load() != 0 {
		t.Fatal("unsupported verification dispatched")
	}
	w = lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "features": []string{"text_json"}}), "")
	op := operationDone(t, a, w)
	if op.State != "succeeded" {
		t.Fatal(op.Error)
	}
	model, _ := a.Store.model(models[0].ID)
	if model.Verification != "failed" {
		t.Fatal("empty output falsely verified text")
	}
}

func TestSpecModelVerificationSubscriptionJSONDoesNotBecomeStream(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return contractResponse(compatDoneWire(), "text/event-stream"), nil
	})
	a.Config.DataDir = t.TempDir()
	src, _ := a.Store.source("source")
	src.Kind, src.AuthStatus = "codex_subscription", "logged_in"
	if err := a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "synthetic", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
		t.Fatal(err)
	}
	src, _ = a.Store.source("source")
	models, _ := a.Store.models("source")
	w := lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "protocol": "responses", "features": []string{"text_json"}}), "")
	a.ownedTasks.Wait()
	if w.Code != 422 || calls.Load() != 0 {
		t.Fatalf("JSON verification substituted a stream: HTTP %d, upstream calls %d", w.Code, calls.Load())
	}
	model, _ := a.Store.model(models[0].ID)
	if len(model.VerificationResults) != 0 {
		t.Fatal("unsupported JSON verification published capability evidence")
	}
}

func TestSpecModelVerificationSubscriptionRequiresActualText(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		for _, fixture := range []struct {
			name, wire string
			text       bool
		}{
			{"empty_completed", "event: response.completed\ndata: {\"response\":{\"id\":\"verification-empty\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", false},
			{"tool_only", "event: response.output_item.added\ndata: {\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_no_text\",\"name\":\"add\",\"arguments\":\"\"}}\n\n" + "event: response.function_call_arguments.delta\ndata: {\"output_index\":0,\"delta\":\"{}\"}\n\n" + "event: response.completed\ndata: {\"response\":{\"id\":\"verification-tool\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_no_text\",\"name\":\"add\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", false},
			{"completed_text_items", compatDoneWire(), true},
		} {
			t.Run(protocol+"/"+fixture.name, func(t *testing.T) {
				var calls atomic.Int32
				a := contractApp(t, func(r *http.Request) (*http.Response, error) {
					calls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if _, ok := body["input"].([]any); !ok {
						t.Fatal("subscription verification must use a message array")
					}
					return contractResponse(fixture.wire, "text/event-stream"), nil
				})
				a.Config.DataDir = t.TempDir()
				src, _ := a.Store.source("source")
				src.Kind, src.AuthStatus = "codex_subscription", "logged_in"
				src.AllowParameterAdjustment = true
				if err := a.Store.saveSource(src); err != nil {
					t.Fatal(err)
				}
				if err := a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "synthetic", Account: "account", Expires: time.Now().Add(time.Hour)})); err != nil {
					t.Fatal(err)
				}
				src, _ = a.Store.source("source")
				models, _ := a.Store.models("source")
				features := []string{"text_json", "text_sse"}
				if protocol == "responses" {
					features = []string{"text_sse"}
				}
				w := lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "protocol": protocol, "features": features}), "")
				op := operationDone(t, a, w)
				if op.State != "succeeded" {
					t.Fatal(op.Error)
				}
				model, _ := a.Store.model(models[0].ID)
				expected := "failed"
				if fixture.text {
					expected = "passed"
				}
				if calls.Load() != int32(len(features)) || model.Verification != expected || len(model.VerificationResults) != len(features) {
					t.Fatalf("text verification claimed wrong evidence: %+v", model.VerificationResults)
				}
				for _, result := range model.VerificationResults {
					if result.Status != expected {
						t.Fatalf("%s verified without actual text: %s", result.Feature, result.Status)
					}
				}
				records := waitRecords(t, a, len(features))
				for _, record := range records {
					if record.Origin != "verification" || record.Status != "succeeded" {
						t.Fatalf("verification or accounting contract changed: %+v", record)
					}
				}
			})
		}
	}
}

func TestSpecModelVerificationCredentialGeneration(t *testing.T) {
	for _, scenario := range []string{"expired_refresh", "credential_changed_after_dispatch", "mixed_generations", "source_changed_after_dispatch"} {
		t.Run(scenario, func(t *testing.T) {
			var refreshes, calls atomic.Int32
			var a *App
			a = contractApp(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/oauth/token" {
					refreshes.Add(1)
					return contractResponse(encode(map[string]any{"access_token": "fresh", "refresh_token": "rotated", "id_token": fakeJWT("account", "subject"), "expires_in": 3600}), "application/json"), nil
				}
				if calls.Add(1) == 1 && scenario != "expired_refresh" {
					a.mu.Lock()
					defer a.mu.Unlock()
					src, err := a.Store.source("source")
					if err != nil {
						return nil, err
					}
					switch scenario {
					case "credential_changed_after_dispatch":
						err = a.replaceCredential(&src, encode(Credential{Access: "replacement", Refresh: "replacement-refresh", Account: "account", Subject: "subject", Expires: time.Now().Add(time.Hour)}), true)
					case "mixed_generations":
						// Synthetic expiry between two probes: the first record keeps
						// its generation while the next probe refreshes normally.
						err = a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "expired", Refresh: "refresh-old", Account: "account", Subject: "subject", Expires: time.Now().Add(-time.Hour)}))
					case "source_changed_after_dispatch":
						src.Version++
						err = a.Store.saveSource(src)
					}
					if err != nil {
						return nil, err
					}
				}
				return contractResponse(compatDoneWire(), "text/event-stream"), nil
			})
			a.Config.DataDir = t.TempDir()
			expiredContractSource(t, a)
			src, err := a.Store.source("source")
			if err != nil {
				t.Fatal(err)
			}
			src.AllowParameterAdjustment = true
			if err = a.Store.saveSource(src); err != nil {
				t.Fatal(err)
			}
			if scenario == "mixed_generations" {
				if err = a.Secrets.Put(src.CredentialRef, encode(Credential{Access: "synthetic", Refresh: "refresh-old", Account: "account", Subject: "subject", Expires: time.Now().Add(time.Hour)})); err != nil {
					t.Fatal(err)
				}
			}
			models, err := a.Store.models(src.ID)
			if err != nil || len(models) != 1 {
				t.Fatal("missing model fixture", err)
			}
			features := []string{"text_sse"}
			if scenario == "mixed_generations" {
				features = []string{"text_json", "text_sse"}
			}
			w := lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "protocol": "chat_completions", "features": features}), "")
			op := operationDone(t, a, w)
			model, err := a.Store.model(models[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			records := waitRecords(t, a, len(features))
			if refreshes.Load() != 1 || calls.Load() != int32(len(features)) {
				t.Fatalf("unexpected replay: refreshes=%d, generations=%d", refreshes.Load(), calls.Load())
			}
			for _, record := range records {
				if record.Status != "succeeded" || record.DeliveryStatus != "completed" {
					t.Fatalf("probe did not complete: %s/%s", record.Status, record.DeliveryStatus)
				}
			}
			if scenario != "expired_refresh" {
				if op.State != "failed" || op.Error != "验证期间配置已改变，旧结果保留于请求记录，请重新验证" || len(model.VerificationResults) != 0 {
					t.Fatalf("stale proof was published: operation=%s/%s, results=%d", op.State, op.Error, len(model.VerificationResults))
				}
				return
			}
			current, err := a.Store.source(src.ID)
			if err != nil {
				t.Fatal(err)
			}
			if op.State != "succeeded" || model.Verification != "passed" || len(model.VerificationResults) != 1 {
				t.Fatalf("normal refresh discarded completed verification: %s/%s", op.State, op.Error)
			}
			result := model.VerificationResults[0]
			if result.AccountGeneration != current.AccountGeneration || result.AccountGeneration != records[0].AccountGeneration || result.AccountGeneration <= src.AccountGeneration || current.Generation != src.Generation || current.Version != src.Version {
				t.Fatal("verification did not retain the actual dispatch credential generation")
			}
		})
	}
}

func TestSpecSourceCreationHonorsEnabled(t *testing.T) {
	enabled, disabled := true, false
	for _, test := range []struct {
		name    string
		enabled *bool
		want    bool
	}{{"default", nil, true}, {"disabled_draft", &disabled, false}, {"enabled", &enabled, true}} {
		t.Run(test.name, func(t *testing.T) {
			a := contractApp(t, nil)
			src, err := a.Store.source("source")
			if err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"name": "Creation state fixture", "account_id": src.AccountID, "kind": src.Kind, "provider": src.Provider, "base_url": "https://upstream.invalid/v1", "models": []string{}}
			if test.enabled != nil {
				body["enabled"] = *test.enabled
			}
			w := lifecycleAdmin(a, "POST", "/admin/sources", encode(body), "")
			var created Source
			if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
				t.Fatalf("source creation failed: HTTP %d", w.Code)
			}
			persisted, err := a.Store.source(created.ID)
			if err != nil || created.Enabled != test.want || persisted.Enabled != test.want {
				t.Fatalf("source enabled state ignored: response=%t persisted=%t want=%t err=%v", created.Enabled, persisted.Enabled, test.want, err)
			}
		})
	}
}
