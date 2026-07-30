-- Harden native compaction probe reservations for crash recovery and payload-free authorization audit.
-- Additive and idempotent: migration 229 remains immutable.
SET LOCAL lock_timeout = '1s';

ALTER TABLE openai_native_compaction_probe_budget_reservations
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE openai_native_compaction_probe_budget_reservations
    ADD COLUMN IF NOT EXISTS authorization_principal_sha256 VARCHAR(64);
ALTER TABLE openai_native_compaction_probe_results
    ADD COLUMN IF NOT EXISTS authorization_principal_sha256 VARCHAR(64);

-- Give reservations created by an older binary a conservative grace period.
UPDATE openai_native_compaction_probe_budget_reservations
SET expires_at = GREATEST(created_at + INTERVAL '10 minutes', NOW() + INTERVAL '10 minutes')
WHERE expires_at IS NULL;

ALTER TABLE openai_native_compaction_probe_budget_reservations
    ALTER COLUMN expires_at SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'openai_native_compaction_probe_reservation_principal_check'
          AND conrelid = 'openai_native_compaction_probe_budget_reservations'::regclass
    ) THEN
        ALTER TABLE openai_native_compaction_probe_budget_reservations
            ADD CONSTRAINT openai_native_compaction_probe_reservation_principal_check
            CHECK (authorization_principal_sha256 IS NULL OR authorization_principal_sha256 ~ '^[0-9a-f]{64}$');
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'openai_native_compaction_probe_result_principal_check'
          AND conrelid = 'openai_native_compaction_probe_results'::regclass
    ) THEN
        ALTER TABLE openai_native_compaction_probe_results
            ADD CONSTRAINT openai_native_compaction_probe_result_principal_check
            CHECK (authorization_principal_sha256 IS NULL OR authorization_principal_sha256 ~ '^[0-9a-f]{64}$');
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_probe_reservations_expired
    ON openai_native_compaction_probe_budget_reservations(expires_at, reservation_id)
    WHERE state = 'reserved';

COMMENT ON COLUMN openai_native_compaction_probe_budget_reservations.authorization_principal_sha256 IS
    'Irreversible identity of the authorization sentinel; never an upstream credential';
COMMENT ON COLUMN openai_native_compaction_probe_results.authorization_principal_sha256 IS
    'Irreversible identity of the authorization sentinel that gated dispatch';
