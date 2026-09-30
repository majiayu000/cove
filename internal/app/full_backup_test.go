package app

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const syntheticBackupPassphrase = "SYNTHETIC_LOCAL_BACKUP_PASSWORD"

func fullBackupApp(t *testing.T) *App {
	t.Helper()
	a := contractApp(t, nil)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	vault, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	for ref, value := range map[string]string{"administrator": "test-administrator", "source": "test-source-secret", "unreferenced": "MUST_NOT_EXPORT_ORPHAN"} {
		if err = vault.Put(ref, value); err != nil {
			t.Fatal(err)
		}
	}
	a.Config.DataDir = dir
	src, sourceErr := a.Store.source("source")
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	src.BaseURL = "https://synthetic.invalid/v1"
	if sourceErr = a.Store.saveSource(src); sourceErr != nil {
		t.Fatal(sourceErr)
	}
	a.Secrets = vault
	a.EnableBackupGate()
	return a
}
func mustBackupPrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func openBackupDB(t *testing.T, folder string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(folder, "db.sqlite"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func backupOwner(t *testing.T, a *App) func() {
	t.Helper()
	a.mu.Lock()
	release, err := a.beginBackupOwnerLocked()
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return release
}
func TestSpecFullBackupAgeAndRestore(t *testing.T) {
	a := fullBackupApp(t)
	record := seedOperationsRecord(t, a, "full-history")
	_ = record
	vault := a.Secrets.(*FileSecrets)
	if err := vault.Put("client-private", "SYNTHETIC_PRIVATE_RECOVERY_BYTES"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("INSERT INTO client_previews(id,expires_at,secret_ref,data) VALUES('preview',?,'client-private','{}')", time.Now().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("INSERT INTO operations(id,data) VALUES('pending',?)", encode(Operation{ID: "pending", State: "running"})); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mustBackupPrivateDir(t), "artifact")
	manifest, err := a.backupArchive(context.Background(), path, "full", true, syntheticBackupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Mode != "full" || !manifest.SecretIncluded || len(manifest.Files) != 3 {
		t.Fatal(manifest)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("age-encryption.org/v1")) || bytes.Contains(raw, []byte("test-source-secret")) || bytes.Contains(raw, []byte(syntheticBackupPassphrase)) {
		t.Fatal("nonstandard/plaintext full archive")
	}
	folder := mustBackupPrivateDir(t)
	restored, err := unpackBackupArchive(context.Background(), bytes.NewReader(raw), folder, syntheticBackupPassphrase, true)
	if err != nil {
		t.Fatal(err)
	}
	values, err := readFullVault(filepath.Join(folder, "secrets", "credentials.json"))
	if err != nil || values["source"] != "test-source-secret" || values["client-private"] != "SYNTHETIC_PRIVATE_RECOVERY_BYTES" || values["unreferenced"] != "" {
		t.Fatal("required vault closure failed", err)
	}
	db := openBackupDB(t, folder)
	var digestValue string
	if err = db.QueryRow("SELECT digest FROM client_keys WHERE id='key'").Scan(&digestValue); err != nil || digestValue != digest("test-client-key") {
		t.Fatal("full changed key authentication", err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM operations").Scan(&n); err != nil || n != 0 {
		t.Fatal("unrestorable local operations kept", err)
	}
	if err = a.Store.DB.QueryRow("SELECT count(*) FROM operations WHERE id='pending'").Scan(&n); err != nil || n != 1 {
		t.Fatal("source DB modified")
	}
	target := mustBackupPrivateDir(t)
	if err = restoreBackupSecrets(folder, target, restored); err != nil {
		t.Fatal(err)
	}
	newVault, err := NewFileSecrets(target)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := newVault.Get("source"); err != nil || value != "test-source-secret" {
		t.Fatal("full restore vault unreadable", err)
	}
	a.mu.Lock()
	paused, owners, stopping := a.backupQuiescing, a.backupOwners, a.stopping
	a.mu.Unlock()
	if paused || owners != 0 || stopping {
		t.Fatal("service was not resumed")
	}
}
func TestSpecEncryptedMetadataHasNoCredentials(t *testing.T) {
	a := fullBackupApp(t)
	path := filepath.Join(mustBackupPrivateDir(t), "artifact")
	manifest, err := a.backupArchive(context.Background(), path, "metadata", true, syntheticBackupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SecretIncluded {
		t.Fatal("metadata included secrets")
	}
	raw, _ := os.ReadFile(path)
	folder := mustBackupPrivateDir(t)
	restored, err := unpackBackupArchive(context.Background(), bytes.NewReader(raw), folder, syntheticBackupPassphrase, true)
	if err != nil || restored.Mode != "metadata" {
		t.Fatal(restored, err)
	}
	if _, err = os.Stat(filepath.Join(folder, "secrets")); !os.IsNotExist(err) {
		t.Fatal("metadata extracted a vault")
	}
	db := openBackupDB(t, folder)
	var value string
	if err = db.QueryRow("SELECT digest FROM client_keys WHERE id='key'").Scan(&value); err != nil || value == digest("test-client-key") {
		t.Fatal("metadata restored usable key")
	}
	if err = db.QueryRow("SELECT credential_ref FROM accounts LIMIT 1").Scan(&value); err != nil || value != "" {
		t.Fatal("metadata retained ref", err)
	}
}
func TestSpecFullBackupRefusesMissingSecretAndUngatedState(t *testing.T) {
	a := fullBackupApp(t)
	if err := a.Secrets.Delete("source"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mustBackupPrivateDir(t), "artifact")
	if _, err := a.backupArchive(context.Background(), path, "full", true, syntheticBackupPassphrase); err == nil {
		t.Fatal("missing secret accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed full published artifact")
	}
	a.mu.Lock()
	paused := a.backupQuiescing
	a.backupChanged = nil
	a.mu.Unlock()
	if paused {
		t.Fatal("failed full did not resume")
	}
	if _, err := a.backupArchive(context.Background(), path, "full", true, syntheticBackupPassphrase); err == nil {
		t.Fatal("ungated full claimed consistency")
	}
	if _, err := a.backupArchive(context.Background(), path, "full", false, ""); err == nil {
		t.Fatal("plaintext full accepted")
	}
}
func TestSpecFullBackupDrainTimeoutResumesWithoutSnapshot(t *testing.T) {
	a := fullBackupApp(t)
	release := backupOwner(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	path := filepath.Join(mustBackupPrivateDir(t), "artifact")
	if _, err := a.backupArchive(ctx, path, "full", true, syntheticBackupPassphrase); err == nil || !strings.Contains(err.Error(), "drain_timeout") {
		t.Fatal(err)
	}
	a.mu.Lock()
	paused, owners, stopping := a.backupQuiescing, a.backupOwners, a.stopping
	a.mu.Unlock()
	if paused || owners != 1 || stopping {
		t.Fatal("timeout falsely drained/shut down")
	}
	release()
	release()
	a.mu.Lock()
	owners = a.backupOwners
	a.mu.Unlock()
	if owners != 0 {
		t.Fatal("owner release is not once")
	}
	next := backupOwner(t, a)
	next()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("timeout published artifact")
	}
}
func TestSpecFullBackupWaitsForSameGenerationPublisher(t *testing.T) {
	a := fullBackupApp(t)
	release := backupOwner(t, a)
	folder := mustBackupPrivateDir(t)
	result := make(chan error, 1)
	go func() { _, err := a.fullSnapshot(context.Background(), folder); result <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		paused := a.backupQuiescing
		a.mu.Unlock()
		if paused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gate did not pause")
		}
		time.Sleep(time.Millisecond)
	}
	a.mu.Lock()
	blocked, err := a.beginBackupOwnerLocked()
	a.mu.Unlock()
	if err == nil {
		blocked()
		t.Fatal("new writer admitted during full drain")
	}
	select {
	case err := <-result:
		t.Fatal("snapshot raced publisher", err)
	default:
	}
	a.mu.Lock()
	source, err := a.Store.source("source")
	if err == nil {
		err = a.replaceCredential(&source, "SYNTHETIC_NEW_GENERATION_SECRET", false)
	}
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	db := openBackupDB(t, folder)
	var ref string
	var generation int
	if err = db.QueryRow("SELECT credential_ref,generation FROM accounts WHERE id=?", source.AccountID).Scan(&ref, &generation); err != nil || generation != source.AccountGeneration {
		t.Fatal("generation mismatch", err)
	}
	values, err := readFullVault(filepath.Join(folder, "secrets", "credentials.json"))
	if err != nil || values[ref] != "SYNTHETIC_NEW_GENERATION_SECRET" || values["source"] != "" {
		t.Fatal("snapshot copied another credential generation", err)
	}
}
func encryptBackupTestBytes(t *testing.T, plain []byte) []byte {
	t.Helper()
	recipient, err := age.NewScryptRecipient(syntheticBackupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	recipient.SetWorkFactor(10) // Synthetic adversarial archive, never production encryption.
	var out bytes.Buffer
	writer, err := age.Encrypt(&out, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func fullTestTar(t *testing.T, manifest backupManifest, folder string, mutate func(map[string][]byte)) []byte {
	t.Helper()
	files := map[string][]byte{}
	for _, entry := range manifest.Files {
		raw, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(entry.Path)))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Path] = raw
	}
	if mutate != nil {
		mutate(files)
	}
	files["manifest.json"], _ = json.Marshal(manifest)
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	for name, raw := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestSpecFullRestoreWrongPassCorruptAndTruncation(t *testing.T) {
	a := fullBackupApp(t)
	folder := mustBackupPrivateDir(t)
	manifest, err := a.fullSnapshot(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	raw := encryptBackupTestBytes(t, fullTestTar(t, manifest, folder, nil))
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)-1] ^= 1
	var lastError string
	for _, tc := range []struct {
		name, pass string
		raw        []byte
	}{{"wrong-pass", "wrong", raw}, {"corrupt", syntheticBackupPassphrase, corrupt}, {"truncated", syntheticBackupPassphrase, raw[:len(raw)-16]}} {
		t.Run(tc.name, func(t *testing.T) {
			stage := mustBackupPrivateDir(t)
			_, err := unpackBackupArchive(context.Background(), bytes.NewReader(tc.raw), stage, tc.pass, true)
			if err == nil {
				t.Fatal("bad archive accepted")
			}
			if lastError != "" && err.Error() != lastError {
				t.Fatal("decryption errors differ")
			}
			lastError = err.Error()
			entries, _ := os.ReadDir(stage)
			if len(entries) != 0 {
				t.Fatal("failed decrypt retained plaintext")
			}
		})
	}
}
func TestSpecFullRestoreHashMissingSecretAndTraversal(t *testing.T) {
	a := fullBackupApp(t)
	folder := mustBackupPrivateDir(t)
	manifest, err := a.fullSnapshot(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		manifest backupManifest
		edit     func(map[string][]byte)
	}{{"hash", manifest, func(files map[string][]byte) { files["db.sqlite"][20] ^= 1 }}, {"traversal", manifest, func(files map[string][]byte) { files["../outside"] = []byte("escape") }}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := encryptBackupTestBytes(t, fullTestTar(t, tc.manifest, folder, tc.edit))
			stage := mustBackupPrivateDir(t)
			if _, err := unpackBackupArchive(context.Background(), bytes.NewReader(raw), stage, syntheticBackupPassphrase, true); err == nil {
				t.Fatal("bad full archive accepted")
			}
			entries, _ := os.ReadDir(stage)
			if len(entries) != 0 {
				t.Fatal("failed restore retained private files")
			}
		})
	}
	values, err := readFullVault(filepath.Join(folder, "secrets", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	delete(values, "source")
	if err = writePrivateJSON(filepath.Join(folder, "secrets", "credentials.json"), values); err != nil {
		t.Fatal(err)
	}
	for i := range manifest.Files {
		if manifest.Files[i].Path == "secrets/credentials.json" {
			n, h, err := hashFile(filepath.Join(folder, "secrets", "credentials.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifest.Files[i].Bytes = n
			manifest.Files[i].SHA256 = h
		}
	}
	raw := encryptBackupTestBytes(t, fullTestTar(t, manifest, folder, nil))
	stage := mustBackupPrivateDir(t)
	if _, err = unpackBackupArchive(context.Background(), bytes.NewReader(raw), stage, syntheticBackupPassphrase, true); err == nil {
		t.Fatal("hash-valid package missing referenced secret accepted")
	}
}
func TestSpecFullRestoreDoesNotReplayRunningRequests(t *testing.T) {
	a := fullBackupApp(t)
	r := seedOperationsRecord(t, a, "restore-running")
	r.Status = "dispatching"
	r.Ended = nil
	if err := a.Store.record(r); err != nil {
		t.Fatal(err)
	}
	folder := mustBackupPrivateDir(t)
	manifest, err := a.fullSnapshot(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	raw := encryptBackupTestBytes(t, fullTestTar(t, manifest, folder, nil))
	stage := mustBackupPrivateDir(t)
	if _, err = unpackBackupArchive(context.Background(), bytes.NewReader(raw), stage, syntheticBackupPassphrase, true); err != nil {
		t.Fatal(err)
	}
	db := openBackupDB(t, stage)
	var status string
	if err = db.QueryRow("SELECT status FROM requests WHERE id=?", r.ID).Scan(&status); err != nil || status != "interrupted" {
		t.Fatal("running generation restored as executable", status, err)
	}
}
func TestSpecAgeStandardDecryptionAuthenticatesEOF(t *testing.T) {
	a := fullBackupApp(t)
	path := filepath.Join(mustBackupPrivateDir(t), "artifact")
	if _, err := a.backupArchive(context.Background(), path, "full", true, syntheticBackupPassphrase); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := age.NewScryptIdentity(syntheticBackupPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := age.Decrypt(file, identity)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal("official library cannot decrypt", err)
	}
	if !bytes.Contains(plain, []byte("manifest.json")) || !bytes.Contains(plain, []byte("secrets/credentials.json")) {
		t.Fatal("not standard tar.age")
	}
}

func TestSpecFullBackupExcludesFileBackedCache(t *testing.T) {
	a := fullBackupApp(t)
	if _, err := a.Store.DB.Exec(`INSERT INTO response_cache VALUES('secret-cache','private.json',4,'future','now');INSERT INTO gemini_history_bindings VALUES('binding','k','s','a',1,1,'m','now');INSERT INTO settings(key,value) VALUES('response_cache','{"enabled":true}')`); err != nil {
		t.Fatal(err)
	}
	folder := mustBackupPrivateDir(t)
	if _, err := a.fullSnapshot(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	db := openBackupDB(t, folder)
	var n int
	for _, query := range []string{"SELECT count(*) FROM response_cache", "SELECT count(*) FROM settings WHERE key='response_cache'"} {
		if err := db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatal("cache retained", query, n, err)
		}
	}
	if err := db.QueryRow("SELECT count(*) FROM gemini_history_bindings").Scan(&n); err != nil || n != 1 {
		t.Fatal("full invalidated credential-backed history")
	}
	if err := a.Store.DB.QueryRow("SELECT count(*) FROM response_cache").Scan(&n); err != nil || n != 1 {
		t.Fatal("source cache changed")
	}
}
func TestSpecRestoreSwitchFailureRestoresOldPointer(t *testing.T) {
	for _, failReady := range []bool{false, true} {
		t.Run(map[bool]string{false: "succeeded", true: "readiness-failed"}[failReady], func(t *testing.T) {
			root := mustBackupPrivateDir(t)
			oldDir, newDir := filepath.Join(root, "old"), filepath.Join(root, "new")
			for _, dir := range []string{oldDir, newDir} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "retained"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			old := RestorePointer{ConfigPath: filepath.Join(oldDir, "config.json"), DataDir: oldDir, BuildID: "same-build"}
			next := RestorePointer{ConfigPath: filepath.Join(newDir, "config.json"), DataDir: newDir, BuildID: "same-build"}
			pointer := filepath.Join(root, "runtime-pointer.json")
			if err := persistRestoreJSON(pointer, old); err != nil {
				t.Fatal(err)
			}
			_, hash, err := ReadRestorePointer(pointer)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(root, "switch-journal.json")
			if _, err = PrepareRestoreSwitch(journalPath, pointer, hash, next); err != nil {
				t.Fatal(err)
			}
			var events []string
			activated := false
			hooks := RestoreSwitchHooks{Drain: func(context.Context, RestorePointer) (func(), error) {
				events = append(events, "drain-old")
				return func() { events = append(events, "resume-old") }, nil
			}, Stop: func(_ context.Context, p RestorePointer) error {
				events = append(events, "stop-"+filepath.Base(p.DataDir))
				return nil
			}, Start: func(_ context.Context, p RestorePointer, closed bool) error {
				events = append(events, "start-"+filepath.Base(p.DataDir))
				if p == next && !closed {
					t.Fatal("new data admitted before readiness")
				}
				return nil
			}, Ready: func(_ context.Context, p RestorePointer) error {
				events = append(events, "ready-"+filepath.Base(p.DataDir))
				if p == next && failReady {
					return errors.New("synthetic readiness failure")
				}
				return nil
			}, Activate: func(context.Context, RestorePointer) error {
				activated = true
				events = append(events, "activate-new")
				return nil
			}}
			err = ApplyRestoreSwitch(context.Background(), journalPath, hooks)
			current, _, readErr := ReadRestorePointer(pointer)
			if readErr != nil {
				t.Fatal(readErr)
			}
			journal, _ := readRestoreSwitchJournal(journalPath)
			if failReady {
				if err == nil || current != old || journal.Phase != "rolled_back" || activated {
					t.Fatal(current, journal, err, events)
				}
			} else {
				if err != nil || current != next || journal.Phase != "succeeded" || !activated {
					t.Fatal(current, journal, err, events)
				}
			}
			for _, dir := range []string{oldDir, newDir} {
				raw, err := os.ReadFile(filepath.Join(dir, "retained"))
				if err != nil || string(raw) != "data" {
					t.Fatal("rollback erased a directory")
				}
			}
		})
	}
}
func TestSpecRestoreSwitchUnwiredHooksNeverStops(t *testing.T) {
	if err := ApplyRestoreSwitch(context.Background(), "nonexistent", RestoreSwitchHooks{}); err == nil {
		t.Fatal("unwired switch succeeded")
	}
}
