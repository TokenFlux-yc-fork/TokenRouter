# 使用记录与运维预聚合

本文说明预聚合的配置、运行状态、历史回填、查询路由和故障降级。该功能用于避免仪表盘、Usage、API Key 用量及运维长时间窗口直接扫描大型原始记录表。

## 配置模型

运行时只有一个设置键：`pre_aggregation_settings`。

```json
{
  "usage": {
    "enabled": true,
    "interval_seconds": 60
  },
  "ops": {
    "enabled": true
  }
}
```

- `usage.enabled` 同时控制全局用量聚合、多维用量聚合及相关查询路由。
- `usage.interval_seconds` 范围为 30 到 3600 秒，默认值来自部署配置。
- `ops.enabled` 同时控制运维聚合任务及运维查询自动路由。
- 设置不存在时直接使用部署配置默认值，不读取旧运维高级设置或旧查询模式。

部署配置是能力上限，运行时设置不能绕过：

```yaml
dashboard_aggregation:
  enabled: true
  interval_seconds: 60
  lookback_seconds: 120
  backfill_enabled: true
  backfill_max_days: 31

ops:
  enabled: true
  aggregation:
    enabled: true
```

`dashboard_aggregation.enabled=false` 会强制关闭用量聚合；`ops.enabled=false` 或 `ops.aggregation.enabled=false` 会强制关闭运维聚合。修改部署能力需要重启，日常启停在“系统设置 > 通用设置 > 预聚合”中完成。

## 管理接口

- `GET /api/v1/admin/settings/pre-aggregation`：读取运行时设置、部署能力及任务状态。
- `PUT /api/v1/admin/settings/pre-aggregation`：完整更新运行时设置。
- `POST /api/v1/admin/settings/pre-aggregation/backfill`：异步请求最近若干天的历史回填。

PUT 请求示例：

```json
{
  "usage": {
    "enabled": true,
    "interval_seconds": 60
  },
  "ops": {
    "enabled": true
  }
}
```

回填请求示例：

```json
{
  "days": 7
}
```

接口返回 `202 Accepted` 后，回填由后台任务继续执行。允许天数由 `dashboard_aggregation.backfill_max_days` 限制，且部署配置必须允许手动回填。

## 用量聚合数据

迁移 `229_usage_analytics_rollups.sql` 只创建以下空表和索引，不修改或回填 `usage_logs`：

- `usage_analytics_hourly`：UTC 小时桶，多维明细。
- `usage_analytics_daily`：由小时表生成的 UTC 日桶。
- `usage_analytics_aggregation_state`：实时水位、历史覆盖游标和任务状态。

维度包括用户、计费用户、团队、API Key、分组、请求模型、请求类型、流式标记、计费类型、计费模式、有效平台和入站端点。指标包括请求数、各类 Token、总费用、实际费用、账号费用及请求耗时。

实时任务重算当前小时和回看窗口，以吸收迟到记录。每轮只刷新多维小时表；当实际回看范围触及已经闭合的 UTC 日期时，会从小时表同步重建对应日表，确保午夜后到达的迟到记录不会遗漏。回看范围完全进入当天后不再改写前一日日表。RPM/TPM 始终读取最近五分钟原始记录，避免聚合延迟影响实时指标。

## 历史回填

历史任务从新到旧按 UTC 小时推进，并在每个小时完成后保存游标：

- 每分钟最多启动一轮历史回填。
- 每轮最多处理 24 个小时块。
- 每轮数据库工作预算为 10 秒。
- 完成至少一个小时后预算不足时，任务保留已保存游标并继续显示 `backfill`，不会记录错误。
- 首个小时使用完整预算仍未完成时，才进入 `error` 并提示单小时块超时。
- 启动下一块前会参考上一小时的完整迭代耗时，剩余预算不足时提前结束本轮。
- 多实例通过 Redis 锁或 PostgreSQL advisory lock 避免重复执行。
- 失败或重启后从 `backfill_cursor` 继续。

自动历史回填逐小时向过去推进，完成一个 UTC 日期或到达最早记录所在的部分日期时才重建一次日表。只有日表重建成功后，覆盖游标才会跨过该日期；失败时下轮会幂等重试小时块和日表。

手动回填会建立独立的目标和游标，只重算请求的最近 N 天。任务仍复用定时聚合的分布式锁和预算，从新到旧逐小时推进，每个小时完成后保存进度；失败或重启后继续该范围，完成后恢复原有自动历史回填。手动回填和使用记录删除后的重算继续完整重建受影响的小时桶与日桶，以保持维护操作的一致性。当前尚未结束的小时由每轮实时聚合处理，不会启动独立的长时间全范围扫描。

## 状态字段

通用设置页面展示以下状态：

- `phase`：`disabled`、`idle`、`live`、`backfill`、`error` 或 `unavailable`。
- `live_watermark`：实时聚合已处理到的时间。
- `coverage_start`：连续聚合覆盖的最早时间。
- `source_oldest_at`：原始使用记录的最早时间。
- `last_run_at`、`last_success_at`、`last_error_at`：最近运行结果。
- `last_duration_ms`、`last_error`：最近耗时和错误。

正常情况下 `live_watermark` 与当前时间的差值应接近刷新间隔。`coverage_start` 会随历史回填逐步向 `source_oldest_at` 移动。

## 查询路由与降级

用量查询按半开区间组合结果，避免边界重复：

1. 完整 UTC 日读取日表。
2. 日边界和非完整日期读取小时表。
3. 覆盖线之前、当前水位之后及不足一小时的边界读取原始表。

趋势查询使用小时表后按配置时区分桶，因此支持任意时区和夏令时切换。账号、上游端点、端点路径、上游模型和模型映射等未聚合维度继续读取原始表。

聚合关闭、覆盖不足、过滤条件不支持或聚合 SQL 失败时，接口透明回退原始表。覆盖不足和不支持的维度属于正常路由，不记录告警；真实聚合 SQL 错误会按操作每分钟最多记录一次告警。运维接口不接受查询模式参数；实时监控、告警计算和自定义忽略状态码由后端内部固定读取原始表。

## 清理、删除与备份

- 小时表和日表跟随 `dashboard_aggregation.retention` 清理。
- 使用记录主动清理完成后，会触发对应时间范围的聚合重算；这是运行时开关关闭时仍会执行的内部一致性维护，周期聚合任务本身仍保持停止。
- 新聚合表归入备份的“使用记录”内容分组；排除使用记录数据时只保留表结构。
- 本次上线不删除现有 `usage_logs` 大索引。应至少采集七天索引使用情况后再单独评估并发删除。

## 上线与验收

推荐顺序：

1. 部署新后端并执行迁移；迁移不会扫描或重写大型使用记录表。
2. 同步部署前端，确认通用设置页面可以读取状态。
3. 开启用量聚合，观察实时水位、单轮耗时和错误。
4. 先回填最近 1 到 7 天，确认查询结果与原始统计一致，再继续历史覆盖。
5. 对仪表盘、Usage、API Key 批量用量和排行采集 P95。

验收目标是目标接口不再执行无界 `usage_logs` 扫描，聚合覆盖后的 P95 低于 300 毫秒，实时聚合低于 2 秒，历史回填每分钟数据库工作不超过 10 秒。
