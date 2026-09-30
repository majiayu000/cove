# Cove 外部适配合同 · v1.2

日期 2026-09-30。本文是主 Spec 第27、30、31、35节的规范附件。只补设计，不修改产品代码、真实凭据、客户端配置或运行服务。公开源码能证明字段和流程；不能证明 Cove 已取得独立客户端准入、当前账号有资格，或实际调用通过。

## 1. 证据、版本和完成口径

本次固定以下源码快照，版本号来自该快照的包清单，**不代表本机已安装或本次运行通过**。Codex、Claude Code、Cursor 的安装版本只执行了版本读取。

| 对象 | 固定版本/commit | 证据用途 |
|---|---|---|
| Codex | 目录/额度源码 `50d9c5deac4aa8cafc95db886d1435726ff0f77c`；本机 CLI `0.158.0`对应官方tag commit `064c6b8c737f5b41d171fdda80bd9ef10ad06eb3` | 模型目录、额度、WS header；源码快照和本机发行版分别记录，不能混作一次测试 |
| Claude Code | 本机 `2.1.281`；官方文档 2026-09-30读取 | settings、MCP、Skills、订阅准入边界 |
| Gemini CLI | `d75234cae935d58f896f4dbf305e0c51602fa385`；package `0.63.0-nightly.20260923.gf50ba8608` | CodeAssist项目、quota、API key自定义endpoint |
| Qwen Code | `a63157304aeccafed6a700bd9566ccd84cf1edfa`；`0.24.7` | 历史device wire与现行停用说明，二者分别保留 |
| OpenCode | `7945de208964a49300d7f770d1a71d078db9a4c4`；`1.18.33` | JSON/JSONC provider配置 |
| Cline VS Code | `647d8cb059f5083c53d959609ce04c82647ae0d6`；`4.1.21` | 模型字段、SecretStorage、MCP及Skills |
| Roo Code | `b867ec9145750d0ae1ff7f02d35406e9bf2a0b16`；`3.53.0` | provider profile、SecretStorage、项目MCP及Skills |
| Continue VS Code | `5522c6f44ca0ac3528b37244818fbfa39b5af470`；`1.3.40` | YAML v1模型及MCP schema |
| Cursor | 本机 `3.20.21`；官方文档 2026-09-30读取 | MCP/Skills文件；没有据此证明该版模型设置的稳定文件写接口 |

[来源索引](evidence/adapter-sources-v12.json)记录固定 URL、SHA-256 和读取范围；[合成样例](adapter-fixtures.json)记录配置、wire映射、三方恢复的输入和预期。所有 fixture 都是人工合成，**没有真实账号录包，没有客户端原生解析器执行结果**。公开源码中出现的内置 client ID/secret 不复制到发布配置或 fixture。

三个状态分开记录：合同文本已经写明；可进入实现的外部条件是否满足；产品及真实账号验收是否通过。阻塞合同写明“不发请求、如何显示、缺什么、满足什么才能恢复”，仍不算成功路径已经获得外部证明。原 D01—D03 不因新增此附件自动关闭，也不把需求移出范围。

## 2. 独立授权、刷新和身份

### 2.1 共同事务边界

沿用主 Spec 的 account、operation 和 credential owner，不增加通用插件注册中心。每个 adapter 的发布配置记录 `provider/version`、授权/token/identity/inference origins、grant、client metadata来源、redirect规则、scope以及 identity 方法。没有可用注册或官方开放依据时，管理操作在创建外部登录副作用前返回422和具体缺项；不生成虚假的登录成功状态。

浏览器流程保存 state、PKCE S256 verifier、nonce（OIDC时）到该 operation 私有内存；redirect必须精确匹配实际监听地址和注册规则。用户关闭/取消后销毁 verifier；过期回调409，不影响旧账号。device流程只把 user_code 和 verification URI交给前端；device_code不返回UI、不落普通日志。按服务端 interval轮询，缺省5秒；slow_down每次增加5秒；429服从更长 Retry-After；在 expires_in到期、拒绝、取消时停止，不自动重开授权。

token成功之后先验证身份，再原子保存 credential+generation，再做模型/额度查询。refresh按account singleflight，旋转token以版本CAS提交；网络超时不清除旧refresh token，invalid_grant转needs_login；存储失败不发布新generation。身份变更走主 Spec 的awaiting_confirmation，不自动将新账号接到旧Key。身份API不可用时不把未验证JWT里的email/sub当可信身份。

### 2.2 逐provider授权卡

