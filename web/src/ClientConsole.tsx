import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type ClientCard = { kind: string; name: string; version: string; contract_version: string; status: string; scopes: string[]; recommended_paths: Record<string, string[]>; evidence: string };
type FieldDiff = { field: string; before: unknown; after: unknown; action: string };
type Preview = { preview_id: string; expires_at: string; kind: string; status: string; client_version: string; files: { path: string; exists: boolean; base_hash: string; redacted_diff: FieldDiff[] }[]; blockers: string[]; warnings: string[]; required_secret: { env_name: string; instruction: string; codex_home?: string } };
type Change = { id: string; kind: string; scope: string; path: string; state: string; client_version: string; created_at: string; files: { status: string }[] };
type RestorePreview = { change_id: string; path: string; current_hash: string; status: string; fields: { field: string; action: string; before: unknown; ours: unknown; current: unknown }[] };
const statusLabel: Record<string, string> = { installed: "已安装，配置卡已核验", not_installed: "未安装", detection_failed: "版本检测失败", unsupported_version: "此版本配置合同未核验", applying: "应用未完成", applied: "文件已应用", restoring: "恢复未完成", restored: "已恢复", partial: "文件可能已改变，请预览恢复", failed: "写入失败" };
function show(value: unknown) { return value === null || value === undefined ? "未设置" : String(value); }

