package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCancelledReadinessDoesNotLatchStorageFault(t *testing.T) {
	a := contractApp(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := contractRequest("GET", "/readyz", "", "").WithContext(ctx)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 503 || a.storageFailed.Load() {
		t.Fatal("cancelled readiness probe must fail without latching a storage fault")
	}
	if contractCall(a, "GET", "/readyz", "", "").Code != 200 {
		t.Fatal("readiness failed to recover after a cancelled probe")
	}
}

func TestReadinessBoundaries(t *testing.T) {
	for _, state := range []string{"ready", "storage_fault", "database_closed", "secrets_fault", "stopped"} {
		t.Run(state, func(t *testing.T) {
			a := contractApp(t, nil)
			want := 503
			switch state {
			case "ready":
				want = 200
			case "storage_fault":
				a.storageFailed.Store(true)
			case "database_closed":
				if err := a.Store.DB.Close(); err != nil {
					t.Fatal(err)
				}
			case "secrets_fault":
				vault, err := NewFileSecrets(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				vault.fault = errors.New("private-vault-fault-detail")
				a.Secrets = vault
			case "stopped":
				assertDrained(t, a)
				if err := a.Store.DB.Close(); err != nil {
					t.Fatal(err)
				}
			}
			w := contractCall(a, "GET", "/readyz", "", "")
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("readiness status = %d, want %d; body = %s", w.Code, want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-vault") {
				t.Fatal("readiness exposed internal fault details")
			}
			if want == 503 && (w.Header().Get("X-Gateway-Request-Id") == "" || !strings.Contains(w.Body.String(), `"type":"gateway_error"`)) {
				t.Fatal("readiness failure lost the gateway error contract")
			}
			if live := contractCall(a, "GET", "/healthz", "", ""); live.Code != 200 {
				t.Fatalf("liveness failed in %s: %d", state, live.Code)
			}
			for _, path := range []string{"/healthz", "/readyz"} {
				r := contractRequest("GET", path, "", "")
				r.Host = "foreign.invalid"
				denied := httptest.NewRecorder()
				a.ServeHTTP(denied, r)
				if denied.Code != 403 {
					t.Fatalf("%s bypassed Host check in %s", path, state)
				}
			}
			if state == "stopped" {
				if contractCall(a, "POST", "/admin/session", `{}`, "").Code != 503 {
					t.Fatal("management work admitted after shutdown")
				}
				assertDrained(t, a)
			}
		})
	}
}
