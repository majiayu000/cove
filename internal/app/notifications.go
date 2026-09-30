package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These tables hold only fixed redacted summaries and local entity identifiers.
const NotificationSchema = `CREATE TABLE IF NOT EXISTS alerts(id TEXT PRIMARY KEY,dedup_key TEXT NOT NULL,kind TEXT NOT NULL,entity_kind TEXT NOT NULL,entity_id TEXT NOT NULL,generation INTEGER,period_start TEXT,version INTEGER NOT NULL DEFAULT 1,severity TEXT NOT NULL,occurrences INTEGER NOT NULL DEFAULT 1,first_seen_at TEXT NOT NULL,last_seen_at TEXT NOT NULL,resolved_at TEXT,dismissed_at TEXT,redacted_detail_json TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS alerts_active ON alerts(dedup_key) WHERE resolved_at IS NULL;
CREATE INDEX IF NOT EXISTS alerts_time ON alerts(last_seen_at DESC,id DESC);
CREATE TABLE IF NOT EXISTS alert_deliveries(event_id TEXT PRIMARY KEY,alert_id TEXT NOT NULL REFERENCES alerts(id),channel TEXT NOT NULL,state TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,next_attempt_at TEXT,created_at TEXT NOT NULL,expires_at TEXT NOT NULL,redacted_payload_json TEXT NOT NULL,last_error_json TEXT);
CREATE INDEX IF NOT EXISTS deliveries_due ON alert_deliveries(state,next_attempt_at);`

const notificationQueueLimit = 256

var notificationKinds = []string{"auth", "quota", "budget", "storage", "source_health", "update", "job"}

type notificationSettings struct {
	Version    int      `json:"version"`
	Enabled    bool     `json:"enabled"`
	URL        string   `json:"url"`
	Signature  bool     `json:"signature"`
	EventKinds []string `json:"event_kinds"`
	HeaderRef  string   `json:"-"`
}
type notificationStored struct {
	notificationSettings
	HeaderRef string `json:"header_ref,omitempty"`
}
type notificationCredentials struct {
	Secret  string            `json:"secret"`
	Headers map[string]string `json:"headers,omitempty"`
}
type notificationInput struct {
	notificationSettings
	Secret  *string           `json:"secret"`
	Headers map[string]string `json:"headers"`
}
type notificationEntity struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Display string `json:"display_name_redacted"`
}
type notificationPayload struct {
	EventID         string             `json:"event_id"`
	Type            string             `json:"type"`
	OccurredAt      time.Time          `json:"occurred_at"`
	Entity          notificationEntity `json:"entity"`
	State           string             `json:"state"`
	PreviousState   string             `json:"previous_state"`
	Summary         string             `json:"summary"`
	LocalDetailPath string             `json:"local_detail_path"`
}
type notificationFact struct {
	localAlert
	EntityKind string
}
type notificationSnapshot struct {
	Faults  map[string]notificationFact
	Unknown map[string]bool // Unknown observations never assert recovery.
}

func notificationScope(kind, entity string, generation int) string {
	return kind + "\x00" + entity + "\x00" + strconv.Itoa(generation)
}
func notificationDedup(v localAlert) string {
	return notificationScope(v.Kind, v.EntityID, v.Generation) + "\x00" + v.Period
}
func notificationSummary(kind string, resolved bool) string {
	if resolved {
		return "此前提醒的状态已恢复"
	}
	switch kind {
	case "auth":
		return "账号需要重新认证"
	case "quota":
		return "已观测额度窗口剩余不超过 10%"
	case "budget":
		return "本地预算可用额不超过 10%"
	case "storage":
		return "本地存储出现故障"
	case "source_health":
		return "来源连续失败或仍在冷却，等待成功观测确认恢复"
	case "update":
		return "已明确记录的更新操作失败"
	case "job":
		return "后台任务状态或账务未决，需要复核"
	default:
		return "合成通知测试"
	}
}
func notificationKindAllowed(kind string) bool {
	for _, v := range notificationKinds {
		if v == kind {
			return true
		}
	}
	return false
}
func (s *Store) readNotifications(ctx context.Context) (notificationSettings, error) {
	v := notificationSettings{Version: 1, EventKinds: append([]string{}, notificationKinds...)}
	var raw string
	e := s.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='webhook'").Scan(&raw)
	if e == sql.ErrNoRows {
		return v, nil
	}
	if e != nil {
		return v, e
	}
	var stored notificationStored
	if json.Unmarshal([]byte(raw), &stored) != nil || stored.Version < 1 {
		return v, storageError()
	}
	stored.notificationSettings.HeaderRef = stored.HeaderRef
	return stored.notificationSettings, nil
}

