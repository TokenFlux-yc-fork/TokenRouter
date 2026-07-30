package service

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type nativeCompactionProbeRunnerAccountRepo struct {
	mu          sync.Mutex
	accounts    map[int64]*Account
	group       []Account
	getCalls    int
	listCalls   int
	listEntered chan struct{}
	listRelease chan struct{}
}

func (r *nativeCompactionProbeRunnerAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCalls++
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	copy := *account
	return &copy, nil
}

func (r *nativeCompactionProbeRunnerAccountRepo) ListByGroup(ctx context.Context, _ int64) ([]Account, error) {
	r.mu.Lock()
	r.listCalls++
	entered := r.listEntered
	release := r.listRelease
	accounts := append([]Account(nil), r.group...)
	r.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return accounts, nil
}

type nativeCompactionProbeRunnerAPIKeyRepo struct {
	mu    sync.Mutex
	key   *APIKey
	keys  []*APIKey
	err   error
	calls int
}

type nativeCompactionProbeRunnerUserRepo struct {
	user *User
	err  error
}

func (r *nativeCompactionProbeRunnerUserRepo) GetByID(_ context.Context, _ int64) (*User, error) {
	return r.user, r.err
}

type nativeCompactionProbeRunnerGroupRepo struct {
	group *Group
	err   error
}

func (r *nativeCompactionProbeRunnerGroupRepo) GetByID(_ context.Context, _ int64) (*Group, error) {
	return r.group, r.err
}

func (r *nativeCompactionProbeRunnerAPIKeyRepo) GetByID(_ context.Context, _ int64) (*APIKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	if len(r.keys) > 0 {
		index := r.calls - 1
		if index >= len(r.keys) {
			index = len(r.keys) - 1
		}
		return r.keys[index], nil
	}
	return r.key, nil
}

type nativeCompactionProbeRunnerCapabilityRepo struct {
	mu        sync.Mutex
	ensured   []OpenAINativeCompactionCapabilityKey
	claims    []OpenAINativeCompactionProbeClaim
	persisted []OpenAINativeCompactionProbeResult
	sequence  []string
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) GetExact(context.Context, OpenAINativeCompactionCapabilityKey) (*OpenAINativeCompactionCapabilityRecord, error) {
	return nil, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) UpsertTrustedOfficial(context.Context, *Account, OpenAINativeCompactionCapabilityKey) (bool, error) {
	return false, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) EnsureAutoCandidate(_ context.Context, key OpenAINativeCompactionCapabilityKey, _ time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensured = append(r.ensured, key)
	r.sequence = append(r.sequence, "ensure")
	return true, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) UpsertProbeResult(_ context.Context, result OpenAINativeCompactionProbeResult) (OpenAINativeCompactionProbeWriteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persisted = append(r.persisted, result)
	r.sequence = append(r.sequence, "persist")
	return OpenAINativeCompactionProbeWriteResult{CanonicalUpdated: true}, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) SetQuarantine(context.Context, OpenAINativeCompactionCapabilityKey, *time.Time, string) (bool, error) {
	return false, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) ClaimDue(context.Context, time.Time, time.Time, string, int) ([]OpenAINativeCompactionProbeClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence = append(r.sequence, "claim")
	return append([]OpenAINativeCompactionProbeClaim(nil), r.claims...), nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) FenceProbeDispatch(context.Context, OpenAINativeCompactionProbeClaim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence = append(r.sequence, "fence")
	return nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) UpsertManualOverride(context.Context, OpenAINativeCompactionManualOverride) error {
	return nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) RevokeManualOverride(context.Context, OpenAINativeCompactionCapabilityKey, time.Time, time.Time) (bool, error) {
	return false, nil
}

func (r *nativeCompactionProbeRunnerCapabilityRepo) ListAudit(context.Context, OpenAINativeCompactionCapabilityKey, int) ([]OpenAINativeCompactionProbeAudit, error) {
	return nil, nil
}

type nativeCompactionProbeRunnerExecutor struct {
	mu      sync.Mutex
	calls   int
	account *Account
	model   string
	options OpenAINativeCompactionProbeOptions
	result  OpenAINativeCompactionProbeAttemptResult
	err     error
	entered chan struct{}
	release chan struct{}
}

