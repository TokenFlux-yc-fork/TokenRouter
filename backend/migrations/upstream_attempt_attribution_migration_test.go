package migrations

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAttemptAttributionMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("228_upstream_attempt_attribution.sql")
	require.NoError(t, err)
	sql := string(content)

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS upstream_attempt_attributions",
		"attempt_id VARCHAR(128) PRIMARY KEY",
		"client_request_id VARCHAR(128) NOT NULL",
		"gateway_request_id VARCHAR(128) NOT NULL",
		"upstream_request_id VARCHAR(128)",
		"upstream_response_id VARCHAR(128)",
		"ws_connection_id VARCHAR(128)",
		"ws_turn_id VARCHAR(128)",
		"upstream_provider VARCHAR(64) NOT NULL",
		"upstream_fingerprint VARCHAR(128) NOT NULL",
		"usage_observed BOOLEAN NOT NULL DEFAULT FALSE",
		"cost_usd DECIMAL(20, 10)",
		"state_version BIGINT NOT NULL DEFAULT 1",
		"transport_observed BOOLEAN NOT NULL DEFAULT FALSE",
		"http_observed BOOLEAN NOT NULL DEFAULT FALSE",
		"semantic_observed BOOLEAN NOT NULL DEFAULT FALSE",
		"delivery_observed BOOLEAN NOT NULL DEFAULT FALSE",
		"delivery_committed BOOLEAN NOT NULL DEFAULT FALSE",
		"safe_to_failover BOOLEAN NOT NULL DEFAULT FALSE",
		"NOT (delivery_committed AND safe_to_failover)",
		"NULL means unknown",
	} {
		require.Contains(t, sql, fragment)
	}

	lower := strings.ToLower(sql)
	for _, forbiddenColumn := range []string{"payload", "encrypted_content", "authorization", "api_key", "access_token", "refresh_token", "raw_url", "base_url"} {
		columnDeclaration := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(forbiddenColumn) + `\s+`)
		require.False(t, columnDeclaration.MatchString(lower), "migration must not declare sensitive column %q", forbiddenColumn)
	}
}
