package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

type Login struct {
	ID                string    `json:"id"`
	Method            string    `json:"method"`
	AuthorizationURL  string    `json:"authorization_url,omitempty"`
	Expires           time.Time `json:"expires_at"`
	Status            string    `json:"status"`
	Message           string    `json:"message,omitempty"`
	PreviousIdentity  string    `json:"previous_identity,omitempty"`
	NewIdentity       string    `json:"new_identity,omitempty"`
	version           int
	generation        int
	accountGeneration int
	nonce             string
	clientID          string
	direct            bool
	pending           *Credential
	state             string
	verifier          string
	cancel            context.CancelFunc
}
type Credential struct {
	Access   string    `json:"access_token"`
	Refresh  string    `json:"refresh_token"`
	IDToken  string    `json:"id_token"`
	Expires  time.Time `json:"expires_at"`
	Account  string    `json:"account"`
	Subject  string    `json:"subject"`
	ClientID string    `json:"client_id,omitempty"`
	Scopes   []string  `json:"scopes,omitempty"`
}
type tokenResponse struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	IDToken string `json:"id_token"`
	Expires int64  `json:"expires_in"`
	Scope   string `json:"scope"`
}

const chatGPTResource = "https://api.openai.com/v1"
const chatGPTDirectScope = "chatgpt.tokens.use.direct"

// Host and issued client IDs survive local logout. They are not bearer credentials.
// Caller holds mu, so first creation is serialized with other login attempts.
func (a *App) chatGPTRegistration(account string) (string, string, error) {
	host, err := a.Secrets.Get("chatgpt-host-id")
	if errors.Is(err, os.ErrNotExist) {
		var uuid [16]byte
		if _, err = rand.Read(uuid[:]); err != nil {
			return "", "", err
		}
		uuid[6], uuid[8] = (uuid[6]&0x0f)|0x40, (uuid[8]&0x3f)|0x80
		host = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
		err = a.Secrets.Put("chatgpt-host-id", host)
	}
	if err != nil {
		return "", "", err
	}
	client, err := a.Secrets.Get("chatgpt-client-" + account)
	if errors.Is(err, os.ErrNotExist) {
		return host, "dynamic_agent_client", nil
	}
	return host, client, err
}

// Caller has already stopped local use and holds mu. Tokens remain private until
// the bounded revocation attempt finishes; a failure never claims remote logout.
func (a *App) revokeChatGPTSessionLocked(ctx context.Context, src Source, ref string) string {
	if !chatGPTDirectSource(src) || ref == "" {
		return "unknown"
	}
	raw, err := a.Secrets.Get(ref)
	var credential Credential
	if err != nil || json.Unmarshal([]byte(raw), &credential) != nil || credential.ClientID == "" || credential.Refresh == "" {
		return "unknown"
	}
	a.mu.Unlock()
	defer a.mu.Lock()
	ctx, cancel := context.WithTimeout(sourceNetwork(ctx, src), 6*time.Second)
	defer cancel()
	var metadata struct {
		Issuer string `json:"issuer"`
		Revoke string `json:"revocation_endpoint"`
	}
	if a.identityDocument(ctx, safeEndpoint(a.Config.Codex.AuthBaseURL, "/.well-known/openid-configuration"), &metadata) != nil || strings.TrimRight(metadata.Issuer, "/") != strings.TrimRight(a.Config.Codex.AuthBaseURL, "/") || metadata.Revoke == "" || origin(metadata.Revoke) != origin(a.Config.Codex.AuthBaseURL) {
		return "unknown"
	}
	values := url.Values{"token": {credential.Refresh}, "token_type_hint": {"refresh_token"}, "client_id": {credential.ClientID}}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", metadata.Revoke, strings.NewReader(values.Encode()))
		if err != nil {
			return "unknown"
		}
		req.GetBody = nil
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := a.doUpstream(req, src)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return "confirmed"
			}
			if resp.StatusCode < 500 {
				return "unknown"
			}
		}
		if attempt == 0 {
			select {
			case <-ctx.Done():
				return "unknown"
			case <-time.After(time.Second):
			}
		}
	}
	return "unknown"
}

func (a *App) loginOwner(source string) string {
	src, err := a.Store.source(source)
	if err == nil {
		return src.AccountID
	}
	return source
}

