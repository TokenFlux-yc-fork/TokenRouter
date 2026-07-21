package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

type qoderFailureCASRepo struct {
	AccountRepository
	account        *Account
	beforeErrorCAS func(*Account)
	beforeTempCAS  func(*Account)
	errorCASErr    error
	tempCASErr     error
	errorCalls     int
	tempCalls      int
}

func (r *qoderFailureCASRepo) SetQoderOAuthRefreshErrorIfSnapshotUnchanged(
	_ context.Context,
	id int64,
	snapshot QoderRefreshFailureSnapshot,
	errorMsg string,
) (bool, error) {
	r.errorCalls++
	if r.beforeErrorCAS != nil {
		r.beforeErrorCAS(r.account)
	}
	if r.errorCASErr != nil {
		return false, r.errorCASErr
	}
	if !qoderFailureCASMatches(r.account, id, snapshot) {
		return false, nil
	}
	r.account.Status = StatusError
	r.account.Schedulable = false
	r.account.ErrorMessage = errorMsg
	return true, nil
}

func (r *qoderFailureCASRepo) SetQoderOAuthRefreshTempUnschedulableIfSnapshotUnchanged(
	_ context.Context,
	id int64,
	snapshot QoderRefreshFailureSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	r.tempCalls++
	if r.beforeTempCAS != nil {
		r.beforeTempCAS(r.account)
	}
	if r.tempCASErr != nil {
		return false, r.tempCASErr
	}
	if !qoderFailureCASMatches(r.account, id, snapshot) {
		return false, nil
	}
	r.account.TempUnschedulableUntil = &until
	r.account.TempUnschedulableReason = reason
	return true, nil
}

func qoderFailureCASMatches(account *Account, id int64, snapshot QoderRefreshFailureSnapshot) bool {
	return account != nil && account.ID == id && account.IsQoderCosy() && account.Status == StatusActive &&
		reflect.DeepEqual(QoderCredentialIdentitySnapshot(account.Credentials), snapshot.Authorization) &&
		reflect.DeepEqual(account.ProxyID, snapshot.ProxyID) &&
		reflect.DeepEqual(QoderRefreshTransportExtraSnapshot(account.Extra), snapshot.TransportExtra)
}

type qoderFailureRefresher struct {
	err   error
	calls int
}

func (r *qoderFailureRefresher) CanRefresh(*Account) bool { return true }

func (r *qoderFailureRefresher) NeedsRefresh(*Account, time.Duration) bool { return true }

func (r *qoderFailureRefresher) Refresh(context.Context, *Account) (map[string]any, error) {
	r.calls++
	return nil, r.err
}

type qoderFailureCacheInvalidator struct {
	calls int
}

func (i *qoderFailureCacheInvalidator) InvalidateToken(context.Context, *Account) error {
	i.calls++
	return nil
}

type qoderFailureRuntimeBlocker struct {
	blockCalls int
}

func (b *qoderFailureRuntimeBlocker) BlockAccountScheduling(*Account, time.Time, string) {
	b.blockCalls++
}

func (*qoderFailureRuntimeBlocker) ClearAccountSchedulingBlock(int64) {}

func newQoderFailureAccount() *Account {
	proxyID := int64(41)
	return &Account{
		ID:          601,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		ProxyID:     &proxyID,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": int64(17),
			"keep":                       "attempted",
		},
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "attempted-access",
			"refresh_token":        "attempted-refresh",
			"machine_id":           "attempted-machine",
			"uid":                  "attempted-user",
			"_token_version":       int64(1),
			"model_mapping":        map[string]any{"old": "route"},
		},
	}
}

func qoderFailureTransportDriftCases() []struct {
	name   string
	mutate func(*Account)
} {
	return []struct {
		name   string
		mutate func(*Account)
	}{
		{
			name: "proxy drift",
			mutate: func(account *Account) {
				proxyID := int64(42)
				account.ProxyID = &proxyID
			},
		},
		{
			name: "TLS enable drift",
			mutate: func(account *Account) {
				account.Extra["enable_tls_fingerprint"] = false
			},
		},
		{
			name: "TLS profile drift",
			mutate: func(account *Account) {
				account.Extra["tls_fingerprint_profile_id"] = int64(18)
			},
		},
	}
}

