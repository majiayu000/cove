package app

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
)

// Enable only after HTTP handlers and every non-HTTP writer owns a matching
// beginBackupOwnerLocked lease. A nil channel means full backup is unavailable.
func (a *App) EnableBackupGate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backupChanged == nil {
		a.backupChanged = make(chan struct{})
	}
}

// The caller holds a.mu. release acquires a.mu itself, so call it after unlocking
// and only after the owner's final SQL/file/credential publication has finished.
func (a *App) beginBackupOwnerLocked() (func(), error) {
	if a.stopping || a.backupQuiescing {
		return nil, errors.New("服务正在关闭或完整备份排空中")
	}
	if a.backupChanged == nil {
		return func() {}, nil
	}
	a.backupOwners++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.backupOwners--
			close(a.backupChanged)
			a.backupChanged = make(chan struct{})
		})
	}, nil
}

// The HTTP backup operation must not own a lease while waiting on this barrier.
// Ordinary cancellation is not a drain: on timeout the service is resumed and
// no full archive can be published. a.stopping is never changed by this method.
func (a *App) quiesceBackup(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a.mu.Lock()
	if a.backupChanged == nil {
		a.mu.Unlock()
		return nil, &accountingError{Status: 422, Field: "mode", Message: "完整备份写入 owner 门禁尚未接齐，拒绝生成不一致的 full 包"}
	}
	if a.stopping || a.backupQuiescing {
		a.mu.Unlock()
		return nil, errors.New("服务正在关闭或已有完整备份")
	}
	a.backupQuiescing = true
	a.signalAdmission()
	for _, login := range a.logins {
		login.cancel()
	}
	a.mu.Unlock()
	var once sync.Once
	resume := func() {
		once.Do(func() {
			a.mu.Lock()
			a.backupQuiescing = false
			close(a.backupChanged)
			a.backupChanged = make(chan struct{})
			a.signalAdmission()
			a.mu.Unlock()
		})
	}
	for {
		a.mu.Lock()
		owners := a.backupOwners
		changed := a.backupChanged
		stopping := a.stopping
		a.mu.Unlock()
		if stopping {
			resume()
			return nil, errors.New("备份期间服务关闭")
		}
		if owners == 0 {
			if err := ctx.Err(); err != nil {
				resume()
				return nil, fmt.Errorf("drain_timeout: %w", err)
			}
			return resume, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			resume()
			return nil, fmt.Errorf("drain_timeout: %w", ctx.Err())
		}
	}
}
func backupGateExempt(r *http.Request) bool {
	if r.URL.Path == "/admin/backups" {
		return true
	}
	return r.Method == "GET" && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/admin/operations" || strings.HasPrefix(r.URL.Path, "/admin/operations/"))
}

