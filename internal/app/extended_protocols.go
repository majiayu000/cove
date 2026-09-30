package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Embed these fields in SourceModel and Record. Capabilities are per model,
// never inherited from another account or a subscription with the same brand.
type ExtendedModelCapabilities struct {
	NativeServerTools []string `json:"native_server_tools,omitempty"`
	OpaqueHistory     bool     `json:"opaque_history"`
	AdapterVersion    string   `json:"adapter_version,omitempty"`
}
type ExtendedRecordFields struct {
	CacheHit             bool    `json:"cache_hit,omitempty"`
	CacheOriginRequestID string  `json:"cache_origin_request_id,omitempty"`
	CachedResultUsage    *Usage  `json:"cached_result_usage,omitempty"`
	SavedEstimate        *string `json:"saved_estimate,omitempty"`
	ActualCost           *string `json:"actual_cost,omitempty"`
	CountedTokens        *int64  `json:"counted_tokens,omitempty"`
	ToolCostStatus       string  `json:"tool_cost_status,omitempty"`
}

const extendedProtocolSchema = `CREATE TABLE IF NOT EXISTS gemini_history_bindings(
 part_hash TEXT NOT NULL,key_id TEXT NOT NULL,source_id TEXT NOT NULL,
 account_id TEXT NOT NULL,generation INTEGER NOT NULL,account_generation INTEGER NOT NULL,
 model TEXT NOT NULL,created_at TEXT NOT NULL,
 PRIMARY KEY(part_hash,key_id,source_id,account_id,generation,account_generation,model));
CREATE TABLE IF NOT EXISTS response_cache(
 cache_key TEXT PRIMARY KEY,filename TEXT UNIQUE NOT NULL,size INTEGER NOT NULL,
 expires_at TEXT NOT NULL,last_access TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS response_cache_lru ON response_cache(last_access);`

func initializeExtendedProtocols(db *sql.DB) error {
	_, err := db.Exec(extendedProtocolSchema)
	return err
}

// Only resource-free native tool kinds have an implemented transport here.
// Resource tools stay closed until their kind-specific ownership adapter exists.
func validateExtendedServerTools(body map[string]json.RawMessage, src Source, model SourceModel) error {
	var tools []map[string]json.RawMessage
	if raw, ok := body["tools"]; ok && string(raw) != "null" {
		if json.Unmarshal(raw, &tools) != nil {
			return errors.New("tools 结构无效")
		}
	}
	var input []map[string]json.RawMessage
	_ = json.Unmarshal(body["input"], &input)
	for _, item := range input {
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		if kind == "additional_tools" {
			var declarations []map[string]json.RawMessage
			if json.Unmarshal(item["tools"], &declarations) != nil {
				return errors.New("additional_tools 结构无效")
			}
			tools = append(tools, declarations...)
		}
	}
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if src.NativeProtocol == "messages" && kind == "" {
			var name string
			if json.Unmarshal(tool["name"], &name) == nil && name != "" {
				continue
			}
		}
		if kind == "function" || kind == "custom" || kind == "namespace" {
			continue
		}
		if kind != "web_search" {
			return fmt.Errorf("服务端工具 %q 尚无已实现的资源/协议合同", kind)
		}
		if src.Kind != "api_key" || src.Provider != "openai" || src.NativeProtocol != "responses" || !slices.Contains(model.NativeServerTools, kind) {
			return fmt.Errorf("模型能力卡未声明原生服务端工具 %s", kind)
		}
	}
	return nil
}

// Call after normal input validation. This recognizer does not authorize tools.
func hasExtendedServerTools(body map[string]json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	_ = json.Unmarshal(body["tools"], &tools)
	var input []map[string]json.RawMessage
	_ = json.Unmarshal(body["input"], &input)
	for _, item := range input {
		var declarations []map[string]json.RawMessage
		if json.Unmarshal(item["tools"], &declarations) == nil {
			tools = append(tools, declarations...)
		}
	}
	for _, tool := range tools {
		var kind string
		_ = json.Unmarshal(tool["type"], &kind)
		if kind != "" && kind != "function" && kind != "custom" && kind != "namespace" {
			return true
		}
	}
	return false
}
func finalizeExtendedCost(rec *Record) {
	if rec.CacheHit {
		zero := "0"
		rec.Cost, rec.ActualCost, rec.PartialCost = &zero, &zero, nil
		return
	}
	if rec.ToolCostStatus == "unknown" {
		if rec.Cost != nil {
			rec.PartialCost = rec.Cost
		}
		rec.Cost, rec.ActualCost = nil, nil
	}
}

// ---------------- Native Gemini data plane ----------------

type geminiEndpoint struct {
	Model, Method, Operation string
	Stream                   bool
}

func parseGeminiEndpoint(r *http.Request) (geminiEndpoint, error) {
	var out geminiEndpoint
	if !strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		return out, errors.New("Gemini 路径不存在")
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
	model, method, ok := strings.Cut(rest, ":")
	if !ok || model == "" || len(model) > 200 || strings.ContainsAny(model, "/\\?#:") {
		return out, errors.New("Gemini 模型路径无效")
	}
	if method != "generateContent" && method != "streamGenerateContent" && method != "countTokens" {
		return out, errors.New("Gemini operation 未支持")
	}
	out = geminiEndpoint{Model: model, Method: method, Operation: "generate", Stream: method == "streamGenerateContent"}
	if method == "countTokens" {
		out.Operation = "count_tokens"
	}
	for key, values := range r.URL.Query() {
		if key != "alt" || !out.Stream || len(values) != 1 || values[0] != "sse" {
			return out, errors.New("Gemini query 只允许 stream 的 alt=sse；客户端 Key 必须使用认证头")
		}
	}
	return out, nil
}
func geminiAuth(r *http.Request) (string, error) {
	google := r.Header.Values("X-Goog-Api-Key")
	auth := r.Header.Values("Authorization")
	if len(google) > 1 || len(auth) > 1 {
		return "", errors.New("重复客户端认证头")
	}
	key := r.Header.Get("X-Goog-Api-Key")
	if len(auth) > 0 && (bearer(r) == "" || !strings.HasPrefix(auth[0], "Bearer ")) {
		return "", errors.New("Bearer 认证头无效")
	}
	if key != "" && len(auth) > 0 && key != bearer(r) {
		return "", errors.New("两种客户端认证头不一致")
	}
	if key == "" {
		key = bearer(r)
	}
	if key == "" {
		return "", errors.New("缺少客户端 Key")
	}
	return key, nil
}
func geminiFail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, geminiErrorBody(status, message))
}
func geminiErrorBody(status int, message string) map[string]any {
	code := map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND", 409: "FAILED_PRECONDITION", 413: "RESOURCE_EXHAUSTED", 422: "INVALID_ARGUMENT", 429: "RESOURCE_EXHAUSTED", 502: "UNAVAILABLE", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}[status]
	if code == "" {
		code = "UNKNOWN"
	}
	return map[string]any{"error": map[string]any{"code": status, "message": message, "status": code}}
}

type geminiErrors struct {
	http.ResponseWriter
	status int
}

func (w *geminiErrors) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *geminiErrors) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *geminiErrors) Write(body []byte) (int, error) {
	if w.status < 400 {
		return w.ResponseWriter.Write(body)
	}
	var value struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &value) != nil || value.Error.Status != "" {
		return w.ResponseWriter.Write(body)
	}
	message := value.Error.Message
	if message == "" {
		message = "网关或上游请求失败"
	}
	converted := []byte(encode(geminiErrorBody(w.status, message)) + "\n")
	n, err := w.ResponseWriter.Write(converted)
	if err == nil && n != len(converted) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(body), nil
}

