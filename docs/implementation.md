## 2026-10-08 本机 Agent 接入补齐

新增 Grok Build 1.0.46 用户模型配置的预览、应用、CAS 与三方恢复，以及 Grok/Qoder CLI 的 MCP/Skills 文件合同。Grok 用户扩展以所选独立 `GROK_HOME` 为根，HTTP MCP 写官方 `headers`；选择绑定来源的 Key 时使用该来源的原生 Responses 或 Chat Completions，并检查 Key 的协议权限。Codex 0.160.1 原生合成 MCP/Skill 检查通过后纳入已测版本。本机其他应用以接入说明卡展示，未验证的文件格式不自动写入。步骤与边界见[本机 Agent 接入](local-agents.md)。

最终聚焦 race 为 29 个顶层用例、69 个含子用例，零失败、零跳过；浏览器 10 项配置/恢复及扩展入口检查通过。用户新增批准最多 6 次生成后，Grok 使用现有 New API `gpt-5.6-sol` 完成 5 次真实 Chat 请求，全部成功且用量完整，MCP 执行及结果回传、Skill 正文加载、最终回答均通过；临时 Key 撤销后返回 401，配置恢复、隔离目录删除。原生真实验收将配置的 base URL 改为已批准来源的有界转发入口，并明确选 Chat 后端；生产 Key 的协议选择另由 API 测试验证。该结果不代表 Grok 订阅来源授权、其他原生桌面或全协议验收。最终证据见 `test-results/local-agents-20261008/execution.json`，台账沿用 R074/R075/R079/R098。本轮没有部署或替换日常运行网关，Cloudflare 继续暂缓。

## 2026-10-08 后续收尾顺序

PR #3 已于 2026-10-06 rebase 合并到 `main`（`0d13264d96529770ab6c54be765c4587ef85e1a6`）；[主分支三平台 CI](https://github.com/majiayu000/cove/actions/runs/37421439366) 已成功，[开发预览版 v0.1.0-dev.11](https://github.com/majiayu000/cove/releases/tag/v0.1.0-dev.11) 已发布。下方 2026-10-06 的 draft 状态是合并前记录；运行服务、签名包和性能证据仍保留各自原 BuildID。

