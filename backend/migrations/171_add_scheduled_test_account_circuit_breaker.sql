-- 171: Add account-level circuit breaker settings to scheduled test plans

ALTER TABLE scheduled_test_plans
    ADD COLUMN IF NOT EXISTS account_circuit_breaker_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS failure_threshold INT NOT NULL DEFAULT 3,
    ADD COLUMN IF NOT EXISTS success_threshold INT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS failure_cooldown_minutes INT NOT NULL DEFAULT 5,
    ADD COLUMN IF NOT EXISTS timeout_seconds INT NOT NULL DEFAULT 30;

COMMENT ON COLUMN scheduled_test_plans.account_circuit_breaker_enabled IS 'Whether this scheduled test can temporarily stop scheduling the target account';
COMMENT ON COLUMN scheduled_test_plans.failure_threshold IS 'Consecutive failed probes required to mark the account temporarily unschedulable';
COMMENT ON COLUMN scheduled_test_plans.success_threshold IS 'Consecutive successful probes required to clear the temporary unschedulable state';
COMMENT ON COLUMN scheduled_test_plans.failure_cooldown_minutes IS 'Temporary unschedulable duration after the failure threshold is reached';
COMMENT ON COLUMN scheduled_test_plans.timeout_seconds IS 'Per-probe timeout; timeout is recorded as a failed probe';
