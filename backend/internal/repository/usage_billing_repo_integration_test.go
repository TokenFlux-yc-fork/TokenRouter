//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/domain"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

func float64Ptr(v float64) *float64 {
	return &v
}

// insertBatchImageAllowanceTestJob 创建额度预记测试所需的最小任务记录。
func insertBatchImageAllowanceTestJob(t *testing.T, batchID string, actorUserID, billingUserID, apiKeyID int64, teamID *int64, createdAt time.Time) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `
		INSERT INTO batch_image_jobs (
			batch_id, user_id, billing_user_id, team_id, api_key_id,
			provider, model, item_count, estimated_cost, hold_amount, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'gemini', 'image-test', 1, 1, 1, $6, $6)`,
		batchID, actorUserID, billingUserID, teamID, apiKeyID, createdAt)
	require.NoError(t, err)
}

func TestUsageBillingRepositoryBatchImageAllowanceReserveCaptureAndRelease(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-allowance-%d@example.com", time.Now().UnixNano()),
		Balance: 10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:      user.ID,
		Key:         "sk-batch-allowance-" + uuid.NewString(),
		Name:        "batch-allowance",
		Quota:       1,
		RateLimit5h: 1,
		RateLimit1d: 1,
		RateLimit7d: 1,
	})

	firstBatchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, firstBatchID, user.ID, user.ID, apiKey.ID, nil, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(firstBatchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     firstBatchID,
		HoldAmount:  0.8,
		ReservedAt:  reservedAt,
	}
	reserveResult, err := repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)
	require.True(t, reserveResult.Applied)

	var balance, frozen, quotaUsed, usage5h, usage1d, usage7d float64
	var allowanceReserved bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 9.2, balance, 0.000001)
	require.InDelta(t, 0.8, frozen, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h, usage_1d, usage_7d FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&quotaUsed, &usage5h, &usage1d, &usage7d))
	require.InDelta(t, 0.8, quotaUsed, 0.000001)
	require.InDelta(t, 0.8, usage5h, 0.000001)
	require.InDelta(t, 0.8, usage1d, 0.000001)
	require.InDelta(t, 0.8, usage7d, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT allowance_reserved FROM batch_image_jobs WHERE batch_id = $1`, firstBatchID).Scan(&allowanceReserved))
	require.True(t, allowanceReserved)

	overflowBatchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	overflowReservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, overflowBatchID, user.ID, user.ID, apiKey.ID, nil, overflowReservedAt)
	_, err = repo.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(overflowBatchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     overflowBatchID,
		HoldAmount:  0.3,
		ReservedAt:  overflowReservedAt,
	})
	require.ErrorIs(t, err, service.ErrAPIKeyQuotaExhausted)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 9.2, balance, 0.000001)
	require.InDelta(t, 0.8, frozen, 0.000001)

	captureCommand := *reserveCommand
	captureCommand.RequestID = service.BatchImageCaptureRequestID(firstBatchID)
	captureCommand.ActualAmount = 0.3
	// 模拟服务层把预占结果写回任务后，再按持久化快照执行结算。
	captureCommand.BalanceHoldAmount = reserveResult.BalanceAmountUSD
	captureCommand.AllowanceReserved = true
	captureCommand.RequestFingerprint = ""
	captureResult, err := repo.CaptureBatchImageBalance(ctx, &captureCommand)
	require.NoError(t, err)
	require.True(t, captureResult.Applied)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 9.7, balance, 0.000001)
	require.InDelta(t, 0, frozen, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&quotaUsed, &usage5h))
	require.InDelta(t, 0.3, quotaUsed, 0.000001)
	require.InDelta(t, 0.3, usage5h, 0.000001)

	// 成功后任务标记会翻转为 false，重复结算仍必须命中原幂等记录。
	retryCapture := captureCommand
	retryCapture.AllowanceReserved = false
	retryCapture.RequestFingerprint = ""
	retryResult, err := repo.CaptureBatchImageBalance(ctx, &retryCapture)
	require.NoError(t, err)
	require.False(t, retryResult.Applied)

	secondBatchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	secondReservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, secondBatchID, user.ID, user.ID, apiKey.ID, nil, secondReservedAt)
	secondReserve := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(secondBatchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     secondBatchID,
		HoldAmount:  0.7,
		ReservedAt:  secondReservedAt,
	}
	_, err = repo.ReserveBatchImageBalance(ctx, secondReserve)
	require.NoError(t, err)

	releaseCommand := *secondReserve
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(secondBatchID)
	releaseCommand.AllowanceReserved = true
	releaseCommand.RequestFingerprint = ""
	releaseResult, err := repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)
	require.True(t, releaseResult.Applied)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h, status FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&quotaUsed, &usage5h, &apiKey.Status))
	require.InDelta(t, 0.3, quotaUsed, 0.000001)
	require.InDelta(t, 0.3, usage5h, 0.000001)
	require.Equal(t, service.StatusAPIKeyActive, apiKey.Status)
}

func TestUsageBillingRepositoryBatchImageSubscriptionFirstReserveAndCapture(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-subscription-%d@example.com", time.Now().UnixNano()),
		Balance: 1,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "batch-subscription-plan-" + uuid.NewString(),
		Description:     "batch image subscription billing test plan",
		Price:           10,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(1),
		WeeklyLimitUSD:  float64Ptr(1),
		MonthlyLimitUSD: float64Ptr(1),
	})
	reservedAt := time.Now().UTC()
	windowStart := reservedAt.Add(-time.Hour)
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		PlanID:             plan.ID,
		DailyWindowStart:   &windowStart,
		WeeklyWindowStart:  &windowStart,
		MonthlyWindowStart: &windowStart,
		DailyLimitUSD:      float64Ptr(1),
		WeeklyLimitUSD:     float64Ptr(1),
		MonthlyLimitUSD:    float64Ptr(1),
		DailyUsageUSD:      0.4,
		WeeklyUsageUSD:     0.4,
		MonthlyUsageUSD:    0.4,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-batch-subscription-" + uuid.NewString(),
		Name:   "batch-subscription",
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, apiKey.ID, nil, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(batchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     batchID,
		HoldAmount:  1,
		ReservedAt:  reservedAt,
	}

	reserved, err := repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)
	require.True(t, reserved.Applied)
	require.InDelta(t, 0.6, reserved.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.4, reserved.BalanceAmountUSD, 0.000001)

	var balance, frozen, dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 0.6, balance, 0.000001)
	require.InDelta(t, 0.4, frozen, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 1, dailyUsage, 0.000001)

	subscriptionHolds := make([]domain.BillingAllocation, 0, 1)
	for _, allocation := range reserved.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			subscriptionHolds = append(subscriptionHolds, allocation)
		}
	}
	captureCommand := *reserveCommand
	captureCommand.RequestID = service.BatchImageCaptureRequestID(batchID)
	captureCommand.RequestFingerprint = ""
	captureCommand.ActualAmount = 0.3
	captureCommand.BalanceHoldAmount = reserved.BalanceAmountUSD
	captureCommand.SubscriptionHoldAllocations = subscriptionHolds
	captureCommand.AllowanceReserved = true

	captured, err := repo.CaptureBatchImageBalance(ctx, &captureCommand)
	require.NoError(t, err)
	require.True(t, captured.Applied)
	require.InDelta(t, 0.3, captured.SubscriptionAmountUSD, 0.000001)
	require.Zero(t, captured.BalanceAmountUSD)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 1, balance, 0.000001)
	require.Zero(t, frozen)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 0.7, dailyUsage, 0.000001)

	releaseReservedAt := time.Now().UTC()
	releaseBatchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	insertBatchImageAllowanceTestJob(t, releaseBatchID, user.ID, user.ID, apiKey.ID, nil, releaseReservedAt)
	releaseReserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(releaseBatchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     releaseBatchID,
		HoldAmount:  0.5,
		ReservedAt:  releaseReservedAt,
	}
	releaseReserved, err := repo.ReserveBatchImageBalance(ctx, releaseReserveCommand)
	require.NoError(t, err)
	require.InDelta(t, 0.3, releaseReserved.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.2, releaseReserved.BalanceAmountUSD, 0.000001)

	releaseSubscriptionHolds := make([]domain.BillingAllocation, 0, 1)
	for _, allocation := range releaseReserved.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			releaseSubscriptionHolds = append(releaseSubscriptionHolds, allocation)
		}
	}
	releaseCommand := *releaseReserveCommand
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(releaseBatchID)
	releaseCommand.RequestFingerprint = ""
	releaseCommand.BalanceHoldAmount = releaseReserved.BalanceAmountUSD
	releaseCommand.SubscriptionHoldAllocations = releaseSubscriptionHolds
	releaseCommand.AllowanceReserved = true

	released, err := repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)
	require.True(t, released.Applied)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 1, balance, 0.000001)
	require.Zero(t, frozen)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 0.7, dailyUsage, 0.000001)

	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET balance = 0.05 WHERE id = $1`, user.ID)
	require.NoError(t, err)
	failedReservedAt := time.Now().UTC()
	failedBatchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	insertBatchImageAllowanceTestJob(t, failedBatchID, user.ID, user.ID, apiKey.ID, nil, failedReservedAt)
	_, err = repo.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(failedBatchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     failedBatchID,
		HoldAmount:  0.5,
		ReservedAt:  failedReservedAt,
	})
	require.ErrorIs(t, err, service.ErrBatchImageInsufficientBalance)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 0.05, balance, 0.000001)
	require.Zero(t, frozen)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 0.7, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryBatchImagePartialSubscriptionUsesBalanceRate(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-partial-rate-%d@example.com", time.Now().UnixNano()),
		Balance: 5,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "batch-partial-rate-group-" + uuid.NewString(),
		Platform: service.PlatformGemini,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:                 "batch-partial-rate-plan-" + uuid.NewString(),
		Description:          "batch image partial subscription rate test plan",
		Price:                10,
		ValidityDays:         30,
		ValidityUnit:         "day",
		GroupIDs:             []int64{group.ID},
		GroupRateMultipliers: map[int64]float64{group.ID: 0.5},
		ForSale:              true,
		DailyLimitUSD:        float64Ptr(1),
		WeeklyLimitUSD:       float64Ptr(1),
		MonthlyLimitUSD:      float64Ptr(1),
	})
	reservedAt := time.Now().UTC()
	windowStart := reservedAt.Add(-time.Hour)
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		PlanID:             plan.ID,
		DailyWindowStart:   &windowStart,
		WeeklyWindowStart:  &windowStart,
		MonthlyWindowStart: &windowStart,
		DailyLimitUSD:      float64Ptr(1),
		WeeklyLimitUSD:     float64Ptr(1),
		MonthlyLimitUSD:    float64Ptr(1),
		DailyUsageUSD:      0.8,
		WeeklyUsageUSD:     0.8,
		MonthlyUsageUSD:    0.8,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-batch-partial-rate-" + uuid.NewString(),
		Name:    "batch-partial-rate",
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, apiKey.ID, nil, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:                       service.BatchImageHoldRequestID(batchID),
		APIKeyID:                        apiKey.ID,
		UserID:                          user.ID,
		ActorUserID:                     user.ID,
		GroupID:                         &group.ID,
		BatchID:                         batchID,
		HoldAmount:                      0.5,
		PricingSnapshotVersion:          2,
		BaseAmountUSD:                   1,
		SubscriptionRateMultiplier:      1,
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           2,
		SettlementRateScale:             0.5,
		ReservedAt:                      reservedAt,
	}

	reserved, err := repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)
	require.InDelta(t, 0.2, reserved.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 1.2, reserved.BalanceAmountUSD, 0.000001)
	require.InDelta(t, 1.4, reserved.HoldAmountUSD, 0.000001)
	require.InDelta(t, 0.4, reserved.EstimatedAmountUSD, 0.000001)
	require.Len(t, reserved.BillingAllocations, 2)
	require.InDelta(t, 0.4, reserved.BillingAllocations[0].BaseAmountUSD, 0.000001)
	require.InDelta(t, 0.5, reserved.BillingAllocations[0].RateMultiplier, 0.000001)

	subscriptionHolds := make([]domain.BillingAllocation, 0, 1)
	for _, allocation := range reserved.BillingAllocations {
		if allocation.Type == domain.BillingAllocationTypeSubscription {
			subscriptionHolds = append(subscriptionHolds, allocation)
		}
	}
	captureCommand := *reserveCommand
	captureCommand.RequestID = service.BatchImageCaptureRequestID(batchID)
	captureCommand.RequestFingerprint = ""
	captureCommand.HoldAmount = reserved.HoldAmountUSD
	captureCommand.ActualBaseAmountUSD = 1
	captureCommand.BalanceHoldAmount = reserved.BalanceAmountUSD
	captureCommand.SubscriptionHoldAllocations = subscriptionHolds
	captureCommand.AllowanceReserved = true

	captured, err := repo.CaptureBatchImageBalance(ctx, &captureCommand)
	require.NoError(t, err)
	require.InDelta(t, 0.2, captured.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.2, captured.BalanceAmountUSD, 0.000001)
	require.InDelta(t, 0.4, captured.ActualAmountUSD, 0.000001)

	var balance, frozen, dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 4.8, balance, 0.000001)
	require.Zero(t, frozen)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1`, subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 1, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryBatchImageUnlimitedKeyReleaseKeepsExistingUsage(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-unlimited-%d@example.com", time.Now().UnixNano()),
		Balance: 10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-batch-unlimited-" + uuid.NewString(),
		Name:   "batch-unlimited",
	})
	_, err := integrationDB.ExecContext(ctx, `
		UPDATE api_keys SET quota_used = 1, usage_5h = 1, usage_1d = 1, usage_7d = 1,
		       window_5h_start = NOW(), window_1d_start = date_trunc('day', NOW()), window_7d_start = date_trunc('day', NOW())
		WHERE id = $1`, apiKey.ID)
	require.NoError(t, err)

	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, apiKey.ID, nil, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(batchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     batchID,
		HoldAmount:  0.8,
		ReservedAt:  reservedAt,
	}
	_, err = repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)

	var quotaUsed, usage5h, usage1d, usage7d float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h, usage_1d, usage_7d FROM api_keys WHERE id = $1`, apiKey.ID).
		Scan(&quotaUsed, &usage5h, &usage1d, &usage7d))
	for _, usage := range []float64{quotaUsed, usage5h, usage1d, usage7d} {
		require.InDelta(t, 1.8, usage, 0.000001)
	}

	releaseCommand := *reserveCommand
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(batchID)
	releaseCommand.AllowanceReserved = true
	releaseCommand.RequestFingerprint = ""
	_, err = repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h, usage_1d, usage_7d FROM api_keys WHERE id = $1`, apiKey.ID).
		Scan(&quotaUsed, &usage5h, &usage1d, &usage7d))
	for _, usage := range []float64{quotaUsed, usage5h, usage1d, usage7d} {
		require.InDelta(t, 1, usage, 0.000001)
	}
}

func TestUsageBillingRepositoryBatchImageMemberAllowanceSerializesConcurrentReserve(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	teamRepo := NewTeamRepository(integrationDB)
	repo := NewUsageBillingRepository(client, integrationDB)
	owner := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("batch-owner"), Balance: 10})
	member := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("batch-member")})
	teamCtx, err := teamRepo.Create(ctx, "批任务并发额度团队", owner.ID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, teamCtx.Team.ID, member.ID, 1, 10, 30))
	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: member.ID,
		TeamID: &teamID,
		Key:    "sk-batch-member-" + uuid.NewString(),
		Name:   "batch-member",
	})

	commands := make([]*service.BatchImageBalanceHoldCommand, 2)
	for index := range commands {
		batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		reservedAt := time.Now().UTC()
		insertBatchImageAllowanceTestJob(t, batchID, member.ID, owner.ID, apiKey.ID, &teamID, reservedAt)
		commands[index] = &service.BatchImageBalanceHoldCommand{
			RequestID:   service.BatchImageHoldRequestID(batchID),
			APIKeyID:    apiKey.ID,
			UserID:      owner.ID,
			ActorUserID: member.ID,
			TeamID:      &teamID,
			BatchID:     batchID,
			HoldAmount:  0.75,
			ReservedAt:  reservedAt,
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(commands))
	var waitGroup sync.WaitGroup
	for _, command := range commands {
		waitGroup.Add(1)
		go func(command *service.BatchImageBalanceHoldCommand) {
			defer waitGroup.Done()
			<-start
			_, reserveErr := repo.ReserveBatchImageBalance(ctx, command)
			results <- reserveErr
		}(command)
	}
	close(start)
	waitGroup.Wait()
	close(results)

	var accepted, limited int
	for result := range results {
		switch {
		case result == nil:
			accepted++
		case errors.Is(result, service.ErrTeamMemberDailyExceeded):
			limited++
		default:
			require.NoError(t, result)
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, limited)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM team_memberships WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL`, teamID, member.ID).Scan(&dailyUsage))
	require.InDelta(t, 0.75, dailyUsage, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, owner.ID).Scan(&owner.Balance, &owner.FrozenBalance))
	require.InDelta(t, 9.25, owner.Balance, 0.000001)
	require.InDelta(t, 0.75, owner.FrozenBalance, 0.000001)
}

