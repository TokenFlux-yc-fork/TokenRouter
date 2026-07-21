package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

type qoderAuthorizationRepoStub struct {
	AccountRepository
	account     *Account
	beforeApply func(*Account)
	applyErr    error
	lastUpdate  *QoderAuthorizationUpdate
}

func (r *qoderAuthorizationRepoStub) GetByID(context.Context, int64) (*Account, error) {
	if r.account == nil {
		return nil, ErrAccountNotFound
	}
	clone := *r.account
	clone.Credentials = shallowCopyMap(r.account.Credentials)
	clone.Extra = shallowCopyMap(r.account.Extra)
	return &clone, nil
}

func (r *qoderAuthorizationRepoStub) ApplyQoderAuthorizationIfUnchanged(_ context.Context, update QoderAuthorizationUpdate) (bool, error) {
	copyUpdate := update
	r.lastUpdate = &copyUpdate
	if r.applyErr != nil {
		return false, r.applyErr
	}
	if r.beforeApply != nil {
		r.beforeApply(r.account)
	}
	if !reflect.DeepEqual(
		QoderCredentialIdentitySnapshot(r.account.Credentials),
		QoderCredentialIdentitySnapshot(update.ExpectedCredentials),
	) {
		return false, nil
	}

	credentials := shallowCopyMap(r.account.Credentials)
	for _, key := range QoderCredentialIdentityKeys() {
		delete(credentials, key)
	}
	for key, value := range QoderCredentialIdentitySnapshot(update.Credentials) {
		credentials[key] = value
	}
	r.account.Credentials = credentials
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range update.Extra {
		r.account.Extra[key] = value
	}
	delete(r.account.Extra, QoderQuotaSnapshotExtraKey)
	delete(r.account.Extra, QoderQuotaUpdatedAtExtraKey)
	if update.RestoreErrorState &&
		r.account.Status == update.ExpectedStatus &&
		r.account.ErrorMessage == update.ExpectedError &&
		r.account.Schedulable == update.ExpectedSchedulable {
		r.account.Status = StatusActive
		r.account.ErrorMessage = ""
		r.account.Schedulable = true
	}
	if reflect.DeepEqual(r.account.TempUnschedulableUntil, update.ExpectedTempUnschedulableUntil) &&
		r.account.TempUnschedulableReason == update.ExpectedTempUnschedulableReason {
		r.account.TempUnschedulableUntil = nil
		r.account.TempUnschedulableReason = ""
	}
	if reflect.DeepEqual(r.account.RateLimitedAt, update.ExpectedRateLimitedAt) &&
		reflect.DeepEqual(r.account.RateLimitResetAt, update.ExpectedRateLimitResetAt) {
		r.account.RateLimitedAt = nil
		r.account.RateLimitResetAt = nil
	}
	return true, nil
}

func withSuccessfulQoderPATValidation(t *testing.T) {
	t.Helper()
	previous := qoderValidateCNPAT
	qoderValidateCNPAT = func(context.Context, *Account, string, *qoder.MachineIdentity, qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		return &qoder.AuthIdentity{UID: "validated-user", SecurityOauthToken: "validated-token"}, nil
	}
	t.Cleanup(func() { qoderValidateCNPAT = previous })
}

func TestApplyQoderAuthorizationPreservesConcurrentConfigurationAndRestoresObservedError(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	tempUntil := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)
	rateLimitedAt := tempUntil.Add(-time.Minute)
	rateLimitResetAt := tempUntil.Add(time.Hour)
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:                      41,
		Platform:                PlatformQoder,
		Type:                    AccountTypeCosy,
		Status:                  StatusError,
		ErrorMessage:            "old authorization failed",
		Schedulable:             false,
		TempUnschedulableUntil:  &tempUntil,
		TempUnschedulableReason: "token refresh retry exhausted: old authorization",
		RateLimitedAt:           &rateLimitedAt,
		RateLimitResetAt:        &rateLimitResetAt,
		Credentials: map[string]any{
			"site":           "cn",
			"pat":            "old-pat",
			"machine_id":     "old-machine",
			"_token_version": int64(5),
			"model_mapping":  map[string]any{"alias": "initial-route"},
			"data_policy":    "disagree",
		},
		Extra: map[string]any{
			"tls_fingerprint_profile_id": int64(3),
			"keep":                       "initial",
			QoderQuotaSnapshotExtraKey:   map[string]any{"user_id": "old-user"},
			QoderQuotaUpdatedAtExtraKey:  "2026-07-01T00:00:00Z",
		},
	}}
	repo.beforeApply = func(account *Account) {
		account.Credentials["model_mapping"] = map[string]any{"alias": "concurrent-route"}
		account.Extra["tls_fingerprint_profile_id"] = int64(9)
		account.Extra["keep"] = "concurrent"
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.ApplyQoderAuthorization(context.Background(), 41, map[string]any{
		"site":          "cn",
		"pat":           "new-pat",
		"model_mapping": map[string]any{"alias": "stale-route"},
		"data_policy":   "agree",
	}, map[string]any{"account_uuid": "new-account"})

	require.NoError(t, err)
	require.NotNil(t, repo.lastUpdate)
	require.Equal(t, "new-pat", updated.GetCredential("pat"))
	require.Equal(t, int64(6), updated.GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"alias": "concurrent-route"}, updated.Credentials["model_mapping"])
	require.Equal(t, "agree", updated.GetCredential("data_policy"))
	require.Equal(t, int64(9), updated.Extra["tls_fingerprint_profile_id"])
	require.Equal(t, "concurrent", updated.Extra["keep"])
	require.Equal(t, "new-account", updated.Extra["account_uuid"])
	require.NotContains(t, updated.Extra, QoderQuotaSnapshotExtraKey)
	require.NotContains(t, updated.Extra, QoderQuotaUpdatedAtExtraKey)
	require.Equal(t, StatusActive, updated.Status)
	require.Empty(t, updated.ErrorMessage)
	require.True(t, updated.Schedulable)
	require.Nil(t, updated.TempUnschedulableUntil)
	require.Empty(t, updated.TempUnschedulableReason)
	require.Nil(t, updated.RateLimitedAt)
	require.Nil(t, updated.RateLimitResetAt)
}

