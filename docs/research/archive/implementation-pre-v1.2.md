# 实施与验收记录

产品名：**Cove · 栖港**，介绍语「你的模型，泊在一处」。独立的 Go / SQLite / React 个人 AI 网关；仓库路径和运行命令仍为 gatt。

日期：2026-09-29。项目：`/Users/lifcc/Desktop/code/AI/tools/gatt`。

基线：[原产品技术 Spec](/Users/lifcc/Downloads/personal-ai-gateway-plan-2026-09-29/03-product-and-technical-spec.md)。[竞品研究](/Users/lifcc/Downloads/personal-ai-gateway-plan-2026-09-29/01-competitor-research.md)仅提供研究依据；本项目未复制或引入竞品网关核心。

## v1.2 W01 运行与入口（2026-09-30）

本轮按 [完整 Spec v1.2](/private/tmp/cove-unified-spec-7zvs0xpk/01-COVE-COMPLETE-SPEC.md) 的 W01 落地。新增 `/readyz`，在初始化完成后检查 admission、现有数据库/凭据故障锁和数据库连通性；探测最多等待一秒，失败沿用带请求 ID 的 503 错误结构，不返回内部错误或凭据。探测取消不写入永久故障锁。`/healthz` 只表示进程存活，关停排空阶段仍可响应；关停后的探测保持 Host 校验，不访问数据库、不登记新任务。`gatt open` 使用就绪检查。

`TestReadinessBoundaries` 覆盖健康、数据库故障锁、数据库关闭、凭据故障锁、关停及 Host 边界；`TestCancelledReadinessDoesNotLatchStorageFault` 覆盖取消后恢复；`TestOpenManagementRequiresReadiness` 覆盖启动器门槛。沿用自动会话与任务排空测试。

浏览器入口验收脚本现在等待 `/readyz`，核对运行接口、设置页与 `bin/build-evidence.json` 的 BuildID，并检查每个导航页和窄屏下 API Keys 入口可见。脚本继续使用临时数据目录、合成凭据，覆盖刷新、新标签、服务重启、保留来源/Key 和会话存储错误。机器可读结果写入 `test-results/local-entry-acceptance.json`。

本轮检查命令：`make test`、`make check`、`make build`、`node web/local-entry-smoke.mjs`。实际完成结果见本轮交付说明和验收文件；下方历史检查不能替代本轮结果。W02 及后续工作包、真实订阅调用和外部准入依赖不计入 W01 完成范围。

## API Key 入口修正

已应用用户提供的 api-key-entry.patch：导航改为「API Keys」，页面标题与按钮改为「创建 API Key」，列表改为「已创建的 API Key」，空状态明确提示选择来源创建。同步 README、接入说明与现有浏览器检查的导航/标题定位；账号、Key 生成和绑定逻辑不变。

本次仅重跑 TypeScript 检查、前端与嵌入式二进制构建，不将下方架构快照的 31 项测试视为本次重跑。当前运行服务尚未切换；需运行新版二进制后才能看到入口变更。

## 按架构复核落地（此前快照）

依据 [架构复核原文](/private/tmp/cove-implementation-review-tw54uteu/ARCHITECTURE-REVIEW.md)，保留单 Go 服务、嵌入 React、SQLite 元数据与私有凭据文件。新增三入口范围由用户后续要求授权，以 [三协议合同](api-compatibility.md) 覆盖外部原 Spec 的 Responses-only 限制；原 Spec 在当前可写范围之外，未直接改动它。

| 复核项 | 本次结果 | 证据 / 未完成项 |
|---|---|---|
| P0 真实订阅工具循环 | 未验收 | 保留用户指定 gpt-5.6-luna；当前会话无法监听/连接本地端口，也不能写现有数据目录，未运行 verify-live.mjs、重置登录或替换运行服务 |
| P0 三入口共享执行与有限语义 | 已实现，进程内验收通过 | TestProtocolEntrySharesExecution：同一 Key/来源策略、鉴权/容量先于正文、一次派发及记录、取消；TestCompatSemanticBoundaries 与 TestCompat*：JSON/SSE、工具回传、增量输出、用量未知、流冲突与超限 |
| P1 后台任务结束约定 | 已实现，进程内验收通过 | App 统一登记主请求、OAuth 监听/回调、刷新和维护；关闭 admission 后禁止启动新任务；TestShutdownDrainsOwnedTasks 覆盖无请求的刷新/登录、晚到回调、强制取消请求、超时及完成后持久状态 |
| P1 构建与验收版本对应 | 构建产物留摘要与源码快照 | make build/package 注入源码 SHA-256 为 BuildID；bin/build-evidence.json 记录源码、dist、二进制和源码归档；设置页及 /admin/status 可核对运行实例。当前运行实例未切换 |
| P2 来源专属规则 | 已收拢为具体函数 | validateSourceCapabilities、prepareUpstream、sourceStream；TestContractSourceRules 验证原生 opaque 正文/流字节、订阅认证头、缺 Content-Type 的有限识别；不增加插件注册器或来源框架 |

