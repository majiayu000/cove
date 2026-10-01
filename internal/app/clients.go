package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tailscale/hujson"
)

// ClientConfigSchema is initialized with the application's other tables.
// Only redacted metadata is kept here. Original field values are in Secrets.
const ClientConfigSchema = `CREATE TABLE IF NOT EXISTS client_previews(id TEXT PRIMARY KEY, expires_at TEXT NOT NULL, secret_ref TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS client_changes(id TEXT PRIMARY KEY, preview_id TEXT UNIQUE NOT NULL, secret_ref TEXT NOT NULL, data TEXT NOT NULL);`

const clientConfigLimit = 1 << 20

type clientCard struct {
	Kind             string              `json:"kind"`
	Name             string              `json:"name"`
	Version          string              `json:"version"`
	ContractVersion  string              `json:"contract_version"`
	Status           string              `json:"status"`
	Binary           string              `json:"binary,omitempty"`
	Scopes           []string            `json:"scopes"`
	RecommendedPaths map[string][]string `json:"recommended_paths"`
	Evidence         string              `json:"evidence"`
}

func clientCards() []clientCard {
	home, _ := os.UserHomeDir()
	return []clientCard{
		{Kind: "codex", Name: "Codex CLI", ContractVersion: "0.158.0", Scopes: []string{"user", "project"}, RecommendedPaths: map[string][]string{"user": {"<选定的独立 CODEX_HOME>/config.toml"}, "project": {"<项目>/.codex/config.toml"}}, Evidence: "Cove v1.2; openai/codex 064c6b8c737f5b41d171fdda80bd9ef10ad06eb3; 0.156.1 isolated features list config parser accepted"},
		{Kind: "claude", Name: "Claude Code", ContractVersion: "2.1.281", Scopes: []string{"user", "project"}, RecommendedPaths: map[string][]string{"user": {filepath.Join(home, ".claude", "settings.json")}, "project": {"<项目>/.claude/settings.local.json"}}, Evidence: "Cove v1.2 external contracts 5.2; code.claude.com/docs/en/settings and llm-gateway"},
		{Kind: "opencode", Name: "OpenCode", ContractVersion: "1.18.33", Scopes: []string{"user", "project"}, RecommendedPaths: map[string][]string{"user": {filepath.Join(home, ".config", "opencode", "opencode.json")}, "project": {"<项目>/opencode.json", "<项目>/opencode.jsonc"}}, Evidence: "Cove v1.2; sst/opencode 7945de208964a49300d7f770d1a71d078db9a4c4; 1.18.27 isolated debug config --pure parser accepted"},
	}
}

func detectClient(ctx context.Context, card clientCard) clientCard {
	name := card.Kind
	if name == "claude" {
		name = "claude"
	}
	binary, err := exec.LookPath(name)
	if err != nil {
		card.Status = "not_installed"
		return card
	}
	card.Binary = binary
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	var out bytes.Buffer
	cmd.Stdout = &clientBoundedWriter{writer: &out, left: 2048}
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		card.Status = "detection_failed"
		return card
	}
	card.Version = strings.TrimSpace(out.String())
	version := strings.TrimPrefix(card.Version, "codex-cli ")
	version = strings.TrimSuffix(version, " (Claude Code)")
	valid := version == card.ContractVersion || card.Kind == "codex" && version == "0.156.1" || card.Kind == "opencode" && version == "1.18.27"
	if valid {
		card.Status = "installed"
	} else {
		card.Status = "unsupported_version"
	}
	return card
}

type clientBoundedWriter struct {
	writer io.Writer
	left   int
}

func (w *clientBoundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > w.left {
		p = p[:w.left]
	}
	if _, err := w.writer.Write(p); err != nil {
		return 0, err
	}
	w.left -= len(p)
	return n, nil
}

type clientConfigInput struct {
	Scope string `json:"scope"`
	Root  string `json:"authorized_root"`
	Path  string `json:"explicit_path"`
	Model string `json:"model"`
	KeyID string `json:"key_id,omitempty"`
	// Explicitly selected higher-priority files are read only for the fields
	// this edit will touch. No other config/auth directories are scanned.
	OverridePaths []string `json:"override_paths,omitempty"`
}
type clientField struct {
	Path          []string `json:"path"`
	BeforePresent bool     `json:"before_present"`
	Before        string   `json:"before_value,omitempty"`
	OursPresent   bool     `json:"ours_present"`
	Ours          string   `json:"ours_value,omitempty"`
}
type clientPrivateChange struct {
	Kind           string            `json:"kind"`
	Scope          string            `json:"scope"`
	Root           string            `json:"authorized_root"`
	Path           string            `json:"path"`
	Version        string            `json:"client_version"`
	BeforeHash     string            `json:"before_hash"`
	AfterHash      string            `json:"after_hash"`
	Existed        bool              `json:"existed"`
	Fields         []clientField     `json:"fields"`
	CreatedParents [][]string        `json:"created_parents"`
	OverrideHashes map[string]string `json:"override_hashes,omitempty"`
}
type clientDiff struct {
	Field         string `json:"field"`
	BeforePresent bool   `json:"before_present"`
	Before        any    `json:"before"`
	AfterPresent  bool   `json:"after_present"`
	After         any    `json:"after"`
	Action        string `json:"action"`
}
type clientFilePreview struct {
	Path     string       `json:"path"`
	Exists   bool         `json:"exists"`
	BaseHash string       `json:"base_hash"`
	Diff     []clientDiff `json:"redacted_diff"`
}
type clientPreview struct {
	ID             string              `json:"preview_id"`
	ExpiresAt      time.Time           `json:"expires_at"`
	Kind           string              `json:"kind"`
	Scope          string              `json:"scope"`
	Version        string              `json:"client_version"`
	Status         string              `json:"status"`
	Files          []clientFilePreview `json:"files"`
	RequiredSecret map[string]any      `json:"required_secret"`
	Validation     map[string]any      `json:"validation"`
	Blockers       []string            `json:"blockers"`
	Warnings       []string            `json:"warnings"`
}
type clientChange struct {
	ID             string           `json:"id"`
	PreviewID      string           `json:"preview_id"`
	Kind           string           `json:"kind"`
	Scope          string           `json:"scope"`
	Path           string           `json:"path"`
	Version        string           `json:"client_version"`
	State          string           `json:"state"`
	CreatedAt      time.Time        `json:"created_at"`
	RestoredFields []string         `json:"restored_fields,omitempty"`
	Files          []map[string]any `json:"files"`
}

