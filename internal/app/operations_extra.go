package app

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-sqlite3"
)

const backupArchiveLimit int64 = 1 << 30
const backupExpandedLimit int64 = 2 << 30
const operationArtifactTTL = 24 * time.Hour

// operationsExtraAPI runs only after the normal management-session and Origin checks.
func (a *App) operationsExtraAPI(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.URL.Path == "/admin/backups":
		if r.Method != "POST" {
			fail(w, 405, "方法不支持", "")
			return true
		}
		var in struct {
			Mode       string `json:"mode"`
			Encrypt    bool   `json:"encrypt"`
			Passphrase string `json:"passphrase"`
		}
		if !decode(w, r, &in) {
			return true
		}
		if in.Mode == "" {
			in.Mode = "metadata"
		}
		if in.Mode != "metadata" && in.Mode != "full" || in.Mode == "full" && (!in.Encrypt || in.Passphrase == "") || in.Encrypt && in.Passphrase == "" {
			fail(w, 422, "完整备份必须使用口令加密，元数据加密也需非空口令", "mode")
			return true
		}
		req := r.Clone(r.Context())
		req.Header = r.Header.Clone()
		if in.Encrypt {
			req.Header.Set("X-Cove-Backup-Archive", in.Mode)
		}
		a.startLocalOperation(w, req, "backup", func(ctx context.Context, folder string) (any, error) {
			manifest, err := a.backupArchive(ctx, filepath.Join(folder, "artifact"), in.Mode, in.Encrypt, in.Passphrase)
			in.Passphrase = ""
			if err != nil {
				return nil, err
			}
			return backupManifestSummary(manifest), nil
		})
	case strings.HasPrefix(r.URL.Path, "/admin/artifacts/"):
		a.artifactAPI(w, r)
	case r.URL.Path == "/admin/restore-preview":
		a.restorePreviewAPI(w, r)
	case r.URL.Path == "/admin/restore-apply":
		a.restoreApplyAPI(w, r)
	case r.URL.Path == "/admin/doctor":
		if r.Method != "GET" {
			fail(w, 405, "方法不支持", "")
		} else {
			a.doctorAPI(w, r)
		}
	case r.URL.Path == "/admin/metrics":
		if r.Method != "GET" {
			fail(w, 405, "方法不支持", "")
		} else {
			a.metricsAPI(w, r)
		}
	case r.URL.Path == "/admin/alerts" || strings.HasPrefix(r.URL.Path, "/admin/alerts/"):
		a.localAlertsAPI(w, r)
	case r.URL.Path == "/admin/exports":
		a.exportsAPI(w, r)
	case r.URL.Path == "/admin/config-transfer/export":
		a.configExportAPI(w, r)
	case r.URL.Path == "/admin/config-transfer/import-preview":
		a.configImportPreviewAPI(w, r)
	case r.URL.Path == "/admin/config-transfer/import-apply":
		a.configImportApplyAPI(w, r)
	default:
		return false
	}
	return true
}

type backupFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type backupManifest struct {
	FormatVersion  int          `json:"format_version"`
	CreatedAt      time.Time    `json:"created_at"`
	AppBuild       string       `json:"app_build"`
	SchemaVersion  int          `json:"schema_version"`
	Mode           string       `json:"mode"`
	SecretIncluded bool         `json:"secret_included"`
	Files          []backupFile `json:"files"`
}
type localArtifact struct {
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"created_at"`
	ContentType string    `json:"content_type"`
	Filename    string    `json:"filename"`
}

func validLocalID(v string) bool {
	if len(v) < 5 || len(v) > 80 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
func (a *App) localRoot() (string, error) {
	if a.Config.DataDir == "" {
		return "", errors.New("数据目录未配置")
	}
	root := filepath.Join(a.Config.DataDir, ".operations")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", storageError()
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", storageError()
	}
	if err = protectAppPath(root, 0700); err != nil {
		return "", storageError()
	}
	return root, nil
}
func writePrivateJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
func (a *App) newLocalArtifact(owner, kind, ct, name string) (localArtifact, string, error) {
	root, err := a.localRoot()
	if err != nil {
		return localArtifact{}, "", err
	}
	descriptor := localArtifact{ID: id("artifact"), Owner: digest(owner), Kind: kind, CreatedAt: time.Now().UTC(), ContentType: ct, Filename: name}
	folder := filepath.Join(root, descriptor.ID)
	if err = os.Mkdir(folder, 0700); err != nil {
		return descriptor, "", storageError()
	}
	if err = writePrivateJSON(filepath.Join(folder, "descriptor.json"), descriptor); err != nil {
		os.RemoveAll(folder)
		return descriptor, "", storageError()
	}
	return descriptor, folder, nil
}
func (a *App) readLocalArtifact(aid, owner string) (localArtifact, string, error) {
	if !validLocalID(aid) {
		return localArtifact{}, "", sql.ErrNoRows
	}
	root, err := a.localRoot()
	if err != nil {
		return localArtifact{}, "", err
	}
	folder := filepath.Join(root, aid)
	info, err := os.Lstat(folder)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return localArtifact{}, "", sql.ErrNoRows
	}
	b, err := os.ReadFile(filepath.Join(folder, "descriptor.json"))
	if err != nil {
		return localArtifact{}, "", sql.ErrNoRows
	}
	var d localArtifact
	if json.Unmarshal(b, &d) != nil || d.ID != aid || d.Owner != digest(owner) {
		return d, "", sql.ErrNoRows
	}
	if time.Since(d.CreatedAt) > operationArtifactTTL {
		if err = os.RemoveAll(folder); err != nil {
			return d, "", storageError()
		}
		return d, "", sql.ErrNoRows
	}
	return d, folder, nil
}
func (a *App) artifactAPI(w http.ResponseWriter, r *http.Request) {
	aid := strings.TrimPrefix(r.URL.Path, "/admin/artifacts/")
	d, folder, err := a.readLocalArtifact(aid, bearer(r))
	if err != nil {
		fail(w, 404, "下载文件不存在或已过期", "")
		return
	}
	if r.Method == "DELETE" {
		if err = os.RemoveAll(folder); err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "方法不支持", "")
		return
	}
	if d.Kind == "restore" || d.Kind == "config_preview" {
		fail(w, 404, "此预览不是下载文件", "")
		return
	}
	path := filepath.Join(folder, "artifact")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		fail(w, 404, "下载文件尚未完成或已删除", "")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", d.Filename))
	http.ServeContent(w, r, d.Filename, info.ModTime(), f)
}
func (a *App) startLocalOperation(w http.ResponseWriter, r *http.Request, kind string, run func(context.Context, string) (any, error)) {
	ct, name := "application/x-tar", "cove-metadata.tar"
	if kind == "backup" && r.Header.Get("X-Cove-Backup-Archive") != "" {
		ct = "application/age"
		name = "cove-" + r.Header.Get("X-Cove-Backup-Archive") + ".tar.age"
	}
	if kind == "export_csv" {
		ct, name = "text/csv; charset=utf-8", "cove-requests.csv"
	}
	if kind == "export_json" {
		ct, name = "application/json", "cove-requests.json"
	}
	d, folder, err := a.newLocalArtifact(bearer(r), kind, ct, name)
	if err != nil {
		fail(w, 503, err.Error(), "")
		return
	}
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		os.RemoveAll(folder)
		fail(w, 503, "服务正在关闭", "")
		return
	}
	op := Operation{ID: id("op"), Kind: kind, ObjectID: d.ID, State: "running", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err = a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES(?,?)", op.ID, encode(op)); err != nil {
		a.mu.Unlock()
		os.RemoveAll(folder)
		fail(w, 503, storageError().Error(), "")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	var release func()
	if kind != "backup" {
		release, err = a.beginBackupOwnerLocked()
		if err != nil {
			cancel()
			a.mu.Unlock()
			os.RemoveAll(folder)
			fail(w, 503, err.Error(), "")
			return
		}
	}
	a.operationCancels[op.ID] = cancel
	a.ownedTasks.Add(1)
	response := op
	a.mu.Unlock()
	go func() {
		defer a.ownedTasks.Done()
		defer cancel()
		if release != nil {
			defer release()
		}
		result, e := run(ctx, folder)
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.operationCancels, op.ID)
		op.State = "succeeded"
		op.Version++
		op.UpdatedAt = time.Now().UTC()
		if e != nil {
			op.State = "failed"
			op.Error = e.Error()
			if errors.Is(e, context.Canceled) {
				op.State = "cancelled"
				op.Error = "操作已取消"
			}
			os.RemoveAll(folder)
		} else {
			if kind == "model_verification" {
				op.Result = result
				os.RemoveAll(folder)
			} else {
				op.Result = map[string]any{"artifact_id": d.ID, "expires_at": d.CreatedAt.Add(operationArtifactTTL), "summary": result, "filename": name}
			}
		}
		if _, e = a.Store.DB.Exec("UPDATE operations SET data=? WHERE id=?", encode(op), op.ID); e != nil {
			a.markStorageFailure()
			os.RemoveAll(folder)
		}
	}()
	writeJSON(w, 202, response)
}

// sqliteSnapshot uses SQLite's online backup API, including the committed WAL state.
func sqliteSnapshot(ctx context.Context, from *sql.DB, toPath string) error {
	dest, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(toPath)+"?_foreign_keys=on&_journal_mode=DELETE&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer dest.Close()
	sourceConn, err := from.Conn(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Close()
	targetConn, err := dest.Conn(ctx)
	if err != nil {
		return err
	}
	defer targetConn.Close()
	err = sourceConn.Raw(func(src any) error {
		return targetConn.Raw(func(dst any) error {
			source, ok := src.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("SQLite 驱动不支持一致快照")
			}
			target, ok := dst.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("SQLite 驱动不支持一致快照")
			}
			backup, err := target.Backup("main", source, "main")
			if err != nil {
				return err
			}
			for {
				if e := ctx.Err(); e != nil {
					backup.Close()
					return e
				}
				done, e := backup.Step(128)
				if e != nil {
					backup.Close()
					return e
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					backup.Close()
					return ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
		})
	})
	if err == nil {
		err = os.Chmod(toPath, 0600)
	}
	return err
}
func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}
func (a *App) metadataBackup(ctx context.Context, path string) (backupManifest, error) {
	work, err := os.MkdirTemp(filepath.Dir(path), "snapshot-")
	if err != nil {
		return backupManifest{}, storageError()
	}
	defer os.RemoveAll(work)
	dbPath := filepath.Join(work, "db.sqlite")
	if err = sqliteSnapshot(ctx, a.Store.DB, dbPath); err != nil {
		if ctx.Err() != nil {
			return backupManifest{}, ctx.Err()
		}
		return backupManifest{}, errors.New("一致数据库快照失败")
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(dbPath)+"?_foreign_keys=on&_journal_mode=DELETE")
	if err != nil {
		return backupManifest{}, storageError()
	}
	if err = sanitizeMetadata(db); err != nil {
		db.Close()
		return backupManifest{}, err
	}
	var schema int
	err = db.QueryRow("PRAGMA user_version").Scan(&schema)
	if err == nil {
		err = checkBackupDB(db, schema)
	}
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		return backupManifest{}, errors.New("元数据快照校验失败")
	}
	n, h, err := hashFile(dbPath)
	if err != nil || n > backupArchiveLimit-(1<<20) {
		return backupManifest{}, errors.New("备份超过 1 GiB 上限")
	}
	manifest := backupManifest{FormatVersion: 1, CreatedAt: time.Now().UTC(), AppBuild: BuildID, SchemaVersion: schema, Mode: "metadata", Files: []backupFile{{Path: "db.sqlite", Bytes: n, SHA256: h}}}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return manifest, storageError()
	}
	tw := tar.NewWriter(f)
	b, _ := json.Marshal(manifest)
	err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg})
	if err == nil {
		_, err = tw.Write(b)
	}
	if err == nil {
		err = tw.WriteHeader(&tar.Header{Name: "db.sqlite", Mode: 0600, Size: n, Typeflag: tar.TypeReg})
	}
	if err == nil {
		var dbf *os.File
		dbf, err = os.Open(dbPath)
		if err == nil {
			_, err = io.Copy(tw, dbf)
			dbf.Close()
		}
	}
	if e := tw.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(path)
		return manifest, storageError()
	}
	return manifest, nil
}

// Typed rewrites remove all non-schema JSON fields (including any saved body), not only known secret names.
func sanitizeMetadata(db *sql.DB) error {
	var schemaObjects int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type IN ('trigger','view') OR upper(sql) LIKE 'CREATE VIRTUAL TABLE%'").Scan(&schemaObjects); err != nil || schemaObjects != 0 {
		return errors.New("备份包含未审核的触发器、视图或虚拟表；原数据保留")
	}
	known := map[string]bool{"accounts": true, "sources": true, "source_models": true, "routes": true, "model_aliases": true, "client_keys": true, "requests": true, "attempts": true, "bindings": true, "settings": true, "operations": true, "client_changes": true, "client_previews": true, "budgets": true, "reservations": true, "accounting_audit": true, "prices": true, "response_cache": true, "alerts": true, "alert_deliveries": true, "gemini_call_bindings": true, "gemini_history_bindings": true, "resources": true, "jobs": true, "job_items": true, "quota_snapshots": true}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !known[name] {
			rows.Close()
			return errors.New("当前 schema 包含尚未审核的备份表；备份已停止，原数据保留")
		}
		tables[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rewrite := func(table string, edit func(string) (string, error)) error {
		if !tables[table] {
			return nil
		}
		rs, e := tx.Query("SELECT rowid,data FROM " + table)
		if e != nil {
			return e
		}
		type entry struct {
			row  int64
			data string
		}
		var values []entry
		for rs.Next() {
			var v entry
			if e = rs.Scan(&v.row, &v.data); e != nil {
				rs.Close()
				return e
			}
			values = append(values, v)
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			return e
		}
		for _, v := range values {
			b, e := edit(v.data)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("UPDATE "+table+" SET data=? WHERE rowid=?", b, v.row); e != nil {
				return e
			}
		}
		return nil
	}
	edits := map[string]func(string) (string, error){
		"prices": func(b string) (string, error) {
			var v PriceVersion
			if e := json.Unmarshal([]byte(b), &v); e != nil {
				return "", e
			}
			v.Provenance.URL = ""
			return encode(v), nil
		},
		"accounts": func(b string) (string, error) {
			var v Account
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			v.Name = ""
			v.Identity = map[string]any{"verified": false}
			v.AuthState = "needs_reauth"
			v.CredentialPresent = false
			return encode(v), nil
		},
		"sources": func(b string) (string, error) {
			var v Source
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			if validateURL(v.BaseURL) != nil {
				return "", errors.New("来源 endpoint 无法安全备份")
			}
			v.Configured = false
			v.AuthStatus = "needs_reauth"
			v.Continuation = false
			v.Quota = map[string]any{"status": "unknown"}
			v.Verification = Verification{Status: "untested", Capabilities: []string{}}
			return encode(v), nil
		},
		"client_keys": func(b string) (string, error) {
			var v ClientKey
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			v.Revoked = true
			v.Fingerprint = ""
			v.Name = ""
			return encode(v), nil
		},
		"source_models": func(b string) (string, error) {
			var v SourceModel
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			v.Verification = "unverified"
			v.VerificationResults = nil
			v.CodexCatalog = nil
			return encode(v), nil
		},
		"routes": func(b string) (string, error) {
			var v Route
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			return encode(v), nil
		},
		"model_aliases": func(b string) (string, error) {
			var v Alias
			if err := json.Unmarshal([]byte(b), &v); err != nil {
				return "", err
			}
			return encode(v), nil
		},
		"requests": sanitizeRecordJSON, "attempts": sanitizeRecordJSON,
	}
	for _, table := range []string{"accounts", "sources", "source_models", "routes", "model_aliases", "client_keys", "requests", "attempts"} {
		if err = rewrite(table, edits[table]); err != nil {
			return err
		}
	}
	if err = rewrite("budgets", sanitizeBackupBudget); err != nil {
		return err
	}
	if err = rewrite("accounting_audit", sanitizeBackupAccountingAudit); err != nil {
		return err
	}
	if tables["reservations"] {
		rs, e := tx.Query("SELECT rowid,allocation_json FROM reservations")
		if e != nil {
			return e
		}
		type allocationRow struct {
			row  int64
			data string
		}
		var values []allocationRow
		for rs.Next() {
			var v allocationRow
			if e = rs.Scan(&v.row, &v.data); e != nil {
				rs.Close()
				return e
			}
			values = append(values, v)
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			return e
		}
		for _, v := range values {
			data, e := sanitizeBackupAllocations([]byte(v.data))
			if e != nil {
				return e
			}
			if _, e = tx.Exec("UPDATE reservations SET allocation_json=? WHERE rowid=?", string(data), v.row); e != nil {
				return e
			}
		}
	}
	for _, query := range []string{"UPDATE accounts SET credential_ref=''", "UPDATE client_keys SET digest='archived_'||lower(hex(randomblob(32)))", "UPDATE requests SET status=json_extract(data,'$.status')", "DELETE FROM bindings", "DELETE FROM operations", "DELETE FROM settings WHERE key NOT IN ('retention_days')"} {
		if _, err = tx.Exec(query); err != nil {
			return err
		}
	}
	// These tables contain private snapshots, output bodies, resource ownership or pending work; none is portable authentication metadata.
	for _, table := range []string{"quota_snapshots", "alert_deliveries", "alerts", "job_items", "jobs", "resources", "client_changes", "client_previews", "response_cache", "gemini_call_bindings", "gemini_history_bindings"} {
		if tables[table] {
			if _, err = tx.Exec("DELETE FROM " + table); err != nil {
				return errors.New("私有数据不能安全移除；备份已停止")
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Rebuild the isolated database so deleted secrets are absent from free pages and the WAL.
	if _, err = db.Exec("PRAGMA secure_delete=ON; VACUUM"); err != nil {
		return err
	}
	return nil
}
func sanitizeRecordJSON(b string) (string, error) {
	var v Record
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		return "", err
	}
	v.ClientName = ""
	v.Fingerprint = ""
	v.SourceName = ""
	v.ResponseID = ""
	v.UpstreamRequestID = ""
	v.ErrorSummary = ""
	v.SelectionReasons = nil
	v.Adjustments = nil
	if v.Status == "admitted" || v.Status == "dispatching" || v.Status == "streaming" {
		v.Status = "interrupted"
		v.ErrorStage = "restore"
	}
	return encode(v), nil
}
func checkBackupDB(db *sql.DB, schema int) error {
	if schema != 2 {
		return errors.New("备份 schema 版本不受支持；需要 v2")
	}
	var actual int
	if err := db.QueryRow("PRAGMA user_version").Scan(&actual); err != nil || actual != schema {
		return errors.New("备份 schema 与 manifest 不符")
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("SQLite 完整性校验失败")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite 外键校验失败")
	}
	return rows.Err()
}

type restorePreview struct {
	ID                 string           `json:"preview_id"`
	Manifest           backupManifest   `json:"manifest"`
	TargetDir          string           `json:"target_dir"`
	TargetHash         string           `json:"target_hash"`
	Counts             map[string]int64 `json:"counts"`
	MissingCredentials int64            `json:"missing_credentials"`
	ExpiresAt          time.Time        `json:"expires_at"`
}

func targetDirectoryHash(target, current string) (string, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", errors.New("请选择绝对路径的全新目标目录")
	}
	currentAbs, err := filepath.Abs(current)
	if err != nil {
		return "", storageError()
	}
	rel, err := filepath.Rel(currentAbs, target)
	if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("恢复目录必须位于当前数据目录之外")
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		return "", errors.New("目标目录已存在；请选择尚未创建的新目录")
	}
	parent := filepath.Dir(target)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return "", errors.New("目标父目录必须存在且不能含符号链接")
	}
	identity, err := appDirectoryIdentity(parent)
	if err != nil {
		return "", errors.New("目标父目录不可用")
	}
	return digest(target + "\x00" + identity), nil
}
func unpackMetadataArchive(ctx context.Context, reader io.Reader, folder string) (backupManifest, error) {
	limited := &io.LimitedReader{R: reader, N: backupArchiveLimit + 1}
	tr := tar.NewReader(limited)
	var manifest backupManifest
	seen := map[string]bool{}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifest, errors.New("备份归档损坏或不是受支持的 tar 格式")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return manifest, errors.New("备份禁止目录、符号链接、设备及其他特殊文件")
		}
		if h.Name != "manifest.json" && h.Name != "db.sqlite" || seen[h.Name] || h.Size < 0 {
			return manifest, errors.New("备份文件不在白名单或路径无效")
		}
		seen[h.Name] = true
		total += h.Size
		if total > backupExpandedLimit || h.Size > backupArchiveLimit {
			return manifest, errors.New("备份超过体积上限")
		}
		if h.Name == "manifest.json" {
			if h.Size > 1<<20 {
				return manifest, errors.New("备份 manifest 超过上限")
			}
			b, err := io.ReadAll(io.LimitReader(tr, h.Size))
			if err != nil || json.Unmarshal(b, &manifest) != nil {
				return manifest, errors.New("备份 manifest 无效")
			}
			continue
		}
		f, err := os.OpenFile(filepath.Join(folder, h.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return manifest, storageError()
		}
		_, err = io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return manifest, errors.New("备份解包失败")
		}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N == 0 {
		return manifest, errors.New("备份上传超过 1 GiB 或无法完整读取")
	}
	if !seen["manifest.json"] || !seen["db.sqlite"] || manifest.FormatVersion != 1 || manifest.Mode != "metadata" || manifest.SecretIncluded || manifest.SchemaVersion != 2 || len(manifest.Files) != 1 {
		return manifest, errors.New("备份格式、模式或 schema 版本不受支持；仅接受 v1/v2 无凭据元数据")
	}
	file := manifest.Files[0]
	n, h, err := hashFile(filepath.Join(folder, "db.sqlite"))
	if err != nil || file.Path != "db.sqlite" || file.Bytes != n || file.SHA256 != h {
		return manifest, errors.New("备份文件 checksum 或长度校验失败")
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(folder, "db.sqlite"))+"?_foreign_keys=on&_journal_mode=DELETE")
	if err != nil {
		return manifest, errors.New("备份数据库不可读")
	}
	defer db.Close()
	if err = checkBackupDB(db, manifest.SchemaVersion); err != nil {
		return manifest, err
	}
	// Uploaded metadata is untrusted: strip secret refs and invalidate all old client credentials again.
	if err = sanitizeMetadata(db); err != nil {
		return manifest, err
	}
	if err = checkBackupDB(db, manifest.SchemaVersion); err != nil {
		return manifest, err
	}
	return manifest, nil
}
func (a *App) restorePreviewAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	// Query carries the explicitly selected directory; the upload body is streamed directly, never buffered as JSON/base64.
	target := r.URL.Query().Get("target_dir")
	targetHash, err := targetDirectoryHash(target, a.Config.DataDir)
	if err != nil {
		fail(w, 422, err.Error(), "target_dir")
		return
	}
	if r.Header.Get("Content-Type") != "application/x-tar" && r.Header.Get("Content-Type") != "application/age" {
		fail(w, 422, "上传需为 application/x-tar 元数据备份；加密或完整恢复尚未启用", "file")
		return
	}
	d, folder, err := a.newLocalArtifact(bearer(r), "restore", "", "")
	if err != nil {
		fail(w, 503, err.Error(), "")
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(folder)
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, backupArchiveLimit)
	manifest, err := unpackBackupArchive(r.Context(), r.Body, folder, r.Header.Get("X-Cove-Backup-Passphrase"), r.Header.Get("Content-Type") == "application/age")
	if err != nil {
		fail(w, 422, err.Error(), "file")
		return
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(folder, "db.sqlite"))+"?mode=ro")
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer db.Close()
	preview := restorePreview{ID: d.ID, Manifest: manifest, TargetDir: target, TargetHash: targetHash, Counts: map[string]int64{}, ExpiresAt: d.CreatedAt.Add(operationArtifactTTL)}
	for _, table := range []string{"sources", "accounts", "source_models", "client_keys", "requests"} {
		var count int64
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			fail(w, 422, "备份数据表缺失或不可读", "file")
			return
		}
		preview.Counts[table] = count
	}
	if manifest.Mode != "full" {
		preview.MissingCredentials = preview.Counts["accounts"]
	}
	if err = writePrivateJSON(filepath.Join(folder, "preview.json"), preview); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	keep = true
	writeJSON(w, 200, preview)
}
func copyPrivateFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return storageError()
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	return err
}
func (a *App) restoreApplyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		PreviewID   string `json:"preview_id"`
		TargetHash  string `json:"target_hash"`
		PrepareOnly bool   `json:"prepare_only"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.PrepareOnly {
		fail(w, 422, "此运行版本只能准备全新恢复目录；自动停止、切换与回退需要服务启动器支持", "prepare_only")
		return
	}
	_, folder, err := a.readLocalArtifact(in.PreviewID, bearer(r))
	if err != nil {
		fail(w, 404, "恢复预览不存在或已过期", "")
		return
	}
	b, err := os.ReadFile(filepath.Join(folder, "preview.json"))
	var p restorePreview
	if err != nil || json.Unmarshal(b, &p) != nil {
		fail(w, 404, "恢复预览不可读", "")
		return
	}
	hash, err := targetDirectoryHash(p.TargetDir, a.Config.DataDir)
	if err != nil || hash != in.TargetHash || hash != p.TargetHash {
		fail(w, 409, "目标目录或父目录已改变；请重新预览", "target_hash")
		return
	}
	if err = os.Mkdir(p.TargetDir, 0700); err != nil {
		fail(w, 503, "无法创建目标目录；原目录保留", "target_dir")
		return
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(p.TargetDir)
		}
	}()
	if err = protectAppPath(p.TargetDir, 0700); err != nil {
		fail(w, 503, "无法设置目标目录私有权限；原目录保留", "target_dir")
		return
	}
	if err = copyPrivateFile(filepath.Join(folder, "db.sqlite"), filepath.Join(p.TargetDir, "gatt.db")); err != nil {
		fail(w, 503, "恢复复制失败；原目录保留", "")
		return
	}
	if err = restoreBackupSecrets(folder, p.TargetDir, p.Manifest); err != nil {
		fail(w, 503, "恢复凭据失败；原目录保留", "")
		return
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(p.TargetDir, "gatt.db"))+"?mode=ro&_foreign_keys=on")
	if err == nil {
		err = checkBackupDB(db, p.Manifest.SchemaVersion)
		db.Close()
	}
	if err != nil {
		fail(w, 422, "目标数据库校验失败；原目录保留", "")
		return
	}
	a.mu.Lock()
	c := a.Config
	a.mu.Unlock()
	c.DataDir = p.TargetDir
	if err = writePrivateJSON(filepath.Join(p.TargetDir, "config.json"), c); err != nil {
		fail(w, 503, "恢复配置写入失败；原目录保留", "")
		return
	}
	complete = true
	// The chosen new data directory is ready; no runtime pointer or existing credential store changes.
	if err = os.RemoveAll(folder); err != nil {
		fail(w, 503, "恢复目录已准备，但预览清理失败；当前服务仍使用原目录", "")
		return
	}
	writeJSON(w, 200, map[string]any{"state": "prepared", "target_dir": p.TargetDir, "switched": false, "requires_restart": true, "missing_credentials": p.MissingCredentials, "message": "新目录已校验。停止旧服务后，用新目录中的 config.json 启动。完整备份保留凭据，元数据备份需要重新配置账号与 Key。"})
}

