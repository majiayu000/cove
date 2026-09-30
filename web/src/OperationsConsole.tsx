import { FullBackupControls, EncryptedRestoreControls } from "./FullBackupControls";
import React, { useEffect, useRef, useState } from "react";

type APIOptions = { responseType?: "blob"; contentType?: string; headers?:Record<string,string> };
type API = <T = any>(path: string, method?: string, body?: unknown, options?: APIOptions) => Promise<T>;
type Props = { api: API; status?: Record<string, any> };
type Operation = { id: string; kind: string; state: string; version: number; error?: string; result?: { artifact_id?: string; expires_at?: string; summary?: any; filename?:string } };

function download(data: Blob | unknown, name: string, type = "application/json") {
  const blob = data instanceof Blob ? data : new Blob([JSON.stringify(data, null, 2)], { type });
  const href = URL.createObjectURL(blob), link = document.createElement("a");
  link.href = href; link.download = name; link.click();
  setTimeout(() => URL.revokeObjectURL(href), 1000);
}

export function OperationsConsole({ api, status }: Props) {
  const [error, setError] = useState(""), [notice, setNotice] = useState(""), [pending, setPending] = useState<string[]>([]);
  const [operation, setOperation] = useState<Operation | null>(null), [alerts, setAlerts] = useState<any[]>([]), [doctor, setDoctor] = useState<any>(null);
  const [restoreFile, setRestoreFile] = useState<File | null>(null), [target, setTarget] = useState(""), [restorePreview, setRestorePreview] = useState<any>(null), [restoreAcknowledged, setRestoreAcknowledged] = useState(false);
  const [configFile, setConfigFile] = useState<File | null>(null), [configPreview, setConfigPreview] = useState<any>(null), [transferMode,setTransferMode]=useState("create"),[resolutions,setResolutions]=useState<Record<string,any>>({});
  const [preparedRestore,setPreparedRestore]=useState<any>(null);
  const mounted = useRef(true), transferRevision = useRef(0), alertRevision=useRef(0), operationSelection=useRef(0), running=useRef(new Set<string>());
  const [from, setFrom] = useState(""), [to, setTo] = useState("");
  async function run(name: string, action: () => Promise<void>) {
    if(running.current.has(name))return;
    running.current.add(name);setPending([...running.current]);setError("");setNotice("");
    try { await action(); } catch (e) {if(mounted.current)setError((e as Error).message)} finally {running.current.delete(name);if(mounted.current)setPending([...running.current])}
  }
  async function loadAlerts() { const revision=++alertRevision.current,value=await api("alerts");if(mounted.current&&revision===alertRevision.current)setAlerts(value.items); }
  function remember(value: Operation, selection:number) { if(!mounted.current||selection!==operationSelection.current)return;setOperation(value);try{sessionStorage.setItem("cove:last-local-operation",value.id)}catch{} }
  useEffect(() => {
    let live = true;
    mounted.current = true;
    loadAlerts().catch((e) => { if (live) setError(e.message); });
    let saved:string|null=null;try{saved=sessionStorage.getItem("cove:last-local-operation")}catch{}
    const selection=operationSelection.current;
    if (saved) api<Operation>(`operations/${saved}`).then((v) => { if (live) remember(v,selection); }).catch((e) => { if (live&&selection===operationSelection.current) setError(e.message); });
    return () => { live = false; mounted.current = false; transferRevision.current++;alertRevision.current++;operationSelection.current++; };
  }, [api]);
  const configBusy=pending.includes("config-import"), operationBusy=!!operation&&pending.includes(`operation:${operation.id}`);
  return <>
    {error && <p className="error" role="alert">{error}</p>}{notice && <p className="notice" role="status">{notice}</p>}
    <section className="panel"><h2>本机运行与提醒</h2>
      <p>活动请求 {status?.active_requests ?? "待读取"} · 存储 {status?.storage_healthy === false ? "故障" : status?.storage_healthy === true ? "正常" : "待读取"}。提醒保存在本机，可在上方单独开启外部通知。</p>
      <button disabled={pending.includes("alerts")} onClick={() => run("alerts", loadAlerts)}>刷新提醒</button>
      {!alerts.length && <p>当前没有已记录的提醒。</p>}
      {alerts.map((alert) => <div className="tool-row" key={alert.id}><div><strong>{alert.summary}</strong><span>{alert.state === "resolved" ? "已恢复" : alert.state === "dismissed" ? "已读，故障状态仍保留" : "待处理"} · {alert.count} 次观测</span></div>
        {alert.state === "active" && <button disabled={pending.includes(alert.id)} onClick={() => run(alert.id, async () => { await api(`alerts/${alert.id}/dismiss`, "POST", { version: alert.version }); await loadAlerts(); })}>标为已读</button>}
      </div>)}
      <button disabled={pending.includes("doctor")} onClick={() => run("doctor", async () => { const value=await api("doctor");if(mounted.current)setDoctor(value); })}>运行本机诊断</button>
      {doctor && <div role="status">{doctor.checks.map((check: any) => <p key={check.kind}>{check.kind} · {check.healthy === false ? "需要处理" : check.healthy === true ? "可用" : check.status} · {check.message}</p>)}<button onClick={() => download(doctor, "cove-doctor.json")}>下载脱敏诊断</button></div>}
    </section>
    <section className="panel"><h2>元数据备份</h2><p>保留来源、模型、路由和报表；移除账号凭据、可用 Key、资源续接、客户端配置快照和请求正文。恢复后需要重新登录并创建新 Key。</p>
      <FullBackupControls create={async input=>{const selection=++operationSelection.current,value=await api("backups","POST",input);remember(value,selection)}}/>
      <p>完整备份采用 age 口令加密；元数据备份不包含凭据。下载文件仅保留 24 小时，并要求当前管理会话。</p>
      {operation && <div role="status"><p>操作 {operation.id} · {operation.kind} · {operation.state === "running" ? "运行中，刷新状态查看结果" : operation.state === "succeeded" ? "已完成" : operation.state === "cancelled" ? "已取消" : "未完成"}</p>
        {operation.error && <p className="error">{operation.error}</p>}
        <button disabled={operationBusy} onClick={() => run(`operation:${operation.id}`, async () => { const selection=operationSelection.current;remember(await api(`operations/${operation.id}`),selection); })}>刷新操作状态</button>
        {operation.state === "running" && <button disabled={operationBusy} onClick={() => run(`operation:${operation.id}`, async () => { await api(`operations/${operation.id}/cancel`, "POST", { version: operation.version }); setNotice("已请求取消；刷新状态确认终态。"); })}>请求取消</button>}
        {operation.state === "succeeded" && operation.result?.artifact_id && <><button disabled={operationBusy} onClick={() => run(`operation:${operation.id}`, async () => { const file = await api<Blob>(`artifacts/${operation.result!.artifact_id}`, "GET", undefined, { responseType: "blob" }); download(file, operation.result?.filename || (operation.kind === "backup" ? "cove-metadata.tar" : `cove-requests.${operation.kind === "export_csv" ? "csv" : "json"}`)); })}>下载文件</button><button disabled={operationBusy} onClick={() => run(`operation:${operation.id}`, async () => { const selection=operationSelection.current;await api(`artifacts/${operation.result!.artifact_id}`, "DELETE"); if(mounted.current&&selection===operationSelection.current){operationSelection.current++;setOperation(null);try{sessionStorage.removeItem("cove:last-local-operation")}catch{}} setNotice("下载文件已删除。"); })}>提前删除文件</button></>}
      </div>}
    </section>
    <section className="panel"><h2>恢复到全新目录</h2><p>先上传备份并核对数量、版本与缺失凭据。此操作准备一个新目录，当前服务继续使用原目录；切换启动需由你另行执行。</p>
      <EncryptedRestoreControls disabled={pending.includes("restore-apply")} preview={async(file,target,passphrase)=>{const value=await api(`restore-preview?target_dir=${encodeURIComponent(target)}`,"POST",file,{contentType:file.name.endsWith(".age")?"application/age":"application/x-tar",headers:passphrase?{"X-Cove-Backup-Passphrase":passphrase}:{}});if(mounted.current){setRestorePreview(value);setRestoreAcknowledged(false)}}}/>

      {restorePreview && <div><p>目标 {restorePreview.target_dir}</p><p>格式 v{restorePreview.manifest.format_version} · schema v{restorePreview.manifest.schema_version} · 凭据 {restorePreview.manifest.secret_included ? "包含" : "不包含"}</p>
        <p>来源 {restorePreview.counts.sources} · 账号 {restorePreview.counts.accounts} · 模型 {restorePreview.counts.source_models} · 历史 Key {restorePreview.counts.client_keys} · 请求 {restorePreview.counts.requests}</p>
        <label><input type="checkbox" checked={restoreAcknowledged} onChange={(e) => setRestoreAcknowledged(e.target.checked)}/>确认创建此新目录，{restorePreview.manifest.mode==="full"?"完整凭据将被保留":"账号需重新配置、历史 Key 已撤销"}；我会用受控启动器检查并切换。</label>
        <button disabled={pending.includes("restore-apply") || !restoreAcknowledged} onClick={() => run("restore-apply", async () => { const result = await api("restore-apply", "POST", { preview_id: restorePreview.preview_id, target_hash: restorePreview.target_hash, prepare_only: true }); setNotice(result.message);setPreparedRestore(result); setRestorePreview(null); setRestoreAcknowledged(false); })}>准备恢复目录</button>
      </div>}
    </section>
    {preparedRestore && <section className="panel" role="status"><h2>恢复目录已准备</h2><p>目标 <code>{preparedRestore.target_dir}</code>；配置 <code>{preparedRestore.target_dir}/config.json</code>。</p><p>用当前 <code>-config</code> 与 <code>-data-dir</code> 执行 <code>restore init --pointer FILE</code>，将返回 hash 用于 <code>restore prepare --pointer FILE --journal FILE --target-config TARGET/config.json --target-data-dir TARGET --expected-pointer-hash HASH</code>，再执行 <code>restore apply --journal FILE</code>。</p><p>FILE 使用私有绝对路径；启动器会排空旧实例、核验目标就绪后激活。具体命令见平台文档。</p></section>}
    <section className="panel"><h2>配置搬运</h2><p>搬运来源、模型、路由、别名、预算、价格与运行设置。凭据、可用 Key 和请求账务留在原实例。逐项选择创建、跳过或替换；替换保留原凭据与历史账务。</p>
      <button disabled={pending.includes("config-export")} onClick={() => run("config-export", async () => { download(await api("config-transfer/export", "POST", { include_dependencies: true }), "cove-config.json"); })}>导出配置与依赖</button>
      <label>导入模式<select disabled={configBusy} value={transferMode} onChange={e=>{transferRevision.current++;setTransferMode(e.target.value);setConfigPreview(null);setResolutions({})}}><option value="create">创建新对象</option><option value="replace_selected">逐项选择替换</option></select></label>
      <label>配置 JSON<input disabled={configBusy} type="file" accept=".json,application/json" onChange={e=>{transferRevision.current++;setConfigFile(e.target.files?.[0]||null);setConfigPreview(null);setResolutions({})}}/></label>
      <button disabled={configBusy||!configFile} onClick={()=>run("config-import",async()=>{
        const file=configFile,mode=transferMode,revision=++transferRevision.current;
        if(!file)return;
        setConfigPreview(null);setResolutions({});
        if(file.size>8*1024*1024)throw new Error("配置文件超过8 MiB");
        let value;
        try{value=await api(`config-transfer/import-preview?mode=${mode}`,"POST",file,{contentType:"application/json"})}
        catch(e){if(mounted.current&&revision===transferRevision.current)throw e;return}
        if(!mounted.current||revision!==transferRevision.current)return;
        setConfigPreview({...value,mode});
        setResolutions(Object.fromEntries((value.entities||[]).map((e:any)=>[e.local_id,e.unsupported?.length||e.kind==="runtime_settings"||e.kind==="settings"||mode==="replace_selected"?"skip":e.conflict?"skip":"create_new"])));
      })}>预览导入</button>
      {configPreview&&<div>{configPreview.unsupported?.length>0&&<p className="error">不支持的顶层字段 {configPreview.unsupported.join("、")}；请修改文件后重新预览。</p>}
        {(configPreview.entities||[]).map((entity:any)=>{const value=resolutions[entity.local_id]||"skip",action=typeof value==="string"?value:value.action==="skip"?"link":"replace";return <fieldset key={entity.local_id}><legend>{entity.kind} · {entity.name||entity.local_id}{entity.conflict?" · 存在冲突":""}</legend>
          {entity.dependencies?.length>0&&<p>依赖 {entity.dependencies.join("、")}</p>}{entity.unsupported?.length>0&&<p className="error">未支持 {entity.unsupported.join("、")}；此对象只能跳过。</p>}
          <label>处理方式<select disabled={configBusy} value={action} onChange={e=>{const chosen=e.target.value;setResolutions(old=>({...old,[entity.local_id]:chosen==="replace"||chosen==="link"?{action:chosen==="link"?"skip":"replace",target_id:""}:chosen}))}}><option value="skip">跳过，不关联</option>{!entity.unsupported?.length&&entity.kind!=="settings"&&entity.kind!=="runtime_settings"&&<option value="create_new">创建新对象</option>}{!entity.unsupported?.length&&entity.targets?.length>0&&<option value="link">保留并关联现有对象</option>}{configPreview.mode==="replace_selected"&&!entity.unsupported?.length&&entity.targets?.length>0&&<option value="replace">替换选定对象</option>}</select></label>
          {(action==="replace"||action==="link")&&<label>现有对象<select disabled={configBusy} value={value.target_id||""} required onChange={e=>{const target=entity.targets.find((t:any)=>t.id===e.target.value);setResolutions(old=>({...old,[entity.local_id]:{action:action==="link"?"skip":"replace",target_id:target?.id||"",...(entity.kind==="price"?{target_hash:target?.hash}:{version:target?.version})}}))}}><option value="">选择明确目标</option>{entity.targets.map((target:any)=><option key={target.id} value={target.id}>{target.name||target.id}{target.version?` · v${target.version}`:""}</option>)}</select></label>}
        </fieldset>})}
        {configPreview.missing_credentials?.length>0&&<p>{configPreview.missing_credentials.length} 个创建候选账号需要单独配置凭据；替换和关联保留原账号。</p>}
        <button disabled={configBusy||!configPreview.preview_id||configPreview.can_apply===false||Object.values(resolutions).some(v=>typeof v!=="string"&&!v.target_id)} onClick={()=>run("config-import",async()=>{const result=await api("config-transfer/import-apply","POST",{preview_id:configPreview.preview_id,expected_config_version:configPreview.expected_config_version,selected_resolutions:resolutions});setNotice(result.message);setConfigPreview(null);setResolutions({})})}>应用逐项选择</button>
      </div>}
    </section>
    <section className="panel"><h2>脱敏请求报表</h2><p>导出稳定快照中的公开统计列。各币种独立记录，未知 token 和费用保留为空；不含凭据、身份、路径、请求或响应正文。</p>
      <label>开始时间<input type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)}/></label><label>结束时间<input type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)}/></label>
      {["csv", "json"].map((format) => <button key={format} disabled={pending.includes("export")} onClick={() => run("export", async () => { const selection=++operationSelection.current;remember(await api("exports", "POST", { format, filters: { ...(from ? { from: new Date(from).toISOString() } : {}), ...(to ? { to: new Date(to).toISOString() } : {}) } }),selection); })}>生成 {format.toUpperCase()}</button>)}
    </section>
  </>;
}