func (e *nativeCompactionProbeRunnerExecutor) RunOpenAINativeCompactionV2Probe(
	ctx context.Context,
	account *Account,
	model string,
	options OpenAINativeCompactionProbeOptions,
) (OpenAINativeCompactionProbeAttemptResult, error) {
	e.mu.Lock()
	e.calls++
	e.account = account
	e.model = model
	e.options = options
	entered := e.entered
	release := e.release
	result := e.result
	err := e.err
	e.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	return result, err
}

func TestOpenAINativeCompactionProbeRunnerDisabledDoesNotStartOrClaim(t *testing.T) {
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{}
	apiKeyRepo := &nativeCompactionProbeRunnerAPIKeyRepo{}
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		apiKeyRepo,
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		&nativeCompactionProbeRunnerExecutor{},
		config.GatewayOpenAINativeCompactionProbeConfig{},
	)

	runner.Start()
	require.NoError(t, runner.RunDue(context.Background()))
	runner.Stop()
	runner.Stop()
	require.Zero(t, accountRepo.listCalls)
	require.Zero(t, apiKeyRepo.calls)
	require.Empty(t, capabilityRepo.sequence)
}

func TestOpenAINativeCompactionProbeRunnerEnrollsMappedExactKeyBeforeClaim(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	account := nativeCompactionProbeRunnerAccount(91, 7)
	account.Credentials["base_url"] = "https://custom.example/tenant/v1"
	account.Credentials["model_mapping"] = map[string]any{"client-model": "gpt-test"}
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{group: []Account{*account}}
	apiKeyRepo := &nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 7)}
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		apiKeyRepo,
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		&nativeCompactionProbeRunnerExecutor{},
		nativeCompactionProbeRunnerConfig("client-model", "gpt-test"),
	)
	runner.now = func() time.Time { return now }

	require.NoError(t, runner.RunDue(context.Background()))
	require.Len(t, capabilityRepo.ensured, 1)
	require.Equal(t, "gpt-test", capabilityRepo.ensured[0].EffectiveModel)
	require.Equal(t, []string{"ensure", "claim"}, capabilityRepo.sequence)
}

func TestOpenAINativeCompactionProbeRunnerMappedModelMustRemainAllowlisted(t *testing.T) {
	account := nativeCompactionProbeRunnerAccount(91, 7)
	account.Credentials["model_mapping"] = map[string]any{"client-model": "not-allowed"}
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{group: []Account{*account}}
	apiKeyRepo := &nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 7)}
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		apiKeyRepo,
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		&nativeCompactionProbeRunnerExecutor{},
		nativeCompactionProbeRunnerConfig("client-model"),
	)

	require.NoError(t, runner.RunDue(context.Background()))
	require.Empty(t, capabilityRepo.ensured)
	require.Equal(t, []string{"claim"}, capabilityRepo.sequence)
}

func TestOpenAINativeCompactionProbeRunnerRejectsIsolatedIdentityMismatch(t *testing.T) {
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{}
	apiKeyRepo := &nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 8)}
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		apiKeyRepo,
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		&nativeCompactionProbeRunnerExecutor{},
		nativeCompactionProbeRunnerConfig("gpt-test"),
	)

	require.ErrorIs(t, runner.RunDue(context.Background()), ErrOpenAINativeCompactionProbeIdentityInvalid)
	require.Zero(t, accountRepo.listCalls)
	require.Empty(t, capabilityRepo.sequence)
}

