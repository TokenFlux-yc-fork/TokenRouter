# OpenAI Gateway Failure Remediation — 2026-07-31

## 范围与证据

本文件记录 TokenRouter 对 2026-07-31（东八区）生产调查的修复边界。调查窗口为 UTC `2026-07-30T16:00:00Z`–`2026-07-31T04:20:00Z`，报告和分类产物位于 `.deployment/log-analysis/20260730T160000Z-20260731T042000Z/`，并已通过 `SHA256SUMS` 校验。调查结果包含 25,941 条不同请求错误，其中 21,287 条为 HTTP 非 2xx，4,654 条为 HTTP 200 下的 error/attempt，后者全部 `resolved=false`；`resolved` 是人工 triage 字段，不表示请求已恢复。

七次 `/v1/messages` 的 `400 Upstream request failed` 发生在任何语义输出前。上游只返回 generic `error.type=upstream_error` 和 `message=Upstream request failed`。因此可以确认是上游 pre-stream 拒绝，不能从 TokenRouter 单方确认 context overflow、payload schema、账号路径或 provider 内部根因。请求体大小与事件相关，但不能作为 context overflow 证据；独立的 `Prompt is too long` 不与该类合并。

## Confirmed defect

- Messages Responses SSE scanner 的 read error 和 data-interval timeout 在语义输出前没有返回类型化 failover，语义输出后也没有写唯一 Anthropic `event:error`，可能造成错误的静默截断或错误重放。
- `response.cancelled` 与 `response.canceled` 曾未统一识别为终态。
- buffered Responses reader 对 bare `type:error` 以及非成功 incomplete 的处理不一致，终态失败可能退化为 missing terminal 或被成功 finalizer 掩盖。
- `response.incomplete` 只有 `max_output_tokens` 可映射为正常 `max_tokens`；`content_filter`、unknown/missing reason 不能合成成功 `message_stop`。
- opaque upstream 400 的切号条件虽窄，但原先未显式表达 scope、reason、next-account action 和 neutral health decision。

本批修复保持首输出 staging：`response.created`、`message_start`、SSE comment 和 keepalive ping 不算语义输出；语义输出后禁止透明 replay。stream read failure 的客户端取消以原始下游 request context 为准，不因上游独立 context error 错误切号。

## Plausible risk

- provider 隐藏确定性 payload 错误时，opaque 400 跨账号重试可能放大请求；因此 classifier 只接受严格白名单 JSON shape，并保留现有 account-switch budget，不引入跨账号指纹熔断。
- H2 fallback map 可能出现高基数或过期回收不足。
- proxy circuit 若只按 proxy 而不按 authority 隔离，A authority 的失败可能影响 B。
- 多实例进程内健康状态并不共享。

这些风险不被七个 generic 400 样本证明，本批不以推测替代证据。

## Correct-by-design

- 语义输出前允许 failover；语义输出后禁止透明 replay。
- keepalive、SSE 注释、`response.created`/`message_start` 不计为 semantic output。
- 已输出内容后只发送一个脱敏 Anthropic `event:error`，不追加 synthetic `message_stop`。
- `input[*].status` 必须由 provider 的精确响应驱动降级。
- HTTP/WS `previous_response_id` 的 continuation 边界不能通过任意重放绕过。

## Observability gap

- generic 400 没有 provider request ID、code、param 或可操作 detail，最底层 provider 根因不可从 TokenRouter 还原。
- 现有 attempt/request 记录仍需进一步区分 provider、account、request scope 以及 wire/upstream/semantic outcome。
- HTTP 200 + SSE error 是对客失败，不能标记为 recovered；只有失败 attempt 后最终完整成功才是 recovered。

## 责任矩阵

