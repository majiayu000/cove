import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type Settings = { version: number; enabled: boolean; url: string; signature: boolean; event_kinds: string[] };
type Delivery = { event_id: string; alert_id: string; state: string; attempt_count: number; created_at: string; next_attempt_at?: string; error?: { code?: string; http_status?: number } };
type Alert = { id: string; kind: string; summary: string; state: string; version: number; count: number; resolved_at?: string; dismissed_at?: string };
const names: Record<string, string> = { auth: "账号认证", quota: "已观测额度", budget: "本地预算", storage: "本地存储", source_health: "来源健康", update: "更新失败", job: "后台任务未决" };
const deliveryNames: Record<string, string> = { pending: "等待发送", sending: "发送中或待恢复确认", delivered: "已送达", delivery_failed: "投递失败", cancelled: "设置改变，已取消" };

export function NotificationSettings({ api }: { api: API }) {
  const [saved, setSaved] = useState<Settings | null>(null), [form, setForm] = useState<Settings | null>(null);
  const [credential, setCredential] = useState(false), [secret, setSecret] = useState(""), [authorization, setAuthorization] = useState("");
  const [clear, setClear] = useState(false), [kinds, setKinds] = useState<string[]>(Object.keys(names));
  const [alerts, setAlerts] = useState<Alert[]>([]), [deliveries, setDeliveries] = useState<Delivery[]>([]);
  const [pending, setPending] = useState<string[]>([]), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const [conflict,setConflict]=useState<Settings|null>(null), [field,setField]=useState("");
  const [actionErrors,setActionErrors]=useState<Record<string,string>>({});
  const active=useRef(true), running=useRef(new Set<string>()), dirty=useRef(false), settingsSequence=useRef(0), recordSequence=useRef(0);
  const [sampleVisible, setSampleVisible] = useState(false), [confirmed, setConfirmed] = useState(false);
  const busy = pending.includes("save") || pending.includes("sample");
  async function loadSettings() {
    const sequence=++settingsSequence.current;
    const v=await api<{settings:Settings;credential_configured:boolean;event_kinds:string[]}>("notifications");
    if(!active.current||sequence!==settingsSequence.current)return;
    setSaved(v.settings);setCredential(v.credential_configured);setKinds(v.event_kinds);setSampleVisible(false);setConfirmed(false);
    if(!dirty.current)setForm(v.settings);
  }
  async function refresh() {
    const sequence=++recordSequence.current;
    const replies=await Promise.allSettled([api<{items:Alert[]}>("alerts"),api<{items:Delivery[]}>("notifications/deliveries")]);
    if(!active.current||sequence!==recordSequence.current)return;
    if(replies[0].status==="fulfilled")setAlerts(replies[0].value.items);
    if(replies[1].status==="fulfilled")setDeliveries(replies[1].value.items);
    const failures=replies.filter((reply):reply is PromiseRejectedResult=>reply.status==="rejected");
    if(failures.length)throw new Error(failures.map(reply=>reply.reason?.message||"提醒记录读取失败").join("；"));
  }
  async function run(action:string,task:()=>Promise<void>,button:HTMLButtonElement) {
    if(running.current.has(action)||["save","sample"].includes(action)&&(running.current.has("save")||running.current.has("sample")))return;
    running.current.add(action);setPending([...running.current]);setActionErrors(old=>({...old,[action]:""}));
    if(action==="save"){setField("");setError("");}setNotice("");
    try{await task();}catch(cause){
      if(!active.current)return;
      const failure=cause as Error&{status?:number;field?:string};setActionErrors(old=>({...old,[action]:failure.message}));
      if(action==="save"){
        setField(failure.field||"");
        if(failure.status===409){settingsSequence.current++;try{const latest=await api<{settings:Settings;credential_configured:boolean}>("notifications");if(active.current){setSaved(latest.settings);setCredential(latest.credential_configured);setConflict(latest.settings);setSampleVisible(false);setConfirmed(false);}}
          catch(readError){if(active.current)setActionErrors(old=>({...old,save:`${failure.message}；当前版本读取失败：${(readError as Error).message}。输入已保留，可再次提交以读取差异。`}));}}
      }
    }finally{running.current.delete(action);if(active.current){setPending([...running.current]);window.requestAnimationFrame(()=>{if(active.current&&button.isConnected&&!button.disabled)button.focus();});}}
  }
  useEffect(()=>{
    active.current=true;
    void loadSettings().catch(e=>{if(active.current)setError(e.message);});
    const load=()=>refresh().catch(e=>{if(active.current)setActionErrors(old=>({...old,refresh:e.message}));});
    void load();const timer=window.setInterval(()=>void load(),5000);
    return()=>{active.current=false;settingsSequence.current++;recordSequence.current++;window.clearInterval(timer);};
  },[api]);
  function change(value:Partial<Settings>){dirty.current=true;setForm(current=>current&&({...current,...value}));setSampleVisible(false);setConfirmed(false);}
  function changeCredential(){dirty.current=true;setSampleVisible(false);setConfirmed(false);}
  const settingsDiff=(value:Settings)=>({version:value.version,enabled:value.enabled,url:value.url,signature:value.signature,event_kinds:value.event_kinds});
  return <section className="panel" id="notifications" data-dirty={dirty.current}>
    <div className="section-title"><h2>提醒与通知</h2><button disabled={pending.includes("settings-refresh")} onClick={e=>void run("settings-refresh",loadSettings,e.currentTarget)}>刷新通知设置</button></div>
    <p>提醒在本机按状态持续观测并记录恢复。额度未知时保留未知；已读不会解除故障。外部 webhook 默认关闭，启用后只发送所选类型的脱敏状态变化。</p>
    {error && <p className="error" role="alert">{error}</p>}{Object.entries(actionErrors).map(([action,message])=>message&&<p key={action} id={`notification-${action}-error`} className="error" role="alert">{message}</p>)}{notice && <p className="notice" role="status">{notice}</p>}
    {conflict&&form&&<div role="region" aria-label="通知版本冲突"><p>本地编辑版本 {form.version}；当前版本 {conflict.version}。凭据输入仅保留在此页面内存。</p><pre>{JSON.stringify({local:settingsDiff(form),current:settingsDiff(conflict)},null,2)}</pre><button disabled={busy} onClick={()=>{setForm({...form,version:conflict.version});setConflict(null);setField("");setActionErrors(old=>({...old,save:""}));}}>使用当前版本，保留通知输入</button><button disabled={busy} onClick={()=>{dirty.current=false;setForm(conflict);setConflict(null);setSecret("");setAuthorization("");setClear(false);setField("");setActionErrors(old=>({...old,save:""}));setSampleVisible(false);setConfirmed(false);}}>放弃通知修改</button></div>}
    <div id="alerts">
      {alerts.length === 0 && <p>暂无已记录的提醒。</p>}
      {alerts.map(alert => <div className="tool-row" key={alert.id}><div><strong>{alert.summary}</strong><span>{alert.state === "resolved" ? "已恢复" : alert.state === "dismissed" ? "已读，故障仍待处理" : "待处理"} · {alert.count} 次观测{alert.resolved_at && ` · ${new Date(alert.resolved_at).toLocaleString()}`}</span></div>
        {alert.state === "active" && <button disabled={pending.includes(`dismiss:${alert.id}`)} onClick={e => run(`dismiss:${alert.id}`, async () => { await api(`alerts/${alert.id}/dismiss`, "POST", { version: alert.version }); await refresh(); },e.currentTarget)}>标为已读</button>}
      </div>)}
    </div>
    {form && <>
      <label><input type="checkbox" checked={form.enabled} disabled={busy} onChange={e => change({ enabled: e.target.checked })}/>启用外部 webhook</label>
      <label>HTTPS webhook URL<input type="url" autoComplete="off" value={form.url} aria-invalid={field==="url"||undefined} aria-describedby={field==="url"?"notification-save-error":undefined} disabled={busy} placeholder="https://notifications.example.test/hook" onChange={e => change({ url: e.target.value })}/></label>
      <p>URL 不接受账号、查询串或内部网络地址。请求直接连接校验后的地址，不跟随重定向。</p>
      <label><input type="checkbox" checked={form.signature} disabled={busy} onChange={e => change({ signature: e.target.checked })}/>使用 HMAC-SHA256 签名</label>
      <label>签名凭据<input type="password" autoComplete="new-password" value={secret} aria-describedby={field==="secret"?"notification-save-error":undefined} disabled={busy || clear} placeholder={credential ? "留空保留已保存凭据" : "输入签名凭据"} onChange={e => {changeCredential();setSecret(e.target.value);}}/></label>
      <label>Authorization header（可选）<input type="password" autoComplete="new-password" value={authorization} disabled={busy || clear} placeholder="留空保留已保存 header" onChange={e => {changeCredential();setAuthorization(e.target.value);}}/></label>
      <label><input type="checkbox" checked={clear} disabled={busy} onChange={e => {changeCredential();setClear(e.target.checked);}}/>清除已保存的通知凭据与 header</label>
      <fieldset disabled={busy} aria-describedby={field==="event_kinds"?"notification-save-error":undefined}><legend>发送这些提醒的发生与恢复</legend>{kinds.map(kind => <label key={kind}><input type="checkbox" checked={form.event_kinds.includes(kind)} onChange={e => change({ event_kinds: e.target.checked ? [...form.event_kinds, kind] : form.event_kinds.filter(k => k !== kind) })}/>{names[kind] || kind}</label>)}</fieldset>
      <button disabled={busy || !form.event_kinds.length} onClick={e => run("save", async () => {
        const result = await api<{ settings: Settings; credential_configured: boolean }>("notifications", "PUT", { ...form, ...(clear ? { secret: "", headers: {} } : { ...(secret ? { secret } : {}), ...(authorization ? { headers: { Authorization: authorization } } : {}) }) });
        if(!active.current)return;settingsSequence.current++;dirty.current=false;setConflict(null);
        setSaved(result.settings); setForm(result.settings); setCredential(result.credential_configured); setSecret(""); setAuthorization(""); setClear(false); setSampleVisible(false); setConfirmed(false); await refresh();
        setNotice(result.settings.enabled ? "通知已启用；今后的所选状态变化会排队投递。旧设置的待发事件已取消。" : "外部通知已关闭，本机提醒继续观测。");
      },e.currentTarget)}>保存通知设置</button>
      <button disabled={busy || !saved?.url} onClick={() => { setSampleVisible(true); setConfirmed(false); setError(""); }}>预览合成测试事件</button>
    </>}
    {sampleVisible && saved && <div role="region" aria-label="合成通知预览">
      <p>本次只向已保存的 URL 发送一个合成事件，即使自动通知关闭也会发送。不会发送现有提醒内容。</p>
      <p>目标 {saved.url}</p>
      <pre>{JSON.stringify({ event_id: "由服务生成的 sample ID", type: "test", entity: { kind: "sample", id: "synthetic", display_name_redacted: "synthetic" }, state: "test", previous_state: "test", summary: "Cove 合成通知测试，不含真实提醒数据", local_detail_path: "/settings#notifications" }, null, 2)}</pre>
      <label><input type="checkbox" checked={confirmed} disabled={busy} onChange={e => setConfirmed(e.target.checked)}/>我确认向此 URL 发送一次合成测试通知</label>
      <button disabled={busy || !confirmed} onClick={e => run("sample", async () => {
        const result = await api<{ event_id: string }>("notifications/preview", "POST", { version: saved.version, confirm_external_send: true }); if(!active.current)return;setNotice(`合成通知已送达 · ${result.event_id}`); setConfirmed(false); await refresh();
      },e.currentTarget)}>发送合成测试通知</button>
    </div>}
    <p>待发队列最多 256 条，每事件最多 3 次尝试，24 小时到期；投递失败只影响通知。</p>
    <button disabled={pending.includes("refresh")} onClick={e => run("refresh", refresh,e.currentTarget)}>刷新提醒与投递记录</button>
    {deliveries.map(item => <p key={item.event_id}>{deliveryNames[item.state] || item.state} · {item.attempt_count}/3 次 · {new Date(item.created_at).toLocaleString()}{item.error?.code && ` · ${item.error.code}`}</p>)}
  </section>;
}
