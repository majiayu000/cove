package app

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// These resources outlive request retention. Root installs this schema and protects jobs' request references.
const ResourceOperationsSchema = `
CREATE TABLE IF NOT EXISTS resources(id TEXT PRIMARY KEY,native_id TEXT NOT NULL,kind TEXT NOT NULL,key_id TEXT NOT NULL REFERENCES client_keys(id),source_id TEXT NOT NULL REFERENCES sources(id),account_id TEXT NOT NULL REFERENCES accounts(id),source_generation INTEGER NOT NULL,account_generation INTEGER NOT NULL,protocol TEXT NOT NULL,model TEXT,state TEXT NOT NULL,version INTEGER NOT NULL DEFAULT 1,created_at TEXT NOT NULL,expires_at TEXT,last_observed_at TEXT,metadata_json TEXT NOT NULL DEFAULT '{}',UNIQUE(key_id,source_id,source_generation,account_id,account_generation,kind,native_id));
CREATE INDEX IF NOT EXISTS resources_key_created ON resources(key_id,created_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,request_id TEXT NOT NULL UNIQUE REFERENCES requests(id),resource_id TEXT REFERENCES resources(id),input_resource_id TEXT REFERENCES resources(id),native_id TEXT NOT NULL,kind TEXT NOT NULL,key_id TEXT NOT NULL REFERENCES client_keys(id),source_id TEXT NOT NULL REFERENCES sources(id),account_id TEXT NOT NULL REFERENCES accounts(id),source_generation INTEGER NOT NULL,account_generation INTEGER NOT NULL,state TEXT NOT NULL,raw_state TEXT,version INTEGER NOT NULL DEFAULT 1,next_poll_at TEXT,last_observed_at TEXT,terminal_fingerprint TEXT,cancel_requested_at TEXT,settled_at TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,metadata_json TEXT NOT NULL DEFAULT '{}',error_json TEXT);
CREATE INDEX IF NOT EXISTS jobs_poll ON jobs(state,next_poll_at);
CREATE TABLE IF NOT EXISTS job_items(job_id TEXT NOT NULL REFERENCES jobs(id),custom_id TEXT NOT NULL,line_number INTEGER NOT NULL,line_hash TEXT NOT NULL,request_id TEXT NOT NULL UNIQUE REFERENCES requests(id),model TEXT NOT NULL,state TEXT NOT NULL,result_fingerprint TEXT,settled_at TEXT,metadata_json TEXT NOT NULL DEFAULT '{}',PRIMARY KEY(job_id,custom_id),UNIQUE(job_id,line_number));`

type resourceBatchLine struct {
	CustomID     string `json:"custom_id"`
	Line         int    `json:"line"`
	Hash         string `json:"hash"`
	Model        string `json:"model"`
	SentModel    string `json:"sent_model"`
	Endpoint     string `json:"endpoint"`
	InputTokens  int64  `json:"input_tokens_estimate"`
	OutputTokens int64  `json:"output_tokens_estimate"`
}
type resourceMetadata struct {
	Filename     string              `json:"filename,omitempty"`
	Purpose      string              `json:"purpose,omitempty"`
	Bytes        int64               `json:"bytes"`
	BytesKnown   bool                `json:"bytes_known"`
	MIME         string              `json:"mime,omitempty"`
	Lines        []resourceBatchLine `json:"batch_lines,omitempty"`
	Endpoint     string              `json:"endpoint,omitempty"`
	PollFailures int                 `json:"poll_failures,omitempty"`
	ParentJob    string              `json:"parent_job,omitempty"`
}
type ownedResource struct {
	ID, NativeID, Kind, KeyID, SourceID, AccountID, Protocol, Model, State, Created string
	Generation, AccountGeneration, Version                                          int
	Expires, Observed                                                               sql.NullString
	Metadata                                                                        resourceMetadata
}
type resourceJob struct {
	ID, RequestID, ResourceID, InputID, NativeID, Kind, KeyID, SourceID, AccountID, State, RawState, Created string
	Generation, AccountGeneration, Version                                                                   int
	Next, Terminal, Cancelled, Settled                                                                       sql.NullString
	Metadata                                                                                                 resourceMetadata
}
type resourceError struct {
	Code           int
	Message, Field string
}

func (e *resourceError) Error() string { return e.Message }
func resourceFail(w http.ResponseWriter, err error) {
	var e *resourceError
	if errors.As(err, &e) {
		fail(w, e.Code, e.Message, e.Field)
		return
	}
	var b *accountingError
	if errors.As(err, &b) {
		accountingFailure(w, err)
		return
	}
	fail(w, 503, storageError().Error(), "")
}
func resourceCapability(src Source, op string, creating bool) error {
	if src.Kind != "api_key" || !slices.Contains(src.NativeOperations, op) || (src.Provider != "" && src.Provider != "openai" && src.Provider != "openai_compatible") {
		return &resourceError{422, "此来源没有独立配置的原生资源 operation；云与订阅不能继承公开 API 合同", "operation"}
	}
	if src.Deleted || !src.Configured || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.AuthStatus == "rejected" {
		return &resourceError{409, "资源的原账号已退出、换代或不可用", "target"}
	}
	if creating && !src.Enabled {
		return &resourceError{422, "来源已停用，不能创建资源", "target"}
	}
	if op == "background" && creating && (src.Verification.Status != "passed" || !slices.Contains(src.Verification.Capabilities, "background")) {
		return &resourceError{422, "后台 Responses 需要单独验证过的原生能力", "background"}
	}
	return nil
}
func (a *App) resourceKey(r *http.Request, op string) (ClientKey, error) {
	key, e := a.Store.keyByDigest(digest(bearer(r)))
	if e != nil && e != sql.ErrNoRows {
		return key, e
	}
	if e == sql.ErrNoRows || !keyValid(key, time.Now()) {
		return key, &resourceError{401, "客户端 Key 无效或已撤销", ""}
	}
	if !allowed(key.ProtocolAllowlist, "responses") || !allowed(key.OperationAllowlist, op) {
		return key, &resourceError{403, "Key 无此资源操作权限", "operation"}
	}
	return key, nil
}
func (a *App) resourceReachable(key ClientKey, src Source) bool {
	if key.SourceID != "" {
		return key.SourceID == src.ID
	}
	route, e := a.Store.route(key.RouteID)
	if e != nil {
		return false
	}
	for _, m := range route.Members {
		model, e := a.Store.model(m.ModelID)
		if e == nil && model.SourceID == src.ID {
			return true
		}
	}
	return false
}
func (a *App) resourceSource(key ClientKey, op, model string) (Source, string, error) {
	if model != "" {
		src, sent, _, e := a.selectSourceExcluding(key, model, "responses", false, map[string]bool{})
		if e != nil {
			return src, sent, &resourceError{422, e.Error(), "model"}
		}
		return src, sent, resourceCapability(src, op, true)
	}
	if key.SourceID != "" {
		src, e := a.Store.source(key.SourceID)
		if e != nil {
			return src, "", e
		}
		return src, "", resourceCapability(src, op, true)
	}
	route, e := a.Store.route(key.RouteID)
	if e != nil || !route.Enabled {
		return Source{}, "", &resourceError{422, "路由不可用", "target"}
	}
	for _, m := range route.Members {
		model, e := a.Store.model(m.ModelID)
		if e != nil || !model.Enabled {
			continue
		}
		src, e := a.Store.source(model.SourceID)
		if e == nil && resourceCapability(src, op, true) == nil {
			return src, "", nil
		}
	}
	return Source{}, "", &resourceError{422, "路由没有支持此原生资源 operation 的来源", "operation"}
}
func (a *App) resourceOwned(key ClientKey, publicID, kind string) (ownedResource, Source, error) {
	var v ownedResource
	var meta string
	e := a.Store.DB.QueryRow("SELECT id,native_id,kind,key_id,source_id,account_id,source_generation,account_generation,protocol,COALESCE(model,''),state,version,created_at,expires_at,last_observed_at,metadata_json FROM resources WHERE id=? AND key_id=? AND kind=?", publicID, key.ID, kind).Scan(&v.ID, &v.NativeID, &v.Kind, &v.KeyID, &v.SourceID, &v.AccountID, &v.Generation, &v.AccountGeneration, &v.Protocol, &v.Model, &v.State, &v.Version, &v.Created, &v.Expires, &v.Observed, &meta)
	if e == sql.ErrNoRows {
		return v, Source{}, &resourceError{404, "资源不存在", "id"}
	}
	if e != nil {
		return v, Source{}, e
	}
	if e = json.Unmarshal([]byte(meta), &v.Metadata); e != nil {
		return v, Source{}, e
	}
	src, e := a.Store.source(v.SourceID)
	if e != nil {
		return v, src, &resourceError{409, "资源原来源不存在", "target"}
	}
	if !a.resourceReachable(key, src) || src.AccountID != v.AccountID || src.Generation != v.Generation || src.AccountGeneration != v.AccountGeneration {
		return v, src, &resourceError{409, "资源不属于当前目标或账号代次", "target"}
	}
	if e = resourceCapability(src, "files", false); e != nil && kind == "file" {
		return v, src, e
	}
	if kind == "batch" {
		e = resourceCapability(src, "batch", false)
	}
	return v, src, e
}
func (a *App) resourceAdmit(r *http.Request, key ClientKey, src Source, op string) (Config, context.Context, func(), error) {
	a.mu.Lock()
	c := a.Config
	if lease := dataIngress(r); lease != nil {
		c = lease.Config
	}
	reject := func(e error) (Config, context.Context, func(), error) { a.mu.Unlock(); return c, nil, nil, e }
	if a.stopping || a.storageFailed.Load() {
		return reject(&resourceError{503, "服务正在关闭或存储异常", ""})
	}
	latest, e := a.resourceCurrentKey(r)
	if e != nil || latest.Version != key.Version || !keyValid(latest, time.Now()) {
		return reject(&resourceError{409, "Key 已改变", ""})
	}
	current, e := a.Store.source(src.ID)
	if e != nil || current.Generation != src.Generation || current.AccountGeneration != src.AccountGeneration || current.Version != src.Version {
		return reject(&resourceError{409, "原来源已改变", "target"})
	}
	if dataIngress(r) == nil && len(a.slots) >= c.MaxConcurrent || dataIngress(r) == nil && key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent || src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		return reject(&resourceError{429, "共享并发容量已满", ""})
	}
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e != nil {
			return reject(e)
		}
		if route.MaxConcurrent != nil && a.routeActive[route.ID] >= *route.MaxConcurrent {
			return reject(&resourceError{429, "路由并发已满", ""})
		}
	}
	if dataIngress(r) == nil {
		select {
		case a.slots <- struct{}{}:
		default:
			return reject(&resourceError{429, "本机并发已满", ""})
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(c.TotalTimeout)*time.Second)
	runningID := id("resource_call")
	a.ownedTasks.Add(1)
	a.running[runningID] = cancel
	a.runningSources[runningID] = src.ID
	a.runningKeys[runningID] = key.ID
	if dataIngress(r) == nil {
		a.keyActive[key.ID]++
	}
	a.accountActive[src.AccountID]++
	if key.RouteID != "" {
		a.routeActive[key.RouteID]++
	}
	a.mu.Unlock()
	release := func() {
		defer a.ownedTasks.Done()
		cancel()
		a.mu.Lock()
		if dataIngress(r) == nil {
			<-a.slots
		}
		delete(a.running, runningID)
		delete(a.runningSources, runningID)
		delete(a.runningKeys, runningID)
		if dataIngress(r) == nil {
			a.keyActive[key.ID]--
		}
		a.accountActive[src.AccountID]--
		if key.RouteID != "" {
			a.routeActive[key.RouteID]--
		}
		a.mu.Unlock()
	}
	return c, ctx, release, nil
}
func (a *App) resourceCall(ctx context.Context, src Source, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	secret, e := a.Secrets.Get(src.CredentialRef)
	if e != nil {
		return nil, &resourceError{409, "原账号凭据不可用", "target"}
	}
	req, e := http.NewRequestWithContext(ctx, method, safeEndpoint(src.BaseURL, path), body)
	if e != nil {
		return nil, &resourceError{502, "原生资源 endpoint 无效", ""}
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+secret)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, e := a.doUpstream(req, src)
	if e != nil {
		return nil, &resourceError{502, "上游资源请求中断；执行可能未知，未重放", ""}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, &resourceError{502, "上游资源重定向被拒绝", ""}
	}
	original := resp.Body
	stop := context.AfterFunc(ctx, func() { original.Close() })
	resp.Body = &resourceResponseBody{ReadCloser: original, Stop: stop}
	return resp, nil
}

