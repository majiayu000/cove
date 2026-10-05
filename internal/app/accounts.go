package app

import (
	"database/sql"
	"io"
	"net/http"
	"strings"
	"time"
)

// Account identity is independent of endpoint configuration. Credentials are
// published once under the admission boundary and never returned in this DTO.
func (a *App) accountsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && r.Method == "GET" {
		a.listAccountsAPI(w, r)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var in struct {
		Version  int     `json:"version"`
		Provider string  `json:"provider"`
		AuthType string  `json:"auth_type"`
		Name     *string `json:"name"`
		Secret   string  `json:"secret"`
	}
	if len(parts) == 2 && r.Method == "POST" {
		if !decode(w, r, &in) {
			return
		}
		if a.Config.PublicAPIBase != "" && (in.AuthType == "aws_profile" || in.AuthType == "google_adc" || in.AuthType == "codex_subscription") {
			fail(w, 422, "企业模式需要本租户显式凭据，不能使用主机 profile/ADC 或本机订阅登录", "auth_type")
			return
		}
		valid := in.AuthType == "api_key" && (in.Provider == "openai" || in.Provider == "openai_compatible" || in.Provider == "anthropic" || in.Provider == "gemini" || in.Provider == "azure") || in.AuthType == "none" && (in.Provider == "ollama" || in.Provider == "local") || in.AuthType == "codex_subscription" && in.Provider == "codex"
		valid = valid || in.Provider == "bedrock" && in.AuthType == "aws_profile" || in.Provider == "vertex" && (in.AuthType == "google_adc" || in.AuthType == "service_account")
		if !valid {
			fail(w, 422, "此 provider 的独立授权或认证适配尚不可用", "auth_type")
			return
		}
		if in.Secret != "" {
			fail(w, 400, "新账号先创建，再通过 credential 专用接口配置凭据", "secret")
			return
		}
		v := Account{ID: id("acct"), Provider: in.Provider, AuthType: in.AuthType, Name: in.Provider, Generation: 1, Version: 1, AuthState: "unconfigured", CreatedAt: time.Now().UTC(), SourceIDs: []string{}, Identity: map[string]any{"verified": false, "subject_hash": nil, "display_name": nil}, QuotaStatus: "unknown"}
		if in.Name != nil {
			v.Name = strings.TrimSpace(*in.Name)
		}
		if v.Name == "" || len(v.Name) > 100 {
			fail(w, 400, "账号名称为1到100字符", "name")
			return
		}
		if v.AuthType == "none" {
			v.AuthState = "not_required"
		}
		if _, e := a.Store.DB.Exec("INSERT INTO accounts(id,credential_ref,generation,data) VALUES(?,'',?,?)", v.ID, v.Generation, encode(v)); e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 201, v)
		return
	}
	if len(parts) < 3 || len(parts) > 4 {
		fail(w, 404, "账号接口不存在", "")
		return
	}
	v, ref, e := a.Store.account(parts[2])
	if e == sql.ErrNoRows || v.Deleted {
		fail(w, 404, "账号不存在", "")
		return
	}
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	sources, e := a.Store.sources()
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	v.SourceIDs = []string{}
	for _, s := range sources {
		if s.AccountID == v.ID {
			v.SourceIDs = append(v.SourceIDs, s.ID)
		}
	}
	if len(parts) == 3 && r.Method == "GET" {
		v.QuotaStatus = accountQuotaStatus(v, sources)
		writeJSON(w, 200, v)
		return
	}
	if r.Method != "PATCH" && r.Method != "POST" && r.Method != "DELETE" {
		fail(w, 405, "方法不支持", "")
		return
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version != v.Version {
		fail(w, 409, "账号已修改，请刷新", "version")
		return
	}
	nextRef := ref
	changingCredential := false
	if len(parts) == 4 {
		if r.Method != "POST" {
			fail(w, 405, "方法不支持", "")
			return
		}
		switch parts[3] {
		case "credential":
			if v.AuthType != "api_key" && v.AuthType != "service_account" {
				fail(w, 422, "此账号使用独立授权而非API Key", "secret")
				return
			}
			if in.Secret == "" || len(in.Secret) > 65536 {
				fail(w, 400, "secret须非空且不超过64KiB", "secret")
				return
			}
			if v.AuthType == "service_account" {
				if err := validateCloudServiceAccount([]byte(in.Secret)); err != nil {
					fail(w, 400, err.Error(), "secret")
					return
				}
			}
			nextRef = id("credential")
			if e = a.Secrets.Put(nextRef, in.Secret); e != nil {
				fail(w, 503, "凭据保存失败，原配置保留", "")
				return
			}
			v.AuthState = "configured"
			v.CredentialPresent = true
			changingCredential = true
		case "logout":
			if a.accountActive[v.ID] > 0 {
				fail(w, 409, "账号仍有运行请求，请先等待或取消", "")
				return
			}
			nextRef = ""
			v.AuthState = "logged_out"
			v.CredentialPresent = false
			changingCredential = true
			for _, s := range sources {
				if s.AccountID == v.ID {
					a.cancelLogin(s.ID)
					a.cancelRefresh(s.ID)
				}
			}
		case "quota-refresh":
			if v.AuthType != "codex_subscription" {
				fail(w, 422, "此账号尚无固定额度查询合同，观测保持未知", "")
				return
			}
			for _, src := range sources {
				if src.AccountID == v.ID && fixedCodexObservationSource(src) && !src.Deleted && src.Enabled && src.Configured && src.AuthStatus == "logged_in" {
					req := r.Clone(r.Context())
					req.Body = io.NopCloser(strings.NewReader(`{}`))
					a.codexQuotaRefreshAPI(w, req, src, &a.quotaObserver)
					return
				}
			}
			fail(w, 409, "请先启用并登录具有固定额度合同的来源", "")
			return
		case "login":
			if v.AuthType != "codex_subscription" || len(v.SourceIDs) == 0 {
				fail(w, 422, "请先绑定独立授权来源；其他订阅准入尚未闭合", "")
				return
			}
			src, e := a.Store.source(v.SourceIDs[0])
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			req := r.Clone(r.Context())
			req.Body = io.NopCloser(strings.NewReader(encode(map[string]int{"version": src.Version})))
			a.loginAPI(w, req, src)
			return
		default:
			fail(w, 404, "账号操作不存在", "")
			return
		}
	} else if r.Method == "PATCH" {
		if in.Provider != "" || in.AuthType != "" || in.Secret != "" {
			fail(w, 400, "provider/auth_type不可原地修改，凭据使用专用接口", "")
			return
		}
		if in.Name != nil {
			v.Name = strings.TrimSpace(*in.Name)
		}
		if v.Name == "" || len(v.Name) > 100 {
			fail(w, 400, "账号名称为1到100字符", "name")
			return
		}
	} else if r.Method == "DELETE" {
		if len(v.SourceIDs) > 0 || a.accountActive[v.ID] > 0 {
			fail(w, 409, "先解除账号的来源引用并等待运行请求", "")
			return
		}
		v.Deleted = true
		v.CredentialPresent = false
		v.AuthState = "logged_out"
		nextRef = ""
		changingCredential = true
	} else {
		fail(w, 405, "方法不支持", "")
		return
	}
	v.Version++
	if changingCredential {
		v.Generation++
		v.QuotaStatus = "stale"
	}
	if _, e = a.Store.DB.Exec("UPDATE accounts SET credential_ref=?,generation=?,data=? WHERE id=?", nextRef, v.Generation, encode(v), v.ID); e != nil {
		if nextRef != ref {
			a.cleanupSecret(nextRef)
		}
		fail(w, 503, storageError().Error(), "")
		return
	}
	revocation := "unknown"
	if len(parts) == 4 && parts[3] == "logout" {
		for _, src := range sources {
			if src.AccountID == v.ID && !src.Deleted && chatGPTDirectSource(src) {
				revocation = a.revokeChatGPTSessionLocked(r.Context(), src, ref)
				break
			}
		}
	}
	if ref != nextRef {
		a.cleanupSecret(ref)
	}
	a.signalAdmission()
	if len(parts) == 4 && parts[3] == "logout" {
		writeJSON(w, 200, struct {
			Account
			UpstreamRevocation string `json:"upstream_revocation"`
		}{v, revocation})
		return
	}
	writeJSON(w, 200, v)
}
