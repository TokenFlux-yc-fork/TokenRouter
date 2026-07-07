-- Add OpenAI Codex backup pool auto-refill settings to groups.
ALTER TABLE groups
  ADD COLUMN IF NOT EXISTS backup_pool_group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS backup_pool_refill_threshold_points DOUBLE PRECISION NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_groups_backup_pool_group_id
  ON groups (backup_pool_group_id)
  WHERE deleted_at IS NULL AND backup_pool_group_id IS NOT NULL;

COMMENT ON COLUMN groups.backup_pool_group_id IS 'OpenAI Codex 备用号池分组 ID；目标分组容量不足时从该分组复制账号绑定';
COMMENT ON COLUMN groups.backup_pool_refill_threshold_points IS 'OpenAI Codex 备用号池自动补充阈值（容量点）；0 表示禁用';
