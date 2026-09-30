package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"
)

// Extension records use the client configuration tables, private Secrets, TTL,
// mutation mutex, and atomic file transaction. No tool or skill is executed.
type extensionCard struct {
	Kind         string   `json:"kind"`
	Version      string   `json:"contract_version"`
	SkillProject string   `json:"skill_project"`
	SkillUser    string   `json:"skill_user"`
	MCPProject   []string `json:"mcp_project"`
	MCPUser      []string `json:"mcp_user"`
}

func extensionCards() []extensionCard {
	return []extensionCard{
		{"codex", "0.158.0", ".agents/skills", ".agents/skills", []string{".codex/config.toml"}, []string{"config.toml"}},
		{"claude", "2.1.281", ".claude/skills", ".claude/skills", []string{".mcp.json"}, nil},
		{"opencode", "1.18.33", ".opencode/skills", ".config/opencode/skills", []string{"opencode.json", "opencode.jsonc"}, []string{".config/opencode/opencode.json"}},
		{"gemini", "nightly-d75234ca", ".gemini/skills", ".gemini/skills", []string{".gemini/settings.json"}, []string{".gemini/settings.json"}},
		{"cline", "4.1.21", ".cline/skills", ".cline/skills", nil, []string{"<所选实际 cline_mcp_settings.json>"}},
		{"roo", "3.53.0", ".roo/skills", ".roo/skills", []string{".roo/mcp.json"}, []string{"<所选实际 MCP settings 文件>"}},
		{"continue", "1.3.40", ".continue/skills", ".continue/skills", nil, []string{".continue/config.yaml"}},
		{"cursor", "3.20.21", ".cursor/skills", ".cursor/skills", []string{".cursor/mcp.json"}, []string{".cursor/mcp.json"}},
	}
}

