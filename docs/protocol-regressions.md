# 产品边界与可复用协议回归

Cove 的个人入口是本机控制台、账号/来源、客户端配置和请求记录。Rekey 的入口是已授权的凭据操作与能力令牌；litellm-rs 的主产品是部署用 HTTP 网关。它们分别维护认证、用户对象、存储和错误合同。共享下面的故障时序与检查点，不要求调用共同运行时或把不同产品的错误码、预算语义统一。

Cove 已有的[单服务器企业模式](enterprise.md)保留独立配置、目录、租户与登录边界。这是现有明确选择的扩展，不使个人模式的本机自动管理会话成为远程认证。`TestLocalAutomaticSessionBoundaries` 与 `TestEnterpriseIsolationAndRoles` 分别检查这两个入口。

## 从已有用例复用

下面均来自现有原生测试。上游数据和凭据为合成夹具；部分用例使用实际 HTTP，部分直接注入 transport/流状态。它们不是实际提供方、真实账号、设备权限或外部维护者采用证据。Cove 基线为 `0d13264d96529770ab6c54be765c4587ef85e1a6`。

| 故障时序/输入 | Cove 的检查点 | 现有用例 |
| --- | --- | --- |
| 上游发送 `response.output_text.delta` 后 EOF，没有完整终态 | 请求记录为 failed；派发一次；转换到 Chat/Messages 时不补 `[DONE]` 或 `message_stop` | [app_test.go](../internal/app/app_test.go)：`TestDisconnectNotSuccessAndNoRetry`；[compat_test.go](../internal/app/compat_test.go)：`TestCompatErrorsNoRetryAndNoSuccessfulTerminator` |
| 文本 A、同名工具 lookup(q=a)、lookup(q=b)、文本 B/C 交错，工具结果随后返回 | 并行工具 ID 不同，文本/工具输出顺序和次数保持；usage 按本协议维度转换 | [gemini_conversion_test.go](../internal/app/gemini_conversion_test.go)：`TestSpecGeminiConversionStreamsInterleavingAndUsage`；[compat_test.go](../internal/app/compat_test.go)：`TestCompatFragmentedStreamsAndToolArguments`、`TestCompatToolResultRoundTrip` |
| POST 可能已经到达，读取响应失败；对照组是明确发送前拨号失败 | 未知提交只尝试一次；可证未发送才按路由上限进行有限重试；一个逻辑请求及一份预留，各次尝试单独保留 | [admission_acceptance_test.go](../internal/app/admission_acceptance_test.go)：`TestAcceptanceBoundedAttemptsNoUnknownReplay`（`unknown_submission` 与 `three_safe_attempts`）；[contract_test.go](../internal/app/contract_test.go)：`TestContractPostBodiesNotReplayable` |
| 流开始后客户端断开，或写入被背压阻塞时请求取消 | 上游 context 结束、记录 cancelled；禁用来源只禁止新准入，不主动取消已有请求 | [app_test.go](../internal/app/app_test.go)：`TestCancellationDisableRevokeAndDelete`；[contract_test.go](../internal/app/contract_test.go)：`TestContractCancellationWakesBlockedWriter` |
| 已预留请求尚在执行时预算降低；中断后费用不完整，再重启和人工对账 | 原请求仍能结算；新准入服从降低后的限额；未知费用保留 pending，不能用伪造零费用释放；对账保留原观察与审计，重复对账返回 409 | [accounting_test.go](../internal/app/accounting_test.go)：`TestSpecAccountingBudgetUpdateDoesNotBreakInFlightSettlement`、`TestSpecAccountingUnknownAndManualReconciliation`、`TestSpecAccountingRestartPreservesUnknownReservation` |

这些是可移植的场景，不是统一断言集。例如 Cove 保留待对账预留；Rekey 对缺失 usage 使用已校验的输出上限保守结算，不能用同一金额断言验收两者。

## 对照现有项目资产

Rekey 基线 `0828fca4ecb130f5b54a08643ea95f2e1f82ba3b` 的 [post_effect_audit.rs](https://github.com/majiayu000/rekey/blob/0828fca4ecb130f5b54a08643ea95f2e1f82ba3b/crates/rekey-broker/tests/post_effect_audit.rs) 中，`post_side_effect_timeout_is_indeterminate` 先让实际 TLS 上游收到完整 POST，再超时。公开响应保持 `UPSTREAM_FAILED`，审计为 `execution.indeterminate`，不能将内部审计状态冒充新的公开错误码。其 [gateway.rs](https://github.com/majiayu000/rekey/blob/0828fca4ecb130f5b54a08643ea95f2e1f82ba3b/crates/rekey-broker/tests/gateway.rs) 中的 `stream_transport_cut_aborts_http_and_uses_saved_max_once` 可对照流中断与保守结算。

litellm-rs 基线 `a0aada3521b72e4ea0ebbf9044b8f4dd359a5d6f` 的 [native_messages_routes.rs](https://github.com/majiayu000/litellm-rs/blob/a0aada3521b72e4ea0ebbf9044b8f4dd359a5d6f/tests/native_messages_routes.rs) 已包含 `malformed_native_message_streams_cannot_complete_successfully`、`native_message_stream_error_is_not_retried_or_duplicated` 和 `unknown_messages_usage_retains_budget_without_recording_an_actual_bill`。这里只读核对对应场景；该项目的执行、升级比较与发布由其维护流程记录，不由 Cove 的测试结果代替。

## 在 Cove 复跑

干净检出先构建嵌入前端，再使用已有测试入口。测试只使用隔离夹具，不需要提供方凭据。

```sh
make build
go test -race -count=1 -timeout 5m ./internal/app -run '^(TestLocalAutomaticSessionBoundaries|TestEnterpriseIsolationAndRoles|TestDisconnectNotSuccessAndNoRetry|TestCompatErrorsNoRetryAndNoSuccessfulTerminator|TestCompatFragmentedStreamsAndToolArguments|TestCompatToolResultRoundTrip|TestSpecGeminiConversionStreamsInterleavingAndUsage|TestAcceptanceBoundedAttemptsNoUnknownReplay|TestContractPostBodiesNotReplayable|TestCancellationDisableRevokeAndDelete|TestContractCancellationWakesBlockedWriter|TestSpecAccountingBudgetUpdateDoesNotBreakInFlightSettlement|TestSpecAccountingUnknownAndManualReconciliation|TestSpecAccountingRestartPreservesUnknownReservation)$'
```

完整仓库检查继续使用 `make test` 和 `make check`。没有重新运行的测试、真实提供方和其他平台安装不标为通过；失败保留原始退出状态与日志。
