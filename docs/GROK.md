# Grok / xAI 使用说明

TokenRouter 支持 Grok OAuth 订阅账号和标准 xAI API Key 账号，并通过 OpenAI 兼容的 Responses、Chat Completions、Messages 和 WebSocket 入口转发请求。Grok 分组还支持图片生成/编辑、视频生成/编辑/扩展以及视频状态查询。

## 基本信息

- 平台名：`grok`
- 账号类型：OAuth 订阅账号、API Key 账号
- Responses 网关入口：`/v1/responses` 和 `/responses`
- API Key 账号默认上游地址：`https://api.x.ai/v1`

## 账号配置

管理员可在控制台选择 OAuth 或 API Key 创建账号。OAuth 账号可通过控制台创建或重新授权；创建 Grok 分组并绑定账号后，用户即可生成分组 API Key。

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
