-- Durable, content-free attribution for each real upstream attempt.
-- This ledger is telemetry only: it never settles customer usage or mutates balances.
SET LOCAL lock_timeout = '1s';

CREATE TABLE IF NOT EXISTS upstream_attempt_attributions (
    attempt_id VARCHAR(128) PRIMARY KEY,
    client_request_id VARCHAR(128) NOT NULL,
    gateway_request_id VARCHAR(128) NOT NULL,
    upstream_request_id VARCHAR(128),
    upstream_response_id VARCHAR(128),
    ws_connection_id VARCHAR(128),
    ws_turn_id VARCHAR(128),
    account_id BIGINT NOT NULL,
    transport VARCHAR(16) NOT NULL,
    upstream_provider VARCHAR(64) NOT NULL,
    upstream_fingerprint VARCHAR(128) NOT NULL,
    effective_model VARCHAR(512) NOT NULL,
    contract_version VARCHAR(64) NOT NULL,
    state VARCHAR(16) NOT NULL,
    state_version BIGINT NOT NULL DEFAULT 1,
    transport_observed BOOLEAN NOT NULL DEFAULT FALSE,
    http_observed BOOLEAN NOT NULL DEFAULT FALSE,
    http_status INTEGER,
    semantic_observed BOOLEAN NOT NULL DEFAULT FALSE,
    semantic_outcome VARCHAR(64) NOT NULL DEFAULT '',
    output_item_done_count BIGINT NOT NULL DEFAULT 0,
    compaction_item_count BIGINT NOT NULL DEFAULT 0,
    malformed_item_count BIGINT NOT NULL DEFAULT 0,
    terminal_event VARCHAR(128) NOT NULL DEFAULT '',
    terminal_count BIGINT NOT NULL DEFAULT 0,
    successful_terminal BOOLEAN NOT NULL DEFAULT FALSE,
    usage_observed BOOLEAN NOT NULL DEFAULT FALSE,
    input_tokens BIGINT,
    output_tokens BIGINT,
    cache_creation_input_tokens BIGINT,
    cache_read_input_tokens BIGINT,
    image_input_tokens BIGINT,
    image_output_tokens BIGINT,
    cost_usd DECIMAL(20, 10),
    delivery_observed BOOLEAN NOT NULL DEFAULT FALSE,
    delivery_committed BOOLEAN NOT NULL DEFAULT FALSE,
    safe_to_failover BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT upstream_attempt_attributions_transport_check
        CHECK (transport IN ('http', 'websocket', 'probe')),
    CONSTRAINT upstream_attempt_attributions_state_check
        CHECK (state IN ('started', 'in_progress', 'terminal')),
    CONSTRAINT upstream_attempt_attributions_state_version_check CHECK (state_version > 0),
    CONSTRAINT upstream_attempt_attributions_http_observed_check CHECK (
        (http_observed AND transport_observed AND http_status BETWEEN 100 AND 999) OR
        (NOT http_observed AND http_status IS NULL)
    ),
    CONSTRAINT upstream_attempt_attributions_semantic_observed_check CHECK (
        semantic_observed OR (
            semantic_outcome = '' AND output_item_done_count = 0 AND compaction_item_count = 0 AND
            malformed_item_count = 0 AND terminal_event = '' AND terminal_count = 0 AND
            successful_terminal = FALSE
        )
    ),
    CONSTRAINT upstream_attempt_attributions_delivery_observed_check CHECK (
        delivery_observed OR delivery_committed = FALSE
    ),
    CONSTRAINT upstream_attempt_attributions_counters_check CHECK (
        output_item_done_count >= 0 AND compaction_item_count >= 0 AND
        malformed_item_count >= 0 AND terminal_count >= 0
    ),
    CONSTRAINT upstream_attempt_attributions_tokens_check CHECK (
        (input_tokens IS NULL OR input_tokens >= 0) AND
        (output_tokens IS NULL OR output_tokens >= 0) AND
        (cache_creation_input_tokens IS NULL OR cache_creation_input_tokens >= 0) AND
        (cache_read_input_tokens IS NULL OR cache_read_input_tokens >= 0) AND
        (image_input_tokens IS NULL OR image_input_tokens >= 0) AND
        (image_output_tokens IS NULL OR image_output_tokens >= 0) AND
        (cost_usd IS NULL OR cost_usd >= 0)
    ),
    CONSTRAINT upstream_attempt_attributions_usage_observed_check CHECK (
        (usage_observed AND (
            input_tokens IS NOT NULL OR output_tokens IS NOT NULL OR
            cache_creation_input_tokens IS NOT NULL OR cache_read_input_tokens IS NOT NULL OR
            image_input_tokens IS NOT NULL OR image_output_tokens IS NOT NULL OR cost_usd IS NOT NULL
        )) OR (
            NOT usage_observed AND input_tokens IS NULL AND output_tokens IS NULL AND
            cache_creation_input_tokens IS NULL AND cache_read_input_tokens IS NULL AND
            image_input_tokens IS NULL AND image_output_tokens IS NULL AND cost_usd IS NULL
        )
    ),
    CONSTRAINT upstream_attempt_attributions_terminal_check CHECK (
        (state = 'terminal' AND completed_at IS NOT NULL) OR
        (state <> 'terminal' AND completed_at IS NULL AND successful_terminal = FALSE)
    ),
    CONSTRAINT upstream_attempt_attributions_failover_check
        CHECK (NOT (delivery_committed AND safe_to_failover)),
    CONSTRAINT upstream_attempt_attributions_time_check
        CHECK (completed_at IS NULL OR completed_at >= started_at)
);

CREATE INDEX IF NOT EXISTS idx_upstream_attempt_attributions_gateway_request
    ON upstream_attempt_attributions(gateway_request_id, started_at, attempt_id);
CREATE INDEX IF NOT EXISTS idx_upstream_attempt_attributions_client_request
    ON upstream_attempt_attributions(client_request_id, started_at, attempt_id);
CREATE INDEX IF NOT EXISTS idx_upstream_attempt_attributions_account_started
    ON upstream_attempt_attributions(account_id, started_at DESC);

COMMENT ON TABLE upstream_attempt_attributions IS
    'Content-free per-upstream-attempt telemetry; not a customer billing ledger';
COMMENT ON COLUMN upstream_attempt_attributions.upstream_fingerprint IS
    'Sanitized compatibility-domain fingerprint; never a raw or sensitive URL';
COMMENT ON COLUMN upstream_attempt_attributions.cost_usd IS
    'Observed upstream cost; NULL means unknown and must not be interpreted as zero';
