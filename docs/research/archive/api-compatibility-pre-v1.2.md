# 三协议接入合同

2026-09-29 用户新增三入口要求。本文件取代原 Spec 中 Responses-only 的产品范围限制；其余账号、凭据、路由与计量合同继续适用。这里声明的是当前已实现的语义子集，不代表完整 OpenAI / Anthropic API 或完整 Codex / Claude Code 兼容。

## 入口与来源

| 客户端入口 | 原生 Responses API 来源 | Codex 订阅来源 |
|---|---|---|
| `POST /v1/responses` | 原生 JSON / SSE | SSE，完整历史，`store=false` |
| `POST /v1/chat/completions` | 文本、function 工具、JSON / SSE | 同一文本与 function 子集；非流式请求内部聚合 SSE；拒绝输出上限、temperature、top_p |
| `POST /v1/messages` | 文本、tool_use / tool_result、JSON / SSE | 当前明确拒绝：Messages 必填 max_tokens，而订阅适配尚不支持对应输出上限 |
| `GET /v1/models` | 当前 Key 所绑定来源的配置模型列表 | 同左，不代表这些模型已完成真实调用验收 |

所有入口最终调用该 Key 固定来源的 Responses 上游。Messages 入口不新增 Anthropic 原生上游或 Claude 订阅。网关返回客户端工具调用，工具仍由客户端执行；没有服务端执行 shell、切换来源或重试模型调用。

三个 POST 入口共用鉴权、容量、来源版本/代次、凭据刷新、一次上游派发、请求与 attempt 原子记账。兼容转换器只解析/编码，不访问数据库、凭据或网络。来源能力检查、请求准备和流分类由同包的具体函数负责。

## 认证和错误

- Responses / Chat 使用 `Authorization: Bearer <Cove 客户端 Key>`。
- Messages 支持 `x-api-key: <Cove 客户端 Key>` 或 Bearer；同时填写必须一致。支持 `anthropic-version: 2023-06-01`，不支持 `anthropic-beta`。
- 上游凭据由网关单独获取，不把客户端 Key、Cookie 或 Anthropic 认证头传给上游。
- 不支持的请求语义在派发前返回 400；无效 Key 为 401，容量不足为 429，停用来源/关停为 503。上游 HTTP 错误保留状态和适用的 Retry-After，Messages 使用其 error 信封。
- 每次调用及前置拒绝获得 `X-Gateway-Request-Id`。前置拒绝不伪造模型 attempt。
- 转换失败在记录中使用 `protocol_conversion`；已经收到的上游结果和 usage 仍保留。流开始后无法更改 HTTP 状态，发协议错误事件并中断，不伪造 `[DONE]` 或 `message_stop`。

## Chat 子集

支持 model、messages、stream、tools、tool_choice、temperature、top_p、max_tokens / max_completion_tokens（二选一）、parallel_tool_calls、reasoning_effort、n=1、stream_options.include_usage。来源与具体模型可进一步拒绝参数。

消息支持 system / developer / user / assistant 文本，assistant function tool_calls，以及 tool 消息的 tool_call_id 与文本结果。文本可为字符串或 text 块；保留工具调用 ID、JSON 对象参数字符串和结果次序。工具定义支持 function.name / description / parameters / strict；未声明 strict 时保持 false。tool_choice 支持 auto / none / required 和指定 function。

返回 chat.completion 或 chat.completion.chunk，流式分片具有稳定工具 index；工具参数以增量字符串输出。终态将 stop、tool_calls、length、content_filter 映射为 finish_reason。签名 reasoning 历史、custom/namespace 工具应使用原生 Responses。

## Messages 子集

支持 model、messages、system、max_tokens（正整数必填）、stream、temperature、top_p、tools、tool_choice。system 接受字符串或 text 块；user / assistant 消息接受文本及 text / tool_use / tool_result 块。暂不支持以 assistant 预填充结束历史。

客户端自定义工具使用 name / description / input_schema / strict。tool_choice 支持 auto / none / any / 指定 tool，disable_parallel_tool_use 映射为上游并行约束。tool_result.is_error 转为包含 is_error 和原始文本的 JSON 字符串交给上游，保留失败含义，但不宣称原生 Anthropic 行为完全等价。

返回 message、text / tool_use 内容块。SSE 使用 message_start、content_block_start / delta / stop、message_delta、message_stop，支持文本和 input_json_delta。结束原因为 end_turn / tool_use / max_tokens / refusal。

## 明确的语义边界

- 未实现的字段明确报错，包括 stop / stop_sequences、response_format、logprobs、图像/音频、服务端托管工具、thinking/signature、cache_control、beta 功能。不会静默丢弃这些请求字段。
- 两个兼容入口使用完整消息历史及 `store=false`，不提供 previous_response_id。原生 Responses 保留其已支持字段、opaque reasoning 与 SSE 字节。
- 上游 reasoning 不伪造成另一个协议的 thinking；仅转换最终文本、拒绝和 function 调用。需要保留原生推理历史时使用 Responses。
- 转换受现有请求、单事件和响应大小限制约束；兼容 SSE 还限制累计转换后字节数。终态须与已输出内容及工具身份一致，超限/缺失/冲突终态不能宣告成功。
- Chat usage 使用 prompt/completion/total，已知缓存与推理计数放入对应 details。Messages input_tokens 扣除已知 cache_read，cache_creation 不伪造。
- 上游缺失 usage 时，Chat usage 为 null；部分计数和 Messages 未知计数为 null，包括无法提前知道用量的 message_start。这保留“未知”，可能不满足强制要求数字 usage 的严格客户端，尚未做完整 SDK 兼容验收。
- 取消仅结束本地传输，不保证服务方停止计算或收费。结果、交付、观察完整性分别记录，订阅不生成虚构逐次费用。

API Keys 页提供按来源、协议与模型生成的 curl 示例；订阅来源不显示尚不可用的 Messages 选项。

## 验证依据

`TestProtocolEntrySharesExecution`、`TestCompatSemanticBoundaries`、其余 `TestCompat*` 使用真实处理器/SQLite 和合成网络边界，覆盖身份与容量前置、来源策略、单次记录、取消、文本、工具回传、逐片输出、错误和用量。当前证据不包括新版真实订阅工具调用、真实客户端 SDK 或浏览器验收，具体构建和命令见 [实施记录](implementation.md)。

协议参考：[OpenAI Chat Completions](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)、[Responses 迁移说明](https://developers.openai.com/api/docs/guides/migrate-to-responses)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[Anthropic 流式事件](https://platform.claude.com/docs/en/build-with-claude/streaming)。