状态归属保持不变：App 协调生命周期；SQLite 决定来源/Key/绑定/请求事实；秘密文件持有唯一凭据材料。replaceCredential 仍是保存新秘密、提交引用和补偿清理的入口，转换器不访问这些副作用。

关停主 HTTP 的宽限期为 5 秒，超时强制取消并关闭传输；随后等待自有任务最多 5 秒。只有等待成功才主动关闭 DB。若仍有任务未结束，报错退出，让操作系统结束进程资源，不把发出 cancel 当作任务已完成。监听服务异常退出也经过此关停流程。

当前已完成的进程内回归为 **31 项顶层测试**（含子场景），命令：

```sh
GOCACHE=/private/tmp/cove-go-cache go test -race ./... \
  -run 'TestContract|TestCompat|TestProtocolEntrySharesExecution|TestShutdownDrainsOwnedTasks|TestFileSecrets|TestBoundedObserver|TestPartialPrice|TestDataDirectoryLock' \
  -count=1 -timeout 40s -json
```

静态检查使用 `make check`。构建使用当前已安装的锁定依赖执行 `npm --prefix web run build`、带 BuildID 的 `go build` 和 `build-evidence.py record`，然后 `make -o build package`；未重复安装 npm 依赖。前端、二进制与归档摘要由构建脚本生成，测试报告关联同一源码 ID。

测试明细为 test-results/architecture-tests.jsonl，验收快照关联为 test-results/architecture-verification.json；构建清单为 bin/build-evidence.json。历史 21 项测试不作为本次三协议证明。

全量 `go test -race ./... -count=1 -timeout 60s` 实际尝试后受阻：`httptest: failed to listen on a port ... bind: operation not permitted`。完整日志保留在 test-results/architecture-full-test.log，不能计为通过。新版浏览器、真实订阅工具循环、网络故障、跨机器安装仍未验收；不宣称完整首版交付。

## 三协议实现前的开发结果与限制

本轮重新核对 Spec 正文及附录 D/E，验收总数为 **45 项**。以前的 38 项表漏掉 A39—A45，不能作为完整实现的证明。

- 管理面：原生启动器凭持久身份申请一次性票据；页面兑换短期 Bearer，保存在当前标签的 sessionStorage。所有管理 fetch 使用 credentials=omit、redirect=error；不使用 Cookie；OAuth 窗口清除复制的管理会话。
- 登录与刷新：浏览器 PKCE、nonce、来源版本/代次检查；ID token 按 RS256、发现文档/JWKS、issuer、audience、exp、nonce 校验后才采用身份。换账号先在原管理页确认，临时新凭据只存内存。localhost 回调同时独占 IPv4/IPv6。真实提供方的发现文档、签名算法与回调仍待联网复验，不以合成签名测试证明提供方兼容。
- 刷新按来源共享任务，网络等待释放生命周期锁。单个调用取消不取消共享刷新；退出或换绑使旧结果失效。退出分别返回本地解绑、秘密清理、上游撤销状态。
- 凭据文件在 rename 后同步父目录；目录同步结果不确定时拒绝继续读写凭据，重启后重新载入。沿用现有不可变随机引用格式，尚未采用附录 D.6 的 schema_version/admin_secret/credentials 顶层布局；没有修改用户已有文件格式。
- 数据面在缓冲请求体前鉴权、获取并发槽，读取设截止时间；模型/token POST 不提供可重放正文。SSE 空闲计时仅覆盖上游读取；取消会唤醒阻塞写入。请求独立记录上游结果、客户端交付和观察完整性，无法观察的终态为 unverified，已知 usage 不丢弃。
- 页面补齐按来源/客户端/时间/调用类型筛选、可读请求详情、结果分布、文本测试模型和记录入口。工具页按所选模型生成独立 Codex 启动命令，隐藏输入客户端 Key，退出后恢复原终端环境。
- 压缩包新增 Finder 可双击的 `Cove.command`。这是未签名/未公证的开发包，不能声称干净机器安装已验收。

