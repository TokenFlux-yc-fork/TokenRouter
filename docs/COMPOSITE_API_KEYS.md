# 复合 API Key

复合 API Key 允许一个密钥绑定多个分组。每个映射由分组和前缀组成，请求通过 `前缀/模型 ID` 选择分组。

## 创建与更新

创建复合 Key 时提交 `is_composite: true` 和完整的 `composite_groups`：

```json
{
  "name": "多平台开发",
  "is_composite": true,
  "composite_groups": [
    { "group_id": 10, "prefix": "GPT" },
    { "group_id": 20, "prefix": "Claude" }
  ]
}
```

更新接口对 `composite_groups` 使用完整替换语义。复合 Key 不能同时提交 `group_id`。从复合 Key 转回普通 Key 时必须提交 `is_composite: false` 和目标 `group_id`。

每个复合 Key 可配置 1 至 20 个映射。同一 Key 内分组不能重复，前缀按小写判重。前缀去除首尾空格后长度必须为 1 至 32，只能包含字母、数字、下划线和连字符。

新增数据共享分组时，请求必须携带当前数据共享须知的确认字段。一次确认覆盖该次请求新增的全部数据共享映射，每个映射都会独立保存确认记录。

## 请求格式

以下示例选择前缀为 `GPT` 的分组，内部实际模型为 `gpt-5`：

```json
{
  "model": "GPT/gpt-5",
  "messages": [{ "role": "user", "content": "Hello" }]
}
```

前缀匹配不区分大小写。服务只按第一个 `/` 拆分，因此 `GPT/vendor/model` 的实际模型 ID 是 `vendor/model`。

Gemini 原生入口将前缀放在模型 URL 中：

```text
POST /v1beta/models/Gemini/gemini-2.5-pro:generateContent
```

服务在鉴权后移除前缀，再执行分组权限、回退、订阅、倍率、限流、账号选择和计费。客户端模型名记录为带前缀名称，内部模型和上游模型记录为去前缀名称。

## 支持入口

- OpenAI、Anthropic 及兼容入口的 JSON 请求。
- 图片生成与编辑的 JSON 或 multipart 请求，包括异步图片提交。
- 单模型批量图片提交。
- Gemini `/v1beta/models/<前缀>/<模型>:<动作>` 入口。
- 无模型的用量、账单、异步图片与视频任务查询和批任务管理接口，这些接口只校验 Key 身份。

`/v1/models`、裸 `/models`、Gemini 模型列表和批量图片模型列表会按映射顺序聚合可用模型，并在每个模型 ID 前添加对应前缀。

## 错误

缺少前缀、未知前缀或非法前缀均返回 HTTP 400，并使用对应入口的 OpenAI、Anthropic 或 Google 错误结构。主要错误码如下：

- `COMPOSITE_KEY_MODEL_PREFIX_REQUIRED`
- `COMPOSITE_KEY_PREFIX_NOT_FOUND`
- `COMPOSITE_KEY_PREFIX_INVALID`
- `COMPOSITE_KEY_PREFIX_DUPLICATE`
- `COMPOSITE_KEY_GROUP_DUPLICATE`
- `COMPOSITE_KEY_GROUPS_REQUIRED`
- `COMPOSITE_KEY_TOO_MANY_GROUPS`
- `COMPOSITE_KEY_ENDPOINT_UNSUPPORTED`

## Realtime 限制

复合 Key 不支持 `/v1/live`、Codex Realtime、Responses WebSocket 和 Live sideband。这些入口可能在同一连接中切换模型，服务会返回 `COMPOSITE_KEY_ENDPOINT_UNSUPPORTED`，不会选择任意默认分组。
