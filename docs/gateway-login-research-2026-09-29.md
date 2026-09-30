# Cove 管理登录与网关使用流程调研

调研日期：2026-09-29。范围：11 个产品，3 条独立研究线程。本文是产品与源码研究，不是这 11 个产品的安装、登录或真实模型调用验收。

**结论：Cove 当前登录入口需要修改。** Spec 把本机启动器设成唯一入口，导致用户直接打开网页却无法登录。建议采用单管理员的网页密码设置与登录，将本机操作留给首次归属确认和密码恢复；保留来源授权、客户端 Key 的独立边界。该建议来自产品适配判断，竞品没有统一采用同一种登录技术。

## 1. 要回答的问题

用户要做的是个人 AI 网关：连接自己已有的 API 或订阅账号，找到可用模型，创建供客户端使用的 API Key，并能查看请求、用量和恢复异常接入。管理界面应当让这条流程可以完成。

本轮重点核对五件事：第一次打开如何进入管理界面；以后从浏览器直接访问如何登录；忘记密码或重启后怎么办；管理身份、上游授权、下游 API Key 是否分开；模型和 Key 在哪里配置、验证与使用。

研究采用官方文档和固定提交的源码。默认分支的实现可能先于发布版本；文档与源码不一致时分别注明。CLIProxyAPI 及其官方管理 UI 合计为一个产品。New API 与 One API 有项目继承关系，不能当作两个完全独立的设计样本。CC Switch 是桌面应用，用于比较个人工具的操作方式，不作为网页无须鉴权的依据。

## 2. Cove 目前的问题已经定位

这次主要问题在 Spec 的产品决策。Spec §13.1 明确要求：没有会话的普通网页只提示从本机启动器进入；启动器以本地管理员秘密申请一次性票据；票据换成最长 12 小时、重启失效的管理会话。它没有提供普通网页密码登录。见 [原 Spec 第 497 行](</Users/lifcc/Downloads/personal-ai-gateway-plan-2026-09-29/03-product-and-technical-spec.md:497>)。

实现基本遵循了这个要求。后端 [loginAdmin](/Users/lifcc/Desktop/code/AI/tools/gatt/internal/app/server.go:261) 只接收 `ticket`；[未登录页面](/Users/lifcc/Desktop/code/AI/tools/gatt/web/src/main.tsx:238) 只有介绍文字和命令；[本机打开逻辑](/Users/lifcc/Desktop/code/AI/tools/gatt/cmd/gatt/main.go:170) 则依赖系统打开浏览器。因此只改标题或增加一个装饰性的“登录”按钮解决不了问题，需要一起修订管理鉴权合同和页面流程。

**设计上的不匹配**：当前交付物是浏览器管理的本机服务，但登录入口假设用户总是通过一个可靠的桌面启动器进入。服务重启、浏览器新会话或直接打开地址时，这个假设就会暴露，用户面对“登录页面”却没有可完成登录的操作。

管理会话过期与上游订阅退出是两件不同的事。管理界面要求重新登录，不能据此判断 Codex 订阅授权已经丢失，也不应要求用户重新授权上游账号。

本轮已撤回尚未部署的密码登录草稿，继续以当前票据方案为分析基线。没有修改运行服务、管理员凭据、来源授权或客户端 Key，也没有启动新一轮真实模型请求。

## 3. 身份和密钥应如何区分

| 对象 | 用户在完成什么 | 应在哪里出现 | 不应承担的职责 |
|---|---|---|---|
| 管理身份 | 进入 Cove、配置来源、查看用量 | 管理登录与设置 | 不交给下游客户端，不拿来登录 ChatGPT |
| 上游凭据 | 让 Cove 使用某个 API 或订阅账号 | 来源页面的 API Key / OAuth 授权 | 不用来登录 Cove，不作为 Cove 客户端 Key 展示 |
| 下游 API Key | 让现有工具调用 Cove | 明确的 API Keys 页面 | 不授予管理后台权限，不直接暴露上游秘密 |

