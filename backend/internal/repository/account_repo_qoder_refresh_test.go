package repository

import (
	"context"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryUpdateQoderOAuthCredentialsIfUnchangedUsesExactAttemptStateAndAtomicOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	applied, err := repo.UpdateQoderOAuthCredentialsIfUnchanged(
		context.Background(),
		42,
		map[string]any{"site": "cn", "refresh_token": "attempted", "_token_version": int64(9), "model_mapping": map[string]any{"custom": "qmodel"}},
		map[string]any{"site": "cn", "refresh_token": "rotated", "_token_version": int64(10), "model_mapping": map[string]any{"custom": "stale"}},
	)

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH updated AS")
	require.Contains(t, normalized, "credentials = (a.credentials - $6::text[]) || $1::jsonb")
	require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value)")
	require.Contains(t, normalized, "entry.key = ANY($6::text[])")
	require.Contains(t, normalized, ") = $5::jsonb")
	require.NotContains(t, normalized, "proxy_id")
	require.NotContains(t, normalized, "a.extra")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Len(t, exec.execArgs[0], 7)
	require.Equal(t, service.PlatformQoder, exec.execArgs[0][2])
	require.Equal(t, service.AccountTypeCosy, exec.execArgs[0][3])
	require.NotContains(t, exec.execArgs[0][0], "model_mapping")
	require.NotContains(t, exec.execArgs[0][4], "model_mapping")
}

func TestAccountRepositoryApplyQoderAuthorizationUsesProjectionMergeConditionalRestoreAndAtomicOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	tempUntil := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	rateLimitedAt := tempUntil.Add(-time.Minute)
	rateLimitResetAt := tempUntil.Add(time.Hour)

	applied, err := repo.ApplyQoderAuthorizationIfUnchanged(context.Background(), service.QoderAuthorizationUpdate{
		AccountID: 42,
		ExpectedCredentials: map[string]any{
			"site":           "cn",
			"pat":            "old-pat",
			"_token_version": int64(4),
			"model_mapping":  map[string]any{"alias": "current"},
		},
		Credentials: map[string]any{
			"site":           "cn",
			"pat":            "new-pat",
			"_token_version": int64(5),
			"model_mapping":  map[string]any{"alias": "stale"},
		},
		Extra:                           map[string]any{"account_uuid": "new-account"},
		ExpectedStatus:                  service.StatusError,
		ExpectedError:                   "old authorization failed",
		ExpectedSchedulable:             false,
		ExpectedTempUnschedulableUntil:  &tempUntil,
		ExpectedTempUnschedulableReason: "token refresh retry exhausted: old authorization",
		ExpectedRateLimitedAt:           &rateLimitedAt,
		ExpectedRateLimitResetAt:        &rateLimitResetAt,
		RestoreErrorState:               true,
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	require.Len(t, exec.execArgs, 1)
	require.Len(t, exec.execArgs[0], 18)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "credentials = (COALESCE(a.credentials, '{}'::jsonb) - $12::text[]) || $1::jsonb")
	require.Contains(t, normalized, "extra = (COALESCE(a.extra, '{}'::jsonb) || $2::jsonb) - $18::text[]")
	require.Contains(t, normalized, "a.status IS NOT DISTINCT FROM $4")
	require.Contains(t, normalized, "a.error_message IS NOT DISTINCT FROM $5")
	require.Contains(t, normalized, "a.schedulable IS NOT DISTINCT FROM $6")
	require.Contains(t, normalized, "a.temp_unschedulable_until IS NOT DISTINCT FROM $14::timestamptz")
	require.Contains(t, normalized, "COALESCE(a.temp_unschedulable_reason, '') = $15")
	require.Contains(t, normalized, "a.rate_limited_at IS NOT DISTINCT FROM $16::timestamptz")
	require.Contains(t, normalized, "a.rate_limit_reset_at IS NOT DISTINCT FROM $17::timestamptz")
	require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value)")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.NotContains(t, exec.execArgs[0][0], "model_mapping")
	require.NotContains(t, exec.execArgs[0][10], "model_mapping")
	require.Contains(t, exec.execArgs[0][1], "account_uuid")
	require.Equal(t, true, exec.execArgs[0][2])
	require.Equal(t, service.PlatformQoder, exec.execArgs[0][8])
	require.Equal(t, service.AccountTypeCosy, exec.execArgs[0][9])
	require.Equal(t, &tempUntil, exec.execArgs[0][13])
	require.Equal(t, "token refresh retry exhausted: old authorization", exec.execArgs[0][14])
	require.Equal(t, &rateLimitedAt, exec.execArgs[0][15])
	require.Equal(t, &rateLimitResetAt, exec.execArgs[0][16])
	require.Equal(t, pq.Array([]string{
		service.QoderQuotaSnapshotExtraKey,
		service.QoderQuotaUpdatedAtExtraKey,
	}), exec.execArgs[0][17])
}

