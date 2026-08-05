package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUsageAnalyticsRollupMigrationAvoidsLargeTableRewrite 验证迁移只建新表和清理小设置记录。
func TestUsageAnalyticsRollupMigrationAvoidsLargeTableRewrite(t *testing.T) {
	content, err := FS.ReadFile("229_usage_analytics_rollups.sql")
	require.NoError(t, err)

	sql := strings.ToUpper(string(content))
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_HOURLY")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_DAILY")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS USAGE_ANALYTICS_AGGREGATION_STATE")
	require.NotContains(t, sql, "ALTER TABLE USAGE_LOGS")
	require.NotContains(t, sql, "UPDATE USAGE_LOGS")
	require.NotContains(t, sql, "INSERT INTO USAGE_ANALYTICS_HOURLY SELECT")
	require.Contains(t, sql, "WHERE KEY = 'OPS_ADVANCED_SETTINGS'")
	require.Contains(t, sql, "DELETE FROM SETTINGS WHERE KEY = 'OPS_QUERY_MODE_DEFAULT'")
}

// TestManualBackfillStateMigrationAvoidsUsageLogs 验证手动游标迁移只修改单行状态表。
func TestManualBackfillStateMigrationAvoidsUsageLogs(t *testing.T) {
	content, err := FS.ReadFile("230_pre_aggregation_manual_backfill.sql")
	require.NoError(t, err)

	sql := strings.ToUpper(string(content))
	require.Contains(t, sql, "ALTER TABLE USAGE_ANALYTICS_AGGREGATION_STATE")
	require.Contains(t, sql, "MANUAL_BACKFILL_START")
	require.Contains(t, sql, "MANUAL_BACKFILL_CURSOR")
	require.NotContains(t, sql, "ALTER TABLE USAGE_LOGS")
	require.NotContains(t, sql, "UPDATE USAGE_LOGS")
	require.NotContains(t, sql, "FROM USAGE_LOGS")
}
