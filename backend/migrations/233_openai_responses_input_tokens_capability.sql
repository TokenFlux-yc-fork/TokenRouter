-- Exact candidate-specific passive capability state for /v1/responses/input_tokens.
SET LOCAL lock_timeout = '1s';

CREATE TABLE IF NOT EXISTS openai_responses_input_tokens_capabilities (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    upstream_fingerprint VARCHAR(128) NOT NULL,
    effective_model VARCHAR(512) NOT NULL,
    contract_version VARCHAR(64) NOT NULL,
    config_generation VARCHAR(128) NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'unknown',
    checked_at TIMESTAMPTZ,
    last_status INTEGER,
    last_outcome VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (account_id, upstream_fingerprint, effective_model, contract_version, config_generation),
    CONSTRAINT openai_responses_input_tokens_capability_state_check
        CHECK (state IN ('unknown', 'supported', 'unsupported', 'scope_denied'))
);

CREATE INDEX IF NOT EXISTS idx_openai_responses_input_tokens_capabilities_account
    ON openai_responses_input_tokens_capabilities(account_id);
CREATE INDEX IF NOT EXISTS idx_openai_responses_input_tokens_capabilities_checked
    ON openai_responses_input_tokens_capabilities(account_id, checked_at DESC);

COMMENT ON TABLE openai_responses_input_tokens_capabilities IS
    'Exact account/upstream/model/contract passive capability state for Responses input_tokens';
COMMENT ON COLUMN openai_responses_input_tokens_capabilities.upstream_fingerprint IS
    'Versioned opaque hash; never a raw upstream URL or credential';
