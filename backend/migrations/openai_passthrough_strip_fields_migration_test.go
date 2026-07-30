package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIPassthroughStripFieldsMigrationDefaultsMaxOutputTokens(t *testing.T) {
	content, err := FS.ReadFile("227_group_openai_passthrough_strip_fields.sql")
	require.NoError(t, err)

	sql := strings.ToLower(strings.Join(strings.Fields(string(content)), " "))
	require.Contains(t, sql, "add column if not exists openai_passthrough_strip_fields jsonb not null")
	require.Contains(t, sql, `default '["max_output_tokens"]'::jsonb`)
	require.Contains(t, sql, "comment on column groups.openai_passthrough_strip_fields")
}
