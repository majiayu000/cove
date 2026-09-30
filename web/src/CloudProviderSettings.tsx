import React from "react";

type CloudSelection = {
  aws_region?: string; aws_profile?: string;
  vertex_project?: string; vertex_location?: string;
  vertex_credentials_mode?: "adc" | "service_account";
};
type Props = { provider: string; value?: CloudSelection; onChange: (value: CloudSelection) => void; disabled?: boolean };

export function CloudProviderSettings({ provider, value = {}, onChange, disabled = false }: Props) {
  if (provider !== "bedrock" && provider !== "vertex") return null;
  const update = (next: Partial<CloudSelection>) => onChange({ ...value, ...next });
  return <fieldset disabled={disabled}>
    <legend>{provider === "bedrock" ? "AWS Bedrock SDK 来源" : "Vertex Gemini SDK 来源"}</legend>
    {provider === "bedrock" ? <>
      <label>AWS region<input value={value.aws_region || ""} placeholder="填写实际使用的 region" autoComplete="off" onChange={e => update({ aws_region: e.target.value })}/></label>
      <label>明确选择的 AWS profile<input value={value.aws_profile || ""} placeholder="填写所选 profile 的名称" autoComplete="off" onChange={e => update({ aws_profile: e.target.value })}/></label>
      <p>请求时由 AWS SDK 加载这个 profile 并完成签名。请手工添加有推理权限的 model ID 或 inference profile ID；目录权限与推理权限分别验证。</p>
    </> : <>
      <label>Google Cloud project<input value={value.vertex_project || ""} placeholder="填写选定 project ID" autoComplete="off" onChange={e => update({ vertex_project: e.target.value })}/></label>
      <label>location<input value={value.vertex_location || ""} placeholder="填写 region 或 global" autoComplete="off" onChange={e => update({ vertex_location: e.target.value })}/></label>
      <label>凭据方式<select value={value.vertex_credentials_mode || ""} onChange={e => update({ vertex_credentials_mode: e.target.value as "adc" | "service_account" })}>
        <option value="">明确选择凭据方式</option><option value="adc">Application Default Credentials</option><option value="service_account">账号私有存储中的 service-account JSON</option>
      </select></label>
      <p>{value.vertex_credentials_mode === "adc" ? "请求时读取本机 Google ADC，SDK 管理临时 token。" : value.vertex_credentials_mode === "service_account" ? "通过账号凭据入口保存服务账号 JSON；SDK 读取私有凭据完成 OAuth。" : "选择后再运行 Gemini 模型验证。"}</p>
    </>}
    <p>此卡提供文本与 function 工具的 JSON、流式调用。额度保持未知，模型与地区权限需明确验证后记录。</p>
  </fieldset>;
}