type extensionInput struct {
	Client         string            `json:"client"`
	ClientVersion  string            `json:"client_version"`
	Scope          string            `json:"scope"`
	Root           string            `json:"authorized_root"`
	Path           string            `json:"explicit_path"`
	Name           string            `json:"name"`
	Transport      string            `json:"transport,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	BearerEnv      string            `json:"bearer_token_env_var,omitempty"`
	SecretDelivery string            `json:"secret_delivery,omitempty"`
	SourceRoot     string            `json:"source_root,omitempty"`
	Files          []string          `json:"files,omitempty"`
	RelatedPaths   []string          `json:"related_paths,omitempty"`
}
type extensionFile struct {
	Root          string `json:"root"`
	Path          string `json:"path"`
	Relative      string `json:"relative_path"`
	Before        []byte `json:"before"`
	Ours          []byte `json:"ours"`
	Existed       bool   `json:"existed"`
	BeforeHash    string `json:"before_hash"`
	AfterHash     string `json:"after_hash"`
	SourcePath    string `json:"source_path,omitempty"`
	SourceRoot    string `json:"source_root,omitempty"`
	SourceHash    string `json:"source_hash,omitempty"`
	Name          string `json:"name,omitempty"`
	Entry         any    `json:"entry,omitempty"`
	ParentExisted bool   `json:"parent_existed"`
}
type extensionPrivate struct {
	Kind           string          `json:"kind"`
	Client         string          `json:"client"`
	ClientVersion  string          `json:"client_version"`
	Scope          string          `json:"scope"`
	SecretDelivery string          `json:"secret_delivery"`
	Files          []extensionFile `json:"files"`
}

func (a *App) configExtensionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/config-extensions" {
		if r.Method != "GET" {
			fail(w, 405, "方法不支持", "")
			return
		}
		writeJSON(w, 200, map[string]any{"items": extensionCards(), "validation": "configuration_contract_only", "activation": "unverified"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[3] != "preview" || (parts[2] != "mcp" && parts[2] != "skills") {
		fail(w, 404, "配置扩展操作不存在", "")
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in extensionInput
	if !decode(w, r, &in) {
		return
	}
	var card extensionCard
	found := false
	for _, c := range extensionCards() {
		if c.Kind == in.Client {
			card = c
			found = true
			break
		}
	}
	if !found {
		fail(w, 422, "该客户端没有已定义的 MCP/Skills 文件合同", "client")
		return
	}
	if in.Scope != "user" && in.Scope != "project" {
		fail(w, 400, "请选择 user 或 project scope", "scope")
		return
	}
	if !extensionName(in.Name) {
		fail(w, 400, "名称必须为一个普通目录或配置名字，不能含路径", "name")
		return
	}
	// The explicit version identifies the fixed configuration card. It is not
	// an assertion that the selected plugin has been activated by its host.
	if in.ClientVersion != card.Version && !(in.Client == "codex" && in.ClientVersion == "0.156.1") && !(in.Client == "opencode" && in.ClientVersion == "1.18.27") {
		fail(w, 422, "此版本的配置文件合同没有核验，请选择对应的固定配置卡", "client_version")
		return
	}
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.storageFailed.Load() {
		fail(w, 503, storageError().Error(), "")
		return
	}
	private := extensionPrivate{Kind: parts[2], Client: in.Client, ClientVersion: in.ClientVersion, Scope: in.Scope, SecretDelivery: "env_reference"}
	var err error
	if private.Kind == "mcp" {
		hasFileSecrets := len(in.Env) > 0 || len(in.Headers) > 0
		if in.Client == "cursor" && len(in.Env) == 0 && extensionCursorReferences(in.Headers) {
			hasFileSecrets = false
		}
		if hasFileSecrets {
			if in.SecretDelivery != "private_file" {
				fail(w, 422, "env/header 值可能包含秘密；请明确选择所选0600配置文件作为秘密落点，或使用客户端支持的环境引用", "secret_delivery")
				return
			}
			private.SecretDelivery = "private_file"
		}
		var file extensionFile
		file, err = extensionMCPPreview(in, card)
		if err == nil {
			private.Files = []extensionFile{file}
		}
	} else {
		private.Files, err = extensionSkillsPreview(in, card)
	}
	if err != nil {
		var problem *extensionProblem
		if errors.As(err, &problem) {
			if problem.diff != nil {
				requestID := id("req")
				w.Header().Set("X-Gateway-Request-Id", requestID)
				writeJSON(w, problem.status, map[string]any{"error": map[string]any{"message": problem.message, "type": "gateway_error", "field": problem.field, "param": problem.field}, "request_id": requestID, "redacted_diff": problem.diff})
			} else {
				fail(w, problem.status, problem.message, problem.field)
			}
		} else {
			fail(w, 422, "配置文件无法安全读取或无损编辑；没有修改原文件", "explicit_path")
		}
		return
	}
	preview := clientPreview{ID: id("cprev"), Kind: private.Kind, Scope: in.Scope, Version: in.ClientVersion, Status: "ready", ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Files: []clientFilePreview{}, Blockers: []string{}, Warnings: []string{"只管理本次选择的配置和文件；没有运行 MCP 命令、技能脚本或模型测试。", "文件写入不代表宿主已激活 MCP/Skills；未选择的全局/项目同名副本与配置优先级未检查。"}, Validation: map[string]any{"format": "passed", "adapter_contract": "cove-v1.2", "activation": "unverified", "client": in.Client, "client_version_source": "explicit_configuration_card"}, RequiredSecret: map[string]any{"mode": private.SecretDelivery, "persisted_in_private_store": true}}
	for _, file := range private.Files {
		field := file.Relative
		if private.Kind == "mcp" {
			field = "MCP." + file.Name
		}
		preview.Files = append(preview.Files, clientFilePreview{Path: file.Path, Exists: file.Existed, BaseHash: file.BeforeHash, Diff: []clientDiff{{Field: field, BeforePresent: file.Existed, Before: "<原值私有保存>", AfterPresent: true, After: map[string]any{"sha256": file.AfterHash, "size": len(file.Ours), "source_sha256": file.SourceHash}, Action: map[bool]string{true: "replace", false: "add"}[file.Existed]}}})
	}
	if private.Kind == "skills" {
		preview.Validation["format"] = "file_copy_only"
		if private.Client == "continue" {
			preview.Validation["frontmatter"] = "passed"
		}
	}
	if private.SecretDelivery == "private_file" {
		preview.Warnings = append(preview.Warnings, "所选配置文件将包含本次 env/header 值，权限为0600；diff和普通DB不显示这些值。")
	}
	if err = a.expireClientPreviews(time.Now().UTC()); err != nil {
		fail(w, 503, "预览清理失败；原文件保留", "")
		return
	}
	ref := "client-preview-" + preview.ID
	b, err := json.Marshal(private)
	if err != nil || a.Secrets.Put(ref, string(b)) != nil {
		fail(w, 503, "私有恢复记录保存失败；原文件保留", "")
		return
	}
	if _, err = a.Store.DB.Exec("INSERT INTO client_previews(id,expires_at,secret_ref,data) VALUES(?,?,?,?)", preview.ID, preview.ExpiresAt.Format(time.RFC3339Nano), ref, encode(preview)); err != nil {
		if a.Secrets.Delete(ref) != nil {
			a.markStorageFailure()
		}
		fail(w, 503, "预览保存失败；原文件保留", "")
		return
	}
	writeJSON(w, 200, preview)
}

type extensionProblem struct {
	status         int
	message, field string
	diff           []clientDiff
}

func (e *extensionProblem) Error() string { return e.message }
func extensionError(status int, message, field string) error {
	return &extensionProblem{status: status, message: message, field: field}
}
func extensionName(name string) bool {
	return name != "" && len(name) <= 128 && filepath.IsLocal(name) && name != "." && !strings.ContainsAny(name, "/\\\r\n\x00")
}
func extensionMCPPath(in extensionInput, card extensionCard) error {
	rel, err := clientRelativePath(in.Root, in.Path)
	if err != nil {
		return err
	}
	if in.Client == "claude" && in.Scope == "user" {
		return extensionError(422, "manual_action_required：Claude 用户scope由官方 claude mcp 命令管理；Cove不修改登录存储", "scope")
	}
	if in.Client == "cline" && in.Scope == "user" && filepath.Base(rel) == "cline_mcp_settings.json" {
		return nil
	}
	if in.Client == "roo" && in.Scope == "user" && strings.HasSuffix(rel, ".json") && filepath.Base(rel) != "auth.json" {
		return nil
	}
	allowed := card.MCPProject
	if in.Scope == "user" {
		allowed = card.MCPUser
	}
	for _, path := range allowed {
		if filepath.ToSlash(rel) == path {
			return nil
		}
	}
	return extensionError(422, "所选scope/path没有该客户端的自动 MCP 配置合同", "explicit_path")
}
func extensionDefinition(in extensionInput) (map[string]any, error) {
	if in.Transport != "stdio" && in.Transport != "http" {
		return nil, extensionError(400, "请选择 stdio 或 http transport", "transport")
	}
	def := map[string]any{}
	if in.Transport == "stdio" {
		if strings.TrimSpace(in.Command) == "" || strings.ContainsAny(in.Command, "\x00\r\n") {
			return nil, extensionError(400, "请填写客户端将使用的 command；Cove不会执行它", "command")
		}
		if in.URL != "" || len(in.Headers) > 0 || in.BearerEnv != "" {
			return nil, extensionError(400, "stdio配置不能包含HTTP字段", "transport")
		}
		if in.Client == "opencode" {
			def["type"] = "local"
			def["command"] = append([]string{in.Command}, in.Args...)
			def["enabled"] = true
			if len(in.Env) > 0 {
				def["environment"] = in.Env
			}
		} else {
			def["command"] = in.Command
			if len(in.Args) > 0 {
				def["args"] = in.Args
			}
			if len(in.Env) > 0 {
				def["env"] = in.Env
			}
			switch in.Client {
			case "claude", "roo":
				def["type"] = "stdio"
				if in.Client == "roo" {
					def["disabled"] = false
				}
			case "cline":
				def["disabled"] = false
			}
		}
	} else {
		parsed, err := url.Parse(in.URL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, extensionError(400, "请填写 http/https MCP URL，不含用户凭据", "url")
		}
		if in.Command != "" || len(in.Args) > 0 || len(in.Env) > 0 {
			return nil, extensionError(400, "HTTP配置不能包含stdio字段", "transport")
		}
		switch in.Client {
		case "codex":
			def["url"] = in.URL
			if in.BearerEnv != "" {
				if !extensionEnvName(in.BearerEnv) {
					return nil, extensionError(400, "bearer_token_env_var必须是环境变量名字", "bearer_token_env_var")
				}
				def["bearer_token_env_var"] = in.BearerEnv
			}
			if len(in.Headers) > 0 {
				def["http_headers"] = in.Headers
			}
		case "gemini":
			def["httpUrl"] = in.URL
			if len(in.Headers) > 0 {
				def["headers"] = in.Headers
			}
		case "opencode":
			def["type"] = "remote"
			def["url"] = in.URL
			def["enabled"] = true
			if len(in.Headers) > 0 {
				def["headers"] = in.Headers
			}
		case "continue":
			def["type"] = "streamable-http"
			def["url"] = in.URL
			if len(in.Headers) > 0 {
				def["requestOptions"] = map[string]any{"headers": in.Headers}
			}
		default:
			def["url"] = in.URL
			if in.Client == "claude" || in.Client == "roo" {
				def["type"] = "http"
			}
			if len(in.Headers) > 0 {
				def["headers"] = in.Headers
			}
		}
		if in.BearerEnv != "" && in.Client != "codex" {
			return nil, extensionError(422, "此客户端未核验 bearer_token_env_var 字段；使用官方支持的 header 引用或明确私有文件", "bearer_token_env_var")
		}
	}
	if in.Client == "continue" {
		def["name"] = in.Name
	}
	// Normalize slices/maps to JSON scalar types for semantic three-way equality.
	b, _ := json.Marshal(def)
	var normalized map[string]any
	_ = json.Unmarshal(b, &normalized)
	return normalized, nil
}
func extensionEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func extensionMCPPreview(in extensionInput, card extensionCard) (extensionFile, error) {
	file := extensionFile{Root: in.Root, Path: in.Path, Name: in.Name}
	if err := extensionMCPPath(in, card); err != nil {
		return file, err
	}
	target, err := openClientFile(in.Root, in.Path, false)
	if err != nil {
		return file, err
	}
	defer target.close()
	file.Before, file.Existed, err = target.read()
	if err != nil {
		return file, err
	}
	file.BeforeHash = clientHash(file.Before, file.Existed)
	def, err := extensionDefinition(in)
	if err != nil {
		return file, err
	}
	file.Entry = def
	old, present, parent, err := extensionReadMCP(in.Client, in.Name, file.Before, file.Existed)
	if err != nil {
		return file, err
	}
	file.ParentExisted = parent
	if present && !reflect.DeepEqual(old, def) {
		return file, &extensionProblem{status: 409, message: "同名 MCP 定义已存在且不同；保留原定义，请选择新的名字", field: "name", diff: extensionConflictDiff(old, def)}
	}
	file.Ours = file.Before
	if !present {
		file.Ours, err = extensionEditMCP(in.Client, in.Name, file.Before, file.Existed, def, true, false)
		if err != nil {
			return file, err
		}
	}
	file.AfterHash = clientHash(file.Ours, true)
	file.Relative, _ = filepath.Rel(in.Root, in.Path)
	return file, nil
}
func extensionSkillsPreview(in extensionInput, card extensionCard) ([]extensionFile, error) {
	prefix := card.SkillProject
	if in.Scope == "user" {
		prefix = card.SkillUser
	}
	desired := filepath.Join(in.Root, filepath.FromSlash(prefix), in.Name)
	if in.Path != desired {
		return nil, extensionError(400, "技能目标必须为所选客户端scope下的选定名字目录", "explicit_path")
	}
	if len(in.Files) == 0 || len(in.Files) > 64 {
		return nil, extensionError(400, "请明确列出1至64个相对文件，包含SKILL.md；不自动枚举目录", "files")
	}
	seen := map[string]bool{}
	hasSkill := false
	total := 0
	var files []extensionFile
	var skillName string
	for _, relative := range in.Files {
		if !filepath.IsLocal(relative) || filepath.Clean(relative) != relative || relative == "." || seen[relative] {
			return nil, extensionError(400, "文件清单包含重复或越界路径", "files")
		}
		seen[relative] = true
		source := filepath.Join(in.SourceRoot, relative)
		targetPath := filepath.Join(in.Path, relative)
		origin, err := openClientFile(in.SourceRoot, source, false)
		if err != nil {
			return nil, extensionError(400, "技能源文件包含符号链接或越界路径", "files")
		}
		content, exists, err := origin.read()
		origin.close()
		if err != nil || !exists {
			return nil, extensionError(422, "技能源文件缺失、不可读或超限", "files")
		}
		total += len(content)
		if total > 8<<20 {
			return nil, extensionError(413, "所选技能文件总大小超过8MiB", "files")
		}
		if relative == "SKILL.md" {
			hasSkill = true
			skillName, err = extensionSkillName(content)
			if in.Client == "continue" && err != nil {
				return nil, extensionError(422, "Continue SKILL.md 必须有非空name/description frontmatter", "files")
			}
		}
		target, err := openClientFile(in.Root, targetPath, false)
		if err != nil {
			return nil, extensionError(400, "技能目标含符号链接或越界路径", "explicit_path")
		}
		before, existed, err := target.read()
		target.close()
		if err != nil {
			return nil, err
		}
		files = append(files, extensionFile{Root: in.Root, Path: targetPath, Relative: relative, Before: before, Ours: content, Existed: existed, BeforeHash: clientHash(before, existed), AfterHash: clientHash(content, true), SourceRoot: in.SourceRoot, SourcePath: source, SourceHash: clientHash(content, true)})
	}
	if !hasSkill {
		return nil, extensionError(400, "技能清单必须明确包含顶层 SKILL.md", "files")
	}
	for _, related := range in.RelatedPaths {
		if filepath.Base(related) != "SKILL.md" {
			return nil, extensionError(400, "同名副本检查只能选择SKILL.md", "related_paths")
		}
		peer, err := openClientFile(in.Root, related, false)
		if err != nil {
			return nil, extensionError(400, "同名副本检查仅接受所选根内的明确SKILL.md路径", "related_paths")
		}
		b, exists, err := peer.read()
		peer.close()
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		name, _ := extensionSkillName(b)
		if related != filepath.Join(in.Path, "SKILL.md") && (name != "" && name == skillName || filepath.Base(filepath.Dir(related)) == in.Name) {
			return nil, extensionError(409, "所选范围已有同名技能副本；请选定唯一目录并处理优先级", "related_paths")
		}
	}
	return files, nil
}
func extensionSkillName(b []byte) (string, error) {
	if !bytes.HasPrefix(b, []byte("---\n")) {
		return "", errors.New("无frontmatter")
	}
	end := bytes.Index(b[4:], []byte("\n---"))
	if end < 0 {
		return "", errors.New("frontmatter未结束")
	}
	var meta map[string]any
	if err := yaml.Unmarshal(b[4:4+end], &meta); err != nil {
		return "", err
	}
	name, ok := meta["name"].(string)
	description, descriptionOK := meta["description"].(string)
	if !ok || !descriptionOK || strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" {
		return "", errors.New("frontmatter字段缺失")
	}
	return name, nil
}

// Called by applyClientConfig after its shared TTL and duplicate-preview checks,
// with clientMu held. The source snapshots and all target files are preflighted
// before the first write; interrupted progress remains in client_changes.
func (a *App) applyConfigExtension(w http.ResponseWriter, r *http.Request, preview clientPreview, ref string, in clientApplyInput) {
	var private extensionPrivate
	raw, err := a.Secrets.Get(ref)
	if err != nil || json.Unmarshal([]byte(raw), &private) != nil {
		fail(w, 503, "扩展私有记录不可读；原文件保留", "")
		return
	}
	if in.SecretDelivery != private.SecretDelivery {
		fail(w, 409, "秘密交付方式与预览不匹配", "secret_delivery")
		return
	}
	if len(in.BaseHashes) != len(private.Files) {
		fail(w, 409, "请提交全部选定文件的预览hash", "base_hashes")
		return
	}
	targets := make([]*clientTarget, 0, len(private.Files))
	defer func() {
		for _, target := range targets {
			target.close()
		}
	}()
	for _, file := range private.Files {
		if in.BaseHashes[file.Path] != file.BeforeHash {
			fail(w, 409, "选定文件hash与预览不一致", "base_hashes")
			return
		}
		if file.SourcePath != "" {
			source, e := openClientFile(file.SourceRoot, file.SourcePath, false)
			if e != nil {
				fail(w, 409, "技能源路径已变化，请重新预览", "files")
				return
			}
			b, exists, e := source.read()
			source.close()
			if e != nil || clientHash(b, exists) != file.SourceHash {
				fail(w, 409, "技能源文件已变化，请重新预览", "files")
				return
			}
		}
		target, e := openClientFile(file.Root, file.Path, true)
		if e != nil {
			fail(w, 409, "配置目标路径已变化或含符号链接", "explicit_path")
			return
		}
		targets = append(targets, target)
		b, exists, e := target.read()
		if e != nil || clientHash(b, exists) != file.BeforeHash {
			fail(w, 409, "选定配置文件已变化，请重新预览", "base_hashes")
			return
		}
	}
	c := clientChange{ID: id("cchange"), PreviewID: preview.ID, Kind: private.Kind, Scope: private.Scope, Version: private.ClientVersion, State: "applying", CreatedAt: time.Now().UTC(), Files: []map[string]any{}}
	if len(private.Files) > 0 {
		c.Path = private.Files[0].Path
	}
	for _, file := range private.Files {
		c.Files = append(c.Files, map[string]any{"path": file.Path, "status": "pending", "base_hash": file.BeforeHash, "after_hash": file.AfterHash})
	}
	if _, err = a.Store.DB.Exec("INSERT INTO client_changes(id,preview_id,secret_ref,data) VALUES(?,?,?,?)", c.ID, c.PreviewID, ref, encode(c)); err != nil {
		fail(w, 503, "变更记录保存失败；原文件保留", "")
		return
	}
	completed := 0
	for index, file := range private.Files {
		changed := false
		if file.BeforeHash != file.AfterHash {
			changed, err = targets[index].replace(file.Ours, file.BeforeHash, false)
		}
		if err != nil {
			c.Files[index]["status"] = "failed"
			if changed {
				c.Files[index]["status"] = "partial"
			}
			c.State = "failed"
			if completed > 0 || changed {
				c.State = "partial"
			}
			a.finishExtensionFailure(w, c, err)
			return
		}
		completed++
		c.Files[index]["status"] = "applied"
		if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
			a.markStorageFailure()
			writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已应用但进度保存失败；已保留恢复记录，请重启检查"}, "change": c})
			return
		}
	}
	c.State = "applied"
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		a.markStorageFailure()
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已应用但完成状态保存失败，请重启检查"}, "change": c})
		return
	}
	writeJSON(w, 201, c)
}

type extensionRestoreInput struct {
	CurrentHashes map[string]string `json:"current_hashes"`
	Resolutions   map[string]string `json:"resolutions,omitempty"`
}
type extensionRestoreFile struct {
	Path        string `json:"path"`
	CurrentHash string `json:"current_hash"`
	Action      string `json:"action"`
	Field       string `json:"field"`
	Size        int    `json:"size"`
}
type extensionRestorePreview struct {
	ID     string                 `json:"change_id"`
	Status string                 `json:"status"`
	Files  []extensionRestoreFile `json:"files"`
}

func (a *App) restoreConfigExtension(w http.ResponseWriter, r *http.Request, c clientChange, ref, action string) {
	var private extensionPrivate
	raw, err := a.Secrets.Get(ref)
	if err != nil || json.Unmarshal([]byte(raw), &private) != nil {
		fail(w, 503, "扩展私有恢复记录不可读", "")
		return
	}
	preview := extensionRestorePreview{ID: c.ID, Status: "ready", Files: []extensionRestoreFile{}}
	targets := []*clientTarget{}
	defer func() {
		for _, target := range targets {
			target.close()
		}
	}()
	current := [][]byte{}
	exists := []bool{}
	done := map[string]bool{}
	for _, path := range c.RestoredFields {
		done[path] = true
	}
	for _, file := range private.Files {
		if done[file.Path] {
			continue
		}
		target, e := openClientFile(file.Root, file.Path, false)
		if e != nil {
			fail(w, 409, "恢复目标路径已变化或含符号链接", "explicit_path")
			return
		}
		targets = append(targets, target)
		b, present, e := target.read()
		if e != nil {
			fail(w, 422, "恢复文件不可安全读取", "explicit_path")
			return
		}
		current = append(current, b)
		exists = append(exists, present)
		hash := clientHash(b, present)
		decision := "conflict"
		if private.Kind == "skills" {
			if hash == file.BeforeHash {
				decision = "already_before"
			} else if hash == file.AfterHash {
				decision = "restore_before"
			}
		} else {
			value, found, _, e := extensionReadMCP(private.Client, file.Name, b, present)
			old, was, _, e2 := extensionReadMCP(private.Client, file.Name, file.Before, file.Existed)
			if e != nil || e2 != nil {
				fail(w, 422, "unsupported_format：当前MCP配置不能无损恢复", "explicit_path")
				return
			}
			if found == was && reflect.DeepEqual(value, old) {
				decision = "already_before"
			} else if found && reflect.DeepEqual(value, file.Entry) {
				decision = "restore_before"
			}
		}
		if decision == "conflict" {
			preview.Status = "conflict"
		}
		field := file.Relative
		if private.Kind == "mcp" {
			field = "MCP." + file.Name
		}
		preview.Files = append(preview.Files, extensionRestoreFile{Path: file.Path, CurrentHash: hash, Action: decision, Field: field, Size: len(b)})
	}
	if action == "restore-preview" {
		writeJSON(w, 200, preview)
		return
	}
	var in extensionRestoreInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.CurrentHashes) != len(preview.Files) {
		fail(w, 409, "请提交全部恢复文件的当前hash", "current_hashes")
		return
	}
	for _, file := range preview.Files {
		if in.CurrentHashes[file.Path] != file.CurrentHash {
			fail(w, 409, "文件已在恢复预览后变化，请重新预览", "current_hashes")
			return
		}
		resolution := in.Resolutions[file.Path]
		if file.Action == "conflict" && resolution != "keep_current" && resolution != "restore_before" {
			fail(w, 409, "用户已修改所选字段/文件，请逐项选择保留或还原", "resolutions")
			return
		}
	}
	for path, resolution := range in.Resolutions {
		valid := false
		for _, file := range preview.Files {
			if file.Path == path && file.Action == "conflict" && (resolution == "keep_current" || resolution == "restore_before") {
				valid = true
			}
		}
		if !valid {
			fail(w, 400, "恢复选择与冲突不匹配", "resolutions")
			return
		}
	}
	outputs := [][]byte{}
	remove := []bool{}
	index := 0
	for _, file := range private.Files {
		if done[file.Path] {
			continue
		}
		row := preview.Files[index]
		b := current[index]
		del := false
		if row.Action != "already_before" && in.Resolutions[file.Path] != "keep_current" {
			if private.Kind == "skills" {
				b = file.Before
				del = !file.Existed
			} else if !file.Existed && row.CurrentHash == file.AfterHash {
				del = true
			} else {
				old, present, _, _ := extensionReadMCP(private.Client, file.Name, file.Before, file.Existed)
				b, err = extensionEditMCP(private.Client, file.Name, b, exists[index], old, present, !file.ParentExisted)
				if err != nil {
					fail(w, 422, "unsupported_format：恢复不能保留其他MCP字段", "explicit_path")
					return
				}
			}
		}
		outputs = append(outputs, b)
		remove = append(remove, del)
		index++
	}
	c.State = "restoring"
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		fail(w, 503, "恢复状态保存失败；原文件保留", "")
		return
	}
	for i, file := range preview.Files {
		changed := false
		if file.Action != "already_before" && in.Resolutions[file.Path] != "keep_current" {
			changed, err = targets[i].replace(outputs[i], file.CurrentHash, remove[i])
		}
		if err != nil {
			c.State = "failed"
			if i > 0 || changed {
				c.State = "partial"
			}
			a.finishExtensionFailure(w, c, err)
			return
		}
		c.RestoredFields = append(c.RestoredFields, file.Path)
		for _, row := range c.Files {
			if row["path"] == file.Path {
				row["status"] = "restored"
				row["resolution"] = in.Resolutions[file.Path]
			}
		}
		if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
			a.markStorageFailure()
			writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已恢复但进度保存失败，请重启检查"}, "change": c})
			return
		}
	}
	c.State = "restored"
	if _, err = a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); err != nil {
		a.markStorageFailure()
		writeJSON(w, 503, map[string]any{"error": map[string]any{"message": "文件已恢复但完成状态保存失败，请重启检查"}, "change": c})
		return
	}
	writeJSON(w, 200, c)
}
func (a *App) finishExtensionFailure(w http.ResponseWriter, c clientChange, err error) {
	if _, e := a.Store.DB.Exec("UPDATE client_changes SET data=? WHERE id=?", encode(c), c.ID); e != nil {
		a.markStorageFailure()
	}
	status := 503
	message := "文件操作失败；每文件进度与恢复记录保留，未盲目回滚其他文件"
	if errors.Is(err, errClientHashChanged) {
		status = 409
		message = "所选文件被其他程序修改；已保留每文件进度，请重新预览恢复"
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}, "change": c})
}

func extensionReadMCP(client, name string, b []byte, exists bool) (any, bool, bool, error) {
	if !exists {
		return nil, false, false, nil
	}
	if client == "continue" {
		root, seq, err := extensionYAML(b)
		if err != nil {
			return nil, false, false, err
		}
		_ = root
		if seq == nil {
			return nil, false, false, nil
		}
		for _, item := range seq.Content {
			var def map[string]any
			if err = item.Decode(&def); err != nil {
				return nil, false, false, err
			}
			if def["name"] == name {
				return extensionNormalize(def), true, true, nil
			}
		}
		return nil, false, true, nil
	}
	kind := "claude"
	if client == "opencode" {
		kind = "opencode"
	}
	if client == "codex" {
		kind = "codex"
	}
	doc, err := parseClientDocument(kind, "", b, true)
	if err != nil {
		return nil, false, false, err
	}
	parent := "mcpServers"
	if client == "codex" {
		parent = "mcp_servers"
	}
	if client == "opencode" {
		parent = "mcp"
	}
	_, parentExists, e := doc.lookup([]string{parent})
	if e != nil {
		return nil, false, false, e
	}
	value, present, e := doc.lookup([]string{parent, name})
	return extensionNormalize(value), present, parentExists, e
}
func extensionNormalize(value any) any {
	b, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(b, &normalized)
	return normalized
}
func extensionEditMCP(client, name string, b []byte, exists bool, value any, present, prune bool) ([]byte, error) {
	if client == "codex" {
		return extensionEditTOMLMCP(name, b, value, present)
	}
	if client == "continue" {
		return extensionEditYAMLMCP(name, b, exists, value, present, prune)
	}
	if !exists {
		b = []byte("{}\n")
	}
	v, err := hujson.Parse(b)
	if err != nil {
		return nil, err
	}
	if err = clientUniqueJSON(v); err != nil {
		return nil, err
	}
	parent := "mcpServers"
	if client == "opencode" {
		parent = "mcp"
	}
	path := []string{parent, name}
	if !present {
		if err = clientJSONDelete(&v, path); err != nil {
			return nil, err
		}
		if prune {
			if at := v.Find(clientPointer([]string{parent})); at != nil {
				if object, ok := at.Value.(*hujson.Object); ok && len(object.Members) == 0 && !bytes.Contains(object.AfterExtra, []byte("//")) && !bytes.Contains(object.AfterExtra, []byte("/*")) {
					if err = clientJSONDelete(&v, []string{parent}); err != nil {
						return nil, err
					}
				}
			}
		}
	} else {
		if v.Find(clientPointer([]string{parent})) == nil {
			if err = clientJSONField(&v, []string{parent, "__cove_temporary__"}, true, ""); err != nil {
				return nil, err
			}
			if err = clientJSONDelete(&v, []string{parent, "__cove_temporary__"}); err != nil {
				return nil, err
			}
		}
		node := v.Find(clientPointer(path))
		if node == nil {
			if err = clientJSONField(&v, path, true, ""); err != nil {
				return nil, err
			}
			node = v.Find(clientPointer(path))
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		entry, err := hujson.Parse(encoded)
		if err != nil {
			return nil, err
		}
		node.Value = entry.Value
	}
	out := v.Pack()
	if _, _, _, err = extensionReadMCP(client, name, out, true); err != nil {
		return nil, err
	}
	return out, nil
}

func extensionEditTOMLMCP(name string, b []byte, value any, present bool) ([]byte, error) {
	var decoded map[string]any
	if err := toml.Unmarshal(b, &decoded); err != nil {
		return nil, err
	}
	// Remove only expressions in this named server, retaining other tables and
	// standalone/trailing comments. Inline parent definitions are unsupported.
	type span struct {
		start, end int
		comments   []byte
	}
	var spans []span
	var table []string
	parser := unstable.Parser{KeepComments: true}
	parser.Reset(b)
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.KeyValue && node.Kind != unstable.ArrayTable {
			continue
		}
		var key []string
		start := 0
		it := node.Key()
		for it.Next() {
			n := it.Node()
			if len(key) == 0 {
				start = int(n.Raw.Offset)
			}
			key = append(key, string(n.Data))
		}
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			table = key
			if len(table) < 2 || table[0] != "mcp_servers" || table[1] != name {
				continue
			}
			lineStart := bytes.LastIndex(b[:start], []byte("\n")) + 1
			end := extensionTOMLEnd(b, lineStart, true)
			spans = append(spans, span{start: lineStart, end: end})
		} else {
			path := append(append([]string{}, table...), key...)
			if len(path) < 2 || path[0] != "mcp_servers" || path[1] != name {
				continue
			}
			eq := bytes.IndexByte(b[start:], '=')
			if eq < 0 {
				return nil, errors.New("无赋值")
			}
			end := extensionTOMLEnd(b, start+eq+1, false)
			spans = append(spans, span{start: start, end: end, comments: extensionTOMLComments(node.Value(), b)})
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	if servers, ok := decoded["mcp_servers"].(map[string]any); ok {
		if _, found := servers[name]; found && len(spans) == 0 {
			return nil, errors.New("unsupported_format：内联MCP表不能无损编辑")
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := append([]byte(nil), b...)
	for _, s := range spans {
		out = append(append(append([]byte{}, out[:s.start]...), s.comments...), out[s.end:]...)
	}
	if present {
		encoded, err := toml.Marshal(map[string]any{"mcp_servers": map[string]any{name: value}})
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(encoded), "\n")
		var stanza strings.Builder
		for _, line := range lines {
			if strings.TrimSpace(line) == "[mcp_servers]" {
				continue
			}
			stanza.WriteString(line)
			stanza.WriteByte('\n')
		}
		out = append(out, []byte("\n"+stanza.String())...)
	}
	if err := toml.Unmarshal(out, &decoded); err != nil {
		return nil, err
	}
	return out, nil
}
func extensionTOMLEnd(b []byte, start int, header bool) int {
	depth := 0
	quote := byte(0)
	triple := false
	escaped := false
	for i := start; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if quote == '"' && c == '\\' && !escaped {
				escaped = true
				continue
			}
			if c == quote && !escaped {
				if triple {
					if i+2 < len(b) && b[i+1] == quote && b[i+2] == quote {
						i += 2
						quote = 0
						triple = false
					}
				} else {
					quote = 0
				}
			}
			escaped = false
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			if i+2 < len(b) && b[i+1] == c && b[i+2] == c {
				triple = true
				i += 2
			}
			continue
		}
		if c == '#' {
			if depth == 0 {
				return i
			}
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		}
		if c == '\n' && depth == 0 {
			return i
		}
		if !header {
			if c == '[' || c == '{' {
				depth++
			}
			if c == ']' || c == '}' {
				depth--
			}
		}
	}
	return len(b)
}

func extensionYAML(b []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, errors.New("空配置")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("配置必须为mapping")
	}
	var check func(*yaml.Node) error
	check = func(n *yaml.Node) error {
		if n.Anchor != "" || n.Kind == yaml.AliasNode {
			return errors.New("unsupported_format：YAML anchors/aliases不能无损编辑")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if seen[key] {
					return errors.New("重复YAML字段")
				}
				seen[key] = true
			}
		}
		for _, child := range n.Content {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(root); err != nil {
		return nil, nil, err
	}
	var seq *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "mcpServers" {
			seq = root.Content[i+1]
			if seq.Kind != yaml.SequenceNode {
				return nil, nil, errors.New("mcpServers必须为列表")
			}
		}
	}
	if seq != nil {
		names := map[string]bool{}
		for _, item := range seq.Content {
			var def map[string]any
			if item.Kind != yaml.MappingNode || item.Decode(&def) != nil {
				return nil, nil, errors.New("MCP条目不是mapping")
			}
			name, ok := def["name"].(string)
			if !ok || name == "" || names[name] {
				return nil, nil, extensionError(409, "Continue MCP列表有重复或缺失name，不能取第一项覆盖", "name")
			}
			names[name] = true
		}
	}
	return root, seq, nil
}
func extensionLineOffsets(b []byte) []int {
	offsets := []int{0}
	for i, c := range b {
		if c == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}
func extensionEditYAMLMCP(name string, b []byte, exists bool, value any, present, prune bool) ([]byte, error) {
	if !exists {
		b = []byte("name: Cove local\nversion: 1.0.0\nschema: v1\n")
	}
	root, seq, err := extensionYAML(b)
	if err != nil {
		return nil, err
	}
	offsets := extensionLineOffsets(b)
	blockEnd := len(b)
	keyLine := -1
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "mcpServers" {
			keyLine = root.Content[i].Line - 1
			if i+2 < len(root.Content) {
				blockEnd = offsets[root.Content[i+2].Line-1]
			}
			break
		}
	}
	start, end := -1, -1
	if seq != nil {
		for i, item := range seq.Content {
			var def map[string]any
			_ = item.Decode(&def)
			if def["name"] != name {
				continue
			}
			start = offsets[item.Line-1]
			end = blockEnd
			if i+1 < len(seq.Content) {
				end = offsets[seq.Content[i+1].Line-1]
			}
			break
		}
	}
	out := append([]byte(nil), b...)
	if start >= 0 {
		var comments []byte
		for _, line := range bytes.SplitAfter(b[start:end], []byte("\n")) {
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("#")) {
				comments = append(comments, line...)
			}
		}
		replacement := comments
		if present {
			encoded, e := yaml.Marshal([]any{value})
			if e != nil {
				return nil, e
			}
			for _, line := range bytes.SplitAfter(encoded, []byte("\n")) {
				if len(line) > 0 {
					replacement = append(replacement, []byte("  ")...)
					replacement = append(replacement, line...)
				}
			}
		}
		out = append(append(append([]byte{}, b[:start]...), replacement...), b[end:]...)
		if !present && prune && seq != nil && len(seq.Content) == 1 {
			keyStart := offsets[keyLine]
			keyEnd := offsets[keyLine+1]
			out = append(out[:keyStart], out[keyEnd:]...)
		}
	} else if present {
		encoded, e := yaml.Marshal([]any{value})
		if e != nil {
			return nil, e
		}
		var entry []byte
		for _, line := range bytes.SplitAfter(encoded, []byte("\n")) {
			if len(line) > 0 {
				entry = append(entry, []byte("  ")...)
				entry = append(entry, line...)
			}
		}
		if seq == nil {
			if len(out) > 0 && out[len(out)-1] != '\n' {
				out = append(out, '\n')
			}
			out = append(out, []byte("mcpServers:\n")...)
			out = append(out, entry...)
		} else {
			out = clientInsertBytes(out, blockEnd, entry)
		}
	}
	if _, _, err = extensionYAML(out); err != nil {
		return nil, err
	}
	return out, nil
}

func extensionConflictDiff(before any, after map[string]any) []clientDiff {
	old, _ := before.(map[string]any)
	keys := map[string]bool{}
	for key := range old {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	var diff []clientDiff
	for _, key := range names {
		a, ap := old[key]
		b, bp := after[key]
		if ap == bp && reflect.DeepEqual(a, b) {
			continue
		}
		var previous, next any
		if ap {
			previous = "<原值保留>"
		}
		if bp {
			next = "<本次值私有保存>"
		}
		diff = append(diff, clientDiff{Field: key, BeforePresent: ap, Before: previous, AfterPresent: bp, After: next, Action: "conflict"})
	}
	return diff
}

func extensionTOMLComments(node *unstable.Node, data []byte) []byte {
	var out []byte
	if node.Kind == unstable.Comment {
		start, end := int(node.Raw.Offset), int(node.Raw.Offset+node.Raw.Length)
		out = append(out, data[start:end]...)
		out = append(out, '\n')
	}
	children := node.Children()
	for children.Next() {
		out = append(out, extensionTOMLComments(children.Node(), data)...)
	}
	return out
}

// Cursor's v1.2 contract explicitly supports these environment references;
// no plaintext token is needed for this path.
func extensionCursorReferences(headers map[string]string) bool {
	for _, value := range headers {
		value = strings.TrimPrefix(value, "Bearer ")
		if !strings.HasPrefix(value, "${env:") || !strings.HasSuffix(value, "}") {
			return false
		}
		if !extensionEnvName(strings.TrimSuffix(strings.TrimPrefix(value, "${env:"), "}")) {
			return false
		}
	}
	return true
}