// Quota adapter contract: fresh normalized windows, not provider raw JSON.
// A missing, expired, or unsupported window is unknown and cannot clear a fault.
func notificationQuotaFacts(src Source, now time.Time) ([]notificationFact, bool) {
	raw, e := json.Marshal(src.Quota)
	if e != nil {
		return nil, false
	}
	var q struct {
		Status     string     `json:"status"`
		ObservedAt *time.Time `json:"observed_at"`
		ExpiresAt  *time.Time `json:"expires_at"`
		Windows    []struct {
			Dimension   string     `json:"dimension"`
			Unit        string     `json:"unit"`
			Remaining   *float64   `json:"remaining"`
			Limit       *float64   `json:"limit"`
			UsedPercent *float64   `json:"used_percent"`
			WindowStart *time.Time `json:"window_start"`
			ResetAt     *time.Time `json:"reset_at"`
		} `json:"windows"`
	}
	if json.Unmarshal(raw, &q) != nil || q.Status != "available" || q.ObservedAt == nil || q.ExpiresAt == nil || q.ObservedAt.After(now.Add(time.Minute)) || !q.ExpiresAt.After(now) || len(q.Windows) == 0 {
		return nil, false
	}
	facts := []notificationFact{}
	complete := true
	for _, w := range q.Windows {
		if w.Dimension == "" || len(w.Dimension) > 128 || w.Unit == "" || w.ResetAt == nil || !w.ResetAt.After(now) {
			complete = false
			continue
		}
		var pct float64
		if w.UsedPercent != nil && *w.UsedPercent >= 0 && *w.UsedPercent <= 100 && !math.IsNaN(*w.UsedPercent) && !math.IsInf(*w.UsedPercent, 0) {
			pct = 100 - *w.UsedPercent
		} else if w.Remaining != nil && w.Limit != nil && *w.Limit > 0 && *w.Remaining >= 0 && *w.Remaining <= *w.Limit && !math.IsInf(*w.Limit, 0) && !math.IsInf(*w.Remaining, 0) {
			pct = 100 * *w.Remaining / *w.Limit
		} else {
			complete = false
			continue
		}
		if pct <= 10 {
			period := digest(w.Dimension+"\x00"+w.Unit) + ":" + w.ResetAt.UTC().Format(time.RFC3339Nano)
			if w.WindowStart != nil {
				period += ":" + w.WindowStart.UTC().Format(time.RFC3339Nano)
			}
			facts = append(facts, notificationFact{localAlert: localAlert{Kind: "quota", EntityID: src.ID, Generation: src.Generation, Period: period}, EntityKind: "source"})
		}
	}
	return facts, complete
}