**运行边界：** 本轮会话禁止本地端口连接/监听，并且不能写入用户现有运行数据目录。HTTP 集成测试实际遇到 `bind: operation not permitted`；已有服务保留运行，没有切换到本轮二进制。进程内测试使用真实 SQLite、处理器和 SSE 观察器，假传输只替换网络边界。真实账号工具循环、实际浏览器和网络故障验收仍待执行。

## 登录合同修正（用户反馈后生效）

默认且唯一账号登录方式改为浏览器 OAuth Authorization Code + PKCE，依据 CLIProxyAPI d33f63f 的 `GenerateAuthURL/ExchangeCodeForTokens` 和 Sub2API 9a62841 的授权会话实现。删除 Device Code 实现，避免维护两套当前不需要的流程。点击「使用 ChatGPT 登录」打开服务方授权页，由配置的 `http://localhost:1455/auth/callback` 接收结果，校验一次性 state 与 PKCE 后保存本地凭据文件，再返回 Gatt。1455 是该 OAuth 身份的固定回调合同，不作为网关服务端口；被占用时明确提示，不偷偷换端口或接管其他程序。

管理身份与来源授权、客户端 API Key 分开。管理页面现在提供首次设置密码和普通密码登录。首次设置仍要求有效本机管理会话：`gatt open` 读取本实例文件凭据，取得 60 秒单次票据，以 fragment 交给页面并立即清除；设置完成后 `open` 直接打开普通登录地址。原生票据接口保留给显式本机工具，不作为日常网页登录前置条件。

管理密码采用随机盐与 PBKDF2-HMAC-SHA256（600,000 次）摘要，保存在 SQLite settings；不写明文。短期 Bearer 保存在 sessionStorage，最长 12 小时，重启失效后可直接输入同一密码登录。首次设置成功撤销之前的管理会话和票据，存储失败保留原状态。`recover-admin` 仅在停机并取得目录锁后清除密码、轮换本机凭据，保留业务数据。OAuth 回调仍不签发管理身份。

## 凭据存储修正（2026-09-29）

用户确认移除 Keychain。现使用 `data_dir/secrets/credentials.json`，目录 `0700`、文件 `0600`；启动读取到进程内缓存，修改在互斥锁内先写临时文件、同步、关闭、原子重命名，成功后才发布缓存。保留现有引用替换与数据库失败补偿机制；文件损坏时失败，不自动覆盖。文件是明文，保护边界是当前用户的文件权限，不宣称具有钥匙串隔离能力。

删除原生 Keychain 实现和依赖；验收脚本直接从本实例私有文件读取管理员凭据，不调用 `/usr/bin/security`。不读取、导出或删除旧钥匙串条目，也不增加兼容迁移路径；现有订阅来源需要重新登录。本机管理身份使用现有 `recover-admin` 重建。

选择依据：CLIProxyAPI d33f63f 的 `SaveTokenToFile` 使用 JSON 文件；CC Switch 846de29 使用 `codex_oauth_auth.json`、`0600` 原子写入及内存缓存；Sub2API 9a62841 使用账号 JSONB 凭据。之前的 Keychain 方案导致外部 `security` 验收脚本读取时反复授权，已撤销该实现决定。

真实验收历史：浏览器 OAuth 曾成功。首轮真实 CLI 验收记录 `req_e26489320019af12df822219` 为 HTTP 200 / `content_type` 失败；独立诊断得到未带 Content-Type 但含 `response.completed` 的 SSE 响应。该协议问题已按实际响应补齐：仅订阅来源缺少 Content-Type 时检查 SSE 前缀，原样转发并观察正式完成事件；明确错误类型、JSON 和 HTML 仍失败。合成上游和真实 CLI 工具循环已回归，真实上游工具循环仍待新文件存储中的授权。

## 实施决定