export function ClientConsole({ api, initialKind, initialModel, onChanged }: { api: API; initialKind?: string; initialModel?: string; onChanged?: () => Promise<void> }) {
  const [clients, setClients] = useState<ClientCard[]>([]), [changes, setChanges] = useState<Change[]>([]);
  const [kind, setKind] = useState(initialKind || "codex"), [scope, setScope] = useState(initialKind && initialKind !== "codex" ? "project" : "user");
  const [root, setRoot] = useState(""), [path, setPath] = useState(""), [model, setModel] = useState(initialModel || ""), [overrides, setOverrides] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null), [restore, setRestore] = useState<RestorePreview | null>(null), [resolutions, setResolutions] = useState<Record<string, string>>({});
  const [pending, setPending] = useState<Set<string>>(() => new Set()), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const revision = useRef(0), restoreRevision = useRef(0), loadRevision = useRef(0), mounted = useRef(true), running = useRef(new Set<string>());
  const configBusy = pending.has("preview") || pending.has("apply");
  const restoreBusy = !!restore && pending.has(`restore:${restore.change_id}`);
  const selected = clients.find((card) => card.kind === kind);
  async function load() {
    const version = ++loadRevision.current;
    const results = await Promise.allSettled([api<{ items: ClientCard[] }>("clients"), api<{ items: Change[] }>("client-changes")]);
    if (!mounted.current || version !== loadRevision.current) return;
    if (results[0].status === "fulfilled") setClients(results[0].value.items);
    if (results[1].status === "fulfilled") setChanges(results[1].value.items.filter(change => ["codex", "claude", "opencode"].includes(change.kind)));
    const errors = results.filter((result): result is PromiseRejectedResult => result.status === "rejected");
    if (errors.length) setError(errors.map((result) => result.reason instanceof Error ? result.reason.message : "客户端信息读取失败").join("；"));
  }
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; revision.current++; restoreRevision.current++; loadRevision.current++; }; }, [api]);
  function changed() { revision.current++; setPreview(null); setError(""); setNotice(""); }
  async function run(action: string, work: (selection: number) => Promise<void>, button?: HTMLButtonElement) {
    const historyAction = action.startsWith("restore:") || action.startsWith("restore-preview:");
    const resource = historyAction ? "change:" + action.split(":")[1] : action === "preview" || action === "apply" ? "config" : action;
    if (running.current.has(resource)) return;
    running.current.add(resource);
    const selection = historyAction ? restoreRevision.current : revision.current;
    setPending((old) => new Set(old).add(action)); setError(""); setNotice("");
    try { await work(selection); } catch (cause) { if (current(selection, historyAction)) setError(cause instanceof Error ? cause.message : "操作失败，请检查具体配置字段"); }
    finally {
      running.current.delete(resource);
      if (mounted.current) {
        setPending((old) => { const next = new Set(old); next.delete(action); return next; });
        if (current(selection, historyAction) && button?.isConnected) button.focus();
      }
    }
  }
  function current(selection: number, historyAction = false) { return mounted.current && (historyAction ? restoreRevision.current : revision.current) === selection; }
  async function createPreview(selection: number) {
    const result = await api<Preview>(`clients/${kind}/preview`, "POST", { scope, authorized_root: root, explicit_path: path, model, override_paths: overrides.split("\n").map((value) => value.trim()).filter(Boolean) });
    if (current(selection)) { setPreview(result); setNotice(result.status === "blocked" ? "预览被阻止，请按具体原因调整。文件保留。" : "预览已生成，核对路径、字段和 hash 后可应用。文件尚未修改。"); }
  }
  async function applyPreview(selection: number) {
    if (!preview) return;
    const result = await api<Change>("client-changes", "POST", { preview_id: preview.preview_id, base_hashes: Object.fromEntries(preview.files.map((file) => [file.path, file.base_hash])), secret_delivery: "env_reference" });
    if (current(selection)) { setNotice(`配置文件已应用（${result.id}）。请向客户端进程注入 ${preview.required_secret.env_name} 后自行启动；连接和工具尚未测试。`); setPreview(null); }
    await load();
    await onChanged?.();
  }
  async function previewRestore(change: Change, selection: number) {
    const result = await api<RestorePreview>(`client-changes/${change.id}/restore-preview`, "POST", {});
    if (current(selection, true)) { setRestore(result); setResolutions({}); setNotice(result.status === "conflict" ? "恢复发现用户后续修改，请逐字段选择。" : "恢复预览已生成，尚未修改文件。"); }
  }
  async function applyRestore(selection: number) {
    if (!restore) return;
    await api(`client-changes/${restore.change_id}/restore`, "POST", { current_hash: restore.current_hash, resolutions });
    if (current(selection, true)) { setNotice("选定字段已恢复，所选保留的用户修改继续保留。没有发送模型测试。"); setRestore(null); }
    await load();
    await onChanged?.();
  }
  const restoreConflicts = restore?.fields.filter((field) => field.action === "conflict") || [];
  return <>
    {error && <div className="error" role="alert" id="client-config-error">{error}</div>}
    {notice && <div className="notice" role="status" aria-live="polite">{notice}</div>}
    <section className="panel">
      <div className="section-title"><h2>客户端配置</h2><button disabled={pending.has("detect")} onClick={(event) => void run("detect", async () => { await load(); }, event.currentTarget)}>{pending.has("detect") ? "正在检测…" : "重新检测版本"}</button></div>
      <p>选择实际客户端和配置位置，再预览、应用或恢复。Cove 只修改显示的字段；Key 通过客户端进程环境交付。</p>
      {!clients.length && <p>尚无检测结果，可重新检测；检测失败会在上方显示原因。</p>}
      {clients.map((client) => <div className="tool-row" key={client.kind}><div><strong>{client.name}</strong><span>{client.version || "版本未知"} · {statusLabel[client.status] || client.status}</span></div><details><summary>配置卡依据</summary><p>{client.evidence}</p><p>规范基线版本 {client.contract_version}。本机版本另行验证的卡只证明原生配置解析。</p></details></div>)}
      <form aria-describedby={error ? "client-config-error" : undefined} onSubmit={(event) => { event.preventDefault(); void run("preview", createPreview); }}>
        <div className="inline-form">
          <label>客户端<select value={kind} disabled={configBusy} onChange={(event) => { changed(); setKind(event.target.value); setScope(event.target.value === "codex" ? "user" : "project"); setRoot(""); setPath(""); }}>{clients.length ? clients.map((client) => <option value={client.kind} key={client.kind}>{client.name}</option>) : <><option value="codex">Codex CLI</option><option value="claude">Claude Code</option><option value="opencode">OpenCode</option></>}</select></label>
          <label>作用域<select value={scope} disabled={configBusy} onChange={(event) => { changed(); setScope(event.target.value); setPath(""); }}><option value="user">用户 / 独立配置目录</option><option value="project">项目</option></select></label>
        </div>
        <label>授权目录（绝对路径）<input value={root} disabled={configBusy} required placeholder={kind === "codex" && scope === "user" ? "选定的独立 CODEX_HOME 目录" : scope === "user" ? "选定的用户主目录" : "选定的项目根目录"} onChange={(event) => { changed(); setRoot(event.target.value); }} /></label>
        <label>配置文件（绝对路径）<input value={path} disabled={configBusy} required placeholder={selected?.recommended_paths[scope]?.join(" 或 ") || "请选择配置文件"} onChange={(event) => { changed(); setPath(event.target.value); }} /></label>
        <label>公开模型 ID<input value={model} disabled={configBusy} required placeholder="例如 coding" onChange={(event) => { changed(); setModel(event.target.value); }} /></label>
        <details><summary>检查明确选定的其他配置</summary><p>如有更高优先级文件，可在这里逐行填写同一授权目录内的配置路径。未选定的管理配置和 CLI 参数须在启动客户端时自行确认。</p><label>额外配置文件（每行一个绝对路径）<textarea value={overrides} disabled={configBusy} rows={3} onChange={(event) => { changed(); setOverrides(event.target.value); }} /></label></details>
        <p>符号链接、目录外路径、未知版本或无法保留的格式会拒绝。已有密钥不会显示在 diff；原字段值私有保存供恢复。</p>
        <button disabled={configBusy}>{pending.has("preview") ? "正在预览…" : "生成字段预览"}</button>
      </form>
    </section>
    {preview && <section className="panel" aria-label="客户端配置预览">
      <h2>应用预览</h2><p>{preview.kind} · {preview.client_version} · {preview.status === "blocked" ? "被阻止" : "可应用"} · 预览到期 {new Date(preview.expires_at).toLocaleString()}</p>
      {preview.blockers.map((blocker, index) => <p className="error" key={index}>{blocker}</p>)}
      {preview.files.map((file) => <div key={file.path}><p><code>{file.path}</code> · {file.exists ? "修改已有文件" : "创建新文件"}</p><p>文件 hash <code>{file.base_hash}</code></p><div className="table-scroll"><table><thead><tr><th>字段</th><th>原值</th><th>Cove 值</th><th>动作</th></tr></thead><tbody>{file.redacted_diff.map((field) => <tr key={field.field}><td><code>{field.field}</code></td><td>{show(field.before)}</td><td>{show(field.after)}</td><td>{field.action === "add" ? "添加" : field.action === "remove" ? "删除" : "替换"}</td></tr>)}</tbody></table></div>{!file.redacted_diff.length && <p>所选字段已是该配置，无需修改。</p>}</div>)}
      <p>Key 环境变量 <code>{preview.required_secret.env_name}</code>。{preview.required_secret.instruction}</p>
      {preview.required_secret.codex_home && <p>启动该客户端时选择 <code>CODEX_HOME={preview.required_secret.codex_home}</code>，保留日常官方登录目录。</p>}
      {preview.warnings.map((warning, index) => <p key={index}>{warning}</p>)}
      <button disabled={configBusy || preview.status !== "ready" || Date.parse(preview.expires_at) <= Date.now()} onClick={(event) => void run("apply", applyPreview, event.currentTarget)}>{pending.has("apply") ? "正在应用…" : "应用所选文件"}</button>
    </section>}
    <section className="panel"><h2>配置变更与恢复</h2>
      {!changes.length && <p>还没有客户端配置变更。应用成功后可在这里预览恢复。</p>}
      {changes.map((change) => <div className="tool-row" key={change.id}><div><strong>{change.kind} · {statusLabel[change.state] || change.state}</strong><code>{change.path}</code><span>{new Date(change.created_at).toLocaleString()} · {change.scope} · {change.id}</span></div><button disabled={pending.has(`restore-preview:${change.id}`) || pending.has(`restore:${change.id}`) || change.state === "restored"} onClick={(event) => { if (running.current.has(`change:${change.id}`)) return; restoreRevision.current++; setRestore(null); void run(`restore-preview:${change.id}`, (selection) => previewRestore(change, selection), event.currentTarget); }}>{pending.has(`restore-preview:${change.id}`) ? "正在预览恢复…" : "预览恢复"}</button></div>)}
    </section>
    {restore && <section className="panel" aria-label="客户端恢复预览"><h2>恢复预览</h2><p><code>{restore.path}</code></p><p>当前 hash <code>{restore.current_hash}</code>。恢复只处理本次 Cove 修改的字段。</p>
      <div className="table-scroll"><table><thead><tr><th>字段</th><th>原值</th><th>Cove 值</th><th>当前值</th><th>恢复动作</th></tr></thead><tbody>{restore.fields.map((field) => <tr key={field.field}><td><code>{field.field}</code></td><td>{show(field.before)}</td><td>{show(field.ours)}</td><td>{show(field.current)}</td><td>{field.action === "conflict" ? <label>冲突处理<select aria-label={`${field.field} 冲突处理`} disabled={restoreBusy} value={resolutions[field.field] || ""} onChange={(event) => setResolutions((old) => ({ ...old, [field.field]: event.target.value }))}><option value="">请选择</option><option value="keep_current">保留用户当前值</option><option value="restore_before">还原私有保存的原值</option></select></label> : field.action === "already_before" ? "已是原值，无需改动" : "恢复原值"}</td></tr>)}</tbody></table></div>
      {!restore.fields.length && <p>本次记录的字段已经全部处理。</p>}
      <button disabled={restoreBusy || !restore.fields.length || restoreConflicts.some((field) => !resolutions[field.field])} onClick={(event) => void run(`restore:${restore.change_id}`, applyRestore, event.currentTarget)}>{restoreBusy ? "正在恢复…" : "按所选处理恢复"}</button>
      <button className="text" disabled={restoreBusy} onClick={() => { restoreRevision.current++; setRestore(null); }}>关闭预览</button>
    </section>}
  </>;
}
