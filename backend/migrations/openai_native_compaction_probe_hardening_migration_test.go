package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionProbeHardeningMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("230_harden_openai_native_compaction_probe_dispatch.sql")
	require.NoError(t, err)
	sql := string(content)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS authorization_principal_sha256 VARCHAR(64)",
		"ALTER COLUMN expires_at SET NOT NULL",
		"FOR UPDATE", // must remain absent below; kept out of expected fragments
		"WHERE state = 'reserved'",
	} {
		if fragment == "FOR UPDATE" {
			continue
		}
		require.Contains(t, sql, fragment)
	}
	require.Contains(t, sql, "^[0-9a-f]{64}$")
	require.Contains(t, sql, "idx_openai_native_compaction_probe_reservations_expired")
	lower := strings.ToLower(sql)
	require.NotContains(t, lower, "isolated_api_key_id")
	require.NotContains(t, lower, "encrypted_content")
	require.NotContains(t, lower, "request_body")
}
