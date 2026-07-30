package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompositeAPIKeyMigrationConstraints(t *testing.T) {
	content, err := FS.ReadFile("226_add_composite_api_keys.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	// 普通 Key 不需要回填，新增列必须具有关闭复合模式的稳定默认值。
	require.Contains(t, sql, "is_composite boolean not null default false")

	// 删除 Key 或分组时映射必须自动清理，避免残留不可达的鉴权配置。
	require.Contains(t, sql, "api_key_id bigint not null references api_keys(id) on delete cascade")
	require.Contains(t, sql, "group_id bigint not null references groups(id) on delete cascade")

	// 同一 Key 的分组与规范化前缀都必须唯一，数据库负责兜底并发写入。
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, group_id)")
	require.Contains(t, sql, "on api_key_composite_groups(api_key_id, normalized_prefix)")
	require.Contains(t, sql, "on api_key_composite_groups(group_id)")

	// 复合 Key 不允许同时保留普通 Key 的单分组绑定。
	require.Contains(t, sql, "check (not is_composite or group_id is null)")

	// 异步批量图片持久化实际分组与客户端模型，但不在首次 DDL 中回填或扫描历史大表。
	normalized := strings.Join(strings.Fields(sql), " ")
	require.Contains(t, normalized, "set local lock_timeout = '1s'")
	require.Contains(t, sql, "add column if not exists group_id bigint")
	require.Contains(t, sql, "add column if not exists requested_model varchar(512)")
	require.Contains(t, sql, "alter column requested_model set default ''")
	require.NotContains(t, normalized, "update batch_image_jobs")
	require.NotContains(t, normalized, "alter column requested_model set not null")
	require.Contains(t, sql, "batch_image_jobs_group_id_fkey")
	require.Contains(t, sql, "foreign key (group_id) references groups(id) on delete set null")
	require.Contains(t, sql, "not valid")
	require.NotContains(t, normalized, "create index if not exists idx_batch_image_jobs_group_id")

	indexContent, err := FS.ReadFile("226a_composite_api_key_existing_table_indexes_notx.sql")
	require.NoError(t, err)
	indexSQL := strings.ToLower(string(indexContent))
	require.Contains(t, indexSQL, "create index concurrently if not exists idx_batch_image_jobs_group_id")
}
