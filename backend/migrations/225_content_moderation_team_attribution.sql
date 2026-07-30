-- 为本地内容审计和 OpenAI Cyber 告警补充付款用户与团队归属。
-- 历史归属无法无歧义恢复时保持 NULL，避免在应用启动迁移中扫描和改写大表。
SET LOCAL lock_timeout = '1s';

ALTER TABLE content_moderation_logs
    ADD COLUMN IF NOT EXISTS billing_user_id BIGINT,
    ADD COLUMN IF NOT EXISTS team_id BIGINT;

ALTER TABLE content_moderation_cyber_warnings
    ADD COLUMN IF NOT EXISTS billing_user_id BIGINT,
    ADD COLUMN IF NOT EXISTS team_id BIGINT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_logs_billing_user_id_fkey') THEN
        ALTER TABLE content_moderation_logs
            ADD CONSTRAINT content_moderation_logs_billing_user_id_fkey
            FOREIGN KEY (billing_user_id) REFERENCES users(id) ON DELETE SET NULL NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_logs_team_id_fkey') THEN
        ALTER TABLE content_moderation_logs
            ADD CONSTRAINT content_moderation_logs_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_cyber_warnings_billing_user_id_fkey') THEN
        ALTER TABLE content_moderation_cyber_warnings
            ADD CONSTRAINT content_moderation_cyber_warnings_billing_user_id_fkey
            FOREIGN KEY (billing_user_id) REFERENCES users(id) ON DELETE SET NULL NOT VALID;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_moderation_cyber_warnings_team_id_fkey') THEN
        ALTER TABLE content_moderation_cyber_warnings
            ADD CONSTRAINT content_moderation_cyber_warnings_team_id_fkey
            FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL NOT VALID;
    END IF;
END $$;
