package migrations

import (
	"strings"
	"testing"
)

func TestOpenAINativeCompactionOverrideHardeningMigrationContract(t *testing.T) {
	body, err := FS.ReadFile("231_harden_openai_native_compaction_manual_overrides.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := strings.ToLower(string(body))
	for _, fragment := range []string{
		"create table if not exists openai_native_compaction_override_audits",
		"action in ('set', 'revoke')",
		"mode in ('force_on', 'force_off')",
		"btrim(override_actor) <> ''",
		"btrim(override_reason) <> ''",
		"override_expires_at > override_created_at",
		"action = 'revoke' and override_revoked_at is not null",
		"create index if not exists idx_openai_native_compaction_override_audits_exact",
		"drop constraint if exists openai_native_compaction_capability_override_check",
		"override_expires_at is not null",
		"override_revoked_at is null",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"encrypted_content", "api_key", "oauth_token", "request_payload", "response_payload"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration must not persist sensitive field %q", forbidden)
		}
	}
}
