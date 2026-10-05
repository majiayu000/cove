package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

type Limits struct {
	RPM           *int `json:"rpm"`
	TPM           *int `json:"tpm"`
	MaxConcurrent *int `json:"max_concurrent"`
}
type Account struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Provider          string         `json:"provider"`
	AuthType          string         `json:"auth_type"`
	Generation        int            `json:"generation"`
	Version           int            `json:"version"`
	AuthState         string         `json:"auth_state"`
	CredentialPresent bool           `json:"credential_present"`
	CreatedAt         time.Time      `json:"created_at"`
	SourceIDs         []string       `json:"source_ids"`
	Deleted           bool           `json:"deleted"`
	Identity          map[string]any `json:"identity"`
	QuotaStatus       string         `json:"quota_status"`
}
type SourceModel struct {
	CodexModelMetadata
	WebSocketModelCapabilities
	ExtendedModelCapabilities
	Modalities          []string                  `json:"modalities"`
	Features            []string                  `json:"features"`
	MetadataReason      string                    `json:"metadata_reason"`
	VerificationResults []ModelVerificationResult `json:"verification_results"`
	ID                  string                    `json:"id"`
	SourceID            string                    `json:"source_id"`
	UpstreamModel       string                    `json:"upstream_model"`
	DisplayName         string                    `json:"display_name"`
	Enabled             bool                      `json:"enabled"`
	Version             int                       `json:"version"`
	Discovery           string                    `json:"discovery"`
	Verification        string                    `json:"verification"`
	DiscoveredAt        *time.Time                `json:"discovered_at"`
	CatalogOrder        *int                      `json:"catalog_order,omitempty"`
	ContextLimit        *int64                    `json:"context_limit"`
	MaxOutput           *int64                    `json:"max_output"`
	Price               *Price                    `json:"price"`
	CreatedAt           time.Time                 `json:"created_at"`
}
type Operation struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	ObjectID  string    `json:"object_id"`
	State     string    `json:"state"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
	Result    any       `json:"result,omitempty"`
}

func (s *Store) model(mid string) (SourceModel, error) {
	var m SourceModel
	var b string
	err := s.DB.QueryRow("SELECT data FROM source_models WHERE id=?", mid).Scan(&b)
	if err == nil {
		err = json.Unmarshal([]byte(b), &m)
	}
	if err == nil {
		s.effectiveModelVerification(&m)
	}
	return m, err
}
func (s *Store) models(source string) ([]SourceModel, error) {
	query := "SELECT data FROM source_models"
	args := []any{}
	if source != "" {
		query += " WHERE source_id=?"
		args = append(args, source)
	}
	query += " ORDER BY source_id,id"
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceModel{}
	for rows.Next() {
		var b string
		var m SourceModel
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		s.effectiveModelVerification(&out[i])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceID != out[j].SourceID {
			return out[i].SourceID < out[j].SourceID
		}
		if out[i].CatalogOrder == nil {
			return false
		}
		if out[j].CatalogOrder == nil {
			return true
		}
		return *out[i].CatalogOrder < *out[j].CatalogOrder
	})
	return out, nil
}
func (s *Store) account(aid string) (Account, string, error) {
	var a Account
	var b, ref string
	err := s.DB.QueryRow("SELECT data,credential_ref FROM accounts WHERE id=?", aid).Scan(&b, &ref)
	if err == nil {
		err = json.Unmarshal([]byte(b), &a)
	}
	return a, ref, err
}

func (a *App) modelsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[3] == "verify" {
		a.verifyModelAPI(w, r, parts[2])
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		items, err := a.Store.models(r.URL.Query().Get("source_id"))
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil, "snapshot_at": time.Now().UTC()})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(parts) == 2 && r.Method == "POST" {
		var in struct {
			SourceID    string `json:"source_id"`
			Model       string `json:"upstream_model"`
			DisplayName string `json:"display_name"`
			ExtendedModelCapabilities
			WebSocketModelCapabilities
			Modalities     []string `json:"modalities"`
			Features       []string `json:"features"`
			MetadataReason string   `json:"metadata_reason"`
		}
		if !decode(w, r, &in) {
			return
		}
		src, err := a.Store.source(in.SourceID)
		if err != nil || src.Deleted {
			fail(w, 404, "来源不存在", "source_id")
			return
		}
		if strings.TrimSpace(in.Model) == "" || len(in.Model) > 200 {
			fail(w, 400, "填写有效模型 ID", "upstream_model")
			return
		}
		models, err := a.Store.models(src.ID)
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		for _, m := range models {
			if m.UpstreamModel == in.Model {
				fail(w, 409, "此模型已存在", "upstream_model")
				return
			}
		}
		src.Models = append(src.Models, in.Model)
		src.Version++
		if err = a.Store.saveSource(src); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		models, err = a.Store.models(src.ID)
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		for _, m := range models {
			if m.UpstreamModel == in.Model {
				m.ExtendedModelCapabilities = in.ExtendedModelCapabilities
				m.WebSocketModelCapabilities = in.WebSocketModelCapabilities
				m.Modalities = in.Modalities
				m.Features = in.Features
				m.MetadataReason = in.MetadataReason
				if len(in.Modalities) > 0 || len(in.Features) > 0 || in.DisplayName != "" || len(in.NativeServerTools) > 0 || in.OpaqueHistory || in.AdapterVersion != "" || in.WebSocketAdapter != "" {
					if in.DisplayName != "" {
						m.DisplayName = in.DisplayName
					}
					if _, err = a.Store.DB.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID); err != nil {
						fail(w, 503, storageError().Error(), "")
						return
					}
				}
				writeJSON(w, 201, m)
				return
			}
		}
	}
	if len(parts) != 3 {
		fail(w, 404, "模型接口不存在", "")
		return
	}
	m, err := a.Store.model(parts[2])
	if err == sql.ErrNoRows {
		fail(w, 404, "模型不存在", "")
		return
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, m)
		return
	}
	if r.Method != "PATCH" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Version           int             `json:"version"`
		Modalities        *[]string       `json:"modalities"`
		Features          *[]string       `json:"features"`
		MetadataReason    *string         `json:"metadata_reason"`
		Enabled           *bool           `json:"enabled"`
		DisplayName       *string         `json:"display_name"`
		ContextLimit      json.RawMessage `json:"context_limit"`
		MaxOutput         json.RawMessage `json:"max_output"`
		Price             *Price          `json:"price"`
		NativeServerTools *[]string       `json:"native_server_tools"`
		OpaqueHistory     *bool           `json:"opaque_history"`
		AdapterVersion    *string         `json:"adapter_version"`
		WebSocketAdapter  *string         `json:"websocket_adapter"`
		WebSocketWarmup   *bool           `json:"websocket_warmup"`
		WebSocketSteering *bool           `json:"websocket_steering"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version != m.Version {
		fail(w, 409, "模型已修改，请刷新", "version")
		return
	}
	if len(in.ContextLimit) > 0 && json.Unmarshal(in.ContextLimit, &m.ContextLimit) != nil || len(in.MaxOutput) > 0 && json.Unmarshal(in.MaxOutput, &m.MaxOutput) != nil {
		fail(w, 400, "模型上限必须为正整数或null", "")
		return
	}
	if m.ContextLimit != nil && *m.ContextLimit <= 0 || m.MaxOutput != nil && *m.MaxOutput <= 0 {
		fail(w, 400, "模型上限必须为正整数或null", "")
		return
	}
	if in.Modalities != nil || in.Features != nil {
		if in.MetadataReason == nil || strings.TrimSpace(*in.MetadataReason) == "" {
			fail(w, 400, "人工能力设置需要注明依据", "metadata_reason")
			return
		}
		if in.Modalities != nil {
			m.Modalities = *in.Modalities
		}
		if in.Features != nil {
			m.Features = *in.Features
		}
		m.MetadataReason = *in.MetadataReason
	}
	if in.MetadataReason != nil && strings.TrimSpace(*in.MetadataReason) != "" {
		m.MetadataReason = *in.MetadataReason
	}
	if in.Price != nil {
		fail(w, 400, "价格为不可变版本，请使用 prices 创建新版本", "price")
		return
	}
	if in.DisplayName != nil {
		m.DisplayName = *in.DisplayName
	}
	if in.NativeServerTools != nil {
		m.NativeServerTools = *in.NativeServerTools
	}
	if in.OpaqueHistory != nil {
		m.OpaqueHistory = *in.OpaqueHistory
	}
	if in.AdapterVersion != nil {
		m.AdapterVersion = *in.AdapterVersion
	}
	if in.WebSocketAdapter != nil {
		m.WebSocketAdapter = *in.WebSocketAdapter
	}
	if in.WebSocketWarmup != nil {
		m.WebSocketWarmup = *in.WebSocketWarmup
	}
	if in.WebSocketSteering != nil {
		m.WebSocketSteering = *in.WebSocketSteering
	}
	m.Version++
	src, err := a.Store.source(m.SourceID)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
		src.Models = slices.DeleteFunc(src.Models, func(v string) bool { return v == m.UpstreamModel })
		if m.Enabled {
			src.Models = append(src.Models, m.UpstreamModel)
		}
		src.Version++
	}
	tx, err := a.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE source_models SET data=? WHERE id=?", encode(m), m.ID); err == nil {
		_, err = tx.Exec("UPDATE sources SET data=? WHERE id=?", encode(src), src.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, m)
}