- D01/D02/D06：采用用户提供方案的 macOS 本机服务、Go + SQLite + React/TS，在空目录 gatt 中独立初始化。
- 端口按本机工作约定选用空闲 5569 并写入配置。相对原 Spec 的首次系统随机分配方案，这里使用固定配置端口，冲突时失败。
- 只设一个具体 app 包，按存储、管理、数据面、SSE、计量、Codex 凭据职责拆文件。没有引入插件 SDK、Redis 或任务运行平台。
- T03 观察到 Codex CLI 0.158.0 默认声明 `web_search`。接入模板必须使用 `web_search = "disabled"`；函数工具和函数 namespace 原样保留。固定客户端合成流程不需要 compact/WS，未据此推断长会话也不需要。
- 用户指定 `gpt-5.6-luna` 后，真实 CLI 捕获到 `input[].type=additional_tools`，role=developer，工具声明位于该输入项的 tools 字段。本轮先把这一具体类型加入协议子集，再实现原样透传；其工具种类仍受相同函数工具边界约束，不删除此项或把模型改成其他模型。
- 同一捕获显示 `functions` namespace 含 `custom:exec` 与 `function:wait/request_user_input`。因此首客户端需要客户端 custom 工具及 `custom_tool_call/custom_tool_call_output` 历史，加入当前有限子集。Gatt 只透传声明、输入及输出，执行仍由 Codex 客户端承担；服务端托管工具仍拒绝。
- 调用期间持有配置和凭据快照。管理变更与凭据发布通过同一生命周期锁串行，模型调用和共享刷新网络等待不持锁；刷新等待者取消与来源刷新任务取消分开。
- 没有独立迁移框架。数据库 schema 版本为 1，核心对象为 sources/client_keys/requests/attempts/settings，外加续接绑定。结构化元数据存在 JSON 列，查询字段与外键有独立列。

## 账号验证卡