“已登录管理后台”“来源授权有效”“文本调用通过”“工具调用通过”也必须分别显示；任何一个状态都不能替代另一个状态的验证。

## 4. 十一项产品对比

### 4.1 首次进入、日常登录与恢复

表内重启行为来自固定源码推导，均未做本轮运行验收；“可保留”都有持久配置、有效期和未撤销等前提。

| 产品与形态 | 第一次如何获得管理访问 | 日常及重启后的行为 | 恢复与证据 |
|---|---|---|---|
| **New API**，分发平台，有自用模式 | 未初始化跳 `/setup`，网页自设管理员；完成回首页 | 用户名/邮箱密码；当前 main 是内存短期 JWT + HttpOnly 刷新 Cookie + 数据库会话。持久 `SESSION_SECRET` 等条件满足可恢复 | 邮件找回；无邮箱离线恢复未核验。[初始化](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/controller/setup.go#L46-L158)、[会话](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/docs/authentication.md#L1-L14) |
| **One API**，多用户分发平台 | 空库创建公开默认管理员；网页登录后提示改密 | 用户名密码 + Cookie；固定 `SESSION_SECRET` 才能维持重启后的签名有效性 | 邮件找回；不照搬公开默认密码。[初始化](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/model/main.go#L24-L56)、[重启说明](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/README.md#L356-L365) |
| **Sub2API**，订阅分发平台，有简易模式 | 无配置走向导；默认 Compose 自动初始化，密码来自环境或首次日志 | 管理邮箱密码 + JWT；浏览器保存 access/refresh token，固定签名配置可恢复 | 邮件找回有配置条件；修改初始化环境变量不等于重置已有账号。[启动分支](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/cmd/server/main.go#L69-L94)、[建管理员](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/setup/setup.go#L420-L459) |
| **GPT-Load 2.0**，单程序网页网关 | 输入管理密钥，来自 `AUTH_KEY` 或自动生成的持久 `auth.key` | 每请求验证 Key；默认仅 sessionStorage，可选择记住；服务重启不轮换固定 Key | 登录页说明密钥位置；配置替换是推导的轮换路径。AccessKey 另可登录受限只读页。[密钥生成](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/platform/authkey/authkey.go#L13-L31)、[登录文案](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/i18n/locales/zh-CN.ts#L67-L114) |
| **CLIProxyAPI + 官方 UI**，Go 服务与网页 | 部署设置管理 secret，打开 `/management.html` 输入管理 Key | 每请求 Bearer；可记住 Key，固定 Key 不随重启失效 | 修改本机管理配置是推导的恢复路径，未找到专门找回向导。[配置](https://github.com/router-for-me/CLIProxyAPI/blob/a270e7b9e57aaecd8f82555f44c2108518ad2330/config.example.yaml#L59-L90)、[官方 UI 流程](https://github.com/router-for-me/Cli-Proxy-API-Management-Center/blob/4530da271ba2e89810d4dccebc57f3091afa590a/README.md#L20-L132) |
| **CC Switch**，Tauri 桌面工具 | 直接打开桌面应用，没有独立管理登录页 | 没有网页管理会话；重开读取本机配置 | 备份恢复管理的是配置，不是管理员密码。[启动路径](https://github.com/farion1231/cc-switch/blob/a1216b7e359466be98f3c783cc290e7040de26d4/src/main.tsx#L88-L130)、[存储恢复](https://github.com/farion1231/cc-switch/blob/a1216b7e359466be98f3c783cc290e7040de26d4/README_ZH.md#L470-L486) |
| **Antigravity Tools**，桌面与 Web 双形态 | 桌面直接进入；Web 输入管理密码，未设时回退共享 API Key | Web 在 sessionStorage 保留输入凭据、逐请求认证；同页重启服务可继续，关页通常重输 | 官方说明本机配置/环境覆盖；管理密码与 API Key 共用回退不建议照搬。[入口](https://github.com/lbjlaq/Antigravity-Manager/blob/0269f045f4e35f34b9ee3b3bcd6c0ff1e741378f/src/components/common/AdminAuthGuard.tsx#L11-L94)、[部署恢复](https://github.com/lbjlaq/Antigravity-Manager/blob/0269f045f4e35f34b9ee3b3bcd6c0ff1e741378f/README.md#L193-L261) |
| **9router**，个人自托管网页 | 默认要求密码，来自已设置值、初始化环境或默认值；当前代码拒绝远程使用公开默认密码登录 | 密码换 24h HttpOnly JWT Cookie，签名秘密持久化；满足条件重启可保持 | CLI 设置菜单可重置；清除 hash 后仍受初始化环境密码影响。[登录](https://github.com/decolua/9router/blob/f01fb909e37189008080632ddaf404f096345cde/src/app/api/auth/login/route.js#L32-L93)、[会话](https://github.com/decolua/9router/blob/f01fb909e37189008080632ddaf404f096345cde/src/lib/auth/dashboardSession.js#L9-L69) |
| **LiteLLM**，团队/服务端 Proxy | operator 预设 master key；`admin` 加 master key 登录，可另设 UI 密码 | UI 会话默认 24h；数据库和配置保留时可恢复，仍受浏览器和到期条件影响 | 本机配置可改 UI 密码；上游加密 salt 是另一种必须保存的秘密。[登录配置](https://github.com/BerriAI/litellm/blob/684a1edd44efa3a7c7f0395ccfa1bf9803017ea2/litellm/proxy/auth/login_utils.py#L121-L145)、[官方起步](https://docs.litellm.ai/docs/proxy/docker_quick_start) |
| **Bifrost**，Go 网关与网页 | 默认未启用管理密码；可在网页安全设置开启用户名密码 | 当前源码是 30 天数据库会话与 HttpOnly Cookie；持久数据库可恢复 | 首次创建管理员的 setup_token 只保护创建动作，不保护全部初始管理访问；本机配置恢复未实测。[默认访问](https://github.com/maximhq/bifrost/blob/bf515003df2acc8a836d5825d0a62a79ce3b2c82/transports/bifrost-http/handlers/middlewares.go#L1191-L1216)、[设置保护](https://github.com/maximhq/bifrost/blob/bf515003df2acc8a836d5825d0a62a79ce3b2c82/transports/bifrost-http/handlers/config.go#L897-L930) |
| **Portkey Gateway**，限定 OSS 本地 Console | 当前 main 的开发 Console 要求预配 `admin_token`，然后在网页输入 | 换取 12h HttpOnly Cookie，服务端内存会话；重启需重新输入 token | 本机配置取回/更换；不等于 Portkey 云账号找回。[入口](https://github.com/Portkey-AI/gateway/blob/669825cbe89ee51569918b8f78a9db486fd69dd4/src/start-server.ts#L24-L65)、[会话](https://github.com/Portkey-AI/gateway/blob/669825cbe89ee51569918b8f78a9db486fd69dd4/src/middlewares/adminAuth/index.ts#L4-L19) |

这些项目没有共同证明“所有网关必须有账号密码”，也没有证明“本机网页应默认免登录”。它们提供了几种明确的入口：自建管理员、输入预设管理 Key、可选管理认证、原生桌面访问。Cove 应选择符合自己交付形态的一种，并完成其日常登录和恢复流程。

### 4.2 模型、来源与客户端 Key 的实际入口

| 产品 | 上游与模型入口 | 下游 Key 入口及需要注意的差别 |
|---|---|---|
| New API | Channels、Models、渠道发现/测试、Playground | **API Keys**；需匹配渠道/模型/分组 |
| One API | **渠道**；模型目录、手动配置、渠道测试 | **令牌**；“系统访问令牌”另外用于管理 |
| Sub2API | **账号管理、分组**；上游模型同步、账号测试 | **API 密钥**；上游“账号”不指后台管理员 |
| GPT-Load 2.0 | **分组**内导入上游凭据与模型；全局“模型”另含规格/价格 | **访问密钥**；AccessKey 的只读界面不等于管理员 |
| CLIProxyAPI | **AI Providers、Auth Files、OAuth、System** | **Config Panel** 中的 proxy API keys；与 management key 分开 |
| CC Switch | **添加供应商 → 获取模型**，不支持时手填 | 本地代理配置接管与占位 Key，不是通用客户端 Key 发行体系 |
| Antigravity Tools | **账号**连接 OAuth；**API 反代**配置模型路由 | API 反代里的 **API 密钥**，另有 **用户 Token** |
| 9router | **Providers**；导入 `/models`、手填与单模型 Test | **Endpoint & Key → API Keys → Create Key** |
| LiteLLM | **Models + Endpoints → Add Model → Test Connect**；Playground | **Virtual Keys → Create New Key** |
| Bifrost | **Model Providers → Add Key**；可刷新模型列表 | **Virtual Keys → Add Virtual Key**；通用一键调用测试未核验 |
| Portkey OSS | 本地 Console 填 provider/key 发 Test Request，查看 Logs | 未核实本地 Key 发行页；云 Model Catalog/API Keys 不能算进本地 OSS |

本表的菜单、模型测试语义及逐项源码链接见 [账户/密钥型四项详查](research/gateway-login-2026-09-29/account-gateways.md)、[个人工具四项详查](research/gateway-login-2026-09-29/personal-gateways.md)、[服务端网关三项详查](research/gateway-login-2026-09-29/api-gateways.md)。一个有价值的差别：CC Switch 的“检测连通”不发送模型请求，9router 的模型 Test 会发送推理请求；二者都不能直接叫“工具调用验证通过”。

### 4.3 三协议与订阅接入的核查范围

这里的“有”仅指已找到官方文档或路由/转换实现。它不保证每个来源、模型、参数、流式事件和工具调用组合都受支持。

| 产品 | Chat Completions / Responses / Messages | 上游订阅授权证据 |
|---|---|---|
| New API | 三者有依据 | 有 Codex 上游凭据校验/刷新；不与面板 OAuth 混用 |
| One API | Chat 有；另外两者本次未证实 | 未证实 Claude/Codex 订阅授权，面板 OAuth 不计 |
| Sub2API | 三者有依据 | 有订阅 OAuth / 凭据导入 |
| GPT-Load 2.0 | 三者有依据 | 文档列 Codex、Claude 等订阅渠道 |
| CLIProxyAPI | 有官方 OpenAI/Claude/Codex 兼容说明；具体组合限制见项目文档 | 有独立 OAuth 页面及凭据存储 |
| CC Switch | 三者均有本地路由 | 有 ChatGPT 等认证中心；部分模式跟随客户端授权 |
| Antigravity Tools | 三者均有路由 | 有上游账号 OAuth/凭据导入 |
| 9router | 官方说明三者格式转换 | 有 Claude/Codex 等上游 OAuth |
| LiteLLM | 三者有依据 | 当前存在 ChatGPT device-code provider；不能用旧印象归为 API Key 专用 |
| Bifrost | 三者有依据 | 未查到 ChatGPT/Claude 个人订阅浏览器 OAuth；云身份、MCP OAuth 不计 |
| Portkey OSS | 三者有依据 | 未查到个人 ChatGPT/Claude 订阅登录；Vertex 服务账号 OAuth 不计 |

协议路由和授权依据分别附在上述三份详查的各项目段落。**本轮没有执行任何模型请求，不产生“订阅转换成功”“三协议全兼容”或“真实工具调用通过”的结论。**

## 5. 可以借鉴什么，哪些不能照搬

**最接近 Cove 当前交付形态的样本是 CLIProxyAPI、GPT-Load 和 9router。** 这是基于“个人/单实例、自托管服务、浏览器管理、可连接上游”的产品判断，不是市场排名。它们都回答了用户如何直接打开页面、凭据从哪里来、如何配置来源及客户端 Key；具体选择管理 Key 还是密码各不相同。

New API 的首次向导与自用模式可参考，但它和 One API、Sub2API 的用户、分组、账单等范围较大。Cove 不需要为解决一个管理登录入口复制完整分发平台。CC Switch 则适合参考添加供应商和客户端配置体验，不能以桌面无登录推导网页服务可无保护。

管理凭据不因进程重启轮换、以及服务会话重启仍有效，是两件事。Portkey 当前源码仍使用内存会话，但重启后用户可以在网页重新输入已有 token。Cove 本次最需要补齐的是这个可完成的重新登录入口；持久登录可以另行判断，不必先建立复杂会话存储。

不能原样照搬的做法包括：公开默认密码、管理密码与客户端 Key 自动共用、把长期管理秘密存入浏览器、把管理员明文日志当日常找回方式，以及默认放行全部管理操作。记录竞品做法不等于建议 Cove 采用。

### 5.1 资料中已经确认的版本差异

- **New API**：当前 main 使用新的 access/refresh 会话机制，不能沿用旧 Gin session 描述。官网找回密码文案与固定源码也不同。
- **GPT-Load**：当前是 2.0；旧 1.x 默认密钥教程不适用本次快照。
- **CLIProxyAPI**：当前 v8 配置把管理、下游 Key、上游 Key 放到不同字段；在线基础配置页面还保留旧字段。
- **Bifrost**：setup_token 文档注明从 `2.0.0-prerelease3` 起要求；网页仍写 localStorage，当前固定源码已经是数据库会话与 HttpOnly Cookie。
- **Portkey**：所查 main 已有 admin_token，但包版本与功能公告存在差异，不能声称安装某个 npm 稳定版一定具有该入口。
- **One API**：本次固定 main 提交日期为 2025-02-21；未把其他项目的新能力套给它。

以上差异及精确链接均保留在对应详查，建议后续参考实现时固定本报告提交，升级参考版本时重新检查。

### 5.2 许可证和商业边界

本轮只参考产品流程，不复制竞品代码。记录各仓库文件声明：New API 为 AGPLv3 并有商业授权说明；One API、GPT-Load、CLIProxyAPI、CC Switch、9router、Portkey 主仓库为 MIT；Sub2API 为 LGPLv3 并有额外项目声明；Antigravity Tools 是带非商业限制的 CC BY-NC-SA 4.0；Bifrost 主仓库为 Apache-2.0；LiteLLM 非 enterprise 范围为 MIT，enterprise 目录另行许可。完整声明与附加条款见各详查链接，不以此作法律适用判断。

LiteLLM/Bifrost 的企业能力和 Portkey 的云控制平面均与本文所述自托管范围分开；没有查明的托管套餐或收费边界保留未知。软件许可、托管收费和上游模型账单也不是一回事。

## 6. 对 Cove 的建议

以下是基于调研和当前产品形态的设计建议，尚未实现，也不是已通过的验收结论。

### 6.1 最小可用的网页管理登录

采用**单管理员、网页设置密码、网页密码登录**。不增加注册、邮箱验证、多租户、团队角色或 SSO。管理密码用于换取短期管理会话；下游继续使用独立 API Key，上游继续使用各来源自己的授权。

首次安装的页面应该直接出现“设置管理密码”和确认密码。设置成功进入来源页，展示添加 API 或连接订阅账号的入口。之后从普通地址打开，应出现可提交的密码表单；会话失效时回到该表单，并说明原因。启动器可作为便捷入口，但不再是唯一进入管理页面的方式。

现有 `sessionStorage`、同源 Bearer、精确 Origin 校验和管理会话撤销机制可以保留，当前需求没有要求为此重写整套会话系统。服务重启后要求再次输入管理密码是可接受的最小实现；页面必须能直接完成，来源授权和已创建的客户端 Key 不受管理退出影响。“关闭浏览器后仍记住我”不放进第一版。

### 6.2 已有实例首次设密码必须证明归属

本机已经有来源和密钥时，不能因为新版本尚未设置密码，就向未认证访问者开放创建管理员的入口。已有实例应在经过现有本机管理员验证的会话内设置新密码。全新实例的首次设置也应绑定本机启动产生的一次性设置凭证，并检查同源请求、限制重复消费。

这份一次性证明只用于首次初始化或显式本机恢复，不能再次变成每次网页登录的前置条件。管理页面应呈现设置表单，启动器只负责把首次归属证明带到表单。无需让用户复制长期管理员秘密，也不调用系统钥匙串来登录管理页面。

忘记密码时提供明确的本机重置指引；重置必须由拥有本实例数据目录权限的本机操作发起，撤销旧管理会话，保留来源及客户端 Key。如果保留旧票据入口，还应使未消费的旧票据、初始化/恢复证明，以及仍能签发票据的旧本机管理员凭据失效，避免重置后旧凭据仍能重新登录。具体命令与持久化格式需在下一步 Spec 修订中确定。本轮不声称已有 `recover-admin` 满足“忘记密码”的新合同，也不操作现有凭据。

### 6.3 登录后应完成的个人网关闭环

1. **来源**：添加 API 地址与凭据，或通过独立 OAuth 连接订阅；授权取消、过期、刷新失败各有准确状态。
2. **模型**：在来源中明确列出或填写模型 ID；发现结果不等于该账号可用，展示实际验证范围、时间及失败原因。
3. **API Keys**：创建客户端 Key，选择来源及允许模型，显示一次秘密，并提供可复制的 API Base URL。
4. **接入示例**：按客户端选择 Chat Completions、Responses 或 Messages，给出该组合实际支持的示例。不能只因为路由存在就声称该来源的三种协议全部可用。
5. **请求与用量**：用请求 ID 对应一次调用，区分已知 token 用量、未知金额和订阅原生额度，错误有可恢复操作。

现有 API Keys 导航已经是明确入口，下一轮应保留。模型可先在来源详情内展示，不必为本次登录修复另建模型市场或路由规则系统。Codex 的真实工具调用仍需按用户指定模型单独验收；本次竞品调研不能代替这项验收。

## 7. 下一步应修订的合同与验收

先修订 Spec §13.1–13.2 的入口、首次设置、日常登录、会话到期、退出和本机重置合同，再实现对应前后端。同步移除“裸地址只能提示启动器”的要求。原有 Host/Origin 边界、管理与调用权限分离、失败限速及结构化错误继续保留；登录失败不应自动清空来源或重新发起上游授权。

| 场景 | 必须能观察到的结果 |
|---|---|
| 全新安装，从启动入口首次打开 | 有密码设置表单；成功后进入来源；一次性设置证明重复使用失败 |
| 已有来源的实例升级登录方式 | 未鉴权请求不能抢先设置密码；已验证本机管理员可完成设置 |
| 两个首次设置请求同时提交 | 只有一个成功；密码写入与初始化凭证消费原子完成，失败不留下半初始化状态 |
| 普通浏览器直接打开地址 | 已设置密码时有可用登录表单；不要求运行命令 |
| 密码错误或请求失败 | 显示准确错误；不显示成功，不自动跳转，不丢失来源 |
| 连续错误登录 | 限速与恢复时间可理解，结构化错误合同保持一致 |
| 刷新当前页面 | 有效会话继续使用；失效会话回到登录表单 |
| 新浏览器、新会话、服务重启 | 可以用同一个管理密码登录；已有来源和客户端 Key 保留 |
| 退出管理界面 | 当前管理会话撤销；客户端调用与上游授权不因退出而注销 |
| 忘记密码 | 界面说明本机恢复步骤；重置后旧管理会话、票据及可签票的旧管理凭据失效，业务数据保留 |
| 订阅授权取消或过期 | 留在来源管理流程，准确提示原因，与管理登录状态分开 |
| 模型列表、文本与工具验证 | 各自显示证据，不能把列表存在或 OAuth 成功标成工具调用通过 |
| 创建 Key 并接入现有客户端 | 地址、Key、模型和协议对应同一个来源；能在请求列表追踪结果 |
| 浏览器存储不可用 | 明确显示故障，不静默退回长期秘密或绕过鉴权 |
| 跨站请求、伪造 Host、重用初始化票据 | 服务端拒绝；不依赖隐藏按钮保护入口 |

这是下一轮工作的最小验收集合，不替代原 Spec 的 38 项产品验收。研究结束不代表原产品已经完成。

## 8. 证据与完成边界

本轮验证范围是官方资料与固定源码的交叉核对、Cove 的 Spec/实现对应检查、报告链接与来源版本检查。没有逐个部署 11 个产品，也没有代用户登录竞品或调用真实订阅模型。因此默认浏览器行为、真实第三方 OAuth 接受情况、具体模型可用性和端到端协议转换仍须运行验收。

原始研究保存在 `/private/tmp/cove-gateway-login-research-20260929/`；三个线程分别拥有独立子目录。研究不读取用户上游 token、浏览器凭据或钥匙串。来源证据使用公开仓库链接，原始下载与查阅记录用于复核，不把竞品代码加入 Cove 实现。

### 固定版本清单

共 11 个产品、12 个仓库；CLIProxyAPI 服务和官方 UI 各固定一个提交，仍只算一个产品。

| 仓库 | 本次固定提交 |
|---|---|
| QuantumNous/new-api | `789c970199ea527e6a26e071915f4a4cd2c64178` |
| songquanpeng/one-api | `8df4a2670b98266bd287c698243fff327d9748cf` |
| Wei-Shaw/sub2api | `a60a29549f488a854966aaec9541abbe006cac22` |
| tbphp/gpt-load | `bab12b287801245630748bb58fe7cc1b75a53980` |
| router-for-me/CLIProxyAPI | `a270e7b9e57aaecd8f82555f44c2108518ad2330` |
| router-for-me/Cli-Proxy-API-Management-Center | `4530da271ba2e89810d4dccebc57f3091afa590a` |
| farion1231/cc-switch | `a1216b7e359466be98f3c783cc290e7040de26d4` |
| lbjlaq/Antigravity-Manager | `0269f045f4e35f34b9ee3b3bcd6c0ff1e741378f` |
| decolua/9router | `f01fb909e37189008080632ddaf404f096345cde` |
| BerriAI/litellm | `684a1edd44efa3a7c7f0395ccfa1bf9803017ea2` |
| maximhq/bifrost | `bf515003df2acc8a836d5825d0a62a79ce3b2c82` |
| Portkey-AI/gateway | `669825cbe89ee51569918b8f78a9db486fd69dd4` |

### 已完成的检查

- 三条研究线程全部回收，11 项均有首次进入、日常登录、恢复、凭据边界、模型和 Key、可借鉴/不可照搬的分析；不适用和未核验项明确标出。
- 四份交付文档共 152 个去重后的固定提交源码引用；核对本地快照提交、文件存在及行号范围全部通过。这个检查证明引用能定位，不代替对结论的内容审阅或运行验证。
- 主报告登录建议经过个人工具研究线程额外只读复核，补齐首次设置并发竞争，以及重置后旧管理凭据失效的验收要求。
- Cove 基线中的 Spec、后端、前端和启动器摘要与研究前一致；本轮文档写入没有修改应用源码或重启服务。基线构建 ID 为 `852eca9e29d5314b25715cd23beeeb14ac5eb42601aff7119ddff474f5837855`；新增研究文档未重新构建成运行产物。
- [引用与基线检查记录](/private/tmp/cove-gateway-login-research-20260929/report-verification.json)、[研究分工记录](/private/tmp/cove-gateway-login-research-20260929/dispatch.json)、[Threads 运行记录](/private/tmp/cove-gateway-login-research-20260929/threads-run-log.jsonl)。

### 详细证据附录

1. [New API、One API、Sub2API、GPT-Load](research/gateway-login-2026-09-29/account-gateways.md)
2. [CLIProxyAPI、CC Switch、Antigravity Tools、9router](research/gateway-login-2026-09-29/personal-gateways.md)
3. [LiteLLM、Bifrost、Portkey Gateway](research/gateway-login-2026-09-29/api-gateways.md)
