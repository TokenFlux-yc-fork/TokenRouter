-- Require bounded manual overrides and preserve immutable set/revoke audit rows.
SET LOCAL lock_timeout = '1s';

CREATE TABLE IF NOT EXISTS openai_native_compaction_override_audits (
    id BIGSERIAL PRIMARY KEY,
    capability_id BIGINT REFERENCES openai_native_compaction_capabilities(id) ON DELETE SET NULL,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    upstream_fingerprint VARCHAR(128) NOT NULL,
    effective_model VARCHAR(512) NOT NULL,
    contract_version VARCHAR(64) NOT NULL,
    action VARCHAR(16) NOT NULL,
    mode VARCHAR(16) NOT NULL,
    override_actor VARCHAR(255) NOT NULL,
    override_reason VARCHAR(2048) NOT NULL,
    override_created_at TIMESTAMPTZ NOT NULL,
    override_expires_at TIMESTAMPTZ NOT NULL,
    override_revoked_at TIMESTAMPTZ,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_native_compaction_override_audit_action_check
        CHECK (action IN ('set', 'revoke')),
    CONSTRAINT openai_native_compaction_override_audit_mode_check
        CHECK (mode IN ('force_on', 'force_off')),
    CONSTRAINT openai_native_compaction_override_audit_metadata_check
        CHECK (
            BTRIM(override_actor) <> ''
            AND BTRIM(override_reason) <> ''
            AND override_expires_at > override_created_at
            AND (
                (action = 'set' AND override_revoked_at IS NULL)
                OR
                (action = 'revoke' AND override_revoked_at IS NOT NULL
                    AND override_revoked_at >= override_created_at)
            )
        )
);

CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_override_audits_exact
    ON openai_native_compaction_override_audits (
        account_id, upstream_fingerprint, effective_model, contract_version,
        recorded_at DESC, id DESC
    );

ALTER TABLE openai_native_compaction_capabilities
    DROP CONSTRAINT IF EXISTS openai_native_compaction_capability_override_check;

ALTER TABLE openai_native_compaction_capabilities
    ADD CONSTRAINT openai_native_compaction_capability_override_check
    CHECK (
        (
            mode = 'auto'
            AND source IN ('probe', 'trusted_official')
            AND override_actor = ''
            AND override_reason = ''
            AND override_created_at IS NULL
            AND override_expires_at IS NULL
            AND override_revoked_at IS NULL
        ) OR (
            mode IN ('force_on', 'force_off')
            AND source = 'manual_override'
            AND BTRIM(override_actor) <> ''
            AND BTRIM(override_reason) <> ''
            AND override_created_at IS NOT NULL
            AND override_expires_at IS NOT NULL
            AND override_expires_at > override_created_at
            AND override_revoked_at IS NULL
        )
    );

COMMENT ON TABLE openai_native_compaction_override_audits IS
    'Payload-free immutable audit of exact-key native compaction manual override set and revoke actions';
