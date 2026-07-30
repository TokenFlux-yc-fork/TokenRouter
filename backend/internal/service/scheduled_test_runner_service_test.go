package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type scheduledPlanRepoStub struct {
	updateAfterRunCalls int
}

func (r *scheduledPlanRepoStub) Create(context.Context, *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	panic("unexpected Create call")
}

func (r *scheduledPlanRepoStub) GetByID(context.Context, int64) (*ScheduledTestPlan, error) {
	panic("unexpected GetByID call")
}

func (r *scheduledPlanRepoStub) ListByAccountID(context.Context, int64) ([]*ScheduledTestPlan, error) {
	panic("unexpected ListByAccountID call")
}

func (r *scheduledPlanRepoStub) ListDue(context.Context, time.Time) ([]*ScheduledTestPlan, error) {
	panic("unexpected ListDue call")
}

func (r *scheduledPlanRepoStub) Update(context.Context, *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	panic("unexpected Update call")
}

func (r *scheduledPlanRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (r *scheduledPlanRepoStub) UpdateAfterRun(context.Context, int64, time.Time, time.Time) error {
	r.updateAfterRunCalls++
	return nil
}

type scheduledResultRepoStub struct {
	nextID  int64
	results map[int64][]*ScheduledTestResult
}

func (r *scheduledResultRepoStub) Create(_ context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	if r.results == nil {
		r.results = make(map[int64][]*ScheduledTestResult)
	}
	r.nextID++
	copied := *result
	copied.ID = r.nextID
	if copied.CreatedAt.IsZero() {
		copied.CreatedAt = time.Now()
	}
	r.results[result.PlanID] = append([]*ScheduledTestResult{&copied}, r.results[result.PlanID]...)
	return &copied, nil
}

func (r *scheduledResultRepoStub) ListByPlanID(_ context.Context, planID int64, limit int) ([]*ScheduledTestResult, error) {
	list := r.results[planID]
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	out := make([]*ScheduledTestResult, len(list))
	copy(out, list)
	return out, nil
}

func (r *scheduledResultRepoStub) ListByAccountID(context.Context, int64, int) ([]*ScheduledTestAccountResult, error) {
	panic("unexpected ListByAccountID call")
}

func (r *scheduledResultRepoStub) PruneOldResults(context.Context, int64, int) error {
	return nil
}

type scheduledAccountTesterStub struct {
	run func(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error)
}

func (s *scheduledAccountTesterStub) RunTestBackground(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
	return s.run(ctx, accountID, modelID)
}

type scheduledRateLimiterStub struct {
	setCalls     int
	setAccountID int64
	setUntil     time.Time
	setMessage   string
	clearCalls   int
	clearID      int64
	recoverCalls int
	recoverID    int64
}

func (s *scheduledRateLimiterStub) SetScheduledTestTempUnschedulable(_ context.Context, accountID int64, until time.Time, message string) error {
	s.setCalls++
	s.setAccountID = accountID
	s.setUntil = until
	s.setMessage = message
	return nil
}

func (s *scheduledRateLimiterStub) ClearTempUnschedulable(_ context.Context, accountID int64) error {
	s.clearCalls++
	s.clearID = accountID
	return nil
}

func (s *scheduledRateLimiterStub) RecoverAccountAfterSuccessfulTest(_ context.Context, accountID int64) (*SuccessfulTestRecoveryResult, error) {
	s.recoverCalls++
	s.recoverID = accountID
	return &SuccessfulTestRecoveryResult{ClearedRateLimit: true}, nil
}

func newScheduledRunnerTestSubject(resultRepo *scheduledResultRepoStub, tester scheduledAccountTester, limiter *scheduledRateLimiterStub) (*ScheduledTestRunnerService, *scheduledPlanRepoStub) {
	planRepo := &scheduledPlanRepoStub{}
	scheduledSvc := NewScheduledTestService(planRepo, resultRepo)
	return &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   scheduledSvc,
		accountTestSvc: tester,
		rateLimitSvc:   limiter,
	}, planRepo
}

func scheduledResult(status string) *ScheduledTestResult {
	now := time.Now()
	return &ScheduledTestResult{
		Status:     status,
		StartedAt:  now,
		FinishedAt: now,
		CreatedAt:  now,
	}
}

