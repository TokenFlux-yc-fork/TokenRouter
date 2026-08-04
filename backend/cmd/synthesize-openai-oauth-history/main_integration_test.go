//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/repository"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestExecuteHistoryAndRollback(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("docker is required in CI: %v", err)
		}
		t.Skipf("docker is unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("tokenrouter_fixture"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(ctx))
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	maintenanceConn, err := db.Conn(ctx)
	require.NoError(t, err)
	maintenanceTx, err := beginMaintenanceTx(ctx, maintenanceConn)
	require.NoError(t, err)
	var isolation string
	require.NoError(t, maintenanceTx.QueryRowContext(ctx, "SHOW transaction_isolation").Scan(&isolation))
	require.Equal(t, "read committed", isolation)
	require.NoError(t, maintenanceTx.Rollback())
	require.NoError(t, maintenanceConn.Close())

	cutoff := time.Date(2026, 8, 4, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	userID, apiKeyID, sourceOne, sourceTwo := seedHistoryFixture(t, ctx, db, cutoff)
	_ = userID
	_ = apiKeyID

	opts := options{
		Before:           cutoff,
		AccountCount:     80,
		TeamRatio:        0.25,
		Seed:             42,
		BatchID:          "integration-history-batch",
		SourceAccountIDs: []int64{sourceOne, sourceTwo},
		ChunkSize:        17,
	}
	dryPlan, err := buildHistoryPlan(ctx, db, opts)
	require.NoError(t, err)
	mismatchOpts := opts
	mismatchOpts.ExpectedDigest = strings.Repeat("0", sha256.Size*2)
	_, _, err = executeHistory(ctx, db, mismatchOpts)
	require.ErrorContains(t, err, "plan digest mismatch")
	var accountsAfterMismatch int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE extra->>$1 = $2", batchIDExtraKey, opts.BatchID).Scan(&accountsAfterMismatch))
	require.Zero(t, accountsAfterMismatch)
	opts.ExpectedDigest = dryPlan.Digest
	plan, result, err := executeHistory(ctx, db, opts)
	require.NoError(t, err)
	require.Equal(t, int64(240), plan.UsageRows)
	require.Len(t, plan.Accounts, 80)
	require.Equal(t, 60, plan.PlusCount)
	require.Equal(t, 20, plan.TeamCount)
	require.Equal(t, int64(80), result.InsertedAccounts)
	require.Equal(t, int64(240), result.ReassignedUsageRows)
	require.Equal(t, int64(80), result.UpdatedUsageSnapshots)
	require.Equal(t, int64(80), result.ErroredAccounts)
	require.Zero(t, result.SourceRowsRemaining)
	require.Zero(t, result.GeneratedRowsAtCutoff)

	var generatedCount, revokedCount, generatedUsageBefore, generatedUsageAfter int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'error' AND schedulable = FALSE AND error_message = $1)
		FROM accounts
		WHERE extra->>$2 = $3`, revokedErrorMessage, batchIDExtraKey, opts.BatchID).Scan(&generatedCount, &revokedCount))
	require.Equal(t, int64(80), generatedCount)
	require.Equal(t, generatedCount, revokedCount)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE ul.created_at < $3),
			COUNT(*) FILTER (WHERE ul.created_at >= $3)
		FROM usage_logs ul
		JOIN accounts a ON a.id = ul.account_id
		WHERE a.extra->>$1 = $2`, batchIDExtraKey, opts.BatchID, cutoff).Scan(&generatedUsageBefore, &generatedUsageAfter))
	require.Equal(t, int64(240), generatedUsageBefore)
	require.Zero(t, generatedUsageAfter)

	var emptyHistoryAccounts, invalidCreationTimes, missingSnapshots, missingGroups int64
	require.NoError(t, db.QueryRowContext(ctx, `
		WITH generated AS (
			SELECT id, created_at, extra
			FROM accounts
			WHERE extra->>$1 = $2
		), stats AS (
			SELECT g.id, g.created_at, g.extra, COUNT(ul.id) AS rows, MIN(ul.created_at) AS first_usage
			FROM generated g
			LEFT JOIN usage_logs ul ON ul.account_id = g.id
			GROUP BY g.id, g.created_at, g.extra
		)
		SELECT
			COUNT(*) FILTER (WHERE rows = 0),
			COUNT(*) FILTER (WHERE created_at >= first_usage),
			COUNT(*) FILTER (WHERE NOT (extra ? 'codex_5h_used_percent' AND extra ? 'codex_7d_used_percent' AND extra ? 'synthetic_history_assigned_cost_usd')),
			COUNT(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = stats.id))
		FROM stats`, batchIDExtraKey, opts.BatchID).Scan(&emptyHistoryAccounts, &invalidCreationTimes, &missingSnapshots, &missingGroups))
	require.Zero(t, emptyHistoryAccounts)
	require.Zero(t, invalidCreationTimes)
	require.Zero(t, missingSnapshots)
	require.Zero(t, missingGroups)

	var sourceRowsBefore, sourceRowsAtOrAfter int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE created_at < $2),
			COUNT(*) FILTER (WHERE created_at >= $2)
		FROM usage_logs
		WHERE account_id = ANY($1)`, pq.Array([]int64{sourceOne, sourceTwo}), cutoff).Scan(&sourceRowsBefore, &sourceRowsAtOrAfter))
	require.Zero(t, sourceRowsBefore)
	require.Equal(t, int64(40), sourceRowsAtOrAfter)

	var generatedIDs pq.Int64Array
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COALESCE(ARRAY_AGG(id ORDER BY id), '{}'::bigint[])
		FROM accounts
		WHERE extra->>$1 = $2`, batchIDExtraKey, opts.BatchID).Scan(&generatedIDs))
	require.Len(t, generatedIDs, 80)
	var outboxWatermark int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox").Scan(&outboxWatermark))
	rollbackOpts := options{RollbackBatch: opts.BatchID, Execute: true, ChunkSize: 13}
	rollbackPlan, rollbackResult, err := runRollback(ctx, db, rollbackOpts)
	require.NoError(t, err)
	require.Equal(t, int64(80), rollbackPlan.AccountCount)
	require.Equal(t, int64(240), rollbackPlan.UsageRows)
	require.Equal(t, int64(240), rollbackResult.RestoredUsageRows)
	require.Equal(t, int64(80), rollbackResult.DeletedAccounts)
	var rollbackEvents, rollbackEventsWithGroups int64
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE payload ? 'group_ids')
		FROM scheduler_outbox
		WHERE id > $1 AND account_id = ANY($2)`, outboxWatermark, pq.Array([]int64(generatedIDs))).Scan(&rollbackEvents, &rollbackEventsWithGroups))
	require.Equal(t, int64(80), rollbackEvents)
	require.Equal(t, rollbackEvents, rollbackEventsWithGroups)

	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE extra->>$1 = $2", batchIDExtraKey, opts.BatchID).Scan(&generatedCount))
	require.Zero(t, generatedCount)
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE created_at < $2),
			COUNT(*) FILTER (WHERE created_at >= $2)
		FROM usage_logs
		WHERE account_id = ANY($1)`, pq.Array([]int64{sourceOne, sourceTwo}), cutoff).Scan(&sourceRowsBefore, &sourceRowsAtOrAfter))
	require.Equal(t, int64(240), sourceRowsBefore)
	require.Equal(t, int64(40), sourceRowsAtOrAfter)
}

