package app

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Verification struct {
	Status       string     `json:"status"`
	TestedAt     *time.Time `json:"tested_at"`
	Capabilities []string   `json:"capabilities"`
	Model        string     `json:"model,omitempty"`
	RequestID    string     `json:"request_id,omitempty"`
}
type Price struct {
	ID            string      `json:"id,omitempty"`
	Units         []PriceUnit `json:"units,omitempty"`
	Currency      string      `json:"currency"`
	Input         string      `json:"input_per_million"`
	Cached        string      `json:"cached_per_million"`
	CacheCreation string      `json:"cache_creation_per_million,omitempty"`
	Output        string      `json:"output_per_million"`
	AsOf          string      `json:"as_of"`
}
type Source struct {
	CloudProviderConfig
	VerifiedIdentity         map[string]any `json:"-"`
	ProxyURL                 *string        `json:"proxy_url"`
	ID                       string         `json:"id"`
	Name                     string         `json:"name"`
	Kind                     string         `json:"kind"`
	BaseURL                  string         `json:"base_url"`
	Enabled                  bool           `json:"enabled"`
	Version                  int            `json:"version"`
	Generation               int            `json:"binding_generation"`
	CredentialRef            string         `json:"-"`
	Configured               bool           `json:"credential_configured"`
	AuthStatus               string         `json:"auth_status"`
	Models                   []string       `json:"models"`
	Verification             Verification   `json:"verification"`
	Quota                    map[string]any `json:"quota"`
	Price                    *Price         `json:"price"`
	Continuation             bool           `json:"continuation_verified"`
	Deleted                  bool           `json:"deleted"`
	AccountID                string         `json:"account_id"`
	AccountGeneration        int            `json:"account_generation"`
	Provider                 string         `json:"provider"`
	NativeOperations         []string       `json:"native_operations"`
	RerankPath               string         `json:"rerank_path,omitempty"`
	NativeProtocol           string         `json:"native_protocol"`
	AllowParameterAdjustment bool           `json:"allow_parameter_adjustment"`
	MaxConcurrent            *int           `json:"max_concurrent"`
	CreatedAt                time.Time      `json:"created_at"`
}
type ClientKey struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Fingerprint        string     `json:"fingerprint"`
	SourceID           string     `json:"source_id"`
	Revoked            bool       `json:"revoked"`
	RevokedAt          *time.Time `json:"revoked_at"`
	LastSeen           *time.Time `json:"last_seen_at"`
	RouteID            string     `json:"route_id,omitempty"`
	Version            int        `json:"version"`
	Enabled            *bool      `json:"enabled"`
	CreatedAt          time.Time  `json:"created_at"`
	ExpiresAt          *time.Time `json:"expires_at"`
	RevokeAt           *time.Time `json:"revoke_at"`
	ProtocolAllowlist  []string   `json:"protocol_allowlist"`
	ModelAllowlist     []string   `json:"model_allowlist"`
	OperationAllowlist []string   `json:"operation_allowlist"`
	Limits             Limits     `json:"limits"`
	BudgetID           string     `json:"budget_id"`
	BudgetScopeID      string     `json:"budget_scope_id,omitempty"`
}

// Rotated secrets retain the original Key's budget scope and ledger.
func (k ClientKey) budgetScopeID() string {
	if k.BudgetScopeID != "" {
		return k.BudgetScopeID
	}
	return k.ID
}