func (a *App) clientsAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 {
		if r.Method != "GET" {
			fail(w, 405, "方法不支持", "")
			return
		}
		cards := clientCards()
		for i := range cards {
			cards[i] = detectClient(r.Context(), cards[i])
		}
		writeJSON(w, 200, map[string]any{"items": cards, "next_cursor": nil})
		return
	}
	if len(parts) != 4 || parts[3] != "preview" {
		fail(w, 404, "客户端操作不存在", "")
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var card clientCard
	found := false
	for _, c := range clientCards() {
		if c.Kind == parts[2] {
			card = c
			found = true
			break
		}
	}
	if !found {
		fail(w, 422, "该客户端没有已核验的自动配置写入合同", "kind")
		return
	}
	var in clientConfigInput
	if !decode(w, r, &in) {
		return
	}
	if in.Scope != "user" && in.Scope != "project" {
		fail(w, 400, "请选择 user 或 project scope", "scope")
		return
	}
	if strings.TrimSpace(in.Model) == "" || len(in.Model) > 256 || strings.ContainsAny(in.Model, "\r\n\x00") {
		fail(w, 400, "请填写有效的公开模型 ID", "model")
		return
	}
	card = detectClient(r.Context(), card)
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.storageFailed.Load() {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if in.KeyID != "" {
		var key ClientKey
		var keyData string
		err := a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", in.KeyID).Scan(&keyData)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fail(w, 503, storageError().Error(), "")
			return
		}
		if err != nil || json.Unmarshal([]byte(keyData), &key) != nil || key.Revoked {
			fail(w, 409, "所选客户端 Key 不存在或已撤销", "key_id")
			return
		}
	}
	target, err := openClientTarget(card.Kind, in.Scope, in.Root, in.Path, false)
	if err != nil {
		fail(w, 400, err.Error(), "explicit_path")
		return
	}
	defer target.close()
	before, existed, err := target.read()
	if err != nil {
		fail(w, 422, "无法安全读取选定配置文件；请检查权限和文件类型", "explicit_path")
		return
	}
	fields := clientDesiredFields(card.Kind, in.Model, "http://"+a.Config.Listen)
	private := clientPrivateChange{Kind: card.Kind, Scope: in.Scope, Root: in.Root, Path: in.Path, Version: card.Version, BeforeHash: clientHash(before, existed), Existed: existed}
	doc, err := parseClientDocument(card.Kind, in.Path, before, existed)
	if err != nil {
		fail(w, 422, "unsupported_format：配置格式不能无损编辑，原文件保留", "explicit_path")
		return
	}
	private.CreatedParents = doc.missingParents(fields)
	for _, field := range fields {
		old, present, e := doc.field(field.Path)
		if e != nil {
			fail(w, 422, "unsupported_format：待修改字段不是字符串，原文件保留", clientFieldName(field.Path))
			return
		}
		field.Before = old
		field.BeforePresent = present
		if present && old == field.Ours {
			continue
		}
		private.Fields = append(private.Fields, field)
	}
	after, err := doc.edit(private.Fields, nil)
	if err != nil {
		fail(w, 422, "unsupported_format：无法保留原有配置结构，原文件保留", "explicit_path")
		return
	}
	private.AfterHash = clientHash(after, true)
	now := time.Now().UTC()
	preview := clientPreview{ID: id("cprev"), ExpiresAt: now.Add(10 * time.Minute), Kind: card.Kind, Scope: in.Scope, Version: card.Version, Status: "ready", Blockers: []string{}, Warnings: []string{"文件解析通过只证明配置格式；连接、文本和工具均未测试。", "未选择的管理配置、客户端 CLI 参数和其他进程环境未检查；启动客户端时确认这些来源未覆盖所选配置。"}, Validation: map[string]any{"format": "passed", "runtime": "untested", "adapter_contract": "cove-v1.2", "effective_config": "unverified"}, Files: []clientFilePreview{{Path: in.Path, Exists: existed, BaseHash: private.BeforeHash, Diff: redactedClientDiff(private.Fields)}}, RequiredSecret: clientSecretDelivery(card.Kind, in.Root)}
	if card.Status != "installed" {
		preview.Blockers = append(preview.Blockers, card.Status+"：当前客户端版本没有已核验的配置卡")
	}
	if card.Kind == "codex" && in.Scope == "project" {
		preview.Warnings = append(preview.Warnings, "Codex 仅在信任项目后读取项目配置；本次没有改变项目信任。")
	}
	preview.Blockers = append(preview.Blockers, clientEnvironmentBlockers(card.Kind, in.Root)...)
	preview.Blockers = append(preview.Blockers, clientDocumentBlockers(card.Kind, doc)...)
	private.OverrideHashes = map[string]string{}
	for _, path := range in.OverridePaths {
		override, e := openClientSelectedFile(in.Root, path)
		if e != nil {
			fail(w, 400, "额外配置必须是选定根内的普通文件，不能为符号链接", "override_paths")
			return
		}
		b, exists, e := override.read()
		override.close()
		if e != nil {
			fail(w, 422, "所选额外配置不可读", "override_paths")
			return
		}
		private.OverrideHashes[path] = clientHash(b, exists)
		if !exists {
			continue
		}
		other, e := parseClientDocument(card.Kind, path, b, true)
		if e != nil {
			fail(w, 422, "额外配置格式不受支持", "override_paths")
			return
		}
		for _, field := range fields {
			if _, present, e := other.field(field.Path); e != nil || present {
				preview.Blockers = append(preview.Blockers, "所选额外配置包含可能覆盖的字段："+clientFieldName(field.Path))
				break
			}
		}
	}
	if len(preview.Blockers) > 0 {
		preview.Status = "blocked"
		writeJSON(w, 200, preview)
		return
	}
	if len(private.Fields) == 0 {
		preview.Warnings = append(preview.Warnings, "选定字段已经是本次 Cove 配置，应用不会改写文件。")
	}
	if err = a.expireClientPreviews(now); err != nil {
		fail(w, 503, "客户端预览清理失败，未写客户端文件", "")
		return
	}
	ref := "client-preview-" + preview.ID
	data, e := json.Marshal(private)
	if e != nil || a.Secrets.Put(ref, string(data)) != nil {
		fail(w, 503, "私有恢复记录保存失败，未写客户端文件", "")
		return
	}
	if _, err = a.Store.DB.Exec("INSERT INTO client_previews(id,expires_at,secret_ref,data) VALUES(?,?,?,?)", preview.ID, preview.ExpiresAt.Format(time.RFC3339Nano), ref, encode(preview)); err != nil {
		cleanup := a.Secrets.Delete(ref)
		if cleanup != nil {
			a.markStorageFailure()
		}
		fail(w, 503, "客户端预览存储失败，未写客户端文件", "")
		return
	}
	writeJSON(w, 200, preview)
}