func geminiBody(raw []byte, endpoint geminiEndpoint) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, errors.New("请求必须为 JSON 对象")
	}
	if v, ok := body["model"]; ok {
		var model string
		if json.Unmarshal(v, &model) != nil || strings.TrimPrefix(model, "models/") != endpoint.Model {
			return nil, errors.New("路径 model 与 body model 不一致")
		}
	}
	delete(body, "model") // REST model is a path parameter, not an upstream credential or body override.
	if endpoint.Operation == "count_tokens" {
		if nested, ok := body["generateContentRequest"]; ok {
			if _, also := body["contents"]; also {
				return nil, errors.New("contents 与 generateContentRequest 不得同时出现")
			}
			var request map[string]json.RawMessage
			if json.Unmarshal(nested, &request) != nil || request == nil {
				return nil, errors.New("generateContentRequest 结构无效")
			}
			if model, ok := request["model"]; ok {
				var value string
				if json.Unmarshal(model, &value) != nil || strings.TrimPrefix(value, "models/") != endpoint.Model {
					return nil, errors.New("计数请求 model 与路径不一致")
				}
			}
			request["model"] = json.RawMessage(encode("models/" + endpoint.Model))
			body["generateContentRequest"] = json.RawMessage(encode(request))
		}
	}
	return body, nil
}
func geminiRequestContent(body map[string]json.RawMessage) map[string]json.RawMessage {
	var nested map[string]json.RawMessage
	if json.Unmarshal(body["generateContentRequest"], &nested) == nil && nested != nil {
		return nested
	}
	return body
}

// Native function history is matched by explicit IDs when supplied. Name-only
// responses are safe only with a single pending call of that name.
func geminiParts(body map[string]json.RawMessage) ([]json.RawMessage, error) {
	request := geminiRequestContent(body)
	if _, ok := request["cachedContent"]; ok {
		return nil, &selectionError{422, "cachedContent 尚无资源归属 adapter"}
	}
	var contents []struct {
		Role  string            `json:"role"`
		Parts []json.RawMessage `json:"parts"`
	}
	if json.Unmarshal(request["contents"], &contents) != nil || len(contents) == 0 {
		return nil, errors.New("contents 必须包含原生 Content")
	}
	pending := map[string][]string{}
	all := []json.RawMessage{}
	for _, content := range contents {
		if content.Role != "" && content.Role != "user" && content.Role != "model" {
			return nil, errors.New("contents role 未支持")
		}
		if len(content.Parts) == 0 {
			return nil, errors.New("Content.parts 不能为空")
		}
		for _, part := range content.Parts {
			var fields map[string]json.RawMessage
			if json.Unmarshal(part, &fields) != nil || fields == nil {
				return nil, errors.New("Part 结构无效")
			}
			if _, ok := fields["fileData"]; ok {
				return nil, &selectionError{422, "fileData 尚无文件资源归属 adapter"}
			}
			for _, field := range []string{"functionCall", "functionResponse"} {
				if value, ok := fields[field]; ok {
					var function struct{ Name, ID string }
					if json.Unmarshal(value, &function) != nil || function.Name == "" {
						return nil, errors.New(field + " 缺少 name")
					}
					if field == "functionCall" {
						if function.ID != "" && slices.Contains(pending[function.Name], function.ID) {
							return nil, errors.New("重复 functionCall id")
						}
						pending[function.Name] = append(pending[function.Name], function.ID)
					} else {
						calls := pending[function.Name]
						index := -1
						if function.ID != "" {
							index = slices.Index(calls, function.ID)
						} else if len(calls) == 1 {
							index = 0
						}
						if index < 0 {
							return nil, errors.New("functionResponse 不能唯一匹配原生工具调用；并行同名调用需要明确 id")
						}
						pending[function.Name] = slices.Delete(calls, index, index+1)
					}
				}
			}
			all = append(all, part)
		}
	}
	return all, nil
}
func geminiSignedPart(raw json.RawMessage) (string, bool, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return "", false, errors.New("Part 结构无效")
	}
	signature, ok := fields["thoughtSignature"]
	if !ok {
		return "", false, nil
	}
	var value string
	if json.Unmarshal(signature, &value) != nil || value == "" {
		return "", true, errors.New("thoughtSignature 必须为非空字符串")
	}
	canonical, err := canonicalCacheJSON(raw)
	return digest(string(canonical)), true, err
}
func (s *Store) verifyGeminiHistory(parts []json.RawMessage, key ClientKey, src Source, model SourceModel) error {
	for _, part := range parts {
		hash, signed, err := geminiSignedPart(part)
		if err != nil {
			return err
		}
		if !signed {
			continue
		}
		if !model.OpaqueHistory {
			return errors.New("模型能力卡未支持 opaque history；请选择原生会话来源")
		}
		var count int
		err = s.DB.QueryRow(`SELECT count(*) FROM gemini_history_bindings WHERE part_hash=? AND key_id=? AND source_id=? AND account_id=? AND generation=? AND account_generation=? AND model=?`, hash, key.ID, src.ID, src.AccountID, src.Generation, src.AccountGeneration, model.UpstreamModel).Scan(&count)
		if err != nil {
			return &selectionError{503, storageError().Error()}
		}
		if count != 1 {
			return errors.New("签名历史不属于当前 Key、来源、账号代次与模型；请建立新会话")
		}
	}
	return nil
}
func validateGeminiTools(body map[string]json.RawMessage, src Source, model SourceModel) (bool, error) {
	var tools []map[string]json.RawMessage
	request := geminiRequestContent(body)
	if raw, ok := request["tools"]; ok && json.Unmarshal(raw, &tools) != nil {
		return false, errors.New("tools 结构无效")
	}
	server := false
	for _, tool := range tools {
		for kind, raw := range tool {
			if kind == "functionDeclarations" {
				var declarations []map[string]json.RawMessage
				if json.Unmarshal(raw, &declarations) != nil {
					return false, errors.New("functionDeclarations 结构无效")
				}
				continue
			}
			if kind != "googleSearch" && kind != "codeExecution" {
				return false, fmt.Errorf("Gemini 服务端工具 %s 尚未支持", kind)
			}
			if src.Kind != "api_key" || src.Provider != "gemini" || !slices.Contains(model.NativeServerTools, kind) {
				return false, fmt.Errorf("模型能力卡未声明原生工具 %s", kind)
			}
			server = true
		}
	}
	return server, nil
}

type geminiObserver struct {
	store    *Store
	key      ClientKey
	source   Source
	model    SourceModel
	record   *Record
	terminal map[int]bool
	seen     map[int]bool
	skipped  bool
}

