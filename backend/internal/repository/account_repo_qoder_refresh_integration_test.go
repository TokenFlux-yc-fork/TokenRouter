//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQoderRefreshCASPreservesConcurrentConfigurationInPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	current := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       int64(1),
		"model_mapping":        map[string]any{"alias": "current-route"},
		"model_whitelist":      []any{"alias"},
	}
	currentJSON, err := json.Marshal(current)
	require.NoError(t, err)
	initialExtraJSON, err := json.Marshal(map[string]any{
		"enable_tls_fingerprint":     true,
		"tls_fingerprint_profile_id": 3,
		"keep":                       "initial",
	})
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, credentials, extra, status, schedulable)
		VALUES ('qoder-refresh-cas', $1, $2, $3::jsonb, $4::jsonb, $5, TRUE)
		RETURNING id
	`, []any{service.PlatformQoder, service.AccountTypeCosy, currentJSON, initialExtraJSON, service.StatusActive}, &accountID))

	refreshed := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "new-access",
		"refresh_token":        "new-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       int64(2),
		"model_mapping":        map[string]any{"alias": "stale-route"},
	}
	var proxyID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO proxies (name, protocol, host, port, status)
		VALUES ('qoder-refresh-new-proxy', 'http', '127.0.0.1', 8080, 'active')
		RETURNING id
	`, nil, &proxyID))
	_, err = tx.ExecContext(ctx, `
		UPDATE accounts
		SET proxy_id = $1,
			extra = extra || '{"enable_tls_fingerprint":false,"tls_fingerprint_profile_id":9,"keep":"concurrent"}'::jsonb
		WHERE id = $2
	`, proxyID, accountID)
	require.NoError(t, err)

	applied, err := repo.UpdateQoderOAuthCredentialsIfUnchanged(ctx, accountID, current, refreshed)
	require.NoError(t, err)
	require.True(t, applied)

	var persistedJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT credentials FROM accounts WHERE id = $1`, []any{accountID}, &persistedJSON))
	var persisted map[string]any
	require.NoError(t, json.Unmarshal(persistedJSON, &persisted))
	require.Equal(t, "new-access", persisted["security_oauth_token"])
	require.Equal(t, "new-refresh", persisted["refresh_token"])
	require.Equal(t, map[string]any{"alias": "current-route"}, persisted["model_mapping"])
	require.Equal(t, []any{"alias"}, persisted["model_whitelist"])
	var persistedProxyID int64
	var persistedExtraJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT proxy_id, extra FROM accounts WHERE id = $1`, []any{accountID}, &persistedProxyID, &persistedExtraJSON))
	require.Equal(t, proxyID, persistedProxyID)
	var persistedExtra map[string]any
	require.NoError(t, json.Unmarshal(persistedExtraJSON, &persistedExtra))
	require.Equal(t, false, persistedExtra["enable_tls_fingerprint"])
	require.Equal(t, float64(9), persistedExtra["tls_fingerprint_profile_id"])
	require.Equal(t, "concurrent", persistedExtra["keep"])

	var outboxCount int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FROM scheduler_outbox
		WHERE account_id = $1 AND event_type = $2
	`, []any{accountID, service.SchedulerOutboxEventAccountChanged}, &outboxCount))
	require.Equal(t, 1, outboxCount)
}

func TestQoderRefreshFailureCASRejectsConcurrentTransportChangesInPostgres(t *testing.T) {
	type failureMutation struct {
		name  string
		apply func(*accountRepository, context.Context, int64, service.QoderRefreshFailureSnapshot) (bool, error)
	}
	mutations := []failureMutation{
		{
			name: "permanent error",
			apply: func(repo *accountRepository, ctx context.Context, id int64, snapshot service.QoderRefreshFailureSnapshot) (bool, error) {
				return repo.SetQoderOAuthRefreshErrorIfSnapshotUnchanged(ctx, id, snapshot, "revoked")
			},
		},
		{
			name: "temporary cooldown",
			apply: func(repo *accountRepository, ctx context.Context, id int64, snapshot service.QoderRefreshFailureSnapshot) (bool, error) {
				return repo.SetQoderOAuthRefreshTempUnschedulableIfSnapshotUnchanged(ctx, id, snapshot, time.Now().Add(time.Minute), "retry exhausted")
			},
		},
	}
	drifts := []struct {
		name        string
		wantApplied bool
	}{
		{name: "proxy"},
		{name: "TLS enable"},
		{name: "TLS profile"},
		{name: "unrelated configuration", wantApplied: true},
	}

	for _, mutation := range mutations {
		for _, drift := range drifts {
			t.Run(mutation.name+"/"+drift.name, func(t *testing.T) {
				ctx := context.Background()
				tx := testEntTx(t)
				repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
				credentials := map[string]any{
					"site":                 "cn",
					"refresh_mode":         "qodercn20",
					"security_oauth_token": "attempted-access",
					"refresh_token":        "attempted-refresh",
					"machine_id":           "attempted-machine",
					"uid":                  "attempted-user",
					"_token_version":       int64(1),
					"model_mapping":        map[string]any{"alias": "attempted"},
				}
				extra := map[string]any{
					"enable_tls_fingerprint":     true,
					"tls_fingerprint_profile_id": int64(17),
					"keep":                       "attempted",
				}
				credentialsJSON, err := json.Marshal(credentials)
				require.NoError(t, err)
				extraJSON, err := json.Marshal(extra)
				require.NoError(t, err)
				var accountID int64
				require.NoError(t, scanSingleRow(ctx, tx, `
					INSERT INTO accounts (name, platform, type, credentials, extra, status, schedulable)
					VALUES ('qoder-refresh-failure-cas', $1, $2, $3::jsonb, $4::jsonb, $5, TRUE)
					RETURNING id
				`, []any{service.PlatformQoder, service.AccountTypeCosy, credentialsJSON, extraJSON, service.StatusActive}, &accountID))

				snapshot := service.QoderRefreshFailureSnapshotForAccount(&service.Account{
					Credentials: credentials,
					Extra:       extra,
				})
				switch drift.name {
				case "proxy":
					var proxyID int64
					require.NoError(t, scanSingleRow(ctx, tx, `
						INSERT INTO proxies (name, protocol, host, port, status)
						VALUES ('qoder-refresh-failure-new-proxy', 'http', '127.0.0.1', 8080, 'active')
						RETURNING id
					`, nil, &proxyID))
					_, err = tx.ExecContext(ctx, `UPDATE accounts SET proxy_id = $1 WHERE id = $2`, proxyID, accountID)
				case "TLS enable":
					_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"enable_tls_fingerprint":false}'::jsonb WHERE id = $1`, accountID)
				case "TLS profile":
					_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra = extra || '{"tls_fingerprint_profile_id":18}'::jsonb WHERE id = $1`, accountID)
				case "unrelated configuration":
					_, err = tx.ExecContext(ctx, `
						UPDATE accounts
						SET credentials = credentials || '{"model_mapping":{"alias":"current"}}'::jsonb,
							extra = extra || '{"keep":"current"}'::jsonb
						WHERE id = $1
					`, accountID)
				}
				require.NoError(t, err)

				applied, err := mutation.apply(repo, ctx, accountID, snapshot)
				require.NoError(t, err)
				require.Equal(t, drift.wantApplied, applied)

				var status string
				var schedulable bool
				var errorMessage sql.NullString
				var tempUntil sql.NullTime
				require.NoError(t, scanSingleRow(ctx, tx, `
					SELECT status, schedulable, error_message, temp_unschedulable_until
					FROM accounts WHERE id = $1
				`, []any{accountID}, &status, &schedulable, &errorMessage, &tempUntil))
				if !drift.wantApplied {
					require.Equal(t, service.StatusActive, status)
					require.True(t, schedulable)
					require.Empty(t, errorMessage.String)
					require.False(t, tempUntil.Valid)
				} else if mutation.name == "permanent error" {
					require.Equal(t, service.StatusError, status)
					require.False(t, schedulable)
					require.Equal(t, "revoked", errorMessage.String)
					require.False(t, tempUntil.Valid)
				} else {
					require.Equal(t, service.StatusActive, status)
					require.True(t, schedulable)
					require.Empty(t, errorMessage.String)
					require.True(t, tempUntil.Valid)
				}

				var outboxCount int
				require.NoError(t, scanSingleRow(ctx, tx, `
					SELECT COUNT(*) FROM scheduler_outbox
					WHERE account_id = $1 AND event_type = $2
				`, []any{accountID, service.SchedulerOutboxEventAccountChanged}, &outboxCount))
				if drift.wantApplied {
					require.Equal(t, 1, outboxCount)
				} else {
					require.Zero(t, outboxCount)
				}
			})
		}
	}
}

func TestQoderAuthorizationGatewayStateRejectsStaleIdentityAndShorterWindowInPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	current := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "current-access",
		"refresh_token":        "current-refresh",
		"machine_id":           "current-machine",
		"uid":                  "current-user",
		"_token_version":       int64(2),
		"model_mapping":        map[string]any{"alias": "current-route"},
	}
	currentJSON, err := json.Marshal(current)
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, credentials, status, schedulable)
		VALUES ('qoder-gateway-state-cas', $1, $2, $3::jsonb, $4, TRUE)
		RETURNING id
	`, []any{service.PlatformQoder, service.AccountTypeCosy, currentJSON, service.StatusActive}, &accountID))

	laterReset := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	stale := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "stale-access",
		"refresh_token":        "stale-refresh",
		"machine_id":           "stale-machine",
		"uid":                  "stale-user",
		"_token_version":       int64(1),
	}
	applied, err := repo.ApplyQoderGatewayStateIfAuthorizationUnchanged(ctx, service.QoderGatewayStateUpdate{
		AccountID:           accountID,
		ExpectedCredentials: stale,
		RateLimitResetAt:    &laterReset,
	})
	require.NoError(t, err)
	require.False(t, applied)

	currentWithStaleConfiguration := make(map[string]any, len(current))
	for key, value := range current {
		currentWithStaleConfiguration[key] = value
	}
	currentWithStaleConfiguration["model_mapping"] = map[string]any{"alias": "stale-route"}
	applied, err = repo.ApplyQoderGatewayStateIfAuthorizationUnchanged(ctx, service.QoderGatewayStateUpdate{
		AccountID:           accountID,
		ExpectedCredentials: currentWithStaleConfiguration,
		RateLimitResetAt:    &laterReset,
	})
	require.NoError(t, err)
	require.True(t, applied)

	earlierReset := laterReset.Add(-30 * time.Minute)
	applied, err = repo.ApplyQoderGatewayStateIfAuthorizationUnchanged(ctx, service.QoderGatewayStateUpdate{
		AccountID:           accountID,
		ExpectedCredentials: current,
		RateLimitResetAt:    &earlierReset,
	})
	require.NoError(t, err)
	require.False(t, applied)

	applied, err = repo.ApplyQoderQuotaStateIfAuthorizationUnchanged(ctx, service.QoderQuotaStateUpdate{
		AccountID:           accountID,
		ExpectedCredentials: current,
		Extra: map[string]any{
			service.QoderQuotaSnapshotExtraKey: map[string]any{"user_id": "current-user"},
		},
		SetRateLimitResetAt: &earlierReset,
	})
	require.NoError(t, err)
	require.True(t, applied, "current quota snapshot should persist even when its window is shorter")

	var persistedReset time.Time
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT rate_limit_reset_at FROM accounts WHERE id = $1
	`, []any{accountID}, &persistedReset))
	require.Equal(t, laterReset, persistedReset)
	var outboxCount int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FROM scheduler_outbox
		WHERE account_id = $1 AND event_type = $2
	`, []any{accountID, service.SchedulerOutboxEventAccountChanged}, &outboxCount))
	require.Equal(t, 2, outboxCount)
}

