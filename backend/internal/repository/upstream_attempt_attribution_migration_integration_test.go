//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration228UpstreamAttemptAttributionUpgradeAndReapply(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("228_upstream_attempt_attribution.sql")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS upstream_attempt_attributions`)
	require.NoError(t, err)

	// Upgrade from the historical state where the attempt ledger did not exist.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO upstream_attempt_attributions (
			attempt_id, client_request_id, gateway_request_id, account_id, transport,
			upstream_provider, upstream_fingerprint, effective_model, contract_version,
			state, state_version, started_at, observed_at
		) VALUES (
			'attempt_reapply_fixture', 'client_reapply_fixture', 'gateway_reapply_fixture',
			42, 'http', 'openai', 'upstream_v1_reapply_fixture', 'gpt-reapply-fixture',
			'v2', 'started', 1, NOW(), NOW()
		)
	`)
	require.NoError(t, err)

	// Explicit reapply must preserve existing rows.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	var version int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT state_version FROM upstream_attempt_attributions
		WHERE attempt_id = 'attempt_reapply_fixture'
	`).Scan(&version))
	require.Equal(t, int64(1), version)

	requireColumn(t, tx, "upstream_attempt_attributions", "attempt_id", "character varying", 128, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "usage_observed", "boolean", 0, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "transport_observed", "boolean", 0, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "http_observed", "boolean", 0, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "semantic_observed", "boolean", 0, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "delivery_observed", "boolean", 0, false)
	requireColumn(t, tx, "upstream_attempt_attributions", "cost_usd", "numeric", 0, true)
	requireIndex(t, tx, "upstream_attempt_attributions", "idx_upstream_attempt_attributions_gateway_request")
	requireConstraintDefinitionContains(t, tx, "upstream_attempt_attributions", "upstream_attempt_attributions_usage_observed_check", "usage_observed", "input_tokens IS NULL", "cost_usd IS NULL")
	requireConstraintDefinitionContains(t, tx, "upstream_attempt_attributions", "upstream_attempt_attributions_failover_check", "delivery_committed", "safe_to_failover")
}
