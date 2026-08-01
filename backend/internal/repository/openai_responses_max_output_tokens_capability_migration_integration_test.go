//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration234OpenAIResponsesMaxOutputTokensCapabilityUpgradeAndReapply(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("234_openai_responses_max_output_tokens_capability.sql")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS openai_responses_max_output_tokens_capabilities`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var accountID, capabilityID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-234-account', 'openai', 'apikey', '{}'::jsonb)
RETURNING id
`).Scan(&accountID))
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO openai_responses_max_output_tokens_capabilities (
    account_id, upstream_fingerprint, effective_model, contract_version,
    config_generation, state, checked_at, last_status, last_outcome
) VALUES ($1, 'responses_fixture', 'gpt-fixture', 'responses_max_output_tokens_v1',
    'generation_fixture', 'unsupported', NOW(), 400, 'explicit_unsupported_parameter')
RETURNING id
`, accountID).Scan(&capabilityID))

	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	var state string
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT state FROM openai_responses_max_output_tokens_capabilities WHERE id = $1
`, capabilityID).Scan(&state))
	require.Equal(t, "unsupported", state)

	requireColumn(t, tx, "openai_responses_max_output_tokens_capabilities", "upstream_fingerprint", "character varying", 128, false)
	requireColumn(t, tx, "openai_responses_max_output_tokens_capabilities", "effective_model", "character varying", 512, false)
	requireColumn(t, tx, "openai_responses_max_output_tokens_capabilities", "config_generation", "character varying", 128, false)
	requireIndex(t, tx, "openai_responses_max_output_tokens_capabilities", "idx_openai_responses_max_output_tokens_capabilities_account")
	requireForeignKeyOnDelete(t, tx, "openai_responses_max_output_tokens_capabilities", "account_id", "accounts", "CASCADE")
}
