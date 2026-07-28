# TokenRouter 团队功能 Review 修复 V2

## Summary

- 将上一轮未提交改动完整归档到 `codex/team-review-ai-backup-20260727`，不从中复用实现。
- 从 `main@c58511ec1` 重新修复全部 Review 问题。
- 并发处理以“无越权、无资产透支”为底线，允许短期偏保守或无害不一致。

## 分支与数据模型

- 使用 `codex/team-review-fixes-v2` 承载新实现，不推送远端，不提交 `SYNC.md`。
- 新增迁移 `223_harden_team_lifecycle_and_allowance.sql`：
  - `api_keys.team_owner_disabled` 区分 Owner 锁定与 Member 自行停用。
  - `batch_image_jobs.allowance_reserved` 标记是否已按预计金额预记额度。
  - 清理已删除 Member 的活跃关系并强化用户删除触发器。
- 生成 Ent 代码，所有手写注释使用中文。

## 核心实现

- 两套生产网关统一校验团队总开关、团队状态、当前 Membership、Actor、Billing Owner 与成员限额，并返回稳定的 401/403/429。
- 团队认证读取不再写 Membership；过期窗口只在返回快照中归零，计费写入时再原子维护。
- Owner 可查看、禁用、启用和删除全团队 Key；Member 无法清除 Owner 锁定。
- 移除与创建 Key 的竞态通过当前 Membership、加入时间和重新入队时禁用旧 Key保证无害，不增加复杂锁。
- `member_limit=0` 禁止新增 Member；邀请接受先锁用户，再锁邀请和团队。
- 邀请邮件按同团队同邮箱 60 秒、每团队每小时 20 次进行 Redis 原子限流。
- 管理员团队 PATCH 单次原子更新，强制转让拒绝已删除用户。
- `team.enabled=false` 阻止用户侧团队能力和团队 Key流量，管理员保留存量管理能力。

## 异步计费与读模型

- 余额冻结事务按预计金额同时预记 Key 总额度、5h/1d/7d额度和 Member 日/周/月额度。
- Capture 回退预计与实际差额；Release 回退全部预记；跨窗口只做安全回退，允许暂时偏保守。
- 旧任务按 `allowance_reserved=false` 兼容，并按实际金额补记额度。
- 批任务缓存失效使用 `BillingUserID`；倍率和用量读取明确区分 Actor 与 BillingUser。
- 新增团队成员用量聚合接口，包含当前及离队成员；Owner 团队页增加 Key管理入口。

## Test Plan

- 覆盖网关错误映射、团队生命周期、Owner Key锁定、成员上限、删除清理、邀请锁顺序和管理员 PATCH。
- 覆盖批任务预记、结算、释放、跨窗口、历史任务及删除/离队收尾。
- 覆盖团队总开关、Owner Key管理、BillingUser 倍率、Key 日用量和离队成员图表。
- 运行迁移测试、Ent 生成校验、`go test -tags=unit ./...`、前端测试、typecheck、lint、build 和 `git diff --check`。
