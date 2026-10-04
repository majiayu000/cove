package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAdminAuditProjectionPaginationAndErrors(t *testing.T) {
	a := contractApp(t, nil)
	w := lifecycleAdmin(a, "POST", "/admin/client-keys", `{"name":"audit-key","target":{"kind":"source","id":"source"}}`, "audit-key-create")
	if w.Code != 201 {
		t.Fatal("Key creation failed", w.Code)
	}
	var created struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		state := "succeeded"
		if i == 0 {
			state = "failed"
		}
		op := Operation{ID: fmt.Sprintf("audit_fixture_%d", i), Kind: "admin_action", State: state, Version: 2, CreatedAt: at, UpdatedAt: at,
			Result: actionResult{Path: "POST /admin/sources?credential=synthetic-query-secret", Hash: "synthetic-private-hash", Status: 200, Response: json.RawMessage(`{"secret":"synthetic-response-secret"}`)}}
		if _, err := a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES(?,?)", op.ID, encode(op)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		w = contractCall(a, "GET", "/admin/audit?limit=2&cursor="+url.QueryEscape(cursor), "", "test-session")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{created.Secret, "synthetic-query-secret", "synthetic-response-secret", "synthetic-private-hash", "body_hash"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("Audit leaked private action data")
			}
		}
		var page struct {
			Items  []struct{ ID, Path string }
			Cursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatal("Unbounded audit page")
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatal("Repeated audit entry")
			}
			seen[item.ID] = true
			if strings.Contains(item.Path, "?") {
				t.Fatal("Query string retained")
			}
		}
		if page.Cursor == nil {
			break
		}
		cursor = *page.Cursor
	}
	if len(seen) != 6 {
		t.Fatal("Audit entries missing", len(seen))
	}
	w = contractCall(a, "GET", "/admin/audit?state=failed", "", "test-session")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "audit_fixture_0") || strings.Contains(w.Body.String(), "audit_fixture_1") {
		t.Fatal("Filter ignored")
	}
	for _, path := range []string{"/admin/audit?limit=0", "/admin/audit?limit=101", "/admin/audit?cursor=broken"} {
		if w = contractCall(a, "GET", path, "", "test-session"); w.Code != 422 {
			t.Fatal(path, w.Code)
		}
	}
	if w = contractCall(a, "GET", "/admin/audit", "", ""); w.Code != 401 {
		t.Fatal("Audit authentication bypassed")
	}
	if w = contractCall(a, "POST", "/admin/audit", "{}", "test-session"); w.Code != 405 {
		t.Fatal("Audit accepted writes")
	}
	if err := a.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if w = contractCall(a, "GET", "/admin/audit", "", "test-session"); w.Code != 503 {
		t.Fatal("Storage failure hidden", w.Code)
	}
}

func TestAdminAuditRetentionPreservesUncertainAndFinancialHistory(t *testing.T) {
	a := contractApp(t, nil)
	now := time.Now().UTC()
	for _, item := range []struct {
		id, kind, state string
		days            int
	}{{"recent_audit", "admin_action", "succeeded", 2}, {"expired_audit", "admin_action", "succeeded", 91}, {"uncertain_audit", "admin_action", "running", 91}, {"expired_artifact", "backup", "succeeded", 2}} {
		at := now.Add(-time.Duration(item.days) * 24 * time.Hour)
		op := Operation{ID: item.id, Kind: item.kind, State: item.state, Version: 1, CreatedAt: at, UpdatedAt: at}
		if _, err := a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES(?,?)", op.ID, encode(op)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Store.DB.Exec("INSERT INTO accounting_audit(id,created_at,data) VALUES(?,?,?)", "financial_audit", now.Add(-48*time.Hour).Format(time.RFC3339Nano), `{"kind":"budget_change"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.cleanup(1); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"recent_audit": 1, "expired_audit": 0, "uncertain_audit": 1, "expired_artifact": 0} {
		var count int
		if err := a.Store.DB.QueryRow("SELECT count(*) FROM operations WHERE id=?", id).Scan(&count); err != nil || count != want {
			t.Fatal("Wrong retention", id, count, err)
		}
	}
	var count int
	if err := a.Store.DB.QueryRow("SELECT count(*) FROM accounting_audit WHERE id='financial_audit'").Scan(&count); err != nil || count != 1 {
		t.Fatal("Financial audit removed with request retention", err)
	}
}
