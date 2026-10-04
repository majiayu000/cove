# 用户服务、恢复与本地开发包

当前实现使用最新 v1.2 合同。开发包含前端、二进制、配置示例、Finder 启动入口、源快照和构建证据，运行不需要 Go 或 Node。构建时 Go、Node、npm、目标架构与文件 hash 均记录在 `bin/build-evidence.json`。用户已选择 MIT，LICENSE 已纳入开发包。正式发行签名尚未提供，当前包用于本地开发验收。Linux、Windows 的代码与模拟测试不能替代各平台原生安装验收。

全局参数放在命令前，路径使用绝对路径。已有运行实例的真实配置、数据目录与 build 必须先核对：

```sh
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' status
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' doctor
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' service install
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' service start
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' service stop
gatt -config /absolute/config.json -data-dir '/absolute/Cove data' service uninstall
```

macOS 注册当前用户 LaunchAgent；Linux 写 `systemd --user` unit；Windows 注册当前用户登录计划任务并使用 LeastPrivilege。安装与启动是独立操作。不同注册会返回冲突；卸载保留数据。程序用固定参数数组启动，不通过 shell 解释用户路径。停止通过私有运行记录的控制令牌排空任务，并等待数据锁释放。无法核验的旧实例需要从原启动窗口正常退出。

`status` 和 `doctor` 显示管理器、真实监听 PID、可执行路径、配置和数据目录、build 以及 readiness。Unix 通过 `lsof` 检查实际 binary 和端口；缺少工具时报告无法核验。私有 `runtime.json` 中的控制令牌不会进入诊断、元数据导出或日志。`/readyz` 在关闭准入、存储故障和备份期间返回 503；受控启动器通过私有状态入口核验已初始化且准入关闭的新实例。

## 恢复切换

控制台先上传并预览备份，核对 schema、数量和凭据状态，再将备份准备到全新目录。元数据恢复保留财务历史，但撤销旧 Key 并要求重新配置凭据；完整备份必须 age 口令加密。恢复目录准备完成后，用当前实例配置初始化私有指针：

```sh
gatt -config /absolute/current.json -data-dir /absolute/current-data restore init --pointer /absolute/private/instance.json
# 使用 init 返回的 hash，确认目标配置指向已准备的数据目录。
gatt -config /absolute/current.json -data-dir /absolute/current-data restore prepare --pointer /absolute/private/instance.json --journal /absolute/private/restore.json --target-config /absolute/restored/config.json --target-data-dir /absolute/restored --expected-pointer-hash HASH
gatt -config /absolute/current.json -data-dir /absolute/current-data restore apply --journal /absolute/private/restore.json
```

启动器先排空并停止旧实例，再以关闭准入方式启动目标，核验 PID、build、数据目录、存储初始化与 readiness，最后激活。切换指针使用版本和 hash 比较。失败时保留 journal；无法确认关闭准入、进程身份或激活结果时拒绝自动回退，不猜测哪份数据可覆盖。macOS 的本地真实子进程切换已纳入测试；本次目标另执行了可清理的真实launchd注册、停启和卸载，证据见 implementation.md。

## 手动可信 hash 更新

先从可信渠道取得压缩包 SHA-256。现版本仅提供 manual-only 更新；没有编造正式签名密钥或在线发布源。

```sh
gatt -config /absolute/config.json -data-dir /absolute/Cove update prepare --package /absolute/cove-development-darwin-arm64.tar.gz --sha256 TRUSTED_HASH
# 使用 prepare 返回的私有 updater 与 journal 绝对路径。
/absolute/stage/gatt-updater -config /absolute/config.json -data-dir /absolute/Cove update apply --journal /absolute/stage/journal.json
```

prepare 检查完整包 hash、路径安全、文件闭包、平台、源快照、嵌入资产和 schema，不停止实例。apply 已接入一致 SQLite 与私有凭据快照、排空、旧实例停止、binary 替换、关闭准入启动、60 秒 readiness 门禁和激活。已有系统服务注册需要先卸载，避免管理器自动启动未受控实例。自动更新仅接受已初始化的 schema v2，当前实现不迁移旧数据。

只有新实例被确认仍关闭准入、尚未产生新的请求事实且 schema 相同，失败流程才恢复旧 binary 与一致数据快照并重新核验。激活不确定或 schema 改变时保留恢复证据并拒绝自动覆盖。`update rollback --journal FILE` 只适用于低层替换完成前的安全阶段，不能用来覆盖激活后的新账务。

## macOS 签名与公证候选包

从当前源码原生构建后，可以运行下面的发行准备工具。证书名称或 SHA-1 仅用于选择已安装的 Developer ID Application 身份；公证凭据留在现有钥匙串 profile 中。

```sh
make build
python3 scripts/package-signed-macos.py --identity 'Developer ID Application certificate name' --notary-profile 'Existing Keychain profile name'
```

工具复用开发包的源码与产物核验，只签名临时副本，更新副本中的二进制 hash 与构建证据，再创建并签名 DMG。只有 Apple 返回 Accepted、公证票据附加及校验成功、Gatekeeper 检查通过后，才输出 `bin/cove-signed-darwin-ARCH-SOURCE.dmg`、SHA-256 和 verification.json。失败保留公证结果报告；没有自动发布。将 DMG 内容复制到选定的本机目录后再运行 `Cove.command`，避免把服务注册到临时挂载卷。

命令依据 [Apple 自定义公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)。目前此工具只完成语法、参数与本地工具接口核对；实际签名和公证仍需可用的签名身份及 profile，不能把脚本存在算作正式发行已通过。Windows 签名证书及原生签名分发另行待验。

## 构建与验收

```sh
make build
make check
make test
bash scripts/package-platform.sh
```

打包脚本复核当前 source_id、binary target/build ID 与所有 artifact hashes。源文件在构建后改变会拒绝打包。各系统还需实际验证当前用户自动启动、无构建工具运行、中文与空格路径、端口冲突、关停屏障、重启和卸载保留数据、目录 ACL、浏览器失败恢复及 Windows binary 占用和断电 journal 恢复。具体完成状态见 `implementation-readiness.tsv`。
