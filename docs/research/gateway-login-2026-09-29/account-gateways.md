[返回总报告](../../gateway-login-research-2026-09-29.md)

# Cove 登录流程对照研究：账户型与密钥型网关（四项）

调查日期 2026-09-29。只读审阅官方站点和官方仓库默认分支，未安装、启动、创建密码、真实登录、模型调用或修改 gatt/Cove。以下“会看到/会跳转”均为源码控制流推断，不代表浏览器运行验收。下载源码、git clone 原始日志和带行号摘录均位于本目录。版本是调查时默认分支的固定 commit，不能自动代表当前稳定发行包；官网页面无 commit，可能与源码漂移。

## 版本与基本可比性

| 项目 | 固定 commit（提交日期） | 定位与个人可比性 | 初始入口 |
|---|---|---|---|
| New API | `789c970199ea527e6a26e071915f4a4cd2c64178`（2026-09-28） | 自托管多模型分发平台，有自用模式；可比但功能重 | 未初始化强制 `/setup`，创建管理员 |
| One API | `8df4a2670b98266bd287c698243fff327d9748cf`（2025-02-21） | 多用户 API 管理和 Key 分发；可比但不是个人专用 | 公开首页，默认管理员登录 |
| Sub2API | `a60a29549f488a854966aaec9541abbe006cac22`（2026-09-29；VERSION 0.2.10） | 订阅账号配额分发平台；有个人简易模式 | 无配置走向导；Compose 自动建管理员后为正常首页/登录 |
| GPT-Load | `bab12b287801245630748bb58fe7cc1b75a53980`（2026-09-29；2.0 分支文档） | 单实例自托管多凭据网关；个人可比性最高 | 登录密钥页面，管理密钥从部署文件获取 |

以上入口不是互斥的最佳实践排名。Cove 的登录选择应依据本机个人工具、远程个人服务器或对外多人服务的实际定位。本报告不添加新的 Cove 实现要求。

## 1. New API

