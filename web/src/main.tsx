import React, { useEffect, useState, useRef } from "react";
import { createRoot } from "react-dom/client";
import "./style.css";
import "./v13.css";
import { V13Console } from "./V13Console";
import { ConsoleModules } from "./ConsoleModules";
import { ExtendedProtocolSettings } from "./ExtendedProtocolSettings";
import { NotificationSettings } from "./NotificationSettings";
import { ResourceConsole } from "./ResourceConsole";
import { OperationsConsole } from "./OperationsConsole";
import { mergeRequestPages } from "./v13-state";
import { ClientConsole } from "./ClientConsole";
import { AccountConsole } from "./AccountConsole";
import { CloudProviderSettings } from "./CloudProviderSettings";
import { NativeOperationCapabilities } from "./NativeOperationCapabilities";
import { ConfigExtensionsConsole } from "./ConfigExtensionsConsole";
import { BudgetConsole, KeyBudgetFields, keyBudgetInput, RequestAccounting } from "./BudgetConsole";
import { KeyPolicyFields, keyPolicyInput } from "./KeyPolicyFields";

type Source = {
  id: string;
  name: string;
  kind: string;
  native_protocol: string;
  provider:string; account_id:string; proxy_url:string|null;
  cloud_config?:{aws_region?:string;aws_profile?:string;vertex_project?:string;vertex_location?:string;vertex_credentials_mode?:"adc"|"service_account"};
  allow_parameter_adjustment: boolean;
  base_url: string;
  enabled: boolean;
  version: number;
  binding_generation: number;
  models: string[];
  credential_configured: boolean;
  auth_status: string;
  verification: {
    status: string;
    tested_at: string | null;
    capabilities: string[];
    model?: string;
    request_id?: string;
  };
  quota: {status:string;call_health?:string;observed_at?:string;expires_at?:string;last_error?:string;windows?:{dimension:string;limit_name?:string;used_percent?:number|null;reset_at?:string;status:string}[]};
  price: {
    currency: string;
    input_per_million: string;
    cached_per_million: string;
    output_per_million: string;
    as_of: string;
  } | null;
};
type Key = {
  id: string;
  name: string;
  fingerprint: string;
  source_id: string;
  revoked: boolean;
  effective_scope?:{active:boolean;protocols:string[];models:string[];operations:string[]};
  budget_summary?:any[];
  route_id?: string;
  version: number;
  expires_at: string | null;
  protocol_allowlist: string[] | null;
  model_allowlist: string[] | null;
  last_seen_at: string | null;
  budget_id?: string;
  enabled?: boolean;
  limits?: {rpm:number|null;tpm:number|null;max_concurrent:number|null};
  operation_allowlist?: string[]|null;
};
type RequestRow = {
  id: string;
  version: number;
  protocol?: string;
  origin: string;
  source_name: string;
  client_name: string;
  requested_model: string;
  status: string;
  started_at: string;
  duration_ms: number;
  usage_completeness: string;
  usage: { input_tokens: number | null; output_tokens: number | null };
};
type Login = {
  id?: string;
  status: string;
  authorization_url?: string;
  message?: string;
  previous_identity?: string;
  new_identity?: string;
};
const labels: Record<string, string> = {
  responses: "Responses",
  chat_completions: "Chat Completions",
  messages: "Messages",
  untested: "尚未验证",
  passed: "测试通过",
  failed: "失败",
  logged_out: "未登录",
  logged_in: "已登录",
  needs_reauth: "需要重新登录",
  refreshing: "刷新中 / 中断后需重登",
  configured: "凭据已设置",
  not_configured: "未配置凭据",
  rejected: "凭据被拒绝",
  succeeded: "成功",
  streaming: "流式传输中",
  dispatching: "请求中",
  cancelled: "已取消",
  interrupted: "服务中断",
  unverified: "结果未确认",
  completed: "完成",
  not_started: "尚未开始",
  unknown: "未知",
  partial: "部分",
  complete: "完整",
};
const label = (s: string) => labels[s] || s;
const sessionKey = "cove.management";
async function api<T = any>(
  path: string,
  method = "GET",
  body?: unknown,
  options?: {responseType?: "blob"; contentType?: string; headers?:Record<string,string>},
): Promise<T> {
  let session: string | null;
  try {
    session = sessionStorage.getItem(sessionKey);
  } catch {
    throw new Error("浏览器会话存储不可用，请允许此站点使用会话存储后重试。");
  }
  let r: Response;
  try {
    r = await fetch("/admin/" + path, {
      method,
      credentials: "omit",
      redirect: "error",
      headers: {
        ...(body !== undefined ? { "Content-Type": options?.contentType || "application/json" } : {}),
        ...(method!=="GET" && path!=="session" ? {"X-Cove-Action-Id":crypto.randomUUID()} : {}),
        ...(options?.headers||{}),
        ...(session ? { Authorization: "Bearer " + session } : {}),
      },
      body: body === undefined ? undefined : body instanceof Blob ? body : JSON.stringify(body),
    });
  } catch {
    throw new Error("无法连接本机 Cove 服务。请确认服务正在运行，再刷新页面；这不代表账号授权已失效。");
  }
  if(r.ok && options?.responseType==="blob") return await r.blob() as T;
  const v = await r.json();
  if (!r.ok) {
    if (r.status === 401) window.dispatchEvent(new Event("gatt-signed-out"));
    throw Object.assign(new Error(v.error?.message || `请求失败 ${r.status}`), { status: r.status, field: v.error?.field, requestId: v.request_id, change: v.change });
  }
  return v;
}
function saveSession(value: string) {
  try {
    sessionStorage.setItem(sessionKey, value);
  } catch {
    throw new Error("浏览器无法保存本机会话，请允许此站点使用会话存储后重新连接。");
  }
}
const when = (v: string | null) => (v ? new Date(v).toLocaleString() : "—");
const localInput = (v: string | null) => {
  if (!v) return "";
  const date = new Date(v);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
const format = (v: number | null | undefined) =>
  v == null ? "未知" : v.toLocaleString();
function App() {
  const refreshSequence=useRef(0);
  const refreshInFlight=useRef(0);
  const runningActions=useRef(new Set<string>());
  const [pendingActions,setPendingActions]=useState<string[]>([]);
  const [routes,setRoutes]=useState<any[]>([]);
  const [keyEdits,setKeyEdits]=useState<Record<string,{version:number;formVersion:number;base:Key;readError?:string}>>({});
  const [aliases,setAliases]=useState<any[]>([]);
  const [logged, setLogged] = useState(false),
    [ready, setReady] = useState(false),
    [page, setPage] = useState(() => {try{return localStorage.getItem("cove.ui.page") || "概览"}catch{return "概览"}}),
    [error, setError] = useState(""),
    [errorField, setErrorField] = useState(""),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  const [sources, setSources] = useState<Source[]>([]),
    [keys, setKeys] = useState<Key[]>([]),
    [requests, setRequests] = useState<RequestRow[]>([]),
    [status, setStatus] = useState<any>({}),
    [usage, setUsage] = useState<any>({}),
    [settings, setSettings] = useState<any>({}),
    [detail, setDetail] = useState<any>(null),
    [quotaHistory,setQuotaHistory]=useState<Record<string,any[]>>({}),
    [login, setLogin] = useState<Record<string, Login>>({}),
    [testModels, setTestModels] = useState<Record<string, string>>({}),
    [freshKey, setFreshKey] = useState(""),
    [guideKey, setGuideKey] = useState<Key | null>(null),
    [guideModel, setGuideModel] = useState(""),
    [guideProtocol, setGuideProtocol] = useState("responses"),
    [filter, setFilter] = useState(""),
    [requestQuery, setRequestQuery] = useState(""),
    [cursor, setCursor] = useState<string | null>(null);
  const detailRevision=useRef(0);
  function closeDetail() {
    detailRevision.current++;
    setDetail(null);
    const url = new URL(window.location.href);
    url.searchParams.delete("request_id");
    window.history.replaceState(null, "", url);
  }
  useEffect(() => {
    if (!detail?.request?.id) return;
    const url = new URL(window.location.href);
    url.searchParams.set("request_id", detail.request.id);
    window.history.replaceState(null, "", url);
  }, [detail]);
  const [settingsEdits,setSettingsEdits]=useState<Partial<Record<"retention"|"limits",{version:number;formVersion:number;base:any;readError?:string}>>>({});
  function beginSettingsEdit(kind:"retention"|"limits"){
    setSettingsEdits(old=>old[kind]?old:{...old,[kind]:{version:settings.version,formVersion:settings.version,base:settings}});
  }
  async function saveSettingsEdit(kind:"retention"|"limits",changes:object){
    const edit=settingsEdits[kind]??{version:settings.version,formVersion:settings.version,base:settings};
    try{await api("settings","PATCH",{version:edit.version,changes})}
    catch(error){
      setSettingsEdits(old=>({...old,[kind]:edit}));
      if((error as Error&{status?:number}).status===409){
        try{const latest=await api("settings");setSettings((current:any)=>latest.version>=current.version?latest:current)}
        catch(readError){setSettingsEdits(old=>({...old,[kind]:{...edit,readError:`无法读取当前设置：${(readError as Error).message}`}}))}
      }
      throw error;
    }
    setSettingsEdits(old=>{const next={...old};delete next[kind];return next});
    await refresh();
    setNotice(kind==="limits"?"新请求将使用新限制；在途请求保留原快照。":"清理影响预览已生成，确认后再应用。");
  }
  const [editing, setEditing] = useState<Source | null>(null),
    [showAdd, setShowAdd] = useState(false),
    [form, setForm] = useState({
      name: "",
      kind: "api_key",
      native_protocol: "responses",
      provider:"openai_compatible", account_id:"", proxy_url:null as string|null,cloud_config:{} as NonNullable<Source["cloud_config"]>,
      allow_parameter_adjustment: false,
      base_url: "",
      models: "",
      credential: "",
    });
  const [management, setManagement] = useState<{page:string;id?:string}|null>(null);
  const managementDialog = useRef<HTMLDialogElement>(null);
  const sourceDialog=useRef<HTMLDialogElement>(null);
  const sourceDirty = useRef(false);
  function hasManagementDraft() { return !!managementDialog.current?.querySelector('[data-dirty="true"]'); }
  function closeManagement() {
    if(hasManagementDraft() && !window.confirm("有未保存的输入。关闭会丢弃这些输入，是否关闭？"))return;
    setFreshKey("");setManagement(null);
  }
  function closeSource() {
    if(sourceDirty.current && !window.confirm("来源有未保存的输入。关闭会丢弃这些输入，是否关闭？"))return;
    sourceDirty.current=false;
    setEditing(null);setSourceConflict(null);setSourceConflictReadError("");setError("");setErrorField("");
    setForm({name:"",kind:"api_key",native_protocol:"responses",provider:"openai_compatible",account_id:"",proxy_url:null,cloud_config:{},allow_parameter_adjustment:false,base_url:"",models:"",credential:""});setShowAdd(false);
  }
  useEffect(()=>{
    const warn=(event:BeforeUnloadEvent)=>{if(sourceDialog.current?.open && sourceDirty.current || managementDialog.current?.open && hasManagementDraft()){event.preventDefault();event.returnValue=""}};
    window.addEventListener("beforeunload",warn);return()=>window.removeEventListener("beforeunload",warn);
  },[]);
  useEffect(() => {try{localStorage.setItem("cove.ui.page",page)}catch{/* Navigation remains available for this session. */}},[page]);
  useEffect(() => {
    const dialog = managementDialog.current;
    if (!dialog) return;
    if (management && !dialog.open) dialog.showModal();
    else if (!management && dialog.open) dialog.close();
  }, [management]);
  useEffect(() => { if (detail) setManagement(null); }, [detail]);
  const [sourceConflict,setSourceConflict]=useState<Source|null>(null),[sourceConflictReadError,setSourceConflictReadError]=useState("");
  useEffect(()=>{
    const dialog=sourceDialog.current;
    if(!dialog)return;
    if(showAdd&&!dialog.open)dialog.showModal();
    else if(!showAdd&&dialog.open)dialog.close();
  },[showAdd]);
  function requestPath(nextCursor?: string) {
    const query = new URLSearchParams(requestQuery);
    if (filter) query.set("status", filter);
    if (nextCursor) query.set("cursor", String(nextCursor));
    return "requests?" + query;
  }
  async function refresh() {
    refreshInFlight.current++;
    try {
    const sequence=++refreshSequence.current;
    const paths=["sources","client-keys",requestPath(),"status","usage","settings","routes","model-aliases"];
    const results=await Promise.allSettled(paths.map(path=>api(path)));
    if(sequence!==refreshSequence.current)return;
    const setters=[setSources,setKeys,(v:any)=>{setRequests(v.items);setCursor(v.next_cursor)},setStatus,setUsage,setSettings,(v:any)=>setRoutes(v.items),(v:any)=>setAliases(v.items)];
    const errors:string[]=[];
    results.forEach((result,index)=>{if(result.status==="fulfilled")setters[index](result.value);else errors.push(paths[index].split("?")[0]+"："+result.reason.message)});
    if(results[0].status==="rejected")setStatus((old:any)=>({...old,sources_stale:true}));
    if(results[3].status==="rejected")setStatus((old:any)=>({...old,runtime_stale:true}));
    if(results[6].status==="rejected")setRoutes(old=>old.map(route=>({...route,runtime:undefined})));
    if(errors.length)setError(errors.join("；"));
    const sourceResult=results[0];
    if(sourceResult.status==="fulfilled"){
      const subs=(sourceResult.value as Source[]).filter(v=>v.kind==="codex_subscription");
      const operations=await Promise.allSettled(subs.map(async(v)=>[v.id,await api<Login>(`sources/${v.id}/login`)] as const));
      if(sequence!==refreshSequence.current)return;
      const next:Record<string,Login>={};operations.forEach((result,index)=>{if(result.status==="fulfilled")next[result.value[0]]=result.value[1];else next[subs[index].id]={status:"unknown",message:result.reason.message}});setLogin(next);
    }
  }
    finally {refreshInFlight.current--;}
  }
  async function loadMoreRequests() {
    if (!cursor) return;
    const selection=refreshSequence.current;
    const path=requestPath(cursor);
    await run(async()=>{
      const next=await api(path);
      if(selection!==refreshSequence.current)return;
      setRequests(old=>mergeRequestPages(old,next.items));
      setCursor(next.next_cursor);
    },"request-pagination");
  }
  async function run(fn: () => Promise<void>, actionID?: string) {
    const id=actionID ?? "global";
    if(runningActions.current.has(id))return;
    runningActions.current.add(id);
    if(actionID)setPendingActions([...runningActions.current]);else setBusy(true);
    setError("");
    setErrorField("");
    setNotice("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
      const field=(e as Error&{field?:string}).field||"";
      setErrorField(field);
      if(sourceDialog.current?.open && field)window.requestAnimationFrame(()=>sourceDialog.current?.querySelector<HTMLElement>(`[name="${CSS.escape(field)}"]`)?.focus());
    } finally {
      runningActions.current.delete(id);
      if(actionID)setPendingActions([...runningActions.current]);else setBusy(false);
    }
  }
  useEffect(() => {
    // Remove a stale launch fragment from pages opened by an older running build.
    if (window.location.hash) window.history.replaceState(null, "", window.location.pathname + window.location.search);
    const off = () => {
      setLogged(false);
      setFreshKey("");
      setError("本机连接已失效，请重新连接。");
    };
    window.addEventListener("gatt-signed-out", off);
    api("session", "POST", {})
      .then((result) => { saveSession(result.session_token); setLogged(true); })
      .catch((e) => setError(e.message))
      .finally(() => setReady(true));
    return () => window.removeEventListener("gatt-signed-out", off);
  }, []);
  useEffect(() => {
    if (!logged) return;
    const requestId = new URL(window.location.href).searchParams.get("request_id");
    if (!requestId) return;
    const selection = ++detailRevision.current;
    setPage("请求");
    void api(`requests/${encodeURIComponent(requestId)}`).then(result => {
      if (selection === detailRevision.current) setDetail(result);
    }).catch(error => {
      if (selection === detailRevision.current) setError(error.message);
    });
    return () => { detailRevision.current++; };
  }, [logged]);
  useEffect(() => {
    if (!logged) return;
    setError("");
    void refresh().catch(error => setError(error.message));
  }, [logged, filter, requestQuery]);
  useEffect(() => {
    if(!logged || !["概览","来源","路由","请求"].includes(page))return;
    let cancelled=false,inFlight=false;
    const timer=window.setInterval(async()=>{
      if(cancelled || document.hidden || inFlight || refreshInFlight.current || runningActions.current.size)return;
      inFlight=true;
      const selection=refreshSequence.current;
      try {
        const results=await Promise.allSettled([api("status"),api("routes"),api("sources")]);
        if(cancelled || selection!==refreshSequence.current || refreshInFlight.current)return;
        if(results[0].status==="fulfilled")setStatus(results[0].value);
        else setStatus((old:any)=>({...old,runtime_stale:true}));
        if(results[1].status==="fulfilled")setRoutes(results[1].value.items);
        else setRoutes(old=>old.map(route=>({...route,runtime:undefined})));
        if(results[2].status==="fulfilled")setSources(results[2].value);
        else setStatus((old:any)=>({...old,sources_stale:true}));
        const errors=results.filter((r):r is PromiseRejectedResult=>r.status==="rejected");
        if(errors.length)setError(errors.map(r=>r.reason.message).join("；"));
      } finally {inFlight=false;}
    },5000);
    return ()=>{cancelled=true;window.clearInterval(timer);};
  },[logged,page]);
  useEffect(() => {
    const pending = Object.entries(login).filter(
      ([, v]) => ["pending", "exchanging", "awaiting_confirmation"].includes(v.status),
    );
    if (!pending.length) return;
    const t = setInterval(() => {
      for (const [id] of pending)
        api<Login>(`sources/${id}/login`)
          .then((v) => {
            setLogin((old) => ({ ...old, [id]: v }));
            if (v.status !== login[id]?.status)
              refresh().catch((e) => setError(e.message));
          })
          .catch((e) => setError(e.message));
    }, 5000);
    return () => clearInterval(t);
  }, [login]);
  if (!ready) return <div className="loading">正在连接本机网关…</div>;
  if (!logged)
    return (
      <main className="login-shell">
        <h1>Cove</h1>
        <div role="alert" className="error">{error || "无法连接本机服务"}</div>
        <button onClick={() => window.location.reload()}>重新连接</button>
      </main>
    );
  const operationPage = management?.page || page;
  const detailSelection = detailRevision.current;
  async function saveSource() {
    let accountId=form.account_id;
    if(!editing && !accountId) {
      const account=await api("accounts","POST",{provider:form.kind==="codex_subscription"?"codex":form.provider,auth_type:form.kind,name:form.name});
      accountId=account.id;setForm(current=>({...current,account_id:accountId}));
      if(form.credential){await api(`accounts/${accountId}/credential`,"POST",{version:account.version,secret:form.credential});setForm(current=>({...current,credential:""}))}
    }
    const data = {
      ...form,
      account_id:accountId,
      ...(!editing?{credential:""}:{}),
      models: form.models
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
      ...(editing ? { version: editing.version } : {}),
    };
    try{await api(
      editing ? `sources/${editing.id}` : "sources",
      editing ? "PATCH" : "POST",
      data,
    )}catch(error){
      if(editing&&(error as Error&{status?:number}).status===409){
        try{const latest=await api<Source>(`sources/${editing.id}`);setSourceConflict(latest);setSourceConflictReadError("")}
        catch(readError){setSourceConflictReadError(`无法读取当前来源：${(readError as Error).message}`)}
      }
      throw error;
    }
    sourceDirty.current=false;setShowAdd(false);
    setEditing(null);
    setSourceConflict(null);setSourceConflictReadError("");
    setForm({
      name: "",
      kind: "api_key",
      native_protocol: "responses",
      provider:"openai_compatible", account_id:"", proxy_url:null as string|null,cloud_config:{} as NonNullable<Source["cloud_config"]>,
      allow_parameter_adjustment: false,
      base_url: "",
      models: "",
      credential: "",
    });
    await refresh();
    if (form.kind === "codex_subscription") setPage("来源");
    setNotice(form.kind === "codex_subscription" ? "来源已添加。点击「登录 ChatGPT」完成账号授权，再到模型页读取可用模型。" : "来源已保存，完成测试后才会显示已验证。");
  }
  function edit(s: Source) {
    sourceDirty.current=false;setErrorField("");
    setSourceConflict(null);setSourceConflictReadError("");
    setEditing(s);
    setForm({
      name: s.name,
      kind: s.kind,
      native_protocol: s.native_protocol || "responses",
      provider:s.provider,account_id:s.account_id,proxy_url:s.proxy_url,cloud_config:s.cloud_config||{},
      allow_parameter_adjustment: s.allow_parameter_adjustment,
      base_url: s.base_url,
      models: s.models.join(", "),
      credential: "",
    });
    setShowAdd(true);
  }
  function loginSource(s: Source) {
    const popup = window.open("about:blank", "_blank");
    if (popup) {
      popup.sessionStorage.removeItem(sessionKey);
      popup.opener = null;
    }
    void run(async () => {
      try {
        const operation = await api<Login>(`sources/${s.id}/login`, "POST", { version: s.version });
        setLogin(old => ({ ...old, [s.id]: operation }));
        if (operation.authorization_url) {
          if (popup) popup.location.href = operation.authorization_url;
          else setNotice("浏览器拦截了新窗口，请点击「继续授权」。");
        } else popup?.close();
      } catch (error) {
        popup?.close();
        throw error;
      }
    }, s.id);
  }
  const currentGuideKey = keys.find((k) => k.id === guideKey?.id);
  const routeGuide=!!currentGuideKey?.route_id;
  const sourceGuide=currentGuideKey?.revoked?undefined:sources.find(s=>s.id===currentGuideKey?.source_id);
  const guideModels=(routeGuide?aliases.filter(a=>a.route_id===currentGuideKey?.route_id).map(a=>a.public_model):(sourceGuide?.models||[])).filter(m=>currentGuideKey?.model_allowlist==null || currentGuideKey.model_allowlist.includes(m));
  const guideSource=sourceGuide || (routeGuide && guideModels.length?{kind:"route",models:guideModels,allow_parameter_adjustment:false}:undefined);
  const guideProtocols=["responses","chat_completions","messages","gemini"].filter(p=>currentGuideKey?.protocol_allowlist==null || currentGuideKey.protocol_allowlist.includes(p)).filter(p=>!(guideSource?.kind==="codex_subscription" && !guideSource.allow_parameter_adjustment && p==="messages"));
  const selectedModel=guideModels.includes(guideModel)?guideModel:guideModels[0];
  const apiProtocol=guideProtocols.includes(guideProtocol)?guideProtocol:(guideProtocols[0]||"responses");
  const apiPath = apiProtocol === "gemini" ? `/v1beta/models/${encodeURIComponent(selectedModel)}:streamGenerateContent?alt=sse` : apiProtocol === "chat_completions" ? "/v1/chat/completions" : apiProtocol === "messages" ? "/v1/messages" : "/v1/responses";
  const apiPayload = apiProtocol === "gemini" ? {contents:[{role:"user",parts:[{text:"Hello"}]}]} : apiProtocol === "responses"
    ? { model: selectedModel, input: [{ role: "user", content: [{ type: "input_text", text: "Hello" }] }], stream: true, store: false }
    : { model: selectedModel, messages: [{ role: "user", content: "Hello" }], stream: true, ...(apiProtocol === "messages" ? { max_tokens: 1024 } : {}) };
  const shellJSON = "'" + JSON.stringify(apiPayload, null, 2).replaceAll("'", "'\\''") + "'";
  const curlExample = `curl -N "http://${status.listen}${apiPath}" \\\n  -H "${apiProtocol === "gemini" ? "x-goog-api-key" : apiProtocol === "messages" ? "x-api-key" : "Authorization"}: ${apiProtocol === "messages" || apiProtocol === "gemini" ? "" : "Bearer "}$PERSONAL_GATEWAY_KEY" \\\n${apiProtocol === "messages" ? '  -H "anthropic-version: 2023-06-01" \\\n' : ""}  -H "Content-Type: application/json" \\\n  -d ${shellJSON}`;
  const template = guideSource
    ? `web_search = "disabled"\nmodel_provider = "gatt"\nmodel = ${JSON.stringify(selectedModel)}\n\n[model_providers.gatt]\nname = "Cove"\nbase_url = ${JSON.stringify("http://" + status.listen + "/v1")}\nenv_key = "PERSONAL_GATEWAY_KEY"\nwire_api = "responses"\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0`
    : "";
  const launchCommand = `(\n  set -e\n  COVE_CLIENT_HOME="$(mktemp -d \"\${TMPDIR:-/tmp}/cove-codex.XXXXXX\")"\n  chmod 700 "$COVE_CLIENT_HOME"\n  cat > "$COVE_CLIENT_HOME/config.toml" <<'COVE_CONFIG'\n${template}\nCOVE_CONFIG\n  printf '粘贴 Cove 客户端 Key（输入不显示）: '\n  read -r -s PERSONAL_GATEWAY_KEY\n  printf '\\n'\n  export PERSONAL_GATEWAY_KEY\n  CODEX_HOME="$COVE_CLIENT_HOME" codex\n)`;
  return (
    <div className="shell">
      <V13Console api={api} page={page} sources={sources} keys={keys} requests={requests} routes={routes} aliases={aliases}
        logins={login} onLogin={loginSource}
        onCancelLogin={s=>run(async()=>{await api(`sources/${s.id}/login`, "DELETE");await refresh();},s.id)}
        hasMoreRequests={!!cursor} onMoreRequests={loadMoreRequests}
        status={status} settings={settings} usage={usage} detail={detail} filter={filter} requestsFiltered={!!filter || !!requestQuery} busy={busy} pending={pendingActions}
        onPage={next=>{setPage(next);setError("");setNotice("");closeDetail()}}
        onManage={(next,id)=>{setFreshKey("");setManagement({page:next,id})}}
        onAdd={preset=>{
          sourceDirty.current=false;setErrorField("");
          const defaults:Partial<Record<string,{kind:string;provider:string;native_protocol:string;base_url:string}>> = {
            codex:{kind:"codex_subscription",provider:"codex",native_protocol:"responses",base_url:""},
            openai:{kind:"api_key",provider:"openai",native_protocol:"responses",base_url:"https://api.openai.com/v1"},
            anthropic:{kind:"api_key",provider:"anthropic",native_protocol:"messages",base_url:"https://api.anthropic.com"},
            deepseek:{kind:"api_key",provider:"openai_compatible",native_protocol:"chat_completions",base_url:"https://api.deepseek.com/v1"},
            gemini:{kind:"api_key",provider:"gemini",native_protocol:"gemini",base_url:"https://generativelanguage.googleapis.com"},
            ollama:{kind:"none",provider:"local",native_protocol:"chat_completions",base_url:"http://127.0.0.1:11434/v1"},
          };
          setEditing(null);setSourceConflict(null);setSourceConflictReadError("");setError("");
          setForm({name:preset==="codex"?"ChatGPT 订阅":"",kind:"api_key",provider:"openai_compatible",native_protocol:"responses",account_id:"",proxy_url:null,cloud_config:{},allow_parameter_adjustment:false,base_url:"",models:"",credential:"",...(defaults[preset||""]||{})});
          setShowAdd(true);
        }} onEdit={edit}
        onDetail={id=>void run(async()=>{const selected=++detailRevision.current;const result=await api(`requests/${id}`);if(selected===detailRevision.current)setDetail(result)})}
        onCloseDetail={closeDetail}
        onReconcile={async()=>{const result=await api(`requests/${detail.request.id}`);if(detailSelection===detailRevision.current)setDetail(result)}}
        onFilter={next=>{if(next===filter)return;refreshSequence.current++;setCursor(null);setFilter(next)}} onRefresh={refresh} run={run} onError={setError}/>
      {!management && !showAdd && <div className="global-feedback" aria-live="polite">
        {error && <div className="error" role="alert">{error}</div>}
        {notice && <div className="notice" role="status">{notice}</div>}
        {status.maintenance_error && <div className="error" role="alert">{status.maintenance_error}</div>}
      </div>}
      <dialog ref={managementDialog} className="management-sheet" aria-label={`${operationPage}管理`} onChangeCapture={event=>{const form=(event.target as HTMLElement).closest("form");if(form&&!form.hasAttribute("data-draft-ignore"))form.dataset.dirty="true"}} onCancel={event=>{event.preventDefault();closeManagement()}}>
      <div className="sheet-heading"><h2>{operationPage === "工具" ? "客户端配置" : operationPage === "API Keys" && !management?.id ? "创建 Key" : `${operationPage}管理`}</h2><button className="icon-button" aria-label="关闭管理" onClick={closeManagement}><span data-i="">close</span></button></div>
      {management && <div className="workspace" data-operation-page={operationPage} data-selection={management.id || "create"}>
        {error && (
          <div className="error" role="alert">
            {error}
          </div>
        )}
        {notice && (
          <div className="notice" role="status">
            {notice}
          </div>
        )}
        {status.maintenance_error && (
          <div className="error">{status.maintenance_error}</div>
        )}
        {operationPage === "来源" && (
          <>
            {!management?.id && <AccountConsole api={api} onChanged={refresh}/>}
            <div className="summary-strip">
              <div>
                <strong>{sources.length}</strong>
                <span>个来源</span>
              </div>
              <div>
                <strong>{sources.filter((s) => s.enabled).length}</strong>
                <span>已启用</span>
              </div>
              <div>
                <strong>
                  {
                    sources.filter((s) => s.verification.status === "passed")
                      .length
                  }
                </strong>
                <span>文本测试通过</span>
              </div>
              <p>从一个来源开始，完成一次可追踪的调用。</p>
            </div>
            {!sources.length ? (
              <div className="empty">
                <div className="empty-icon">◈</div>
                <h2>连接你的第一个模型来源</h2>
                <p>添加原生 Responses API，或独立登录 Codex 订阅账号。</p>
                <button onClick={() => setShowAdd(true)}>添加来源 →</button>
              </div>
            ) : (
              <div className="source-grid">
                {sources.filter(s=>!management?.id || management.id===s.id).map((s) => {
                  const sourceBusy=busy||pendingActions.includes(s.id);
                  const runSource=(fn:()=>Promise<void>)=>run(fn,s.id);
                  return (
                  <article className="source-card" key={s.id}>
                    <div className="card-top">
                      <span
                        className={
                          "source-symbol " +
                          (s.kind === "codex_subscription" ? "dark" : "")
                        }
                      >
                        {s.kind === "codex_subscription" ? "C" : "A"}
                      </span>
                      <div>
                        <h2>{s.name}</h2>
                        <span>
                          {s.kind === "codex_subscription"
                            ? "Codex 订阅 · 实验接入"
                            : `${s.provider||"API"} · ${s.native_protocol||"responses"}` }
                        </span>
                      </div>
                      <span className={"badge " + (!s.enabled ? "muted" : "")}>
                        {s.enabled ? "已启用" : "已停用"}
                      </span>
                    </div>
                    <p className="endpoint">{s.base_url}</p>
                    <div className="chips">
                      {s.models.map((m) => (
                        <code key={m}>{m}</code>
                      ))}
                    </div>
                    <dl>
                      <div>
                        <dt>认证</dt>
                        <dd>{label(s.auth_status)}</dd>
                      </div>
                      <div>
                        <dt>调用验证</dt>
                        <dd>{label(s.verification.status)}{s.verification.model && <small> · {s.verification.model}</small>}</dd>
                      </div>
                      <div>
                        <dt>最近调用</dt>
                        <dd>
                          {s.quota.call_health
                            ? label(s.quota.call_health)
                            : "尚无请求"}
                        </dd>
                      </div>
                      <div>
                        <dt>原生额度</dt>
                        <dd>{s.quota.status==="available"?"已观测":s.quota.status==="stale"?"已过期，保留旧值":s.quota.status==="unsupported"?"未支持":"未知"}</dd>
                      </div>
                    </dl>
                    {s.quota.last_error&&<p className="hint">{s.quota.last_error}</p>}
                    {s.quota.observed_at&&<p className="hint">观测 {new Date(s.quota.observed_at).toLocaleString()}{s.quota.expires_at?` · 过期 ${new Date(s.quota.expires_at).toLocaleString()}`:""}</p>}
                    {(s.quota.windows||[]).map((window,index)=><p className="hint" key={window.dimension+index}>{window.limit_name||window.dimension} · {window.used_percent==null?"用量未知":`已用 ${window.used_percent}%`}{window.reset_at?` · 重置 ${new Date(window.reset_at).toLocaleString()}`:""} · {s.quota.status==="available"&&window.status==="available"?"当前观测":"旧值或未知"}</p>)}
                    {s.kind==="codex_subscription"&&<details><summary>刷新与窗口历史</summary><p>查询仅观测额度，不产生模型输出；窗口按提供方单位保存，过期值不作为当前余额。</p><button disabled={sourceBusy} onClick={()=>runSource(async()=>{let op=await api(`sources/${s.id}/quota-refresh`,"POST",{});let delay=1000;while(op.state==="running"){await new Promise(r=>setTimeout(r,delay));op=await api(`operations/${op.id}`);delay=Math.min(delay*2,8000)}await refresh();if(op.state&&op.state!=="succeeded")throw new Error(op.error||"观测失败，旧窗口保留");setNotice(op.cached?"仍在刷新间隔内，保留当前观测。":"额度观测已完成。")})}>刷新额度</button><button disabled={sourceBusy} onClick={()=>runSource(async()=>{const value=await api(`sources/${s.id}/quota-history`);setQuotaHistory(old=>({...old,[s.id]:value.data}))})}>读取近30天历史</button>{quotaHistory[s.id]?.map((snapshot,index)=><p className="hint" key={index}>{snapshot.observed_at?new Date(snapshot.observed_at).toLocaleString():"观测时间未知"} · {snapshot.status} · {(snapshot.windows||[]).map((window:any)=>`${window.limit_name||window.dimension}: ${window.used_percent==null?"未知":window.used_percent+"%"}`).join("；")}</p>)}</details>}
                    {s.verification.request_id && <button className="text" onClick={() => runSource(async () => {const selected=++detailRevision.current;const result=await api(`requests/${s.verification.request_id}`);if(selected===detailRevision.current)setDetail(result)})}>查看最近文本测试 →</button>}
                    {s.kind === "codex_subscription" && (
                      <p className="hint">
                        使用官方 Codex
                        客户端标识进行独立授权实验。登录成功不代表工具调用已验证。
                      </p>
                    )}
                    {login[s.id] && login[s.id].status !== "idle" && (
                      <div className="login-operation">
                        <b>
                          {login[s.id].message ||
                            (login[s.id].status === "exchanging"
                              ? "正在完成授权…"
                              : "在浏览器完成 ChatGPT 授权后，回到此页面")}
                        </b>
                        {login[s.id].authorization_url && (
                          <a
                            href={login[s.id].authorization_url}
                            target="_blank"
                            rel="noreferrer"
                          >
                            重新打开授权页面 ↗
                          </a>
                        )}
                        {login[s.id].status === "awaiting_confirmation" && <>
                          <p>原身份：{login[s.id].previous_identity}<br />新身份：{login[s.id].new_identity}</p>
                          <p className="hint">确认后，新的调用使用新账号，旧会话需要重新开始。取消会保留原账号。</p>
                          <button disabled={sourceBusy} onClick={() => runSource(async () => {
                            await api(`sources/${s.id}/login/confirm`, "POST", { version: s.version, operation_id: login[s.id].id });
                            await refresh();
                          })}>确认更换账号</button>
                        </>}
                        {["pending", "exchanging", "awaiting_confirmation"].includes(
                          login[s.id].status,
                        ) && (
                          <button
                            className="text"
                            onClick={() =>
                              runSource(async () => {
                                await api(`sources/${s.id}/login`, "DELETE");
                                setLogin((v) => ({
                                  ...v,
                                  [s.id]: {
                                    status: "cancelled",
                                    message: "登录已取消",
                                  },
                                }));
                              })
                            }
                          >
                            取消登录
                          </button>
                        )}
                      </div>
                    )}
                    <div className="card-actions">
                      {s.models.length > 1 && <select aria-label={s.name + " 的测试模型"} value={testModels[s.id] || s.models[0]} onChange={(e) => setTestModels((old) => ({ ...old, [s.id]: e.target.value }))}>
                        {s.models.map((m) => <option key={m} value={m}>{m}</option>)}
                      </select>}
                      <button
                        className="secondary"
                        disabled={sourceBusy}
                        onClick={() =>
                          runSource(async () => {
                            const selected=++detailRevision.current;
                            const v = await api(
                              `sources/${s.id}/test`,
                              "POST",
                              { model: s.models.includes(testModels[s.id]) ? testModels[s.id] : s.models[0] },
                            );
                            await refresh();
                            setNotice(
                              `文本测试${label(v.request.status)}；工具循环需另行验证。`,
                            );
                            if(selected===detailRevision.current)setDetail(v);
                          })
                        }
                      >
                        测试调用
                      </button>
                      {s.kind === "codex_subscription" && (
                        <button
                          className="secondary"
                          disabled={sourceBusy}
                          onClick={() => loginSource(s)}
                        >
                          使用 ChatGPT 登录
                        </button>
                      )}
                      <button className="text" disabled={sourceBusy} onClick={() => edit(s)}>
                        编辑
                      </button>
                      <button
                        className="text"
                        disabled={sourceBusy}
                        onClick={() =>
                          runSource(async () => {
                            await api(`sources/${s.id}`, "PATCH", {
                              version: s.version,
                              enabled: !s.enabled,
                            });
                            await refresh();
                          })
                        }
                      >
                        {s.enabled ? "停用" : "启用"}
                      </button>
                    </div>
                    <details>
                      <summary>来源管理与计量</summary>
                      {s.kind === "api_key" && (
                        <>
                          <button
                            className="secondary"
                            disabled={sourceBusy}
                            onClick={() =>
                              runSource(async () => {
                                const v = await api(
                                  `sources/${s.id}/test`,
                                  "POST",
                                  { continuation: true },
                                );
                                await refresh();
                                setNotice(
                                  `两轮续接测试${label(v.request.status)}`,
                                );
                              })
                            }
                          >
                            验证服务端续接（两次调用）
                          </button>
                          <form
                            className="price-form"
                            onSubmit={(e) => {
                              e.preventDefault();
                              const data = new FormData(e.currentTarget);
                              runSource(async () => {
                                await api(`sources/${s.id}`, "PATCH", {
                                  version: s.version,
                                  price: {
                                    currency: data.get("currency"),
                                    input_per_million: data.get("input"),
                                    cached_per_million: data.get("cached"),
                                    output_per_million: data.get("output"),
                                    as_of: new Date().toISOString(),
                                  },
                                });
                                await refresh();
                                setNotice("价格已保存，仅对后续请求估算生效。");
                              });
                            }}
                          >
                            <label>
                              币种
                              <input
                                name="currency"
                                required
                                pattern="[A-Z]{3}"
                                placeholder="USD"
                                defaultValue={s.price?.currency}
                              />
                            </label>
                            <label>
                              输入 / 百万 token
                              <input
                                name="input"
                                required
                                inputMode="decimal"
                                defaultValue={s.price?.input_per_million}
                              />
                            </label>
                            <label>
                              缓存输入 / 百万（可空）
                              <input
                                name="cached"
                                inputMode="decimal"
                                defaultValue={s.price?.cached_per_million}
                              />
                            </label>
                            <label>
                              输出 / 百万 token
                              <input
                                name="output"
                                required
                                inputMode="decimal"
                                defaultValue={s.price?.output_per_million}
                              />
                            </label>
                            <button className="secondary">
                              保存已核对的价格
                            </button>
                          </form>
                        </>
                      )}
                      <p className="hint">
                        停用与退出只阻止新请求。删除前请撤销 Key
                        并结束运行请求。
                      </p>
                      {s.kind === "codex_subscription" && (
                        <button
                          className="secondary"
                          onClick={() =>
                            runSource(async () => {
                              await api(`sources/${s.id}/logout`, "POST", {});
                              await refresh();
                            })
                          }
                        >
                          退出来源账号
                        </button>
                      )}
                      <button
                        className="danger"
                        onClick={() =>
                          runSource(async () => {
                            await api(`sources/${s.id}`, "DELETE");
                            await refresh();
                          })
                        }
                      >
                        删除来源
                      </button>
                    </details>
                  </article>
                );})}
              </div>
            )}
            <div className="footnote">
              <span>↗</span> 请求将发送到配置的上游地址。Cove
              固定 Key 使用指定来源；路由 Key 按明确的成员和策略选择。
            </div>
          </>
        )}
        {(operationPage === "模型" || operationPage === "路由") && <ConsoleModules api={api} sources={sources} clientKeys={keys} page={operationPage} selectedId={management?.id} onChanged={refresh} />}
        {operationPage === "工具" && <><ClientConsole api={api} onChanged={refresh} initialKind={management?.id?.split(":")[0]} initialModel={management?.id?.split(":").slice(1).join(":")}/><ExtendedProtocolSettings api={api}/><ConfigExtensionsConsole api={api}/><NativeOperationCapabilities api={api} sources={sources} onChanged={refresh}/></>}
        {operationPage === "运维" && <><NotificationSettings api={api}/><ResourceConsole api={api}/><OperationsConsole api={api} status={status}/></>}
        {operationPage === "预算" && <BudgetConsole api={api} keys={keys}/>}
        {operationPage === "API Keys" && (
          <>
            <section className="panel key-create">
              <h2>创建 API Key</h2>
              {!freshKey && <form
                className="key-create-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  run(async () => {
                    const v = await api("client-keys", "POST", {
                      name: f.get("name"),
                      ...keyPolicyInput(f),
                      ...keyBudgetInput(f),
                    });
                    setFreshKey(v.secret);
                    setGuideKey(v.key);
                    await refresh();
                  });
                }}
              >
                <label>Key 名称<input
                  name="name"
                  required
                  placeholder="例如：Codex · 个人项目"
                /></label>
                <KeyPolicyFields sources={sources} routes={routes}/>
                <details><summary>预算设置</summary><KeyBudgetFields api={api}/></details>
                <button disabled={busy || !sources.length && !routes.length}>创建 API Key</button>
              </form>}
              {freshKey && (
                <div className="secret">
                  <b>只显示这一次，请保存在你的客户端。</b>
                  <code>{freshKey}</code>
                  <button
                    className="secondary"
                    onClick={() =>
                      run(async () => {
                        await navigator.clipboard.writeText(freshKey);
                        setNotice("客户端 Key 已复制");
                      })
                    }
                  >
                    复制
                  </button>
                  <button className="text" onClick={() => setFreshKey("")}>
                    已保存，隐藏
                  </button>
                </div>
              )}
            </section>
            <section className="panel key-list">
              <h2>
                已创建的 API Key <small>{keys.length}</small>
              </h2>
              {!keys.length ? (
                <p className="placeholder">还没有 API Key，请先选择来源并创建。</p>
              ) : (
                keys.filter(k=>!management?.id || management.id===k.id).map((k) => {
                  const keyBusy=pendingActions.includes(k.id);
                  const keyEdit=keyEdits[k.id];
                  const runKey=(fn:()=>Promise<void>)=>run(fn,k.id);
                  return (
                  <div className="tool-row" key={k.id}>
                    <div>
                      <strong>{k.name}</strong>
                      <code>{k.fingerprint}…</code>
                      <span>
                        {k.enabled===false?"已停用 · ":""}{k.last_seen_at
                          ? "最近请求 " + when(k.last_seen_at)
                          : "尚未观察到接入"}
                      </span>
                      {k.budget_summary?.map(b=><span key={b.id}>{b.name} · {b.currency} 已结算 {b.settled} · 预留 {b.reserved} · 待对账 {b.pending}</span>)}
                    </div>
                    {k.revoked ? (
                      <span className="badge muted">已撤销</span>
                    ) : (
                      <>
                        <button disabled={keyBusy} onClick={()=>runKey(async()=>{await api(`client-keys/${k.id}`,"PATCH",{version:k.version,enabled:k.enabled===false});await refresh()})}>{k.enabled===false?"启用":"停用"}</button>
                        <select
                          disabled={keyBusy}
                          aria-label={"切换 " + k.name + " 的来源"}
                          value={(k.route_id?"route:":"source:")+(k.route_id||k.source_id)}
                          onChange={(e) =>
                            runKey(async () => {
                              await api(`client-keys/${k.id}`, "PATCH", {
                                target: { kind:e.target.value.split(":")[0], id:e.target.value.split(":").slice(1).join(":") },
                                version: k.version,
                              });
                              await refresh();
                              setNotice(
                                "来源已切换；旧 previous_response_id 不能跨来源续接。",
                              );
                            })
                          }
                        >
                          {sources.map((s) => (
                            <option value={"source:"+s.id} key={s.id}>
                              {s.name}
                            </option>
                          ))}
                          {routes.map(route=><option value={"route:"+route.id} key={route.id}>路由 · {route.name}</option>)}
                        </select>
                        <details className="key-policy"><summary>权限、限额与轮换</summary>
                          <form data-dirty={!!keyEdit} key={keyEdit?.formVersion??k.version} onChange={()=>setKeyEdits(old=>old[k.id]?old:{...old,[k.id]:{version:k.version,formVersion:k.version,base:k}})} onSubmit={e=>{e.preventDefault();const f=new FormData(e.currentTarget),edit=keyEdit??{version:k.version,formVersion:k.version,base:k};runKey(async()=>{
                            try{await api(`client-keys/${k.id}`,"PATCH",{version:edit.version,...keyPolicyInput(f)})}
                            catch(error){
                              setKeyEdits(old=>({...old,[k.id]:edit}));
                              if((error as Error&{status?:number}).status===409){
                                try{const latest=await api<Key>(`client-keys/${k.id}`);setKeys(old=>old.map(v=>v.id===k.id&&latest.version>=v.version?latest:v))}
                                catch(readError){setKeyEdits(old=>({...old,[k.id]:{...edit,readError:`无法读取当前 Key：${(readError as Error).message}`}}))}
                              }
                              throw error;
                            }
                            setKeyEdits(old=>{const next={...old};delete next[k.id];return next});await refresh();
                          })}}>
                            <fieldset disabled={keyBusy}><KeyPolicyFields sources={sources} routes={routes} initial={keyEdit?.base??k}/></fieldset>
                            <button disabled={keyBusy}>保存权限与限额</button>
                            {keyEdit&&keyEdit.version!==k.version&&<div role="region" aria-label="Key 权限版本冲突" aria-live="polite">
                              <p>编辑基于 v{keyEdit.version}；当前 v{k.version}。本地输入已保留，本次尚未覆盖服务器设置。</p>
                              <p>当前目标 {(k.route_id?"路由 ":"来源 ")+(k.route_id||k.source_id)}；协议 {JSON.stringify(k.protocol_allowlist)}；模型 {JSON.stringify(k.model_allowlist)}；操作 {JSON.stringify(k.operation_allowlist)}；到期 {k.expires_at||"未设置"}；限额 {JSON.stringify(k.limits)}。</p>
                              <button type="button" disabled={keyBusy} onClick={()=>setKeyEdits(old=>({...old,[k.id]:{...keyEdit,version:k.version,readError:undefined}}))}>使用当前版本，保留权限输入</button>
                              <button type="button" disabled={keyBusy} onClick={()=>setKeyEdits(old=>{const next={...old};delete next[k.id];return next})}>放弃权限修改</button>
                            </div>}
                            {keyEdit?.readError&&<p role="alert" className="error">{keyEdit.readError}；权限输入已保留。</p>}
                          </form>
                          <form onSubmit={e=>{e.preventDefault();const element=e.currentTarget,f=new FormData(element);runKey(async()=>{const date=String(f.get("revoke_at")||"");const result=await api(`client-keys/${k.id}/rotate`,"POST",{version:k.version,revoke_at:date?new Date(date).toISOString():null});element.reset();element.dataset.dirty="false";setFreshKey(result.secret);setGuideKey(result.key);await refresh()})}}>
                            <label>旧 Key 计划失效时间<input name="revoke_at" type="datetime-local"/></label><p>留空时旧 Key 保持有效，验证新 Key 后再明确撤销。新旧 Key 共用原有专属预算，轮换保留已用额和未释放预留。</p><button disabled={keyBusy}>创建轮换 Key</button>
                          </form>
                        </details>
                        <button
                          className="secondary"
                          onClick={() => setGuideKey(k)}
                        >
                          接入与恢复
                        </button>
                        <button
                          className="text"
                          disabled={keyBusy}
                          onClick={() =>
                            runKey(async () => {
                              await api(`client-keys/${k.id}`, "DELETE",{version:k.version});
                              await refresh();
                            })
                          }
                        >
                          撤销
                        </button>
                        <button className="text" disabled={keyBusy} onClick={()=>runKey(async()=>{await api(`client-keys/${k.id}`,"DELETE",{version:k.version,cancel_active:true});await refresh();setNotice("Key 已撤销并请求取消在途调用；最终状态及未知费用保留在请求记录中。")})}>撤销并取消在途</button>
                      </>
                    )}
                  </div>
                );})
              )}
            </section>
            {guideKey && guideSource && (
              <section className="panel">
                <h2>接入 · {guideKey.name}</h2>
                <label>使用模型
                  <select value={selectedModel} onChange={(e) => setGuideModel(e.target.value)}>
                    {guideModels.map((m) => <option key={m} value={m}>{m}</option>)}
                  </select>
                </label>
                <h3>API 调用</h3>
                <p>先把该客户端 Key 设置到当前终端的 <code>PERSONAL_GATEWAY_KEY</code>，选择客户端使用的协议。</p>
                <label>客户端协议
                  <select value={apiProtocol} onChange={(e) => setGuideProtocol(e.target.value)}>
                    <option value="responses" disabled={!guideProtocols.includes("responses")}>OpenAI Responses</option>
                    <option value="chat_completions" disabled={!guideProtocols.includes("chat_completions")}>OpenAI Chat Completions</option>
                    <option value="gemini" disabled={!guideProtocols.includes("gemini")}>Gemini REST</option>
                    <option value="messages" disabled={!guideProtocols.includes("messages")}>Anthropic Messages</option>
                  </select>
                </label>
                <pre>{curlExample}</pre>
                <button className="secondary" onClick={() => run(async () => {
                  await navigator.clipboard.writeText(curlExample);
                  setNotice("API 调用示例已复制，请使用当前工具的客户端 Key。");
                })}>复制 API 示例</button>
                <p className="hint">HTTP 协议支持文本、流式输出和客户端函数工具。图像、签名历史与服务端工具按选定来源和模型能力处理；未声明的组合会明确拒绝。</p>
                {guideSource.kind === "codex_subscription" && <p className="hint">此订阅不执行输出 token 上限。Messages 需要在来源明确开启参数调整；严格预算不适用。</p>}
                <h3>Codex CLI 独立接入</h3>
                <p>
                  复制命令到已安装 Codex CLI 的终端运行，再按提示粘贴刚创建的客户端 Key。
                  命令会创建独立的临时配置目录；退出 Codex 后，此终端自动恢复原环境。
                </p>
                <pre>{launchCommand}</pre>
                <button className="secondary" onClick={() => run(async () => {
                  await navigator.clipboard.writeText(launchCommand);
                  setNotice("启动命令已复制。运行后按终端提示粘贴客户端 Key。");
                })}>复制启动命令</button>
                <p>进入 Codex 后发送：<code>请执行 printf COVE_TOOL_OK，然后报告结果。</code></p>
                <p className="hint">首次执行可能需要你在 Codex 中允许命令。临时目录保留本次客户端会话，网关只记录调用元数据。</p>
                <p>{currentGuideKey?.last_seen_at ? "已观察到该 Key 的请求：" + when(currentGuideKey.last_seen_at) : "尚未观察到该 Key 的请求。创建 Key 不代表客户端已经接入。"}</p>
                <button className="text" onClick={() => {
                  setRequestQuery(new URLSearchParams({ client_key_id: guideKey.id, origin: "client" }).toString());
                  setFilter(""); setPage("请求");setManagement(null);
                }}>查看这个工具的调用 →</button>
                <details><summary>已有配置的手动接入模板</summary><pre>{template}</pre></details>
                <p className="hint">
                  HTTP/SSE；关闭客户端重试。compact 与 WebSocket
                  不在当前已验证范围，长会话兼容性待验。
                </p>
                <h3>恢复原配置</h3>
                <p>使用上面的独立启动命令时，退出 Codex 即结束本次接入；再次正常运行 Codex 使用原来的配置。下面的步骤适用于手动修改已有配置。</p>
                <ol>
                  <li>修改前记下上述字段的原值。</li>
                  <li>
                    恢复时逐项比较原值、本次建议值、现在的值。现在的值若再次改过，由你决定保留或恢复。
                  </li>
                  <li>
                    仅恢复本次修改项，退出该终端的
                    PERSONAL_GATEWAY_KEY；保留其他配置和原登录。
                  </li>
                  <li>发起直连请求，并确认 Cove 不再出现该请求。</li>
                </ol>
              </section>
            )}
          </>
        )}
        {operationPage === "请求" && (
          <section className="panel">
            {(status.queued||[]).length>0&&<section><h3>等待派发</h3>{status.queued.map((q:any)=><div className="tool-row" key={q.id}><div><code>{q.id}</code><span>排队阶段 {q.stage} · 尚未扣除RPM或预算 · {new Date(q.queued_at).toLocaleTimeString()}</span></div><button disabled={busy} onClick={()=>run(async()=>{await api(`requests/${q.id}/cancel`,"POST",{version:q.version});await refresh()})}>取消排队</button></div>)}</section>}
            <div className="section-title">
              <h2>调用记录</h2>
              <select
                aria-label="筛选请求状态"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              >
                <option value="">全部状态</option>
                {[
                  "succeeded",
                  "failed",
                  "cancelled",
                  "interrupted",
                  "unverified",
                  "dispatching",
                  "streaming",
                ].map((s) => (
                  <option value={s} key={s}>
                    {label(s)}
                  </option>
                ))}
              </select>
            </div>
            <form data-draft-ignore key={requestQuery} className="inline-form request-filters" onSubmit={(e) => {
              e.preventDefault();
              const data = new FormData(e.currentTarget), query = new URLSearchParams();
              for (const name of ["source_id", "client_key_id", "origin", "from", "to"]) {
                const value = String(data.get(name) || "");
                if (value) query.set(name, name === "from" || name === "to" ? new Date(value).toISOString() : value);
              }
              setRequestQuery(query.toString());
            }}>
              <select name="source_id" aria-label="筛选请求来源" defaultValue={new URLSearchParams(requestQuery).get("source_id") || ""}>
                <option value="">全部来源</option>{sources.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
              </select>
              <select name="client_key_id" aria-label="筛选客户端" defaultValue={new URLSearchParams(requestQuery).get("client_key_id") || ""}>
                <option value="">全部客户端</option>{keys.map((k) => <option key={k.id} value={k.id}>{k.name}{k.revoked ? "（已撤销）" : ""}</option>)}
              </select>
              <select name="origin" aria-label="筛选调用类型" defaultValue={new URLSearchParams(requestQuery).get("origin") || ""}>
                <option value="">全部调用</option><option value="client">客户端调用</option><option value="admin_test">管理端测试</option>
              </select>
              <input name="from" type="datetime-local" aria-label="请求起始时间" defaultValue={localInput(new URLSearchParams(requestQuery).get("from"))} />
              <input name="to" type="datetime-local" aria-label="请求结束时间" defaultValue={localInput(new URLSearchParams(requestQuery).get("to"))} />
              <button className="secondary" disabled={busy}>筛选</button>
              <button type="reset" className="text" onClick={() => { setRequestQuery(""); setFilter(""); }}>清除筛选</button>
            </form>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>时间 / 工具</th>
                    <th>来源 / 模型</th>
                    <th>结果</th>
                    <th>耗时</th>
                    <th>Token · 输入 / 输出</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {requests.map((v) => (
                    <tr key={v.id}>
                      <td>
                        {when(v.started_at)}
                        <small>
                          {v.client_name}
                          {v.origin === "admin_test" ? " · 测试" : ""}
                          {" · " + label(v.protocol || "responses")}
                        </small>
                      </td>
                      <td>
                        {v.source_name}
                        <small>{v.requested_model}</small>
                      </td>
                      <td>
                        <span
                          className={
                            "badge " + (v.status === "succeeded" ? "" : "muted")
                          }
                        >
                          {label(v.status)}
                        </span>
                      </td>
                      <td>{v.duration_ms} ms</td>
                      <td>
                        {format(v.usage.input_tokens)} /{" "}
                        {format(v.usage.output_tokens)}
                        <small>{label(v.usage_completeness)}</small>
                      </td>
                      <td>
                        <button
                          className="text"
                          onClick={() =>
                            run(async () => {const selected=++detailRevision.current;const result=await api(`requests/${v.id}`);if(selected===detailRevision.current)setDetail(result)})
                          }
                        >
                          详情 ↗
                        </button>
                        {["streaming", "dispatching"].includes(v.status) && (
                          <button
                            className="text"
                            onClick={() =>
                              run(async () => {
                                await api(
                                  `requests/${v.id}/cancel`,
                                  "POST",
                                  {version:v.version},
                                );
                                await refresh();
                              })
                            }
                          >
                            取消
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!requests.length && (
              <div className="placeholder">
                尚无匹配的请求。完成来源测试或接入工具后，这里会出现真实记录。
              </div>
            )}
            {cursor && (
              <button
                className="secondary"
                onClick={() =>
                  loadMoreRequests()
                }
              >
                加载更多
              </button>
            )}
          </section>
        )}
        {operationPage === "用量" && (
          <>
            <div className="metrics">
              {[
                ["请求数", usage.requests],
                ["已知输入 token", usage.known_input_tokens],
                ["已知输出 token", usage.known_output_tokens],
                ["用量未知请求", usage.usage_unknown_requests],
              ].map(([t, v]) => (
                <div key={t} className="metric">
                  <span>{t}</span>
                  <strong>{format(v)}</strong>
                </div>
              ))}
            </div>
            <section className="panel">
              <h2>请求结果</h2>
              <dl className="settings-list">
                {["succeeded", "failed", "cancelled", "interrupted", "unverified", "dispatching", "streaming"].map((state) => (
                  <div key={state}><dt>{label(state)}</dt><dd>{format(usage.states?.[state] ?? 0)}</dd></div>
                ))}
              </dl>
              <p className="hint">“结果未确认”表示数据已转发，但观察范围不足以确认终态。已知用量仍计入，未知用量不按零处理。</p>
            </section>
            <section className="panel">
              <h2>费用估算</h2>
              {Object.keys(usage.estimated_cost_by_currency || {}).length ? (
                Object.entries(usage.estimated_cost_by_currency).map(
                  ([k, v]) => (
                    <p key={k}>
                      {k} <strong>{String(v)}</strong>
                    </p>
                  ),
                )
              ) : (
                <p className="placeholder">
                  暂无可完整估算的费用。请先在来源配置已核对的价格；订阅请求不估作
                  API 账单。
                </p>
              )}
              {Object.entries(
                usage.partial_estimated_cost_by_currency || {},
              ).map(([k, v]) => (
                <p key={k}>
                  已知部分估算 {k} {String(v)} · 总费用仍未知
                </p>
              ))}
              <p>
                {usage.cost_unknown_requests} 条请求费用未知 ·{" "}
                {usage.usage_partial_requests} 条用量不完整 · 包含{" "}
                {usage.admin_test_requests} 条管理测试
              </p>
            </section>
            <section className="panel">
              <h2>统计口径</h2>
              <p>{usage.notice}</p>
              <p>
                缓存与推理 token
                不重复叠加；金额按币种分别累计，使用请求发生时的价格快照。来源原生额度目前保持未知。
              </p>
              <form
                data-draft-ignore
                className="inline-form"
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  run(async () => {
                    const q = new URLSearchParams();
                    for (const k of ["from", "to"]) {
                      const value = String(f.get(k) || "");
                      if (value) q.set(k, new Date(value).toISOString());
                    }
                    if (f.get("source"))
                      q.set("source_id", String(f.get("source")));
                    setUsage(await api("usage?" + q));
                  });
                }}
              >
                <input
                  aria-label="起始时间"
                  type="datetime-local"
                  name="from"
                />
                <input aria-label="结束时间" type="datetime-local" name="to" />
                <select name="source" aria-label="用量来源">
                  <option value="">全部来源</option>
                  {sources.map((s) => (
                    <option value={s.id} key={s.id}>
                      {s.name}
                    </option>
                  ))}
                </select>
                <button className="secondary">筛选</button>
              </form>
            </section>
          </>
        )}
        {operationPage === "设置" && (
          <>
            <section className="panel">
              <h2>本机运行</h2>
              <dl className="settings-list">
                <div>
                  <dt>版本</dt>
                  <dd>{status.version}</dd>
                </div>
                <div>
                  <dt>构建</dt>
                  <dd title={status.build_id}>{status.build_id?.slice(0, 12) || "未标记"}</dd>
                </div>
                <div>
                  <dt>监听地址</dt>
                  <dd>{status.listen}</dd>
                </div>
                <div>
                  <dt>数据目录</dt>
                  <dd>
                    <code>{status.data_dir}</code>
                  </dd>
                </div>
                <div>
                  <dt>存储状态</dt>
                  <dd>
                    {status.storage_healthy ? "可写" : "异常，已停止接纳调用"}
                  </dd>
                </div>
              </dl>
              <form
                className="inline-form"
                data-dirty={!!settingsEdits.retention} onInput={()=>beginSettingsEdit("retention")}
                onSubmit={(e) => {
                  e.preventDefault();
                  const days=Number(new FormData(e.currentTarget).get("days"));
                  void run(()=>saveSettingsEdit("retention",{retention_days:days}),"settings-retention");
                }}
              >
                <label>
                  请求与续接绑定保留天数
                  <input
                    name="days"
                    type="number"
                    min="1"
                    max="365"
                    disabled={pendingActions.includes("settings-retention")}
                    defaultValue={settings.retention_days}
                    key={settingsEdits.retention?.formVersion??settings.version}
                  />
                </label>
                <button className="secondary" disabled={pendingActions.includes("settings-retention")}>保存</button>
              </form>
              {settingsEdits.retention&&(settingsEdits.retention.version!==settings.version||settingsEdits.retention.readError)&&<div role="region" aria-label="保留期版本冲突"><p>本地输入已保留。原保留期 {settingsEdits.retention.base.retention_days} 天；当前保留期 {settings.retention_days} 天，当前清理预览 {settings.retention_preview?.days??"无"} 天。</p>{settingsEdits.retention.readError&&<p role="alert">{settingsEdits.retention.readError}</p>}<button disabled={pendingActions.includes("settings-retention")||!!settingsEdits.retention.readError} onClick={()=>setSettingsEdits(old=>({...old,retention:{...old.retention!,version:settings.version,base:settings}}))}>使用当前版本，保留保留期输入</button><button disabled={pendingActions.includes("settings-retention")} onClick={()=>setSettingsEdits(old=>{const next={...old};delete next.retention;return next})}>放弃保留期修改</button></div>}
              {settings.retention_preview && <div><p>可清理 {settings.retention_preview.candidate_requests} 条旧请求；保护 {settings.retention_preview.protected_requests} 条运行、当前周期或待核对账务记录。</p><button className="danger" disabled={busy} onClick={()=>run(async()=>{await api("retention-cleanup","POST",{version:settings.version,days:settings.retention_preview.days});await refresh();setNotice("清理任务已提交，可在运维中查看结果。")})}>确认应用保留期并清理</button></div>}
              <p className="hint">
                缩短保留期会清理旧记录，对应 response ID
                将不能继续经此网关续接。
              </p>
            </section>
            <section className="panel"><h2>新请求的运行限制</h2><form data-dirty={!!settingsEdits.limits} onInput={()=>beginSettingsEdit("limits")} onSubmit={e=>{e.preventDefault();const f=new FormData(e.currentTarget);void run(()=>saveSettingsEdit("limits",{max_concurrent:Number(f.get("max_concurrent")),idle_timeout_seconds:Number(f.get("idle")),total_timeout_seconds:Number(f.get("total"))}),"settings-limits")}}><label>全局并发<input name="max_concurrent" type="number" min="1" disabled={pendingActions.includes("settings-limits")} defaultValue={settings.limits?.max_concurrent} key={settingsEdits.limits?.formVersion??settings.version}/></label><label>流空闲超时（秒）<input name="idle" type="number" min="1" disabled={pendingActions.includes("settings-limits")} defaultValue={settings.limits?.idle_timeout_seconds} key={"idle"+(settingsEdits.limits?.formVersion??settings.version)}/></label><label>请求总超时（秒）<input name="total" type="number" min="1" disabled={pendingActions.includes("settings-limits")} defaultValue={settings.limits?.total_timeout_seconds} key={"total"+(settingsEdits.limits?.formVersion??settings.version)}/></label><button disabled={pendingActions.includes("settings-limits")}>保存运行限制</button></form>{settingsEdits.limits&&(settingsEdits.limits.version!==settings.version||settingsEdits.limits.readError)&&<div role="region" aria-label="运行限制版本冲突"><p>本地输入已保留。原并发 {settingsEdits.limits.base.limits?.max_concurrent}、空闲/总超时 {settingsEdits.limits.base.limits?.idle_timeout_seconds}/{settingsEdits.limits.base.limits?.total_timeout_seconds}；当前并发 {settings.limits?.max_concurrent}、空闲/总超时 {settings.limits?.idle_timeout_seconds}/{settings.limits?.total_timeout_seconds}。</p>{settingsEdits.limits.readError&&<p role="alert">{settingsEdits.limits.readError}</p>}<button disabled={pendingActions.includes("settings-limits")||!!settingsEdits.limits.readError} onClick={()=>setSettingsEdits(old=>({...old,limits:{...old.limits!,version:settings.version,base:settings}}))}>使用当前版本，保留运行限制输入</button><button disabled={pendingActions.includes("settings-limits")} onClick={()=>setSettingsEdits(old=>{const next={...old};delete next.limits;return next})}>放弃运行限制修改</button></div>}<p>待重启设置：{JSON.stringify(settings.restart_required||{})}</p></section>
            <section className="panel">
              <h2>诊断与备份</h2>
              <p>
                诊断包仅包含最近 100
                条请求的编号、状态、阶段、耗时和用量完整性。不包含密钥、账号、设备码、地址、提示词和输出。
              </p>
              <button
                className="secondary"
                onClick={() =>
                  run(async () => {
                    const v = await api("diagnostics/export", "POST", {});
                    const u = URL.createObjectURL(
                      new Blob([JSON.stringify(v, null, 2)], {
                        type: "application/json",
                      }),
                    );
                    const a = document.createElement("a");
                    a.href = u;
                    a.download = "gatt-diagnostics.json";
                    a.click();
                    URL.revokeObjectURL(u);
                  })
                }
              >
                导出诊断包 ↓
              </button>
              <h3>备份与恢复</h3>
              <p>{settings.backup}</p>
              <p className="hint">
                监听端口和数据目录保存为待重启设置；请求限制保存后用于新准入。来源额度、真实订阅调用和干净机器安装尚待实测。
              </p>
            </section>
          </>
        )}
      </div>}
      </dialog>

          <dialog
            ref={sourceDialog}
            className="modal"
            aria-label="来源编辑"
            onCancel={event=>{event.preventDefault();closeSource()}}
          >
            <div className="section-title">
              <h2>{editing ? "编辑来源" : "添加模型来源"}</h2>
              <button className="text" onClick={closeSource}>
                关闭 ×
              </button>
            </div>
            <form
              onChangeCapture={()=>{sourceDirty.current=true}}
              aria-describedby={error?"source-form-error":undefined}
              onSubmit={(e) => {
                e.preventDefault();
                run(saveSource);
              }}
            >
              <label>
                来源名称
                <input
                  value={form.name}
                  name="name"
                  required
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="给它起个容易识别的名字"
                />
              </label>
              <label>
                认证方式
                <select
                  disabled={!!editing}
                  name="kind"
                  value={form.kind}
                  onChange={(e) => {const kind=e.target.value;setForm({...form,kind,provider:kind==="none"?"local":kind==="aws_profile"?"bedrock":kind==="google_adc"||kind==="service_account"?"vertex":"openai_compatible",native_protocol:kind==="google_adc"||kind==="service_account"?"gemini":"responses",cloud_config:kind==="google_adc"?{vertex_credentials_mode:"adc"}:kind==="service_account"?{vertex_credentials_mode:"service_account"}:{}})}}
                >
                  <option value="api_key">API Key</option><option value="none">明确无认证的本机服务</option><option value="aws_profile">AWS Bedrock · 选定 profile</option><option value="google_adc">Vertex · Google ADC</option><option value="service_account">Vertex · 服务账号 JSON</option>
                  <option value="codex_subscription">
                    Codex 订阅 · 独立授权实验
                  </option>
                </select>
              </label>
              {form.kind !== "codex_subscription" && (
                <>
                  <label>原生协议<select name="native_protocol" value={form.native_protocol} onChange={(e) => setForm({ ...form, native_protocol: e.target.value })}><option value="responses">Responses</option><option value="chat_completions">Chat Completions</option><option value="messages">Messages</option><option value="gemini">Gemini 原生</option><option value="realtime_websocket">Realtime WebSocket</option></select></label><label>Provider<input name="provider" value={form.provider} onChange={e=>setForm({...form,provider:e.target.value})}/></label>
                  <CloudProviderSettings provider={form.provider} value={form.cloud_config} onChange={cloud_config=>setForm({...form,cloud_config})}/>
                  {!["bedrock","vertex"].includes(form.provider)&&<label>
                    API 基础地址
                    <input
                      type="url"
                      name="base_url"
                      aria-invalid={errorField==="base_url"||undefined}
                      aria-describedby={errorField==="base_url"?"source-form-error":undefined}
                      required
                      value={form.base_url}
                      onChange={(e) =>
                        setForm({ ...form, base_url: e.target.value })
                      }
                      placeholder="https://你的服务商/v1"
                    />
                  </label>}
                  {["api_key","service_account"].includes(form.kind) && (
                  <label>
                    {form.kind==="service_account"?"服务账号 JSON（私有凭据保存）":editing ? "替换凭据（留空保留当前凭据）" : "来源 API Key"}
                    <input
                      type="password"
                      name="credential"
                      value={form.credential}
                      autoComplete="new-password"
                      onChange={(e) =>
                        setForm({ ...form, credential: e.target.value })
                      }
                    />
                  </label>)}
                </>
              )}
              {form.kind === "codex_subscription" && <label><input type="checkbox" checked={form.allow_parameter_adjustment} onChange={(e) => setForm({ ...form, allow_parameter_adjustment: e.target.checked })}/>启用订阅兼容调用（Chat/Messages 的输出 token 上限不会强制执行）</label>}
              <label>复用账号 ID（可选）<input name="account_id" value={form.account_id} onChange={e=>setForm({...form,account_id:e.target.value})} placeholder="从账号卡复制；留空创建独立账号"/></label>
              <label>网络代理<select value={form.proxy_url===null?"inherit":form.proxy_url===""?"direct":"custom"} onChange={e=>setForm({...form,proxy_url:e.target.value==="inherit"?null:e.target.value==="direct"?"":"http://127.0.0.1:"})}><option value="inherit">继承进程环境</option><option value="direct">直连</option><option value="custom">指定代理</option></select></label>
              <label>代理 URL<input name="proxy_url" value={form.proxy_url||""} onChange={e=>setForm({...form,proxy_url:e.target.value})} placeholder="http:// 或 socks5://（无凭据）"/></label>
              <label>
                模型标识
                <input
                  value={form.models}
                  name="models"
                  onChange={(e) => setForm({ ...form, models: e.target.value })}
                  placeholder="实际模型 ID，多个用英文逗号分隔"
                />
              </label>
              {form.kind === "codex_subscription" && <p className="hint">模型可以留空。先登录 ChatGPT，再到模型页读取该账号的可用模型目录。</p>}
              <p className="hint">
                凭据保存在 Cove 私有目录。更换地址或凭据会使旧会话绑定失效；跨站更换地址必须提供新目标凭据。
              </p>
              {editing&&(sourceConflict||sourceConflictReadError)&&<div role="region" aria-label="来源版本冲突"><p>本地输入已保留。原版本 {editing.version}；当前版本 {sourceConflict?.version??"未读取"}。</p>{sourceConflict&&<><p>原名称 {editing.name}；当前名称 {sourceConflict.name}。</p><p>原地址 {editing.base_url}；当前地址 {sourceConflict.base_url}。</p><p>原模型 {editing.models.join(", ")}；当前模型 {sourceConflict.models.join(", ")}。</p><pre>{JSON.stringify({原配置:{kind:editing.kind,provider:editing.provider,native_protocol:editing.native_protocol,account_id:editing.account_id,proxy_url:editing.proxy_url,cloud_config:editing.cloud_config,allow_parameter_adjustment:editing.allow_parameter_adjustment},当前配置:{kind:sourceConflict.kind,provider:sourceConflict.provider,native_protocol:sourceConflict.native_protocol,account_id:sourceConflict.account_id,proxy_url:sourceConflict.proxy_url,cloud_config:sourceConflict.cloud_config,allow_parameter_adjustment:sourceConflict.allow_parameter_adjustment}},null,2)}</pre><p>当前凭据：{sourceConflict.credential_configured?"已配置":"未配置"}；绑定代次 {sourceConflict.binding_generation}。凭据内容不进入差异。</p></>}{sourceConflictReadError&&<p role="alert">{sourceConflictReadError}</p>}<button type="button" disabled={busy||!sourceConflict||!!sourceConflictReadError} onClick={()=>{setEditing(sourceConflict);setSourceConflict(null)}}>使用当前版本，保留来源输入</button><button type="button" disabled={busy||!sourceConflict||!!sourceConflictReadError} onClick={()=>{if(sourceConflict)edit(sourceConflict)}}>放弃来源修改</button>{sourceConflictReadError&&<button type="button" disabled={busy} onClick={()=>void run(async()=>{const latest=await api<Source>(`sources/${editing.id}`);setSourceConflict(latest);setSourceConflictReadError("")})}>重新读取当前来源</button>}</div>}
              {error && <div id="source-form-error" className="error" role="alert">{error}</div>}
              <button disabled={busy}>{busy ? "正在保存…" : "保存来源"}</button>
            </form>
          </dialog>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