func TestQoderRefreshFailureSnapshotDetachesAttemptState(t *testing.T) {
	account := newQoderFailureAccount()
	snapshot := QoderRefreshFailureSnapshotForAccount(account)

	account.Credentials["refresh_token"] = "changed-refresh"
	account.Extra["enable_tls_fingerprint"] = false
	account.Extra["tls_fingerprint_profile_id"] = int64(99)
	account.Extra["keep"] = "changed"
	*account.ProxyID = 99

	require.Equal(t, "attempted-refresh", snapshot.Authorization["refresh_token"])
	require.Equal(t, map[string]any{
		"enable_tls_fingerprint":     true,
		"tls_fingerprint_profile_id": int64(17),
	}, snapshot.TransportExtra)
	require.Equal(t, int64(41), *snapshot.ProxyID)
	require.NotContains(t, snapshot.Authorization, "model_mapping")
	require.NotContains(t, snapshot.TransportExtra, "keep")
}

func TestSnapshotOAuthRefreshAccountDetachesExtra(t *testing.T) {
	account := newQoderFailureAccount()
	snapshot := snapshotOAuthRefreshAccount(account)

	account.Extra["enable_tls_fingerprint"] = false
	account.Extra["tls_fingerprint_profile_id"] = int64(99)
	account.Extra["keep"] = "changed"

	require.Equal(t, true, snapshot.Extra["enable_tls_fingerprint"])
	require.Equal(t, int64(17), snapshot.Extra["tls_fingerprint_profile_id"])
	require.Equal(t, "attempted", snapshot.Extra["keep"])
}

func newQoderFailureRefreshService(
	repo *qoderFailureCASRepo,
	invalidator *qoderFailureCacheInvalidator,
	blocker *qoderFailureRuntimeBlocker,
) *TokenRefreshService {
	service := &TokenRefreshService{
		accountRepo:      repo,
		refreshPolicy:    DefaultBackgroundRefreshPolicy(),
		cfg:              &config.TokenRefreshConfig{MaxRetries: 1},
		cacheInvalidator: invalidator,
	}
	service.SetAccountRuntimeBlocker(blocker)
	return service
}

func replaceQoderFailureIdentityWithPAT(account *Account) {
	account.Credentials = map[string]any{
		"site":           "cn",
		"pat":            "reauthorized-pat",
		"machine_id":     "reauthorized-machine",
		"_token_version": int64(2),
		"model_mapping":  map[string]any{"current": "route"},
	}
}

