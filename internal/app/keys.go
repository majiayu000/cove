package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type keyInput struct {
	Name     *string `json:"name"`
	SourceID string  `json:"source_id"`
	Target   *struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"target"`
	Version      int             `json:"version"`
	Enabled      *bool           `json:"enabled"`
	Protocol     json.RawMessage `json:"protocol_allowlist"`
	Models       json.RawMessage `json:"model_allowlist"`
	Operations   json.RawMessage `json:"operation_allowlist"`
	Expires      json.RawMessage `json:"expires_at"`
	RevokeAt     json.RawMessage `json:"revoke_at"`
	Limits       *Limits         `json:"limits"`
	CancelActive bool            `json:"cancel_active"`
	BudgetID     json.RawMessage `json:"budget_id"`
	Budget       *Budget         `json:"budget"`
}

func (a *App) keysAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	a.mu.Lock()
	defer a.mu.Unlock()
	keys, err := a.Store.keys()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		views := []clientKeyView{}
		for _, k := range keys {
			v, e := a.clientKeyView(k)
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			views = append(views, v)
		}
		writeJSON(w, 200, views)
		return
	}
	var k ClientKey
	found := false
	// Body upload must not hold generation admission's mutex. Reload the Key
	// afterwards so version checks still use the current published state.
	readInput := func(in *keyInput) bool {
		a.mu.Unlock()
		ok := decode(w, r, in)
		a.mu.Lock()
		if !ok {
			return false
		}
		if found {
			k, err = a.Store.keyByID(parts[2])
			if err != nil {
				fail(w, 503, storageError().Error(), "")
				return false
			}
		}
		return true
	}
	if len(parts) >= 3 {
		for _, item := range keys {
			if item.ID == parts[2] {
				k = item
				found = true
				break
			}
		}
		if !found {
			fail(w, 404, "Key 不存在", "")
			return
		}
		if len(parts) == 3 && r.Method == "GET" {
			a.signalAdmission()
			view, e := a.clientKeyView(k)
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			writeJSON(w, 200, view)
			return
		}
	}
	if len(parts) == 3 && r.Method == "DELETE" {
		var in keyInput
		if r.ContentLength > 0 {
			if !readInput(&in) {
				return
			}
			if in.Version != k.Version {
				fail(w, 409, "Key 已修改，请刷新", "version")
				return
			}
		}
		k.Revoked = true
		now := time.Now().UTC()
		k.RevokedAt = &now
		k.Version++
		if _, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(k), k.ID); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		a.signalAdmission()
		cancelled := []string{}
		for rid, q := range a.queued {
			if q.KeyID == k.ID {
				q.cancel()
				cancelled = append(cancelled, rid)
			}
		}
		if in.CancelActive {
			for rid, cancel := range a.running {
				if a.runningKeys[rid] == k.ID {
					cancel()
					cancelled = append(cancelled, rid)
				}
			}
		}
		writeJSON(w, 200, map[string]any{"key": k, "id": k.ID, "revoked": true, "cancel_requested": cancelled, "upstream_execution": "unknown"})
		return
	}
	rotate := len(parts) == 4 && parts[3] == "rotate" && r.Method == "POST"
	create := len(parts) == 2 && r.Method == "POST"
	patch := len(parts) == 3 && r.Method == "PATCH"
	if !create && !patch && !rotate {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in keyInput
	if !readInput(&in) {
		return
	}
	if !create {
		if k.Revoked {
			fail(w, 409, "已撤销的 Key 不可修改或轮换", "")
			return
		}
		if in.Version != k.Version {
			fail(w, 409, "Key 已修改，请刷新", "version")
			return
		}
	}
	if !rotate && (len(in.RevokeAt) > 0 || in.CancelActive) {
		fail(w, 400, "revoke_at 只用于轮换，cancel_active 只用于撤销", "")
		return
	}
	old := k
	if create {
		enabled := true
		k = ClientKey{ID: id("key"), Version: 1, Enabled: &enabled, CreatedAt: time.Now().UTC()}
	} else {
		k.Version++
	}
	if rotate {
		if in.Target != nil || in.SourceID != "" || in.Enabled != nil || len(in.Protocol) > 0 || len(in.Models) > 0 || len(in.Operations) > 0 || len(in.Expires) > 0 || in.Limits != nil || in.Budget != nil || len(in.BudgetID) > 0 {
			fail(w, 400, "轮换继承旧 Key 范围；修改策略请使用 PATCH", "")
			return
		}
		k.BudgetScopeID = old.budgetScopeID()
		k.ID = id("key")
		k.Version = 1
		k.CreatedAt = time.Now().UTC()
		k.LastSeen = nil
		k.RevokeAt = nil
		k.RevokedAt = nil
		if len(in.RevokeAt) > 0 && string(in.RevokeAt) != "null" {
			var t time.Time
			if json.Unmarshal(in.RevokeAt, &t) != nil || !t.After(time.Now()) {
				fail(w, 400, "旧 Key 失效时间必须是未来 UTC 时间", "revoke_at")
				return
			}
			old.RevokeAt = &t
			old.Version++
		}
	}
	if in.Name != nil {
		k.Name = strings.TrimSpace(*in.Name)
	}
	if k.Name == "" || len(k.Name) > 100 {
		fail(w, 400, "填写 1 到 100 字符的 Key 名称", "name")
		return
	}
	if !rotate {
		if in.Target != nil {
			switch in.Target.Kind {
			case "source":
				k.SourceID = in.Target.ID
				k.RouteID = ""
			case "route":
				k.RouteID = in.Target.ID
				k.SourceID = ""
			default:
				fail(w, 400, "target.kind 必须为 source 或 route", "target")
				return
			}
		} else if in.SourceID != "" {
			k.SourceID = in.SourceID
			k.RouteID = ""
		}
		if in.Enabled != nil {
			k.Enabled = in.Enabled
		}
		for _, field := range []struct {
			raw  json.RawMessage
			dest *[]string
			name string
		}{{in.Protocol, &k.ProtocolAllowlist, "protocol_allowlist"}, {in.Models, &k.ModelAllowlist, "model_allowlist"}, {in.Operations, &k.OperationAllowlist, "operation_allowlist"}} {
			if len(field.raw) > 0 && json.Unmarshal(field.raw, field.dest) != nil {
				fail(w, 400, "权限必须为字符串数组或 null", field.name)
				return
			}
		}
		if len(in.Expires) > 0 {
			if string(in.Expires) == "null" {
				k.ExpiresAt = nil
			} else {
				var t time.Time
				if json.Unmarshal(in.Expires, &t) != nil || !t.After(time.Now()) {
					fail(w, 400, "到期时间必须晚于当前时间", "expires_at")
					return
				}
				k.ExpiresAt = &t
			}
		}
		if in.Limits != nil {
			k.Limits = *in.Limits
		}
	}
	if !validLimits(k.Limits) {
		fail(w, 400, "限流与并发必须为正整数或 null", "limits")
		return
	}
	for _, v := range k.ProtocolAllowlist {
		if v != "responses" && v != "chat_completions" && v != "messages" && v != "gemini" && v != "realtime_websocket" {
			fail(w, 400, "协议权限无效", "protocol_allowlist")
			return
		}
	}
	var source any
	var route any
	if k.SourceID != "" && k.RouteID == "" {
		src, e := a.Store.source(k.SourceID)
		if e != nil || src.Deleted {
			fail(w, 400, "选择有效来源", "target")
			return
		}
		source = k.SourceID
	} else if k.RouteID != "" && k.SourceID == "" {
		if _, e := a.Store.route(k.RouteID); e != nil {
			fail(w, 400, "选择有效路由", "target")
			return
		}
		route = k.RouteID
	} else {
		fail(w, 400, "Key 必须绑定一个来源或路由", "target")
		return
	}
	if create || rotate {
		secret := "cove_" + token()
		k.Fingerprint = secret[:13]
		tx, e := a.Store.DB.Begin()
		if e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer tx.Rollback()
		if _, e = tx.Exec("INSERT INTO client_keys(id,digest,source_id,route_id,data) VALUES(?,?,?,?,?)", k.ID, digest(secret), source, route, encode(k)); e == nil && rotate {
			_, e = tx.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(old), old.ID)
		}
		if e == nil {
			if !rotate && (len(in.BudgetID) > 0 || in.Budget != nil) {
				var bid string
				if len(in.BudgetID) > 0 && json.Unmarshal(in.BudgetID, &bid) != nil {
					fail(w, 400, "budget_id 必须为字符串或 null", "budget_id")
					return
				}
				e = linkKeyBudget(tx, &k, bid, in.Budget)
			}
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			accountingFailure(w, e)
			return
		}
		writeJSON(w, 201, map[string]any{"key": k, "secret": secret, "old_key_id": old.ID, "old_revoke_at": old.RevokeAt})
		return
	}
	tx, err := a.Store.DB.Begin()
	if err != nil {
		accountingFailure(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE client_keys SET source_id=?,route_id=?,data=? WHERE id=?", source, route, encode(k), k.ID)
	if err == nil && (len(in.BudgetID) > 0 || in.Budget != nil) {
		var bid string
		if len(in.BudgetID) > 0 && json.Unmarshal(in.BudgetID, &bid) != nil {
			fail(w, 400, "budget_id 必须为字符串或 null", "budget_id")
			return
		}
		err = linkKeyBudget(tx, &k, bid, in.Budget)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		accountingFailure(w, err)
		return
	}
	a.signalAdmission()
	writeJSON(w, 200, k)
}

type clientKeyView struct {
	ClientKey
	EffectiveScope map[string]any  `json:"effective_scope"`
	BudgetSummary  []BudgetSummary `json:"budget_summary"`
	RevokedAt      *time.Time      `json:"revoked_at"`
}

func (a *App) clientKeyView(k ClientKey) (clientKeyView, error) {
	models := []string{}
	operations := []string{}
	protocols := []string{}
	add := func(dst *[]string, value string) {
		for _, v := range *dst {
			if v == value {
				return
			}
		}
		*dst = append(*dst, value)
	}
	addSource := func(src Source) {
		if src.Deleted || !src.Enabled || !src.Configured {
			return
		}
		for _, p := range []string{"responses", "chat_completions", "messages", "gemini"} {
			if p == "messages" && src.Kind == "codex_subscription" && !src.AllowParameterAdjustment {
				continue
			}
			if allowed(k.ProtocolAllowlist, p) {
				add(&protocols, p)
			}
		}
		if allowed(k.OperationAllowlist, "generate") {
			add(&operations, "generate")
		}
		for _, op := range src.NativeOperations {
			if allowed(k.OperationAllowlist, op) {
				add(&operations, op)
			}
		}
	}
	if keyValid(k, time.Now()) {
		if k.RouteID == "" {
			src, e := a.Store.source(k.SourceID)
			if e != nil {
				return clientKeyView{}, e
			}
			addSource(src)
			for _, m := range src.Models {
				if src.Enabled && src.Configured && allowed(k.ModelAllowlist, m) {
					add(&models, m)
				}
			}
		} else {
			route, e := a.Store.route(k.RouteID)
			if e != nil {
				return clientKeyView{}, e
			}
			if route.Enabled {
				for _, member := range route.Members {
					m, me := a.Store.model(member.ModelID)
					s, se := a.Store.source(m.SourceID)
					if me == nil && se == nil && m.Enabled {
						addSource(s)
					}
				}
				rows, e := a.Store.DB.Query("SELECT public_model FROM model_aliases WHERE route_id=? ORDER BY public_model", k.RouteID)
				if e != nil {
					return clientKeyView{}, e
				}
				for rows.Next() {
					var m string
					if e = rows.Scan(&m); e != nil {
						rows.Close()
						return clientKeyView{}, e
					}
					if allowed(k.ModelAllowlist, m) && len(protocols) > 0 {
						add(&models, m)
					}
				}
				e = rows.Err()
				rows.Close()
				if e != nil {
					return clientKeyView{}, e
				}
			}
		}
	}
	summaries := []BudgetSummary{}
	budgets, e := matchingBudgets(a.Store.DB, k.budgetScopeID(), k.RouteID)
	if e != nil {
		return clientKeyView{}, e
	}
	for _, b := range budgets {
		summary, e := summarizeBudget(a.Store.DB, b, time.Now().UTC())
		if e != nil {
			return clientKeyView{}, e
		}
		summaries = append(summaries, summary)
	}
	revoked := k.RevokedAt
	if revoked == nil && k.RevokeAt != nil && !time.Now().Before(*k.RevokeAt) {
		revoked = k.RevokeAt
	}
	return clientKeyView{ClientKey: k, EffectiveScope: map[string]any{"protocols": protocols, "models": models, "operations": operations, "active": keyValid(k, time.Now())}, BudgetSummary: summaries, RevokedAt: revoked}, nil
}