// Discovery performs only a provider catalog GET. It never generates model output.
func (a *App) discoverModels(w http.ResponseWriter, r *http.Request, sid string) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	src, err := a.Store.source(sid)
	if err != nil || src.Deleted {
		a.mu.Unlock()
		fail(w, 404, "来源不存在", "")
		return
	}
	if src.Kind == "codex_subscription" {
		a.codexModelDiscoveryAPI(w, r, src, &a.quotaObserver)
		a.mu.Unlock()
		return
	}
	if cloudProvider(src) {
		a.mu.Unlock()
		fail(w, 422, "此云适配卡未定义模型目录接口，请手工添加选定区域的模型 ID", "")
		return
	}
	secret := ""
	if src.Kind != "none" {
		secret, err = a.Secrets.Get(src.CredentialRef)
	}
	if err != nil {
		a.mu.Unlock()
		fail(w, 503, "请先配置来源凭据", "")
		return
	}
	op := Operation{ID: id("op"), Kind: "model_discovery", ObjectID: sid, State: "running", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err = a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES(?,?)", op.ID, encode(op)); err != nil {
		a.mu.Unlock()
		fail(w, 503, storageError().Error(), "")
		return
	}
	cfg := a.Config
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HeaderTimeout)*time.Second)
	release, gateErr := a.beginBackupOwnerLocked()
	if gateErr != nil {
		cancel()
		op.State, op.Error, op.UpdatedAt = "failed", gateErr.Error(), time.Now().UTC()
		_, saveErr := a.Store.DB.Exec("UPDATE operations SET data=? WHERE id=?", encode(op), op.ID)
		a.mu.Unlock()
		if saveErr != nil {
			a.markStorageFailure()
		}
		fail(w, 503, gateErr.Error(), "")
		return
	}
	a.operationCancels[op.ID] = cancel
	a.ownedTasks.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.ownedTasks.Done()
		defer release()
		defer cancel()
		items, e := a.discoverProviderModels(ctx, src, secret, cfg.MaxResponse)
		result := struct{ Data []discoveredProviderModel }{Data: items}
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.operationCancels, op.ID)
		current, ce := a.Store.source(sid)
		if ce != nil {
			e = ce
		} else if current.Generation != src.Generation || current.AccountGeneration != src.AccountGeneration || current.Deleted {
			e = errors.New("发现期间来源或账号发生变化，请重新获取")
		}
		if e == nil {
			tx, te := a.Store.DB.Begin()
			if te != nil {
				e = te
			} else {
				models, me := modelsInTx(tx, sid)
				if me != nil {
					e = me
				} else {
					seen := map[string]bool{}
					now := time.Now().UTC()
					for _, item := range result.Data {
						if strings.TrimSpace(item.ID) == "" || len(item.ID) > 200 || seen[item.ID] {
							e = errors.New("模型目录包含无效或重复 ID")
							break
						}
						seen[item.ID] = true
						m := models[item.ID]
						if m.ID == "" {
							m = SourceModel{ID: id("model"), SourceID: sid, UpstreamModel: item.ID, DisplayName: item.ID, Version: 1, Discovery: "discovered", Verification: "unverified", CreatedAt: now}
						}
						m.DiscoveredAt = &now
						if m.MetadataReason == "" || m.MetadataReason == "provider discovery" {
							if item.ContextLimit != nil && *item.ContextLimit > 0 {
								m.ContextLimit = item.ContextLimit
							}
							if item.MaxOutput != nil && *item.MaxOutput > 0 {
								m.MaxOutput = item.MaxOutput
							}
							if item.DisplayName != "" {
								m.DisplayName = item.DisplayName
							}
							m.MetadataReason = "provider discovery"
						}
						if m.Discovery != "manual" {
							m.Discovery = "discovered"
						}
						models[item.ID] = m
					}
					if e == nil {
						for name, m := range models {
							if m.Discovery == "discovered" && !seen[name] {
								m.Discovery = "stale"
							}
							if _, e = tx.Exec("INSERT INTO source_models(id,source_id,upstream_model,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", m.ID, sid, name, encode(m)); e != nil {
								break
							}
						}
					}
				}
				if e == nil {
					e = tx.Commit()
				}
				tx.Rollback()
			}
		}
		op.Version++
		op.UpdatedAt = time.Now().UTC()
		op.State = "succeeded"
		op.Result = map[string]any{"discovered_count": len(result.Data), "generation": src.Generation, "call_verified": false}
		if e != nil {
			op.State = "failed"
			op.Error = "获取模型失败，旧目录保留；请核对认证、端点和来源版本"
			if ctx.Err() != nil {
				op.State = "cancelled"
			}
		}
		if _, se := a.Store.DB.Exec("UPDATE operations SET data=? WHERE id=?", encode(op), op.ID); se != nil {
			a.markStorageFailure()
		}
	}()
	writeJSON(w, 202, op)
}
func modelsInTx(tx *sql.Tx, sid string) (map[string]SourceModel, error) {
	rows, err := tx.Query("SELECT data FROM source_models WHERE source_id=?", sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SourceModel{}
	for rows.Next() {
		var b string
		var m SourceModel
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &m); err != nil {
			return nil, err
		}
		out[m.UpstreamModel] = m
	}
	return out, rows.Err()
}
func (a *App) operationsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		fail(w, 404, "操作不存在", "")
		return
	}
	var b string
	if err := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", parts[2]).Scan(&b); err == sql.ErrNoRows {
		fail(w, 404, "操作不存在", "")
		return
	} else if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	var op Operation
	if json.Unmarshal([]byte(b), &op) != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if len(parts) == 3 && r.Method == "GET" {
		writeJSON(w, 200, op)
		return
	}
	if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
		var in struct {
			Version int `json:"version"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Version != op.Version {
			fail(w, 409, "操作版本已变化", "version")
			return
		}
		a.mu.Lock()
		cancel := a.operationCancels[op.ID]
		if cancel != nil {
			cancel()
		}
		a.mu.Unlock()
		writeJSON(w, 200, op)
		return
	}
	fail(w, 405, "方法不支持", "")
}
func (a *App) listAccountsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && r.Method == "GET" {
		rows, err := a.Store.DB.Query("SELECT data FROM accounts WHERE coalesce(json_extract(data,'$.deleted'),0)=0 ORDER BY id")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		items := []Account{}
		for rows.Next() {
			var b string
			var v Account
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
		rows.Close()
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		sources, err := a.Store.sources()
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		for i := range items {
			items[i].QuotaStatus = accountQuotaStatus(items[i], sources)
			items[i].SourceIDs = []string{}
			for _, s := range sources {
				if s.AccountID == items[i].ID {
					items[i].SourceIDs = append(items[i].SourceIDs, s.ID)
				}
			}
		}
		writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nil})
		return
	}
	fail(w, 404, "账号接口不存在", "")
}