type resourceResponseBody struct {
	io.ReadCloser
	Stop func() bool
}

func (b *resourceResponseBody) Close() error { b.Stop(); return b.ReadCloser.Close() }
func resourceJSON(resp *http.Response, limit int64) (map[string]json.RawMessage, []byte, error) {
	defer resp.Body.Close()
	raw, e := readLimited(resp.Body, limit)
	if e != nil {
		return nil, nil, &resourceError{502, "资源响应超限或中断", ""}
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return nil, nil, &resourceError{502, "资源响应不是 JSON 对象", ""}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &resourceError{resp.StatusCode, "原生资源请求被上游拒绝", ""}
	}
	return body, raw, nil
}
func resourceNativeID(raw json.RawMessage) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 512 || strings.ContainsAny(value, "/\\\r\n?#") {
		return "", &resourceError{502, "上游资源 ID 无效", "id"}
	}
	return value, nil
}
func resourceInsert(tx *sql.Tx, v ownedResource) error {
	_, e := tx.Exec("INSERT INTO resources(id,native_id,kind,key_id,source_id,account_id,source_generation,account_generation,protocol,model,state,version,created_at,expires_at,last_observed_at,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", v.ID, v.NativeID, v.Kind, v.KeyID, v.SourceID, v.AccountID, v.Generation, v.AccountGeneration, v.Protocol, v.Model, v.State, v.Version, v.Created, v.Expires, v.Observed, encode(v.Metadata))
	return e
}
func resourceWire(v ownedResource) map[string]any {
	created, _ := time.Parse(time.RFC3339Nano, v.Created)
	out := map[string]any{"id": v.ID, "object": v.Kind, "bytes": v.Metadata.Bytes, "created_at": created.Unix(), "filename": v.Metadata.Filename, "purpose": v.Metadata.Purpose, "status": v.State}
	if !v.Metadata.BytesKnown {
		out["bytes"] = nil
	}
	if v.Expires.Valid {
		if t, e := time.Parse(time.RFC3339Nano, v.Expires.String); e == nil {
			out["expires_at"] = t.Unix()
		}
	}
	return out
}
func (a *App) resourceOperationsAPI(w http.ResponseWriter, r *http.Request) bool {
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] != "v1" || (parts[1] != "files" && parts[1] != "batches" && !(parts[1] == "responses" && len(parts) > 2 && parts[2] != "compact")) {
		return false
	}
	if parts[1] == "files" {
		a.resourceFiles(w, r, parts[2:])
		return true
	}
	if parts[1] == "batches" {
		a.resourceBatches(w, r, parts[2:])
		return true
	}
	a.resourceResponses(w, r, parts[2:])
	return true
}

// Run once at startup before opening admission. Never removes recent or symlink entries.
func (a *App) cleanupResourceSpools(ctx context.Context) error {
	a.mu.Lock()
	dir := a.Config.DataDir
	a.mu.Unlock()
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.HasPrefix(entry.Name(), ".resource-spool-") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if time.Since(info.ModTime()) > time.Hour {
			if e = os.RemoveAll(filepath.Join(dir, entry.Name())); e != nil {
				return e
			}
		}
	}
	return nil
}

