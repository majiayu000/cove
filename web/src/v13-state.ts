// Display observed state without turning configuration or installation into a
// claim about a successful client or provider call.
export function clientDisplay(c: any, en = false) {
  const cfg = c.configuration;
  const labels: Record<string, [string, string]> = {
    configured: ["已写配置", "Applied"], modified: ["已变更", "Changed"],
    restored: ["已恢复", "Restored"], applying: ["应用中", "Applying"],
    restoring: ["恢复中", "Restoring"], failed: ["应用失败", "Failed"],
    partial: ["待核对", "Check"], unavailable: ["不可读", "Unknown"],
    unverified: ["待核对", "Unknown"], not_configured: ["待配置", "Configure"],
  };
  const installed = c.status === "installed";
  const manual = c.status === "manual_setup";
  const label = c.status === "not_installed" ? ["未安装", "Not installed"] :
    manual ? ["接入待核验", "Setup unverified"] :
    c.status === "unsupported_version" ? ["版本未核验", "Unsupported"] :
    !installed ? ["检测失败", "Unknown"] : labels[cfg?.state] || ["状态未知", "Unknown"];
  const model = cfg?.state === "configured" ? cfg.model || "" : "";
  return {
    path: cfg?.path || c.recommended_paths?.user?.[0] || c.recommended_paths?.project?.[0] || "—",
    currentModel: model,
    val: model || (en ? "Choose model" : "选择模型"),
    valC: model ? "var(--ink)" : "var(--accent)",
    stZh: label[0], stEn: label[1],
    dot: c.status === "not_installed" ? "var(--ink3)" : installed && model ? "var(--ok)" : "var(--warn)",
    title: (manual && c.manual_setup) || cfg?.reason || (en ? "Configuration has not been checked" : "配置状态尚未读取"),
  };
}

export function gatewayDisplay(status: any, sources: any[]) {
  const unknown = status.runtime_stale || typeof status.storage_healthy !== "boolean";
  const paused = status.accepting_requests === false;
  const secretsFailed = status.secrets_healthy === false;
  const candidates = sources.filter(s => s.enabled && !s.deleted && s.credential_configured &&
    !["needs_reauth", "logged_out", "rejected", "not_configured"].includes(s.auth_status) && s.models?.length);
  const verified = candidates.filter(s => s.verification?.status === "passed" && s.verification?.tested_at);
  return {
    gatewayZh: unknown ? "本机状态未知" : !status.storage_healthy ? "存储异常，暂停调用" : secretsFailed ? "凭据存储异常，暂停调用" : paused ? "本机暂停接收请求" : "本机服务正常",
    gatewayEn: unknown ? "Local status unknown" : !status.storage_healthy ? "Storage unavailable" : secretsFailed ? "Credential storage unavailable" : paused ? "Local admission paused" : "Local service ready",
    gatewaySubZh: unknown ? "状态读取失败，请刷新核对" : status.sources_stale ? "上游状态读取失败，请刷新核对" : !sources.length ? "上游：尚未添加来源" : !candidates.length ? "上游：暂无已启用且配置完整的来源" : !verified.length ? "上游：尚无来源测试通过记录" : `上游：${verified.length} 个来源有测试通过记录；具体协议与能力另行验证`,
    gatewaySubEn: unknown ? "Refresh to verify local status" : status.sources_stale ? "Upstream status could not be read" : !sources.length ? "Upstream: no sources added" : !candidates.length ? "Upstream: no enabled, configured source" : !verified.length ? "Upstream: no passed source test recorded" : `Upstream: ${verified.length} source(s) have a passed test; protocol and feature support require separate verification`,
    gatewayDot: !unknown && (!status.storage_healthy || secretsFailed) ? "var(--err)" : unknown || paused || status.sources_stale || !verified.length ? "var(--warn)" : "var(--ok)",
  };
}

export function routeMemberDisplay(route: any, member: any, source: any, status: any, en = false) {
  const candidate = route.runtime?.candidates?.find((c: any) => c.model_id === member.model_id);
  const counts = status.runtime_stale ? undefined : status.account_active;
  const active = source?.account_id && counts ? counts[source.account_id] ?? 0 : undefined;
  const slot = active === undefined ? "—" : `${active}/${source.max_concurrent ?? "∞"}`;
  const noRuntime = !route.runtime;
  const verified = !status.sources_stale && source?.verification?.status === "passed";
  const label = !route.enabled ? ["已停用", "Disabled"] : noRuntime ? ["未知", "Unknown"] :
    !candidate ? ["需别名", "No alias"] : !candidate.eligible ? ["已排除", "Excluded"] :
    verified ? ["可候选", "Eligible"] : ["待验证", "Untested"];
  return {
    slot, h: label[en ? 1 : 0],
    dot: !route.enabled ? "var(--ink3)" : candidate?.eligible && verified ? "var(--ok)" : "var(--warn)",
    title: `${candidate?.reason || route.runtime?.error || "运行数据尚未读取"} · ${en ? "Account concurrency" : "账号并发"} ${slot} · ${en ? "Configuration preview only" : "仅配置资格检查"}`,
  };
}

export function keyDisplay(k: any, now = Date.now()) {
  const expired = k.expires_at && new Date(k.expires_at).getTime() <= now;
  const scheduled = k.revoke_at && new Date(k.revoke_at).getTime() <= now;
  const active = !k.revoked && k.enabled !== false && !expired && !scheduled;
  return {active,dim:active ? "1" : "0.5",dot:active ? "var(--ok)" : (expired || scheduled) && !k.revoked && k.enabled !== false ? "var(--warn)" : "var(--ink3)",
    stZh:k.revoked ? "已撤销" : k.enabled === false ? "已停用" : scheduled ? "已失效" : expired ? "已过期" : "有效",
    stEn:k.revoked ? "Revoked" : k.enabled === false ? "Disabled" : scheduled ? "Inactive" : expired ? "Expired" : "Active"};
}

export function mergeRequestPages(current: any[], incoming: any[]) {
  const seen = new Set(current.map(r => r.id));
  return [...current, ...incoming.filter(r => { if (seen.has(r.id)) return false; seen.add(r.id); return true; })];
}