func TestQoderBackgroundNonRetryableFailureCAS(t *testing.T) {
	upstreamErr := errors.New("invalid_grant: attempted refresh token was revoked")

	t.Run("shared provider configuration skips account mutation", func(t *testing.T) {
		account := newQoderFailureAccount()
		repo := &qoderFailureCASRepo{account: account}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)
		sharedErr := &qoder.OpenAPIError{
			Operation:  "token refresh",
			StatusCode: 400,
			Message:    "invalid_scope",
		}

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: sharedErr}, nil, time.Hour)

		var providerErr *providerConfigurationRefreshError
		require.ErrorAs(t, err, &providerErr)
		require.ErrorIs(t, err, sharedErr)
		require.Zero(t, repo.errorCalls)
		require.Equal(t, StatusActive, account.Status)
		require.True(t, account.Schedulable)
		require.Zero(t, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})

	t.Run("config drift still applies", func(t *testing.T) {
		account := newQoderFailureAccount()
		repo := &qoderFailureCASRepo{account: account}
		repo.beforeErrorCAS = func(account *Account) {
			account.Credentials["model_mapping"] = map[string]any{"current": "route"}
			account.Extra["keep"] = "current"
		}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		var permanentErr *accountPermanentRefreshError
		require.ErrorAs(t, err, &permanentErr)
		require.ErrorIs(t, err, upstreamErr)
		require.True(t, permanentErr.persistentlyBlocked)
		require.Equal(t, 1, repo.errorCalls)
		require.Equal(t, StatusError, account.Status)
		require.False(t, account.Schedulable)
		require.Equal(t, map[string]any{"current": "route"}, account.Credentials["model_mapping"])
		require.Equal(t, "current", account.Extra["keep"])
		require.Equal(t, 1, blocker.blockCalls)
		require.Equal(t, 1, invalidator.calls)
	})

	for _, tc := range qoderFailureTransportDriftCases() {
		t.Run(tc.name+" skips side effects", func(t *testing.T) {
			account := newQoderFailureAccount()
			repo := &qoderFailureCASRepo{account: account, beforeErrorCAS: tc.mutate}
			invalidator := &qoderFailureCacheInvalidator{}
			blocker := &qoderFailureRuntimeBlocker{}
			service := newQoderFailureRefreshService(repo, invalidator, blocker)

			err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

			require.ErrorIs(t, err, errRefreshSkipped)
			require.Equal(t, 1, repo.errorCalls)
			require.Equal(t, StatusActive, account.Status)
			require.True(t, account.Schedulable)
			require.Zero(t, blocker.blockCalls)
			require.Zero(t, invalidator.calls)
		})
	}

	t.Run("identity drift skips side effects", func(t *testing.T) {
		account := newQoderFailureAccount()
		repo := &qoderFailureCASRepo{account: account, beforeErrorCAS: replaceQoderFailureIdentityWithPAT}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		require.ErrorIs(t, err, errRefreshSkipped)
		require.Equal(t, 1, repo.errorCalls)
		require.Equal(t, StatusActive, account.Status)
		require.True(t, account.Schedulable)
		require.Equal(t, "reauthorized-pat", account.GetCredential("pat"))
		require.Zero(t, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})

	t.Run("repository error contains provider cycle", func(t *testing.T) {
		account := newQoderFailureAccount()
		casErr := errors.New("qoder error CAS unavailable")
		repo := &qoderFailureCASRepo{account: account, errorCASErr: casErr}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		var containmentErr *providerCycleContainmentRefreshError
		require.ErrorAs(t, err, &containmentErr)
		require.ErrorIs(t, err, casErr)
		require.NotErrorIs(t, err, upstreamErr)
		require.Equal(t, StatusActive, account.Status)
		require.True(t, account.Schedulable)
		require.Zero(t, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})
}

func TestQoderBackgroundRetryExhaustedFailureCAS(t *testing.T) {
	upstreamErr := errors.New("temporary qoder refresh transport failure")

	t.Run("config drift still applies", func(t *testing.T) {
		account := newQoderFailureAccount()
		repo := &qoderFailureCASRepo{account: account}
		repo.beforeTempCAS = func(account *Account) {
			account.Credentials["model_mapping"] = map[string]any{"current": "route"}
			account.Extra["keep"] = "current"
		}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		require.ErrorIs(t, err, upstreamErr)
		require.Equal(t, 1, repo.tempCalls)
		require.NotNil(t, account.TempUnschedulableUntil)
		require.Contains(t, account.TempUnschedulableReason, upstreamErr.Error())
		require.Equal(t, map[string]any{"current": "route"}, account.Credentials["model_mapping"])
		require.Equal(t, "current", account.Extra["keep"])
		require.Equal(t, 1, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})

	for _, tc := range qoderFailureTransportDriftCases() {
		t.Run(tc.name+" skips side effects", func(t *testing.T) {
			account := newQoderFailureAccount()
			repo := &qoderFailureCASRepo{account: account, beforeTempCAS: tc.mutate}
			invalidator := &qoderFailureCacheInvalidator{}
			blocker := &qoderFailureRuntimeBlocker{}
			service := newQoderFailureRefreshService(repo, invalidator, blocker)

			err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

			require.ErrorIs(t, err, errRefreshSkipped)
			require.Equal(t, 1, repo.tempCalls)
			require.Nil(t, account.TempUnschedulableUntil)
			require.Empty(t, account.TempUnschedulableReason)
			require.Equal(t, StatusActive, account.Status)
			require.True(t, account.Schedulable)
			require.Zero(t, blocker.blockCalls)
			require.Zero(t, invalidator.calls)
		})
	}

	t.Run("identity drift skips side effects", func(t *testing.T) {
		account := newQoderFailureAccount()
		repo := &qoderFailureCASRepo{account: account, beforeTempCAS: replaceQoderFailureIdentityWithPAT}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		require.ErrorIs(t, err, errRefreshSkipped)
		require.Equal(t, 1, repo.tempCalls)
		require.Nil(t, account.TempUnschedulableUntil)
		require.Empty(t, account.TempUnschedulableReason)
		require.Equal(t, "reauthorized-pat", account.GetCredential("pat"))
		require.Zero(t, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})

	t.Run("repository error contains provider cycle", func(t *testing.T) {
		account := newQoderFailureAccount()
		casErr := errors.New("qoder cooldown CAS unavailable")
		repo := &qoderFailureCASRepo{account: account, tempCASErr: casErr}
		invalidator := &qoderFailureCacheInvalidator{}
		blocker := &qoderFailureRuntimeBlocker{}
		service := newQoderFailureRefreshService(repo, invalidator, blocker)

		err := service.refreshWithRetry(context.Background(), account, &qoderFailureRefresher{err: upstreamErr}, nil, time.Hour)

		var containmentErr *providerCycleContainmentRefreshError
		require.ErrorAs(t, err, &containmentErr)
		require.ErrorIs(t, err, casErr)
		require.NotErrorIs(t, err, upstreamErr)
		require.Nil(t, account.TempUnschedulableUntil)
		require.Zero(t, blocker.blockCalls)
		require.Zero(t, invalidator.calls)
	})
}
