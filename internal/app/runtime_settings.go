package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type settingsChanges struct {
	MaxConcurrent *int    `json:"max_concurrent,omitempty"`
	MaxBody       *int64  `json:"max_body_bytes,omitempty"`
	MaxResponse   *int64  `json:"max_response_bytes,omitempty"`
	MaxEvent      *int    `json:"max_event_bytes,omitempty"`
	IdleTimeout   *int    `json:"idle_timeout_seconds,omitempty"`
	TotalTimeout  *int    `json:"total_timeout_seconds,omitempty"`
	RetentionDays *int    `json:"retention_days,omitempty"`
	Listen        *string `json:"listen,omitempty"`
	DataDir       *string `json:"data_dir,omitempty"`
	HeaderTimeout *int    `json:"header_timeout_seconds,omitempty"`
}
type runtimeSettings struct {
	Version       int             `json:"version"`
	Current       settingsChanges `json:"current"`
	Pending       settingsChanges `json:"pending"`
	RetentionDays *int            `json:"retention_preview_days,omitempty"`
}

func (s *Store) readRuntimeSettings() (runtimeSettings, error) {
	var v runtimeSettings
	v.Version = 1
	var raw string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key='runtime_settings'").Scan(&raw)
	if err == sql.ErrNoRows {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}
func (c settingsChanges) apply(config *Config) {
	if c.MaxConcurrent != nil {
		config.MaxConcurrent = *c.MaxConcurrent
	}
	if c.MaxBody != nil {
		config.MaxBody = *c.MaxBody
	}
	if c.MaxResponse != nil {
		config.MaxResponse = *c.MaxResponse
	}
	if c.MaxEvent != nil {
		config.MaxEvent = *c.MaxEvent
	}
	if c.IdleTimeout != nil {
		config.IdleTimeout = *c.IdleTimeout
	}
	if c.TotalTimeout != nil {
		config.TotalTimeout = *c.TotalTimeout
	}
	if c.RetentionDays != nil {
		config.RetentionDays = *c.RetentionDays
	}
}
func mergeSettings(old, next settingsChanges) settingsChanges {
	data := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(encode(old)), &data)
	var updates map[string]json.RawMessage
	_ = json.Unmarshal([]byte(encode(next)), &updates)
	for k, v := range updates {
		data[k] = v
	}
	var out settingsChanges
	_ = json.Unmarshal([]byte(encode(data)), &out)
	return out
}
func (a *App) settingsAPI(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state, err := a.Store.readRuntimeSettings()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	applied := settingsChanges{}
	pending := state.Pending
	if r.Method == "PATCH" {
		var in struct {
			Version int             `json:"version"`
			Changes settingsChanges `json:"changes"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Version != state.Version {
			fail(w, 409, "设置已变化，请重新读取", "version")
			return
		}
		candidate := a.Config
		in.Changes.apply(&candidate)
		if candidate.MaxConcurrent < 1 || candidate.MaxBody < 1 || candidate.MaxResponse < 1 || candidate.MaxEvent < 1 || candidate.IdleTimeout < 1 || candidate.TotalTimeout < 1 || candidate.RetentionDays < 1 || candidate.RetentionDays > 365 {
			fail(w, 400, "限制与超时必须为正数，保留期为1到365天", "changes")
			return
		}
		if in.Changes.Listen != nil {
			host, port, e := net.SplitHostPort(*in.Changes.Listen)
			n, pe := strconv.Atoi(port)
			ip := net.ParseIP(host)
			if e != nil || pe != nil || ip == nil || !ip.IsLoopback() || n < 1 || n > 65535 {
				fail(w, 400, "listen 必须为 loopback IP 与有效端口", "changes.listen")
				return
			}
			if *in.Changes.Listen != a.Config.Listen {
				probe, e := net.Listen("tcp", *in.Changes.Listen)
				if e != nil {
					fail(w, 409, "新端口无法监听，请解除占用", "changes.listen")
					return
				}
				probe.Close()
			}
			pending.Listen = in.Changes.Listen
		}
		if in.Changes.DataDir != nil {
			if !filepath.IsAbs(*in.Changes.DataDir) {
				fail(w, 400, "新数据目录必须为绝对路径", "changes.data_dir")
				return
			}
			info, e := os.Lstat(*in.Changes.DataDir)
			if e != nil && !os.IsNotExist(e) || e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				fail(w, 409, "新目录不可用", "changes.data_dir")
				return
			}
			pending.DataDir = in.Changes.DataDir
		}
		if in.Changes.HeaderTimeout != nil {
			if *in.Changes.HeaderTimeout < 1 {
				fail(w, 400, "响应头超时必须为正数", "changes.header_timeout_seconds")
				return
			}
			pending.HeaderTimeout = in.Changes.HeaderTimeout
		}
		applied = in.Changes
		applied.Listen = nil
		applied.DataDir = nil
		applied.HeaderTimeout = nil
		applied.RetentionDays = nil
		if in.Changes.RetentionDays != nil {
			state.RetentionDays = in.Changes.RetentionDays
		}
		state.Current = mergeSettings(state.Current, applied)
		state.Pending = pending
		state.Version++
		if _, err = a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('runtime_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(state)); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		applied.apply(&a.Config)
		a.signalAdmission()
		// Replacement happens under the same lock as slot acquisition/release.
		if applied.MaxConcurrent != nil {
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
	if r.Method != "GET" && r.Method != "PATCH" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var preview any
	if state.RetentionDays != nil {
		var eligible int
		var protected sql.NullInt64
		cutoff := time.Now().UTC().Add(-time.Duration(*state.RetentionDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		if err = a.Store.DB.QueryRow("SELECT count(*),sum(CASE WHEN status IN ('queued','admitted','dispatching','streaming') OR EXISTS(SELECT 1 FROM reservations r WHERE r.request_id=requests.id AND (r.period_end>? OR r.status!='settled')) THEN 1 ELSE 0 END) FROM requests WHERE started<?", time.Now().UTC().Format(time.RFC3339Nano), cutoff).Scan(&eligible, &protected); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		preview = map[string]any{"days": *state.RetentionDays, "candidate_requests": eligible - int(protected.Int64), "protected_requests": int(protected.Int64)}
	}
	writeJSON(w, 200, map[string]any{"version": state.Version, "retention_days": a.Config.RetentionDays, "limits": a.Config, "applied_now": applied, "restart_required": pending, "rejected": []string{}, "retention_preview": preview, "backup": "运维页可创建一致元数据备份并准备全新恢复目录。"})
}

func (a *App) retentionCleanupAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Version int `json:"version"`
		Days    int `json:"days"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	state, err := a.Store.readRuntimeSettings()
	if err != nil {
		a.mu.Unlock()
		fail(w, 503, storageError().Error(), "")
		return
	}
	if state.Version != in.Version || state.RetentionDays == nil || *state.RetentionDays != in.Days {
		a.mu.Unlock()
		fail(w, 409, "清理预览已变化，请重新读取设置", "version")
		return
	}
	state.Current.RetentionDays = state.RetentionDays
	state.RetentionDays = nil
	state.Version++
	if _, err = a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('runtime_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", encode(state)); err != nil {
		a.mu.Unlock()
		fail(w, 503, storageError().Error(), "")
		return
	}
	a.Config.RetentionDays = in.Days
	a.mu.Unlock()
	a.startLocalOperation(w, r, "retention_cleanup", func(ctx context.Context, folder string) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := a.Store.cleanup(in.Days)
		return map[string]any{"retention_days": in.Days, "completed": err == nil}, err
	})
}

func loadSavedRuntimeConfig(c *Config) error {
	dir := c.DataDir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(base, "gatt")
	}
	path := filepath.Join(dir, "gatt.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro&_busy_timeout=1000")
	if err != nil {
		return err
	}
	defer db.Close()
	var raw string
	err = db.QueryRow("SELECT value FROM settings WHERE key='runtime_settings'").Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var state runtimeSettings
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return err
	}
	state.Current.apply(c)
	if state.Pending.Listen != nil {
		c.Listen = *state.Pending.Listen
	}
	if state.Pending.DataDir != nil {
		c.DataDir = *state.Pending.DataDir
	}
	if state.Pending.HeaderTimeout != nil {
		c.HeaderTimeout = *state.Pending.HeaderTimeout
	}
	return nil
}
