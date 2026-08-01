package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// TestGetUserDashboardStatsUsesScalarOwnedTeamScope 锁定仪表盘全部团队范围查询的标量子查询形状。
func TestGetUserDashboardStatsUsesScalarOwnedTeamScope(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	const scalarTeamScope = "team_id = \\(.*SELECT tm\\.team_id FROM team_memberships tm.*tm\\.role = 'owner'.*tm\\.left_at IS NULL.*\\)"

	mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\) FROM api_keys.*WHERE deleted_at IS NULL.*" + scalarTeamScope).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))
	mock.ExpectQuery("(?s)SELECT COUNT\\(\\*\\) FROM api_keys.*WHERE status = \\$2 AND deleted_at IS NULL.*"+scalarTeamScope).
		WithArgs(int64(7), service.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(2)))
	mock.ExpectQuery("(?s)FROM usage_logs.*WHERE user_id = \\$1 OR " + scalarTeamScope).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens", "total_cache_creation_tokens",
			"total_cache_read_tokens", "total_cost", "total_actual_cost", "avg_duration_ms",
		}).AddRow(int64(3), int64(10), int64(20), int64(2), int64(4), 0.4, 0.3, 100.0))
	mock.ExpectQuery("(?s)FROM usage_logs.*WHERE created_at >= \\$2.*AND \\(user_id = \\$1 OR "+scalarTeamScope).
		WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"today_requests", "today_input_tokens", "today_output_tokens", "today_cache_creation_tokens",
			"today_cache_read_tokens", "today_cost", "today_actual_cost",
		}).AddRow(int64(1), int64(5), int64(6), int64(1), int64(2), 0.2, 0.15))
	mock.ExpectQuery("(?s)FROM usage_logs.*WHERE created_at >= \\$1.*AND \\(user_id = \\$2 OR "+scalarTeamScope).
		WithArgs(sqlmock.AnyArg(), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"request_count", "token_count"}).AddRow(int64(10), int64(100)))

	stats, err := repo.GetUserDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalAPIKeys)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(36), stats.TotalTokens)
	require.Equal(t, int64(2), stats.Rpm)
	require.Equal(t, int64(20), stats.Tpm)
	require.NoError(t, mock.ExpectationsWereMet())
}
