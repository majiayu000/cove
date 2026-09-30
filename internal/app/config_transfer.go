package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type transferSource struct {
	LocalID        string `json:"local_id"`
	AccountLocalID string `json:"account_local_id"`
	Source         Source `json:"source"`
}
type transferModel struct {
	LocalID string      `json:"local_id"`
	Model   SourceModel `json:"model"`
}
type transferRoute struct {
	LocalID string `json:"local_id"`
	Route   Route  `json:"route"`
}
type transferAlias struct {
	LocalID string `json:"local_id"`
	Alias   Alias  `json:"alias"`
}
type transferBudget struct {
	LocalID string `json:"local_id"`
	Budget  Budget `json:"budget"`
}
type transferPrice struct {
	LocalID string       `json:"local_id"`
	Price   PriceVersion `json:"price"`
}
type credentialPlaceholder struct {
	LocalID  string `json:"local_id"`
	Provider string `json:"provider"`
	AuthType string `json:"auth_type"`
	Required bool   `json:"required"`
}
type configTransfer struct {
	Format                 string                     `json:"format"`
	Version                int                        `json:"version"`
	ExportedAt             time.Time                  `json:"exported_at"`
	Sources                []transferSource           `json:"sources"`
	Models                 []transferModel            `json:"models"`
	Routes                 []transferRoute            `json:"routes"`
	Aliases                []transferAlias            `json:"aliases"`
	Budgets                []transferBudget           `json:"budgets"`
	Prices                 []transferPrice            `json:"prices"`
	Settings               map[string]settingsChanges `json:"settings"`
	CredentialPlaceholders []credentialPlaceholder    `json:"credential_placeholders"`
}
type transferTarget struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version,omitempty"`
	Hash    string `json:"hash,omitempty"` // Immutable price records have no mutable version counter.
}
type transferEntity struct {
	LocalID      string           `json:"local_id"`
	Kind         string           `json:"kind"`
	Action       string           `json:"action"`
	Name         string           `json:"name"`
	Conflict     bool             `json:"conflict"`
	Dependencies []string         `json:"dependencies"`
	Unsupported  []string         `json:"unsupported,omitempty"`
	Targets      []transferTarget `json:"targets"`
}
type configTransferPreview struct {
	ID                    string                  `json:"preview_id"`
	Hash                  string                  `json:"hash"`
	PackageHash           string                  `json:"package_hash"`
	Mode                  string                  `json:"mode"`
	ExpectedConfigVersion string                  `json:"expected_config_version"`
	Entities              []transferEntity        `json:"entities"`
	MissingCredentials    []credentialPlaceholder `json:"missing_credentials"`
	Unsupported           []string                `json:"unsupported"`
	ExpiresAt             time.Time               `json:"expires_at"`
	Package               configTransfer          `json:"-"`
}

// A string chooses create_new or skip. Replacement always names an exact target
// and its version; immutable price targets use target_hash instead.
type transferResolution struct {
	Action     string `json:"action"`
	TargetID   string `json:"target_id,omitempty"`
	Version    int    `json:"version,omitempty"`
	TargetHash string `json:"target_hash,omitempty"`
}

func (v *transferResolution) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &v.Action)
	}
	type plain transferResolution
	var p plain
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return err
	}
	*v = transferResolution(p)
	return nil
}

type sqlQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Prices and budgets participate even when unrelated to a selected object: the
// preview cannot publish financial configuration after a financial change.
func configurationVersion(ctx context.Context, db sqlQuerier) (string, error) {
	h := sha256.New()
	for _, table := range []string{"accounts", "sources", "source_models", "routes", "model_aliases", "budgets", "prices", "settings"} {
		column := "id"
		data := "data"
		where := ""
		switch table {
		case "accounts":
			data = "json_remove(data,'$.quota_status')"
		case "sources":
			data = "json_remove(data,'$.quota','$.verification','$.continuation_verified')"
		case "source_models":
			data = "json_remove(data,'$.verification','$.verification_results','$.codex_catalog','$.discovered_at')"
		}
		if table == "model_aliases" {
			column = "public_model"
		}
		if table == "settings" {
			column = "key"
			data = "value"
			where = " WHERE key='runtime_settings'"
		}
		rows, err := db.QueryContext(ctx, "SELECT "+column+","+data+" FROM "+table+where+" ORDER BY "+column)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var identity, b string
			if err = rows.Scan(&identity, &b); err != nil {
				rows.Close()
				return "", err
			}
			io.WriteString(h, table+"\x00"+identity+"\x00"+b+"\x00")
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func portablePrice(p *Price) *Price {
	if p == nil {
		return nil
	}
	v := *p
	v.ID = ""
	return &v
}
func portableSource(s Source) Source {
	s.ID = ""
	s.CredentialRef = ""
	s.ProxyURL = nil
	s.Configured = false
	s.AuthStatus = "not_configured"
	s.Version = 1
	s.Generation = 1
	s.AccountGeneration = 1
	s.Continuation = false
	s.Deleted = false
	s.CreatedAt = time.Time{}
	s.Models = []string{}
	s.VerifiedIdentity = nil
	s.Verification = Verification{Status: "untested", Capabilities: []string{}}
	s.Quota = map[string]any{"status": "unknown"}
	s.Price = portablePrice(s.Price)
	return s
}
func portableModel(m SourceModel) SourceModel {
	m.CodexCatalog = nil
	m.ID = ""
	m.Version = 1
	m.Verification = "unverified"
	m.VerificationResults = nil
	m.Discovery = "manual"
	m.DiscoveredAt = nil
	m.MetadataReason = ""
	m.CreatedAt = time.Time{}
	m.Price = portablePrice(m.Price)
	return m
}
func (a *App) collectConfig(ctx context.Context) (configTransfer, error) {
	out := configTransfer{Format: "cove-config", Version: 1, ExportedAt: time.Now().UTC(),
		Sources: []transferSource{}, Models: []transferModel{}, Routes: []transferRoute{}, Aliases: []transferAlias{},
		Budgets: []transferBudget{}, Prices: []transferPrice{}, Settings: map[string]settingsChanges{}, CredentialPlaceholders: []credentialPlaceholder{}}
	sources, err := a.Store.sources()
	if err != nil {
		return out, err
	}
	accounts := map[string]bool{}
	activeSources := map[string]bool{}
	for _, s := range sources {
		if s.Deleted {
			continue
		}
		activeSources[s.ID] = true
		out.Sources = append(out.Sources, transferSource{s.ID, s.AccountID, portableSource(s)})
		if !accounts[s.AccountID] {
			accounts[s.AccountID] = true
			out.CredentialPlaceholders = append(out.CredentialPlaceholders, credentialPlaceholder{s.AccountID, s.Provider, s.Kind, s.Kind != "none"})
		}
	}
	models, err := a.Store.models("")
	if err != nil {
		return out, err
	}
	activeModels := map[string]bool{}
	for _, m := range models {
		if !activeSources[m.SourceID] {
			continue
		}
		activeModels[m.ID] = true
		out.Models = append(out.Models, transferModel{m.ID, portableModel(m)})
	}
	for _, table := range []string{"routes", "model_aliases", "budgets", "prices"} {
		rows, e := a.Store.DB.QueryContext(ctx, "SELECT data FROM "+table+" ORDER BY rowid")
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var b string
			if e = rows.Scan(&b); e != nil {
				break
			}
			switch table {
			case "routes":
				var v Route
				e = json.Unmarshal([]byte(b), &v)
				if e == nil {
					local := v.ID
					v.ID = ""
					v.Version = 1
					v.CreatedAt = time.Time{}
					out.Routes = append(out.Routes, transferRoute{local, v})
				}
			case "model_aliases":
				var v Alias
				e = json.Unmarshal([]byte(b), &v)
				if e == nil {
					v.Version = 1
					out.Aliases = append(out.Aliases, transferAlias{"alias_" + digest(v.PublicModel)[:24], v})
				}
			case "budgets":
				var v Budget
				e = json.Unmarshal([]byte(b), &v)
				if e == nil {
					local := v.ID
					v.ID = ""
					v.Version = 1
					v.CreatedAt = time.Time{}
					out.Budgets = append(out.Budgets, transferBudget{local, v})
				}
			case "prices":
				var v PriceVersion
				e = json.Unmarshal([]byte(b), &v)
				if e == nil && activeModels[v.ModelID] {
					local := v.ID
					v.ID = ""
					out.Prices = append(out.Prices, transferPrice{local, v})
				}
			}
			if e != nil {
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		closeErr := rows.Close()
		if e != nil {
			return out, e
		}
		if closeErr != nil {
			return out, closeErr
		}
	}
	// Actual portable limits are exported even before the first settings edit.
	c := a.Config
	out.Settings["runtime_settings"] = settingsChanges{MaxConcurrent: &c.MaxConcurrent, MaxBody: &c.MaxBody, MaxResponse: &c.MaxResponse,
		MaxEvent: &c.MaxEvent, IdleTimeout: &c.IdleTimeout, TotalTimeout: &c.TotalTimeout, RetentionDays: &c.RetentionDays}
	return out, nil
}
func transferEntities(pack configTransfer) []transferEntity {
	out := []transferEntity{}
	add := func(local, kind, name string, deps ...string) {
		out = append(out, transferEntity{LocalID: local, Kind: kind, Name: name, Dependencies: deps, Targets: []transferTarget{}})
	}
	for _, v := range pack.Sources {
		add(v.LocalID, "source", v.Source.Name, v.AccountLocalID)
	}
	for _, v := range pack.Models {
		add(v.LocalID, "model", v.Model.DisplayName, v.Model.SourceID)
	}
	for _, v := range pack.Routes {
		deps := []string{}
		for _, m := range v.Route.Members {
			deps = append(deps, m.ModelID)
		}
		add(v.LocalID, "route", v.Route.Name, deps...)
	}
	for _, v := range pack.Aliases {
		add(v.LocalID, "alias", v.Alias.PublicModel, v.Alias.RouteID)
	}
	for _, v := range pack.Budgets {
		deps := []string{}
		if v.Budget.Scope.Kind == "route" {
			deps = append(deps, v.Budget.Scope.ID)
		}
		if v.Budget.Scope.Kind == "key" {
			deps = append(deps, "key:"+v.Budget.Scope.ID)
		}
		add(v.LocalID, "budget", v.Budget.Name, deps...)
		if v.Budget.Scope.Kind == "key" {
			out[len(out)-1].Unsupported = []string{"Key 作用域不在明文配置包内；需在目标实例单独创建预算"}
		}
	}
	for _, v := range pack.Prices {
		add(v.LocalID, "price", v.Price.Currency+" "+v.Price.EffectiveAt.Format(time.RFC3339), v.Price.ModelID)
	}
	keys := []string{}
	for key := range pack.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		add(key, "settings", key)
	}
	return out
}
func filterTransferPackage(pack configTransfer, want map[string]bool) configTransfer {
	out := pack
	out.Sources = []transferSource{}
	out.Models = []transferModel{}
	out.Routes = []transferRoute{}
	out.Aliases = []transferAlias{}
	out.Budgets = []transferBudget{}
	out.Prices = []transferPrice{}
	out.Settings = map[string]settingsChanges{}
	out.CredentialPlaceholders = []credentialPlaceholder{}
	accounts := map[string]bool{}
	for _, v := range pack.Sources {
		if want[v.LocalID] {
			out.Sources = append(out.Sources, v)
			accounts[v.AccountLocalID] = true
		}
	}
	for _, v := range pack.Models {
		if want[v.LocalID] {
			out.Models = append(out.Models, v)
		}
	}
	for _, v := range pack.Routes {
		if want[v.LocalID] {
			out.Routes = append(out.Routes, v)
		}
	}
	for _, v := range pack.Aliases {
		if want[v.LocalID] {
			out.Aliases = append(out.Aliases, v)
		}
	}
	for _, v := range pack.Budgets {
		if want[v.LocalID] {
			out.Budgets = append(out.Budgets, v)
		}
	}
	for _, v := range pack.Prices {
		if want[v.LocalID] {
			out.Prices = append(out.Prices, v)
		}
	}
	for key, v := range pack.Settings {
		if want[key] {
			out.Settings[key] = v
		}
	}
	for _, v := range pack.CredentialPlaceholders {
		if accounts[v.LocalID] {
			out.CredentialPlaceholders = append(out.CredentialPlaceholders, v)
		}
	}
	return out
}
func selectConfigDependencies(pack configTransfer, selected []string, include bool) (configTransfer, error) {
	all := transferEntities(pack)
	exists := map[string]transferEntity{}
	want := map[string]bool{}
	for _, v := range all {
		exists[v.LocalID] = v
	}
	if len(selected) == 0 {
		for _, v := range all {
			want[v.LocalID] = true
		}
	} else {
		for _, local := range selected {
			if _, ok := exists[local]; !ok {
				return pack, errors.New("选择的配置对象不存在")
			}
			want[local] = true
		}
	}
	if include {
		for changed := true; changed; {
			changed = false
			for local := range want {
				for _, dep := range exists[local].Dependencies {
					if _, ok := exists[dep]; ok && !want[dep] {
						want[dep] = true
						changed = true
					}
				}
			}
		}
	}
	out := filterTransferPackage(pack, want)
	return out, validateConfigPackage(out)
}
func validateConfigPackage(pack configTransfer) error {
	for _, v := range pack.Budgets {
		if v.Budget.Scope.Kind == "key" {
			return errors.New("Key 预算依赖无法随明文配置包搬运；请选择其他配置")
		}
	}
	return validateTransferPackage(pack, nil)
}
func validateTransferPackage(pack configTransfer, unknown []transferEntity) error {
	if pack.Format != "cove-config" || pack.Version != 1 {
		return errors.New("配置包格式或版本不受支持")
	}
	ids := map[string]string{}
	add := func(local, kind string) error {
		if local == "" || len(local) > 200 || ids[local] != "" {
			return errors.New("配置 local_id 缺失或重复")
		}
		ids[local] = kind
		return nil
	}
	for _, v := range pack.CredentialPlaceholders {
		if e := add(v.LocalID, "credential"); e != nil {
			return e
		}
		if (v.AuthType != "api_key" && v.AuthType != "codex_subscription" && v.AuthType != "none") || v.Required != (v.AuthType != "none") {
			return errors.New("凭据占位认证类型或 required 无效")
		}
	}
	for _, e := range append(transferEntities(pack), unknown...) {
		if err := add(e.LocalID, e.Kind); err != nil {
			return err
		}
	}
	placeholders := map[string]credentialPlaceholder{}
	for _, v := range pack.CredentialPlaceholders {
		placeholders[v.LocalID] = v
	}
	for _, v := range pack.Sources {
		s := v.Source
		if s.ID != "" || s.CredentialRef != "" || s.Configured || s.ProxyURL != nil || s.AccountID != v.AccountLocalID || s.Deleted ||
			len(s.Models) > 0 || s.Continuation || s.Verification.RequestID != "" || s.Verification.TestedAt != nil || len(s.Verification.Capabilities) > 0 || encode(s.Quota) != encode(map[string]any{"status": "unknown"}) ||
			strings.TrimSpace(s.Name) == "" || len(s.Name) > 100 || validateURL(s.BaseURL) != nil ||
			s.MaxConcurrent != nil && *s.MaxConcurrent <= 0 {
			return errors.New("来源含不可搬运状态或无效配置")
		}
		p, ok := placeholders[v.AccountLocalID]
		if !ok && ids[v.AccountLocalID] != "credential" {
			return errors.New("来源引用了缺失凭据占位")
		}
		if ok && (s.Kind != p.AuthType || s.Provider != p.Provider) {
			return errors.New("来源与凭据占位认证类型不一致")
		}
		if s.NativeProtocol != "responses" && s.NativeProtocol != "chat_completions" && s.NativeProtocol != "messages" && s.NativeProtocol != "gemini" {
			return errors.New("来源原生协议不受支持")
		}
		if s.Kind == "none" {
			u, e := url.Parse(s.BaseURL)
			ip := net.ParseIP(u.Hostname())
			if e != nil || s.Provider != "local" && s.Provider != "ollama" || u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return errors.New("无认证来源必须为明确本地 provider 和 loopback 端点")
			}
		}
		if s.Kind == "codex_subscription" && (s.Provider != "codex" || s.NativeProtocol != "responses") {
			return errors.New("订阅来源配置无效")
		}
		if s.Price != nil && !validPrice(*s.Price) {
			return errors.New("来源价格无效")
		}
	}
	for _, v := range pack.Models {
		m := v.Model
		if m.CodexCatalog != nil || m.ID != "" || len(m.VerificationResults) > 0 || m.DiscoveredAt != nil || m.MetadataReason != "" || strings.TrimSpace(m.UpstreamModel) == "" || len(m.UpstreamModel) > 200 || m.ContextLimit != nil && *m.ContextLimit <= 0 ||
			m.MaxOutput != nil && *m.MaxOutput <= 0 || m.Price != nil && !validPrice(*m.Price) {
			return errors.New("模型配置无效")
		}
	}
	for _, v := range pack.Routes {
		r := v.Route
		if r.ID != "" || strings.TrimSpace(r.Name) == "" || len(r.Name) > 100 || len(r.Members) == 0 || r.MaxAttempts < 1 || r.MaxAttempts > 3 ||
			r.MaxConcurrent != nil && *r.MaxConcurrent < 1 || !validRouteStrategy(r.Strategy) || r.QueueLimit < 0 || r.QueueTimeoutMS < 0 || validateRoutePolicy(r.RoutePolicy) != nil {
			return errors.New("路由策略或配置不受支持")
		}
		seen := map[string]bool{}
		for _, m := range r.Members {
			if m.Weight < 1 || seen[m.ModelID] {
				return errors.New("路由成员权重无效")
			}
			seen[m.ModelID] = true
		}
	}
	aliases := map[string]bool{}
	for _, v := range pack.Aliases {
		if strings.TrimSpace(v.Alias.PublicModel) == "" || len(v.Alias.PublicModel) > 200 || aliases[v.Alias.PublicModel] {
			return errors.New("公开模型别名缺失或重复")
		}
		aliases[v.Alias.PublicModel] = true
	}
	for _, v := range pack.Budgets {
		if v.Budget.ID != "" {
			return errors.New("预算必须使用 local_id")
		}
		if err := validateBudget(v.Budget); err != nil {
			return err
		}
	}
	for _, v := range pack.Prices {
		p := v.Price
		if p.ID != "" || p.EffectiveAt.IsZero() || !currencyPattern.MatchString(p.Currency) || !validPriceUnits(p.Units) ||
			p.Provenance.Kind == "" || p.Provenance.URL != "" && validateURL(p.Provenance.URL) != nil {
			return errors.New("价格版本配置无效")
		}
	}
	for key, v := range pack.Settings {
		if key != "runtime_settings" {
			return errors.New("此运行设置对象不受支持")
		}
		if err := validatePortableSettings(v); err != nil {
			return err
		}
	}
	for _, v := range pack.Models {
		if ids[v.Model.SourceID] != "source" {
			return errors.New("模型引用了缺失来源")
		}
	}
	for _, v := range pack.Routes {
		for _, m := range v.Route.Members {
			if ids[m.ModelID] != "model" {
				return errors.New("路由引用了缺失模型")
			}
		}
	}
	for _, v := range pack.Aliases {
		if ids[v.Alias.RouteID] != "route" {
			return errors.New("别名引用了缺失路由")
		}
	}
	for _, v := range pack.Budgets {
		if v.Budget.Scope.Kind == "route" && ids[v.Budget.Scope.ID] != "route" {
			return errors.New("预算引用了缺失路由")
		}
	}
	for _, v := range pack.Prices {
		if ids[v.Price.ModelID] != "model" {
			return errors.New("价格引用了缺失模型")
		}
	}
	return nil
}
func validatePortableSettings(v settingsChanges) error {
	if v.Listen != nil || v.DataDir != nil || v.HeaderTimeout != nil {
		return errors.New("本机 listen/data_dir/header 设置不能由配置包替换")
	}
	for _, n := range []*int{v.MaxConcurrent, v.MaxEvent, v.IdleTimeout, v.TotalTimeout} {
		if n != nil && *n < 1 {
			return errors.New("运行限制与超时必须为正数")
		}
	}
	for _, n := range []*int64{v.MaxBody, v.MaxResponse} {
		if n != nil && *n < 1 {
			return errors.New("运行大小限制必须为正数")
		}
	}
	if v.RetentionDays != nil && (*v.RetentionDays < 1 || *v.RetentionDays > 365) {
		return errors.New("保留期必须为1到365天")
	}
	return nil
}

// Decode each object independently so an explicit skip can import a supported
// subset without silently stripping an unknown field from a selected object.
func decodeTransferPackage(b []byte) (configTransfer, []transferEntity, []string, error) {
	var pack configTransfer
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || raw == nil {
		return pack, nil, nil, errors.New("配置 JSON 无效")
	}
	supported := map[string]bool{}
	for _, k := range []string{"format", "version", "exported_at", "sources", "models", "routes", "aliases", "budgets", "prices", "settings", "credential_placeholders"} {
		supported[k] = true
	}
	global := []string{}
	for k := range raw {
		if !supported[k] {
			global = append(global, k)
		}
	}
	sort.Strings(global)
	if len(global) > 0 {
		return pack, nil, global, nil
	}
	for k, dst := range map[string]any{"format": &pack.Format, "version": &pack.Version, "exported_at": &pack.ExportedAt} {
		if value, ok := raw[k]; ok {
			if json.Unmarshal(value, dst) != nil {
				return pack, nil, nil, errors.New("配置 JSON 无效")
			}
		}
	}
	unknown := []transferEntity{}
	read := func(key, kind string, create func() any, appendValue func(any)) error {
		var list []json.RawMessage
		if value, ok := raw[key]; ok && json.Unmarshal(value, &list) != nil {
			return errors.New("配置实体列表无效")
		}
		for _, value := range list {
			var local struct {
				LocalID string `json:"local_id"`
			}
			_ = json.Unmarshal(value, &local)
			dst := create()
			d := json.NewDecoder(strings.NewReader(string(value)))
			d.DisallowUnknownFields()
			if err := d.Decode(dst); err != nil {
				if strings.HasPrefix(err.Error(), "json: unknown field ") {
					unknown = append(unknown, transferEntity{LocalID: local.LocalID, Kind: kind, Action: "skip", Unsupported: []string{strings.TrimPrefix(err.Error(), "json: unknown field ")}, Dependencies: []string{}, Targets: []transferTarget{}})
					continue
				}
				return errors.New("配置实体 JSON 无效")
			}
			appendValue(dst)
		}
		return nil
	}
	for _, e := range []error{
		read("sources", "source", func() any { return &transferSource{} }, func(v any) { pack.Sources = append(pack.Sources, *v.(*transferSource)) }),
		read("models", "model", func() any { return &transferModel{} }, func(v any) { pack.Models = append(pack.Models, *v.(*transferModel)) }),
		read("routes", "route", func() any { return &transferRoute{} }, func(v any) { pack.Routes = append(pack.Routes, *v.(*transferRoute)) }),
		read("aliases", "alias", func() any { return &transferAlias{} }, func(v any) { pack.Aliases = append(pack.Aliases, *v.(*transferAlias)) }),
		read("budgets", "budget", func() any { return &transferBudget{} }, func(v any) { pack.Budgets = append(pack.Budgets, *v.(*transferBudget)) }),
		read("prices", "price", func() any { return &transferPrice{} }, func(v any) { pack.Prices = append(pack.Prices, *v.(*transferPrice)) }),
		read("credential_placeholders", "credential", func() any { return &credentialPlaceholder{} }, func(v any) {
			pack.CredentialPlaceholders = append(pack.CredentialPlaceholders, *v.(*credentialPlaceholder))
		}),
	} {
		if e != nil {
			return pack, nil, nil, e
		}
	}
	var settings map[string]json.RawMessage
	if value, ok := raw["settings"]; ok && json.Unmarshal(value, &settings) != nil {
		return pack, nil, nil, errors.New("settings 对象无效")
	}
	pack.Settings = map[string]settingsChanges{}
	for key, value := range settings {
		var v settingsChanges
		d := json.NewDecoder(strings.NewReader(string(value)))
		d.DisallowUnknownFields()
		err := d.Decode(&v)
		if key != "runtime_settings" || err != nil || validatePortableSettings(v) != nil {
			unknown = append(unknown, transferEntity{LocalID: key, Kind: "settings", Action: "skip", Unsupported: []string{"不支持的运行设置字段或对象"}, Dependencies: []string{}, Targets: []transferTarget{}})
		} else {
			pack.Settings[key] = v
		}
	}
	if err := validateTransferPackage(pack, unknown); err != nil {
		return pack, nil, nil, err
	}
	return pack, unknown, nil, nil
}
func (a *App) configExportAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Selected []string `json:"selected_entity_ids"`
		Include  *bool    `json:"include_dependencies"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	pack, err := a.collectConfig(r.Context())
	a.mu.Unlock()
	if err == nil {
		pack, err = selectConfigDependencies(pack, in.Selected, in.Include == nil || *in.Include)
	}
	if err != nil {
		fail(w, 422, err.Error(), "")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=cove-config.json")
	writeJSON(w, 200, pack)
}
func transferTargets(ctx context.Context, db sqlQuerier) (map[string][]transferTarget, error) {
	out := map[string][]transferTarget{}
	for _, kind := range []string{"source", "model", "route", "alias", "budget", "price", "settings"} {
		table, column, data := "", "", "data"
		where := ""
		switch kind {
		case "source":
			table = "sources"
			column = "id"
			where = " WHERE json_extract(data,'$.deleted')!=1"
		case "model":
			table = "source_models"
			column = "id"
		case "route":
			table = "routes"
			column = "id"
		case "alias":
			table = "model_aliases"
			column = "public_model"
		case "budget":
			table = "budgets"
			column = "id"
		case "price":
			table = "prices"
			column = "id"
		case "settings":
			table = "settings"
			column = "key"
			data = "value"
			where = " WHERE key='runtime_settings'"
		}
		rows, err := db.QueryContext(ctx, "SELECT "+column+","+data+" FROM "+table+where+" ORDER BY "+column)
		if err != nil {
			return nil, err
		}
		out[kind] = []transferTarget{}
		for rows.Next() {
			var identity, b string
			if err = rows.Scan(&identity, &b); err != nil {
				break
			}
			var meta struct {
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				PublicModel string `json:"public_model"`
				Version     int    `json:"version"`
			}
			if err = json.Unmarshal([]byte(b), &meta); err != nil {
				break
			}
			name := meta.Name
			if kind == "model" {
				name = meta.DisplayName
			}
			if kind == "alias" {
				name = meta.PublicModel
			}
			if kind == "price" || kind == "settings" {
				name = identity
			}
			out[kind] = append(out[kind], transferTarget{ID: identity, Name: name, Version: meta.Version, Hash: digest(b)})
		}
		if err == nil {
			err = rows.Err()
		}
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if len(out["settings"]) == 0 {
		out["settings"] = []transferTarget{{ID: "runtime_settings", Name: "runtime_settings", Version: 1}}
	}
	return out, nil
}
func (a *App) configImportPreviewAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "replace_selected" {
		fail(w, 422, "mode 必须为 create 或 replace_selected", "mode")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, 422, "配置 JSON 超过 8 MiB 或不可读", "file")
		return
	}
	pack, unknown, global, err := decodeTransferPackage(b)
	if err != nil {
		fail(w, 422, err.Error(), "file")
		return
	}
	if len(global) > 0 {
		writeJSON(w, 200, map[string]any{"unsupported": global, "entities": []transferEntity{}, "can_apply": false})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	version, err := configurationVersion(r.Context(), a.Store.DB)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	targets, err := transferTargets(r.Context(), a.Store.DB)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	d, folder, err := a.newLocalArtifact(bearer(r), "config_preview", "", "")
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	p := configTransferPreview{ID: d.ID, Hash: digest(string(b)), Mode: mode, ExpectedConfigVersion: version, PackageHash: digest(encode(pack)),
		Entities: append(transferEntities(pack), unknown...), MissingCredentials: pack.CredentialPlaceholders, Unsupported: []string{}, ExpiresAt: d.CreatedAt.Add(operationArtifactTTL)}
	for i := range p.Entities {
		e := &p.Entities[i]
		e.Targets = targets[e.Kind]
		if e.Targets == nil {
			e.Targets = []transferTarget{}
		}
		for _, t := range e.Targets {
			if t.Name == e.Name || e.Kind == "settings" {
				e.Conflict = true
			}
		}
		e.Action = "create"
		if mode == "replace_selected" {
			e.Action = "skip"
		} else if e.Conflict {
			e.Action = "conflict"
		}
		if len(e.Unsupported) > 0 {
			e.Action = "skip"
			for _, u := range e.Unsupported {
				p.Unsupported = append(p.Unsupported, e.LocalID+": "+u)
			}
		}
	}
	// Persist only known typed fields, never the untrusted raw JSON or a secret.
	if err = writePrivateJSON(filepath.Join(folder, "package.json"), pack); err == nil {
		err = writePrivateJSON(filepath.Join(folder, "preview.json"), p)
	}
	if err != nil {
		cleanupErr := os.RemoveAll(folder)
		if cleanupErr != nil {
			a.maintenanceError = "配置预览清理失败"
		}
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, p)
}
func (a *App) configImportApplyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		PreviewID   string                        `json:"preview_id"`
		Expected    string                        `json:"expected_config_version"`
		Resolutions map[string]transferResolution `json:"selected_resolutions"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	descriptor, folder, err := a.readLocalArtifact(in.PreviewID, bearer(r))
	if err != nil || descriptor.Kind != "config_preview" {
		fail(w, 404, "配置预览不存在或已过期", "")
		return
	}
	var preview configTransferPreview
	var pack configTransfer
	b, err := os.ReadFile(filepath.Join(folder, "preview.json"))
	if err != nil || json.Unmarshal(b, &preview) != nil {
		fail(w, 404, "配置预览不可读", "")
		return
	}
	b, err = os.ReadFile(filepath.Join(folder, "package.json"))
	if err != nil || json.Unmarshal(b, &pack) != nil || digest(encode(pack)) != preview.PackageHash {
		fail(w, 404, "配置预览包不可读", "")
		return
	}
	tx, err := a.Store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	version, err := configurationVersion(r.Context(), tx)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if version != in.Expected || version != preview.ExpectedConfigVersion {
		fail(w, 409, "配置已改变，请重新预览", "expected_config_version")
		return
	}
	ids := map[string]string{}
	chosen := map[string]transferResolution{}
	want := map[string]bool{}
	targetsUsed := map[string]bool{}
	for _, e := range preview.Entities {
		res, explicit := in.Resolutions[e.LocalID]
		if !explicit {
			if preview.Mode == "replace_selected" {
				res.Action = "skip"
			} else {
				res.Action = "create_new"
			}
			if preview.Mode == "create" && (e.Conflict || len(e.Unsupported) > 0) {
				fail(w, 409, "冲突或不支持对象需逐项明确解决或跳过", "selected_resolutions")
				return
			}
		}
		if res.Action != "create_new" && res.Action != "skip" && res.Action != "replace" {
			fail(w, 422, "action 必须为 create_new、skip 或 replace", "selected_resolutions")
			return
		}
		chosen[e.LocalID] = res
		if res.Action == "skip" && res.TargetID == "" {
			continue
		}
		if len(e.Unsupported) > 0 || e.Kind == "credential" {
			fail(w, 422, "不支持对象不能发布；请明确跳过", "selected_resolutions")
			return
		}
		if res.Action == "replace" || res.Action == "skip" {
			if res.Action == "replace" && preview.Mode != "replace_selected" {
				fail(w, 409, "替换需要 replace_selected 预览", "mode")
				return
			}
			found := false
			for _, t := range e.Targets {
				if t.ID == res.TargetID {
					found = true
					if e.Kind == "price" {
						if res.TargetHash == "" || res.TargetHash != t.Hash {
							fail(w, 409, "价格目标快照不匹配", "target_hash")
							return
						}
					} else if res.Version != t.Version {
						fail(w, 409, "替换目标版本不匹配", "version")
						return
					}
				}
			}
			if !found || res.Action == "replace" && targetsUsed[e.Kind+"\x00"+res.TargetID] {
				fail(w, 409, "替换目标不存在或被重复选择", "target_id")
				return
			}
			if res.Action == "replace" {
				targetsUsed[e.Kind+"\x00"+res.TargetID] = true
			}
			ids[e.LocalID] = res.TargetID
		} else {
			if e.Kind == "settings" {
				fail(w, 409, "运行设置为单例；需明确选择 replace 和目标版本", "selected_resolutions")
				return
			}
			if e.Kind == "alias" && e.Conflict {
				fail(w, 409, "公开别名已存在；需替换或跳过", "selected_resolutions")
				return
			}
			prefix := e.Kind
			if prefix == "source" {
				prefix = "src"
			}
			ids[e.LocalID] = id(prefix)
		}
		if res.Action != "skip" {
			want[e.LocalID] = true
		}
	}
	for local := range in.Resolutions {
		if _, ok := chosen[local]; !ok {
			fail(w, 422, "选择了预览以外的对象", "selected_resolutions")
			return
		}
	}
	for _, e := range preview.Entities {
		if want[e.LocalID] {
			for _, dep := range e.Dependencies {
				// Credential placeholders are supplied separately and never merge by name.
				isCredential := false
				for _, p := range pack.CredentialPlaceholders {
					if p.LocalID == dep {
						isCredential = true
					}
				}
				if !isCredential && ids[dep] == "" {
					fail(w, 409, "所选对象依赖被跳过或不支持的配置："+e.LocalID, "selected_resolutions")
					return
				}
			}
		}
	}
	selected := filterTransferPackage(pack, want)
	bound := []transferEntity{}
	for _, e := range preview.Entities {
		if chosen[e.LocalID].Action == "skip" && ids[e.LocalID] != "" {
			bound = append(bound, e)
		}
	}
	if err = validateTransferPackage(selected, bound); err != nil {
		fail(w, 422, err.Error(), "selected_resolutions")
		return
	}
	if len(want) == 0 {
		fail(w, 422, "至少选择一个受支持对象", "selected_resolutions")
		return
	}
	now := time.Now().UTC()
	if err = a.publishTransfer(r.Context(), tx, selected, chosen, ids, now); err != nil {
		var conflict *transferConflict
		if errors.As(err, &conflict) {
			fail(w, 409, conflict.Error(), "selected_resolutions")
		} else {
			fail(w, 503, "配置发布失败，所有变更已回滚", "")
		}
		return
	}
	audit := map[string]any{"kind": "config_import", "at": now, "preview_id": preview.ID, "entity_count": len(want), "configuration_hash": preview.Hash, "resolutions": chosen}
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO settings(key,value) VALUES(?,?)", "config_import_audit_"+id("audit"), encode(audit)); err != nil {
		fail(w, 503, "配置发布失败，所有变更已回滚", "")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 503, "配置事务提交失败；请刷新检查状态", "")
		return
	}
	if changes, ok := selected.Settings["runtime_settings"]; ok {
		a.applyTransferredSettings(changes)
	}
	for _, v := range selected.Routes {
		delete(a.routeWeights, ids[v.LocalID])
	}
	a.signalAdmission()
	if err = os.RemoveAll(folder); err != nil {
		fail(w, 503, "配置已提交，但预览清理失败；请刷新配置页", "")
		return
	}
	created, updated := 0, 0
	for local := range want {
		if chosen[local].Action == "create_new" {
			created++
		} else {
			updated++
		}
	}
	writeJSON(w, 200, map[string]any{"state": "succeeded", "published_entities": len(want), "created_entities": created, "updated_entities": updated, "credential_state": "new_placeholders_unconfigured_existing_preserved",
		"message": "所选配置已在同一事务发布；新账号凭据需单独配置，替换账号凭据保留。"})
}

