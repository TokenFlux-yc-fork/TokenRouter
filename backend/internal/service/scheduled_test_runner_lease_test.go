package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type scheduledRunnerLeaseFake struct {
	mu sync.Mutex

	owner        string
	token        int64
	nextToken    int64
	expiresAt    time.Time
	acquireErr   error
	renewErr     error
	renewCalls   int
	releaseCalls int
}

func (f *scheduledRunnerLeaseFake) TryAcquireFencedLeaderLease(_ context.Context, _ string, owner string, ttl time.Duration) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return 0, false, f.acquireErr
	}
	now := time.Now()
	if f.owner != "" && now.Before(f.expiresAt) {
		return f.token, false, nil
	}
	f.nextToken++
	f.owner = owner
	f.token = f.nextToken
	f.expiresAt = now.Add(ttl)
	return f.token, true, nil
}

func (f *scheduledRunnerLeaseFake) RenewFencedLeaderLease(_ context.Context, _ string, owner string, token int64, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewCalls++
	if f.renewErr != nil {
		return false, f.renewErr
	}
	if f.owner != owner || f.token != token || !time.Now().Before(f.expiresAt) {
		return false, nil
	}
	f.expiresAt = time.Now().Add(ttl)
	return true, nil
}

func (f *scheduledRunnerLeaseFake) ReleaseFencedLeaderLease(_ context.Context, _ string, owner string, token int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if f.owner == owner && f.token == token {
		f.owner = ""
		f.expiresAt = time.Time{}
		return true, nil
	}
	return false, nil
}

func (f *scheduledRunnerLeaseFake) GetFencedLeaderLease(context.Context, string) (*FencedLeaderLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owner == "" || !time.Now().Before(f.expiresAt) {
		return nil, nil
	}
	return &FencedLeaderLease{
		Owner:        f.owner,
		FencingToken: f.token,
		TTL:          time.Until(f.expiresAt),
	}, nil
}

func (f *scheduledRunnerLeaseFake) setRenewError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewErr = err
}

func (f *scheduledRunnerLeaseFake) renewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renewCalls
}

func (f *scheduledRunnerLeaseFake) forceTakeover(owner string, ttl time.Duration) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextToken++
	f.owner = owner
	f.token = f.nextToken
	f.expiresAt = time.Now().Add(ttl)
	return f.token
}

type scheduledDuePlanRepo struct {
	scheduledPlanRepoStub
	mu        sync.Mutex
	listCalls int
	plans     []*ScheduledTestPlan
}

func (r *scheduledDuePlanRepo) ListDue(context.Context, time.Time) ([]*ScheduledTestPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	return r.plans, nil
}