func TestScheduledTestRunnerStartHonorsInstanceSwitch(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		runner := &ScheduledTestRunnerService{cfg: &config.Config{ScheduledRunnerEnabled: false}}
		runner.Start()
		require.Nil(t, runner.cron)
	})

	t.Run("enabled", func(t *testing.T) {
		runner := &ScheduledTestRunnerService{cfg: &config.Config{
			ScheduledRunnerEnabled: true,
			Timezone:               "UTC",
		}, leaseCache: &scheduledRunnerLeaseFake{}, instanceID: "enabled-test"}
		runner.Start()
		t.Cleanup(runner.Stop)
		require.NotNil(t, runner.cron)
	})
}

func TestNormalizeScheduledTestPlanDefaultsClampsCircuitBreakerInputs(t *testing.T) {
	plan := &ScheduledTestPlan{
		AccountCircuitBreakerEnabled: true,
		MaxResults:                   1,
		FailureThreshold:             999,
		SuccessThreshold:             999,
		FailureCooldownMinutes:       999999,
		TimeoutSeconds:               999,
	}

	normalizeScheduledTestPlanDefaults(plan)

	require.Equal(t, maxScheduledTestThreshold, plan.MaxResults)
	require.Equal(t, maxScheduledTestThreshold, plan.FailureThreshold)
	require.Equal(t, maxScheduledTestThreshold, plan.SuccessThreshold)
	require.Equal(t, maxScheduledTestFailureCooldownMinutes, plan.FailureCooldownMinutes)
	require.Equal(t, maxScheduledTestTimeoutSeconds, plan.TimeoutSeconds)
}

func TestNormalizeScheduledTestPlanDefaultsKeepsSmallMaxResultsWhenCircuitBreakerDisabled(t *testing.T) {
	plan := &ScheduledTestPlan{
		MaxResults: 1,
	}

	normalizeScheduledTestPlanDefaults(plan)

	require.Equal(t, 1, plan.MaxResults)
}

func TestScheduledTestRunner_AccountCircuitBreakerTriggersAfterConsecutiveFailures(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{
		results: map[int64][]*ScheduledTestResult{
			10: {scheduledResult("failed")},
		},
	}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		result := scheduledResult("failed")
		result.ErrorMessage = "upstream unavailable"
		return result, nil
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, planRepo := newScheduledRunnerTestSubject(resultRepo, tester, limiter)
	plan := &ScheduledTestPlan{
		ID:                           10,
		AccountID:                    42,
		ModelID:                      "claude-sonnet",
		CronExpression:               "*/30 * * * *",
		MaxResults:                   20,
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	}

	runner.runOnePlan(context.Background(), plan)

	require.Equal(t, 1, limiter.setCalls)
	require.Equal(t, int64(42), limiter.setAccountID)
	require.True(t, limiter.setUntil.After(time.Now().Add(4*time.Minute)))
	require.Contains(t, limiter.setMessage, "scheduled test failed 2 consecutive times")
	require.Contains(t, limiter.setMessage, "upstream unavailable")
	require.Equal(t, 0, limiter.clearCalls)
	require.Equal(t, 0, limiter.recoverCalls)
	require.Equal(t, 1, planRepo.updateAfterRunCalls)
}

func TestScheduledTestRunner_AccountCircuitBreakerBlockCoversRecoveryProbeWindow(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 10, 0, time.UTC)
	until := scheduledTestCircuitBreakerUntil(&ScheduledTestPlan{
		CronExpression:         "*/30 * * * *",
		SuccessThreshold:       2,
		FailureCooldownMinutes: 5,
		TimeoutSeconds:         30,
	}, now)

	require.Equal(t, time.Date(2026, 7, 4, 13, 1, 30, 0, time.UTC), until)
}

func TestScheduledTestRunner_AccountCircuitBreakerCountsTesterErrorsAsFailedProbe(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{
		results: map[int64][]*ScheduledTestResult{
			15: {scheduledResult("failed")},
		},
	}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		return nil, errors.New("upstream dial failed")
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, _ := newScheduledRunnerTestSubject(resultRepo, tester, limiter)

	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:                           15,
		AccountID:                    48,
		ModelID:                      "claude-sonnet",
		CronExpression:               "* * * * *",
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	})

	require.Equal(t, 1, limiter.setCalls)
	require.Contains(t, limiter.setMessage, "upstream dial failed")
	results, err := resultRepo.ListByPlanID(context.Background(), 15, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "failed", results[0].Status)
	require.Equal(t, "upstream dial failed", results[0].ErrorMessage)
}

