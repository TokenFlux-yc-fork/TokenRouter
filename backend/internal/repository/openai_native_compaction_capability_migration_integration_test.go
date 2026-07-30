//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration227OpenAINativeCompactionCapabilityUpgradeAndReapply(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("227_openai_native_compaction_v2_capability.sql")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `
DROP TABLE IF EXISTS openai_native_compaction_override_audits;
DROP TABLE IF EXISTS openai_native_compaction_probe_results;
DROP TABLE IF EXISTS openai_native_compaction_capabilities;
`)
	require.NoError(t, err)

	var upgradeAccountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-227-upgrade-account', 'openai', 'apikey', '{}'::jsonb)
RETURNING id
`).Scan(&upgradeAccountID))

	// Upgrade from the historical pre-v2 state where neither capability table exists.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var fixtureCapabilityID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source, next_probe_at
)
VALUES ($1, 'upstream_v1_reapply_fixture', 'gpt-reapply-fixture',
	'remote_compaction_v2', TRUE, 'auto', 'probe', NOW())
RETURNING id
`, upgradeAccountID).Scan(&fixtureCapabilityID))

	// Reapplying the idempotent migration must preserve existing canonical rows.
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	var preserved bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT supported
FROM openai_native_compaction_capabilities
WHERE id = $1
`, fixtureCapabilityID).Scan(&preserved))
	require.True(t, preserved)

	var capabilitiesRegclass, resultsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT to_regclass('public.openai_native_compaction_capabilities')").Scan(&capabilitiesRegclass))
	require.True(t, capabilitiesRegclass.Valid)
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT to_regclass('public.openai_native_compaction_probe_results')").Scan(&resultsRegclass))
	require.True(t, resultsRegclass.Valid)

	requireColumn(t, tx, "openai_native_compaction_capabilities", "upstream_fingerprint", "character varying", 128, false)
	requireColumn(t, tx, "openai_native_compaction_capabilities", "effective_model", "character varying", 512, false)
	requireColumn(t, tx, "openai_native_compaction_capabilities", "contract_version", "character varying", 64, false)
	requireColumn(t, tx, "openai_native_compaction_capabilities", "supported", "boolean", 0, false)
	requireColumn(t, tx, "openai_native_compaction_capabilities", "next_probe_at", "timestamp with time zone", 0, true)
	requireColumn(t, tx, "openai_native_compaction_probe_results", "stale_identity", "boolean", 0, false)
	requireIndex(t, tx, "openai_native_compaction_capabilities", "idx_openai_native_compaction_capabilities_account")
	requireIndex(t, tx, "openai_native_compaction_capabilities", "idx_openai_native_compaction_capabilities_probe_due")
	requireIndex(t, tx, "openai_native_compaction_probe_results", "idx_openai_native_compaction_probe_results_account_checked")
	requireForeignKeyOnDelete(t, tx, "openai_native_compaction_capabilities", "account_id", "accounts", "CASCADE")
	requireForeignKeyOnDelete(t, tx, "openai_native_compaction_probe_results", "capability_id", "openai_native_compaction_capabilities", "SET NULL")
	requireConstraintDefinitionContains(
		t,
		tx,
		"openai_native_compaction_capabilities",
		"openai_native_compaction_capability_override_check",
		"mode",
		"'auto'",
		"source",
		"'probe'",
		"'trusted_official'",
		"'force_on'",
		"'force_off'",
		"'manual_override'",
		"override_created_at IS NOT NULL",
	)

	var accountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-227-account', 'openai', 'apikey', '{}'::jsonb)
RETURNING id
`).Scan(&accountID))

	_, err = tx.ExecContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source
) VALUES ($1, 'upstream_v1_fixture', 'gpt-fixture', 'remote_compaction_v2', TRUE, 'auto', 'probe')
`, accountID)
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, "SAVEPOINT invalid_override")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source
) VALUES ($1, 'upstream_v1_invalid', 'gpt-fixture', 'remote_compaction_v2', FALSE, 'force_on', 'manual_override')
`, accountID)
	require.ErrorContains(t, err, "openai_native_compaction_capability_override_check")
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT invalid_override")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, "SAVEPOINT invalid_source")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source
) VALUES ($1, 'upstream_v1_invalid_source', 'gpt-fixture', 'remote_compaction_v2', FALSE, 'auto', 'manual_override')
`, accountID)
	require.ErrorContains(t, err, "openai_native_compaction_capability_override_check")
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT invalid_source")
	require.NoError(t, err)
}
