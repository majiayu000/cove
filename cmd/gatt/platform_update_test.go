package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func testDigest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func testTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	writer := tar.NewWriter(gz)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func packageFixture(t *testing.T, env platformEnvironment, mutate func(*localPackageManifest, map[string][]byte)) (string, string) {
	t.Helper()
	sources := map[string][]byte{"README.md": []byte("source fixture")}
	sourceHashes := map[string]string{"README.md": testDigest(sources["README.md"])}
	sourceJSON, _ := json.Marshal(sourceHashes)
	sourceID := testDigest(sourceJSON)
	files := map[string][]byte{"bin/gatt": []byte("new binary"), "bin/source-snapshot.tar.gz": testTar(t, sources), "web/dist/index.html": []byte("embedded")}
	if env.OS == "windows" {
		files["bin/gatt.exe"] = files["bin/gatt"]
		delete(files, "bin/gatt")
	}
	artifacts := map[string]string{}
	for name, data := range files {
		artifacts[name] = testDigest(data)
	}
	evidence := map[string]any{"source_id": sourceID, "source_sha256": sourceHashes, "artifact_sha256": artifacts}
	files["bin/build-evidence.json"], _ = json.Marshal(evidence)
	manifest := localPackageManifest{Format: "cove-local-package-v1", Version: "dev", BuildID: sourceID, SourceID: sourceID, GoVersion: "go1.26.2", NodeVersion: "unrecorded", OS: env.OS, Arch: env.Arch, AdapterVersion: "fixture", WebEmbedded: true, ManualOnly: true, License: "unlicensed-development-artifact", MinDataSchema: 0, MaxDataSchema: 2, DataSchema: 2, Files: map[string]packageFile{}}
	for name, data := range files {
		manifest.Files[name] = packageFile{testDigest(data), int64(len(data))}
	}
	if mutate != nil {
		mutate(&manifest, files)
	}
	files["manifest.json"], _ = json.Marshal(manifest)
	filename := filepath.Join(privateTempDir(t), "package.tar.gz")
	data := testTar(t, files)
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	return filename, testDigest(data)
}
func TestSpecUpdateValidateBeforeStage(t *testing.T) {
	env := fakePlatform(t, "darwin")
	for _, tc := range []struct {
		name   string
		mutate func(*localPackageManifest, map[string][]byte)
	}{{"platform", func(m *localPackageManifest, _ map[string][]byte) { m.OS = "windows" }}, {"schema", func(m *localPackageManifest, _ map[string][]byte) { m.DataSchema = 3 }}, {"traversal", func(_ *localPackageManifest, f map[string][]byte) { f["../outside"] = []byte("bad") }}, {"hash", func(_ *localPackageManifest, f map[string][]byte) { f["bin/gatt"] = []byte("tampered") }}, {"evidence", func(m *localPackageManifest, f map[string][]byte) {
		f["bin/build-evidence.json"] = []byte(`{"source_id":"wrong"}`)
		m.Files["bin/build-evidence.json"] = packageFile{testDigest(f["bin/build-evidence.json"]), int64(len(f["bin/build-evidence.json"]))}
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			filename, hash := packageFixture(t, env, tc.mutate)
			if _, err := env.prepareUpdate(filename, hash); err == nil {
				t.Fatal("bad package prepared")
			}
			matches, _ := filepath.Glob(filepath.Join(filepath.Dir(env.Executable), ".cove-update-*"))
			if len(matches) != 0 {
				t.Fatal("bad package staged")
			}
			data, _ := os.ReadFile(env.Executable)
			if string(data) != "old binary" {
				t.Fatal("old binary changed")
			}
		})
	}
	filename, _ := packageFixture(t, env, nil)
	if _, err := env.prepareUpdate(filename, strings.Repeat("0", 64)); err == nil {
		t.Fatal("untrusted hash accepted")
	}
}
func TestSpecUpdatePrepareApplyAndRollback(t *testing.T) {
	env := fakePlatform(t, "darwin")
	db, err := sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=2;CREATE TABLE data(value TEXT);INSERT INTO data VALUES('retained')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	filename, hash := packageFixture(t, env, nil)
	journalPath, err := env.prepareUpdate(filename, hash)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := readUpdateJournal(journalPath, env)
	if err != nil || journal.OldSchema != 2 || journal.Phase != "prepared" {
		t.Fatal(journal, err)
	}
	old, _ := os.ReadFile(env.Executable)
	if string(old) != "old binary" {
		t.Fatal("prepare replaced old")
	}
	lock, err := lockDataDir(env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = env.applyUpdate(journalPath, false); err == nil {
		t.Fatal("running owner replaced")
	}
	lock.Close()
	if err = env.applyUpdate(journalPath, false); err != nil {
		t.Fatal(err)
	}
	journal, _ = readUpdateJournal(journalPath, env)
	if journal.Phase != "replaced_pending_start" {
		t.Fatal(journal.Phase)
	}
	if err = env.applyUpdate(journalPath, false); err != nil {
		t.Fatal("not idempotent", err)
	}
	if err = env.applyUpdate(journalPath, true); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(env.Executable)
	if string(current) != "old binary" {
		t.Fatal("rollback binary wrong")
	}
	db, _ = sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
	defer db.Close()
	var retained string
	if err = db.QueryRow("SELECT value FROM data").Scan(&retained); err != nil || retained != "retained" {
		t.Fatal("data altered", retained, err)
	}
}
func TestSpecUpdateCannotRollbackNewRunOrSchema(t *testing.T) {
	for _, mode := range []string{"new-run", "schema"} {
		t.Run(mode, func(t *testing.T) {
			env := fakePlatform(t, "darwin")
			filename, hash := packageFixture(t, env, nil)
			journalPath, err := env.prepareUpdate(filename, hash)
			if err != nil {
				t.Fatal(err)
			}
			if err = env.applyUpdate(journalPath, false); err != nil {
				t.Fatal(err)
			}
			if mode == "new-run" {
				record := runtimePrivate{runtimeInstance{StartedAt: time.Now().Add(time.Second)}, "synthetic"}
				_ = writePrivate(filepath.Join(env.DataDir, "runtime.json"), mustPlatformJSON(record))
			} else {
				db, err := sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("PRAGMA user_version=3")
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = env.applyUpdate(journalPath, true); err == nil {
				t.Fatal("unsafe rollback allowed")
			}
			current, _ := os.ReadFile(env.Executable)
			if string(current) != "new binary" {
				t.Fatal("failure altered current binary")
			}
		})
	}
}
func TestSpecUpdateJournalRecoveryAfterReplace(t *testing.T) {
	env := fakePlatform(t, "darwin")
	filename, hash := packageFixture(t, env, nil)
	journalPath, err := env.prepareUpdate(filename, hash)
	if err != nil {
		t.Fatal(err)
	}
	journal, _ := readUpdateJournal(journalPath, env)
	if err = copyPlatformFile(journal.Target, journal.Backup); err != nil {
		t.Fatal(err)
	}
	journal.Phase = "backed_up"
	if err = writePrivate(journalPath, mustPlatformJSON(journal)); err != nil {
		t.Fatal(err)
	}
	if err = copyPlatformFile(journal.NewBinary, journal.Target); err != nil {
		t.Fatal(err)
	}
	if err = env.applyUpdate(journalPath, false); err != nil {
		t.Fatal(err)
	}
	journal, _ = readUpdateJournal(journalPath, env)
	if journal.Phase != "replaced_pending_start" {
		t.Fatal("interrupted replacement not recognized")
	}
}

func TestSpecWindowsUpdateStagesExecutable(t *testing.T) {
	env := fakePlatform(t, "windows")
	filename, hash := packageFixture(t, env, nil)
	journalPath, err := env.prepareUpdate(filename, hash)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := readUpdateJournal(journalPath, env)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(journal.NewBinary) != "gatt.exe" || filepath.Base(journal.Updater) != "gatt-updater.exe" {
		t.Fatal("Windows staged executables must retain the exe extension")
	}
	content, err := os.ReadFile(journal.NewBinary)
	if err != nil || testDigest(content) != journal.NewBinarySHA {
		t.Fatal("Windows staged binary does not match package evidence")
	}
}
