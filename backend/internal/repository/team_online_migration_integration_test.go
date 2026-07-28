//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	migrationfs "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestTeamMigrationPreservesHistoricalAttributionWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)

	_, err := tx.ExecContext(ctx, `
CREATE SCHEMA team_online_migration;
SET LOCAL search_path TO team_online_migration, pg_catalog;

CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    deleted_at TIMESTAMPTZ NULL
);
CREATE TABLE api_keys (
    id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    deleted_at TIMESTAMPTZ NULL
);
CREATE TABLE usage_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE batch_image_jobs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NULL REFERENCES api_keys(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO users (id) VALUES (1);
INSERT INTO api_keys (id, user_id) VALUES (10, 1);
INSERT INTO usage_logs (user_id, api_key_id) VALUES (1, 10);
INSERT INTO batch_image_jobs (user_id, api_key_id) VALUES (1, 10);
`)
	require.NoError(t, err)

	content, err := migrationfs.FS.ReadFile("221_add_teams.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(content))
	require.NoError(t, err)

	var usageBilling, batchBilling sql.NullInt64
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT billing_user_id FROM usage_logs WHERE id = 1`).Scan(&usageBilling))
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT billing_user_id FROM batch_image_jobs WHERE id = 1`).Scan(&batchBilling))
	require.False(t, usageBilling.Valid, "historical usage row must not be rewritten")
	require.False(t, batchBilling.Valid, "historical batch row must not be rewritten")

	for _, constraint := range []string{
		"usage_logs_billing_user_id_fkey",
		"usage_logs_billing_user_id_present_check",
		"batch_image_jobs_billing_user_id_fkey",
		"batch_image_jobs_billing_user_id_present_check",
	} {
		var validated bool
		require.NoError(t, tx.QueryRowContext(ctx, `
SELECT convalidated
FROM pg_constraint
WHERE conname = $1
  AND connamespace = 'team_online_migration'::regnamespace
`, constraint).Scan(&validated))
		require.Falsef(t, validated, "%s should remain NOT VALID during the online phase", constraint)
	}

	var insertedUsageBilling, insertedBatchBilling int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO usage_logs (user_id, api_key_id)
VALUES (1, 10)
RETURNING billing_user_id
`).Scan(&insertedUsageBilling))
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO batch_image_jobs (user_id, api_key_id)
VALUES (1, 10)
RETURNING billing_user_id
`).Scan(&insertedBatchBilling))
	require.Equal(t, int64(1), insertedUsageBilling)
	require.Equal(t, int64(1), insertedBatchBilling)

	require.NoError(t, tx.QueryRowContext(ctx, `
UPDATE usage_logs SET created_at = created_at WHERE id = 1
RETURNING billing_user_id
`).Scan(&insertedUsageBilling))
	require.NoError(t, tx.QueryRowContext(ctx, `
UPDATE batch_image_jobs SET created_at = created_at WHERE id = 1
RETURNING billing_user_id
`).Scan(&insertedBatchBilling))
	require.Equal(t, int64(1), insertedUsageBilling)
	require.Equal(t, int64(1), insertedBatchBilling)
}
