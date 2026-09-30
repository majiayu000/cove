package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptanceRateWindowSettlementAndRetry(t *testing.T) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	rpm, tpm := 2, 100
	key := ClientKey{ID: "key", Limits: Limits{RPM: &rpm, TPM: &tpm}}
	a := &App{rateBuckets: map[string]*rateBucket{}, tpmWindows: map[string]map[string]tokenReservation{}}
	for i := 0; i < 2; i++ {
		if ok, _ := a.consumeRPM(key, now); !ok {
			t.Fatal("initial RPM capacity rejected")
		}
	}
	if ok, retry := a.consumeRPM(key, now.Add(29*time.Second)); ok || retry != 1 {
		t.Fatalf("RPM refill boundary: %v %d", ok, retry)
	}
	if ok, _ := a.consumeRPM(key, now.Add(30*time.Second)); !ok {
		t.Fatal("RPM capacity did not refill")
	}
	a.reserveTPM(key, "early", 10, now)
	a.reserveTPM(key, "later", 90, now.Add(10*time.Second))
	if ok, retry := a.checkTPM(key, 50, now.Add(20*time.Second)); ok || retry != 50 {
		t.Fatalf("TPM must wait until sufficient capacity, not first expiry: %v %d", ok, retry)
	}
	if ok, _ := a.checkTPM(key, 50, now.Add(60*time.Second)); ok {
		t.Fatal("partial expiry released insufficient capacity")
	}
	if ok, _ := a.checkTPM(key, 50, now.Add(70*time.Second)); !ok {
		t.Fatal("expired reservations were retained")
	}
	a.reserveTPM(key, "settled", 80, now)
	input, output := int64(5), int64(2)
	a.settleTPM(key, Record{ID: "settled", Submission: "sent", Usage: Usage{Input: &input, Output: &output}})
	if ok, _ := a.checkTPM(key, 93, now); !ok {
		t.Fatal("actual usage did not replace estimate")
	}
	a.reserveTPM(key, "unknown", 30, now)
	a.settleTPM(key, Record{ID: "unknown", Submission: "sent"})
	if ok, _ := a.checkTPM(key, 64, now); ok {
		t.Fatal("unknown usage incorrectly refunded")
	}
	a.settleTPM(key, Record{ID: "unknown", Submission: "not_sent"})
	if ok, _ := a.checkTPM(key, 93, now); !ok {
		t.Fatal("unsent request not refunded")
	}
	if ok, retry := a.checkTPM(key, 101, now); ok || retry != 0 {
		t.Fatalf("impossible request must not advertise a retry window: %v %d", ok, retry)
	}
}