func TestUsageBillingRepositoryBatchImageLegacyJobChargesActualAmount(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-legacy-%d@example.com", time.Now().UnixNano()),
		Balance: 10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-batch-legacy-" + uuid.NewString(),
		Name:   "batch-legacy",
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	createdAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, apiKey.ID, nil, createdAt)
	// 模拟升级前已经冻结余额、但任务行还没有 allowance_reserved 标记的存量任务。
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET balance = balance - 1, frozen_balance = frozen_balance + 1 WHERE id = $1`, user.ID)
	require.NoError(t, err)

	result, err := repo.CaptureBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
		RequestID:          service.BatchImageCaptureRequestID(batchID),
		APIKeyID:           apiKey.ID,
		UserID:             user.ID,
		ActorUserID:        user.ID,
		BatchID:            batchID,
		HoldAmount:         1,
		ActualAmount:       0.4,
		AllowanceReserved:  false,
		ReservedAt:         createdAt,
		RequestPayloadHash: "legacy-job",
	})
	require.NoError(t, err)
	require.True(t, result.Applied)

	var balance, frozen, quotaUsed, usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 9.6, balance, 0.000001)
	require.InDelta(t, 0, frozen, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&quotaUsed, &usage5h))
	require.InDelta(t, 0.4, quotaUsed, 0.000001)
	require.InDelta(t, 0.4, usage5h, 0.000001)
}

func TestUsageBillingRepositoryBatchImageReleaseAfterKeyDeletedAndMemberLeft(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	teamRepo := NewTeamRepository(integrationDB)
	repo := NewUsageBillingRepository(client, integrationDB)
	owner := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("release-owner"), Balance: 10})
	member := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("release-member")})
	teamCtx, err := teamRepo.Create(ctx, "离队退款团队", owner.ID, 5)
	require.NoError(t, err)
	token := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, token, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, token, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, teamCtx.Team.ID, member.ID, 2, 5, 10))
	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: member.ID,
		TeamID: &teamID,
		Key:    "sk-batch-release-" + uuid.NewString(),
		Name:   "batch-release",
		Quota:  2,
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, member.ID, owner.ID, apiKey.ID, &teamID, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(batchID),
		APIKeyID:    apiKey.ID,
		UserID:      owner.ID,
		ActorUserID: member.ID,
		TeamID:      &teamID,
		BatchID:     batchID,
		HoldAmount:  0.5,
		ReservedAt:  reservedAt,
	}
	_, err = repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)
	require.NoError(t, teamRepo.RemoveMember(ctx, teamID, member.ID, time.Now()))
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET deleted_at = NOW(), status = 'disabled' WHERE id = $1`, apiKey.ID)
	require.NoError(t, err)

	releaseCommand := *reserveCommand
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(batchID)
	releaseCommand.AllowanceReserved = true
	releaseCommand.RequestFingerprint = ""
	result, err := repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)
	require.True(t, result.Applied)

	var balance, frozen, dailyUsage float64
	var allowanceReserved bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance, frozen_balance FROM users WHERE id = $1`, owner.ID).Scan(&balance, &frozen))
	require.InDelta(t, 10, balance, 0.000001)
	require.InDelta(t, 0, frozen, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd FROM team_memberships WHERE team_id = $1 AND user_id = $2`, teamID, member.ID).Scan(&dailyUsage))
	require.InDelta(t, 0, dailyUsage, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT allowance_reserved FROM batch_image_jobs WHERE batch_id = $1`, batchID).Scan(&allowanceReserved))
	require.False(t, allowanceReserved)
}

func TestUsageBillingRepositoryBatchImageReleaseOnlyUpdatesReservedMembership(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	teamRepo := NewTeamRepository(integrationDB)
	repo := NewUsageBillingRepository(client, integrationDB)
	owner := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("release-cycle-owner"), Balance: 10})
	member := mustCreateUser(t, client, &service.User{Email: uniqueTeamTestEmail("release-cycle-member")})
	teamCtx, err := teamRepo.Create(ctx, "退款成员周期团队", owner.ID, 5)
	require.NoError(t, err)
	firstToken := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamCtx.Team.ID, owner.ID, member.Email, firstToken, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = teamRepo.ResolveInvitation(ctx, firstToken, member.ID, member.Email, "accepted", time.Now())
	require.NoError(t, err)
	require.NoError(t, teamRepo.UpdateMemberLimits(ctx, teamCtx.Team.ID, member.ID, 2, 5, 10))
	teamID := teamCtx.Team.ID
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: member.ID,
		TeamID: &teamID,
		Key:    "sk-batch-release-cycle-" + uuid.NewString(),
		Name:   "batch-release-cycle",
		Quota:  2,
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, member.ID, owner.ID, apiKey.ID, &teamID, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(batchID),
		APIKeyID:    apiKey.ID,
		UserID:      owner.ID,
		ActorUserID: member.ID,
		TeamID:      &teamID,
		BatchID:     batchID,
		HoldAmount:  0.5,
		ReservedAt:  reservedAt,
	}
	_, err = repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)

	leftAt := reservedAt.Add(time.Millisecond)
	require.NoError(t, teamRepo.RemoveMember(ctx, teamID, member.ID, leftAt))
	secondToken := uuid.NewString()
	_, err = teamRepo.CreateInvitation(ctx, teamID, owner.ID, member.Email, secondToken, leftAt.Add(time.Hour))
	require.NoError(t, err)
	joinedAgainAt := leftAt.Add(time.Millisecond)
	_, err = teamRepo.ResolveInvitation(ctx, secondToken, member.ID, member.Email, "accepted", joinedAgainAt)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
		UPDATE team_memberships
		SET daily_usage_usd = 0.4, daily_window_start = date_trunc('day', $3::timestamptz)
		WHERE team_id = $1 AND user_id = $2 AND left_at IS NULL`, teamID, member.ID, reservedAt)
	require.NoError(t, err)

	releaseCommand := *reserveCommand
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(batchID)
	releaseCommand.AllowanceReserved = true
	releaseCommand.RequestFingerprint = ""
	_, err = repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)

	rows, err := integrationDB.QueryContext(ctx, `
		SELECT daily_usage_usd FROM team_memberships
		WHERE team_id = $1 AND user_id = $2 ORDER BY joined_at`, teamID, member.ID)
	require.NoError(t, err)
	defer rows.Close()
	var usages []float64
	for rows.Next() {
		var usage float64
		require.NoError(t, rows.Scan(&usage))
		usages = append(usages, usage)
	}
	require.NoError(t, rows.Err())
	require.Len(t, usages, 2)
	require.InDelta(t, 0, usages[0], 0.000001)
	require.InDelta(t, 0.4, usages[1], 0.000001)
}