func TestAccountRepositoryUpdateRejectsStaleQoderAuthorizationInPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	current := map[string]any{
		"site":           "cn",
		"pat":            "current-pat",
		"machine_id":     "current-machine",
		"_token_version": int64(6),
		"model_mapping":  map[string]any{"alias": "current-route"},
	}
	currentJSON, err := json.Marshal(current)
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO accounts (name, platform, type, credentials, status, schedulable)
		VALUES ('qoder-stale-update', $1, $2, $3::jsonb, $4, TRUE)
		RETURNING id
	`, []any{service.PlatformQoder, service.AccountTypeCosy, currentJSON, service.StatusActive}, &accountID))

	stale := &service.Account{
		ID:          accountID,
		Name:        "qoder-stale-update",
		Platform:    service.PlatformQoder,
		Type:        service.AccountTypeCosy,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    50,
		Credentials: map[string]any{
			"site":           "cn",
			"pat":            "stale-pat",
			"machine_id":     "stale-machine",
			"_token_version": int64(5),
			"model_mapping":  map[string]any{"alias": "stale-route"},
		},
	}

	err = repo.Update(ctx, stale)
	require.ErrorIs(t, err, service.ErrQoderAuthorizationConflict)

	var persistedJSON []byte
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT credentials FROM accounts WHERE id = $1`, []any{accountID}, &persistedJSON))
	var persisted map[string]any
	require.NoError(t, json.Unmarshal(persistedJSON, &persisted))
	require.Equal(t, "current-pat", persisted["pat"])
	require.Equal(t, float64(6), persisted["_token_version"])
}

