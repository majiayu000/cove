import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type Budget = {
  id: string; name: string; scope: { kind: string; id?: string }; currency: string;
  amount_limit: string; mode: string; enabled: boolean; version: number;
  period: { kind: string; timezone: string; start_at?: string; end_at?: string };
  period_start: string; period_end: string; settled: string; reserved: string;
  pending: string; available: string; eligibility: string; blocked_reason?: string;
};
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
const amountPattern = "[0-9]+(\\.[0-9]{1,12})?";

export function BudgetConsole({ api, keys }: { api: API; keys: { id: string; name: string }[] }) {
  const [budgets, setBudgets] = useState<Budget[]>([]), [routes, setRoutes] = useState<any[]>([]);
  const [scope, setScope] = useState("instance"), [period, setPeriod] = useState("calendar_month");
  const [error, setError] = useState(""), [notice, setNotice] = useState(""), [pending, setPending] = useState<string[]>([]);
  const [edits,setEdits]=useState<Record<string,{version:number;value:string;current?:Budget;readError?:string}>>({});
  const running=useRef(new Set<string>()), mounted=useRef(true), revision=useRef(0);
  async function load() {
    const sequence=++revision.current;
    const [b, r] = await Promise.all([api("budgets"), api("routes")]);
    if(mounted.current&&sequence===revision.current){setBudgets(old=>b.items.map((item:Budget)=>{const previous=old.find(v=>v.id===item.id);return previous&&previous.version>item.version?previous:item}));setRoutes(r.items)}
  }
  useEffect(() => { mounted.current=true;load().catch((e) => {if(mounted.current)setError(e.message)});return()=>{mounted.current=false;revision.current++}; }, []);
  async function run(id:string,action: () => Promise<void>) {
    if(running.current.has(id))return;
    running.current.add(id);setPending([...running.current]);setError("");setNotice("");
    try { await action();await load(); } catch (e) {if(mounted.current)setError((e as Error).message)} finally {running.current.delete(id);if(mounted.current)setPending([...running.current])}
  }
  return <>
    {error && <div className="error" role="alert">{error}</div>}{notice && <div className="notice" role="status">{notice}</div>}
    <section className="panel"><h2>本地预算</h2>
      <p>实例、路由和 Key 预算同时生效。软限制使用明确估算，未知费用保留预留；这里的可用额是本地预算余额。</p>
      <p>严格模式表示“本地已知计费规则下的准入上限”。当前只支持官方 OpenAI Responses 的无状态文本/函数工具：请求须指定 default 服务层和正数输出上限，并配置完整 token 价格。先获取提供方输入计数再原子预留；订阅、媒体、服务端工具及其他未知费用操作会明确拒绝。</p>
      <form onSubmit={(e) => { e.preventDefault(); const f = new FormData(e.currentTarget); run("create",async () => {
        await api("budgets", "POST", { name: f.get("name"), scope: { kind: scope, ...(scope !== "instance" ? { id: f.get("scope_id") } : {}) },
          currency: f.get("currency"), amount_limit: f.get("amount"), mode: f.get("mode"),
          period: { kind: period, timezone: f.get("timezone"), ...(period === "fixed" ? { start_at: f.get("start"), end_at: f.get("end") } : {}) } });
        setNotice("预算已创建，符合该作用域的后续请求会执行预算检查。");
      }); }}>
        <label>预算名称<input name="name" required maxLength={100}/></label>
        <label>作用域<select value={scope} onChange={(e) => setScope(e.target.value)}><option value="instance">整个实例</option><option value="key">指定 API Key</option><option value="route">指定路由</option></select></label>
        {scope !== "instance" && <label>{scope === "key" ? "API Key" : "路由"}<select name="scope_id" required><option value="">选择作用域</option>{(scope === "key" ? keys : routes).map((v) => <option key={v.id} value={v.id}>{v.name}</option>)}</select></label>}
        <label>币种<input name="currency" defaultValue="USD" pattern="[A-Z]{3}" required/></label>
        <label>周期金额上限<input name="amount" inputMode="decimal" pattern={amountPattern} required/></label>
        <label>模式<select name="mode"><option value="soft">估算软限制</option><option value="strict">本地计费规则准入上限</option></select></label>
        <label>周期<select value={period} onChange={(e) => setPeriod(e.target.value)}><option value="calendar_day">自然日</option><option value="calendar_month">自然月</option><option value="fixed">固定时间范围</option></select></label>
        <label>周期时区<input name="timezone" defaultValue={timezone} required/><small>创建后冻结，使用 IANA 时区；自然日会遵循夏令时。</small></label>
        {period === "fixed" && <><label>起点（RFC3339）<input name="start" placeholder="2026-10-01T00:00:00Z" required/></label><label>终点（RFC3339）<input name="end" placeholder="2026-11-01T00:00:00Z" required/></label></>}
        <button disabled={pending.includes("create")}>创建预算</button>
      </form>
    </section>
    <section className="panel"><h2>预算占用与待核对</h2>{!budgets.length && <p>还没有预算。你可以创建实例预算，也可以为某个 Key 或路由单独设置预算。</p>}
      {budgets.map((b) => {
        const edit=edits[b.id], current=edit?.current&&edit.current.version>b.version?edit.current:b;
        const conflict=edit&&current.version!==edit.version;
        return <article className="source-card" key={b.id}>
        <div className="section-title"><h3>{b.name}</h3><span className="badge muted">{b.enabled ? "启用" : "停用"} · {b.mode === "strict" ? "严格准入" : "估算软限制"}</span></div>
        <p>{b.scope.kind === "instance" ? "整个实例" : `${b.scope.kind === "key" ? "API Key" : "路由"} ${[...keys, ...routes].find((v) => v.id === b.scope.id)?.name || b.scope.id}`} · {b.period.timezone}</p>
        <p>{new Date(b.period_start).toLocaleString()} 至 {new Date(b.period_end).toLocaleString()}</p>
        <div className="table-scroll"><table><thead><tr><th>币种</th><th>金额上限</th><th>已结算</th><th>未释放预留</th><th>其中待核对</th><th>本地可用额</th></tr></thead><tbody><tr><td>{b.currency}</td><td>{b.amount_limit}</td><td>{b.settled}</td><td>{b.reserved}</td><td>{b.pending}</td><td>{b.available}</td></tr></tbody></table></div>
        {b.blocked_reason && <p role="status">{b.blocked_reason}</p>}
        <form className="inline-form" onSubmit={(e) => { e.preventDefault(); const f = new FormData(e.currentTarget), value=String(f.get("limit")), version=edit?.version??b.version; run(b.id,async () => {
          try { await api(`budgets/${b.id}`, "PATCH", { version, amount_limit: value }); }
          catch(error) {
            if((error as Error&{status?:number}).status===409) {
              try { const latest=await api<Budget>(`budgets/${b.id}`); if(mounted.current){setBudgets(old=>old.map(item=>item.id===b.id&&latest.version>=item.version?latest:item));setEdits(old=>({...old,[b.id]:{version,value,current:latest}}));} }
              catch(readError) { if(mounted.current)setEdits(old=>({...old,[b.id]:{version,value,readError:`无法读取当前预算：${(readError as Error).message}`}})); }
            }
            throw error;
          }
          if(mounted.current){setEdits(old=>{const next={...old};delete next[b.id];return next});setNotice("当前上限已更新；已结算金额和待核对预留保留。");}
        }); }}>
          <label>调整当前上限<input name="limit" value={edit?.value??b.amount_limit} onChange={e=>{const value=e.target.value;setEdits(old=>({...old,[b.id]:{...(old[b.id]??{version:b.version}),value}}))}} disabled={pending.includes(b.id)} pattern={amountPattern} required aria-label={`${b.name} 金额上限`} aria-describedby={conflict?`budget-${b.id}-conflict`:undefined}/></label><button disabled={pending.includes(b.id)}>保存上限</button>
          {conflict&&<div id={`budget-${b.id}-conflict`} role="region" aria-label={`${b.name} 上限版本冲突`} aria-live="polite">
            <p>编辑基于 v{edit.version}；当前 v{current.version}。当前上限 {current.amount_limit}；本地输入 {edit.value}。服务器值尚未被本次输入覆盖。</p>
            <button type="button" disabled={pending.includes(b.id)} onClick={()=>setEdits(old=>({...old,[b.id]:{version:current.version,value:edit.value}}))}>使用当前版本，保留我的输入</button>
            <button type="button" disabled={pending.includes(b.id)} onClick={()=>setEdits(old=>{const next={...old};delete next[b.id];return next})}>放弃本地修改</button>
          </div>}
          {edit?.readError&&<p className="error" role="alert">{edit.readError}；本地输入已保留。</p>}
          <button type="button" disabled={pending.includes(b.id)} onClick={() => run(b.id,async () => { await api(`budgets/${b.id}`, "PATCH", { version: b.version, enabled: !b.enabled }); })}>{b.enabled ? "停用预算" : "启用预算"}</button>
          <button className="text" type="button" disabled={pending.includes(b.id)} onClick={() => run(b.id,async () => { await api(`budgets/${b.id}`, "DELETE"); })}>删除无引用预算</button>
        </form>
      </article>})}
    </section>
  </>;
}

