# Cove for Roo Code

通过 Roo Code **3.53.0 官方 profile API** 接入 Cove。适配器只创建独立 `Cove` profile，已有同名项时拒绝覆盖；不直接访问 VS Code 状态数据库、Roo SecretStorage 或原 provider Key。

在此目录打包 VSIX：`npx @vscode/vsce package`。在 VS Code 的“扩展：从 VSIX 安装”选择生成的文件；安装 Roo Code 3.53.0 后，从命令面板运行 **Cove: Connect Roo Code**，填写 Cove Base URL（包含 `/v1`）、公开模型 ID 和 Cove Key。Key 由 Roo 官方 API 存入 Roo 的私有凭据存储，适配器的恢复记录只保存原 profile 名、所建 profile ID、Base URL 和模型 ID。

连接命令完成只证明 profile 已创建并选中，不证明提供方资格、额度、价格、上下文上限或工具成功；未填写这些未知模型元数据。实际支持的模型和操作以 Cove 来源验收为准。

运行 **Cove: Restore Roo Code Profile** 恢复。若当前仍选中 Cove，切回原 profile；用户已选其他 profile 时保留该选择。删除 Cove profile 前需要确认，因为 Roo 的公开 API 不提供其中 Key 的读取比较，适配器不能判断用户后来是否换过 Key。取消确认时保留配置和恢复记录；同名 profile 被重新创建或原 profile 消失时保留当前配置。删除失败保留恢复记录，可重试。

原生宿主集成可调用 `cove.roo.connect` 命令并传入 `{baseUrl, model, apiKey}`，调用方须已有创建该独立 profile 和存储此 Key 的授权。返回值不含 Key。恢复始终要求原生确认。测试使用 `npm test`。

实测范围：macOS arm64、VS Code 1.140.0、官方 Roo 3.53.0，9 项适配器回归及隔离宿主 profile 检查通过；既有 New API `gpt-5.6-sol` 上由模型加载 Skill、执行 stdio MCP 并准确回传，3 次请求成功且用量完整。验收通过 Roo 设置将 `maxWorkspaceFiles` 设为 0，未覆盖默认自动目录扫描、GUI 输入/编辑及最终反馈按钮；测试清理由生产恢复函数执行，删除确认使用用户已批准的可清理 fixture。适配器连接命令本身只写 provider profile，不改这些通用设置。网络测试保留 TLS 校验，使用临时宿主的环境代理，不修改日常 IDE。证据在 `test-results/followup-20261006/roo-tools.json`。