func clientDesiredFields(kind, model, origin string) []clientField {
	add := func(path []string, value string) clientField {
		return clientField{Path: path, OursPresent: true, Ours: value}
	}
	switch kind {
	case "codex":
		return []clientField{add([]string{"model_provider"}, "cove"), add([]string{"model"}, model), add([]string{"model_providers", "cove", "name"}, "Cove"), add([]string{"model_providers", "cove", "base_url"}, origin+"/v1"), add([]string{"model_providers", "cove", "wire_api"}, "responses"), add([]string{"model_providers", "cove", "env_key"}, "COVE_API_KEY")}
	case "claude":
		return []clientField{add([]string{"model"}, model), add([]string{"env", "ANTHROPIC_BASE_URL"}, origin)}
	default:
		return []clientField{add([]string{"model"}, "cove/"+model), add([]string{"provider", "cove", "npm"}, "@ai-sdk/openai-compatible"), add([]string{"provider", "cove", "name"}, "Cove"), add([]string{"provider", "cove", "options", "baseURL"}, origin+"/v1"), add([]string{"provider", "cove", "options", "apiKey"}, "{env:COVE_API_KEY}"), add([]string{"provider", "cove", "models", model, "name"}, model)}
	}
}
func clientSecretDelivery(kind, root string) map[string]any {
	name := "COVE_API_KEY"
	if kind == "claude" {
		name = "ANTHROPIC_AUTH_TOKEN"
	}
	out := map[string]any{"mode": "env_reference", "env_name": name, "status": "needs_secret", "persisted": false, "instruction": "启动客户端时仅向该进程传入新建 Cove Key；不要修改全局 shell 或 .env。"}
	if kind == "codex" {
		out["codex_home"] = root
	}
	return out
}
func clientEnvironmentBlockers(kind, root string) []string {
	var names []string
	switch kind {
	case "claude":
		names = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	case "codex":
		if v := os.Getenv("CODEX_HOME"); v != "" && filepath.Clean(v) != filepath.Clean(root) {
			return []string{"Cove 当前进程 CODEX_HOME 指向其他目录；为客户端选择独立启动环境后重新预览"}
		}
	case "opencode":
		names = []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR"}
	}
	var blockers []string
	for _, name := range names {
		if os.Getenv(name) != "" {
			blockers = append(blockers, "Cove 当前启动环境存在覆盖或认证冲突："+name+"（值不读取到响应）")
		}
	}
	return blockers
}
func redactedClientDiff(fields []clientField) []clientDiff {
	out := make([]clientDiff, 0, len(fields))
	for _, f := range fields {
		var before any
		if f.BeforePresent {
			before = "<原值私有保存>"
		}
		var after any
		if f.OursPresent {
			after = f.Ours
		}
		action := "replace"
		if !f.BeforePresent {
			action = "add"
		}
		if !f.OursPresent {
			action = "remove"
		}
		out = append(out, clientDiff{Field: clientFieldName(f.Path), BeforePresent: f.BeforePresent, Before: before, AfterPresent: f.OursPresent, After: after, Action: action})
	}
	return out
}
func clientFieldName(path []string) string { return strings.Join(path, ".") }
func clientHash(b []byte, exists bool) string {
	if !exists {
		return "missing"
	}
	return digest(string(b))
}