func (a *App) doctorAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	storageOK := a.Store.DB.PingContext(ctx) == nil && !a.storageFailed.Load()
	credentialOK := a.Secrets.Health() == nil
	a.mu.Lock()
	active, closing := len(a.running), a.stopping
	a.mu.Unlock()
	counts := map[string]int64{}
	routeOK := false
	if storageOK {
		for _, table := range []string{"sources", "accounts", "source_models", "routes"} {
			var n int64
			if err := a.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				storageOK = false
				break
			}
			counts[table] = n
		}
		routeOK = counts["sources"] > 0 && counts["source_models"] > 0
	}
	writeJSON(w, 200, map[string]any{"version": Version, "build_id": BuildID, "created_at": time.Now().UTC(), "storage_healthy": storageOK, "credential_store_healthy": credentialOK, "admission_open": !closing, "active_requests": active, "configuration_counts": counts, "route_configured": routeOK, "checks": []map[string]any{{"kind": "listener", "status": "serving", "message": "当前管理接口可达；未探测其他端口"}, {"kind": "storage", "healthy": storageOK, "message": "检查数据库访问与已知写入故障；失败时检查磁盘权限与剩余空间"}, {"kind": "credentials", "healthy": credentialOK, "message": "仅检查凭据存储可用性；账号认证状态需查看来源页"}, {"kind": "routing", "healthy": routeOK, "message": "来源和模型数量只证明配置存在；不会发起模型调用"}}, "excluded": []string{"credentials", "tokens", "identity", "paths", "endpoints", "prompts", "outputs"}})
}

