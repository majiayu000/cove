package app

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func operationsApp(t *testing.T) *App {
	t.Helper()
	a := contractApp(t, nil)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.Config.DataDir = dir
	src, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	src.BaseURL = "https://synthetic.invalid/v1"
	src.NativeProtocol = "responses"
	src.Provider = "openai_compatible"
	if err = a.Store.saveSource(src); err != nil {
		t.Fatal(err)
	}
	return a
}
func operationsCall(a *App, method, path string, body []byte, contentType, session string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:5569"+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer "+session)
	w := httptest.NewRecorder()
	if !a.operationsExtraAPI(w, r) {
		panic("test path is outside operationsExtraAPI")
	}
	return w
}
func operationsJSON(a *App, method, path string, v any) *httptest.ResponseRecorder {
	var b []byte
	if v != nil {
		b, _ = json.Marshal(v)
	}
	return operationsCall(a, method, path, b, "application/json", "test-session")
}
func mustOperationsStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status=%d want=%d body=%s", w.Code, code, w.Body.String())
	}
}
func operationDone(t *testing.T, a *App, w *httptest.ResponseRecorder) Operation {
	t.Helper()
	mustOperationsStatus(t, w, 202)
	var op Operation
	if json.Unmarshal(w.Body.Bytes(), &op) != nil {
		t.Fatal("invalid operation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var b string
		if err := a.Store.DB.QueryRow("SELECT data FROM operations WHERE id=?", op.ID).Scan(&b); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal([]byte(b), &op) != nil {
			t.Fatal("invalid persisted operation")
		}
		if op.State != "running" {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return op
}
func operationArtifact(t *testing.T, op Operation) string {
	t.Helper()
	if op.State != "succeeded" {
		t.Fatalf("operation state %s error %s", op.State, op.Error)
	}
	raw, _ := json.Marshal(op.Result)
	var result struct {
		ArtifactID string `json:"artifact_id"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ArtifactID == "" {
		t.Fatal("missing artifact")
	}
	return result.ArtifactID
}
func seedOperationsRecord(t *testing.T, a *App, identity string) Record {
	t.Helper()
	source, err := a.Store.source("source")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ended := now.Add(time.Second)
	input, output := int64(10), int64(5)
	record := Record{ID: identity, Protocol: "responses", Origin: "client", KeyID: "key", SourceID: source.ID, AccountID: source.AccountID, Generation: source.Generation, AccountGeneration: source.AccountGeneration, Status: "completed", Started: now, Ended: &ended, DurationMS: 1000, HTTPStatus: 200, ErrorSummary: "SYNTHETIC_PRIVATE_ERROR_SENTINEL", UpstreamRequestID: "SYNTHETIC_PRIVATE_ID_SENTINEL", Usage: Usage{Input: &input, Output: &output}, Completeness: "complete"}
	if err = a.Store.record(record); err != nil {
		t.Fatal(err)
	}
	return record
}
func archiveContents(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name], err = io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func writeTestArchive(t *testing.T, manifest backupManifest, db []byte, extra *tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	writer := tar.NewWriter(&b)
	encoded, _ := json.Marshal(manifest)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(encoded))}); err != nil {
		t.Fatal(err)
	}
	writer.Write(encoded)
	if err := writer.WriteHeader(&tar.Header{Name: "db.sqlite", Mode: 0600, Size: int64(len(db))}); err != nil {
		t.Fatal(err)
	}
	writer.Write(db)
	if extra != nil {
		if err := writer.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	return b.Bytes()
}

func TestSpecBackupMetadataSnapshotAndRestore(t *testing.T) {
	a := operationsApp(t)
	rec := seedOperationsRecord(t, a, "request_in_wal")
	if _, err := a.Store.DB.Exec("UPDATE accounts SET credential_ref='SYNTHETIC_CREDENTIAL_REF_SENTINEL',data=json_set(data,'$.unknown_secret','SYNTHETIC_ACCOUNT_SECRET_SENTINEL')"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("UPDATE requests SET data=json_set(data,'$.output','SYNTHETIC_SAVED_OUTPUT_SENTINEL') WHERE id=?", rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("INSERT INTO bindings(response_id,key_id,source_id,generation,account_generation,model,request_id) VALUES(?,?,?,?,?,?,?)", "old_resource", "key", "source", 1, 1, "fixture-model", rec.ID); err != nil {
		t.Fatal(err)
	}
	op := operationDone(t, a, operationsJSON(a, "POST", "/admin/backups", map[string]string{"mode": "metadata"}))
	artifact := operationArtifact(t, op)
	downloaded := operationsJSON(a, "GET", "/admin/artifacts/"+artifact, nil)
	mustOperationsStatus(t, downloaded, 200)
	if downloaded.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("backup download must not cache")
	}
	for _, secret := range []string{"SYNTHETIC_CREDENTIAL_REF_SENTINEL", "SYNTHETIC_ACCOUNT_SECRET_SENTINEL", "SYNTHETIC_SAVED_OUTPUT_SENTINEL", "SYNTHETIC_PRIVATE_ERROR_SENTINEL", "SYNTHETIC_PRIVATE_ID_SENTINEL", digest("test-client-key")} {
		if bytes.Contains(downloaded.Body.Bytes(), []byte(secret)) {
			t.Fatalf("metadata backup contains synthetic secret %s", secret)
		}
	}
	contents := archiveContents(t, downloaded.Body.Bytes())
	var manifest backupManifest
	if json.Unmarshal(contents["manifest.json"], &manifest) != nil || manifest.SecretIncluded || manifest.Mode != "metadata" {
		t.Fatal("invalid manifest")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "new-data")
	previewResponse := operationsCall(a, "POST", "/admin/restore-preview?target_dir="+target, downloaded.Body.Bytes(), "application/x-tar", "test-session")
	mustOperationsStatus(t, previewResponse, 200)
	var preview restorePreview
	if json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil {
		t.Fatal("invalid preview")
	}
	if preview.Counts["requests"] != 1 || preview.Counts["source_models"] != 1 || preview.MissingCredentials != 1 {
		t.Fatalf("snapshot lost committed WAL data: %+v", preview.Counts)
	}
	denied := operationsJSON(a, "POST", "/admin/restore-apply", map[string]any{"preview_id": preview.ID, "target_hash": preview.TargetHash, "prepare_only": false})
	mustOperationsStatus(t, denied, 422)
	applied := operationsJSON(a, "POST", "/admin/restore-apply", map[string]any{"preview_id": preview.ID, "target_hash": preview.TargetHash, "prepare_only": true})
	mustOperationsStatus(t, applied, 200)
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(target, "gatt.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var refs, revoked, bindings, admin int
	if err = db.QueryRow("SELECT count(*) FROM accounts WHERE credential_ref!='' OR json_extract(data,'$.auth_state')!='needs_reauth'").Scan(&refs); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT count(*) FROM client_keys WHERE json_extract(data,'$.revoked')=1 AND digest LIKE 'archived_%'").Scan(&revoked)
	db.QueryRow("SELECT count(*) FROM bindings").Scan(&bindings)
	db.QueryRow("SELECT count(*) FROM settings WHERE key='admin_digest'").Scan(&admin)
	if refs != 0 || revoked != 1 || bindings != 0 || admin != 0 {
		t.Fatalf("restored authentication remains usable refs=%d revoked=%d bindings=%d admin=%d", refs, revoked, bindings, admin)
	}
	original, err := a.Store.keyByDigest(digest("test-client-key"))
	if err != nil || original.Revoked {
		t.Fatal("backup modified original Key")
	}
	var originalCount int
	a.Store.DB.QueryRow("SELECT count(*) FROM bindings").Scan(&originalCount)
	if originalCount != 1 {
		t.Fatal("restore modified original bindings")
	}
	assertPrivateTestPermissions(t, target, 0700)
}
func TestSpecBackupRejectsInvalidArchiveAndTarget(t *testing.T) {
	a := operationsApp(t)
	seedOperationsRecord(t, a, "backup_failure_record")
	path := filepath.Join(t.TempDir(), "backup.tar")
	manifest, err := a.metadataBackup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := archiveContents(t, raw)
	for _, test := range []struct {
		name     string
		manifest backupManifest
		header   *tar.Header
	}{{"bad_hash", func() backupManifest {
		v := manifest
		v.Files = append([]backupFile{}, v.Files...)
		v.Files[0].SHA256 = strings.Repeat("0", 64)
		return v
	}(), nil}, {"bad_schema", func() backupManifest { v := manifest; v.SchemaVersion = 999; return v }(), nil}, {"full_mode", func() backupManifest { v := manifest; v.Mode = "full"; v.SecretIncluded = true; return v }(), nil}, {"traversal", manifest, &tar.Header{Name: "../escaped", Size: 0}}, {"absolute", manifest, &tar.Header{Name: "/escaped", Size: 0}}, {"symlink", manifest, &tar.Header{Name: "secret-link", Typeflag: tar.TypeSymlink, Linkname: "/private"}}} {
		t.Run(test.name, func(t *testing.T) {
			folder := t.TempDir()
			_, err := unpackMetadataArchive(context.Background(), bytes.NewReader(writeTestArchive(t, test.manifest, contents["db.sqlite"], test.header)), folder)
			if err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	mustOperationsStatus(t, operationsJSON(a, "POST", "/admin/backups", map[string]string{"mode": "full"}), 422)
	mustOperationsStatus(t, operationsJSON(a, "POST", "/admin/backups", map[string]any{"mode": "metadata", "encrypt": true}), 422)
	mustOperationsStatus(t, operationsCall(a, "GET", "/admin/artifacts/../../config.json", nil, "", "test-session"), 404)
	if _, err := targetDirectoryHash(a.Config.DataDir, a.Config.DataDir); err == nil {
		t.Fatal("existing data directory accepted")
	}
	if _, err := targetDirectoryHash(filepath.Join(a.Config.DataDir, "child"), a.Config.DataDir); err == nil {
		t.Fatal("child of current directory accepted")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "created-later")
	hash, err := targetDirectoryHash(target, a.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if next, err := targetDirectoryHash(target, a.Config.DataDir); err == nil && next == hash {
		t.Fatal("target change accepted")
	}
	if _, err = a.Store.DB.Exec("CREATE TABLE unreviewed_private_table(secret TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.metadataBackup(context.Background(), filepath.Join(t.TempDir(), "blocked.tar")); err == nil {
		t.Fatal("unreviewed schema exported")
	}
}
func TestSpecBackupArtifactOwnershipExpiryAndFailure(t *testing.T) {
	a := operationsApp(t)
	op := operationDone(t, a, operationsJSON(a, "POST", "/admin/backups", map[string]string{}))
	aid := operationArtifact(t, op)
	denied := operationsCall(a, "GET", "/admin/artifacts/"+aid, nil, "", "different-session")
	mustOperationsStatus(t, denied, 404)
	descriptor, folder, err := a.readLocalArtifact(aid, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	descriptor.CreatedAt = time.Now().Add(-25 * time.Hour)
	if err = writePrivateJSON(filepath.Join(folder, "descriptor.json"), descriptor); err != nil {
		t.Fatal(err)
	}
	mustOperationsStatus(t, operationsJSON(a, "GET", "/admin/artifacts/"+aid, nil), 404)
	if _, err = os.Stat(folder); !os.IsNotExist(err) {
		t.Fatal("expired artifact retained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.metadataBackup(ctx, filepath.Join(t.TempDir(), "cancelled.tar")); err == nil {
		t.Fatal("cancelled snapshot reported success")
	}
}

func TestSpecObservabilityRealCountsAndSafeLabels(t *testing.T) {
	a := operationsApp(t)
	record := seedOperationsRecord(t, a, "metrics_record")
	record.Protocol = "SYNTHETIC_SECRET_PROTOCOL"
	if err := a.Store.record(record); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.record(record); err != nil {
		t.Fatal(err)
	}
	response := operationsJSON(a, "GET", "/admin/metrics", nil)
	mustOperationsStatus(t, response, 200)
	body := response.Body.String()
	if !strings.Contains(body, "cove_requests_total{protocol=\"other\",operation=\"inference\",outcome=\"completed\"} 1") || !strings.Contains(body, "cove_attempts_total{provider=\"openai_compatible\",outcome=\"completed\"} 1") {
		t.Fatalf("duplicate terminal counted twice or real rows absent: %s", body)
	}
	for _, secret := range []string{"SYNTHETIC_SECRET_PROTOCOL", "SYNTHETIC_PRIVATE_ERROR_SENTINEL", "metrics_record", "test-source-secret", "test-client-key", "upstream.invalid"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leaked synthetic private value %s", secret)
		}
	}
	if !strings.Contains(body, "cove_ttft_seconds_count 0") {
		t.Fatal("unknown semantic TTFT treated as measured")
	}
	doctor := operationsJSON(a, "GET", "/admin/doctor", nil)
	mustOperationsStatus(t, doctor, 200)
	if strings.Contains(doctor.Body.String(), a.Config.DataDir) || strings.Contains(doctor.Body.String(), "synthetic.invalid") {
		t.Fatal("doctor leaked selected path/endpoint")
	}
}
func TestSpecObservabilityAlertDedupDismissAndRecovery(t *testing.T) {
	a := operationsApp(t)
	if _, err := a.Store.DB.Exec("UPDATE accounts SET data=json_set(data,'$.auth_state','needs_reauth')"); err != nil {
		t.Fatal(err)
	}
	var alerts []localAlert
	for i := 0; i < 100; i++ {
		w := operationsJSON(a, "GET", "/admin/alerts", nil)
		mustOperationsStatus(t, w, 200)
		var response struct {
			Items []localAlert `json:"items"`
		}
		json.Unmarshal(w.Body.Bytes(), &response)
		alerts = response.Items
	}
	if len(alerts) != 1 || alerts[0].Count != 100 {
		t.Fatalf("fault not deduplicated %+v", alerts)
	}
	alert := alerts[0]
	stale := operationsJSON(a, "POST", "/admin/alerts/"+alert.ID+"/dismiss", map[string]int{"version": alert.Version - 1})
	mustOperationsStatus(t, stale, 409)
	dismissed := operationsJSON(a, "POST", "/admin/alerts/"+alert.ID+"/dismiss", map[string]int{"version": alert.Version})
	mustOperationsStatus(t, dismissed, 200)
	var state string
	a.Store.DB.QueryRow("SELECT json_extract(data,'$.auth_state') FROM accounts").Scan(&state)
	if state != "needs_reauth" {
		t.Fatal("dismissal changed account fault")
	}
	if _, err := a.Store.DB.Exec("UPDATE accounts SET data=json_set(data,'$.auth_state','configured')"); err != nil {
		t.Fatal(err)
	}
	resolved := operationsJSON(a, "GET", "/admin/alerts?state=resolved", nil)
	mustOperationsStatus(t, resolved, 200)
	if !strings.Contains(resolved.Body.String(), "resolved_at") {
		t.Fatal("recovery absent")
	}
	if _, err := a.Store.DB.Exec("UPDATE accounts SET data=json_set(data,'$.auth_state','needs_reauth')"); err != nil {
		t.Fatal(err)
	}
	again := operationsJSON(a, "GET", "/admin/alerts?state=active", nil)
	mustOperationsStatus(t, again, 200)
	var response struct {
		Items []localAlert `json:"items"`
	}
	json.Unmarshal(again.Body.Bytes(), &response)
	if len(response.Items) != 1 || response.Items[0].ID == alert.ID {
		t.Fatal("new fault cycle did not get a new alert")
	}
}

func configPackageForTest(t *testing.T, a *App) configTransfer {
	t.Helper()
	pack, err := a.collectConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pack.Settings = map[string]settingsChanges{}
	if len(pack.Sources) != 1 || len(pack.Models) != 1 {
		t.Fatal("fixture configuration incomplete")
	}
	model := pack.Models[0].LocalID
	pack.Routes = []transferRoute{{LocalID: "route_fixture", Route: Route{Name: "import route", Enabled: true, Version: 1, Strategy: "priority", MaxAttempts: 1, Members: []RouteMember{{ModelID: model, Weight: 1}}}}}
	pack.Aliases = []transferAlias{{LocalID: "alias_fixture", Alias: Alias{PublicModel: "imported-model", RouteID: "route_fixture", Version: 1}}}
	return pack
}
func configPreviewForTest(t *testing.T, a *App, pack configTransfer) configTransferPreview {
	t.Helper()
	w := operationsJSON(a, "POST", "/admin/config-transfer/import-preview", pack)
	mustOperationsStatus(t, w, 200)
	var p configTransferPreview
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || p.ID == "" {
		t.Fatalf("no preview: %s", w.Body.String())
	}
	return p
}
func configApplyForTest(a *App, p configTransferPreview, resolutions map[string]string) *httptest.ResponseRecorder {
	copyResolutions := map[string]string{}
	for k, v := range resolutions {
		copyResolutions[k] = v
	}
	for _, entity := range p.Entities {
		if entity.Kind == "model" && entity.Conflict {
			copyResolutions[entity.LocalID] = "create_new"
		}
	}
	return operationsJSON(a, "POST", "/admin/config-transfer/import-apply", map[string]any{"preview_id": p.ID, "expected_config_version": p.ExpectedConfigVersion, "selected_resolutions": copyResolutions})
}
func tableCount(t *testing.T, a *App, table string) int {
	t.Helper()
	var n int
	if err := a.Store.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestSpecConfigTransferClosureCreateAndNoSecrets(t *testing.T) {
	a := operationsApp(t)
	pack := configPackageForTest(t, a)
	pack.Sources[0].Source.Name = "distinct imported source"
	selected, err := selectConfigDependencies(pack, []string{"alias_fixture"}, true)
	if err != nil || len(selected.Sources) != 1 || len(selected.Models) != 1 || len(selected.CredentialPlaceholders) != 1 {
		t.Fatalf("dependency closure failed %v", err)
	}
	if _, err = selectConfigDependencies(pack, []string{"alias_fixture"}, false); err == nil {
		t.Fatal("missing dependency export accepted")
	}
	p := configPreviewForTest(t, a, selected)
	mustOperationsStatus(t, configApplyForTest(a, p, nil), 200)
	if tableCount(t, a, "sources") != 2 || tableCount(t, a, "source_models") != 2 || tableCount(t, a, "routes") != 1 || tableCount(t, a, "model_aliases") != 1 {
		t.Fatal("configuration closure not fully created")
	}
	var missing int
	a.Store.DB.QueryRow("SELECT count(*) FROM accounts WHERE credential_ref='' AND json_extract(data,'$.credential_present')=0").Scan(&missing)
	if missing != 1 {
		t.Fatal("import changed credential state")
	}
	exported := operationsJSON(a, "POST", "/admin/config-transfer/export", map[string]any{"include_dependencies": true})
	mustOperationsStatus(t, exported, 200)
	for _, secret := range []string{"admin_digest", "test-source-secret", "test-client-key", digest("test-client-key"), "credential_ref", "SYNTHETIC_PRIVATE"} {
		if strings.Contains(exported.Body.String(), secret) {
			t.Fatalf("config export leaked %s", secret)
		}
	}
}
func TestSpecConfigTransferUnknownConflictStaleAndRollback(t *testing.T) {
	a := operationsApp(t)
	pack := configPackageForTest(t, a)
	p := configPreviewForTest(t, a, pack)
	mustOperationsStatus(t, configApplyForTest(a, p, nil), 409)
	resolutions := map[string]string{pack.Sources[0].LocalID: "create_new"}
	if _, err := a.Store.DB.Exec("UPDATE source_models SET data=json_set(data,'$.version',2)"); err != nil {
		t.Fatal(err)
	}
	mustOperationsStatus(t, configApplyForTest(a, p, resolutions), 409)
	pack.Models[0].Model.SourceID = "missing_source"
	mustOperationsStatus(t, operationsJSON(a, "POST", "/admin/config-transfer/import-preview", pack), 422)
	unknown := operationsCall(a, "POST", "/admin/config-transfer/import-preview", []byte(`{"format":"cove-config","version":1,"new_secret_field":"SYNTHETIC_SENTINEL"}`), "application/json", "test-session")
	mustOperationsStatus(t, unknown, 200)
	if !strings.Contains(unknown.Body.String(), "unsupported") || strings.Contains(unknown.Body.String(), "SYNTHETIC_SENTINEL") {
		t.Fatal("unsupported field hidden or echoed")
	}
	pack = configPackageForTest(t, a)
	pack.Sources[0].Source.Name = "transaction source"
	p = configPreviewForTest(t, a, pack)
	if _, err := a.Store.DB.Exec("CREATE TRIGGER reject_import_audit BEFORE INSERT ON settings WHEN NEW.key LIKE 'config_import_audit_%' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	mustOperationsStatus(t, configApplyForTest(a, p, nil), 503)
	if tableCount(t, a, "sources") != 1 || tableCount(t, a, "routes") != 0 || tableCount(t, a, "model_aliases") != 0 {
		t.Fatal("transaction failure published partial configuration")
	}
}
func TestSpecExportsStreamingSnapshotRedactionAndFormulaInjection(t *testing.T) {
	a := operationsApp(t)
	seedOperationsRecord(t, a, "=FORMULA()")
	op := operationDone(t, a, operationsJSON(a, "POST", "/admin/exports", map[string]any{"format": "csv", "filters": map[string]string{}}))
	aid := operationArtifact(t, op)
	w := operationsJSON(a, "GET", "/admin/artifacts/"+aid, nil)
	mustOperationsStatus(t, w, 200)
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1][0] != "'=FORMULA()" {
		t.Fatalf("formula text not escaped %+v", rows)
	}
	for _, secret := range []string{"SYNTHETIC_PRIVATE_ERROR_SENTINEL", "SYNTHETIC_PRIVATE_ID_SENTINEL", "test-client-key"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("request export leaked synthetic secret")
		}
	}
	for _, text := range []string{"=a", "+a", "-a", "@a", "\ta", "\ra", " \t=a"} {
		if !strings.HasPrefix(csvSafeText(text), "'") {
			t.Fatalf("unsafe CSV prefix %q", text)
		}
	}
	if exportCSVValue(int64(-7)) != "-7" {
		t.Fatal("numeric cell treated as formula text")
	}
	mustOperationsStatus(t, operationsJSON(a, "POST", "/admin/exports", map[string]any{"format": "json", "columns": []string{"credential"}}), 422)
	mustOperationsStatus(t, operationsJSON(a, "POST", "/admin/exports", map[string]any{"format": "json", "filters": map[string]string{"raw_sql": "true"}}), 422)
}

func TestSpecBackupPreservesPendingLedgerAndRemovesClientSnapshots(t *testing.T) {
	a := operationsApp(t)
	record := seedOperationsRecord(t, a, "ledger_backup_request")
	const schema = `CREATE TABLE IF NOT EXISTS budgets(id TEXT PRIMARY KEY,scope_kind TEXT NOT NULL,scope_id TEXT,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS reservations(request_id TEXT NOT NULL REFERENCES requests(id),budget_id TEXT NOT NULL REFERENCES budgets(id),period_start TEXT NOT NULL,period_end TEXT NOT NULL,reserved_amount TEXT NOT NULL,allocation_json TEXT NOT NULL,settled_amount TEXT NOT NULL,pending_amount TEXT NOT NULL,currency TEXT NOT NULL,budget_version INTEGER NOT NULL,status TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(request_id,budget_id,period_start));
 CREATE TABLE IF NOT EXISTS accounting_audit(id TEXT PRIMARY KEY,request_id TEXT REFERENCES requests(id),created_at TEXT NOT NULL,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS client_previews(id TEXT PRIMARY KEY,expires_at TEXT NOT NULL,secret_ref TEXT NOT NULL,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS client_changes(id TEXT PRIMARY KEY,preview_id TEXT UNIQUE NOT NULL,secret_ref TEXT NOT NULL,data TEXT NOT NULL);`
	if _, err := a.Store.DB.Exec(schema); err != nil {
		t.Fatal(err)
	}
	budget := `{"id":"budget1","name":"monthly budget","scope":{"kind":"instance","unexpected":"SYNTHETIC_SCOPE_PRIVATE"},"currency":"USD","amount_limit":"100","mode":"soft","period":{"kind":"monthly","timezone":"UTC"},"enabled":true,"version":1,"created_at":"2026-09-30T00:00:00Z","credential":"SYNTHETIC_BUDGET_PRIVATE"}`
	if _, err := a.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,data) VALUES('budget1','instance',?)", budget); err != nil {
		t.Fatal(err)
	}
	allocation := `{"attempt1":{"reserved":"12.30","state":"pending","provenance":"soft estimate","credential":"SYNTHETIC_ALLOCATION_PRIVATE"}}`
	if _, err := a.Store.DB.Exec("INSERT INTO reservations VALUES(?, 'budget1','2026-09-01T00:00:00Z','2026-10-01T00:00:00Z','12.30',?,'0','12.30','USD',1,'pending_reconciliation','2026-09-30T00:00:00Z')", record.ID, allocation); err != nil {
		t.Fatal(err)
	}
	audit := map[string]any{"kind": "manual_reconciliation", "version": 1, "provenance": "manual_reported", "attempt_costs": []map[string]any{{"attempt_id": "attempt1", "currency": "USD", "amount": "12.30", "reason": "SYNTHETIC_RECONCILIATION_REASON", "evidence_ref": "SYNTHETIC_PRIVATE_PATH"}}, "before": json.RawMessage(allocation), "after": json.RawMessage(allocation), "unknown": "SYNTHETIC_AUDIT_PRIVATE"}
	if _, err := a.Store.DB.Exec("INSERT INTO accounting_audit VALUES('audit1',?,'2026-09-30T00:00:00Z',?)", record.ID, encode(audit)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("INSERT INTO client_previews VALUES('preview1','2026-10-01','SYNTHETIC_CLIENT_REF','SYNTHETIC_CLIENT_PATH'); INSERT INTO client_changes VALUES('change1','preview1','SYNTHETIC_CLIENT_CHANGE_REF','SYNTHETIC_CLIENT_CHANGE_PATH')"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ledger.tar")
	if _, err := a.metadataBackup(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"SYNTHETIC_SCOPE_PRIVATE", "SYNTHETIC_BUDGET_PRIVATE", "SYNTHETIC_ALLOCATION_PRIVATE", "SYNTHETIC_RECONCILIATION_REASON", "SYNTHETIC_PRIVATE_PATH", "SYNTHETIC_AUDIT_PRIVATE", "SYNTHETIC_CLIENT_REF", "SYNTHETIC_CLIENT_PATH", "SYNTHETIC_CLIENT_CHANGE_REF", "SYNTHETIC_CLIENT_CHANGE_PATH"} {
		if bytes.Contains(raw, []byte(sentinel)) {
			t.Fatalf("metadata backup leaked %s", sentinel)
		}
	}
	contents := archiveContents(t, raw)
	dbPath := filepath.Join(t.TempDir(), "ledger.sqlite")
	if err = os.WriteFile(dbPath, contents["db.sqlite"], 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pending, state string
	if err = db.QueryRow("SELECT pending_amount,status FROM reservations").Scan(&pending, &state); err != nil {
		t.Fatal(err)
	}
	if pending != "12.30" || state != "pending_reconciliation" {
		t.Fatal("unknown accounting amount became spendable")
	}
	for _, table := range []string{"client_previews", "client_changes"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("client snapshots retained")
		}
	}
	if err := checkBackupDB(db, 2); err != nil {
		t.Fatal(err)
	}
}
func TestSpecBackupRejectsExecutableSchemaAndCleansExpiredArtifacts(t *testing.T) {
	a := operationsApp(t)
	if _, err := a.Store.DB.Exec("CREATE TRIGGER restore_auth AFTER UPDATE ON accounts BEGIN UPDATE accounts SET credential_ref='synthetic-revived'; END"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.metadataBackup(context.Background(), filepath.Join(t.TempDir(), "unsafe.tar")); err == nil {
		t.Fatal("executable imported schema could revive credentials")
	}
	if _, err := a.Store.DB.Exec("DROP TRIGGER restore_auth"); err != nil {
		t.Fatal(err)
	}
	descriptor, folder, err := a.newLocalArtifact("test-session", "backup", "application/x-tar", "metadata.tar")
	if err != nil {
		t.Fatal(err)
	}
	descriptor.CreatedAt = time.Now().Add(-25 * time.Hour)
	if err = writePrivateJSON(filepath.Join(folder, "descriptor.json"), descriptor); err != nil {
		t.Fatal(err)
	}
	if err = a.cleanupOperationArtifacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(folder); !os.IsNotExist(err) {
		t.Fatal("maintenance did not delete expired artifact")
	}
}

func TestSpecRestoreTargetTracksIdentityNotAccessTimes(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "restore-target")
	current := t.TempDir()
	before, err := targetDirectoryHash(target, current)
	if err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(-time.Hour)
	if err := os.Chtimes(parent, changed, changed); err != nil {
		t.Fatal(err)
	}
	after, err := targetDirectoryHash(target, current)
	if err != nil || before != after {
		t.Fatal("parent access/write time falsely invalidated restore preview", err)
	}
	moved := parent + "-replaced"
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	replaced, err := targetDirectoryHash(target, current)
	if err != nil || replaced == before {
		t.Fatal("replaced parent directory retained restore authorization", err)
	}
}