// Only the current v2 tables have audited secret ownership. Full backup keeps
// referenced private rollback records in the vault, while local downloadable
// operation artifacts are invalidated because their directories are not packed.
func checkFullBackupSchema(db *sql.DB) error {
	known := map[string]bool{"accounts": true, "sources": true, "source_models": true, "routes": true, "model_aliases": true, "client_keys": true, "requests": true, "attempts": true, "bindings": true, "settings": true, "operations": true, "client_changes": true, "client_previews": true, "budgets": true, "reservations": true, "accounting_audit": true, "prices": true, "alerts": true, "alert_deliveries": true, "gemini_call_bindings": true, "gemini_history_bindings": true, "response_cache": true, "resources": true, "jobs": true, "job_items": true, "quota_snapshots": true}
	var unsafe int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type IN ('trigger','view') OR upper(sql) LIKE 'CREATE VIRTUAL TABLE%'").Scan(&unsafe); err != nil || unsafe > 0 {
		return errors.New("full 备份含未审核 schema 对象，原目录保留")
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		if !known[name] {
			return errors.New("full 备份存在未审核表/私有引用，原目录保留")
		}
	}
	return rows.Err()
}
func fullRequiredSecrets(db *sql.DB, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	require := func(ref string) error {
		if ref == "" {
			return nil
		}
		value, ok := values[ref]
		if !ok || value == "" {
			return errors.New("full 备份缺少必需 secret；不会生成不可恢复的包")
		}
		out[ref] = value
		return nil
	}
	ref, err := notificationRequiredSecret(db)
	if err != nil {
		return nil, err
	}
	if err = require(ref); err != nil {
		return nil, err
	}
	for _, table := range []string{"accounts", "client_previews", "client_changes"} {
		column := "secret_ref"
		if table == "accounts" {
			column = "credential_ref"
		}
		rows, err := db.Query("SELECT " + column + " FROM " + table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref string
			if err = rows.Scan(&ref); err == nil {
				err = require(ref)
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var adminDigest string
	err = db.QueryRow("SELECT value FROM settings WHERE key='admin_digest'").Scan(&adminDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if err = require("administrator"); err != nil {
			return nil, err
		}
		if digest(out["administrator"]) != adminDigest {
			return nil, errors.New("full 备份 administrator 与数据库代次不一致")
		}
	}
	var raw string
	err = db.QueryRow("SELECT value FROM settings WHERE key='telemetry'").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		var stored telemetryStored
		if json.Unmarshal([]byte(raw), &stored) != nil {
			return nil, errors.New("观测配置不可读")
		}
		if err = require(stored.HeaderRef); err != nil {
			return nil, err
		}
	}
	// A configured source must have the account generation and credential
	// represented by its SQL parent. 'none' sources intentionally have no secret.
	rows, err := db.Query("SELECT s.data,a.generation,a.credential_ref FROM sources s JOIN accounts a ON a.id=s.account_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, ref string
		var generation int
		var src Source
		if err = rows.Scan(&raw, &generation, &ref); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &src) != nil {
			return nil, errors.New("来源数据不可读")
		}
		if src.AccountGeneration != 0 && src.AccountGeneration != generation {
			return nil, errors.New("source/account 凭据代次不一致")
		}
		if src.Configured && src.Kind != "none" && ref == "" {
			return nil, errors.New("已配置来源缺少 secret ref")
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
func (a *App) fullSnapshot(ctx context.Context, folder string) (backupManifest, error) {
	resume, err := a.quiesceBackup(ctx)
	if err != nil {
		return backupManifest{}, err
	}
	defer resume()
	// These locks freeze configuration and publication even if a future caller
	// bypasses HTTP admission. All existing async writers must have drained first.
	a.mu.Lock()
	defer a.mu.Unlock()
	vault, ok := a.Secrets.(*FileSecrets)
	if !ok {
		return backupManifest{}, errors.New("full 备份仅支持已实现一致快照的 FileSecrets")
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if vault.fault != nil {
		return backupManifest{}, errors.New("凭据存储异常，拒绝 full 备份")
	}
	entries, e := os.ReadDir(a.Config.DataDir)
	if e != nil {
		return backupManifest{}, storageError()
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".resource-spool-") {
			return backupManifest{}, errors.New("完整备份检测到未清理资源临时文件，请完成故障清理后重试")
		}
	}
	dbPath := filepath.Join(folder, "db.sqlite")
	if err = sqliteSnapshot(ctx, a.Store.DB, dbPath); err != nil {
		return backupManifest{}, errors.New("full 一致数据库快照失败")
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(dbPath)+"?_foreign_keys=on&_journal_mode=DELETE")
	if err != nil {
		return backupManifest{}, storageError()
	}
	defer db.Close()
	var schema int
	err = db.QueryRow("PRAGMA user_version").Scan(&schema)
	if err == nil {
		err = checkBackupDB(db, schema)
	}
	if err == nil {
		err = checkFullBackupSchema(db)
	}
	if err != nil {
		return backupManifest{}, err
	}
	values, err := fullRequiredSecrets(db, vault.values)
	if err != nil {
		return backupManifest{}, err
	}
	if err = excludeFullResponseCache(db); err != nil {
		return backupManifest{}, err
	}
	if _, err = db.Exec("DELETE FROM operations"); err != nil {
		return backupManifest{}, err
	}
	if err = db.Close(); err != nil {
		return backupManifest{}, err
	}
	if err = os.Mkdir(filepath.Join(folder, "secrets"), 0700); err != nil {
		return backupManifest{}, err
	}
	if err = writePrivateJSON(filepath.Join(folder, "secrets", "credentials.json"), values); err != nil {
		return backupManifest{}, err
	}
	config := a.Config
	config.DataDir = ""
	if err = writePrivateJSON(filepath.Join(folder, "config.json"), config); err != nil {
		return backupManifest{}, err
	}
	manifest := backupManifest{FormatVersion: 1, CreatedAt: time.Now().UTC(), AppBuild: BuildID, SchemaVersion: schema, Mode: "full", SecretIncluded: true, Files: []backupFile{}}
	var total int64
	for _, name := range []string{"db.sqlite", "config.json", "secrets/credentials.json"} {
		n, h, err := hashFile(filepath.Join(folder, filepath.FromSlash(name)))
		if err != nil {
			return backupManifest{}, err
		}
		total += n
		if total > backupArchiveLimit-(1<<20) {
			return backupManifest{}, errors.New("full 备份超过 1 GiB 上限")
		}
		manifest.Files = append(manifest.Files, backupFile{Path: name, Bytes: n, SHA256: h})
	}
	return manifest, nil
}
func writeAgeArchive(ctx context.Context, path, passphrase string, manifest backupManifest, folder string) error {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return errors.New("备份口令无效")
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		return errors.New("备份 artifact 已存在或不可检查，拒绝覆盖")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".age-output-*")
	if err != nil {
		return storageError()
	}
	defer func() { file.Close(); _ = os.Remove(file.Name()) }()
	encrypted, err := age.Encrypt(file, recipient)
	if err != nil {
		return errors.New("age 加密初始化失败")
	}
	archive := tar.NewWriter(encrypted)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = archive.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err == nil {
		_, err = archive.Write(raw)
	}
	for _, entry := range manifest.Files {
		if err != nil {
			break
		}
		if err = ctx.Err(); err != nil {
			break
		}
		err = archive.WriteHeader(&tar.Header{Name: entry.Path, Mode: 0600, Size: entry.Bytes, Typeflag: tar.TypeReg})
		if err == nil {
			var input *os.File
			input, err = os.Open(filepath.Join(folder, filepath.FromSlash(entry.Path)))
			if err == nil {
				_, err = io.Copy(archive, &backupContextReader{ctx, input})
				closeErr := input.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := encrypted.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("age 加密归档失败；未发布完整备份")
	}
	if err = replaceAppFile(file.Name(), path); err != nil {
		return storageError()
	}
	return nil
}

type backupContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Passphrases stay in this invocation/operation closure. Do not place them in an
// Operation DTO, action replay snapshot, telemetry span or persistent settings.
func (a *App) backupArchive(ctx context.Context, path, mode string, encrypt bool, passphrase string) (backupManifest, error) {
	if mode == "" {
		mode = "metadata"
	}
	if mode != "metadata" && mode != "full" {
		return backupManifest{}, errors.New("备份模式无效")
	}
	if mode == "full" && !encrypt {
		return backupManifest{}, errors.New("full 必须使用 age 口令加密")
	}
	if encrypt && (passphrase == "" || len(passphrase) > 4096) || !encrypt && passphrase != "" {
		return backupManifest{}, errors.New("请选择加密并提供非空口令；不会保存口令")
	}
	if !encrypt {
		return a.metadataBackup(ctx, path)
	}
	folder, err := os.MkdirTemp(filepath.Dir(path), ".backup-private-")
	if err != nil {
		return backupManifest{}, storageError()
	}
	defer os.RemoveAll(folder)
	var manifest backupManifest
	if mode == "full" {
		manifest, err = a.fullSnapshot(ctx, folder)
	} else {
		plain := filepath.Join(folder, "metadata.tar")
		manifest, err = a.metadataBackup(ctx, plain)
		if err == nil {
			input, openErr := os.Open(plain)
			if openErr != nil {
				return backupManifest{}, openErr
			}
			_, err = unpackMetadataArchive(ctx, input, folder)
			input.Close()
			if err == nil {
				n, h, hashErr := hashFile(filepath.Join(folder, "db.sqlite"))
				err = hashErr
				manifest.Files = []backupFile{{Path: "db.sqlite", Bytes: n, SHA256: h}}
			}
		}
	}
	if err != nil {
		return backupManifest{}, err
	}
	if err = writeAgeArchive(ctx, path, passphrase, manifest, folder); err != nil {
		return backupManifest{}, err
	}
	return manifest, nil
}

// The decrypting stream is consumed to authenticated EOF before its plaintext
// tar is parsed. Truncation after a valid tar end marker still fails.
func unpackBackupArchive(ctx context.Context, reader io.Reader, folder, passphrase string, encrypted bool) (backupManifest, error) {
	if !encrypted {
		return unpackMetadataArchive(ctx, reader, folder)
	}
	invalid := errors.New("备份解密或完整性校验失败；请检查文件与口令，原目录保留")
	if passphrase == "" || len(passphrase) > 4096 {
		return backupManifest{}, invalid
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return backupManifest{}, invalid
	}
	// Bound authenticated uploads to this pinned age release's default work.
	// This does not change the age passphrase format or encryption parameters.
	identity.SetMaxWorkFactor(18)
	limited := &io.LimitedReader{R: reader, N: backupArchiveLimit + 1}
	decrypted, err := age.Decrypt(&backupContextReader{ctx, limited}, identity)
	if err != nil {
		return backupManifest{}, invalid
	}
	plain, err := os.CreateTemp(folder, ".decrypted-*")
	if err != nil {
		return backupManifest{}, storageError()
	}
	defer os.Remove(plain.Name())
	n, err := io.Copy(plain, io.LimitReader(&backupContextReader{ctx, decrypted}, backupExpandedLimit+1))
	if closeErr := plain.Close(); err == nil {
		err = closeErr
	}
	if err != nil || n > backupExpandedLimit || limited.N == 0 {
		return backupManifest{}, invalid
	}
	input, err := os.Open(plain.Name())
	if err != nil {
		return backupManifest{}, storageError()
	}
	defer input.Close()
	manifest, err := unpackEncryptedBackupTar(ctx, input, folder)
	if err != nil {
		return backupManifest{}, invalid
	}
	return manifest, nil
}
func unpackEncryptedBackupTar(ctx context.Context, reader io.Reader, folder string) (backupManifest, error) {
	var manifest backupManifest
	seen := map[string]bool{}
	allowed := map[string]bool{"manifest.json": true, "db.sqlite": true, "config.json": true, "secrets/credentials.json": true}
	archive := tar.NewReader(reader)
	var total int64
	success := false
	defer func() {
		if !success {
			for name := range seen {
				if name != "manifest.json" {
					_ = os.Remove(filepath.Join(folder, filepath.FromSlash(name)))
				}
			}
			_ = os.Remove(filepath.Join(folder, "secrets"))
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return manifest, err
		}
		entry, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, err
		}
		if !allowed[entry.Name] || seen[entry.Name] || entry.Typeflag != tar.TypeReg && entry.Typeflag != tar.TypeRegA || entry.Size < 0 || entry.Size > backupArchiveLimit || total+entry.Size > backupExpandedLimit {
			return manifest, errors.New("备份路径/文件/体积不符")
		}
		seen[entry.Name] = true
		total += entry.Size
		if entry.Name == "manifest.json" {
			if entry.Size > 1<<20 {
				return manifest, errors.New("manifest 过大")
			}
			decoder := json.NewDecoder(io.LimitReader(archive, entry.Size))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&manifest); err != nil {
				return manifest, err
			}
			continue
		}
		filename := filepath.Join(folder, filepath.FromSlash(entry.Name))
		if entry.Name == "secrets/credentials.json" {
			if err = os.Mkdir(filepath.Dir(filename), 0700); err != nil {
				return manifest, err
			}
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return manifest, err
		}
		_, err = io.CopyN(file, archive, entry.Size)
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return manifest, err
		}
	}
	if manifest.FormatVersion != 1 || manifest.SchemaVersion != 2 || !seen["manifest.json"] || !seen["db.sqlite"] {
		return manifest, errors.New("manifest/schema 不符")
	}
	expectedCount := 1
	if manifest.Mode == "full" && manifest.SecretIncluded {
		expectedCount = 3
		if !seen["secrets/credentials.json"] || !seen["config.json"] {
			return manifest, errors.New("full 缺 secret/config")
		}
	} else if manifest.Mode != "metadata" || manifest.SecretIncluded {
		return manifest, errors.New("模式不符")
	}
	if len(manifest.Files) != expectedCount || len(seen) != expectedCount+1 {
		return manifest, errors.New("备份文件闭包不符")
	}
	files := map[string]bool{}
	for _, entry := range manifest.Files {
		if files[entry.Path] || !seen[entry.Path] || entry.Path == "manifest.json" {
			return manifest, errors.New("manifest 文件闭包不符")
		}
		files[entry.Path] = true
		n, h, err := hashFile(filepath.Join(folder, filepath.FromSlash(entry.Path)))
		if err != nil || entry.Bytes != n || entry.SHA256 != h {
			return manifest, errors.New("备份 hash/长度不符")
		}
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(folder, "db.sqlite"))+"?_foreign_keys=on&_journal_mode=DELETE")
	if err != nil {
		return manifest, err
	}
	defer db.Close()
	if err = checkBackupDB(db, 2); err != nil {
		return manifest, err
	}
	if manifest.Mode == "metadata" {
		if err = sanitizeMetadata(db); err != nil {
			return manifest, err
		}
	} else {
		if err = checkFullBackupSchema(db); err != nil {
			return manifest, err
		}
		values, err := readFullVault(filepath.Join(folder, "secrets", "credentials.json"))
		if err != nil {
			return manifest, err
		}
		if _, err = fullRequiredSecrets(db, values); err != nil {
			return manifest, err
		}
		var config Config
		raw, err := os.ReadFile(filepath.Join(folder, "config.json"))
		if err != nil || json.Unmarshal(raw, &config) != nil {
			return manifest, errors.New("full config 无效")
		}
		if err = prepareFullRestoredDB(db); err != nil {
			return manifest, err
		}
	}
	if err = checkBackupDB(db, 2); err != nil {
		return manifest, err
	}
	if err = db.Close(); err != nil {
		return manifest, err
	}
	for i := range manifest.Files {
		n, h, err := hashFile(filepath.Join(folder, filepath.FromSlash(manifest.Files[i].Path)))
		if err != nil {
			return manifest, err
		}
		manifest.Files[i].Bytes = n
		manifest.Files[i].SHA256 = h
	}
	success = true
	return manifest, nil
}
func readFullVault(filename string) (map[string]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > backupArchiveLimit {
		return nil, errors.New("vault 过大")
	}
	var values map[string]string
	decoder := json.NewDecoder(file)
	if err = decoder.Decode(&values); err != nil || values == nil {
		return nil, errors.New("vault 无效")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("vault 多余数据")
	}
	return values, nil
}
func prepareFullRestoredDB(db *sql.DB) error {
	if err := excludeFullResponseCache(db); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{`DELETE FROM operations`, `UPDATE requests SET status='interrupted',data=json_set(data,'$.status','interrupted','$.error_stage','restore','$.error_summary','恢复未重发；上游执行及费用可能未知') WHERE status IN ('queued','admitted','dispatching','streaming')`, `UPDATE attempts SET data=json_set(data,'$.status','interrupted','$.error_stage','restore','$.error_summary','恢复未重发；上游执行及费用可能未知') WHERE json_extract(data,'$.status') IN ('admitted','dispatching','streaming')`} {
		if _, err = tx.Exec(query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Call after preview hash revalidation, before publishing the new directory.
// Metadata remains credential-free. This only copies full's verified vault; it
// never mutates the current vault or opens/replays a provider job.
func restoreBackupSecrets(folder, target string, manifest backupManifest) error {
	if manifest.Mode == "metadata" {
		return nil
	}
	if manifest.Mode != "full" || !manifest.SecretIncluded {
		return errors.New("恢复模式不符")
	}
	var matched bool
	for _, entry := range manifest.Files {
		if entry.Path == "secrets/credentials.json" {
			n, h, err := hashFile(filepath.Join(folder, "secrets", "credentials.json"))
			if err != nil || n != entry.Bytes || h != entry.SHA256 {
				return errors.New("恢复 vault hash 已改变")
			}
			matched = true
		}
	}
	if !matched {
		return errors.New("full 缺必要 vault")
	}
	if err := os.Mkdir(filepath.Join(target, "secrets"), 0700); err != nil {
		return err
	}
	return copyPrivateFile(filepath.Join(folder, "secrets", "credentials.json"), filepath.Join(target, "secrets", "credentials.json"))
}

// A small deterministic summary is safe for Operation.result/UI; it contains no
// secret refs, values or passphrase. File hashes are archive integrity evidence.
func backupManifestSummary(manifest backupManifest) map[string]any {
	paths := []string{}
	for _, entry := range manifest.Files {
		paths = append(paths, entry.Path)
	}
	sort.Strings(paths)
	return map[string]any{"mode": manifest.Mode, "secret_included": manifest.SecretIncluded, "schema_version": manifest.SchemaVersion, "files": paths}
}

// RestorePointer is an explicit user-selected startup pointer, outside either
// data directory. The launcher must resolve it before loading configuration.
type RestorePointer struct {
	ConfigPath string `json:"config_path"`
	DataDir    string `json:"data_dir"`
	BuildID    string `json:"build_id"`
}
type RestoreSwitchJournal struct {
	Phase          string         `json:"phase"`
	PointerPath    string         `json:"pointer_path"`
	Old            RestorePointer `json:"old"`
	New            RestorePointer `json:"new"`
	OldPointerHash string         `json:"old_pointer_hash"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Error          string         `json:"error,omitempty"`
}

// The caller is a separate controlled launcher, not the HTTP process it stops.
// Start must enforce admissionClosed; Ready must prove PID/path/build/storage
// and secrets for that exact pointer. Activate is explicit after readiness.
type RestoreSwitchHooks struct {
	Drain    func(context.Context, RestorePointer) (func(), error)
	Stop     func(context.Context, RestorePointer) error
	Start    func(context.Context, RestorePointer, bool) error
	Ready    func(context.Context, RestorePointer) error
	Activate func(context.Context, RestorePointer) error
}

func validRestorePointer(pointer RestorePointer) bool {
	return filepath.IsAbs(pointer.ConfigPath) && filepath.Clean(pointer.ConfigPath) == pointer.ConfigPath && filepath.IsAbs(pointer.DataDir) && filepath.Clean(pointer.DataDir) == pointer.DataDir && pointer.BuildID != ""
}
func ReadRestorePointer(filename string) (RestorePointer, string, error) {
	var pointer RestorePointer
	info, err := os.Lstat(filename)
	if err != nil {
		return pointer, "", err
	}
	if !info.Mode().IsRegular() {
		return pointer, "", errors.New("恢复启动指针不是普通文件")
	}
	raw, err := os.ReadFile(filename)
	if err != nil || len(raw) > 65536 {
		return pointer, "", errors.New("恢复启动指针不可读")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&pointer); err != nil || !validRestorePointer(pointer) {
		return pointer, "", errors.New("恢复启动指针无效")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return pointer, "", errors.New("恢复启动指针有多余数据")
	}
	return pointer, digest(string(raw)), nil
}
func persistRestoreJSON(filename string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(filename); err == nil && !info.Mode().IsRegular() {
		return errors.New("恢复 journal/pointer 不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".restore-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceAppFile(file.Name(), filename)
}
func PrepareRestoreSwitch(journalPath, pointerPath, expectedOldHash string, next RestorePointer) (RestoreSwitchJournal, error) {
	var journal RestoreSwitchJournal
	old, hash, err := ReadRestorePointer(pointerPath)
	if err != nil {
		return journal, err
	}
	if hash != expectedOldHash || !validRestorePointer(next) || old.DataDir == next.DataDir {
		return journal, errors.New("恢复目标/旧指针已改变，需重新确认")
	}
	if !filepath.IsAbs(journalPath) || !filepath.IsAbs(pointerPath) {
		return journal, errors.New("journal/pointer 需为绝对路径")
	}
	if _, err = os.Lstat(journalPath); !os.IsNotExist(err) {
		return journal, errors.New("恢复 journal 已存在，不能覆盖")
	}
	for _, dir := range []string{old.DataDir, next.DataDir} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return journal, errors.New("恢复数据目录必须是真实目录")
		}
	}
	if _, err = os.Stat(next.ConfigPath); err != nil {
		return journal, errors.New("新目录启动配置缺失")
	}
	now := time.Now().UTC()
	journal = RestoreSwitchJournal{Phase: "prepared", PointerPath: pointerPath, Old: old, New: next, OldPointerHash: hash, CreatedAt: now, UpdatedAt: now}
	return journal, persistRestoreJSON(journalPath, journal)
}
func readRestoreSwitchJournal(filename string) (RestoreSwitchJournal, error) {
	var journal RestoreSwitchJournal
	raw, err := os.ReadFile(filename)
	if err != nil || len(raw) > 65536 {
		return journal, errors.New("恢复 journal 不可读")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&journal); err != nil || !validRestorePointer(journal.Old) || !validRestorePointer(journal.New) || !filepath.IsAbs(journal.PointerPath) {
		return journal, errors.New("恢复 journal 无效")
	}
	return journal, nil
}
func ApplyRestoreSwitch(ctx context.Context, journalPath string, hooks RestoreSwitchHooks) error {
	if hooks.Drain == nil || hooks.Stop == nil || hooks.Start == nil || hooks.Ready == nil || hooks.Activate == nil {
		return errors.New("恢复切换的本地 drain/停止/启动/readiness/准入门禁尚未接齐；未切换")
	}
	journal, err := readRestoreSwitchJournal(journalPath)
	if err != nil {
		return err
	}
	if journal.Phase == "succeeded" {
		return nil
	}
	if journal.Phase != "prepared" {
		return errors.New("已有未闭合恢复切换，请按 journal 检查进程/指针后重新预览")
	}
	current, hash, err := ReadRestorePointer(journal.PointerPath)
	if err != nil || hash != journal.OldPointerHash || current != journal.Old {
		return errors.New("旧指针已改变；未切换")
	}
	save := func(phase, message string) error {
		journal.Phase = phase
		journal.Error = message
		journal.UpdatedAt = time.Now().UTC()
		return persistRestoreJSON(journalPath, journal)
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 30*time.Second)
	resumeOld, drainErr := hooks.Drain(drainCtx, journal.Old)
	err = drainErr
	drainCancel()
	if err != nil {
		_ = save("failed_before_stop", "drain_timeout")
		return fmt.Errorf("drain_timeout，旧指针未变: %w", err)
	}
	if err = save("drained", ""); err != nil {
		if resumeOld != nil {
			resumeOld()
		}
		return err
	}
	if err = hooks.Stop(ctx, journal.Old); err != nil {
		_ = save("stop_uncertain", "旧进程停止未确认")
		return err
	}
	if err = save("old_stopped", ""); err != nil {
		return err
	}
	// Rollback is permitted only before activation. It restores the pointer and
	// starts the old directory; neither directory's DB/vault is copied or erased.
	rollback := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if stopErr := hooks.Stop(rollbackCtx, journal.New); stopErr != nil {
			_ = save("rollback_blocked", "新实例停止未确认，保留新指针与两个目录")
			return errors.Join(cause, stopErr)
		}
		if pointerErr := persistRestoreJSON(journal.PointerPath, journal.Old); pointerErr != nil {
			_ = save("rollback_blocked", "旧指针恢复失败，两个目录保留")
			return errors.Join(cause, pointerErr)
		}
		if startErr := hooks.Start(rollbackCtx, journal.Old, false); startErr != nil {
			_ = save("rollback_failed", "旧指针已恢复，旧进程启动失败")
			return errors.Join(cause, startErr)
		}
		if readyErr := hooks.Ready(rollbackCtx, journal.Old); readyErr != nil {
			_ = save("rollback_failed", "旧指针已恢复，旧进程未就绪")
			return errors.Join(cause, readyErr)
		}
		if saveErr := save("rolled_back", "新版未通过门禁，已恢复旧指针/旧实例，两个目录保留"); saveErr != nil {
			return errors.Join(cause, saveErr)
		}
		return fmt.Errorf("恢复切换失败，旧实例已恢复: %w", cause)
	}
	if err = persistRestoreJSON(journal.PointerPath, journal.New); err != nil {
		return rollback(err)
	}
	if err = save("pointed_new", ""); err != nil {
		return rollback(err)
	}
	if err = hooks.Start(ctx, journal.New, true); err != nil {
		return rollback(err)
	}
	if err = save("new_started_closed", ""); err != nil {
		return rollback(err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = hooks.Ready(readyCtx, journal.New)
	cancel()
	if err != nil {
		return rollback(err)
	}
	if err = save("ready_closed", ""); err != nil {
		return rollback(err)
	}
	if err = hooks.Activate(ctx, journal.New); err != nil {
		_ = save("activation_uncertain", "准入开启结果未知；不自动恢复指针/数据，请检查新实例")
		return err
	}
	return save("succeeded", "")
}

// Response cache is private, optional and file-backed. No cache file is copied;
// reset both facts and settings in the isolated DB so restored refs cannot lie.
func excludeFullResponseCache(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='response_cache'").Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		if _, err := db.Exec("DELETE FROM response_cache"); err != nil {
			return err
		}
	}
	_, err := db.Exec("DELETE FROM settings WHERE key='response_cache'")
	return err
}
