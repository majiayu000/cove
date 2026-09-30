package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transferFixture(t *testing.T, a *App) configTransfer {
	t.Helper()
	p := configPackageForTest(t, a)
	p.Settings = map[string]settingsChanges{}
	p.Sources[0].Source.Name = "portable source"
	p.Models[0].Model.DisplayName = "portable model"
	b := Budget{Name: "portable budget", Scope: BudgetScope{Kind: "route", ID: "route_fixture"}, Currency: "USD", AmountLimit: "100", Mode: "soft",
		Period: BudgetPeriod{Kind: "calendar_month", Timezone: "America/New_York"}, Enabled: true, Version: 1}
	p.Budgets = []transferBudget{{LocalID: "budget_fixture", Budget: b}}
	price := PriceVersion{ModelID: p.Models[0].LocalID, Currency: "USD", EffectiveAt: time.Now().UTC().Add(-time.Hour), Units: []PriceUnit{{Dimension: "input_token", Amount: "1.25", Per: "1000000"}, {Dimension: "output_token", Amount: "5", Per: "1000000"}}}
	price.Provenance.Kind = "manual"
	price.Provenance.ObservedAt = time.Now().UTC()
	p.Prices = []transferPrice{{LocalID: "price_fixture", Price: price}}
	return p
}