func (o *geminiObserver) observe(raw []byte) error {
	var value struct {
		ResponseID string `json:"responseId"`
		Model      string `json:"modelVersion"`
		Candidates []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage struct {
			Input     *int64 `json:"promptTokenCount"`
			Output    *int64 `json:"candidatesTokenCount"`
			Cached    *int64 `json:"cachedContentTokenCount"`
			Reasoning *int64 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return errors.New("Gemini event 不是合法 JSON")
	}
	if value.ResponseID != "" {
		o.record.ResponseID = value.ResponseID
	}
	if value.Model != "" {
		o.record.ReportedModel = value.Model
	}
	for _, pair := range []struct {
		to   **int64
		from *int64
	}{{&o.record.Usage.Input, value.Usage.Input}, {&o.record.Usage.Output, value.Usage.Output}, {&o.record.Usage.Cached, value.Usage.Cached}, {&o.record.Usage.Reasoning, value.Usage.Reasoning}} {
		if pair.from != nil && *pair.from >= 0 {
			*pair.to = pair.from
		}
	}
	if value.Usage.Output != nil && value.Usage.Reasoning != nil {
		total := *value.Usage.Output + *value.Usage.Reasoning
		if total < 0 {
			return errors.New("Gemini token usage 超出有效范围")
		}
		o.record.Usage.Output = &total
	}
	if value.PromptFeedback.BlockReason != "" {
		o.record.Status = "succeeded"
		o.record.UpstreamStatus = "safety_blocked"
	}
	for _, candidate := range value.Candidates {
		o.seen[candidate.Index] = true
		if candidate.FinishReason != "" {
			o.terminal[candidate.Index] = true
		}
		for _, part := range candidate.Content.Parts {
			hash, signed, err := geminiSignedPart(part)
			if err != nil {
				return err
			}
			if !signed {
				continue
			}
			if !o.model.OpaqueHistory {
				return errors.New("上游返回签名，但模型能力卡未支持 opaque history")
			}
			_, err = o.store.DB.Exec(`INSERT OR IGNORE INTO gemini_history_bindings(part_hash,key_id,source_id,account_id,generation,account_generation,model,created_at) VALUES(?,?,?,?,?,?,?,?)`, hash, o.key.ID, o.source.ID, o.source.AccountID, o.source.Generation, o.source.AccountGeneration, o.model.UpstreamModel, time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				return &selectionError{503, storageError().Error()}
			}
		}
	}
	return nil
}
func (o *geminiObserver) finish() {
	if o.record.Status == "succeeded" {
		return
	}
	if !o.skipped && len(o.seen) > 0 && len(o.seen) == len(o.terminal) {
		o.record.Status = "succeeded"
		o.record.UpstreamStatus = "completed"
	}
}

func (a *App) geminiData(w http.ResponseWriter, r *http.Request) {
	endpoint, err := parseGeminiEndpoint(r)
	if err != nil {
		geminiFail(w, 400, err.Error())
		return
	}
	if r.Method != "POST" {
		geminiFail(w, 405, "方法不支持")
		return
	}
	clientSecret, err := geminiAuth(r)
	if err != nil {
		geminiFail(w, 401, err.Error())
		return
	}
	if endpoint.Operation == "generate" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+clientSecret)
		r.Header.Del("X-Goog-Api-Key")
		a.forward(&geminiErrors{ResponseWriter: w}, r, "")
		return
	}
	a.mu.Lock()
	cfg := a.Config
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.TotalTimeout)*time.Second)
	defer cancel()
	key, err := a.Store.keyByDigest(digest(clientSecret))
	if a.stopping || a.storageFailed.Load() {
		a.mu.Unlock()
		geminiFail(w, 503, "服务正在关闭或存储异常")
		return
	}
	if err != nil || !keyValid(key, time.Now()) {
		a.mu.Unlock()
		geminiFail(w, 401, "客户端 Key 无效或已撤销")
		return
	}
	if !allowed(key.ProtocolAllowlist, "gemini") || !allowed(key.OperationAllowlist, endpoint.Operation) || !allowed(key.ModelAllowlist, endpoint.Model) {
		a.mu.Unlock()
		geminiFail(w, 403, "Key 无此模型、协议或操作权限")
		return
	}
	if len(a.slots) >= cfg.MaxConcurrent || key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent {
		a.mu.Unlock()
		geminiFail(w, 429, "本机或 Key 并发容量已满")
		return
	}
	a.slots <- struct{}{}
	a.keyActive[key.ID]++
	a.mu.Unlock()
	defer func() { a.mu.Lock(); <-a.slots; a.keyActive[key.ID]--; a.signalAdmission(); a.mu.Unlock() }()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Duration(cfg.HeaderTimeout) * time.Second))
	stopRead := context.AfterFunc(ctx, func() { _ = controller.SetReadDeadline(time.Now()) })
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.MaxBody))
	stopRead()
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		status, message := 400, "请求体读取失败"
		var limit *http.MaxBytesError
		var timeout net.Error
		if errors.As(err, &limit) {
			status, message = 413, "请求体超出本机限制"
		} else if errors.As(err, &timeout) && timeout.Timeout() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, message = 408, "请求体读取超时"
		}
		geminiFail(w, status, message)
		return
	}
	body, err := geminiBody(raw, endpoint)
	if err != nil {
		geminiFail(w, 400, err.Error())
		return
	}
	parts, err := geminiParts(body)
	if err != nil {
		status := 400
		var unsupported *selectionError
		if errors.As(err, &unsupported) {
			status = unsupported.Status
		}
		geminiFail(w, status, err.Error())
		return
	}
	a.mu.Lock()
	reject := func(code int, message string) { a.mu.Unlock(); geminiFail(w, code, message) }
	currentKey, keyErr := a.Store.keyByDigest(digest(clientSecret))
	if keyErr != nil || !keyValid(currentKey, time.Now()) || currentKey.Version != key.Version {
		reject(409, "Key 已修改，请重新发送请求")
		return
	}
	if a.stopping || a.storageFailed.Load() {
		reject(503, "服务正在关闭或存储异常")
		return
	}
	// The existing route selector can select native Gemini candidates without
	// invoking any cross-protocol converter or retrying a dispatched generation.
	excluded := map[string]bool{}
	sources, err := a.Store.sources()
	if err != nil {
		reject(503, storageError().Error())
		return
	}
	for _, source := range sources {
		if source.Kind != "api_key" || source.NativeProtocol != "gemini" || source.Provider != "gemini" || !slices.Contains(source.NativeOperations, endpoint.Operation) {
			excluded[source.ID] = true
		}
	}
	var src Source
	var sent string
	var reasons []CandidateReason
	if key.RouteID == "" {
		src, err = a.Store.source(key.SourceID)
		sent = endpoint.Model
	} else {
		src, sent, reasons, err = a.selectSourceExcluding(key, endpoint.Model, "gemini", false, excluded)
	}
	if err != nil || src.Deleted || !src.Enabled || !src.Configured || src.AuthStatus == "rejected" || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" {
		reject(503, "没有可用的原生 Gemini 来源")
		return
	}
	if excluded[src.ID] {
		reject(422, "该来源未实现 Gemini 原生 operation；跨协议转换尚未启用")
		return
	}
	candidate, err := a.candidateFor(src, sent)
	if err != nil || !candidate.Model.Enabled {
		reject(422, "所选原生模型不存在或已停用")
		return
	}
	src = candidate.Source
	if err = a.Store.verifyGeminiHistory(parts, key, src, candidate.Model); err != nil {
		status := 409
		var boundary *selectionError
		if errors.As(err, &boundary) {
			status = boundary.Status
		}
		reject(status, err.Error())
		return
	}
	serverTools, err := validateGeminiTools(body, src, candidate.Model)
	if err != nil {
		reject(422, err.Error())
		return
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		reject(429, "账号并发已满")
		return
	}
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e != nil {
			reject(503, storageError().Error())
			return
		}
		if route.MaxConcurrent != nil && a.routeActive[key.RouteID] >= *route.MaxConcurrent {
			reject(429, "路由并发已满")
			return
		}
	}
	secret, err := a.Secrets.Get(src.CredentialRef)
	if err != nil {
		reject(503, "上游凭据不可用")
		return
	}
	if request, ok := body["generateContentRequest"]; ok {
		var nested map[string]json.RawMessage
		_ = json.Unmarshal(request, &nested)
		nested["model"] = json.RawMessage(encode("models/" + sent))
		body["generateContentRequest"] = json.RawMessage(encode(nested))
	}
	raw = []byte(encode(body))
	accountingBody := map[string]json.RawMessage{"input": json.RawMessage(encode(string(raw)))}
	var generation map[string]json.RawMessage
	_ = json.Unmarshal(geminiRequestContent(body)["generationConfig"], &generation)
	if cap, ok := generation["maxOutputTokens"]; ok {
		accountingBody["max_output_tokens"] = cap
	}
	amount, provenance, err := reservationEstimate(accountingBody, raw)
	if endpoint.Operation == "count_tokens" {
		amount = 0
		provenance = "native_count_only"
	}
	if err != nil {
		reject(400, err.Error())
		return
	}
	if ok, retry := a.checkTPM(key, amount, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		reject(429, "Key token 窗口达到上限")
		return
	}
	var plan *AccountingPlan
	if endpoint.Operation == "generate" {
		plan, err = a.prepareAccounting(key, src, accountingBody)
		if err != nil {
			a.mu.Unlock()
			accountingFailure(w, err)
			return
		}
	}
	if ok, retry := a.consumeRPM(key, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		reject(429, "Key 请求速率达到上限")
		return
	}
	if !a.policyState.ClaimProbe(candidate, time.Now()) {
		a.refundRPM(key)
		reject(429, "来源冷却或恢复探测已占用")
		return
	}
	now := time.Now().UTC()
	rec := Record{ID: id("req"), AttemptID: id("att"), Sequence: 1, Version: 1, Origin: "client", KeyID: key.ID, ClientName: key.Name, Fingerprint: key.Fingerprint, KeyVersion: key.Version, SourceID: src.ID, SourceName: src.Name, SourceVersion: src.Version, Generation: src.Generation, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, RouteID: key.RouteID, ModelID: candidate.Model.ID, Model: endpoint.Model, SentModel: sent, Protocol: "gemini", Operation: endpoint.Operation, Started: now, AttemptStarted: now, Status: "dispatching", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", Completeness: "unknown", RequestBytes: int64(len(raw)), Price: src.Price, Submission: "not_sent", Accounting: plan, ReservedTokens: amount, TokenReservationSource: provenance, SelectionReasons: reasons}
	rec.TraceParent = r.Header.Get("Traceparent")
	if key.RouteID != "" {
		if route, e := a.Store.route(key.RouteID); e == nil {
			rec.RouteVersion = route.Version
		}
	}
	if serverTools {
		rec.ToolCostStatus = "unknown"
	}
	if settings, e := a.Store.readRuntimeSettings(); e == nil {
		rec.ConfigVersion = settings.Version
	}
	if err = a.Store.record(rec); err != nil {
		a.refundRPM(key)
		a.policyState.ReleaseProbe(candidate)
		reject(503, storageError().Error())
		return
	}
	a.reserveTPM(key, rec.ID, amount, now)
	a.running[rec.ID] = cancel
	a.runningSources[rec.ID] = src.ID
	a.runningKeys[rec.ID] = key.ID
	a.accountActive[src.AccountID]++
	if key.RouteID != "" {
		a.routeActive[key.RouteID]++
	}
	a.mu.Unlock()
	w.Header().Set("X-Gateway-Request-Id", rec.ID)
	var idleExpired atomic.Bool
	defer func() {
		end := time.Now().UTC()
		rec.Ended = &end
		rec.DurationMS = end.Sub(rec.Started).Milliseconds()
		if rec.Status == "dispatching" || rec.Status == "streaming" {
			rec.Status = "failed"
			rec.ErrorSummary = "上游未提供完整终态；执行与费用可能未知"
		}
		if ctx.Err() != nil {
			rec.Status = "cancelled"
			rec.ErrorStage = "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || idleExpired.Load() {
				rec.Status = "failed"
				rec.ErrorStage = "timeout"
			}
		}
		rec.Completeness = usageCompleteness(rec.Usage)
		if rec.Submission == "not_sent" {
			zero := "0"
			rec.Cost = &zero
		} else if endpoint.Operation == "generate" {
			rec.Cost = estimate(rec.Usage, rec.Price)
			if rec.Cost == nil {
				rec.PartialCost = estimatePartial(rec.Usage, rec.Price)
			}
		}
		finalizeExtendedCost(&rec)
		if err := a.Store.record(rec); err != nil {
			a.markStorageFailure()
		} else {
			a.observeExecution(rec, src, true)
		}
		if !rec.CacheHit {
			a.policyState.ObserveAttempt(candidate, rec, "", "", false, end)
		} else {
			a.policyState.ReleaseProbe(candidate)
		}
		a.mu.Lock()
		a.settleTPM(key, rec)
		delete(a.running, rec.ID)
		delete(a.runningSources, rec.ID)
		delete(a.runningKeys, rec.ID)
		a.accountActive[src.AccountID]--
		if key.RouteID != "" {
			a.routeActive[key.RouteID]--
		}
		a.signalAdmission()
		if rec.HTTPStatus == 401 && !rec.CacheHit {
			current, e := a.Store.source(src.ID)
			if e == nil && current.Generation == src.Generation && current.AccountGeneration == src.AccountGeneration && current.Version == src.Version {
				current.AuthStatus = "rejected"
				if e = a.Store.saveSource(current); e != nil {
					a.markStorageFailure()
				}
			}
		}
		a.mu.Unlock()
	}()
	a.mu.Lock()
	var cache *ResponseCacheDescriptor
	var cacheErr error
	if !endpoint.Stream {
		cache, cacheErr = a.responseCacheDescriptor(key, src, candidate.Model, rec, raw, http.Header{})
	}
	var cached *ResponseCacheEntry
	if cacheErr == nil {
		cached, cacheErr = a.lookupResponseCache(cache)
	}
	a.mu.Unlock()
	if cacheErr != nil {
		w.Header().Set("X-Cove-Cache", "bypass;storage_error")
	} else if cached != nil {
		_ = replayResponseCache(w, cached, "gemini", &rec)
		return
	} else if cache != nil {
		w.Header().Set("X-Cove-Cache", "miss")
	} else {
		w.Header().Set("X-Cove-Cache", "bypass")
	}
	path := "/v1beta/models/" + url.PathEscape(sent) + ":" + endpoint.Method
	base, parseErr := url.Parse(src.BaseURL)
	if parseErr != nil || base.RawQuery != "" || base.User != nil || base.Fragment != "" {
		rec.Status = "failed"
		geminiFail(w, 502, "上游 base URL 无效")
		return
	}
	address := strings.TrimRight(src.BaseURL, "/")
	if strings.HasSuffix(address, "/v1beta") {
		address = strings.TrimSuffix(address, "/v1beta")
	}
	address += path
	if endpoint.Stream {
		address += "?alt=sse"
	}
	request, err := http.NewRequestWithContext(ctx, "POST", address, bytes.NewReader(raw))
	if err != nil {
		geminiFail(w, 502, "上游地址无效")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Goog-Api-Key", secret)
	if endpoint.Stream {
		request.Header.Set("Accept", "text/event-stream")
	}
	rec.Submission = "possible"
	if err = a.Store.record(rec); err != nil {
		rec.Submission = "not_sent"
		rec.ErrorStage = "attempt_store"
		a.markStorageFailure()
		geminiFail(w, 503, storageError().Error())
		return
	}
	response, err := a.doUpstream(request, src)
	if err != nil {
		rec.ErrorStage = "transport"
		geminiFail(w, 502, "上游连接失败；未重试，执行与费用可能未知")
		return
	}
	defer response.Body.Close()
	rec.HTTPStatus = response.StatusCode
	rec.UpstreamRequestID = redact(response.Header.Get("X-Request-Id"), secret, clientSecret)
	for _, header := range []string{"Content-Type", "Retry-After"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, redact(value, secret, clientSecret))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	idle := time.AfterFunc(time.Duration(cfg.IdleTimeout)*time.Second, func() { idleExpired.Store(true); cancel() })
	defer idle.Stop()
	writeDone := make(chan struct{})
	stopWrite := context.AfterFunc(ctx, func() { defer close(writeDone); _ = controller.SetWriteDeadline(time.Now()) })
	defer func() {
		if !stopWrite() {
			<-writeDone
		}
	}()
	write := func(payload []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = controller.SetWriteDeadline(time.Now().Add(time.Duration(cfg.IdleTimeout) * time.Second))
		_, e := w.Write(payload)
		if e != nil {
			rec.DeliveryStatus = "failed"
			rec.ErrorStage = "downstream_write"
		}
		return e
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		rec.Status = "failed"
		rec.ErrorStage = "redirect"
		geminiFail(w, 502, "拒绝上游重定向")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		b, e := readLimited(response.Body, cfg.MaxResponse)
		rec.Status = "failed"
		rec.UpstreamStatus = "rejected"
		rec.ErrorStage = "upstream_http"
		if e != nil {
			geminiFail(w, 502, "上游错误响应读取失败")
			return
		}
		w.WriteHeader(response.StatusCode)
		if write([]byte(redact(string(b), secret, clientSecret))) == nil {
			rec.DeliveryStatus = "completed"
		}
		return
	}
	observer := geminiObserver{store: a.Store, key: key, source: src, model: candidate.Model, record: &rec, terminal: map[int]bool{}, seen: map[int]bool{}}
	if !endpoint.Stream {
		b, e := readLimited(response.Body, cfg.MaxResponse)
		if e != nil || !json.Valid(b) {
			geminiFail(w, 502, "上游响应不完整或 JSON 无效")
			return
		}
		if endpoint.Operation == "count_tokens" {
			var result struct {
				Total *int64 `json:"totalTokens"`
			}
			if json.Unmarshal(b, &result) != nil || result.Total == nil || *result.Total < 0 {
				geminiFail(w, 502, "原生 countTokens 未返回有效 totalTokens")
				return
			}
			rec.CountedTokens = result.Total
			rec.Status = "succeeded"
			rec.UpstreamStatus = "completed"
		} else {
			if e = observer.observe(b); e != nil {
				status := 502
				var boundary *selectionError
				if errors.As(e, &boundary) {
					status = boundary.Status
					a.markStorageFailure()
				}
				geminiFail(w, status, e.Error())
				return
			}
			observer.finish()
		}
		if rec.Status != "succeeded" {
			geminiFail(w, 502, "上游响应未提供完整 candidate 终态")
			return
		}
		rec.ResponseBytes = int64(len(b))
		w.WriteHeader(response.StatusCode)
		if write(b) == nil {
			rec.DeliveryStatus = "completed"
		}
		if cacheErr == nil && cache != nil {
			a.mu.Lock()
			if storeErr := a.storeResponseCache(cache, b, response.Header.Get("Content-Type"), rec); storeErr != nil {
				rec.ObservationStatus = "partial"
				rec.ErrorSummary = "上游响应已交付，但本地结果缓存保存失败"
			}
			a.mu.Unlock()
		}
		return
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		geminiFail(w, 502, "Gemini 流未返回 SSE")
		return
	}
	w.WriteHeader(response.StatusCode)
	rec.Status = "streaming"
	sse := newSSE(cfg.MaxEvent, func(frame []byte) error {
		data, _ := sseData(frame)
		if len(data) > 0 {
			if e := observer.observe(data); e != nil {
				return e
			}
		}
		if e := write(frame); e != nil {
			return e
		}
		return controller.Flush()
	}, func(frame []byte) error {
		observer.skipped = true
		rec.ObservationStatus = "partial"
		if e := write(frame); e != nil {
			return e
		}
		return controller.Flush()
	})
	buffer := make([]byte, 8192)
	for {
		n, e := response.Body.Read(buffer)
		if n > 0 {
			idle.Reset(time.Duration(cfg.IdleTimeout) * time.Second)
			rec.ResponseBytes += int64(n)
			if frameErr := sse.Feed(buffer[:n]); frameErr != nil {
				rec.ErrorStage = "stream_observation"
				return
			}
		}
		if e != nil {
			if e != io.EOF {
				rec.ErrorStage = "stream_transport"
				return
			}
			break
		}
	}
	if err = sse.End(); err != nil {
		return
	}
	observer.skipped = observer.skipped || sse.Skipped
	observer.finish()
	rec.DeliveryStatus = "completed"
}