func TestOpenAINativeCompactionProbeRunnerReloadsClaimAndPersistsRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	account := nativeCompactionProbeRunnerAccount(91, 7)
	key, err := ResolveOpenAINativeCompactionProbeKey(account, "gpt-test")
	require.NoError(t, err)
	retryAfter := now.Add(20 * time.Minute)
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{claims: []OpenAINativeCompactionProbeClaim{{
		Capability: OpenAINativeCompactionCapabilityRecord{
			OpenAINativeCompactionCapability: OpenAINativeCompactionCapability{Key: key},
		},
		ClaimToken:      "worker:claim",
		AccountRevision: "revision",
	}}}
	executor := &nativeCompactionProbeRunnerExecutor{result: OpenAINativeCompactionProbeAttemptResult{
		Key:             key,
		SemanticOutcome: OpenAINativeCompactionHTTPFailure,
		CheckedAt:       now,
		RetryAfterUntil: &retryAfter,
	}}
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{
		accounts: map[int64]*Account{account.ID: account},
		group:    []Account{*account},
	}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		&nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 7)},
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		executor,
		nativeCompactionProbeRunnerConfig("gpt-test"),
	)
	runner.now = func() time.Time { return now }
	runner.jitter = func(delay time.Duration, _ float64) time.Duration { return delay }

	require.NoError(t, runner.RunDue(context.Background()))
	require.Equal(t, 1, accountRepo.getCalls)
	require.Equal(t, 1, executor.calls)
	require.Len(t, capabilityRepo.persisted, 1)
	require.NotNil(t, capabilityRepo.persisted[0].NextProbeAt)
	require.Equal(t, retryAfter, *capabilityRepo.persisted[0].NextProbeAt)
	require.Nil(t, capabilityRepo.persisted[0].Supported)
}

func TestOpenAINativeCompactionProbeRunnerReloadsIsolatedAPIKeyBeforeDispatch(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	account := nativeCompactionProbeRunnerAccount(91, 7)
	key, err := ResolveOpenAINativeCompactionProbeKey(account, "gpt-test")
	require.NoError(t, err)
	capabilityRepo := &nativeCompactionProbeRunnerCapabilityRepo{claims: []OpenAINativeCompactionProbeClaim{{
		Capability: OpenAINativeCompactionCapabilityRecord{
			OpenAINativeCompactionCapability: OpenAINativeCompactionCapability{Key: key},
		},
		ClaimToken:      "worker:claim",
		AccountRevision: "revision",
	}}}
	validKey := nativeCompactionProbeRunnerAPIKey(9, 7)
	invalidKey := nativeCompactionProbeRunnerAPIKey(9, 8)
	apiKeyRepo := &nativeCompactionProbeRunnerAPIKeyRepo{keys: []*APIKey{validKey, invalidKey}}
	executor := &nativeCompactionProbeRunnerExecutor{}
	runner := newOpenAINativeCompactionProbeRunnerService(
		&nativeCompactionProbeRunnerAccountRepo{
			accounts: map[int64]*Account{account.ID: account},
			group:    []Account{*account},
		},
		apiKeyRepo,
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		capabilityRepo,
		executor,
		nativeCompactionProbeRunnerConfig("gpt-test"),
	)
	runner.now = func() time.Time { return now }
	runner.jitter = func(delay time.Duration, _ float64) time.Duration { return delay }

	require.NoError(t, runner.RunDue(context.Background()))
	require.Equal(t, 2, apiKeyRepo.calls)
	require.Zero(t, executor.calls)
	require.Len(t, capabilityRepo.persisted, 1)
	require.Nil(t, capabilityRepo.persisted[0].Supported)
	require.Equal(t, OpenAINativeCompactionTransportFailure, capabilityRepo.persisted[0].SemanticOutcome)
}

func TestOpenAINativeCompactionProbeRunnerRunDueSerializesCycles(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{
		listEntered: entered,
		listRelease: release,
	}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		&nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 7)},
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		&nativeCompactionProbeRunnerCapabilityRepo{},
		&nativeCompactionProbeRunnerExecutor{},
		nativeCompactionProbeRunnerConfig("gpt-test"),
	)

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- runner.RunDue(context.Background()) }()
	<-entered
	go func() { secondDone <- runner.RunDue(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	accountRepo.mu.Lock()
	require.Equal(t, 1, accountRepo.listCalls)
	accountRepo.mu.Unlock()
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	accountRepo.mu.Lock()
	require.Equal(t, 2, accountRepo.listCalls)
	accountRepo.mu.Unlock()
}

func TestOpenAINativeCompactionProbeRunnerSlotAcquisitionIsContextAware(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, acquireOpenAINativeCompactionProbeSlot(ctx, slots), context.Canceled)
}