func (a *App) expireClientPreviews(now time.Time) error {
	rows, err := a.Store.DB.Query("SELECT id,secret_ref FROM client_previews WHERE expires_at < ? AND id NOT IN (SELECT preview_id FROM client_changes)", now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type expired struct{ id, ref string }
	var all []expired
	for rows.Next() {
		var x expired
		if err = rows.Scan(&x.id, &x.ref); err != nil {
			rows.Close()
			return err
		}
		all = append(all, x)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, x := range all {
		if err = a.Secrets.Delete(x.ref); err != nil {
			return err
		}
		if _, err = a.Store.DB.Exec("DELETE FROM client_previews WHERE id=?", x.id); err != nil {
			return err
		}
	}
	return nil
}

type clientApplyInput struct {
	PreviewID      string            `json:"preview_id"`
	BaseHashes     map[string]string `json:"base_hashes"`
	SecretDelivery string            `json:"secret_delivery"`
}
type clientRestoreInput struct {
	CurrentHash string            `json:"current_hash"`
	Resolutions map[string]string `json:"resolutions,omitempty"`
}
type clientRestorePreview struct {
	ID          string               `json:"change_id"`
	Path        string               `json:"path"`
	CurrentHash string               `json:"current_hash"`
	Status      string               `json:"status"`
	Fields      []clientRestoreField `json:"fields"`
}
type clientRestoreField struct {
	Field   string `json:"field"`
	Action  string `json:"action"`
	Before  any    `json:"before"`
	Ours    any    `json:"ours"`
	Current any    `json:"current"`
}

func (a *App) clientChangesAPI(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.storageFailed.Load() {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if len(parts) == 2 && r.Method == "GET" {
		rows, err := a.Store.DB.Query("SELECT data FROM client_changes ORDER BY rowid DESC LIMIT 200")
		if err != nil {
			fail(w, 503, storageError().Error(), "")
			return
		}
		changes := []clientChange{}
		for rows.Next() {
			var data string
			var c clientChange
			if err = rows.Scan(&data); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(data), &c); err != nil {
				break
			}
			changes = append(changes, c)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			fail(w, 503, "客户端变更记录不可读", "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": changes, "next_cursor": nil})
		return
	}
	if len(parts) == 2 && r.Method == "POST" {
		a.applyClientConfig(w, r)
		return
	}
	if len(parts) == 2 {
		fail(w, 405, "方法不支持", "")
		return
	}
	if len(parts) != 4 || (parts[3] != "restore-preview" && parts[3] != "restore") {
		fail(w, 404, "客户端变更操作不存在", "")
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var c clientChange
	var ref, data string
	err := a.Store.DB.QueryRow("SELECT secret_ref,data FROM client_changes WHERE id=?", parts[2]).Scan(&ref, &data)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "客户端变更不存在", "")
		return
	}
	if err != nil || json.Unmarshal([]byte(data), &c) != nil {
		fail(w, 503, "客户端变更记录不可读", "")
		return
	}
	if c.Kind == "mcp" || c.Kind == "skills" {
		a.restoreConfigExtension(w, r, c, ref, parts[3])
		return
	}
	var private clientPrivateChange
	value, err := a.Secrets.Get(ref)
	if err != nil || json.Unmarshal([]byte(value), &private) != nil {
		fail(w, 503, "私有恢复记录不可读，原客户端文件保留", "")
		return
	}
	target, err := openClientTarget(private.Kind, private.Scope, private.Root, private.Path, false)
	if err != nil {
		fail(w, 409, "配置路径已改变或含符号链接，请重新选择目标", "explicit_path")
		return
	}
	defer target.close()
	current, exists, err := target.read()
	if err != nil {
		fail(w, 422, "当前配置不可安全读取", "explicit_path")
		return
	}
	doc, err := parseClientDocument(private.Kind, private.Path, current, exists)
	if err != nil {
		fail(w, 422, "unsupported_format：当前配置格式不能无损恢复", "explicit_path")
		return
	}
	preview := clientRestorePreview{ID: c.ID, Path: private.Path, CurrentHash: clientHash(current, exists), Status: "ready", Fields: []clientRestoreField{}}
	var restore []clientField
	var conflicts []clientField
	done := map[string]bool{}
	for _, s := range c.RestoredFields {
		done[s] = true
	}
	for _, f := range private.Fields {
		name := clientFieldName(f.Path)
		if done[name] {
			continue
		}
		value, present, e := doc.field(f.Path)
		action := "restore_before"
		if e == nil && present == f.BeforePresent && (!present || value == f.Before) {
			action = "already_before"
		} else if e != nil || present != f.OursPresent || present && value != f.Ours {
			action = "conflict"
			conflicts = append(conflicts, f)
		} else {
			restore = append(restore, clientField{Path: f.Path, OursPresent: f.BeforePresent, Ours: f.Before})
		}
		var before, ours, cur any
		if f.BeforePresent {
			before = "<原值私有保存>"
		}
		if f.OursPresent {
			ours = f.Ours
		}
		if present || e != nil {
			cur = "<当前值保留>"
		}
		preview.Fields = append(preview.Fields, clientRestoreField{Field: name, Action: action, Before: before, Ours: ours, Current: cur})
	}
	if len(conflicts) > 0 {
		preview.Status = "conflict"
	}
	if parts[3] == "restore-preview" {
		writeJSON(w, 200, preview)
		return
	}
	var in clientRestoreInput
	if !decode(w, r, &in) {
		return
	}
	if in.CurrentHash != preview.CurrentHash {
		fail(w, 409, "配置已在恢复预览后改变，请重新预览", "current_hash")
		return
	}
	allowed := map[string]bool{}
	for _, f := range conflicts {
		name := clientFieldName(f.Path)
		allowed[name] = true
		switch in.Resolutions[name] {
		case "keep_current":
		case "restore_before":
			restore = append(restore, clientField{Path: f.Path, OursPresent: f.BeforePresent, Ours: f.Before})
		default:
			fail(w, 409, "恢复包含用户后续修改，请为每个冲突选择保留或还原", name)
			return
		}
	}
	for name, resolution := range in.Resolutions {
		if !allowed[name] || resolution != "keep_current" && resolution != "restore_before" {
			fail(w, 400, "恢复选择与当前冲突不匹配", name)
			return
		}
	}
	removeFile := !private.Existed && exists && preview.CurrentHash == private.AfterHash
	after := current
	if !removeFile && len(restore) > 0 {
		after, err = doc.edit(restore, private.CreatedParents)
		if err != nil {
			fail(w, 422, "unsupported_format：恢复无法保留用户配置结构", "explicit_path")
			return
		}
	}
	c.State = "restoring"
	c.Files = []map[string]any{{"path": private.Path, "status": "restoring"}}
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		fail(w, 503, "恢复状态保存失败，未写客户端文件", "")
		return
	}
	mutated := false
	if removeFile {
		mutated, err = target.replace(nil, preview.CurrentHash, true)
	} else if len(restore) > 0 {
		mutated, err = target.replace(after, preview.CurrentHash, false)
	}
	if err != nil {
		a.clientWriteFailure(w, c, mutated, err)
		return
	}
	for _, f := range preview.Fields {
		c.RestoredFields = append(c.RestoredFields, f.Field)
	}
	c.State = "restored"
	c.Files = []map[string]any{{"path": private.Path, "status": "restored", "removed": removeFile}}
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		a.markStorageFailure()
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已恢复，但恢复完成状态保存失败；请重启检查记录"}, "change": c})
		return
	}
	writeJSON(w, 200, c)
}

