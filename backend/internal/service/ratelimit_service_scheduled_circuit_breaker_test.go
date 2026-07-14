package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type scheduledCircuitBreakerAccountRepo struct {
	AccountRepository
	account *Account
	calls   []scheduledCircuitBreakerCall
}

type scheduledCircuitBreakerCall struct {
	accountID int64
	until     time.Time
	reason    string
}

func (r *scheduledCircuitBreakerAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	if r.account == nil {
		return nil, errors.New("account not found")
	}
	return r.account, nil
}

func (r *scheduledCircuitBreakerAccountRepo) SetTempUnschedulable(_ context.Context, accountID int64, until time.Time, reason string) error {
	r.calls = append(r.calls, scheduledCircuitBreakerCall{accountID: accountID, until: until, reason: reason})
	return nil
}

type scheduledCircuitBreakerPlanReader struct {
	plans []*ScheduledTestPlan
}

func (r *scheduledCircuitBreakerPlanReader) ListByAccountID(context.Context, int64) ([]*ScheduledTestPlan, error) {
	return r.plans, nil
}

func newScheduledCircuitBreakerPoolAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}
}

func TestPassiveAccountCircuitBreakerSkipsAccountWithScheduledCircuitBreaker(t *testing.T) {
	account := newScheduledCircuitBreakerPoolAccount(501)
	repo := &scheduledCircuitBreakerAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	svc.SetScheduledTestPlanReader(&scheduledCircuitBreakerPlanReader{plans: []*ScheduledTestPlan{{
		AccountID:                    account.ID,
		Enabled:                      true,
		AccountCircuitBreakerEnabled: true,
	}}})

	svc.recordPassiveAccountFailure(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		[]byte(`{"error":{"message":"upstream overloaded"}}`),
	)

	require.Empty(t, repo.calls)
}

func TestPassiveAccountCircuitBreakerRecordsTransportFailure(t *testing.T) {
	account := newScheduledCircuitBreakerPoolAccount(502)
	repo := &scheduledCircuitBreakerAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	svc.RecordUpstreamRequestFailure(context.Background(), account, errors.New("upstream connection reset"))

	require.Len(t, repo.calls, 1)
	require.Equal(t, account.ID, repo.calls[0].accountID)
	require.Contains(t, repo.calls[0].reason, "passive_account_circuit_breaker")
	require.NotNil(t, account.TempUnschedulableUntil)
}

func TestPassiveAccountCircuitBreakerSkipsCanceledTransportFailure(t *testing.T) {
	account := newScheduledCircuitBreakerPoolAccount(505)
	repo := &scheduledCircuitBreakerAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	svc.RecordUpstreamRequestFailure(
		context.Background(),
		account,
		fmt.Errorf("upstream request failed: %w", context.Canceled),
	)

	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

func TestPassiveAccountCircuitBreakerKeepsPoolHTTPFailuresOptIn(t *testing.T) {
	account := newScheduledCircuitBreakerPoolAccount(503)
	repo := &scheduledCircuitBreakerAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)

	require.False(t, svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"upstream overloaded"}}`),
	))
	require.Empty(t, repo.calls)
}

func TestSetScheduledTestTempUnschedulablePersistsTaggedState(t *testing.T) {
	account := newScheduledCircuitBreakerPoolAccount(504)
	repo := &scheduledCircuitBreakerAccountRepo{account: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	until := time.Now().Add(10 * time.Minute)

	require.NoError(t, svc.SetScheduledTestTempUnschedulable(context.Background(), account.ID, until, "three failed probes"))

	require.Len(t, repo.calls, 1)
	require.Contains(t, repo.calls[0].reason, "scheduled_test_circuit_breaker")
	require.Equal(t, until, *account.TempUnschedulableUntil)
}
