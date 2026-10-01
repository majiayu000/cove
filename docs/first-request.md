# 从来源到首个请求

Cove · 栖港是个人本机 AI 网关；源码仓库是 [majiayu000/cove](https://github.com/majiayu000/cove)，当前命令名为 `gatt`。这里使用已有控制台完成接入，不假设有正式发行的安装包。先按 [README 构建与启动](../README.md#构建与启动) 运行开发版本。

## 1. 确认本机服务就绪

以下地址来自 [示例配置](../config.example.json)。如果改过监听地址，使用实际地址；不要把管理页暴露到公网。

```sh
COVE_BASE_URL=http://127.0.0.1:5569
curl --fail-with-body "$COVE_BASE_URL/healthz"
curl --fail-with-body "$COVE_BASE_URL/readyz"
```

`/healthz` 表示进程响应，`/readyz` 还检查当前是否可接收工作。503 可能发生在关闭准入、备份或存储故障期间，不能据此直接重建数据。用现有启动窗口和 [status/doctor 命令](platform-commands.md) 核对配置、数据目录、PID 和实际二进制。

## 2. 配置账号、来源和模型

在管理页创建或复用账号，再保存来源、认证材料和模型。模型能力按来源与模型能力卡开启；一个模型能返回文本，不代表它同时支持工具、媒体或 Realtime。先在控制台执行逐模型验证，再选定客户端需要的协议。

账号凭据是给上游提供方使用的。下一步生成的客户端 API Key 是 Cove 的准入凭据；它们不能互换。凭据通过控制台专用接口保存到私有文件，不要放进源码、截图或公开问题材料。

## 3. 创建客户端 Key 并选择入口

创建绑定来源或路由的客户端 API Key，确认权限和模型范围，再按客户端接入向导配置地址与 Key。常见入口包括 Responses、Chat Completions、Messages 和 Gemini REST；路径、模型名及能力应与向导和模型卡一致。

对已启用 Chat Completions 的模型，可以用下面的请求形状。先通过自己的秘密管理方式设置 `COVE_CLIENT_API_KEY`，并把 `COVE_MODEL` 设为该 Key 可调用的模型名；这两个变量不是 Cove 的配置字段。

```sh
COVE_MODEL=your-enabled-model
curl --fail-with-body "$COVE_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $COVE_CLIENT_API_KEY" \
  -H 'Content-Type: application/json' \
  --data "{\"model\":\"$COVE_MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with hello.\"}],\"stream\":false}"
```

模型请求可能产生上游费用。本指南没有替你调用提供方；HTTP 成功也不能替代实际文本、工具执行或最终回复的验收。

## 4. 按失败环节排查

| 现象 | 先核对什么 |
|---|---|
| 管理页打不开 | 实际监听地址、端口占用及同一数据目录是否已有实例；不要删除运行锁来强行启动。 |
| 服务响应但拒绝新请求 | `/readyz`、备份/排空状态和存储健康；按平台命令检查真实运行实例。 |
| Key 或模型访问被拒绝 | 是否使用 Cove 客户端 Key、Key 是否有效，以及绑定来源/路由、模型与权限是否匹配。 |
| 来源测试通过，客户端失败 | 客户端所用协议与模型能力是否一致；文本测试通过不等于客户端完整流程通过。 |
| 费用未知或预算拒绝 | 调用记录的用量完整性、价格版本/币种、pending 费用和准入条件；未知费用不是免费。 |
| 客户端配置写入失败 | 先看路径预览与 hash 冲突；未知版本按手动步骤操作，不覆盖原配置。 |

## 发布状态与支持

正式签名、跨平台安装和真实账号验收的完成情况以 [实施记录](implementation.md) 与 [验收台账](implementation-readiness.tsv) 为准。服务、恢复和开发包说明见 [平台命令](platform-commands.md)，许可见 [MIT License](../LICENSE)。

提交 [GitHub Issue](https://github.com/majiayu000/cove/issues) 时提供源码提交、平台、请求协议、脱敏错误和最小步骤；不要贴 Key、提示词、响应正文或私有备份。
