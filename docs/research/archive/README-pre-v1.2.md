# Cove · 栖港

**你的模型，泊在一处。**

在 macOS 本机管理自己的 AI 来源，让 Codex 等客户端通过 Responses、Chat Completions 或 Messages 入口调用，并查看请求、用量与恢复方式。

**当前为开发预览，不能标记完整 v0.1 发布。** API 与订阅适配的合成回归、真实 Codex CLI 对合成上游的工具循环已通过；真实浏览器订阅授权已成功；真实上游工具循环、跨机器安装与恢复仍需完成。凭据存储已改为本地文件，原钥匙串授权需重新登录。具体证据见 [实施与验收记录](docs/implementation.md)。

## 本机启动

环境：macOS、Go 1.26.2、Node 22.12+、npm、Python 3、Xcode Command Line Tools。生产运行只需生成的二进制，不需要 Node。SQLite 使用 CGO。

```sh
make build
./bin/gatt -config config.example.json serve
```

默认监听 `http://127.0.0.1:5569`。启动前检查该端口空闲；端口占用时明确失败。更改监听地址须同步更新客户端配置。只接受 loopback IP，管理 API 校验准确的 Host 与 Origin。同一数据目录仅允许一个服务进程，改端口也不能重复运行；退出或崩溃后系统自动释放目录锁。

`GET /healthz` 仅报告进程存活；`GET /readyz` 在初始化完成、接纳请求、数据库可用且数据库/凭据故障锁未触发时返回 200，否则返回 503。就绪检查不调用模型，不证明上游账号可用。`open` 命令检查 `/readyz` 后打开管理页。

打开 `http://127.0.0.1:5569` 直接进入管理页，无需设置或输入管理密码。服务启动后自动打开页面，也可手动运行以下命令。服务仍仅监听本机 loopback，管理请求保留准确 Host/Origin 校验和自动短期会话；客户端调用仍须使用独立 API Key。

```sh
./bin/gatt -config config.example.json open
```

默认数据目录为 `~/Library/Application Support/gatt`。管理员凭据和来源秘密保存在 `secrets/credentials.json`，目录权限 `0700`、文件权限 `0600`。启动时载入内存，每次更新先写临时文件、同步并原子替换，再更新内存；数据库只保存摘要与引用。凭据文件是明文，仅靠当前用户的文件权限保护，不使用系统钥匙串。文件损坏时明确失败，不自动覆盖。运行中不要手工编辑该文件。

## 使用

1. 在「来源」添加原生 Responses API 基础地址和模型 ID，保存 API Key；或添加 Codex 订阅来源并点击「使用 ChatGPT 登录」，在自动打开的服务方页面授权，随后通过本机回调自动返回 Cove。
2. 点击「测试调用」。该操作实际生成文本并计量，成功只证明文本路径。函数工具循环必须单独验收。
3. 在「API Keys」选择来源，点击「创建 API Key」。这把 Key 由 Cove 创建，订阅授权或上游凭据由 Cove 在后台使用。完整值只显示一次，设置到客户端启动终端的 `PERSONAL_GATEWAY_KEY`，不能使用上游来源密钥代替。
4. 在 API Keys 页选择模型、复制独立启动命令，运行后按提示隐藏输入客户端 Key，再完成一次工具调用并查看对应记录。独立目录不改日常配置，退出后恢复原终端环境。模板明确关闭 web search、WebSocket 和客户端重试。
5. 需要服务端 `previous_response_id` 的 API 来源，先在「来源管理与计量」执行两轮续接测试。订阅来源只接受完整历史、`stream=true`、`store=false`。

`gpt-5.6-luna` 是本次用户指定的真实订阅验收模型，不是内置的可用性保证。所有来源的模型名都由用户配置，不自动替换模型。

## 当前边界

