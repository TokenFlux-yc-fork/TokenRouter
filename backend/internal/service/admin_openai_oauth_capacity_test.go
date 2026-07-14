package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIOAuthCapacityAccountRepoStub struct {
	AccountRepository
	accounts []Account
	err      error
	filters  struct {
		platform    string
		accountType string
		status      string
		groupID     int64
	}
}

func (s *openAIOAuthCapacityAccountRepoStub) ListAllWithFilters(
	_ context.Context,
	platform, accountType, status, _ string,
	groupID int64,
	_ string,
) ([]Account, error) {
	s.filters.platform = platform
	s.filters.accountType = accountType
	s.filters.status = status
	s.filters.groupID = groupID
	return append([]Account(nil), s.accounts...), s.err
}

func TestBuildOpenAIOAuthPoolCapacityAggregatesPlansAndWindows(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	stale := now.Add(-9 * time.Hour).Format(time.RFC3339)
	pastReset := now.Add(-time.Minute).Format(time.RFC3339)
	expired := now.Add(-time.Minute)
	futureReset := now.Add(time.Hour)
	parentID := int64(1)

	account := func(id int64, plan string, extra map[string]any) Account {
		if extra == nil {
			extra = map[string]any{}
		}
		return Account{
			ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"plan_type": plan}, Extra: extra,
		}
	}
	accounts := []Account{
		account(1, "k12", map[string]any{
			"codex_5h_used_percent": 20.0, "codex_7d_used_percent": 50.0,
			"codex_usage_updated_at": fresh,
		}),
		account(2, "pro", nil),
		account(3, "self_serve_business_usage_based", map[string]any{
			"codex_5h_used_percent": 120.0, "codex_7d_used_percent": -5.0,
			"codex_usage_updated_at": stale,
		}),
		account(4, "plus", map[string]any{
			"codex_7d_used_percent": 80.0, "codex_7d_reset_at": pastReset,
			"codex_usage_updated_at": fresh,
		}),
		account(5, "free", map[string]any{
			"codex_5h_used_percent": 50.0, "codex_7d_used_percent": 40.0,
			"codex_usage_updated_at": fresh,
		}),
		account(6, "enterprise", nil),
		account(7, "plus", nil),
		account(8, "free", nil),
		account(9, "team", nil),
		{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		account(11, "pro", nil),
	}
	accounts[6].Status = StatusDisabled
	accounts[7].AutoPauseOnExpired = true
	accounts[7].ExpiresAt = &expired
	accounts[8].ParentAccountID = &parentID
	accounts[10].RateLimitResetAt = &futureReset

	result := buildOpenAIOAuthPoolCapacity(context.Background(), accounts, now, nil)

	require.Equal(t, "2026-07-14T12:00:00Z", result.GeneratedAt)
	require.Equal(t, 0.15, result.FiveHourRatio)
	require.Equal(t, 10, result.ManagedAccountCount)
	require.Equal(t, 5, result.IncludedAccountCount)
	require.Equal(t, 3, result.ExcludedAccountCount)
	require.Equal(t, 1, result.ShadowAccountCount)
	require.Equal(t, 1, result.UnknownPlanAccountCount)
	require.Equal(t, []OpenAIOAuthPoolCapacityUnknownPlanType{{PlanType: "enterprise", AccountCount: 1}}, result.UnknownPlanTypes)

	require.Equal(t, 2745.0, result.Totals.Parent.EstimatedLimitUSD)
	require.Equal(t, 52.0, result.Totals.Parent.EstimatedUsedUSD)
	require.Equal(t, 2693.0, result.Totals.Parent.EstimatedRemainingUSD)
	require.Equal(t, 53.0, result.Totals.Parent.ObservedRemainingUSD)
	require.Equal(t, 2640.0, result.Totals.Parent.UnobservedLimitUSD)
	require.Equal(t, 2, result.Totals.Parent.ObservedAccountCount)
	require.Equal(t, 1, result.Totals.Parent.MissingSnapshotCount)
	require.Equal(t, 2, result.Totals.Parent.StaleSnapshotCount)

	require.Equal(t, 411.75, result.Totals.FiveHour.EstimatedLimitUSD)
	require.Equal(t, 18.375, result.Totals.FiveHour.EstimatedUsedUSD)
	require.Equal(t, 393.375, result.Totals.FiveHour.EstimatedRemainingUSD)
	require.Equal(t, 12.375, result.Totals.FiveHour.ObservedRemainingUSD)
	require.Equal(t, 396.0, result.Totals.FiveHour.UnobservedLimitUSD)
	require.Equal(t, 2, result.Totals.FiveHour.ObservedAccountCount)
	require.Equal(t, 2, result.Totals.FiveHour.MissingSnapshotCount)
	require.Equal(t, 1, result.Totals.FiveHour.StaleSnapshotCount)

	require.Equal(t, 2740.0, result.Totals.Weekly.EstimatedLimitUSD)
	require.Equal(t, 2690.0, result.Totals.Weekly.EstimatedRemainingUSD)
	require.Equal(t, 5.0, result.Totals.Monthly.EstimatedLimitUSD)
	require.Equal(t, 3.0, result.Totals.Monthly.EstimatedRemainingUSD)

	require.Len(t, result.Plans, 5)
	require.Equal(t, []string{"k12", "pro", "team", "plus", "free"}, []string{
		result.Plans[0].PlanType,
		result.Plans[1].PlanType,
		result.Plans[2].PlanType,
		result.Plans[3].PlanType,
		result.Plans[4].PlanType,
	})
	require.Equal(t, "monthly", result.Plans[4].Period)
	require.Equal(t, 0.75, result.Plans[4].FiveHourLimitPerAccountUSD)
}

