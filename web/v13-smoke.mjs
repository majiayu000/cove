import { chromium, expect } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { createServer } from "node:http";

// Use an isolated Cove instance; fixtures and generated keys never touch the user's instance.
const base = process.env.COVE_TEST_BASE;
if (!base) throw new Error("Set COVE_TEST_BASE to an isolated Cove instance.");
const output = resolve(import.meta.dirname, "../test-results/v13");
mkdirSync(output, { recursive: true });
const browser = await chromium.launch({ headless: true, channel: "chrome" });
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
async function api(path, method = "GET", body) {
  return page.evaluate(
    async ({ path, method, body }) => {
      const r = await fetch(`/admin/${path}`, {
        method,
        credentials: "omit",
        redirect: "error",
        headers: {
          Authorization: `Bearer ${sessionStorage.getItem("cove.management")}`,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
          ...(method !== "GET"
            ? { "X-Cove-Action-Id": crypto.randomUUID() }
            : {}),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error?.message || `HTTP ${r.status}`);
      return data;
    },
    { path, method, body },
  );
}
const upstream = createServer((req, res) => {
  req.resume();
  req.on("end", () => {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({
        id: "resp-v13-ui",
        object: "response",
        status: "completed",
        model: "v13-test-model",
        output: [
          {
            type: "message",
            role: "assistant",
            content: [{ type: "output_text", text: "UI test complete" }],
          },
        ],
        usage: { input_tokens: 10, output_tokens: 4, total_tokens: 14 },
      }),
    );
  });
});
await new Promise((resolve) => upstream.listen(0, "127.0.0.1", resolve));
try {
  await page.goto(base);
  await expect(
    page.getByRole("heading", { name: "概览", exact: true }),
  ).toBeVisible();
  await page.evaluate(() => document.fonts.ready);
  await expect(page.locator(".v13-topbar")).toHaveCSS("height", "60px");
  await expect(page.locator(".v13-page > div")).toHaveCSS("max-width", "860px");
  await expect(page.locator("h1")).toHaveCSS("font-size", "36px");
  const links = [
    ["概览", "概览"],
    ["来源", "来源与账号"],
    ["模型", "模型"],
    ["路由", "路由"],
    ["Keys", "API Keys"],
    ["请求", "请求"],
    ["用量", "用量"],
  ];
  for (const [nav, heading] of links) {
    await page
      .locator("nav")
      .getByRole("button", { name: nav, exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: heading, exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: `${output}/dark-${nav}.png`,
      fullPage: true,
    });
  }
  await page.getByTitle("settings", { exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "设置", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".v13-page > div")).toHaveCSS("max-width", "640px");
  await page.screenshot({
    path: `${output}/dark-settings.png`,
    fullPage: true,
  });
  await page.getByTitle("theme", { exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("data-mode", "light");
  await page.getByTitle("language", { exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Settings", exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-mode", "light");
  await expect(page.locator("html")).toHaveAttribute("data-lang", "en");
  await page.getByTitle("language", { exact: true }).click();
  await page.getByTitle("theme", { exact: true }).click();
  await page.keyboard.press("Meta+k");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await palette.locator("input").fill("来源");
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: "来源与账号", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "add 添加来源", exact: true }).click();
  const chooser = page.getByRole("dialog", { name: "添加来源", exact: true });
  await expect(chooser).toBeVisible();
  await page.screenshot({ path: `${output}/source-presets.png` });
  await chooser
    .getByRole("button", { name: "Ollama 本地，无需 Key", exact: true })
    .click();
  const sourceDialog = page.getByRole("dialog", { name: "来源编辑" });
  await expect(sourceDialog).toBeVisible();
  await sourceDialog
    .getByLabel("来源名称", { exact: true })
    .fill("v13-local-test");
  await sourceDialog
    .getByLabel("模型标识", { exact: true })
    .fill("v13-test-model");
  await sourceDialog
    .getByRole("button", { name: "保存来源", exact: true })
    .click();
  await expect(sourceDialog).not.toBeVisible();
  await expect(
    page.getByRole("switch", { name: "v13-local-test 停用", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("switch", { name: "v13-local-test 停用", exact: true })
    .click();
  await expect(
    page.getByRole("switch", { name: "v13-local-test 启用", exact: true }),
  ).toHaveAttribute("aria-checked", "false");
  await page
    .getByRole("switch", { name: "v13-local-test 启用", exact: true })
    .click();
  await expect(
    page.getByRole("switch", { name: "v13-local-test 停用", exact: true }),
  ).toHaveAttribute("aria-checked", "true");

  // The subscription preset must lead to a visible real OAuth action, even
  // before a model is selected. Intercept only the external authorization page;
  // the login operation and cancellation use Cove's actual backend.
  await page.getByRole("button", { name: "add 添加来源", exact: true }).click();
  await chooser.getByRole("button", { name: "ChatGPT 订阅 OAuth 登录", exact: true }).click();
  await expect(sourceDialog.getByLabel("来源名称", { exact: true })).toHaveValue("ChatGPT 订阅");
  await expect(sourceDialog.getByLabel("模型标识", { exact: true })).toHaveValue("");
  await sourceDialog.getByRole("button", { name: "保存来源", exact: true }).click();
  await expect(sourceDialog).not.toBeVisible();
  const subscription = (await api("sources")).find((s) => s.kind === "codex_subscription");
  const signIn = page.getByRole("button", { name: "登录 ChatGPT", exact: true });
  await expect(signIn).toBeVisible();
  await context.route("https://auth.openai.com/oauth/authorize?*", (route) => route.fulfill({
    contentType: "text/html",
    body: "<h1>Isolated authorization page</h1>",
  }));
  const popupOpened = page.waitForEvent("popup");
  await signIn.click();
  const popup = await popupOpened;
  await popup.waitForURL("https://auth.openai.com/oauth/authorize?*");
  const authorization = new URL(popup.url());
  expect(authorization.searchParams.get("code_challenge_method")).toBe("S256");
  expect(authorization.searchParams.get("code_challenge")).toBeTruthy();
  expect(authorization.searchParams.get("state")).toBeTruthy();
  expect(authorization.searchParams.get("scope")).toContain("offline_access");
  expect(await popup.evaluate(() => window.opener)).toBeNull();
  await expect(page.getByRole("button", { name: "正在登录…", exact: true })).toBeDisabled();
  await expect(page.getByRole("link", { name: "继续授权 ↗", exact: true })).toHaveAttribute("href", popup.url());
  expect((await api(`sources/${subscription.id}/login`)).status).toBe("pending");
  await page.screenshot({ path: `${output}/subscription-login.png`, fullPage: true });
  await page.getByRole("button", { name: "取消登录", exact: true }).click();
  await expect(signIn).toBeEnabled();
  expect((await api(`sources/${subscription.id}/login`)).status).toBe("cancelled");
  await popup.close();

  // A blocked popup still exposes the authorization link and can be cancelled.
  await page.evaluate(() => { window.originalOpen = window.open; window.open = () => null; });
  await signIn.click();
  await expect(page.getByRole("link", { name: "继续授权 ↗", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "取消登录", exact: true }).click();
  await expect(signIn).toBeEnabled();
  await page.evaluate(() => { window.open = window.originalOpen; delete window.originalOpen; });

  // Rejected login requests retain the backend error and close the blank popup.
  const loginEndpoint = `**/admin/sources/${subscription.id}/login`;
  await context.route(loginEndpoint, (route) => route.request().method() === "POST"
    ? route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: { message: "OAuth 登录测试冲突" } }) })
    : route.continue());
  const rejectedPopupOpened = page.waitForEvent("popup");
  await signIn.click();
  const rejectedPopup = await rejectedPopupOpened;
  await expect.poll(() => rejectedPopup.isClosed()).toBe(true);
  await expect(page.getByRole("alert").filter({ hasText: "OAuth 登录测试冲突" })).toBeVisible();
  await context.unroute(loginEndpoint);
  await context.unroute("https://auth.openai.com/oauth/authorize?*");
  const sources = await api("sources"),
    source = sources.find((s) => s.name === "v13-local-test");
  const mockSource = await api("sources", "POST", {
    name: "v13-test-upstream",
    kind: "none",
    provider: "local",
    account_id: source.account_id,
    models: ["v13-test-model"],
    base_url: `http://127.0.0.1:${upstream.address().port}/v1`,
    native_protocol: "responses",
  });
  await page.reload();
  await page
    .locator("nav")
    .getByRole("button", { name: "来源", exact: true })
    .click();
  await page
    .getByText("v13-test-upstream", { exact: false })
    .first()
    .locator("..")
    .locator("..")
    .getByTitle("latency test", { exact: true })
    .click();
  const drawer = page.getByRole("dialog", { name: "请求详情", exact: true });
  await expect(drawer).toBeVisible();
  await expect(
    drawer.getByText("v13-test-model", { exact: false }).first(),
  ).toBeVisible();
  await page.screenshot({ path: `${output}/request-drawer.png` });
  await page.keyboard.press("Escape");
  await expect(drawer).not.toBeVisible();
  await api(`sources/${mockSource.id}/test`, "POST", { model: "v13-test-model" });
  const firstPage = await api("requests?limit=1");
  if (!firstPage.next_cursor) throw new Error("Request pagination fixture has no second page");
  const secondPage = await api(`requests?limit=1&cursor=${encodeURIComponent(firstPage.next_cursor)}`);
  // Reduce only the page size; rows, cursors and pagination still use the real isolated backend.
  const requestsURL = /\/admin\/requests(?:\?.*)?$/;
  await page.route(requestsURL, async route => {
    const url = new URL(route.request().url());
    url.searchParams.set("limit", "1");
    await route.fulfill({ response: await route.fetch({ url: url.href }) });
  });
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "请求", exact: true }).click();
  await expect(page.getByRole("button").filter({ hasText: firstPage.items[0].id })).toBeVisible();
  await expect(page.getByRole("button").filter({ hasText: secondPage.items[0].id })).toHaveCount(0);
  await page.getByRole("button", { name: "加载更多请求", exact: true }).click();
  await expect(page.getByRole("button").filter({ hasText: firstPage.items[0].id })).toBeVisible();
  await expect(page.getByRole("button").filter({ hasText: secondPage.items[0].id })).toBeVisible();
  await expect(page.getByRole("button", { name: "加载更多请求", exact: true })).toHaveCount(0);
  await page.unroute(requestsURL);
  await api("models", "POST", {
    source_id: source.id,
    upstream_model: "v13-ui-extra",
    display_name: "UI test model",
  });
  await page.reload();
  await page
    .locator("nav")
    .getByRole("button", { name: "模型", exact: true })
    .click();
  await expect(page.getByText("v13-ui-extra", { exact: true })).toBeVisible();
  await page.getByPlaceholder("model id").fill("does-not-exist");
  await expect(page.getByText("没有匹配的模型", { exact: true })).toBeVisible();
  await page.getByPlaceholder("model id").fill("");
  await page
    .locator("nav")
    .getByRole("button", { name: "Keys", exact: true })
    .click();
  await page.getByRole("button", { name: "add 创建 Key", exact: true }).click();
  const manager = page.getByRole("dialog", { name: "API Keys管理" });
  await expect(manager).toBeVisible();
  await manager.getByLabel("Key 名称", { exact: true }).fill("v13-test-key");
  await manager
    .locator("select[name=target]")
    .selectOption(`source:${mockSource.id}`);
  const budget = await api("budgets", "POST", {
    name: "v13-existing-budget", scope: { kind: "instance" }, currency: "USD",
    amount_limit: "100", mode: "soft", period: { kind: "calendar_month", timezone: "UTC" },
  });
  // Reload the editor's budget choices after creating the isolated fixture.
  await manager.getByRole("button", { name: "关闭管理", exact: true }).click();
  await page.getByRole("button", { name: "add 创建 Key", exact: true }).click();
  await manager.getByLabel("Key 名称", { exact: true }).fill("v13-test-key");
  await manager.locator("select[name=target]").selectOption(`source:${mockSource.id}`);
  await manager.getByText("预算设置", { exact: true }).click();
  await manager.locator("select[name=key_budget_choice]").selectOption("existing");
  await manager.locator("select[name=budget_id]").selectOption(budget.id);
  await manager
    .getByRole("button", { name: "创建 API Key", exact: true })
    .click();
  await expect(manager.locator(".secret")).toBeVisible();
  const clientKey = await manager.locator(".secret code").textContent();
  const createdKey = (await api("client-keys")).find((k) => k.name === "v13-test-key");
  expect(createdKey.budget_id).toBe(budget.id);
  await page.screenshot({
    path: `${output}/create-key.png`,
    mask: [manager.locator(".secret code")],
  });
  await manager
    .getByRole("button", { name: "已保存，隐藏", exact: true })
    .click();
  await expect(manager.getByRole("heading", { name: "接入 · v13-test-key", exact: true })).toBeVisible();
  await expect(manager.getByLabel("使用模型")).toHaveValue("v13-test-model");
  await expect(manager.getByText("尚未观察到该 Key 的请求。创建 Key 不代表客户端已经接入。", { exact: true })).toBeVisible();
  const generatedExample = manager.locator("pre").filter({ hasText: "/v1/responses" }).first();
  await expect(generatedExample).toContainText(base);
  const callGateway = () => page.evaluate(async ({ clientKey }) => {
    const r = await fetch("/v1/responses", {
      method: "POST", headers: { Authorization: `Bearer ${clientKey}`, "Content-Type": "application/json" },
      body: JSON.stringify({ model: "v13-test-model", input: "isolated key acceptance", stream: false }),
    });
    return { status: r.status, error: r.ok ? "" : (await r.json()).error?.message };
  }, { clientKey });
  const unpriced = await callGateway();
  expect(unpriced.status).toBe(422);
  expect(unpriced.error).toContain("没有有效的用户配置价格");
  const pricedModel = (await api(`models?source_id=${mockSource.id}`)).items.find((m) => m.upstream_model === "v13-test-model");
  const priceTime = new Date(Date.now() - 60_000).toISOString();
  await api("prices", "POST", {
    model_id: pricedModel.id, currency: "USD", effective_at: priceTime,
    units: [{ dimension: "input_token", amount: "1", per: "1000000" }, { dimension: "output_token", amount: "2", per: "1000000" }],
    provenance: { kind: "manual", observed_at: priceTime },
  });
  const gatewayStatus = await callGateway();
  expect(gatewayStatus.status, gatewayStatus.error).toBe(200);
  await expect.poll(async () => (await api("client-keys")).find((k) => k.id === createdKey.id).last_seen_at).toBeTruthy();
  await manager.getByRole("button", { name: "关闭管理", exact: true }).click();
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "Keys", exact: true }).click();
  await expect(page.locator(".secret")).toHaveCount(0);
  await expect(page.getByText("v13-test-key", { exact: false })).toBeVisible();
  await page.getByTitle("Revoke", { exact: true }).click();
  await expect(page.getByText("已撤销", { exact: true })).toBeVisible();
  await page
    .locator("nav")
    .getByRole("button", { name: "路由", exact: true })
    .click();
  await expect(page.getByText("请先创建 coding 公开模型名", { exact: true })).toBeVisible();
  const paidFallback = page.getByRole("switch", { name: "订阅用完后使用付费 API", exact: true });
  const beforeScheduling = await api("settings");
  await expect(paidFallback).toBeEnabled();
  await paidFallback.click();
  await expect(paidFallback).toHaveAttribute("aria-checked", String(!beforeScheduling.limits.allow_paid_fallback));
  await expect.poll(async () => (await api("settings")).limits.allow_paid_fallback).toBe(!beforeScheduling.limits.allow_paid_fallback);
  await page.getByRole("button", { name: "20%", exact: true }).click();
  await expect.poll(async () => (await api("settings")).limits.subscription_quota_threshold).toBe(20);
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "路由", exact: true }).click();
  await expect(paidFallback).toHaveAttribute("aria-checked", String(!beforeScheduling.limits.allow_paid_fallback));
  await expect(page.getByRole("button", { name: "20%", exact: true })).toHaveAttribute("aria-pressed", "true");
  // A local unauthenticated source remains eligible when paid fallback is off.
  // Use a separate API-key account to exercise the paid fallback boundary.
  const paidAccount = await api("accounts", "POST", {
    provider: "openai_compatible", auth_type: "api_key", name: "v13-paid-fixture",
  });
  await api(`accounts/${paidAccount.id}/credential`, "POST", {
    version: paidAccount.version, secret: "v13-synthetic-paid-secret",
  });
  const paidSource = await api("sources", "POST", {
    name: "v13-paid-upstream", kind: "api_key", provider: "openai_compatible",
    account_id: paidAccount.id, models: ["v13-test-model"],
    base_url: `http://127.0.0.1:${upstream.address().port}/v1`, native_protocol: "responses",
  });
  const routeModel = (await api(`models?source_id=${paidSource.id}`)).items.find((m) => m.upstream_model === "v13-test-model");
  const codingRoute = await api("routes", "POST", {
    name: "v13-coding", strategy: "priority", max_attempts: 1,
    members: [{ model_id: routeModel.id, priority: 0, weight: 1 }],
  });
  await api("model-aliases", "POST", { public_model: "coding", route_id: codingRoute.id });
  if ((await api("settings")).limits.allow_paid_fallback) {
    await paidFallback.click();
    await expect(paidFallback).toHaveAttribute("aria-checked", "false");
  }
  await page.reload();
  await page.locator("nav").getByRole("button", { name: "路由", exact: true }).click();
  const nextRequest = page.getByText("下一次 coding 请求", { exact: true }).locator("..").locator("..");
  await expect(nextRequest.getByText("没有可用候选，请查看排除原因", { exact: true }).first()).toBeVisible();
  await paidFallback.click();
  await expect(nextRequest.getByText("v13-paid-upstream · v13-test-model", { exact: true })).toBeVisible();
  await expect(page.getByText("0/∞", { exact: true }).first()).toBeVisible();
  await page.screenshot({ path: `${output}/subscription-scheduling.png` });
  await page.keyboard.press("Meta+k");
  await palette.locator("input").fill("工具");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog", { name: "工具管理" })).toBeVisible();
  await page.getByRole("button", { name: "关闭管理", exact: true }).click();
  for (const [nav] of links) {
    await page
      .locator("nav")
      .getByRole("button", { name: nav, exact: true })
      .click();
    await page.setViewportSize({ width: 390, height: 844 });
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    );
    if (overflow) throw new Error(`Mobile page overflow: ${nav}`);
    await page.screenshot({
      path: `${output}/mobile-${nav}.png`,
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 1000 });
  }
  if (errors.length) throw new Error(errors.join("\n"));
  console.log(
    "PASS: 8 pages, v13 layout geometry, theme/language persistence, command palette, source presets/save/toggle, subscription OAuth initiation/reopen/cancel/popup-block/error, model search, isolated mock-upstream request/drawer/pagination, key creation/existing-budget/guide/last-use/revoke, unpriced budget rejection, persisted subscription scheduling, coding preview, account occupancy, management access, mobile overflow, no page errors.",
  );
} finally {
  await context.close();
  await browser.close();
  await new Promise((resolve) => upstream.close(resolve));
}
