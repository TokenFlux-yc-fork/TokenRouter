-- 为 OpenAI native remote compaction v2 probe 增加独立运营费用账本。
-- 228 预留给 upstream attempt usage；本迁移不触碰客户结算、余额或 usage 表。
SET LOCAL lock_timeout = '1s';

CREATE TABLE IF NOT EXISTS openai_native_compaction_probe_budgets (
    budget_day DATE PRIMARY KEY,
    reserved_micro_usd BIGINT NOT NULL DEFAULT 0,
    committed_micro_usd BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_native_compaction_probe_budgets_nonnegative_check
        CHECK (reserved_micro_usd >= 0 AND committed_micro_usd >= 0)
);

CREATE TABLE IF NOT EXISTS openai_native_compaction_probe_budget_reservations (
    reservation_id UUID PRIMARY KEY,
    budget_day DATE NOT NULL REFERENCES openai_native_compaction_probe_budgets(budget_day) ON DELETE RESTRICT,
    amount_micro_usd BIGINT NOT NULL,
    state VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ,
    CONSTRAINT openai_native_compaction_probe_budget_reservation_amount_check
        CHECK (amount_micro_usd > 0),
    CONSTRAINT openai_native_compaction_probe_budget_reservation_state_check
        CHECK (state IN ('reserved', 'committed', 'released')),
    CONSTRAINT openai_native_compaction_probe_budget_reservation_settlement_check
        CHECK (
            (state = 'reserved' AND settled_at IS NULL) OR
            (state IN ('committed', 'released') AND settled_at IS NOT NULL)
        ),
    CONSTRAINT openai_native_compaction_probe_budget_reservation_dispatch_check
        CHECK (
            (state = 'released' AND dispatched_at IS NULL) OR
            (state = 'committed' AND dispatched_at IS NOT NULL) OR
            state = 'reserved'
        )
);

CREATE INDEX IF NOT EXISTS idx_openai_native_compaction_probe_budget_reservations_day
    ON openai_native_compaction_probe_budget_reservations(budget_day, created_at);

COMMENT ON TABLE openai_native_compaction_probe_budgets IS
    'UTC daily operator-cost ledger for native compaction probes; never customer settlement';
COMMENT ON TABLE openai_native_compaction_probe_budget_reservations IS
    'Idempotent payload-free probe cost reservations in integer micro-USD';
