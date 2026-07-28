-- Existing-table indexes must be built outside the migration transaction.
-- Partial predicates keep legacy rows with NULL attribution out of the new indexes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS api_keys_team_id_idx
    ON api_keys (team_id) WHERE team_id IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS usage_logs_billing_user_created_idx
    ON usage_logs (billing_user_id, created_at DESC)
    WHERE billing_user_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS usage_logs_team_created_idx
    ON usage_logs (team_id, created_at DESC)
    WHERE team_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS batch_image_jobs_team_created_idx
    ON batch_image_jobs (team_id, created_at DESC)
    WHERE team_id IS NOT NULL;