func TestAcceptanceHTTPTokenWindow(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, contractJSON)
	})
	key, err := f.a.Store.keyByID("key")
	if err != nil {
		t.Fatal(err)
	}
	limit := 200
	key.Limits.TPM = &limit
	if _, err = f.a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"fixture-model","input":"Hi","max_output_tokens":150}`
	for i := 0; i < 2; i++ {
		resp, b := f.request("POST", "/v1/responses", body, false)
		if resp.StatusCode != 200 {
			t.Fatalf("settled estimates not replaced: %d %s", resp.StatusCode, b)
		}
	}
	f.a.mu.Lock()
	f.a.reserveTPM(key, "unknown-pending", 100, time.Now())
	f.a.mu.Unlock()
	resp, b := f.request("POST", "/v1/responses", body, false)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || calls.Load() != 2 {
		t.Fatalf("HTTP TPM admission: %d %s calls=%d", resp.StatusCode, b, calls.Load())
	}
	var envelope map[string]any
	if json.Unmarshal(b, &envelope) != nil {
		t.Fatal("denial is not JSON")
	}
}

func TestAcceptanceHTTPQueueBoundCancellationAndRevalidation(t *testing.T) {
	for _, scenario := range []string{"cancel_and_revoke", "timeout", "route_changed", "disabled_default"} {
		t.Run(scenario, func(t *testing.T) {
			entered, release := make(chan struct{}, 1), make(chan struct{})
			defer close(release)
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, contractJSON)
			})
			models, err := f.a.Store.models("source")
			if err != nil || len(models) != 1 {
				t.Fatalf("models: %v", err)
			}
			one := 1
			route := Route{ID: "queue-route", Enabled: true, Version: 1, Strategy: "priority", MaxAttempts: 1, MaxConcurrent: &one, QueueLimit: 2, QueueTimeoutMS: 3000, Members: []RouteMember{{ModelID: models[0].ID, Weight: 1}}}
			if scenario == "timeout" {
				route.QueueTimeoutMS = 80
			}
			if scenario == "disabled_default" {
				route.QueueLimit = 0
			}
			if _, err = f.a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?)", route.ID, encode(route)); err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", "queue-model", route.ID, encode(Alias{PublicModel: "queue-model", RouteID: route.ID, Version: 1})); err != nil {
				t.Fatal(err)
			}
			key, err := f.a.Store.keyByID("key")
			if err != nil {
				t.Fatal(err)
			}
			key.SourceID = ""
			key.RouteID = route.ID
			rpm := 10
			key.Limits.RPM = &rpm
			if _, err = f.a.Store.DB.Exec("UPDATE client_keys SET source_id=NULL,route_id=?,data=? WHERE id=?", route.ID, encode(key), key.ID); err != nil {
				t.Fatal(err)
			}
			send := func(ctx context.Context) <-chan int {
				done := make(chan int, 1)
				go func() {
					req, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"queue-model","input":"Hi"}`))
					req.Header.Set("Authorization", "Bearer "+f.key)
					req.Header.Set("Content-Type", "application/json")
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						done <- 0
						return
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					done <- resp.StatusCode
				}()
				return done
			}
			activeCtx, cancelActive := context.WithCancel(context.Background())
			defer cancelActive()
			active := send(activeCtx)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("active request did not dispatch")
			}
			queuedCtx, cancelQueued := context.WithCancel(context.Background())
			defer cancelQueued()
			queued := send(queuedCtx)
			awaitCount := func(count int) {
				t.Helper()
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					f.a.mu.Lock()
					n := len(f.a.queued)
					f.a.mu.Unlock()
					if n == count {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Fatalf("queue count did not reach %d", count)
			}
			status := func(done <-chan int, want int) {
				t.Helper()
				select {
				case got := <-done:
					if got != want {
						t.Fatalf("HTTP status=%d want=%d", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("request did not finish")
				}
			}
			if scenario == "disabled_default" {
				status(queued, 429)
			} else {
				awaitCount(1)
				f.a.mu.Lock()
				tokens := f.a.rateBuckets[key.ID].Tokens
				accountSlots := 0
				for _, n := range f.a.accountActive {
					accountSlots += n
				}
				f.a.mu.Unlock()
				if tokens != 9 || accountSlots != 1 {
					t.Fatalf("queued request consumed admission: rpm=%v slots=%d", tokens, accountSlots)
				}
				switch scenario {
				case "timeout":
					status(queued, 429)
					awaitCount(0)
				case "route_changed":
					route.Version++
					if _, err = f.a.Store.DB.Exec("UPDATE routes SET data=? WHERE id=?", encode(route), route.ID); err != nil {
						t.Fatal(err)
					}
					f.a.mu.Lock()
					f.a.signalAdmission()
					f.a.mu.Unlock()
					status(queued, 409)
					awaitCount(0)
				case "cancel_and_revoke":
					second := send(context.Background())
					awaitCount(2)
					status(send(context.Background()), 429)
					cancelQueued()
					status(queued, 0)
					awaitCount(1)
					key.Revoked = true
					if _, err = f.a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
						t.Fatal(err)
					}
					f.a.mu.Lock()
					f.a.signalAdmission()
					f.a.mu.Unlock()
					status(second, 401)
					awaitCount(0)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("queued/rejected requests dispatched: %d", calls.Load())
			}
			cancelActive()
			status(active, 0)
		})
	}
}

