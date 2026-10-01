import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type Target = { id: string; name: string; deleted?: boolean };
type Settings = { version: number; source_ids: string[]; route_ids: string[]; ttl_minutes: number };
type Snapshot = { settings: Settings; entries: number; bytes: number; capacity_bytes: number; privacy: string };

export function ExtendedProtocolSettings({ api }: { api: API }) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null), [sources, setSources] = useState<Target[]>([]), [routes, setRoutes] = useState<Target[]>([]);
  const [draft, setDraft] = useState<Settings | null>(null), [consent, setConsent] = useState(false), [conflict, setConflict] = useState<Settings | null>(null);
  const [pending, setPending] = useState<string[]>([]), [error, setError] = useState(""), [field, setField] = useState(""), [notice, setNotice] = useState("");
  const active = useRef(true), sequence = useRef(0), dirty = useRef(false), running = useRef(new Set<string>());
  async function load() {
    const version = ++sequence.current;
    const [cache, configuredSources, configuredRoutes] = await Promise.all([api<Snapshot>("response-cache"), api<Target[]>("sources"), api<{ items: Target[] }>("routes")]);
    if (!active.current || version !== sequence.current) return;
    setSnapshot(cache); if (!dirty.current) { setDraft(cache.settings); setConsent(false); }
    setSources(configuredSources.filter(source => !source.deleted)); setRoutes(configuredRoutes.items);
  }
  useEffect(() => { active.current = true; void load().catch(cause => { if (active.current) setError(cause instanceof Error ? cause.message : "缓存设置读取失败"); }); return () => { active.current = false; sequence.current++; }; }, [api]);
  async function run(action: string, work: () => Promise<void>, button: HTMLButtonElement) {
    if (running.current.has(action) || action !== "refresh" && (running.current.has("save") || running.current.has("clear"))) return;
    running.current.add(action); setPending([...running.current]); setError(""); setField(""); setNotice("");
    try { await work(); } catch (cause) {
      if (!active.current) return;
      const failure = cause as Error & { status?: number; field?: string };
      setError(failure.message || "缓存操作失败"); setField(failure.field || "");
      if (action === "save" && failure.status === 409) {
        sequence.current++;
        try { const current = await api<Snapshot>("response-cache"); if (active.current) { setSnapshot(current); setConflict(current.settings); } }
        catch (readError) { if (active.current) setError(`${failure.message}；当前版本读取失败：${(readError as Error).message}。输入已保留，可再次提交以读取差异。`); }
      }
    } finally { running.current.delete(action); if (active.current) { setPending([...running.current]); window.requestAnimationFrame(()=>{if(active.current&&button.isConnected&&!button.disabled)button.focus();}); } }
  }
  function change(value: Partial<Settings>) { dirty.current = true; setDraft(current => current && { ...current, ...value }); setNotice(""); }
  function toggle(name: "source_ids" | "route_ids", id: string, checked: boolean) {
    if (draft) change({ [name]: checked ? [...draft[name], id] : draft[name].filter(value => value !== id) });
  }
  const writing = pending.includes("save") || pending.includes("clear");
  const enabled = !!draft && (draft.source_ids.length > 0 || draft.route_ids.length > 0);
  return <section className="panel">
    <div className="section-title"><h2>本机结果缓存</h2><button disabled={pending.includes("refresh")} onClick={event => void run("refresh", load, event.currentTarget)}>刷新</button></div>
    <p>默认关闭。显式开启后，Cove 会在本机私有目录保存输入 hash 和输出正文。命中表示复用旧结果，不保证模型确定性。</p>
    <p>仅缓存温度为 0 的无状态、无工具纯文本请求。流、文件、图像、音频、签名历史和未知参数会跳过缓存。Provider 的 prompt cache 由上游独立报告。</p>
    {error && <div className="error" role="alert" id="cache-error">{error}</div>}
    {notice && <div className="notice" role="status">{notice}</div>}
    {snapshot && <p>已保存 {snapshot.entries} 个结果，{(snapshot.bytes / 1048576).toFixed(2)} MiB / {(snapshot.capacity_bytes / 1048576).toFixed(0)} MiB；每个正文最多 1 MiB。</p>}
    {conflict && draft && <div role="region" aria-label="缓存版本冲突"><p>编辑版本 {draft.version}；当前版本 {conflict.version}。本地有效期 {draft.ttl_minutes} 分钟；当前有效期 {conflict.ttl_minutes} 分钟。</p><pre>{JSON.stringify({local:{source_ids:draft.source_ids,route_ids:draft.route_ids},current:{source_ids:conflict.source_ids,route_ids:conflict.route_ids}},null,2)}</pre><button disabled={writing} onClick={()=>{setDraft({...draft,version:conflict.version});setConflict(null);setError("");setField("");}}>使用当前版本，保留缓存输入</button><button disabled={writing} onClick={()=>{dirty.current=false;setDraft(conflict);setConsent(false);setConflict(null);setError("");setField("");}}>放弃缓存修改</button></div>}
    {draft && <fieldset disabled={writing}>
      <legend>选择开启缓存的来源或路由</legend>
      {sources.map(source => <label key={source.id}><input type="checkbox" checked={draft.source_ids.includes(source.id)} aria-describedby={field==="source_ids"?"cache-error":undefined} onChange={event => toggle("source_ids", source.id, event.target.checked)} />来源：{source.name}</label>)}
      {routes.map(route => <label key={route.id}><input type="checkbox" checked={draft.route_ids.includes(route.id)} aria-describedby={field==="route_ids"?"cache-error":undefined} onChange={event => toggle("route_ids", route.id, event.target.checked)} />路由：{route.name}</label>)}
      {!sources.length && !routes.length && <p>尚无可配置的来源或路由。</p>}
      <label>有效期（1–60 分钟）<input type="number" min={1} max={60} value={draft.ttl_minutes} aria-invalid={field==="ttl_minutes"||undefined} aria-describedby={field==="ttl_minutes"?"cache-error":undefined} onChange={event => change({ ttl_minutes: Number(event.target.value) })} /></label>
      {enabled && <label><input type="checkbox" checked={consent} aria-describedby={field==="save_output_consent"?"cache-error":undefined} onChange={event => {dirty.current=true;setConsent(event.target.checked);}} />我同意在本机保存这些请求的输入 hash 和输出正文。</label>}
      <button disabled={enabled && !consent || draft.ttl_minutes < 1 || draft.ttl_minutes > 60} onClick={event => void run("save", async () => {
        const saved=await api<Settings>("response-cache", "PUT", { ...draft, save_output_consent: consent });
        if (!active.current) return; sequence.current++; dirty.current=false;setDraft(saved);setConflict(null);setConsent(false);setNotice("缓存设置已保存，旧缓存已清空。");await load();
      }, event.currentTarget)}>保存设置</button>
    </fieldset>}
    <button disabled={writing || !snapshot} onClick={event => void run("clear", async () => { await api("response-cache", "DELETE"); await load(); if (active.current) setNotice("缓存正文和索引已清空。"); }, event.currentTarget)}>清空缓存</button>
  </section>;
}
