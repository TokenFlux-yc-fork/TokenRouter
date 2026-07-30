package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

const scheduledTestDefaultMaxWorkers = 10
const scheduledTestResultStatusSuccess = "success"

const (
	scheduledTestRunnerLeaseKey              = "scheduled-test-runner"
	scheduledTestRunnerLeaseTTL              = 45 * time.Second
	scheduledTestRunnerLeaseRenewInterval    = 10 * time.Second
	scheduledTestRunnerLeaseRetryInterval    = 2 * time.Second
	scheduledTestRunnerLeaseOperationTimeout = 2 * time.Second
)

// ScheduledTestRunnerService periodically scans due test plans and executes them.
type ScheduledTestRunnerService struct {
	planRepo       ScheduledTestPlanRepository
	scheduledSvc   *ScheduledTestService
	accountTestSvc scheduledAccountTester
	rateLimitSvc   scheduledTestAccountRateLimiter
	cfg            *config.Config
	leaseCache     FencedLeaderLeaseCache
	instanceID     string

	leaseTTL              time.Duration
	leaseRenewInterval    time.Duration
	leaseRetryInterval    time.Duration
	leaseOperationTimeout time.Duration

	leaseMu         sync.Mutex
	leaseCtx        context.Context
	leaseCancel     context.CancelFunc
	leaseToken      int64
	leaseValidUntil time.Time
	runnerCancel    context.CancelFunc
	leaseWG         sync.WaitGroup
	runMu           sync.Mutex

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
	leaseCache FencedLeaderLeaseCache,
) *ScheduledTestRunnerService {
	return &ScheduledTestRunnerService{
		planRepo:       planRepo,
		scheduledSvc:   scheduledSvc,
		accountTestSvc: accountTestSvc,
		rateLimitSvc:   rateLimitSvc,
		cfg:            cfg,
		leaseCache:     leaseCache,
		instanceID:     uuid.NewString(),
	}
}

// Start begins the cron ticker (every minute).
func (s *ScheduledTestRunnerService) Start() {
	if s == nil {
		return
	}
	if s.cfg != nil && !s.cfg.ScheduledRunnerEnabled {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] not started (disabled)")
		return
	}
	if s.leaseCache == nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] not started (fenced lease backend unavailable)")
		return
	}
	s.startOnce.Do(func() {
		s.normalizeLeaseSettings()
		if s.instanceID == "" {
			s.instanceID = uuid.NewString()
		}
		runnerCtx, runnerCancel := context.WithCancel(context.Background())
		s.runnerCancel = runnerCancel
		s.leaseWG.Add(1)
		go s.maintainFencedLease(runnerCtx)

		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}

		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() { s.runScheduled() })
		if err != nil {
			runnerCancel()
			s.leaseWG.Wait()
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
		if s.runnerCancel != nil {
			s.runnerCancel()
		}
		if s.cron != nil {
			ctx := s.cron.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] cron stop timed out")
			}
		}
		s.leaseWG.Wait()
	})
}

func (s *ScheduledTestRunnerService) runScheduled() {
	// Delay 10s so execution lands at ~:10 of each minute instead of :00.
	time.Sleep(10 * time.Second)

	s.runScheduledDue()
}

func (s *ScheduledTestRunnerService) runScheduledDue() {
	leaseCtx, fencingToken, ok := s.currentFencedLease()
	if !ok || !s.runMu.TryLock() {
		return
	}
	defer s.runMu.Unlock()

	ctx, cancel := context.WithTimeout(leaseCtx, 5*time.Minute)
	defer cancel()

	if !s.confirmFencedLease(ctx, fencingToken) {
		return
	}
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
		if !s.fencedLeaseActive(fencingToken) {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p *ScheduledTestPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			s.runOnePlanFenced(ctx, p, fencingToken)
		}(plan)
	}

	wg.Wait()
}

func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	s.runOnePlanInternal(ctx, plan, 0, false)
}

