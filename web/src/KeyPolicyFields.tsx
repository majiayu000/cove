import React from "react";

export function KeyPolicyFields({
  sources,
  routes,
  initial,
}: {
  sources: { id: string; name: string; models?: string[] }[];
  routes: { id: string; name: string }[];
  initial?: any;
}) {
  const [kind, setKind] = React.useState(
    initial?.route_id ? "route" : "source",
  );
  const [target, setTarget] = React.useState(
    initial
      ? `${initial.route_id ? "route" : "source"}:${initial.route_id || initial.source_id}`
      : "",
  );
  const [protocolMode, setProtocolMode] = React.useState(
    initial?.protocol_allowlist == null
      ? "inherit"
      : initial.protocol_allowlist.length
        ? "selected"
        : "deny",
  );
  const [protocolText, setProtocolText] = React.useState(
    (initial?.protocol_allowlist || []).join(", "),
  );
  const protocols = protocolText
    .split(",")
    .map((v: string) => v.trim())
    .filter(Boolean);
  const setProtocols = (values: string[]) => setProtocolText(values.join(", "));
  const [modelMode, setModelMode] = React.useState(
    initial?.model_allowlist == null
      ? "inherit"
      : initial.model_allowlist.length
        ? "selected"
        : "deny",
  );
  const [modelText, setModelText] = React.useState(
    (initial?.model_allowlist || []).join(", "),
  );
  const modelList = modelText
    .split(",")
    .map((v: string) => v.trim())
    .filter(Boolean);
  const setModelList = (values: string[]) => setModelText(values.join(", "));
  const targetSource = sources.find((s) => target === `source:${s.id}`);
  function toggle(
    value: string,
    items: string[],
    setItems: (v: string[]) => void,
    setMode: (v: string) => void,
  ) {
    const next = items.includes(value)
      ? items.filter((v) => v !== value)
      : [...items, value];
    setItems(next);
    setMode(next.length ? "selected" : "deny");
  }
  return (
    <fieldset className="key-policy">
      <legend className="sr-only">目标、权限与本地限制</legend>
      <div className="segmented" aria-label="目标类型">
        {[
          ["route", "路由"],
          ["source", "固定来源"],
        ].map(([value, title]) => (
          <button
            type="button"
            key={value}
            className={kind === value ? "selected" : ""}
            onClick={() => {
              setKind(value);
              setTarget("");
            }}
          >
            {title}
          </button>
        ))}
      </div>
      <label>
        调用目标
        <select
          name="target"
          required
          value={target}
          onChange={(e) => setTarget(e.target.value)}
        >
          <option value="">选择{kind === "route" ? "路由" : "来源"}</option>
          {(kind === "route" ? routes : sources).map((s) => (
            <option value={`${kind}:${s.id}`} key={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </label>
      <div className="permission-label">
        协议权限{" "}
        <span>
          {protocolMode === "inherit"
            ? "继承目标允许范围"
            : protocolMode === "deny"
              ? "禁止全部"
              : "仅允许指定值"}
        </span>
      </div>
      <div className="scope-chips">
        {["responses", "chat_completions", "messages", "gemini"].map(
          (value) => (
            <button
              type="button"
              key={value}
              aria-pressed={
                protocolMode === "selected" && protocols.includes(value)
              }
              className={
                protocolMode === "selected" && protocols.includes(value)
                  ? "selected"
                  : ""
              }
              onClick={() =>
                toggle(value, protocols, setProtocols, setProtocolMode)
              }
            >
              {value === "chat_completions" ? "chat" : value}
            </button>
          ),
        )}
      </div>
      <div className="permission-label">
        模型权限{" "}
        <span>
          {modelMode === "inherit"
            ? "继承目标允许范围"
            : modelMode === "deny"
              ? "禁止全部"
              : "仅允许指定值"}
        </span>
      </div>
      <input
        name="model_allowlist"
        aria-label="模型权限列表"
        value={modelText}
        placeholder="精确模型或公开别名，逗号分隔"
        onChange={(e) => {
          const value = e.target.value
            .split(",")
            .map((v) => v.trim())
            .filter(Boolean);
          setModelText(e.target.value);
          setModelMode(value.length ? "selected" : "deny");
        }}
      />
      {!!targetSource?.models?.length && (
        <div className="scope-chips">
          {targetSource.models.map((value) => (
            <button
              type="button"
              key={value}
              aria-pressed={
                modelMode === "selected" && modelList.includes(value)
              }
              className={
                modelMode === "selected" && modelList.includes(value)
                  ? "selected"
                  : ""
              }
              onClick={() =>
                toggle(value, modelList, setModelList, setModelMode)
              }
            >
              {value}
            </button>
          ))}
        </div>
      )}
      <details className="key-advanced">
        <summary>高级设置 · 过期、限流与操作权限</summary>
        <div className="advanced-fields">
          <label>
            协议权限
            <select
              name="protocol_allowlist_mode"
              value={protocolMode}
              onChange={(e) => setProtocolMode(e.target.value)}
            >
              <option value="inherit">继承目标允许范围</option>
              <option value="selected">仅允许指定值</option>
              <option value="deny">禁止全部</option>
            </select>
          </label>
          <label>
            协议权限列表
            <input
              name="protocol_allowlist"
              value={protocolText}
              onChange={(e) => setProtocolText(e.target.value)}
              placeholder="responses, chat_completions, messages, gemini, realtime_websocket"
            />
          </label>
          <label>
            模型权限
            <select
              name="model_allowlist_mode"
              value={modelMode}
              onChange={(e) => setModelMode(e.target.value)}
            >
              <option value="inherit">继承目标允许范围</option>
              <option value="selected">仅允许指定值</option>
              <option value="deny">禁止全部</option>
            </select>
          </label>
          <label>
            操作权限
            <select
              name="operation_allowlist_mode"
              defaultValue={
                initial?.operation_allowlist == null
                  ? "inherit"
                  : initial.operation_allowlist.length
                    ? "selected"
                    : "deny"
              }
            >
              <option value="inherit">继承目标允许范围</option>
              <option value="selected">仅允许指定值</option>
              <option value="deny">禁止全部</option>
            </select>
          </label>
          <label>
            操作权限列表
            <input
              name="operation_allowlist"
              defaultValue={initial?.operation_allowlist?.join(",") || ""}
              placeholder="generate, compact, warmup, realtime, files.upload, background, batch"
            />
          </label>
          <label>
            到期时间
            <input
              type="datetime-local"
              name="expires_at"
              defaultValue={
                initial?.expires_at
                  ? new Date(
                      new Date(initial.expires_at).getTime() -
                        new Date(initial.expires_at).getTimezoneOffset() *
                          60000,
                    )
                      .toISOString()
                      .slice(0, 16)
                  : ""
              }
            />
          </label>
          {[
            ["rpm", "每分钟请求数"],
            ["tpm", "60秒 token 窗口"],
            ["max_concurrent", "最大并发"],
          ].map(([name, title]) => (
            <label key={name}>
              {title}
              <input
                type="number"
                name={name}
                min="1"
                step="1"
                defaultValue={initial?.limits?.[name] ?? ""}
                placeholder="不设置此层限制"
              />
            </label>
          ))}
          <p>
            TPM 使用本地 token
            预留；符合严格预算资格时采用提供方输入计数和输出上限，其余采用固定
            tokenizer
            估算。完成后替换为已知实际量；未知保留至窗口过期。进程重启会重置速率窗口。
          </p>
        </div>
      </details>
    </fieldset>
  );
}
export function keyPolicyInput(f: FormData) {
  const target = String(f.get("target") || ""),
    separator = target.indexOf(":");
  const scopes: Record<string, string[] | null> = {};
  for (const name of [
    "protocol_allowlist",
    "model_allowlist",
    "operation_allowlist",
  ]) {
    const mode = f.get(name + "_mode");
    scopes[name] =
      mode === "inherit"
        ? null
        : mode === "deny"
          ? []
          : String(f.get(name) || "")
              .split(",")
              .map((v) => v.trim())
              .filter(Boolean);
  }
  const expiry = String(f.get("expires_at") || "");
  const limits: Record<string, number | null> = {};
  for (const name of ["rpm", "tpm", "max_concurrent"]) {
    const value = String(f.get(name) || "");
    limits[name] = value ? Number(value) : null;
  }
  return {
    target: {
      kind: target.slice(0, separator),
      id: target.slice(separator + 1),
    },
    ...scopes,
    expires_at: expiry ? new Date(expiry).toISOString() : null,
    limits,
  };
}
