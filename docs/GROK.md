# Grok / xAI 使用说明

TokenRouter 支持 Grok OAuth 订阅账号和标准 xAI API Key 账号，并通过 OpenAI 兼容的 Responses、Chat Completions、Messages 和 WebSocket 入口转发请求。Grok 分组还支持图片生成/编辑、视频生成/编辑/扩展以及视频状态查询。

## 基本信息

- 平台名：`grok`
- 账号类型：OAuth 订阅账号、API Key 账号
- Responses 网关入口：`/v1/responses` 和 `/responses`
- API Key 账号默认上游地址：`https://api.x.ai/v1`

## 账号配置

管理员可在控制台选择 OAuth 或 API Key 创建账号。OAuth 账号可通过控制台创建或重新授权；创建 Grok 分组并绑定账号后，用户即可生成分组 API Key。

## 媒体请求格式

JSON 图片编辑和视频生成请求可在 `image`、`images`、`reference_images` 与 `mask` 对象中提供参考图片。与 xAI 直接兼容的请求应使用 `url` 字段；历史 `image_url` 字段仍可使用，TokenRouter 会在转发前把它规范化为 `url`。如果两者同时存在，则保留非空的 `url`；空白 `url` 会回退使用 `image_url`。multipart 图片编辑中的上传文件也会转换为 `url` 形式的 data URL。

## 媒体账号资格

新的 Grok 图片或视频生成请求会执行媒体专用的账号资格检查。API Key 账号保持可用；OAuth 账号必须由 xAI 计费探测提供明确的付费资格证据。Free、禁止访问、缺少观测、观测格式错误或结论不明确的 OAuth 账号都不会承接新的媒体生成请求。尚无观测的 OAuth 账号会在第一次转发媒体请求前执行探测，导入账号时也会主动先执行计费探测。聊天请求和已有视频任务的状态查询不受这项隔离影响。当分组中没有合格账号时，媒体端点返回 HTTP `503`，错误类型为 `grok_media_no_eligible_account`。

管理员可通过账号创建或更新 API 的 `extra.grok_media_eligible` 覆盖自动判定：`false` 表示排除，`true` 表示强制允许；更新时传 `null` 可删除覆盖并恢复基于探测结果的自动判定，省略该字段则保留现有覆盖。仅出现每周用量周期不能作为付费层级证据。图片接口返回成功时必须包含至少一个实际图片输出；空的 HTTP `200` 响应会触发账号 failover，不会作为成功生成结果计数或返回。

## 客户端配置

用户可在 API Key 页面通过“使用密钥”生成 Grok Build CLI 或 OpenCode 配置。现有 `config.toml` 应先备份，再合并新模型配置。

Grok Build CLI 的模型配置必须指向 TokenRouter 对外地址（以 `/v1` 结尾），不能直接使用 `api.x.ai` 或内部 OAuth 代理地址。OAuth 流量默认转发到 Grok CLI 订阅代理。

## 初始模型

- `grok-4.3`
- `grok-build-0.1`
- `grok-4.20-0309-reasoning`
- `grok-4.20-0309-non-reasoning`
- `grok-4.20-multi-agent-0309`

## 环境变量

- `XAI_OAUTH_CLIENT_ID`
- `XAI_OAUTH_SCOPE`
- `XAI_OAUTH_REDIRECT_URI`
- `XAI_OAUTH_AUTHORIZE_URL`
- `XAI_OAUTH_TOKEN_URL`
- `XAI_BASE_URL`
- `XAI_GROK_CLI_VERSION`：覆盖 Grok CLI 客户端版本，配置值不得低于内置支持版本