// ---------------- Explicit opt-in local result cache ----------------

const responseCacheLimit int64 = 100 << 20
const responseCacheResultLimit int64 = 1 << 20
const responseCacheEnvelopeLimit int64 = ((responseCacheResultLimit+2)/3)*4 + (64 << 10)
const extendedAdapterVersion = "cove-gemini-native-v1.2-1"
const cacheTimeFormat = "2006-01-02T15:04:05.000000000Z"

type ResponseCacheSettings struct {
	Version    int      `json:"version"`
	SourceIDs  []string `json:"source_ids"`
	RouteIDs   []string `json:"route_ids"`
	TTLMinutes int      `json:"ttl_minutes"`
}

func (s *Store) responseCacheSettings() (ResponseCacheSettings, error) {
	out := ResponseCacheSettings{Version: 1, SourceIDs: []string{}, RouteIDs: []string{}, TTLMinutes: 5}
	var raw string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key='response_cache'").Scan(&raw)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if json.Unmarshal([]byte(raw), &out) != nil || out.TTLMinutes < 1 || out.TTLMinutes > 60 {
		return out, storageError()
	}
	return out, nil
}
func (a *App) extendedProtocolsAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/admin/response-cache" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.Store.responseCacheSettings()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return true
	}
	if r.Method == "GET" {
		var count, size int64
		if err = a.Store.DB.QueryRow("SELECT count(*),coalesce(sum(size),0) FROM response_cache").Scan(&count, &size); err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		writeJSON(w, 200, map[string]any{"settings": current, "entries": count, "bytes": size, "capacity_bytes": responseCacheLimit, "result_limit_bytes": responseCacheResultLimit, "privacy": "显式启用会在本机私有目录保存输入 hash 和输出正文；这是复用旧结果，不保证模型确定性。默认关闭。"})
		return true
	}
	if r.Method == "DELETE" {
		if err = a.clearResponseCache(); err != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		writeJSON(w, 200, map[string]bool{"cleared": true})
		return true
	}
	if r.Method != "PUT" {
		fail(w, 405, "方法不支持", "")
		return true
	}
	var in struct {
		ResponseCacheSettings
		Consent bool `json:"save_output_consent"`
	}
	if !decode(w, r, &in) {
		return true
	}
	if in.Version != current.Version {
		fail(w, 409, "缓存设置已改变，请刷新", "version")
		return true
	}
	if in.TTLMinutes < 1 || in.TTLMinutes > 60 {
		fail(w, 400, "TTL 必须为1到60分钟", "ttl_minutes")
		return true
	}
	if (len(in.SourceIDs) > 0 || len(in.RouteIDs) > 0) && !in.Consent {
		fail(w, 400, "启用结果缓存需要明确同意保存输入 hash 和输出正文", "save_output_consent")
		return true
	}
	for _, sid := range in.SourceIDs {
		src, e := a.Store.source(sid)
		if e != nil || src.Deleted {
			fail(w, 400, "缓存来源不存在", "source_ids")
			return true
		}
	}
	for _, rid := range in.RouteIDs {
		if _, e := a.Store.route(rid); e != nil {
			fail(w, 400, "缓存路由不存在", "route_ids")
			return true
		}
	}
	// Clear before changing enablement: disabling cannot leave a late request's
	// old snapshot writable. Store and lookup both compare this settings version.
	if err = a.clearResponseCache(); err != nil {
		fail(w, 503, storageError().Error(), "")
		return true
	}
	in.Version++
	if _, err = a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('response_cache',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(in.ResponseCacheSettings)); err != nil {
		fail(w, 503, storageError().Error(), "")
		return true
	}
	writeJSON(w, 200, in.ResponseCacheSettings)
	return true
}