func TestUsageBillingRepositoryBatchImageReleaseKeepsNewWindowConservative(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("batch-window-%d@example.com", time.Now().UnixNano()),
		Balance: 10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:      user.ID,
		Key:         "sk-batch-window-" + uuid.NewString(),
		Name:        "batch-window",
		Quota:       2,
		RateLimit5h: 2,
	})
	batchID := "imgbatch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	reservedAt := time.Now().UTC()
	insertBatchImageAllowanceTestJob(t, batchID, user.ID, user.ID, apiKey.ID, nil, reservedAt)
	reserveCommand := &service.BatchImageBalanceHoldCommand{
		RequestID:   service.BatchImageHoldRequestID(batchID),
		APIKeyID:    apiKey.ID,
		UserID:      user.ID,
		ActorUserID: user.ID,
		BatchID:     batchID,
		HoldAmount:  0.5,
		ReservedAt:  reservedAt,
	}
	_, err := repo.ReserveBatchImageBalance(ctx, reserveCommand)
	require.NoError(t, err)
	// 模拟额度窗口已经滚动，新窗口中的 0.5 不能被旧任务退款减掉。
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET window_5h_start = $2, usage_5h = 0.5 WHERE id = $1`, apiKey.ID, reservedAt.Add(6*time.Hour))
	require.NoError(t, err)

	releaseCommand := *reserveCommand
	releaseCommand.RequestID = service.BatchImageReleaseRequestID(batchID)
	releaseCommand.AllowanceReserved = true
	releaseCommand.RequestFingerprint = ""
	_, err = repo.ReleaseBatchImageBalance(ctx, &releaseCommand)
	require.NoError(t, err)

	var quotaUsed, usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT quota_used, usage_5h FROM api_keys WHERE id = $1`, apiKey.ID).Scan(&quotaUsed, &usage5h))
	require.InDelta(t, 0, quotaUsed, 0.000001)
	require.InDelta(t, 0.5, usage5h, 0.000001)
}

