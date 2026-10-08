package app

// These clients are discoverable, but have no verified automatic model writer.
// Keep their official setup and unresolved contracts visible without offering
// a file mutation that the client may ignore or that could replace its login.
func manualClientCards() []clientCard {
	return []clientCard{
		{Kind: "qodercli", Name: "Qoder CLI", Executable: "qodercli", ManualSetup: "在 /model → Custom → Add custom model 中配置。供应商、模型和凭据字段以当前账号向导为准；官方明确不应手写 settings.json 配置 BYOK。Skills 可使用配置扩展页的 Qoder CLI 卡。", DocsURL: "https://docs.qoder.com/cli/custom-models"},
		{Kind: "cursor-agent", Name: "Cursor Agent CLI", Executable: "cursor-agent", ManualSetup: "CURSOR_API_KEY 用于 Cursor 账号认证，不能填 Cove Key。当前尚未核验 CLI 自定义模型端点。项目 MCP/Skills 可使用 Cursor 配置扩展卡；模型调用需单独验收。", DocsURL: "https://cursor.com/docs/cli/reference/parameters"},
		{Kind: "cursor", Name: "Cursor 内置 Agent", AppBundle: "Cursor.app", ManualSetup: "在 Models 设置中配置 API Key 与自定义 Base URL。Key 会经 Cursor 后端转发，需先确认凭据目的地；内置模型真实调用尚未验收。Roo 使用独立 Cove 原生适配器。", DocsURL: "https://cursor.com/help/models-and-usage/api-keys"},
		{Kind: "qoder", Name: "Qoder 桌面 Agent", AppBundle: "Qoder.app", ManualSetup: "Settings → Models → Add，选择 Custom 下的 OpenAI Compatible，使用 Cove 的 /v1 地址、模型 ID 和专用 Key；根据模型实际元数据填写上下文能力，再 Validate and Add Model。", DocsURL: "https://docs.qoder.com/qoder/custom-models"},
		{Kind: "zed", Name: "Zed Agent", AppBundle: "Zed.app", ManualSetup: "运行 agent: open settings，在 LLM Providers 中 Add Provider，添加 OpenAI-compatible 地址、模型 ID 和实际上下文窗口。provider ID 使用 cove，Key 通过官方界面保存到钥匙串，或仅向 Zed 进程传入 COVE_API_KEY。", DocsURL: "https://zed.dev/docs/ai/use-api-access"},
		{Kind: "zcode", Name: "ZCode Agent", AppBundle: "ZCode.app", ManualSetup: "从模型选择器底部 Manage Models 打开 Model Settings，添加 OpenAI 或 Anthropic 兼容渠道，填写 Cove 地址、模型 ID 与专用 Key。尚未核验的配置文件不自动改写。", DocsURL: "https://zcode.z.ai/en/docs/configuration"},
		{Kind: "antigravity", Name: "Antigravity", AppBundle: "Antigravity.app", ManualSetup: "保留官方登录。自定义模型端点尚未核验，Cove 独立订阅授权仍未闭合；不将官方登录凭据复制为 Cove 来源。MCP 与模型接入分别验收。", DocsURL: "https://antigravity.google/docs/models"},
		{Kind: "warp", Name: "Warp Agent", AppBundle: "Warp.app", ManualSetup: "在 Settings 搜索 inference endpoint，添加公网 Cove 的 /v1 地址、模型 ID 和专用 Key，再明确选择该模型。官方说明请求及 Key 会经 Warp 后端转发，localhost 不可用，使用前需确认凭据目的地。本机版本的入口和真实工具链仍待验收。", DocsURL: "https://docs.warp.dev/agents/inference/custom-inference-endpoint/"},
		{Kind: "warp-celestial", Name: "Warp Celestial", AppBundle: "Warp Celestial.app", ManualSetup: "与 Warp 正式版分别盘点；自定义模型端点和配置合同尚未核验，暂不自动修改。"},
		{Kind: "workbuddy", Name: "WorkBuddy", AppBundle: "WorkBuddy.app", ManualSetup: "已纳入本机接入盘点；自定义模型端点、凭据存储和工具链仍待核验，暂不自动修改。"},
		{Kind: "kimi", Name: "Kimi 桌面", AppBundle: "Kimi.app", ManualSetup: "桌面应用与 Kimi API 来源分别管理；当前未核验桌面自定义模型端点，不读取或转移已有登录。"},
		{Kind: "claude-desktop", Name: "Claude 桌面", AppBundle: "Claude.app", ManualSetup: "桌面应用与 Claude Code 分别验收；保留官方登录，当前未核验可连接 Cove 的自定义模型端点。"},
		{Kind: "gemini-desktop", Name: "Gemini 桌面", AppBundle: "Gemini.app", ManualSetup: "桌面应用与 Gemini CLI 分别验收；当前未核验桌面自定义模型端点，不转移 Google 登录凭据。"},
		{Kind: "grok-bot", Name: "Grok Bot", AppBundle: "Grok Bot.app", ManualSetup: "Grok Bot 与 Grok Build CLI 分别验收；Bot 的自定义端点尚未核验，CLI 可使用 Grok Build 配置卡。"},
		{Kind: "chatgpt-desktop", Name: "ChatGPT / Codex 桌面", AppBundle: "ChatGPT.app", ManualSetup: "桌面应用与 Codex CLI 分别验收；当前仅 CLI 配置卡已有真实工具证据，不自动改动桌面账号或共享登录目录。"},
		{Kind: "dsh-desk", Name: "DSH Desk", AppBundle: "DSH Desk.app", ManualSetup: "已纳入本机接入盘点；模型配置接口和凭据保存方式仍待核验，暂不自动修改。"},
		{Kind: "dethink", Name: "Dethink", AppBundle: "Dethink.app", ManualSetup: "已纳入本机接入盘点；自定义供应商配置和 Agent 工具链仍待核验，暂不自动修改。"},
		{Kind: "mirasim", Name: "Mirasim", AppBundle: "Mirasim.app", ManualSetup: "已纳入本机 AI 应用盘点；模型配置、代理工具链与 API 入口尚未核验，暂不自动修改。"},
		{Kind: "otty", Name: "Otty", AppBundle: "Otty.app", ManualSetup: "已纳入本机 AI 应用盘点；自定义模型配置与 API 入口尚未核验，暂不自动修改。"},
		{Kind: "chatgpt-classic", Name: "ChatGPT Classic", AppBundle: "ChatGPT Classic.app", ManualSetup: "与 ChatGPT.app 及 Codex CLI 分别盘点；当前未核验自定义模型端点，不改动现有登录。"},
	}
}
