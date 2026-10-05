const STATE = "cove.roo.profile";
const PROFILE = "Cove";

function errorMessage(error, secret = "") {
  let message = error instanceof Error ? error.message : String(error);
  if (secret) message = message.split(secret).join("[redacted]");
  return message.replace(/(?:sk-|cove_)[A-Za-z0-9_*.-]+/g, "[redacted]");
}

async function connect(roo, state, { baseUrl, model, apiKey }) {
  baseUrl = String(baseUrl || "").trim().replace(/\/+$/, "");
  model = String(model || "").trim();
  const url = new URL(baseUrl);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash)
    throw new Error("请填写不含凭据、查询参数或片段的 HTTP/HTTPS Base URL。");
  if (!model || typeof apiKey !== "string" || !apiKey.trim()) throw new Error("模型 ID 和 Cove Key 不能为空。");
  if (state.get(STATE) || roo.getProfiles().includes(PROFILE))
    throw new Error("已有 Cove profile 或未恢复的连接记录；请先恢复，不会覆盖原配置。");
  const previous = roo.getActiveProfile();
  if (!previous) throw new Error("请先在 Roo 选中原 profile，再连接 Cove。");
  let profileId;
  try {
    profileId = await roo.createProfile(PROFILE, {
      apiProvider: "openai", openAiBaseUrl: baseUrl, openAiModelId: model, openAiApiKey: apiKey,
    }, true);
    try {
      await state.update(STATE, { previous, profileId, baseUrl, model });
    } catch (error) {
      try {
        if (roo.getActiveProfile() === PROFILE) await roo.setActiveProfile(previous);
        if (roo.getProfileEntry(PROFILE)?.id === profileId) await roo.deleteProfile(PROFILE);
      } catch (rollback) {
        throw new Error("恢复记录保存失败，原生回滚也未完成；请在 Roo 切回 " + previous + " 并检查 Cove profile。" + errorMessage(rollback, apiKey));
      }
      throw new Error("恢复记录保存失败，新建 profile 已撤回。" + errorMessage(error, apiKey));
    }
    return { profile: PROFILE, previous, baseUrl, model };
  } catch (error) {
    throw new Error(errorMessage(error, apiKey));
  }
}

async function restore(roo, state, confirmDelete) {
  const record = state.get(STATE);
  if (!record) throw new Error("没有由 Cove 保存的连接记录；未修改 Roo。");
  const entry = roo.getProfileEntry(PROFILE);
  if (entry && entry.id !== record.profileId) throw new Error("Cove profile 已被重新创建，保留当前配置，请在 Roo 手动处理。");
  if (!roo.getProfiles().includes(record.previous)) throw new Error("原 profile 已移除，保留当前配置，请先在 Roo 选择恢复目标。");
  if (entry && !(await confirmDelete(record))) return { restored: false };
  if (roo.getActiveProfile() === PROFILE) await roo.setActiveProfile(record.previous);
  if (entry) await roo.deleteProfile(PROFILE);
  await state.update(STATE, undefined);
  return { restored: true, activeProfile: roo.getActiveProfile() };
}

function activate(context) {
  const vscode = require("vscode");
  let busy = false;
  async function run(work) {
    if (busy) throw new Error("Cove profile 操作正在进行。");
    busy = true;
    try {
      const extension = vscode.extensions.getExtension("RooVeterinaryInc.roo-cline");
      if (!extension || extension.packageJSON.version !== "3.53.0") throw new Error("当前适配仅核验 Roo Code 3.53.0，请确认已安装该版本。");
      return await work(await extension.activate());
    } catch (error) {
      const message = errorMessage(error);
      void vscode.window.showErrorMessage(message);
      throw new Error(message);
    } finally { busy = false; }
  }
  context.subscriptions.push(vscode.commands.registerCommand("cove.roo.connect", (input) => run(async (roo) => {
    const baseUrl = input?.baseUrl ?? await vscode.window.showInputBox({ title: "Cove Base URL", prompt: "填写 Cove 的 OpenAI-compatible Base URL（包含 /v1）", ignoreFocusOut: true });
    if (baseUrl === undefined) return;
    const model = input?.model ?? await vscode.window.showInputBox({ title: "Cove Model ID", prompt: "填写所选 Cove Key 允许的公开模型 ID", ignoreFocusOut: true });
    if (model === undefined) return;
    const apiKey = input?.apiKey ?? await vscode.window.showInputBox({ title: "Cove Key", prompt: "只提供 Cove Key；由 Roo 自己保存在私有凭据存储", password: true, ignoreFocusOut: true });
    if (apiKey === undefined) return;
    const result = await connect(roo, context.globalState, { baseUrl, model, apiKey });
    void vscode.window.showInformationMessage("已创建并选中独立 Cove profile；连接和模型能力需实际调用验收。");
    return result;
  })));
  context.subscriptions.push(vscode.commands.registerCommand("cove.roo.restore", () => run(async (roo) => {
    const result = await restore(roo, context.globalState, async (record) => {
      const choice = await vscode.window.showWarningMessage(
        "删除 Cove profile 及其中凭据？Roo 的公开 API 无法比较后来改过的 Key；请确认此 profile 可以删除。原 profile：" + record.previous,
        { modal: true }, "删除 Cove profile",
      );
      return choice === "删除 Cove profile";
    });
    if (result.restored) void vscode.window.showInformationMessage("Cove profile 已移除，保留其他 profile 和用户当前选择。");
    return result;
  })));
}

module.exports = { activate, connect, restore, errorMessage };
