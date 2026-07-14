package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
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

type openAIOAuthCapacityGroupRepoStub struct {
	GroupRepository
	groups []Group
	err    error
	calls  int
	filter struct {
		platform  string
		status    string
		page      int
		pageSize  int
		sortBy    string
		sortOrder string
	}
}

func (s *openAIOAuthCapacityGroupRepoStub) ListWithFilters(
	_ context.Context,
	params pagination.PaginationParams,
	platform, status, _ string,
	_ *bool,
) ([]Group, *pagination.PaginationResult, error) {
	s.calls++
	s.filter.platform = platform
	s.filter.status = status
	s.filter.page = params.Page
	s.filter.pageSize = params.PageSize
	s.filter.sortBy = params.SortBy
	s.filter.sortOrder = params.SortOrder
	if s.err != nil {
		return nil, nil, s.err
	}
	start := (params.Page - 1) * params.PageSize
	if start >= len(s.groups) {
		return []Group{}, &pagination.PaginationResult{Total: int64(len(s.groups))}, nil
	}
	end := start + params.PageSize
	if end > len(s.groups) {
		end = len(s.groups)
	}
	return append([]Group(nil), s.groups[start:end]...), &pagination.PaginationResult{Total: int64(len(s.groups))}, nil
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

func TestBuildOpenAIOAuthPoolCapacityAggregatesOpenAIGroups(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	groups := []Group{
		{ID: 10, Name: "Alpha", Platform: PlatformOpenAI, Status: StatusActive, SortOrder: 20},
		{ID: 20, Name: "Beta", Platform: PlatformOpenAI, Status: StatusDisabled, SortOrder: 10},
		{ID: 30, Name: "Empty", Platform: PlatformOpenAI, Status: StatusActive, SortOrder: 30},
		{ID: 40, Name: "Other platform", Platform: PlatformAnthropic, Status: StatusActive},
	}
	account := func(id int64, plan string, groupIDs ...int64) Account {
		return Account{
			ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true, GroupIDs: groupIDs,
			Credentials: map[string]any{"plan_type": plan}, Extra: map[string]any{},
		}
	}
	accounts := []Account{
		account(1, "pro", 10, 20, 20),
		account(2, "free"),
		account(3, "plus", 999),
		account(4, "enterprise", 10),
		account(5, "team", 20),
		account(6, "k12", 10),
		account(7, "plus"),
	}
	accounts[4].Status = StatusDisabled
	parentID := int64(1)
	accounts[5].ParentAccountID = &parentID
	accounts[6].AccountGroups = []AccountGroup{{GroupID: 10}}

	result := buildOpenAIOAuthPoolCapacityWithGroups(context.Background(), accounts, groups, now, nil)

	require.Equal(t, 7, result.ManagedAccountCount)
	require.Equal(t, 4, result.IncludedAccountCount)
	require.Equal(t, 2685.0, result.Totals.Parent.EstimatedLimitUSD)
	require.Len(t, result.Groups, 4)
	require.Equal(t, []int64{0, 20, 10, 30}, []int64{
		result.Groups[0].GroupID,
		result.Groups[1].GroupID,
		result.Groups[2].GroupID,
		result.Groups[3].GroupID,
	})

	ungrouped := result.Groups[0]
	require.Equal(t, 1, ungrouped.ManagedAccountCount)
	require.Equal(t, 1, ungrouped.IncludedAccountCount)
	require.Equal(t, 5.0, ungrouped.Totals.Parent.EstimatedLimitUSD)

	beta := result.Groups[1]
	require.Equal(t, StatusDisabled, beta.GroupStatus)
	require.Equal(t, 2, beta.ManagedAccountCount)
	require.Equal(t, 1, beta.IncludedAccountCount)
	require.Equal(t, 1, beta.ExcludedAccountCount)
	require.Equal(t, 2400.0, beta.Totals.Parent.EstimatedLimitUSD)

	alpha := result.Groups[2]
	require.Equal(t, 4, alpha.ManagedAccountCount)
	require.Equal(t, 2, alpha.IncludedAccountCount)
	require.Equal(t, 1, alpha.ShadowAccountCount)
	require.Equal(t, 1, alpha.UnknownPlanAccountCount)
	require.Equal(t, 2540.0, alpha.Totals.Parent.EstimatedLimitUSD)

	empty := result.Groups[3]
	require.Zero(t, empty.ManagedAccountCount)
	require.Zero(t, empty.Totals.Parent.EstimatedLimitUSD)
	require.Len(t, empty.Plans, len(openAIOAuthCapacityPlanRules))
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

func TestGetOpenAIOAuthPoolCapacityUsesFilteredAccountAndGroupQueries(t *testing.T) {
	repo := &openAIOAuthCapacityAccountRepoStub{accounts: []Account{}}
	groupRepo := &openAIOAuthCapacityGroupRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: groupRepo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, PlatformOpenAI, repo.filters.platform)
	require.Equal(t, AccountTypeOAuth, repo.filters.accountType)
	require.Empty(t, repo.filters.status)
	require.Zero(t, repo.filters.groupID)
	require.Equal(t, 1, groupRepo.calls)
	require.Equal(t, PlatformOpenAI, groupRepo.filter.platform)
	require.Empty(t, groupRepo.filter.status)
	require.Equal(t, 1, groupRepo.filter.page)
	require.Equal(t, 1000, groupRepo.filter.pageSize)
	require.Equal(t, "sort_order", groupRepo.filter.sortBy)
	require.Equal(t, pagination.SortOrderAsc, groupRepo.filter.sortOrder)
	require.Len(t, result.Plans, 5)
	require.Len(t, result.Groups, 1)
}

func TestGetOpenAIOAuthPoolCapacityLoadsAllGroupPages(t *testing.T) {
	groups := make([]Group, openAIOAuthCapacityGroupPageSize+1)
	for i := range groups {
		groups[i] = Group{
			ID: int64(i + 1), Name: fmt.Sprintf("group-%d", i+1),
			Platform: PlatformOpenAI, Status: StatusActive, SortOrder: i,
		}
	}
	account := Account{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		GroupIDs:    []int64{int64(len(groups))},
		Credentials: map[string]any{"plan_type": "plus"}, Extra: map[string]any{},
	}
	repo := &openAIOAuthCapacityAccountRepoStub{accounts: []Account{account}}
	groupRepo := &openAIOAuthCapacityGroupRepoStub{groups: groups}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: groupRepo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, groupRepo.calls)
	require.Len(t, result.Groups, len(groups)+1)
	require.Zero(t, result.Groups[0].ManagedAccountCount)
	last := result.Groups[len(result.Groups)-1]
	require.Equal(t, int64(len(groups)), last.GroupID)
	require.Equal(t, 1, last.ManagedAccountCount)
	require.Equal(t, 140.0, last.Totals.Parent.EstimatedLimitUSD)
}

func TestGetOpenAIOAuthPoolCapacityReturnsRepositoryError(t *testing.T) {
	repo := &openAIOAuthCapacityAccountRepoStub{err: errors.New("query failed")}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.Nil(t, result)
	require.ErrorContains(t, err, "list OpenAI OAuth accounts for capacity")
}

func TestGetOpenAIOAuthPoolCapacityReturnsGroupRepositoryError(t *testing.T) {
	repo := &openAIOAuthCapacityAccountRepoStub{}
	groupRepo := &openAIOAuthCapacityGroupRepoStub{err: errors.New("group query failed")}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: groupRepo}

	result, err := svc.GetOpenAIOAuthPoolCapacity(context.Background())

	require.Nil(t, result)
	require.ErrorContains(t, err, "list OpenAI groups for capacity")
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