| 责任方 | 负责内容 |
| --- | --- |
| 调用方 | 合法 tool arguments、完整且一致的 tool history、单项 payload 大小、合法图片、合法 continuation ID |
| 配置/运维 | model/group/channel/account capability mapping、失效账号摘除、容量、并发、发布排空 |
| TokenRouter | 协议转换、流终态、错误保真、failover 安全边界、failure scope、健康治理、Ops/SLA |
| provider | upstream HTTP/stream failure、能力真实性、可操作的 400 code/param/request ID |

只返回 generic wrapper 时，provider 的最底层根因不可由 TokenRouter 单方推导；TokenRouter 不将其武断标记为 context overflow。

## 已实施的窄修复批次

1. opaque 400 classifier 要求 HTTP 400、合法 JSON、根对象仅含 `error`、error object 仅含白名单字段、固定 generic message、`type=upstream_error`、空或同名 code、空 param；额外 details/reason/嵌套字段拒绝匹配。
2. 命中 opaque 400 时设置稳定 reason `openai_opaque_upstream_bad_request`、`GatewayFailureScopeProvider`、`NextAccountRetry`、`RetryableOnSameAccount=false`，并设置 `SuppressAccountScheduleFailure=true`。这表示可在本请求切换账号，但不因一次 opaque 400 惩罚当前账号。
3. Messages 同步 scanner、keepalive scanner 和 timeout 统一进入 read-failure 分层；输出前返回 failover，输出后写唯一 SSE error 并标记 Ops，禁止 finalizer/replay。
4. buffered Messages/Chat 读取故障改为 failover-safe；取消以客户端 request context 为准；Responses 终态识别包含 `cancelled/canceled` 和 bare error；content-filter/未知 incomplete 不转为成功。
5. handler 优先通过 typed marker 判断错误是否已向客户端传达，同时保留旧字符串兼容判断。

## 验证与回滚

使用 fake upstream/httptest，不接生产数据、不记录请求正文、Authorization、客户端 IP、真实账户名或完整 URL/query。本批自动测试已覆盖严格 classifier 正反例、Messages/Chat 的 scanner read failure 与失败终态边界、opaque 400 在 Responses、Embeddings、Images API-key 和 Images OAuth 入口的一致 metadata/neutral-health 语义、handler 层 A opaque 400 → B 成功和全部候选耗尽，以及无重复成功终止事件。多账号测试确认每个候选仅尝试一次、opaque 400 不做 same-account retry、最终错误不暴露 upstream request ID；实现继续复用既有 account-switch budget。

当前已通过：

```text
go test -tags=unit ./...
go test -tags=integration ./...
go test -race -tags=unit ./internal/service -run 'Messages.*(Read|Keepalive|Failover|Cancel|Incomplete|Terminal)'
go vet ./internal/service ./internal/handler ./internal/pkg/apicompat
git diff --check
```

`golangci-lint run ./...` 未执行成功：当前环境没有安装 `golangci-lint`（exit 127），不是代码检查失败。

每个实施批次应独立灰度并可独立 revert。出现 post-semantic account switch、普通 client 400 被切号、retry 放大、重复 SSE error/message_stop、账号健康误伤或敏感字段泄漏时立即回滚对应批次。本次不部署、不修改 `.deployment/ACTIVE_RELEASE`，不修改 Codex。

## 非紧急 enhancement（独立 worktree/PR）

1. model/group 的 policy 403、静态 unsupported 404、暂时不可调度 503 跨入口一致性。
2. 仅在请求明确携带完整历史时做双向 tool call/output preflight；只拒绝，不删除、补空、重排或转换未知 arguments。
3. provider/item-specific 10 MiB 原始 JSON/UTF-8/decoded media 限制，超限返回 413，绝不截断。
4. Grok invalid image 的窄 classifier；request/item scope，不切号、不惩罚账号。
5. per-account/endpoint/credential generation/mapped model 的 `input_tokens` capability cache 与 singleflight。
6. bounded H2 fallback eviction、proxy/egress + authority circuit key，以及多实例健康状态观测。