// Place this inside the Key create/edit form; keyBudgetInput reads its fields.
export function KeyBudgetFields({ api, keyId, routeId }: { api: API; keyId?: string; routeId?: string }) {
  const [mode, setMode] = useState("none"), [budgets, setBudgets] = useState<Budget[]>([]), [error, setError] = useState("");
  useEffect(() => { api("budgets").then((v) => setBudgets(v.items)).catch((e) => setError(e.message)); }, []);
  const allowed = budgets.filter((b) => b.enabled && (b.scope.kind === "instance" || b.scope.kind === "key" && b.scope.id === keyId || b.scope.kind === "route" && b.scope.id === routeId));
  return <fieldset><legend>预算（可选）</legend>
    <p>实例和当前路由预算自动生效。独立 Key 预算只能绑定自己的 Key。</p>
    {error && <p role="alert" className="error">{error}</p>}
    <label>Key 预算<select name="key_budget_choice" value={mode} onChange={(e) => setMode(e.target.value)}><option value="none">不增加此层预算</option><option value="new">同时创建 Key 专属预算</option><option value="existing">关联合法的已有预算</option></select></label>
    {mode === "existing" && <label>已有预算<select name="budget_id" required><option value="">选择预算</option>{allowed.map((b) => <option key={b.id} value={b.id}>{b.name} · {b.currency} {b.amount_limit}</option>)}</select></label>}
    {mode === "new" && <><label>预算名称<input name="budget_name" required/></label><label>币种<input name="budget_currency" defaultValue="USD" pattern="[A-Z]{3}" required/></label><label>金额上限<input name="budget_amount" pattern={amountPattern} inputMode="decimal" required/></label><label>预算模式<select name="budget_mode"><option value="soft">估算软限制</option><option value="strict">本地计费规则准入上限</option></select></label><label>周期<select name="budget_period"><option value="calendar_month">自然月</option><option value="calendar_day">自然日</option></select></label><label>周期时区<input name="budget_timezone" defaultValue={timezone} required/></label></>}
  </fieldset>;
}
export function keyBudgetInput(f: FormData) {
  if (f.get("key_budget_choice") === "existing") return { budget_id: f.get("budget_id") };
  if (f.get("key_budget_choice") === "new") return { budget: { name: f.get("budget_name"), currency: f.get("budget_currency"), amount_limit: f.get("budget_amount"), mode: f.get("budget_mode"), period: { kind: f.get("budget_period"), timezone: f.get("budget_timezone") } } };
  return { budget_id: null };
}

