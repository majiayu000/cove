import { homedir } from "node:os";
import { chromium, request } from "@playwright/test";
import { readFileSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";

// Explicit local smoke check: credentials stay in process memory and masked inputs.
const root = resolve(import.meta.dirname, "..");
const config = JSON.parse(
  readFileSync(resolve(root, "config.example.json"), "utf8"),
);
const base = `http://${config.listen}`;
const dataDir =
  config.data_dir || resolve(homedir(), "Library/Application Support/gatt");
let credential;
try {
  credential = JSON.parse(
    readFileSync(resolve(dataDir, "secrets/credentials.json"), "utf8"),
  ).administrator;
} catch {
  throw new Error("无法读取 Cove 本地管理凭据；请先启动或恢复本实例");
}
if (typeof credential !== "string" || !credential)
  throw new Error("Cove 管理凭据缺失；请先恢复本实例");
const browser = await chromium.launch({ headless: true, channel: "chrome" });
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const nativeTicket = await context.request.post(base + "/admin/browser-tickets", {
    headers: { Authorization: "Bearer " + credential }, maxRedirects: 0,
  });
  if (!nativeTicket.ok()) throw new Error("Native administrator ticket failed");
  const ticket = await nativeTicket.json();
  await page.goto(base + "/#access=" + ticket.ticket);
  await page.getByRole("heading", { name: "你的模型，泊在一处。" }).waitFor();
  const session = await page.evaluate(() => sessionStorage.getItem("cove.management"));
  if (!session || (await context.cookies(base)).length) throw new Error("Management must use explicit bearer, not cookies");
  await page.reload();
  await page.getByRole("heading", { name: "你的模型，泊在一处。" }).waitFor();
  const unsigned = await context.newPage();
  await unsigned.goto(base);
  await unsigned.locator("nav").waitFor();
  await unsigned.close();
  let redirected = false;
  await page.route("http://127.0.0.1:1/**", route => { redirected = true; return route.abort(); });
  await page.route("**/admin/status", route => route.fulfill({ status: 302, headers: { Location: "http://127.0.0.1:1/admin/status" } }));
  const redirectBlocked = await page.evaluate(async () => {
    try {
      await fetch("/admin/status", { credentials: "omit", redirect: "error", headers: { Authorization: "Bearer " + sessionStorage.getItem("cove.management") } });
      return false;
    } catch { return true; }
  });
  await page.unroute("**/admin/status");
  await page.unroute("http://127.0.0.1:1/**");
  if (!redirectBlocked || redirected) throw new Error("Management redirect was followed");
  const management = await request.newContext({ extraHTTPHeaders: { Authorization: "Bearer " + session } });
  const sourceResponse = await management.get(base + "/admin/sources");
  const sources = await sourceResponse.json();
  let source = sources.find(
    (s) => s.kind === "codex_subscription" && s.models.includes("gpt-5.6-luna"),
  );
  if (!source) {
    await page.getByRole("button", { name: "+ 添加来源" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("来源名称").fill("Codex · 个人订阅");
    await dialog.getByLabel("认证方式").selectOption("codex_subscription");
    await dialog.getByLabel("模型标识").fill("gpt-5.6-luna");
    await dialog.getByRole("button", { name: "保存来源" }).click();
    await dialog.waitFor({ state: "hidden" });
    source = (
      await (await management.get(base + "/admin/sources")).json()
    ).find(
      (s) =>
        s.kind === "codex_subscription" && s.models.includes("gpt-5.6-luna"),
    );
  }
  mkdirSync(resolve(root, "test-results"), { recursive: true });
  await page.screenshot({
    path: resolve(root, "test-results/sources-desktop.png"),
    fullPage: true,
  });
  for (const [name, heading] of [
    ["API Keys", "创建和管理 API Key"],
    ["请求", "每一次调用，都有迹可循。"],
    ["用量", "看清已知，也保留未知。"],
    ["设置", "始终在你的掌控之中。"],
  ]) {
    await page
      .locator("nav")
      .getByRole("button", { name, exact: false })
      .click();
    await page.getByRole("heading", { name: heading }).waitFor();
  }
  await page
    .locator("nav")
    .getByRole("button", { name: "来源", exact: false })
    .click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: resolve(root, "test-results/sources-mobile.png"),
    fullPage: true,
  });
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  if (overflow || errors.length)
    throw new Error(
      `UI failed: overflow=${overflow}, page errors=${errors.length}`,
    );
  console.log(
    "Browser checks passed: administrator login, subscription source creation, five pages, 390px layout, no page errors.",
  );
  if (process.argv.includes("--oauth")) {
    await page.setViewportSize({ width: 1440, height: 1000 });
    await context.route("**/oauth/authorize?**", (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/html",
        body: "Synthetic browser authorization landing page",
      }),
    );
    const popupPromise = context.waitForEvent("page");
    await page.getByRole("button", { name: "使用 ChatGPT 登录" }).click();
    const popup = await popupPromise;
    await popup.waitForURL("**/oauth/authorize?**");
    const auth = new URL(popup.url());
    if (
      auth.origin !== config.codex.auth_base_url ||
      auth.searchParams.get("code_challenge_method") !== "S256" ||
      auth.searchParams.get("redirect_uri") !== config.codex.redirect_uri
    )
      throw new Error("Browser OAuth contract failed");
    await management.delete(`${base}/admin/sources/${source.id}/login`, {
      headers: { Origin: base },
    });
    console.log(
      "Browser OAuth UI passed: click opens provider authorization, PKCE and callback match; operation cancelled after synthetic landing.",
    );
  }
  await page.locator("nav").getByRole("button", { name: "设置", exact: false }).click();
  await page.route("**/admin/sources", (route) => route.abort("connectionrefused"));
  await page.getByRole("button", { name: "刷新数据 ↻" }).click();
  await page.getByText("无法连接本机 Cove 服务。请确认服务正在运行，再刷新页面；这不代表账号授权已失效。", { exact: true }).waitFor();
  await page.getByRole("heading", { name: "始终在你的掌控之中。" }).waitFor();
  await page.unroute("**/admin/sources");
  await page.getByRole("button", { name: "刷新数据 ↻" }).click();
  await page.getByText("无法连接本机 Cove 服务。请确认服务正在运行，再刷新页面；这不代表账号授权已失效。", { exact: true }).waitFor({ state: "hidden" });
  console.log("Connection failure and recovery UI passed without signing out.");
  await management.dispose();
  if (page.url().includes("access="))
    throw new Error("One-time ticket retained in URL");
} finally {
  await browser.close();
}