func (a *App) resourceFiles(w http.ResponseWriter, r *http.Request, parts []string) {
	key, e := a.resourceKey(r, "files")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case "POST":
			a.uploadResourceFile(w, r, key)
		case "GET":
			a.listResourceFiles(w, r, key)
		default:
			fail(w, 405, "方法不支持", "")
		}
		return
	}
	if len(parts) > 2 || len(parts) == 2 && (parts[1] != "content" || r.Method != "GET") {
		fail(w, 404, "资源接口不存在", "")
		return
	}
	v, src, e := a.resourceOwned(key, parts[0], "file")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if r.Method != "GET" && r.Method != "DELETE" {
		fail(w, 405, "方法不支持", "")
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "files")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	path := "/files/" + url.PathEscape(v.NativeID)
	if len(parts) == 2 {
		a.resourceFileContent(w, r, key, src, v, path+"/content", ctx, c)
		return
	}
	if r.Method == "DELETE" {
		var n int
		e = a.Store.DB.QueryRow("SELECT count(*) FROM jobs WHERE (input_resource_id=? OR resource_id=? OR id=?) AND settled_at IS NULL", v.ID, v.ID, v.Metadata.ParentJob).Scan(&n)
		if e != nil {
			resourceFail(w, e)
			return
		}
		if n > 0 {
			fail(w, 409, "文件仍被运行或账务未决的 job 引用", "id")
			return
		}
		if v.State == "deleted" {
			writeJSON(w, 200, map[string]any{"id": v.ID, "object": "file", "deleted": true})
			return
		}
		if _, e = a.Store.DB.Exec("UPDATE resources SET state='deleting_unknown',version=version+1 WHERE id=? AND version=?", v.ID, v.Version); e != nil {
			resourceFail(w, e)
			return
		}
		resp, e := a.resourceCall(ctx, src, "DELETE", path, nil, "")
		if e != nil {
			resourceFail(w, e)
			return
		}
		if resp.StatusCode == 404 {
			resp.Body.Close()
		} else {
			body, _, e := resourceJSON(resp, c.MaxResponse)
			if e != nil {
				resourceFail(w, e)
				return
			}
			var deleted bool
			if json.Unmarshal(body["deleted"], &deleted) != nil || !deleted {
				fail(w, 502, "provider 未确认删除；保持 deleting_unknown", "id")
				return
			}
		}
		if _, e = a.Store.DB.Exec("UPDATE resources SET state='deleted',version=version+1,last_observed_at=? WHERE id=?", time.Now().UTC().Format(time.RFC3339Nano), v.ID); e != nil {
			a.storageFailed.Store(true)
			resourceFail(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"id": v.ID, "object": "file", "deleted": true})
		return
	}
	resp, e := a.resourceCall(ctx, src, "GET", path, nil, "")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if resp.StatusCode == 404 {
		resp.Body.Close()
		state := "not_found"
		if v.State == "deleting_unknown" {
			state = "deleted"
		}
		if _, e = a.Store.DB.Exec("UPDATE resources SET state=?,version=version+1,last_observed_at=? WHERE id=?", state, time.Now().UTC().Format(time.RFC3339Nano), v.ID); e != nil {
			a.storageFailed.Store(true)
			resourceFail(w, e)
			return
		}
		fail(w, 404, "原生文件已不存在", "id")
		return
	}
	body, _, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(body["id"])
	if e != nil || native != v.NativeID {
		fail(w, 502, "provider 返回了不同文件 ID", "id")
		return
	}
	var state string
	json.Unmarshal(body["status"], &state)
	if state == "" {
		state = v.State
	}
	v.State = state
	v.Version++
	v.Observed = sql.NullString{String: time.Now().UTC().Format(time.RFC3339Nano), Valid: true}
	var reportedBytes int64
	if json.Unmarshal(body["bytes"], &reportedBytes) == nil && reportedBytes >= 0 {
		v.Metadata.Bytes = reportedBytes
		v.Metadata.BytesKnown = true
	}
	if _, e = a.Store.DB.Exec("UPDATE resources SET state=?,version=?,last_observed_at=?,metadata_json=? WHERE id=?", v.State, v.Version, v.Observed, encode(v.Metadata), v.ID); e != nil {
		a.storageFailed.Store(true)
		resourceFail(w, e)
		return
	}
	body["id"] = json.RawMessage(encode(v.ID))
	writeJSON(w, 200, body)
}
func (a *App) listResourceFiles(w http.ResponseWriter, r *http.Request, key ClientKey) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			fail(w, 400, "limit 必须为 1 到 100", "limit")
			return
		}
		limit = n
	}
	order := r.URL.Query().Get("order")
	if order == "" {
		order = "desc"
	}
	if order != "asc" && order != "desc" {
		fail(w, 400, "order 无效", "order")
		return
	}
	query := "SELECT id FROM resources WHERE key_id=? AND kind='file'"
	args := []any{key.ID}
	if purpose := r.URL.Query().Get("purpose"); purpose != "" {
		query += " AND json_extract(metadata_json,'$.purpose')=?"
		args = append(args, purpose)
	}
	if after := r.URL.Query().Get("after"); after != "" {
		var created string
		if e := a.Store.DB.QueryRow("SELECT created_at FROM resources WHERE id=? AND key_id=? AND kind='file'", after, key.ID).Scan(&created); e != nil {
			fail(w, 400, "after 不属于本 Key 的文件列表", "after")
			return
		}
		cmp := "<"
		if order == "asc" {
			cmp = ">"
		}
		query += " AND (created_at " + cmp + " ? OR (created_at=? AND id " + cmp + " ?))"
		args = append(args, created, created, after)
	}
	query += " ORDER BY created_at " + order + ",id " + order + " LIMIT ?"
	args = append(args, limit+1)
	rows, e := a.Store.DB.Query(query, args...)
	if e != nil {
		resourceFail(w, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var value string
		if e = rows.Scan(&value); e != nil {
			break
		}
		ids = append(ids, value)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		resourceFail(w, e)
		return
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	out := []any{}
	for _, public := range ids {
		v, _, e := a.resourceOwned(key, public, "file")
		if e != nil {
			var re *resourceError
			if !errors.As(e, &re) || re.Code != 409 {
				resourceFail(w, e)
				return
			} // Stale resources remain visible without leaking native IDs.
			var meta, created, state string
			a.Store.DB.QueryRow("SELECT metadata_json,created_at,state FROM resources WHERE id=? AND key_id=?", public, key.ID).Scan(&meta, &created, &state)
			json.Unmarshal([]byte(meta), &v.Metadata)
			v.ID = public
			v.Kind = "file"
			v.Created = created
			v.State = "owner_unavailable"
		}
		out = append(out, resourceWire(v))
	}
	first, last := "", ""
	if len(ids) > 0 {
		first, last = ids[0], ids[len(ids)-1]
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": out, "has_more": more, "first_id": first, "last_id": last})
}
func (a *App) resourceFileContent(w http.ResponseWriter, r *http.Request, key ClientKey, src Source, v ownedResource, path string, ctx context.Context, c Config) {
	secret, e := a.Secrets.Get(src.CredentialRef)
	if e != nil {
		resourceFail(w, &resourceError{409, "原账号凭据不可用", "target"})
		return
	}
	req, e := http.NewRequestWithContext(ctx, "GET", safeEndpoint(src.BaseURL, path), nil)
	if e != nil {
		resourceFail(w, e)
		return
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	rangeValue := r.Header.Get("Range")
	if rangeValue != "" {
		if !strings.HasPrefix(rangeValue, "bytes=") || strings.ContainsAny(rangeValue, ",\r\n") {
			fail(w, 416, "只支持单一原生 byte Range", "Range")
			return
		}
		req.Header.Set("Range", rangeValue)
	}
	resp, e := a.doUpstream(req, src)
	if e != nil {
		resourceFail(w, &resourceError{502, "文件读取中断，未重新上传", ""})
		return
	}
	defer resp.Body.Close()
	stop := context.AfterFunc(ctx, func() { resp.Body.Close() })
	defer stop()
	if rangeValue != "" && (resp.StatusCode != 206 || resp.Header.Get("Content-Range") == "") {
		fail(w, 416, "provider 未提供可验证的原生 Range", "Range")
		return
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			fail(w, 502, "文件内容重定向被拒绝", "")
			return
		}
		fail(w, resp.StatusCode, "文件内容不可用", "")
		return
	}
	if resp.ContentLength > nativeMediaLimit {
		fail(w, 502, "文件内容超过本地 32 MiB 上限", "")
		return
	}
	w.Header().Set("Content-Type", redact(resp.Header.Get("Content-Type"), secret, bearer(r)))
	w.Header().Set("Cache-Control", "no-store")
	if rangeValue != "" {
		w.Header().Set("Content-Range", redact(resp.Header.Get("Content-Range"), secret, bearer(r)))
	}
	fileRequestID := id("req")
	w.Header().Set("X-Gateway-Request-Id", fileRequestID)
	w.WriteHeader(resp.StatusCode)
	record := Record{ID: fileRequestID, Protocol: "responses", Operation: "files.content", Origin: "client", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, Started: time.Now().UTC(), AttemptStarted: time.Now().UTC(), AttemptID: id("att"), Sequence: 1, Version: 1, Submission: "possible", ResponseMIME: redact(resp.Header.Get("Content-Type"), secret, bearer(r)), ObservationStatus: "complete", UpstreamStatus: "unknown", DeliveryStatus: "failed", Status: "failed"}
	buffer := make([]byte, 32<<10)
	for {
		n, e := resp.Body.Read(buffer)
		if n > 0 {
			if record.ResponseBytes+int64(n) > nativeMediaLimit {
				record.ErrorStage = "response_limit"
				break
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(c.IdleTimeout) * time.Second))
			written, we := w.Write(buffer[:n])
			record.ResponseBytes += int64(written)
			if we != nil || written != n {
				record.ErrorStage = "downstream_write"
				break
			}
		}
		if e == io.EOF {
			if ctx.Err() != nil {
				record.Status = "cancelled"
				record.ErrorStage = "cancelled"
				break
			}
			if resp.ContentLength > 0 && record.ResponseBytes != resp.ContentLength {
				record.ErrorStage = "upstream_body"
				break
			}
			record.Status = "succeeded"
			record.UpstreamStatus = "completed"
			record.DeliveryStatus = "completed"
			break
		}
		if e != nil {
			record.ErrorStage = "upstream_body"
			break
		}
	}
	ended := time.Now().UTC()
	if ctx.Err() != nil {
		record.Status = "cancelled"
		record.ErrorStage = "cancelled"
		record.DeliveryStatus = "failed"
	}
	record.Ended = &ended
	record.DurationMS = ended.Sub(record.Started).Milliseconds()
	record.Completeness = "unknown"
	if e = a.Store.record(record); e != nil {
		a.markStorageFailure()
	} else {
		a.observeExecution(record, src, true)
	}
}
func (a *App) uploadResourceFile(w http.ResponseWriter, r *http.Request, key ClientKey) {
	a.mu.Lock()
	src, _, e := a.resourceSource(key, "files", "")
	a.mu.Unlock()
	if e != nil {
		resourceFail(w, e)
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "files")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	if r.ContentLength > nativeMediaLimit {
		fail(w, 413, "multipart 总大小超过 32 MiB", "")
		return
	}
	available, e := resourceFreeSpace(ctx, c.DataDir)
	if e != nil {
		fail(w, 503, "无法确认暂存磁盘空闲容量，未上传", "")
		return
	}
	if available < uint64(nativeMediaLimit)*2 {
		fail(w, 507, "暂存磁盘空闲容量不足，未上传", "")
		return
	}
	media, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "multipart/form-data" || params["boundary"] == "" {
		fail(w, 400, "上传需 multipart/form-data", "Content-Type")
		return
	}
	folder, e := os.MkdirTemp(c.DataDir, ".resource-spool-")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer os.RemoveAll(folder)
	path := filepath.Join(folder, "request")
	target, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer target.Close()
	stop := context.AfterFunc(ctx, func() { r.Body.Close() })
	defer stop()
	limited := http.MaxBytesReader(w, r.Body, nativeMediaLimit)
	reader := multipart.NewReader(limited, params["boundary"])
	writer := multipart.NewWriter(target)
	meta := resourceMetadata{}
	files := 0
	parts := 0
	purposeSeen := false
	for {
		part, pe := reader.NextPart()
		if pe == io.EOF {
			break
		}
		if pe != nil {
			var large *http.MaxBytesError
			if errors.As(pe, &large) {
				e = pe
			} else {
				e = &resourceError{400, "multipart 结构无效，未上传", "file"}
			}
			break
		}
		parts++
		if parts > 2048 {
			e = &resourceError{413, "multipart part 过多", ""}
			break
		}
		name := part.FormName()
		if part.FileName() == "" {
			value, pe := readLimited(part, 64<<10)
			if pe != nil {
				e = pe
				break
			}
			if name == "purpose" {
				if purposeSeen || len(value) == 0 || len(value) > 256 {
					e = &resourceError{400, "purpose 缺失、重复或超限", "purpose"}
					break
				}
				purposeSeen = true
				meta.Purpose = string(value)
			}
			output, pe := writer.CreatePart(part.Header)
			if pe != nil {
				e = pe
				break
			}
			_, e = output.Write(value)
			if e != nil {
				break
			}
			continue
		}
		if name != "file" || files != 0 || !purposeSeen {
			e = &resourceError{422, "需一个 file，且 purpose 元数据应置于文件之前", "file"}
			break
		}
		files++
		meta.Filename = part.FileName()
		if len(meta.Filename) > 255 {
			e = &resourceError{400, "显示文件名超过 255 字节", "file"}
			break
		}
		meta.MIME = part.Header.Get("Content-Type")
		buffered := bufio.NewReader(part)
		prefix, _ := buffered.Peek(512)
		declared, _, pe := mime.ParseMediaType(meta.MIME)
		if pe != nil {
			e = &resourceError{400, "文件 MIME 无效", "file"}
			break
		}
		if strings.HasPrefix(declared, "image/") || strings.HasPrefix(declared, "audio/") {
			if pe = validateNativeMedia(prefix, declared, strings.HasPrefix(declared, "audio/")); pe != nil {
				e = &resourceError{400, pe.Error(), "file"}
				break
			}
		}
		if declared == "application/pdf" && !bytes.HasPrefix(prefix, []byte("%PDF-")) {
			e = &resourceError{400, "PDF MIME 与文件 magic 不一致", "file"}
			break
		}
		output, pe := writer.CreatePart(part.Header)
		if pe != nil {
			e = pe
			break
		}
		if meta.Purpose == "batch" {
			if e = resourceCapability(src, "batch", true); e != nil {
				break
			}
			meta.Lines, meta.Bytes, e = a.prepareBatchFile(buffered, output, key, src)
		} else {
			meta.Bytes, e = io.CopyBuffer(output, buffered, make([]byte, 32<<10))
		}
		if e != nil {
			break
		}
		meta.BytesKnown = true
	}
	if pe := writer.Close(); e == nil {
		e = pe
	}
	if e != nil {
		if ctx.Err() != nil {
			fail(w, 408, "上传读取已取消或超时，未上传", "")
			return
		}
		var large *http.MaxBytesError
		if errors.As(e, &large) {
			fail(w, 413, "multipart 总大小超过 32 MiB", "")
		} else {
			resourceFail(w, e)
		}
		return
	}
	if files != 1 || !purposeSeen {
		fail(w, 400, "file 与 purpose 必须提供", "file")
		return
	}
	if _, e = target.Seek(0, io.SeekStart); e != nil {
		resourceFail(w, e)
		return
	}
	// Recheck publication after the bounded upload has been prepared.
	a.mu.Lock()
	current, ke := a.Store.keyByDigest(digest(bearer(r)))
	latest, se := a.Store.source(src.ID)
	changed := ke != nil || se != nil || current.Version != key.Version || !keyValid(current, time.Now()) || latest.Version != src.Version || latest.Generation != src.Generation || latest.AccountGeneration != src.AccountGeneration
	a.mu.Unlock()
	if changed {
		fail(w, 409, "上传准备期间 Key 或原来源发生改变；未上传", "")
		return
	}
	nowTime := time.Now().UTC()
	uploadRecord := Record{ID: id("req"), Protocol: "responses", Operation: "files.upload", Origin: "client", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, RouteID: key.RouteID, Started: nowTime, AttemptStarted: nowTime, AttemptID: id("att"), Sequence: 1, Version: 1, Status: "dispatching", Submission: "possible", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", RequestBytes: meta.Bytes, Completeness: "unknown"}
	if e = a.Store.record(uploadRecord); e != nil {
		a.storageFailed.Store(true)
		resourceFail(w, e)
		return
	}
	w.Header().Set("X-Gateway-Request-Id", uploadRecord.ID)
	saved := false
	defer func() {
		if saved {
			return
		}
		end := time.Now().UTC()
		uploadRecord.Ended = &end
		uploadRecord.DurationMS = end.Sub(uploadRecord.Started).Milliseconds()
		uploadRecord.Version++
		if uploadRecord.Status == "dispatching" {
			uploadRecord.Status = "failed"
		}
		if ctx.Err() != nil {
			uploadRecord.Status = "cancelled"
		}
		if e = a.Store.record(uploadRecord); e != nil {
			a.markStorageFailure()
		} else {
			a.observeExecution(uploadRecord, src, true)
		}
	}()
	resp, e := a.resourceCall(ctx, src, "POST", "/files", target, writer.FormDataContentType())
	if e != nil {
		uploadRecord.ErrorStage = "orphan_unknown"
		resourceFail(w, e)
		return
	}
	body, providerRaw, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		uploadRecord.ErrorStage = "orphan_unknown"
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(body["id"])
	if e != nil {
		resourceFail(w, e)
		return
	}
	var conflicts int
	e = a.Store.DB.QueryRow("SELECT count(*) FROM resources WHERE key_id=? AND native_id=? AND kind='file'", key.ID, native).Scan(&conflicts)
	if e != nil {
		a.storageFailed.Store(true)
		resourceFail(w, e)
		return
	}
	if conflicts > 0 {
		uploadRecord.ErrorStage = "binding_conflict"
		fail(w, 409, "同 native ID 已有资源归属，不能覆盖", "id")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	v := ownedResource{ID: "file-cove-" + token(), NativeID: native, Kind: "file", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, Generation: src.Generation, AccountGeneration: src.AccountGeneration, Protocol: "responses", State: "uploaded", Version: 1, Created: now, Observed: sql.NullString{String: now, Valid: true}, Metadata: meta}
	var expires int64
	if json.Unmarshal(body["expires_at"], &expires) == nil && expires > 0 {
		v.Expires = sql.NullString{String: time.Unix(expires, 0).UTC().Format(time.RFC3339Nano), Valid: true}
	}
	tx, e := a.Store.DB.Begin()
	if e == nil {
		e = resourceInsert(tx, v)
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		a.storageFailed.Store(true)
		uploadRecord.ErrorStage = "storage_binding"
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if deleted, de := a.resourceCall(cleanup, src, "DELETE", "/files/"+url.PathEscape(native), nil, ""); de == nil {
			deleted.Body.Close()
		}
		fail(w, 503, "上游已上传但本地绑定失败，已停止准入并尝试原来源删除；不能宣称未上传", "")
		return
	}
	uploadRecord.UpstreamStatus = "completed"
	uploadRecord.DeliveryStatus = "completed"
	uploadRecord.Status = "succeeded"
	uploadRecord.HTTPStatus = 200
	uploadRecord.ResponseBytes = int64(len(providerRaw))
	uploadRecord.ResponseMIME = "application/json"
	end := time.Now().UTC()
	uploadRecord.Ended = &end
	uploadRecord.DurationMS = end.Sub(uploadRecord.Started).Milliseconds()
	uploadRecord.Version++
	if e = a.Store.record(uploadRecord); e != nil {
		a.storageFailed.Store(true)
		resourceFail(w, e)
		return
	}
	saved = true
	a.observeExecution(uploadRecord, src, true)
	body["id"] = json.RawMessage(encode(v.ID))
	writeJSON(w, 200, body)
}
func (a *App) resourceModel(key ClientKey, src Source, model string) (string, error) {
	if !allowed(key.ModelAllowlist, model) {
		return "", &resourceError{403, "Key 无此批量模型权限", "model"}
	}
	if key.SourceID != "" {
		if slices.Contains(src.Models, model) {
			return model, nil
		}
		return "", &resourceError{422, "批量模型不属于文件绑定来源", "model"}
	}
	route, e := a.Store.route(key.RouteID)
	if e != nil {
		return "", e
	}
	var rid string
	if e = a.Store.DB.QueryRow("SELECT route_id FROM model_aliases WHERE public_model=?", model).Scan(&rid); e != nil || rid != route.ID {
		return "", &resourceError{422, "批量模型别名不属于当前路由", "model"}
	}
	for _, member := range route.Members {
		m, e := a.Store.model(member.ModelID)
		if e == nil && m.Enabled && m.SourceID == src.ID {
			return m.UpstreamModel, nil
		}
	}
	return "", &resourceError{422, "批量必须由文件绑定的同一来源执行", "model"}
}
func batchEndpoint(path string) (string, bool) {
	switch path {
	case "/v1/responses":
		return "responses", true
	case "/v1/chat/completions":
		return "chat", true
	default:
		return "", false
	}
}
func (a *App) prepareBatchFile(reader io.Reader, writer io.Writer, key ClientKey, src Source) ([]resourceBatchLine, int64, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32<<10), int(nativeJSONLimit))
	items := []resourceBatchLine{}
	seen := map[string]bool{}
	var total int64
	endpoint := ""
	for scanner.Scan() {
		line := len(items) + 1
		if line > 10000 {
			return nil, total, &resourceError{413, "Batch 超过本地 10000 行上限", "file"}
		}
		raw := scanner.Bytes()
		total += int64(len(raw)) + 1
		var item struct {
			CustomID string                     `json:"custom_id"`
			Method   string                     `json:"method"`
			URL      string                     `json:"url"`
			Body     map[string]json.RawMessage `json:"body"`
		}
		if json.Unmarshal(raw, &item) != nil || item.CustomID == "" || len(item.CustomID) > 256 || seen[item.CustomID] || item.Method != "POST" {
			return nil, total, &resourceError{400, fmt.Sprintf("Batch line %d custom_id=%q 无效或重复", line, item.CustomID), "file"}
		}
		seen[item.CustomID] = true
		op, valid := batchEndpoint(item.URL)
		if !valid || !allowed(key.ProtocolAllowlist, op) || !allowed(key.OperationAllowlist, "generate") && !allowed(key.OperationAllowlist, "background") {
			return nil, total, &resourceError{400, fmt.Sprintf("Batch line %d custom_id=%q endpoint 或 operation 无权限", line, item.CustomID), "url"}
		}
		if endpoint != "" && endpoint != item.URL {
			return nil, total, &resourceError{400, "同一 Batch 文件只能使用一个已实现 endpoint", "url"}
		}
		endpoint = item.URL
		var model string
		if json.Unmarshal(item.Body["model"], &model) != nil || model == "" {
			return nil, total, &resourceError{400, fmt.Sprintf("Batch line %d custom_id=%q 缺 model", line, item.CustomID), "model"}
		}
		sent, e := a.resourceModel(key, src, model)
		if e != nil {
			return nil, total, e
		}
		var opaque []string
		var generic any
		json.Unmarshal(raw, &generic)
		if e = nativeResources(generic, &opaque); e != nil || len(opaque) > 0 {
			return nil, total, &resourceError{422, "Batch 输入暂不接受未适配资源或 opaque 历史", "body"}
		}
		if e = resourceValidateTools(item.Body); e != nil {
			return nil, total, e
		}
		inputRaw, _ := json.Marshal(item.Body)
		estimated, _, e := reservationEstimate(item.Body, inputRaw)
		if e != nil {
			return nil, total, &resourceError{422, "Batch token 预留估算无法闭合", "body"}
		}
		output := int64(4096)
		for _, field := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens"} {
			if value, ok := item.Body[field]; ok {
				if json.Unmarshal(value, &output) != nil || output < 1 {
					return nil, total, &resourceError{400, "输出 token 上限无效", field}
				}
				break
			}
		}
		items = append(items, resourceBatchLine{CustomID: item.CustomID, Line: line, Hash: digest(string(raw)), Model: model, SentModel: sent, Endpoint: item.URL, InputTokens: estimated - output, OutputTokens: output})
		item.Body["model"] = json.RawMessage(encode(sent))
		encoded, _ := json.Marshal(item)
		if _, e = writer.Write(append(encoded, '\n')); e != nil {
			return nil, total, e
		}
	}
	if e := scanner.Err(); e != nil {
		return nil, total, &resourceError{413, "Batch JSONL 单行超限或读取失败", "file"}
	}
	if len(items) == 0 {
		return nil, total, &resourceError{400, "Batch JSONL 不能为空", "file"}
	}
	return items, total, nil
}

