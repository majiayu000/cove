package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type actionResult struct {
	Path      string          `json:"path"`
	Hash      string          `json:"body_hash,omitempty"`
	Sensitive bool            `json:"sensitive"`
	Status    int             `json:"http_status,omitempty"`
	EntityID  string          `json:"entity_id,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
}

type actionWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *actionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *actionWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *actionWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(b) <= 1<<20 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}
func (w *actionWriter) Flush() {
	if w.status == 0 {
		w.status = 200
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// A durable claim precedes side effects. Interrupted or sensitive actions are
// never executed twice; a lost Key response cannot recover its plaintext secret.
func (a *App) withAdminAction(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	actionID := r.Header.Get("X-Cove-Action-Id")
	if actionID == "" || r.Method == "GET" || r.Method == "HEAD" {
		next(w, r)
		return
	}
	if len(actionID) > 200 {
		fail(w, 400, "action_id 过长", "X-Cove-Action-Id")
		return
	}
	result := actionResult{Path: r.Method + " " + r.URL.RequestURI()}
	// Client configuration can contain arbitrary third-party secret fields.
	result.Sensitive = strings.HasPrefix(r.URL.Path, "/admin/notifications") || strings.HasPrefix(r.URL.Path, "/admin/clients/") || strings.HasPrefix(r.URL.Path, "/admin/config-extensions/") || strings.HasSuffix(r.URL.Path, "/credential") || strings.HasSuffix(r.URL.Path, "/login") || strings.HasSuffix(r.URL.Path, "/rotate") || (r.URL.Path == "/admin/client-keys" && r.Method == "POST") || strings.Contains(r.Header.Get("Content-Type"), "application/x-tar") || r.URL.Path == "/admin/config-transfer/import-preview" || r.URL.Path == "/admin/telemetry"
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") || r.ContentLength == 0 {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			fail(w, 413, "管理动作请求体超限", "")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		var body map[string]json.RawMessage
		if len(b) > 0 && json.Unmarshal(b, &body) != nil {
			next(w, r)
			return
		}
		for _, field := range []string{"credential", "secret", "passphrase", "access_token", "refresh_token", "api_key"} {
			if raw, ok := body[field]; ok && string(raw) != "null" && string(raw) != `""` {
				result.Sensitive = true
			}
		}
		if !result.Sensitive {
			result.Hash = digest(encode(body))
		}
	} else {
		result.Sensitive = true
	}
	opID := "action_" + digest(actionID)
	now := time.Now().UTC()
	op := Operation{ID: opID, Kind: "admin_action", State: "running", Version: 1, CreatedAt: now, UpdatedAt: now, Result: result}
	res, err := a.Store.DB.Exec("INSERT OR IGNORE INTO operations(id,data) VALUES(?,?)", opID, encode(op))
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if n == 0 {
		var raw string
		if err = a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", opID).Scan(&raw); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		var previous struct {
			Operation
			Result actionResult `json:"result"`
		}
		if json.Unmarshal([]byte(raw), &previous) != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if previous.Result.Sensitive || result.Sensitive || previous.Result.Path != result.Path || previous.Result.Hash != result.Hash || previous.State != "succeeded" {
			code := "action_conflict"
			message := "该管理动作已登记，请查看结果后明确发起新动作"
			if previous.Result.Sensitive && previous.Result.EntityID != "" && (previous.Result.Path == "POST /admin/client-keys" || strings.HasSuffix(previous.Result.Path, "/rotate")) {
				code = "secret_not_recoverable"
				message = "动作已执行；密钥不再可取回。请核对并撤销原 Key，再明确创建新 Key"
			}
			writeJSON(w, 409, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{"operation_id": opID, "entity_id": previous.Result.EntityID}}, "request_id": id("req")})
			return
		}
		if previous.Result.Status == 0 || !json.Valid(previous.Result.Response) {
			fail(w, 409, "动作结果未确认，请查看操作记录", "")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Cove-Action-Replayed", "true")
		w.WriteHeader(previous.Result.Status)
		_, _ = w.Write(previous.Result.Response)
		return
	}
	writer := &actionWriter{ResponseWriter: w}
	next(writer, r)
	result.Status = writer.status
	var response map[string]json.RawMessage
	if json.Unmarshal(writer.body.Bytes(), &response) == nil {
		_ = json.Unmarshal(response["id"], &result.EntityID)
		for _, field := range []string{"key", "operation", "source"} {
			var entity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(response[field], &entity) == nil && entity.ID != "" {
				result.EntityID = entity.ID
			}
		}
		if !result.Sensitive && result.Status < 400 {
			result.Response = append(json.RawMessage(nil), writer.body.Bytes()...)
		}
	}
	op.State = "succeeded"
	if result.Status >= 400 || result.Status == 0 {
		op.State = "failed"
	}
	op.Result = result
	op.ObjectID = result.EntityID
	op.UpdatedAt = time.Now().UTC()
	op.Version++
	if _, err = a.Store.DB.Exec("UPDATE operations SET data=? WHERE id=?", encode(op), opID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.markStorageFailure()
	}
}
