package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const scheduledTestDefaultMaxWorkers = 10
const scheduledTestResultStatusSuccess = "success"

// ScheduledTestRunnerService periodically scans due test plans and executes them.
type ScheduledTestRunnerService struct {
	planRepo       ScheduledTestPlanRepository
	scheduledSvc   *ScheduledTestService
	accountTestSvc scheduledAccountTester
	rateLimitSvc   scheduledTestAccountRateLimiter
	cfg            *config.Config

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

type scheduledAccountTester interface {
	RunTestBackground(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error)
}

type scheduledTestAccountRateLimiter interface {
	SetScheduledTestTempUnschedulable(ctx context.Context, accountID int64, until time.Time, message string) error
	ClearTempUnschedulable(ctx context.Context, accountID int64) error
	RecoverAccountAfterSuccessfulTest(ctx context.Context, accountID int64) (*SuccessfulTestRecoveryResult, error)
}

// NewScheduledTestRunnerService creates a new runner.
func NewScheduledTestRunnerService(
	planRepo ScheduledTestPlanRepository,
	scheduledSvc *ScheduledTestService,
	accountTestSvc *AccountTestService,
	rateLimitSvc *RateLimitService,
	cfg *config.Config,
) *ScheduledTestRunnerService {
	return &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   scheduledSvc,
		accountTestSvc: accountTestSvc,
		rateLimitSvc:   rateLimitSvc,
		cfg:            cfg,
	}
}

// Start begins the cron ticker (every minute).
func (s *ScheduledTestRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}

		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() { s.runScheduled() })
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] not started (invalid schedule): %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] started (tick=every minute)")
	})
}

// Stop gracefully shuts down the cron scheduler.
func (s *ScheduledTestRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			ctx := s.cron.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] cron stop timed out")
			}
		}
	})
}

func (s *ScheduledTestRunnerService) runScheduled() {
	// Delay 10s so execution lands at ~:10 of each minute instead of :00.
	time.Sleep(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	now := time.Now()
	plans, err := s.planRepo.ListDue(ctx, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] ListDue error: %v", err)
		return
	}
	if len(plans) == 0 {
		return
	}

	logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] found %d due plans", len(plans))

	sem := make(chan struct{}, scheduledTestDefaultMaxWorkers)
	var wg sync.WaitGroup

	for _, plan := range plans {
		sem <- struct{}{}
		wg.Add(1)
		go func(p *ScheduledTestPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runOnePlan(ctx, p)
		}(plan)
	}

	wg.Wait()
}

func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	normalizeScheduledTestPlanDefaults(plan)

	result, err := s.runAccountTestWithTimeout(ctx, plan)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d RunTestBackground error: %v", plan.ID, err)
		return
	}

	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d SaveResult error: %v", plan.ID, err)
	} else if plan.AccountCircuitBreakerEnabled {
		s.applyAccountCircuitBreakerPolicy(ctx, plan)
	} else if result.Status == scheduledTestResultStatusSuccess && plan.AutoRecover {
		// Auto-recover account if test succeeded and auto_recover is enabled.
		s.tryRecoverAccount(ctx, plan.AccountID, plan.ID)
	}

	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d computeNextRun error: %v", plan.ID, err)
		return
	}

	if err := s.planRepo.UpdateAfterRun(ctx, plan.ID, time.Now(), nextRun); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d UpdateAfterRun error: %v", plan.ID, err)
	}
}

func (s *ScheduledTestRunnerService) runAccountTestWithTimeout(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestResult, error) {
	timeout := time.Duration(plan.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(defaultScheduledTestTimeoutSeconds) * time.Second
	}

	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	type testOutput struct {
		result *ScheduledTestResult
		err    error
	}
	done := make(chan testOutput, 1)
	go func() {
		result, err := s.accountTestSvc.RunTestBackground(testCtx, plan.AccountID, plan.ModelID)
		done <- testOutput{result: result, err: err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			return failedScheduledTestResult(startedAt, out.err.Error(), out.result), nil
		}
		if out.result == nil {
			return failedScheduledTestResult(startedAt, "scheduled test returned no result", nil), nil
		}
		return out.result, nil
	case <-testCtx.Done():
		if !errors.Is(testCtx.Err(), context.DeadlineExceeded) {
			return nil, testCtx.Err()
		}
		return failedScheduledTestResult(startedAt, fmt.Sprintf("scheduled test timed out after %d seconds", int(timeout/time.Second)), nil), nil
	}
}

