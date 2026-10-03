import { homedir, tmpdir } from "node:os";
import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { resolve, join } from "node:path";

// Explicit real-model acceptance. Only Cove's own credentials are read.
// Client secrets, provider credentials, prompts and replies never enter the report.
const root = resolve(import.meta.dirname, "..");
const configPath = resolve(process.argv[2] || join(root, "config.example.json"));
const outputPath = resolve(process.argv[3] || join(root, "test-results/live-acceptance.json"));
const config = JSON.parse(readFileSync(configPath, "utf8"));
const base = `http://${config.listen}`;
const dataDir = config.data_dir || join(homedir(), "Library/Application Support/gatt");
const build = JSON.parse(readFileSync(join(root, "bin/build-evidence.json")));
const report = {
  created_at: new Date().toISOString(), base, build_id: build.source_id,
  binary_sha256: createHash("sha256").update(readFileSync(join(root, "bin/gatt"))).digest("hex"),
  passed: false, cases: [],
  scope: "Real Cove OAuth subscription, live model catalog, browser-created Key, three protocol tool rounds and isolated Codex CLI. No strict cost cap or full Spec acceptance claim.",
};
let session, key, browser, cliDir;
const wireUsage = new Map();
async function admin(path, method = "GET", body) {
  const response = await fetch(base + "/admin/" + path, {
    method, redirect: "error", signal: AbortSignal.timeout(120000),
    headers: { Origin: base, Authorization: "Bearer " + session, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error?.message || `Management HTTP ${response.status}`);
  return data;
}
async function call(path, body) {
  const response = await fetch(base + path, {
    method: "POST", redirect: "error", signal: AbortSignal.timeout(120000),
    headers: { Authorization: "Bearer " + key.secret, "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const requestID = response.headers.get("X-Gateway-Request-Id");
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error(`Model HTTP ${response.status}; request ${requestID}`);
  }
  const raw = await response.text();
  if (response.headers.get("Content-Type")?.includes("text/event-stream")) {
    const completedItems = new Map();
    for (const frame of raw.split(/\r?\n\r?\n/)) {
      const data = frame.split(/\r?\n/).filter(line => line.startsWith("data:")).map(line => line.slice(5).trim()).join("\n");
      if (!data || data === "[DONE]") continue;
      const event = JSON.parse(data);
      if (event.type === "response.output_item.done") {
        check(Number.isInteger(event.output_index) && event.output_index >= 0 && !completedItems.has(event.output_index), "Invalid or duplicate completed output item");
        completedItems.set(event.output_index, event.item);
      }
      if (event.type === "response.completed") {
        wireUsage.set(requestID, event.response.usage);
        // Assemble the client view from actual completed items. Native wire
        // remains unchanged; terminal completion and real usage are required.
        const value = event.response;
        if (!value.output?.length && completedItems.size) {
          value.output = Array.from({ length: completedItems.size }, (_, index) => {
            check(completedItems.has(index), "Missing completed output item");
            return completedItems.get(index);
          });
        }
        return { value, requestID };
      }
      if (["error", "response.failed", "response.incomplete"].includes(event.type)) throw new Error(`Non-complete provider terminal; request ${requestID}`);
    }
    throw new Error(`Missing completed response; request ${requestID}`);
  }
  const value = JSON.parse(raw);
  const usage = value.usage;
  wireUsage.set(requestID, path === "/v1/chat/completions" ? { input_tokens: usage?.prompt_tokens, output_tokens: usage?.completion_tokens } : usage);
  return { value, requestID };
}
function check(ok, message) { if (!ok) throw new Error(message); }
const parameters = { type: "object", properties: { a: { type: "integer" }, b: { type: "integer" } }, required: ["a", "b"], additionalProperties: false };
const prompt = "Call add with a=7 and b=5 exactly once. After its actual tool result arrives, reply with only that result.";
function executeTool(args) {
  const value = typeof args === "string" ? JSON.parse(args) : args;
  check(value?.a === 7 && value?.b === 5, "Unexpected tool arguments; no tool executed");
  return String(value.a + value.b);
}
try {
  const administrator = JSON.parse(readFileSync(resolve(dataDir, "secrets/credentials.json"))).administrator;
  check(typeof administrator === "string" && administrator.length > 0, "Cove management credential missing");
  const ticketResponse = await fetch(base + "/admin/browser-tickets", { method: "POST", headers: { Authorization: "Bearer " + administrator } });
  check(ticketResponse.ok, "Management ticket rejected");
  const ticket = await ticketResponse.json();
  const login = await fetch(base + "/admin/session", {
    method: "POST", headers: { Origin: base, "Content-Type": "application/json" }, body: JSON.stringify({ ticket: ticket.ticket }),
  });
  check(login.ok, "Management session rejected");
  session = (await login.json()).session_token;
  check((await admin("status")).build_id === build.source_id, "Running binary differs from frozen build");
  const source = (await admin("sources")).find(s => s.kind === "codex_subscription" && s.enabled && s.auth_status === "logged_in");
  check(source, "请在此 Cove 实例完成独立订阅登录；未发起真实模型调用");
  report.source_id = source.id;
  const discovery = await admin(`sources/${source.id}/models/discover`, "POST", {});
  check(discovery, "Live model discovery did not complete");
  const deadline = Date.now() + 120000;
  let observation = discovery;
  while (["pending", "running"].includes(observation.state) && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 1000));
    observation = await admin(`operations/${discovery.id}`);
  }
  check(observation.state === "succeeded", "Live model catalog observation failed or timed out");
  const models = (await admin(`models?source_id=${encodeURIComponent(source.id)}`)).items.filter(m => m.enabled && m.discovery === "discovered" && m.modalities?.includes("text"));
  check(models.length > 0, "No enabled provider-discovered model; no model calls made");
  const model = models.find(m => m.upstream_model === "gpt-5.6-luna") || models[0];
  report.model = model.upstream_model;
  report.live_catalog_models = models.map(m => m.upstream_model);
  check(source.allow_parameter_adjustment, "Messages acceptance requires explicit subscription parameter adjustment consent");
  const { chromium, expect } = createRequire(join(root, "web/package.json"))("@playwright/test");
  browser = await chromium.launch({ headless: true, channel: "chrome" });
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.addInitScript(() => {
    localStorage.setItem("cove.ui.mode", "dark");
    localStorage.setItem("cove.ui.lang", "zh");
    localStorage.setItem("cove.ui.page", "概览");
  });
  await page.goto(base);
  await page.locator("nav").getByRole("button", { name: "Keys", exact: true }).click();
  await page.getByRole("button", { name: "add 创建 Key", exact: true }).click();
  const panel = page.getByRole("dialog", { name: "API Keys管理", exact: true });
  const name = "真实验收 · " + Date.now();
  await panel.locator('input[name="name"]').fill(name);
  await panel.locator('select[name="target"]').selectOption("source:" + source.id);
  await panel.getByRole("button", { name: "创建 API Key", exact: true }).click();
  await expect(panel.locator(".secret code")).toBeVisible();
  const secret = await panel.locator(".secret code").textContent();
  const created = (await admin("client-keys")).find(k => k.name === name);
  check(created && secret, "Browser did not create a usable Key");
  key = { key: created, secret };
  report.browser_key_creation = true;
  await panel.getByRole("button", { name: "已保存，隐藏", exact: true }).click();
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "Keys", exact: true }).click();
  await expect(page.locator(".secret")).toHaveCount(0);
  report.once_secret_absent_after_refresh = true;
  await browser.close(); browser = undefined;
  const clientModels = await fetch(base + "/v1/models", { headers: { Authorization: "Bearer " + secret } });
  check(clientModels.ok && (await clientModels.json()).data.some(m => m.id === report.model), "Discovered model absent from client catalog");
  for (const protocol of ["responses", "chat_completions", "messages"]) {
    const result = { protocol, passed: false, request_ids: [] };
    report.cases.push(result);
    try {
      let path, firstBody;
      if (protocol === "responses") {
        path = "/v1/responses";
        firstBody = { model: report.model, stream: true, store: false, input: [{ role: "user", content: [{ type: "input_text", text: prompt }] }], tools: [{ type: "function", name: "add", description: "Add two integers", parameters, strict: true }], tool_choice: { type: "function", name: "add" } };
      } else if (protocol === "chat_completions") {
        path = "/v1/chat/completions";
        firstBody = { model: report.model, messages: [{ role: "user", content: prompt }], tools: [{ type: "function", function: { name: "add", description: "Add two integers", parameters, strict: true } }], tool_choice: { type: "function", function: { name: "add" } } };
      } else {
        path = "/v1/messages";
        firstBody = { model: report.model, max_tokens: 256, messages: [{ role: "user", content: prompt }], tools: [{ name: "add", description: "Add two integers", input_schema: parameters }], tool_choice: { type: "tool", name: "add" } };
      }
      const first = await call(path, firstBody); result.request_ids.push(first.requestID);
      let follow, text;
      if (protocol === "responses") {
        const calls = first.value.output.filter(v => v.type === "function_call");
        check(calls.length === 1 && calls[0].name === "add", "Expected exactly one add tool call");
        follow = { ...firstBody, tool_choice: "none", input: [...firstBody.input, ...first.value.output, { type: "function_call_output", call_id: calls[0].call_id, output: executeTool(calls[0].arguments) }] };
      } else if (protocol === "chat_completions") {
        const message = first.value.choices[0].message;
        check(message.tool_calls?.length === 1 && message.tool_calls[0].function.name === "add", "Expected exactly one add tool call");
        const tool = message.tool_calls[0];
        follow = { ...firstBody, tool_choice: "none", messages: [...firstBody.messages, message, { role: "tool", tool_call_id: tool.id, content: executeTool(tool.function.arguments) }] };
      } else {
        const tools = first.value.content.filter(v => v.type === "tool_use");
        check(tools.length === 1 && tools[0].name === "add", "Expected exactly one add tool call");
        follow = { ...firstBody, tool_choice: { type: "none" }, messages: [...firstBody.messages, { role: "assistant", content: first.value.content }, { role: "user", content: [{ type: "tool_result", tool_use_id: tools[0].id, content: executeTool(tools[0].input) }] }] };
      }
      result.actual_tool_result = 12;
      const second = await call(path, follow); result.request_ids.push(second.requestID);
      if (protocol === "responses") text = second.value.output.filter(v => v.type === "message").flatMap(v => v.content || []).map(v => v.text || "").join("");
      else if (protocol === "chat_completions") text = second.value.choices[0].message.content;
      else text = second.value.content.filter(v => v.type === "text").map(v => v.text).join("");
      check(text?.trim() === "12", "Final reply differs from actual tool output");
      const records = (await admin(`requests?client_key_id=${key.key.id}&protocol=${protocol}`)).items;
      for (const id of result.request_ids) {
        const record = records.find(r => r.id === id);
        const observed = wireUsage.get(id);
        check(record?.status === "succeeded" && Number.isInteger(record.usage?.input_tokens) && Number.isInteger(record.usage?.output_tokens), "Tool request lacks successful recorded usage");
        check(observed?.input_tokens === record.usage.input_tokens && observed?.output_tokens === record.usage.output_tokens, "Wire usage differs from recorded usage");
      }
      result.final_reply_matches_tool = true; result.passed = true;
    } catch (error) { result.error = error.message; }
  }
  cliDir = mkdtempSync(join(tmpdir(), "cove-live-cli-"));
  const home = join(cliDir, "codex-home"); mkdirSync(home, { mode: 0o700 });
  writeFileSync(join(home, "config.toml"), `model_provider = "cove"\nmodel = ${JSON.stringify(report.model)}\nweb_search = "disabled"\ncli_auth_credentials_store = "ephemeral"\n[model_providers.cove]\nname = "Cove"\nbase_url = ${JSON.stringify(base + "/v1")}\nenv_key = "PERSONAL_GATEWAY_KEY"\nwire_api = "responses"\nsupports_websockets = false\nrequest_max_retries = 0\nstream_max_retries = 0\n`, { mode: 0o600 });
  const child = spawn("codex", ["exec", "--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only", "--cd", cliDir, "只运行一次命令 printf GATT_REAL_TOOL_OK，根据实际工具返回值回答。不要读写文件、访问网络或执行其他命令。"], {
    env: { PATH: process.env.PATH, HOME: cliDir, CODEX_HOME: home, PERSONAL_GATEWAY_KEY: key.secret, NO_COLOR: "1" }, stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "", finalReply = "";
  child.stdout.on("data", b => { finalReply = (finalReply + b.toString()).slice(-10000); });
  for (const stream of [child.stdout, child.stderr]) stream.on("data", b => { output = (output + b.toString()).slice(-500000); });
  const timeout = setTimeout(() => child.kill("SIGTERM"), 120000);
  let exitCode;
  try { exitCode = await new Promise((resolve, reject) => { child.on("error", reject); child.on("close", resolve); }); } finally { clearTimeout(timeout); }
  report.cli = { client_version: execFileSync("codex", ["--version"], { encoding: "utf8" }).trim(), exit_code: exitCode, tool_command_observed: output.includes("printf GATT_REAL_TOOL_OK"), tool_success_observed: /succeeded[^\n]*\nGATT_REAL_TOOL_OK/.test(output), final_reply_matches_tool: finalReply.trim() === "GATT_REAL_TOOL_OK" };
  const records = (await admin(`requests?client_key_id=${key.key.id}`)).items;
  report.requests = records.map(r => ({ id: r.id, protocol: r.protocol, status: r.status, upstream_status: r.upstream_status, delivery_status: r.delivery_status, observation_status: r.observation_status, http_status: r.http_status, error_stage: r.error_stage, usage: r.usage, usage_completeness: r.usage_completeness }));
  const usage = await admin(`usage?client_key_id=${key.key.id}`);
  report.usage_summary = usage;
  const input = records.reduce((sum, r) => sum + (r.usage.input_tokens ?? 0), 0);
  const outputTokens = records.reduce((sum, r) => sum + (r.usage.output_tokens ?? 0), 0);
  report.usage_totals_match_records = usage.requests === records.length && usage.known_input_tokens === input && usage.known_output_tokens === outputTokens;
  // A CLI may close the HTTP body after consuming the formal terminal. Keep
  // that local cancellation visible; require independent CLI final-output and
  // upstream-completion evidence rather than rewriting it as request success.
  const cliTerminalClose = r => !wireUsage.has(r.id) && r.status === "cancelled" && r.error_stage === "cancelled" && r.upstream_status === "completed" && r.observation_status === "complete" && r.usage_completeness === "complete" && report.cli.final_reply_matches_tool;
  report.cli_terminal_close_request_ids = records.filter(cliTerminalClose).map(r => r.id);
  report.passed = report.cases.every(r => r.passed) && exitCode === 0 && report.cli.tool_success_observed && report.cli.final_reply_matches_tool && records.length >= 8 && records.every(r => (r.status === "succeeded" || cliTerminalClose(r)) && Number.isInteger(r.usage.input_tokens) && Number.isInteger(r.usage.output_tokens)) && report.usage_totals_match_records;
} catch (error) {
  report.error = error.message;
} finally {
  try {
    await browser?.close();
    if (key) await admin(`client-keys/${key.key.id}`, "DELETE", { version: key.key.version });
    if (cliDir) rmSync(cliDir, { recursive: true, force: true });
    if (session) await admin("session", "DELETE");
  } catch (error) { report.cleanup_error = error.message; report.passed = false; }
  mkdirSync(resolve(outputPath, ".."), { recursive: true });
  writeFileSync(outputPath, JSON.stringify(report, null, 2) + "\n");
  console.log(JSON.stringify(report, null, 2));
  if (!report.passed) process.exitCode = 1;
}
