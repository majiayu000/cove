package app

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type sourceInput struct {
	CloudProviderConfig
	ProxyURL         json.RawMessage `json:"proxy_url"`
	NativeOperations *[]string       `json:"native_operations"`
	RerankPath       *string         `json:"rerank_path"`
	Name             *string         `json:"name"`
	Kind             string          `json:"kind"`
	BaseURL          *string         `json:"base_url"`
	Models           *[]string       `json:"models"`
	Enabled          *bool           `json:"enabled"`
	Version          int             `json:"version"`
	Credential       string          `json:"credential"`
	Price            *Price          `json:"price"`
	Provider         string          `json:"provider"`
	NativeProtocol   string          `json:"native_protocol"`
	AccountID        string          `json:"account_id"`
	AllowAdjustment  *bool           `json:"allow_parameter_adjustment"`
	MaxConcurrent    json.RawMessage `json:"max_concurrent"`
}

func (a *App) sourcesAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 5 && parts[3] == "models" && parts[4] == "discover" {
		a.discoverModels(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "test" && r.Method == "POST" {
		a.testSource(w, r, parts[2])
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(parts) == 2 {
		if r.Method == "GET" {
			v, e := a.Store.sources()
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			writeJSON(w, 200, v)
			return
		}
		if r.Method != "POST" {
			fail(w, 405, "方法不支持", "")
			return
		}
		var in sourceInput
		if !decode(w, r, &in) {
			return
		}
		src := Source{ID: id("src"), Kind: in.Kind, Enabled: true, Version: 1, Generation: 1, Models: []string{}, AuthStatus: "not_configured", Verification: Verification{Status: "untested", Capabilities: []string{}}, Quota: map[string]any{"status": "unknown", "observed_at": nil}}
		if in.Enabled != nil {
			src.Enabled = *in.Enabled
		}
		src.AccountID = in.AccountID
		src.Provider = in.Provider
		src.CloudProviderConfig = in.CloudProviderConfig
		src.NativeProtocol = in.NativeProtocol
		src.CreatedAt = time.Now().UTC()
		if len(in.MaxConcurrent) > 0 && json.Unmarshal(in.MaxConcurrent, &src.MaxConcurrent) != nil {
			fail(w, 400, "max_concurrent 必须为正整数或null", "max_concurrent")
			return
		}
		if len(in.ProxyURL) > 0 && json.Unmarshal(in.ProxyURL, &src.ProxyURL) != nil {
			fail(w, 400, "proxy_url 必须为字符串或 null", "proxy_url")
			return
		}
		if in.NativeOperations != nil {
			src.NativeOperations = *in.NativeOperations
		}
		if in.RerankPath != nil {
			src.RerankPath = *in.RerankPath
		}
		if src.AccountID == "" {
			fail(w, 400, "先创建或选择账号，再添加来源", "account_id")
			return
		} else {
			acct, ref, e := a.Store.account(src.AccountID)
			if src.Kind == "" {
				src.Kind = acct.AuthType
			}
			if src.Provider == "" {
				src.Provider = acct.Provider
			}
			if e != nil || acct.AuthType != src.Kind || (src.Provider != "" && src.Provider != acct.Provider) {
				fail(w, 409, "账号不存在或认证种类不匹配", "account_id")
				return
			}
			if in.Credential != "" {
				fail(w, 409, "复用账号时请通过凭据接口修改账号", "credential")
				return
			}
			src.CredentialRef = ref
			src.AuthStatus = acct.AuthState
			src.Configured = ref != "" || acct.AuthType == "none"
			src.AccountGeneration = acct.Generation
		}
		if src.NativeProtocol == "" {
			src.NativeProtocol = "responses"
		}
		if src.Provider == "" {
			src.Provider = "openai_compatible"
		}
		if in.AllowAdjustment != nil {
			src.AllowParameterAdjustment = *in.AllowAdjustment
		}
		if src.Kind != "api_key" && src.Kind != "codex_subscription" && src.Kind != "none" && !cloudProvider(src) {
			fail(w, 400, "只支持 API Key、明确无认证的本地服务或 Codex 订阅来源", "kind")
			return
		}
		if in.Name != nil {
			src.Name = strings.TrimSpace(*in.Name)
		}
		if len(in.ProxyURL) > 0 && json.Unmarshal(in.ProxyURL, &src.ProxyURL) != nil {
			fail(w, 400, "proxy_url 必须为字符串或 null", "proxy_url")
			return
		}
		if in.NativeOperations != nil {
			src.NativeOperations = *in.NativeOperations
		}
		if in.RerankPath != nil {
			src.RerankPath = *in.RerankPath
		}
		if in.Models != nil {
			src.Models = *in.Models
		}
		if in.BaseURL != nil && !cloudProvider(src) {
			src.BaseURL = *in.BaseURL
		}
		if cloudProvider(src) {
			src.BaseURL = cloudEndpoint(src.Provider, src.CloudConfig)
			src.Configured = cloudConfigured(src)
		}
		if src.Kind == "codex_subscription" {
			src.Provider = "codex"
			src.NativeProtocol = "responses"
			src.BaseURL = a.Config.Codex.BaseURL
			if in.BaseURL != nil && strings.TrimRight(*in.BaseURL, "/") == chatGPTResource {
				src.BaseURL = chatGPTResource
			}
			if in.AccountID == "" {
				src.AuthStatus = "logged_out"
			}
			if in.Credential != "" {
				fail(w, 400, "订阅来源必须通过独立登录授权", "credential")
				return
			}
		}
		src.Price = in.Price
		if !validSource(w, src) {
			return
		}
		if in.Credential != "" {
			if err := a.replaceCredential(&src, in.Credential, false); err != nil {
				fail(w, 503, err.Error(), "")
				return
			}
		} else if err := a.Store.saveSource(src); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		src, err := a.Store.source(src.ID)
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 201, src)
		return
	}
	if len(parts) < 3 || len(parts) > 5 {
		fail(w, 404, "来源接口不存在", "")
		return
	}
	src, err := a.Store.source(parts[2])
	if err != nil || src.Deleted {
		fail(w, 404, "来源不存在", "")
		return
	}
	if len(parts) == 5 {
		if parts[3] == "login" && parts[4] == "confirm" {
			a.confirmLogin(w, r, src)
		} else {
			fail(w, 404, "来源接口不存在", "")
		}
		return
	}
	if len(parts) == 4 {
		switch parts[3] {
		case "credential":
			if r.Method != "POST" || src.Kind != "api_key" && src.Kind != "service_account" {
				fail(w, 400, "此来源不支持该凭据操作", "")
				return
			}
			var in sourceInput
			if !decode(w, r, &in) {
				return
			}
			if in.Version != src.Version {
				fail(w, 409, "来源已修改，请刷新后重试", "version")
				return
			}
			if in.Credential == "" {
				fail(w, 400, "凭据不能为空", "credential")
				return
			}
			src.Version++
			src.Generation++
			src.Continuation = false
			src.Verification = Verification{Status: "untested", Capabilities: []string{}}
			src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
			if err := a.replaceCredential(&src, in.Credential, false); err != nil {
				fail(w, 503, err.Error(), "")
				return
			}
			writeJSON(w, 200, src)
			return
		case "login":
			a.loginAPI(w, r, src)
			return
		case "logout":
			if r.Method != "POST" || src.Kind != "codex_subscription" {
				fail(w, 400, "此来源不支持退出", "")
				return
			}
			if a.accountActive[src.AccountID] > 0 {
				fail(w, 409, "请先等待或取消此账号全部来源的运行请求", "")
				return
			}
			a.cancelLogin(src.ID)
			a.cancelRefresh(src.ID)
			old := src.CredentialRef
			src.CredentialRef = ""
			src.Configured = false
			src.AuthStatus = "logged_out"
			src.Generation++
			src.Version++
			src.Verification = Verification{Status: "untested", Capabilities: []string{}}
			src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
			if err := a.Store.saveSource(src); err != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			revocation := a.revokeChatGPTSessionLocked(r.Context(), src, old)
			cleanup := "completed"
			if a.cleanupSecret(old) != nil {
				cleanup = "failed"
			}
			message := "本地授权已退出；上游会话撤销未确认，可到 ChatGPT 设置断开 Cove"
			if revocation == "confirmed" {
				message = "本地授权已退出，上游可刷新会话已撤销"
			}
			writeJSON(w, 200, map[string]any{"source": src, "local_logout": "completed", "secret_cleanup": cleanup, "upstream_revocation": revocation, "message": message, "cleanup_warning": a.maintenanceError})
			return
		case "quota-refresh":
			a.codexQuotaRefreshAPI(w, r, src, &a.quotaObserver)
			return
		case "quota-history":
			a.codexQuotaHistoryAPI(w, r, src)
			return
		}
		fail(w, 404, "来源接口不存在", "")
		return
	}
	switch r.Method {
	case "GET":
		writeJSON(w, 200, src)
	case "PATCH":
		var in sourceInput
		if !decode(w, r, &in) {
			return
		}
		if in.Version != src.Version {
			fail(w, 409, "来源已修改，请刷新后重试", "version")
			return
		}
		if in.AccountID != "" && in.AccountID != src.AccountID {
			acct, ref, e := a.Store.account(in.AccountID)
			if e != nil || acct.Deleted || acct.AuthType != src.Kind || acct.Provider != src.Provider {
				fail(w, 409, "账号不存在或认证/provider不匹配", "account_id")
				return
			}
			if in.Credential != "" {
				fail(w, 400, "换账号与替换凭据请分别提交", "")
				return
			}
			src.AccountID, src.CredentialRef, src.AccountGeneration, src.AuthStatus = in.AccountID, ref, acct.Generation, acct.AuthState
			src.Generation++
			src.Continuation = false
			src.Verification = Verification{Status: "stale", Capabilities: []string{}}
			src.Quota = map[string]any{"status": "stale", "observed_at": nil}
		}
		if in.Kind != "" && in.Kind != src.Kind {
			fail(w, 400, "来源类型不能修改，请新建来源", "kind")
			return
		}
		if in.Name != nil {
			src.Name = strings.TrimSpace(*in.Name)
		}
		if in.CloudConfig != nil && cloudProvider(src) {
			src.CloudProviderConfig = in.CloudProviderConfig
			src.BaseURL = cloudEndpoint(src.Provider, src.CloudConfig)
			src.Generation++
			src.Verification = Verification{Status: "stale", Capabilities: []string{}}
			src.Continuation = false
		}
		if len(in.ProxyURL) > 0 && json.Unmarshal(in.ProxyURL, &src.ProxyURL) != nil {
			fail(w, 400, "proxy_url 必须是字符串或 null", "proxy_url")
			return
		}
		if in.NativeOperations != nil {
			src.NativeOperations = *in.NativeOperations
		}
		if in.RerankPath != nil {
			src.RerankPath = *in.RerankPath
		}
		if in.AllowAdjustment != nil {
			src.AllowParameterAdjustment = *in.AllowAdjustment
		}
		if len(in.MaxConcurrent) > 0 && json.Unmarshal(in.MaxConcurrent, &src.MaxConcurrent) != nil {
			fail(w, 400, "max_concurrent 必须为正整数或null", "max_concurrent")
			return
		}
		if in.NativeProtocol != "" && in.NativeProtocol != src.NativeProtocol {
			if src.Kind == "codex_subscription" {
				fail(w, 400, "订阅原生协议固定为 Responses", "native_protocol")
				return
			}
			src.NativeProtocol = in.NativeProtocol
			src.Generation++
			src.Continuation = false
			src.Verification = Verification{Status: "stale", Capabilities: []string{}}
		}
		if in.Models != nil {
			src.Models = *in.Models
			src.Verification = Verification{Status: "untested", Capabilities: []string{}}
		}
		if in.Enabled != nil {
			src.Enabled = *in.Enabled
		}
		if in.BaseURL != nil && *in.BaseURL != src.BaseURL {
			if src.Kind == "codex_subscription" {
				fail(w, 400, "订阅端点由运行配置固定", "base_url")
				return
			}
			if origin(*in.BaseURL) != origin(src.BaseURL) && in.Credential == "" {
				fail(w, 400, "跨 origin 更换地址必须同时提供新目标凭据", "credential")
				return
			}
			src.BaseURL = *in.BaseURL
			src.Generation++
			src.Continuation = false
			src.Verification = Verification{Status: "untested", Capabilities: []string{}}
			src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
		}
		if in.Price != nil {
			src.Price = in.Price
		}
		src.Version++
		if !validSource(w, src) {
			return
		}
		if in.Credential != "" {
			if src.Kind != "api_key" && src.Kind != "service_account" {
				fail(w, 400, "订阅来源请重新登录", "")
				return
			}
			src.Generation++
			src.Continuation = false
			src.Verification = Verification{Status: "untested", Capabilities: []string{}}
			src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
			err = a.replaceCredential(&src, in.Credential, false)
		} else {
			err = a.Store.saveSource(src)
		}
		if err != nil {
			fail(w, 503, "来源保存失败，原配置仍保留", "")
			return
		}
		a.signalAdmission()
		src, err = a.Store.source(src.ID)
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, src)
	case "DELETE":
		var members int
		if err = a.Store.DB.QueryRow("SELECT count(*) FROM routes r, json_each(json_extract(r.data,'$.members')) m JOIN source_models sm ON sm.id=json_extract(m.value,'$.model_id') WHERE sm.source_id=?", src.ID).Scan(&members); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if members > 0 {
			fail(w, 409, "请先移除路由对此来源模型的引用", "")
			return
		}
		keys, e := a.Store.keys()
		if e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		for _, k := range keys {
			if !k.Revoked && k.SourceID == src.ID {
				fail(w, 409, "请先撤销或切换引用此来源的客户端 Key", "")
				return
			}
		}
		if a.sourceRunning(src.ID) {
			fail(w, 409, "请等待或取消该来源的运行请求", "")
			return
		}
		var siblings int
		if err = a.Store.DB.QueryRow("SELECT count(*) FROM sources WHERE account_id=? AND id!=? AND json_extract(data,'$.deleted')=0", src.AccountID, src.ID).Scan(&siblings); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		old := ""
		if siblings == 0 {
			a.cancelLogin(src.ID)
			a.cancelRefresh(src.ID)
			old = src.CredentialRef
			src.CredentialRef = ""
			src.Configured = false
		}
		src.Deleted = true
		src.Enabled = false
		src.Version++
		if err = a.Store.saveSource(src); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		a.cleanupSecret(old)
		writeJSON(w, 200, map[string]any{"deleted": true, "cleanup_warning": a.maintenanceError})
	default:
		fail(w, 405, "方法不支持", "")
	}
}
func validSource(w http.ResponseWriter, s Source) bool {
	if cloudProvider(s) {
		if err := validateCloudSelection(s.Provider, s.CloudConfig); err != nil {
			accountingFailure(w, err)
			return false
		}
		if s.Kind != cloudAuthType(s.Provider, s.CloudConfig) || s.Provider == "bedrock" && s.NativeProtocol != "responses" || s.Provider == "vertex" && s.NativeProtocol != "gemini" {
			fail(w, 422, "云来源认证与原生协议不匹配", "cloud_config")
			return false
		}
	}
	if s.ProxyURL != nil {
		if err := validProxy(*s.ProxyURL); err != nil {
			fail(w, 400, err.Error(), "proxy_url")
			return false
		}
	}
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 100 {
		fail(w, 400, "填写 1 到 100 字符的名称", "name")
		return false
	}
	if s.NativeProtocol != "" && s.NativeProtocol != "responses" && s.NativeProtocol != "chat_completions" && s.NativeProtocol != "messages" && s.NativeProtocol != "gemini" && s.NativeProtocol != "realtime_websocket" {
		fail(w, 400, "原生协议无效", "native_protocol")
		return false
	}
	if s.Kind == "none" {
		if s.Provider != "local" && s.Provider != "ollama" {
			fail(w, 400, "无认证仅用于明确选择的本地来源", "provider")
			return false
		}
		u, e := url.Parse(s.BaseURL)
		ip := net.ParseIP(u.Hostname())
		if e != nil || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			fail(w, 400, "无认证来源必须使用loopback端点", "base_url")
			return false
		}
	}
	if s.MaxConcurrent != nil && *s.MaxConcurrent <= 0 {
		fail(w, 400, "并发上限必须为正整数或 null", "max_concurrent")
		return false
	}
	seen := map[string]bool{}
	for _, m := range s.Models {
		if strings.TrimSpace(m) == "" || len(m) > 200 || seen[m] {
			fail(w, 400, "模型标识无效", "models")
			return false
		}
		seen[m] = true
	}
	if err := validateURL(s.BaseURL); err != nil {
		fail(w, 400, err.Error(), "base_url")
		return false
	}
	if s.Price != nil && !validPrice(*s.Price) {
		fail(w, 400, "价格必须为非负十进制字符串，币种为三个大写字母", "price")
		return false
	}
	return true
}
func (a *App) cleanupSecret(ref string) error {
	if err := a.Secrets.Delete(ref); err != nil {
		a.maintenanceError = err.Error()
		return err
	}
	return nil
}
func (a *App) replaceCredential(src *Source, material string, subscription bool) error {
	if src.Kind == "service_account" {
		if e := validateCloudServiceAccount([]byte(material)); e != nil {
			return e
		}
	}
	old := src.CredentialRef
	fresh := id("credential")
	if err := a.Secrets.Put(fresh, material); err != nil {
		return err
	}
	src.CredentialRef = fresh
	src.Configured = true
	if subscription {
		var c Credential
		if json.Unmarshal([]byte(material), &c) == nil && c.Subject != "" && c.Account != "" {
			src.VerifiedIdentity = map[string]any{"verified": true, "subject_hash": digest(c.Subject + "\x00" + c.Account), "display_name": identityLabel(c)}
			if c.ClientID != "" {
				src.VerifiedIdentity["plan_usage_enabled"] = slices.Contains(c.Scopes, chatGPTDirectScope)
			}
		}
	}
	if subscription {
		src.AuthStatus = "logged_in"
	} else {
		src.AuthStatus = "configured"
	}
	if err := a.Store.saveSource(*src); err != nil {
		a.cleanupSecret(fresh)
		src.CredentialRef = old
		return storageError()
	}
	a.cleanupSecret(old)
	current, e := a.Store.source(src.ID)
	if e != nil {
		return storageError()
	}
	*src = current
	a.signalAdmission()
	return nil
}
func (a *App) sourceRunning(source string) bool {
	for _, s := range a.runningSources {
		if s == source {
			return true
		}
	}
	return false
}
func (a *App) listRecords(q map[string][]string, limit int) ([]Record, error) {
	query := "SELECT data FROM requests WHERE 1=1"
	args := []any{}
	for _, f := range []struct{ param, expr string }{{"status", "status"}, {"state", "status"}, {"client_key_id", "json_extract(data,'$.client_key_id')"}, {"key_id", "json_extract(data,'$.client_key_id')"}, {"origin", "json_extract(data,'$.origin')"}, {"route_id", "json_extract(data,'$.route_id')"}, {"model", "json_extract(data,'$.requested_model')"}, {"protocol", "json_extract(data,'$.protocol')"}} {
		if len(q[f.param]) > 0 && q[f.param][0] != "" {
			query += " AND " + f.expr + " IN (" + strings.TrimRight(strings.Repeat("?,", len(q[f.param])), ",") + ")"
			for _, value := range q[f.param] {
				args = append(args, value)
			}
		}
	}
	for _, f := range []struct{ param, expr string }{{"source_id", "a.source_id"}, {"account_id", "a.account_id"}} {
		if len(q[f.param]) > 0 && q[f.param][0] != "" {
			query += " AND EXISTS (SELECT 1 FROM attempts a WHERE a.request_id=requests.id AND " + f.expr + " IN (" + strings.TrimRight(strings.Repeat("?,", len(q[f.param])), ",") + "))"
			for _, value := range q[f.param] {
				args = append(args, value)
			}
		}
	}
	if len(q["from"]) > 0 {
		query += " AND started>=?"
		args = append(args, q["from"][0])
	}
	if len(q["to"]) > 0 {
		query += " AND started<?"
		args = append(args, q["to"][0])
	}
	if len(q["snapshot"]) > 0 {
		query += " AND started<=?"
		args = append(args, q["snapshot"][0])
	}
	if len(q["before_started"]) > 0 {
		query += " AND (started<? OR (started=? AND id<?))"
		args = append(args, q["before_started"][0], q["before_started"][0], q["before_id"][0])
	}
	query += " ORDER BY started DESC,id DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := a.Store.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var b string
		var v Record
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (a *App) requestsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && r.Method == "GET" {
		a.recordsPage(w, r)
		return
	}
	if len(parts) == 4 && parts[3] == "reconcile" {
		a.reconcileAccountingAPI(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
		var in struct {
			Version int `json:"version"`
		}
		if !decode(w, r, &in) {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if queued, ok := a.queued[parts[2]]; ok {
			if queued.Version != in.Version {
				fail(w, 409, "排队请求版本已变化", "version")
				return
			}
			queued.cancel()
			writeJSON(w, 202, map[string]any{"state": "cancelling", "request_id": queued.ID, "upstream_execution": "not_submitted"})
			return
		}
		var raw string
		err := a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", parts[2]).Scan(&raw)
		if err == sql.ErrNoRows {
			fail(w, 404, "请求不存在", "")
			return
		}
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		var record Record
		if json.Unmarshal([]byte(raw), &record) != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		cancel := a.running[parts[2]]
		if cancel == nil {
			writeJSON(w, 200, map[string]any{"request_id": record.ID, "state": record.Status, "cancel_requested": false, "completed": true})
			return
		}
		if in.Version != record.Version {
			fail(w, 409, "请求版本已变化，请重新读取详情", "version")
			return
		}
		cancel()
		writeJSON(w, 202, map[string]any{"request_id": record.ID, "state": "cancelling", "cancel_requested": true, "upstream_execution": "unknown"})
		return
	}
	if len(parts) == 3 && r.Method == "GET" {
		var data string
		err := a.Store.DB.QueryRow("SELECT data FROM requests WHERE id=?", parts[2]).Scan(&data)
		if err == sql.ErrNoRows {
			fail(w, 404, "请求不存在", "")
			return
		}
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		rows, err := a.Store.DB.Query("SELECT data FROM attempts WHERE request_id=? ORDER BY sequence", parts[2])
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		attempts := []json.RawMessage{}
		for rows.Next() {
			var b string
			if err = rows.Scan(&b); err != nil {
				break
			}
			attempts = append(attempts, json.RawMessage(b))
		}
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		rows.Close()
		accounting, accountingErr := a.Store.accountingDetails(parts[2])
		if accountingErr != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"request": json.RawMessage(data), "attempts": attempts, "accounting": accounting})
		return
	}
	fail(w, 404, "请求接口不存在", "")
}
func (a *App) diagnostics(w http.ResponseWriter, r *http.Request) {
	items, err := a.listRecords(nil, 100)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	out := []map[string]any{}
	for _, v := range items {
		out = append(out, map[string]any{"id": v.ID, "status": v.Status, "http_status": v.HTTPStatus, "error_stage": v.ErrorStage, "duration_ms": v.DurationMS, "usage_completeness": v.Completeness, "origin": v.Origin})
	}
	w.Header().Set("Content-Disposition", "attachment; filename=gatt-diagnostics.json")
	writeJSON(w, 200, map[string]any{"version": Version, "build_id": BuildID, "created_at": time.Now().UTC(), "storage_healthy": !a.storageFailed.Load(), "requests": out, "excluded": "credentials, device codes, names, account IDs, URLs, local paths, response IDs, prompts, outputs"})
}
