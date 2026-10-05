package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Enterprise is an opt-in, separately initialized directory. Tenant data is
// physically separate; existing gateway handlers never query a shared tenant table.
type EnterpriseConfig struct {
	PublicOrigin string `json:"public_origin"`
}

func (c EnterpriseConfig) validate() error {
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("enterprise.public_origin 必须为无路径的 HTTPS origin")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return errors.New("企业公开地址必须为 HTTPS；仅 loopback 开发验收允许 HTTP")
	}
	return nil
}

const enterpriseSchema = `CREATE TABLE IF NOT EXISTS enterprise_users(id TEXT PRIMARY KEY,username TEXT UNIQUE NOT NULL,password TEXT NOT NULL,platform_admin INTEGER NOT NULL DEFAULT 0,enabled INTEGER NOT NULL DEFAULT 1,version INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS enterprise_tenants(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,version INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS enterprise_members(user_id TEXT NOT NULL REFERENCES enterprise_users(id),tenant_id TEXT NOT NULL REFERENCES enterprise_tenants(id),role TEXT NOT NULL CHECK(role IN ('tenant_admin','viewer')),enabled INTEGER NOT NULL DEFAULT 1,version INTEGER NOT NULL DEFAULT 1,PRIMARY KEY(user_id,tenant_id));
CREATE TABLE IF NOT EXISTS enterprise_audit(id TEXT PRIMARY KEY,created_at TEXT NOT NULL,actor_id TEXT NOT NULL,action TEXT NOT NULL,entity_id TEXT NOT NULL);`

type enterpriseUser struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	PlatformAdmin bool   `json:"platform_admin"`
	Enabled       bool   `json:"enabled"`
	Version       int    `json:"version"`
}
type enterpriseTenant struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Version int    `json:"version"`
	Role    string `json:"role,omitempty"`
}
type enterpriseSession struct {
	UserID       string
	Expires      time.Time
	TenantTokens map[string]string
}
type Enterprise struct {
	root          *App
	assets        fs.FS
	mu            sync.Mutex
	tenants       map[string]*App
	sessions      map[string]*enterpriseSession
	failures      map[string][]time.Time
	passwordSlots chan struct{}
	ctx           context.Context
	stopped       bool
	closed        bool
}
type enterpriseActorKey struct{}

func enterprisePassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("密码须为 12 到 1024 字节")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return base64.RawStdEncoding.EncodeToString(salt) + ":" + base64.RawStdEncoding.EncodeToString(key), nil
}
func enterprisePasswordMatches(stored, password string) bool {
	saltText, keyText, ok := strings.Cut(stored, ":")
	if !ok || len(password) > 1024 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(saltText)
	key, e2 := base64.RawStdEncoding.DecodeString(keyText)
	if e1 != nil || e2 != nil || len(salt) != 16 || len(key) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(actual, key) == 1
}
func InitializeEnterprise(s *Store, username, password string) error {
	if strings.TrimSpace(username) != username || username == "" || len(username) > 200 {
		return errors.New("用户名须为 1 到 200 字节且无首尾空白")
	}
	var count int
	if err := s.DB.QueryRow("SELECT (SELECT count(*) FROM accounts)+(SELECT count(*) FROM sources)+(SELECT count(*) FROM client_keys)").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("企业模式必须使用独立 data_dir，不能直接接管已有个人账号或 Key")
	}
	if _, err := s.DB.Exec(enterpriseSchema); err != nil {
		return err
	}
	hash, err := enterprisePassword(password)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("SELECT count(*) FROM enterprise_users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("企业目录已初始化，不会覆盖已有用户")
	}
	if _, err = tx.Exec("INSERT INTO enterprise_users(id,username,password,platform_admin) VALUES(?,?,?,1)", id("user"), username, hash); err != nil {
		return err
	}
	return tx.Commit()
}
func NewEnterprise(root *App, assets fs.FS, ctx context.Context) (*Enterprise, error) {
	if root.Config.Enterprise == nil {
		return nil, errors.New("缺少企业配置")
	}
	if err := root.Config.Enterprise.validate(); err != nil {
		return nil, err
	}
	var count int
	if err := root.Store.DB.QueryRow("SELECT count(*) FROM enterprise_users WHERE platform_admin=1 AND enabled=1").Scan(&count); err != nil || count == 0 {
		return nil, errors.New("先在独立企业目录运行 enterprise-init 初始化管理员")
	}
	e := &Enterprise{root: root, assets: assets, tenants: map[string]*App{}, sessions: map[string]*enterpriseSession{}, failures: map[string][]time.Time{}, passwordSlots: make(chan struct{}, 2), ctx: ctx}
	rows, err := root.Store.DB.Query("SELECT id FROM enterprise_tenants")
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			break
		}
		ids = append(ids, value)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, tenantID := range ids {
		a, err := e.openTenant(tenantID)
		if err != nil {
			_ = e.Close(context.Background())
			return nil, err
		}
		e.tenants[tenantID] = a
	}
	return e, nil
}
func (e *Enterprise) openTenant(tenantID string) (*App, error) {
	if !strings.HasPrefix(tenantID, "tenant_") || filepath.Base(tenantID) != tenantID || strings.ContainsAny(tenantID, "/\\") {
		return nil, errors.New("租户目录标识无效")
	}
	parent := filepath.Join(e.root.Config.DataDir, "tenants")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() {
		return nil, errors.New("租户根目录必须为普通目录")
	}
	dir := filepath.Join(parent, tenantID)
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return nil, errors.New("租户数据目录必须为普通目录")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	c := e.root.Config
	c.DataDir = dir
	c.Enterprise = nil
	c.PublicAPIBase = e.root.Config.Enterprise.PublicOrigin + "/t/" + tenantID
	s, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	vault, err := NewFileSecrets(dir)
	if err != nil {
		s.DB.Close()
		return nil, err
	}
	a, err := New(c, s, vault, e.assets)
	if err != nil {
		s.DB.Close()
		return nil, err
	}
	a.StartMaintenance(e.ctx)
	return a, nil
}
func (e *Enterprise) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.stopped = true
	apps := make([]*App, 0, len(e.tenants))
	for _, a := range e.tenants {
		apps = append(apps, a)
		a.CloseAdmission()
		a.CancelAll()
	}
	e.mu.Unlock()
	var result error
	for _, a := range apps {
		if err := a.WaitOwnedTasks(ctx); err != nil {
			result = errors.Join(result, err)
			continue
		}
		a.CloseNetwork()
		result = errors.Join(result, a.Store.DB.Close())
	}
	return result
}
func (e *Enterprise) user(userID string) (enterpriseUser, error) {
	var u enterpriseUser
	err := e.root.Store.DB.QueryRow("SELECT id,username,platform_admin,enabled,version FROM enterprise_users WHERE id=?", userID).Scan(&u.ID, &u.Username, &u.PlatformAdmin, &u.Enabled, &u.Version)
	return u, err
}
func (e *Enterprise) current(r *http.Request) (enterpriseUser, *enterpriseSession, error) {
	cookie, err := r.Cookie("cove_enterprise")
	if err != nil {
		return enterpriseUser{}, nil, sql.ErrNoRows
	}
	e.mu.Lock()
	session := e.sessions[digest(cookie.Value)]
	e.mu.Unlock()
	if session == nil || !session.Expires.After(time.Now()) {
		return enterpriseUser{}, nil, sql.ErrNoRows
	}
	u, err := e.user(session.UserID)
	if err != nil {
		return enterpriseUser{}, nil, err
	}
	if !u.Enabled {
		return enterpriseUser{}, nil, sql.ErrNoRows
	}
	return u, session, nil
}
func (e *Enterprise) audit(tx *sql.Tx, actor, action, entity string) error {
	_, err := tx.Exec("INSERT INTO enterprise_audit(id,created_at,actor_id,action,entity_id) VALUES(?,?,?,?,?)", id("audit"), time.Now().UTC().Format(time.RFC3339Nano), actor, action, entity)
	return err
}
func (e *Enterprise) cookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: "cove_enterprise", Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(e.root.Config.Enterprise.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: func() int {
		if value == "" {
			return -1
		}
		return int(time.Until(expires).Seconds())
	}()})
}
func (e *Enterprise) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	origin := e.root.Config.Enterprise.PublicOrigin
	parsed, _ := url.Parse(origin)
	probe := (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") && r.Method == "GET"
	if r.Host != parsed.Host && !(probe && r.Host == e.root.Config.Listen) {
		fail(w, 403, "Host 与企业公开地址不符", "")
		return
	}
	if supplied := r.Header.Get("Origin"); supplied != "" && supplied != origin {
		fail(w, 403, "跨来源请求被拒绝", "")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/enterprise/") && r.Method != "GET" && r.Header.Get("Origin") != origin {
		fail(w, 403, "管理写请求需要精确 Origin", "")
		return
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		q := r.Clone(r.Context())
		q.Host = e.root.Config.Listen
		e.root.ServeHTTP(w, q)
		return
	}
	if r.URL.Path == "/enterprise/session" {
		e.sessionAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/enterprise/") {
		u, _, err := e.current(r)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(w, 401, "请登录企业账户", "")
			} else {
				fail(w, 503, storageError().Error(), "")
			}
			return
		}
		e.management(w, r, u)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/t/") {
		e.tenantHTTP(w, r)
		return
	}
	if r.URL.Path == "/" {
		if e.assets == nil {
			fail(w, 404, "页面尚未构建", "")
			return
		}
		b, err := fs.ReadFile(e.assets, "index.html")
		if err != nil {
			fail(w, 503, "页面不可用", "")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.Replace(string(b), "<body>", "<body><div id=\"cove-enterprise\" hidden></div>", 1)))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/favicon.ico" {
		q := r.Clone(r.Context())
		q.Host = e.root.Config.Listen
		e.root.ServeHTTP(w, q)
		return
	}
	fail(w, 404, "接口不存在", "")
}
func (e *Enterprise) sessionAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		now := time.Now()
		e.mu.Lock()
		for address, failures := range e.failures {
			keep := failures[:0]
			for _, at := range failures {
				if now.Sub(at) < time.Minute {
					keep = append(keep, at)
				}
			}
			if len(keep) == 0 {
				delete(e.failures, address)
			} else {
				e.failures[address] = keep
			}
		}
		loginBucket := host + ":" + in.Username
		blocked := len(e.failures[loginBucket]) >= 5
		e.mu.Unlock()
		if blocked {
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "登录失败过多，请稍后重试", "")
			return
		}
		select {
		case e.passwordSlots <- struct{}{}:
			defer func() { <-e.passwordSlots }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, 429, "登录繁忙，请稍后重试", "")
			return
		}
		var userID, hash string
		var version int
		var enabled bool
		err := e.root.Store.DB.QueryRow("SELECT id,password,enabled,version FROM enterprise_users WHERE username=?", in.Username).Scan(&userID, &hash, &enabled, &version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			hash = base64.RawStdEncoding.EncodeToString(make([]byte, 16)) + ":" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
		}
		valid := enterprisePasswordMatches(hash, in.Password)
		if !valid || !enabled {
			e.mu.Lock()
			if len(e.failures) < 1024 || e.failures[loginBucket] != nil {
				e.failures[loginBucket] = append(e.failures[loginBucket], now)
			}
			e.mu.Unlock()
			fail(w, 401, "用户名或密码不正确", "")
			return
		}
		secret := token()
		session := &enterpriseSession{UserID: userID, Expires: now.Add(12 * time.Hour), TenantTokens: map[string]string{}}
		e.mu.Lock()
		// Password verification is slow. Recheck under the same lock used for
		// revocation so a changed password or disable/re-enable cannot publish
		// a session from a stale verification result.
		u, err := e.user(userID)
		if err != nil {
			e.mu.Unlock()
			fail(w, 503, storageError().Error(), "")
			return
		}
		if !u.Enabled || u.Version != version {
			e.mu.Unlock()
			fail(w, 401, "账户已变化，请重新登录", "")
			return
		}
		for k, s := range e.sessions {
			if !s.Expires.After(now) {
				delete(e.sessions, k)
			}
		}
		if len(e.sessions) >= 1024 {
			e.mu.Unlock()
			fail(w, 429, "会话数量已达上限，请稍后重试", "")
			return
		}
		e.sessions[digest(secret)] = session
		delete(e.failures, loginBucket)
		e.mu.Unlock()
		e.cookie(w, secret, session.Expires)
		writeJSON(w, 200, u)
	case "GET":
		u, _, err := e.current(r)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(w, 401, "请登录企业账户", "")
			} else {
				fail(w, 503, storageError().Error(), "")
			}
			return
		}
		writeJSON(w, 200, u)
	case "DELETE":
		if cookie, err := r.Cookie("cove_enterprise"); err == nil {
			e.mu.Lock()
			delete(e.sessions, digest(cookie.Value))
			e.mu.Unlock()
		}
		e.cookie(w, "", time.Unix(0, 0))
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 405, "方法不支持", "")
	}
}