| 字段 | 当前事实 |
|---|---|
| 目标 | 订阅模型入口，真实验收模型由用户指定为 `gpt-5.6-luna` |
| 官方依据 | [Codex Authentication](https://learn.chatgpt.com/docs/auth)描述订阅登录和 Device Code；不证明 Gatt 获得第三方 OAuth 注册 |
| 身份 | 配置中的 `app_EMoamEEZ73f0CkXaXp7hrann` 是所研究的官方 Codex 身份，不是 Gatt 自有身份 |
| 方式 | 浏览器 OAuth + PKCE + 本机自动回调；用户在服务方完成授权；Gatt 不读取现有客户端 token |
| 调用 | 配置指向 ChatGPT Codex backend 的 Responses SSE；API 来源另行配置 |
| 凭据 | 本地私有文件，独立实例目录；刷新意图先落盘，再消耗轮换 token |
| 失败 | 刷新响应未知、持久化失败或身份变化，转为需重登；不重用旧 token；重启遇 refreshing 同样拒绝调用 |
| 退出 | 本地退出、清理秘密；无经过验证的上游撤销接口，不宣称远端会话已撤销 |
| 当前结果 | 合成登录生命周期与刷新故障通过；真实浏览器授权成功，首轮真实工具循环失败，详见弹窗排查 |

## 15 项任务

| 任务 | 状态与证据 |
|---|---|
| T01 环境 | 本机 macOS arm64；Go 1.26.2、Node 26.7.0、Codex CLI 0.158.0 |
| T02 账号实验 | 真实独立授权成功；首轮真实调用未通过，详见弹窗排查 |
| T03 客户端协议 | 真实 CLI 对合成上游完成两轮函数工具循环；长会话 compact/WS 未验 |
| T04 直连对照 | 真实来源直连与网关对比待执行 |
| T05 最小服务 | Go 单进程、loopback、health、嵌入 UI、受控关停已实现 |
| T06 来源与凭据 | CRUD、本地凭据引用、替换事务补偿与哨兵检查通过 |
| T07 Key/Responses | 单来源 JSON、SSE、models、模型边界通过 |
| T08 工具/取消/错误 | HTTP 合成回归与真实 CLI 合成工具循环通过；真实模型待验 |
| T09 记录/usage/绑定 | 一个请求一次 attempt；原生 usage、未知/部分、绑定代次和清理通过 |
| T10 账号路径 | 浏览器 OAuth/回调/取消/刷新/退出已实现；真实账号待验 |
| T11 分离状态 | 页面分别展示认证、文本验证、调用健康、原生额度；真实额度未知 |
| T12 五页面 | 来源、工具、请求、用量、设置已实现；浏览器五页面、390px 布局、无页面异常检查通过 |
| T13 接入恢复 | 模板与逐字段恢复说明已实现；真实直连恢复待验 |
| T14 计量/诊断/保留 | 十进制费用、币种分组、脱敏导出、7 天保留与重启中断已实现 |
| T15 包装安装 | 本机二进制/压缩包构建；干净机器、签名公证未完成 |

## 45 项验收

“部分”表示已验证一部分边界，不能视为完整场景通过。A01—A38 的通过项记录此前版本的已完成检查；本轮受网络权限限制，不能视为当前版本全量回归通过。A39—A45 列出本轮实际覆盖及尚缺证据。

| ID | 当前结果 | 证据或待补项 |
|---|---|---|
| A01 | 部分 | verify-runtime.py 验证全新隔离目录启动、文件管理身份与 health；另一台干净机器待验 |
| A02 | 通过 | TestAdminBoundaryAndSecretIsolation；会话与准确 Origin |
| A03 | 部分 | 哨兵 DB/WAL/诊断检查通过；原生钥匙串实现已移除；文件存储权限与故障测试见本轮检查 |
| A04 | 通过 | browser-smoke.mjs 创建来源，保存后保持未验证；五页面与 390px 布局通过 |
| A05 | 通过 | TestNativeJSONAndOpaqueToolHistory；上下游凭据分离 |
| A06 | 通过 | TestSSEArbitraryChunksAndFunctionCycle、TestBoundedObserverOversizeAndUTF8 |
| A07 | 部分 | TestCodexCLIToolLoop：真实 CLI、合成上游、真实 printf；真实模型待验 |
| A08 | 部分 | 合成 opaque reasoning/phase/namespace 原样传递；真实加密推理待验 |
| A09 | 部分 | 短工具循环只使用 HTTP/SSE；长会话 compact 未覆盖，明确不承诺 |
| A10 | 通过 | TestUpstreamErrorsNoRetriesAndRedaction：400/401/429/503 |
| A11 | 通过 | TestHeaderAndIdleTimeoutNoReplay + TestTotalTimeoutWithActiveHeartbeats：响应头、空闲和总超时均不重放、不记成功 |
| A12 | 通过 | TestDisconnectNotSuccessAndNoRetry |
| A13 | 部分 | TestCancellationDisableRevokeAndDelete；真实上游取消待验 |
| A14 | 通过 | 两轮同来源绑定测试；管理端两轮续接验证 |
| A15 | 通过 | 换凭据旧引用拒绝；TestBindingsRejectAnotherKeyAndSource 验证跨 Key/来源拒绝 |
| A16 | 通过 | 外部 ID 拒绝、TestRetentionDeletesBindings |
| A17 | 通过 | 停用不取消已运行请求、撤销后新请求拒绝 |
| A18 | 通过 | 活跃请求或有效 Key 引用时删除返回 409 |
| A19 | 通过 | 缺失/部分 usage 保留 null，错误不计免费 |
| A20 | 通过 | 缓存子集、推理不重复加、历史价/多币种；TestPartialPriceDoesNotClaimTotal 验证部分费用不冒充总额 |
| A21 | 通过 | 网关和 CLI 均零重试；TestClientResendRecordsSeparateAttempts 验证主动重发产生独立请求与 attempt |
| A22 | 通过（本机） | verify-runtime.py 对运行中的真实二进制进程 SIGKILL，再启动后请求为 interrupted，合成上游计数不增加 |
| A23 | 通过 | 派发前 SQLite query_only 无调用；TestPostDispatchStorageFailureStopsAdmission 验证派发后故障阻止新请求 |
| A24 | 部分 | 重复登录、state/PKCE、取消和晚到结果已回归；真实 OAuth 曾成功，切换文件存储后需再次授权 |
| A25 | 待执行 | 必须使用用户授权的真实订阅模型完成工具循环 |
| A26 | 部分 | 合成刷新失败/退出已验；真实过期/重登待验 |
| A27 | 部分 | 登录/调用/额度独立，额度未知；真实返回待验 |
| A28 | 待执行 | 已提供手动三值比较流程，用户配置变更实测待验 |
| A29 | 待执行 | 不读写日常认证；真实直连恢复待验 |
| A30 | 部分 | 8 并发流、容量拒绝、取消与有界事件缓冲通过；持续慢消费者内存曲线待验 |
| A31 | 部分 | verify-runtime.py：完整目录恢复可调用；仅元数据恢复管理端保持锁定，recover-admin 后来源可见但无凭据仍禁止派发；异机待验 |
| A32 | 通过 | 跨 host 307 不跟随，目标未收到请求 |
| A33 | 通过 | TestCredentialTransactionRollback：新项清理、旧凭据保留 |
| A34 | 通过 | 工具/模态/模型/并发提前拒绝；TestBodyLimitsRejectBeforeDispatch 验证请求及响应体积边界 |
| A35 | 通过（合成） | 并发单刷新、未知旋转拒绝、响应丢失不复用、退出晚到不复活 |
| A36 | 通过（合成） | 订阅 nonstream/store=true/previous_response_id 发前拒绝 |
| A37 | 通过 | 管理文本与两轮测试走相同登记、用量路径 |
| A38 | 部分 | URL 与绑定代次同次更新；双真实端点续接仍拒绝 |
| A39 | 部分 | TestContractManagementBearerIsolation 验证票据、Bearer/Cookie/数据面边界、跨端口 Origin、注销；浏览器脚本已补新标签、刷新和重定向测试，实际浏览器待执行 |
| A40 | 部分 | 4 项 TestFileSecrets 测试验证并发保留、重开、权限、rename 失败；父目录同步后才发布缓存，掉电与目录 fsync 故障注入尚未完成 |
| A41 | 部分 | TestContractLogoutDurableBeforeCleanup：清理失败保留秘密，重开真实 DB 仍退出；真实进程各崩溃点注入待执行 |
| A42 | 通过（进程内） | TestContractOldResultDoesNotVerifyChangedSource 验证 version/generation 两种变更均不被旧结果覆盖，历史结果保留 |
| A43 | 部分 | TestContractAdmissionBeforeBody：慢正文占槽、无效 Key 和超容量在读正文前拒绝、槽释放后可复用；真实 socket 上传超时待验 |
| A44 | 通过（进程内） | TestContractDeliveryFailureKeepsUpstreamUsage + TestContractOversizeTerminalIsUnverified：JSON/SSE 写失败、超大终态原样传递、未知结果不计零 |
| A45 | 部分 | TestContractPostBodiesNotReplayable、TestContractBackpressureNotUpstreamIdle、TestContractCancellationWakesBlockedWriter；真实 Go 复用连接零写重连与断网故障仍待验 |

## 上一快照检查（三协议实现前）

进程内回归命令：`GOCACHE=/private/tmp/cove-go-cache go test -race ./... -run 'TestContract|TestFileSecrets|TestBoundedObserver|TestPartialPrice|TestDataDirectoryLock' -count=1 -timeout 40s`，21 项顶层测试通过。包括签名身份拒绝伪造/错误 issuer/audience/nonce/过期、账号改绑确认/取消/旧版本、共享刷新取消与晚到结果。

构建与静态检查：`go vet ./...`、TypeScript/Vite 构建、Go 二进制构建；真实账号、浏览器、HTTP 监听测试和干净机器安装不计入通过。

## 此前检查记录（不等于本轮重跑）

- 本轮流式与运行恢复：`go vet ./...`、`GATT_CODEX_E2E=1 go test -race -timeout 60s ./...` 通过，共 35 项测试函数；后端包 9.664 秒。真实 Codex CLI 的合成工具测试改为订阅来源且上游省略 Content-Type，仍完成两轮调用。
- `python3 scripts/verify-runtime.py`：新目录启动、跨进程数据目录锁、SIGKILL 中断恢复且不重放、完整备份恢复、仅元数据恢复、显式管理员恢复、缺来源秘密发前拒绝全部通过。仅使用临时目录及合成凭据，没有改动日常实例。
- 本地服务已更新；没有进行中的 OAuth 才执行重启，保留账号及文件凭据。前端将网络连接错误显示为可恢复的中文提示，不因此退出账号。`node web/browser-smoke.mjs` 已验证五页面、390px 布局，以及模拟断网后恢复且未退出管理会话。

- 本轮凭据存储改动：`go vet ./...` 与 `GATT_CODEX_E2E=1 go test -race -timeout 60s ./...` 通过，共 31 项测试函数，耗时 7.854 秒。新增文件权限、重启持久化、失败不发布缓存、临时文件清理、损坏/符号链接拒绝及并发保存检查。
- TypeScript/Vite 与 Go 二进制构建通过。服务已重启到文件存储版本，管理身份通过 `recover-admin` 重建；旧订阅来源已用正常退出接口标为未登录，没有读取或删除旧钥匙串秘密。

- `GATT_CODEX_E2E=1 go test -race -timeout 60s ./...`：27 项测试函数通过（26 项默认测试及 1 项显式启用的 Codex CLI 工具循环），耗时 8.479 秒。
- `GATT_CODEX_E2E=1 go test ./internal/app -run TestCodexCLIToolLoop -v -count=1`：真实 Codex CLI 0.158.0 两次 HTTP Responses、真实执行 `printf GATT_TOOL_OK`、工具结果回传并结束通过。
- `npm --prefix web run build`：TypeScript + Vite 成功，嵌入静态产物。
- `go vet ./...` 通过。`npm audit --omit=dev` 当次生产依赖检查无已知漏洞。
- `node web/browser-smoke.mjs`：启动器一次性票据登录、来源创建、五页面、390px 布局、无页面异常通过。`--oauth` 合成授权页面验证 PKCE 与回调地址后主动取消，仅作为 UI 检查。
- 本机服务启动并实际请求 `/healthz` 返回 `{"status":"ok"}`。

上述仅证明列明的边界。T02/T04/T15 和 G3/G6 尚有真实用户操作与外部环境依赖，完整首版不得标为完成。

## 网页管理密码登录验收（2026-09-29）

依据 11 项网关登录调研及修订后的 Spec §13.1–13.2，实现单管理员网页密码设置/登录、本机忘记密码恢复。设置页在有效本机管理会话内可提交；日常裸地址显示密码表单。修复同一标签收到首次设置 fragment 时未重新处理凭据的问题。没有改动来源 OAuth、模型派发或三协议转换。

本轮完成：

- `make test`：全量 Go race 测试通过，包含密码持久化、登录/退出/重启、匿名抢设拒绝、并发设置单一成功、存储失败保留设置会话、Origin 隔离、错误限速及恢复凭据轮换。
- `make check`、`make build`：Go vet、TypeScript 与 Vite/Go 构建通过。
- `node web/password-smoke.mjs`：独立临时数据目录与真实浏览器，验证首次归属设置、两次密码不一致、裸地址登录、错误密码、刷新/退出、重启后相同密码、重置后旧密码/票据/本机凭据失效、来源/Key/上游秘密保留、存储不可用说明；1280px 和 390px 截图已检查。仅假凭据，不请求真实模型。
- `python3 scripts/verify-runtime.py`：真实进程崩溃、目录锁、完整备份和仅元数据恢复回归通过。
- 浏览器结果见 `test-results/password-login-acceptance.json`；进程恢复见 `test-results/runtime-acceptance.json`；实机更新结果另记 `test-results/password-login-restart.json`。

构建时 `npm audit` 仍报告既有 Vite 7.1.12 开发依赖存在高危公告；本轮未进行无关依赖升级。当前运行方式为 Go 内嵌构建后的静态文件，不运行 Vite 开发服务器。真实订阅模型工具调用仍属于单独待验收项。

## 用户决定：关闭管理密码（2026-09-29，覆盖上一节）

移除密码设置/登录表单和密码设置端点，页面通过精确同源请求自动取得短期会话，直接进入来源页。`open` 直接打开普通地址，不读取本机凭据。刷新复用会话，服务重启自动重新建立会话；最多 32 个会话，满额淘汰最早到期者。保留 loopback、Host/Origin 校验、来源授权和客户端 API Key 边界。没有新增功能开关，不清理现有密码摘要或业务数据。

验收命令为 `make test`、`make check`、`make build` 和 `node web/local-entry-smoke.mjs`，本轮结果写入 `test-results/local-entry-acceptance.json` 与 `test-results/local-entry-restart.json`。前一节密码流程及其测试为历史记录，不再代表当前产品行为。
