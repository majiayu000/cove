import React from "react";

type Policy = { allow_cross_model?: boolean; affinity_ttl_seconds?: number; strict_context?: boolean };
export function RoutingStrategyOptions() {
  return <><option value="cost">已知规则下的最低估算费用</option><option value="latency">最近观测的最低首字延迟</option><option value="context_fit">筛选满足上下文估算的成员</option></>;
}
// Insert in the existing route form. The route's strategy stays a single select.
export function RoutingPolicyFields({ policy = {} }: { policy?: Policy }) {
  return <details><summary>会话、模型与上下文策略</summary>
    <label><input name="allow_cross_model" type="checkbox" defaultChecked={policy.allow_cross_model || false}/>允许在这个别名已配置的成员中切换不同上游模型</label>
    <p>未开启时，别名采用最低优先级数值、稳定模型 ID 对应的主模型；主模型不可用不会自动换成另一种模型。</p>
    <label>会话亲和时长（秒）<input name="affinity_ttl_seconds" required type="number" min="0" step="1" defaultValue={policy.affinity_ttl_seconds ?? 1800}/><small>0 关闭。客户端可发送 X-Cove-Session-Id；仅在当前 API Key 内生效，权威资源绑定优先。</small></label>
    <label><input name="strict_context" type="checkbox" defaultChecked={policy.strict_context || false}/>缺少上下文信息或无法估算时排除候选</label>
    <p>上下文筛选使用已配置的窗口和明确标注来源的文本估算，不会自动删消息。图像等输入不能用文本 token 估算保证放得下。</p>
    <p>成本只比较同币种、同计费规则下的明确估算。延迟只使用最近 5 分钟、最多 100 条成功首字观测，至少需要 5 条样本；缺少证据时回到基础优先级与权重。</p>
  </details>;
}
export function routingPolicyInput(f: FormData) {
  return { allow_cross_model: f.get("allow_cross_model") === "on", affinity_ttl_seconds: Number(f.get("affinity_ttl_seconds") ?? 1800), strict_context: f.get("strict_context") === "on" };
}
type Candidate = { model_id: string; estimate_provenance?: string; estimated_input_tokens?: number; output_reserve?: number; estimated_cost?: string; currency?: string; price_version?: string; context_limit?: number; latency_count: number; latency_ewma_ms?: number; latency_from?: string; latency_to?: string; reason: string };
type Snapshot = { algorithm: string; evaluated_at: string; notice?: string; affinity?: string; candidates: Candidate[] };
export function RoutingPolicyEvidence({ snapshot }: { snapshot: Snapshot }) {
  return <section className="panel"><h3>路由选择证据</h3><p>策略 {snapshot.algorithm} · {new Date(snapshot.evaluated_at).toLocaleString()}</p>
    {snapshot.notice && <p role="status">{snapshot.notice}</p>}
    {snapshot.affinity && <p>{snapshot.affinity === "key_scoped_session_hint" ? "采用当前 Key 的会话亲和" : "权威资源绑定优先于会话亲和"}</p>}
    {snapshot.candidates.map((c) => <article className="source-card" key={c.model_id}>
      <code>{c.model_id}</code><p>{c.reason}</p>
      <p>输入估算 {c.estimated_input_tokens ?? "未知"} · 输出预留 {c.output_reserve ?? "未知"} · 配置的上下文 {c.context_limit ?? "未知"}</p>
      {c.estimate_provenance && <small>{c.estimate_provenance}</small>}
      <p>费用估算 {c.estimated_cost == null ? "未知" : `${c.currency} ${c.estimated_cost}`}{c.price_version && ` · 价格快照 ${c.price_version}`}</p>
      <p>成功首字样本 {c.latency_count} · 首字 EWMA {c.latency_ewma_ms == null ? "未知" : `${c.latency_ewma_ms.toFixed(1)} ms`}</p>
      {c.latency_from && c.latency_to && <small>观测范围 {new Date(c.latency_from).toLocaleString()} 至 {new Date(c.latency_to).toLocaleString()}</small>}
    </article>)}
  </section>;
}