| Provider | 选定授权和身份合同 | 刷新与端点隔离 | 当前准入状态和解除条件 |
|---|---|---|---|
| Codex订阅 | 保留现有Cove PKCE/state/nonce流程；OIDC按配置issuer/JWKS验证签名、aud、exp、nonce；以已验证账号标识绑定来源 | token只发既定token origin；Bearer和账号header只发Codex origin；不读取日常auth.json | 现有代码采用官方Codex客户端标识，**不是Cove自有注册**。保留用户现有功能；正式独立接入声明仍需Cove允许使用该客户端身份的依据；本轮不修改此配置 |
| Claude订阅 | 不新增可用的“用Claude登录Cove”按钮。用户自己的原版Claude Code订阅登录与Cove来源授权是不同对象 | 不收集或中转Claude.ai登录token；API来源使用x-api-key，不假称订阅刷新 | 官方文档明确第三方应用不能提供Claude.ai登录或代用户路由订阅凭据。只有供应商针对Cove的明确准入方案才能解除；API Key来源保留，不能悄悄把订阅需求改成API已完成 |
| Gemini CLI订阅 | Cove自有Google Desktop OAuth client，authorization_code+PKCE，loopback `http://127.0.0.1:{port}/oauth2callback`；Google userinfo可信`id`作为subject，email只展示 | Google授权/token/userinfo分域；scope按固定源码为cloud-platform、userinfo.email、userinfo.profile；离线refresh；项目资格另走CodeAssist | Google Desktop OAuth的注册流程可定义；**自有client能否使用CodeAssist private API未获证明**。需注册metadata、已准入client+scope、project/eligibility成功样本，不能复制Gemini内置secret |
| Antigravity订阅 | 与Gemini CLI不同provider/account；不共享refresh或资格；未取得官方自有client/identity/项目合同前登录返回422 | 不把Gemini CLI的project和API授权推定为Antigravity授权；认证域未明确前不发credential | 尚缺针对Cove的client/redirect/scope/audience、可信subject接口、项目/模型/额度合同。竞品cloudcode transport只作调查线索；这是实质阻塞，保留D01/D02 |
| GitHub Copilot | 选择Cove自有GitHub OAuth App的device flow；启用device授权；POST github.com/login/device/code，轮询login/oauth/access_token；GET api.github.com/user的数值id作为稳定subject | Accept application/json；identity每次新授权重查；若返回expires_in/refresh_token则按返回值刷新，没有refresh则禁止捏造refresh请求。scope选择read:user与offline_access；不申请repo权限 | 官方Copilot SDK支持应用传入自有OAuth token且禁用读取已登录用户。**SDK是agent接口，不能直接等同透明模型网关**。独立注册可作为待配置项；原目标的短token换取、直接模型wire/额度合同仍未有足够官方证据，D02保留 |
| Qwen订阅 | 当前新授权不可用；旧源码device字段见下一节，仅作历史合同，不用于默认启用新账号 | 不读取用户~/.qwen/oauth_creds.json；不得因源码仍含refresh函数就宣称服务仍开放 | 官方认证文档说明OAuth免费档已于2026-04-15停止且不再是/auth选项。需当前有效的Cove接入方式或供应商重新开放的证据；Coding Plan/API Key属于独立来源，不替代该未完成目标 |
| Grok Build订阅 | 官方CLI文档确认OAuth/device形式，但没有取得Cove独立client注册合同。设计要求从固定issuer的OIDC发现取得授权/token/JWKS并校验issuer/aud/nonce/sub；device流程遵循2.1 | discovery不能任意扩展credential目的域；订阅transport与xAI API Key分开；refresh只在该client确实支持且拿到refresh_token时调用 | 需自有client准入、所需scope/audience、推理origin/header和模型/额度响应样本。CLI可登录不证明Cove可注册；未满足前不启用 |

