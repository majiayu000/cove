# Cove v1.2 实施与验收记录

这是当前源码的实施快照，基线为[完整 Spec v1.2](spec/01-COVE-COMPLETE-SPEC.md)。[104 项实施台账](implementation-readiness.tsv)是唯一逐项记录：设计状态取自设计合同，实现状态按当前代码和已完成检查重新核对，没有沿用规范中的旧 `current_status`。当前是本地开发预览，不能标记“104 项全部完成”。

已实现本机账号与来源、模型、API Key、协议转换、路由、用量与软预算、记录、客户端文件配置、备份、受控恢复和更新，以及可观测性与通知。高级协议按来源及模型能力开放；未知资格、额度、价格或计费上界保持未知并给出拒绝原因。严格预算已实现一个限定成功路径：官方 OpenAI Responses、无状态文本/客户端函数工具、明确 default 服务层、正 max_output_tokens 和完整用户配置 token 价格。通过官方输入计数接口取得输入量，按输入量和输出上限向上取整预留；这是本地已知计费规则下的准入上限，真实提供方成功验收仍待执行。其他来源或操作没有可信完整上界时拒绝。请求只在可证未发送的拨号失败时有限重选；未知提交、429、5xx、超时和流中断不会重放。

先前审查修复轮使用独立临时目录与合成凭据、回环上游验证，没有读取日常客户端凭据或调用真实收费模型。本次完整目标另执行了可清理的当前用户服务验收，见下文；没有停止用户已有实例。当前数据库为 schema v2，不提供旧数据迁移或回填。凭据受当前用户目录和文件权限保护，默认记录与导出不保存提示词、响应正文、工具内容或凭据。完整备份使用 age 口令加密并校验一致快照和秘密闭包；元数据备份需要重新补齐认证。恢复和更新先关闭新生成准入，验证实际进程身份和初始化健康后再激活；失败回退只适用于已定义的同 schema 流程。

## 2026-10-04 管理审计与编辑保护补齐

来源页增加直接打开账号与来源管理的入口，复用原有账号重命名、凭据、来源计量与高级设置。运维页新增管理审计，读取已有持久管理动作记录，按结果筛选、稳定游标分页和下载当前已加载记录。接口只投影时间、方法、路径、目标 ID、状态和 HTTP 状态，不返回请求体、请求 hash、重放响应或凭据，路径去除查询串。管理动作和财务审计保留90天；未确认动作继续保留，不随24小时备份文件清理。HTTP202标为已接受，管理请求成功不替代异步任务终态。

来源及管理窗口关闭和 Escape 现在检查未保存输入。取消关闭保留当前输入，确认关闭丢弃草稿并清空凭据；页面离开使用浏览器原生提示，不将草稿或秘密写入本地存储。成功提交清除草稿标记，只读筛选不触发草稿提示。模型和预算读取分别保留独立成功结果，读取失败不显示假空库并提供重试；模型、路由和预算保存后恢复提交按钮焦点。预算、单次费用估算、金额预留和缺价格拦截继续保留。

新增两个后端回归检查覆盖管理审计真实 Key 创建、脱敏、同时间分页、过滤、认证/方法/游标/存储错误、90天保留和未确认动作保护。本轮完整 race 共326项顶层、含子项677项通过，测试零跳过；没有测试的嵌入资源包由 Go 标作 package skip，与测试跳过分开记录。`make check` 已完成静态检查、类型检查和5项前端行为测试。

隔离 Chrome 通过真实本机管理接口新增模型与路由的 GUI 创建/编辑/删除、模型价格保存、模型网络失败保留输入、模型和路由409显式重提、提交焦点、来源错误字段焦点/关联、关闭取消/丢弃/凭据清空，以及审计筛选/分页/脱敏下载。十个管理模块展开表单全部控件标签检查、375/768/1280宽度、模态键盘焦点与实际 Chrome 200% 浏览器缩放均通过；没有页面脚本错误。授权页仍只验证发起/取消/错误，不代表真实用户授权成功。

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

通知使用完整 Spec §33.2 的默认剩余额度 <=10%；适配附件已统一，没有增加阈值配置。R099 团队充值和远程多租户明确范围外，不计为已实现。用户已选择 MIT，LICENSE 已纳入交付。正式签名尚未提供，当前包仍是本平台开发产物：[macOS arm64开发包](../bin/cove-development-darwin-arm64.tar.gz)、[SHA-256](../bin/cove-development-darwin-arm64.tar.gz.sha256)。最终源码、checks与产物状态见 [final-checks.json](../test-results/v12-final-checks.json)。

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