func seedHistoryFixture(t *testing.T, ctx context.Context, db *sql.DB, cutoff time.Time) (int64, int64, int64, int64) {
	t.Helper()
	var groupOne, groupTwo int64
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO groups (name, platform, status) VALUES ('fixture-openai-one', 'openai', 'active') RETURNING id").Scan(&groupOne))
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO groups (name, platform, status) VALUES ('fixture-openai-two', 'openai', 'active') RETURNING id").Scan(&groupTwo))

	var userID int64
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO users (email, password_hash) VALUES ('fixture-user@example.invalid', 'fixture-hash') RETURNING id").Scan(&userID))
	var apiKeyID int64
	require.NoError(t, db.QueryRowContext(ctx, "INSERT INTO api_keys (user_id, key, name, group_id) VALUES ($1, $2, 'fixture-key', $3) RETURNING id", userID, "sk-fixture-integration-key", groupOne).Scan(&apiKeyID))

	insertSource := func(name string, groupID int64) int64 {
		var accountID int64
		require.NoError(t, db.QueryRowContext(ctx, `
			INSERT INTO accounts (name, platform, type, credentials, extra, concurrency, priority, status, schedulable)
			VALUES ($1, 'openai', 'apikey', '{"api_key":"fixture"}'::jsonb, '{}'::jsonb, 8, 30, 'active', TRUE)
			RETURNING id`, name).Scan(&accountID))
		_, err := db.ExecContext(ctx, "INSERT INTO account_groups (account_id, group_id, priority) VALUES ($1, $2, 10)", accountID, groupID)
		require.NoError(t, err)
		return accountID
	}
	sourceOne := insertSource("fixture-apikey-one", groupOne)
	sourceTwo := insertSource("fixture-apikey-two", groupTwo)

	insertUsage := func(sourceID int64, prefix string, beforeRows int, spacing time.Duration) {
		for i := 0; i < beforeRows; i++ {
			createdAt := cutoff.Add(-time.Duration(beforeRows-i) * spacing)
			cost := 0.10 + float64(i%10)*0.05
			_, err := db.ExecContext(ctx, `
				INSERT INTO usage_logs (
					user_id, billing_user_id, api_key_id, account_id, request_id, model,
					total_cost, actual_cost, account_stats_cost, account_rate_multiplier, created_at
				) VALUES ($1, $1, $2, $3, $4, 'gpt-5', $5, $5, $5, 1, $6)`,
				userID, apiKeyID, sourceID, fmt.Sprintf("%s-before-%03d", prefix, i), cost, createdAt)
			require.NoError(t, err)
		}
		for i := 0; i < 20; i++ {
			createdAt := cutoff.Add(time.Duration(i) * time.Minute)
			_, err := db.ExecContext(ctx, `
				INSERT INTO usage_logs (
					user_id, billing_user_id, api_key_id, account_id, request_id, model,
					total_cost, actual_cost, account_stats_cost, account_rate_multiplier, created_at
				) VALUES ($1, $1, $2, $3, $4, 'gpt-5', 1, 1, 1, 1, $5)`,
				userID, apiKeyID, sourceID, fmt.Sprintf("%s-after-%03d", prefix, i), createdAt)
			require.NoError(t, err)
		}
	}
	insertUsage(sourceOne, "one", 160, 15*time.Minute)
	insertUsage(sourceTwo, "two", 80, 30*time.Minute)
	_, err := db.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", sourceTwo)
	require.NoError(t, err)
	return userID, apiKeyID, sourceOne, sourceTwo
}
