//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigration231OpenAINativeCompactionOverrideHardeningUpgradeAndReapply(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	capabilitySQL, err := dbmigrations.FS.ReadFile("227_openai_native_compaction_v2_capability.sql")
	require.NoError(t, err)
	hardeningSQL, err := dbmigrations.FS.ReadFile("231_harden_openai_native_compaction_manual_overrides.sql")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, `
DROP TABLE IF EXISTS openai_native_compaction_override_audits;
DROP TABLE IF EXISTS openai_native_compaction_probe_results;
DROP TABLE IF EXISTS openai_native_compaction_capabilities;
`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(capabilitySQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(hardeningSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(hardeningSQL))
	require.NoError(t, err)

	var auditsRegclass sql.NullString
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT to_regclass('public.openai_native_compaction_override_audits')").Scan(&auditsRegclass))
	require.True(t, auditsRegclass.Valid)
	requireColumn(t, tx, "openai_native_compaction_override_audits", "override_actor", "character varying", 255, false)
	requireColumn(t, tx, "openai_native_compaction_override_audits", "override_reason", "character varying", 2048, false)
	requireColumn(t, tx, "openai_native_compaction_override_audits", "override_expires_at", "timestamp with time zone", 0, false)
	requireIndex(t, tx, "openai_native_compaction_override_audits", "idx_openai_native_compaction_override_audits_exact")
	requireForeignKeyOnDelete(t, tx, "openai_native_compaction_override_audits", "capability_id", "openai_native_compaction_capabilities", "SET NULL")
	requireConstraintDefinitionContains(
		t, tx, "openai_native_compaction_capabilities", "openai_native_compaction_capability_override_check",
		"btrim", "override_actor", "override_reason", "override_expires_at IS NOT NULL",
		"override_expires_at > override_created_at", "override_revoked_at IS NULL",
	)

	var accountID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-231-account', 'openai', 'apikey', '{}'::jsonb)
RETURNING id
`).Scan(&accountID))

	_, err = tx.ExecContext(ctx, "SAVEPOINT permanent_override")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source, override_actor, override_reason, override_created_at
) VALUES ($1, 'upstream_v1_permanent', 'gpt-fixture', 'remote_compaction_v2',
	FALSE, 'force_on', 'manual_override', 'operator', 'incident', NOW())
`, accountID)
	require.ErrorContains(t, err, "openai_native_compaction_capability_override_check")
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT permanent_override")
	require.NoError(t, err)

	_, err = tx.ExecContext(ctx, "SAVEPOINT blank_reason")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
INSERT INTO openai_native_compaction_capabilities (
	account_id, upstream_fingerprint, effective_model, contract_version,
	supported, mode, source, override_actor, override_reason,
	override_created_at, override_expires_at
) VALUES ($1, 'upstream_v1_blank_reason', 'gpt-fixture', 'remote_compaction_v2',
	FALSE, 'force_off', 'manual_override', 'operator', '', NOW(), NOW() + INTERVAL '1 hour')
`, accountID)
	require.ErrorContains(t, err, "openai_native_compaction_capability_override_check")
	_, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT blank_reason")
	require.NoError(t, err)
}
