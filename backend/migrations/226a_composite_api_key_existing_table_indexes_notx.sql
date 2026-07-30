-- batch_image_jobs 是既有历史表，分组索引必须在迁移事务外并发构建。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_batch_image_jobs_group_id
    ON batch_image_jobs(group_id)
    WHERE group_id IS NOT NULL;