func (s *ScheduledTestRunnerService) applyAccountCircuitBreakerPolicy(ctx context.Context, plan *ScheduledTestPlan) {
	if s == nil || s.scheduledSvc == nil || s.rateLimitSvc == nil || plan == nil {
		return
	}

	limit := maxScheduledTestInt(plan.FailureThreshold, plan.SuccessThreshold)
	if limit <= 0 {
		return
	}
	results, err := s.scheduledSvc.ListResults(ctx, plan.ID, limit)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d ListResults error: %v", plan.ID, err)
		return
	}

	if hasConsecutiveScheduledTestFailures(results, plan.FailureThreshold) {
		now := time.Now()
		until := scheduledTestCircuitBreakerUntil(plan, now)
		message := fmt.Sprintf("scheduled test failed %d consecutive times", plan.FailureThreshold)
		if len(results) > 0 && results[0].ErrorMessage != "" {
			message = fmt.Sprintf("%s: %s", message, results[0].ErrorMessage)
		}
		if err := s.rateLimitSvc.SetScheduledTestTempUnschedulable(ctx, plan.AccountID, until, message); err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account circuit breaker set failed: %v", plan.ID, err)
		}
		return
	}

	if hasConsecutiveScheduledTestSuccesses(results, plan.SuccessThreshold) {
		if plan.AutoRecover {
			s.tryRecoverAccount(ctx, plan.AccountID, plan.ID)
			return
		}
		if err := s.rateLimitSvc.ClearTempUnschedulable(ctx, plan.AccountID); err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d account circuit breaker clear failed: %v", plan.ID, err)
		}
	}
}

func failedScheduledTestResult(startedAt time.Time, message string, result *ScheduledTestResult) *ScheduledTestResult {
	finishedAt := time.Now()
	if result == nil {
		result = &ScheduledTestResult{}
	}
	result.Status = "failed"
	if result.ErrorMessage == "" {
		result.ErrorMessage = message
	}
	if result.StartedAt.IsZero() {
		result.StartedAt = startedAt
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = finishedAt
	}
	if result.LatencyMs <= 0 {
		result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	}
	return result
}

func scheduledTestCircuitBreakerUntil(plan *ScheduledTestPlan, now time.Time) time.Time {
	if plan == nil {
		return now
	}
	cooldownMinutes := plan.FailureCooldownMinutes
	if cooldownMinutes <= 0 {
		cooldownMinutes = defaultScheduledTestFailureCooldownMinutes
	}
	until := now.Add(time.Duration(cooldownMinutes) * time.Minute)

	recoveryUntil, ok := scheduledTestRecoveryProbeWindowUntil(plan, now)
	if ok && recoveryUntil.After(until) {
		return recoveryUntil
	}
	return until
}

func scheduledTestRecoveryProbeWindowUntil(plan *ScheduledTestPlan, from time.Time) (time.Time, bool) {
	if plan == nil || plan.CronExpression == "" {
		return time.Time{}, false
	}
	successThreshold := plan.SuccessThreshold
	if successThreshold <= 0 {
		successThreshold = defaultScheduledTestSuccessThreshold
	}
	timeoutSeconds := plan.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultScheduledTestTimeoutSeconds
	}

	sched, err := scheduledTestCronParser.Parse(plan.CronExpression)
	if err != nil {
		return time.Time{}, false
	}
	probeAt := from
	for i := 0; i < successThreshold; i++ {
		probeAt = sched.Next(probeAt)
	}
	return probeAt.Add(time.Duration(timeoutSeconds)*time.Second + time.Minute), true
}

func hasConsecutiveScheduledTestFailures(results []*ScheduledTestResult, threshold int) bool {
	if threshold <= 0 || len(results) < threshold {
		return false
	}
	failures := 0
	for _, result := range results {
		if result == nil || result.Status == scheduledTestResultStatusSuccess {
			break
		}
		if statusCode, ok := extractAccountTestHTTPStatus(result.ErrorMessage); ok &&
			isOpenAIRequestBlockedError(statusCode, result.ErrorMessage, []byte(result.ErrorMessage)) {
			break
		}
		failures++
		if failures >= threshold {
			return true
		}
	}
	return false
}

func hasConsecutiveScheduledTestSuccesses(results []*ScheduledTestResult, threshold int) bool {
	if threshold <= 0 || len(results) < threshold {
		return false
	}
	successes := 0
	for _, result := range results {
		if result == nil || result.Status != scheduledTestResultStatusSuccess {
			break
		}
		successes++
		if successes >= threshold {
			return true
		}
	}
	return false
}

func maxScheduledTestInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// tryRecoverAccount attempts to recover an account from recoverable runtime state.
func (s *ScheduledTestRunnerService) tryRecoverAccount(ctx context.Context, accountID int64, planID int64) {
	if s.rateLimitSvc == nil {
		return
	}

	recovery, err := s.rateLimitSvc.RecoverAccountAfterSuccessfulTest(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover failed: %v", planID, err)
		return
	}
	if recovery == nil {
		return
	}

	if recovery.ClearedError {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d recovered from error status", planID, accountID)
	}
	if recovery.ClearedRateLimit {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d cleared rate-limit/runtime state", planID, accountID)
	}
}