证据：[Claude凭据使用边界](https://code.claude.com/docs/en/legal-and-compliance)、[Google桌面OAuth](https://developers.google.com/identity/protocols/oauth2/native-app)、[GitHub device及刷新](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)、[Copilot SDK自有OAuth](https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate)、[Qwen现行认证](https://qwenlm.github.io/qwen-code-docs/en/users/configuration/auth/)、[Grok Build认证形式](https://docs.x.ai/build/enterprise)。这些是技术接入依据和产品启用决定，不将本次调查写成供应商对Cove的批准。

### 2.3 Qwen历史device wire的精确记录

固定源码 `packages/core/src/qwen/qwenOAuth2.ts`：device为 `https://chat.qwen.ai/api/v1/oauth2/device/code`；token为同origin的 `/api/v1/oauth2/token`。表单携带client_id、scope、code_challenge、code_challenge_method=S256；轮询携带client_id、device_code、grant_type=device_code完整URN和code_verifier。返回device_code/user_code/verification_uri/verification_uri_complete/expires_in；token包含access_token、可选refresh_token、token_type、expires_in、可选scope/endpoint/resource_url。refresh响应更新access_token及有效期，若新refresh_token存在再旋转。

Cove目标解析保留缺失与null，不能把不存在的expires_in写成永不过期。resource_url只接受供应商已经确认的HTTPS推理origin及路径；它不能授权token发到任意返回URL。没有可信身份接口或已验证ID token，状态不得成为identity_verified；不以access token hash进行跨来源账号合并。字段记录足够写解析fixture，但当前服务开放与身份成功路径仍阻塞。

## 3. 模型、额度和上下文

### 3.1 Codex固定源码合同

订阅推理base是 `https://chatgpt.com/backend-api/codex`。模型请求为该base的 `GET /models?client_version={实际声明的客户端版本}`；保留ETag/条件缓存，不能填造新版client版本以试探资格。解析顶层`models`，条目的slug→upstream_model_id，display_name→展示名，context_window/max_context_window→上下文元数据，input_modalities与supported_reasoning_levels→声明能力；supported_in_api不能被解释成当前订阅授权结果。缺失max_output_tokens不根据context_window推算。

额度读取独立base下 `GET https://chatgpt.com/backend-api/wham/usage`，不是在codex base后追加wham。固定源码还存在另一部署的`/api/codex/usage`路径；Cove只采用与当前ChatGPT来源对应的路径，不自动扫描另一host。Bearer和ChatGPT-Account-Id来自已验证账号。主窗口在rate_limit.primary_window/secondary_window，附加限制在additional_rate_limits；保留每个limit的名称/标识，不能只显示一个百分比覆盖所有模型限制。

window.used_percent映射已用百分比，剩余=100-used_percent仅在0..100有效值内计算；limit_window_seconds是秒，reset_at是epoch秒，reset_after_seconds是相对秒。缺窗口=null，不当作0使用率；非法数值标unknown并保留脱敏错误。credits与套餐窗口分开，字符串金额不能当token数；本机预算、套餐使用率、provider消费账单三者不混合。只读查询不消费reset credit、不触发充值。模型、usage查询401允许一次共享refresh后重查；这项安全读重试不授权生成请求重放。

依据：[Codex模型endpoint](https://github.com/openai/codex/blob/50d9c5deac4aa8cafc95db886d1435726ff0f77c/codex-rs/codex-api/src/endpoint/models.rs)、[模型字段](https://github.com/openai/codex/blob/50d9c5deac4aa8cafc95db886d1435726ff0f77c/codex-rs/protocol/src/openai_models.rs)、[usage路径](https://github.com/openai/codex/blob/50d9c5deac4aa8cafc95db886d1435726ff0f77c/codex-rs/backend-client/src/client/rate_limit_resets.rs)、[窗口字段](https://github.com/openai/codex/blob/50d9c5deac4aa8cafc95db886d1435726ff0f77c/codex-rs/codex-backend-openapi-models/src/models/rate_limit_window_snapshot.rs)。以上为源码合同，真实账号字段及权限仍需E04。

### 3.2 Gemini CLI项目与额度

固定源码CodeAssist origin为 `https://cloudcode-pa.googleapis.com`，版本`v1internal`；POST `:loadCodeAssist`、`:onboardUser`、`:retrieveUserQuota`由adapter明确选方法，不能把所有冒号方法任意开放成通用代理。推理为对应`:generateContent`或`:streamGenerateContent?alt=sse`，请求包裹project、model和原生request；不等同Gemini API的`/models/{model}:...`。

loadCodeAssist输入可选cloudaicompanionProject及metadata；输出currentTier、allowedTiers、ineligibleTiers、cloudaicompanionProject分别解析。存在有效currentTier+project后才进入project_ready；存在validation URL时显示需用户处理，不能自动访问或同意条款。选择允许tier后才能onboardUser；free-tier请求不随意传外部project。异步响应name/done/response.cloudaicompanionProject.id逐步处理；按operation期限停止，取消后不得把迟到结果绑定到新账号。

quota输入`{"project":"synthetic-project"}`；响应buckets每项可含remainingAmount（十进制字符串）、remainingFraction、resetTime（时间戳字符串）、tokenType、modelId。remainingFraction=0.25→剩余25%；字段缺失=unknown；不能将fraction当用量百分比。modelId/tokenType共同区分bucket；resetTime解析失败只使该窗口时间unknown。未经provider定义的remainingAmount单位保持原始dimension，不猜请求数/token数。

模型列表和上下文不能由quota的modelId集合推定完整目录；只有官方目录响应或固定版本官方声明可以提供候选模型，后续权限探测单列。当前自有client的CodeAssist资格和完整动态模型目录仍是D01/D02。依据：[server方法](https://github.com/google-gemini/gemini-cli/blob/d75234cae935d58f896f4dbf305e0c51602fa385/packages/core/src/code_assist/server.ts)、[wire类型](https://github.com/google-gemini/gemini-cli/blob/d75234cae935d58f896f4dbf305e0c51602fa385/packages/core/src/code_assist/types.ts)、[项目握手](https://github.com/google-gemini/gemini-cli/blob/d75234cae935d58f896f4dbf305e0c51602fa385/packages/core/src/code_assist/setup.ts)。

### 3.3 其他订阅的明确返回和恢复合同

| Provider | 模型发现/元数据 | 额度 | 恢复条件 |
|---|---|---|---|
| Claude订阅 | 准入阻塞，不借API models结果宣称订阅模型资格 | 不调用未授权的订阅usage；unknown，reason为准入未建立 | 取得允许的自有授权与版本化模型/usage响应再补fixture |
| Antigravity | 不复用Gemini CLI模型列表或上下文；未验证目录为unknown | 未确认的fetchAvailableModels/quota路径不作为可用API发布 | 自有client+project握手+独立目录/额度实际协议 |
| Copilot | 官方SDK模型接口可作调查方向；未把agent session映射为透明Chat/Responses | premium requests、tokens与额度倍数不可混用；无已确认direct接口则unsupported | 固定受支持direct wire或经用户接受的SDK架构修改；后者本轮不实施 |
| Qwen订阅 | OAuth入口停用；不据遗留resource_url宣布当前模型目录 | 不把历史免费次数作为当前余额 | 当前供应商授权方案及目录/额度协议 |
| Grok Build订阅 | 不将xAI API的GET models当订阅资格 | 不把SuperGrok、Build credits、API美元余额合并 | 订阅专用origin、model与quota的当前官方合同/录包 |

**unsupported是当前可执行行为，不是需求完成的替身。** 这些成功路径的外部缺项仍记录在设计台账。手填model可以保存配置，但ready需有效账号且能力验证；未知上下文时显示unknown，严格依赖上下文的选路不可用，不静默填入常见数字。

### 3.4 额度快照、提醒和恢复

沿用5分钟刷新和主 Spec退避。归一化记录provider/account generation、model/operation scope、dimension/unit、used/remaining/limit、window_start/reset_at、observed_at、confidence及source adapter version。原始usage正文只在用户明确诊断时短期保存；常规存储仅保留解析需要的数值和脱敏错误。

同一窗口跨10%剩余阈值只产生一次低额度提醒；归零产生耗尽提醒；reset_at改变或新有效观测恢复为正值时解除，不能用本机时钟推测上游必已重置。过期快照标stale，不能永久封禁账号；尚未拿到新观测时路由按已有unknown策略处理。401转需登录、403转资格不足、429冷却、5xx/超时保留旧快照并标stale，不重置成100%。提醒链接指向Cove账号详情，不含provider token。

## 4. 原生operation、Compact和WebSocket

### 4.1 Provider与operation绑定表

主 Spec 的媒体/资源/任务合同仍有效，下表指定出站路径。所有base来自来源设置或该adapter版本配置，不增加可执行脚本式路径模板。HTTP method和operation固定映射，只有配置过的来源能成为路由候选。

| 来源 | operation与原生路径 | 认证/资格与不支持分支 |
|---|---|---|
| OpenAI API/明确兼容 | responses、responses/compact；chat/completions；images/generations、images/edits；audio/transcriptions、audio/translations、audio/speech；embeddings；files、batches及ID子资源；WS responses/realtime | API Bearer；GET models仅目录；兼容服务逐operation验证，不继承整个OpenAI目录；OpenAI没有通用rerank端点 |
| Anthropic API | messages、messages/count_tokens、models | x-api-key、anthropic-version；原生签名/缓存保留；files/server tools必须有对应版本的beta和资源归属适配；不伪造Compact/Responses WS |
| Gemini API | v1beta/models；models/{model}:generateContent、:streamGenerateContent、:countTokens | x-goog-api-key；分页保留；context上限取模型字段；cache/file原生操作须使用对应资源适配，不能裸透传别的Key的ID |
| Azure OpenAI v1 | resource的/openai/v1/responses、chat/completions及已验证的媒体operation | model使用deployment；api-key或选定Entra凭据；不把Azure管理面list权限作为使用手填deployment的前置 |
| Bedrock | region runtime的/model/{modelId}/converse、converse-stream；目录来自控制面ListFoundationModels/对应inference profiles | AWS SDK SigV4；流是AWS eventstream，不是SSE；工具映射到toolUse/toolResult；跨region profile与模型权限分别验证 |
| Vertex Gemini | v1或已声明v1beta1的projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent及streamGenerateContent | Google SDK选定凭据；地区endpoint与global分别配置；不是Google个人订阅；非Gemini模型不继承此卡 |
| Ollama OpenAI入口 | /v1/models、chat/completions及目标版本明确支持的operation | auth_type=none仅限明确选择；Cove客户端Key仍必需；不使用管理API拉取/删除模型 |
| Codex订阅 | 独立的Responses transport、usage和model目录 | media/files/batches/realtime不能从OpenAI API卡继承；不支持operation派发前422 |

此表为Cove选定实现边界；云上逐模型和地区限制仍需要对应版本验证。上游返回404/不支持不能触发改protocol重试；401/403/429/5xx沿用主Spec错误合同。额度、目录等安全读与可能产生费用的生成/文件/批次写操作区分处理。

### 4.2 Compact合同与已发现的版本边界

公开OpenAI API按主 Spec30.2：POST /v1/responses/compact，保留完整output窗口，usage独立记录；不把摘要prompt称为native compact。合成样例只描述原生compaction对象外形，encrypted_content是不可解释的字符串，不拿fixture假数据发真实调用。

Codex 0.158.0已核对官方`rust-v0.158.0` tag，commit为`064c6b8c737f5b41d171fdda80bd9ef10ad06eb3`。该版remote compaction v2走**普通Responses流**：原生input在末尾添加`{"type":"compaction_trigger"}`，保留instructions、当前model、tools和parallel_tool_calls；由客户端的ModelClientSession.stream选择SSE或WS transport，并不是调用公开API的`/responses/compact`路径。Cove不得删除未知的compaction_trigger或把它转换成普通摘要prompt。

响应以response.output_item.done交付type=compaction、encrypted_content不透明值，随后必须有response.completed；客户端收集器要求恰好一个compaction item，允许有其他输出item。0个或2个compaction、结束前断流均为失败；usage取completed实际返回。Cove原样交付全部事件，不替客户端重建压缩后的history，不把API compact的“完整output窗口”规则套成订阅客户端只保留一个item。该版客户端负责把保留历史与compaction item组装到下一轮input，Cove只维护其账号归属和opaque保真。

Cove operation识别：Responses请求包含该原生trigger时记operation=compact，但只有一次request/attempt及一次usage结算；不另外产生一个虚构HTTP compact调用。只允许Codex原生Responses路径使用此trigger；跨Chat/Messages转换遇到它返回422。客户端stream设置、store=false、账号header沿用该订阅卡。上游未派发前可以拒绝未验证能力；一旦派发，Cove不复制官方客户端内部重试，更不因缺item重新生成。`POST /v1/responses/compact`绑定到此订阅卡时返回422并说明该固定客户端采用streamed compaction；公开API原生compact端点照常按单独卡支持。这是版本化协议差异，保留R096目标，不宣称两种compact wire相同。

验收输入：一轮真实工具历史+compaction_trigger；服务端输出一个compaction item、completed及usage；下一轮由同版CLI提交保留历史+该item+新用户消息。成功断言为CLI续接完成、工具历史不串、只有一个compact attempt。失败覆盖缺item、双item、completed前断流、错误账号复用、客户端取消。设计合同可据此实现，E09真实实验仍未执行。

固定依据：[trigger请求构造](https://github.com/openai/codex/blob/064c6b8c737f5b41d171fdda80bd9ef10ad06eb3/codex-rs/core/src/compact_remote_v2_attempt.rs)、[流与单item收集](https://github.com/openai/codex/blob/064c6b8c737f5b41d171fdda80bd9ef10ad06eb3/codex-rs/core/src/compact_remote_v2.rs)、[官方合成续接测试](https://github.com/openai/codex/blob/064c6b8c737f5b41d171fdda80bd9ef10ad06eb3/codex-rs/core/tests/suite/compact_remote.rs)。本次读取这些测试源码，没有执行其测试。
官方依据：[Compaction](https://developers.openai.com/api/docs/guides/compaction)。

### 4.3 两种Responses WS不能混写

公开API：`wss://api.openai.com/v1/responses`，Bearer API Key；response.create不含HTTP专用stream/background字段；stream_id支持lane、每lane FIFO，最多32个命名lane，连接最长60分钟。generate=false预热不是文本生成。主 Spec的每create准入/记账/取消继续适用；达到连接寿命只关闭并告知客户端，不重放未确认请求。[官方WS合同](https://developers.openai.com/api/docs/guides/websocket-mode)。

Codex订阅固定源码：以订阅base组成`wss://chatgpt.com/backend-api/codex/responses`；Bearer及已验证账号header；源码声明`OpenAI-Beta: responses_websockets=2026-02-06`，还处理`x-codex-turn-state`粘性状态、模型ETag和`codex.rate_limits`事件。turn-state只在同一账号/turn归属内复用，不能回显为公共会话授权。下游传入的账号header不能覆盖Cove的账号。

关键差异：该官方WS实现以guard串行占用response stream，不能从公开API文档推出订阅也有32 lane并行能力。因此初始订阅卡只声明一个生成lane，第二个并发create在派发前返回unsupported，不悄悄拆成另一账号/socket；待独立订阅lane证据通过再扩展。该限制是基于已读代码的保守适配决定，不声称服务端必然不支持。源码中的内部恢复/重试也不整体复制：Cove继续遵守“已派发生成不隐式重放”。

401发生在upgrade前可以共享刷新后重试握手一次；101之后任何已发create失败不重新发送。错误事件保留status/error形状；不能把非JSON二进制frame解成SSE。上游关闭但未见终态记interrupted/usage unknown；Key撤销阻止新create；连接失效后客户端只有完整历史或有效compact窗口才能自行重建下一轮。

依据：[订阅WS](https://github.com/openai/codex/blob/064c6b8c737f5b41d171fdda80bd9ef10ad06eb3/codex-rs/codex-api/src/endpoint/responses_websocket.rs)、[订阅header](https://github.com/openai/codex/blob/064c6b8c737f5b41d171fdda80bd9ef10ad06eb3/codex-rs/core/src/client.rs)。E09仍须记录实际upgrade和工具续接；源码支持不是实际授权通过。

### 4.4 服务端工具、缓存和资源的逐项失败合同

OpenAI web_search只在模型声明支持时原生透传；file_search需要vector_store/file资源归属；code_interpreter需要container及挂载file归属。Cove尚无该资源kind handler时拒绝有关资源引用，不能放开任意native ID。工具结果包含的citation/source/container引用原样保留，不偷偷抓取URL。工具费用缺价则partial，不算免费。

Anthropic web_search等工具按供应商版本化type保留；cache_control属于prompt语义而非Cove结果缓存；cache_creation_input_tokens/cache_read_input_tokens分别归一化，原始wire不改成OpenAI字段。Gemini googleSearch/codeExecution与函数工具不同，只有原生provider声明支持才透传；cachedContent是资源ID，需相同account/project归属。订阅provider不得从同厂商API卡继承server tool或缓存资格。

R097仍需要各订阅实际的tool type、资源路径、缓存字段和计价维度证据；现在定义的拒绝和原生保真规则不替代这些成功合同。E10需包含工具单独收费、资源越权、签名缓存历史、上游部分成功、无usage五类样例。

## 5. 客户端的具体配置与恢复

### 5.1 路径、变量和权限

以下`${COVE_ORIGIN}`、`${COVE_MODEL}`、`${PROJECT}`是**文档模板参数**，由Cove预览阶段渲染；不假设每个客户端会自动展开。origin取当前实际监听地址。`COVE_API_KEY`是启动进程环境变量，仅在该客户端已证实支持引用语法的位置引用；JSON里不能随便写`${COVE_API_KEY}`冒充secret引用。

配置优先级必须检查到所选scope，若managed/CLI/env会覆盖本次设置，preview返回blocked及实际覆盖来源，不写一个注定不生效的文件。探测仅看二进制版本、应用package、选定配置的非敏感字段，不扫描auth、浏览器或扩展secret。未安装返回not_installed；不能把“未安装”记录为配置验证通过。所有文件编辑保留未知字段与注释，无法无损编辑的YAML锚点/JSONC语法返回unsupported_format，不整体重排。

### 5.2 Codex与Claude Code

| 客户端 | 精确写入合同 | secret与恢复 | 验收 |
|---|---|---|---|
| Codex 0.158.0目标 | 用户选择的独立CODEX_HOME/config.toml；`model_provider="cove"`、model；`[model_providers.cove]`含name、base_url=`origin/v1`、wire_api=responses、env_key=COVE_API_KEY。已有文件时只添加选定provider/profile；不改auth.json | env_key为变量名；不往TOML写密钥；用独立进程环境或已有profile；恢复只删/还原本次provider/profile字段，保留用户新增内容 | TOML解析；实际进程读取配置、文本/工具/compact/WS各自结果；本次只读取版本，未运行这些验收 |
| Claude Code 2.1.281目标 | 项目`.claude/settings.local.json`或用户`~/.claude/settings.json`的model和env.ANTHROPIC_BASE_URL=`origin`；不要附加/v1使客户端重复拼接 | Key通过启动进程ANTHROPIC_AUTH_TOKEN传入；预览识别与ANTHROPIC_API_KEY/CLAUDE_CODE_OAUTH_TOKEN冲突，当前启动环境显式只选一种；不删除用户的官方登录文件或改全局shell | 原生Messages与Codex转换来源分别测试；CountTokens/prompt cache未支持时明确能力，不静默吞字段；restore保留用户后来修改的model |

Codex独立目录是本次生成模板的输出位置，不读取日常账号数据。Claude管理Key与上游订阅token永远不同。全局MCP若存于同时含登录信息的客户端文件，优先官方命令或项目文件；不能为加一条MCP把整个登录配置读进Cove日志。

依据：[Codex配置](https://developers.openai.com/codex/config-reference)、[Claude配置scope](https://code.claude.com/docs/en/settings)、[Claude网关](https://code.claude.com/docs/en/llm-gateway)。

### 5.3 OpenCode、Gemini CLI、Continue

| 客户端 | 配置路径与精确字段 | 密钥、协议与恢复 |
|---|---|---|
| OpenCode 1.18.33快照 | 项目opencode.json/opencode.jsonc，或用户`~/.config/opencode/opencode.json`；provider.cove.npm=`@ai-sdk/openai-compatible`，name=Cove，options.baseURL=`origin/v1`，options.apiKey=`{env:COVE_API_KEY}`；models以public model id作key；model=`cove/{public-id}` | 该模板选Chat协议；Responses原生必须另用经验证的provider配置，不因npm名字相近推定等价。恢复provider.cove和model各字段；其他provider不动。配置文件同时存在时先按原生优先级报告生效位置 |
| Gemini CLI固定nightly | 项目`.gemini/settings.json`或用户`~/.gemini/settings.json`；security.auth.selectedType=gemini-api-key，model.name={public-id}；启动环境GOOGLE_GEMINI_BASE_URL=`origin`、GOOGLE_GENAI_API_VERSION=v1beta、GEMINI_API_KEY={Cove Key} | 必须是Gemini原生入站协议，不指向/v1/chat/completions；API Key auth才适用此base变量。恢复selectedType/model及本次进程环境，保留OAuth token。无需修改.env |
| Continue 1.3.40 | 用户`~/.continue/config.yaml`，schema:v1，models列表新增name=Cove、provider:openai、model、apiBase=`origin/v1`、apiKey为客户端支持的secret引用；roles按已验能力选择chat/edit/apply | secret引用使用`${{ secrets.COVE_API_KEY }}`语法；secret来源由用户在Continue官方流程配置。Continue IDE按官方文档从项目.env、项目.continue/.env、用户~/.continue/.env依次解析secret；CLI还可用进程环境。本轮不创建或修改这些文件；后续实施必须在预览中让用户明确选择并授权对应secret落点。secret未提供时apply配置可完成但连接状态为needs_secret。不将chat成功标作autocomplete/embed/rerank支持 |

JSON对象按key编辑，Continue models按唯一name定位；同名两项先报冲突，不能取第一项覆盖。没有Cove条目时新增，恢复只移除本次新增且未被用户修改的条目；不删除其他models。新增文件恢复时current内容未变才删；若有新增字段，按字段删除Cove部分并保留文件。

依据：[OpenCode配置与变量](https://opencode.ai/docs/config/)、[OpenCode provider实现](https://github.com/sst/opencode/blob/7945de208964a49300d7f770d1a71d078db9a4c4/packages/opencode/src/provider/provider.ts)、[Gemini base URL](https://geminicli.com/docs/reference/configuration/)、[Gemini请求构造](https://github.com/google-gemini/gemini-cli/blob/d75234cae935d58f896f4dbf305e0c51602fa385/packages/core/src/core/contentGenerator.ts)、[Continue秘密解析](https://docs.continue.dev/faqs#managing-local-secrets-and-environment-variables)、[Continue模型schema](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/packages/config-yaml/src/schemas/models.ts)。

### 5.4 Cline、Roo和Cursor的界面配置合同

这三者的模型设置不能在没有官方写接口时伪装成“写settings.json即可”。**文件自动写入与官方界面配置分别显示**；自动写目标仍保留D03，下面的手动路径不关闭该目标。

Cline 4.1.21：在设置选择OpenAI Compatible，输入Base URL=`origin/v1`、Cove Key、public Model ID。配置需明确作用于Plan还是Act或两者。固定源码的非秘密字段包括openAiBaseUrl、planModeOpenAiModelId/actModeOpenAiModelId和两个模式的ApiProvider；openAiApiKey属于secret key集合。Cove不得直接写VS Code state.vscdb或模拟SecretStorage文件。若扩展没有稳定官方外部写接口，apply返回422 manual_action_required，并返回可复制的非秘密字段和用户自己完成输入的步骤；不报告applied。恢复由用户在同一模式选择原provider/model，清除这次Cove Key；Cove只保留原非秘密值，不导出原凭据。

Roo 3.53.0：建立独立名称Cove的API profile，选择OpenAI Compatible，Base URL=`origin/v1`、Key、model；按实测填写context/max output，不虚构。ProviderSettingsManager通过VS Code SecretStorage保存api_config profile集合，Cove不直接覆写。先记录原选中profile，恢复时切回原profile；若用户之后修改Cove profile则提示冲突，不替用户删profile。工具能力需native tool calling测试。外部自动apply同样保留D03，不能通过写一个无效JSON假闭环。

Cursor 3.20.21：本轮只证明已安装该版；旧API Keys文档URL现在重定向到文档首页，没有得到该版本稳定的BYOK文件写schema或Cove loopback请求路径证明。模型配置的自动apply返回422并显示“该版本模型配置自动写入尚未验证”；不能写猜测字段，也不能创建公网隧道绕开本机网关边界。MCP/Skills的公开文件入口可独立设计，不由此宣传模型/agent接入已支持。D03解除需要该版官方模型配置API/导入格式，或用户认可只保留手动接入的范围调整。

依据：[Cline配置入口](https://docs.cline.bot/provider-config/openai-compatible)、[Cline字段及secret集合](https://github.com/cline/cline/blob/647d8cb059f5083c53d959609ce04c82647ae0d6/apps/vscode/src/shared/storage/state-keys.ts)、[Roo profile存储](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/core/config/ProviderSettingsManager.ts)、[Roo接入](https://roocodeinc.github.io/Roo-Code/providers/openai-compatible/)。

### 5.5 MCP与Skills具体落点

| 客户端 | MCP文件与条目 | Skills文件与恢复单位 |
|---|---|---|
| Codex | 所选CODEX_HOME/config.toml或项目.codex/config.toml；mcp_servers.{name}；stdio command/args/env；HTTP url/bearer_token_env_var/env_http_headers | 项目.agents/skills/{name}/SKILL.md；用户~/.agents/skills。优先项目；不同时复制到两个目录造成重复 |
| Claude Code | 项目根.mcp.json的mcpServers.{name}；type=stdio或http；command/args/env或url/headers；用户scope使用官方claude mcp命令，原CLI负责自己的存储 | 项目.claude/skills/{name}/SKILL.md；用户~/.claude/skills；逐文件恢复，不运行脚本 |
| OpenCode | opencode.json[c]的mcp.{name}；local使用type:local及command数组、environment；remote使用type:remote及url/headers | 项目.opencode/skills/{name}/SKILL.md；用户~/.config/opencode/skills；按该版本能力启用 |
| Gemini CLI | .gemini/settings.json的mcpServers.{name}；stdio command/args/env；HTTP用httpUrl；不把url的SSE含义随意改成HTTP | 项目.gemini/skills/{name}/SKILL.md；用户~/.gemini/skills；不改Google认证文件 |
| Cline | 从其设置界面“Configure MCP Servers”选择实际cline_mcp_settings.json；内部数据目录随宿主/profile变化，必须采用用户所选实际路径；mcpServers.{name} | 项目.cline/skills/{name}/SKILL.md或用户~/.cline/skills；全局/项目同名要预览实际优先级 |
| Roo | 项目.roo/mcp.json的mcpServers.{name}；global使用扩展打开的实际settings文件路径，不能猜宿主目录 | 项目.roo/skills/{name}/SKILL.md；用户~/.roo/skills；不顺带更新.agents共享目录 |
| Continue | ~/.continue/config.yaml的mcpServers列表；唯一name；stdio command/args/env/cwd；HTTP type=streamable-http、url及requestOptions.headers | 项目.continue/skills/{name}/SKILL.md、用户~/.continue/skills；frontmatter需要非空name/description；加载器还扫描.claude，但Cove只写选定的.continue路径；readSkill按name查找，重复name必须在预览报冲突 |
| Cursor | 项目.cursor/mcp.json或用户~/.cursor/mcp.json；mcpServers.{name}；stdio command/args/env或HTTP url/headers；secret用`${env:NAME}` | 项目.cursor/skills/{name}/SKILL.md；用户~/.cursor/skills；不会自动打开云同步 |

每个MCP只写选定名字；同名不同定义报409并提供diff。stdio配置完成不代表程序已被启动或能工作；只有客户端在自己的授权边界内实际列出工具后才标activation_verified。HTTP OAuth session由客户端自己管理，Cove不拷贝其缓存。Cove只处理配置与文件，不成为MCP执行代理。

技能目录输入必须解析到用户授权的根内；文件清单包含相对路径、hash与大小。目标已存在时逐文件做三方比较；被用户修改的SKILL.md保留conflict，不能整目录删除。复制不执行脚本、不安装依赖、不改全局agent指令。Continue的固定加载器和readSkill调用已核对：[加载器](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/core/config/markdown/loadMarkdownSkills.ts)、[工具调用](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/core/tools/implementations/readSkill.ts)。其他客户端同一name在全局/项目已有副本时列出实际优先级；不能只校验文件写成功就说客户端会使用新版本。

依据：[Codex MCP](https://developers.openai.com/codex/mcp)、[Claude MCP](https://code.claude.com/docs/en/mcp)、[Claude Skills](https://code.claude.com/docs/en/skills)、[Cursor MCP](https://cursor.com/docs/mcp)、[Cursor Skills](https://cursor.com/docs/skills)、[Continue MCP schema](https://github.com/continuedev/continue/blob/5522c6f44ca0ac3528b37244818fbfa39b5af470/packages/config-yaml/src/schemas/mcp/index.ts)、[Cline Skills](https://github.com/cline/cline/blob/647d8cb059f5083c53d959609ce04c82647ae0d6/docs/customization/skills.mdx)、[Roo Skills](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/apps/docs/docs/features/skills.mdx)。

### 5.6 三方恢复的精确判定

对每个Cove写过的字段记录before_present/before_value、ours_present/ours_value，secret只保留私有引用；预览diff脱敏，操作记录不持有明文Key。恢复时只对当前选定文件的current_hash做一次CAS，不复制整份before文件覆盖用户新改动。

| before | ours | current | 恢复动作 |
|---|---|---|---|
| 未存在 | Cove新增值 | 仍为Cove值 | 删除该字段 |
| 原值A | Cove值B | 仍为B | 恢复A |
| 原值A | Cove值B | 用户改成C | conflict；默认保留C；用户显式选restore_before才还原A |
| 原值A | Cove删除 | 仍未存在 | 恢复A |
| 文件不存在 | Cove创建整个文件 | 原样未改 | 删除该新文件 |
| 文件不存在 | Cove创建文件 | 用户新增独立字段/文件 | 仅撤回仍等于ours的Cove字段；保留用户字段/文件 |

多文件应用前检查全部路径、权限与格式；写第2份失败则返回partial及每文件结果，不把第一份悄悄当成功结束，也不盲目全目录回滚。恢复中途崩溃按已有client_changes逐文件状态继续，不重复读原账号秘密。secret仅在本次创建且仍对应本次引用时可撤回；无法通过官方宿主接口读取比较的secret恢复标manual_action_required，不能声称自动恢复成功。

## 6. 逐项完成情况与未解除条件

原92项本地合同及范围项保持；原11项中，R096已补齐固定Codex版本streamed compaction与WS差异，R098已补齐8客户端的MCP/Skills文件、schema与恢复合同，二者设计状态改为适配合同已定义。其余9项的外部成功路径仍未全部具备，**不整体标成完成**。新的设计统计为94项合同已定义、9项外部适配未完全闭合、1项范围明确；不是实现通过率。

| 需求 | 本次补齐 | 仍缺的最小证据/决定 |
|---|---|---|
| R012 | 原生provider的出站路径、认证、云部署与协议错误边界 | 云provider全部高级operation/资源的固定版本wire；逐订阅资格 |
| R018 | 7类订阅逐一列授权、刷新、身份、注册及阻塞行为 | Cove注册与准入；Claude/Qwen当前受限入口；其他private资格 |
| R019 | Codex窗口和Gemini bucket字段、刷新错误及unknown | 其他订阅可用quota接口；自有client资格 |
| R028 | Codex上下文字段、缺失字段处理、不以quota推模型 | 各订阅权威max output/目录字段，未知不填默认 |
| R066 | 窗口单位、stale、提醒去重和恢复 | 与R019相同的外部数据源 |
| R074 | 8客户端版本证据、路径、检测、scope与secret分离 | Cursor模型配置schema；扩展官方可写入口 |
| R075 | 文件apply与官方UI配置分开、CAS和partial恢复样例 | Cline/Roo/Cursor自动配置成功合同，不能以manual替代自动目标 |
| R079 | 各客户端协议、字段、secret入口与工具验收 | Cursor本机网关路径与以上自动配置边界 |
| R096 | 0.158.0 streamed compaction trigger/item/终态；API compact与订阅WS差异 | 设计已定义；实际资格、opaque续接与WS能力属于E09，未执行 |
| R097 | server tools/缓存/资源归属与拒绝条件 | 各订阅具体tool/cache/资源wire、计价字段 |
| R098 | 8客户端MCP与Skills落点、逐文件恢复；Continue加载器和readSkill调用链已定位 | 设计已定义；实际客户端激活、MCP连接与恢复属于E07验收，未执行 |

D01解除需要供应商支持的Cove独立client metadata和身份资格证据；D02解除需要上述成功wire样例；D03解除需要官方可写入口/支持格式或用户明确接受的范围调整。新credential不在本轮索取或落盘，也不以真实付费调用试探接口。

下一位实现者可直接实现已定义的本地操作与已证实文件格式；必须在每个受阻adapter前保留上述状态和失败行为。不得把全文写完、fixture解析通过、来源URL有效解释成所有适配已就绪。