type ResponseCacheDescriptor struct {
	KeyID             string            `json:"key_id"`
	KeyVersion        int               `json:"key_version"`
	SourceID          string            `json:"source_id"`
	SourceVersion     int               `json:"source_version"`
	SourceGeneration  int               `json:"source_generation"`
	AccountID         string            `json:"account_id"`
	AccountGeneration int               `json:"account_generation"`
	PublicModel       string            `json:"public_model"`
	SentModel         string            `json:"sent_model"`
	ModelVersion      int               `json:"model_version"`
	NativeProtocol    string            `json:"native_protocol"`
	ClientProtocol    string            `json:"client_protocol"`
	Operation         string            `json:"operation"`
	AdapterVersion    string            `json:"adapter_version"`
	ConfigVersion     int               `json:"config_version"`
	RouteVersion      int               `json:"route_version"`
	RouteID           string            `json:"route_id"`
	Price             *Price            `json:"price"`
	SemanticHeaders   map[string]string `json:"semantic_headers"`
	SettingsVersion   int               `json:"settings_version"`
	TTLMinutes        int               `json:"-"`
	CacheKey          string            `json:"-"`
}
type ResponseCacheEntry struct {
	Body            []byte  `json:"body_base64"`
	ContentType     string  `json:"content_type"`
	OriginRequestID string  `json:"origin_request_id"`
	Usage           Usage   `json:"usage"`
	SavedEstimate   *string `json:"saved_estimate"`
	BodyHash        string  `json:"body_hash"`
}