- 提供 `/v1/models`、`/v1/responses`、`/v1/chat/completions`、`/v1/messages`。三种调用入口共用来源、Key、派发与计量；支持范围见 [三协议合同](docs/api-compatibility.md)。
- Responses 支持文本、客户端 function/custom 工具及命名空间；Chat / Messages 支持文本与 function 工具转换。工具执行在原客户端，网关不执行命令。
- Messages 当前可接原生 Responses API 来源；Codex 订阅尚不支持 Messages 必填的输出 token 上限，因此明确拒绝，不能宣称已可接 Claude Code。
- 支持 API JSON / SSE；订阅仅 SSE。完整历史保留 opaque reasoning、工具 ID、参数、输出顺序和 assistant phase。
- 不支持 WebSocket、compact、服务端托管工具、图像等其他模态；兼容入口不支持签名 thinking、缓存控制等完整厂商语义。长会话 compact 需求仍待专门实测，不能宣传完整 Codex 兼容。
- 每个 Key 固定绑定一个来源。没有自动切源、自动重试、账号池、多用户、充值或分销。
- 取消只终止本地传输，不保证上游计算或收费停止。上游结果、客户端交付、观察完整性分别记录。上游完成但下游写失败仍保留已知用量；超出观察上限而无法识别终态时显示“结果未确认”。
- 用量缺失保留未知，部分用量单列；费用按十进制与币种计算，保留历史价格快照。订阅不计算虚构的逐次账单。原生套餐额度尚无已验证接口，显示未知。
- 账号使用配置中明确标出的官方 Codex 客户端标识进行独立浏览器 OAuth + PKCE 接入；它不是 Cove 自有 OAuth 注册身份。官方文档未证明此实现具备独立第三方产品准入，不能以合成测试作发布依据。

## 检查

```sh
make test
make check
# 构建后，以临时数据目录验证免密码进入、刷新和重启
node web/local-entry-smoke.mjs
# 使用本机安装的 Codex CLI，隔离 HOME/CODEX_HOME，仅连接合成上游
GATT_CODEX_E2E=1 go test ./internal/app -run TestCodexCLIToolLoop -v -count=1
# 构建后，以独立临时目录和合成上游验证真实进程崩溃、备份与恢复（需 Python 3）
python3 scripts/verify-runtime.py
```

测试使用内存假凭据，不读现有 Codex 认证。HTTP 集成测试启动随机 loopback 端口，不使用真实付费模型。

## 停止、备份、恢复

向服务发送 Ctrl-C。停止接纳新请求并取消登录、刷新和维护；主 HTTP 服务最多等待 5 秒，再取消剩余传输。随后最多等待 5 秒，让请求记账、OAuth 监听/回调、共享刷新和维护任务全部结束，再关闭数据库；超时明确报错退出，不在任务仍可能回写时主动关闭数据库。下次启动将未结束记录标为 `interrupted`，不补发。

完全停止后复制数据目录；不要复制正在写入的 SQLite 文件。`secrets` 目录包含真实凭据，完整数据目录备份也包含这些秘密，必须妥善保管；只备份数据库时，恢复后需要重新配置 API 凭据或登录。管理页面无需密码，也不依赖凭据文件中的原生管理员秘密。仅供本机自动化工具使用的管理凭据缺失时，可停机运行 `./bin/gatt -config config.example.json recover-admin`；该命令保留业务数据。仅元数据备份缺少上游秘密时仍须重新配置来源。本机隔离目录的完整备份、仅元数据备份及显式管理员恢复已通过真实进程测试；异机恢复仍待实测。

恢复工具直连时，对比「修改前值、本次模板值、当前值」，只恢复本次修改项，保留之后的用户改动。清除该启动终端的网关 Key，执行一次直连请求，并确认 Cove 不再新增相应记录。Cove 不读取或覆盖日常 `~/.codex`。

## 分发

`make package` 生成包含二进制、配置、README、验收说明、源码快照、构建 SHA-256 清单和 `Cove.command` 的本机架构压缩包。解压后可双击 `Cove.command`：已有当前版本服务时打开管理页，否则启动服务并打开页面。请保留启动终端，Control-C 停止服务。

`bin/build-evidence.json` 对应源码、前端与二进制摘要，`bin/source-snapshot.tar.gz` 固定未提交源码；设置页和管理状态接口显示构建 ID，可据此核对运行实例。构建成功本身不代表真实账号和客户端验收通过。

更新版本前，先停止现有 Cove 进程，再解压并启动新版；保留原数据目录。运行实例是否已更新，以设置页构建 ID 和本轮验收记录为准。它仍是未公证的开发产物；签名、公证和另一台干净机器验证未完成，没有进行公开发布。