func TestAccountRepositoryApplyQoderAuthorizationReportsCASMiss(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.ApplyQoderAuthorizationIfUnchanged(context.Background(), service.QoderAuthorizationUpdate{
		AccountID:           42,
		ExpectedCredentials: map[string]any{"site": "cn", "pat": "old"},
		Credentials:         map[string]any{"site": "cn", "pat": "new"},
	})

	require.NoError(t, err)
	require.False(t, applied)
}

func TestAccountRepositoryApplyQoderQuotaStateUsesAuthorizationCASAndAtomicOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	resetAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	applied, err := repo.ApplyQoderQuotaStateIfAuthorizationUnchanged(context.Background(), service.QoderQuotaStateUpdate{
		AccountID: 42,
		ExpectedCredentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "attempted-token",
			"_token_version":       int64(7),
			"model_mapping":        map[string]any{"alias": "route"},
		},
		Extra: map[string]any{
			service.QoderQuotaSnapshotExtraKey: map[string]any{"user_id": "current-user"},
		},
		SetRateLimitResetAt: &resetAt,
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	require.Len(t, exec.execArgs[0], 12)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "extra = COALESCE(a.extra, '{}'::jsonb) || $1::jsonb")
	require.Contains(t, normalized, "WHEN $2::boolean AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at < $3::timestamptz) THEN NOW()")
	require.Contains(t, normalized, "WHEN $2::boolean AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at < $3::timestamptz) THEN $3::timestamptz")
	require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value)")
	require.Contains(t, normalized, "entry.key = ANY($11::text[])")
	require.Contains(t, normalized, ") = $10::jsonb")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Contains(t, exec.execArgs[0][0], service.QoderQuotaSnapshotExtraKey)
	require.NotContains(t, exec.execArgs[0][9], "model_mapping")
	require.Contains(t, exec.execArgs[0][9], "attempted-token")
	require.Equal(t, true, exec.execArgs[0][1])
	require.Equal(t, &resetAt, exec.execArgs[0][2])
	require.Equal(t, service.PlatformQoder, exec.execArgs[0][7])
	require.Equal(t, service.AccountTypeCosy, exec.execArgs[0][8])
}

func TestAccountRepositoryApplyQoderGatewayStateUsesAuthorizationCASAndExtendsWindow(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	resetAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	applied, err := repo.ApplyQoderGatewayStateIfAuthorizationUnchanged(context.Background(), service.QoderGatewayStateUpdate{
		AccountID: 42,
		ExpectedCredentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "attempted-token",
			"_token_version":       int64(8),
			"model_mapping":        map[string]any{"alias": "route"},
		},
		RateLimitResetAt: &resetAt,
	})

	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	require.Len(t, exec.execArgs[0], 10)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at < $2::timestamptz")
	require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value)")
	require.Contains(t, normalized, "entry.key = ANY($9::text[])")
	require.Contains(t, normalized, ") = $8::jsonb")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Equal(t, true, exec.execArgs[0][0])
	require.Equal(t, &resetAt, exec.execArgs[0][1])
	require.Equal(t, service.PlatformQoder, exec.execArgs[0][5])
	require.Equal(t, service.AccountTypeCosy, exec.execArgs[0][6])
	require.Contains(t, exec.execArgs[0][7], "attempted-token")
	require.NotContains(t, exec.execArgs[0][7], "model_mapping")
}