func canonicalCacheJSON(raw []byte) ([]byte, error) {
	unique := json.NewDecoder(bytes.NewReader(raw))
	unique.UseNumber()
	if err := uniqueCacheJSON(unique); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("JSON 包含额外内容")
	}
	return json.Marshal(value) // map keys only are sorted; numbers and arrays retain their exact semantic order/value.
}
func uniqueCacheJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return errors.New("重复 JSON 字段不能安全复用")
			}
			seen[key] = true
			if err = uniqueCacheJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err = uniqueCacheJSON(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON 结构无效")
	}
	_, err = decoder.Token()
	return err
}
func cacheOnlyKeys(value map[string]json.RawMessage, names ...string) bool {
	for key := range value {
		if !slices.Contains(names, key) {
			return false
		}
	}
	return true
}
func cacheTextParts(raw json.RawMessage, protocol string) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if protocol == "gemini" {
			if !cacheOnlyKeys(part, "text") || json.Unmarshal(part["text"], &text) != nil {
				return false
			}
			continue
		}
		var kind string
		_ = json.Unmarshal(part["type"], &kind)
		if !cacheOnlyKeys(part, "type", "text", "annotations", "logprobs") || kind != "text" && kind != "input_text" && kind != "output_text" || json.Unmarshal(part["text"], &text) != nil {
			return false
		}
		for _, field := range []string{"annotations", "logprobs"} {
			if value, ok := part[field]; ok {
				var empty []any
				if kind != "output_text" || json.Unmarshal(value, &empty) != nil || len(empty) > 0 {
					return false
				}
			}
		}
	}
	return true
}
func cacheMessages(raw json.RawMessage, protocol string) bool {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		var role, kind string
		_ = json.Unmarshal(message["role"], &role)
		_ = json.Unmarshal(message["type"], &kind)
		if protocol == "gemini" {
			if !cacheOnlyKeys(message, "role", "parts") || role != "" && role != "user" || !cacheTextParts(message["parts"], protocol) {
				return false
			}
			continue
		}
		if !cacheOnlyKeys(message, "role", "type", "content") || kind != "" && kind != "message" || role != "user" && role != "system" && role != "developer" || !cacheTextParts(message["content"], protocol) {
			return false
		}
	}
	return true
}
func responseCacheEligible(raw []byte, protocol, operation string) bool {
	if operation != "generate" {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return false
	}
	var stream bool
	if value, ok := body["stream"]; ok && (json.Unmarshal(value, &stream) != nil || stream) {
		return false
	}
	var temperature float64
	if protocol == "gemini" {
		if !cacheOnlyKeys(body, "contents", "systemInstruction", "generationConfig") {
			return false
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(body["generationConfig"], &config) != nil || !cacheOnlyKeys(config, "temperature", "maxOutputTokens", "topP", "topK", "stopSequences", "candidateCount") {
			return false
		}
		if json.Unmarshal(config["temperature"], &temperature) != nil || temperature != 0 || !cacheMessages(body["contents"], protocol) {
			return false
		}
		if value, ok := config["candidateCount"]; ok {
			var n int
			if json.Unmarshal(value, &n) != nil || n != 1 {
				return false
			}
		}
		if system, ok := body["systemInstruction"]; ok {
			var content map[string]json.RawMessage
			if json.Unmarshal(system, &content) != nil || !cacheOnlyKeys(content, "parts", "role") || !cacheTextParts(content["parts"], protocol) {
				return false
			}
		}
		return true
	}
	if json.Unmarshal(body["temperature"], &temperature) != nil || temperature != 0 {
		return false
	}
	switch protocol {
	case "responses":
		if !cacheOnlyKeys(body, "model", "input", "instructions", "temperature", "top_p", "max_output_tokens", "stream", "store") {
			return false
		}
		if value, ok := body["store"]; ok {
			var store bool
			if json.Unmarshal(value, &store) != nil || store {
				return false
			}
		}
		var text string
		if json.Unmarshal(body["input"], &text) != nil && !cacheMessages(body["input"], protocol) {
			return false
		}
		if value, ok := body["instructions"]; ok && json.Unmarshal(value, &text) != nil {
			return false
		}
		return true
	case "chat_completions":
		return cacheOnlyKeys(body, "model", "messages", "temperature", "top_p", "max_tokens", "max_completion_tokens", "stop", "stream", "n") && cacheMessages(body["messages"], protocol) && cacheSingleChoice(body["n"])
	case "messages":
		if !cacheOnlyKeys(body, "model", "messages", "system", "temperature", "top_p", "top_k", "max_tokens", "stop_sequences", "stream") || !cacheMessages(body["messages"], protocol) {
			return false
		}
		if value, ok := body["system"]; ok && !cacheTextParts(value, protocol) {
			return false
		}
		return true
	}
	return false
}
func cacheSingleChoice(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var n int
	return json.Unmarshal(raw, &n) == nil && n == 1
}

// Caller holds a.mu, after rechecking current Key/source/model state and budget
// eligibility. raw is the exact effective native request, not the user envelope.
func (a *App) responseCacheDescriptor(key ClientKey, src Source, model SourceModel, rec Record, raw []byte, headers http.Header) (*ResponseCacheDescriptor, error) {
	if rec.Origin == "admin_test" {
		return nil, nil
	}
	settings, err := a.Store.responseCacheSettings()
	if err != nil {
		return nil, err
	}
	if !slices.Contains(settings.SourceIDs, src.ID) && !slices.Contains(settings.RouteIDs, key.RouteID) {
		return nil, nil
	}
	if src.Kind != "api_key" || !responseCacheEligible(raw, src.NativeProtocol, rec.Operation) {
		return nil, nil
	}
	canonical, err := canonicalCacheJSON(raw)
	if err != nil {
		return nil, nil
	}
	semantic := map[string]string{}
	for _, name := range []string{"Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if value := headers.Get(name); value != "" {
			semantic[name] = value
		}
	}
	adapter := model.AdapterVersion
	if adapter == "" {
		adapter = extendedAdapterVersion
	}
	d := &ResponseCacheDescriptor{KeyID: key.ID, KeyVersion: key.Version, SourceID: src.ID, SourceVersion: src.Version, SourceGeneration: src.Generation, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, PublicModel: rec.Model, SentModel: rec.SentModel, ModelVersion: model.Version, NativeProtocol: src.NativeProtocol, ClientProtocol: rec.Protocol, Operation: rec.Operation, AdapterVersion: adapter, ConfigVersion: rec.ConfigVersion, RouteVersion: rec.RouteVersion, Price: rec.Price, SemanticHeaders: semantic, SettingsVersion: settings.Version, TTLMinutes: settings.TTLMinutes}
	d.RouteID = key.RouteID
	d.CacheKey = digest(encode(d) + "\n" + string(canonical))
	return d, nil
}
func (a *App) responseCacheRoot() (*os.Root, error) {
	if a.Config.DataDir == "" {
		return nil, errors.New("结果缓存需要私有数据目录")
	}
	path := filepath.Join(a.Config.DataDir, ".response-cache")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, storageError()
	}
	if err = os.Chmod(path, 0700); err != nil {
		return nil, err
	}
	return os.OpenRoot(path)
}
func cacheFilename(name string) bool {
	return strings.HasSuffix(name, ".json") && validLocalID(strings.TrimSuffix(name, ".json"))
}
func (a *App) removeResponseCache(root *os.Root, key, name string) error {
	if cacheFilename(name) {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_, err := a.Store.DB.Exec("DELETE FROM response_cache WHERE cache_key=?", key)
	return err
}
func (a *App) pruneResponseCache(root *os.Root, now time.Time, reserve int64) error {
	rows, err := a.Store.DB.Query("SELECT cache_key,filename,size,expires_at FROM response_cache ORDER BY last_access,cache_key")
	if err != nil {
		return err
	}
	type item struct {
		key, name, expires string
		size               int64
	}
	items := []item{}
	var total int64
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.key, &v.name, &v.size, &v.expires); err != nil {
			break
		}
		items = append(items, v)
		total += v.size
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if v.expires <= now.UTC().Format(cacheTimeFormat) {
			if err = a.removeResponseCache(root, v.key, v.name); err != nil {
				return err
			}
			total -= v.size
		}
	}
	for _, v := range items {
		if v.expires > now.UTC().Format(cacheTimeFormat) && total+reserve > responseCacheLimit {
			if err = a.removeResponseCache(root, v.key, v.name); err != nil {
				return err
			}
			total -= v.size
		}
	}
	// Files from a process interrupted before index publication are owned cache
	// artifacts, not user files. Remove only our generated regular file names.
	known := map[string]bool{}
	rows, err = a.Store.DB.Query("SELECT filename FROM response_cache")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		known[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	files, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, file := range files {
		if !known[file.Name()] && cacheFilename(file.Name()) {
			if err = root.Remove(file.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *App) clearResponseCache() error {
	root, err := a.responseCacheRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	rows, err := a.Store.DB.Query("SELECT cache_key,filename FROM response_cache")
	if err != nil {
		return err
	}
	type item struct{ key, name string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.key, &v.name); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		if err = a.removeResponseCache(root, v.key, v.name); err != nil {
			return err
		}
	}
	return a.pruneResponseCache(root, time.Now(), 0)
}

// Called from the existing owned maintenance loop. Default-off installations
// do not create a directory unless a user has explicitly enabled/saved cache.
func (a *App) maintainResponseCache() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var count int
	if err := a.Store.DB.QueryRow("SELECT count(*) FROM response_cache").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := os.Lstat(filepath.Join(a.Config.DataDir, ".response-cache")); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
	}
	root, err := a.responseCacheRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return a.pruneResponseCache(root, time.Now(), 0)
}
func (a *App) lookupResponseCache(d *ResponseCacheDescriptor) (*ResponseCacheEntry, error) {
	if d == nil {
		return nil, nil
	}
	settings, err := a.Store.responseCacheSettings()
	if err != nil {
		return nil, err
	}
	if settings.Version != d.SettingsVersion {
		return nil, nil
	}
	current, err := a.responseCacheStateCurrent(d)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, nil
	}
	root, err := a.responseCacheRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = a.pruneResponseCache(root, time.Now(), 0); err != nil {
		return nil, err
	}
	var name string
	var size int64
	err = a.Store.DB.QueryRow("SELECT filename,size FROM response_cache WHERE cache_key=?", d.CacheKey).Scan(&name, &size)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry ResponseCacheEntry
	valid := cacheFilename(name) && size > 0 && size <= responseCacheEnvelopeLimit
	if valid {
		info, e := root.Lstat(name)
		valid = e == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() == size
	}
	if valid {
		file, e := root.Open(name)
		if e != nil {
			valid = false
		} else {
			raw, e := readLimited(file, responseCacheEnvelopeLimit)
			file.Close()
			valid = e == nil && json.Unmarshal(raw, &entry) == nil && json.Valid(entry.Body) && entry.BodyHash == digest(string(entry.Body)) && entry.OriginRequestID != "" && responseCacheOutputEligible(entry.Body, d.ClientProtocol)
		}
	}
	if !valid {
		if err = a.removeResponseCache(root, d.CacheKey, name); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if _, err = a.Store.DB.Exec("UPDATE response_cache SET last_access=? WHERE cache_key=?", time.Now().UTC().Format(cacheTimeFormat), d.CacheKey); err != nil {
		return nil, err
	}
	return &entry, nil
}