type transferConflict struct{ message string }

func (e *transferConflict) Error() string { return e.message }
func transferReject(message string) error { return &transferConflict{message} }
func readTransferRow(ctx context.Context, tx *sql.Tx, table, column, identity string, dst any) error {
	var b string
	if err := tx.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE "+column+"=?", identity).Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal([]byte(b), dst)
}
func (a *App) publishTransfer(ctx context.Context, tx *sql.Tx, pack configTransfer, res map[string]transferResolution, ids map[string]string, now time.Time) error {
	exec := func(q string, args ...any) error { _, e := tx.ExecContext(ctx, q, args...); return e }
	accounts := map[string]Account{}
	createdAccounts := map[string]bool{}
	// Replaced sources retain their exact account and credential generation.
	for _, v := range pack.Sources {
		if res[v.LocalID].Action != "replace" {
			continue
		}
		var old Source
		if e := readTransferRow(ctx, tx, "sources", "id", ids[v.LocalID], &old); e != nil {
			return e
		}
		var account Account
		if e := readTransferRow(ctx, tx, "accounts", "id", old.AccountID, &account); e != nil {
			return e
		}
		if old.Deleted || old.Kind != v.Source.Kind || old.Provider != v.Source.Provider || account.AuthType != v.Source.Kind || account.Provider != v.Source.Provider {
			return transferReject("替换来源的认证类型与 provider 必须保持一致；请创建新来源")
		}
		if origin(old.BaseURL) != origin(v.Source.BaseURL) {
			return transferReject("跨 origin 更换来源需要新目标凭据；配置包不能携带凭据，请创建新来源")
		}
		if v.Source.Kind == "codex_subscription" && old.BaseURL != v.Source.BaseURL {
			return transferReject("订阅端点由本机运行配置固定")
		}
		if previous, ok := accounts[v.AccountLocalID]; ok && previous.ID != account.ID {
			return transferReject("同一凭据占位被选择到不同账号；请拆分配置包或调整目标")
		}
		accounts[v.AccountLocalID] = account
	}
	for _, v := range pack.CredentialPlaceholders {
		if _, ok := accounts[v.LocalID]; ok {
			continue
		}
		state := "not_configured"
		if v.AuthType == "none" {
			state = "not_required"
		}
		account := Account{ID: id("acct"), Name: v.Provider, Provider: v.Provider, AuthType: v.AuthType, Generation: 1, Version: 1, AuthState: state, CreatedAt: now, Identity: map[string]any{"verified": false, "subject_hash": nil, "display_name": nil}, QuotaStatus: "unknown", SourceIDs: []string{}}
		if e := exec("INSERT INTO accounts(id,credential_ref,generation,data) VALUES(?,'',?,?)", account.ID, account.Generation, encode(account)); e != nil {
			return e
		}
		accounts[v.LocalID] = account
		createdAccounts[v.LocalID] = true
	}
	// Do not attach a newly imported source to a configured replacement account.
	// A shared placeholder may represent only newly created sources or only the
	// explicitly selected replacement account, never implicit credential reuse.
	for _, v := range pack.Sources {
		if res[v.LocalID].Action == "create_new" && !createdAccounts[v.AccountLocalID] {
			return transferReject("创建新来源不能通过替换对象隐式复用账号凭据；请拆分凭据占位")
		}
		s := portableSource(v.Source)
		account := accounts[v.AccountLocalID]
		s.ID = ids[v.LocalID]
		s.AccountID = account.ID
		s.AccountGeneration = account.Generation
		s.AuthStatus = account.AuthState
		s.Configured = account.CredentialPresent || account.AuthType == "none"
		s.CreatedAt = now
		if s.Kind == "codex_subscription" && s.BaseURL != a.Config.Codex.BaseURL {
			return transferReject("导入订阅端点与本机运行配置不一致")
		}
		if res[v.LocalID].Action == "replace" {
			var old Source
			if e := readTransferRow(ctx, tx, "sources", "id", s.ID, &old); e != nil {
				return e
			}
			s.Version = old.Version + 1
			s.Generation = old.Generation
			s.CreatedAt = old.CreatedAt
			s.ProxyURL = old.ProxyURL
			changed := old.BaseURL != s.BaseURL || old.NativeProtocol != s.NativeProtocol
			if changed {
				if a.sourceRunning(s.ID) {
					return transferReject("来源仍有运行请求；请等待结束后替换端点")
				}
				s.Generation++
				// Model evidence and old resource bindings retain their original
				// generations; no binding is moved onto the new endpoint.
				if e := exec("UPDATE source_models SET data=json_set(data,'$.verification','unverified','$.verification_results',json('[]')) WHERE source_id=?", s.ID); e != nil {
					return e
				}
			}
			if e := exec("UPDATE sources SET data=? WHERE id=?", encode(s), s.ID); e != nil {
				return e
			}
		} else if e := exec("INSERT INTO sources(id,account_id,data) VALUES(?,?,?)", s.ID, s.AccountID, encode(s)); e != nil {
			return e
		}
	}
	for _, v := range pack.Models {
		m := portableModel(v.Model)
		m.ID = ids[v.LocalID]
		m.SourceID = ids[m.SourceID]
		m.CreatedAt = now
		if res[v.LocalID].Action == "replace" {
			var old SourceModel
			if e := readTransferRow(ctx, tx, "source_models", "id", m.ID, &old); e != nil {
				return e
			}
			if old.SourceID != m.SourceID || old.UpstreamModel != m.UpstreamModel {
				return transferReject("替换模型不能移动来源或改变上游标识；请创建新模型")
			}
			m.Version = old.Version + 1
			m.CreatedAt = old.CreatedAt
			if e := exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID); e != nil {
				return e
			}
		} else {
			var count int
			if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM source_models WHERE source_id=? AND upstream_model=?", m.SourceID, m.UpstreamModel).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return transferReject("目标来源已有同标识模型；请选择替换或跳过")
			}
			if e := exec("INSERT INTO source_models(id,source_id,upstream_model,data) VALUES(?,?,?,?)", m.ID, m.SourceID, m.UpstreamModel, encode(m)); e != nil {
				return e
			}
		}
	}
	// Keep the source's model catalog consistent, preserving every unselected model.
	changedSources := map[string]bool{}
	for _, v := range pack.Sources {
		changedSources[ids[v.LocalID]] = true
	}
	for _, v := range pack.Models {
		changedSources[ids[v.Model.SourceID]] = true
	}
	for sid := range changedSources {
		var s Source
		if e := readTransferRow(ctx, tx, "sources", "id", sid, &s); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, "SELECT upstream_model FROM source_models WHERE source_id=? AND json_extract(data,'$.enabled')=1 ORDER BY upstream_model", s.ID)
		if e != nil {
			return e
		}
		oldModels := append([]string{}, s.Models...)
		s.Models = []string{}
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				break
			}
			s.Models = append(s.Models, name)
		}
		if e == nil {
			e = rows.Err()
		}
		closeErr := rows.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if resBySource(pack, res, ids, s.ID) == "skip" {
			sort.Strings(oldModels)
			if encode(oldModels) == encode(s.Models) {
				continue
			}
			s.Version++
		}
		if e = exec("UPDATE sources SET data=? WHERE id=?", encode(s), s.ID); e != nil {
			return e
		}
	}
	for _, v := range pack.Routes {
		r := v.Route
		r.ID = ids[v.LocalID]
		r.Version = 1
		r.CreatedAt = now
		if r.QueueTimeoutMS == 0 {
			r.QueueTimeoutMS = 5000
		}
		// The package is reused by value; deep-copy before changing member IDs.
		r.Members = append([]RouteMember(nil), r.Members...)
		mapped := map[string]bool{}
		for i := range r.Members {
			r.Members[i].ModelID = ids[r.Members[i].ModelID]
			if mapped[r.Members[i].ModelID] {
				return transferReject("路由成员解析后重复；请调整显式关联目标")
			}
			mapped[r.Members[i].ModelID] = true
		}
		if res[v.LocalID].Action == "replace" {
			var old Route
			if e := readTransferRow(ctx, tx, "routes", "id", r.ID, &old); e != nil {
				return e
			}
			r.Version = old.Version + 1
			r.CreatedAt = old.CreatedAt
			if e := exec("UPDATE routes SET data=? WHERE id=?", encode(r), r.ID); e != nil {
				return e
			}
		} else if e := exec("INSERT INTO routes(id,data) VALUES(?,?)", r.ID, encode(r)); e != nil {
			return e
		}
	}
	for _, v := range pack.Aliases {
		alias := v.Alias
		alias.RouteID = ids[alias.RouteID]
		alias.Version = 1
		if res[v.LocalID].Action == "replace" {
			var old Alias
			if e := readTransferRow(ctx, tx, "model_aliases", "public_model", ids[v.LocalID], &old); e != nil {
				return e
			}
			if old.PublicModel != alias.PublicModel {
				return transferReject("替换别名不能改名；请创建新别名")
			}
			alias.Version = old.Version + 1
			if e := exec("UPDATE model_aliases SET route_id=?,data=? WHERE public_model=?", alias.RouteID, encode(alias), alias.PublicModel); e != nil {
				return e
			}
		} else {
			var count int
			if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM model_aliases WHERE public_model=?", alias.PublicModel).Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return transferReject("公开别名冲突未解决")
			}
			if e := exec("INSERT INTO model_aliases(public_model,route_id,data) VALUES(?,?,?)", alias.PublicModel, alias.RouteID, encode(alias)); e != nil {
				return e
			}
		}
	}
	for _, v := range pack.Budgets {
		b := v.Budget
		b.ID = ids[v.LocalID]
		b.Version = 1
		b.CreatedAt = now
		if b.Scope.Kind == "route" {
			b.Scope.ID = ids[b.Scope.ID]
		}
		var old Budget
		if res[v.LocalID].Action == "replace" {
			var e error
			old, e = readBudget(tx, b.ID)
			if e != nil {
				return e
			}
			if old.Scope != b.Scope || old.Currency != b.Currency || old.Mode != b.Mode || encode(old.Period) != encode(b.Period) {
				return transferReject("预算作用域、币种、模式和周期冻结；请创建新预算")
			}
			_, end, e := budgetBounds(old.Period, now)
			if e != nil {
				return e
			}
			if old.Period.Kind == "fixed" && !now.Before(end) {
				return transferReject("已关闭预算周期只读")
			}
			b.Version = old.Version + 1
			b.CreatedAt = old.CreatedAt
		}
		if e := validateBudgetScope(tx, b); e != nil {
			return e
		}
		if e := saveBudget(tx, b); e != nil {
			return e
		}
		if e := exec("INSERT INTO accounting_audit(id,request_id,created_at,data) VALUES(?,NULL,?,?)", id("audit"), now.Format(time.RFC3339Nano), encode(map[string]any{"kind": "budget_change", "budget_id": b.ID, "before": old, "after": b})); e != nil {
			return e
		}
	}
	for _, v := range pack.Prices {
		p := v.Price
		p.ID = ids[v.LocalID]
		p.ModelID = ids[p.ModelID]
		p.EffectiveAt = p.EffectiveAt.UTC()
		if res[v.LocalID].Action == "replace" {
			var old PriceVersion
			if e := readTransferRow(ctx, tx, "prices", "id", p.ID, &old); e != nil {
				return e
			}
			if old.ModelID != p.ModelID || !p.EffectiveAt.After(old.EffectiveAt) {
				return transferReject("价格历史不可覆盖；替换须为同模型较晚生效的新版本")
			}
			p.ID = id("price")
			ids[v.LocalID] = p.ID
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM prices WHERE model_id=? AND effective_at=?", p.ModelID, p.EffectiveAt.Format(time.RFC3339Nano)).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return transferReject("同模型同生效时间已有不可变价格版本")
		}
		if e := exec("INSERT INTO prices(id,model_id,effective_at,data) VALUES(?,?,?,?)", p.ID, p.ModelID, p.EffectiveAt.Format(time.RFC3339Nano), encode(p)); e != nil {
			return e
		}
	}
	// Snapshot the latest currently effective price once all versions are inserted,
	// independent of package ordering and without touching historical attempts.
	for _, v := range pack.Prices {
		mid := ids[v.Price.ModelID]
		var raw string
		e := tx.QueryRowContext(ctx, "SELECT data FROM prices WHERE model_id=? AND effective_at<=? ORDER BY effective_at DESC LIMIT 1", mid, now.Format(time.RFC3339Nano)).Scan(&raw)
		if e == sql.ErrNoRows {
			continue
		}
		if e != nil {
			return e
		}
		var price PriceVersion
		if e = json.Unmarshal([]byte(raw), &price); e != nil {
			return e
		}
		var m SourceModel
		if e = readTransferRow(ctx, tx, "source_models", "id", mid, &m); e != nil {
			return e
		}
		snapshot := price.snapshot()
		if encode(m.Price) != encode(&snapshot) {
			m.Price = &snapshot
			m.Version++
			if e = exec("UPDATE source_models SET data=? WHERE id=?", encode(m), mid); e != nil {
				return e
			}
		}
	}
	if changes, ok := pack.Settings["runtime_settings"]; ok {
		var state runtimeSettings
		state.Version = 1
		var raw string
		e := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='runtime_settings'").Scan(&raw)
		if e != sql.ErrNoRows && e != nil {
			return e
		}
		if e == nil {
			if e = json.Unmarshal([]byte(raw), &state); e != nil {
				return e
			}
		}
		applied := changes
		applied.RetentionDays = nil
		state.Current = mergeSettings(state.Current, applied)
		if changes.RetentionDays != nil {
			state.RetentionDays = changes.RetentionDays
		}
		state.Version++
		if e = exec("INSERT INTO settings(key,value) VALUES('runtime_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(state)); e != nil {
			return e
		}
	}
	return nil
}
func (a *App) applyTransferredSettings(changes settingsChanges) {
	changes.RetentionDays = nil // Destructive retention cleanup still needs its own preview and action.
	changes.apply(&a.Config)
	if changes.MaxConcurrent != nil {
		active := len(a.slots)
		capacity := a.Config.MaxConcurrent
		if capacity < active {
			capacity = active
		}
		next := make(chan struct{}, capacity)
		for i := 0; i < active; i++ {
			next <- struct{}{}
		}
		a.slots = next
	}
}

func resBySource(pack configTransfer, res map[string]transferResolution, ids map[string]string, sid string) string {
	for _, v := range pack.Sources {
		if ids[v.LocalID] == sid {
			return res[v.LocalID].Action
		}
	}
	return "skip"
}
