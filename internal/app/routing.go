package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
)

type RouteMember struct {
	ModelID  string `json:"model_id"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}
type Route struct {
	RoutePolicy
	QueueLimit               int           `json:"queue_limit"`
	QueueTimeoutMS           int           `json:"queue_timeout_ms"`
	ID                       string        `json:"id"`
	Name                     string        `json:"name"`
	Enabled                  bool          `json:"enabled"`
	Version                  int           `json:"version"`
	Members                  []RouteMember `json:"members"`
	Strategy                 string        `json:"strategy"`
	MaxAttempts              int           `json:"max_attempts"`
	MaxConcurrent            *int          `json:"max_concurrent"`
	AllowParameterAdjustment bool          `json:"allow_parameter_adjustment"`
	CreatedAt                time.Time     `json:"created_at"`
}
type Alias struct {
	PublicModel string `json:"public_model"`
	RouteID     string `json:"route_id"`
	Version     int    `json:"version"`
}
type CandidateReason struct {
	ModelID       string `json:"model_id"`
	SourceID      string `json:"source_id"`
	AccountID     string `json:"account_id"`
	UpstreamModel string `json:"upstream_model"`
	Eligible      bool   `json:"eligible"`
	Reason        string `json:"reason"`
}
type selectionError struct {
	Status  int
	Message string
}

func (e *selectionError) Error() string { return e.Message }
func (a *App) candidateFor(s Source, model string) (RoutingPolicyCandidate, error) {
	var raw string
	err := a.Store.DB.QueryRow("SELECT data FROM source_models WHERE source_id=? AND upstream_model=?", s.ID, model).Scan(&raw)
	var m SourceModel
	if err == nil {
		err = json.Unmarshal([]byte(raw), &m)
	}
	if err == sql.ErrNoRows || err == nil && !m.Enabled {
		return RoutingPolicyCandidate{}, &selectionError{422, "模型不存在或已停用"}
	}
	if err != nil {
		return RoutingPolicyCandidate{}, err
	}
	price, priceErr := a.Store.effectivePrice(m.ID, m.Price, time.Now())
	if priceErr != nil {
		return RoutingPolicyCandidate{}, priceErr
	}
	if price != nil {
		s.Price = price
	}
	return RoutingPolicyCandidate{Source: s, Model: m}, err
}

type rateBucket struct {
	Tokens float64
	At     time.Time
	RPM    int
}

func keyValid(k ClientKey, now time.Time) bool {
	return !k.Revoked && (k.Enabled == nil || *k.Enabled) && (k.ExpiresAt == nil || k.ExpiresAt.After(now)) && (k.RevokeAt == nil || k.RevokeAt.After(now))
}
func allowed(values []string, v string) bool { return values == nil || slices.Contains(values, v) }
func validLimits(l Limits) bool {
	return (l.RPM == nil || *l.RPM > 0) && (l.TPM == nil || *l.TPM > 0) && (l.MaxConcurrent == nil || *l.MaxConcurrent > 0)
}
func (s *Store) route(rid string) (Route, error) {
	var v Route
	var b string
	err := s.DB.QueryRow("SELECT data FROM routes WHERE id=?", rid).Scan(&b)
	if err == nil {
		err = json.Unmarshal([]byte(b), &v)
	}
	return v, err
}

// Caller holds mu. Reuse the actual configuration preview without advancing
// weights, reserving capacity or contacting an upstream provider.
func (a *App) routeRuntime(route Route) (map[string]any, error) {
	out := map[string]any{"active_requests": a.routeActive[route.ID], "queued_requests": 0, "candidates": []CandidateReason{}, "full_request_eligibility": false}
	queued := 0
	for _, q := range a.queued {
		if q.RouteID == route.ID {
			queued++
		}
	}
	out["queued_requests"] = queued
	var model string
	err := a.Store.DB.QueryRow("SELECT public_model FROM model_aliases WHERE route_id=? ORDER BY public_model LIMIT 1", route.ID).Scan(&model)
	if err == sql.ErrNoRows {
		out["error"] = "未绑定公开模型名，候选资格尚未预览"
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	source, sent, reasons, selectionErr := a.selectSource(ClientKey{RouteID: route.ID}, model, "responses", true)
	out["public_model"], out["protocol"], out["candidates"], out["error"] = model, "responses", reasons, errorMessage(selectionErr)
	out["selected_candidate"] = nil
	if selectionErr == nil {
		out["selected_candidate"] = map[string]any{"source_id": source.ID, "upstream_model": sent}
	}
	return out, nil
}

func (a *App) routesAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[3] == "preview" {
		a.previewRoute(w, r, parts[2])
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(parts) == 2 && r.Method == "GET" {
		rows, err := a.Store.DB.Query("SELECT data FROM routes ORDER BY id")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		items := []Route{}
		for rows.Next() {
			var b string
			var v Route
			if err = rows.Scan(&b); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(b), &v); err != nil {
				break
			}
			items = append(items, v)
		}
		if err == nil {
			err = rows.Err()
		}
		err = errors.Join(err, rows.Close())
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		views := make([]any, 0, len(items))
		for _, route := range items {
			runtime, err := a.routeRuntime(route)
			if err != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			views = append(views, struct {
				Route
				Runtime map[string]any `json:"runtime"`
			}{route, runtime})
		}
		writeJSON(w, 200, map[string]any{"items": views, "next_cursor": nil})
		return
	}
	var v Route
	if len(parts) == 3 {
		var err error
		v, err = a.Store.route(parts[2])
		if err == sql.ErrNoRows {
			fail(w, 404, "路由不存在", "")
			return
		}
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if r.Method == "GET" {
			runtime, err := a.routeRuntime(v)
			if err != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			writeJSON(w, 200, struct {
				Route
				Runtime map[string]any `json:"runtime"`
			}{v, runtime})
			return
		}
		if r.Method == "DELETE" {
			var refs int
			err = a.Store.DB.QueryRow("SELECT (SELECT count(*) FROM client_keys WHERE route_id=? AND json_extract(data,'$.revoked')=0)+(SELECT count(*) FROM model_aliases WHERE route_id=?)", v.ID, v.ID).Scan(&refs)
			if err != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			if refs > 0 || a.routeActive[v.ID] > 0 {
				fail(w, 409, "路由仍被 Key、别名或运行请求引用，请先解除引用", "")
				return
			}
			if _, err = a.Store.DB.Exec("DELETE FROM routes WHERE id=?", v.ID); err != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			delete(a.routeWeights, v.ID)
			writeJSON(w, 200, map[string]any{"id": v.ID, "deleted": true})
			return
		}
	}
	if len(parts) != 2 && len(parts) != 3 {
		fail(w, 404, "路由接口不存在", "")
		return
	}
	if len(parts) == 2 && r.Method != "POST" || len(parts) == 3 && r.Method != "PATCH" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		AllowCrossModel *bool           `json:"allow_cross_model"`
		AffinityTTL     json.RawMessage `json:"affinity_ttl_seconds"`
		StrictContext   *bool           `json:"strict_context"`
		QueueLimit      *int            `json:"queue_limit"`
		QueueTimeoutMS  *int            `json:"queue_timeout_ms"`
		Name            *string         `json:"name"`
		Enabled         *bool           `json:"enabled"`
		Version         int             `json:"version"`
		Members         *[]RouteMember  `json:"members"`
		Strategy        *string         `json:"strategy"`
		MaxAttempts     *int            `json:"max_attempts"`
		MaxConcurrent   json.RawMessage `json:"max_concurrent"`
		AllowAdjustment *bool           `json:"allow_parameter_adjustment"`
	}
	if !decode(w, r, &in) {
		return
	}
	status := 200
	if len(parts) == 2 {
		v = Route{ID: id("route"), Enabled: true, Version: 1, Members: []RouteMember{}, Strategy: "priority", MaxAttempts: 1, QueueTimeoutMS: 5000, CreatedAt: time.Now().UTC()}
		status = 201
	} else {
		if in.Version != v.Version {
			fail(w, 409, "路由版本已变化", "version")
			return
		}
		v.Version++
	}
	if in.AllowCrossModel != nil {
		v.AllowCrossModel = *in.AllowCrossModel
	}
	if in.StrictContext != nil {
		v.StrictContext = *in.StrictContext
	}
	if len(in.AffinityTTL) > 0 && json.Unmarshal(in.AffinityTTL, &v.AffinityTTLSeconds) != nil {
		fail(w, 400, "亲和期限必须为非负秒数或 null", "affinity_ttl_seconds")
		return
	}
	if err := validateRoutePolicy(v.RoutePolicy); err != nil {
		fail(w, 400, err.Error(), "affinity_ttl_seconds")
		return
	}
	if in.QueueLimit != nil {
		v.QueueLimit = *in.QueueLimit
	}
	if in.QueueTimeoutMS != nil {
		v.QueueTimeoutMS = *in.QueueTimeoutMS
	}
	if v.QueueLimit < 0 || v.QueueTimeoutMS <= 0 {
		fail(w, 400, "队列容量不能为负，等待期限必须为正毫秒数", "queue_limit")
		return
	}
	if in.Name != nil {
		v.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		v.Enabled = *in.Enabled
	}
	if in.Members != nil {
		v.Members = *in.Members
	}
	if in.Strategy != nil {
		v.Strategy = *in.Strategy
	}
	if in.MaxAttempts != nil {
		v.MaxAttempts = *in.MaxAttempts
	}
	if len(in.MaxConcurrent) > 0 && json.Unmarshal(in.MaxConcurrent, &v.MaxConcurrent) != nil {
		fail(w, 400, "路由并发上限必须为正整数或null", "max_concurrent")
		return
	}
	if in.AllowAdjustment != nil {
		v.AllowParameterAdjustment = *in.AllowAdjustment
	}
	if v.Name == "" || len(v.Name) > 100 || v.MaxAttempts < 1 || v.MaxAttempts > 3 || !validRouteStrategy(v.Strategy) || v.MaxConcurrent != nil && *v.MaxConcurrent < 1 {
		fail(w, 400, "路由名称、策略或限制无效", "")
		return
	}
	seen := map[string]bool{}
	for _, member := range v.Members {
		if member.Weight < 1 || seen[member.ModelID] {
			fail(w, 400, "成员不能重复且权重必须为正整数", "members")
			return
		}
		seen[member.ModelID] = true
		m, err := a.Store.model(member.ModelID)
		if err != nil {
			fail(w, 400, "模型成员不存在", "members")
			return
		}
		src, err := a.Store.source(m.SourceID)
		if err != nil || src.Deleted {
			fail(w, 409, "成员来源已删除", "members")
			return
		}
	}
	if _, err := a.Store.DB.Exec("INSERT INTO routes(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", v.ID, encode(v)); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	delete(a.routeWeights, v.ID)
	a.signalAdmission()
	writeJSON(w, status, v)
}
func (a *App) aliasesAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(parts) == 2 && r.Method == "GET" {
		rows, err := a.Store.DB.Query("SELECT data FROM model_aliases ORDER BY public_model")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		items := []Alias{}
		for rows.Next() {
			var b string
			var v Alias
			if err = rows.Scan(&b); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(b), &v); err != nil {
				break
			}
			items = append(items, v)
		}
		if err == nil {
			err = rows.Err()
		}
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil})
		return
	}
	if r.Method == "POST" && len(parts) == 2 {
		var in Alias
		if !decode(w, r, &in) {
			return
		}
		if in.PublicModel == "" || len(in.PublicModel) > 200 || strings.ContainsAny(in.PublicModel, "/\\") {
			fail(w, 400, "别名无效", "public_model")
			return
		}
		if _, err := a.Store.route(in.RouteID); err != nil {
			fail(w, 400, "路由不存在", "route_id")
			return
		}
		var n int
		if err := a.Store.DB.QueryRow("SELECT count(*) FROM model_aliases WHERE public_model=?", in.PublicModel).Scan(&n); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if n > 0 {
			fail(w, 409, "别名已存在", "public_model")
			return
		}
		in.Version = 1
		if _, err := a.Store.DB.Exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", in.PublicModel, in.RouteID, encode(in)); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 201, in)
		return
	}
	if len(parts) == 3 && (r.Method == "GET" || r.Method == "PATCH") {
		var v Alias
		var raw string
		err := a.Store.DB.QueryRow("SELECT data FROM model_aliases WHERE public_model=?", parts[2]).Scan(&raw)
		if err == sql.ErrNoRows {
			fail(w, 404, "别名不存在", "")
			return
		}
		if err != nil || json.Unmarshal([]byte(raw), &v) != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if r.Method == "GET" {
			writeJSON(w, 200, v)
			return
		}
		var in struct {
			Version int    `json:"version"`
			RouteID string `json:"route_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Version != v.Version {
			fail(w, 409, "别名已修改，请刷新", "version")
			return
		}
		if _, e := a.Store.route(in.RouteID); e != nil {
			fail(w, 400, "路由不存在", "route_id")
			return
		}
		v.RouteID, v.Version = in.RouteID, v.Version+1
		if _, e := a.Store.DB.Exec("UPDATE model_aliases SET route_id=?,data=? WHERE public_model=?", v.RouteID, encode(v), v.PublicModel); e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		a.signalAdmission()
		writeJSON(w, 200, v)
		return
	}
	if len(parts) == 3 && r.Method == "DELETE" {
		result, err := a.Store.DB.Exec("DELETE FROM model_aliases WHERE public_model=?", parts[2])
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		n, err := result.RowsAffected()
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if n == 0 {
			fail(w, 404, "别名不存在", "")
			return
		}
		writeJSON(w, 200, map[string]any{"id": parts[2], "deleted": true})
		return
	}
	fail(w, 405, "方法不支持", "")
}