type Allocation = { reserved: string; actual?: string; known_partial_cost?: string; state: string; provenance: string; reservation_provenance?: string; input_tokens_bound?:number; output_tokens_bound?:number };
type Accounting = { accounting_version: number; reservations: { budget_id: string; period_start: string; period_end: string; currency: string; reserved: string; settled: string; pending: string; status: string; allocations: Record<string, Allocation> }[] };
export function RequestAccounting({ api, requestId, accounting, onReconciled }: { api: API; requestId: string; accounting: Accounting; onReconciled: () => Promise<void> }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const costs = new Map<string, { currency: string; allocation: Allocation }>();
  for (const reservation of accounting.reservations) for (const [id, allocation] of Object.entries(reservation.allocations)) {
    if (allocation.state === "pending_reconciliation") costs.set(id, { currency: reservation.currency, allocation });
  }
  return <section className="panel"><h3>预算账务</h3><p>账务版本 {accounting.accounting_version}。人工核对保留原始 usage，并记录实际金额、说明和证据引用。</p>
    {error && <div className="error" role="alert">{error}</div>}
    {accounting.reservations.map((r) => <p key={`${r.budget_id}_${r.period_start}`}>{r.currency} · 已结算 {r.settled} · 未释放预留 {r.reserved} · 待核对 {r.pending} · {r.status}</p>)}
    {accounting.reservations.map(r=>Object.entries(r.allocations).map(([id,a])=><p key={`${r.budget_id}_${r.period_start}_${id}`}>尝试 {id} · 准入依据 {a.reservation_provenance??"未记录"} · 当前账务依据 {a.provenance}{a.input_tokens_bound!==undefined&&` · 输入计数 ${a.input_tokens_bound} / 输出上限 ${a.output_tokens_bound}`}</p>))}
    {[...costs.entries()].map(([id, value]) => <form key={id} onSubmit={async (e) => { e.preventDefault(); const f = new FormData(e.currentTarget); setBusy(true); setError(""); try {
      await api(`requests/${requestId}/reconcile`, "POST", { version: accounting.accounting_version, attempt_costs: [{ attempt_id: id, currency: value.currency, amount: f.get("amount"), reason: f.get("reason"), evidence_ref: f.get("evidence") }] }); await onReconciled();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }}>
      <p>尝试 <code>{id}</code> · {value.currency} · 预留 {value.allocation.reserved}{value.allocation.known_partial_cost && ` · 已知部分费用 ${value.allocation.known_partial_cost}`}</p>
      <label>核对后的实际金额<input name="amount" inputMode="decimal" pattern={amountPattern} required/></label><label>核对说明<input name="reason" required maxLength={1000}/></label><label>证据引用<input name="evidence" required maxLength={2000} placeholder="账单编号或证据引用"/></label><button disabled={busy}>保存人工核对</button>
    </form>)}
  </section>;
}