func (r *scheduledDuePlanRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func configureFastScheduledRunnerLease(runner *ScheduledTestRunnerService) {
	runner.leaseTTL = 120 * time.Millisecond
	runner.leaseRenewInterval = 20 * time.Millisecond
	runner.leaseRetryInterval = 5 * time.Millisecond
	runner.leaseOperationTimeout = 15 * time.Millisecond
}

func waitForScheduledRunnerLease(t *testing.T, runner *ScheduledTestRunnerService, want bool) int64 {
	t.Helper()
	var token int64
	require.Eventually(t, func() bool {
		_, currentToken, ok := runner.currentFencedLease()
		token = currentToken
		return ok == want
	}, 2*time.Second, 5*time.Millisecond)
	return token
}

func TestScheduledTestRunnerLease_MissingBackendFailsClosed(t *testing.T) {
	repo := &scheduledDuePlanRepo{}
	runner := &ScheduledTestRunnerService{
		planRepo: repo,
		cfg:      &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
	}
	runner.Start()
	t.Cleanup(runner.Stop)

	runner.runScheduledDue()
	require.Zero(t, repo.calls())
}

func TestScheduledTestRunnerLease_AcquireErrorFailsClosed(t *testing.T) {
	repo := &scheduledDuePlanRepo{}
	runner := &ScheduledTestRunnerService{
		planRepo:   repo,
		leaseCache: &scheduledRunnerLeaseFake{acquireErr: errors.New("redis unavailable")},
		instanceID: "instance-acquire-error",
		cfg:        &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
	}
	configureFastScheduledRunnerLease(runner)
	runner.Start()
	t.Cleanup(runner.Stop)

	time.Sleep(30 * time.Millisecond)
	runner.runScheduledDue()
	require.Zero(t, repo.calls())
}

func TestScheduledTestRunnerLease_OnlyOneLeaderAndReleaseHandsOffWithHigherToken(t *testing.T) {
	lease := &scheduledRunnerLeaseFake{}
	newRunner := func() *ScheduledTestRunnerService {
		runner := &ScheduledTestRunnerService{
			leaseCache: lease,
			instanceID: time.Now().String(),
			cfg:        &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
		}
		configureFastScheduledRunnerLease(runner)
		return runner
	}

	first := newRunner()
	second := newRunner()
	first.Start()
	firstToken := waitForScheduledRunnerLease(t, first, true)
	second.Start()
	t.Cleanup(second.Stop)
	waitForScheduledRunnerLease(t, second, false)
	require.Positive(t, firstToken)

	first.Stop()
	secondToken := waitForScheduledRunnerLease(t, second, true)
	require.Greater(t, secondToken, firstToken)
}

func TestScheduledTestRunnerLease_RenewsWithoutChangingToken(t *testing.T) {
	lease := &scheduledRunnerLeaseFake{}
	runner := &ScheduledTestRunnerService{
		leaseCache: lease,
		instanceID: "instance-renew",
		cfg:        &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
	}
	configureFastScheduledRunnerLease(runner)
	runner.Start()
	t.Cleanup(runner.Stop)

	token := waitForScheduledRunnerLease(t, runner, true)
	require.Eventually(t, func() bool { return lease.renewCount() >= 2 }, time.Second, 5*time.Millisecond)
	_, renewedToken, ok := runner.currentFencedLease()
	require.True(t, ok)
	require.Equal(t, token, renewedToken)
}

func TestScheduledTestRunnerLease_RenewFailureCancelsAttemptAndPreventsSideEffects(t *testing.T) {
	lease := &scheduledRunnerLeaseFake{}
	testStarted := make(chan struct{})
	tester := &scheduledAccountTesterStub{run: func(ctx context.Context, _ int64, _ string) (*ScheduledTestResult, error) {
		close(testStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	planRepo := &scheduledDuePlanRepo{plans: []*ScheduledTestPlan{{
		ID:             99,
		AccountID:      42,
		ModelID:        "probe-model",
		CronExpression: "* * * * *",
		MaxResults:     5,
		TimeoutSeconds: 30,
	}}}
	resultRepo := &scheduledResultRepoStub{}
	runner := &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   NewScheduledTestService(planRepo, resultRepo),
		accountTestSvc: tester,
		leaseCache:     lease,
		instanceID:     "instance-loss",
		cfg:            &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
	}
	configureFastScheduledRunnerLease(runner)
	runner.Start()
	t.Cleanup(runner.Stop)
	waitForScheduledRunnerLease(t, runner, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.runScheduledDue()
	}()
	select {
	case <-testStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled test did not start")
	}

	lease.setRenewError(errors.New("redis unavailable"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled test was not cancelled after lease loss")
	}
	waitForScheduledRunnerLease(t, runner, false)

	results, err := resultRepo.ListByPlanID(context.Background(), 99, 10)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Zero(t, planRepo.updateAfterRunCalls)
}

func TestScheduledTestRunnerLease_StaleTokenCannotCommitAfterTakeover(t *testing.T) {
	lease := &scheduledRunnerLeaseFake{}
	testStarted := make(chan struct{})
	allowReturn := make(chan struct{})
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		close(testStarted)
		<-allowReturn
		return scheduledResult("success"), nil
	}}
	planRepo := &scheduledDuePlanRepo{plans: []*ScheduledTestPlan{{
		ID:             100,
		AccountID:      43,
		ModelID:        "probe-model",
		CronExpression: "* * * * *",
		MaxResults:     5,
		TimeoutSeconds: 30,
	}}}
	resultRepo := &scheduledResultRepoStub{}
	runner := &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   NewScheduledTestService(planRepo, resultRepo),
		accountTestSvc: tester,
		leaseCache:     lease,
		instanceID:     "instance-stale",
		cfg:            &config.Config{ScheduledRunnerEnabled: true, Timezone: "UTC"},
	}
	configureFastScheduledRunnerLease(runner)
	runner.leaseTTL = 2 * time.Second
	runner.leaseRenewInterval = time.Second
	runner.Start()
	t.Cleanup(runner.Stop)
	firstToken := waitForScheduledRunnerLease(t, runner, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.runScheduledDue()
	}()
	select {
	case <-testStarted:
	case <-time.After(time.Second):
		t.Fatal("scheduled test did not start")
	}

	secondToken := lease.forceTakeover("instance-new", 2*time.Second)
	require.Greater(t, secondToken, firstToken)
	close(allowReturn)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale runner did not finish")
	}

	results, err := resultRepo.ListByPlanID(context.Background(), 100, 10)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Zero(t, planRepo.updateAfterRunCalls)
}