func TestUsageBillingRepositoryApply_DeduplicatesBalanceBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      1,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-" + uuid.NewString(),
		Name:   "billing",
		Quota:  1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:           requestID,
		APIKeyID:            apiKey.ID,
		UserID:              user.ID,
		AccountID:           account.ID,
		AccountType:         service.AccountTypeAPIKey,
		BillableAmountUSD:   1.25,
		APIKeyQuotaCost:     1.25,
		APIKeyRateLimitCost: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result1)
	require.True(t, result1.Applied)
	require.True(t, result1.APIKeyQuotaExhausted)
	require.InDelta(t, 1.25, result1.BalanceAmountUSD, 0.000001)
	require.NotNil(t, result1.NewBalance)
	require.InDelta(t, -0.25, *result1.NewBalance, 0.000001)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.NotNil(t, result2)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, -0.25, balance, 0.000001)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id = $1", apiKey.ID).Scan(&quotaUsed))
	require.InDelta(t, 1.25, quotaUsed, 0.000001)

	var usage5h float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT usage_5h FROM api_keys WHERE id = $1", apiKey.ID).Scan(&usage5h))
	require.InDelta(t, 1.25, usage5h, 0.000001)

	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM api_keys WHERE id = $1", apiKey.ID).Scan(&status))
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)

	var dedupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, apiKey.ID).Scan(&dedupCount))
	require.Equal(t, 1, dedupCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesSubscriptionBilling(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-group-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-plan-" + uuid.NewString(),
		Description:     "usage billing test plan",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(200),
		MonthlyLimitUSD: float64Ptr(300),
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-sub-" + uuid.NewString(),
		Name:    "billing-sub",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          plan.ID,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(200),
		MonthlyLimitUSD: float64Ptr(300),
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:         requestID,
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		AccountID:         0,
		BillableAmountUSD: 2.5,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 2.5, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryResolveUsableSubscriptionForGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-resolve-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-resolve-group-" + uuid.NewString(),
		Platform: service.PlatformOpenAI,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-resolve-plan-" + uuid.NewString(),
		Description:     "usage billing resolve test plan",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		GroupIDs:        []int64{group.ID},
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(1000),
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          plan.ID,
		DailyLimitUSD:   float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(1000),
	})

	got, err := repo.ResolveUsableSubscriptionForGroup(ctx, user.ID, group.ID)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, subscription.ID, got.ID)
	require.Equal(t, plan.ID, got.PlanID)
}

