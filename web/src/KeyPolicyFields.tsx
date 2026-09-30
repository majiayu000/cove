import React from "react";

export function KeyPolicyFields({sources,routes,initial}:{sources:{id:string;name:string}[];routes:{id:string;name:string}[];initial?:any}) {
  const fields=[
    ["protocol_allowlist","协议权限","responses, chat_completions, messages, gemini, realtime_websocket"],
    ["model_allowlist","模型权限","精确模型或公开别名，逗号分隔"],
    ["operation_allowlist","操作权限","generate, compact, warmup, realtime, files.upload, background, batch"],
  ];
  return <fieldset><legend>目标、权限与本地限制</legend>
    <label>调用目标<select name="target" required defaultValue={initial?(initial.route_id?"route:"+initial.route_id:"source:"+initial.source_id):""}>
      <option value="">选择来源或路由</option>{sources.map(s=><option value={"source:"+s.id} key={s.id}>来源 · {s.name}</option>)}{routes.map(r=><option value={"route:"+r.id} key={r.id}>路由 · {r.name}</option>)}
    </select></label>
    {fields.map(([name,title,hint])=><React.Fragment key={name}><label>{title}<select name={name+"_mode"} defaultValue={initial?.[name]===null||initial?.[name]===undefined?"inherit":initial[name].length?"selected":"deny"}><option value="inherit">继承目标允许范围</option><option value="selected">仅允许指定值</option><option value="deny">禁止全部</option></select></label><label>{title}列表<input name={name} defaultValue={initial?.[name]?.join(",")||""} placeholder={hint}/></label></React.Fragment>)}
    <label>到期时间<input type="datetime-local" name="expires_at" defaultValue={initial?.expires_at?new Date(new Date(initial.expires_at).getTime()-new Date(initial.expires_at).getTimezoneOffset()*60000).toISOString().slice(0,16):""}/></label>
    {[['rpm','每分钟请求数'],['tpm','60秒 token 窗口'],['max_concurrent','最大并发']].map(([name,title])=><label key={name}>{title}<input type="number" name={name} min="1" step="1" defaultValue={initial?.limits?.[name]??""} placeholder="不设置此层限制"/></label>)}
    <p>TPM 使用本地 token 预留；符合严格预算资格时采用提供方输入计数和输出上限，其余采用固定 tokenizer 估算。完成后替换为已知实际量；未知保留至窗口过期。进程重启会重置速率窗口。</p>
  </fieldset>;
}
export function keyPolicyInput(f:FormData) {
  const target=String(f.get("target")||""),separator=target.indexOf(":");
  const scopes:Record<string,string[]|null>={};
  for(const name of ["protocol_allowlist","model_allowlist","operation_allowlist"]){const mode=f.get(name+"_mode");scopes[name]=mode==="inherit"?null:mode==="deny"?[]:String(f.get(name)||"").split(",").map(v=>v.trim()).filter(Boolean)}
  const expiry=String(f.get("expires_at")||"");
  const limits:Record<string,number|null>={};for(const name of ["rpm","tpm","max_concurrent"]){const value=String(f.get(name)||"");limits[name]=value?Number(value):null}
  return {target:{kind:target.slice(0,separator),id:target.slice(separator+1)},...scopes,expires_at:expiry?new Date(expiry).toISOString():null,limits};
}