func (a *App) notificationSnapshot(ctx context.Context, now time.Time) (notificationSnapshot, error) {
	out := notificationSnapshot{Faults: map[string]notificationFact{}, Unknown: map[string]bool{}}
	add := func(f notificationFact) {
		f.Summary = notificationSummary(f.Kind, false)
		out.Faults[notificationDedup(f.localAlert)] = f
	}
	rows, e := a.Store.DB.QueryContext(ctx, "SELECT id,generation,data FROM accounts")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var account Account
		var raw, aid string
		var gen int
		if e = rows.Scan(&aid, &gen, &raw); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &account); e != nil {
			break
		}
		if !account.Deleted && account.AuthState == "needs_reauth" {
			add(notificationFact{localAlert: localAlert{Kind: "auth", EntityID: aid, Generation: gen}, EntityKind: "account"})
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	if a.storageFailed.Load() || a.Secrets.Health() != nil {
		add(notificationFact{localAlert: localAlert{Kind: "storage", EntityID: "local"}, EntityKind: "instance"})
	}
	rows, e = a.Store.DB.QueryContext(ctx, "SELECT data FROM sources")
	if e != nil {
		return out, e
	}
	sources := []Source{}
	for rows.Next() {
		var src Source
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &src); e != nil {
			break
		}
		if !src.Deleted {
			sources = append(sources, src)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	a.policyState.mu.Lock()
	health := map[policyScope]policyHealth{}
	for k, v := range a.policyState.health {
		health[k] = v
	}
	a.policyState.mu.Unlock()
	for _, src := range sources {
		fs, known := notificationQuotaFacts(src, now)
		for _, f := range fs {
			add(f)
		}
		if !known {
			out.Unknown[notificationScope("quota", src.ID, src.Generation)] = true
		}
		h, seen := health[policyScope{Kind: "source", ID: src.ID, Generation: src.Generation}]
		if !seen || h.ObservedAt.IsZero() {
			out.Unknown[notificationScope("source_health", src.ID, src.Generation)] = true
		} else if h.Failures >= 3 || h.Blocked || h.Until.After(now) {
			add(notificationFact{localAlert: localAlert{Kind: "source_health", EntityID: src.ID, Generation: src.Generation}, EntityKind: "source"})
		}
	}
	rows, e = a.Store.DB.QueryContext(ctx, "SELECT data FROM budgets")
	if e != nil {
		return out, e
	}
	budgets := []Budget{}
	for rows.Next() {
		var b Budget
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal([]byte(raw), &b); e != nil {
			break
		}
		if b.Enabled {
			budgets = append(budgets, b)
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, b := range budgets {
		sum, e := summarizeBudget(a.Store.DB, b, now)
		if e != nil {
			return out, e
		}
		limit, e := accountingRat(b.AmountLimit)
		if e != nil {
			return out, e
		}
		available, e := ledgerRat(sum.Available)
		if e != nil {
			return out, e
		}
		threshold := new(big.Rat).Quo(limit, big.NewRat(10, 1))
		if available.Cmp(threshold) <= 0 {
			add(notificationFact{localAlert: localAlert{Kind: "budget", EntityID: b.ID, Generation: 0, Period: sum.PeriodStart.UTC().Format(time.RFC3339Nano)}, EntityKind: "budget"})
		}
	}
	rows, e = a.Store.DB.QueryContext(ctx, "SELECT id,source_generation,state,created_at,last_observed_at FROM jobs WHERE settled_at IS NULL")
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var jid, state, created string
		var gen int
		var observed sql.NullString
		if e = rows.Scan(&jid, &gen, &state, &created, &observed); e != nil {
			break
		}
		at, pe := time.Parse(time.RFC3339Nano, created)
		if pe != nil {
			e = pe
			break
		}
		if observed.Valid {
			if t, pe := time.Parse(time.RFC3339Nano, observed.String); pe == nil {
				at = t
			}
		}
		unresolved := state == "unknown" || state == "unrecognized" || state == "submitting_unknown" || state == "pending_reconciliation" || state == "interrupted" || state == "cancel_unknown"
		if unresolved || now.Sub(at) > 5*time.Minute {
			add(notificationFact{localAlert: localAlert{Kind: "job", EntityID: jid, Generation: gen, Period: created}, EntityKind: "job"})
		}
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return out, e
	}
	var update string
	e = a.Store.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='notification_update'").Scan(&update)
	if e != nil && e != sql.ErrNoRows {
		return out, e
	}
	if e == nil {
		var state struct {
			Failed     bool `json:"failed"`
			Generation int  `json:"generation"`
		}
		if json.Unmarshal([]byte(update), &state) != nil {
			return out, storageError()
		}
		if state.Failed {
			add(notificationFact{localAlert: localAlert{Kind: "update", EntityID: "local", Generation: state.Generation}, EntityKind: "instance"})
		}
	}
	return out, nil
}

type notificationAlertDetail struct {
	Summary string `json:"summary"`
	State   string `json:"state"`
}

func notificationScan(rows interface{ Scan(...any) error }) (localAlert, error) {
	var v localAlert
	var created, seen, detail string
	var resolved, dismissed sql.NullString
	e := rows.Scan(&v.ID, &v.Kind, &v.EntityID, &v.Generation, &v.Period, &v.Version, &v.Count, &created, &seen, &resolved, &dismissed, &detail)
	if e != nil {
		return v, e
	}
	v.CreatedAt, e = time.Parse(time.RFC3339Nano, created)
	if e != nil {
		return v, e
	}
	v.LastSeen, e = time.Parse(time.RFC3339Nano, seen)
	if e != nil {
		return v, e
	}
	var d notificationAlertDetail
	if json.Unmarshal([]byte(detail), &d) != nil {
		return v, storageError()
	}
	v.Summary = notificationSummary(v.Kind, false)
	v.State = "active"
	if resolved.Valid {
		t, e := time.Parse(time.RFC3339Nano, resolved.String)
		if e != nil {
			return v, e
		}
		v.ResolvedAt = &t
		v.State = "resolved"
	}
	if dismissed.Valid {
		t, e := time.Parse(time.RFC3339Nano, dismissed.String)
		if e != nil {
			return v, e
		}
		v.DismissedAt = &t
		if v.ResolvedAt == nil {
			v.State = "dismissed"
		}
	}
	return v, nil
}

const notificationAlertColumns = "id,kind,entity_id,coalesce(generation,0),coalesce(period_start,''),version,occurrences,first_seen_at,last_seen_at,resolved_at,dismissed_at,redacted_detail_json"

func notificationReadAlerts(q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, ctx context.Context) ([]localAlert, error) {
	rows, e := q.QueryContext(ctx, "SELECT "+notificationAlertColumns+" FROM alerts ORDER BY last_seen_at DESC,id DESC")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []localAlert{}
	for rows.Next() {
		v, e := notificationScan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func notificationEnqueue(tx *sql.Tx, settings notificationSettings, f notificationFact, previous string, now time.Time) error {
	if !settings.Enabled {
		return nil
	}
	selected := false
	for _, k := range settings.EventKinds {
		if k == f.Kind {
			selected = true
		}
	}
	if !selected {
		return nil
	}
	payload := notificationPayload{EventID: id("event"), Type: f.Kind, OccurredAt: now, Entity: notificationEntity{Kind: f.EntityKind, ID: f.EntityID, Display: f.EntityKind}, State: f.State, PreviousState: previous, Summary: notificationSummary(f.Kind, f.State == "resolved"), LocalDetailPath: "/settings#alerts"}
	var count int
	if e := tx.QueryRow("SELECT count(*) FROM alert_deliveries WHERE state IN ('pending','sending')").Scan(&count); e != nil {
		return e
	}
	state := "pending"
	var errorJSON any
	if count >= notificationQueueLimit {
		state = "delivery_failed"
		errorJSON = encode(map[string]string{"code": "queue_full"})
	}
	_, e := tx.Exec("INSERT INTO alert_deliveries(event_id,alert_id,channel,state,next_attempt_at,created_at,expires_at,redacted_payload_json,last_error_json) VALUES(?,?,?,?,?,?,?,?,?)", payload.EventID, f.ID, "webhook:v"+strconv.Itoa(settings.Version), state, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(24*time.Hour).Format(time.RFC3339Nano), encode(payload), errorJSON)
	return e
}

// Root replaces reconcileLocalAlerts with this method, without holding a.mu.
func (a *App) reconcileNotifications(ctx context.Context, now time.Time) ([]localAlert, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot, e := a.notificationSnapshot(ctx, now)
	if e != nil {
		return nil, e
	}
	settings, e := a.Store.readNotifications(ctx)
	if e != nil {
		return nil, e
	}
	tx, e := a.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	alerts, e := notificationReadAlerts(tx, ctx)
	if e != nil {
		return nil, e
	}
	for _, v := range alerts {
		if v.ResolvedAt != nil {
			continue
		}
		key := notificationDedup(v)
		_, active := snapshot.Faults[key]
		if !active {
			// State faults have no provider window. Their durable period begins
			// at activation and remains fixed until confirmed recovery.
			stateKey := notificationScope(v.Kind, v.EntityID, v.Generation) + "\x00"
			if fact, exists := snapshot.Faults[stateKey]; exists && fact.Period == "" {
				key, active = stateKey, true
			}
		}
		if active {
			_, e = tx.ExecContext(ctx, "UPDATE alerts SET occurrences=occurrences+1,last_seen_at=?,version=version+1 WHERE id=?", now.Format(time.RFC3339Nano), v.ID)
			delete(snapshot.Faults, key)
		} else if !snapshot.Unknown[notificationScope(v.Kind, v.EntityID, v.Generation)] {
			v.State = "resolved"
			v.ResolvedAt = &now
			_, e = tx.ExecContext(ctx, "UPDATE alerts SET resolved_at=?,last_seen_at=?,version=version+1 WHERE id=?", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), v.ID)
			if e == nil {
				e = notificationEnqueue(tx, settings, notificationFact{localAlert: v, EntityKind: notificationEntityKind(v.Kind)}, "active", now)
			}
		}
		if e != nil {
			return nil, e
		}
	}
	keys := []string{}
	for k := range snapshot.Faults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := snapshot.Faults[k]
		f.ID = id("alert")
		f.State = "active"
		f.Count = 1
		f.Version = 1
		f.CreatedAt = now
		f.LastSeen = now
		if f.Period == "" {
			f.Period = now.Format(time.RFC3339Nano)
		}
		k = notificationDedup(f.localAlert)
		_, e = tx.ExecContext(ctx, "INSERT INTO alerts(id,dedup_key,kind,entity_kind,entity_id,generation,period_start,severity,first_seen_at,last_seen_at,redacted_detail_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)", f.ID, k, f.Kind, f.EntityKind, f.EntityID, f.Generation, f.Period, "warning", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), encode(notificationAlertDetail{Summary: f.Summary, State: "active"}))
		if e != nil {
			return nil, e
		}
		if e = notificationEnqueue(tx, settings, f, "healthy", now); e != nil {
			return nil, e
		}
	}
	// Retain at most 1000 recent resolved alerts; active faults are never evicted.
	_, e = tx.ExecContext(ctx, "DELETE FROM alert_deliveries WHERE state NOT IN ('pending','sending') AND (created_at<? OR event_id IN (SELECT event_id FROM alert_deliveries WHERE state NOT IN ('pending','sending') ORDER BY created_at DESC,event_id DESC LIMIT -1 OFFSET 1000))", now.Add(-24*time.Hour).Format(time.RFC3339Nano))
	if e != nil {
		return nil, e
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM alerts WHERE resolved_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM alert_deliveries d WHERE d.alert_id=alerts.id) AND (resolved_at<? OR id IN (SELECT id FROM alerts WHERE resolved_at IS NOT NULL ORDER BY last_seen_at DESC,id DESC LIMIT -1 OFFSET 1000))", now.Add(-90*24*time.Hour).Format(time.RFC3339Nano))
	if e != nil {
		return nil, e
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM settings WHERE key LIKE 'notification_audit_%' AND (json_extract(value,'$.at')<? OR key IN (SELECT key FROM settings WHERE key LIKE 'notification_audit_%' ORDER BY json_extract(value,'$.at') DESC,key DESC LIMIT -1 OFFSET 1000))", now.Add(-90*24*time.Hour).Format(time.RFC3339Nano))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return notificationReadAlerts(a.Store.DB, ctx)
}
func notificationEntityKind(kind string) string {
	switch kind {
	case "auth":
		return "account"
	case "quota", "source_health":
		return "source"
	case "budget":
		return "budget"
	case "job":
		return "job"
	default:
		return "instance"
	}
}

// Explicit update lifecycle hook. Caller owns the backup gate, calls outside a.mu.
// Never pass raw updater errors, endpoint URLs, or release notes here.
func (a *App) noteNotificationUpdate(ctx context.Context, generation int, failed bool) error {
	if generation < 1 {
		return errors.New("更新代次无效")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var raw string
	e := a.Store.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='notification_update'").Scan(&raw)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil {
		var current struct {
			Generation int `json:"generation"`
		}
		if json.Unmarshal([]byte(raw), &current) != nil {
			return storageError()
		}
		if generation < current.Generation {
			return &accountingError{Status: 409, Field: "generation", Message: "旧更新结果不能覆盖新代次"}
		}
	}
	_, e = a.Store.DB.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('notification_update',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(map[string]any{"failed": failed, "generation": generation}))
	return e
}
func (a *App) notificationsAlertsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/admin/alerts" {
		state := r.URL.Query().Get("state")
		if state != "" && state != "active" && state != "resolved" && state != "dismissed" {
			fail(w, 422, "提醒状态过滤无效", "state")
			return
		}
		alerts, e := a.reconcileNotifications(r.Context(), time.Now().UTC())
		if e != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		out := []localAlert{}
		for _, v := range alerts {
			if state == "" || v.State == state {
				out = append(out, v)
			}
		}
		writeJSON(w, 200, map[string]any{"items": out, "next_cursor": nil})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "dismiss" || r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Version int `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	result, e := a.Store.DB.ExecContext(r.Context(), "UPDATE alerts SET dismissed_at=?,version=version+1 WHERE id=? AND version=?", now.Format(time.RFC3339Nano), parts[2], in.Version)
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	n, e := result.RowsAffected()
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	v, e := notificationScan(a.Store.DB.QueryRowContext(r.Context(), "SELECT "+notificationAlertColumns+" FROM alerts WHERE id=?", parts[2]))
	if e == sql.ErrNoRows {
		fail(w, 404, "提醒不存在", "")
		return
	}
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if n == 0 {
		fail(w, 409, "提醒已改变，请刷新", "version")
		return
	}
	writeJSON(w, 200, v)
}

func (a *App) notificationsAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/admin/notifications" && r.URL.Path != "/admin/notifications/preview" && r.URL.Path != "/admin/notifications/deliveries" {
		return false
	}
	switch {
	case r.URL.Path == "/admin/notifications" && r.Method == "GET":
		a.mu.Lock()
		s, e := a.Store.readNotifications(r.Context())
		a.mu.Unlock()
		if e != nil {
			fail(w, 503, storageError().Error(), "")
			return true
		}
		writeJSON(w, 200, map[string]any{"settings": s, "credential_configured": s.HeaderRef != "", "queue_capacity": notificationQueueLimit, "event_kinds": notificationKinds})
		return true
	case r.URL.Path == "/admin/notifications" && r.Method == "PUT":
		a.saveNotifications(w, r)
		return true
	case r.URL.Path == "/admin/notifications/preview" && r.Method == "POST":
		a.previewNotification(w, r)
		return true
	case r.URL.Path == "/admin/notifications/deliveries" && r.Method == "GET":
		a.notificationDeliveriesAPI(w, r)
		return true
	default:
		fail(w, 405, "方法不支持", "")
		return true
	}
}
func (a *App) saveNotifications(w http.ResponseWriter, r *http.Request) {
	var in notificationInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.EventKinds) == 0 || len(in.EventKinds) > len(notificationKinds) {
		fail(w, 422, "请选择提醒类型", "event_kinds")
		return
	}
	seen := map[string]bool{}
	for _, k := range in.EventKinds {
		if !notificationKindAllowed(k) || seen[k] {
			fail(w, 422, "提醒类型无效", "event_kinds")
			return
		}
		seen[k] = true
	}
	if in.URL != "" {
		if _, e := notificationURL(in.URL); e != nil {
			fail(w, 422, "需无凭据、无查询串的 HTTPS webhook URL", "url")
			return
		}
	} else if in.Enabled {
		fail(w, 422, "请填写 HTTPS webhook URL", "url")
		return
	}
	if in.Secret != nil && (len(*in.Secret) > 4096 || strings.ContainsAny(*in.Secret, "\r\n")) {
		fail(w, 422, "签名凭据无效", "secret")
		return
	}
	for k, v := range in.Headers {
		if k != "Authorization" && k != "X-API-Key" || len(v) > 8192 || strings.ContainsAny(v, "\r\n") {
			fail(w, 422, "通知凭据 header 无效", "headers")
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, e := a.Store.readNotifications(r.Context())
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if in.Version != current.Version {
		fail(w, 409, "通知设置已修改", "version")
		return
	}
	creds := notificationCredentials{}
	if current.HeaderRef != "" {
		raw, e := a.Secrets.Get(current.HeaderRef)
		if e != nil || json.Unmarshal([]byte(raw), &creds) != nil {
			fail(w, 503, "通知凭据不可读", "")
			return
		}
	}
	if in.Secret != nil {
		creds.Secret = *in.Secret
	}
	if in.Headers != nil {
		creds.Headers = in.Headers
	}
	if in.Enabled && in.Signature && creds.Secret == "" {
		fail(w, 422, "签名开启时需要签名凭据", "secret")
		return
	}
	next := in.notificationSettings
	next.Version = current.Version + 1
	next.HeaderRef = current.HeaderRef
	sort.Strings(next.EventKinds)
	changed := in.Secret != nil || in.Headers != nil
	if changed {
		next.HeaderRef = id("webhook")
		if e = a.Secrets.Put(next.HeaderRef, encode(creds)); e != nil {
			fail(w, 503, "通知凭据保存失败", "")
			return
		}
	}
	tx, e := a.Store.DB.BeginTx(r.Context(), nil)
	if e == nil {
		defer tx.Rollback()
		_, e = tx.Exec("INSERT INTO settings(key,value) VALUES('webhook',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(notificationStored{notificationSettings: next, HeaderRef: next.HeaderRef}))
		if e == nil {
			_, e = tx.Exec("UPDATE alert_deliveries SET state='cancelled',next_attempt_at=NULL,last_error_json=? WHERE state IN ('pending','sending')", encode(map[string]string{"code": "settings_changed"}))
		}
		if e == nil {
			_, e = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?)", "notification_audit_"+id("audit"), encode(map[string]any{"at": time.Now().UTC(), "action": "webhook_settings", "version": next.Version, "enabled": next.Enabled, "signature": next.Signature, "event_kinds": next.EventKinds}))
		}
		if e == nil {
			e = tx.Commit()
		}
	}
	if e != nil {
		if changed {
			a.cleanupSecret(next.HeaderRef)
		}
		fail(w, 503, storageError().Error(), "")
		return
	}
	if changed && current.HeaderRef != "" {
		a.cleanupSecret(current.HeaderRef)
	}
	writeJSON(w, 200, map[string]any{"settings": next, "credential_configured": next.HeaderRef != ""})
}

func notificationURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(raw) > 2048 {
		return nil, errors.New("webhook URL 无效")
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return nil, errors.New("webhook 端口无效")
		}
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && !notificationPublicIP(ip) {
		return nil, errors.New("webhook 地址不允许")
	}
	return u, nil
}
func notificationPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// Includes CGNAT, benchmark/documentation ranges, protocol assignments and
	// well-known IPv6 translation prefixes that could embed an internal address.
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		if netip.MustParsePrefix(s).Contains(ip) {
			return false
		}
	}
	return true
}

type notificationLookup func(context.Context, string, string) ([]netip.Addr, error)
type notificationDial func(context.Context, string, string) (net.Conn, error)

// DNS is resolved once, every result is validated, then the socket receives only
// a validated numeric address. Host/SNI remain the user's HTTPS hostname.
func notificationPinnedClient(ctx context.Context, u *url.URL, lookup notificationLookup, dial notificationDial) (*http.Client, error) {
	host := u.Hostname()
	ips, e := lookup(ctx, "ip", host)
	if e != nil || len(ips) == 0 || len(ips) > 32 {
		return nil, errors.New("webhook DNS 不可用")
	}
	for _, ip := range ips {
		if !notificationPublicIP(ip) {
			return nil, errors.New("webhook DNS 地址不允许")
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false, MaxResponseHeaderBytes: 32 << 10, ResponseHeaderTimeout: 5 * time.Second, TLSHandshakeTimeout: 5 * time.Second, TLSClientConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		requestedHost, requestedPort, e := net.SplitHostPort(address)
		if e != nil || requestedHost != host || requestedPort != port {
			return nil, errors.New("webhook 连接目标改变")
		}
		var last error
		for _, ip := range ips {
			conn, e := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func notificationSignedRequest(ctx context.Context, settings notificationSettings, creds notificationCredentials, payload string, now time.Time) (*http.Request, error) {
	var parsed notificationPayload
	if json.Unmarshal([]byte(payload), &parsed) != nil || parsed.EventID == "" || len(payload) > 8192 {
		return nil, errors.New("通知 payload 无效")
	}
	req, e := http.NewRequestWithContext(ctx, "POST", settings.URL, strings.NewReader(payload))
	if e != nil {
		return nil, e
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cove-Event-ID", parsed.EventID)
	stamp := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set("X-Cove-Timestamp", stamp)
	for k, v := range creds.Headers {
		if k != "Authorization" && k != "X-API-Key" || strings.ContainsAny(v, "\r\n") || len(v) > 8192 {
			return nil, errors.New("通知 header 无效")
		}
		req.Header.Set(k, v)
	}
	if settings.Signature {
		if creds.Secret == "" {
			return nil, errors.New("通知签名凭据缺失")
		}
		mac := hmac.New(sha256.New, []byte(creds.Secret))
		mac.Write([]byte(stamp + "."))
		mac.Write([]byte(payload))
		req.Header.Set("X-Cove-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return req, nil
}

type notificationSend func(context.Context, notificationSettings, notificationCredentials, string, time.Time) (int, string, error)

func sendNotification(ctx context.Context, s notificationSettings, c notificationCredentials, payload string, now time.Time) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u, e := notificationURL(s.URL)
	if e != nil {
		return 0, "", e
	}
	client, e := notificationPinnedClient(ctx, u, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 5 * time.Second}).DialContext)
	if e != nil {
		return 0, "", e
	}
	defer client.CloseIdleConnections()
	req, e := notificationSignedRequest(ctx, s, c, payload, now)
	if e != nil {
		return 0, "", e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, "", errors.New("webhook 网络投递失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header.Get("Retry-After"), nil
}
func notificationRetryAfter(raw string, now time.Time) time.Duration {
	if n, e := strconv.Atoi(raw); e == nil {
		if n <= 0 {
			return 0
		}
		if n > 300 {
			n = 300
		}
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(raw); e == nil {
		d := t.Sub(now)
		if d <= 0 {
			return 0
		}
		if d > 5*time.Minute {
			d = 5 * time.Minute
		}
		return d
	}
	return 0
}
func (a *App) deliverNotificationQueue(ctx context.Context, now time.Time, send notificationSend) error {
	for i := 0; i < 4; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a.mu.Lock()
		s, e := a.Store.readNotifications(ctx)
		if e != nil {
			a.mu.Unlock()
			return e
		}
		// Expired/sending claims are recovered with their stable event ID; an attempt
		// was already charged before network I/O, including crashes after submission.
		_, e = a.Store.DB.ExecContext(ctx, "UPDATE alert_deliveries SET state='delivery_failed',next_attempt_at=NULL,last_error_json=? WHERE state IN ('pending','sending') AND (expires_at<=? OR attempt_count>=3)", encode(map[string]string{"code": "expired_or_attempt_limit"}), now.Format(time.RFC3339Nano))
		if e != nil {
			a.mu.Unlock()
			return e
		}
		if !s.Enabled {
			a.mu.Unlock()
			return nil
		}
		var event, payload string
		var attempts int
		e = a.Store.DB.QueryRowContext(ctx, "SELECT event_id,redacted_payload_json,attempt_count FROM alert_deliveries WHERE channel=? AND state IN ('pending','sending') AND next_attempt_at<=? AND expires_at>? AND attempt_count<3 ORDER BY next_attempt_at,event_id LIMIT 1", "webhook:v"+strconv.Itoa(s.Version), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)).Scan(&event, &payload, &attempts)
		if e == sql.ErrNoRows {
			a.mu.Unlock()
			return nil
		}
		if e != nil {
			a.mu.Unlock()
			return e
		}
		result, e := a.Store.DB.ExecContext(ctx, "UPDATE alert_deliveries SET state='sending',attempt_count=attempt_count+1,next_attempt_at=? WHERE event_id=? AND attempt_count=? AND state IN ('pending','sending') AND next_attempt_at<=?", now.Add(60*time.Second).Format(time.RFC3339Nano), event, attempts, now.Format(time.RFC3339Nano))
		if e != nil {
			a.mu.Unlock()
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			a.mu.Unlock()
			return e
		}
		if n == 0 {
			a.mu.Unlock()
			continue
		}
		creds := notificationCredentials{}
		if s.HeaderRef != "" {
			var raw string
			raw, e = a.Secrets.Get(s.HeaderRef)
			if e == nil {
				e = json.Unmarshal([]byte(raw), &creds)
			}
		}
		a.mu.Unlock()
		status, retry := 0, ""
		if e == nil {
			status, retry, e = send(ctx, s, creds, payload, now)
		}
		state, code, due := "delivered", "", any(nil)
		attempts++
		if e != nil || status < 200 || status >= 300 {
			code = "network_failed"
			if status >= 300 && status < 400 {
				code = "redirect_blocked"
			} else if status > 0 {
				code = "http_failed"
			}
			state = "delivery_failed"
			retryable := e != nil || status == 429 || status >= 500 || status == 408
			if attempts < 3 && retryable {
				state = "pending"
				delay := time.Second
				if attempts == 2 {
					delay = 10 * time.Second
				}
				if status == 429 {
					if after := notificationRetryAfter(retry, now); after > delay {
						delay = after
					}
				}
				due = now.Add(delay).Format(time.RFC3339Nano)
			}
		}
		var errorJSON any
		if code != "" {
			errorJSON = encode(map[string]any{"code": code, "http_status": status})
		}
		// Persist results even after cancellation, using a bounded local context.
		persist, cancel := context.WithTimeout(context.Background(), time.Second)
		a.mu.Lock()
		_, e = a.Store.DB.ExecContext(persist, "UPDATE alert_deliveries SET state=?,next_attempt_at=?,last_error_json=? WHERE event_id=? AND state='sending' AND attempt_count=?", state, due, errorJSON, event, attempts)
		a.mu.Unlock()
		cancel()
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *App) notificationDeliveriesAPI(w http.ResponseWriter, r *http.Request) {
	rows, e := a.Store.DB.QueryContext(r.Context(), "SELECT event_id,alert_id,state,attempt_count,next_attempt_at,created_at,last_error_json FROM alert_deliveries ORDER BY created_at DESC,event_id DESC LIMIT 100")
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var event, alert, state, created string
		var next, last sql.NullString
		var n int
		if e = rows.Scan(&event, &alert, &state, &n, &next, &created, &last); e != nil {
			break
		}
		v := map[string]any{"event_id": event, "alert_id": alert, "state": state, "attempt_count": n, "created_at": created, "next_attempt_at": nil}
		if next.Valid {
			v["next_attempt_at"] = next.String
		}
		if last.Valid {
			var redacted map[string]any
			if json.Unmarshal([]byte(last.String), &redacted) == nil {
				v["error"] = redacted
			}
		}
		out = append(out, v)
	}
	if e == nil {
		e = rows.Err()
	}
	if e != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	writeJSON(w, 200, map[string]any{"items": out, "next_cursor": nil})
}
func (a *App) previewNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int  `json:"version"`
		Confirm bool `json:"confirm_external_send"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Confirm {
		fail(w, 422, "测试通知需要明确确认发送合成事件", "confirm_external_send")
		return
	}
	a.mu.Lock()
	s, e := a.Store.readNotifications(r.Context())
	if e == nil && s.Version != in.Version {
		e = &accountingError{Status: 409, Field: "version", Message: "通知设置已修改"}
	}
	creds := notificationCredentials{}
	if e == nil && s.HeaderRef != "" {
		var raw string
		raw, e = a.Secrets.Get(s.HeaderRef)
		if e == nil {
			e = json.Unmarshal([]byte(raw), &creds)
		}
	}
	a.mu.Unlock()
	if e != nil {
		var conflict *accountingError
		if errors.As(e, &conflict) {
			fail(w, conflict.Status, conflict.Message, conflict.Field)
		} else {
			fail(w, 503, "通知设置或凭据不可读", "")
		}
		return
	}
	if s.URL == "" {
		fail(w, 422, "请先保存通知 URL", "url")
		return
	}
	now := time.Now().UTC()
	payload := notificationPayload{EventID: id("sample"), Type: "test", OccurredAt: now, Entity: notificationEntity{Kind: "sample", ID: "synthetic", Display: "synthetic"}, State: "test", PreviousState: "test", Summary: "Cove 合成通知测试，不含真实提醒数据", LocalDetailPath: "/settings#notifications"}
	status, _, sendErr := sendNotification(r.Context(), s, creds, encode(payload), now)
	auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
	defer auditCancel()
	a.mu.Lock()
	_, auditErr := a.Store.DB.ExecContext(auditCtx, "INSERT INTO settings(key,value) VALUES(?,?)", "notification_audit_"+id("audit"), encode(map[string]any{"at": now, "action": "webhook_sample", "version": s.Version, "event_id": payload.EventID, "delivered": sendErr == nil && status >= 200 && status < 300, "http_status": status}))
	a.mu.Unlock()
	if auditErr != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if sendErr != nil || status < 200 || status >= 300 {
		fail(w, 502, "合成测试通知未送达；未重试任何模型请求", "")
		return
	}
	writeJSON(w, 200, map[string]any{"event_id": payload.EventID, "state": "delivered", "synthetic": true})
}

// Root starts exactly one loop under its existing maintenance context and owns
// its goroutine in ownedTasks. Each tick separately owns the full-backup gate.
func (a *App) notificationsLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		e := a.notificationsTick(ctx)
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		const message = "通知状态观测或投递记录保存失败"
		if e != nil && !a.backupQuiescing && !a.stopping && !a.stagedAdmission {
			a.maintenanceError = message
		} else if e == nil && a.maintenanceError == message {
			a.maintenanceError = ""
		}
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) notificationsTick(ctx context.Context) error {
	a.mu.Lock()
	if a.stagedAdmission {
		a.mu.Unlock()
		return errors.New("恢复实例尚未激活")
	}
	release, e := a.beginBackupOwnerLocked()
	if e != nil {
		a.mu.Unlock()
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	requestID := id("notifications")
	a.running[requestID] = cancel
	a.ownedTasks.Add(1)
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.running, requestID)
		a.mu.Unlock()
		release()
		a.ownedTasks.Done()
	}()
	if _, e = a.reconcileNotifications(ctx, time.Now().UTC()); e != nil {
		return e
	}
	return a.deliverNotificationQueue(ctx, time.Now().UTC(), sendNotification)
}

// Allow backup owners to inspect the webhook reference without secret values.
func notificationRequiredSecret(db *sql.DB) (string, error) {
	var raw string
	e := db.QueryRow("SELECT value FROM settings WHERE key='webhook'").Scan(&raw)
	if e == sql.ErrNoRows {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	var s notificationStored
	if json.Unmarshal([]byte(raw), &s) != nil {
		return "", errors.New("通知设置不可读")
	}
	return s.HeaderRef, nil
}