func (s *ScheduledTestRunnerService) runOnePlanFenced(ctx context.Context, plan *ScheduledTestPlan, fencingToken int64) {
	s.runOnePlanInternal(ctx, plan, fencingToken, true)
}

func (s *ScheduledTestRunnerService) runOnePlanInternal(ctx context.Context, plan *ScheduledTestPlan, fencingToken int64, requireLease bool) {
	if !s.scheduledRunnerSideEffectsAllowed(ctx, fencingToken, requireLease) {
		return
	}
	normalizeScheduledTestPlanDefaults(plan)

	result, err := s.runAccountTestWithTimeout(ctx, plan)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d RunTestBackground error: %v", plan.ID, err)
		return
	}
	if !s.scheduledRunnerSideEffectsAllowed(ctx, fencingToken, requireLease) {
		return
	}

	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d SaveResult error: %v", plan.ID, err)
	} else if !s.scheduledRunnerSideEffectsAllowed(ctx, fencingToken, requireLease) {
		return
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

	if !s.scheduledRunnerSideEffectsAllowed(ctx, fencingToken, requireLease) {
		return
	}
	if err := s.planRepo.UpdateAfterRun(ctx, plan.ID, time.Now(), nextRun); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d UpdateAfterRun error: %v", plan.ID, err)
	}
}

func (s *ScheduledTestRunnerService) normalizeLeaseSettings() {
	if s.leaseTTL <= 0 {
		s.leaseTTL = scheduledTestRunnerLeaseTTL
	}
	if s.leaseRenewInterval <= 0 {
		s.leaseRenewInterval = scheduledTestRunnerLeaseRenewInterval
	}
	if s.leaseRetryInterval <= 0 {
		s.leaseRetryInterval = scheduledTestRunnerLeaseRetryInterval
	}
	if s.leaseOperationTimeout <= 0 {
		s.leaseOperationTimeout = scheduledTestRunnerLeaseOperationTimeout
	}
}

func (s *ScheduledTestRunnerService) maintainFencedLease(ctx context.Context) {
	defer s.leaseWG.Done()
	var fencingToken int64
	defer func() {
		if fencingToken > 0 {
			releaseCtx, cancel := context.WithTimeout(context.Background(), s.leaseOperationTimeout)
			released, err := s.leaseCache.ReleaseFencedLeaderLease(releaseCtx, scheduledTestRunnerLeaseKey, s.instanceID, fencingToken)
			if err != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease release failed fencing_token=%d: %v", fencingToken, err)
			} else if released {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease released fencing_token=%d", fencingToken)
			} else {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] stale lease release skipped fencing_token=%d", fencingToken)
			}
			cancel()
		}
		s.clearFencedLease(fencingToken)
	}()

	for {
		if ctx.Err() != nil {
			return
		}

		operationStarted := time.Now()
		operationCtx, cancel := context.WithTimeout(ctx, s.leaseOperationTimeout)
		if fencingToken == 0 {
			token, acquired, err := s.leaseCache.TryAcquireFencedLeaderLease(
				operationCtx,
				scheduledTestRunnerLeaseKey,
				s.instanceID,
				s.leaseTTL,
			)
			cancel()
			if err == nil && acquired {
				fencingToken = token
				s.setFencedLease(ctx, token, operationStarted.Add(s.leaseTTL))
				logger.LegacyPrintf(
					"service.scheduled_test_runner",
					"[ScheduledTestRunner] lease acquired owner=%s fencing_token=%d ttl=%s",
					s.instanceID,
					token,
					s.leaseTTL,
				)
				if !waitScheduledRunnerLease(ctx, s.leaseRenewInterval) {
					return
				}
				continue
			}
			if err != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease acquire failed: %v", err)
			}
			if !waitScheduledRunnerLease(ctx, s.leaseRetryInterval) {
				return
			}
			continue
		}

		renewed, err := s.leaseCache.RenewFencedLeaderLease(
			operationCtx,
			scheduledTestRunnerLeaseKey,
			s.instanceID,
			fencingToken,
			s.leaseTTL,
		)
		cancel()
		if err != nil || !renewed {
			if err != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease renewal failed: %v", err)
			} else {
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease lost fencing_token=%d", fencingToken)
			}
			s.clearFencedLease(fencingToken)
			fencingToken = 0
			if !waitScheduledRunnerLease(ctx, s.leaseRetryInterval) {
				return
			}
			continue
		}

		if !s.extendFencedLease(fencingToken, operationStarted.Add(s.leaseTTL)) {
			s.setFencedLease(ctx, fencingToken, operationStarted.Add(s.leaseTTL))
		}
		if !waitScheduledRunnerLease(ctx, s.leaseRenewInterval) {
			return
		}
	}
}