func TestQoderAuthorizationApplyPreservesConcurrentConfigurationAndRestoresObservedErrorInPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	observedTempUntil := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	observedRateLimitedAt := observedTempUntil.Add(-time.Minute)
	observedRateResetAt := observedTempUntil.Add(time.Hour)
	expected := map[string]any{
		"site":           "cn",
		"pat":            "old-pat",
		"machine_id":     "old-machine",
		"_token_version": int64(7),
		"model_mapping":  map[string]any{"alias": "initial-route"},
		"data_policy":    "disagree",
	}
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	initialExtra, err := json.Marshal(map[string]any{
		"tls_fingerprint_profile_id":        3,
		"keep":                              "initial",
		service.QoderQuotaSnapshotExtraKey:  map[string]any{"user_id": "old-user"},
		service.QoderQuotaUpdatedAtExtraKey: "2026-07-01T00:00:00Z",
	})
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO accounts (
			name, platform, type, credentials, extra, status, error_message, schedulable,
			temp_unschedulable_until, temp_unschedulable_reason, rate_limited_at, rate_limit_reset_at
		)
		VALUES (
			'qoder-reauth-cas', $1, $2, $3::jsonb, $4::jsonb, $5, 'old authorization failed', FALSE,
			$6, 'token refresh retry exhausted: observed', $7, $8
		)
		RETURNING id
	`, []any{
		service.PlatformQoder, service.AccountTypeCosy, expectedJSON, initialExtra, service.StatusError,
		observedTempUntil, observedRateLimitedAt, observedRateResetAt,
	}, &accountID))

	concurrentCredentials := map[string]any{
		"site":            "cn",
		"pat":             "old-pat",
		"machine_id":      "old-machine",
		"_token_version":  int64(7),
		"model_mapping":   map[string]any{"alias": "concurrent-route"},
		"model_whitelist": []any{"alias"},
		"data_policy":     "disagree",
	}
	concurrentJSON, err := json.Marshal(concurrentCredentials)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `
		UPDATE accounts
		SET credentials = $1::jsonb,
			extra = extra || '{"tls_fingerprint_profile_id":9,"keep":"concurrent"}'::jsonb
		WHERE id = $2
	`, concurrentJSON, accountID)
	require.NoError(t, err)

	applied, err := repo.ApplyQoderAuthorizationIfUnchanged(ctx, service.QoderAuthorizationUpdate{
		AccountID:           accountID,
		ExpectedCredentials: expected,
		Credentials: map[string]any{
			"site":           "cn",
			"pat":            "new-pat",
			"machine_id":     "new-machine",
			"_token_version": int64(8),
			"model_mapping":  map[string]any{"alias": "stale-route"},
		},
		Extra:                           map[string]any{"account_uuid": "new-account"},
		ExpectedStatus:                  service.StatusError,
		ExpectedError:                   "old authorization failed",
		ExpectedSchedulable:             false,
		ExpectedTempUnschedulableUntil:  &observedTempUntil,
		ExpectedTempUnschedulableReason: "token refresh retry exhausted: observed",
		ExpectedRateLimitedAt:           &observedRateLimitedAt,
		ExpectedRateLimitResetAt:        &observedRateResetAt,
		RestoreErrorState:               true,
	})
	require.NoError(t, err)
	require.True(t, applied)

	var persistedCredentialsJSON, persistedExtraJSON []byte
	var status, errorMessage string
	var schedulable bool
	var tempUntil, rateLimitedAt, rateLimitResetAt sql.NullTime
	var tempReason sql.NullString
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT credentials, extra, status, error_message, schedulable,
			temp_unschedulable_until, temp_unschedulable_reason, rate_limited_at, rate_limit_reset_at
		FROM accounts WHERE id = $1
	`, []any{accountID},
		&persistedCredentialsJSON, &persistedExtraJSON, &status, &errorMessage, &schedulable,
		&tempUntil, &tempReason, &rateLimitedAt, &rateLimitResetAt,
	))
	var persistedCredentials, persistedExtra map[string]any
	require.NoError(t, json.Unmarshal(persistedCredentialsJSON, &persistedCredentials))
	require.NoError(t, json.Unmarshal(persistedExtraJSON, &persistedExtra))
	require.Equal(t, "new-pat", persistedCredentials["pat"])
	require.Equal(t, map[string]any{"alias": "concurrent-route"}, persistedCredentials["model_mapping"])
	require.Equal(t, []any{"alias"}, persistedCredentials["model_whitelist"])
	require.NotContains(t, persistedCredentials, "data_policy")
	require.Equal(t, float64(9), persistedExtra["tls_fingerprint_profile_id"])
	require.Equal(t, "concurrent", persistedExtra["keep"])
	require.Equal(t, "new-account", persistedExtra["account_uuid"])
	require.NotContains(t, persistedExtra, service.QoderQuotaSnapshotExtraKey)
	require.NotContains(t, persistedExtra, service.QoderQuotaUpdatedAtExtraKey)
	require.Equal(t, service.StatusActive, status)
	require.Empty(t, errorMessage)
	require.True(t, schedulable)
	require.False(t, tempUntil.Valid)
	require.False(t, tempReason.Valid)
	require.False(t, rateLimitedAt.Valid)
	require.False(t, rateLimitResetAt.Valid)

	var outboxCount int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FROM scheduler_outbox
		WHERE account_id = $1 AND event_type = $2
	`, []any{accountID, service.SchedulerOutboxEventAccountChanged}, &outboxCount))
	require.Equal(t, 1, outboxCount)
}

func TestQoderAuthorizationApplyPreservesConcurrentAdministrativeStateInPostgres(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	observedTempUntil := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	observedRateLimitedAt := observedTempUntil.Add(-time.Minute)
	observedRateResetAt := observedTempUntil.Add(time.Hour)
	concurrentTempUntil := observedTempUntil.Add(time.Hour)
	concurrentRateLimitedAt := observedRateLimitedAt.Add(time.Minute)
	concurrentRateResetAt := observedRateResetAt.Add(time.Hour)
	expected := map[string]any{"site": "cn", "pat": "old-pat", "_token_version": int64(2)}
	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	var accountID int64
	require.NoError(t, scanSingleRow(ctx, tx, `
		INSERT INTO accounts (
			name, platform, type, credentials, status, error_message, schedulable,
			temp_unschedulable_until, temp_unschedulable_reason, rate_limited_at, rate_limit_reset_at
		)
		VALUES (
			'qoder-reauth-state-cas', $1, $2, $3::jsonb, $4, 'old authorization failed', FALSE,
			$5, 'token refresh retry exhausted: observed', $6, $7
		)
		RETURNING id
	`, []any{
		service.PlatformQoder, service.AccountTypeCosy, expectedJSON, service.StatusError,
		observedTempUntil, observedRateLimitedAt, observedRateResetAt,
	}, &accountID))
	_, err = tx.ExecContext(ctx, `
		UPDATE accounts
		SET status = $1,
			error_message = 'disabled by administrator',
			schedulable = FALSE,
			temp_unschedulable_until = $2,
			temp_unschedulable_reason = 'token refresh retry exhausted: concurrent',
			rate_limited_at = $3,
			rate_limit_reset_at = $4
		WHERE id = $5
	`, service.StatusDisabled, concurrentTempUntil, concurrentRateLimitedAt, concurrentRateResetAt, accountID)
	require.NoError(t, err)

	applied, err := repo.ApplyQoderAuthorizationIfUnchanged(ctx, service.QoderAuthorizationUpdate{
		AccountID:                       accountID,
		ExpectedCredentials:             expected,
		Credentials:                     map[string]any{"site": "cn", "pat": "new-pat", "_token_version": int64(3)},
		ExpectedStatus:                  service.StatusError,
		ExpectedError:                   "old authorization failed",
		ExpectedSchedulable:             false,
		ExpectedTempUnschedulableUntil:  &observedTempUntil,
		ExpectedTempUnschedulableReason: "token refresh retry exhausted: observed",
		ExpectedRateLimitedAt:           &observedRateLimitedAt,
		ExpectedRateLimitResetAt:        &observedRateResetAt,
		RestoreErrorState:               true,
	})
	require.NoError(t, err)
	require.True(t, applied)

	var persistedPAT, status, errorMessage string
	var schedulable bool
	var tempUntil, rateLimitedAt, rateLimitResetAt time.Time
	var tempReason string
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT credentials->>'pat', status, error_message, schedulable,
			temp_unschedulable_until, temp_unschedulable_reason, rate_limited_at, rate_limit_reset_at
		FROM accounts WHERE id = $1
	`, []any{accountID},
		&persistedPAT, &status, &errorMessage, &schedulable,
		&tempUntil, &tempReason, &rateLimitedAt, &rateLimitResetAt,
	))
	require.Equal(t, "new-pat", persistedPAT)
	require.Equal(t, service.StatusDisabled, status)
	require.Equal(t, "disabled by administrator", errorMessage)
	require.False(t, schedulable)
	require.WithinDuration(t, concurrentTempUntil, tempUntil, time.Microsecond)
	require.Equal(t, "token refresh retry exhausted: concurrent", tempReason)
	require.WithinDuration(t, concurrentRateLimitedAt, rateLimitedAt, time.Microsecond)
	require.WithinDuration(t, concurrentRateResetAt, rateLimitResetAt, time.Microsecond)
}