type Usage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	Cached        *int64 `json:"cached_tokens"`
	Reasoning     *int64 `json:"reasoning_tokens"`
	CacheCreation *int64 `json:"cache_creation_tokens"`
}
type Record struct {
	RefreshTiming credentialRefreshTiming `json:"-"`
	ExtendedRecordFields
	UsageDimensions        map[string]string      `json:"usage_dimensions,omitempty"`
	TraceParent            string                 `json:"-"`
	UpstreamBytes          int64                  `json:"upstream_bytes"`
	ModelID                string                 `json:"model_id"`
	RoutingPolicy          *RoutingPolicySnapshot `json:"routing_policy,omitempty"`
	QueueMS                int64                  `json:"queue_ms"`
	FirstContentAt         *time.Time             `json:"first_content_at"`
	ConfigVersion          int                    `json:"config_version"`
	SourceVersion          int                    `json:"source_version"`
	KeyVersion             int                    `json:"key_version"`
	RouteVersion           int                    `json:"route_version"`
	Operation              string                 `json:"operation,omitempty"`
	RequestBytes           int64                  `json:"request_bytes"`
	ResponseBytes          int64                  `json:"response_bytes"`
	ResponseMIME           string                 `json:"response_mime,omitempty"`
	ID                     string                 `json:"id"`
	Protocol               string                 `json:"protocol"`
	Origin                 string                 `json:"origin"`
	KeyID                  string                 `json:"client_key_id"`
	ClientName             string                 `json:"client_name"`
	Fingerprint            string                 `json:"key_fingerprint"`
	SourceID               string                 `json:"source_id"`
	SourceName             string                 `json:"source_name"`
	Generation             int                    `json:"binding_generation"`
	Model                  string                 `json:"requested_model"`
	SentModel              string                 `json:"sent_model"`
	ReportedModel          string                 `json:"reported_model"`
	ResponseID             string                 `json:"upstream_response_id"`
	Status                 string                 `json:"status"`
	UpstreamStatus         string                 `json:"upstream_status"`
	DeliveryStatus         string                 `json:"delivery_status"`
	ObservationStatus      string                 `json:"observation_status"`
	Started                time.Time              `json:"started_at"`
	Ended                  *time.Time             `json:"ended_at"`
	FirstEvent             *time.Time             `json:"first_event_at"`
	DurationMS             int64                  `json:"duration_ms"`
	HTTPStatus             int                    `json:"http_status"`
	UpstreamRequestID      string                 `json:"upstream_request_id"`
	ErrorStage             string                 `json:"error_stage"`
	ErrorSummary           string                 `json:"error_summary"`
	Usage                  Usage                  `json:"usage"`
	Completeness           string                 `json:"usage_completeness"`
	Cost                   *string                `json:"estimated_cost"`
	PartialCost            *string                `json:"partial_estimated_cost"`
	Price                  *Price                 `json:"price_snapshot"`
	RouteID                string                 `json:"route_id,omitempty"`
	AccountID              string                 `json:"account_id"`
	AccountGeneration      int                    `json:"account_generation"`
	AttemptID              string                 `json:"attempt_id"`
	Sequence               int                    `json:"sequence"`
	Adjustments            []string               `json:"adjustments"`
	SelectionReasons       []CandidateReason      `json:"selection_reasons,omitempty"`
	WireUsageSource        string                 `json:"wire_usage_source,omitempty"`
	Version                int                    `json:"version"`
	Submission             string                 `json:"submission_evidence"`
	AttemptStarted         time.Time              `json:"attempt_started_at"`
	ReservedTokens         int64                  `json:"reserved_tokens_estimate"`
	Accounting             *AccountingPlan        `json:"-"`
	TokenReservationSource string                 `json:"token_reservation_source,omitempty"`
}
type Store struct{ DB *sql.DB }

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "gatt.db")
	// Inspect an existing file read-only before opening a WAL writer.
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		probe, openErr := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro")
		if openErr != nil {
			return nil, openErr
		}
		var version int
		openErr = probe.QueryRow("PRAGMA user_version").Scan(&version)
		probe.Close()
		if openErr != nil {
			return nil, openErr
		}
		if version != 2 {
			return nil, fmt.Errorf("数据 schema v%d 不受支持（需要 v2）；请保留原目录，使用独立 data_dir；不会自动迁移或清空", version)
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=FULL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var schemaVersion int
	if err = db.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil {
		db.Close()
		return nil, err
	}
	if schemaVersion != 0 && schemaVersion != 2 {
		db.Close()
		return nil, fmt.Errorf("数据 schema v%d 不受支持（需要 v2）；请保留并备份原目录，使用独立 data_dir 启动新版；不会自动迁移或清空", schemaVersion)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY, credential_ref TEXT NOT NULL, generation INTEGER NOT NULL, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS sources(id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES accounts(id), data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS source_models(id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES sources(id), upstream_model TEXT NOT NULL, data TEXT NOT NULL, UNIQUE(source_id,upstream_model));
 CREATE TABLE IF NOT EXISTS routes(id TEXT PRIMARY KEY, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS model_aliases(public_model TEXT PRIMARY KEY, route_id TEXT NOT NULL REFERENCES routes(id), data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS client_keys(id TEXT PRIMARY KEY, digest TEXT UNIQUE NOT NULL, source_id TEXT REFERENCES sources(id), route_id TEXT REFERENCES routes(id), data TEXT NOT NULL, CHECK((source_id IS NULL)!=(route_id IS NULL)));
 CREATE TABLE IF NOT EXISTS requests(id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES sources(id), started TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS requests_started ON requests(started DESC,id DESC);
 CREATE INDEX IF NOT EXISTS requests_key_started ON requests(json_extract(data,'$.client_key_id'),started);
 CREATE INDEX IF NOT EXISTS requests_state_started ON requests(status,started);
 CREATE INDEX IF NOT EXISTS requests_model_started ON requests(json_extract(data,'$.requested_model'),started DESC,id DESC);
 CREATE INDEX IF NOT EXISTS requests_usage ON requests(status,started,json_extract(data,'$.usage.input_tokens'),json_extract(data,'$.usage.output_tokens'),json_extract(data,'$.usage_completeness'),json_extract(data,'$.estimated_cost'),json_extract(data,'$.price_snapshot.currency'),json_extract(data,'$.origin'),json_extract(data,'$.duration_ms'),json_extract(data,'$.first_content_at'),json_extract(data,'$.partial_estimated_cost'));
 CREATE INDEX IF NOT EXISTS requests_costs ON requests(json_extract(data,'$.price_snapshot.currency'),json_extract(data,'$.estimated_cost'),json_extract(data,'$.partial_estimated_cost'));
 CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE, sequence INTEGER NOT NULL CHECK(sequence>0), source_id TEXT NOT NULL REFERENCES sources(id), account_id TEXT NOT NULL REFERENCES accounts(id), data TEXT NOT NULL, UNIQUE(request_id,sequence));
 CREATE TABLE IF NOT EXISTS bindings(response_id TEXT NOT NULL, key_id TEXT NOT NULL, source_id TEXT NOT NULL, generation INTEGER NOT NULL, account_generation INTEGER NOT NULL, model TEXT NOT NULL, request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE, PRIMARY KEY(response_id,key_id,source_id,generation,account_generation,model));
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,data TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS operations_audit_created ON operations(json_extract(data,'$.kind'),julianday(json_extract(data,'$.created_at')) DESC,id DESC);
 PRAGMA user_version=2;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db}
	_, err = db.Exec(`UPDATE requests SET status='interrupted', data=json_set(data,'$.status','interrupted','$.error_summary','进程退出，上游执行和费用可能未知','$.error_stage','restart') WHERE status IN ('queued','admitted','dispatching','streaming')`)
	if err == nil {
		_, err = db.Exec(`UPDATE attempts SET data=json_set(data,'$.status','interrupted','$.error_summary','进程退出，上游执行和费用可能未知','$.error_stage','restart') WHERE json_extract(data,'$.status') IN ('admitted','dispatching','streaming')`)
	}
	if err == nil {
		_, err = db.Exec(`UPDATE operations SET data=json_set(data,'$.state','interrupted','$.error','服务重启，请重新发起操作') WHERE json_extract(data,'$.state')='running'`)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(PriceSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(ClientConfigSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(NotificationSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(ResourceOperationsSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(QuotaObservationSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err = initializeExtendedProtocols(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = initializeGeminiConversions(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = initializeAccounting(db); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func id(prefix string) string { return prefix + "_" + token()[:24] }
func digest(v string) string  { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func (s *Store) source(id string) (Source, error) {
	var v Source
	var data string
	var account string
	var accountData string
	err := s.DB.QueryRow("SELECT a.credential_ref,s.data,a.id,a.generation,a.data FROM sources s JOIN accounts a ON a.id=s.account_id WHERE s.id=?", id).Scan(&v.CredentialRef, &data, &account, &v.AccountGeneration, &accountData)
	if err == nil {
		err = json.Unmarshal([]byte(data), &v)
		var a Account
		if err == nil {
			err = json.Unmarshal([]byte(accountData), &a)
		}
		if v.AccountGeneration != 0 && v.AccountGeneration != a.Generation {
			v.Verification = Verification{Status: "stale", Capabilities: []string{}}
			v.Continuation = false
			if v.Quota == nil {
				v.Quota = map[string]any{}
			}
			v.Quota["status"] = "stale"
		}
		v.AccountID, v.AccountGeneration, v.AuthStatus, v.Configured = account, a.Generation, a.AuthState, v.CredentialRef != "" || a.AuthType == "none"
		if cloudProvider(v) {
			v.Configured = cloudConfigured(v)
		}
		expireQuotaObservation(&v, time.Now())
	}
	return v, err
}
func (s *Store) saveSource(v Source) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if v.AccountID == "" {
		err = tx.QueryRow("SELECT account_id FROM sources WHERE id=?", v.ID).Scan(&v.AccountID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if v.AccountID == "" {
			v.AccountID = id("acct")
		}
	}
	if v.NativeProtocol == "" {
		v.NativeProtocol = "responses"
	}
	if v.Provider == "" {
		if v.Kind == "codex_subscription" {
			v.Provider = "codex"
		} else {
			v.Provider = "openai_compatible"
		}
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	var a Account
	var raw, ref string
	err = tx.QueryRow("SELECT credential_ref,data FROM accounts WHERE id=?", v.AccountID).Scan(&ref, &raw)
	if err == sql.ErrNoRows {
		a = Account{ID: v.AccountID, Name: v.Name, Provider: v.Provider, AuthType: v.Kind, Generation: 1, Version: 1, CreatedAt: v.CreatedAt, QuotaStatus: "unknown"}
	} else if err != nil {
		return err
	} else if err = json.Unmarshal([]byte(raw), &a); err != nil {
		return err
	}
	if ref != v.CredentialRef {
		a.Generation++
		a.Version++
	}
	if cloudProvider(v) {
		var previous string
		if e := tx.QueryRow("SELECT data FROM sources WHERE id=?", v.ID).Scan(&previous); e == nil {
			var prior Source
			if e = json.Unmarshal([]byte(previous), &prior); e != nil {
				return e
			}
			if encode(prior.CloudConfig) != encode(v.CloudConfig) {
				a.Generation++
				a.Version++
			}
		} else if e != sql.ErrNoRows {
			return e
		}
	}
	if v.VerifiedIdentity != nil {
		a.Identity = v.VerifiedIdentity
	}
	a.AuthState, a.CredentialPresent = v.AuthStatus, v.CredentialRef != ""
	v.AccountGeneration = a.Generation
	if _, err = tx.Exec("INSERT INTO accounts(id,credential_ref,generation,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET credential_ref=excluded.credential_ref,generation=excluded.generation,data=excluded.data", a.ID, v.CredentialRef, a.Generation, encode(a)); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO sources(id,account_id,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET account_id=excluded.account_id,data=excluded.data", v.ID, v.AccountID, encode(v)); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT id,data FROM source_models WHERE source_id=?", v.ID)
	if err != nil {
		return err
	}
	known := map[string]SourceModel{}
	for rows.Next() {
		var mid, b string
		if err = rows.Scan(&mid, &b); err != nil {
			rows.Close()
			return err
		}
		var m SourceModel
		if err = json.Unmarshal([]byte(b), &m); err != nil {
			rows.Close()
			return err
		}
		known[m.UpstreamModel] = m
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range v.Models {
		if _, ok := known[name]; !ok {
			known[name] = SourceModel{ID: id("model"), SourceID: v.ID, UpstreamModel: name, DisplayName: name, Enabled: true, Version: 1, Discovery: "manual", Verification: "unverified", CreatedAt: v.CreatedAt}
		}
	}
	for name, m := range known {
		m.Enabled = false
		for _, enabled := range v.Models {
			if name == enabled {
				m.Enabled = true
			}
		}
		if _, err = tx.Exec("INSERT INTO source_models(id,source_id,upstream_model,data) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", m.ID, v.ID, name, encode(m)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) sources() ([]Source, error) {
	rows, err := s.DB.Query("SELECT s.data,a.data,a.credential_ref FROM sources s JOIN accounts a ON a.id=s.account_id WHERE json_extract(s.data,'$.deleted')=0 ORDER BY s.rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		var d, ad, ref string
		var v Source
		if err = rows.Scan(&d, &ad, &ref); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(d), &v); err != nil {
			return nil, err
		}
		var account Account
		if err = json.Unmarshal([]byte(ad), &account); err != nil {
			return nil, err
		}
		v.AuthStatus = account.AuthState
		v.Configured = ref != "" || account.AuthType == "none"
		v.CredentialRef = ref
		if cloudProvider(v) {
			v.Configured = cloudConfigured(v)
		}
		if v.AccountGeneration != 0 && v.AccountGeneration != account.Generation {
			v.Verification = Verification{Status: "stale", Capabilities: []string{}}
			v.Continuation = false
			if v.Quota == nil {
				v.Quota = map[string]any{}
			}
			v.Quota["status"] = "stale"
		}
		v.CredentialRef = ref
		v.AccountGeneration = account.Generation
		expireQuotaObservation(&v, time.Now())
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) keyByDigest(d string) (ClientKey, error) {
	var v ClientKey
	var b string
	err := s.DB.QueryRow("SELECT data FROM client_keys WHERE digest=?", d).Scan(&b)
	if err == nil {
		err = json.Unmarshal([]byte(b), &v)
	}
	return v, err
}
func (s *Store) keys() ([]ClientKey, error) {
	rows, err := s.DB.Query("SELECT data FROM client_keys ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientKey{}
	for rows.Next() {
		var b string
		var v ClientKey
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
func (s *Store) record(v Record) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data := encode(v)
	_, err = tx.Exec("INSERT INTO requests(id,source_id,started,status,data) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,data=excluded.data", v.ID, v.SourceID, v.Started.Format(time.RFC3339Nano), v.Status, data)
	if err != nil {
		return err
	}
	if v.Sequence == 0 {
		v.Sequence = 1
	}
	if v.AttemptID == "" {
		v.AttemptID = v.ID + "_attempt_1"
	}
	if v.AccountID == "" {
		if err = tx.QueryRow("SELECT account_id FROM sources WHERE id=?", v.SourceID).Scan(&v.AccountID); err != nil {
			return err
		}
	}
	_, err = tx.Exec("INSERT INTO attempts(id,request_id,sequence,source_id,account_id,data) VALUES(?,?,?,?,?,?) ON CONFLICT(request_id,sequence) DO UPDATE SET data=excluded.data", v.AttemptID, v.ID, v.Sequence, v.SourceID, v.AccountID, encode(v))
	if err != nil {
		return err
	}
	if err = reserveAccounting(tx, v); err != nil {
		return err
	}
	if err = settleAccounting(tx, v); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) bind(v Record, responseID string) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO bindings(response_id,key_id,source_id,generation,account_generation,model,request_id) VALUES(?,?,?,?,?,?,?)", responseID, v.KeyID, v.SourceID, v.Generation, v.AccountGeneration, v.SentModel, v.ID)
	return err
}
func (s *Store) continuation(responseID string, key ClientKey, src Source, model string) bool {
	var n int
	err := s.DB.QueryRow("SELECT count(*) FROM bindings WHERE response_id=? AND key_id=? AND source_id=? AND generation=? AND account_generation=? AND model=?", responseID, key.ID, src.ID, src.Generation, src.AccountGeneration, model).Scan(&n)
	return err == nil && n == 1
}
func (s *Store) cleanup(days int) error {
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	auditCutoff := now.Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err = tx.Exec("DELETE FROM accounting_audit WHERE created_at < ?", auditCutoff); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM reservations WHERE period_end <= ? AND status='settled' AND request_id IN (SELECT id FROM requests WHERE started < ?)", now.Format(time.RFC3339Nano), cutoff); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM requests WHERE started < ? AND status NOT IN ('queued','admitted','dispatching','streaming') AND NOT EXISTS(SELECT 1 FROM reservations r WHERE r.request_id=requests.id) AND NOT EXISTS(SELECT 1 FROM accounting_audit a WHERE a.request_id=requests.id) AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.request_id=requests.id) AND NOT EXISTS(SELECT 1 FROM job_items ji WHERE ji.request_id=requests.id)", cutoff); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM operations WHERE id IN (SELECT id FROM operations WHERE json_extract(data,'$.state') NOT IN ('running','pending','uncertain') AND json_extract(data,'$.updated_at') < CASE WHEN json_extract(data,'$.kind')='admin_action' THEN ? ELSE ? END LIMIT 500)", auditCutoff, now.Add(-24*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("来源 URL 无效，不允许用户名、密码、查询串或片段")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("来源必须使用 HTTPS；仅 loopback 可使用 HTTP")
}
func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func safeEndpoint(base, path string) string { return strings.TrimRight(base, "/") + path }
func storageError() error                   { return fmt.Errorf("本地存储不可用；检查磁盘后重启服务") }
