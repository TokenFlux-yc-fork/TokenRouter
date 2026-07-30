package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionProbeBudgetMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("229_openai_native_compaction_probe_budget.sql")
	require.NoError(t, err)
	sql := string(content)

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS openai_native_compaction_probe_budgets",
		"budget_day DATE PRIMARY KEY",
		"reserved_micro_usd BIGINT NOT NULL DEFAULT 0",
		"committed_micro_usd BIGINT NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS openai_native_compaction_probe_budget_reservations",
		"reservation_id UUID PRIMARY KEY",
		"amount_micro_usd BIGINT NOT NULL",
		"state IN ('reserved', 'committed', 'released')",
		"state = 'released' AND dispatched_at IS NULL",
		"state = 'committed' AND dispatched_at IS NOT NULL",
		"REFERENCES openai_native_compaction_probe_budgets(budget_day) ON DELETE RESTRICT",
	} {
		require.Contains(t, sql, fragment)
	}

	lower := strings.ToLower(sql)
	require.NotContains(t, lower, "user_id")
	require.NotContains(t, lower, "api_key_id")
	require.NotContains(t, lower, "subscription")
	require.NotContains(t, lower, "usage_logs")
	require.NotContains(t, lower, "encrypted_content")
}
