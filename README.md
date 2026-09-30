# Cove · 栖港

你的模型，泊在一处。

Cove 是本机 AI 网关：一个 Go 进程、嵌入 React 控制台、SQLite 元数据和私有凭据文件。可管理账号、来源、模型、客户端 API Key、路由、预算、调用记录、客户端配置及备份恢复。

实现基线为 [完整 Spec v1.2](docs/spec/01-COVE-COMPLETE-SPEC.md)。代码与真实提供方验收分别记录，见 [实施验收记录](docs/implementation.md) 与 [104 项台账](docs/implementation-readiness.tsv)。当前为本地开发预览，外部注册合同、真实账号实验和跨平台安装尚不能标记全部完成。

## 构建与启动

构建需要 Go 1.26.2、Node 22.12+、npm、Python 3 和本平台 C 编译器；SQLite 使用 CGO。生成的二进制已嵌入前端，运行不需要 Go 或 Node。

```sh
git clone https://github.com/majiayu000/cove.git
cd cove
make build
./bin/gatt -config config.example.json -data-dir /absolute/private/Cove serve
```

示例监听 `127.0.0.1:5569`。先检查该端口空闲，也可复制配置修改 `listen` 和数据目录。启动拒绝端口冲突和数据目录第二实例。管理页在匹配的 loopback Host 与 Origin 下自动建立本机会话，无管理密码。请保持个人本机部署范围。

## 调用与配置

在来源页先创建或复用账号，再保存来源、模型和认证材料；账号凭据通过专用接口存入私有文件。随后创建绑定来源或路由的客户端 API Key，按接入向导配置客户端。调用入口包含 Responses、Chat Completions、Messages 与 Gemini REST；原生媒体、Files、后台任务、Compact、WebSocket 和 Realtime 按来源及模型能力卡分别开启。请求权限、并发、费用和未知状态共用本地执行与账务边界。

客户端工具页提供选定路径的预览、hash 冲突保护、原子修改和三方恢复。官方登录与 Cove 的 API Key 各有归属；未知版本或未解锁客户端只提供明确手动步骤。

## 数据与运维

凭据目录 `0700`、文件 `0600`，本地明文受当前用户权限保护。默认请求记录只保存元数据；提示词、响应、工具内容和凭据不进入诊断与导出。成本按不可变价格版本和币种记录；未知费用保留 pending。软预算是本地准入限制，严格上界不具资格时明确拒绝。

当前新数据库 schema v2。不自动迁移或回填旧数据库；使用独立数据目录，保留已有运行实例。运维页可生成无秘密元数据备份或含秘密的 age 加密一致备份。恢复先预览并准备新目录，再用本机受控启动器切换；具体命令及用户服务、更新包流程见 [平台命令](docs/platform-commands.md)。

```sh
make test
make check
make package
```

包为本机原生开发产物，构建证据记录源码、Go/Node/npm 版本、平台、嵌入前端和文件 SHA-256。项目使用 [MIT License](LICENSE)。当前产物为开发包；正式签名和跨平台发行验收另行记录。