func TestOpenAINativeCompactionProbeClaimErrorClassDoesNotExposeURL(t *testing.T) {
	sensitiveURL := "https://secret.example/tenant/v1/responses?access_token=probe-secret"
	err := errors.Join(
		ErrOpenAINativeCompactionProbeBudgetUnavailable,
		&url.Error{Op: "Post", URL: sensitiveURL, Err: errors.New("dial failed")},
	)

	errorClass := openAINativeCompactionProbeClaimErrorClass(err)
	require.Equal(t, "budget_unavailable", errorClass)
	require.NotContains(t, errorClass, sensitiveURL)
	require.Equal(t, "transport_error", openAINativeCompactionProbeClaimErrorClass(
		&url.Error{Op: "Post", URL: sensitiveURL, Err: errors.New("dial failed")},
	))
}

func TestOpenAINativeCompactionProbeRunnerBackoffIsBounded(t *testing.T) {
	checked := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	next := checked.Add(40 * time.Second)
	require.Equal(t, 80*time.Second, exponentialOpenAINativeCompactionProbeBackoff(time.Minute/2, 5*time.Minute, &checked, &next))
	require.Equal(t, 5*time.Minute, exponentialOpenAINativeCompactionProbeBackoff(time.Minute, 5*time.Minute, &checked, timePointer(checked.Add(4*time.Minute))))
}

func TestOpenAINativeCompactionProbeRunnerStopJoinsActiveCycle(t *testing.T) {
	entered := make(chan struct{})
	accountRepo := &nativeCompactionProbeRunnerAccountRepo{listEntered: entered, listRelease: make(chan struct{})}
	runner := newOpenAINativeCompactionProbeRunnerService(
		accountRepo,
		&nativeCompactionProbeRunnerAPIKeyRepo{key: nativeCompactionProbeRunnerAPIKey(9, 7)},
		&nativeCompactionProbeRunnerUserRepo{user: nativeCompactionProbeRunnerUser(21)},
		&nativeCompactionProbeRunnerGroupRepo{group: nativeCompactionProbeRunnerGroup(7)},
		&nativeCompactionProbeRunnerCapabilityRepo{},
		&nativeCompactionProbeRunnerExecutor{},
		nativeCompactionProbeRunnerConfig("gpt-test"),
	)

	runner.Start()
	<-entered
	done := make(chan struct{})
	go func() {
		runner.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel and join the active cycle")
	}
	runner.Start()
	runner.Stop()
}

func nativeCompactionProbeRunnerConfig(models ...string) config.GatewayOpenAINativeCompactionProbeConfig {
	return config.GatewayOpenAINativeCompactionProbeConfig{
		Enabled:                   true,
		TickIntervalSeconds:       3600,
		MaxWorkers:                2,
		ClaimLimit:                2,
		ClaimTTLSeconds:           180,
		RequestTimeoutSeconds:     90,
		MaxResponseBytes:          20 * 1024 * 1024,
		MaxEvents:                 256,
		SuccessReprobeMinutes:     1440,
		UnsupportedReprobeMinutes: 360,
		RetryInitialSeconds:       60,
		RetryMaxSeconds:           3600,
		RetryJitterRatio:          0,
		IsolatedGroupID:           7,
		IsolatedAPIKeyID:          9,
		ModelAllowlist:            models,
		MaxOutputTokens:           256,
		MaxCostPerRunMicroUSD:     1000,
		MaxCostPerDayMicroUSD:     10000,
	}
}

func nativeCompactionProbeRunnerAccount(id, groupID int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"api_key":  "synthetic-api-key",
			"base_url": "https://api.openai.com/v1",
			"openai_capabilities": []any{
				"chat_completions",
				"responses",
			},
		},
		Extra: map[string]any{
			"openai_responses_supported": true,
		},
	}
}

func nativeCompactionProbeRunnerAPIKey(id, groupID int64) *APIKey {
	return &APIKey{
		ID:        id,
		UserID:    21,
		GroupID:   &groupID,
		Status:    StatusAPIKeyActive,
		Quota:     10,
		QuotaUsed: 0,
	}
}

func nativeCompactionProbeRunnerUser(id int64) *User {
	return &User{ID: id, Status: StatusActive}
}

func nativeCompactionProbeRunnerGroup(id int64) *Group {
	return &Group{ID: id, Status: StatusActive}
}

func timePointer(value time.Time) *time.Time { return &value }