**产品、部署与协议。** Go/Gin 后端、React 控制台，自托管；本地快速路径是 Docker + SQLite，Compose 默认另带 PostgreSQL 与 Redis，也支持 MySQL。官方源码文档列明 OpenAI Chat Completions、Responses、Anthropic Messages、Gemini 原生入口；上游渠道涵盖 API Key，源码还存在 Codex OAuth JSON 凭据校验与刷新。不能把 GitHub/OIDC 面板 OAuth 当成上游订阅授权能力。自用模式在当前开源源码里，初始化界面描述其会隐藏计价与计费选项，因此比纯 SaaS 后台更可比。证据：[协议和起步文档](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/README.md#L122-L176)、[技术栈与存储](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/README.md#L211-L241)、[自用模式](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/features/setup/components/usage-mode-step.tsx#L41-L67)、[Codex 上游凭据](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/controller/channel.go#L650-L675)。

**第一次打开裸 URL。** 根路由先查询 setup 状态；未完成会跳 `/setup`。向导顺序是数据库检查、管理员账号、使用模式、确认初始化。用户自己填写管理员用户名/密码与确认密码，后端哈希密码并创建 Root；没有在该首次路径发放一个要去日志找的默认密码。完成后前端返回 `/`，该路由是公开 Home，不等于自动登录；后台保护页恢复登录状态失败后跳 `/sign-in`。已初始化时裸 `/` 仍是首页。证据：[首次路由](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/routes/__root.tsx#L120-L149)、[向导步骤](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/features/setup/setup-wizard.tsx#L53-L77)、[管理员创建](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/controller/setup.go#L46-L158)、[后台登录门禁](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/routes/_authenticated/route.tsx#L25-L40)。完成回首页的精确代码还见本地 `web/src/features/setup/setup-wizard.tsx:106–115`。

**日常登录、重启、恢复。** 日常是用户名/邮箱与密码，按实例配置可有 Passkey、2FA、第三方身份登录。官网如此描述，但具体实例启用了哪些方法未实测。当前 main 已不使用旧 Gin session：15 分钟 JWT 只存内存，最长 30 天 Refresh Token 在 HttpOnly/SameSite Strict Cookie；数据库保存登录会话。持久数据库、相同 `SESSION_SECRET`、未过期且未撤销的刷新凭据是重启后恢复会话的条件。未设 secret 时源码初值为每进程 UUID，因此不能无条件声称默认重启保持登录。证据：[当前会话契约](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/docs/authentication.md#L1-L14)（[时效常量](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/service/auth_token.go#L18-L25)、[Cookie 实现](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/service/auth_session.go#L310-L326)）、[随机默认 secret](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/common/constants.go#L31-L37)、[环境变量覆盖](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/common/init.go#L50-L64)；[官方注册登录说明](https://docs.newapi.ai/zh/docs/guide/feature-guide/user/auth)。

**发现明确的文档/源码差异。** 官网说重置链接中“设置新密码”，但固定源码的 `/api/user/reset` 只收 email/token，服务端生成 12 位新密码并返回，前端展示并复制该密码。应以固定版本源码说明，不能写成已验证可自行输入新密码。邮件找回需要账户邮箱与邮件发送条件；没有邮箱且所有管理会话已丢失时，本次未证明有官方离线重置命令。证据：[重置后端](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/controller/misc.go#L275-L311)、[重置前端](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/features/auth/reset-password-confirm/index.tsx#L67-L86)。

**身份和导航。** 管理员/普通用户是面板身份；渠道中保存上游 Key/Codex OAuth 凭据；`API Keys`（`/keys`）生成下游调用凭据，另有面板 PAT，不可与客户端 Key 混称。导航真实入口包括 `Channels`（`/channels`）、`Models`（`/models/metadata`）、`Playground`、`API Keys`。渠道路由明确有上游模型获取、单渠道/全部渠道测试。起步文档要求添加渠道、设置可用模型与分组、测试，再创建同组下游 Key，必要时配置额度或订阅。模型发现不等于某模型付费调用成功，本研究没有触发测试。证据：[当前菜单](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/web/src/hooks/use-sidebar-data.ts#L56-L145)、[发现和测试接口](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/router/channel-router.go#L39-L69)、[第一条请求流程](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/README.md#L122-L176)。PAT 边界见同 commit `docs/authentication.md:144–148`。

**开源/企业边界。** 仓库标 AGPLv3 并附署名要求，README 提供不接受 AGPL 义务时的商业授权联系途径。这是许可边界证据，不足以断言某登录/模型功能必须买企业版。此次涉及的向导、自用、登录、模型与 Key 都在公开源码中。证据：[官方许可说明](https://github.com/QuantumNous/new-api/blob/789c970199ea527e6a26e071915f4a4cd2c64178/README.md#L309-L323)。

**对 Cove 的判断（设计推断）。** 可借鉴首次明确创建管理身份、设置完成与日常登录分离、重新打开自动恢复会话、把渠道与客户端 Key 清楚分开。不能因此要求 Cove 同时引入用户充值、计费分组、复杂 OAuth/2FA 或数据库会话治理；这些来自分发平台的范围。尤其不能依据旧版教程给 Cove 放通用默认口令。

## 2. One API

**产品、部署与协议。** 官方定位是大模型 API 管理与二次分发，单 Go 可执行文件/Docker，内嵌 React 前端，SQLite 可起步并支持外部数据库。当前 relay 路由能直接证明 OpenAI Chat Completions、Completions、Embeddings、Audio 等；不少路径明确 `RelayNotImplemented`，不能算完整 OpenAI API 支持。该固定版未证明 Anthropic Messages/Gemini 原生下游或 Responses 入口，也未证明 Claude/Codex 订阅 OAuth 上游接入。GitHub、OIDC、Lark 等 OAuth 出现在用户认证路由，是面板身份登录。证据：[实际 relay 路由](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/router/relay.go#L10-L46)、[面板 OAuth 路由](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/router/api.go#L23-L31)；[官方 README](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/README.md)。

**首次打开与凭据获取。** 默认前端 `/` 是公开 Home，渠道和令牌页面有保护门禁。空用户库启动时自动创建用户名 `root`、文档公开的示例默认密码 `123456`。这是公开默认值而非本次取得的真实凭据。默认前端检测这组默认用户名密码登录，会跳到 `/user/edit` 提醒改密码；不构成后端强制改密门禁的证明。这里没有“第一次网页创建管理员”的流程。证据：[空库建管理员](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/model/main.go#L24-L56)、[根页面与保护页](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/App.js#L95-L134)、[默认口令后的改密提示](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/components/LoginForm.js#L74-L94)。

**日常登录、重启、恢复。** 登录验证用户名密码，写入签名 cookie session；前端 localStorage 另存用户显示数据，不能把它当后端授权。配置持久 `SESSION_SECRET` 后，官方 README 明确说重启 cookie 仍有效；未配置则每进程随机值，会失去之前签名的有效性。邮件恢复先发链接，凭 email/token 完成时服务端生成 12 位新密码返回。需要有效绑定邮箱和发信配置；遗失全部管理员访问且没邮箱的离线恢复路径未核验。证据：[登录与会话创建](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/controller/user.go#L21-L91)、[cookie store](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/main.go#L109-L114)、[官方重启条件](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/README.md#L356-L365)、[密码找回](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/controller/misc.go#L145-L230)。

**三类凭据与实际导航。** 面板管理员密码/会话，以及设置页的“生成系统访问令牌”，属于管理身份。上游 Key 填在“渠道”；下游 Key 在“令牌”创建。源码文案特别警告系统令牌不能用于请求 OpenAI 服务，值得保留这条概念分界。渠道编辑的模型列表调用网关 `/api/channel/models` 目录，同时支持模型选择/自定义；本次没有证明该按钮会现场查询每一个上游真实模型能力，不能叫实时自动发现。渠道表有测试模型选择、单渠道测试与批量测试。证据：[中文菜单名](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/locales/zh/translation.json#L1-L18)、[系统令牌文案](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/locales/zh/translation.json#L424-L453)、[模型目录获取](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/pages/Channel/EditChannel.js#L115-L129)、[渠道测试](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/web/default/src/components/ChannelsTable.js#L317-L346)。

**开源/企业边界。** LICENSE 是 MIT；README 又说明页面底部署名/项目链接以及不保留需获授权。这里仅记录两个官方文件的内容，不进行法律有效性判断。未找到足以证明独立付费企业版及功能门槛的证据，不能把赞助商服务算成 One API 商业版。证据：[LICENSE](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/LICENSE#L1-L13)、[README 许可说明](https://github.com/songquanpeng/one-api/blob/8df4a2670b98266bd287c698243fff327d9748cf/README.md#L474-L480)。

**对 Cove 的判断（设计推断）。** “渠道”和“令牌”分离、登录后直接到客户端令牌页有明确用途。通用默认 root/密码及靠前端提示改密不适合作为 Cove 的默认方案；它说明“用户会去哪里拿初始密码”必须回答，而不是说明初始密码一定要预置。

## 3. Sub2API

**产品、部署与协议。** 订阅账号配额分发平台，公开实现包含 OAuth/API Key 上游、多账号调度、计费、用户 API Key 和管理后台。Go/Gin/Ent + Vue，依赖 PostgreSQL、Redis，可用 Compose 或二进制。`RUN_MODE=simple` 是公开的个人/内部团队简易模式，隐藏 SaaS 功能并跳过计费，并非企业收费开关。数据面路由可证 OpenAI Responses、Chat Completions、Anthropic Messages、Gemini v1beta（适用平台与转换条件不能忽略）。证据：[定位和技术栈](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README_CN.md#L176-L208)、[简易模式](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README_CN.md#L733-L742)、[主要协议路由](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/server/routes/gateway.go#L190-L243)、[Gemini 路由](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/server/routes/gateway.go#L345-L355)。

**首次裸 URL 必须区分两条安装路径。** 无配置且无 install lock 时，需要 setup；未开启 AUTO_SETUP 的二进制进入专门 setup server，前端检测 needs_setup 跳 `/setup`。向导配置数据库、Redis、管理员邮箱密码等，安装会写配置与锁。官方 Compose 默认 `AUTO_SETUP=true`，在第一次服务启动时先从环境变量配置并建管理员，通常不会让用户看到网页安装向导。管理员邮箱默认 `admin@sub2api.local`；`ADMIN_PASSWORD` 未给时生成随机密码，仅在初始化时写一次部署日志；README 给出读取日志方法。这些环境变量是首次初始化输入，不能推断更改后会覆盖既有账号密码。证据：[启动分支](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/cmd/server/main.go#L69-L94)、[初始化判断](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/setup/setup.go#L163-L181)、[随机密码及建用户条件](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/setup/setup.go#L420-L459)、[初始化环境变量](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/setup/setup.go#L580-L609)。Compose 默认值见 [AUTO_SETUP 配置](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/deploy/docker-compose.yml#L38-L48)，文档见 [找初始密码](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README_CN.md#L441-L448)。

**完成后的裸 URL 与日常登录。** 正常模式 `/` 重定向公开 `/home`；进入 `/admin` 等保护页时无身份跳 `/login`，邮箱密码登录后管理员进入 `/admin/dashboard`。后台模式启用时另会把匿名公开页面导向登录。因此不能一句写“任何安装后根 URL 必定登录页”。前端把 access/refresh token 放 localStorage，启动恢复并校验/刷新；README 要求固定 JWT secret 以维持重启后的登录，TOTP encryption key 另管双因素秘密。是否在某部署重启后实际续会话，仍取决于持久化、过期与撤销状态，本次未运行。证据：[根路由](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/frontend/src/router/index.ts#L190-L213)、[登录门禁和返回路径](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/frontend/src/router/index.ts#L820-L882)、[刷新时恢复](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/frontend/src/stores/auth.ts#L105-L138)、[JWT/TOTP 重启配置](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README_CN.md#L369-L384)。

**密码恢复。** 登录页的“忘记密码”仅在重置开启且非后台模式时显示；后端要求开启重置和邮件服务，验证一次性 email/token 后设置新密码并使旧认证版本失效。默认 `.local` 邮箱不能据此假定能收重置邮件。没有实证的管理员离线重置 CLI 不写为存在；重新设置 `ADMIN_PASSWORD` 也不能写为重置方案。证据：[入口条件](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/frontend/src/views/auth/LoginView.vue#L69-L77)、[服务条件](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/auth_service.go#L1565-L1585)、[重置实现](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/service/auth_service.go#L1613-L1653)。

**凭据边界与模型/Key 操作。** 面板管理员是邮箱密码/JWT，管理自动化还有专门 Admin API Key；上游在“账号管理”中以 OAuth、API Key/导入凭据接入，绑定“分组”；下游在“API 密钥”创建。菜单“账号”是上游账户，不是管理员用户，也不是客户端 Key。管理员账户路由明确提供模型可用列表、上游模型同步预览/同步、账号测试、刷新凭据；新手引导顺序是建分组、加上游账号、创建客户端 API Key。证据：[中文引导和菜单](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/frontend/src/i18n/locales/zh/misc.ts#L193-L250)、[模型、测试和导入路由](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/backend/internal/server/routes/admin.go#L369-L415)、[管理自动化身份](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/skills/sub2api-admin/references/admin-cli.md#L1-L22)。实际模型可调性与账号刷新成功没有实测。

**开源/企业边界。** 当前 LICENSE 是 LGPLv3；README 另有“无商业授权”项目声明。只记录原文所在位置，不据此作商业使用合法性结论。未核验独立收费企业版；公开源码已有 SaaS 计费、支付、管理功能，不能把 README 中赞助商的“企业服务”当成本项目专有版本。证据：[LICENSE](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/LICENSE#L1-L11)、[项目声明](https://github.com/Wei-Shaw/sub2api/blob/a60a29549f488a854966aaec9541abbe006cac22/README_CN.md#L19-L30)。

**对 Cove 的判断（设计推断）。** 最有用的是按部署方式解释初始管理员凭据，并清楚显示管理邮箱和上游账号两种身份。不能把 PostgreSQL/Redis、计费、支付、多层分组和完整注册体系复制进个人工具。默认仅 `.local` 管理邮箱又依赖邮件恢复的组合，也提醒 Cove 需要确定真实可用的恢复路径。

## 4. GPT-Load

**产品、部署与协议。** 此次调查的是 2.0 默认分支，不是大量旧教程对应的 1.x。单 Go 程序内嵌 Vue 管理界面，默认 SQLite，支持其他数据库，自托管多凭据调度。公开文档列 OpenAI Chat/Responses、Anthropic Messages、Gemini 等协议；上游同时支持 API Key 和 Codex、Claude、Antigravity、Grok 订阅授权/凭据导入。其体量和不注册账号的入口更接近个人网关。证据：[产品/技术形态](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/README_CN.md#L59-L68)、[协议和渠道](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/README_CN.md#L132-L151)；[官方订阅接入文档](https://www.gpt-load.com/docs/groups/subscription)。

**首次裸 URL 与密钥来源。** 没有用户名密码注册向导，无凭据的保护路由到登录页。当前页写的是“登录 GPT-Load”“登录密钥”，并明确可以填管理员密钥或访问密钥，系统识别权限。部署可预先设置 `AUTH_KEY`；未设置则读取或安全生成 `${DATA_DIR}/auth.key`，后续继续读取同一文件。Compose 文档让用户在自己终端读取 `/app/data/auth.key`。登录帮助直接解释两种密钥来源。不是固定默认密码，也不是必须自己先生成账号。证据：[生成与读取实现](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/platform/authkey/authkey.go#L13-L31)、[路由保护](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/router.ts#L79-L103)、[页面文案与帮助](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/i18n/locales/zh-CN.ts#L67-L114)；[官方快速开始](https://www.gpt-load.com/docs/quickstart?lang=zh)。

**日常登录、重启与恢复。** 后端每请求校验 Bearer 密钥，前端调用 session 端点验证后记住凭据。当前 Modern 界面“记住登录”默认 false，成功时不勾选写 sessionStorage，勾选写 localStorage；刷新页面会恢复并重新验证，不是服务器签发用户 cookie session。服务重启只要 AUTH_KEY/持久 auth.key 不变且浏览器凭据仍在，应可继续认证，这是源码推断。重开浏览器与仅重启服务不同，sessionStorage 不应被承诺跨浏览器会话保持。丢失“登录密码”时其实是重新从部署 env 或 auth.key 取管理密钥，不走邮箱重置。源码可推断通过部署配置替换 AUTH_KEY 并重启会轮换管理密钥，但没有此次实测的独立重置向导/命令，不写成已验证操作方案。证据：[默认记住状态](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/features/auth/LoginView.vue#L23-L29)、[存储选择](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/features/auth/auth-session.ts#L116-L140)、[恢复逻辑](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/features/auth/auth-session.ts#L20-L52)、[请求认证](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/auth.go#L82-L107)。

**三类凭据与只读会话。** `AUTH_KEY` 是全权管理；分组里的 API Key/OAuth 是上游凭据；`AccessKey` 是给客户端的数据面访问密钥。AccessKey 还可登录同一个管理 UI，但后端只允许 GET allowlist（自己的首页、模型、用量、日志），不能修改配置。因此“下游 Key 也可登录”不等于“可当管理员”。这是本次四款里必须特别标出的例外。证据：[身份识别](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/auth.go#L82-L107)、[只读授权](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/auth.go#L199-L215)、[允许的管理路径](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/auth.go#L50-L58)、[只读 UI 说明](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/i18n/locales/zh-CN.ts#L67-L114)。

**模型发现、测试、导航。** 实际中文入口“分组”“模型”“访问密钥”“总览”。分组选择渠道并录入 Key 或 OAuth 凭据；分组模型页从上游获取列表后选择开放模型，也支持手动添加。全局“模型”是规格/价格管理，不应当作已接入成功清单。源码有单凭据测试和分组模型发现处理。生成客户端 AccessKey 时选择允许分组/协议，总览可生成接入参数。证据：[菜单名称](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/web/src/frontends/modern/i18n/locales/zh-CN.ts#L67-L114)、[模型发现实现](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/server.go#L867-L885)、[凭据测试实现](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/internal/control/server.go#L731-L752)；[官方模型管理](https://www.gpt-load.com/docs/models)。未触发收费调用；模型目录存在不能证明推理可用。

**开源/企业边界。** 当前公开仓库 MIT，无证据表明上述 OAuth、模型、登录或只读 Key 功能需要付费企业版。官网 FAQ 明确描述 2.0 为单实例，不能因此包装成集群企业网关。证据：[MIT LICENSE](https://github.com/tbphp/gpt-load/blob/bab12b287801245630748bb58fe7cc1b75a53980/LICENSE#L1-L13)；[官方 FAQ](https://www.gpt-load.com/docs/faq)。上游调用/订阅费用与网关软件许可分开。

**对 Cove 的判断（设计推断）。** 可借鉴登录页告诉用户凭据究竟在哪里、后台管理密钥与客户端 Key 的用语分开，以及持久密钥避免服务重启失忆。不能只复制“输一把长 Key”的外观而漏掉安装时交付和找回路径。Cove 是否需要 AccessKey 只读登录必须来自用户需求，不是为了模仿而新增角色系统。

## 汇总给 Cove 的有限结论

1. “裸 URL 打开什么”至少存在首次向导、公开首页、管理密钥登录三类路径，不能从网关品类推断统一标准。
2. 四款都需要明确定义管理身份与上游接入、下游 Key 的边界；GPT-Load 只读 Key 登录是额外受限能力。
3. 没有证据支持“个人网关必须有邮箱注册”或“必须用永久 API Key 登录”。个人模式仍可用本地管理员账号，密钥模式也必须有安装交付与恢复说明。
4. 重启后无需再输入凭据取决于密钥/数据库持久化和客户端会话保存。只在前端显示已登录状态不足以证明授权仍有效。
5. 模型目录/上游发现/渠道测试分别是不同阶段。调研中没有执行任何模型请求，不能给出可用性验收结论。

## 完成的检查与未知项

完成官方 GitHub/官网 web open/search，四仓库 shallow clone、固定 SHA、登录相关源码交叉检查、源文件行号范围校验。没有执行软件安装/启动、单元测试、E2E、真实认证、任何付费或免费模型请求。sources.json 是逐条证据索引，*-source-excerpts.txt 保存固定版本摘录。one-api/gpt-load/sub2api/new-api 子目录是原始源码；官方动态页面存档在 official-pages，抓取状态见 fetch-results.json。

未核验：发行包与所查 main 的一致性、某实际部署开启哪些 SSO/邮箱选项、浏览器跨重启保留策略、无邮箱锁定情况下三款账号型网关的离线管理员恢复命令、付费企业 SKU 的完整目录。没有把这些未知项写成已不存在。