func (s *ScheduledTestRunnerService) setFencedLease(parent context.Context, fencingToken int64, validUntil time.Time) {
	leaseCtx, leaseCancel := context.WithCancel(parent)
	s.leaseMu.Lock()
	previousCancel := s.leaseCancel
	s.leaseCtx = leaseCtx
	s.leaseCancel = leaseCancel
	s.leaseToken = fencingToken
	s.leaseValidUntil = validUntil
	s.leaseMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
}

func (s *ScheduledTestRunnerService) extendFencedLease(fencingToken int64, validUntil time.Time) bool {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.leaseToken == fencingToken {
		s.leaseValidUntil = validUntil
		return true
	}
	return false
}

func (s *ScheduledTestRunnerService) clearFencedLease(fencingToken int64) {
	s.leaseMu.Lock()
	if fencingToken != 0 && s.leaseToken != fencingToken {
		s.leaseMu.Unlock()
		return
	}
	cancel := s.leaseCancel
	s.leaseCtx = nil
	s.leaseCancel = nil
	s.leaseToken = 0
	s.leaseValidUntil = time.Time{}
	s.leaseMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *ScheduledTestRunnerService) currentFencedLease() (context.Context, int64, bool) {
	s.leaseMu.Lock()
	if s.leaseToken <= 0 || s.leaseCtx == nil || s.leaseCtx.Err() != nil || !time.Now().Before(s.leaseValidUntil) {
		cancel := s.leaseCancel
		s.leaseCtx = nil
		s.leaseCancel = nil
		s.leaseToken = 0
		s.leaseValidUntil = time.Time{}
		s.leaseMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil, 0, false
	}
	leaseCtx := s.leaseCtx
	token := s.leaseToken
	s.leaseMu.Unlock()
	return leaseCtx, token, true
}

func (s *ScheduledTestRunnerService) fencedLeaseActive(fencingToken int64) bool {
	_, currentToken, ok := s.currentFencedLease()
	return ok && fencingToken > 0 && currentToken == fencingToken
}

func (s *ScheduledTestRunnerService) scheduledRunnerSideEffectsAllowed(ctx context.Context, fencingToken int64, requireLease bool) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	if !requireLease {
		return true
	}
	return s.confirmFencedLease(ctx, fencingToken)
}

func (s *ScheduledTestRunnerService) confirmFencedLease(ctx context.Context, fencingToken int64) bool {
	if !s.fencedLeaseActive(fencingToken) || s.leaseCache == nil {
		return false
	}
	operationStarted := time.Now()
	operationCtx, cancel := context.WithTimeout(ctx, s.leaseOperationTimeout)
	renewed, err := s.leaseCache.RenewFencedLeaderLease(
		operationCtx,
		scheduledTestRunnerLeaseKey,
		s.instanceID,
		fencingToken,
		s.leaseTTL,
	)
	cancel()
	if err != nil || !renewed {
		s.clearFencedLease(fencingToken)
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] lease fence check failed: %v", err)
		} else {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] stale fencing_token=%d rejected", fencingToken)
		}
		return false
	}
	s.extendFencedLease(fencingToken, operationStarted.Add(s.leaseTTL))
	return true
}

func waitScheduledRunnerLease(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