func TestUsageBillingRepositoryApply_DeductsBalanceDeficitAfterPartialSubscription(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-sub-deficit-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      0,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-deficit-plan-" + uuid.NewString(),
		Description:     "usage billing deficit test plan",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(10),
		WeeklyLimitUSD:  float64Ptr(10),
		MonthlyLimitUSD: float64Ptr(10),
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-sub-deficit-" + uuid.NewString(),
		Name:   "billing-sub-deficit",
	})
	windowStart := time.Now().Add(-time.Hour)
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:             user.ID,
		PlanID:             plan.ID,
		DailyWindowStart:   &windowStart,
		WeeklyWindowStart:  &windowStart,
		MonthlyWindowStart: &windowStart,
		DailyLimitUSD:      float64Ptr(10),
		WeeklyLimitUSD:     float64Ptr(10),
		MonthlyLimitUSD:    float64Ptr(10),
		DailyUsageUSD:      9.99,
		WeeklyUsageUSD:     9.99,
		MonthlyUsageUSD:    9.99,
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         uuid.NewString(),
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		BillableAmountUSD: 1.25,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 0.01, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 1.24, result.BalanceAmountUSD, 0.000001)
	require.NotNil(t, result.NewBalance)
	require.InDelta(t, -1.24, *result.NewBalance, 0.000001)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, -1.24, balance, 0.000001)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 10.0, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryApply_PricesByActualSubscriptionAllocations(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-allocation-rate-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      10,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-allocation-rate-group-" + uuid.NewString(),
		Platform: service.PlatformOpenAI,
	})
	planA := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:                 "usage-billing-allocation-rate-plan-a-" + uuid.NewString(),
		Description:          "discounted plan with little remaining quota",
		Price:                19.9,
		ValidityDays:         30,
		ValidityUnit:         "day",
		GroupIDs:             []int64{group.ID},
		GroupRateMultipliers: map[int64]float64{group.ID: 0.5},
		ForSale:              true,
		DailyLimitUSD:        float64Ptr(1),
		WeeklyLimitUSD:       float64Ptr(1),
		MonthlyLimitUSD:      float64Ptr(1),
	})
	planB := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:                 "usage-billing-allocation-rate-plan-b-" + uuid.NewString(),
		Description:          "higher-rate plan",
		Price:                19.9,
		ValidityDays:         30,
		ValidityUnit:         "day",
		GroupIDs:             []int64{group.ID},
		GroupRateMultipliers: map[int64]float64{group.ID: 2},
		ForSale:              true,
		DailyLimitUSD:        float64Ptr(100),
		WeeklyLimitUSD:       float64Ptr(100),
		MonthlyLimitUSD:      float64Ptr(100),
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-allocation-rate-" + uuid.NewString(),
		Name:    "billing-allocation-rate",
	})
	subA := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          planA.ID,
		DailyLimitUSD:   float64Ptr(1),
		WeeklyLimitUSD:  float64Ptr(1),
		MonthlyLimitUSD: float64Ptr(1),
	})
	subB := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          planB.ID,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:                       uuid.NewString(),
		APIKeyID:                        apiKey.ID,
		UserID:                          user.ID,
		GroupID:                         &group.ID,
		BillableAmountUSD:               1.5,
		BaseAmountUSD:                   3,
		SubscriptionRateMultiplier:      1,
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           1,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 3.0, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.0, result.BalanceAmountUSD, 0.000001)

	var usageA float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subA.ID).Scan(&usageA))
	require.InDelta(t, 1.0, usageA, 0.000001)

	var usageB float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subB.ID).Scan(&usageB))
	require.InDelta(t, 2.0, usageB, 0.000001)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 10.0, balance, 0.000001)
}