// Metrics are reconstructed from unique persisted request/attempt rows. Upserts and duplicate terminals cannot increment counters twice.
func metricLabel(v string, allowed ...string) string {
	for _, known := range allowed {
		if v == known {
			return known
		}
	}
	return "other"
}
func (a *App) operationsStatus() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observations.mu.Lock()
	enabled := a.observations.settings.Enabled
	a.observations.mu.Unlock()
	return map[string]any{"active_requests": len(a.running), "storage_fault": a.storageFailed.Load(), "telemetry_enabled": enabled, "webhook_enabled": false}
}
func (a *App) metricsAPI(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Store.DB.QueryContext(r.Context(), "SELECT data FROM requests WHERE status NOT IN ('admitted','dispatching','streaming')")
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	requests := map[string]int64{}
	buckets := []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120}
	duration := make([]int64, len(buckets))
	ttft := make([]int64, len(buckets))
	var durationCount, ttftCount int64
	var durationSum, ttftSum float64
	for rows.Next() {
		var b string
		var v Record
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			break
		}
		protocol := metricLabel(v.Protocol, "responses", "chat_completions", "messages", "gemini", "realtime_websocket")
		outcome := metricLabel(v.Status, "succeeded", "unverified", "completed", "failed", "cancelled", "interrupted", "rejected", "partial")
		requests[protocol+"\x00"+outcome]++
		if v.Ended != nil && v.DurationMS >= 0 {
			seconds := float64(v.DurationMS) / 1000
			durationCount++
			durationSum += seconds
			for i, limit := range buckets {
				if seconds <= limit {
					duration[i]++
				}
			}
		}
		// FirstEvent currently means transport/provider event, not necessarily semantic content. Do not fabricate TTFT from it.
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	attempts := map[string]int64{}
	rows, err = a.Store.DB.QueryContext(r.Context(), "SELECT a.data,s.data FROM attempts a JOIN sources s ON s.id=a.source_id")
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	for rows.Next() {
		var b, s string
		var v Record
		var source Source
		if err = rows.Scan(&b, &s); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(s), &source); err != nil {
			break
		}
		if v.Status == "admitted" || v.Status == "dispatching" || v.Status == "streaming" {
			continue
		}
		provider := metricLabel(source.Provider, "openai", "openai_compatible", "codex", "anthropic", "gemini", "azure", "bedrock", "vertex", "local")
		outcome := metricLabel(v.Status, "succeeded", "unverified", "completed", "failed", "cancelled", "interrupted", "rejected", "partial")
		attempts[provider+"\x00"+outcome]++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	var out strings.Builder
	out.WriteString("# HELP cove_requests_total Unique completed request records retained locally; history can expire.\n# TYPE cove_requests_total gauge\n")
	keys := func(values map[string]int64) []string {
		var ks []string
		for k := range values {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	for _, k := range keys(requests) {
		p := strings.Split(k, "\x00")
		fmt.Fprintf(&out, "cove_requests_total{protocol=%q,operation=\"inference\",outcome=%q} %d\n", p[0], p[1], requests[k])
	}
	out.WriteString("# TYPE cove_attempts_total gauge\n")
	for _, k := range keys(attempts) {
		p := strings.Split(k, "\x00")
		fmt.Fprintf(&out, "cove_attempts_total{provider=%q,outcome=%q} %d\n", p[0], p[1], attempts[k])
	}
	for _, h := range []struct {
		name   string
		counts []int64
		count  int64
		sum    float64
	}{{"request_duration_seconds", duration, durationCount, durationSum}, {"ttft_seconds", ttft, ttftCount, ttftSum}} {
		fmt.Fprintf(&out, "# TYPE cove_%s histogram\n", h.name)
		for i, limit := range buckets {
			fmt.Fprintf(&out, "cove_%s_bucket{le=%q} %d\n", h.name, strconv.FormatFloat(limit, 'f', -1, 64), h.counts[i])
		}
		fmt.Fprintf(&out, "cove_%s_bucket{le=\"+Inf\"} %d\ncove_%s_count %d\ncove_%s_sum %g\n", h.name, h.count, h.name, h.count, h.name, h.sum)
	}
	status := a.operationsStatus()
	fmt.Fprintf(&out, "# TYPE cove_active_requests gauge\ncove_active_requests %d\n", status["active_requests"])
	if a.storageFailed.Load() {
		out.WriteString("cove_storage_fault 1\n")
	} else {
		out.WriteString("cove_storage_fault 0\n")
	}
	out.WriteString("# These metrics describe retained database history. Unknown TTFT and unavailable byte/queue/error counters are omitted, never fabricated.\n")
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	io.WriteString(w, out.String())
}

type localAlert struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	EntityID    string     `json:"entity_id"`
	Generation  int        `json:"generation"`
	Period      string     `json:"period"`
	State       string     `json:"state"`
	Summary     string     `json:"summary"`
	Count       int        `json:"count"`
	Version     int        `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeen    time.Time  `json:"last_seen"`
	ResolvedAt  *time.Time `json:"resolved_at"`
	DismissedAt *time.Time `json:"dismissed_at"`
}

func (a *App) reconcileLocalAlerts(ctx context.Context) ([]localAlert, error) {
	return a.reconcileNotifications(ctx, time.Now().UTC())
}

func (a *App) localAlertsAPI(w http.ResponseWriter, r *http.Request) { a.notificationsAlertsAPI(w, r) }

var requestExportColumns = []string{"request_id", "started_at", "protocol", "status", "origin", "duration_ms", "http_status", "input_tokens", "output_tokens", "cached_tokens", "reasoning_tokens", "usage_completeness", "currency", "estimated_cost"}

func exportQuery(filters map[string]string) (string, []any, error) {
	query := " FROM requests WHERE 1=1"
	args := []any{}
	expressions := map[string]string{"source_id": "source_id", "status": "status", "client_key_id": "json_extract(data,'$.client_key_id')", "origin": "json_extract(data,'$.origin')", "from": "started>=?", "to": "started<=?"}
	var keys []string
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := filters[k]
		expr, ok := expressions[k]
		if !ok {
			return "", nil, errors.New("导出过滤字段不受支持")
		}
		if v == "" {
			continue
		}
		if k == "from" || k == "to" {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return "", nil, errors.New("导出时间必须为 RFC3339")
			}
			query += " AND " + expr
		} else {
			query += " AND " + expr + "=?"
		}
		args = append(args, v)
	}
	return query, args, nil
}
func csvSafeText(v string) string {
	if v == "" {
		return v
	}
	first := []rune(v)[0]
	trimmed := strings.TrimLeftFunc(v, unicode.IsSpace)
	if unicode.IsControl(first) || trimmed != "" && strings.ContainsRune("=+-@", []rune(trimmed)[0]) {
		return "'" + v
	}
	return v
}
func exportRecord(v Record) map[string]any {
	out := map[string]any{"request_id": v.ID, "started_at": v.Started.Format(time.RFC3339Nano), "protocol": metricLabel(v.Protocol, "responses", "chat_completions", "messages", "gemini", "realtime_websocket"), "status": metricLabel(v.Status, "succeeded", "unverified", "completed", "failed", "cancelled", "interrupted", "rejected", "partial", "admitted", "dispatching", "streaming"), "origin": metricLabel(v.Origin, "gateway", "client", "admin_test", "verification", "provider_vad"), "duration_ms": v.DurationMS, "http_status": v.HTTPStatus, "input_tokens": v.Usage.Input, "output_tokens": v.Usage.Output, "cached_tokens": v.Usage.Cached, "reasoning_tokens": v.Usage.Reasoning, "usage_completeness": metricLabel(v.Completeness, "known", "complete", "partial", "unknown"), "currency": nil, "estimated_cost": v.Cost}
	if v.Price != nil && validPrice(*v.Price) {
		out["currency"] = v.Price.Currency
	}
	return out
}
func exportCSVValue(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case *int64:
		if n == nil {
			return ""
		}
		return strconv.FormatInt(*n, 10)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case *string:
		if n == nil {
			return ""
		}
		return csvSafeText(*n)
	case string:
		return csvSafeText(n)
	default:
		return csvSafeText(fmt.Sprint(n))
	}
}
func (a *App) exportsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Format  string            `json:"format"`
		Filters map[string]string `json:"filters"`
		Columns []string          `json:"columns"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Format != "csv" && in.Format != "json" {
		fail(w, 422, "导出格式只能为 csv 或 json", "format")
		return
	}
	if len(in.Columns) == 0 {
		in.Columns = append([]string{}, requestExportColumns...)
	}
	seen := map[string]bool{}
	for _, col := range in.Columns {
		found := false
		for _, known := range requestExportColumns {
			if col == known {
				found = true
			}
		}
		if !found || seen[col] {
			fail(w, 422, "导出列不是公开字段或重复", "columns")
			return
		}
		seen[col] = true
	}
	query, args, err := exportQuery(in.Filters)
	if err != nil {
		fail(w, 422, err.Error(), "filters")
		return
	}
	var estimate int64
	if err = a.Store.DB.QueryRowContext(r.Context(), "SELECT count(*)"+query, args...).Scan(&estimate); err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if estimate > 1000000 {
		fail(w, 422, "导出超过 100 万行，请缩短日期范围", "filters")
		return
	}
	a.startLocalOperation(w, r, "export_"+in.Format, func(ctx context.Context, folder string) (any, error) {
		snapshot := filepath.Join(folder, "export-snapshot.sqlite")
		defer os.Remove(snapshot)
		if err := sqliteSnapshot(ctx, a.Store.DB, snapshot); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("导出一致快照失败")
		}
		db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(snapshot)+"?mode=ro")
		if err != nil {
			return nil, storageError()
		}
		defer db.Close()
		rows, err := db.QueryContext(ctx, "SELECT data"+query+" ORDER BY rowid", args...)
		if err != nil {
			return nil, storageError()
		}
		defer rows.Close()
		f, err := os.OpenFile(filepath.Join(folder, "artifact"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, storageError()
		}
		defer f.Close()
		writer := csv.NewWriter(f)
		if in.Format == "csv" {
			if err = writer.Write(in.Columns); err != nil {
				return nil, storageError()
			}
		} else {
			if _, err = f.WriteString("["); err != nil {
				return nil, storageError()
			}
		}
		count := 0
		for rows.Next() {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			count++
			if count > 1000000 {
				return nil, errors.New("快照超过 100 万行，请缩短日期范围")
			}
			var b string
			var v Record
			if err = rows.Scan(&b); err != nil {
				return nil, storageError()
			}
			if err = json.Unmarshal([]byte(b), &v); err != nil {
				return nil, errors.New("请求记录无法读取")
			}
			values := exportRecord(v)
			if in.Format == "csv" {
				columns := make([]string, len(in.Columns))
				for i, col := range in.Columns {
					columns[i] = exportCSVValue(values[col])
				}
				if err = writer.Write(columns); err != nil {
					return nil, storageError()
				}
			} else {
				row := map[string]any{}
				for _, col := range in.Columns {
					row[col] = values[col]
				}
				if count > 1 {
					if _, err = f.WriteString(","); err != nil {
						return nil, storageError()
					}
				}
				encoded, e := json.Marshal(row)
				if e != nil {
					return nil, e
				}
				if _, err = f.Write(encoded); err != nil {
					return nil, storageError()
				}
			}
		}
		if err = rows.Err(); err != nil {
			return nil, storageError()
		}
		if in.Format == "csv" {
			writer.Flush()
			if err = writer.Error(); err != nil {
				return nil, storageError()
			}
		} else {
			if _, err = f.WriteString("]\n"); err != nil {
				return nil, storageError()
			}
		}
		if err = f.Sync(); err != nil {
			return nil, storageError()
		}
		return map[string]any{"rows": count, "row_estimate": estimate, "format": in.Format, "columns": in.Columns}, nil
	})
}