func TestGetOpenAIOAuthPoolCapacityExcludesQuotaAutoPausedAccounts(t *testing.T) {
	now := time.Now()
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"plan_type": "plus"},
		Extra: map[string]any{
			"codex_5h_used_percent":  96.0,
			"codex_usage_updated_at": now.Format(time.RFC3339),
		},
	}
	repo := &openAIOAuthCapacityAccountRepoStub{accounts: []Account{account}}
	settingService := &SettingService{}
	settingService.SetOpenAIQuotaAutoPauseSettings(OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.95})
	svc := &adminServiceImpl{accountRepo: repo, settingService: settingService}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.Zero(t, result.IncludedAccountCount)
	require.Equal(t, 1, result.ExcludedAccountCount)
	require.Zero(t, result.Totals.Parent.EstimatedLimitUSD)

	repo.accounts[0].Extra["auto_pause_5h_disabled"] = true
	result, err = svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, result.IncludedAccountCount)
	require.Zero(t, result.ExcludedAccountCount)
	require.Equal(t, 140.0, result.Totals.Parent.EstimatedLimitUSD)
}

func TestGetOpenAIOAuthPoolCapacityExcludesRuntimeBlockedAccounts(t *testing.T) {
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"plan_type": "plus"},
	}
	repo := &openAIOAuthCapacityAccountRepoStub{accounts: []Account{account}}
	runtimeBlocker := &OpenAIGatewayService{}
	runtimeBlocker.BlockAccountScheduling(&account, time.Now().Add(time.Minute), "test")
	svc := &adminServiceImpl{accountRepo: repo, runtimeBlocker: runtimeBlocker}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.Zero(t, result.IncludedAccountCount)
	require.Equal(t, 1, result.ExcludedAccountCount)

	runtimeBlocker.ClearAccountSchedulingBlock(account.ID)
	result, err = svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, result.IncludedAccountCount)
	require.Zero(t, result.ExcludedAccountCount)
}

func TestBuildOpenAIOAuthPoolCapacityTreatsNonFiniteUsageAsUnobserved(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	accounts := []Account{{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"plan_type": "plus"},
		Extra: map[string]any{
			"codex_5h_used_percent":  "NaN",
			"codex_7d_used_percent":  "+Inf",
			"codex_usage_updated_at": now.Format(time.RFC3339),
		},
	}}

	result := buildOpenAIOAuthPoolCapacity(context.Background(), accounts, now, nil)

	require.Equal(t, 140.0, result.Totals.Parent.EstimatedRemainingUSD)
	require.Equal(t, 140.0, result.Totals.Parent.UnobservedLimitUSD)
	require.Equal(t, 1, result.Totals.Parent.MissingSnapshotCount)
	require.Equal(t, 21.0, result.Totals.FiveHour.EstimatedRemainingUSD)
	require.Equal(t, 21.0, result.Totals.FiveHour.UnobservedLimitUSD)
	require.Equal(t, 1, result.Totals.FiveHour.MissingSnapshotCount)
	payload, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "NaN")
}