// Same requests/attempts and the existing budget owner, within the resource CAS transaction.
func resourceRecordTx(tx *sql.Tx, v Record) error {
	if v.Sequence == 0 {
		v.Sequence = 1
	}
	if v.AttemptID == "" {
		v.AttemptID = v.ID + "_attempt_1"
	}
	_, e := tx.Exec("INSERT INTO requests(id,source_id,started,status,data) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data", v.ID, v.SourceID, v.Started.Format(time.RFC3339Nano), v.Status, encode(v))
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO attempts(id,request_id,sequence,source_id,account_id,data) VALUES(?,?,?,?,?,?) ON CONFLICT(request_id,sequence) DO UPDATE SET data=excluded.data", v.AttemptID, v.ID, v.Sequence, v.SourceID, v.AccountID, encode(v))
	if e != nil {
		return e
	}
	if e = reserveAccounting(tx, v); e != nil {
		return e
	}
	if v.Ended != nil && v.Cost != nil && v.Completeness == "complete" && v.ObservationStatus != "partial" { // A final provider observation can close reservations made uncertain by restart.
		rows, e := tx.Query("SELECT budget_id,period_start,allocation_json FROM reservations WHERE request_id=?", v.ID)
		if e != nil {
			return e
		}
		type item struct{ budget, start, raw string }
		pending := []item{}
		for rows.Next() {
			var it item
			if e = rows.Scan(&it.budget, &it.start, &it.raw); e != nil {
				break
			}
			pending = append(pending, it)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		for _, it := range pending {
			var allocations map[string]attemptAllocation
			if e = json.Unmarshal([]byte(it.raw), &allocations); e != nil {
				return e
			}
			allocation, ok := allocations[v.AttemptID]
			if ok && allocation.State == "pending_reconciliation" {
				allocation.State = "pending"
				allocations[v.AttemptID] = allocation
				if e = saveAllocations(tx, v.ID, it.budget, it.start, it.raw, allocations); e != nil {
					return e
				}
			}
		}
	}
	return settleAccounting(tx, v)
}
func resourceReadRecord(q accountingQuery, requestID string) (Record, error) {
	var raw string
	var v Record
	e := q.QueryRow("SELECT data FROM requests WHERE id=?", requestID).Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &v)
	}
	return v, e
}
func resourceBindTx(tx *sql.Tx, record Record, native string) error {
	var conflict int
	if e := tx.QueryRow("SELECT count(*) FROM bindings WHERE response_id=? AND key_id=? AND (source_id!=? OR generation!=? OR account_generation!=? OR model!=?)", native, record.KeyID, record.SourceID, record.Generation, record.AccountGeneration, record.SentModel).Scan(&conflict); e != nil {
		return e
	}
	if conflict > 0 {
		return &resourceError{409, "同 native response ID 已属于另一账号或模型，不能覆盖", "id"}
	}
	_, e := tx.Exec("INSERT OR IGNORE INTO bindings(response_id,key_id,source_id,generation,account_generation,model,request_id) VALUES(?,?,?,?,?,?,?)", native, record.KeyID, record.SourceID, record.Generation, record.AccountGeneration, record.SentModel, record.ID)
	return e
}
func resourceJobInsert(tx *sql.Tx, j resourceJob) error {
	_, e := tx.Exec("INSERT INTO jobs(id,request_id,resource_id,input_resource_id,native_id,kind,key_id,source_id,account_id,source_generation,account_generation,state,raw_state,version,next_poll_at,created_at,updated_at,metadata_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", j.ID, j.RequestID, nullable(j.ResourceID), nullable(j.InputID), j.NativeID, j.Kind, j.KeyID, j.SourceID, j.AccountID, j.Generation, j.AccountGeneration, j.State, j.RawState, j.Version, j.Next, j.Created, j.Created, encode(j.Metadata))
	return e
}
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (a *App) resourceReadJob(idValue, keyID, kind string) (resourceJob, error) {
	var j resourceJob
	var meta string
	e := a.Store.DB.QueryRow("SELECT id,request_id,COALESCE(resource_id,''),COALESCE(input_resource_id,''),native_id,kind,key_id,source_id,account_id,source_generation,account_generation,state,COALESCE(raw_state,''),version,next_poll_at,terminal_fingerprint,cancel_requested_at,settled_at,created_at,metadata_json FROM jobs WHERE (id=? OR native_id=? OR resource_id=?) AND key_id=? AND kind=?", idValue, idValue, idValue, keyID, kind).Scan(&j.ID, &j.RequestID, &j.ResourceID, &j.InputID, &j.NativeID, &j.Kind, &j.KeyID, &j.SourceID, &j.AccountID, &j.Generation, &j.AccountGeneration, &j.State, &j.RawState, &j.Version, &j.Next, &j.Terminal, &j.Cancelled, &j.Settled, &j.Created, &meta)
	if e == sql.ErrNoRows {
		return j, &resourceError{404, "远程任务不存在", "id"}
	}
	if e == nil {
		e = json.Unmarshal([]byte(meta), &j.Metadata)
	}
	return j, e
}
func (a *App) resourceJobSource(key ClientKey, j resourceJob) (Source, error) {
	src, e := a.Store.source(j.SourceID)
	if e != nil {
		return src, &resourceError{409, "任务的原来源不存在", "target"}
	}
	if !a.resourceReachable(key, src) || src.Generation != j.Generation || src.AccountGeneration != j.AccountGeneration || src.AccountID != j.AccountID {
		return src, &resourceError{409, "任务不属于当前目标或账号代次", "target"}
	}
	op := "batch"
	if j.Kind == "background" {
		op = "background"
	}
	return src, resourceCapability(src, op, false)
}
func jobState(kind, raw string) (state string, terminal bool) {
	if kind == "background" {
		switch raw {
		case "queued", "in_progress":
			return raw, false
		case "completed", "failed", "incomplete", "cancelled":
			return raw, true
		}
	} else {
		switch raw {
		case "validating", "in_progress", "finalizing", "cancelling":
			return raw, false
		case "completed", "failed", "expired", "cancelled":
			return raw, true
		}
	}
	return "unrecognized", false
}
func jobPollTime(failures int) string {
	seconds := []int{2, 5, 10, 30}
	n := failures
	if n >= len(seconds) {
		return time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	}
	return time.Now().UTC().Add(time.Duration(seconds[n])*time.Second + time.Duration(time.Now().UnixNano()%401)*time.Millisecond).Format(time.RFC3339Nano)
}
func (a *App) resourceJobLimit(tx *sql.Tx) error {
	var n int
	if e := tx.QueryRow("SELECT count(*) FROM jobs WHERE terminal_fingerprint IS NULL").Scan(&n); e != nil {
		return e
	}
	if n >= 32 {
		return &resourceError{429, "本机最多 32 个活动或待核对远程 job", ""}
	}
	return nil
}

// Root dispatches this only for decoded background=true, before ordinary validateRequest/forward.
func (a *App) backgroundResponses(w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) {
	key, e := a.resourceKey(r, "background")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if !allowed(key.OperationAllowlist, "generate") {
		fail(w, 403, "Key 无生成权限", "operation")
		return
	}
	var model string
	if json.Unmarshal(body["model"], &model) != nil || model == "" || !allowed(key.ModelAllowlist, model) {
		fail(w, 403, "Key 无此模型权限", "model")
		return
	}
	var stream bool
	if value, ok := body["stream"]; ok && (json.Unmarshal(value, &stream) != nil || stream) {
		fail(w, 422, "此后台子集只接受非流式创建；通过 GET 观察终态", "stream")
		return
	}
	var store bool
	if value, ok := body["store"]; ok && (json.Unmarshal(value, &store) != nil || !store) {
		fail(w, 422, "此后台子集不能在 store=false 下保证持久轮询，未修改隐私选择", "store")
		return
	}
	a.mu.Lock()
	src, sent, e := a.resourceSource(key, "background", model)
	a.mu.Unlock()
	if e != nil {
		resourceFail(w, e)
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "background")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	a.backgroundResponsesAdmitted(w, r, body, key, src, model, sent, c, ctx)
}