// Accounting metadata is preserved, including unresolved amounts, while free-form reconciliation evidence is excluded.
func backupJSONFields(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, field := range fields {
		if v, ok := all[field]; ok {
			out[field] = v
		}
	}
	return out, nil
}
func sanitizeBackupBudget(raw string) (string, error) {
	value, err := backupJSONFields([]byte(raw), "id", "name", "scope", "currency", "amount_limit", "mode", "period", "enabled", "version", "created_at")
	if err != nil {
		return "", err
	}
	if scope, ok := value["scope"]; ok {
		v, e := backupJSONFields(scope, "kind", "id")
		if e != nil {
			return "", e
		}
		value["scope"], _ = json.Marshal(v)
	}
	if period, ok := value["period"]; ok {
		v, e := backupJSONFields(period, "kind", "timezone", "start_at", "end_at")
		if e != nil {
			return "", e
		}
		value["period"], _ = json.Marshal(v)
	}
	b, err := json.Marshal(value)
	return string(b), err
}
func sanitizeBackupAllocations(raw []byte) ([]byte, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for key, value := range all {
		v, err := backupJSONFields(value, "reserved", "actual", "known_partial_cost", "state", "provenance")
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	return json.Marshal(out)
}
func sanitizeBackupAccountingAudit(raw string) (string, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return "", err
	}
	var kind string
	if err := json.Unmarshal(all["kind"], &kind); err != nil {
		return "", err
	}
	out := map[string]json.RawMessage{"kind": all["kind"]}
	switch kind {
	case "budget_change":
		out["budget_id"] = all["budget_id"]
		for _, field := range []string{"before", "after"} {
			value, err := sanitizeBackupBudget(string(all[field]))
			if err != nil {
				return "", err
			}
			out[field] = json.RawMessage(value)
		}
	case "manual_reconciliation":
		out["version"] = all["version"]
		out["provenance"] = all["provenance"]
		for _, field := range []string{"before", "after"} {
			value, err := sanitizeBackupAllocations(all[field])
			if err != nil {
				return "", err
			}
			out[field] = value
		}
		var costs []json.RawMessage
		if err := json.Unmarshal(all["attempt_costs"], &costs); err != nil {
			return "", err
		}
		safe := []any{}
		for _, cost := range costs {
			value, err := backupJSONFields(cost, "attempt_id", "currency", "amount")
			if err != nil {
				return "", err
			}
			safe = append(safe, value)
		}
		out["attempt_costs"], _ = json.Marshal(safe)
	default:
		return "", errors.New("账务审计类型尚未审核；备份已停止")
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// Call at startup and from hourly maintenance. The bounded pass removes expired private artifacts without following links.
func (a *App) cleanupOperationArtifacts(ctx context.Context) error {
	root, err := a.localRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return storageError()
	}
	checked := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validLocalID(entry.Name()) || !entry.IsDir() {
			continue
		}
		folder := filepath.Join(root, entry.Name())
		b, err := os.ReadFile(filepath.Join(folder, "descriptor.json"))
		if err != nil {
			return storageError()
		}
		var d localArtifact
		if json.Unmarshal(b, &d) != nil || d.ID != entry.Name() {
			return errors.New("本地操作目录元数据不可读")
		}
		if time.Since(d.CreatedAt) > operationArtifactTTL {
			if err = os.RemoveAll(folder); err != nil {
				return storageError()
			}
			checked++
			if checked == 500 {
				break
			}
		}
	}
	_, err = a.Store.DB.ExecContext(ctx, "DELETE FROM settings WHERE key IN (SELECT key FROM settings WHERE key LIKE 'config_import_audit_%' AND json_extract(value,'$.at')<? LIMIT 500)", time.Now().UTC().Add(-90*24*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return storageError()
	}
	return nil
}
