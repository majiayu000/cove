package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestLocalAutomaticSessionBoundaries(t *testing.T) {
	a := contractApp(t, nil)
	// An old password setting must not re-enable a removed feature.
	if _, err := a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('admin_password','unused-old-hash')"); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"", "https://other.invalid", "http://127.0.0.1:5570"} {
		r := contractRequest("POST", "/admin/session", `{}`, "")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("wrong-origin automatic session accepted")
		}
	}
	for range 35 {
		w := contractCall(a, "POST", "/admin/session", `{}`, "")
		var result struct {
			Token string `json:"session_token"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token == "" {
			t.Fatal("local automatic session failed")
		}
		if contractCall(a, "GET", "/admin/sources", "", result.Token).Code != 200 {
			t.Fatal("automatic session cannot manage")
		}
		if contractCall(a, "POST", "/v1/responses", contractBody, result.Token).Code != 401 {
			t.Fatal("management token used as model API key")
		}
		if contractCall(a, "POST", "/admin/password", `{"password":"unused-password"}`, result.Token).Code != 404 {
			t.Fatal("password setup is still available")
		}
		var reused struct {
			Token string `json:"session_token"`
		}
		json.Unmarshal(contractCall(a, "POST", "/admin/session", `{}`, result.Token).Body.Bytes(), &reused)
		if reused.Token != result.Token {
			t.Fatal("page refresh does not reuse session")
		}
	}
	if len(a.sessions) != 32 {
		t.Fatal("automatic session count is unbounded")
	}
}