func (a *App) backgroundResponsesAdmitted(w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage, key ClientKey, src Source, model, sent string, c Config, ctx context.Context) {
	var e error
	if e = resourceValidateTools(body); e != nil {
		resourceFail(w, e)
		return
	}
	if e = a.mapOwnedFileInputs(body, key, src); e != nil {
		resourceFail(w, e)
		return
	}
	if e = a.validateNativeOpaqueHistory(body, key, src, sent); e != nil {
		fail(w, 409, e.Error(), "input")
		return
	}
	var previous string
	if value, ok := body["previous_response_id"]; ok && string(value) != "null" {
		if json.Unmarshal(value, &previous) != nil || !a.Store.continuation(previous, key, src, sent) {
			fail(w, 409, "前序响应不属于当前 Key 与原账号模型", "previous_response_id")
			return
		}
	}
	body["model"] = json.RawMessage(encode(sent))
	encoded, e := json.Marshal(body)
	if e != nil || int64(len(encoded)) > nativeJSONLimit {
		fail(w, 413, "后台请求超过 8 MiB", "")
		return
	}
	tokens, provenance, e := reservationEstimate(body, encoded)
	if e != nil {
		resourceFail(w, e)
		return
	}
	a.mu.Lock()
	latestKey, keyErr := a.resourceCurrentKey(r)
	latestSource, sourceErr := a.Store.source(src.ID)
	if keyErr != nil || sourceErr != nil || latestKey.Version != key.Version || !keyValid(latestKey, time.Now()) || latestSource.Version != src.Version || latestSource.Generation != src.Generation || latestSource.AccountGeneration != src.AccountGeneration || ctx.Err() != nil {
		a.mu.Unlock()
		fail(w, 409, "后台准备期间 Key 或原来源已改变，未提交", "")
		return
	}
	ok, retry := a.checkTPM(key, tokens, time.Now())
	if !ok {
		a.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		fail(w, 429, "Key token 窗口已满", "limits.tpm")
		return
	}
	plan, e := a.prepareAccounting(key, src, body)
	if e != nil {
		a.mu.Unlock()
		resourceFail(w, e)
		return
	}
	if ok, retry = a.consumeRPM(key, time.Now()); !ok {
		a.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		fail(w, 429, "Key 请求速率已满", "limits.rpm")
		return
	}
	now := time.Now().UTC()
	record := Record{ID: id("req"), Protocol: "responses", Operation: "background", Origin: resourceVerificationOrigin(r.Context()), KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, RouteID: key.RouteID, Model: model, SentModel: sent, Price: src.Price, Started: now, AttemptStarted: now, AttemptID: id("att"), Sequence: 1, Version: 1, Status: "queued", Submission: "possible", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", Accounting: plan, ReservedTokens: tokens, TokenReservationSource: provenance, RequestBytes: int64(len(encoded))}
	j := resourceJob{ID: id("job"), RequestID: record.ID, Kind: "background", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, Generation: src.Generation, AccountGeneration: src.AccountGeneration, State: "submitting_unknown", Version: 1, Created: now.Format(time.RFC3339Nano), Metadata: resourceMetadata{Endpoint: "/responses"}}
	tx, e := a.Store.DB.Begin()
	if e == nil {
		e = a.resourceJobLimit(tx)
		if e == nil {
			e = resourceRecordTx(tx, record)
		}
		if e == nil {
			e = resourceJobInsert(tx, j)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		a.refundRPM(key)
		a.mu.Unlock()
		resourceFail(w, e)
		return
	}
	a.reserveTPM(key, record.ID, tokens, now)
	a.mu.Unlock()
	w.Header().Set("X-Gateway-Request-Id", record.ID)
	resp, e := a.resourceCall(ctx, src, "POST", "/responses", bytes.NewReader(encoded), "application/json")
	if e != nil {
		resourceFail(w, e)
		return
	}
	wire, raw, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(wire["id"])
	if e != nil {
		resourceFail(w, e)
		return
	}
	j.NativeID = native
	var conflicts int
	e = a.Store.DB.QueryRow("SELECT count(*) FROM jobs WHERE native_id=? AND key_id=? AND id<>?", native, key.ID, j.ID).Scan(&conflicts)
	if e != nil {
		resourceFail(w, e)
		return
	}
	if conflicts > 0 {
		fail(w, 409, "同 native response ID 已有另一归属，不能覆盖", "id")
		return
	}
	tx, e = a.Store.DB.Begin()
	if e == nil {
		_, e = tx.Exec("UPDATE jobs SET native_id=?,next_poll_at=?,updated_at=?,version=version+1 WHERE id=? AND native_id=''", native, jobPollTime(0), time.Now().UTC().Format(time.RFC3339Nano), j.ID)
		if e == nil {
			e = resourceBindTx(tx, record, native)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		a.storageFailed.Store(true)
		fail(w, 503, "后台任务已提交但绑定失败，停止准入；不重新创建", "")
		return
	}
	j.Version++
	j, e = a.resourceReadJob(j.ID, key.ID, "background")
	if e == nil {
		e = a.observeBackgroundJob(j, wire)
	}
	if e != nil {
		resourceFail(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}
func (a *App) mapOwnedFileInputs(body map[string]json.RawMessage, key ClientKey, src Source) error {
	raw, ok := body["input"]
	if !ok {
		return nil
	}
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return &resourceError{400, "input 无效", "input"}
	}
	mapped := false
	var walk func(any) error
	walk = func(v any) error {
		switch item := v.(type) {
		case []any:
			for _, child := range item {
				if e := walk(child); e != nil {
					return e
				}
			}
		case map[string]any:
			kind, _ := item["type"].(string)
			if fileID, exists := item["file_id"]; exists {
				if kind != "input_file" && kind != "input_image" {
					return &resourceError{422, "此 file_id 路径没有归属适配", "file_id"}
				}
				public, ok := fileID.(string)
				if !ok {
					return &resourceError{400, "file_id 无效", "file_id"}
				}
				file, owner, e := a.resourceOwned(key, public, "file")
				if e != nil {
					return e
				}
				if owner.ID != src.ID || owner.Generation != src.Generation || owner.AccountGeneration != src.AccountGeneration {
					return &resourceError{409, "文件必须属于当前原账号来源", "file_id"}
				}
				if file.State == "deleted" || file.State == "not_found" {
					return &resourceError{409, "文件已不可用", "file_id"}
				}
				item["file_id"] = file.NativeID
				mapped = true
			}
			for field, child := range item {
				if field == "vector_store_ids" || field == "conversation" {
					return &resourceError{422, "此资源引用尚未适配", "input"}
				}
				if e := walk(child); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(input); e != nil {
		return e
	}
	if mapped {
		body["input"] = json.RawMessage(encode(input))
	}
	if raw, ok := body["tools"]; ok {
		var tools any
		if json.Unmarshal(raw, &tools) != nil {
			return &resourceError{400, "tools 无效", "tools"}
		}
		var opaque []string
		if e := nativeResources(tools, &opaque); e != nil {
			return &resourceError{422, "工具资源引用尚无归属适配", "tools"}
		}
	}
	return nil
}
func (a *App) observeBackgroundJob(j resourceJob, wire map[string]json.RawMessage) error {
	if !validJobUsage(wire) {
		return &resourceError{502, "后台响应 usage 无效，保持原账务与待观察状态", "usage"}
	}
	var rawState string
	json.Unmarshal(wire["status"], &rawState)
	state, terminal := jobState("background", rawState)
	raw, _ := json.Marshal(wire)
	record, e := resourceReadRecord(a.Store.DB, j.RequestID)
	if e != nil {
		return e
	}
	observeNativeUsage(&record, raw)
	record.ResponseID = j.NativeID
	record.UpstreamStatus = rawState
	record.Version++
	record.Completeness = usageCompleteness(record.Usage)
	fingerprint := digest(encode(struct {
		State string
		Usage Usage
	}{rawState, record.Usage}))
	if j.Terminal.Valid {
		if j.Terminal.String != fingerprint {
			return &resourceError{409, "远程终态变更与已导入证据冲突，保持原账务", "id"}
		}
		return nil
	}
	tx, e := a.Store.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	next := sql.NullString{String: jobPollTime(j.Metadata.PollFailures), Valid: true}
	settled := sql.NullString{}
	term := sql.NullString{}
	if terminal {
		term = sql.NullString{String: fingerprint, Valid: true}
		settled = sql.NullString{String: now.Format(time.RFC3339Nano), Valid: true}
		next = sql.NullString{}
		record.Ended = &now
		record.Status = "failed"
		if state == "completed" {
			record.Status = "succeeded"
		}
		if state == "cancelled" {
			record.Status = "cancelled"
		}
		record.DeliveryStatus = "background_result"
		record.Cost = estimate(record.Usage, record.Price)
		if record.Cost == nil {
			record.PartialCost = estimatePartial(record.Usage, record.Price)
		}
		record.DurationMS = now.Sub(record.Started).Milliseconds()
	} else {
		record.Status = "queued"
		record.Ended = nil
		record.Cost = nil
	}
	result, e := tx.Exec("UPDATE jobs SET state=?,raw_state=?,version=version+1,next_poll_at=?,last_observed_at=?,terminal_fingerprint=?,settled_at=?,updated_at=? WHERE id=? AND version=? AND terminal_fingerprint IS NULL", state, rawState, next, now.Format(time.RFC3339Nano), term, settled, now.Format(time.RFC3339Nano), j.ID, j.Version)
	if e != nil {
		return e
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &resourceError{409, "远程任务已由另一观察更新", "id"}
	}
	if e = resourceRecordTx(tx, record); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if terminal {
		if source, se := a.Store.source(j.SourceID); se == nil {
			a.observeExecution(record, source, true)
		}
		key, e := a.Store.DB.Query("SELECT data FROM client_keys WHERE id=?", j.KeyID)
		if e == nil {
			var raw string
			if key.Next() {
				key.Scan(&raw)
			}
			key.Close()
			var k ClientKey
			if json.Unmarshal([]byte(raw), &k) == nil {
				a.mu.Lock()
				a.settleTPM(k, record)
				a.mu.Unlock()
			}
		}
	}
	return nil
}
func (a *App) resourceResponses(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) < 1 || len(parts) > 2 {
		fail(w, 404, "任务接口不存在", "")
		return
	}
	key, e := a.resourceKey(r, "background")
	if e != nil {
		resourceFail(w, e)
		return
	}
	j, e := a.resourceReadJob(parts[0], key.ID, "background")
	if e != nil {
		resourceFail(w, e)
		return
	}
	src, e := a.resourceJobSource(key, j)
	if e != nil {
		resourceFail(w, e)
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "background")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	path := "/responses/" + url.PathEscape(j.NativeID)
	if len(parts) == 2 {
		if parts[1] == "cancel" && r.Method == "POST" {
			a.cancelResourceJob(w, r, j, src, ctx, c, path+"/cancel")
			return
		}
		if parts[1] == "input_items" && r.Method == "GET" {
			path += "/input_items"
			if r.URL.RawQuery != "" {
				q := r.URL.Query()
				for name := range q {
					if name != "limit" && name != "order" && name != "after" {
						fail(w, 400, "未知分页参数", name)
						return
					}
				}
				path += "?" + q.Encode()
			}
			resp, e := a.resourceCall(ctx, src, "GET", path, nil, "")
			if e != nil {
				resourceFail(w, e)
				return
			}
			body, _, e := resourceJSON(resp, c.MaxResponse)
			if e != nil {
				resourceFail(w, e)
				return
			}
			if e = a.mapReturnedFileInputs(body, key, src); e != nil {
				resourceFail(w, e)
				return
			}
			writeJSON(w, 200, body)
			return
		}
		fail(w, 405, "方法或操作不支持", "")
		return
	}
	if r.Method == "DELETE" {
		if !j.Settled.Valid {
			fail(w, 409, "任务未完成或账务未决，不能删除", "id")
			return
		}
		var pending int
		if e = a.Store.DB.QueryRow("SELECT count(*) FROM reservations WHERE request_id=? AND status!='settled'", j.RequestID).Scan(&pending); e != nil {
			resourceFail(w, e)
			return
		}
		if pending > 0 {
			fail(w, 409, "任务账务未决，不能删除远程存储结果", "id")
			return
		}
		resp, e := a.resourceCall(ctx, src, "DELETE", path, nil, "")
		if e != nil {
			resourceFail(w, e)
			return
		}
		body, _, e := resourceJSON(resp, c.MaxResponse)
		if e != nil {
			resourceFail(w, e)
			return
		}
		writeJSON(w, 200, body)
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "方法不支持", "")
		return
	}
	resp, e := a.resourceCall(ctx, src, "GET", path, nil, "")
	if e != nil {
		resourceFail(w, e)
		return
	}
	wire, _, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(wire["id"])
	if e != nil || native != j.NativeID {
		fail(w, 502, "响应任务 ID 不匹配", "id")
		return
	}
	if e = a.observeBackgroundJob(j, wire); e != nil {
		resourceFail(w, e)
		return
	}
	writeJSON(w, 200, wire)
}
func (a *App) cancelResourceJob(w http.ResponseWriter, r *http.Request, j resourceJob, src Source, ctx context.Context, c Config, path string) {
	if j.Cancelled.Valid || j.Settled.Valid {
		public := j.NativeID
		if j.Kind == "batch" {
			public = j.ResourceID
		}
		writeJSON(w, 200, map[string]any{"id": public, "status": j.State, "cancel_requested": j.Cancelled.Valid, "already_requested": true})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, e := a.Store.DB.Exec("UPDATE jobs SET cancel_requested_at=?,state='cancelling',version=version+1 WHERE id=? AND version=? AND cancel_requested_at IS NULL", now, j.ID, j.Version)
	if e != nil {
		resourceFail(w, e)
		return
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		fail(w, 409, "取消已由另一请求处理", "id")
		return
	}
	resp, e := a.resourceCall(ctx, src, "POST", path, strings.NewReader("{}"), "application/json")
	if e != nil {
		resourceFail(w, e)
		return
	}
	wire, _, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	updated, e := a.resourceReadJob(j.ID, j.KeyID, j.Kind)
	if e != nil {
		resourceFail(w, e)
		return
	}
	if j.Kind == "background" {
		e = a.observeBackgroundJob(updated, wire)
	} else {
		e = a.observeBatchJob(ctx, updated, src, wire)
	}
	if e != nil {
		resourceFail(w, e)
		return
	}
	if j.Kind == "batch" {
		key, keyErr := a.resourceKey(r, "batch")
		if keyErr != nil {
			resourceFail(w, keyErr)
			return
		}
		if e = a.mapBatchWire(wire, key, src, j); e != nil {
			resourceFail(w, e)
			return
		}
	}
	writeJSON(w, 200, wire)
}

func (a *App) resourceSoftPlan(key ClientKey, src Source, line resourceBatchLine) (*AccountingPlan, error) {
	budgets, e := matchingBudgets(a.Store.DB, key.budgetScopeID(), key.RouteID)
	if e != nil {
		return nil, e
	}
	if key.BudgetID != "" {
		b, e := readBudget(a.Store.DB, key.BudgetID)
		if e != nil {
			return nil, e
		}
		if b.Scope.Kind == "key" && b.Scope.ID != key.budgetScopeID() || b.Scope.Kind == "route" && b.Scope.ID != key.RouteID {
			return nil, &resourceError{409, "预算作用域无效", "budget_id"}
		}
	}
	if len(budgets) == 0 {
		return nil, nil
	}
	if src.Price == nil || !validPrice(*src.Price) {
		return nil, &resourceError{422, "Batch 软预算需要用户配置的完整 token 价格；费用不能假定为零", "budget"}
	}
	plan := &AccountingPlan{EstimatedInputTokens: line.InputTokens, EstimatedOutputTokens: line.OutputTokens, EstimateProvenance: "soft uploaded JSONL token estimate; configured price; no assumed provider batch discount"}
	price := *src.Price
	price.Cached = ""
	price.CacheCreation = ""
	amount := estimate(Usage{Input: &line.InputTokens, Output: &line.OutputTokens}, &price)
	if amount == nil {
		return nil, &resourceError{422, "Batch 预算预留维度不能闭合", "budget"}
	}
	now := time.Now().UTC()
	for _, b := range budgets {
		if b.Mode == "strict" {
			return nil, &accountingError{422, "budget", strictBudgetReason}
		}
		if b.Currency != src.Price.Currency {
			return nil, &resourceError{422, "Batch 价格与预算币种不同", "budget"}
		}
		start, end, e := budgetBounds(b.Period, now)
		if e != nil {
			return nil, e
		}
		if now.Before(start) || !now.Before(end) {
			return nil, &resourceError{429, "预算周期不可用", "budget"}
		}
		plan.Reservations = append(plan.Reservations, PlannedReservation{BudgetID: b.ID, BudgetVersion: b.Version, PeriodStart: start, PeriodEnd: end, Amount: *amount, Currency: b.Currency})
	}
	return plan, nil
}
func (a *App) resourceBatches(w http.ResponseWriter, r *http.Request, parts []string) {
	key, e := a.resourceKey(r, "batch")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if len(parts) == 0 {
		if r.Method == "POST" {
			a.createResourceBatch(w, r, key)
			return
		}
		if r.Method == "GET" {
			a.listResourceBatches(w, r, key)
			return
		}
		fail(w, 405, "方法不支持", "")
		return
	}
	if len(parts) > 2 {
		fail(w, 404, "任务接口不存在", "")
		return
	}
	j, e := a.resourceReadJob(parts[0], key.ID, "batch")
	if e != nil {
		resourceFail(w, e)
		return
	}
	src, e := a.resourceJobSource(key, j)
	if e != nil {
		resourceFail(w, e)
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "batch")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	path := "/batches/" + url.PathEscape(j.NativeID)
	if len(parts) == 2 {
		if parts[1] == "cancel" && r.Method == "POST" {
			a.cancelResourceJob(w, r, j, src, ctx, c, path+"/cancel")
			return
		}
		fail(w, 405, "方法不支持", "")
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "方法不支持", "")
		return
	}
	resp, e := a.resourceCall(ctx, src, "GET", path, nil, "")
	if e != nil {
		resourceFail(w, e)
		return
	}
	wire, _, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(wire["id"])
	if e != nil || native != j.NativeID {
		fail(w, 502, "Batch ID 与归属不匹配", "id")
		return
	}
	if e = a.observeBatchJob(ctx, j, src, wire); e != nil {
		resourceFail(w, e)
		return
	}
	if e = a.mapBatchWire(wire, key, src, j); e != nil {
		resourceFail(w, e)
		return
	}
	writeJSON(w, 200, wire)
}
func (a *App) createResourceBatch(w http.ResponseWriter, r *http.Request, key ClientKey) {
	raw, e := readLimited(r.Body, 64<<10)
	if e != nil {
		fail(w, 413, "Batch 创建元数据超过 64 KiB", "")
		return
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		fail(w, 400, "Batch 创建需 JSON 对象", "")
		return
	}
	var public, endpoint, window string
	if json.Unmarshal(body["input_file_id"], &public) != nil || json.Unmarshal(body["endpoint"], &endpoint) != nil || json.Unmarshal(body["completion_window"], &window) != nil || window != "24h" {
		fail(w, 400, "input_file_id、endpoint 与原生 completion_window=24h 必须提供", "")
		return
	}
	protocol, valid := batchEndpoint(endpoint)
	if !valid || !allowed(key.ProtocolAllowlist, protocol) || !allowed(key.OperationAllowlist, "generate") {
		fail(w, 422, "Batch endpoint 或生成权限不支持", "endpoint")
		return
	}
	file, src, e := a.resourceOwned(key, public, "file")
	if e != nil {
		resourceFail(w, e)
		return
	}
	if e = resourceCapability(src, "batch", true); e != nil {
		resourceFail(w, e)
		return
	}
	if file.Metadata.Purpose != "batch" || len(file.Metadata.Lines) == 0 || file.State == "deleted" || file.State == "not_found" {
		fail(w, 409, "输入文件不是已验证的本 Key Batch JSONL", "input_file_id")
		return
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "batch")
	if e != nil {
		resourceFail(w, e)
		return
	}
	defer release()
	records := []Record{}
	var total int64
	now := time.Now().UTC()
	for _, line := range file.Metadata.Lines {
		if line.Endpoint != endpoint {
			fail(w, 400, "Batch endpoint 与输入文件逐行合同不一致", "endpoint")
			return
		}
		sent, e := a.resourceModel(key, src, line.Model)
		if e != nil || sent != line.SentModel {
			fail(w, 409, "上传后模型权限或映射改变，需重新准备文件", "model")
			return
		}
		plan, e := a.resourceSoftPlan(key, src, line)
		if e != nil {
			resourceFail(w, e)
			return
		}
		tokens := line.InputTokens + line.OutputTokens
		if tokens < 0 || total > int64(^uint64(0)>>1)-tokens {
			fail(w, 413, "Batch token 估算溢出", "")
			return
		}
		total += tokens
		records = append(records, Record{ID: id("req"), Protocol: protocol, Operation: "batch.item", Origin: "client", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, RouteID: key.RouteID, Model: line.Model, SentModel: sent, Price: src.Price, Started: now, AttemptStarted: now, AttemptID: id("att"), Sequence: 1, Version: 1, Status: "queued", Submission: "possible", DeliveryStatus: "not_started", UpstreamStatus: "unknown", ObservationStatus: "complete", Accounting: plan, ReservedTokens: tokens, TokenReservationSource: "estimated uploaded JSONL admission"})
	}
	a.mu.Lock()
	if ok, retry := a.checkTPM(key, total, now); !ok {
		a.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		fail(w, 429, "Batch token 窗口不足", "limits.tpm")
		return
	}
	if ok, retry := a.consumeRPM(key, now); !ok {
		a.mu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		fail(w, 429, "Key 请求速率已满", "limits.rpm")
		return
	}
	parent := Record{ID: id("req"), Protocol: "responses", Operation: "batch", Origin: "client", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, RouteID: key.RouteID, Started: now, AttemptStarted: now, AttemptID: id("att"), Sequence: 1, Version: 1, Status: "queued", Submission: "possible", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete"}
	publicBatch := "batch-cove-" + token()
	j := resourceJob{ID: id("job"), RequestID: parent.ID, ResourceID: publicBatch, InputID: file.ID, Kind: "batch", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, Generation: src.Generation, AccountGeneration: src.AccountGeneration, State: "submitting_unknown", Version: 1, Created: now.Format(time.RFC3339Nano), Metadata: resourceMetadata{Endpoint: endpoint}}
	resource := ownedResource{ID: publicBatch, NativeID: "pending-" + j.ID, Kind: "batch", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, Generation: src.Generation, AccountGeneration: src.AccountGeneration, Protocol: "responses", State: "submitting_unknown", Version: 1, Created: j.Created, Metadata: j.Metadata}
	tx, e := a.Store.DB.Begin()
	if e == nil {
		e = a.resourceJobLimit(tx)
		if e == nil {
			e = resourceRecordTx(tx, parent)
		}
		if e == nil {
			e = resourceInsert(tx, resource)
		}
		if e == nil {
			e = resourceJobInsert(tx, j)
		}
		for i, record := range records {
			if e != nil {
				break
			}
			e = resourceRecordTx(tx, record)
			if e == nil {
				line := file.Metadata.Lines[i]
				_, e = tx.Exec("INSERT INTO job_items(job_id,custom_id,line_number,line_hash,request_id,model,state,metadata_json) VALUES(?,?,?,?,?,?,'queued',?)", j.ID, line.CustomID, line.Line, line.Hash, record.ID, line.SentModel, encode(line))
			}
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		a.refundRPM(key)
		a.mu.Unlock()
		resourceFail(w, e)
		return
	}
	for _, record := range records {
		a.reserveTPM(key, record.ID, record.ReservedTokens, now)
	}
	a.mu.Unlock()
	w.Header().Set("X-Gateway-Request-Id", parent.ID)
	body["input_file_id"] = json.RawMessage(encode(file.NativeID))
	encoded, _ := json.Marshal(body)
	resp, e := a.resourceCall(ctx, src, "POST", "/batches", bytes.NewReader(encoded), "application/json")
	if e != nil {
		resourceFail(w, e)
		return
	}
	wire, _, e := resourceJSON(resp, c.MaxResponse)
	if e != nil {
		resourceFail(w, e)
		return
	}
	native, e := resourceNativeID(wire["id"])
	if e != nil {
		resourceFail(w, e)
		return
	}
	var conflict int
	if e = a.Store.DB.QueryRow("SELECT count(*) FROM resources WHERE native_id=? AND kind='batch' AND key_id=?", native, key.ID).Scan(&conflict); e != nil {
		resourceFail(w, e)
		return
	}
	if conflict > 0 {
		fail(w, 409, "同 native Batch ID 已有归属，不能覆盖", "id")
		return
	}
	tx, e = a.Store.DB.Begin()
	if e == nil {
		_, e = tx.Exec("UPDATE resources SET native_id=?,state='validating',version=version+1 WHERE id=?", native, publicBatch)
		if e == nil {
			_, e = tx.Exec("UPDATE jobs SET native_id=?,next_poll_at=?,version=version+1 WHERE id=? AND native_id=''", native, jobPollTime(0), j.ID)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		a.storageFailed.Store(true)
		fail(w, 503, "Batch 已提交但本地绑定失败；停止准入且不重复提交", "")
		return
	}
	j, e = a.resourceReadJob(j.ID, key.ID, "batch")
	if e == nil {
		e = a.observeBatchJob(ctx, j, src, wire)
	}
	if e == nil {
		e = a.mapBatchWire(wire, key, src, j)
	}
	if e != nil {
		resourceFail(w, e)
		return
	}
	writeJSON(w, 200, wire)
}
func (a *App) listResourceBatches(w http.ResponseWriter, r *http.Request, key ClientKey) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			fail(w, 400, "limit 必须为 1 到 100", "limit")
			return
		}
		limit = n
	}
	query := "SELECT resource_id FROM jobs WHERE key_id=? AND kind='batch'"
	args := []any{key.ID}
	if after := r.URL.Query().Get("after"); after != "" {
		var created string
		if e := a.Store.DB.QueryRow("SELECT created_at FROM jobs WHERE key_id=? AND resource_id=?", key.ID, after).Scan(&created); e != nil {
			fail(w, 400, "after 不属于本 Key", "after")
			return
		}
		query += " AND created_at < ?"
		args = append(args, created)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, e := a.Store.DB.Query(query, args...)
	if e != nil {
		resourceFail(w, e)
		return
	}
	ids := []string{}
	for rows.Next() {
		var value string
		if e = rows.Scan(&value); e != nil {
			break
		}
		ids = append(ids, value)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		resourceFail(w, e)
		return
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	data := []any{}
	for _, value := range ids {
		j, e := a.resourceReadJob(value, key.ID, "batch")
		if e != nil {
			resourceFail(w, e)
			return
		}
		data = append(data, map[string]any{"id": j.ResourceID, "object": "batch", "status": j.State, "input_file_id": j.InputID})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data, "has_more": more})
}
func (a *App) ensureJobFile(tx *sql.Tx, j resourceJob, native string) (string, error) {
	var idValue, source, account string
	var generation, accountGeneration int
	e := tx.QueryRow("SELECT id,source_id,account_id,source_generation,account_generation FROM resources WHERE native_id=? AND key_id=? AND kind='file' ORDER BY created_at LIMIT 1", native, j.KeyID).Scan(&idValue, &source, &account, &generation, &accountGeneration)
	if e == nil {
		if source != j.SourceID || account != j.AccountID || generation != j.Generation || accountGeneration != j.AccountGeneration {
			return "", &resourceError{409, "输出 file ID 与已有账号归属冲突", "file_id"}
		}
		return idValue, nil
	}
	if e != sql.ErrNoRows {
		return "", e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	file := ownedResource{ID: "file-cove-" + token(), NativeID: native, Kind: "file", KeyID: j.KeyID, SourceID: j.SourceID, AccountID: j.AccountID, Generation: j.Generation, AccountGeneration: j.AccountGeneration, Protocol: "responses", State: "processed", Version: 1, Created: now, Metadata: resourceMetadata{Purpose: "batch_output", ParentJob: j.ID}}
	return file.ID, resourceInsert(tx, file)
}
func (a *App) mapBatchWire(wire map[string]json.RawMessage, key ClientKey, src Source, j resourceJob) error {
	wire["id"] = json.RawMessage(encode(j.ResourceID))
	wire["input_file_id"] = json.RawMessage(encode(j.InputID))
	for _, name := range []string{"output_file_id", "error_file_id"} {
		raw, ok := wire[name]
		if !ok || string(raw) == "null" {
			continue
		}
		native, e := resourceNativeID(raw)
		if e != nil {
			return e
		}
		var public string
		e = a.Store.DB.QueryRow("SELECT id FROM resources WHERE native_id=? AND key_id=? AND source_id=? AND source_generation=? AND account_generation=? AND kind='file'", native, key.ID, src.ID, src.Generation, src.AccountGeneration).Scan(&public)
		if e != nil {
			return e
		}
		wire[name] = json.RawMessage(encode(public))
	}
	return nil
}
func (a *App) observeBatchJob(ctx context.Context, j resourceJob, src Source, wire map[string]json.RawMessage) error {
	var rawState string
	json.Unmarshal(wire["status"], &rawState)
	state, terminal := jobState("batch", rawState)
	tx, e := a.Store.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, name := range []string{"output_file_id", "error_file_id"} {
		raw, ok := wire[name]
		if !ok || string(raw) == "null" {
			continue
		}
		native, e := resourceNativeID(raw)
		if e != nil {
			return e
		}
		if _, e = a.ensureJobFile(tx, j, native); e != nil {
			return e
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	next := sql.NullString{String: jobPollTime(0), Valid: true}
	result, e := tx.Exec("UPDATE jobs SET state=?,raw_state=?,next_poll_at=?,last_observed_at=?,version=version+1,updated_at=? WHERE id=? AND version=?", state, rawState, next, now, now, j.ID, j.Version)
	if e != nil {
		return e
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return &resourceError{409, "Batch 已由另一观察更新", "id"}
	}
	if _, e = tx.Exec("UPDATE resources SET state=?,version=version+1,last_observed_at=? WHERE id=?", state, now, j.ResourceID); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if terminal {
		return a.importBatchTerminal(ctx, j, src, wire)
	}
	return nil
}

type batchResult struct {
	Hash      string
	Body      map[string]json.RawMessage
	Status    int
	Duplicate bool
	Invalid   bool
}

func (a *App) readBatchResults(ctx context.Context, src Source, native string, out map[string]batchResult) error {
	resp, e := a.resourceCall(ctx, src, "GET", "/files/"+url.PathEscape(native)+"/content", nil, "")
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &resourceError{502, "Batch 结果文件暂不可读，保持待核对", "file_id"}
	}
	limited := io.LimitReader(resp.Body, nativeMediaLimit+1)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 32<<10), int(nativeJSONLimit))
	var total int64
	for scanner.Scan() {
		raw := scanner.Bytes()
		total += int64(len(raw)) + 1
		if total > nativeMediaLimit {
			return &resourceError{502, "Batch 结果超过 32 MiB 本地上限", "file_id"}
		}
		var line struct {
			CustomID string `json:"custom_id"`
			Response *struct {
				Status int                        `json:"status_code"`
				Body   map[string]json.RawMessage `json:"body"`
			} `json:"response"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &line) != nil || line.CustomID == "" {
			return &resourceError{502, "Batch 结果 JSONL 无效，保持待核对", "file_id"}
		}
		value := batchResult{Hash: digest(string(raw)), Status: 500}
		if line.Response != nil {
			value.Status = line.Response.Status
			value.Body = line.Response.Body
		}
		if old, ok := out[line.CustomID]; ok {
			value.Duplicate = true
			value.Hash = digest(old.Hash + value.Hash)
		}
		out[line.CustomID] = value
	}
	if scanner.Err() != nil {
		return &resourceError{502, "Batch 结果 JSONL 超限或中断，保持待核对", "file_id"}
	}
	return nil
}
func validJobUsage(body map[string]json.RawMessage) bool {
	raw, ok := body["usage"]
	if !ok || string(raw) == "null" {
		return true
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil {
		return false
	}
	for _, name := range []string{"input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens"} {
		if raw, ok := usage[name]; ok {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 {
				return false
			}
		}
	}
	return true
}
func (a *App) importBatchTerminal(parentContext context.Context, j resourceJob, src Source, wire map[string]json.RawMessage) error {
	a.mu.Lock()
	c := a.Config
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(parentContext, time.Duration(c.TotalTimeout)*time.Second)
	defer cancel()
	results := map[string]batchResult{}
	for _, name := range []string{"output_file_id", "error_file_id"} {
		raw, ok := wire[name]
		if !ok || string(raw) == "null" {
			continue
		}
		native, e := resourceNativeID(raw)
		if e != nil {
			return e
		}
		if e = a.readBatchResults(ctx, src, native, results); e != nil {
			return e
		}
	}
	rows, e := a.Store.DB.Query("SELECT custom_id,request_id,result_fingerprint,state FROM job_items WHERE job_id=? ORDER BY line_number", j.ID)
	if e != nil {
		return e
	}
	type item struct {
		Custom, Request, State string
		Fingerprint            sql.NullString
	}
	items := []item{}
	known := map[string]bool{}
	for rows.Next() {
		var value item
		if e = rows.Scan(&value.Custom, &value.Request, &value.Fingerprint, &value.State); e != nil {
			break
		}
		items = append(items, value)
		known[value.Custom] = true
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for custom := range results {
		if !known[custom] {
			return &resourceError{502, "Batch 结果出现未提交的 custom_id，保持原账务", "custom_id"}
		}
	}
	tx, e := a.Store.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	pending := false
	terminalRecords := []Record{}
	for _, item := range items {
		result, exists := results[item.Custom]
		if item.Fingerprint.Valid {
			if item.State != "settled" {
				pending = true
			}
			if exists && item.Fingerprint.String != result.Hash {
				return &resourceError{409, "同 custom_id 终态证据发生冲突，未覆盖原账务", "custom_id"}
			}
			continue
		}
		record, e := resourceReadRecord(tx, item.Request)
		if e != nil {
			return e
		}
		state := "pending_reconciliation"
		record.Status = "failed"
		record.ErrorStage = "missing_result"
		record.ObservationStatus = "partial"
		record.Completeness = "unknown"
		record.Cost = nil
		record.PartialCost = nil
		fingerprint := "missing:" + digest(j.ID+":"+item.Custom)
		if exists {
			fingerprint = result.Hash
			record.HTTPStatus = result.Status
			record.ObservationStatus = "complete"
			if result.Duplicate {
				record.ErrorStage = "duplicate_result"
				record.ObservationStatus = "partial"
			} else if !validJobUsage(result.Body) {
				record.ErrorStage = "invalid_usage"
				record.ObservationStatus = "partial"
			} else {
				raw, _ := json.Marshal(result.Body)
				observeNativeUsage(&record, raw)
				if idRaw, ok := result.Body["id"]; ok {
					native, idErr := resourceNativeID(idRaw)
					if idErr != nil {
						return idErr
					}
					record.ResponseID = native
				}
				record.Completeness = usageCompleteness(record.Usage)
				record.Cost = estimate(record.Usage, record.Price)
				record.PartialCost = estimatePartial(record.Usage, record.Price)
				record.ErrorStage = "upstream_http"
				if result.Status >= 200 && result.Status < 300 {
					record.Status = "succeeded"
					record.ErrorStage = ""
				}
				if record.Completeness == "complete" && record.Cost != nil {
					state = "settled"
				}
			}
		}
		if state != "settled" {
			pending = true
		}
		record.Ended = &now
		record.DurationMS = now.Sub(record.Started).Milliseconds()
		record.Version++
		record.DeliveryStatus = "batch_result"
		if exists {
			record.UpstreamStatus = "completed"
		} else {
			record.UpstreamStatus = "unknown"
		}
		if e = resourceRecordTx(tx, record); e != nil {
			return e
		}
		terminalRecords = append(terminalRecords, record)
		if record.ResponseID != "" {
			if e = resourceBindTx(tx, record, record.ResponseID); e != nil {
				return e
			}
		}
		res, e := tx.Exec("UPDATE job_items SET state=?,result_fingerprint=?,settled_at=? WHERE job_id=? AND custom_id=? AND result_fingerprint IS NULL", state, nullable(fingerprint), sql.NullString{String: now.Format(time.RFC3339Nano), Valid: state == "settled"}, j.ID, item.Custom)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return &resourceError{409, "Batch item 已被另一导入处理", "custom_id"}
		}
	}
	parent, e := resourceReadRecord(tx, j.RequestID)
	if e != nil {
		return e
	}
	var rawState string
	json.Unmarshal(wire["status"], &rawState)
	parent.Status = "failed"
	if rawState == "completed" && !pending {
		parent.Status = "succeeded"
	}
	parent.UpstreamStatus = rawState
	parent.Ended = &now
	parent.Cost = nil
	parent.Completeness = "unknown"
	parent.Version++
	if e = resourceRecordTx(tx, parent); e != nil {
		return e
	}
	state := rawState
	if pending {
		state = "pending_reconciliation"
	}
	fingerprint := digest(encode(struct {
		State string
		Items int
	}{rawState, len(items)}))
	_, e = tx.Exec("UPDATE jobs SET state=?,terminal_fingerprint=?,settled_at=?,next_poll_at=NULL,version=version+1,updated_at=? WHERE id=?", state, fingerprint, sql.NullString{String: now.Format(time.RFC3339Nano), Valid: !pending}, now.Format(time.RFC3339Nano), j.ID)
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	for _, record := range terminalRecords {
		a.observeExecution(record, src, true)
	}
	a.observeExecution(parent, src, true)
	return nil
}

// Root schedules this once per second. CAS claims prevent overlapping pollers; every remote action here is GET.
func (a *App) pollResourceJobs(ctx context.Context) error {
	rows, e := a.Store.DB.Query("SELECT id,key_id,kind FROM jobs WHERE native_id!='' AND terminal_fingerprint IS NULL AND next_poll_at IS NOT NULL AND next_poll_at<=? ORDER BY next_poll_at LIMIT 32", time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return e
	}
	type due struct{ id, key, kind string }
	items := []due{}
	for rows.Next() {
		var value due
		if e = rows.Scan(&value.id, &value.key, &value.kind); e != nil {
			break
		}
		items = append(items, value)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		j, e := a.resourceReadJob(item.id, item.key, item.kind)
		if e != nil {
			return e
		}
		claim, e := a.Store.DB.Exec("UPDATE jobs SET next_poll_at=?,version=version+1 WHERE id=? AND version=? AND terminal_fingerprint IS NULL", time.Now().UTC().Add(5*time.Minute).Format(time.RFC3339Nano), j.ID, j.Version)
		if e != nil {
			return e
		}
		n, _ := claim.RowsAffected()
		if n != 1 {
			continue
		}
		j.Version++
		var rawKey string
		var key ClientKey
		if e = a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", j.KeyID).Scan(&rawKey); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(rawKey), &key); e != nil {
			return e
		}
		src, e := a.resourceJobSource(key, j)
		if e != nil {
			a.deferResourcePoll(j)
			continue
		}
		a.mu.Lock()
		c := a.Config
		busy := false
		if key.RouteID != "" {
			route, routeErr := a.Store.route(key.RouteID)
			busy = routeErr != nil || route.MaxConcurrent != nil && a.routeActive[key.RouteID] >= *route.MaxConcurrent
		}
		if a.stopping || busy || len(a.slots) >= c.MaxConcurrent || key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent || src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
			a.mu.Unlock()
			a.deferResourcePoll(j)
			continue
		}
		a.slots <- struct{}{}
		a.accountActive[src.AccountID]++
		a.keyActive[key.ID]++
		a.ownedTasks.Add(1)
		if key.RouteID != "" {
			a.routeActive[key.RouteID]++
		}
		poll, cancel := context.WithTimeout(ctx, time.Duration(c.TotalTimeout)*time.Second)
		runningID := id("resource_poll")
		a.running[runningID] = cancel
		a.runningSources[runningID] = src.ID
		a.runningKeys[runningID] = key.ID
		a.mu.Unlock()
		func() {
			defer func() {
				cancel()
				a.mu.Lock()
				<-a.slots
				a.accountActive[src.AccountID]--
				a.keyActive[key.ID]--
				if key.RouteID != "" {
					a.routeActive[key.RouteID]--
				}
				delete(a.running, runningID)
				delete(a.runningSources, runningID)
				delete(a.runningKeys, runningID)
				a.mu.Unlock()
				a.ownedTasks.Done()
			}()
			path := "/batches/" + url.PathEscape(j.NativeID)
			if j.Kind == "background" {
				path = "/responses/" + url.PathEscape(j.NativeID)
			}
			resp, callErr := a.resourceCall(poll, src, "GET", path, nil, "")
			if callErr != nil {
				a.deferResourcePoll(j)
				return
			}
			wire, _, callErr := resourceJSON(resp, c.MaxResponse)
			if callErr != nil {
				a.deferResourcePoll(j)
				return
			}
			native, callErr := resourceNativeID(wire["id"])
			if callErr != nil || native != j.NativeID {
				a.deferResourcePoll(j)
				return
			}
			if j.Kind == "background" {
				callErr = a.observeBackgroundJob(j, wire)
			} else {
				callErr = a.observeBatchJob(poll, j, src, wire)
			}
			if callErr != nil {
				a.deferResourcePoll(j)
			}
		}()
	}
	return nil
}
func (a *App) deferResourcePoll(j resourceJob) {
	j.Metadata.PollFailures++
	_, e := a.Store.DB.Exec("UPDATE jobs SET state='unknown',metadata_json=?,next_poll_at=?,updated_at=?,version=version+1 WHERE id=? AND terminal_fingerprint IS NULL", encode(j.Metadata), jobPollTime(j.Metadata.PollFailures), time.Now().UTC().Format(time.RFC3339Nano), j.ID)
	if e != nil {
		a.storageFailed.Store(true)
	}
}

func (a *App) mapReturnedFileInputs(body map[string]json.RawMessage, key ClientKey, src Source) error {
	raw, ok := body["data"]
	if !ok {
		return nil
	}
	var items any
	if json.Unmarshal(raw, &items) != nil {
		return &resourceError{502, "input_items data 无效", ""}
	}
	var walk func(any) error
	walk = func(value any) error {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				if e := walk(child); e != nil {
					return e
				}
			}
		case map[string]any:
			kind, _ := item["type"].(string)
			if kind == "input_file" || kind == "input_image" {
				if native, ok := item["file_id"].(string); ok {
					var public string
					e := a.Store.DB.QueryRow("SELECT id FROM resources WHERE native_id=? AND kind='file' AND key_id=? AND source_id=? AND source_generation=? AND account_generation=?", native, key.ID, src.ID, src.Generation, src.AccountGeneration).Scan(&public)
					if e != nil {
						return &resourceError{502, "input_items 返回了未绑定的文件引用", "file_id"}
					}
					item["file_id"] = public
				}
			}
			for _, child := range item {
				if e := walk(child); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(items); e != nil {
		return e
	}
	body["data"] = json.RawMessage(encode(items))
	return nil
}

// Root calls this before the ordinary forward runner acquires any slot. False replays its bounded prefix.
func (a *App) backgroundResponsesEntry(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" || r.URL.Path != "/v1/responses" {
		return false
	}
	key, e := a.Store.keyByDigest(digest(bearer(r)))
	if e != nil || !keyValid(key, time.Now()) {
		if e != nil && e != sql.ErrNoRows {
			resourceFail(w, e)
		} else {
			fail(w, 401, "客户端 Key 无效或已撤销", "")
		}
		return true
	}
	if !allowed(key.ProtocolAllowlist, "responses") || !allowed(key.OperationAllowlist, "generate") {
		fail(w, 403, "Key 无生成协议或操作权限", "operation")
		return true
	}
	a.mu.Lock()
	c := a.Config
	a.mu.Unlock()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Duration(c.HeaderTimeout) * time.Second))
	original := r.Body
	prefix, e := io.ReadAll(io.LimitReader(original, nativeAdmissionLimit))
	_ = controller.SetReadDeadline(time.Time{})
	r.Body = &resourceReplayBody{Reader: io.MultiReader(bytes.NewReader(prefix), original), Closer: original}
	if e != nil {
		fail(w, 400, "请求元数据读取失败", "")
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	opening, e := decoder.Token()
	if e != nil || opening != json.Delim('{') {
		return false
	}
	background := false
	model := ""
	for decoder.More() {
		token, e := decoder.Token()
		if e != nil {
			break
		}
		name, ok := token.(string)
		if !ok {
			break
		}
		var raw json.RawMessage
		if e = decoder.Decode(&raw); e != nil {
			break
		}
		switch name {
		case "background":
			if json.Unmarshal(raw, &background) != nil {
				return false
			}
		case "model":
			json.Unmarshal(raw, &model)
		}
		if background && model != "" {
			break
		}
	}
	if !background {
		return false
	}
	if model == "" {
		fail(w, 422, "后台 model 与 background 元数据必须位于前 64 KiB，并置于大 input 前", "model")
		return true
	}
	if !allowed(key.OperationAllowlist, "background") || !allowed(key.ModelAllowlist, model) {
		fail(w, 403, "Key 无后台操作或模型权限", "operation")
		return true
	}
	a.mu.Lock()
	src, sent, e := a.resourceSource(key, "background", model)
	a.mu.Unlock()
	if e != nil {
		resourceFail(w, e)
		return true
	}
	c, ctx, release, e := a.resourceAdmit(r, key, src, "background")
	if e != nil {
		resourceFail(w, e)
		return true
	}
	defer release()
	stop := context.AfterFunc(ctx, func() { r.Body.Close() })
	defer stop()
	limit := nativeJSONLimit
	if c.MaxBody > 0 && c.MaxBody < limit {
		limit = c.MaxBody
	}
	raw, e := readLimited(r.Body, limit)
	if e != nil {
		fail(w, 413, "后台 body 超限、取消或中断，未提交", "")
		return true
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		fail(w, 400, "后台 JSON 无效", "")
		return true
	}
	var fullModel string
	var fullBackground, stream, store bool
	if json.Unmarshal(body["model"], &fullModel) != nil || fullModel != model || json.Unmarshal(body["background"], &fullBackground) != nil || !fullBackground {
		fail(w, 422, "后台 model/background 与已授权前缀不一致", "")
		return true
	}
	if value, ok := body["stream"]; ok && (json.Unmarshal(value, &stream) != nil || stream) {
		fail(w, 422, "后台创建子集只接受非流式 JSON", "stream")
		return true
	}
	if value, ok := body["store"]; ok && (json.Unmarshal(value, &store) != nil || !store) {
		fail(w, 422, "后台创建子集不能保证 store=false 持久轮询，未修改隐私选择", "store")
		return true
	}
	a.backgroundResponsesAdmitted(w, r, body, key, src, model, sent, c, ctx)
	return true
}

type resourceReplayBody struct {
	io.Reader
	io.Closer
}

func resourceValidateTools(body map[string]json.RawMessage) error {
	raw, ok := body["tools"]
	if !ok || string(raw) == "null" {
		return nil
	}
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return &resourceError{400, "tools 无效", "tools"}
	}
	for _, tool := range tools {
		var kind string
		if json.Unmarshal(tool["type"], &kind) != nil || kind != "function" {
			return &resourceError{422, "此资源 job 子集尚未闭合服务器工具的权限与额外费用；未提交", "tools"}
		}
	}
	return nil
}
