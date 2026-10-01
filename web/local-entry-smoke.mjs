// Isolated UI acceptance: temporary data and a synthetic loopback upstream.
import { chromium, expect } from "@playwright/test";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { createServer } from "node:net";
import { createServer as createHTTPServer } from "node:http";
import { spawn } from "node:child_process";
import { once } from "node:events";

const root = resolve(import.meta.dirname, "..");
const temp = mkdtempSync(join(tmpdir(), "cove-local-ui-"));
const launcher = join(temp, "launchers");
mkdirSync(launcher);
writeFileSync(join(launcher, "open"), "#!/bin/sh\nexit 0\n", { mode: 0o700 });
const env = { ...process.env, PATH: `${launcher}:${process.env.PATH}` };
const reservation = createServer();
reservation.listen(0, "127.0.0.1");
await once(reservation, "listening");
const port = reservation.address().port;
await new Promise(resolve => reservation.close(resolve));
const config = { ...JSON.parse(readFileSync(join(root, "config.example.json"))), listen: `127.0.0.1:${port}`, data_dir: join(temp, "data") };
const configPath = join(temp, "config.json");
writeFileSync(configPath, JSON.stringify(config));
const base = `http://${config.listen}`;
const binary = join(root, "bin/gatt");
const build = JSON.parse(readFileSync(join(root, "bin/build-evidence.json")));
let server;
let browser;
let zoomContext;
const errors = [];
const results = {};
let syntheticCalls=0;
const upstream=createHTTPServer(async(req,res)=>{
  if(req.headers.authorization!=="Bearer synthetic-upstream-secret"){res.writeHead(401);res.end();return}
  const chunks=[];for await(const chunk of req)chunks.push(chunk);
  const input=JSON.parse(Buffer.concat(chunks).toString());
  syntheticCalls++;
  const response={id:`synthetic-ui-${syntheticCalls}`,object:"response",status:"completed",model:"synthetic-model",output:[{id:"synthetic-message",type:"message",role:"assistant",status:"completed",content:[{type:"output_text",text:"OK",annotations:[]}]}],usage:{input_tokens:3,output_tokens:2,total_tokens:5}};
  res.setHeader("Content-Type",input.stream?"text/event-stream":"application/json");
  if(input.stream)res.end(`event: response.completed\ndata: ${JSON.stringify({type:"response.completed",response})}\n\n`);
  else res.end(JSON.stringify(response));
});
upstream.listen(0,"127.0.0.1");
await once(upstream,"listening");
const upstreamURL=`http://127.0.0.1:${upstream.address().port}/v1`;