func TestScheduledTestRunner_AccountCircuitBreakerSkipsBeforeFailureThreshold(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		return scheduledResult("failed"), nil
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, _ := newScheduledRunnerTestSubject(resultRepo, tester, limiter)

	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:                           11,
		AccountID:                    43,
		ModelID:                      "claude-sonnet",
		CronExpression:               "*/30 * * * *",
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	})

	require.Equal(t, 0, limiter.setCalls)
	require.Equal(t, 0, limiter.clearCalls)
	require.Equal(t, 0, limiter.recoverCalls)
}

func TestScheduledTestRunner_AccountCircuitBreakerClearsAfterConsecutiveSuccesses(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{
		results: map[int64][]*ScheduledTestResult{
			12: {scheduledResult("success")},
		},
	}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		return scheduledResult("success"), nil
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, _ := newScheduledRunnerTestSubject(resultRepo, tester, limiter)

	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:                           12,
		AccountID:                    44,
		ModelID:                      "claude-sonnet",
		CronExpression:               "*/30 * * * *",
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	})

	require.Equal(t, 0, limiter.setCalls)
	require.Equal(t, 1, limiter.clearCalls)
	require.Equal(t, int64(44), limiter.clearID)
	require.Equal(t, 0, limiter.recoverCalls)
}

func TestScheduledTestRunner_AccountCircuitBreakerDoesNotAutoRecoverBeforeSuccessThreshold(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		return scheduledResult("success"), nil
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, _ := newScheduledRunnerTestSubject(resultRepo, tester, limiter)

	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:                           13,
		AccountID:                    45,
		ModelID:                      "claude-sonnet",
		CronExpression:               "*/30 * * * *",
		AutoRecover:                  true,
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	})

	require.Equal(t, 0, limiter.setCalls)
	require.Equal(t, 0, limiter.clearCalls)
	require.Equal(t, 0, limiter.recoverCalls)
}

func TestScheduledTestRunner_AccountCircuitBreakerAutoRecoversAfterSuccessThreshold(t *testing.T) {
	resultRepo := &scheduledResultRepoStub{
		results: map[int64][]*ScheduledTestResult{
			14: {scheduledResult("success")},
		},
	}
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		return scheduledResult("success"), nil
	}}
	limiter := &scheduledRateLimiterStub{}
	runner, _ := newScheduledRunnerTestSubject(resultRepo, tester, limiter)

	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:                           14,
		AccountID:                    46,
		ModelID:                      "claude-sonnet",
		CronExpression:               "*/30 * * * *",
		AutoRecover:                  true,
		AccountCircuitBreakerEnabled: true,
		FailureThreshold:             2,
		SuccessThreshold:             2,
		FailureCooldownMinutes:       5,
		TimeoutSeconds:               30,
	})

	require.Equal(t, 0, limiter.setCalls)
	require.Equal(t, 0, limiter.clearCalls)
	require.Equal(t, 1, limiter.recoverCalls)
	require.Equal(t, int64(46), limiter.recoverID)
}

func TestScheduledTestRunner_RunAccountTestWithTimeoutRecordsFailedProbe(t *testing.T) {
	tester := &scheduledAccountTesterStub{run: func(context.Context, int64, string) (*ScheduledTestResult, error) {
		time.Sleep(2 * time.Second)
		return scheduledResult("success"), nil
	}}
	runner := &ScheduledTestRunnerService{accountTestSvc: tester}

	result, err := runner.runAccountTestWithTimeout(context.Background(), &ScheduledTestPlan{
		AccountID:      47,
		ModelID:        "claude-sonnet",
		TimeoutSeconds: 1,
	})

	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.True(t, strings.Contains(result.ErrorMessage, "timed out after 1 seconds"))
	require.GreaterOrEqual(t, result.LatencyMs, int64(900))
}
