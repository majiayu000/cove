# 本机 Agent 接入

2026-10-08：客户端配置写入、客户端原生读取、真实模型调用分别记录。检测到应用或显示接入说明，不表示它已经能通过 Cove 调用模型。实施状态统一见 [实施台账](implementation-readiness.tsv) 的 R074、R075、R079、R098。

## Grok Build

本机 Grok Build 1.0.46 已加入客户端配置页。这里是让 Grok Build 使用 Cove 提供的模型，与把 SuperGrok 订阅授权给 Cove 分别验收；不会读取或复制官方登录。

真实验收通过：现有 New API 的 gpt-5.6-sol 共 5 次 Chat 请求全部成功、用量完整，覆盖 MCP 执行/结果回传、Skill 加载和正确最终回复。测试明确选择 Chat 后端，并将地址指向已批准来源的有界转发入口；生产配置的来源 Key 协议选择另由 API 测试验证。临时 Key 已撤销并验证 401，配置恢复、隔离目录删除。

1. 准备一个独立目录作为 `GROK_HOME`，在 Cove 的客户端配置页选择 Grok Build、用户作用域。
2. 选择绑定目标来源的 Cove Key，填写该目录及其 `config.toml`、公开模型 ID，先预览再应用。Chat Completions 来源选择原生 `chat_completions`，其他来源默认 `responses`；所选 Key 不允许该协议时阻止应用。未选 Key 或使用路由 Key 时，仍需核对实际协议。
3. 通过当前进程环境提供 `GROK_HOME` 和 `COVE_API_KEY`，再运行 `grok inspect`、`grok models` 核对。Key 不写入配置、全局 shell 或 `.env`。
4. 在扩展页配置用户 MCP/Skills 时，授权目录同样选这个 `GROK_HOME`：MCP 文件为 `config.toml`，技能为 `skills/<name>/SKILL.md`。项目扩展仍使用 `.grok/config.toml` 和 `.grok/skills`。
5. 在客户端自己的批准流程中验证 MCP。HTTP MCP 使用 `headers` 和可选的 `bearer_token_env_var`；不要用 Codex 的 `http_headers` 字段。

配置只改 `models.default` 和 `model.cove` 中列出的字段。辅助模型、其他模型、MCP、注释和官方登录保留；并发改动阻止应用，后来改过的字段在恢复时由用户选择处理。项目配置不能设置 Grok 模型。配置解析通过之后还要做真实调用验收。

[官方 Settings](https://docs.x.ai/build/settings) · [Settings Reference](https://docs.x.ai/build/settings/reference) · [MCP](https://docs.x.ai/build/features/mcp-servers)

## 其他本机客户端

| 客户端 | Cove 当前接入方式与边界 |
|---|---|
| Codex CLI | 已有模型配置与 MCP/Skills；本机 0.160.1 新增原生合成工具检查，历史真实证据保留 |
| Claude Code / OpenCode / Cline CLI / Continue / Roo | 沿用已实现的专用适配；具体版本、宿主和真实验收边界见实施台账，CLI 与 IDE 扩展分别记录 |
| Qoder CLI 1.1.12 | 已补 MCP/Skills 文件适配并验证原生发现；模型必须走 `/model → Custom → Add custom model`，以当前账号的供应商目录为准，不能手写 BYOK settings.json；实际 MCP 执行和模型向导待验 |
| Qoder 桌面 | 官方 Settings → Models → Add → Custom → OpenAI Compatible；本机原生设置和真实调用待验 |
| Zed | 官方 `agent: open settings` → LLM Providers → Add Provider；填实际模型上下文，Key 使用官方钥匙串或进程环境；本机设置和真实调用待验 |
| ZCode | 模型选择器 → Manage Models → Model Settings，支持 OpenAI/Anthropic 兼容渠道；本机设置和真实调用待验 |
| Warp | 官方 Settings 搜索 inference endpoint；要求公网 HTTPS 的 Chat Completions 入口，不能直接连 loopback。Key 每次随请求经过 Warp 后端；尚未配置或批准此凭据目的地，本机入口也待验 |
| Cursor 内置 Agent | 官方模型设置与 Key/Base URL；请求经过 Cursor 后端，凭据目的地需另行确认。Cursor 中的 Roo 扩展真实验收不代表内置 Agent 已验 |
| Cursor Agent CLI | Cursor 账号 Key 不是 Cove Key；CLI 自定义模型端点尚未核验。MCP/Skills 使用 Cursor 文件合同 |
| Antigravity / Warp Celestial | 已盘点；模型端点合同尚未核验，保留官方登录 |
| Grok Bot | 与 Grok Build CLI 分别盘点；Bot 端点尚未核验 |
| Kimi / Claude / Gemini / ChatGPT Classic 桌面 | 与同名 API 或 CLI 分别盘点；自定义端点尚未核验，不转移登录 |
| ChatGPT.app（bundle ID 为 com.openai.codex） | 按独立桌面应用盘点；Codex CLI 的配置验收不能外推到桌面 |
| WorkBuddy / DSH Desk / Dethink / Mirasim / Otty | 已盘点应用包；端点、凭据和 Agent 工具链尚未核验，说明卡不提供自动写入 |

Gemini CLI 不在本轮当前 PATH；既有配置卡保留，旧的上游 422 不作成功证据。清单根据顶层应用目录和已知 CLI 名称盘点，不扫描或搬运各客户端的秘密文件。

[Qoder CLI 模型](https://docs.qoder.com/cli/custom-models) · [Qoder MCP](https://docs.qoder.com/cli/mcp-reference) · [Qoder 桌面模型](https://docs.qoder.com/qoder/custom-models) · [Zed API](https://zed.dev/docs/ai/use-api-access) · [ZCode 配置](https://zcode.z.ai/en/docs/configuration) · [Warp 自定义端点](https://docs.warp.dev/agents/inference/custom-inference-endpoint/) · [Cursor API Key](https://cursor.com/help/models-and-usage/api-keys)

本轮最终验收入口为 `test-results/local-agents-20261008/execution.json`。原生 CLI 合成检查、浏览器文件操作与真实来源证据分列；真实模型用既有 New API 的 gpt-5.6-sol，不代表 xAI 订阅或其他供应商资格通过。Computer Use 禁止操作 Warp，Qoder 窗口请求超时；这些原生界面项保留待验。Cloudflare 按用户要求暂缓。

参考了 CC Switch、CLIProxyAPI、sub2api、Quotio、cliproxy-api-provider 的固定版本相关源码，按官方配置合同独立实现，未复制竞品源码或 OAuth 注册身份。