// The live snapshot is checked while the caller holds mu. A revoked Key or
// changed binding cannot resurrect a result published under the old snapshot.
func (a *App) responseCacheStateCurrent(d *ResponseCacheDescriptor) (bool, error) {
	keys, err := a.Store.keys()
	if err != nil {
		return false, err
	}
	found := false
	for _, key := range keys {
		if key.ID == d.KeyID && key.Version == d.KeyVersion && keyValid(key, time.Now()) && key.RouteID == d.RouteID && (key.RouteID != "" || key.SourceID == d.SourceID) && allowed(key.ProtocolAllowlist, d.ClientProtocol) && allowed(key.OperationAllowlist, d.Operation) && allowed(key.ModelAllowlist, d.PublicModel) {
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}
	src, err := a.Store.source(d.SourceID)
	if err != nil {
		return false, err
	}
	if !src.Enabled || src.Deleted || !src.Configured || src.AuthStatus == "rejected" || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.Generation != d.SourceGeneration || src.AccountID != d.AccountID || src.AccountGeneration != d.AccountGeneration || src.Version != d.SourceVersion {
		return false, nil
	}
	candidate, err := a.candidateFor(src, d.SentModel)
	if err != nil {
		return false, err
	}
	if !candidate.Model.Enabled || candidate.Model.Version != d.ModelVersion {
		return false, nil
	}
	if d.RouteID != "" {
		route, e := a.Store.route(d.RouteID)
		if e != nil {
			return false, e
		}
		if !route.Enabled || route.Version != d.RouteVersion {
			return false, nil
		}
	}
	if d.ConfigVersion > 0 {
		settings, e := a.Store.readRuntimeSettings()
		if e != nil {
			return false, e
		}
		if settings.Version != d.ConfigVersion {
			return false, nil
		}
	}
	return true, nil
}
func responseCacheOutputEligible(raw []byte, protocol string) bool {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch protocol {
	case "responses":
		var status string
		_ = json.Unmarshal(value["status"], &status)
		if status != "completed" {
			return false
		}
		var output []map[string]json.RawMessage
		if json.Unmarshal(value["output"], &output) != nil || len(output) == 0 {
			return false
		}
		for _, item := range output {
			var typ string
			_ = json.Unmarshal(item["type"], &typ)
			if typ != "message" || !cacheTextParts(item["content"], protocol) {
				return false
			}
		}
		return true
	case "messages":
		var reason string
		_ = json.Unmarshal(value["stop_reason"], &reason)
		return reason == "end_turn" && cacheTextParts(value["content"], protocol)
	case "chat_completions":
		var choices []struct {
			Finish  string                     `json:"finish_reason"`
			Message map[string]json.RawMessage `json:"message"`
		}
		if json.Unmarshal(value["choices"], &choices) != nil || len(choices) != 1 {
			return false
		}
		var text string
		return choices[0].Finish == "stop" && cacheOnlyKeys(choices[0].Message, "role", "content") && json.Unmarshal(choices[0].Message["content"], &text) == nil
	case "gemini":
		var candidates []struct {
			Reason  string                     `json:"finishReason"`
			Content map[string]json.RawMessage `json:"content"`
		}
		if json.Unmarshal(value["candidates"], &candidates) != nil || len(candidates) != 1 {
			return false
		}
		return candidates[0].Reason == "STOP" && cacheOnlyKeys(candidates[0].Content, "role", "parts") && cacheTextParts(candidates[0].Content["parts"], protocol)
	}
	return false
}

// Caller owns a.mu. Cache storage failures do not repeat an upstream attempt.
func (a *App) storeResponseCache(d *ResponseCacheDescriptor, body []byte, contentType string, rec Record) error {
	if d == nil || rec.Status != "succeeded" || rec.DeliveryStatus != "completed" || rec.CacheHit || int64(len(body)) > responseCacheResultLimit || !responseCacheOutputEligible(body, d.ClientProtocol) {
		return nil
	}
	if rec.KeyID != d.KeyID || rec.SourceID != d.SourceID || rec.SentModel != d.SentModel || rec.Model != d.PublicModel || rec.Protocol != d.ClientProtocol || rec.AccountGeneration != d.AccountGeneration || rec.Generation != d.SourceGeneration {
		return nil
	}
	settings, err := a.Store.responseCacheSettings()
	if err != nil {
		return err
	}
	if settings.Version != d.SettingsVersion {
		return nil
	}
	// A credential/model/Key/config change while the request was running makes
	// this snapshot ineligible for publication, not a new upstream retry.
	current, err := a.responseCacheStateCurrent(d)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	root, err := a.responseCacheRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	entry := ResponseCacheEntry{Body: body, ContentType: contentType, OriginRequestID: rec.ID, Usage: rec.Usage, SavedEstimate: estimate(rec.Usage, rec.Price), BodyHash: digest(string(body))}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err = a.pruneResponseCache(root, time.Now(), int64(len(encoded))); err != nil {
		return err
	}
	var oldName string
	queryErr := a.Store.DB.QueryRow("SELECT filename FROM response_cache WHERE cache_key=?", d.CacheKey).Scan(&oldName)
	if queryErr != nil && queryErr != sql.ErrNoRows {
		return queryErr
	}
	name := id("cache") + ".json"
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(encoded)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		root.Remove(name)
		return err
	}
	dir, err := root.Open(".")
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		root.Remove(name)
		return err
	}
	now := time.Now().UTC()
	_, err = a.Store.DB.Exec("INSERT INTO response_cache(cache_key,filename,size,expires_at,last_access) VALUES(?,?,?,?,?) ON CONFLICT(cache_key) DO UPDATE SET filename=excluded.filename,size=excluded.size,expires_at=excluded.expires_at,last_access=excluded.last_access", d.CacheKey, name, len(encoded), now.Add(time.Duration(d.TTLMinutes)*time.Minute).Format(cacheTimeFormat), now.Format(cacheTimeFormat))
	if err != nil {
		root.Remove(name)
		return err
	}
	if oldName != "" && oldName != name {
		if !cacheFilename(oldName) {
			return storageError()
		}
		if err = root.Remove(oldName); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func cachedResponseID(value string) bool { return strings.HasPrefix(value, "cove_cache_") }
func replayResponseCache(w http.ResponseWriter, entry *ResponseCacheEntry, protocol string, rec *Record) error {
	var value map[string]json.RawMessage
	if json.Unmarshal(entry.Body, &value) != nil {
		return storageError()
	}
	responseID := id("cove_cache")
	if protocol == "gemini" {
		value["responseId"] = json.RawMessage(encode(responseID))
	} else {
		value["id"] = json.RawMessage(encode(responseID))
	}
	body := []byte(encode(value))
	zero := "0"
	rec.CacheHit = true
	rec.CacheOriginRequestID = entry.OriginRequestID
	rec.CachedResultUsage = &entry.Usage
	rec.SavedEstimate = entry.SavedEstimate
	rec.ActualCost = &zero
	rec.Cost = &zero
	rec.PartialCost = nil
	rec.Usage = Usage{}
	rec.ResponseID = responseID
	rec.Status = "succeeded"
	rec.UpstreamStatus = "not_called"
	rec.Submission = "not_sent"
	rec.HTTPStatus = 200
	rec.ResponseBytes = int64(len(body))
	rec.WireUsageSource = "replayed_cached_result"
	w.Header().Set("Content-Type", entry.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Cove-Cache", "hit")
	w.Header().Set("X-Cove-Usage-Source", "replayed;no_new_upstream_usage")
	w.Header().Set("X-Cove-Cache-Origin", entry.OriginRequestID)
	w.WriteHeader(200)
	_, err := w.Write(body)
	if err != nil {
		rec.DeliveryStatus = "failed"
		rec.ErrorStage = "downstream_write"
	} else {
		rec.DeliveryStatus = "completed"
	}
	return err
}
