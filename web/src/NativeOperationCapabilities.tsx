import React, { useState } from "react";
type API = <T = any>(path: string, method?: string, body?: unknown) => Promise<T>;
const operations = [
  ["images.generate", "图像生成"], ["images.edit", "图像编辑"], ["audio.transcribe", "音频转录"], ["audio.translate", "音频翻译"], ["audio.speech", "语音输出"], ["embeddings", "向量"], ["rerank", "重排"], ["compact", "原生上下文压缩"], ["files", "Files CRUD"], ["background", "后台 Responses"], ["batch", "文本 Batch"], ["responses_websocket", "Responses WebSocket"], ["realtime_websocket", "Realtime WebSocket"], ["generate", "原生 Gemini 文本"],
];
export function NativeOperationCapabilities({ api, sources, onChanged }: { api: API; sources: any[]; onChanged?: () => void | Promise<void> }) {
  const [pending, setPending] = useState(""), [error, setError] = useState(""), [notice, setNotice] = useState("");
  return <section className="panel"><h2>原生操作能力</h2><p>每项操作必须由来源明确开启，同时遵守客户端 Key 的模型和操作权限。配置只允许转发；真实服务权限、模型能力和计费仍需分别验证。</p>
    {error && <p className="error" role="alert">{error}</p>}{notice && <p className="notice" role="status">{notice}</p>}
    {!sources.length && <p>先添加一个 API Key 来源。</p>}
    {sources.map((source) => <form className="source-card" key={`${source.id}:${source.version}`} onSubmit={async (event) => {
      event.preventDefault(); const form = new FormData(event.currentTarget); setPending(source.id); setError(""); setNotice("");
      try { await api(`sources/${source.id}`, "PATCH", { version: source.version, native_operations: form.getAll("operations"), rerank_path: form.get("rerank_path") || "" }); setNotice("原生转发配置已保存；未发起模型调用，能力仍未验证。"); await onChanged?.(); } catch (e) { setError((e as Error).message); } finally { setPending(""); }
    }}><h3>{source.name}</h3>
      {source.kind !== "api_key" && source.kind !== "codex_subscription" ? <p>此来源有独立协议合同，不能继承公开 API 的媒体或 Compact 能力。</p> : <><div className="inline-form">{operations.filter(([id])=>source.kind!=="codex_subscription"||id==="compact"||id==="responses_websocket").map(([id, label]) => <label key={id}><input type="checkbox" name="operations" value={id} defaultChecked={(source.native_operations || []).includes(id)}/>{label} · 未验证</label>)}</div>
        <label>重排原生相对路径<input name="rerank_path" defaultValue={source.rerank_path || ""} placeholder="例如 /v2/rerank；与来源 base URL 拼接"/></label><p>媒体暂存总上限 32 MiB；请求 model 应在前 64 KiB 元数据内，并置于大媒体字段前。未知用量保留未知；缺少媒体计费维度时，带预算或 token 限额的请求会在派发前说明限制。Files、后台任务和 Batch 在运维页显示资源状态。WebSocket 还需在模型设置中选定固定适配卡；开启配置不等于真实提供方验收。</p>
        <button disabled={!!pending}>保存原生转发配置</button></>}
    </form>)}
  </section>;
}
