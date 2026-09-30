import React, { useEffect, useRef, useState } from "react";

type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
type Target = { id: string; name: string; deleted?: boolean };
type Settings = { version: number; source_ids: string[]; route_ids: string[]; ttl_minutes: number };
type Snapshot = { settings: Settings; entries: number; bytes: number; capacity_bytes: number; privacy: string };

export function ExtendedProtocolSettings({ api }: { api: API }) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null), [sources, setSources] = useState<Target[]>([]), [routes, setRoutes] = useState<Target[]>([]);
  const [draft, setDraft] = useState<Settings | null>(null), [consent, setConsent] = useState(false);
  const [pending, setPending] = useState(false), [error, setError] = useState(""), [notice, setNotice] = useState("");
  const active = useRef(true), sequence = useRef(0);
  async function load() {
    const version = ++sequence.current;
    const [cache, configuredSources, configuredRoutes] = await Promise.all([api<Snapshot>("response-cache"), api<Target[]>("sources"), api<{ items: Target[] }>("routes")]);
    if (!active.current || version !== sequence.current) return;
    setSnapshot(cache); setDraft(cache.settings); setSources(configuredSources.filter(source => !source.deleted)); setRoutes(configuredRoutes.items); setConsent(false);
  }
  useEffect(() => { active.current = true; void load().catch(cause => { if (active.current) setError(cause instanceof Error ? cause.message : "缓存设置读取失败"); }); return () => { active.current = false; sequence.current++; }; }, [api]);
  async function run(work: () => Promise<void>, button: HTMLButtonElement) {
    setPending(true); setError(""); setNotice("");
    try { await work(); } catch (cause) { if (active.current) setError(cause instanceof Error ? cause.message : "缓存操作失败"); }
    finally { if (active.current) { setPending(false); button.focus(); } }
  }
  function toggle(field: "source_ids" | "route_ids", id: string, checked: boolean) {
    if (!draft) return;
    setDraft({ ...draft, [field]: checked ? [...draft[field], id] : draft[field].filter(value => value !== id) }); setNotice("");
  }
  const enabled = !!draft && (draft.source_ids.length > 0 || draft.route_ids.length > 0);
  return <section className="panel">
    <div className="section-title"><h2>本机结果缓存</h2><button disabled={pending} onClick={event => void run(load, event.currentTarget)}>刷新</button></div>
    <p>默认关闭。显式开启后，Cove 会在本机私有目录保存输入 hash 和输出正文。命中表示复用旧结果，不保证模型确定性。</p>
    <p>仅缓存温度为 0 的无状态、无工具纯文本请求。流、文件、图像、音频、签名历史和未知参数会跳过缓存。Provider 的 prompt cache 由上游独立报告。</p>
    {error && <div className="error" role="alert">{error}</div>}
    {notice && <div className="notice" role="status">{notice}</div>}
    {snapshot && <p>已保存 {snapshot.entries} 个结果，{(snapshot.bytes / 1048576).toFixed(2)} MiB / {(snapshot.capacity_bytes / 1048576).toFixed(0)} MiB；每个正文最多 1 MiB。</p>}
    {draft && <fieldset disabled={pending}>
      <legend>选择开启缓存的来源或路由</legend>
      {sources.map(source => <label key={source.id}><input type="checkbox" checked={draft.source_ids.includes(source.id)} onChange={event => toggle("source_ids", source.id, event.target.checked)} />来源：{source.name}</label>)}
      {routes.map(route => <label key={route.id}><input type="checkbox" checked={draft.route_ids.includes(route.id)} onChange={event => toggle("route_ids", route.id, event.target.checked)} />路由：{route.name}</label>)}
      {!sources.length && !routes.length && <p>尚无可配置的来源或路由。</p>}
      <label>有效期（1–60 分钟）<input type="number" min={1} max={60} value={draft.ttl_minutes} onChange={event => setDraft({ ...draft, ttl_minutes: Number(event.target.value) })} /></label>
      {enabled && <label><input type="checkbox" checked={consent} onChange={event => setConsent(event.target.checked)} />我同意在本机保存这些请求的输入 hash 和输出正文。</label>}
      <button disabled={enabled && !consent || draft.ttl_minutes < 1 || draft.ttl_minutes > 60} onClick={event => void run(async () => { await api("response-cache", "PUT", { ...draft, save_output_consent: consent }); await load(); if (active.current) setNotice("缓存设置已保存，旧缓存已清空。"); }, event.currentTarget)}>保存设置</button>
    </fieldset>}
    <button disabled={pending || !snapshot} onClick={event => void run(async () => { await api("response-cache", "DELETE"); await load(); if (active.current) setNotice("缓存正文和索引已清空。"); }, event.currentTarget)}>清空缓存</button>
  </section>;
}
