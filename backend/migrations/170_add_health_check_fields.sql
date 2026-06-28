-- 为 groups 表添加健康检查和熔断机制相关字段
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS health_check_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS health_check_interval_sec INT NOT NULL DEFAULT 60,
    ADD COLUMN IF NOT EXISTS health_check_timeout_sec INT NOT NULL DEFAULT 10,
    ADD COLUMN IF NOT EXISTS health_check_failure_threshold INT NOT NULL DEFAULT 3,
    ADD COLUMN IF NOT EXISTS health_check_success_threshold INT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS health_last_check_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS health_consecutive_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS health_consecutive_successes INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS health_status VARCHAR(20) NOT NULL DEFAULT 'unknown';

-- 为健康检查相关字段创建索引
CREATE INDEX IF NOT EXISTS idx_groups_health_check_enabled
    ON groups(health_check_enabled)
    WHERE deleted_at IS NULL AND health_check_enabled = true;

CREATE INDEX IF NOT EXISTS idx_groups_health_status
    ON groups(health_status)
    WHERE deleted_at IS NULL;

-- 添加字段注释
COMMENT ON COLUMN groups.health_check_enabled IS '是否启用健康检查（管理员手动控制）';
COMMENT ON COLUMN groups.health_check_interval_sec IS '健康检查间隔（秒），默认 60 秒';
COMMENT ON COLUMN groups.health_check_timeout_sec IS '单次健康检查超时时间（秒），默认 10 秒';
COMMENT ON COLUMN groups.health_check_failure_threshold IS '连续失败多少次后触发熔断（自动停用），默认 3 次';
COMMENT ON COLUMN groups.health_check_success_threshold IS '熔断后连续成功多少次后自动恢复（重新启用），默认 2 次';
COMMENT ON COLUMN groups.health_last_check_at IS '上次健康检查的时间戳';
COMMENT ON COLUMN groups.health_consecutive_failures IS '当前连续失败次数计数器';
COMMENT ON COLUMN groups.health_consecutive_successes IS '当前连续成功次数计数器';
COMMENT ON COLUMN groups.health_status IS '实时健康状态：unknown（未检查）、healthy（健康）、unhealthy（异常）';
