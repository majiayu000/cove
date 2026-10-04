import React, {useEffect,useRef,useState} from "react";
type API=<T=any>(path:string,method?:string,body?:unknown)=>Promise<T>;
export function ResourceConsole({api}:{api:API}) {
 const [data,setData]=useState<any>({resources:[],jobs:[]}),[error,setError]=useState(""),[pending,setPending]=useState<string[]>([]),[jobErrors,setJobErrors]=useState<Record<string,string>>({});
 const [loading,setLoading]=useState(true);
 const mounted=useRef(true),revision=useRef(0),running=useRef(new Set<string>());
 async function load(){
  const sequence=++revision.current;
  setLoading(true);
  try{const value=await api("resource-status");if(mounted.current&&sequence===revision.current){setData(value);setError("")}}
  catch(e){if(mounted.current&&sequence===revision.current)setError((e as Error).message);throw e}
  finally{if(mounted.current&&sequence===revision.current)setLoading(false)}
 }
 useEffect(()=>{mounted.current=true;const refresh=()=>{void load().catch(()=>{});};refresh();const timer=setInterval(refresh,5000);return()=>{mounted.current=false;revision.current++;clearInterval(timer)}},[api]);
 async function cancel(job:any,trigger:HTMLButtonElement){
  if(running.current.has(job.id))return;
  running.current.add(job.id);setPending([...running.current]);setJobErrors(old=>{const next={...old};delete next[job.id];return next});
  try{
   await api(`jobs/${job.id}/cancel`,"POST",{version:job.version});
   if(mounted.current&&document.activeElement===trigger)document.getElementById(`job-${job.id}`)?.focus();
   await load();
  }catch(e){if(mounted.current)setJobErrors(old=>({...old,[job.id]:(e as Error).message}))}
  finally{running.current.delete(job.id);if(mounted.current)setPending([...running.current])}
 }
 return <section className="panel"><h2>文件与后台任务</h2><p>显示最近 200 项本地归属和任务状态。重启只查询已有任务；未获得最终用量的任务保留待对账。</p>{loading&&<p role="status">正在读取文件与任务…</p>}{error&&<><p role="alert" className="error">{error}</p><button disabled={loading} onClick={()=>void load().catch(()=>{})}>重新读取文件与任务</button></>}
 {!loading&&!error&&!data.jobs.length&&<p>暂无后台或批处理任务。</p>}{data.jobs.map((j:any)=><div className="tool-row" key={j.id} id={`job-${j.id}`} tabIndex={-1} aria-label={`${j.kind} 任务 ${j.id}`}><div><strong>{j.kind} · {j.state}</strong><code>{j.id}</code><span>{j.settled?"已结算":"待结算"}{j.cancel_requested?" · 已请求取消":""}</span>{jobErrors[j.id]&&<p className="error" role="alert">{jobErrors[j.id]}</p>}</div>{!j.cancel_requested&&!j.settled&&<button disabled={pending.includes(j.id)} onClick={event=>void cancel(j,event.currentTarget)}>请求取消</button>}</div>)}
 <details><summary>文件及资源（{data.resources.length}）</summary>{data.resources.map((r:any)=><div className="tool-row" key={r.id}><div><strong>{r.filename||r.kind}</strong><code>{r.id}</code><span>{r.purpose} · {r.state} · {r.bytes_known?`${r.bytes} 字节`:"大小未知"}</span></div></div>)}</details></section>;
}