func (a *App) cancelLogin(source string) {
	if op := a.logins[a.loginOwner(source)]; op != nil {
		op.Status = "cancelled"
		op.AuthorizationURL = ""
		op.state = ""
		op.verifier = ""
		op.nonce = ""
		op.pending = nil
		op.Message = "登录已取消"
		op.cancel()
	}
}

// Caller holds the lifecycle mutex. Browser authorization uses one fixed provider callback.
func (a *App) loginAPI(w http.ResponseWriter, r *http.Request, src Source) {
	if src.Kind != "codex_subscription" {
		fail(w, 422, "此来源不支持账号登录", "")
		return
	}
	if r.Method == "DELETE" {
		a.cancelLogin(src.ID)
		writeJSON(w, 200, map[string]bool{"cancelled": true})
		return
	}
	current := a.logins[src.AccountID]
	if r.Method == "GET" {
		if current == nil {
			writeJSON(w, 200, map[string]string{"status": "idle"})
		} else {
			writeJSON(w, 200, current)
		}
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	if a.stopping {
		fail(w, 503, "服务正在关闭", "")
		return
	}
	var input struct {
		Version int `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Version != src.Version {
		fail(w, 409, "来源已修改，请刷新后重试", "version")
		return
	}
	if current != nil && loginActive(current) && current.Expires.After(time.Now()) {
		writeJSON(w, 200, current)
		return
	}
	for source, operation := range a.logins {
		if source != src.AccountID && loginActive(operation) && operation.Expires.After(time.Now()) {
			fail(w, 409, "另一个来源正在登录，请先完成或取消", "")
			return
		}
	}
	if a.accountActive[src.AccountID] > 0 {
		fail(w, 409, "请先结束该来源的运行请求", "")
		return
	}
	if sources, err := a.Store.sources(); err != nil {
		fail(w, 503, "来源账号绑定不可读", "")
		return
	} else {
		for _, other := range sources {
			if other.AccountID == src.AccountID && !other.Deleted && chatGPTDirectSource(other) != chatGPTDirectSource(src) {
				fail(w, 409, "ChatGPT 独立注册须使用独立账号记录，不能替换 Codex 私有来源共享的凭据", "account_id")
				return
			}
		}
	}
	callback, e := url.Parse(a.Config.Codex.RedirectURI)
	if e != nil || callback.Scheme != "http" || callback.Hostname() != "localhost" && callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/auth/callback" {
		fail(w, 503, "OAuth 本机回调配置无效", "")
		return
	}
	clientID, hostID := a.Config.Codex.ClientID, ""
	direct := chatGPTDirectSource(src)
	if direct {
		if callback.Hostname() != "127.0.0.1" || strings.TrimRight(src.BaseURL, "/") != chatGPTResource {
			fail(w, 503, "ChatGPT 独立授权要求 127.0.0.1 回调和公开 API 来源", "")
			return
		}
		var err error
		hostID, clientID, err = a.chatGPTRegistration(src.AccountID)
		if err != nil {
			fail(w, 503, "独立授权注册记录不可读写", "")
			return
		}
	}
	listener, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", callback.Port()))
	if e != nil {
		fail(w, 409, "OAuth 回调端口正在使用，请结束其他登录流程后重试", "")
		return
	}
	listeners := []net.Listener{listener}
	if callback.Hostname() == "localhost" {
		ipv6, err := net.Listen("tcp6", net.JoinHostPort("::1", callback.Port()))
		if err != nil {
			_ = listener.Close()
			fail(w, 409, "OAuth IPv6 回调端口不可用，请结束其他登录流程后重试", "")
			return
		}
		listeners = append(listeners, ipv6)
	}
	state, verifier, nonce := token(), token(), token()
	hash := sha256.Sum256([]byte(verifier))
	params := url.Values{"client_id": {a.Config.Codex.ClientID}, "response_type": {"code"}, "redirect_uri": {a.Config.Codex.RedirectURI}, "scope": {"openid email profile offline_access"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "prompt": {"login"}, "id_token_add_organizations": {"true"}, "codex_cli_simplified_flow": {"true"}}
	params.Set("nonce", nonce)
	authorizePath := "/oauth/authorize"
	if direct {
		authorizePath = "/api/accounts/authorize"
		params = url.Values{"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {a.Config.Codex.RedirectURI}, "scope": {"openid profile email offline_access resource.invoke " + chatGPTDirectScope}, "resource": {chatGPTResource}, "state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "ext_agent_host_id": {hostID}}
		if clientID == "dynamic_agent_client" {
			params.Set("agent_name_hint", "Cove")
		}
	}
	ctx, cancel := context.WithTimeout(sourceNetwork(context.Background(), src), 10*time.Minute)
	op := &Login{ID: id("login"), Method: "browser_oauth", AuthorizationURL: safeEndpoint(a.Config.Codex.AuthBaseURL, authorizePath) + "?" + params.Encode(), Expires: time.Now().UTC().Add(10 * time.Minute), Status: "pending", state: state, verifier: verifier, nonce: nonce, clientID: clientID, direct: direct, version: src.Version, generation: src.Generation, accountGeneration: src.AccountGeneration, cancel: cancel}
	a.logins[src.AccountID] = op
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 40 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.oauthCallback(w, r, ctx, src.ID, op, callback) })
	a.serveLogin(ctx, cancel, src.ID, op, server, listeners)
	writeJSON(w, 201, op)
}

// Caller holds mu. Listener, shutdown coordinator and callbacks all belong to App.
func (a *App) serveLogin(ctx context.Context, cancel context.CancelFunc, source string, op *Login, server *http.Server, listeners []net.Listener) {
	a.ownedTasks.Add(1 + len(listeners))
	go func() {
		defer a.ownedTasks.Done()
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.logins[a.loginOwner(source)] == op && loginActive(op) {
			op.Status = "expired"
			op.Message = "登录已过期，请重试"
			op.AuthorizationURL = ""
			op.state = ""
			op.verifier = ""
			op.nonce = ""
			op.pending = nil
		}
	}()
	for _, listener := range listeners {
		go func() {
			defer a.ownedTasks.Done()
			if e := server.Serve(listener); e != nil && e != http.ErrServerClosed {
				cancel()
			}
		}()
	}
}
func (a *App) oauthCallback(w http.ResponseWriter, r *http.Request, ctx context.Context, source string, op *Login, callback *url.URL) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "GET" || r.URL.Path != callback.Path || r.Host != callback.Host {
		http.Error(w, "Invalid callback", 400)
		return
	}
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		http.Error(w, "Cove is shutting down", 503)
		return
	}
	release, gateErr := a.beginBackupOwnerLocked()
	if gateErr != nil {
		a.mu.Unlock()
		http.Error(w, "Cove is draining", 503)
		return
	}
	defer release()
	a.ownedTasks.Add(1)
	defer a.ownedTasks.Done()
	state := r.URL.Query().Get("state")
	if a.logins[a.loginOwner(source)] != op || op.Status != "pending" || ctx.Err() != nil || !op.Expires.After(time.Now()) || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(op.state)) != 1 {
		a.mu.Unlock()
		http.Error(w, "Invalid or expired login state. Return to Cove and retry.", 400)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || r.URL.Query().Get("error") != "" {
		op.Status = "failed"
		op.Message = "授权未完成，请重试"
		op.AuthorizationURL = ""
		op.state = ""
		op.verifier = ""
		op.nonce = ""
		a.mu.Unlock()
		defer op.cancel()
		oauthResult(w, "授权未完成。请回到原 Cove 页面重试。")
		return
	}
	verifier, nonce := op.verifier, op.nonce
	if op.direct {
		issued := r.URL.Query().Get("client_id")
		if op.clientID == "dynamic_agent_client" {
			if issued == "" || issued == "dynamic_agent_client" || len(issued) > 256 || strings.ContainsAny(issued, "\r\n\x00") {
				a.mu.Unlock()
				http.Error(w, "Registration callback is missing an issued client ID. Return to Cove and retry.", 400)
				return
			}
			src, err := a.Store.source(source)
			if err != nil || src.Deleted || src.Version != op.version || src.Generation != op.generation || src.AccountGeneration != op.accountGeneration {
				a.mu.Unlock()
				http.Error(w, "Source changed. Return to Cove and retry.", 409)
				return
			}
			if a.Secrets.Put("chatgpt-client-"+src.AccountID, issued) != nil {
				a.mu.Unlock()
				http.Error(w, "Registration could not be saved. Return to Cove and retry.", 503)
				return
			}
			op.clientID = issued
		} else if issued != "" && issued != op.clientID {
			a.mu.Unlock()
			http.Error(w, "Registration changed. Return to Cove and retry.", 400)
			return
		}
	}
	op.Status = "exchanging"
	op.AuthorizationURL = ""
	op.state = ""
	op.verifier = ""
	op.nonce = ""
	a.mu.Unlock()
	values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {a.Config.Codex.RedirectURI}, "client_id": {op.clientID}}
	if op.direct {
		values.Set("resource", chatGPTResource)
	}
	credential, err := a.exchange(ctx, values, nonce)
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if op.Status != "awaiting_confirmation" {
			op.cancel()
		}
	}()
	if a.logins[a.loginOwner(source)] != op || op.Status != "exchanging" || ctx.Err() != nil {
		http.Error(w, "Login cancelled. Return to Cove.", 409)
		return
	}
	src, e := a.Store.source(source)
	if err == nil && (e != nil || src.Deleted || a.stopping || a.accountActive[src.AccountID] > 0 || src.Version != op.version || src.Generation != op.generation || src.AccountGeneration != op.accountGeneration) {
		err = errors.New("来源状态已变化，请结束调用后重新登录")
	}
	if err == nil && src.Configured && !op.direct {
		raw, readErr := a.Secrets.Get(src.CredentialRef)
		var old Credential
		if readErr != nil || json.Unmarshal([]byte(raw), &old) != nil || old.Account != credential.Account || old.Subject != credential.Subject {
			op.Status, op.Message = "awaiting_confirmation", "登录身份与原来源不同，请在原管理页确认更换账号"
			op.PreviousIdentity = identityLabel(old)
			op.NewIdentity = identityLabel(credential)
			op.pending = &credential
			oauthResult(w, "授权已收到。请回到原 Cove 页面，确认是否更换来源账号。")
			return
		}
	}
	if err == nil && op.direct {
		previous, readErr := a.Secrets.Get("chatgpt-subject-" + src.AccountID)
		if readErr == nil && previous != credential.Subject {
			err = errors.New("独立注册返回不同身份，请为新账号建立独立来源")
		} else if errors.Is(readErr, os.ErrNotExist) {
			err = a.Secrets.Put("chatgpt-subject-"+src.AccountID, credential.Subject)
		} else if readErr != nil {
			err = readErr
		}
	}
	if err == nil {
		src.Generation++
		src.Version++
		src.Verification = Verification{Status: "untested", Capabilities: []string{}}
		src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
		err = a.replaceCredential(&src, encode(credential), true)
		if err == nil {
			a.cancelRefresh(src.ID)
		}
	}
	if err != nil {
		op.Status = "failed"
		op.Message = err.Error()
	} else {
		op.Status = "succeeded"
		op.Message = "已登录，调用尚未验证"
		if credential.ClientID != "" && !slices.Contains(credential.Scopes, chatGPTDirectScope) {
			op.Message = "身份已登录，ChatGPT 计划使用未授权；不能调用模型"
		}
	}
	oauthResult(w, op.Message+"。请回到原 Cove 页面，随后可关闭此窗口。")
}

func loginActive(op *Login) bool {
	return op.Status == "pending" || op.Status == "exchanging" || op.Status == "awaiting_confirmation"
}

func identityLabel(c Credential) string {
	if c.Account == "" || c.Subject == "" {
		return "身份未知"
	}
	return "账号 " + digest(c.Subject)[:8] + " · 工作区 " + digest(c.Account)[:8]
}

func oauthResult(w http.ResponseWriter, message string) {
	nonce := token()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; base-uri 'none'")
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>Cove 登录</title><h1>Cove · 栖港</h1><p>%s</p><script nonce="%s">history.replaceState(null, "", "/auth/callback");</script></html>`, html.EscapeString(message), nonce)
}

// Caller holds a.mu; confirmation cannot apply to a changed source or expired operation.
func (a *App) confirmLogin(w http.ResponseWriter, r *http.Request, src Source) {
	var in struct {
		Version   int    `json:"version"`
		Operation string `json:"operation_id"`
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	if !decode(w, r, &in) {
		return
	}
	op := a.logins[src.AccountID]
	if op == nil || op.ID != in.Operation || op.Status != "awaiting_confirmation" || op.pending == nil || !op.Expires.After(time.Now()) || src.Version != in.Version || src.Version != op.version || src.Generation != op.generation || src.AccountGeneration != op.accountGeneration || a.stopping {
		fail(w, 409, "登录确认已失效或来源已修改，请重新登录", "operation_id")
		return
	}
	if a.accountActive[src.AccountID] > 0 {
		fail(w, 409, "请先结束该来源的运行请求", "")
		return
	}
	src.Generation++
	src.Version++
	src.Verification = Verification{Status: "untested", Capabilities: []string{}}
	src.Quota = map[string]any{"status": "unknown", "observed_at": nil}
	err := a.replaceCredential(&src, encode(op.pending), true)
	op.pending = nil
	op.cancel()
	if err != nil {
		op.Status, op.Message = "failed", err.Error()
		fail(w, 503, err.Error(), "")
		return
	}
	a.cancelRefresh(src.ID)
	op.Status, op.Message = "succeeded", "新账号已绑定，调用尚未验证"
	writeJSON(w, 200, src)
}

func (a *App) exchange(ctx context.Context, values url.Values, expectedNonce ...string) (Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if values.Get("client_id") == "" {
		values.Set("client_id", a.Config.Codex.ClientID)
	}
	tokenPath := "/oauth/token"
	direct := values.Get("resource") == chatGPTResource
	if direct {
		if values.Get("client_id") == "dynamic_agent_client" {
			return Credential{}, errors.New("独立注册尚未签发客户端 ID，请重新登录")
		}
		tokenPath = "/api/accounts/oauth/token"
		values.Set("resource", chatGPTResource)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", safeEndpoint(a.Config.Codex.AuthBaseURL, tokenPath), strings.NewReader(values.Encode()))
	if err != nil {
		return Credential{}, errors.New("授权地址无效")
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "gatt/"+Version)
	resp, err := a.doUpstream(req, Source{})
	if err != nil {
		return Credential{}, errors.New("授权结果未知，请重新登录；不会重用旧刷新凭据")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Credential{}, errors.New("授权被拒绝，请重新登录")
	}
	b, err := readLimited(resp.Body, 1<<20)
	if err != nil {
		return Credential{}, errors.New("授权响应读取失败，请重新登录")
	}
	var t tokenResponse
	if json.Unmarshal(b, &t) != nil || t.Access == "" || t.Refresh == "" {
		return Credential{}, errors.New("授权响应缺少必要凭据，请重新登录")
	}
	c := Credential{Access: t.Access, Refresh: t.Refresh, IDToken: t.IDToken}
	nonce := ""
	if len(expectedNonce) > 0 {
		nonce = expectedNonce[0]
	}
	if direct {
		c.ClientID, c.Scopes = values.Get("client_id"), strings.Fields(t.Scope)
		c.Account, c.Subject, err = a.verifiedIdentity(ctx, t.IDToken, nonce, c.ClientID)
	} else {
		c.Account, c.Subject, err = a.verifiedIdentity(ctx, t.IDToken, nonce)
	}
	if err != nil {
		return Credential{}, err
	}
	if t.Expires <= 0 {
		var claims map[string]any
		decodeClaims(t.Access, &claims)
		if exp, ok := claims["exp"].(float64); ok {
			c.Expires = time.Unix(int64(exp), 0).UTC()
		} else {
			return c, errors.New("授权响应没有有效期限，请重新登录")
		}
	} else {
		c.Expires = time.Now().UTC().Add(time.Duration(t.Expires) * time.Second)
	}
	return c, nil
}

// Claims are metadata from the TLS token endpoint, never proof for accepting a downstream identity.
func decodeClaims(token string, out any) {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return
	}
	b, err := base64.RawURLEncoding.DecodeString(p[1])
	if err == nil {
		_ = json.Unmarshal(b, out)
	}
}

type credentialRefresh struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}
type credentialRefreshTiming struct{ Start, End time.Time }
type credentialRefreshTimingKey struct{}

// Called and returned with a.mu held; waiting releases it. A request's cancellation
// ends only its wait, while the shared refresh belongs to the source lifecycle.
func (a *App) subscriptionCredential(ctx context.Context, src *Source, rejectedAccess ...string) (Credential, error) {
	if a.stopping {
		return Credential{}, errors.New("服务正在关闭")
	}
	if task := a.refreshes[src.AccountID]; task != nil {
		return a.waitRefresh(ctx, src, task)
	}
	if src.AuthStatus != "logged_in" {
		return Credential{}, errors.New("订阅授权需要重新登录")
	}
	raw, err := a.Secrets.Get(src.CredentialRef)
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if json.Unmarshal([]byte(raw), &c) != nil || c.Access == "" {
		return c, errors.New("授权凭据不可读，请重新登录")
	}
	if chatGPTDirectSource(*src) != (c.ClientID != "") {
		return c, errors.New("授权注册与来源端点不匹配；独立 ChatGPT 和 Codex 私有来源不能共享账号凭据")
	}
	if c.ClientID != "" && !slices.Contains(c.Scopes, chatGPTDirectScope) {
		return c, errors.New("身份已登录，但 ChatGPT 计划使用未授权，请重新授权或选择 API 来源")
	}
	if time.Until(c.Expires) > time.Minute && !(len(rejectedAccess) > 0 && rejectedAccess[0] == c.Access) {
		return c, nil
	}
	src.AuthStatus = "refreshing"
	if err = a.Store.saveSource(*src); err != nil {
		return Credential{}, storageError()
	}
	refreshCtx, cancel := context.WithTimeout(sourceNetwork(context.Background(), *src), 30*time.Second)
	task := &credentialRefresh{done: make(chan struct{}), cancel: cancel}
	a.refreshes[src.AccountID] = task
	snapshot := *src
	release, gateErr := a.beginBackupOwnerLocked()
	if gateErr != nil {
		delete(a.refreshes, src.AccountID)
		cancel()
		return Credential{}, gateErr
	}
	a.ownedTasks.Add(1)
	go func() {
		defer a.ownedTasks.Done()
		defer cancel()
		defer release()
		values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.Refresh}}
		if c.ClientID != "" {
			values.Set("client_id", c.ClientID)
			values.Set("resource", chatGPTResource)
		}
		fresh, err := a.exchange(refreshCtx, values)
		if err == nil && (fresh.Account != c.Account || fresh.Subject != c.Subject) {
			err = errors.New("刷新后的账号身份变化，请重新登录绑定")
		}
		if err == nil && c.ClientID != "" && !slices.Contains(fresh.Scopes, chatGPTDirectScope) {
			err = errors.New("刷新结果未授予 ChatGPT 计划使用权限，请重新授权")
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		defer close(task.done)
		defer func() {
			if a.refreshes[snapshot.AccountID] == task {
				delete(a.refreshes, snapshot.AccountID)
			}
		}()
		current, readErr := a.Store.source(snapshot.ID)
		if a.refreshes[snapshot.AccountID] != task || readErr != nil || current.Deleted || current.Generation != snapshot.Generation || current.AccountGeneration != snapshot.AccountGeneration || current.CredentialRef != snapshot.CredentialRef {
			task.err = errors.New("来源授权已变化，旧刷新结果已丢弃")
			return
		}
		if refreshCtx.Err() != nil || a.stopping {
			err = errors.New("刷新已中断，结果未知，请重新登录")
		}
		if err == nil {
			err = a.replaceCredential(&current, encode(fresh), true)
		}
		if err != nil {
			a.observations.credentialRefreshFailures.Add(1)
			current.AuthStatus = "needs_reauth"
			if e := a.Store.saveSource(current); e != nil {
				a.markStorageFailure()
			}
		}
		task.err = err
	}()
	return a.waitRefresh(ctx, src, task)
}

