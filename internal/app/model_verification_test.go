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
				w := lifecycleAdmin(a, "POST", "/admin/models/"+models[0].ID+"/verify", encode(map[string]any{"expected_source_generation": src.Generation, "protocol": protocol, "features": []string{"text_json", "text_sse"}}), "")
				op := operationDone(t, a, w)
				if op.State != "succeeded" {
					t.Fatal(op.Error)
				}
				model, _ := a.Store.model(models[0].ID)
				expected := "failed"
				if fixture.text {
					expected = "passed"
				}
				if calls.Load() != 2 || model.Verification != expected || len(model.VerificationResults) != 2 {
					t.Fatalf("text verification claimed wrong evidence: %+v", model.VerificationResults)
				}
				for _, result := range model.VerificationResults {
					if result.Status != expected {
						t.Fatalf("%s verified without actual text: %s", result.Feature, result.Status)
					}
				}
				records := waitRecords(t, a, 2)
				for _, record := range records {
					if record.Origin != "verification" || record.Status != "succeeded" {
						t.Fatalf("verification or accounting contract changed: %+v", record)
					}
				}
			})
		}
	}
}
