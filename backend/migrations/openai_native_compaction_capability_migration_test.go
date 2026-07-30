package migrations

import (
	"strings"
	"testing"
)

func TestOpenAINativeCompactionCapabilityMigrationContract(t *testing.T) {
	body, err := FS.ReadFile("227_openai_native_compaction_v2_capability.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(body))
	for _, fragment := range []string{
		"create table if not exists openai_native_compaction_capabilities",
		"unique (account_id, upstream_fingerprint, effective_model, contract_version)",
		"mode in ('auto', 'force_on', 'force_off')",
		"source in ('probe', 'trusted_official', 'manual_override')",
		"mode = 'auto'",
		"source in ('probe', 'trusted_official')",
		"override_actor = ''",
		"override_reason = ''",
		"override_created_at is null",
		"override_expires_at is null",
		"override_revoked_at is null",
		"source = 'manual_override' and override_created_at is not null",
		"override_expires_at timestamptz",
		"next_probe_at timestamptz",
		"where mode = 'auto'",
		"create table if not exists openai_native_compaction_probe_results",
		"stale_identity boolean not null default false",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"authorization", "encrypted_content", "api_key", "oauth_token", "request_payload", "response_payload"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration must not persist sensitive field %q", forbidden)
		}
	}
}