公网脚本访问已复现 Cloudflare `403 / error code: 1010`：10 次无认证 GET 中，urllib 默认标识和 curl 的同一 Python 标识均被拦截；两种传输改为浏览器标识后，`/readyz` 返回 200，未携带 Key 的租户 `/v1/models` 返回 Cove 401；默认 curl 也通过。证据见 `test-results/usability-closeout-20261008/cloudflare-before.json`。该探测没有生成调用或凭据变更。按 [Cloudflare 官方合同](https://developers.cloudflare.com/waf/tools/browser-integrity-check/)，准备仅匹配 `cove.silencestar.com`、将 `bic` 设为 `false` 的 configuration rule，保留 Cove 认证与其他域名规则。现有终端凭据没有相应规则权限；用户随后明确选择“稍后处理 Cloudflare”，规则尚未提交。具体变更和回退见同目录 `cloudflare-rule-plan.json`，不能标记已修复。

新增 Roo 3.53.0 在 Cursor 3.20.21 **扩展宿主**中的默认目录上下文验收：9 项合成检查和 9 项真实检查通过，真实模型加载 Skill、调用 stdio MCP 并准确回传，3 次 New API `gpt-5.6-sol` 请求均成功且用量完整；此前批准的批次累计 33/40 次。临时 Key 已撤销并验证 401，独立 profile、工作区和测试进程已清理。连接及恢复使用原生产适配器。另通过 5 项原生 GUI profile 检查：三个输入框连接、取消删除保留配置、确认删除恢复原项；仅使用假 Key，零模型请求。任务文字输入、文件编辑和最后反馈尚未覆盖，隔离 Cursor 登录页会遮挡任务界面。证据见 `test-results/usability-closeout-20261008/roo-cursor-default-context.json` 、`roo-cursor-real-tools.json` 和 `roo-cursor-gui-profile.json`。这不是 Cursor 内置聊天的接入证明，也没有重建运行中的网关。

同时确认 Roo 3.53.0 与本机 VS Code 1.140.0 的默认扫描不兼容：扩展只查旧 ripgrep 路径，实际二进制位于 `node_modules.asar.unpacked/@vscode/ripgrep-universal/bin/darwin-arm64/rg`，在模型派发前报 `Could not find ripgrep binary`。保留失败日志，不修改宿主安装目录。Cline 4.1.21 官方包激活及四个公开任务 API 通过预检；源码另确认它使用共享文件存储，不能继续假设全部由 VS Code SecretStorage 保存。隔离文件配置能读取 provider，但模式选择仍回退到默认 OpenRouter，返回 No cookie auth credentials found，未向 Cove 派发；仅写 providers.json 不能完成扩展配置。早期 GUI 输入受其他桌面操作打断，且键盘自动输入遗漏冒号；保留失败记录，后续以原生字段赋值核对地址后完成 profile GUI 验收。Cursor 官方 BYOK 文档明确 Key 经其后端转发，内置聊天真实验收需相应凭据目的地授权。

| 优先级 | 台账项 | 处理动作 | 关闭条件及所需输入 |
| --- | --- | --- | --- |
| 1（用户暂缓） | R099/R100 | 保留 Cove 专用 BIC 规则草案；用户恢复此项后读取现有规则并确认线上变更。 | 默认 Python 标识健康检查 200、无 Key 数据接口 401、其他域名规则保留；当前没有实施。 |
| 2 | R074/R075/R079/R098 | 继续 Cline VS Code、Cursor 内置模型配置与 Roo 任务 GUI；Roo 在 Cursor 宿主的默认上下文和真实工具子项已通过。 | Cline 共享文件路径尚未完成模型往返；Roo/VS Code 默认扫描失败，需上游兼容修复或经验证的宿主。任务 GUI 需在隔离 Cursor 配置完成登录后继续，profile 连接/取消/删除确认已通过；真实调用累计 33/40，保持既有来源、模型及临时凭据范围。 |
| 3 | R086/R100 | 恢复 Windows 验收机联网，再做安装、启动、权限和服务恢复；另补干净 Mac/Finder/登录及断电场景。 | 2026-10-08 Tailscale 仍报告 Windows 离线，SSH 超时；需用户开机并恢复 Tailscale。托管 CI 不能替代实机证据。 |
| 4 | R091—R097 | 为媒体、文件、后台任务、compact、WS 等操作取得明确的来源、模型和能力合同，再逐项做真实资源、用量和取消验收。 | 当前已验文本模型不能代表这些接口；需要对应账号资格、模型和费用范围，缺项继续标记待验。 |
| 5 | R018/R019/R028/R066 | 补供应商独立注册、授权范围、额度合同与正式账单对账。 | 需要供应商材料及与请求时间窗口对应的价格/账单；签署条款或新增授权须用户明确确认，不用本地测试替代。 |

## 2026-10-06 当前收尾结果

功能提交 `a0643cf595f7` 的完整 Go race 共 723 个测试/子用例通过，零失败、零跳过；官方 SDK 34/34、生命周期 7/7、实际 HTTP 排队与重试 TTFT 回归通过。随后 `aa567f49ed2f` 仅将 Windows race 整套测试时限从默认 10 分钟改为 20 分钟，未改变生产超时或断言。[三平台 CI](https://github.com/majiayu000/cove/actions/runs/37365523138) 的 Linux、Windows、macOS 构建、检查、完整 race、隔离网页和打包均成功。各包核验 30 个文件及 194 个源码快照文件；Windows checkout 的 CRLF 与其他平台 LF 形成不同原始哈希，保留实际 BuildID，不重标为相同字节。

本机与大阪运行冻结构建 `343e720e5e66…`。该 macOS arm64 DMG 已获 Apple Accepted（`2b6d95fb-fe4a-47e7-a6b4-1d7665a4f808`），票据、严格签名、Gatekeeper、30 个包内文件和隔离原生启动通过；第二台物理 Mac 安装/锁/权限/重启 25 项及 launchd/受控 SIGKILL 后手动启动恢复 20 项通过。合成性能保留原 `6047476412ae…` 构建：8 路流、1800 秒、百万记录的 7 项门槛通过，附加 TTFT P95 4.385ms、请求第一页 P95 1.236ms、取消释放 0.061s；不冒称大阪压力测试或最终构建的 30 分钟压力结果。

在已批准的 New API `gpt-5.6-sol` 来源上，Continue 官方 VS Code 宿主、OpenCode 1.18.27 CLI、Cline 3.0.68 CLI 均完成真实 MCP 工具及结果回传。OpenCode/Cline 由模型实际加载隔离 Skill；Continue 的 Skill 由受控测试驱动加载执行。Cline 验收脚本使用 `+00:00` 时间戳时被官方解析器拒绝并重置来源，修正为 `Z` 后通过；Cove 生产配置原已使用 `Z`，没有相应生产修复。包含早期失败尝试在内，上述批次共 27 次 Cove 生成请求，均限于批准来源和模型，所有临时 Key 在两小时内到期且已撤销，撤销后 401、私有配置和测试进程清理通过。实际请求数和每个构建范围见 `test-results/all-completion-20261006/execution.json`。

新增 [Roo 原生 VSIX](../scripts/roo-vscode/README.md)：固定 Roo 3.53.0 官方 profile API 创建、选择和删除独立 Cove profile，保留原项和用户后来选择；已存在/重建同名项、原项删除、取消和失败重试均有保护。9 项回归、6 项原生 profile 预检通过；正式官方扩展经大阪 New API 由模型加载 Skill、调用 stdio MCP 并准确回传，3 次成功请求均有完整用量，累计 30 次生成。临时 Key 撤销后 401、profile 删除及进程清理通过。宿主工具验收关闭自动目录扫描（`maxWorkspaceFiles=0`），GUI 输入/编辑、默认目录上下文、最后反馈按钮和删除确认按钮仍未验；删除使用同一生产恢复函数与已批准 fixture。该独立 VSIX 不改变网页文件 autoapply 的 422 边界或运行中的 `343e720e5e66…` 网关。报告见 `test-results/followup-20261006/roo-tools.json`。上一文档提交的 [三平台 CI](https://github.com/majiayu000/cove/actions/runs/37373872323) 也已全部成功，最初 Linux 没有取得托管 runner 的取消记录保留。

[审阅 PR #3](https://github.com/majiayu000/cove/pull/3) 保持 draft，没有合并或发布正式版本。Windows 实机 `100.81.107.120` 最新 SSH 仍不可达；供应商注册/授权、完整资源与计费合同、正式账单、额外 IDE 自动配置和未覆盖的高级操作/物理实验仍以 [104 项实施台账](implementation-readiness.tsv) 的剩余栏为准。整个库仍未全部完成。

## 2026-10-06 早期功能与验收记录（保留原 BuildID）

修复请求开始时间在排队后才建立、运行指标从最后一次尝试计 TTFT 的问题：准入入口记录开始时间，尝试时间仍单独保存；请求详情、用量统计与 Prometheus 请求 TTFT 使用相同的逻辑请求开始时间。保留 `ttft-before.log` 中 3 秒等待被记成 1 秒的复现，定向 race 修复验证通过。TPM 预留和额度调用观测仍使用实际尝试派发时间，避免排队时间使限流预留过早过期；随后完整 race 另存于 `go-race-final.jsonl`，不覆盖初轮快照。没有改写历史记录。

冻结快照 `6047476412ae…` 的 `make build`、`make check`、SDK 34/34、生命周期 7/7、完整 v13 网页流程通过。完整 Go race 720 项通过，显式启用的 3 项安装 CLI 测试另行通过，合计 723 个唯一测试/子用例；没有把初次跳过写成完整运行零跳过。该快照 macOS 签名公证已 Accepted（`c0c4c039-e904-4176-a140-f52bf795a0cf`），钉合和 Gatekeeper 验证通过。后续文档变更不重标已有 BuildID。

本人现有 New API 来源经大阪公网完成 8 次真实生成：同回合双工具及结果回传、50 消息历史、4 路并发与流断开释放；成功请求有完整用量，取消请求保留未知用量。Continue 1.3.40 的官方实际 VS Code 宿主也经此来源完成 MCP 工具两回合，实际加载并执行隔离 Skill/stdio MCP。所有临时 Key 撤销后的 401、隔离配置删除均已验。文件工具使用随包测试导出时遇到源码布局 worker 路径缺失，失败记录保留；未将其写成完整 GUI/文件编辑验收，也不据此指称生产客户端有同样问题。

当前生产 App 与官方 OpenTelemetry Collector 0.162.0 的实际 TLS/OTLP 解析、父 trace、关闭 flush 与脱敏六项通过；只有隔离测试进程加入一次性 CA，未关闭证书验证。合成模型上游和真实 Collector 分开记录，不推定大阪已部署生产 Collector。[适配来源索引](spec/evidence/adapter-sources-v12.json)重新读取原附件的 24 个固定 commit 文件并保存哈希，补回失效的本地引用；不是恢复原始读取时间的哈希。

本轮原始验收和失败记录在 `test-results/all-completion-20261006/`，具体证据与剩余范围仍以 [104 项实施台账](implementation-readiness.tsv) 为准。供应商独立授权/完整资源和计费合同、剩余 IDE 自动模型配置、其他提供方媒体/Files/Batch/WS/compact、正式账单对账及 Windows 物理桌面验收仍未闭合，不宣称整个库已全部完成。

## 2026-10-06 提交状态与后续验收

本次提交包含单服务器企业多租户、客户端配置扩展、查询性能改进、Cove 自有 ChatGPT 授权和验证刷新修复。当前仍是开发预览；唯一完成台账为 [implementation-readiness.tsv](implementation-readiness.tsv)。源码和运维凭据分开，真实 Key、数据库、部署私有目录及验收原始文件不纳入提交。

已验收的发布快照 `b8deb6363808…` 通过静态检查、前端类型检查和 5 项行为测试；macOS arm64 DMG 获 Apple `Accepted`（`1ba8f90d-2a31-4da9-b9b9-1f8cf576a307`），票据装订、Gatekeeper、30 个包内文件及原生启动通过。第二台物理 Mac 的包安装/重启 25 项和 launchd/受控进程崩溃恢复 20 项通过。大阪持久服务通过 23 项内网、40 项公网检查，现有管理员、租户和其他产品保留。之后的提交前改动仅更新本节、部署说明及台账，并清理一行尾空白；已发布包的 BuildID 不重写为提交后的文档快照。

New API 真实验收属于 `474ddfe6d879…`：15 个目录标识中仅启用 `gpt-5.6-sol`，两次模型验证及四次公网生成通过，覆盖 JSON/Chat SSE、实际函数执行与第二轮结果、Responses SSE 转换，记录用量完整，临时 Key 已撤销。该快照的 1800 秒、8 路流、百万记录性能验收 7 项通过；附加 TTFT P95 1.352ms、第一页 P95 1.337ms、取消释放 0.059s。这是本机合成性能验收，不是大阪压力测试或提供方延迟测试。完整 Go race 的原快照 `bde2c86f8713…` 共 722 项含子用例通过、零跳过；其后 Go 源码未改变，最终修正限于前端显示和文档。各证据保留原 BuildID，读取 `test-results/continuation-20261005/execution.json`。

后续按以下顺序推进，完成后更新同一台账的对应行：

| 顺序 | 关联项 | 下一步与关闭条件 |
| --- | --- | --- |
| 1 | R086/R100、E08 | 推送审阅分支后执行仓库已有 macOS/Linux/Windows CI；原生安装、锁/权限、浏览器和服务生命周期仍需对应实机。2026-10-06 提交前复查 Windows 验收机 SSH 仍连接超时，未执行原生验收；需先恢复可连接状态。第二台 Mac 的局部验收不替代干净 OS、Finder、重启或断电实验。 |
| 2 | R074/R075/R079/R098、D03/E07/E10 | 优先复用隔离宿主流程验证 Continue 的 GUI 与工具模式，再逐版本补 Cline VS Code/Roo/Cursor 自动配置和 MCP/Skills 激活、实际工具回传及三方恢复；隔离目录执行，保留日常登录和用户后改字段。新增 secret 落点须单独获得用户批准。 |
| 3 | R091—R097、D02/E09/E10 | 按实际来源能力选定媒体/文件/后台/compact/WS 的模型与操作，分别验证资源归属、工具历史、用量及取消；既有 New API 文本成功不能推导这些能力。缺可用提供方或当前账号资格时先取得必要输入，不派发盲测。 |
| 4 | R028/R066、E03/E04/E06 | 取得与请求 ID/时间窗口对应的提供方价格与正式账单，核对金额、币种、缓存/推理/工具等维度；缺价或缺账单继续标记 pending，不能用 token 记录当作正式对账。 |
| 5 | R018/R019/R097、D01/D02 | 逐供应商取得 Cove 独立授权、scope/redirect/身份与模型/额度/资源合同，再补真实账号验证；不借用其他客户端内置注册或订阅凭据。需要签署或接受供应商协议的最终操作由用户明确批准。 |

## 2026-10-05 全功能收尾与单服务器企业扩展
用户明确选择扩大 R099 到单服务器、多租户登录、权限和预算隔离。实现与部署步骤见 [企业部署](enterprise.md) 和 [独立规格](spec/04-ENTERPRISE-SINGLE-SERVER.md)。各租户复用既有网关和预算逻辑，使用独立数据库/凭据目录，远程入口不能获取个人桌面的自动管理会话，不能操作服务器上的客户端文件或借用 AWS profile/Google ADC。

百万记录扩展测试发现不存在模型的筛选约1987ms；新模型索引使该类第一页约0.60ms，十类页面均通过300ms门槛。汇总查询增加覆盖索引，合并TTFT扫描并避免无筛选的重复ID集合，金额仍用精确十进制合并。基线与中间结果分别在 `completion-20261005-full/query-baseline.json` 与 `query-after.json`；最终结果必须读取同目录 `performance-final.json`，核对冻结 BuildID、8路1800秒与百万记录，不能从短时结果推导完成。

客户端版本按独立只读进程并发检测，逐客户端超时及版本失败合同保持原值。模型、预算和客户端读取独立发布且保留迟到响应保护，慢 CLI 不再阻塞模型页；浏览器以阻塞客户端读取的场景验证该边界。企业登录在签发会话前重查用户版本和启用状态，防止密码修改或停用期间的旧验证结果重新签发会话。

实际本机 Codex 宿主在隔离目录加载显式 Skill 与官方 MCP stdio 工具，实际执行3*4并将12送入下一轮请求。该上游是合成提供方；证据在 `codex-mcp-skill.log`，不宣称本人订阅或其他客户端已完成E10。测试清理临时目录，不读取日常客户端登录材料。

企业真实HTTP/权限/隔离/重启/预算保留证据在 `enterprise-tests.log`，网页操作在 `enterprise-browser.json`；静态检查、完整race、SDK、最终性能、签名公证及实际包运行由同目录 `execution.json` 汇总，并保留各层原 BuildID。供应商准入、部分实际高级调用、IDE原生恢复、Windows及干净Mac实机、正式计费对账仍按各台账剩余栏记录。

## 2026-10-05 Continue 宿主和 Cline CLI 功能验收

Continue 1.3.40 已在真实 VS Code 1.140.0 扩展宿主读取 Cove 生产生成的配置、解析用户批准的临时 secret，并通过本人 Cove 自有 ChatGPT 来源完成两轮对话；每条请求均完成并有完整用量。订阅配置改为 Chat 路径，明确使用默认采样且不请求输出 token 硬上限，保留其他请求选项。验收调用官方扩展随包测试入口的真实 core 流程，覆盖宿主集成，未覆盖 GUI 输入及 agent/tool 等其他模式，见 [Continue 宿主报告](../test-results/completion-20261005-features/continue-ide.json)。临时 Key 已撤销并复查 401，临时 .env、配置及 VS Code 用户目录已删除。

网页实测发现配置应用前的 CLI 版本复核偶发返回 409；独立读取同一官方 CLI 版本正常。Cline 检测时限调整为 8 秒，仍在应用边界复核版本和启动环境，无法确认时保持拒绝。最终网页选择、预览、应用和恢复结果见本次执行汇总。

新增 Cline CLI 3.0.68 自动配置卡：所选独立数据目录、官方 version 1 providers.json、OPENAI_API_KEY 进程引用、版本及环境冲突阻止、CAS 与三方恢复。持久凭据优先于环境变量时阻止改写，其他 provider/login 和后改字段保留。未修改的官方 CLI 和 SDK 已实际读取配置，通过本人来源执行一次 printf 并准确回复；两条请求完成、用量完整，恢复删除本次新增 provider 并保留原登录，Key 未持久化到 CLI 文件。见 [Cline CLI 报告](../test-results/completion-20261005-features/cline-cli.json)。Cline VS Code、Roo、Cursor 的自动入口仍与 CLI 分开记录。

最终源码检查、构建、本机运行和本次功能版本的签名公证结果统一记录在 [本次执行汇总](../test-results/completion-20261005-features/execution.json)，以实际 passed 和 BuildID 为准。以下公证及早期验收记录保留其原构建范围；不将旧冻结包标作新功能版本。仍缺其他供应商合同与凭据、其余 IDE 自动入口、Windows/干净 macOS 安装及未覆盖的真实高级协议验收，详见唯一[实施台账](implementation-readiness.tsv)。

## 2026-10-05 macOS 冻结包签名公证完成

复核 Codex 历史记录后，复用本机既有同团队 App Store Connect API 密钥配置 `cove-183`，无需另建 App 专用密码。冻结 BuildID `4937dc59de9b105b7fe34fd52ed5cfc485914a13a80ec9560754b8f69d55fdf5` 的 Ma JiaYu（C5UWZ934C2）签名 DMG 已获 Apple `Accepted`，提交 ID 为 `a98301a2-6c2b-4250-ab5d-6362bc1309ec`，Apple 日志无 issues；票据装订、票据校验、严格签名和 Gatekeeper 均通过。签名后的隔离原生启动、健康/就绪检查及包内 27 个文件哈希检查也通过。产物为 [macOS arm64 签名公证包](../bin/cove-signed-darwin-arm64-4937dc59de9b.dmg)，结果和最终 SHA-256 见 [公证验收报告](../test-results/completion-20261005-closeout/notarization-final.json)。

公证覆盖上述冻结包及 BuildID；后续台账文档更新另行记录。干净机器安装、Windows 实机、其余供应商、客户端宿主和未覆盖真实协议实验继续保留在实施台账。

## 2026-10-05 剩余接入与真实客户端收尾

Claude Code 2.1.281已读取Cove实际生成的隔离配置，经本人订阅完成两轮请求、一次Bash printf及准确最终输出。metadata.user_id省略、effort同名映射、文本system角色映射和已声明beta不转发均需来源允许参数调整并写入响应/请求记录；thinking、interleaved thinking、缓存和实验功能在生成配置中关闭。未知beta、签名/缓存历史、工具增删等仍在上游派发前拒绝。实际测试后原配置恢复、临时Key撤销及日常来源保留均通过。

新增Gemini CLI0.62.0版本卡及settings.json模型/认证字段自动预览、应用和三方恢复；保留后改字段和Google OAuth文件。实际CLI读取生成配置并请求Cove，Codex转换因不支持topK/thinkingConfig返回422且零上游派发，不能标为真实推理成功。其原生Gemini验收需该提供方凭据。Continue 1.3.40补充所选用户YAML的模型字段配置、官方扩展版本检测、三方恢复与Cove Key secret引用；不修改.env、仅选择chat角色，格式或用户修改不能安全恢复时保留原文件。官方解析器和实际文件证据见`continue-config.json`，官方解析器属于当时的文件验收；后续真实宿主两轮对话和 Cline CLI 验收见本页顶部，Continue 工具模式及其余 IDE 自动配置仍留在 D03/E07。

OpenAI现已公开本地开源应用的Sign in with ChatGPT合同。新来源使用Cove名称、稳定host ID和动态入口注册，保存签发client ID，校验OIDC签名/aud/sub及计划scope；公开models/responses端点与旧私有Codex凭据隔离。刷新携带签发ID/resource，退出尝试可信同origin撤销并如实报告未确认。本人已批准并完成Cove自有注册真实授权；公开目录返回5个可列模型，native Responses两轮add返回12，Codex0.160.0真实printf与用量对账通过。隔离实例仅复制该新账号/来源/模型，以故意到期的本地元数据触发真实刷新，access/refresh轮换、身份/注册及计划scope保留，推理200；退出撤销收到200并清理凭据，随后使用原注册重新登录。自然过期与真实失败恢复未宣称通过。实际结果单独记录，合成回归不替代用户授权。原日常来源保持原端点、凭据和参数调整设置，仅本机OAuth回调host从localhost改为官方要求的127.0.0.1。

新增Continue前的完整race共337项顶层、含子项697项通过、零失败、零跳过。收尾源码的完整race、静态/类型/前端行为检查、构建哈希、本机PID、真实复测和桌面开发包结果以 `test-results/completion-20261005-closeout/execution.json` 为准；较早真实报告各自保留其BuildID。Apple 冻结包公证完成，结果见本页顶部；Windows主机本轮无法连接，供应商与额外客户端未闭合项仍见唯一[实施台账](implementation-readiness.tsv)。

# Cove v1.2 实施与验收记录

## 2026-10-05 本人订阅真实验收与公证准备

本人已完成指定 ChatGPT 账号授权。本轮先在 `ac307c3f1e147…` 构建完成原生 Responses SSE、Chat/Messages JSON 的真实两轮函数工具调用、最终结果 12、网页创建 Key 的一次性明文保护、Codex 0.160.0 实际 `printf` 执行及 8 条用量对账，见 [修复前真实报告](../test-results/completion-20261005/live-before-verification-fix.json)。测试使用可清理临时来源共享本人账号，日常来源的参数调整选择保留；这些证据不关闭 Cove 独立 OAuth 注册或其他提供方合同。

继续验收发现原生订阅 Responses 的显式 JSON 文本验证实际派发 SSE 并误记 JSON 通过，[修复前回归](../test-results/completion-20261005/verification-format-before.log)已复现。现原生 JSON 验证返回 422 且不调用上游，默认和网页按钮选择受支持 SSE；Chat/Messages 按所选 JSON 或 SSE 真正调用并解析对应输出。[定向 race](../test-results/completion-20261005/verification-format-after.log)通过。此前“六项全部通过”不证明原生 JSON 能力，受支持格式共五项；最终构建的 [文本报告](../test-results/completion-20261005/text.json)、[工具与 CLI 报告](../test-results/completion-20261005/live.json)以及 [执行汇总](../test-results/completion-20261005/execution.json)需读取实际 `passed`、BuildID 和哈希，未产生或失败不计完成。

真实额度取得提供方报告的 percent 单位与周窗口观测，尚不证明实际重置和失败恢复。Ma JiaYu（C5UWZ934C2）Developer ID 身份已核验；`cove-183` 公证 profile 在准备检查时缺失，随后复用既有 App Store Connect API 密钥配置并完成冻结包公证，正式结果见本页顶部。凭据不进入源码和报告。本轮 Windows 原验收主机 `Administrator@100.81.107.120` 连接超时；starlight 不是 Windows 验收证据。D01–D03、未覆盖外部实验和桌面验收继续保留在唯一台账中。


这是当前源码的实施快照，基线为[完整 Spec v1.2](spec/01-COVE-COMPLETE-SPEC.md)。[104 项实施台账](implementation-readiness.tsv)是唯一逐项记录：设计状态取自设计合同，实现状态按当前代码和已完成检查重新核对，没有沿用规范中的旧 `current_status`。当前是本地开发预览，不能标记“104 项全部完成”。

已实现本机账号与来源、模型、API Key、协议转换、路由、用量与软预算、记录、客户端文件配置、备份、受控恢复和更新，以及可观测性与通知。高级协议按来源及模型能力开放；未知资格、额度、价格或计费上界保持未知并给出拒绝原因。严格预算已实现一个限定成功路径：官方 OpenAI Responses、无状态文本/客户端函数工具、明确 default 服务层、正 max_output_tokens 和完整用户配置 token 价格。通过官方输入计数接口取得输入量，按输入量和输出上限向上取整预留；这是本地已知计费规则下的准入上限，真实提供方成功验收仍待执行。其他来源或操作没有可信完整上界时拒绝。请求只在可证未发送的拨号失败时有限重选；未知提交、429、5xx、超时和流中断不会重放。

先前审查修复轮使用独立临时目录与合成凭据、回环上游验证，没有读取日常客户端凭据或调用真实收费模型。本次完整目标另执行了可清理的当前用户服务验收，见下文；没有停止用户已有实例。当前数据库为 schema v2，不提供旧数据迁移或回填。凭据受当前用户目录和文件权限保护，默认记录与导出不保存提示词、响应正文、工具内容或凭据。完整备份使用 age 口令加密并校验一致快照和秘密闭包；元数据备份需要重新补齐认证。恢复和更新先关闭新生成准入，验证实际进程身份和初始化健康后再激活；失败回退只适用于已定义的同 schema 流程。

## 2026-10-05 剩余界面状态与配置恢复补齐

实际HTTP验收发现 `/admin/config-extensions` 列表未接管理路由而返回404；现补上入口，并用真实管理会话验证列表、401、405及预览。账号、客户端、MCP/Skills、文件任务和运行提醒增加读取状态与失败重试；读取失败保留已有数据并阻止假空库提示，通知提醒与投递记录分别记录读取成功状态。客户端、扩展配置及通知 URL/凭据按服务返回的字段错误关联输入并聚焦。扩展配置卡尚在读取时暂缓切换客户端，防止从未返回的元数据清空必填版本。管理 API 的失败对象继续保留 status、field 和 requestId，并保留服务已返回的 change；配置写入或恢复部分失败后立即刷新变更记录，显示逐文件恢复入口，继续保留输入和未成功结果。

新增真实文件系统故障检查：Unix 禁止第二份文件父目录写入，Windows 持有允许读取但拒绝替换的文件句柄；验证第一份已完成、第二份失败时仍保存 partial 与每文件进度，原预览不能重放。恢复中第二份再次失败后，只恢复余下文件，不触碰已经恢复后被用户修改的第一份。macOS 当前用户 launchd 验收新增只终止测试自己 PID 的 SIGKILL 场景，重新 service start 后核对 BuildID、数据库和凭据保留；这不替代 Windows 登录、物理断电或更新 journal 的故障证明。

本机已安装 Codex 0.160.0 完成隔离合成工具两回合；配置改为调用产品实际的 clientDesiredFields 与 TOML 编辑器生成，客户端自行读取该配置并回传工具结果。该版本加入已核验的配置版本，未知版本仍保持原有拒绝。新增定向检查验证该版本的预览与应用；这不证明真实订阅授权、长会话、parallel、WS 或 compact 验收。

浏览器新增账号 GUI 创建/改名409重提/凭据保存清空/删除、各集合失败/空库/重试、动态字段错误与焦点、多文件配置真实写入后注入失败响应并恢复，以及十模块完整正反键盘遍历。扩展变更历史的长文件路径在管理窗口内允许折行，避免窄屏横向溢出。键盘检查在集合加载后展开全部表单，并考虑原生日期时间输入内部的多个 Tab 停靠点。原生 Tab 可将焦点移到浏览器界面，document.activeElement 为 BODY；验收要求全部当前可见控件可到达且不落到模态窗口后面的应用控件。具体完成结果、源 BuildID、CI 与包核验以 `test-results/completion-20261004/finish.json` 为准，未运行或未完成的检查不计通过。

新增 macOS 签名公证候选包工具，复用原生开发包核验并验证签名、公证 Accepted、票据和 Gatekeeper 后才产生 DMG。工具准备阶段已核对参数、语法和用户选定的签名身份；随后完成的 macOS 冻结包签名公证见本页顶部。D01–D03 的供应商独立授权/完整模型额度资源合同及额外闭源客户端自动入口、真实 E01–E10 和其余桌面故障验收仍保持未完成。

## 2026-10-04 管理审计与编辑保护补齐

来源页增加直接打开账号与来源管理的入口，复用原有账号重命名、凭据、来源计量与高级设置。运维页新增管理审计，读取已有持久管理动作记录，按结果筛选、稳定游标分页和下载当前已加载记录。接口只投影时间、方法、路径、目标 ID、状态和 HTTP 状态，不返回请求体、请求 hash、重放响应或凭据，路径去除查询串。管理动作和财务审计保留90天；未确认动作继续保留，不随24小时备份文件清理。HTTP202标为已接受，管理请求成功不替代异步任务终态。

来源及管理窗口关闭和 Escape 现在检查未保存输入。取消关闭保留当前输入，确认关闭丢弃草稿并清空凭据；页面离开使用浏览器原生提示，不将草稿或秘密写入本地存储。成功提交清除草稿标记，只读筛选不触发草稿提示。模型和预算读取分别保留独立成功结果，读取失败不显示假空库并提供重试；模型、路由和预算保存后恢复提交按钮焦点。预算、单次费用估算、金额预留和缺价格拦截继续保留。

新增两个后端回归检查覆盖管理审计真实 Key 创建、脱敏、同时间分页、过滤、认证/方法/游标/存储错误、90天保留和未确认动作保护。本轮完整 race 共326项顶层、含子项678项通过，测试零跳过；没有测试的嵌入资源包由 Go 标作 package skip，与测试跳过分开记录。`make check` 已完成静态检查、类型检查和5项前端行为测试。

隔离 Chrome 通过真实本机管理接口新增模型与路由的 GUI 创建/编辑/删除、模型价格保存、模型网络失败保留输入、模型和路由409显式重提、提交焦点、来源错误字段焦点/关联、关闭取消/丢弃/凭据清空，以及审计筛选/分页/脱敏下载。十个管理模块展开表单全部控件标签检查、375/768/1280宽度、模态键盘焦点与实际 Chrome 200% 浏览器缩放均通过；没有页面脚本错误。授权页仍只验证发起/取消/错误，不代表真实用户授权成功。

Windows首次CI在100毫秒后台观察测试的时序断言失败。将创建延迟150毫秒后，本地复现一次POST、零GET、任务待核对且返回DeadlineExceeded；旧断言错误要求一次GET。测试现允许截止期限前尚未读取，仍禁止重复创建、未决任务丢失或错误发布资格；新增首次GET后取消的确定性检查，生产计时与错误合同保持原值。定向race连续三次通过，最终完整复验与新CI结果单独记录。 下一轮Windows完整race通过，浏览器失败于新缩放测试写死640px；实际200%像素比和scale正确，原生边框使内容宽度632px。缩放检查改为比较同一窗口缩放前后的内容宽度与像素比，继续要求scale=1及十模块无溢出，并保存实际指标。

本轮最终构建、检查、浏览器、开发包与本机实例证据见 `test-results/completion-20261004/remaining.json`。旧报告保留原构建范围，原浏览器日志复用情况另行注明。新提交的三平台 CI 与 prerelease 以该提交 Actions 的实际结果为准。真实提供方调用/账单、额外订阅独立授权合同、桌面故障实验和正式签名仍保留在台账，不能用本轮合成结果标记完成。

## 2026-10-04 请求恢复与异步筛选补齐

请求详情新增来源/账号、Key、模型、路由及预算入口，来源、Key、模型和路由打开对应实体。逐次尝试展示当时的账号代次、配置版本、候选排除原因、路由策略证据、参数调整和失败阶段。详情链接使用 `request_id`，复制或刷新后重新打开该记录；关闭详情会清除参数。空库提示与筛选无匹配提示分开。预算及单次费用准入合同按用户决定保留，没有删减金额预留、价格资格或拒绝检查。

隔离浏览器曾实际复现：上一次刷新尚未返回时切换请求筛选，新读取被全局防重复操作锁丢弃。只读刷新现独立于写操作锁，继续使用已有刷新序号丢弃旧响应。回归场景明确等待新筛选响应，再释放旧响应并确认新结果保留，未放宽写操作的重复保护。

新增浏览器场景通过真实本机管理接口与合成 HTTP 上游执行：请求链接重开、来源/Key/模型/路由修复跳转、未发送拨号失败后的两次安全尝试及各自策略快照、移动详情宽度、人工对账（保留原始未知费用）、预算创建/删除与409显式重提、doctor运行与诊断下载脱敏。原有 OAuth 发起/取消、分页、Key使用反馈与未配置价格422拒绝同场景复验通过；没有真实账号授权或收费提供方调用。

本轮 `make check` 与 5 项前端行为测试通过。完整 `GATT_CODEX_E2E=1 GATT_CLAUDE_E2E=1 go test -race -json -count=1 ./...` 共675项（含子项）通过、零跳过，包含已安装 Codex 和 Claude Code 的合成工具循环。各次源码与浏览器 BuildID、失败复现和最终本机更新/开发包证据单独记录于 `test-results/completion-20261004/continuation.json`，不将旧包报告移作新包验收。

上次 CI 的 macOS 浏览器安装步骤删除 runner 自带 Chrome 后无法下载替换包，Linux 和 Windows 检查已通过。本轮使用三种 runner 自带的 Chrome，浏览器无法启动仍使测试失败；当前提交的三平台检查和 prerelease 结果以 Actions 为准。真实提供方、额外适配合同、桌面安装故障实验和正式签名仍列在唯一台账中。

## 2026-10-04 登录入口与完整本地复验

ChatGPT 订阅来源卡片直接提供“登录 ChatGPT”、授权进度、“继续授权”和取消操作。订阅预设自带名称，模型可先留空；保存后说明真实账号授权步骤。不同身份仍进入原有确认更换流程。API Key 来源继续使用提供方真实密钥，不生成模拟账号或登录成功状态。首次操作见[接入指南](first-request.md)。

本轮实际执行 `GATT_CODEX_E2E=1 GATT_CLAUDE_E2E=1 go test -race -json -count=1 ./...`：324 项顶层、含子项 675 项全部通过，零跳过；实际 Codex 和 Claude Code 完成隔离合成上游的工具执行与结果回传，不代表真实提供方验收。首次完整执行出现一次 OpenCode 合成版本命令超时；关联测试连续三次与完整复跑均通过，保留失败记录，没有放宽版本或超时合同。

构建与 `make check`、34 项官方 SDK 合成测试、7 项运行/备份恢复检查、8 项 macOS 原生 launchd 生命周期检查通过。v13 浏览器实际执行导航、主题/语言、来源 CRUD、OAuth 发起/PKCE/弹窗拦截/取消/接口失败、模型筛选、真实后端请求分页、Key 创建/撤销、调度设置持久化、付费候选边界和移动尺寸检查。OAuth 授权页在隔离测试中被截获，未模拟真实账号登录成功。旧浏览器脚本误用本地无认证来源测试付费回退、双语文本严格匹配两个元素的问题已修正。

新增 Key 浏览器场景验证关联合法已有预算、当前实例接入示例、首次请求前的未使用提示、实际合成请求后更新 `last_seen_at`，以及刷新后一次性秘密不再显示。未配置模型价格时，软预算请求明确返回422；添加隔离测试的用户价格后请求成功，不把未知价格当成免费。该结果不证明真实账单核对通过。

本轮过程与 BuildID 保存在 `test-results/completion-20261004/`；该目录不进入源码。已在 `127.0.0.1:5573` 更新本机实例并保留用户来源。检查报告仅对记录的构建和范围有效。Windows 原验收主机本轮 SSH 连接关闭，旧报告不替代当前包；真实账号、供应商独立授权合同和正式签名/公证仍不能用合成结果关闭。[CI](../.github/workflows/verify.yml)对 macOS、Linux、Windows 分别构建、检查、race、隔离浏览器测试并生成带哈希的原生开发包，所有平台成功后才发布 prerelease；实际结果以该提交的 Actions 为准。

## 2026-10-03 v13 状态与分页补齐

客户端卡片与模型选择器读取最近一次 Cove 配置变更，仅检查当时明确选择的文件及 Cove 字段，显示实际模型、路径和恢复/变更/不可读状态。无记录不扫描客户端文件；其他用户字段保持独立；文件删除、路径替换为符号链接、配置字段修改均不显示已写配置。Codex、Claude Code、OpenCode 复用现有手动配置流程，应用和恢复后立即更新卡片。文件匹配不代表客户端已加载，也不代表 Key 环境已配置或真实调用成功；没有增加订阅授权或额外客户端自动配置入口。

路由列表与单路由接口返回临时运行视图：实际在途与排队数量、共享账号并发、现有健康/额度/调度检查的候选排除原因。GET 不调用上游、不预留槽位、不改变轮转权重，不持久化运行数据。界面每5秒刷新可见页面的本机/来源/路由状态，刷新失败标明未知；完整刷新、筛选和加载更多之间的迟到结果不会覆盖新选择。路由预览在配置或运行候选变化后失效。网关提示区分本机可接收请求与上游已有测试记录，不推断协议或工具能力。

请求页接入后端游标分页并去除交叠记录；API Key 显示实际过期、停用、撤销及计划失效状态，打开 Key 页期间自动更新时间判断。保留 v13 的主题、布局与正常尺寸，路由名称和长名称可换行。复制完成标志短暂显示后恢复。

本轮实际检查和最终包哈希见[状态功能交付核验](../test-results/v13/runtime-delivery.json)。[后端关联 race](../test-results/v13/runtime-related-race.log)包括3项新增顶层回归及所关联的路由/额度/调度/TTFT检查；使用真实管理处理器、SQLite、选路准入及隔离文件，提供方 transport 为替身且不建立 TCP 监听。[前端5项行为回归](../test-results/v13/runtime-check.log)覆盖状态真实性、共享并发、Key失效与分页去重，并加入 `make check`。浏览器脚本增加实际后端游标分页与账号占用断言，仅缩小隔离实例请求页大小以形成分页；本轮只完成语法检查，没有浏览器执行证据。旧报告继续保留原 BuildID，真实提供方和完整浏览器验收未算通过。

## 2026-10-03 v13 禁用功能补齐

路由页的“订阅用完后使用付费 API”、5% / 10% / 20% 剩余额度阈值已接入已有设置接口，使用版本检查保存并立即生效，重启与配置导入/导出保留设置。默认允许付费回退，阈值为5%；仅作用于路由，固定来源 Key 保持明确绑定。请求转换、权限/预算、健康与上下文资格检查通过后先选可用订阅，再在同类候选内应用原有优先级、策略和会话亲和；禁止付费回退时不会派发付费候选，权威前序响应绑定不会转到另一来源。已观测剩余额度**等于或低于**阈值的账号跳过（与 v13 原设计的比较一致）。仅使用有效期内、当前账号及来源代次、匹配模型/操作作用域的账号额度窗口；未知、过期或无账号作用域的额外额度池不会被当作耗尽。

“下一次 coding 请求”调用真实路由配置预览，不调用提供方或消耗轮转权重。没有 coding 公开模型名时提示创建；没有候选时显示真实错误；有候选时显示实际来源和上游模型，并说明实际请求内容与 Key 权限仍影响选择。保留 v13 原有布局和尺寸，只改变实际数据、交互状态和无障碍名称。

[调度与相关回归](../test-results/v13/scheduling-race.log)完成42项顶层测试（含子场景），覆盖设置保存、409版本冲突、无效设置拒绝、导出、订阅优先、阈值边界、关闭回退时429、过期额度、请求能力与绑定，以及原有路由、额度和配置导入检查。该测试通过真实管理处理器、SQLite和生产请求选择代码执行，提供方使用合成观测，不代表真实订阅验收。TypeScript与生产网页构建通过；本轮完整检查与新包哈希记录见[交付核验](../test-results/v13/scheduling-delivery.json)。浏览器脚本已增加设置保存/刷新与 coding 预览测试；本环境 Chrome 启动被拒绝，见[本轮浏览器重跑](../test-results/v13/browser-scheduling-retest.log)，不计通过。

用户已授权提交，但本环境 `.git` 为只读；GitHub写连接器仍要求宿主审批，而审批策略为 never。源码与开发包继续保存在工作目录，提交、远端Actions和Release不能据此标记完成。真实订阅调用与完整浏览器验收仍待在允许运行服务与浏览器的环境完成；先前报告保留其原BuildID。

## 2026-10-02 v13 修复与交付

v13 控制台接入真实管理接口。请求详情与小时均值的 TTFT 统一为 `first_content_at - started_at`，包含排队和重试耗时；仅有元数据、心跳或缺少语义内容时间的记录不产生 TTFT 样本，不回填旧记录。`TestUsageTTFTUsesSemanticContentAndExcludesUnknown` 覆盖统计、筛选与未知值；真实调用脚本 `scripts/verify-live.mjs` 已更新为 v13 Key 弹窗入口。

[CI 与开发版发布流程](../.github/workflows/verify.yml)在 main push、PR 或手动触发时执行原生 macOS 构建、静态检查、完整 race 与隔离的 v13 浏览器测试，并打包上传产物。main push 仅在这些检查通过后发布以 Actions run number 区分的 prerelease；这是未签名开发包，不证明真实订阅验收。Actions 配置使用[官方 Go action](https://github.com/actions/setup-go)、[Node action](https://github.com/actions/setup-node)与[产物 action](https://github.com/actions/upload-artifact)的文档接口。

本轮实际检查、构建 ID、包哈希及未完成项统一记录于 `test-results/v13/delivery-verification.json`；上一次 `verification.json` 只证明其对应构建。本机环境无法监听 loopback，Git 元数据只读且 CLI 无法连接 GitHub；远端读取由 GitHub 连接器核对；创建分支因宿主审批策略 never 被拒绝，提交、Actions 执行与发布仍未完成，最新包的真实订阅三协议工具回合仍单独待验。真实验收使用已登录的 Cove 独立订阅来源执行 `node scripts/verify-live.mjs <实际配置路径> <报告路径>`；它会验证运行 BuildID、从提供方发现模型、通过网页创建 Key、执行三协议工具两回合、验证 Codex CLI 的实际工具输出与用量，并撤销测试 Key。

## “全部完成”目标的当前推进（2026-10-01）

### 本轮交付版本对齐

2026-10-01 独立审查后继续处理源码、开发包与校验文件的一致性。已恢复本地仓库与 `majiayu000/cove` 的提交关系，并保留远端 README 的 clone 快速入口；工作目录源码保留。Linux 原包内部文件哈希通过，外层 SHA-256 旁文件过期；Windows 原包内部文件哈希通过但缺少旁文件，均按实际压缩包重新生成。旁文件修复不代表旧包包含最新源码。

同包 SDK 复跑发现 Messages 开始事件的 `output_tokens=0` 在取消/流截断后被计成完整费用；原失败报告保留于 `test-results/delivery-20261001/iteration-845990ea/mac-sdk.json`。已新增原生 Messages 及 Responses/Chat 转换的六项终态回归：未观测终态保留实际已知用量、标记 partial、完整费用保持未知并仅列已知部分费用；收到正式完成仍正常结算，不改变失败/取消或不重放合同。修复前后证据分别是 `cancel-cost-before.log` 与 `cancel-cost-after.log`，最终整包结果另见本轮汇总。

本轮最终结果统一记录于 `test-results/delivery-20261001/final-checks.json`：按当前冻结源码重建原生开发包，分别记录完整 race、静态检查、SDK、网页、生命周期及平台可用性。报告未生成、命令未完成或来源/产物哈希不匹配的检查不计通过。既有真实订阅和 1800 秒性能报告仍属于旧 `b9cf` 构建，不迁移为新包证据。

剩余完整产品验收包括：真实订阅经 Claude Code 的工具执行及最终回复、长历史/并行/WS/compact、真实提供方严格预算成功路径、额外 OAuth 提供方与各客户端 MCP/Skills 实际加载，以及剩余表单流程和 Windows 实际登录/断电。外部注册合同与未知计费规则需提供方证据；本轮交付对齐不将这些项目改为完成。


最新完成的冻结包是 `b9cf1d595a13…`，[同源码三平台汇总](../test-results/goal-platform-final-checks.json)已核对三包42项文件哈希：Mac race314/630、Linux314/630及Windows316/632通过，Linux/Windows各跳过两项外部CLI；Mac已安装两项CLI实际启用。Mac/Windows网页各43项、SDK34项、Mac/Linux生命周期各7项、真实launchd8项、Windows计划任务10项和Linux user-systemd10项通过。同包真实订阅主流程三协议工具回合、网页Key、Codex0.159.2最终回复和用量对账通过；保留两条上游已完成后的CLI本地取消。Mac同包1800秒/8流/100万记录五项性能门槛通过。Windows另完成实际旧包到此包的手动更新8项核验，SQLite及凭据保持一致，测试进程清理；实际登录触发和物理断电仍未验。

当前源码又补了R072缓存/通知草稿、409差异/显式重提/放弃、通知提醒独立等待和重复保护、这两个模块的字段错误关联/焦点返回。[48项网页源码迭代](../test-results/goal-ui-cache-notification-accessibility.json)通过，[缓存原复现](../test-results/goal-ui-cache-before-final.log)保留。其后真实本机智谱API的Messages JSON/SSE已返回实际文本；转换拒绝同时丢失已知usage的问题由[相关race](../test-results/goal-glm-conversion-usage-related-retest.log)覆盖并修复，正式终态与转换失败分开记录，未知终态不改成完成。该Go变化及新UI尚未替换已验的b9cf三平台包，真实复验以各报告实际结果为准，不能把旧包性能或平台证据标成新源码最终验收。

后续[50项网页源码迭代](../test-results/goal-ui-keyboard-zoom.json)也通过：覆盖10页可见且启用控件的Tab/Enter导航，并在独立临时Chrome配置以实际浏览器200%缩放验证10页无横向溢出；并未替代所有展开编辑流程的键盘验收。用量修复后的[完整Mac race](../test-results/goal-glm-known-usage-full-race.json)315个顶层/635项含子用例、零跳过通过，静态检查通过。[本机既有智谱凭据的原生Messages JSON/SSE复验](../test-results/goal-real-local-glm-native-final.json)通过；仅保留枚举、用量、终态与元数据，临时凭据/实例已删除。实际响应包含带签名thinking，因此跨协议仍按合同拒绝；[完整三协议探测](../test-results/goal-real-local-glm-source.json)仍为失败，不能写成三协议已支持。该报告同时证明已知JSONusage现在保留、未观测到正式流终态仍为partial。

以下段落保留各轮历史证据与当时未完成项；当前全项结论仍为开发预览。已有本机客户端凭据、已安装客户端登录、Cove独立OAuth注册和严格预算提供方资格分别记录。[本机来源元数据](../test-results/goal-local-provider-metadata-inventory.json)只读取公开配置及凭据是否存在，没有导出凭据。日常5569进程未停止或修改。

用户已指定先完成 Mac 验收，Linux 使用 Docker，Windows 使用另一台电脑。当前目标持续推进；没有把外部条件不足的项目改成完成。除原审查三项之外，本轮还修复：TPM 的 Retry-After 等到足够容量释放；普通 API 来源编辑不再误算云端点；launchd 安装先准备0700数据/日志目录；来源、账号、模型发现、Key和预算不冻结无关对象；路由/模型变更刷新父级数据，Key权限表单保留编辑版本，冲突由用户明确选择重提；来源弹窗使用原生dialog处理键盘、Esc和焦点返回；Anthropic beta仅在原生Messages及模型明确声明 `anthropic_beta:<tag>` 时传递，转换和未声明组合仍返回422。

前一冻结构建 `7d5d9ba06cdf…` 已完成 Mac 297 个顶层测试、537 个含子用例、零跳过；SDK 34/34、网页、生命周期和当前用户 launchd 均通过。同包 1800 秒、8 流、100 万记录性能五项门槛通过：附加 TTFT P95 1.015ms、查询 P95 1.169ms、RSS 增长 7056KiB、取消释放 0.0098 秒，见[前一包汇总](../test-results/goal-phase2-final-checks.json)与[性能报告](../test-results/goal-phase2-performance.json)。Linux arm64 同源码原生 CGO 构建、完整 race、静态检查、生命周期和未安装 Go/Node/GCC 的独立 Debian 容器运行通过，见[Linux 汇总](../test-results/goal-linux-final-checks.json)。Linux 三项跳过是 Darwin 实际更新助手和容器未安装的两种 CLI；普通容器本身没有证明 systemd 用户会话。后续新增独立 systemd 容器，实际用户管理器验收见下文。此前并发 401 测试缺少屏障，已修复测试并在 Mac/Linux 各连续 20 次 race 通过；生产刷新代码未变。

本轮新增严格预算成功路径，见 [strict_budget.go](../internal/app/strict_budget.go)和[定向 race](../test-results/goal-strict-final.log)。计数网络和正文读取释放全局锁，计数后复核 Key、模型、来源、价格、预算版本、路由和并发；共享总期限，失败或变更不派发生成。测试覆盖计数、原子预留、实际结算、未知用量保留、8 请求竞争、纯路由预览、函数工具、管理员测试和安全未发送重选。准入费用向上取整到账本精度，并持久化输入计数、输出上限及准入依据。重选的 TPM 使用官方计数和输出上限，在最终准入处检查一次，避免先用 tokenizer 估算误拒绝。另修复同账号第二来源重选把本请求占有的并发槽误算为其他请求的问题，仅凭服务器登记的运行请求和不可变账号记录扣除自己的槽。

[网页报告](../test-results/local-entry-acceptance.json)覆盖来源/账号/Key/预算/路由等已声明案例，包括独立慢操作及重复点击保护、轮换一次明文、移动宽度和会话存储失败；剪贴板和模型上游使用替身。Mac 服务脚本使用可清理的实际当前用户 launchd，检查安装不启动、PID/端口/build、中文空格路径、无 Go/Node 的系统 PATH、权限、冲突、停启和卸载保留数据。各模块完整键盘、脏表单、CAS 差异和跨平台桌面仍按台账保留。Vite 7.3.6 的[本次 npm 审计](../test-results/goal-npm-audit-after.json)零项，不能保证未来公告。额度提醒附件与主 Spec 已统一为剩余 <=10%。

Linux 实际 systemd 用户服务首次发现 `WorkingDirectory` 引号被当作路径字符，含中文空格安装失败。服务已使用绝对 binary/config/data 参数，删除多余的 WorkingDirectory 设置后，[真实用户管理器源码迭代报告](../test-results/goal-linux-service-source-iteration.json)十项通过：安装不启动、default.target 启用、PID/端口/build、中文空格路径/私有权限、无 Go/Node/GCC 的网页、用户管理器重启自动启动、冲突保护、停启、卸载保留数据。该报告为修复验证，不代替最终包；独立 Docker PID1 是 systemd 257，用户 UID1000，本机原有服务和其他容器未改动。另将真实更新助手测试扩展到 Linux 原生子进程，使用对应平台包 fixture；[Linux 定向 race](../test-results/goal-linux-real-update-source.log)已验证关闭准入、健康核验和激活，管理器查询仍是替身，真实 systemd 生命周期由前述独立实验验证。

UI 状态收尾已复现三项此前缺口：无关预算刷新覆盖未保存上限（31→32）、慢诊断禁用配置导出、一个后台任务取消禁用其他任务。修复前分别见[预算](../test-results/goal-ui-budget-before.log)、[运维](../test-results/goal-ui-operations-before.log)、[资源](../test-results/goal-ui-resource-before.log)。预算保留编辑版本/输入，409读取当前值并显示差异，只有明确选择当前版本后重新提交；运维/任务使用各自等待和重复提交保护，旧轮询与旧创建响应不覆盖较新状态。四项专项在[源码迭代浏览器报告](../test-results/goal-ui-state-browser.json)通过；任务与慢请求部分管理回复使用替身，只证明UI状态，不代替后台提供方实验。另新增375/390/768/1280和十页字段标签检查，修复768px Key行按钮溢出、一个label包两个权限控件以及缺少名称的Key/用量控件；[本轮最终网页报告](../test-results/goal-ui-final-browser.json)未产生或失败时不计通过。后续在旧包再次复现[账号名称草稿丢失](../test-results/goal-ui-account-before-retest.json)与[Key限额草稿丢失](../test-results/goal-ui-key-before.json)。账号名称/凭据、Key权限、模型元数据和路由配置现保留独立编辑版本/输入，409读取当前值并展示差异，只有用户明确选择当前版本后重提；放弃修改读取当前值。凭据差异只显示版本、代次和配置状态，成功后清空输入。四类实体专项先通过，整套脚本在新增辅助模型引起的导入fixture数量断言失败，修正fixture隔离后[源码迭代整套网页报告](../test-results/goal-ui-entities-browser-final.json)通过；较早失败报告保留。模型元数据保存后价格失败会明确显示已完成部分，并保留输入和原错误。[本轮包网页报告](../test-results/goal-entities-final-browser.json)需核对实际结果与BuildID。来源/设置等表单、完整键盘、错误字段关联与200%缩放仍按R072保留。后续在该包[复现客户端全局等待](../test-results/goal-ui-client-focused-before-corrected.log)：A恢复预览暂停，B记录被禁用。客户端历史/配置/检测现使用独立等待、同步重复提交保护及分别的选择版本，旧恢复预览不能覆盖新记录；错误仍保留原消息。该UI修改的[源码迭代两项专项](../test-results/goal-ui-client-focused-source.json)通过：A慢恢复不阻塞B、旧回复不覆盖新预览，慢检测不阻塞配置预览，重复submit仅派发一次。初次测试选错导航/下拉框的失败日志保留。[本轮整套网页](../test-results/goal-ui-client-final-browser.json)及[同包汇总](../test-results/goal-ui-client-final-checks.json)需读取实际结果，未产生或失败不计通过；管理回复使用明确替身，不涉及实际客户端文件。

Windows 接入已从此前Codex记录恢复，实际连接为 `Administrator@100.81.107.120`，只在进程内传入认证。[环境清单](../test-results/goal-windows-current-inventory.json)确认Windows 11专业版64位；starlight实际为Darwin，不能作为Windows证据。验收仅使用独立临时目录、便携Go/LLVM工具链和可清理的本人计划任务。Windows原生CGO构建与包闭包校验已通过；[服务源码迭代报告](../test-results/goal-windows-service-source-v5.json)八项通过，含安装不启动、PID/端口/build、内置网页、最低权限任务配置、冲突保护、停启和卸载保留数据，任务已删除。该报告未证明实际登录触发或无Node的全新电脑。实机修复了本地化任务查询、UTF-16任务XML、导出省略RunLevel、exe更新路径、Windows原生ACL/磁盘容量、稳定目录身份及HTTP路径分隔符。此前完整Windows测试失败仍保留，完整race、真实更新助手和最终同包尚待读取新结果；不能以构建或服务通过替代这些验收。

逐模型文本验证还发现一个独立假阳性：订阅来源的空完成事件和只有函数参数的流均可被标记文本通过。[修复前日志](../test-results/goal-model-text-before.log)覆盖三协议的JSON/SSE；现从有界验证输出读取实际文本，不把工具参数或成功传输当作文本。上游请求成功、usage和逐项失败结果的合同保持原样。[定向race](../test-results/goal-model-text-after.log)通过。随后旧冻结包的真实验证被上游400拒绝；[请求形状诊断](../test-results/goal-model-text-request-shape.json)确认订阅Responses要求消息数组，字符串输入失败。来源测试与逐模型验证现构造标准input_text消息数组，不修改用户原生请求。上述属于源码迭代，新冻结包真实六项文本验证未产生或失败时不计通过。

本轮Windows原生源码迭代的[完整race](../test-results/goal-windows-native-race-v9-summary.json)已通过：314个顶层、630项含子用例，2项外部CLI未运行；包含真实临时更新子进程的关闭准入、健康核验及激活，管理器查询仍为替身。Windows的真实任务生命周期由前述独立报告证明。最终客户端ACL通过重新打开同一文件对象取得READ_CONTROL/WRITE_DAC，保留os.Root目录约束；写入的客户端配置和Skills文件不授予执行权限。[定向实机修复日志](../test-results/goal-windows-client-acl-read-control.jsonl)与此前失败记录均保留。断电与实际登录触发尚未完成，不能由race推导。

[来源与设置源码迭代网页报告](../test-results/goal-ui-source-settings-after.json)共43项通过。新增来源409差异/显式重提/放弃，运行限制及保留期保留编辑版本和草稿；[设置复现](../test-results/goal-ui-settings-before-retest.log)确认刷新将4覆盖为5，[来源复现](../test-results/goal-ui-source-cas-before.log)确认只有错误提示而没有差异选择。缓存/通知的同类状态、字段错误关联、完整键盘/200%与导航草稿仍需独立核验，不计R072全部完成。

`c725b4f2f7f4…` [Mac冻结包检查](../test-results/goal-three-platform-mac-final-checks.json)已完成314个顶层/630项含子用例、零跳过，40项网页、34项SDK及7项生命周期；[真实六项文本验证](../test-results/goal-three-platform-real-text-verification.json)通过。旧验收脚本两次将CLI在上游正式完成之后关闭HTTP判为整轮失败，两个原报告保留；三协议工具和用量对账实际通过。数据库只读检查确认取消请求的upstream_status为completed、observation及usage完整、delivery为partial，符合已定义取消合同。新脚本增加独立CLI最终答复断言，只在这些证据均成立时接纳此客户端终态，同时报告原取消状态；不修改生产失败/取消合同。网页Responses示例也使用上游已验证接受的input_text消息数组。本轮新冻结包以[三平台Mac汇总](../test-results/goal-platform-final-checks.json)为准；真实结果、Windows/Linux同源码产物及性能报告未产生或失败时不计通过。

同一冻结包的最新结果由[汇总报告](../test-results/goal-final-checks.json)记录。新增严格预算改动需要新包的完整检查，前一包的绿色结果不会用于证明新包。报告未产生、命令非零、BuildID/hash 不匹配或门槛未全通过时不计验收；源码迭代证据不代替同包检查。

独立5572实例已完成本人Cove OAuth，实际目录返回9个模型；在网页启用gpt-5.6-luna并创建Key，一次性明文刷新后消失。`c9c12bcf5ce7…` 的[真实主流程首次报告](../test-results/goal-entities-real-live-retest.json)中，Codex CLI 0.159.2实际执行本地printf工具并完成最终答复，5条请求的实际usage与汇总一致；三协议专项虽然均返回200，但未通过工具断言，整份报告仍失败。不能将200视作工具验收通过。

[真实流诊断](../test-results/goal-real-tool-stream-diagnostic.json)确认完整add调用在 `response.output_item.done` 中，正式完成事件的output为空数组。转换器与验收客户端已补读完成输出项，仍要求正式终态，并限制累计大小、索引、重复完成及终态/已发送内容冲突；原生Responses线数据保持直传。[官方Codex解析器](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/sse/responses.rs)分别消费完成项与完成事件。定向检查以[实际日志](../test-results/goal-real-output-focused.log)为准；`7cc37314a390…` [修复包真实报告](../test-results/goal-real-output-live.json)三协议两轮、网页Key及实际Codex CLI均通过，8条成功请求的15287输入/359输出token与汇总相符；订阅费用保持unknown。此包34项官方SDK合成、38项网页、完整race（首次两CLI被错误环境变量跳过，单独正确启用后两项补测通过）见[同包汇总](../test-results/goal-real-output-checks.json)。该包之后的UI修改属于下一源码迭代，不能沿用其BuildID宣称新包已验收。较早的测试fixture错误与失败日志保留。真实报告继续使用 `scripts/verify-live.mjs`；未登录或空cases不算通过。Windows连接已恢复；自有OAuth注册和其余供应商成功合同仍待补齐；D01-D03 与 E01-E10、严格预算真实提供方实验、其余模块专项验收仍按台账保留。

## 本次审查修复与同一构建复验（2026-09-30）

已复现并修复原审查的三个问题，修复前失败见 [review-regression-before.log](../test-results/review-regression-before.log)，回归见 [review-regression-after.log](../test-results/review-regression-after.log)：

- 固定来源的原生 Chat/Messages 派发前读取模型的启用状态；停用后上游调用次数不再增加。Responses 保留原有请求验证错误语义。
- Key 管理请求读取正文时释放生成准入锁；读完后重新加载 Key 并执行 version CAS，慢上传不会阻塞无关模型调用，也不会覆盖期间的新修改。
- 轮换保留原 Key 的预算作用域，所有代次共用原预算周期与账本；已结算金额、在途预留和待核对金额保留。显式关联和独立创建的 Key 预算都跟随轮换，旧请求仍可结算，其他 Key 不能关联。网页说明同一预算的共享关系。

本次的实际检查退出状态、源码 BuildID、二进制与开发包 hash，以及报告是否匹配同一构建，以 [review-final-checks.json](../test-results/review-final-checks.json)为准。完整 Go TCP/race 测试、静态检查、34 项官方 SDK 合成案例、隔离生命周期与网页业务复验分别记录在 [race JSON](../test-results/review-full-race.json)、[check log](../test-results/review-check.log)、[SDK](../test-results/review-sdk.json)、[runtime](../test-results/runtime-acceptance.json)和[浏览器](../test-results/local-entry-acceptance.json)。浏览器覆盖网页创建专属预算 Key、轮换一次明文、验证新 Key 的模型目录认证后撤销旧 Key、隐藏/刷新不可找回、路由/别名预览、预算修改、配置导入/替换与移动宽度；不把模型目录认证算作真实模型调用。

真实订阅验收使用独立空数据目录，不读取日常 Codex 凭据。流程为本实例 OAuth 登录 → 实际模型目录 → 网页创建 Key → 三协议实际工具结果回传与最终回答 → Codex CLI 工具 → wire usage、请求与汇总对账。必须由账号本人完成登录；[review-live.json](../test-results/review-live.json)记录实际状态，`passed=false` 或空 cases 不算验收通过。脚本失败仍输出报告，不输出凭据、Key 明文、提示词或响应正文，创建的验收 Key 在退出时撤销：

```sh
node scripts/verify-live.mjs /absolute/isolated/config.json test-results/review-live.json
```

本次修复不闭合九条外部合同，不证明严格费用硬封顶，也不重用早期性能数据为新包背书。仍为 macOS arm64 开发预览，不能标记“Spec v1.2 全部完成”。下文是此前证据与当时限制的归档；最新结果优先读取上述本次报告。

## 此前已完成的检查与边界

| 检查 | 当前证据 | 能证明的范围与剩余 |
| --- | --- | --- |
| Go race | [v12-go-race.log](../test-results/v12-go-race.log) | `cmd/gatt 20.155s`、`internal/app 48.964s` 通过。这是 WS、Quota 和后台接入之前的完整基线。最终主目录的 [154个无监听测试](../test-results/v12-final-offline-tests.json) race 通过（internal/app 31.659s）；[最终日志](../test-results/v12-final-offline-race.log)不包含被沙箱阻断的 TCP 测试。 |
| vet 与构建 | [v12-go-vet.log](../test-results/v12-go-vet.log)、[v12-build.log](../test-results/v12-build.log) | 已完成 `go vet`、前端类型检查/Vite 与嵌入 Go 构建；vet 日志为空，退出成功由本轮执行记录确认。最终主目录 `go vet ./...` 与前端类型检查/Vite 通过，分别见 [final vet](../test-results/v12-final-go-vet.log) 和 [final web build](../test-results/v12-final-web-build.log)。原生二进制、内嵌资产及源码快照由 [build-evidence.json](../bin/build-evidence.json)记录。 |
| 真实 Codex CLI | [v12-codex-cli.log](../test-results/v12-codex-cli.log)、[codex_client_test.go](../internal/app/codex_client_test.go) | 本轮安装的 Codex 0.158 实际发出两次 HTTP 请求，执行本地合成工具并回传结果。上游为合成服务；真实订阅模型、长会话、并行工具、WS 与 compaction 尚未验收。 |
| 官方 Python SDK | [v12-sdk.json](../test-results/v12-sdk.json)、[verify-sdk.py](../scripts/verify-sdk.py) | OpenAI 3.22.0、Anthropic 1.9.0、google-genai 2.25.0，34/34 合成案例通过。六方向为 Gemini 与 Responses/Chat/Messages 双向，每方向 JSON、SSE、工具、取消与拒绝语义各一例，另有四协议本地认证错误；不是所有协议组合或真实提供方全部通过。 |
| 浏览器入口 | [v12-browser.log](../test-results/v12-browser.log)、[local-entry-smoke.mjs](../web/local-entry-smoke.mjs) | 隔离构建的十页入口及 375/390 宽度、直达/刷新/新标签/重启、API Keys 导航与存储故障解释已测。路由/别名/预算/配置替换业务脚本已补，待最终 binary 执行；未执行的表单、键盘和异步交互不计 PASS。 |
| 生命周期 | [v12-runtime.log](../test-results/v12-runtime.log)、[verify-runtime.py](../scripts/verify-runtime.py) | 新目录、目录锁、强制结束后的 interrupted、缺凭据阻止派发等回环实验已测。该脚本的复制目录恢复不能替代 age 备份和受控启动器全流程。 |
| age、恢复与更新 | [full_backup_test.go](../internal/app/full_backup_test.go)、[platform_restore_test.go](../cmd/gatt/platform_restore_test.go)、[update_switch_test.go](../cmd/gatt/update_switch_test.go) | Go 测试覆盖错误口令/hash、秘密闭包、一致 generation、恢复日志与失败回退。macOS 真实临时子进程已验证恢复/更新共用的 closed-admission、私有初始化健康、公共 ready503 与激活门禁；服务管理器测试使用模拟 runner。Linux/Windows 实机及干净机器安装未执行。 |
| 配置转移 | [v12-config-transfer.log](../test-results/v12-config-transfer.log)、[config_transfer_test.go](../internal/app/config_transfer_test.go) | typed 全事务、金融版本 CAS、依赖冲突、明确 skip 关联和选定实体替换已回归。可转移 sources/models/routes/aliases/budgets/prices/runtimeSettings；不导出账号凭据、Key、请求或秘密，不支持猜测 Key 预算依赖。 |
| Quota | [v12-quota.log](../test-results/v12-quota.log)、[quota_observation_test.go](../internal/app/quota_observation_test.go) | 11.470s focused race 通过；API、共享刷新、代次/CAS、窗口 TTL、历史、派发/路由门禁与通知事实源已接入。测试仍为固定官方合同的假上游，真实提供方额度实验 E04 未执行。 |
| 可观测性 | [v12-tracing.log](../test-results/v12-tracing.log)、[observability_test.go](../internal/app/observability_test.go) | 合成 OTLP HTTP collector 验证请求/尝试/refresh span、时间关系、隐私和关闭 flush；不代表真实部署 collector 的验收。 |
| 性能 | [verify-performance.py](../scripts/verify-performance.py)；[performance-v12.json](../test-results/performance-v12.json) | [performance-v12.json](../test-results/performance-v12.json) 已完成：8路/1800秒、100万记录，查询P95 1.325ms、附加TTFT P95 2.325ms、RSS增长6.61MiB、取消释放0.063秒，全部门槛通过。报告的 build_id 精确限定为早期基线，不冒充最终构建。 |

早期 SDK 报告 binary SHA-256 为 `5c8dd60ea72251a2c98374f3d0bf133a2cd2015a02bd27e8cad3c4f00acffa3b`；浏览器证据 BuildID 为 `961a5583ead47d8b9e1888eb8e618b970548e41464e72e6791068ece8656c3bd`。这些属于基线临时构建，原报告保留；最终构建独立记录源码与二进制 hash，不能把基线报告移作最终产物的运行验收。构建日志还报告 npm 依赖审计的一项 high severity，当前没有修复证据。

此前实现轮次的后半程环境从 unrestricted 改为 workspace-write，且 network restricted / approval never。实际 `socket.bind(127.0.0.1:0)` 返回 `Operation not permitted`；依赖新TCP监听的测试因此不能在当前环境重跑，不能记为代码失败或PASS。后台模块已复制到主目录；最终检查没有使用 overlay。[集成定向race](../test-results/v12-final-integration.log)（3.344秒）与154个无监听测试的race通过，覆盖后台验证、配置搬运、账号额度TTL、Key取消归属和Operation初始快照。配置预览也已加文件/模式绑定与迟到响应保护。离线浏览器尝试不使用TCP，但 Chrome 在启动阶段 SIGABRT，未进入页面；[错误记录](../test-results/v12-final-offline-browser.json)的 UI cases 为空，不能记PASS。

## 尚未闭合的合同和验收

设计合同仍有九条外部待补：R012、R018、R019、R028、R066、R074、R075、R079、R097。D01 是额外订阅提供方的 Cove 独立 OAuth 注册/redirect/scope/audience；D02 是提供方模型、额度、私有接口、完整 operation、服务端资源和费用合同；D03 是剩余扩展/闭源客户端及 Cursor 的稳定自动模型配置入口。固定 Codex 0.158 WS/compact 与八客户端 MCP/Skills 的适配设计已定义，真实使用证据仍单独待验。[设计台账](spec/design-readiness.tsv)与[适配附件](spec/03-EXTERNAL-ADAPTER-CONTRACTS.md)保留这些区别。

E01–E10 是账号授权、三协议工具、计数与限制、真实额度、故障切换、预算并发、配置恢复、干净安装、高级协议及更多提供方的运行实验。合成 E05/E06 场景已有局部证据，不能推导整组实验完成。真实授权和 provider wire、Codex 长会话、媒体/资源/后台、各客户端实际加载与直连恢复、各平台安装和服务生命周期仍待执行。WS原生取消修复的早期冻结版本通过19项真实TCP定向race（6.712秒），[证据转录](../test-results/v12-websocket-baseline.json)注明当时工具stdout与源码hash；之后新增显式Key owner的最终TCP复验受限。后台资格验证已接入真实现有Key、共享准入/预算/记录和只读终态观察，7组新测试及最终主目录定向race通过；未知状态、无可核验文本或失败不发布资格。后半程沙箱变更禁止新建本机监听套接字，最终完整网络测试、SDK、浏览器与真实子进程复验受环境限制；此限制与D/E外部合同缺口分别记录。

通知使用完整 Spec §33.2 的默认剩余额度 <=10%；适配附件已统一，没有增加阈值配置。R099 在该历史基线中范围外；2026-10-05用户已选择单服务器企业扩展，当前实现与验收见本文新增章节及独立企业规格。用户已选择 MIT，LICENSE 已纳入交付。正式签名尚未提供，当前包仍是本平台开发产物：[macOS arm64开发包](../bin/cove-development-darwin-arm64.tar.gz)、[SHA-256](../bin/cove-development-darwin-arm64.tar.gz.sha256)。最终源码、checks与产物状态见 [final-checks.json](../test-results/v12-final-checks.json)。

台账的 `implemented_local*` 表示已有代码和相应本地检查，剩余栏仍限制结论；`supported_subset*`、`*_external_pending` 表示支持子集或合同未闭合；`integration_pending` 是本地接入/回归未完；`performance_pending`、`*_ui_partial` 和 `ui_acceptance_pending` 不算相应验收通过。每行 code/test 是当前实际文件与符号锚点，evidence 指向已记录的执行范围；整包 race 日志只证明该次基线，不保证其后新增测试已在同一产物运行。

## 启动和复验

源码构建需要 Go 1.26.2、Node 22.12+、npm、Python 3 和本平台 C 编译器，SQLite 使用 CGO；嵌入产物运行不需要 Go 或 Node。先复制配置选择空闲 loopback 端口和独立私有目录，保留现有实例，再执行：

```sh
make build
./bin/gatt -config config.example.json -data-dir /absolute/private/Cove serve
```

示例端口是 `127.0.0.1:5569`，有现有监听时须先修改副本配置。健康检查使用所选端口的 `/healthz` 和 `/readyz`；staged/quiesce 时公共 ready 为 503，只有持有私有运行令牌的启动器能查询真实初始化健康。管理页从该 origin 进入，依次保存账号/来源、模型、API Key，再按客户端向导调用。HTTP 200 或配置文件写入成功不能替代工具执行与最终回复证据。

源码冻结后的无监听检查与原生构建已独立归档。此前受限时留下以下复验流程；本次执行状态以 review-final-checks.json 为准。以下现有流程用于补齐最终产物验收，并记录二进制 hash、BuildID 与退出结果：

```sh
make test
make check
GATT_CODEX_E2E=1 go test ./internal/app -run '^TestCodexCLIToolLoop$' -count=1 -v
python3 -m venv /private/tmp/cove-sdk-verification
/private/tmp/cove-sdk-verification/bin/python -m pip install -r scripts/verify-sdk-requirements.txt
/private/tmp/cove-sdk-verification/bin/python scripts/verify-sdk.py --binary bin/gatt --output test-results/v12-sdk-final.json
node web/local-entry-smoke.mjs
python3 scripts/verify-runtime.py
python3 scripts/verify-performance.py --binary bin/gatt --duration 1800 --rows 1000000 --output test-results/performance-v12-final.json
make package
```

浏览器脚本依赖其声明的 Playwright 环境；浏览器和 runtime 脚本读取仓库的 bin/gatt，并自行创建隔离数据目录。基线性能实例已完成；报告与基线 BuildID 绑定。原生构建证据和本地包由 [build-evidence.py](../scripts/build-evidence.py) 与 [package-platform.sh](../scripts/package-platform.sh)校验；用户服务、备份恢复和更新的具体步骤见[平台命令](platform-commands.md)。不得将跨平台编译、模拟管理器或本机开发启动写成对应平台实机安装通过。