func TestApplyQoderAuthorizationPreservesConcurrentRuntimeBlocks(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	observedTempUntil := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	observedRateLimitedAt := observedTempUntil.Add(-time.Minute)
	observedRateResetAt := observedTempUntil.Add(time.Hour)
	concurrentTempUntil := observedTempUntil.Add(time.Hour)
	concurrentRateLimitedAt := observedRateLimitedAt.Add(time.Minute)
	concurrentRateResetAt := observedRateResetAt.Add(time.Hour)
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:                      45,
		Platform:                PlatformQoder,
		Type:                    AccountTypeCosy,
		Status:                  StatusActive,
		Schedulable:             true,
		TempUnschedulableUntil:  &observedTempUntil,
		TempUnschedulableReason: "token refresh retry exhausted: observed",
		RateLimitedAt:           &observedRateLimitedAt,
		RateLimitResetAt:        &observedRateResetAt,
		Credentials:             map[string]any{"site": "cn", "pat": "old-pat", "_token_version": int64(2)},
	}}
	repo.beforeApply = func(account *Account) {
		account.TempUnschedulableUntil = &concurrentTempUntil
		account.TempUnschedulableReason = "token refresh retry exhausted: concurrent"
		account.RateLimitedAt = &concurrentRateLimitedAt
		account.RateLimitResetAt = &concurrentRateResetAt
	}

	updated, err := (&adminServiceImpl{accountRepo: repo}).ApplyQoderAuthorization(
		context.Background(), 45, map[string]any{"site": "cn", "pat": "new-pat"}, nil,
	)

	require.NoError(t, err)
	require.Equal(t, concurrentTempUntil, *updated.TempUnschedulableUntil)
	require.Equal(t, "token refresh retry exhausted: concurrent", updated.TempUnschedulableReason)
	require.Equal(t, concurrentRateLimitedAt, *updated.RateLimitedAt)
	require.Equal(t, concurrentRateResetAt, *updated.RateLimitResetAt)
}

func TestApplyQoderAuthorizationPreservesConcurrentAdministrativeState(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:           42,
		Platform:     PlatformQoder,
		Type:         AccountTypeCosy,
		Status:       StatusError,
		ErrorMessage: "old authorization failed",
		Credentials:  map[string]any{"site": "cn", "pat": "old-pat", "_token_version": int64(2)},
	}}
	repo.beforeApply = func(account *Account) {
		account.Status = StatusDisabled
		account.ErrorMessage = "disabled by administrator"
		account.Schedulable = false
	}

	updated, err := (&adminServiceImpl{accountRepo: repo}).ApplyQoderAuthorization(
		context.Background(), 42, map[string]any{"site": "cn", "pat": "new-pat"}, nil,
	)

	require.NoError(t, err)
	require.Equal(t, "new-pat", updated.GetCredential("pat"))
	require.Equal(t, StatusDisabled, updated.Status)
	require.Equal(t, "disabled by administrator", updated.ErrorMessage)
	require.False(t, updated.Schedulable)
}

func TestApplyQoderAuthorizationRejectsConcurrentAuthorization(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:          43,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"site": "cn", "pat": "old-pat", "_token_version": int64(2)},
	}}
	repo.beforeApply = func(account *Account) {
		account.Credentials["pat"] = "competing-pat"
		account.Credentials["_token_version"] = int64(3)
	}

	updated, err := (&adminServiceImpl{accountRepo: repo}).ApplyQoderAuthorization(
		context.Background(), 43, map[string]any{"site": "cn", "pat": "new-pat"}, nil,
	)

	require.Nil(t, updated)
	require.ErrorIs(t, err, ErrQoderAuthorizationConflict)
	require.Equal(t, "competing-pat", repo.account.GetCredential("pat"))
}

func TestApplyQoderAuthorizationPropagatesAtomicRepositoryFailure(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	repoErr := errors.New("atomic outbox write failed")
	repo := &qoderAuthorizationRepoStub{
		account: &Account{
			ID:          44,
			Platform:    PlatformQoder,
			Type:        AccountTypeCosy,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"site": "cn", "pat": "old-pat"},
		},
		applyErr: repoErr,
	}

	updated, err := (&adminServiceImpl{accountRepo: repo}).ApplyQoderAuthorization(
		context.Background(), 44, map[string]any{"site": "cn", "pat": "new-pat"}, nil,
	)

	require.Nil(t, updated)
	require.ErrorIs(t, err, repoErr)
	require.Equal(t, "old-pat", repo.account.GetCredential("pat"))
}

func TestUpdateAccountRejectsQoderAuthorizationIdentityMutation(t *testing.T) {
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:          45,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"site": "cn", "pat": "old-pat", "model_mapping": map[string]any{"alias": "route"}},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 45, &UpdateAccountInput{
		Credentials: map[string]any{"site": "cn", "pat": "new-pat"},
	})

	require.Nil(t, updated)
	require.ErrorIs(t, err, errQoderAuthorizationEndpointRequired)
	require.Equal(t, "old-pat", repo.account.GetCredential("pat"))
}