func TestGetOpenAIOAuthPoolCapacityUsesOneFilteredAccountQuery(t *testing.T) {
	repo := &openAIOAuthCapacityAccountRepoStub{accounts: []Account{}}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, PlatformOpenAI, repo.filters.platform)
	require.Equal(t, AccountTypeOAuth, repo.filters.accountType)
	require.Empty(t, repo.filters.status)
	require.Zero(t, repo.filters.groupID)
	require.Len(t, result.Plans, 5)
}

func TestGetOpenAIOAuthPoolCapacityReturnsRepositoryError(t *testing.T) {
	repo := &openAIOAuthCapacityAccountRepoStub{err: errors.New("query failed")}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.Nil(t, result)
	require.ErrorContains(t, err, "list OpenAI OAuth accounts for capacity")
}

func TestResolveOpenAIOAuthCapacityPlanAliases(t *testing.T) {
	tests := map[string]string{
		" K12 ":                           "k12",
		"chatgpt_edu":                     "k12",
		"PRO":                             "pro",
		"business":                        "team",
		"self_serve_business_usage_based": "team",
		"plus":                            "plus",
		"free":                            "free",
	}
	for input, expected := range tests {
		rule, ok := resolveOpenAIOAuthCapacityPlan(input)
		require.True(t, ok, input)
		require.Equal(t, expected, rule.PlanType, input)
	}
	_, ok := resolveOpenAIOAuthCapacityPlan("enterprise")
	require.False(t, ok)
}

func TestOpenAIOAuthCapacityAccountIncludedRejectsPersistedSchedulingBlocks(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	base := Account{Status: StatusActive, Schedulable: true}

	tests := []struct {
		name    string
		account Account
		want    bool
	}{
		{name: "available", account: base, want: true},
		{name: "rate limited", account: func() Account { a := base; a.RateLimitResetAt = &future; return a }()},
		{name: "temporarily unschedulable", account: func() Account { a := base; a.TempUnschedulableUntil = &future; return a }()},
		{name: "overloaded", account: func() Account { a := base; a.OverloadUntil = &future; return a }()},
		{name: "past runtime block", account: func() Account {
			a := base
			a.RateLimitResetAt = &past
			a.TempUnschedulableUntil = &past
			a.OverloadUntil = &past
			return a
		}(), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, openAIOAuthCapacityAccountIncluded(context.Background(), &tt.account, now, nil))
		})
	}
}

func TestOpenAIOAuthCapacitySnapshotStaleUsesWindowAndReset(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	extra := map[string]any{
		"codex_usage_updated_at": now.Add(-6 * time.Hour).Format(time.RFC3339),
	}
	require.True(t, openAIOAuthCapacitySnapshotStale(extra, "5h", now))
	require.False(t, openAIOAuthCapacitySnapshotStale(extra, "7d", now))

	extra["codex_usage_updated_at"] = now.Add(-time.Minute).Format(time.RFC3339)
	extra["codex_7d_reset_at"] = now.Add(-time.Second).Format(time.RFC3339)
	require.True(t, openAIOAuthCapacitySnapshotStale(extra, "7d", now))

	extra = map[string]any{
		"codex_usage_updated_at":          now.Add(-time.Minute).Format(time.RFC3339),
		"codex_5h_updated_at":             now.Add(-time.Minute).Format(time.RFC3339),
		"codex_window_timestamps_version": 1,
	}
	require.False(t, openAIOAuthCapacitySnapshotStale(extra, "5h", now))
	require.True(t, openAIOAuthCapacitySnapshotStale(extra, "7d", now))

	extra["codex_7d_updated_at"] = now.Add(-9 * time.Hour).Format(time.RFC3339)
	require.True(t, openAIOAuthCapacitySnapshotStale(extra, "7d", now))
}
