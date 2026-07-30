-- 为 OpenAI native remote compaction v2 增加 candidate-specific capability 状态。
-- 该能力不能由 generic Responses 或 legacy /responses/compact 状态推导。
SET LOCAL lock_timeout = '1s';

CREATE TABLE IF NOT EXISTS openai_native_compaction_capabilities (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    upstream_fingerprint VARCHAR(128) NOT NULL,
    effective_model VARCHAR(512) NOT NULL,
    contract_version VARCHAR(64) NOT NULL,
    supported BOOLEAN NOT NULL DEFAULT FALSE,
    mode VARCHAR(16) NOT NULL DEFAULT 'auto',
    source VARCHAR(32) NOT NULL,
    checked_at TIMESTAMPTZ,
    last_status INTEGER,
    last_semantic_failure VARCHAR(64) NOT NULL DEFAULT '',
    quarantined_until TIMESTAMPTZ,
    override_actor VARCHAR(255) NOT NULL DEFAULT '',
    override_reason VARCHAR(2048) NOT NULL DEFAULT '',
    override_created_at TIMESTAMPTZ,
    override_expires_at TIMESTAMPTZ,
    override_revoked_at TIMESTAMPTZ,
    next_probe_at TIMESTAMPTZ,
    probe_claimed_until TIMESTAMPTZ,
    probe_claimed_by VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, upstream_fingerprint, effective_model, contract_version),
    CONSTRAINT openai_native_compaction_capability_mode_check
        CHECK (mode IN ('auto', 'force_on', 'force_off')),
    CONSTRAINT openai_native_compaction_capability_source_check
        CHECK (source IN ('probe', 'trusted_official', 'manual_override')),
    CONSTRAINT openai_native_compaction_capability_override_check
        CHECK (
            (
                mode = 'auto'
                AND source IN ('probe', 'trusted_official')
                AND override_actor = ''
                AND override_reason = ''
                AND override_created_at IS NULL
                AND override_expires_at IS NULL
                AND override_revoked_at IS NULL
            ) OR
            (mode IN ('force_on', 'force_off') AND source = 'manual_override' AND override_created_at IS NOT NULL)
        )
);

CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_capabilities_account
    ON openai_native_compaction_capabilities(account_id);
CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_capabilities_probe_due
    ON openai_native_compaction_capabilities(next_probe_at, id)
    WHERE mode = 'auto';

CREATE TABLE IF NOT EXISTS openai_native_compaction_probe_results (
    id BIGSERIAL PRIMARY KEY,
    capability_id BIGINT REFERENCES openai_native_compaction_capabilities(id) ON DELETE SET NULL,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    upstream_fingerprint VARCHAR(128) NOT NULL,
    effective_model VARCHAR(512) NOT NULL,
    contract_version VARCHAR(64) NOT NULL,
    supported BOOLEAN,
    semantic_outcome VARCHAR(64) NOT NULL,
    status_code INTEGER,
    stale_identity BOOLEAN NOT NULL DEFAULT FALSE,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_probe_results_account_checked
    ON openai_native_compaction_probe_results(account_id, checked_at DESC);

COMMENT ON TABLE openai_native_compaction_capabilities IS
    'Candidate-specific native remote compaction v2 capability and override state';
COMMENT ON COLUMN openai_native_compaction_capabilities.upstream_fingerprint IS
    'Versioned opaque hash; never a raw upstream URL or credential';
COMMENT ON TABLE openai_native_compaction_probe_results IS
    'Payload-free audit of native compaction capability probes, including stale identity results';
