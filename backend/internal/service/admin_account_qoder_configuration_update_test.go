package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qoderConfigurationUpdateRepoStub struct {
	AccountRepository
	account  *Account
	expected *Account
	updated  *Account
	err      error
}

func (r *qoderConfigurationUpdateRepoStub) GetByID(context.Context, int64) (*Account, error) {
	if r.account == nil {
		return nil, ErrAccountNotFound
	}
	clone := *r.account
	clone.Credentials = shallowCopyMap(r.account.Credentials)
	clone.Extra = shallowCopyMap(r.account.Extra)
	return &clone, nil
}

func (r *qoderConfigurationUpdateRepoStub) UpdateQoderConfigurationIfRuntimeUnchanged(_ context.Context, account, expected *Account) error {
	expectedClone := *expected
	r.expected = &expectedClone
	if r.err != nil {
		return r.err
	}
	updatedClone := *account
	updatedClone.Credentials = shallowCopyMap(account.Credentials)
	updatedClone.Extra = shallowCopyMap(account.Extra)
	r.updated = &updatedClone
	r.account = &updatedClone
	return nil
}

func TestUpdateQoderConfigurationUsesRuntimeCAS(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	rateReset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	repo := &qoderConfigurationUpdateRepoStub{account: &Account{
		ID:               71,
		Name:             "before",
		Platform:         PlatformQoder,
		Type:             AccountTypeCosy,
		Status:           StatusActive,
		Schedulable:      true,
		RateLimitResetAt: &rateReset,
		Credentials:      map[string]any{"site": "cn", "pat": "pat-token", "machine_id": "machine"},
		Extra:            map[string]any{"keep": "runtime"},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 71, &UpdateAccountInput{Name: "after"})

	require.NoError(t, err)
	require.NotNil(t, repo.expected)
	require.NotNil(t, repo.updated)
	require.Equal(t, "before", repo.expected.Name)
	require.Equal(t, rateReset, *repo.expected.RateLimitResetAt)
	require.Equal(t, "after", updated.Name)
}

func TestUpdateQoderConfigurationPropagatesRuntimeConflict(t *testing.T) {
	withSuccessfulQoderPATValidation(t)
	repo := &qoderConfigurationUpdateRepoStub{
		account: &Account{
			ID:          72,
			Name:        "before",
			Platform:    PlatformQoder,
			Type:        AccountTypeCosy,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{"site": "cn", "pat": "pat-token", "machine_id": "machine"},
		},
		err: ErrQoderAccountUpdateConflict,
	}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 72, &UpdateAccountInput{Name: "after"})

	require.Nil(t, updated)
	require.ErrorIs(t, err, ErrQoderAccountUpdateConflict)
	require.Equal(t, "before", repo.account.Name)
}