func (a *App) waitRefresh(ctx context.Context, src *Source, task *credentialRefresh) (Credential, error) {
	if trace, ok := ctx.Value(credentialRefreshTimingKey{}).(*credentialRefreshTiming); ok {
		trace.Start = time.Now().UTC()
		defer func() { trace.End = time.Now().UTC() }()
	}
	a.mu.Unlock()
	select {
	case <-ctx.Done():
		a.mu.Lock()
		return Credential{}, ctx.Err()
	case <-task.done:
	}
	a.mu.Lock()
	if task.err != nil {
		return Credential{}, task.err
	}
	current, err := a.Store.source(src.ID)
	if err != nil || current.Deleted || !current.Enabled || current.Generation != src.Generation || current.Version != src.Version || current.AuthStatus != "logged_in" {
		return Credential{}, errors.New("来源配置已变化，请重新发起请求")
	}
	*src = current
	raw, err := a.Secrets.Get(current.CredentialRef)
	var credential Credential
	if err != nil || json.Unmarshal([]byte(raw), &credential) != nil || credential.Access == "" || !credential.Expires.After(time.Now()) {
		return Credential{}, errors.New("新授权凭据不可用，请重新登录")
	}
	return credential, nil
}

// Caller holds a.mu. A late response cannot publish after the durable reference changes.
func (a *App) cancelRefresh(source string) {
	src, err := a.Store.source(source)
	if err != nil {
		return
	}
	owner := src.AccountID
	if task := a.refreshes[owner]; task != nil {
		task.cancel()
		delete(a.refreshes, owner)
	}
}
