package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTeamMigrationContainsIsolationAndAttributionConstraints(t *testing.T) {
	content, err := FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 这些部分唯一索引共同保证单团队关系和单 Owner 约束。
	require.Contains(t, sql, "team_memberships_active_user_uq")
	require.Contains(t, sql, "on team_memberships (user_id) where left_at is null")
	require.Contains(t, sql, "team_memberships_active_owner_uq")
	require.Contains(t, sql, "where left_at is null and role = 'owner'")

	// Key、普通用量和异步图片任务都必须保留团队及付款人归因。
	require.Contains(t, sql, "api_keys_team_id_fkey")
	require.Contains(t, sql, "usage_logs_billing_user_id_fkey")
	require.Contains(t, sql, "usage_logs_team_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_billing_user_id_fkey")
	require.Contains(t, sql, "batch_image_jobs_team_id_fkey")

	// 删除 Owner 前必须先转让所有权或解散团队。
	require.Contains(t, sql, "prevent_active_team_owner_deletion")
	require.Contains(t, sql, "users_prevent_active_team_owner_soft_delete")
	require.Contains(t, sql, "users_prevent_active_team_owner_hard_delete")
}

func TestTeamMigrationKeepsHistoricalAttributionOnlineCompatible(t *testing.T) {
	content, err := FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	normalized := strings.Join(strings.Fields(sql), " ")

	require.Contains(t, normalized, "set local lock_timeout = '1s'")
	require.NotContains(t, normalized, "update usage_logs set billing_user_id")
	require.NotContains(t, normalized, "update batch_image_jobs set billing_user_id")
	require.NotContains(t, normalized, "alter table usage_logs alter column billing_user_id set not null")
	require.NotContains(t, normalized, "alter table batch_image_jobs alter column billing_user_id set not null")
	require.Contains(t, normalized, "usage_logs_billing_user_id_present_check")
	require.Contains(t, normalized, "batch_image_jobs_billing_user_id_present_check")
	require.Contains(t, normalized, "before insert or update on usage_logs")
	require.Contains(t, normalized, "before insert or update on batch_image_jobs")
	require.Contains(t, normalized, "not valid")

	indexContent, err := FS.ReadFile("221a_team_attribution_indexes_notx.sql")
	require.NoError(t, err)
	indexSQL := strings.ToLower(string(indexContent))
	require.Contains(t, indexSQL, "create index concurrently if not exists usage_logs_billing_user_created_idx")
	require.Contains(t, indexSQL, "where billing_user_id is not null")
	require.Contains(t, indexSQL, "create index concurrently if not exists usage_logs_team_created_idx")
	require.Contains(t, indexSQL, "create index concurrently if not exists batch_image_jobs_team_created_idx")
	require.NotContains(t, sql, "create index if not exists usage_logs_billing_user_created_idx")
}

func TestTeamDefaultMemberLimitsMigration(t *testing.T) {
	content, err := FS.ReadFile("222_add_team_default_member_limits.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 三种自然周期默认限额必须同时存在，并由数据库阻止负数。
	require.Contains(t, sql, "default_daily_limit_usd")
	require.Contains(t, sql, "default_weekly_limit_usd")
	require.Contains(t, sql, "default_monthly_limit_usd")
	require.Contains(t, sql, "teams_default_member_limits_check")
}

func TestTeamLifecycleAndAllowanceMigration(t *testing.T) {
	content, err := FS.ReadFile("223_harden_team_lifecycle_and_allowance.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	require.Contains(t, sql, "set local lock_timeout = '1s'")

	// 两个轻量字段分别承载 Owner 锁定和批任务额度预记，不引入额外预留表。
	require.Contains(t, sql, "team_owner_disabled")
	require.Contains(t, sql, "allowance_reserved")
	require.NotContains(t, sql, "batch_image_billing_reservations")

	// 删除用户必须同时处理 Owner 保护、Member 离队和团队 Key 禁用。
	require.Contains(t, sql, "team_owner_transfer_required")
	require.Contains(t, sql, "update team_memberships")
	require.Contains(t, sql, "update api_keys")
}

func TestBatchImageSubscriptionMigrationUsesBoundedLockWait(t *testing.T) {
	content, err := FS.ReadFile("224_batch_image_subscription_billing.sql")
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(string(content)), "set local lock_timeout = '1s'")
}
