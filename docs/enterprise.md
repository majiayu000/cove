# 单服务器企业部署

用户选择的企业扩展在独立运行目录启用，个人桌面实例不会自动切换。设计、权限和验收边界见 [企业规格](spec/04-ENTERPRISE-SINGLE-SERVER.md)。

复制 `config.example.json` 为企业部署配置；保留请求限制，设置独立 `data_dir`，增加：

```json
"enterprise": {"public_origin": "https://cove.example.com"}
```

`listen` 仍为 loopback 固定地址。由服务器管理员配置 HTTPS 反向代理，转发完整 URL、保留原始 Host，并支持 SSE 与 WebSocket；公开 origin 必须与浏览器地址精确一致。不会信任客户端传入的转发头来绕过 Host、Origin 或登录校验。本机开发验收可使用精确的 `http://127.0.0.1:<配置端口>`。

先初始化管理员，然后启动：

```sh
./bin/gatt -config /path/to/enterprise-config.json enterprise-init admin
./bin/gatt -config /path/to/enterprise-config.json -background serve
```

初始化命令在终端隐藏读取密码，最少12字节。密码不放在命令参数、配置或日志中。已有个人来源/Key 的目录拒绝初始化；已有企业用户不会被覆盖。

打开公开地址登录，在目录中创建租户、创建用户，再按租户分配“租户管理员”或“查看者”。一个用户可以加入多个租户。平台管理员管理目录和所有租户；租户管理员使用原 Cove 控制台管理自己的来源、Key、模型、路由、预算和请求；查看者只有用量、预算和最近请求报表。停用用户、停用租户、移除成员或修改密码后，下一次请求重新检查资格。修改密码撤销已有企业会话。

各租户的数据面地址为 `https://cove.example.com/t/<tenant_id>/v1`，Gemini 为同一租户前缀下的 `v1beta`；生成示例和复制地址使用当前外部地址。SDK 使用该租户创建的服务 Key。用户密码/浏览器会话不用于模型调用。成员权限撤销约束管理访问，服务 Key 属于租户并独立管理；人员离开时需撤销或轮换其已获得的 Key。停用租户会同时阻止该租户的数据面调用。租户之间数据库、凭据、Key 摘要、历史、资源归属与预算账本分开，不能互用 Key 或资源。

在租户预算页创建“实例”预算，即约束该租户全部调用；Key/路由预算仍独立叠加。币种不合并，未知费用保持待核对，订阅费用不当作 API 账单。严格预算只适用于原有已证明的提供方/操作资格，不能靠启用企业模式扩大资格。

远程租户不能写服务器上的 IDE、MCP/Skills 或 shell 配置，不能执行主机备份恢复、更新、服务安装、诊断导出和遥测变更。本机 OAuth、AWS profile、Google ADC 不借给租户；Vertex 可使用本租户显式服务账号。其他 API 来源按既有能力合同处理。

企业目录审计记录操作者、操作和实体标识；租户管理审计增加 `actor_id`。均不记录密码或凭据值。会话在重启后失效，目录及各租户账本保留。服务器运维需对完整独立企业数据目录做停服备份，不能用个人模式单库恢复代替整个企业恢复。

用户选定的大阪独立服务已部署至 `https://cove.silencestar.com`，使用 Cloudflare 管理的 DNS、Cove 专用 Tunnel 和可信 HTTPS；管理员、租户数据和原有个人代理已保留。腾讯云的 Cove 服务已停用，回退数据保留。当前公网、内网与其他产品保留结果读取 `test-results/continuation-20261005/execution.json`；腾讯云报告保留其迁移前的验收范围。

经用户明确批准，租户复用既有 New API 网关的 Key，通过 Cove 专用 HTTPS 入口接入独立 `openai_compatible` 来源。目录发现 15 个网关模型标识，仅启用已验证的 `gpt-5.6-sol`。两次模型验证和四次公网生成调用通过，覆盖文本 JSON、Chat SSE、实际函数执行及第二轮结果、Responses SSE 转换，用量记录完整；临时服务 Key 已撤销。原官方 OpenAI 来源仍为停用草稿。该结果不证明官方 OpenAI 账号资格、全部高级接口或正式账单对账。

本地真实 HTTP、race 和浏览器结果读取 `test-results/completion-20261005-full/`，30分钟性能报告 `performance-final.json` 仅覆盖其中记录的构建。SSO、支付订阅和跨服务器云同步未纳入本次选择；供应商合同、未验收的实机、提供方和正式账单继续在实施台账记录。