func (e *Enterprise) tenantHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/t/"), "/", 2)
	if len(parts) != 2 {
		fail(w, 404, "租户接口不存在", "")
		return
	}
	tenantID := parts[0]
	path := "/" + parts[1]
	if path == "/v1/messages" {
		w = &messagesErrors{ResponseWriter: w}
	}
	var enabled bool
	if err := e.root.Store.DB.QueryRow("SELECT enabled FROM enterprise_tenants WHERE id=?", tenantID).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "租户不可用", "")
		} else {
			fail(w, 503, storageError().Error(), "")
		}
		return
	}
	if !enabled {
		fail(w, 403, "租户已停用", "")
		return
	}
	e.mu.Lock()
	a := e.tenants[tenantID]
	e.mu.Unlock()
	if a == nil {
		fail(w, 503, "租户尚未就绪", "")
		return
	}
	q := r.Clone(r.Context())
	q.Host = a.Config.Listen
	q.URL.Path = path
	q.URL.RawPath = ""
	if path == "/" {
		a.ServeHTTP(w, q)
		return
	}
	if strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") {
		if r.Header.Get("Origin") != "" {
			q.Header.Set("Origin", "http://"+a.Config.Listen)
		}
		a.ServeHTTP(w, q)
		return
	}
	u, session, err := e.current(r)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 401, "请登录企业账户", "")
		} else {
			fail(w, 503, storageError().Error(), "")
		}
		return
	}
	role := "tenant_admin"
	if !u.PlatformAdmin {
		err = e.root.Store.DB.QueryRow("SELECT role FROM enterprise_members WHERE user_id=? AND tenant_id=? AND enabled=1", u.ID, tenantID).Scan(&role)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				fail(w, 403, "没有此租户的访问权限", "")
			} else {
				fail(w, 503, storageError().Error(), "")
			}
			return
		}
	}
	if !strings.HasPrefix(path, "/admin/") {
		fail(w, 404, "租户接口不存在", "")
		return
	}
	if r.Method != "GET" && r.Header.Get("Origin") != e.root.Config.Enterprise.PublicOrigin {
		fail(w, 403, "管理写请求需要精确 Origin", "")
		return
	}
	if !enterpriseTenantAllowed(path, r.Method, role) {
		fail(w, 403, "此角色或企业模式不允许此操作", "")
		return
	}
	if path == "/admin/session" && r.Method == "DELETE" {
		e.sessionAPI(w, r)
		return
	}
	e.mu.Lock()
	inner := session.TenantTokens[tenantID]
	if inner == "" {
		inner = token()
		session.TenantTokens[tenantID] = inner
	}
	e.mu.Unlock()
	a.mu.Lock()
	a.sessions[digest(inner)] = session.Expires
	a.mu.Unlock()
	q = q.WithContext(context.WithValue(q.Context(), enterpriseActorKey{}, u.ID))
	q.Header.Set("Authorization", "Bearer "+inner)
	q.Header.Set("Origin", "http://"+a.Config.Listen)
	a.ServeHTTP(w, q)
}
func enterpriseTenantAllowed(path, method, role string) bool {
	if path == "/admin/session" {
		return method == "POST" || method == "DELETE"
	}
	for _, prefix := range []string{"/admin/browser-tickets", "/admin/clients", "/admin/client-changes", "/admin/config-extensions", "/admin/config-transfer", "/admin/diagnostics", "/admin/backup", "/admin/restore", "/admin/platform", "/admin/telemetry"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return false
		}
	}
	if strings.Contains(path, "/login") || strings.Contains(path, "/logout") || strings.Contains(path, "/refresh") || (strings.HasPrefix(path, "/admin/settings") && method != "GET") {
		return false
	}
	if path == "/admin/settings" && method == "GET" && role == "tenant_admin" {
		return true
	}
	if role == "tenant_admin" {
		for _, prefix := range []string{"/admin/status", "/admin/accounts", "/admin/sources", "/admin/routes", "/admin/model-aliases", "/admin/client-keys", "/admin/models", "/admin/prices", "/admin/budgets", "/admin/requests", "/admin/usage", "/admin/audit", "/admin/metrics", "/admin/notifications", "/admin/alerts", "/admin/operations"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		return false
	}
	if method != "GET" {
		return false
	}
	for _, prefix := range []string{"/admin/status", "/admin/requests", "/admin/usage", "/admin/budgets", "/admin/audit", "/admin/metrics"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (e *Enterprise) management(w http.ResponseWriter, r *http.Request, u enterpriseUser) {
	path := r.URL.Path
	if path == "/enterprise/tenants" && r.Method == "GET" {
		query := "SELECT t.id,t.name,t.enabled,t.version,coalesce(m.role,'tenant_admin') FROM enterprise_tenants t LEFT JOIN enterprise_members m ON m.tenant_id=t.id AND m.user_id=? AND m.enabled=1"
		if !u.PlatformAdmin {
			query += " WHERE m.user_id IS NOT NULL"
		}
		query += " ORDER BY t.name,t.id"
		rows, err := e.root.Store.DB.Query(query, u.ID)
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		items := []enterpriseTenant{}
		for rows.Next() {
			var t enterpriseTenant
			if err = rows.Scan(&t.ID, &t.Name, &t.Enabled, &t.Version, &t.Role); err != nil {
				break
			}
			items = append(items, t)
		}
		if err != nil || rows.Err() != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	if path == "/enterprise/password" && r.Method == "POST" {
		e.changePassword(w, r, u)
		return
	}
	if !u.PlatformAdmin {
		fail(w, 403, "仅平台管理员可管理企业目录", "")
		return
	}
	if path == "/enterprise/tenants" && r.Method == "POST" {
		e.createTenant(w, r, u)
		return
	}
	if path == "/enterprise/users" {
		e.usersAPI(w, r, u)
		return
	}
	if path == "/enterprise/audit" && r.Method == "GET" {
		rows, err := e.root.Store.DB.Query("SELECT id,created_at,actor_id,action,entity_id FROM enterprise_audit ORDER BY created_at DESC,id DESC LIMIT 100")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		items := []map[string]string{}
		for rows.Next() {
			var id, at, actor, action, entity string
			if err = rows.Scan(&id, &at, &actor, &action, &entity); err != nil {
				break
			}
			items = append(items, map[string]string{"id": id, "created_at": at, "actor_id": actor, "action": action, "entity_id": entity})
		}
		if err != nil || rows.Err() != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/enterprise/"), "/")
	if len(parts) == 2 && parts[0] == "tenants" && r.Method == "PATCH" {
		e.updateTenant(w, r, u, parts[1])
		return
	}
	if len(parts) == 2 && parts[0] == "users" && r.Method == "PATCH" {
		e.updateUser(w, r, u, parts[1])
		return
	}
	if len(parts) == 4 && parts[0] == "tenants" && parts[2] == "members" {
		e.membersAPI(w, r, u, parts[1], parts[3])
		return
	}
	fail(w, 404, "企业接口不存在", "")
}
func (e *Enterprise) createTenant(w http.ResponseWriter, r *http.Request, u enterpriseUser) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 {
		fail(w, 422, "租户名称须为 1 到 200 字节", "name")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		fail(w, 503, "企业服务正在关停", "")
		return
	}
	tenantID := id("tenant")
	a, err := e.openTenant(tenantID)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	published := false
	defer func() {
		if !published {
			a.CloseAdmission()
			a.CancelAll()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if a.WaitOwnedTasks(ctx) == nil {
				a.CloseNetwork()
				a.Store.DB.Close()
			}
		}
	}()
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO enterprise_tenants(id,name) VALUES(?,?)", tenantID, in.Name); err == nil {
		err = e.audit(tx, u.ID, "tenant_create", tenantID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	e.tenants[tenantID] = a
	published = true
	writeJSON(w, 201, enterpriseTenant{ID: tenantID, Name: in.Name, Enabled: true, Version: 1, Role: "tenant_admin"})
}
func (e *Enterprise) updateTenant(w http.ResponseWriter, r *http.Request, u enterpriseUser, tenantID string) {
	var in struct {
		Version int     `json:"version"`
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 200) {
		fail(w, 422, "租户名称无效", "name")
		return
	}
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	var t enterpriseTenant
	err = tx.QueryRow("SELECT id,name,enabled,version FROM enterprise_tenants WHERE id=?", tenantID).Scan(&t.ID, &t.Name, &t.Enabled, &t.Version)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "租户不存在", "")
		return
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if in.Version != t.Version {
		fail(w, 409, "租户版本已变化，请重新读取", "version")
		return
	}
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	t.Version++
	if _, err = tx.Exec("UPDATE enterprise_tenants SET name=?,enabled=?,version=? WHERE id=?", t.Name, t.Enabled, t.Version, t.ID); err == nil {
		err = e.audit(tx, u.ID, "tenant_update", t.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if !t.Enabled {
		e.mu.Lock()
		a := e.tenants[t.ID]
		e.mu.Unlock()
		if a != nil {
			a.CancelAll()
		}
	}
	writeJSON(w, 200, t)
}
func (e *Enterprise) usersAPI(w http.ResponseWriter, r *http.Request, u enterpriseUser) {
	if r.Method == "GET" {
		rows, err := e.root.Store.DB.Query("SELECT id,username,platform_admin,enabled,version FROM enterprise_users ORDER BY username")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		defer rows.Close()
		items := []enterpriseUser{}
		for rows.Next() {
			var user enterpriseUser
			if err = rows.Scan(&user.ID, &user.Username, &user.PlatformAdmin, &user.Enabled, &user.Version); err != nil {
				break
			}
			items = append(items, user)
		}
		if err != nil || rows.Err() != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		PlatformAdmin bool   `json:"platform_admin"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Username) != in.Username || in.Username == "" || len(in.Username) > 200 {
		fail(w, 422, "用户名无效", "username")
		return
	}
	select {
	case e.passwordSlots <- struct{}{}:
		defer func() { <-e.passwordSlots }()
	default:
		fail(w, 429, "密码操作繁忙，请稍后重试", "")
		return
	}
	hash, err := enterprisePassword(in.Password)
	if err != nil {
		fail(w, 422, err.Error(), "password")
		return
	}
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM enterprise_users WHERE username=?", in.Username).Scan(&count); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if count != 0 {
		fail(w, 409, "用户名不可用", "username")
		return
	}
	user := enterpriseUser{ID: id("user"), Username: in.Username, Enabled: true, PlatformAdmin: in.PlatformAdmin, Version: 1}
	if _, err = tx.Exec("INSERT INTO enterprise_users(id,username,password,platform_admin) VALUES(?,?,?,?)", user.ID, user.Username, hash, user.PlatformAdmin); err == nil {
		err = e.audit(tx, u.ID, "user_create", user.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 201, user)
}
func (e *Enterprise) updateUser(w http.ResponseWriter, r *http.Request, u enterpriseUser, userID string) {
	var in struct {
		Version int  `json:"version"`
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	var target enterpriseUser
	err = tx.QueryRow("SELECT id,username,platform_admin,enabled,version FROM enterprise_users WHERE id=?", userID).Scan(&target.ID, &target.Username, &target.PlatformAdmin, &target.Enabled, &target.Version)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "用户不存在", "")
		return
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if target.Version != in.Version {
		fail(w, 409, "用户版本已变化", "version")
		return
	}
	if !in.Enabled && target.PlatformAdmin {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM enterprise_users WHERE enabled=1 AND platform_admin=1 AND id!=?", userID).Scan(&count); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if count == 0 {
			fail(w, 409, "必须保留至少一名启用的平台管理员", "enabled")
			return
		}
	}
	target.Enabled = in.Enabled
	target.Version++
	if _, err = tx.Exec("UPDATE enterprise_users SET enabled=?,version=? WHERE id=?", target.Enabled, target.Version, target.ID); err == nil {
		err = e.audit(tx, u.ID, "user_update", target.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if !target.Enabled {
		e.mu.Lock()
		for k, session := range e.sessions {
			if session.UserID == target.ID {
				delete(e.sessions, k)
			}
		}
		e.mu.Unlock()
	}
	writeJSON(w, 200, target)
}
func (e *Enterprise) membersAPI(w http.ResponseWriter, r *http.Request, u enterpriseUser, tenantID, userID string) {
	var in struct {
		Role    string `json:"role"`
		Version int    `json:"version"`
	}
	if r.Method == "PUT" || r.Method == "DELETE" {
		if !decode(w, r, &in) {
			return
		}
	} else if r.Method != "GET" {
		fail(w, 405, "方法不支持", "")
		return
	}
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	var count int
	err = tx.QueryRow("SELECT (SELECT count(*) FROM enterprise_tenants WHERE id=?)*(SELECT count(*) FROM enterprise_users WHERE id=?)", tenantID, userID).Scan(&count)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if count == 0 {
		fail(w, 404, "用户或租户不存在", "")
		return
	}
	var role string
	var enabled bool
	version := 0
	err = tx.QueryRow("SELECT role,version,enabled FROM enterprise_members WHERE user_id=? AND tenant_id=?", userID, tenantID).Scan(&role, &version, &enabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if r.Method == "GET" {
		if version == 0 {
			fail(w, 404, "成员关系不存在", "")
			return
		}
		writeJSON(w, 200, map[string]any{"role": role, "version": version, "enabled": enabled})
		return
	}
	if in.Version != version {
		fail(w, 409, "成员版本已变化，请重新读取", "version")
		return
	}
	if r.Method == "PUT" {
		if in.Role != "tenant_admin" && in.Role != "viewer" {
			fail(w, 422, "角色须为租户管理员或查看者", "role")
			return
		}
		_, err = tx.Exec("INSERT INTO enterprise_members(user_id,tenant_id,role,version) VALUES(?,?,?,?) ON CONFLICT(user_id,tenant_id) DO UPDATE SET role=excluded.role,version=excluded.version,enabled=1", userID, tenantID, in.Role, version+1)
	} else {
		_, err = tx.Exec("UPDATE enterprise_members SET enabled=0,version=version+1 WHERE user_id=? AND tenant_id=?", userID, tenantID)
	}
	if err == nil {
		err = e.audit(tx, u.ID, "member_"+strings.ToLower(r.Method), tenantID+":"+userID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, map[string]any{"role": in.Role, "version": version + 1, "removed": r.Method == "DELETE"})
}
func (e *Enterprise) changePassword(w http.ResponseWriter, r *http.Request, u enterpriseUser) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	select {
	case e.passwordSlots <- struct{}{}:
		defer func() { <-e.passwordSlots }()
	default:
		fail(w, 429, "密码操作繁忙，请稍后重试", "")
		return
	}
	var old string
	if err := e.root.Store.DB.QueryRow("SELECT password FROM enterprise_users WHERE id=?", u.ID).Scan(&old); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if !enterprisePasswordMatches(old, in.Current) {
		fail(w, 401, "当前密码不正确", "current_password")
		return
	}
	fresh, err := enterprisePassword(in.New)
	if err != nil {
		fail(w, 422, err.Error(), "new_password")
		return
	}
	tx, err := e.root.Store.DB.Begin()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE enterprise_users SET password=?,version=version+1 WHERE id=? AND password=?", fresh, u.ID, old)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if changed != 1 {
		fail(w, 409, "密码已变化，请重新登录", "")
		return
	}
	if err = e.audit(tx, u.ID, "password_change", u.ID); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	e.mu.Lock()
	for k, s := range e.sessions {
		if s.UserID == u.ID {
			delete(e.sessions, k)
		}
	}
	e.mu.Unlock()
	e.cookie(w, "", time.Unix(0, 0))
	writeJSON(w, 200, map[string]bool{"ok": true, "login_required": true})
}