func (a *App) applyClientConfig(w http.ResponseWriter, r *http.Request) {
	var in clientApplyInput
	if !decode(w, r, &in) {
		return
	}
	var expires, ref, data string
	err := a.Store.DB.QueryRow("SELECT expires_at,secret_ref,data FROM client_previews WHERE id=?", in.PreviewID).Scan(&expires, &ref, &data)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "客户端预览不存在", "")
		return
	}
	var preview clientPreview
	expiry, e := time.Parse(time.RFC3339Nano, expires)
	if err != nil || e != nil || json.Unmarshal([]byte(data), &preview) != nil {
		fail(w, 503, "客户端预览不可读", "")
		return
	}
	if !time.Now().Before(expiry) {
		fail(w, 409, "客户端预览已过期，请重新预览", "preview_id")
		return
	}
	var existing string
	err = a.Store.DB.QueryRow("SELECT data FROM client_changes WHERE preview_id=?", in.PreviewID).Scan(&existing)
	if err == nil {
		fail(w, 409, "该预览已经应用或正在恢复，请查看变更记录", "preview_id")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 503, storageError().Error(), "")
		return
	}
	if preview.Kind == "mcp" || preview.Kind == "skills" {
		a.applyConfigExtension(w, r, preview, ref, in)
		return
	}
	if in.SecretDelivery != "env_reference" {
		fail(w, 422, "本客户端合同只支持进程环境交付 Key；不接收或持久化明文 Key", "secret_delivery")
		return
	}
	var private clientPrivateChange
	value, err := a.Secrets.Get(ref)
	if err != nil || json.Unmarshal([]byte(value), &private) != nil {
		fail(w, 503, "私有配置记录不可读，未写客户端文件", "")
		return
	}
	if len(in.BaseHashes) != 1 || in.BaseHashes[private.Path] != private.BeforeHash {
		fail(w, 409, "请提交预览中选定文件的 base hash", "base_hashes")
		return
	}
	// Recheck the version/environment at the mutation boundary. A client can be
	// upgraded, removed, or a launch override changed after the preview.
	var card clientCard
	for _, candidate := range clientCards() {
		if candidate.Kind == private.Kind {
			card = candidate
			break
		}
	}
	card = detectClient(r.Context(), card)
	if card.Status != "installed" || card.Version != private.Version || len(clientEnvironmentBlockers(private.Kind, private.Root)) > 0 {
		fail(w, 409, "客户端版本或启动环境已变化，请重新预览", "client_version")
		return
	}
	for path, hash := range private.OverrideHashes {
		other, e := openClientSelectedFile(private.Root, path)
		if e != nil {
			fail(w, 409, "所选额外配置路径已变化，请重新预览", "override_paths")
			return
		}
		b, exists, e := other.read()
		other.close()
		if e != nil || clientHash(b, exists) != hash {
			fail(w, 409, "所选额外配置已变化，请重新预览", "override_paths")
			return
		}
	}
	target, err := openClientTarget(private.Kind, private.Scope, private.Root, private.Path, true)
	if err != nil {
		fail(w, 409, "配置路径不再安全，请重新预览", "explicit_path")
		return
	}
	defer target.close()
	before, exists, err := target.read()
	if err != nil {
		fail(w, 422, "配置文件不可读，未应用", "explicit_path")
		return
	}
	if clientHash(before, exists) != private.BeforeHash {
		fail(w, 409, "配置已在预览后修改，请重新预览", "base_hashes")
		return
	}
	doc, err := parseClientDocument(private.Kind, private.Path, before, exists)
	if err != nil {
		fail(w, 422, "unsupported_format：原文件保留", "explicit_path")
		return
	}
	after, err := doc.edit(private.Fields, nil)
	if err != nil || clientHash(after, true) != private.AfterHash {
		fail(w, 409, "预览输出已变化，请重新预览", "preview_id")
		return
	}
	c := clientChange{ID: id("cchange"), PreviewID: preview.ID, Kind: private.Kind, Scope: private.Scope, Path: private.Path, Version: private.Version, State: "applying", CreatedAt: time.Now().UTC(), Files: []map[string]any{{"path": private.Path, "status": "pending"}}}
	if _, err = a.Store.DB.Exec("INSERT INTO client_changes(id,preview_id,secret_ref,data) VALUES(?,?,?,?)", c.ID, c.PreviewID, ref, encode(c)); err != nil {
		fail(w, 503, "变更记录保存失败，未写客户端文件", "")
		return
	}
	mutated := false
	if len(private.Fields) > 0 {
		mutated, err = target.replace(after, private.BeforeHash, false)
	}
	if err != nil {
		a.clientWriteFailure(w, c, mutated, err)
		return
	}
	c.State = "applied"
	c.Files = []map[string]any{{"path": private.Path, "status": "applied", "base_hash": private.BeforeHash, "after_hash": private.AfterHash}}
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		a.markStorageFailure()
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已应用，但完成状态保存失败；恢复记录已保留，请重启后检查"}, "change": c})
		return
	}
	writeJSON(w, 201, c)
}
func (a *App) clientWriteFailure(w http.ResponseWriter, c clientChange, mutated bool, err error) {
	c.State = "failed"
	if mutated {
		c.State = "partial"
	}
	c.Files = []map[string]any{{"path": c.Path, "status": c.State, "changed": mutated}}
	if _, e := a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); e != nil {
		a.markStorageFailure()
	}
	status := 503
	message := "客户端文件写入失败；恢复记录保留，请检查权限并重新预览"
	if errors.Is(err, errClientHashChanged) {
		status = 409
		message = "配置已被其他程序修改，请重新预览"
	}
	if mutated {
		message = "客户端文件已改变但持久化结果不确定；不要重试应用，请按变更记录预览恢复"
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "field": "explicit_path"}, "change": c})
}