func TestAcceptanceBoundedAttemptsNoUnknownReplay(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		maximum, calls, status int
	}{
		{"three_safe_attempts", 3, 3, 200}, {"maximum_two", 2, 2, 502}, {"unknown_submission", 3, 1, 502}, {"provider_429", 3, 1, 429}, {"total_deadline", 3, 1, 504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				n := int(calls.Add(1))
				if tc.name == "unknown_submission" {
					return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("synthetic interrupted read")}
				}
				if tc.name == "provider_429" {
					response := contractResponse(`{"error":{"message":"synthetic rate limit"}}`, "application/json")
					response.StatusCode = 429
					return response, nil
				}
				if tc.name == "total_deadline" {
					<-r.Context().Done()
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: r.Context().Err()}
				}
				if n <= 2 {
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("synthetic connection refused before send")}
				}
				return contractResponse(contractJSON, "application/json"), nil
			})
			if tc.name == "total_deadline" {
				a.Config.TotalTimeout = 1
			}
			src, err := a.Store.source("source")
			if err != nil {
				t.Fatal(err)
			}
			src.Price = &Price{Currency: "USD", Input: "1", Output: "1", AsOf: time.Now().UTC().Format(time.RFC3339)}
			route := Route{ID: "bounded-attempt-route", Enabled: true, Version: 1, Strategy: "priority", MaxAttempts: tc.maximum}
			for i := 0; i < 3; i++ {
				candidate := src
				candidate.ID = fmt.Sprintf("attempt-source-%d", i)
				candidate.BaseURL = fmt.Sprintf("https://attempt-%d.invalid/v1", i)
				candidate.AccountID = ""
				if err = a.Store.saveSource(candidate); err != nil {
					t.Fatal(err)
				}
				models, err := a.Store.models(candidate.ID)
				if err != nil || len(models) != 1 {
					t.Fatalf("models: %v", err)
				}
				route.Members = append(route.Members, RouteMember{ModelID: models[0].ID, Priority: i, Weight: 1})
			}
			if _, err = a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?)", route.ID, encode(route)); err != nil {
				t.Fatal(err)
			}
			if _, err = a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", "bounded-model", route.ID, encode(Alias{PublicModel: "bounded-model", RouteID: route.ID, Version: 1})); err != nil {
				t.Fatal(err)
			}
			key, err := a.Store.keyByID("key")
			if err != nil {
				t.Fatal(err)
			}
			key.SourceID = ""
			key.RouteID = route.ID
			if _, err = a.Store.DB.Exec("UPDATE client_keys SET source_id=NULL,route_id=?,data=? WHERE id=?", route.ID, encode(key), key.ID); err != nil {
				t.Fatal(err)
			}
			budget := lifecycleAdmin(a, "POST", "/admin/budgets", `{"name":"Attempt budget","scope":{"kind":"instance"},"currency":"USD","amount_limit":"10","mode":"soft","period":{"kind":"calendar_day","timezone":"UTC"}}`, "")
			if budget.Code != 201 {
				t.Fatalf("budget: %d %s", budget.Code, budget.Body.String())
			}
			began := time.Now()
			w := contractCall(a, "POST", "/v1/responses", `{"model":"bounded-model","input":"Hi","max_output_tokens":16}`, "test-client-key")
			if w.Code != tc.status || int(calls.Load()) != tc.calls {
				t.Fatalf("status=%d calls=%d want=%d/%d body=%s", w.Code, calls.Load(), tc.status, tc.calls, w.Body.String())
			}
			if tc.name == "total_deadline" && time.Since(began) > 2*time.Second {
				t.Fatal("attempts exceeded shared total deadline")
			}
			var requests, attempts, reservations int
			for query, dest := range map[string]*int{"SELECT count(*) FROM requests": &requests, "SELECT count(*) FROM attempts": &attempts, "SELECT count(*) FROM reservations": &reservations} {
				if err = a.Store.DB.QueryRow(query).Scan(dest); err != nil {
					t.Fatal(err)
				}
			}
			if requests != 1 || attempts != tc.calls || reservations != 1 {
				t.Fatalf("request/attempt/budget counts=%d/%d/%d", requests, attempts, reservations)
			}
			if tc.name == "three_safe_attempts" {
				var reserved, settled string
				if err = a.Store.DB.QueryRow("SELECT reserved_amount,settled_amount FROM reservations").Scan(&reserved, &settled); err != nil {
					t.Fatal(err)
				}
				if reserved != "0.000000000000" || settled == "0.000000000000" {
					t.Fatalf("safe retry ledger was duplicated or not settled: reserved=%s settled=%s", reserved, settled)
				}
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if len(a.slots) != 0 || a.keyActive[key.ID] != 0 || a.routeActive[route.ID] != 0 {
				t.Fatal("attempt capacity leaked")
			}
			for _, n := range a.accountActive {
				if n != 0 {
					t.Fatal("account capacity leaked")
				}
			}
		})
	}
}