func TestUsageBillingRepositoryApply_UsesBalanceRateAfterPartialSubscription(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-partial-rate-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      10,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-partial-rate-group-" + uuid.NewString(),
		Platform: service.PlatformOpenAI,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:                 "usage-billing-partial-rate-plan-" + uuid.NewString(),
		Description:          "subscription covers only part of the base amount",
		Price:                19.9,
		ValidityDays:         30,
		ValidityUnit:         "day",
		GroupIDs:             []int64{group.ID},
		GroupRateMultipliers: map[int64]float64{group.ID: 0.5},
		ForSale:              true,
		DailyLimitUSD:        float64Ptr(1),
		WeeklyLimitUSD:       float64Ptr(1),
		MonthlyLimitUSD:      float64Ptr(1),
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-partial-rate-" + uuid.NewString(),
		Name:    "billing-partial-rate",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          plan.ID,
		DailyLimitUSD:   float64Ptr(1),
		WeeklyLimitUSD:  float64Ptr(1),
		MonthlyLimitUSD: float64Ptr(1),
	})

	// 套餐以 0.5x 覆盖 2 美元基础费用，剩余 1 美元按非 token 的 0.25x 从余额扣除。
	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:                       uuid.NewString(),
		APIKeyID:                        apiKey.ID,
		UserID:                          user.ID,
		GroupID:                         &group.ID,
		BillableAmountUSD:               0.75,
		BaseAmountUSD:                   3,
		SubscriptionRateMultiplier:      0.25,
		SubscriptionRateMultiplierScale: 1,
		BalanceRateMultiplier:           0.25,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 1.0, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.25, result.BalanceAmountUSD, 0.000001)
	require.NotNil(t, result.EffectiveRateMultiplier)
	require.InDelta(t, 1.25/3.0, *result.EffectiveRateMultiplier, 0.000001)

	var usage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&usage))
	require.InDelta(t, 1.0, usage, 0.000001)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 9.75, balance, 0.000001)
}