// Each target pins its authorized directory and, when present, its parent.
// os.Root confines every subsequent operation even if directories are renamed.
type clientTarget struct {
	root     *os.Root
	parent   *os.Root
	relative string
	name     string
}

func (t *clientTarget) close() {
	if t.parent != nil {
		_ = t.parent.Close()
	}
	_ = t.root.Close()
}
func openClientTarget(kind, scope, root, path string, create bool) (*clientTarget, error) {
	relative, err := clientRelativePath(root, path)
	if err != nil {
		return nil, err
	}
	valid := false
	switch kind {
	case "codex":
		valid = scope == "user" && relative == "config.toml" || scope == "project" && relative == filepath.Join(".codex", "config.toml")
	case "claude":
		valid = scope == "user" && relative == filepath.Join(".claude", "settings.json") || scope == "project" && relative == filepath.Join(".claude", "settings.local.json")
	case "opencode":
		valid = scope == "user" && relative == filepath.Join(".config", "opencode", "opencode.json") || scope == "project" && (relative == "opencode.json" || relative == "opencode.jsonc")
	}
	if !valid {
		return nil, errors.New("所选路径不符合客户端与 scope 的配置合同；不会操作其他文件")
	}
	return openClientFile(root, path, create)
}
func openClientSelectedFile(root, path string) (*clientTarget, error) {
	relative, err := clientRelativePath(root, path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(relative)
	// Explicit override selections are config files only; authentication files,
	// .env, browser stores, and arbitrary extension databases are never inputs.
	allowed := name == "config.toml" || name == "settings.json" || name == "settings.local.json" || name == "managed-settings.json" || name == "opencode.json" || name == "opencode.jsonc"
	if !allowed {
		return nil, errors.New("只允许选定客户端的配置文件")
	}
	return openClientFile(root, path, false)
}
func clientRelativePath(root, path string) (string, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || filepath.Clean(root) != root || filepath.Clean(path) != path {
		return "", errors.New("请选择规范的绝对目录和文件路径，不能包含 . 或 ..")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return "", errors.New("配置文件必须在本次明确选定的目录内")
	}
	return relative, nil
}
func openClientFile(root, path string, create bool) (*clientTarget, error) {
	relative, err := clientRelativePath(root, path)
	if err != nil {
		return nil, err
	}
	// Check the selected root's ancestry without following any user-created link.
	for at := root; at != filepath.Dir(at); at = filepath.Dir(at) {
		info, e := os.Lstat(at)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("选定根必须为真实目录，不能包含符号链接")
		}
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.New("选定根目录不可打开")
	}
	t := &clientTarget{root: r, relative: relative, name: filepath.Base(relative)}
	dir := filepath.Dir(relative)
	if dir != "." {
		prefix := ""
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			prefix = filepath.Join(prefix, part)
			info, e := r.Lstat(prefix)
			if errors.Is(e, os.ErrNotExist) {
				if !create {
					return t, nil
				}
				if e = r.Mkdir(prefix, 0700); e != nil {
					t.close()
					return nil, errors.New("配置父目录创建失败")
				}
				continue
			}
			if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				t.close()
				return nil, errors.New("配置路径含符号链接或非目录，未操作")
			}
		}
	}
	t.parent, err = r.OpenRoot(dir)
	if err != nil {
		t.close()
		return nil, errors.New("配置父目录不可打开")
	}
	info, err := t.parent.Lstat(t.name)
	if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		t.close()
		return nil, errors.New("配置目标必须为普通文件，不能为符号链接")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.close()
		return nil, errors.New("配置目标不可检查")
	}
	return t, nil
}
func (t *clientTarget) read() ([]byte, bool, error) {
	if t.parent == nil {
		return nil, false, nil
	}
	before, err := t.parent.Lstat(t.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !before.Mode().IsRegular() || before.Size() > clientConfigLimit {
		return nil, false, errors.New("不是受支持的普通配置文件")
	}
	f, err := t.parent.Open(t.name)
	if err != nil {
		return nil, false, err
	}
	actual, e := f.Stat()
	if e != nil || !os.SameFile(before, actual) {
		f.Close()
		return nil, false, errors.New("读取时文件已改变")
	}
	b, e := io.ReadAll(io.LimitReader(f, clientConfigLimit+1))
	e = errors.Join(e, f.Close())
	after, last := t.parent.Lstat(t.name)
	if e != nil || last != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || len(b) > clientConfigLimit {
		return nil, false, errors.New("读取时文件已改变或超限")
	}
	return b, true, nil
}

var errClientHashChanged = errors.New("客户端配置 hash 已改变")

func (t *clientTarget) replace(content []byte, expected string, remove bool) (changed bool, result error) {
	if t.parent == nil {
		return false, errors.New("配置父目录不存在")
	}
	// Prepare and fsync the new file before the final CAS. No client file is
	// altered when formatting, permissions, writing, or fsync fails here.
	temp := ".cove-client-" + id("tmp")
	if !remove {
		f, err := t.parent.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return false, err
		}
		defer func() {
			if err := t.parent.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}()
		err = protectAppFile(f)
		if err == nil {
			_, err = f.Write(content)
		}
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return false, err
		}
	}
	b, exists, err := t.read()
	if err != nil {
		return false, err
	}
	if clientHash(b, exists) != expected {
		return false, errClientHashChanged
	}
	if remove {
		err = t.parent.Remove(t.name)
	} else {
		err = t.parent.Rename(temp, t.name)
	}
	if err != nil {
		return false, err
	}
	changed = true
	dir, err := t.parent.Open(".")
	if err != nil {
		return true, err
	}
	return true, errors.Join(syncAppDirectory(dir), dir.Close())
}