// Caller owns the admission lock; preview evaluates the same snapshot without advancing weights.
func (a *App) selectSource(k ClientKey, model, protocol string, preview bool) (Source, string, []CandidateReason, error) {
	return a.selectSourceExcluding(k, model, protocol, preview, nil)
}
func (a *App) selectSourceExcluding(k ClientKey, model, protocol string, preview bool, excluded map[string]bool, plans ...RoutingPolicyInput) (Source, string, []CandidateReason, error) {
	if k.RouteID == "" {
		src, err := a.Store.source(k.SourceID)
		if err != nil {
			return src, "", nil, err
		}
		if !slices.Contains(src.Models, model) {
			return src, "", nil, fmt.Errorf("模型未在来源配置")
		}
		return src, model, nil, nil
	}
	route, err := a.Store.route(k.RouteID)
	if err != nil || !route.Enabled {
		return Source{}, "", nil, fmt.Errorf("路由不存在或已停用")
	}
	var aliasRoute string
	if err = a.Store.DB.QueryRow("SELECT route_id FROM model_aliases WHERE public_model=?", model).Scan(&aliasRoute); err != nil || aliasRoute != route.ID {
		return Source{}, "", nil, fmt.Errorf("模型别名未绑定此路由")
	}
	ownedAccount := ""
	if len(plans) > 0 && plans[0].OwnedRequestID != "" && a.runningKeys[plans[0].OwnedRequestID] == k.ID {
		if err := a.Store.DB.QueryRow("SELECT json_extract(data,'$.account_id') FROM requests WHERE id=?", plans[0].OwnedRequestID).Scan(&ownedAccount); err != nil {
			return Source{}, "", nil, err
		}
	}
	all := []RoutingPolicyCandidate{}
	for _, member := range route.Members {
		m, me := a.Store.model(member.ModelID)
		src, se := a.Store.source(m.SourceID)
		c := RoutingPolicyCandidate{Source: src, Model: m, Member: member}
		activeAccount := a.accountActive[src.AccountID]
		if ownedAccount != "" && ownedAccount == src.AccountID {
			activeAccount--
		}

		switch {
		case len(plans) > 0 && plans[0].AuthoritativeBinding && (plans[0].BoundSourceID != src.ID || plans[0].BoundModel != "" && plans[0].BoundModel != m.UpstreamModel):
			c.ExcludedReason = "权威前序响应绑定到另一来源或模型"
		case excluded[src.ID]:
			c.ExcludedReason = "本次请求已尝试此来源"
		case me != nil || se != nil:
			c.ExcludedReason = "成员不存在"
		case src.Deleted || !src.Enabled || !m.Enabled:
			c.ExcludedReason = "来源或模型已停用"
		case !src.Configured:
			c.ExcludedReason = "账号未配置"
		case src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.AuthStatus == "rejected":
			c.ExcludedReason = "账号需要重新授权"
		case src.MaxConcurrent != nil && activeAccount >= *src.MaxConcurrent && (len(plans) == 0 || !plans[0].IncludeBusySources):
			c.ExcludedReason = "账号并发已满"
		case protocol == "messages" && src.Kind == "codex_subscription" && (!src.AllowParameterAdjustment || !route.AllowParameterAdjustment):
			c.ExcludedReason = "订阅参数调整未启用"
		}
		if !route.AllowParameterAdjustment {
			c.Source.AllowParameterAdjustment = false
		}
		if c.ExcludedReason == "" {
			operation := "generate"
			if len(plans) > 0 && plans[0].Operation != "" {
				operation = plans[0].Operation
			}
			threshold := 0
			if src.Kind == "codex_subscription" {
				threshold = a.Config.SubscriptionQuotaThreshold
			}
			if blocked, reason := quotaBelowThreshold(src, m.UpstreamModel, operation, time.Now(), threshold); blocked {
				c.ExcludedReason = reason
			}
		}
		all = append(all, c)
	}
	reasons := []CandidateReason{}
	eligible := []RoutingPolicyCandidate{}
	preferred := ""
	if len(plans) > 0 {
		plan := plans[0]
		plan.SubscriptionFirst = true
		plan.AllowPaidFallback = a.Config.AllowPaidFallback
		result, e := a.policyState.Filter(route.RoutePolicy, route.Strategy, plan, all, time.Now())
		reasons = result.Reasons
		if plan.SnapshotOut != nil {
			*plan.SnapshotOut = result.Snapshot
		}
		if e != nil {
			return Source{}, "", reasons, e
		}
		eligible = result.Candidates
		preferred = result.PreferredModelID
	} else {
		anchor := policyModelAnchor(all)
		a.policyState.mu.Lock()
		for _, c := range all {
			reason := CandidateReason{ModelID: c.Model.ID, SourceID: c.Source.ID, AccountID: c.Source.AccountID, UpstreamModel: c.Model.UpstreamModel}
			reason.Reason = c.ExcludedReason
			if reason.Reason == "" && !route.AllowCrossModel && c.Model.UpstreamModel != anchor {
				reason.Reason = "跨模型切换未明确启用"
			}
			if reason.Reason == "" {
				reason.Reason = a.policyState.candidateHealth(c, time.Now())
			}
			if reason.Reason == "" {
				reason.Eligible = true
				reason.Reason = "配置可路由，具体请求能力与真实调用另行验证"
				eligible = append(eligible, c)
			}
			reasons = append(reasons, reason)
		}
		a.policyState.mu.Unlock()
		eligible = preferSubscriptions(eligible, reasons, a.Config.AllowPaidFallback)
		priority := math.MaxInt
		for _, c := range eligible {
			priority = min(priority, c.Member.Priority)
		}
		eligible = slices.DeleteFunc(eligible, func(c RoutingPolicyCandidate) bool { return c.Member.Priority != priority })
	}
	slices.SortFunc(eligible, func(x, y RoutingPolicyCandidate) int { return strings.Compare(x.Model.ID, y.Model.ID) })
	if len(eligible) == 0 {
		status := 429
		if len(plans) > 0 {
			for _, c := range all {
				if c.ExcludedReason == "" {
					status = 422
					break
				}
			}
		}
		return Source{}, "", reasons, &selectionError{status, "没有可用候选，请查看排除原因"}
	}
	selected := eligible[0]
	if preferred != "" {
		for _, c := range eligible {
			if c.Model.ID == preferred {
				selected = c
				break
			}
		}
	} else if route.Strategy != "priority" {
		weights := a.routeWeights[route.ID]
		next := map[string]int{}
		for k, v := range weights {
			next[k] = v
		}
		total, max := 0, math.MinInt
		for _, c := range eligible {
			next[c.Model.ID] += c.Member.Weight
			total += c.Member.Weight
			if next[c.Model.ID] > max {
				max = next[c.Model.ID]
				selected = c
			}
		}
		next[selected.Model.ID] -= total
		if !preview {
			a.routeWeights[route.ID] = next
		}
	}
	if selected.Model.Price != nil {
		selected.Source.Price = selected.Model.Price
	}
	return selected.Source, selected.Model.UpstreamModel, reasons, nil
}
func (a *App) previewRoute(w http.ResponseWriter, r *http.Request, rid string) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Model     string          `json:"model"`
		Protocol  string          `json:"protocol"`
		KeyID     string          `json:"key_id"`
		Request   json.RawMessage `json:"request"`
		SessionID string          `json:"session_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Protocol == "" {
		in.Protocol = "responses"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := ClientKey{RouteID: rid}
	if in.KeyID != "" {
		var b string
		if err := a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", in.KeyID).Scan(&b); err != nil || json.Unmarshal([]byte(b), &key) != nil {
			fail(w, 404, "Key 不存在", "key_id")
			return
		}
		if key.RouteID != rid || !keyValid(key, time.Now()) || !allowed(key.ModelAllowlist, in.Model) || !allowed(key.ProtocolAllowlist, in.Protocol) {
			fail(w, 403, "Key 不允许此预览目标", "")
			return
		}
	}
	var snapshot RoutingPolicySnapshot
	var src Source
	var sent string
	var reasons []CandidateReason
	var err error
	if len(in.Request) > 0 {
		plan := RoutingPolicyInput{Key: key, PublicModel: in.Model, Protocol: in.Protocol, Operation: "generate", ClientRaw: in.Request, SessionID: in.SessionID, MaxResponse: a.Config.MaxResponse, SnapshotOut: &snapshot, Eligibility: func(s Source, b map[string]json.RawMessage) error { _, e := a.prepareAccounting(key, s, b); return e }}
		src, sent, reasons, err = a.selectSourceExcluding(key, in.Model, in.Protocol, true, nil, plan)
	} else {
		src, sent, reasons, err = a.selectSource(key, in.Model, in.Protocol, true)
	}
	var selected any
	if err == nil {
		selected = map[string]any{"source_id": src.ID, "account_id": src.AccountID, "upstream_model": sent}
	}
	writeJSON(w, 200, map[string]any{"selected_candidate": selected, "candidates": reasons, "error": errorMessage(err), "upstream_calls": 0, "policy": snapshot, "full_request_eligibility": len(in.Request) > 0})
}
func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *App) consumeRPM(k ClientKey, now time.Time) (bool, int) {
	if k.Limits.RPM == nil {
		return true, 0
	}
	rpm := *k.Limits.RPM
	b := a.rateBuckets[k.ID]
	if b == nil || b.RPM != rpm {
		b = &rateBucket{Tokens: float64(rpm), At: now, RPM: rpm}
		a.rateBuckets[k.ID] = b
	}
	b.Tokens = math.Min(float64(rpm), b.Tokens+now.Sub(b.At).Seconds()*float64(rpm)/60)
	b.At = now
	if b.Tokens < 1 {
		return false, int(math.Ceil((1 - b.Tokens) * 60 / float64(rpm)))
	}
	b.Tokens--
	return true, 0
}