func TestAccountRepositoryQoderRefreshFailureMutationsUseAttemptSnapshot(t *testing.T) {
	proxyID := int64(23)
	snapshot := service.QoderRefreshFailureSnapshot{
		Authorization: map[string]any{
			"site":          "cn",
			"refresh_token": "attempted",
			"model_mapping": map[string]any{"custom": "qmodel"},
		},
		ProxyID: &proxyID,
		TransportExtra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(7),
			"keep":                       "unrelated",
		},
	}
	tests := []struct {
		name   string
		mutate func(*accountRepository) (bool, error)
	}{
		{
			name: "permanent error",
			mutate: func(repo *accountRepository) (bool, error) {
				return repo.SetQoderOAuthRefreshErrorIfSnapshotUnchanged(
					context.Background(), 42, snapshot, "revoked",
				)
			},
		},
		{
			name: "temporary cooldown",
			mutate: func(repo *accountRepository) (bool, error) {
				return repo.SetQoderOAuthRefreshTempUnschedulableIfSnapshotUnchanged(
					context.Background(), 42, snapshot, time.Now().Add(time.Minute), "retry exhausted",
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
			repo := newAccountRepositoryWithSQL(nil, exec, nil)

			applied, err := tt.mutate(repo)

			require.NoError(t, err)
			require.True(t, applied)
			require.Len(t, exec.execQueries, 1)
			normalized := normalizeSQLWhitespace(exec.execQueries[0])
			require.Contains(t, normalized, "jsonb_object_agg(entry.key, entry.value)")
			require.Contains(t, normalized, "entry.key = ANY($9::text[])")
			require.Contains(t, normalized, ") = $7::jsonb")
			require.Contains(t, normalized, "proxy_id IS NOT DISTINCT FROM $8")
			require.Contains(t, normalized, "jsonb_each(COALESCE(a.extra, '{}'::jsonb))")
			require.Contains(t, normalized, "entry.key = ANY($11::text[])")
			require.Contains(t, normalized, ") = $10::jsonb")
			require.Contains(t, normalized, "SELECT $12, updated.id")
			require.Len(t, exec.execArgs[0], 12)
			require.Equal(t, service.PlatformQoder, exec.execArgs[0][3])
			require.Equal(t, service.AccountTypeCosy, exec.execArgs[0][4])
			require.NotContains(t, exec.execArgs[0][6], "model_mapping")
			require.Equal(t, &proxyID, exec.execArgs[0][7])
			require.Contains(t, exec.execArgs[0][9], "enable_tls_fingerprint")
			require.Contains(t, exec.execArgs[0][9], "tls_fingerprint_profile_id")
			require.NotContains(t, exec.execArgs[0][9], "keep")
		})
	}
}

func TestRejectStaleQoderAuthorizationUpdate(t *testing.T) {
	baseCredentials := map[string]any{
		"site":           "cn",
		"pat":            "pat-v5",
		"machine_id":     "machine-v5",
		"_token_version": int64(5),
		"model_mapping":  map[string]any{"local": "incoming"},
	}

	tests := []struct {
		name               string
		incoming           map[string]any
		currentCredentials string
		wantConflict       bool
	}{
		{
			name:               "same identity allows concurrent configuration drift",
			incoming:           baseCredentials,
			currentCredentials: `{"site":"cn","pat":"pat-v5","machine_id":"machine-v5","_token_version":5,"model_mapping":{"local":"current"}}`,
		},
		{
			name:               "newer persisted authorization rejects stale update",
			incoming:           baseCredentials,
			currentCredentials: `{"site":"cn","pat":"pat-v6","machine_id":"machine-v6","_token_version":6}`,
			wantConflict:       true,
		},
		{
			name: "strictly newer complete authorization may replace current identity",
			incoming: map[string]any{
				"site":           "cn",
				"pat":            "pat-v7",
				"machine_id":     "machine-v7",
				"_token_version": int64(7),
			},
			currentCredentials: `{"site":"cn","pat":"pat-v6","machine_id":"machine-v6","_token_version":6}`,
		},
		{
			name: "equal version cannot replace a different authorization",
			incoming: map[string]any{
				"site":           "cn",
				"pat":            "other-v6",
				"machine_id":     "other-machine-v6",
				"_token_version": int64(6),
			},
			currentCredentials: `{"site":"cn","pat":"pat-v6","machine_id":"machine-v6","_token_version":6}`,
			wantConflict:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &service.Account{
				Platform:    service.PlatformQoder,
				Type:        service.AccountTypeCosy,
				Credentials: tt.incoming,
			}

			err := rejectStaleQoderAuthorizationUpdate(account, []byte(tt.currentCredentials))

			if tt.wantConflict {
				require.ErrorIs(t, err, service.ErrQoderAuthorizationConflict)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
