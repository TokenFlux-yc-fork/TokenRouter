package service

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

var scheduledTestCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

const (
	defaultScheduledTestMaxResults             = 50
	defaultScheduledTestFailureThreshold       = 3
	defaultScheduledTestSuccessThreshold       = 2
	defaultScheduledTestFailureCooldownMinutes = 5
	defaultScheduledTestTimeoutSeconds         = 30
	maxScheduledTestThreshold                  = 10
	maxScheduledTestFailureCooldownMinutes     = 10080
	maxScheduledTestTimeoutSeconds             = 300
)

// ScheduledTestService provides CRUD operations for scheduled test plans and results.
type ScheduledTestService struct {
	planRepo   ScheduledTestPlanRepository
	resultRepo ScheduledTestResultRepository
}

// NewScheduledTestService creates a new ScheduledTestService.
func NewScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
) *ScheduledTestService {
	return &ScheduledTestService{
		planRepo:   planRepo,
		resultRepo: resultRepo,
	}
}

// CreatePlan validates the cron expression, computes next_run_at, and persists the plan.
func (s *ScheduledTestService) CreatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun

	normalizeScheduledTestPlanDefaults(plan)

	return s.planRepo.Create(ctx, plan)
}

// GetPlan retrieves a plan by ID.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByAccount returns all plans for a given account.
func (s *ScheduledTestService) ListPlansByAccount(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByAccountID(ctx, accountID)
}

// UpdatePlan validates cron and updates the plan.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	plan.NextRunAt = &nextRun
	normalizeScheduledTestPlanDefaults(plan)

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan removes a plan and its results (via CASCADE).
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// ListResults returns the most recent results for a plan.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit)
}

// SaveResult inserts a result and prunes old entries beyond maxResults.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	if _, err := s.resultRepo.Create(ctx, result); err != nil {
		return err
	}
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}

func computeNextRun(cronExpr string, from time.Time) (time.Time, error) {
	sched, err := scheduledTestCronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

func normalizeScheduledTestPlanDefaults(plan *ScheduledTestPlan) {
	if plan == nil {
		return
	}
	if plan.MaxResults <= 0 {
		plan.MaxResults = defaultScheduledTestMaxResults
	}
	if plan.FailureThreshold <= 0 {
		plan.FailureThreshold = defaultScheduledTestFailureThreshold
	} else if plan.FailureThreshold > maxScheduledTestThreshold {
		plan.FailureThreshold = maxScheduledTestThreshold
	}
	if plan.SuccessThreshold <= 0 {
		plan.SuccessThreshold = defaultScheduledTestSuccessThreshold
	} else if plan.SuccessThreshold > maxScheduledTestThreshold {
		plan.SuccessThreshold = maxScheduledTestThreshold
	}
	if plan.FailureCooldownMinutes <= 0 {
		plan.FailureCooldownMinutes = defaultScheduledTestFailureCooldownMinutes
	} else if plan.FailureCooldownMinutes > maxScheduledTestFailureCooldownMinutes {
		plan.FailureCooldownMinutes = maxScheduledTestFailureCooldownMinutes
	}
	if plan.TimeoutSeconds <= 0 {
		plan.TimeoutSeconds = defaultScheduledTestTimeoutSeconds
	} else if plan.TimeoutSeconds > maxScheduledTestTimeoutSeconds {
		plan.TimeoutSeconds = maxScheduledTestTimeoutSeconds
	}
	if plan.AccountCircuitBreakerEnabled {
		minResultsForCircuitBreaker := plan.FailureThreshold
		if plan.SuccessThreshold > minResultsForCircuitBreaker {
			minResultsForCircuitBreaker = plan.SuccessThreshold
		}
		if plan.MaxResults < minResultsForCircuitBreaker {
			plan.MaxResults = minResultsForCircuitBreaker
		}
	}
}
