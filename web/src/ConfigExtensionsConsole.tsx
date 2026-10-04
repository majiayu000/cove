import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type Card = { kind: string; contract_version: string; skill_project: string; skill_user: string; mcp_project: string[]; mcp_user: string[] };
type Preview = { preview_id: string; files: { path: string; exists: boolean; base_hash: string; redacted_diff: { after: { sha256: string; size: number; source_sha256?: string } }[] }[]; expires_at: string; warnings: string[]; required_secret: { mode: string } };
type Change = { id: string; kind: string; path: string; state: string; files: { path: string; status: string }[] };
type Restore = { change_id: string; status: string; files: { path: string; current_hash: string; action: string; field: string; size: number }[] };
const names: Record<string, string> = { codex: "Codex", claude: "Claude Code", opencode: "OpenCode", gemini: "Gemini CLI", cline: "Cline", roo: "Roo", continue: "Continue", cursor: "Cursor" };
const states: Record<string, string> = { applied: "文件已应用", partial: "部分完成，按记录恢复", failed: "文件操作失败", applying: "应用未完成", restoring: "恢复未完成", restored: "已恢复" };
const lines = (text: string) => text.split("\n").map((line) => line.trim()).filter(Boolean);

export function ConfigExtensionsConsole({ api }: { api: API }) {
  const [cards, setCards] = useState<Card[]>([]), [changes, setChanges] = useState<Change[]>([]);
  const [kind, setKind] = useState("mcp"), [client, setClient] = useState("codex"), [version, setVersion] = useState("0.158.0"), [scope, setScope] = useState("project");
  const [root, setRoot] = useState(""), [path, setPath] = useState(""), [name, setName] = useState("");
  const [transport, setTransport] = useState("stdio"), [command, setCommand] = useState(""), [args, setArgs] = useState(""), [url, setURL] = useState(""), [bearerEnv, setBearerEnv] = useState("");
  const [secretName, setSecretName] = useState(""), [secretValue, setSecretValue] = useState(""), [privateFile, setPrivateFile] = useState(false);
  const [sourceRoot, setSourceRoot] = useState(""), [files, setFiles] = useState("SKILL.md"), [relatedPaths, setRelatedPaths] = useState("");
  const [preview, setPreview] = useState<Preview | null>(null), [restore, setRestore] = useState<Restore | null>(null), [resolutions, setResolutions] = useState<Record<string, string>>({});
  const [pending, setPending] = useState(""), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [loading,setLoading]=useState(true),[errorField,setErrorField]=useState("");
  const inputError=(field:string)=>({"aria-invalid":!!error&&errorField===field||undefined,"aria-describedby":error&&errorField===field?"config-extension-error":undefined});
  const loadRevision=useRef(0);
  const configForm=useRef<HTMLFormElement>(null);
  const mounted = useRef(true), selection = useRef(0), focus = useRef<HTMLButtonElement | null>(null);
  const card = cards.find((item) => item.kind === client);
  const recommended = kind === "skills" ? `${scope === "project" ? card?.skill_project || "" : card?.skill_user || ""}/${name || "<name>"}` : (scope === "project" ? card?.mcp_project : card?.mcp_user)?.join(" 或 ");
  async function load() {
    const revision=++loadRevision.current;setLoading(true);
    const results = await Promise.allSettled([api<{ items: Card[] }>("config-extensions"), api<{ items: Change[] }>("client-changes")]);
    if (!mounted.current || revision!==loadRevision.current) return;
    setLoading(false);
    if (results[0].status === "fulfilled") setCards(results[0].value.items);
    if (results[1].status === "fulfilled") setChanges(results[1].value.items.filter((item) => item.kind === "mcp" || item.kind === "skills"));
    const failures = results.filter((result): result is PromiseRejectedResult => result.status === "rejected");
    setError(failures.map((result) => result.reason instanceof Error ? result.reason.message : "配置扩展读取失败").join("；"));
  }
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; selection.current++;loadRevision.current++; }; }, [api]);
  function invalidate() { selection.current++; setPreview(null); setNotice(""); setError("");setErrorField(""); }
  async function run(id: string, work: (revision: number) => Promise<void>, button?: HTMLButtonElement) {
    const revision = selection.current; focus.current = button || null; setPending(id); setError("");setErrorField(""); setNotice("");
    try { await work(revision); } catch (cause) {
      const failure=cause as Error&{field?:string;change?:Change};
      if(failure.change && current(revision))await load();
      if (current(revision)) {setError(cause instanceof Error ? cause.message : "操作失败，请重新预览");setErrorField(failure.field||"");if(failure.change){setPreview(null);setRestore(null);setNotice("文件操作未全部完成。每份文件的进度已保留，请在扩展变更记录中预览恢复。");}}
    }
    finally { if (mounted.current) { setPending("");requestAnimationFrame(()=>{if(!current(revision))return;const invalid=configForm.current?.querySelector<HTMLElement>('[aria-invalid="true"]');if(invalid)invalid.focus();else focus.current?.focus();}); } }
  }
  function current(revision: number) { return mounted.current && revision === selection.current; }
  async function makePreview(revision: number) {
    const input: Record<string, unknown> = { client, client_version: version, scope, authorized_root: root, explicit_path: path, name };
    if (kind === "skills") { input.source_root = sourceRoot; input.files = lines(files); input.related_paths = lines(relatedPaths); }
    else { input.transport = transport; input.secret_delivery = privateFile ? "private_file" : "env_reference";
      if (transport === "stdio") { input.command = command; input.args = args.split("\n").filter((value) => value !== ""); if (secretName) input.env = { [secretName]: secretValue }; }
      else { input.url = url; if (secretName) input.headers = { [secretName]: secretValue }; if (bearerEnv) input.bearer_token_env_var = bearerEnv; }
    }
    const result = await api<Preview>(`config-extensions/${kind}/preview`, "POST", input);
    if (current(revision)) { setPreview(result); setSecretValue(""); setNotice("预览已保存。文件尚未修改，输入的秘密只留在私有记录。没有运行客户端工具。"); }
  }
  async function apply(revision: number) {
    if (!preview) return;
    await api("client-changes", "POST", { preview_id: preview.preview_id, base_hashes: Object.fromEntries(preview.files.map((file) => [file.path, file.base_hash])), secret_delivery: preview.required_secret.mode });
    if (current(revision)) { if(configForm.current)configForm.current.dataset.dirty="false";setPreview(null); setNotice("所选配置文件已应用。MCP/Skills 是否激活仍需在客户端自己的批准流程中核验。没有执行命令或模型测试。"); }
    await load();
  }
  async function restorePreview(change: Change, revision: number) {
    const result = await api<Restore>(`client-changes/${change.id}/restore-preview`, "POST", {});
    if (current(revision)) { setRestore(result); setResolutions({}); setNotice(result.status === "conflict" ? "用户修改的同名 MCP 定义或技能文件已保留，请明确选择。" : "恢复预览已生成。文件尚未修改。"); }
  }
  async function restoreApply(revision: number) {
    if (!restore) return;
    await api(`client-changes/${restore.change_id}/restore`, "POST", { current_hashes: Object.fromEntries(restore.files.map((file) => [file.path, file.current_hash])), resolutions });
    if (current(revision)) { setRestore(null); setNotice("选定项目已按所选处理恢复；其他 MCP 定义和未列出的用户文件保留。"); }
    await load();
  }
  return <>
    {loading&&<p role="status">正在读取扩展配置…</p>}
    {error&&<button disabled={loading} onClick={()=>void load()}>重新读取扩展配置</button>}
    {error && <div className="error" role="alert" id="config-extension-error">{error}</div>}{notice && <div className="notice" role="status" aria-live="polite">{notice}</div>}
    <section className="panel"><h2>MCP 与 Skills 配置</h2><p>管理明确选择的 MCP 定义和技能文件。技能正文作为文件复制，Cove 不采纳其中指令，也不启动 MCP command 或执行脚本。</p>
      <form ref={configForm} aria-describedby={error ? "config-extension-error" : undefined} onSubmit={(event) => { event.preventDefault(); const submitter = event.currentTarget.querySelector("button"); void run("preview", makePreview, submitter || undefined); }}>
        <div className="inline-form"><label>内容<select aria-label="内容" {...inputError("kind")} value={kind} disabled={!!pending} onChange={(event) => { invalidate(); setKind(event.target.value); setPath(""); }}><option value="mcp">MCP 定义</option><option value="skills">Skills 文件</option></select></label>
          <label>客户端<select aria-label="客户端" {...inputError("client")} value={client} disabled={loading||!!pending} onChange={(event) => { invalidate(); setClient(event.target.value); setVersion(cards.find((item) => item.kind === event.target.value)?.contract_version || ""); setPath(""); }}>{Object.entries(names).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
          <label>作用域<select aria-label="作用域" {...inputError("scope")} value={scope} disabled={!!pending} onChange={(event) => { invalidate(); setScope(event.target.value); setPath(""); }}><option value="project">项目</option><option value="user">用户 / 明确选择的宿主目录</option></select></label></div>
        <label>配置卡版本<input {...inputError("client_version")} value={version} disabled={!!pending} required onChange={(event) => { invalidate(); setVersion(event.target.value); }} /></label><p>此版本标识所选配置合同，尚未证明宿主安装或激活。已定义基线 {card?.contract_version || "正在读取"}。</p>
        <label>授权目录（绝对路径）<input {...inputError("authorized_root")} value={root} disabled={!!pending} required onChange={(event) => { invalidate(); setRoot(event.target.value); }} /></label>
        <label>名字<input {...inputError("name")} value={name} disabled={!!pending} required onChange={(event) => { invalidate(); setName(event.target.value); }} /></label>
        <label>{kind === "skills" ? "目标技能目录（绝对路径）" : "目标配置文件（绝对路径）"}<input {...inputError("explicit_path")} value={path} disabled={!!pending} required placeholder={recommended || "该scope暂无自动写入合同"} onChange={(event) => { invalidate(); setPath(event.target.value); }} /></label>
        {kind === "skills" ? <>
          <label>明确选择的技能源目录（绝对路径）<input {...inputError("source_root")} value={sourceRoot} disabled={!!pending} required onChange={(event) => { invalidate(); setSourceRoot(event.target.value); }} /></label>
          <label>复制文件清单（每行一个相对路径）<textarea aria-label="复制文件清单（每行一个相对路径）" {...inputError("files")} value={files} disabled={!!pending} rows={4} required onChange={(event) => { invalidate(); setFiles(event.target.value); }} /></label><p>必须包含 SKILL.md；仅复制列出的文件，拒绝符号链接、重复路径和目录逃逸。</p>
          <label>明确选择的同名副本 SKILL.md（每行一个绝对路径，可选）<textarea {...inputError("related_paths")} value={relatedPaths} disabled={!!pending} rows={2} onChange={(event) => { invalidate(); setRelatedPaths(event.target.value); }} /></label>
        </> : <>
          <label>Transport<select aria-label="Transport" {...inputError("transport")} value={transport} disabled={!!pending} onChange={(event) => { invalidate(); setTransport(event.target.value); setSecretName(""); setSecretValue(""); setBearerEnv(""); }}><option value="stdio">stdio</option><option value="http">HTTP</option></select></label>
          {transport === "stdio" ? <><label>Command<input {...inputError("command")} value={command} disabled={!!pending} required onChange={(event) => { invalidate(); setCommand(event.target.value); }} /></label><label>Args（每行一个参数）<textarea {...inputError("args")} value={args} disabled={!!pending} rows={3} onChange={(event) => { invalidate(); setArgs(event.target.value); }} /></label></> : <><label>HTTP URL<input {...inputError("url")} type="url" value={url} disabled={!!pending} required onChange={(event) => { invalidate(); setURL(event.target.value); }} /></label>{client === "codex" && <label>Bearer token 环境变量名字（可选）<input {...inputError("bearer_token_env_var")} value={bearerEnv} disabled={!!pending} onChange={(event) => { invalidate(); setBearerEnv(event.target.value); }} /></label>}</>}
          <details><summary>明确选择 env / header 落点</summary><label>{transport === "stdio" ? "Env 名字" : "Header 名字"}<input value={secretName} disabled={!!pending} onChange={(event) => { invalidate(); setSecretName(event.target.value); }} /></label><label>值（仅本次输入，不放浏览器持久存储）<input type="password" autoComplete="off" value={secretValue} disabled={!!pending} onChange={(event) => { invalidate(); setSecretValue(event.target.value); }} /></label><label><input type="checkbox" checked={privateFile} disabled={!!pending} onChange={(event) => { invalidate(); setPrivateFile(event.target.checked); }} />允许将该值写入上方明确选择的0600配置文件；原值和本次值仅在私有恢复记录保存。</label></details>
        </>}
        <button disabled={!!pending}>{pending === "preview" ? "正在预览…" : "生成配置预览"}</button>
      </form>
    </section>
    {preview && <section className="panel"><h2>配置预览</h2><p>到期 {new Date(preview.expires_at).toLocaleString()}。秘密交付 {preview.required_secret.mode === "private_file" ? "所选私有文件" : "环境引用或无秘密"}。</p>{preview.files.map((file) => <article key={file.path}><p><code>{file.path}</code> · {file.exists ? "更新选定内容" : "创建文件"}</p><p>原 hash <code>{file.base_hash}</code></p>{file.redacted_diff.map((diff, index) => <p key={index}>新 hash <code>{diff.after.sha256}</code> · {diff.after.size} 字节{diff.after.source_sha256 && <> · 来源 hash <code>{diff.after.source_sha256}</code></>}</p>)}</article>)}{preview.warnings.map((warning, index) => <p key={index}>{warning}</p>)}<button disabled={!!pending || Date.parse(preview.expires_at) <= Date.now()} onClick={(event) => void run("apply", apply, event.currentTarget)}>{pending === "apply" ? "正在应用…" : "应用明确列出的文件"}</button></section>}
    <section className="panel"><h2>扩展变更记录</h2>{!loading && !error && !changes.length && <p>还没有 MCP/Skills 配置变更。</p>}{changes.map((change) => <div className="tool-row" key={change.id}><div><strong>{change.kind} · {states[change.state] || change.state}</strong>{change.files.map((file) => <code key={file.path}>{file.path} · {file.status}</code>)}</div><button disabled={!!pending || change.state === "restored"} onClick={(event) => { selection.current++; void run(change.id, (revision) => restorePreview(change, revision), event.currentTarget); }}>预览恢复</button></div>)}</section>
    {restore && <section className="panel" data-dirty={Object.keys(resolutions).length>0}><h2>扩展恢复预览</h2><p>恢复本次命名的 MCP 定义或本次列出的技能文件。对于用户改过的内容，保留当前值是默认可选处理。</p>{restore.files.map((file) => <article key={file.path}><p><code>{file.path}</code> · {file.field}</p><p>当前 hash <code>{file.current_hash}</code> · {file.size} 字节</p>{file.action === "conflict" ? <label>冲突处理<select value={resolutions[file.path] || ""} disabled={!!pending} onChange={(event) => setResolutions((old) => ({ ...old, [file.path]: event.target.value }))}><option value="">请选择</option><option value="keep_current">保留用户当前定义 / 文件</option><option value="restore_before">撤回本次内容，恢复原定义 / 文件</option></select></label> : <p>{file.action === "already_before" ? "已经是原值，无需修改" : "恢复原值或删除未改的本次新建内容"}</p>}</article>)}<button disabled={!!pending || !restore.files.length || restore.files.some((file) => file.action === "conflict" && !resolutions[file.path])} onClick={(event) => void run("restore", restoreApply, event.currentTarget)}>{pending === "restore" ? "正在恢复…" : "按所选处理恢复"}</button><button className="text" disabled={!!pending} onClick={() => setRestore(null)}>关闭预览</button></section>}
  </>;
}
