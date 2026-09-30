package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Management reads local metadata only. Native IDs and file contents stay private.
func (a *App) resourceConsoleAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/admin/resource-status" {
		if r.Method != "GET" {
			fail(w, 405, "方法不支持", "")
			return true
		}
		resources := []map[string]any{}
		jobs := []map[string]any{}
		rows, err := a.Store.DB.Query("SELECT id,kind,key_id,source_id,account_id,source_generation,account_generation,state,version,created_at,metadata_json FROM resources ORDER BY created_at DESC,id DESC LIMIT 200")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		for rows.Next() {
			var id, kind, key, source, account, state, created, raw string
			var gen, agen, version int
			if err = rows.Scan(&id, &kind, &key, &source, &account, &gen, &agen, &state, &version, &created, &raw); err != nil {
				break
			}
			var metadata resourceMetadata
			if err = json.Unmarshal([]byte(raw), &metadata); err != nil {
				break
			}
			resources = append(resources, map[string]any{"id": id, "kind": kind, "client_key_id": key, "source_id": source, "account_id": account, "source_generation": gen, "account_generation": agen, "state": state, "version": version, "created_at": created, "filename": metadata.Filename, "purpose": metadata.Purpose, "bytes": metadata.Bytes, "bytes_known": metadata.BytesKnown, "mime": metadata.MIME})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		rows, err = a.Store.DB.Query("SELECT id,request_id,kind,key_id,source_id,state,version,cancel_requested_at,settled_at,created_at FROM jobs ORDER BY created_at DESC,id DESC LIMIT 200")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		for rows.Next() {
			var id, request, kind, key, source, state, created string
			var version int
			var cancelled, settled sql.NullString
			if err = rows.Scan(&id, &request, &kind, &key, &source, &state, &version, &cancelled, &settled, &created); err != nil {
				break
			}
			jobs = append(jobs, map[string]any{"id": id, "request_id": request, "kind": kind, "client_key_id": key, "source_id": source, "state": state, "version": version, "cancel_requested": cancelled.Valid, "settled": settled.Valid, "created_at": created})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		writeJSON(w, 200, map[string]any{"resources": resources, "jobs": jobs, "limit": 200})
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/jobs/") {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "cancel" {
		fail(w, 404, "任务操作不存在", "")
		return true
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return true
	}
	var input struct {
		Version int `json:"version"`
	}
	if !decode(w, r, &input) {
		return true
	}
	a.mu.Lock()
	var keyID, kind string
	err := a.Store.DB.QueryRow("SELECT key_id,kind FROM jobs WHERE id=?", parts[2]).Scan(&keyID, &kind)
	if err != nil {
		a.mu.Unlock()
		fail(w, 404, "任务不存在", "")
		return true
	}
	j, err := a.resourceReadJob(parts[2], keyID, kind)
	if err != nil {
		a.mu.Unlock()
		resourceFail(w, err)
		return true
	}
	if j.Version != input.Version {
		a.mu.Unlock()
		fail(w, 409, "任务已修改，请刷新", "version")
		return true
	}
	if j.Cancelled.Valid || j.Terminal.Valid {
		a.mu.Unlock()
		writeJSON(w, 200, map[string]any{"id": j.ID, "state": j.State, "already_requested": true})
		return true
	}
	src, err := a.Store.source(j.SourceID)
	if err != nil || src.AccountID != j.AccountID || src.Generation != j.Generation || src.AccountGeneration != j.AccountGeneration || !src.Configured {
		a.mu.Unlock()
		fail(w, 409, "原账号代次已改变，无法取消旧任务", "target")
		return true
	}
	if j.NativeID == "" {
		a.mu.Unlock()
		fail(w, 409, "上游任务ID未知；不能重发创建或取消", "id")
		return true
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cfg := a.Config
	result, err := a.Store.DB.Exec("UPDATE jobs SET cancel_requested_at=?,state='cancelling',version=version+1 WHERE id=? AND version=? AND cancel_requested_at IS NULL", now, j.ID, j.Version)
	a.mu.Unlock()
	if err != nil {
		resourceFail(w, err)
		return true
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		fail(w, 409, "另一操作已修改此任务", "version")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.TotalTimeout)*time.Second)
	defer cancel()
	path := "/responses/" + url.PathEscape(j.NativeID) + "/cancel"
	if kind == "batch" {
		path = "/batches/" + url.PathEscape(j.NativeID) + "/cancel"
	}
	resp, err := a.resourceCall(ctx, src, "POST", path, strings.NewReader("{}"), "application/json")
	if err != nil {
		resourceFail(w, err)
		return true
	}
	wire, _, err := resourceJSON(resp, cfg.MaxResponse)
	if err != nil {
		resourceFail(w, err)
		return true
	}
	current, err := a.resourceReadJob(j.ID, j.KeyID, j.Kind)
	if err == nil {
		if kind == "batch" {
			err = a.observeBatchJob(ctx, current, src, wire)
		} else {
			err = a.observeBackgroundJob(current, wire)
		}
	}
	if err != nil {
		resourceFail(w, err)
		return true
	}
	writeJSON(w, 200, map[string]any{"id": j.ID, "cancel_requested": true, "upstream_execution": "unknown"})
	return true
}

func (a *App) fileInputSource(raw json.RawMessage, key ClientKey) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &resourceError{400, "input 结构无效", "input"}
	}
	source := ""
	var visit func(any) error
	visit = func(v any) error {
		switch item := v.(type) {
		case []any:
			for _, child := range item {
				if err := visit(child); err != nil {
					return err
				}
			}
		case map[string]any:
			if file, ok := item["file_id"].(string); ok && file != "" {
				owned, _, err := a.resourceOwned(key, file, "file")
				if err != nil {
					return err
				}
				if source != "" && source != owned.SourceID {
					return &resourceError{409, "同一请求的文件属于不同来源", "input"}
				}
				source = owned.SourceID
			}
			for _, child := range item {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := visit(value)
	return source, err
}