// The document editor changes selected scalar values only. JSONC is kept as an
// exact syntax tree; TOML replacements use parser byte ranges, not re-encoding.
type clientDocument struct {
	kind, path string
	raw        []byte
	values     map[string]any
	json       *hujson.Value
}

func parseClientDocument(kind, path string, b []byte, exists bool) (*clientDocument, error) {
	d := &clientDocument{kind: kind, path: path, raw: append([]byte(nil), b...), values: map[string]any{}}
	if kind == "codex" {
		if exists {
			if err := toml.Unmarshal(b, &d.values); err != nil {
				return nil, err
			}
		}
		return d, nil
	}
	if !exists {
		b = []byte("{}\n")
		d.raw = b
	}
	if kind == "claude" && !json.Valid(b) {
		return nil, errors.New("Claude settings 必须为 JSON")
	}
	v, err := hujson.Parse(b)
	if err != nil {
		return nil, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return nil, errors.New("配置必须为对象")
	}
	if err = clientUniqueJSON(v); err != nil {
		return nil, err
	}
	canonical := v.Clone()
	canonical.Standardize()
	if err = json.Unmarshal(canonical.Pack(), &d.values); err != nil {
		return nil, err
	}
	d.json = &v
	return d, nil
}
func clientUniqueJSON(v hujson.Value) error {
	switch node := v.Value.(type) {
	case *hujson.Object:
		names := map[string]bool{}
		for _, m := range node.Members {
			name := m.Name.Value.(hujson.Literal).String()
			if names[name] {
				return errors.New("配置存在重复字段")
			}
			names[name] = true
			if err := clientUniqueJSON(m.Value); err != nil {
				return err
			}
		}
	case *hujson.Array:
		for _, e := range node.Elements {
			if err := clientUniqueJSON(e); err != nil {
				return err
			}
		}
	}
	return nil
}
func (d *clientDocument) lookup(path []string) (any, bool, error) {
	var value any = d.values
	for _, part := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false, errors.New("父字段不是对象")
		}
		next, exists := object[part]
		if !exists {
			return nil, false, nil
		}
		value = next
	}
	return value, true, nil
}
func (d *clientDocument) field(path []string) (string, bool, error) {
	value, present, err := d.lookup(path)
	if err != nil || !present {
		return "", present, err
	}
	text, ok := value.(string)
	if !ok {
		return "", true, errors.New("字段不是字符串")
	}
	return text, true, nil
}
func (d *clientDocument) missingParents(fields []clientField) [][]string {
	var out [][]string
	seen := map[string]bool{}
	for _, f := range fields {
		for i := 1; i < len(f.Path); i++ {
			path := f.Path[:i]
			_, exists, _ := d.lookup(path)
			name := clientPointer(path)
			if !exists && !seen[name] {
				seen[name] = true
				out = append(out, append([]string(nil), path...))
			}
		}
	}
	return out
}
func (d *clientDocument) edit(fields []clientField, prune [][]string) ([]byte, error) {
	if d.kind == "codex" {
		return d.editTOML(fields, prune)
	}
	v := d.json.Clone()
	for _, field := range fields {
		if err := clientJSONField(&v, field.Path, field.OursPresent, field.Ours); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(prune, func(i, j int) bool { return len(prune[i]) > len(prune[j]) })
	for _, path := range prune {
		at := v.Find(clientPointer(path))
		if at == nil {
			continue
		}
		if obj, ok := at.Value.(*hujson.Object); ok && len(obj.Members) == 0 {
			if err := clientJSONDelete(&v, path); err != nil {
				return nil, err
			}
		}
	}
	out := v.Pack()
	if _, err := parseClientDocument(d.kind, d.path, out, true); err != nil {
		return nil, err
	}
	return out, nil
}
func clientPointer(path []string) string {
	var out string
	for _, part := range path {
		out += "/" + strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return out
}
func clientJSONField(v *hujson.Value, path []string, present bool, value string) error {
	if !present {
		return clientJSONDelete(v, path)
	}
	at := v
	for i, part := range path {
		obj, ok := at.Value.(*hujson.Object)
		if !ok {
			return errors.New("父字段不是对象")
		}
		var next *hujson.Value
		for j := range obj.Members {
			if obj.Members[j].Name.Value.(hujson.Literal).String() == part {
				next = &obj.Members[j].Value
				break
			}
		}
		if next == nil {
			obj.Members = append(obj.Members, hujson.ObjectMember{Name: hujson.Value{BeforeExtra: []byte("\n  "), Value: hujson.String(part)}, Value: hujson.Value{BeforeExtra: []byte(" "), Value: &hujson.Object{}}})
			next = &obj.Members[len(obj.Members)-1].Value
		}
		if i == len(path)-1 {
			next.Value = hujson.String(value)
			return nil
		}
		at = next
	}
	return nil
}
func clientJSONDelete(v *hujson.Value, path []string) error {
	at := v.Find(clientPointer(path[:len(path)-1]))
	if at == nil {
		return nil
	}
	obj, ok := at.Value.(*hujson.Object)
	if !ok {
		return errors.New("父字段不是对象")
	}
	for i, m := range obj.Members {
		if m.Name.Value.(hujson.Literal).String() != path[len(path)-1] {
			continue
		}
		// Keep comments associated with the removed member in the same object.
		extra := append(append(append(append([]byte{}, m.Name.BeforeExtra...), m.Name.AfterExtra...), m.Value.BeforeExtra...), m.Value.AfterExtra...)
		if i+1 < len(obj.Members) {
			obj.Members[i+1].Name.BeforeExtra = append(extra, obj.Members[i+1].Name.BeforeExtra...)
		} else {
			obj.AfterExtra = append(extra, obj.AfterExtra...)
		}
		obj.Members = append(obj.Members[:i], obj.Members[i+1:]...)
		return nil
	}
	return nil
}

type clientTOMLSpan struct {
	start, end int
	keyStart   int
}

func (d *clientDocument) editTOML(fields []clientField, prune [][]string) ([]byte, error) {
	content := append([]byte(nil), d.raw...)
	for _, field := range fields {
		current, err := parseClientDocument("codex", d.path, content, true)
		if err != nil {
			return nil, err
		}
		_, exists, e := current.field(field.Path)
		if e != nil {
			return nil, e
		}
		spans, tables, firstTable, err := clientTOMLSpans(content)
		if err != nil {
			return nil, err
		}
		name := clientPointer(field.Path)
		if span, ok := spans[name]; ok {
			if field.OursPresent {
				value, _ := json.Marshal(field.Ours)
				content = append(append(append([]byte{}, content[:span.start]...), value...), content[span.end:]...)
			} else {
				// Remove the assignment, keeping its trailing comment and all other lines.
				content = append(append([]byte{}, content[:span.keyStart]...), content[span.end:]...)
			}
			continue
		}
		if exists {
			return nil, errors.New("无法无损编辑内联 TOML 字段")
		}
		if !field.OursPresent {
			continue
		}
		value, _ := json.Marshal(field.Ours)
		key := field.Path[len(field.Path)-1]
		parent := field.Path[:len(field.Path)-1]
		line := clientTOMLKey(key) + " = " + string(value) + "\n"
		if len(parent) == 0 {
			content = clientInsertBytes(content, firstTable, []byte(line))
			continue
		}
		if _, exists, e := current.lookup(parent); e != nil {
			return nil, e
		} else if exists {
			if at, ok := tables[clientPointer(parent)]; ok {
				content = clientInsertBytes(content, at, []byte(line))
				continue
			}
			return nil, errors.New("不能无损扩展内联或隐式 TOML 表")
		}
		header := "\n["
		for i, part := range parent {
			if i > 0 {
				header += "."
			}
			header += clientTOMLKey(part)
		}
		header += "]\n"
		if len(content) > 0 && content[len(content)-1] != '\n' {
			header = "\n" + header
		}
		content = append(content, []byte(header+line)...)
	}
	// Remove only empty tables created by this change. Other tables and comments
	// survive, including unknown providers and fields added after apply.
	for _, path := range prune {
		current, err := parseClientDocument("codex", d.path, content, true)
		if err != nil {
			return nil, err
		}
		value, exists, err := current.lookup(path)
		if err != nil || !exists {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok || len(object) != 0 {
			continue
		}
		_, tables, _, err := clientTOMLSpans(content)
		if err != nil {
			return nil, err
		}
		if end, ok := tables[clientPointer(path)]; ok {
			start := bytes.LastIndex(content[:end-1], []byte("\n")) + 1
			content = append(append([]byte{}, content[:start]...), content[end:]...)
		}
	}
	if _, err := parseClientDocument("codex", d.path, content, true); err != nil {
		return nil, err
	}
	return content, nil
}
func clientInsertBytes(b []byte, at int, value []byte) []byte {
	out := make([]byte, 0, len(b)+len(value))
	out = append(out, b[:at]...)
	if at > 0 && b[at-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, value...)
	return append(out, b[at:]...)
}
func clientTOMLKey(key string) string {
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			b, _ := json.Marshal(key)
			return string(b)
		}
	}
	return key
}
func clientTOMLSpans(b []byte) (map[string]clientTOMLSpan, map[string]int, int, error) {
	spans := map[string]clientTOMLSpan{}
	tables := map[string]int{}
	firstTable := len(b)
	var table []string
	parser := unstable.Parser{}
	parser.Reset(b)
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.KeyValue && node.Kind != unstable.Table && node.Kind != unstable.ArrayTable {
			continue
		}
		var key []string
		var keyStart int
		it := node.Key()
		for it.Next() {
			n := it.Node()
			if len(key) == 0 {
				keyStart = int(n.Raw.Offset)
			}
			key = append(key, string(n.Data))
		}
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			table = key
			start := bytes.LastIndex(b[:keyStart], []byte("\n")) + 1
			if start < firstTable {
				firstTable = start
			}
			end := bytes.IndexByte(b[keyStart:], '\n')
			if end < 0 {
				end = len(b)
			} else {
				end += keyStart + 1
			}
			if node.Kind == unstable.Table {
				tables[clientPointer(table)] = end
			}
			continue
		}
		path := append(append([]string{}, table...), key...)
		value := node.Value()
		if value.Kind != unstable.String {
			continue
		}
		spans[clientPointer(path)] = clientTOMLSpan{start: int(value.Raw.Offset), end: int(value.Raw.Offset + value.Raw.Length), keyStart: keyStart}
	}
	return spans, tables, firstTable, parser.Error()
}

func clientDocumentBlockers(kind string, doc *clientDocument) []string {
	var blockers []string
	if kind == "claude" {
		for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
			value, present, err := doc.field([]string{"env", name})
			if err != nil || present && value != "" {
				blockers = append(blockers, "选定配置存在认证冲突：env."+name+"；在客户端启动环境明确选择一种认证后重新预览")
			}
		}
	}
	if kind == "codex" {
		profile, present, err := doc.field([]string{"profile"})
		if err != nil {
			blockers = append(blockers, "选定配置的 profile 无法识别")
		}
		if present && profile != "" {
			for _, field := range []string{"model", "model_provider"} {
				if _, exists, e := doc.field([]string{"profiles", profile, field}); exists || e != nil {
					blockers = append(blockers, "选定配置的活动 profile 覆盖字段："+field)
				}
			}
		}
	}
	return blockers
}
