-- 既有风控表索引必须在迁移事务外并发构建。
-- 部分索引排除归属未知的历史记录，并减小索引体积。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_moderation_logs_billing_user_created_at
    ON content_moderation_logs(billing_user_id, created_at DESC)
    WHERE billing_user_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_moderation_logs_team_created_at
    ON content_moderation_logs(team_id, created_at DESC)
    WHERE team_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_moderation_cyber_warnings_billing_user_created_at
    ON content_moderation_cyber_warnings(billing_user_id, created_at DESC)
    WHERE billing_user_id IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_moderation_cyber_warnings_team_created_at
    ON content_moderation_cyber_warnings(team_id, created_at DESC)
    WHERE team_id IS NOT NULL;