func TestSpecConfigTransferObservationChangesKeepConfigurationCAS(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	preview := transferPreview(t, a, p, "create")
	if _, err := a.Store.DB.Exec("UPDATE sources SET data=json_set(data,'$.quota',json(?),'$.verification',json(?),'$.continuation_verified',1) WHERE id='source'", `{"status":"available","call_used":2}`, `{"status":"passed","capabilities":["text_json"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.DB.Exec("UPDATE source_models SET data=json_set(data,'$.verification','passed','$.verification_results',json('[]'),'$.codex_catalog',json(?))", `{"adapter_version":"synthetic","qualification":"observed"}`); err != nil {
		t.Fatal(err)
	}
	current, err := configurationVersion(context.Background(), a.Store.DB)
	if err != nil || current != preview.ExpectedConfigVersion {
		t.Fatal("runtime observations invalidated configuration preview", err)
	}
	if _, err = a.Store.DB.Exec("UPDATE sources SET data=json_set(data,'$.name','changed configuration','$.version',2) WHERE id='source'"); err != nil {
		t.Fatal(err)
	}
	current, err = configurationVersion(context.Background(), a.Store.DB)
	if err != nil || current == preview.ExpectedConfigVersion {
		t.Fatal("configuration change escaped the preview CAS", err)
	}
}
func transferPreview(t *testing.T, a *App, p any, mode string) configTransferPreview {
	t.Helper()
	w := operationsJSON(a, "POST", "/admin/config-transfer/import-preview?mode="+mode, p)
	mustOperationsStatus(t, w, 200)
	var result configTransferPreview
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" {
		t.Fatalf("preview missing: %s", w.Body.String())
	}
	return result
}
func transferApply(a *App, p configTransferPreview, res map[string]transferResolution) *httptest.ResponseRecorder {
	return operationsJSON(a, "POST", "/admin/config-transfer/import-apply", map[string]any{"preview_id": p.ID, "expected_config_version": p.ExpectedConfigVersion, "selected_resolutions": res})
}
func transferCreateChoices(p configTransferPreview) map[string]transferResolution {
	out := map[string]transferResolution{}
	for _, e := range p.Entities {
		action := "create_new"
		if e.Kind == "settings" || len(e.Unsupported) > 0 {
			action = "skip"
		}
		out[e.LocalID] = transferResolution{Action: action}
	}
	return out
}
func transferReplaceChoices(t *testing.T, p configTransferPreview, ids map[string]string) map[string]transferResolution {
	t.Helper()
	out := map[string]transferResolution{}
	for _, e := range p.Entities {
		out[e.LocalID] = transferResolution{Action: "skip"}
		if target, ok := ids[e.LocalID]; ok {
			found := false
			for _, v := range e.Targets {
				if v.ID == target {
					out[e.LocalID] = transferResolution{Action: "replace", TargetID: target, Version: v.Version, TargetHash: v.Hash}
					found = true
				}
			}
			if !found {
				t.Fatalf("no target %s for %s (%+v)", target, e.LocalID, e.Targets)
			}
		}
	}
	return out
}
func transferVersion(t *testing.T, a *App) string {
	t.Helper()
	v, e := configurationVersion(context.Background(), a.Store.DB)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestSpecConfigTransferFinancialClosureAndTypedExport(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	selected, e := selectConfigDependencies(p, []string{"budget_fixture", "price_fixture"}, true)
	if e != nil || len(selected.Sources) != 1 || len(selected.Models) != 1 || len(selected.Routes) != 1 || len(selected.Budgets) != 1 || len(selected.Prices) != 1 || len(selected.Aliases) != 0 {
		t.Fatalf("financial closure incomplete: %v %+v", e, selected)
	}
	if _, e = selectConfigDependencies(p, []string{"price_fixture"}, false); e == nil {
		t.Fatal("missing price model accepted")
	}
	preview := transferPreview(t, a, selected, "create")
	mustOperationsStatus(t, transferApply(a, preview, transferCreateChoices(preview)), 200)
	if tableCount(t, a, "prices") != 1 || tableCount(t, a, "budgets") != 1 || tableCount(t, a, "accounting_audit") != 1 {
		t.Fatal("financial objects or audit missing")
	}
	var budget Budget
	var raw string
	if e = a.Store.DB.QueryRow("SELECT data FROM budgets").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	_ = json.Unmarshal([]byte(raw), &budget)
	var routeID string
	if e = a.Store.DB.QueryRow("SELECT id FROM routes").Scan(&routeID); e != nil {
		t.Fatal(e)
	}
	if budget.Scope.ID != routeID {
		t.Fatal("budget scope did not map to newly created route")
	}
	var sourceID string
	if e = a.Store.DB.QueryRow("SELECT id FROM sources WHERE id!='source'").Scan(&sourceID); e != nil {
		t.Fatal(e)
	}
	export := operationsJSON(a, "POST", "/admin/config-transfer/export", map[string]any{"selected_entity_ids": []string{budget.ID}, "include_dependencies": true})
	mustOperationsStatus(t, export, 200)
	var exported configTransfer
	_ = json.Unmarshal(export.Body.Bytes(), &exported)
	if len(exported.Budgets) != 1 || len(exported.Sources) != 1 || len(exported.Models) != 1 || len(exported.Routes) != 1 || len(exported.Prices) != 0 || len(exported.Settings) != 0 {
		t.Fatal("export includes unrelated objects")
	}
	src, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	proxy := "http://synthetic-user:synthetic-proxy-secret@127.0.0.1:9000"
	src.ProxyURL = &proxy
	src.Verification.RequestID = "synthetic-request-id"
	src.Quota["access_token"] = "synthetic-quota-token"
	if e = a.Store.saveSource(src); e != nil {
		t.Fatal(e)
	}
	if _, e = a.Store.DB.Exec("UPDATE source_models SET data=json_set(data,'$.verification_results',json('[{\"request_ids\":[\"synthetic-request-secret\"]}]'),'$.metadata_reason','synthetic-metadata-secret') WHERE source_id='source'"); e != nil {
		t.Fatal(e)
	}
	export = operationsJSON(a, "POST", "/admin/config-transfer/export", map[string]any{"selected_entity_ids": []string{"source"}, "include_dependencies": true})
	mustOperationsStatus(t, export, 200)
	for _, secret := range []string{"test-source-secret", "credential_ref", "admin_digest", "synthetic-proxy-secret", "synthetic-request-id", "synthetic-quota-token", "synthetic-request-secret", "synthetic-metadata-secret", "test-client-key", digest("test-client-key")} {
		if strings.Contains(export.Body.String(), secret) {
			t.Fatalf("export leaked known secret field %s", secret)
		}
	}
}
func TestSpecConfigTransferReplaceKeepsCredentialsBindingsAndUnselected(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	p.Aliases = nil
	p.Budgets = nil
	p.Prices = nil
	src, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	models, e := a.Store.models("source")
	if e != nil {
		t.Fatal(e)
	}
	record := seedOperationsRecord(t, a, "transfer-binding-record")
	if e = a.Store.bind(record, "transfer-old-response"); e != nil {
		t.Fatal(e)
	}
	p.Sources[0].Source.BaseURL = "https://synthetic.invalid/new/v1"
	p.Models[0].Model.DisplayName = "replaced model"
	targets := map[string]string{p.Sources[0].LocalID: src.ID, p.Models[0].LocalID: models[0].ID}
	preview := transferPreview(t, a, p, "replace_selected")
	res := transferReplaceChoices(t, preview, targets)
	before := transferVersion(t, a)
	wrong := transferReplaceChoices(t, preview, targets)
	v := wrong[p.Sources[0].LocalID]
	v.Version++
	wrong[p.Sources[0].LocalID] = v
	mustOperationsStatus(t, transferApply(a, preview, wrong), 409)
	if transferVersion(t, a) != before {
		t.Fatal("wrong version mutated configuration")
	}
	mustOperationsStatus(t, transferApply(a, preview, res), 200)
	next, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	if next.AccountID != src.AccountID || next.CredentialRef != src.CredentialRef || next.AccountGeneration != src.AccountGeneration || next.Generation != src.Generation+1 || next.Version != src.Version+1 {
		t.Fatalf("credentials/generation changed incorrectly: %+v", next)
	}
	var bindingGen int
	if e = a.Store.DB.QueryRow("SELECT generation FROM bindings WHERE response_id='transfer-old-response'").Scan(&bindingGen); e != nil {
		t.Fatal(e)
	}
	if bindingGen != src.Generation {
		t.Fatal("old binding moved to new endpoint")
	}
	if tableCount(t, a, "accounts") != 1 || tableCount(t, a, "sources") != 1 || tableCount(t, a, "routes") != 0 {
		t.Fatal("skipped entities were replaced or account created")
	}
	m, e := a.Store.model(models[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	if m.DisplayName != "replaced model" || m.Version != models[0].Version+1 {
		t.Fatal("model replace failed")
	}
}
func TestSpecConfigTransferDependencyConflictCrossOriginAndAtomicRollback(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	preview := transferPreview(t, a, p, "create")
	res := transferCreateChoices(preview)
	res[p.Models[0].LocalID] = transferResolution{Action: "skip"}
	before := transferVersion(t, a)
	mustOperationsStatus(t, transferApply(a, preview, res), 409)
	if transferVersion(t, a) != before {
		t.Fatal("skipped dependency changed configuration")
	}
	// A transaction can insert all object kinds but an audit failure publishes none.
	preview = transferPreview(t, a, p, "create")
	if _, e := a.Store.DB.Exec("CREATE TRIGGER transfer_fail_audit BEFORE INSERT ON settings WHEN NEW.key LIKE 'config_import_audit_%' BEGIN SELECT RAISE(ABORT,'synthetic rollback'); END"); e != nil {
		t.Fatal(e)
	}
	mustOperationsStatus(t, transferApply(a, preview, transferCreateChoices(preview)), 503)
	if transferVersion(t, a) != before || tableCount(t, a, "accounting_audit") != 0 || tableCount(t, a, "accounts") != 1 {
		t.Fatal("audit failure published partial or financial objects")
	}
	if _, e := a.Store.DB.Exec("DROP TRIGGER transfer_fail_audit"); e != nil {
		t.Fatal(e)
	}
	// Cross-origin replacement cannot reuse the old secret for a new destination.
	p.Sources[0].Source.BaseURL = "https://other-synthetic.invalid/v1"
	preview = transferPreview(t, a, p, "replace_selected")
	res = transferReplaceChoices(t, preview, map[string]string{p.Sources[0].LocalID: "source"})
	mustOperationsStatus(t, transferApply(a, preview, res), 409)
	if transferVersion(t, a) != before {
		t.Fatal("cross-origin replacement modified configuration")
	}
}
func TestSpecConfigTransferFinancialCASSettingsAndRetentionPreview(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	for _, kind := range []string{"budget", "price", "settings"} {
		t.Run(kind, func(t *testing.T) {
			preview := transferPreview(t, a, p, "create")
			switch kind {
			case "budget":
				b := p.Budgets[0].Budget
				b.ID = id("budget")
				b.Scope = BudgetScope{Kind: "instance"}
				b.Version = 1
				if _, e := a.Store.DB.Exec("INSERT INTO budgets(id,scope_kind,scope_id,data) VALUES(?,'instance',NULL,?)", b.ID, encode(b)); e != nil {
					t.Fatal(e)
				}
			case "price":
				price := p.Prices[0].Price
				price.ID = id("price")
				models, e := a.Store.models("source")
				if e != nil {
					t.Fatal(e)
				}
				price.ModelID = models[0].ID
				if _, e = a.Store.DB.Exec("INSERT INTO prices(id,model_id,effective_at,data) VALUES(?,?,?,?)", price.ID, price.ModelID, price.EffectiveAt.Format(time.RFC3339Nano), encode(price)); e != nil {
					t.Fatal(e)
				}
			case "settings":
				if _, e := a.Store.DB.Exec("INSERT INTO settings(key,value) VALUES('runtime_settings',?)", encode(runtimeSettings{Version: 2})); e != nil {
					t.Fatal(e)
				}
			}
			mustOperationsStatus(t, transferApply(a, preview, transferCreateChoices(preview)), 409)
		})
	}
	nextConcurrent := 3
	nextRetention := 2
	nextTotal := 11
	settings := configTransfer{Format: "cove-config", Version: 1, Settings: map[string]settingsChanges{"runtime_settings": {MaxConcurrent: &nextConcurrent, RetentionDays: &nextRetention, TotalTimeout: &nextTotal}}}
	preview := transferPreview(t, a, settings, "replace_selected")
	choices := transferReplaceChoices(t, preview, map[string]string{"runtime_settings": "runtime_settings"})
	beforeRetention := a.Config.RetentionDays
	if _, e := a.Store.DB.Exec("CREATE TRIGGER transfer_settings_fail BEFORE INSERT ON settings WHEN NEW.key LIKE 'config_import_audit_%' BEGIN SELECT RAISE(ABORT,'synthetic failure'); END"); e != nil {
		t.Fatal(e)
	}
	mustOperationsStatus(t, transferApply(a, preview, choices), 503)
	if a.Config.MaxConcurrent == nextConcurrent || a.Config.TotalTimeout == nextTotal {
		t.Fatal("runtime config changed before commit")
	}
	if _, e := a.Store.DB.Exec("DROP TRIGGER transfer_settings_fail"); e != nil {
		t.Fatal(e)
	}
	mustOperationsStatus(t, transferApply(a, preview, choices), 200)
	state, e := a.Store.readRuntimeSettings()
	if e != nil {
		t.Fatal(e)
	}
	if a.Config.MaxConcurrent != 3 || cap(a.slots) != 3 || a.Config.TotalTimeout != 11 || a.Config.RetentionDays != beforeRetention || state.RetentionDays == nil || *state.RetentionDays != 2 {
		t.Fatal("portable settings or retention preview wrong")
	}
	if a.Config.Listen != "127.0.0.1:5569" || a.Config.DataDir == "" {
		t.Fatal("local settings replaced")
	}
}
func TestSpecConfigTransferBudgetFrozenFieldsAndImmutablePriceReplacement(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	preview := transferPreview(t, a, p, "create")
	mustOperationsStatus(t, transferApply(a, preview, transferCreateChoices(preview)), 200)
	var route, model, source, budget, price string
	for query, dst := range map[string]*string{"SELECT id FROM routes": &route, "SELECT id FROM sources WHERE id!='source'": &source, "SELECT id FROM source_models WHERE source_id!='source'": &model, "SELECT id FROM budgets": &budget, "SELECT id FROM prices": &price} {
		if e := a.Store.DB.QueryRow(query).Scan(dst); e != nil {
			t.Fatal(e)
		}
	}
	p.Sources[0].Source.Name = "replacement source"
	p.Budgets[0].Budget.AmountLimit = "200"
	p.Prices[0].Price.EffectiveAt = p.Prices[0].Price.EffectiveAt.Add(30 * time.Minute)
	ids := map[string]string{p.Sources[0].LocalID: source, p.Models[0].LocalID: model, "route_fixture": route, "alias_fixture": "imported-model", "budget_fixture": budget, "price_fixture": price}
	preview = transferPreview(t, a, p, "replace_selected")
	mustOperationsStatus(t, transferApply(a, preview, transferReplaceChoices(t, preview, ids)), 200)
	if tableCount(t, a, "prices") != 2 || tableCount(t, a, "budgets") != 1 {
		t.Fatal("price history or budget identity replaced")
	}
	var raw string
	if e := a.Store.DB.QueryRow("SELECT data FROM budgets WHERE id=?", budget).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var b Budget
	_ = json.Unmarshal([]byte(raw), &b)
	if b.Version != 2 || b.AmountLimit != "200" {
		t.Fatal("budget replace version/amount incorrect")
	}
	if e := a.Store.DB.QueryRow("SELECT data FROM prices WHERE id=?", price).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var old PriceVersion
	_ = json.Unmarshal([]byte(raw), &old)
	if !old.EffectiveAt.Equal(p.Prices[0].Price.EffectiveAt.Add(-30 * time.Minute)) {
		t.Fatal("old price mutated")
	}
	before := transferVersion(t, a)
	p.Budgets[0].Budget.Currency = "EUR"
	preview = transferPreview(t, a, p, "replace_selected")
	// Skip price, since reusing its effective time would independently conflict.
	choices := transferReplaceChoices(t, preview, ids)
	choices["price_fixture"] = transferResolution{Action: "skip"}
	mustOperationsStatus(t, transferApply(a, preview, choices), 409)
	if transferVersion(t, a) != before {
		t.Fatal("frozen budget failure left partial replacement")
	}
}
func TestSpecConfigTransferUnsupportedSubsetAndNoRawSecretArtifact(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	data, _ := json.Marshal(p)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	raw["models"].([]any)[0].(map[string]any)["model"].(map[string]any)["future_secret_field"] = "SYNTHETIC_UNSUPPORTED_SECRET"
	preview := transferPreview(t, a, raw, "create")
	if len(preview.Unsupported) == 0 {
		t.Fatal("unknown object field hidden")
	}
	// Explicitly select the supported source and skip every dependent object.
	choices := map[string]transferResolution{}
	for _, e := range preview.Entities {
		choices[e.LocalID] = transferResolution{Action: "skip"}
	}
	choices[p.Sources[0].LocalID] = transferResolution{Action: "create_new"}
	_, folder, e := a.readLocalArtifact(preview.ID, "test-session")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"package.json", "preview.json"} {
		b, e := os.ReadFile(filepath.Join(folder, name))
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), "SYNTHETIC_UNSUPPORTED_SECRET") {
			t.Fatal("raw unknown secret persisted")
		}
	}
	mustOperationsStatus(t, transferApply(a, preview, choices), 200)
	if tableCount(t, a, "sources") != 2 || tableCount(t, a, "source_models") != 1 || tableCount(t, a, "routes") != 0 {
		t.Fatal("unsupported object was imported")
	}
	// Key budgets are visible but cannot be used as a portable budget.
	keyBudget := p.Budgets[0].Budget
	keyBudget.Scope = BudgetScope{Kind: "key", ID: "key"}
	p.Budgets = []transferBudget{{LocalID: "key_budget", Budget: keyBudget}}
	preview = transferPreview(t, a, p, "create")
	choices = transferCreateChoices(preview)
	choices["key_budget"] = transferResolution{Action: "create_new"}
	mustOperationsStatus(t, transferApply(a, preview, choices), 422)
	if _, e = selectConfigDependencies(p, []string{"key_budget"}, true); e == nil {
		t.Fatal("exported unresolved Key budget")
	}
}
func TestSpecConfigTransferRejectsInvalidLocalGraphAndSettings(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	p.Routes[0].LocalID = p.Models[0].LocalID
	if e := validateConfigPackage(p); e == nil {
		t.Fatal("duplicate/cyclic identity accepted")
	}
	p = transferFixture(t, a)
	p.Routes[0].Route.Members[0].ModelID = "missing-model"
	if e := validateConfigPackage(p); e == nil {
		t.Fatal("missing model accepted")
	}
	p = transferFixture(t, a)
	bad := "SYNTHETIC_PATH"
	p.Settings = map[string]settingsChanges{"runtime_settings": {DataDir: &bad}}
	preview := transferPreview(t, a, p, "create")
	choices := transferCreateChoices(preview)
	choices["runtime_settings"] = transferResolution{Action: "create_new"}
	mustOperationsStatus(t, transferApply(a, preview, choices), 422)
}

func TestSpecConfigTransferExplicitSkipTargetPreservesDependencies(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	preview := transferPreview(t, a, p, "create")
	mustOperationsStatus(t, transferApply(a, preview, transferCreateChoices(preview)), 200)
	current, e := a.collectConfig(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	// Select only the route from the exported package, explicitly resolving its
	// model dependency to an existing model without replacing that model/source.
	var source, model, route, budget string
	for query, dst := range map[string]*string{"SELECT id FROM routes": &route, "SELECT id FROM sources WHERE id!='source'": &source, "SELECT id FROM source_models WHERE source_id!='source'": &model, "SELECT id FROM budgets": &budget} {
		if e = a.Store.DB.QueryRow(query).Scan(dst); e != nil {
			t.Fatal(e)
		}
	}
	selected, e := selectConfigDependencies(current, []string{budget}, true)
	if e != nil {
		t.Fatal(e)
	}
	selected.Budgets[0].Budget.AmountLimit = "300"
	beforeSource, e := a.Store.source(source)
	if e != nil {
		t.Fatal(e)
	}
	beforeModel, e := a.Store.model(model)
	if e != nil {
		t.Fatal(e)
	}
	beforeRoute, e := a.Store.route(route)
	if e != nil {
		t.Fatal(e)
	}
	preview = transferPreview(t, a, selected, "replace_selected")
	res := transferReplaceChoices(t, preview, map[string]string{budget: budget, route: route})
	v := res[route]
	v.Action = "skip"
	res[route] = v
	mustOperationsStatus(t, transferApply(a, preview, res), 200)
	afterSource, e := a.Store.source(source)
	if e != nil {
		t.Fatal(e)
	}
	afterModel, e := a.Store.model(model)
	if e != nil {
		t.Fatal(e)
	}
	afterRoute, e := a.Store.route(route)
	if e != nil {
		t.Fatal(e)
	}
	if encode(afterSource) != encode(beforeSource) || encode(afterModel) != encode(beforeModel) || encode(afterRoute) != encode(beforeRoute) {
		t.Fatal("unselected dependencies changed")
	}
	b, e := readBudget(a.Store.DB, budget)
	if e != nil {
		t.Fatal(e)
	}
	if b.AmountLimit != "300" || b.Version != 2 {
		t.Fatal("selected budget not replaced")
	}
	// A new model can refer to an explicitly chosen existing source; the source
	// catalog updates, but its credentials and endpoint remain the original ones.
	sub := configTransfer{Format: "cove-config", Version: 1, Sources: selected.Sources, Models: []transferModel{selected.Models[0]}, CredentialPlaceholders: selected.CredentialPlaceholders}
	sub.Models[0].Model.UpstreamModel = "second-synthetic-model"
	sub.Models[0].Model.DisplayName = "second model"
	preview = transferPreview(t, a, sub, "create")
	res = transferReplaceChoices(t, preview, map[string]string{sub.Sources[0].LocalID: source})
	v = res[sub.Sources[0].LocalID]
	v.Action = "skip"
	res[sub.Sources[0].LocalID] = v
	res[sub.Models[0].LocalID] = transferResolution{Action: "create_new"}
	mustOperationsStatus(t, transferApply(a, preview, res), 200)
	last, e := a.Store.source(source)
	if e != nil {
		t.Fatal(e)
	}
	if last.CredentialRef != beforeSource.CredentialRef || last.Generation != beforeSource.Generation || len(last.Models) != 2 || last.Version != beforeSource.Version+1 {
		t.Fatal("explicit model/source binding changed credential or lost catalog")
	}
}
func TestSpecConfigTransferDuplicateResolvedRouteMemberRejected(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	p.Budgets = nil
	p.Prices = nil
	p.Aliases = nil
	second := p.Models[0]
	second.LocalID = "second_model"
	second.Model.UpstreamModel = "other-model"
	p.Models = append(p.Models, second)
	p.Routes[0].Route.Members = append(p.Routes[0].Route.Members, RouteMember{ModelID: second.LocalID, Weight: 1})
	models, e := a.Store.models("source")
	if e != nil {
		t.Fatal(e)
	}
	preview := transferPreview(t, a, p, "create")
	res := transferReplaceChoices(t, preview, map[string]string{p.Models[0].LocalID: models[0].ID, second.LocalID: models[0].ID})
	for _, local := range []string{p.Models[0].LocalID, second.LocalID} {
		v := res[local]
		v.Action = "skip"
		res[local] = v
	}
	res["route_fixture"] = transferResolution{Action: "create_new"}
	before := transferVersion(t, a)
	mustOperationsStatus(t, transferApply(a, preview, res), 409)
	if transferVersion(t, a) != before {
		t.Fatal("duplicate route mapping mutated state")
	}
}

func TestSpecConfigTransferModelOnlyEnabledCatalogAndPackageIntegrity(t *testing.T) {
	a := operationsApp(t)
	p := transferFixture(t, a)
	p.Routes = nil
	p.Aliases = nil
	p.Budgets = nil
	p.Prices = nil
	models, e := a.Store.models("source")
	if e != nil {
		t.Fatal(e)
	}
	src, e := a.Store.source("source")
	if e != nil {
		t.Fatal(e)
	}
	p.Models[0].Model.Enabled = false
	preview := transferPreview(t, a, p, "replace_selected")
	res := transferReplaceChoices(t, preview, map[string]string{p.Sources[0].LocalID: src.ID, p.Models[0].LocalID: models[0].ID})
	v := res[p.Sources[0].LocalID]
	v.Action = "skip"
	res[p.Sources[0].LocalID] = v
	mustOperationsStatus(t, transferApply(a, preview, res), 200)
	next, e := a.Store.source(src.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Models) != 0 || next.Version != src.Version+1 || next.Generation != src.Generation || next.CredentialRef != src.CredentialRef {
		t.Fatal("model disable catalog mutation incorrect")
	}
	p.Models[0].Model.DisplayName = "metadata update"
	preview = transferPreview(t, a, p, "replace_selected")
	res = transferReplaceChoices(t, preview, map[string]string{p.Sources[0].LocalID: src.ID, p.Models[0].LocalID: models[0].ID})
	v = res[p.Sources[0].LocalID]
	v.Action = "skip"
	res[p.Sources[0].LocalID] = v
	mustOperationsStatus(t, transferApply(a, preview, res), 200)
	unchanged, e := a.Store.source(src.ID)
	if e != nil {
		t.Fatal(e)
	}
	if encode(unchanged) != encode(next) {
		t.Fatal("model metadata replacement changed unselected source")
	}
	preview = transferPreview(t, a, p, "replace_selected")
	_, folder, e := a.readLocalArtifact(preview.ID, "test-session")
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(folder, "package.json"))
	if e != nil {
		t.Fatal(e)
	}
	var stored configTransfer
	_ = json.Unmarshal(b, &stored)
	stored.Models[0].Model.DisplayName = "tampered after preview"
	if e = writePrivateJSON(filepath.Join(folder, "package.json"), stored); e != nil {
		t.Fatal(e)
	}
	before := transferVersion(t, a)
	mustOperationsStatus(t, transferApply(a, preview, res), 404)
	if transferVersion(t, a) != before {
		t.Fatal("tampered preview was applied")
	}
}