async function launch() {
  server = spawn(binary, ["-config", configPath, "serve"], { env, stdio: "ignore" });
  for (let i = 0; i < 100; i++) {
    if (server.exitCode !== null) throw new Error("Isolated server exited");
    try { if ((await fetch(base + "/readyz")).ok) return; } catch {}
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("Isolated server startup timed out");
}
async function stop() {
  if (!server || server.exitCode !== null) return;
  const exited = once(server, "exit");
  server.kill("SIGTERM");
  await exited;
}
async function api(path, method = "GET", body, session) {
  const response = await fetch(base + "/admin/" + path, {
    method, redirect: "error",
    headers: { Origin: base, "Content-Type": "application/json", ...(session ? { Authorization: `Bearer ${session}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { status: response.status, body: await response.json() };
}

try {
  await launch();
  browser = await chromium.launch({ headless: true, channel: "chrome" });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(base);
  await expect(page.locator("nav")).toBeVisible();
  await expect(page.locator('input[type="password"]:visible')).toHaveCount(0);
  await expect(page.getByText("设置管理密码", { exact: true })).toHaveCount(0);
  expect(await context.cookies()).toHaveLength(0);
  const session = await page.evaluate(() => sessionStorage.getItem("cove.management"));
  expect((await fetch(base + "/healthz")).status).toBe(200);
  expect((await api("status", "GET", undefined, session)).body.build_id).toBe(build.source_id);
  await page.locator("nav").getByRole("button", { name: "设置" }).click();
  await expect(page.locator(`[title="${build.source_id}"]`)).toBeVisible();
  results.build_id = build.source_id;
  for (const section of ["来源", "模型", "API Keys", "路由", "工具", "请求", "用量", "预算", "运维", "设置"]) {
    await page.locator("nav").getByRole("button", { name: section }).click();
    await expect(page.locator("nav").getByRole("button", { name: "API Keys" })).toBeVisible();
  }
  results.persistent_api_keys_navigation = true;
  await page.locator("nav").getByRole("button", { name: "来源" }).click();
  expect((await api("password", "POST", { password: "unused" }, session)).status).toBe(404);
  results.direct_entry_without_password = true;
  const addSource=page.getByRole("button",{name:"+ 添加来源",exact:true});
  await addSource.click();
  await expect(page.getByRole("dialog",{name:"来源编辑"})).toBeVisible();
  for(let i=0;i<20;i++){
    await page.keyboard.press(i%2?"Tab":"Shift+Tab");
    expect(await page.evaluate(()=>document.activeElement?.closest('dialog')?.getAttribute('aria-label'))).toBe("来源编辑");
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog",{name:"来源编辑"})).not.toBeVisible();
  await expect(addSource).toBeFocused();
  results.source_dialog_keyboard_focus_escape_ui=true;
  await addSource.click();
  const sourceDialog=page.getByRole("dialog",{name:"来源编辑"});
  await sourceDialog.getByLabel("来源名称",{exact:true}).fill("Isolated source");
  await sourceDialog.getByLabel("API 基础地址",{exact:true}).fill(upstreamURL);
  await sourceDialog.getByLabel("来源 API Key",{exact:true}).fill("synthetic-upstream-secret");
  await sourceDialog.getByLabel("模型标识",{exact:true}).fill("synthetic-model");
  await sourceDialog.getByRole("button",{name:"保存来源",exact:true}).click();
  await expect(sourceDialog).not.toBeVisible();
  await expect(page.getByRole("heading",{name:"Isolated source",level:2,exact:true})).toBeVisible();
  const source={body:(await api("sources","GET",undefined,session)).body.find(s=>s.name==="Isolated source")};
  expect(source.body.credential_configured).toBe(true);
  const account={body:(await api("accounts","GET",undefined,session)).body.items.find(a=>a.id===source.body.account_id)};
  results.source_create_account_credential_ui=true;
  for(const name of ["Renamed source","Isolated source"]){
    const card=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:name==="Renamed source"?"Isolated source":"Renamed source",level:2,exact:true})});
    await card.getByRole("button",{name:"编辑",exact:true}).click();
    await sourceDialog.getByLabel("来源名称",{exact:true}).fill(name);
    await sourceDialog.getByRole("button",{name:"保存来源",exact:true}).click();
    await expect(sourceDialog).not.toBeVisible();
    await expect(page.getByRole("heading",{name,level:2,exact:true})).toBeVisible();
  }
  results.source_edit_version_ui=true;
  const sourceCardForCAS=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"Isolated source",level:2,exact:true})});
  await sourceCardForCAS.getByRole("button",{name:"编辑",exact:true}).click();
  await sourceDialog.getByLabel("来源名称",{exact:true}).fill("Local source draft");
  let currentSourceForCAS=(await api(`sources/${source.body.id}`,"GET",undefined,session)).body;
  expect((await api(`sources/${source.body.id}`,"PATCH",{version:currentSourceForCAS.version,name:"External source edit"},session)).status).toBe(200);
  await sourceDialog.getByRole("button",{name:"保存来源",exact:true}).click();
  await expect(sourceDialog.getByRole("region",{name:"来源版本冲突",exact:true})).toContainText("External source edit");
  await expect(sourceDialog.getByLabel("来源名称",{exact:true})).toHaveValue("Local source draft");
  expect((await api(`sources/${source.body.id}`,"GET",undefined,session)).body.name).toBe("External source edit");
  await sourceDialog.getByRole("button",{name:"使用当前版本，保留来源输入",exact:true}).click();
  await sourceDialog.getByRole("button",{name:"保存来源",exact:true}).click();
  await expect(sourceDialog).not.toBeVisible();
  await page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"Local source draft",level:2,exact:true})}).getByRole("button",{name:"编辑",exact:true}).click();
  await sourceDialog.getByLabel("来源名称",{exact:true}).fill("Discard source draft");
  currentSourceForCAS=(await api(`sources/${source.body.id}`,"GET",undefined,session)).body;
  expect((await api(`sources/${source.body.id}`,"PATCH",{version:currentSourceForCAS.version,name:"Isolated source"},session)).status).toBe(200);
  await sourceDialog.getByRole("button",{name:"保存来源",exact:true}).click();
  await expect(sourceDialog.getByRole("region",{name:"来源版本冲突",exact:true})).toContainText("Isolated source");
  await sourceDialog.getByRole("button",{name:"放弃来源修改",exact:true}).click();
  await expect(sourceDialog.getByLabel("来源名称",{exact:true})).toHaveValue("Isolated source");
  await sourceDialog.getByRole("button",{name:"关闭 ×",exact:true}).click();
  // Refresh only public metadata; the external edit restored the fixture name.
  await page.locator("nav").getByRole("button",{name:"设置"}).click();
  await page.getByRole("button",{name:"刷新数据 ↻",exact:true}).click();
  await expect(page.getByRole("button",{name:"刷新数据 ↻",exact:true})).toBeEnabled();
  await page.locator("nav").getByRole("button",{name:"来源"}).click();
  await expect(page.getByRole("heading",{name:"Isolated source",level:2,exact:true})).toBeVisible();
  results.source_dirty_cas_explicit_resubmit_discard_ui=true;
  const key = await api("client-keys", "POST", { name: "Isolated client", source_id: source.body.id }, session);
  expect(key.status).toBe(201);
  results.setup_and_business_data = true;
  await page.reload();
  await expect(page.getByRole("heading",{name:"Isolated source",level:2,exact:true})).toBeVisible();

  const otherSource = await api("sources", "POST", { name: "Independent source", account_id:account.body.id, base_url:"https://example.invalid/v1", models:["other-model"] }, session);
  expect(otherSource.status).toBe(201);
  const otherAccount=await api("accounts","POST",{provider:"openai_compatible",auth_type:"api_key",name:"Independent account"},session);
  expect(otherAccount.status).toBe(201);
  await page.reload();
  const firstAccountCard=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"Isolated source",level:3,exact:true})});
  const otherAccountCard=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"Independent account",exact:true})});
  await firstAccountCard.getByText("修改名称",{exact:true}).click();
  await otherAccountCard.getByText("修改名称",{exact:true}).click();
  let releaseAccount;
  const delayedAccount=new Promise(resolve=>{releaseAccount=resolve});
  await page.route(`**/admin/accounts/${account.body.id}`,async route=>{await delayedAccount;await route.fulfill({status:503,json:{error:{message:"Synthetic delayed account failure"}}})});
  try {
    await firstAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
    await expect(firstAccountCard.getByRole("button",{name:"保存名称",exact:true})).toBeDisabled();
    await expect(otherAccountCard.getByRole("button",{name:"保存名称",exact:true})).toBeEnabled();
    await otherAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
    await expect.poll(async()=>(await api("accounts","GET",undefined,session)).body.items.find(a=>a.id===otherAccount.body.id).version).toBeGreaterThan(otherAccount.body.version);
    await expect(firstAccountCard.getByRole("button",{name:"保存名称",exact:true})).toBeDisabled();
    releaseAccount();
    await expect(page.getByRole("alert")).toContainText("Synthetic delayed account failure");
    results.independent_account_action_ui=true;
  } finally {releaseAccount()}
  await page.unroute(`**/admin/accounts/${account.body.id}`);
  const dirtyAccountCard=page.locator("article.source-card").filter({has:page.locator("code").filter({hasText:account.body.id})});
  const dirtyAccountName=dirtyAccountCard.getByLabel("账号名称",{exact:true});
  const accountBeforeEdit=(await api(`accounts/${account.body.id}`,"GET",undefined,session)).body;
  await dirtyAccountName.fill("Local account draft");
  expect((await api(`accounts/${account.body.id}`,"PATCH",{version:accountBeforeEdit.version,name:"Remote account name"},session)).status).toBe(200);
  if(!await otherAccountCard.getByRole("button",{name:"保存名称",exact:true}).isVisible())await otherAccountCard.getByText("修改名称",{exact:true}).click();
  await otherAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
  await expect(dirtyAccountCard.getByRole("heading",{name:"Remote account name",exact:true})).toBeVisible();
  await expect(dirtyAccountName).toHaveValue("Local account draft");
  await dirtyAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
  await expect(dirtyAccountCard.getByRole("region",{name:"账号名称版本冲突"})).toContainText("Remote account name");
  expect((await api(`accounts/${account.body.id}`,"GET",undefined,session)).body.name).toBe("Remote account name");
  await dirtyAccountCard.getByRole("button",{name:"使用当前版本，保留名称输入",exact:true}).click();
  await dirtyAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
  await expect(dirtyAccountCard.getByRole("heading",{name:"Local account draft",exact:true})).toBeVisible();
  await dirtyAccountName.fill("Discarded name");
  const savedAccount=(await api(`accounts/${account.body.id}`,"GET",undefined,session)).body;
  expect((await api(`accounts/${account.body.id}`,"PATCH",{version:savedAccount.version,name:"Current account name"},session)).status).toBe(200);
  await dirtyAccountCard.getByRole("button",{name:"保存名称",exact:true}).click();
  await expect(dirtyAccountCard.getByRole("region",{name:"账号名称版本冲突"})).toContainText("Current account name");
  await dirtyAccountCard.getByRole("button",{name:"放弃名称修改",exact:true}).click();
  await expect(dirtyAccountName).toHaveValue("Current account name");
  await dirtyAccountCard.getByText("替换凭据",{exact:true}).click();
  const credentialInput=dirtyAccountCard.getByLabel("账号API凭据",{exact:true});
  await credentialInput.fill("synthetic-upstream-secret");
  const credentialBase=(await api(`accounts/${account.body.id}`,"GET",undefined,session)).body;
  expect((await api(`accounts/${account.body.id}`,"PATCH",{version:credentialBase.version,name:"Credential concurrent edit"},session)).status).toBe(200);
  await dirtyAccountCard.getByRole("button",{name:"保存凭据",exact:true}).click();
  await expect(dirtyAccountCard.getByRole("region",{name:"账号凭据版本冲突"})).toContainText("凭据输入已保留");
  await expect(credentialInput).toHaveValue("synthetic-upstream-secret");
  expect((await api(`accounts/${account.body.id}`,"GET",undefined,session)).body.generation).toBe(credentialBase.generation);
  await dirtyAccountCard.getByRole("button",{name:"使用当前版本，保留凭据输入",exact:true}).click();
  await dirtyAccountCard.getByRole("button",{name:"保存凭据",exact:true}).click();
  await expect.poll(async()=>(await api(`accounts/${account.body.id}`,"GET",undefined,session)).body.generation).toBe(credentialBase.generation+1);
  await expect(credentialInput).toHaveValue("");
  const restoredAccount=(await api(`accounts/${account.body.id}`,"GET",undefined,session)).body;
  expect((await api(`accounts/${account.body.id}`,"PATCH",{version:restoredAccount.version,name:"Isolated source"},session)).status).toBe(200);
  await page.reload();
  results.account_dirty_name_and_credential_cas_explicit_resubmit_ui=true;
  const firstCard = page.locator("article.source-card").filter({has:page.getByRole("heading", {name:"Isolated source",level:2,exact:true})});
  const otherCard = page.locator("article.source-card").filter({has:page.getByRole("heading", {name:"Independent source",level:2,exact:true})});
  let releaseTest;
  const delayedTest = new Promise(resolve => { releaseTest = resolve; });
  await page.route(`**/admin/sources/${source.body.id}/test`, async route => {
    await delayedTest;
    await route.fulfill({json:{request:{id:"synthetic-delayed-test",status:"succeeded"}}});
  });
  try {
    await firstCard.getByRole("button", {name:"测试调用",exact:true}).click();
    await expect(firstCard.getByRole("button", {name:"测试调用",exact:true})).toBeDisabled();
    await expect(otherCard.getByRole("button", {name:"停用",exact:true})).toBeEnabled();
    await otherCard.getByRole("button", {name:"停用",exact:true}).click();
    await expect(otherCard.getByRole("button", {name:"启用",exact:true})).toBeEnabled();
    await otherCard.getByRole("button", {name:"启用",exact:true}).click();
    await expect(otherCard.getByRole("button", {name:"停用",exact:true})).toBeEnabled();
    await page.locator("nav").getByRole("button", {name:"请求"}).click();
    releaseTest();
    await expect(page.getByRole("status")).toContainText("文本测试成功");
    await expect(page.getByRole("heading", {name:"请求详情",exact:true})).toHaveCount(0);
    results.independent_source_action_and_stale_detail_ui = true;
  } finally { releaseTest(); }
  await page.unroute(`**/admin/sources/${source.body.id}/test`);
  await page.locator("nav").getByRole("button",{name:"模型"}).click();
  let releaseDiscovery;
  const delayedDiscovery=new Promise(resolve=>{releaseDiscovery=resolve});
  await page.route(`**/admin/sources/${source.body.id}/models/discover`,route=>route.fulfill({json:{id:"synthetic-discovery",state:"running"}}));
  await page.route("**/admin/operations/synthetic-discovery",async route=>{await delayedDiscovery;await route.fulfill({json:{id:"synthetic-discovery",state:"succeeded"}})});
  await page.route(`**/admin/sources/${otherSource.body.id}/models/discover`,route=>route.fulfill({json:{id:"synthetic-other-discovery",state:"succeeded"}}));
  const firstModelRow=page.locator(".tool-row").filter({has:page.getByText("Isolated source",{exact:true})});
  const otherModelRow=page.locator(".tool-row").filter({has:page.getByText("Independent source",{exact:true})});
  try {
    await firstModelRow.getByRole("button",{name:"获取模型"}).click();
    await expect(firstModelRow.getByRole("button",{name:"获取模型"})).toBeDisabled();
    await expect(otherModelRow.getByRole("button",{name:"获取模型"})).toBeEnabled();
    await otherModelRow.getByRole("button",{name:"获取模型"}).click();
    await expect(otherModelRow.getByRole("button",{name:"获取模型"})).toBeEnabled();
    await expect(firstModelRow.getByRole("button",{name:"获取模型"})).toBeDisabled();
    releaseDiscovery();
    await expect(firstModelRow.getByRole("button",{name:"获取模型"})).toBeEnabled();
    results.independent_model_discovery_ui=true;
  } finally {releaseDiscovery()}
  await page.unroute(`**/admin/sources/${source.body.id}/models/discover`);
  await page.unroute("**/admin/operations/synthetic-discovery");
  await page.unroute(`**/admin/sources/${otherSource.body.id}/models/discover`);
  await page.locator("nav").getByRole("button",{name:"来源"}).click();
  await otherCard.getByText("来源管理与计量",{exact:true}).click();
  await otherCard.getByRole("button",{name:"删除来源",exact:true}).click();
  await expect(otherCard).toHaveCount(0);
  results.source_delete_ui=true;
  const otherAccountVersion=(await api("accounts","GET",undefined,session)).body.items.find(a=>a.id===otherAccount.body.id).version;
  expect((await api(`accounts/${otherAccount.body.id}`,"DELETE",{version:otherAccountVersion},session)).status).toBe(200);
  await page.reload();

  await firstCard.getByRole("button",{name:"测试调用",exact:true}).click();
  await expect(page.getByRole("status")).toContainText("文本测试成功");
  await expect(page.getByRole("heading",{name:"请求详情",exact:true})).toBeVisible();
  const clientResponse=await fetch(base+"/v1/responses",{method:"POST",headers:{Authorization:`Bearer ${key.body.secret}`,"Content-Type":"application/json"},body:JSON.stringify({model:"synthetic-model",input:"Synthetic acceptance",max_output_tokens:16})});
  expect(clientResponse.status).toBe(200);await clientResponse.json();
  const requestID=clientResponse.headers.get("X-Gateway-Request-Id");
  await page.locator("nav").getByRole("button",{name:"请求"}).click();
  await page.getByRole("button",{name:"刷新数据 ↻",exact:true}).click();
  await page.getByLabel("筛选客户端",{exact:true}).selectOption(key.body.key.id);
  await page.getByRole("button",{name:"筛选",exact:true}).click();
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.getByRole("button",{name:"详情 ↗",exact:true}).click();
  await expect(page.locator(".detail code").filter({hasText:requestID})).toBeVisible();
  const recorded=(await api(`requests/${requestID}`,"GET",undefined,session)).body.request;
  expect(recorded.status).toBe("succeeded");expect(recorded.usage.input_tokens).toBe(3);expect(recorded.usage.output_tokens).toBe(2);
  results.source_call_request_filter_detail_usage_ui=true;

  await page.locator("nav").getByRole("button",{name:"设置"}).click();
  const dirtyLimitsPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"新请求的运行限制",exact:true})});
  const concurrentInput=dirtyLimitsPanel.getByLabel("全局并发",{exact:true});
  await concurrentInput.fill("4");
  let externalSettings=(await api("settings","GET",undefined,session)).body;
  expect((await api("settings","PATCH",{version:externalSettings.version,changes:{max_concurrent:5}},session)).status).toBe(200);
  await page.getByRole("button",{name:"刷新数据 ↻",exact:true}).click();
  await expect(page.getByRole("button",{name:"刷新数据 ↻",exact:true})).toBeEnabled();
  await expect(concurrentInput).toHaveValue("4");
  await dirtyLimitsPanel.getByRole("button",{name:"保存运行限制",exact:true}).click();
  await expect(dirtyLimitsPanel.getByRole("region",{name:"运行限制版本冲突",exact:true})).toContainText("当前并发 5");
  expect((await api("settings","GET",undefined,session)).body.limits.max_concurrent).toBe(5);
  await dirtyLimitsPanel.getByRole("button",{name:"使用当前版本，保留运行限制输入",exact:true}).click();
  await dirtyLimitsPanel.getByRole("button",{name:"保存运行限制",exact:true}).click();
  await expect.poll(async()=>(await api("settings","GET",undefined,session)).body.limits.max_concurrent).toBe(4);
  await concurrentInput.fill("7");externalSettings=(await api("settings","GET",undefined,session)).body;
  expect((await api("settings","PATCH",{version:externalSettings.version,changes:{max_concurrent:6}},session)).status).toBe(200);
  await dirtyLimitsPanel.getByRole("button",{name:"保存运行限制",exact:true}).click();
  await expect(dirtyLimitsPanel.getByRole("region",{name:"运行限制版本冲突",exact:true})).toContainText("当前并发 6");
  await dirtyLimitsPanel.getByRole("button",{name:"放弃运行限制修改",exact:true}).click();
  await expect(concurrentInput).toHaveValue("6");
  results.settings_limits_dirty_cas_explicit_resubmit_discard_ui=true;
  const dirtyRuntimePanel=page.locator("section").filter({has:page.getByRole("heading",{name:"本机运行",exact:true})});
  const retentionInput=dirtyRuntimePanel.getByLabel("请求与续接绑定保留天数",{exact:true});
  await retentionInput.fill("8");externalSettings=(await api("settings","GET",undefined,session)).body;
  expect((await api("settings","PATCH",{version:externalSettings.version,changes:{retention_days:9}},session)).status).toBe(200);
  await page.getByRole("button",{name:"刷新数据 ↻",exact:true}).click();
  await expect(page.getByRole("button",{name:"刷新数据 ↻",exact:true})).toBeEnabled();
  await expect(retentionInput).toHaveValue("8");
  await dirtyRuntimePanel.getByRole("button",{name:"保存",exact:true}).click();
  await expect(dirtyRuntimePanel.getByRole("region",{name:"保留期版本冲突",exact:true})).toContainText("当前清理预览 9 天");
  expect((await api("settings","GET",undefined,session)).body.retention_preview.days).toBe(9);
  await dirtyRuntimePanel.getByRole("button",{name:"使用当前版本，保留保留期输入",exact:true}).click();
  await dirtyRuntimePanel.getByRole("button",{name:"保存",exact:true}).click();
  await expect.poll(async()=>(await api("settings","GET",undefined,session)).body.retention_preview.days).toBe(8);
  results.settings_retention_dirty_cas_explicit_resubmit_ui=true;

  await page.locator("nav").getByRole("button",{name:"工具"}).click();
  const cachePanel=page.locator("section").filter({has:page.getByRole("heading",{name:"本机结果缓存",exact:true})});
  const cacheTTL=cachePanel.getByLabel("有效期（1–60 分钟）",{exact:true});
  await cacheTTL.fill("7");
  let cacheState=(await api("response-cache","GET",undefined,session)).body.settings;
  expect((await api("response-cache","PUT",{...cacheState,ttl_minutes:12,save_output_consent:false},session)).status).toBe(200);
  await cachePanel.getByRole("button",{name:"刷新",exact:true}).click();
  await expect(cachePanel.getByRole("button",{name:"刷新",exact:true})).toBeEnabled();
  await expect(cacheTTL).toHaveValue("7");
  await cachePanel.getByRole("button",{name:"保存设置",exact:true}).click();
  await expect(cachePanel.getByRole("region",{name:"缓存版本冲突",exact:true})).toContainText("12");
  expect((await api("response-cache","GET",undefined,session)).body.settings.ttl_minutes).toBe(12);
  await cachePanel.getByRole("button",{name:"使用当前版本，保留缓存输入",exact:true}).click();
  await cachePanel.getByRole("button",{name:"保存设置",exact:true}).click();
  await expect.poll(async()=>(await api("response-cache","GET",undefined,session)).body.settings.ttl_minutes).toBe(7);
  await cacheTTL.fill("9");cacheState=(await api("response-cache","GET",undefined,session)).body.settings;
  expect((await api("response-cache","PUT",{...cacheState,ttl_minutes:13,save_output_consent:false},session)).status).toBe(200);
  await cachePanel.getByRole("button",{name:"保存设置",exact:true}).click();
  await expect(cachePanel.getByRole("region",{name:"缓存版本冲突",exact:true})).toBeVisible();
  await cachePanel.getByRole("button",{name:"放弃缓存修改",exact:true}).click();
  await expect(cacheTTL).toHaveValue("13");
  results.cache_dirty_refresh_cas_explicit_resubmit_discard_ui=true;

  const cacheValidation=route=>route.request().method()==="PUT"?route.fulfill({status:422,contentType:"application/json",body:JSON.stringify({error:{message:"TTL field fixture",field:"ttl_minutes"}})}):route.continue();
  await page.route("**/admin/response-cache",cacheValidation);
  try{await cachePanel.getByRole("button",{name:"保存设置",exact:true}).click();await expect(cacheTTL).toHaveAttribute("aria-describedby","cache-error");await expect(cachePanel.getByRole("alert")).toContainText("TTL field fixture");await expect(cachePanel.getByRole("button",{name:"保存设置",exact:true})).toBeFocused();await expect(cacheTTL).toHaveValue("13");}
  finally{await page.unroute("**/admin/response-cache",cacheValidation);}
  results.cache_field_error_focus_and_draft_ui=true;

  await page.locator("nav").getByRole("button",{name:"运维",exact:true}).click();
  const notifyPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"提醒与通知",exact:true})});
  const notifyAuth=notifyPanel.getByRole("checkbox",{name:"账号认证",exact:true});
  await expect(notifyAuth).toBeChecked();await notifyAuth.uncheck();
  const notifySecret=notifyPanel.getByLabel("签名凭据",{exact:true});await notifySecret.fill("SYNTHETIC_NOTIFY_DRAFT");
  let notifyState=(await api("notifications","GET",undefined,session)).body.settings;
  expect((await api("notifications","PUT",{version:notifyState.version,enabled:false,url:"",signature:false,event_kinds:["auth","quota"]},session)).status).toBe(200);
  await notifyPanel.getByRole("button",{name:"刷新通知设置",exact:true}).click();
  await expect(notifyPanel.getByRole("button",{name:"刷新通知设置",exact:true})).toBeEnabled();
  await expect(notifyAuth).not.toBeChecked();await expect(notifySecret).toHaveValue("SYNTHETIC_NOTIFY_DRAFT");
  await notifyPanel.getByRole("button",{name:"保存通知设置",exact:true}).click();
  const notifyConflict=notifyPanel.getByRole("region",{name:"通知版本冲突",exact:true});
  await expect(notifyConflict).toBeVisible();await expect(notifyConflict).not.toContainText("SYNTHETIC_NOTIFY_DRAFT");
  expect((await api("notifications","GET",undefined,session)).body.settings.event_kinds).toEqual(["auth","quota"]);
  await notifyPanel.getByRole("button",{name:"使用当前版本，保留通知输入",exact:true}).click();
  await expect(notifySecret).toHaveValue("SYNTHETIC_NOTIFY_DRAFT");
  await notifyPanel.getByRole("button",{name:"保存通知设置",exact:true}).click();
  await expect.poll(async()=>(await api("notifications","GET",undefined,session)).body.settings.event_kinds.includes("auth")).toBe(false);
  await expect(notifySecret).toHaveValue("");
  await notifyPanel.getByRole("checkbox",{name:"已观测额度",exact:true}).uncheck();await notifySecret.fill("SYNTHETIC_NOTIFY_DISCARD");
  notifyState=(await api("notifications","GET",undefined,session)).body.settings;
  expect((await api("notifications","PUT",{version:notifyState.version,enabled:false,url:"",signature:false,event_kinds:["auth"]},session)).status).toBe(200);
  await notifyPanel.getByRole("button",{name:"保存通知设置",exact:true}).click();await expect(notifyConflict).toBeVisible();
  await notifyPanel.getByRole("button",{name:"放弃通知修改",exact:true}).click();
  await expect(notifyAuth).toBeChecked();await expect(notifySecret).toHaveValue("");
  results.notifications_dirty_cas_explicit_resubmit_discard_ui=true;

  const notificationURL=notifyPanel.getByLabel("HTTPS webhook URL",{exact:true});
  await notificationURL.fill("http://127.0.0.1/hook");await notifyPanel.getByRole("button",{name:"保存通知设置",exact:true}).click();
  await expect(notificationURL).toHaveAttribute("aria-describedby","notification-save-error");await expect(notifyPanel.getByRole("alert")).toContainText("HTTPS webhook URL");await expect(notifyPanel.getByRole("button",{name:"保存通知设置",exact:true})).toBeFocused();await expect(notificationURL).toHaveValue("http://127.0.0.1/hook");
  await notificationURL.fill("");
  results.notifications_field_error_focus_and_draft_ui=true;

  let releaseNotificationA;const notificationGate=new Promise(resolve=>releaseNotificationA=resolve);let notificationACalls=0,notificationBCalls=0;
  const notificationFixtures={items:[{id:"notification-ui-a",kind:"auth",summary:"Notification A",state:"active",version:1,count:1},{id:"notification-ui-b",kind:"quota",summary:"Notification B",state:"active",version:1,count:1}]};
  const notificationList=route=>route.fulfill({status:200,contentType:"application/json",body:JSON.stringify(notificationFixtures)});
  const notificationDismissA=async route=>{notificationACalls++;await notificationGate;await route.fulfill({status:200,contentType:"application/json",body:"{}"});};
  const notificationDismissB=async route=>{notificationBCalls++;await route.fulfill({status:200,contentType:"application/json",body:"{}"});};
  await page.route("**/admin/alerts",notificationList);await page.route("**/admin/alerts/notification-ui-a/dismiss",notificationDismissA);await page.route("**/admin/alerts/notification-ui-b/dismiss",notificationDismissB);
  try{
    await notifyPanel.getByRole("button",{name:"刷新提醒与投递记录",exact:true}).click();
    const dismissA=notifyPanel.locator(".tool-row").filter({hasText:"Notification A"}).getByRole("button",{name:"标为已读",exact:true});
    const dismissB=notifyPanel.locator(".tool-row").filter({hasText:"Notification B"}).getByRole("button",{name:"标为已读",exact:true});
    await dismissA.evaluate(button=>{button.click();button.click();});
    await expect.poll(()=>notificationACalls).toBe(1);await expect(dismissA).toBeDisabled();await expect(dismissB).toBeEnabled();
    await expect(notifyPanel.getByRole("button",{name:"保存通知设置",exact:true})).toBeEnabled();
    await dismissB.click();await expect.poll(()=>notificationBCalls).toBe(1);await expect(dismissB).toBeEnabled();
    releaseNotificationA();await expect(dismissA).toBeEnabled();expect(notificationACalls).toBe(1);
    results.notifications_independent_alert_duplicate_guard_ui=true;
  }finally{releaseNotificationA();await page.unroute("**/admin/alerts",notificationList);await page.unroute("**/admin/alerts/notification-ui-a/dismiss",notificationDismissA);await page.unroute("**/admin/alerts/notification-ui-b/dismiss",notificationDismissB);}

  await page.locator("nav").getByRole("button",{name:"设置"}).click();
  const limitsPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"新请求的运行限制",exact:true})});
  await limitsPanel.getByLabel("全局并发",{exact:true}).fill("4");
  await limitsPanel.getByLabel("流空闲超时（秒）",{exact:true}).fill("10");
  await limitsPanel.getByLabel("请求总超时（秒）",{exact:true}).fill("30");
  await limitsPanel.getByRole("button",{name:"保存运行限制",exact:true}).click();
  await expect(page.getByRole("status")).toContainText("新请求将使用新限制");
  expect((await api("settings","GET",undefined,session)).body.limits.max_concurrent).toBe(4);
  const runtimePanel=page.locator("section").filter({has:page.getByRole("heading",{name:"本机运行",exact:true})});
  await runtimePanel.locator('input[name="days"]').fill("8");
  await runtimePanel.getByRole("button",{name:"保存",exact:true}).click();
  await expect(runtimePanel.getByRole("button",{name:"确认应用保留期并清理",exact:true})).toBeVisible();
  expect((await api("settings","GET",undefined,session)).body.retention_preview.days).toBe(8);
  await runtimePanel.getByRole("button",{name:"确认应用保留期并清理",exact:true}).click();
  await expect(page.getByRole("status")).toContainText("清理任务已提交");
  await expect.poll(async()=>(await api("settings","GET",undefined,session)).body.retention_days).toBe(8);
  results.runtime_limits_retention_preview_confirm_ui=true;

  await page.locator("nav").getByRole("button",{name:"路由" }).click();
  const routePanel=page.locator("section").filter({has:page.getByRole("heading",{name:"创建路由",exact:true})});
  await routePanel.getByLabel("名称",{exact:true}).fill("UI route");
  await routePanel.getByRole("checkbox",{name:"Isolated source / synthetic-model"}).check();
  await routePanel.getByRole("button",{name:"创建路由",exact:true}).click();
  await expect(page.getByRole("status")).toHaveText("路由已保存，添加公开模型别名后可绑定 Key。");
  await page.getByLabel("公开模型 ID",{exact:true}).fill("ui-model");
  await page.getByRole("button",{name:"添加别名",exact:true}).click();
  await expect(page.getByText("ui-model",{exact:true})).toBeVisible();
  await page.getByRole("button",{name:"预览选择",exact:true}).click();
  await expect(page.getByText("选择 synthetic-model",{exact:true})).toBeVisible();
  results.route_alias_preview_ui=true;

  await page.locator("nav").getByRole("button",{name:"API Keys"}).click();
  const existingKeyRow=page.locator(".tool-row").filter({has:page.getByText("Isolated client",{exact:true})});
  const secondKey=await api("client-keys","POST",{name:"Independent client",source_id:source.body.id},session);
  expect(secondKey.status).toBe(201);
  await page.reload();
  await page.locator("nav").getByRole("button",{name:"API Keys"}).click();
  const secondKeyRow=page.locator(".tool-row").filter({has:page.getByText("Independent client",{exact:true})});
  let releaseKey,keyPatches=0;
  const delayedKey=new Promise(resolve=>{releaseKey=resolve});
  await page.route(`**/admin/client-keys/${key.body.key.id}`,async route=>{keyPatches++;await delayedKey;await route.fulfill({status:503,json:{error:{message:"Synthetic delayed Key failure"}}})});
  try {
    await existingKeyRow.getByRole("button",{name:"停用",exact:true}).evaluate(button=>{button.click();button.click()});
    await expect(existingKeyRow.getByRole("button",{name:"停用",exact:true})).toBeDisabled();
    await expect(secondKeyRow.getByRole("button",{name:"停用",exact:true})).toBeEnabled();
    await secondKeyRow.getByRole("button",{name:"停用",exact:true}).click();
    await expect(secondKeyRow.getByRole("button",{name:"启用",exact:true})).toBeVisible();
    await expect(existingKeyRow.getByRole("button",{name:"停用",exact:true})).toBeDisabled();
    releaseKey();
    await expect(page.getByRole("alert")).toContainText("Synthetic delayed Key failure");
    expect(keyPatches).toBe(1);
    results.independent_key_action_and_duplicate_guard_ui=true;
  } finally {releaseKey()}
  await page.unroute(`**/admin/client-keys/${key.body.key.id}`);
  await existingKeyRow.locator("summary").click();
  const dirtyPolicy=existingKeyRow.locator("form").first();
  const dirtyRPM=dirtyPolicy.locator('input[name="rpm"]');
  await dirtyRPM.fill("14");
  const dirtyKeyBase=(await api(`client-keys/${key.body.key.id}`,"GET",undefined,session)).body;
  expect((await api(`client-keys/${key.body.key.id}`,"PATCH",{version:dirtyKeyBase.version,limits:{rpm:13}},session)).status).toBe(200);
  await secondKeyRow.getByRole("button",{name:"启用",exact:true}).click();
  await expect(secondKeyRow.getByRole("button",{name:"停用",exact:true})).toBeVisible();
  await expect(dirtyRPM).toHaveValue("14");
  await dirtyPolicy.getByRole("button",{name:"保存权限与限额",exact:true}).click();
  await expect(dirtyPolicy.getByRole("region",{name:"Key 权限版本冲突"})).toContainText('"rpm":13');
  expect((await api(`client-keys/${key.body.key.id}`,"GET",undefined,session)).body.limits.rpm).toBe(13);
  await dirtyPolicy.getByRole("button",{name:"使用当前版本，保留权限输入",exact:true}).click();
  await dirtyPolicy.getByRole("button",{name:"保存权限与限额",exact:true}).click();
  await expect.poll(async()=>(await api(`client-keys/${key.body.key.id}`,"GET",undefined,session)).body.limits.rpm).toBe(14);
  await expect(dirtyRPM).toHaveValue("14");
  await dirtyRPM.fill("15");
  const savedKey=(await api(`client-keys/${key.body.key.id}`,"GET",undefined,session)).body;
  expect((await api(`client-keys/${key.body.key.id}`,"PATCH",{version:savedKey.version,limits:{rpm:16}},session)).status).toBe(200);
  await dirtyPolicy.getByRole("button",{name:"保存权限与限额",exact:true}).click();
  await expect(dirtyPolicy.getByRole("region",{name:"Key 权限版本冲突"})).toContainText('"rpm":16');
  await dirtyPolicy.getByRole("button",{name:"放弃权限修改",exact:true}).click();
  await expect(dirtyRPM).toHaveValue("16");
  await existingKeyRow.locator("summary").click();
  results.key_dirty_policy_cas_explicit_resubmit_ui=true;
  const secondCurrent=(await api("client-keys","GET",undefined,session)).body.find(k=>k.id===secondKey.body.key.id);
  expect((await api(`client-keys/${secondCurrent.id}`,"DELETE",{version:secondCurrent.version},session)).status).toBe(200);
  const uiRoute=(await api("routes","GET",undefined,session)).body.items.find(r=>r.name==="UI route");
  await existingKeyRow.getByLabel("切换 Isolated client 的来源",{exact:true}).selectOption(`route:${uiRoute.id}`);
  await expect(page.getByRole("status")).toContainText("来源已切换");
  await existingKeyRow.locator("summary").click();
  const policy=existingKeyRow.locator("form").first();
  await expect(policy.locator('select[name="target"]')).toHaveValue(`route:${uiRoute.id}`);
  await policy.locator('input[name="rpm"]').fill("12");
  await policy.locator('input[name="tpm"]').fill("50000");
  await policy.locator('input[name="max_concurrent"]').fill("2");
  await policy.locator('select[name="protocol_allowlist_mode"]').selectOption("selected");
  await policy.locator('input[name="protocol_allowlist"]').fill("responses");
  await policy.getByRole("button",{name:"保存权限与限额",exact:true}).click();
  await expect.poll(async()=>(await api("client-keys","GET",undefined,session)).body.find(k=>k.id===key.body.key.id).limits.rpm).toBe(12);
  expect((await fetch(base+"/v1/models",{headers:{Authorization:`Bearer ${key.body.secret}`}})).status).toBe(200);
  await existingKeyRow.getByRole("button",{name:"停用",exact:true}).click();
  await expect(existingKeyRow.getByRole("button",{name:"启用",exact:true})).toBeVisible();
  expect((await fetch(base+"/v1/models",{headers:{Authorization:`Bearer ${key.body.secret}`}})).status).toBe(401);
  await existingKeyRow.getByRole("button",{name:"启用",exact:true}).click();
  await expect(existingKeyRow.getByRole("button",{name:"停用",exact:true})).toBeVisible();
  await existingKeyRow.getByLabel("切换 Isolated client 的来源",{exact:true}).selectOption(`source:${source.body.id}`);
  await expect(page.getByRole("status")).toContainText("来源已切换");
  results.key_target_scope_limits_enable_ui=true;
  const modelForCAS=(await api("models","GET",undefined,session)).body.items.find(m=>m.source_id===source.body.id&&m.upstream_model==="synthetic-model");
  const helperModel=await api("models","POST",{source_id:source.body.id,upstream_model:"cas-helper"},session);
  expect(helperModel.status).toBe(201);
  await page.locator("nav").getByRole("button",{name:"模型"}).click();
  const dirtyModelCard=page.locator("article.source-card").filter({has:page.locator("code").filter({hasText:"synthetic-model"})});
  await dirtyModelCard.getByText("配置价格与元数据",{exact:true}).click();
  const modelContext=dirtyModelCard.getByLabel("上下文上限",{exact:true});
  await modelContext.fill("4100");
  await dirtyModelCard.getByLabel("元数据依据",{exact:true}).fill("Synthetic UI CAS test");
  expect((await api(`models/${modelForCAS.id}`,"PATCH",{version:modelForCAS.version,context_limit:4200,metadata_reason:"Synthetic external edit"},session)).status).toBe(200);
  // A separate model action refreshes the catalog while the settings draft is open.
  const helperCard=page.locator("article.source-card").filter({has:page.locator("code").filter({hasText:"cas-helper"})});
  await helperCard.getByRole("button",{name:helperModel.body.enabled?"停用模型":"启用模型",exact:true}).click();
  await expect(helperCard.getByRole("button",{name:helperModel.body.enabled?"启用模型":"停用模型",exact:true})).toBeVisible();
  await expect(modelContext).toHaveValue("4100");
  await dirtyModelCard.getByRole("button",{name:"保存模型设置",exact:true}).click();
  await expect(dirtyModelCard.getByRole("region",{name:"模型设置版本冲突"})).toContainText("当前上下文 4200");
  expect((await api(`models/${modelForCAS.id}`,"GET",undefined,session)).body.context_limit).toBe(4200);
  await dirtyModelCard.getByRole("button",{name:"使用当前版本，保留模型输入",exact:true}).click();
  await dirtyModelCard.getByRole("button",{name:"保存模型设置",exact:true}).click();
  await expect.poll(async()=>(await api(`models/${modelForCAS.id}`,"GET",undefined,session)).body.context_limit).toBe(4100);
  await modelContext.fill("4300");
  const savedModel=(await api(`models/${modelForCAS.id}`,"GET",undefined,session)).body;
  expect((await api(`models/${modelForCAS.id}`,"PATCH",{version:savedModel.version,context_limit:4400,metadata_reason:"Synthetic external edit"},session)).status).toBe(200);
  await dirtyModelCard.getByRole("button",{name:"保存模型设置",exact:true}).click();
  await expect(dirtyModelCard.getByRole("region",{name:"模型设置版本冲突"})).toContainText("当前上下文 4400");
  await dirtyModelCard.getByRole("button",{name:"放弃模型修改",exact:true}).click();
  await expect(modelContext).toHaveValue("4400");
  if(!helperModel.body.enabled){await helperCard.getByRole("button",{name:"停用模型",exact:true}).click();await expect(helperCard.getByRole("button",{name:"启用模型",exact:true})).toBeVisible();}
  results.model_dirty_metadata_cas_explicit_resubmit_ui=true;
  await modelContext.fill("4500");
  await dirtyModelCard.getByLabel("每百万输入 token",{exact:true}).fill("1");
  await dirtyModelCard.getByLabel("每百万输出 token",{exact:true}).fill("2");
  await page.route("**/admin/prices",async route=>route.fulfill({status:503,json:{error:{message:"Synthetic price write failure"}}}));
  try{
    await dirtyModelCard.getByRole("button",{name:"保存模型设置",exact:true}).click();
    await expect(page.getByRole("alert")).toContainText("Synthetic price write failure");
    await expect(page.getByRole("status")).toContainText("模型元数据已保存，价格更新未完成");
    expect((await api(`models/${modelForCAS.id}`,"GET",undefined,session)).body.context_limit).toBe(4500);
    await expect(dirtyModelCard.getByLabel("每百万输入 token",{exact:true})).toHaveValue("1");
    await expect(dirtyModelCard.getByRole("region",{name:"模型设置版本冲突"})).toContainText("当前上下文 4500");
    await dirtyModelCard.getByRole("button",{name:"放弃模型修改",exact:true}).click();
    await expect(modelContext).toHaveValue("4500");
    await expect(dirtyModelCard.getByLabel("每百万输入 token",{exact:true})).toHaveValue("");
    results.model_metadata_saved_price_failure_preserves_draft_ui=true;
  }finally{await page.unroute("**/admin/prices")}


  await page.locator("nav").getByRole("button",{name:"路由"}).click();
  const dirtyRouteRow=page.locator(".tool-row").filter({has:page.locator("strong").filter({hasText:"UI route"})});
  await dirtyRouteRow.getByText("编辑路由",{exact:true}).click();
  const routeName=dirtyRouteRow.getByLabel("名称",{exact:true});
  await routeName.fill("Local route draft");
  const routeBase=(await api(`routes/${uiRoute.id}`,"GET",undefined,session)).body;
  expect((await api(`routes/${uiRoute.id}`,"PATCH",{version:routeBase.version,name:"Remote route name"},session)).status).toBe(200);
  // Preview reloads current routes without switching pages or resetting the draft.
  await dirtyRouteRow.getByRole("button",{name:"预览选择",exact:true}).click();
  const currentRouteRow=page.locator(".tool-row").filter({has:page.locator("strong").filter({hasText:"Remote route name"})});
  await expect(currentRouteRow.locator('input[name="name"]')).toHaveValue("Local route draft");
  await currentRouteRow.getByRole("button",{name:"保存路由",exact:true}).click();
  await expect(currentRouteRow.getByRole("region",{name:"路由设置版本冲突"})).toContainText("当前名称 Remote route name");
  expect((await api(`routes/${uiRoute.id}`,"GET",undefined,session)).body.name).toBe("Remote route name");
  await currentRouteRow.getByRole("button",{name:"使用当前版本，保留路由输入",exact:true}).click();
  await currentRouteRow.getByRole("button",{name:"保存路由",exact:true}).click();
  const savedRouteRow=page.locator(".tool-row").filter({has:page.locator("strong").filter({hasText:"Local route draft"})});
  await expect(savedRouteRow).toBeVisible();
  await savedRouteRow.getByLabel("名称",{exact:true}).fill("Discarded route name");
  const routeSaved=(await api(`routes/${uiRoute.id}`,"GET",undefined,session)).body;
  expect((await api(`routes/${uiRoute.id}`,"PATCH",{version:routeSaved.version,name:"UI route"},session)).status).toBe(200);
  await savedRouteRow.getByRole("button",{name:"保存路由",exact:true}).click();
  await expect(dirtyRouteRow.getByRole("region",{name:"路由设置版本冲突"})).toContainText("当前名称 UI route");
  await dirtyRouteRow.getByRole("button",{name:"放弃路由修改",exact:true}).click();
  await expect(routeName).toHaveValue("UI route");
  results.route_dirty_policy_cas_explicit_resubmit_ui=true;


  const resourceJobs=[{id:"synthetic-job-a",kind:"background",state:"running",version:1,cancel_requested:false,settled:false},{id:"synthetic-job-b",kind:"background",state:"running",version:1,cancel_requested:false,settled:false}];
  let releaseJob,releaseOldPoll,oldPollRequest,jobCalls=0,resourceReads=0;
  const delayedJob=new Promise(resolve=>{releaseJob=resolve}),oldPoll=new Promise(resolve=>{releaseOldPoll=resolve});
  await page.route("**/admin/resource-status",async route=>{resourceReads++;const snapshot={resources:[],jobs:structuredClone(resourceJobs)};if(resourceReads===2){oldPollRequest=route.request();await oldPoll}await route.fulfill({status:200,json:snapshot})});
  await page.route("**/admin/jobs/synthetic-job-a/cancel",async route=>{jobCalls++;await delayedJob;await route.fulfill({status:503,json:{error:{message:"Synthetic delayed job failure"}}})});
  await page.route("**/admin/jobs/synthetic-job-b/cancel",async route=>{resourceJobs[1].cancel_requested=true;await route.fulfill({status:200,json:{cancel_requested:true}})});
  try {
    await page.locator("nav").getByRole("button",{name:"运维",exact:true}).click();
    const resourcesPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"文件与后台任务",exact:true})});
    const firstJob=resourcesPanel.locator(".tool-row").filter({hasText:"synthetic-job-a"}),secondJob=resourcesPanel.locator(".tool-row").filter({hasText:"synthetic-job-b"});
    await firstJob.getByRole("button",{name:"请求取消",exact:true}).evaluate(button=>{button.click();button.click()});
    await expect(firstJob.getByRole("button",{name:"请求取消",exact:true})).toBeDisabled();
    await expect(secondJob.getByRole("button",{name:"请求取消",exact:true})).toBeEnabled();
    await expect.poll(()=>!!oldPollRequest,{timeout:10000}).toBe(true);
    await secondJob.getByRole("button",{name:"请求取消",exact:true}).click();
    await expect(secondJob.getByRole("button",{name:"请求取消",exact:true})).toHaveCount(0);
    const previousPoll=page.waitForResponse(response=>response.request()===oldPollRequest);
    releaseOldPoll();await (await previousPoll).finished();
    await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
    await expect(secondJob.getByRole("button",{name:"请求取消",exact:true})).toHaveCount(0);
    releaseJob();
    await expect(firstJob.getByRole("alert")).toContainText("Synthetic delayed job failure");
    expect(jobCalls).toBe(1);
    results.independent_resource_cancel_duplicate_and_stale_poll_ui=true;
  } finally {releaseJob();releaseOldPoll()}
  await page.unroute("**/admin/resource-status");
  await page.unroute("**/admin/jobs/synthetic-job-a/cancel");
  await page.unroute("**/admin/jobs/synthetic-job-b/cancel");
  await page.locator("nav").getByRole("button",{name:"API Keys"}).click();
  await page.locator("nav").getByRole("button",{name:"运维",exact:true}).click();
  const operationsStatus=page.locator("section").filter({has:page.getByRole("heading",{name:"本机运行与提醒",exact:true})});
  const operationsTransfer=page.locator("section").filter({has:page.getByRole("heading",{name:"配置搬运",exact:true})});
  let releaseDoctor,doctorCalls=0;
  const delayedDoctor=new Promise(resolve=>{releaseDoctor=resolve});
  await page.route("**/admin/doctor",async route=>{doctorCalls++;await delayedDoctor;await route.fulfill({status:503,json:{error:{message:"Synthetic delayed diagnostic failure"}}})});
  try {
    await operationsStatus.getByRole("button",{name:"运行本机诊断",exact:true}).evaluate(button=>{button.click();button.click()});
    await expect(operationsStatus.getByRole("button",{name:"运行本机诊断",exact:true})).toBeDisabled();
    await expect(operationsTransfer.getByRole("button",{name:"导出配置与依赖",exact:true})).toBeEnabled();
    const downloaded=page.waitForEvent("download");
    await operationsTransfer.getByRole("button",{name:"导出配置与依赖",exact:true}).click();
    expect((await downloaded).suggestedFilename()).toBe("cove-config.json");
    await expect(operationsStatus.getByRole("button",{name:"运行本机诊断",exact:true})).toBeDisabled();
    releaseDoctor();
    await expect(page.getByRole("alert")).toContainText("Synthetic delayed diagnostic failure");
    expect(doctorCalls).toBe(1);
    results.independent_operations_action_and_duplicate_guard_ui=true;
  } finally {releaseDoctor()}
  await page.unroute("**/admin/doctor");
  const operationPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"元数据备份",exact:true})});
  let releaseOlderBackup,backupCreates=0;
  const olderBackup=new Promise(resolve=>{releaseOlderBackup=resolve});
  await page.route("**/admin/backups",async route=>{backupCreates++;await olderBackup;await route.fulfill({status:202,json:{id:"synthetic-old-backup",kind:"backup",state:"succeeded",version:1}})});
  try {
    await operationPanel.getByRole("button",{name:"创建备份",exact:true}).evaluate(button=>{button.click();button.click()});
    await expect(operationPanel.getByRole("button",{name:"正在提交…",exact:true})).toBeDisabled();
    const exportPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"脱敏请求报表",exact:true})});
    await expect(exportPanel.getByRole("button",{name:"生成 JSON",exact:true})).toBeEnabled();
    const exported=page.waitForResponse(response=>response.url()===base+"/admin/exports"&&response.request().method()==="POST");
    await exportPanel.getByRole("button",{name:"生成 JSON",exact:true}).click();
    const newerOperation=await (await exported).json();
    expect(newerOperation.id).toBeTruthy();
    await expect(operationPanel.getByRole("status")).toContainText(newerOperation.id);
    releaseOlderBackup();
    await expect(operationPanel.getByRole("button",{name:"创建备份",exact:true})).toBeEnabled();
    await expect(operationPanel.getByRole("status")).toContainText(newerOperation.id);
    expect(await page.evaluate(()=>sessionStorage.getItem("cove:last-local-operation"))).toBe(newerOperation.id);
    expect(backupCreates).toBe(1);
    results.operation_old_create_does_not_replace_new_selection_ui=true;
  } finally {releaseOlderBackup()}
  await page.unroute("**/admin/backups");
  await page.locator("nav").getByRole("button",{name:"预算" }).click();
  await page.getByLabel("预算名称",{exact:true}).fill("UI budget");
  await page.getByLabel("周期金额上限",{exact:true}).fill("10");
  await page.getByRole("button",{name:"创建预算",exact:true}).click();
  await expect(page.getByRole("heading",{name:"UI budget",exact:true})).toBeVisible();
  await page.getByLabel("UI budget 金额上限",{exact:true}).fill("12");
  await page.getByRole("button",{name:"保存上限",exact:true}).click();
  await expect(page.getByRole("status")).toHaveText("当前上限已更新；已结算金额和待核对预留保留。");
  results.budget_create_cas_edit_ui=true;
  const firstBudget=(await api("budgets","GET",undefined,session)).body.items.find(b=>b.name==="UI budget");
  const secondBudget=await api("budgets","POST",{name:"Independent budget",scope:{kind:"instance"},currency:"USD",amount_limit:"20",mode:"soft",period:{kind:"calendar_month",timezone:"UTC"}},session);
  expect(secondBudget.status).toBe(201);
  await page.reload();
  await page.locator("nav").getByRole("button",{name:"预算"}).click();
  const firstBudgetCard=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"UI budget",exact:true})});
  const secondBudgetCard=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"Independent budget",exact:true})});
  let releaseBudget,budgetPatches=0;
  const delayedBudget=new Promise(resolve=>{releaseBudget=resolve});
  await page.route(`**/admin/budgets/${firstBudget.id}`,async route=>{budgetPatches++;await delayedBudget;await route.fulfill({status:503,json:{error:{message:"Synthetic delayed budget failure"}}})});
  try {
    await firstBudgetCard.getByRole("button",{name:"保存上限",exact:true}).evaluate(button=>{button.click();button.click()});
    await expect(firstBudgetCard.getByRole("button",{name:"保存上限",exact:true})).toBeDisabled();
    await expect(secondBudgetCard.getByRole("button",{name:"保存上限",exact:true})).toBeEnabled();
    await secondBudgetCard.getByLabel("Independent budget 金额上限",{exact:true}).fill("21");
    await secondBudgetCard.getByRole("button",{name:"保存上限",exact:true}).click();
    await expect.poll(async()=>(await api("budgets","GET",undefined,session)).body.items.find(b=>b.id===secondBudget.body.id).amount_limit).toBe("21");
    await expect(firstBudgetCard.getByRole("button",{name:"保存上限",exact:true})).toBeDisabled();
    releaseBudget();
    await expect(page.getByRole("alert")).toContainText("Synthetic delayed budget failure");
    expect(budgetPatches).toBe(1);
    results.independent_budget_action_and_duplicate_guard_ui=true;
  } finally {releaseBudget()}
  await page.unroute(`**/admin/budgets/${firstBudget.id}`);
  const dirtyBudget=await api("budgets","POST",{name:"CAS budget",scope:{kind:"instance"},currency:"USD",amount_limit:"30",mode:"soft",period:{kind:"calendar_month",timezone:"UTC"}},session);
  expect(dirtyBudget.status).toBe(201);
  await page.reload();
  await page.locator("nav").getByRole("button",{name:"预算"}).click();
  const dirtyBudgetCard=page.locator("article.source-card").filter({has:page.getByRole("heading",{name:"CAS budget",exact:true})});
  const dirtyLimit=dirtyBudgetCard.getByLabel("CAS budget 金额上限",{exact:true});
  await dirtyLimit.fill("31");
  expect((await api(`budgets/${dirtyBudget.body.id}`,"PATCH",{version:dirtyBudget.body.version,amount_limit:"32"},session)).status).toBe(200);
  await secondBudgetCard.getByLabel("Independent budget 金额上限",{exact:true}).fill("22");
  await secondBudgetCard.getByRole("button",{name:"保存上限",exact:true}).click();
  await expect.poll(async()=>(await api(`budgets/${secondBudget.body.id}`,"GET",undefined,session)).body.amount_limit).toBe("22");
  await expect(dirtyBudgetCard.locator("tbody")).toContainText("32");
  await expect(dirtyLimit).toHaveValue("31");
  await dirtyBudgetCard.getByRole("button",{name:"保存上限",exact:true}).click();
  await expect(page.getByRole("alert")).toContainText("预算版本已变化");
  await expect(dirtyBudgetCard.getByRole("region",{name:"CAS budget 上限版本冲突"})).toContainText("当前上限 32");
  await expect(dirtyBudgetCard.getByRole("region",{name:"CAS budget 上限版本冲突"})).toContainText("本地输入 31");
  await expect(dirtyLimit).toHaveValue("31");
  expect((await api(`budgets/${dirtyBudget.body.id}`,"GET",undefined,session)).body.amount_limit).toBe("32");
  await dirtyBudgetCard.getByRole("button",{name:"使用当前版本，保留我的输入",exact:true}).click();
  await dirtyBudgetCard.getByRole("button",{name:"保存上限",exact:true}).click();
  await expect.poll(async()=>(await api(`budgets/${dirtyBudget.body.id}`,"GET",undefined,session)).body.amount_limit).toBe("31");
  await dirtyLimit.fill("33");
  const savedDirty=(await api(`budgets/${dirtyBudget.body.id}`,"GET",undefined,session)).body;
  expect((await api(`budgets/${dirtyBudget.body.id}`,"PATCH",{version:savedDirty.version,amount_limit:"34"},session)).status).toBe(200);
  await dirtyBudgetCard.getByRole("button",{name:"保存上限",exact:true}).click();
  await expect(dirtyBudgetCard.getByRole("region",{name:"CAS budget 上限版本冲突"})).toContainText("当前上限 34");
  await expect(dirtyLimit).toHaveValue("33");
  await dirtyBudgetCard.getByRole("button",{name:"放弃本地修改",exact:true}).click();
  await expect(dirtyLimit).toHaveValue("34");
  expect((await api(`budgets/${dirtyBudget.body.id}`,"DELETE",undefined,session)).status).toBe(200);
  results.budget_dirty_input_cas_diff_explicit_resubmit_ui=true;
  expect((await api(`budgets/${secondBudget.body.id}`,"DELETE",undefined,session)).status).toBe(200);


  const pack=(await api("config-transfer/export","POST",{include_dependencies:true},session)).body;
  expect(pack.budgets).toHaveLength(1);
  // Keep this import fixture independent of the temporary CAS helper model.
  pack.models=pack.models.filter(v=>v.model.upstream_model!=="cas-helper");
  for(const item of pack.sources)item.source.models=(item.source.models||[]).filter(v=>v!=="cas-helper");
  pack.sources[0].source.name="Imported source";
  pack.routes[0].route.name="Imported route";
  pack.aliases[0].alias.public_model="imported-ui-model";
  pack.budgets[0].budget.name="Imported budget";
  await page.locator("nav").getByRole("button",{name:"运维" }).click();
  const transfer=page.locator("section").filter({has:page.getByRole("heading",{name:"配置搬运",exact:true})});
  await transfer.getByLabel("配置 JSON",{exact:true}).setInputFiles({name:"synthetic-config.json",mimeType:"application/json",buffer:Buffer.from(JSON.stringify(pack))});
  await transfer.getByRole("button",{name:"预览导入",exact:true}).click();
  await expect(transfer.locator("fieldset")).toHaveCount(6);
  const modelChoice=transfer.locator("fieldset").filter({has:page.locator("legend",{hasText:"model ·"})});
  await modelChoice.getByLabel("处理方式").selectOption("create_new");
  await transfer.getByRole("button",{name:"应用逐项选择",exact:true}).click();
  await expect(transfer.locator("fieldset")).toHaveCount(0);
  await expect.poll(async()=>(await api("sources","GET",undefined,session)).body.some(s=>s.name==="Imported source")).toBe(true);
  results.config_create_with_financial_dependency_ui=true;

  await transfer.getByLabel("导入模式").selectOption("replace_selected");
  pack.budgets[0].budget.name="UI budget changed by import";
  await transfer.getByLabel("配置 JSON",{exact:true}).setInputFiles({name:"synthetic-replace.json",mimeType:"application/json",buffer:Buffer.from(JSON.stringify(pack))});
  await transfer.getByRole("button",{name:"预览导入",exact:true}).click();
  const budgetChoice=transfer.locator("fieldset").filter({has:page.locator("legend",{hasText:"budget ·"})});
  await budgetChoice.getByLabel("处理方式").selectOption("replace");
  await budgetChoice.getByLabel(/^现有对象/).selectOption({label:"UI budget · v2"});
  await transfer.getByRole("button",{name:"应用逐项选择",exact:true}).click();
  await expect(transfer.locator("fieldset")).toHaveCount(0);
  await expect.poll(async()=>(await api("budgets","GET",undefined,session)).body.items.some(b=>b.name==="UI budget changed by import"&&b.amount_limit==="12")).toBe(true);
  results.config_selected_replace_version_ui=true;


  // Client history waits independently; an older preview cannot replace a
  // newer selection. These management fixtures never access client files.
  const historyA={id:"synthetic-history-a",kind:"codex",scope:"user",path:join(temp,"a.toml"),state:"applied",client_version:"synthetic",created_at:new Date().toISOString(),files:[]};
  const historyB={...historyA,id:"synthetic-history-b",path:join(temp,"b.toml")};
  await page.route("**/admin/client-changes",route=>route.request().method()==="GET"?route.fulfill({status:200,json:{items:[historyA,historyB],next_cursor:null}}):route.continue());
  let releaseHistoryA,historyCallsA=0;
  const delayedHistoryA=new Promise(resolve=>{releaseHistoryA=resolve});
  const restoreFixture=change=>({change_id:change.id,path:change.path,current_hash:"synthetic-current-hash",status:"ready",fields:[{field:"model",action:"restore_before",before:"before",ours:"ours",current:"ours"}]});
  await page.route(`**/admin/client-changes/${historyA.id}/restore-preview`,async route=>{historyCallsA++;await delayedHistoryA;await route.fulfill({status:200,json:restoreFixture(historyA)})});
  await page.route(`**/admin/client-changes/${historyB.id}/restore-preview`,route=>route.fulfill({status:200,json:restoreFixture(historyB)}));
  await page.locator("nav").getByRole("button",{name:"工具"}).click();
  const historyRowA=page.locator(".tool-row").filter({has:page.locator("code",{hasText:historyA.path})});
  const historyRowB=page.locator(".tool-row").filter({has:page.locator("code",{hasText:historyB.path})});
  try {
    await historyRowA.getByRole("button",{name:"预览恢复",exact:true}).click();
    await expect.poll(()=>historyCallsA).toBe(1);
    await expect(historyRowA.getByRole("button")).toBeDisabled();
    await expect(historyRowB.getByRole("button",{name:"预览恢复",exact:true})).toBeEnabled();
    await expect(page.getByRole("button",{name:"生成字段预览",exact:true})).toBeEnabled();
    await historyRowB.getByRole("button",{name:"预览恢复",exact:true}).click();
    const restoreRegion=page.getByRole("region",{name:"客户端恢复预览",exact:true});
    await expect(restoreRegion).toContainText(historyB.path);
    releaseHistoryA();
    await expect(historyRowA.getByRole("button",{name:"预览恢复",exact:true})).toBeEnabled();
    await expect(restoreRegion).toContainText(historyB.path);
    await expect(restoreRegion).not.toContainText(historyA.path);
    expect(historyCallsA).toBe(1);
    results.independent_client_history_and_stale_preview_ui=true;
  } finally {
    releaseHistoryA();
    await page.unroute(`**/admin/client-changes/${historyA.id}/restore-preview`);
    await page.unroute(`**/admin/client-changes/${historyB.id}/restore-preview`);
    await page.unroute("**/admin/client-changes");
  }


  const clientPanel=page.locator("section").filter({has:page.getByRole("heading",{name:"客户端配置",exact:true})});
  const cards=(await api("clients","GET",undefined,session)).body;
  let releaseDetection,releaseConfigPreview,detectionCalls=0,configPreviewCalls=0;
  const slowDetection=new Promise(resolve=>{releaseDetection=resolve});
  const slowConfigPreview=new Promise(resolve=>{releaseConfigPreview=resolve});
  const isolatedClientPath=join(temp,"isolated.toml");
  await page.route("**/admin/clients",async route=>{detectionCalls++;await slowDetection;await route.fulfill({status:200,json:cards})});
  await page.route("**/admin/clients/codex/preview",async route=>{configPreviewCalls++;const body=route.request().postDataJSON();expect(body.explicit_path).toBe(isolatedClientPath);await slowConfigPreview;await route.fulfill({status:200,json:{preview_id:"synthetic-ui-config-preview",expires_at:new Date(Date.now()+60000).toISOString(),kind:"codex",status:"ready",client_version:"synthetic",files:[{path:isolatedClientPath,exists:false,base_hash:"synthetic-before-hash",redacted_diff:[]}],blockers:[],warnings:[],required_secret:{env_name:"SYNTHETIC_GATEWAY_KEY",instruction:"Synthetic UI only; no client files or secrets are read."}}})});
  try {
    await clientPanel.getByRole("button",{name:"重新检测版本",exact:true}).click();
    await expect.poll(()=>detectionCalls).toBe(1);
    await expect(clientPanel.getByRole("button",{name:"正在检测…",exact:true})).toBeDisabled();
    await clientPanel.getByRole("combobox",{name:/^客户端/}).selectOption("codex");
    await clientPanel.getByLabel("授权目录（绝对路径）",{exact:true}).fill(temp);
    await clientPanel.getByLabel("配置文件（绝对路径）",{exact:true}).fill(isolatedClientPath);
    await clientPanel.getByLabel("公开模型 ID",{exact:true}).fill("synthetic-ui-model");
    await clientPanel.locator("form").evaluate(form=>{form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));form.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}))});
    await expect.poll(()=>configPreviewCalls).toBe(1);
    await expect(clientPanel.getByLabel("公开模型 ID",{exact:true})).toBeDisabled();
    releaseConfigPreview();
    const configPreviewRegion=page.getByRole("region",{name:"客户端配置预览",exact:true});
    await expect(configPreviewRegion).toContainText(isolatedClientPath);
    await expect(configPreviewRegion.getByRole("button",{name:"应用所选文件",exact:true})).toBeEnabled();
    expect(configPreviewCalls).toBe(1);
    releaseDetection();
    await expect(clientPanel.getByRole("button",{name:"重新检测版本",exact:true})).toBeEnabled();
    await expect(configPreviewRegion).toContainText(isolatedClientPath);
    await expect(clientPanel.getByLabel("公开模型 ID",{exact:true})).toHaveValue("synthetic-ui-model");
    results.independent_client_detection_config_preview_duplicate_guard_ui=true;
  } finally {
    releaseDetection();releaseConfigPreview();
    await page.unroute("**/admin/clients");
    await page.unroute("**/admin/clients/codex/preview");
  }

  await page.locator("nav").getByRole("button", { name: "API Keys" }).click();
  const keyPanel = page.locator("section").filter({ has: page.getByRole("heading", { name: "创建 API Key", exact: true }) });
  await keyPanel.locator('input[name="name"]').fill("UI budget rotation");
  await keyPanel.getByLabel("调用目标").selectOption(`source:${source.body.id}`);
  await keyPanel.getByLabel("Key 预算").selectOption("new");
  await keyPanel.getByLabel("预算名称", { exact: true }).fill("UI Key budget");
  await keyPanel.getByLabel("金额上限", { exact: true }).fill("10");
  await keyPanel.getByRole("button", { name: "创建 API Key", exact: true }).click();
  await expect(keyPanel.locator(".secret code")).toBeVisible();
  const originalSecret = await keyPanel.locator(".secret code").textContent();
  await page.evaluate(()=>{Object.defineProperty(navigator.clipboard,"writeText",{configurable:true,value:async value=>{globalThis.__syntheticClipboard=value}})});
  await keyPanel.getByRole("button",{name:"复制",exact:true}).click();
  await expect(page.getByRole("status")).toHaveText("客户端 Key 已复制");
  expect(await page.evaluate(()=>globalThis.__syntheticClipboard)).toBe(originalSecret);
  await page.evaluate(()=>{Object.defineProperty(navigator.clipboard,"writeText",{configurable:true,value:async()=>{throw new Error("Synthetic clipboard denied")}})});
  await keyPanel.getByRole("button",{name:"复制",exact:true}).click();
  await expect(page.getByRole("alert")).toContainText("Synthetic clipboard denied");
  await expect(keyPanel.locator(".secret code")).toHaveText(originalSecret);
  results.key_copy_callback_success_and_denial_preserves_secret=true;
  const originalKey = (await api("client-keys", "GET", undefined, session)).body.find(k => k.name === "UI budget rotation");
  expect(originalKey.budget_id).toBeTruthy();
  const row = page.locator(".tool-row").filter({ has: page.getByText("UI budget rotation", { exact: true }) });
  await row.locator("summary").click();
  const revokeInput=new Date(Date.now()+3600000);
  const revokeLocal=new Date(revokeInput.getTime()-revokeInput.getTimezoneOffset()*60000).toISOString().slice(0,16);
  await row.locator('input[name="revoke_at"]').fill(revokeLocal);
  await row.getByRole("button", { name: "创建轮换 Key", exact: true }).click();
  await expect.poll(async()=>(await api("client-keys","GET",undefined,session)).body.find(k=>k.id===originalKey.id).revoke_at).toBe(new Date(revokeLocal).toISOString().replace(".000Z","Z"));
  results.rotation_planned_utc_boundary_ui=true;
  await expect.poll(() => keyPanel.locator(".secret code").textContent()).not.toBe(originalSecret);
  const rotatedSecret = await keyPanel.locator(".secret code").textContent();
  const rotatedKeys = (await api("client-keys", "GET", undefined, session)).body.filter(k => k.name === "UI budget rotation");
  expect(rotatedKeys).toHaveLength(2);
  expect(rotatedKeys.every(k => k.budget_id === originalKey.budget_id && !k.revoked)).toBe(true);
  expect(rotatedKeys.every(k => k.budget_summary.some(b => b.id === originalKey.budget_id))).toBe(true);
  results.key_create_once_secret_and_budget_rotation_ui = true;
  expect((await fetch(base + "/v1/models", { headers: { Authorization: `Bearer ${rotatedSecret}` } })).status).toBe(200);
  const oldRow = page.locator(".tool-row").filter({ has: page.getByText(originalKey.fingerprint + "…", { exact: true }) });
  await oldRow.getByRole("button", { name: "撤销", exact: true }).click();
  await expect(oldRow.getByText("已撤销", { exact: true })).toBeVisible();
  expect((await fetch(base + "/v1/models", { headers: { Authorization: `Bearer ${originalSecret}` } })).status).toBe(401);
  expect((await fetch(base + "/v1/models", { headers: { Authorization: `Bearer ${rotatedSecret}` } })).status).toBe(200);
  results.rotation_verify_new_key_then_revoke_old_ui = true;
  await keyPanel.getByRole("button", { name: "已保存，隐藏", exact: true }).click();
  await expect(keyPanel.locator(".secret")).toHaveCount(0);
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "API Keys" }).click();
  await expect(page.locator(".secret")).toHaveCount(0);
  results.key_secret_absent_after_refresh = true;
  await page.reload();
  await expect(page.locator("nav")).toBeVisible();
  expect(await page.evaluate(() => sessionStorage.getItem("cove.management"))).toBe(session);
  const fresh = await context.newPage();
  fresh.on("pageerror", error => errors.push(error.message));
  await fresh.goto(base);
  await expect(fresh.locator("nav")).toBeVisible();
  results.refresh_and_new_tab = true;
  await stop();
  await launch();
  await fresh.reload();
  await expect(fresh.locator("nav")).toBeVisible();
  await expect(fresh.getByRole("heading",{name:"Isolated source",level:2,exact:true})).toBeVisible();
  const restartedSession = await fresh.evaluate(() => sessionStorage.getItem("cove.management"));
  expect((await api("client-keys", "GET", undefined, restartedSession)).body.some(k => k.id === key.body.key.id)).toBe(true);
  results.restart_direct_entry_preserves_data = true;
  const missingLabels=[];
  for (const width of [375,390,768,1280]) {
    await fresh.setViewportSize({width,height:844});
    for (const section of ["来源","模型","API Keys","路由","工具","请求","用量","预算","运维","设置"]) {
      await fresh.locator("nav").getByRole("button",{name:section}).click();
      await expect(fresh.locator("nav").getByRole("button",{name:"API Keys"})).toBeVisible();
      if(await fresh.evaluate(()=>document.documentElement.scrollWidth>innerWidth)){console.error(JSON.stringify({section,width,overflow:await fresh.evaluate(()=>[...document.querySelectorAll("body *")].filter(e=>e.getBoundingClientRect().right>innerWidth+1).slice(0,8).map(e=>({tag:e.tagName,cls:e.className,right:e.getBoundingClientRect().right,width:e.getBoundingClientRect().width}))) }))}
      await expect.poll(()=>fresh.evaluate(()=>document.documentElement.scrollWidth>innerWidth),{timeout:3000,message:`${section} overflows at ${width}`}).toBe(false);
      if(width===1280){
        await fresh.waitForLoadState("networkidle");
        const unlabeled=await fresh.evaluate(()=>[...document.querySelectorAll('input:not([type="hidden"]),select,textarea')].filter(input=>!input.labels?.length&&!input.getAttribute("aria-label")&&!input.getAttribute("aria-labelledby")).map(input=>({tag:input.tagName,name:input.getAttribute("name"),placeholder:input.getAttribute("placeholder")})));
        if(unlabeled.length)missingLabels.push({section,controls:unlabeled});
      }
    }
  }
  expect(missingLabels,"controls require labels on all ten pages").toEqual([]);
  mkdirSync(join(root, "test-results"), { recursive: true });
  await fresh.screenshot({ path: join(root, "test-results/local-entry-mobile.png"), fullPage: true });
  results.responsive_direct_entry = true;
  results.semantic_control_labels_10_pages_ui = true;

  // Actual Tab/Enter navigation and focus traversal; no mouse activation.
  for(const section of ["来源","模型","API Keys","路由","工具","请求","用量","预算","运维","设置"]){
    await fresh.reload();await expect(fresh.locator("nav")).toBeVisible();
    let selected=false;
    for(let i=0;i<30;i++){
      await fresh.keyboard.press("Tab");
      if(await fresh.evaluate(name=>document.activeElement?.closest("nav")&&document.activeElement.textContent.includes(name),section)){selected=true;break;}
    }
    expect(selected,`${section} navigation reachable by Tab`).toBe(true);await fresh.keyboard.press("Enter");
    await fresh.waitForLoadState("networkidle");
    const required=await fresh.evaluate(()=>{
      window.__coveFocusIDs=new WeakMap();
      const elements=[...document.querySelectorAll('main button,main input:not([type="hidden"]),main select,main textarea,main a[href],main summary,main [tabindex]')].filter(el=>!el.matches(':disabled')&&el.tabIndex>=0&&el.checkVisibility({visibilityProperty:true})&&el.getClientRects().length&&el.getBoundingClientRect().width>0);
      elements.forEach((el,i)=>window.__coveFocusIDs.set(el,i));return elements.length;
    });
    const reached=new Set();
    for(let i=0;i<required*3+30&&reached.size<required;i++){
      await fresh.keyboard.press("Tab");
      const focused=await fresh.evaluate(()=>{
        const el=document.activeElement,id=window.__coveFocusIDs.get(el);if(id===undefined)return null;
        const r=el.getBoundingClientRect();return {id,visible:r.width>0&&r.height>0&&r.bottom>0&&r.top<innerHeight&&r.right>0&&r.left<innerWidth};
      });
      if(focused){expect(focused.visible,`${section} focused control is visible`).toBe(true);reached.add(focused.id);}
    }
    expect(reached.size,`${section} all visible enabled controls reachable by Tab`).toBe(required);
  }
  results.keyboard_navigation_and_visible_controls_10_pages_ui=true;
  const zoomProfile=join(temp,"zoom-profile");mkdirSync(join(zoomProfile,"Default"),{recursive:true});
  // ChromeZoomLevelPrefs: default storage-partition key x; 1.2 ** level.
  // This sets browser zoom, not CSS zoom or pinch/page-scale emulation.
  writeFileSync(join(zoomProfile,"Default","Preferences"),JSON.stringify({partition:{per_host_zoom_levels:{x:{"127.0.0.1":{zoom_level:Math.log(2)/Math.log(1.2),last_modified:"13400000000000000"}}}}}));
  zoomContext=await chromium.launchPersistentContext(zoomProfile,{headless:true,channel:"chrome",viewport:null,args:["--window-size=1280,900"]});
  const zoomPage=zoomContext.pages()[0];await zoomPage.goto(base);
  await expect(zoomPage.locator("nav")).toBeVisible();
  const zoomMetrics=await zoomPage.evaluate(()=>({width:innerWidth,pixelRatio:devicePixelRatio,scale:visualViewport.scale}));
  expect(zoomMetrics).toEqual({width:640,pixelRatio:2,scale:1});
  for(const section of ["来源","模型","API Keys","路由","工具","请求","用量","预算","运维","设置"]){
    await zoomPage.locator("nav").getByRole("button",{name:section}).click();await zoomPage.waitForLoadState("networkidle");
    await expect.poll(()=>zoomPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth),{message:`${section} overflows at actual 200 percent browser zoom`}).toBe(false);
    await expect(zoomPage.locator("nav").getByRole("button",{name:"API Keys"})).toBeVisible();
  }
  await zoomPage.screenshot({path:join(root,"test-results/local-entry-200-percent.png"),fullPage:true});
  results.actual_chrome_200_percent_zoom_10_pages_ui=true;results.browser_zoom_metrics=zoomMetrics;
  await zoomContext.close();zoomContext=null;

  const unavailable = await browser.newContext();
  await unavailable.addInitScript(() => { Storage.prototype.getItem = () => { throw new Error("storage blocked"); }; });
  const blocked = await unavailable.newPage();
  await blocked.goto(base);
  await expect(blocked.getByRole("alert")).toHaveText("浏览器会话存储不可用，请允许此站点使用会话存储后重试。");
  results.storage_failure_explained = true;
  await unavailable.close();
  expect(errors).toHaveLength(0);
  results.synthetic_upstream_calls=syntheticCalls;
  results.scope = "Isolated binary/browser and synthetic loopback model HTTP; clipboard callbacks and selected management responses stubbed for UI races; no real account/provider requests";
  writeFileSync(join(root, "test-results/local-entry-acceptance.json"), JSON.stringify(results, null, 2) + "\n");
  console.log(JSON.stringify(results, null, 2));
} catch (error) {
  mkdirSync(join(root, "test-results"), { recursive: true });
  const diagnostic = { passed: false, completed: results, errors, error: error.message };
  writeFileSync(join(root, "test-results/local-entry-acceptance.json"), JSON.stringify(diagnostic, null, 2) + "\n");
  for (const context of browser?.contexts() || []) {
    const page = context.pages()[0];
    if (page) {
      diagnostic.visible_text = await page.locator("body").innerText();
      await page.screenshot({ path: join(root, "test-results/local-entry-failure.png"), fullPage: true });
      break;
    }
  }
  console.error(JSON.stringify(diagnostic));
  throw error;
} finally {
  await zoomContext?.close();
  await browser?.close();
  await stop();
  await new Promise(resolve=>upstream.close(resolve));
  rmSync(temp, { recursive: true, force: true });
}