func TestUsageBillingRepositoryApply_UsesOnlySubscriptionPlansContainingRequestGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-plan-group-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      10,
	})
	groupA := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-plan-group-a-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	groupB := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-plan-group-b-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	planA := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-plan-a-" + uuid.NewString(),
		Description:     "plan for group A",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})
	planB := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-plan-b-" + uuid.NewString(),
		Description:     "plan for group B",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO subscription_plan_groups (plan_id, group_id)
		VALUES ($1, $2), ($3, $4)
	`, planA.ID, groupA.ID, planB.ID, groupB.ID)
	require.NoError(t, err)

	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &groupB.ID,
		Key:     "sk-usage-billing-plan-group-" + uuid.NewString(),
		Name:    "billing-plan-group",
	})
	subA := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          planA.ID,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})
	subB := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          planB.ID,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         uuid.NewString(),
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		GroupID:           &groupB.ID,
		BillableAmountUSD: 2.5,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 2.5, result.SubscriptionAmountUSD, 0.000001)

	var usageA float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subA.ID).Scan(&usageA))
	require.InDelta(t, 0.0, usageA, 0.000001)

	var usageB float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subB.ID).Scan(&usageB))
	require.InDelta(t, 2.5, usageB, 0.000001)
}

func TestUsageBillingRepositoryApply_GlobalPlanAppliesToNewRequestGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-global-plan-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      10,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:            "usage-billing-global-plan-" + uuid.NewString(),
		Description:     "global plan applies to future groups",
		Price:           19.9,
		ValidityDays:    30,
		ValidityUnit:    "day",
		ForSale:         true,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:     "usage-billing-new-group-" + uuid.NewString(),
		Platform: service.PlatformAnthropic,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID:  user.ID,
		GroupID: &group.ID,
		Key:     "sk-usage-billing-global-plan-" + uuid.NewString(),
		Name:    "billing-global-plan",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		PlanID:          plan.ID,
		DailyLimitUSD:   float64Ptr(100),
		WeeklyLimitUSD:  float64Ptr(100),
		MonthlyLimitUSD: float64Ptr(100),
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         uuid.NewString(),
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		GroupID:           &group.ID,
		BillableAmountUSD: 2.5,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 2.5, result.SubscriptionAmountUSD, 0.000001)

	var usage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&usage))
	require.InDelta(t, 2.5, usage, 0.000001)
}

func TestUsageBillingRepositoryApply_UnlimitedSubscriptionDoesNotDeductBalance(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-unlimited-sub-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      0,
	})
	plan := mustCreatePlan(t, client, &service.SubscriptionPlan{
		Name:         "usage-billing-unlimited-plan-" + uuid.NewString(),
		Description:  "usage billing unlimited subscription test plan",
		Price:        19.9,
		ValidityDays: 30,
		ValidityUnit: "day",
		ForSale:      true,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-unlimited-sub-" + uuid.NewString(),
		Name:   "billing-unlimited-sub",
	})
	subscription := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID: user.ID,
		PlanID: plan.ID,
	})

	result, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         uuid.NewString(),
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		BillableAmountUSD: 1.25,
	})

	require.NoError(t, err)
	require.True(t, result.Applied)
	require.InDelta(t, 1.25, result.SubscriptionAmountUSD, 0.000001)
	require.InDelta(t, 0.0, result.BalanceAmountUSD, 0.000001)
	require.Nil(t, result.NewBalance)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 0.0, balance, 0.000001)

	var dailyUsage float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_subscriptions WHERE id = $1", subscription.ID).Scan(&dailyUsage))
	require.InDelta(t, 0.0, dailyUsage, 0.000001)
}

func TestUsageBillingRepositoryApply_RequestFingerprintConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-conflict-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-conflict-" + uuid.NewString(),
		Name:   "billing-conflict",
	})

	requestID := uuid.NewString()
	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         requestID,
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		BillableAmountUSD: 1.25,
	})
	require.NoError(t, err)

	_, err = repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:         requestID,
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		BillableAmountUSD: 2.50,
	})
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
}

func TestUsageBillingRepositoryApply_UpdatesAccountQuota(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-account-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-account-" + uuid.NewString(),
		Name:   "billing-account",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "usage-billing-account-quota-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
		Extra: map[string]any{
			"quota_limit": 100.0,
		},
	})

	_, err := repo.Apply(ctx, &service.UsageBillingCommand{
		RequestID:        uuid.NewString(),
		APIKeyID:         apiKey.ID,
		UserID:           user.ID,
		AccountID:        account.ID,
		AccountType:      service.AccountTypeAPIKey,
		AccountQuotaCost: 3.5,
	})
	require.NoError(t, err)

	var quotaUsed float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE((extra->>'quota_used')::numeric, 0) FROM accounts WHERE id = $1", account.ID).Scan(&quotaUsed))
	require.InDelta(t, 3.5, quotaUsed, 0.000001)
}

func TestUsageBillingRepositoryApply_EnqueuesSchedulerOutboxOnQuotaCrossing(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)

	newFixture := func(t *testing.T, extra map[string]any) (int64, int64) {
		t.Helper()
		user := mustCreateUser(t, client, &service.User{
			Email:        fmt.Sprintf("usage-billing-outbox-user-%d-%s@example.com", time.Now().UnixNano(), uuid.NewString()),
			PasswordHash: "hash",
		})
		apiKey := mustCreateApiKey(t, client, &service.APIKey{
			UserID: user.ID,
			Key:    "sk-usage-billing-outbox-" + uuid.NewString(),
			Name:   "billing-outbox",
		})
		account := mustCreateAccount(t, client, &service.Account{
			Name:  "usage-billing-outbox-" + uuid.NewString(),
			Type:  service.AccountTypeAPIKey,
			Extra: extra,
		})
		return apiKey.ID, account.ID
	}

	outboxCountFor := func(t *testing.T, accountID int64) int {
		t.Helper()
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1 AND account_id = $2",
			service.SchedulerOutboxEventAccountChanged, accountID,
		).Scan(&count))
		return count
	}

	t.Run("daily_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_daily_limit": 10.0,
		})
		// 第一次低于日限额：不应入队 outbox
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 4,
		})
		require.NoError(t, err)
		require.Equal(t, 0, outboxCountFor(t, accountID), "below limit should not enqueue")

		// 第二次跨越日限额：应入队一次 outbox
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 8,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "crossing daily limit should enqueue once")

		// 再次递增（已超）：不应重复入队
		_, err = repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 2,
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "subsequent increments beyond limit should not re-enqueue")
	})

	t.Run("weekly_first_crossing_enqueues", func(t *testing.T) {
		apiKeyID, accountID := newFixture(t, map[string]any{
			"quota_weekly_limit": 10.0,
		})
		_, err := repo.Apply(ctx, &service.UsageBillingCommand{
			RequestID:        uuid.NewString(),
			APIKeyID:         apiKeyID,
			AccountID:        accountID,
			AccountType:      service.AccountTypeAPIKey,
			AccountQuotaCost: 15, // 单次即跨越
		})
		require.NoError(t, err)
		require.Equal(t, 1, outboxCountFor(t, accountID), "single-shot crossing weekly limit should enqueue once")
	})
}

func TestDashboardAggregationRepositoryCleanupUsageBillingDedup_BatchDeletesOldRows(t *testing.T) {
	ctx := context.Background()
	repo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	oldRequestID := "dedup-old-" + uuid.NewString()
	newRequestID := "dedup-new-" + uuid.NewString()
	oldCreatedAt := time.Now().UTC().AddDate(0, 0, -400)
	newCreatedAt := time.Now().UTC().Add(-time.Hour)

	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint, created_at)
		VALUES ($1, 1, $2, $3), ($4, 1, $5, $6)
	`,
		oldRequestID, strings.Repeat("a", 64), oldCreatedAt,
		newRequestID, strings.Repeat("b", 64), newCreatedAt,
	)
	require.NoError(t, err)

	require.NoError(t, repo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	var oldCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", oldRequestID).Scan(&oldCount))
	require.Equal(t, 0, oldCount)

	var newCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1", newRequestID).Scan(&newCount))
	require.Equal(t, 1, newCount)

	var archivedCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup_archive WHERE request_id = $1", oldRequestID).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestUsageBillingRepositoryApply_DeduplicatesAgainstArchivedKey(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	aggRepo := newDashboardAggregationRepositoryWithSQL(integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("usage-billing-archive-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-usage-billing-archive-" + uuid.NewString(),
		Name:   "billing-archive",
	})

	requestID := uuid.NewString()
	cmd := &service.UsageBillingCommand{
		RequestID:         requestID,
		APIKeyID:          apiKey.ID,
		UserID:            user.ID,
		BillableAmountUSD: 1.25,
	}

	result1, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result1.Applied)

	_, err = integrationDB.ExecContext(ctx, `
		UPDATE usage_billing_dedup
		SET created_at = $1
		WHERE request_id = $2 AND api_key_id = $3
	`, time.Now().UTC().AddDate(0, 0, -400), requestID, apiKey.ID)
	require.NoError(t, err)
	require.NoError(t, aggRepo.CleanupUsageBillingDedup(ctx, time.Now().UTC().AddDate(0, 0, -365)))

	result2, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result2.Applied)

	var balance float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
}
