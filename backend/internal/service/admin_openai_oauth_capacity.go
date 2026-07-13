package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	openAIOAuthCapacityFiveHourRatio      = 0.15
	openAIOAuthCapacityFiveHourStaleAfter = 5 * time.Hour
	openAIOAuthCapacitySnapshotStaleAfter = 8 * time.Hour
)

type openAIOAuthCapacityPlanRule struct {
	PlanType string
	Period   string
	LimitUSD float64
	Aliases  []string
}

var openAIOAuthCapacityPlanRules = []openAIOAuthCapacityPlanRule{
	{PlanType: "k12", Period: "weekly", LimitUSD: 100, Aliases: []string{"k12", "edu", "chatgpt_edu", "education"}},
	{PlanType: "pro", Period: "weekly", LimitUSD: 2400, Aliases: []string{"pro"}},
	{PlanType: "team", Period: "weekly", LimitUSD: 100, Aliases: []string{"team", "business", "self_serve_business", "self_serve_business_usage_based"}},
	{PlanType: "plus", Period: "weekly", LimitUSD: 140, Aliases: []string{"plus"}},
	{PlanType: "free", Period: "monthly", LimitUSD: 5, Aliases: []string{"free"}},
}

// OpenAIOAuthPoolCapacityWindowSummary contains the estimated capacity for one quota window.
// Missing snapshots contribute their full limit to EstimatedRemainingUSD. Missing and stale
// snapshots are also exposed through UnobservedLimitUSD so callers can distinguish fresh,
// observed headroom from the estimate.
type OpenAIOAuthPoolCapacityWindowSummary struct {
	EstimatedLimitUSD     float64 `json:"estimated_limit_usd"`
	EstimatedUsedUSD      float64 `json:"estimated_used_usd"`
	EstimatedRemainingUSD float64 `json:"estimated_remaining_usd"`
	ObservedRemainingUSD  float64 `json:"observed_remaining_usd"`
	UnobservedLimitUSD    float64 `json:"unobserved_limit_usd"`
	ObservedAccountCount  int     `json:"observed_account_count"`
	MissingSnapshotCount  int     `json:"missing_snapshot_count"`
	StaleSnapshotCount    int     `json:"stale_snapshot_count"`
}

type OpenAIOAuthPoolCapacityPlanSummary struct {
	PlanType                   string                               `json:"plan_type"`
	Period                     string                               `json:"period"`
	AccountCount               int                                  `json:"account_count"`
	LimitPerAccountUSD         float64                              `json:"limit_per_account_usd"`
	FiveHourLimitPerAccountUSD float64                              `json:"five_hour_limit_per_account_usd"`
	Parent                     OpenAIOAuthPoolCapacityWindowSummary `json:"parent"`
	FiveHour                   OpenAIOAuthPoolCapacityWindowSummary `json:"five_hour"`
}

type OpenAIOAuthPoolCapacityTotals struct {
	Parent   OpenAIOAuthPoolCapacityWindowSummary `json:"parent"`
	FiveHour OpenAIOAuthPoolCapacityWindowSummary `json:"five_hour"`
	Weekly   OpenAIOAuthPoolCapacityWindowSummary `json:"weekly"`
	Monthly  OpenAIOAuthPoolCapacityWindowSummary `json:"monthly"`
}

type OpenAIOAuthPoolCapacityUnknownPlanType struct {
	PlanType     string `json:"plan_type"`
	AccountCount int    `json:"account_count"`
}

type OpenAIOAuthPoolCapacitySummary struct {
	GeneratedAt             string                                   `json:"generated_at"`
	FiveHourRatio           float64                                  `json:"five_hour_ratio"`
	ManagedAccountCount     int                                      `json:"managed_account_count"`
	IncludedAccountCount    int                                      `json:"included_account_count"`
	ExcludedAccountCount    int                                      `json:"excluded_account_count"`
	ShadowAccountCount      int                                      `json:"shadow_account_count"`
	UnknownPlanAccountCount int                                      `json:"unknown_plan_account_count"`
	UnknownPlanTypes        []OpenAIOAuthPoolCapacityUnknownPlanType `json:"unknown_plan_types"`
	Totals                  OpenAIOAuthPoolCapacityTotals            `json:"totals"`
	Plans                   []OpenAIOAuthPoolCapacityPlanSummary     `json:"plans"`
}

func (s *adminServiceImpl) GetOpenAIOAuthPoolCapacity(ctx context.Context) (*OpenAIOAuthPoolCapacitySummary, error) {
	if s == nil || s.accountRepo == nil {
		return buildOpenAIOAuthPoolCapacity(nil, time.Now()), nil
	}

	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, "", "", 0, "")
	if err != nil {
		return nil, fmt.Errorf("list OpenAI OAuth accounts for capacity: %w", err)
	}
	return buildOpenAIOAuthPoolCapacity(accounts, time.Now()), nil
}

func buildOpenAIOAuthPoolCapacity(accounts []Account, now time.Time) *OpenAIOAuthPoolCapacitySummary {
	now = now.UTC()
	result := &OpenAIOAuthPoolCapacitySummary{
		GeneratedAt:      now.Format(time.RFC3339),
		FiveHourRatio:    openAIOAuthCapacityFiveHourRatio,
		UnknownPlanTypes: []OpenAIOAuthPoolCapacityUnknownPlanType{},
		Plans:            make([]OpenAIOAuthPoolCapacityPlanSummary, len(openAIOAuthCapacityPlanRules)),
	}
	planIndexes := make(map[string]int, len(openAIOAuthCapacityPlanRules))
	for i, rule := range openAIOAuthCapacityPlanRules {
		planIndexes[rule.PlanType] = i
		result.Plans[i] = OpenAIOAuthPoolCapacityPlanSummary{
			PlanType:                   rule.PlanType,
			Period:                     rule.Period,
			LimitPerAccountUSD:         rule.LimitUSD,
			FiveHourLimitPerAccountUSD: roundCapacityUSD(rule.LimitUSD * openAIOAuthCapacityFiveHourRatio),
		}
	}

	unknownPlans := make(map[string]int)
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAIOAuth() {
			continue
		}
		result.ManagedAccountCount++
		if account.IsShadow() {
			result.ShadowAccountCount++
			continue
		}

		if !openAIOAuthCapacityAccountIncluded(account, now) {
			result.ExcludedAccountCount++
			continue
		}
		rule, ok := resolveOpenAIOAuthCapacityPlan(account.GetCredential("plan_type"))
		if !ok {
			planType := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
			if planType == "" {
				planType = "unknown"
			}
			unknownPlans[planType]++
			result.UnknownPlanAccountCount++
			continue
		}
		result.IncludedAccountCount++

		plan := &result.Plans[planIndexes[rule.PlanType]]
		plan.AccountCount++
		parent := buildCodexUsageProgressFromExtra(account.Extra, "7d", now)
		fiveHour := buildCodexUsageProgressFromExtra(account.Extra, "5h", now)
		addOpenAIOAuthCapacityWindow(
			&plan.Parent,
			rule.LimitUSD,
			parent,
			openAIOAuthCapacitySnapshotStale(account.Extra, "7d", now),
		)
		addOpenAIOAuthCapacityWindow(
			&plan.FiveHour,
			rule.LimitUSD*openAIOAuthCapacityFiveHourRatio,
			fiveHour,
			openAIOAuthCapacitySnapshotStale(account.Extra, "5h", now),
		)
	}

	for planType, count := range unknownPlans {
		result.UnknownPlanTypes = append(result.UnknownPlanTypes, OpenAIOAuthPoolCapacityUnknownPlanType{
			PlanType:     planType,
			AccountCount: count,
		})
	}
	sort.Slice(result.UnknownPlanTypes, func(i, j int) bool {
		return result.UnknownPlanTypes[i].PlanType < result.UnknownPlanTypes[j].PlanType
	})

	for i := range result.Plans {
		plan := &result.Plans[i]
		finalizeOpenAIOAuthCapacityWindow(&plan.Parent)
		finalizeOpenAIOAuthCapacityWindow(&plan.FiveHour)
		addOpenAIOAuthCapacityWindowSummary(&result.Totals.Parent, plan.Parent)
		addOpenAIOAuthCapacityWindowSummary(&result.Totals.FiveHour, plan.FiveHour)
		if plan.Period == "monthly" {
			addOpenAIOAuthCapacityWindowSummary(&result.Totals.Monthly, plan.Parent)
		} else {
			addOpenAIOAuthCapacityWindowSummary(&result.Totals.Weekly, plan.Parent)
		}
	}
	finalizeOpenAIOAuthCapacityWindow(&result.Totals.Parent)
	finalizeOpenAIOAuthCapacityWindow(&result.Totals.FiveHour)
	finalizeOpenAIOAuthCapacityWindow(&result.Totals.Weekly)
	finalizeOpenAIOAuthCapacityWindow(&result.Totals.Monthly)
	return result
}

func openAIOAuthCapacityAccountIncluded(account *Account, now time.Time) bool {
	if account == nil || !account.IsActive() || !account.Schedulable {
		return false
	}
	if account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt) {
		return false
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return false
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		return false
	}
	return account.OverloadUntil == nil || !now.Before(*account.OverloadUntil)
}

func resolveOpenAIOAuthCapacityPlan(planType string) (openAIOAuthCapacityPlanRule, bool) {
	planType = strings.ToLower(strings.TrimSpace(planType))
	for _, rule := range openAIOAuthCapacityPlanRules {
		for _, alias := range rule.Aliases {
			if planType == alias {
				return rule, true
			}
		}
	}
	return openAIOAuthCapacityPlanRule{}, false
}

func openAIOAuthCapacitySnapshotStale(extra map[string]any, window string, now time.Time) bool {
	if openAIQuotaWindowReset(extra, window, now) {
		return true
	}
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return true
	}
	updatedAt, err := parseTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return true
	}
	staleAfter := openAIOAuthCapacitySnapshotStaleAfter
	if window == "5h" {
		staleAfter = openAIOAuthCapacityFiveHourStaleAfter
	}
	return now.Sub(updatedAt) >= staleAfter
}

func addOpenAIOAuthCapacityWindow(summary *OpenAIOAuthPoolCapacityWindowSummary, limitUSD float64, progress *UsageProgress, stale bool) {
	if summary == nil {
		return
	}
	summary.EstimatedLimitUSD += limitUSD
	if progress == nil {
		summary.EstimatedRemainingUSD += limitUSD
		summary.UnobservedLimitUSD += limitUSD
		summary.MissingSnapshotCount++
		return
	}

	usedRatio := math.Max(0, math.Min(1, progress.Utilization/100))
	usedUSD := limitUSD * usedRatio
	remainingUSD := limitUSD - usedUSD
	summary.EstimatedUsedUSD += usedUSD
	summary.EstimatedRemainingUSD += remainingUSD
	if stale {
		summary.UnobservedLimitUSD += limitUSD
		summary.StaleSnapshotCount++
		return
	}
	summary.ObservedRemainingUSD += remainingUSD
	summary.ObservedAccountCount++
}

func addOpenAIOAuthCapacityWindowSummary(target *OpenAIOAuthPoolCapacityWindowSummary, source OpenAIOAuthPoolCapacityWindowSummary) {
	if target == nil {
		return
	}
	target.EstimatedLimitUSD += source.EstimatedLimitUSD
	target.EstimatedUsedUSD += source.EstimatedUsedUSD
	target.EstimatedRemainingUSD += source.EstimatedRemainingUSD
	target.ObservedRemainingUSD += source.ObservedRemainingUSD
	target.UnobservedLimitUSD += source.UnobservedLimitUSD
	target.ObservedAccountCount += source.ObservedAccountCount
	target.MissingSnapshotCount += source.MissingSnapshotCount
	target.StaleSnapshotCount += source.StaleSnapshotCount
}

func finalizeOpenAIOAuthCapacityWindow(summary *OpenAIOAuthPoolCapacityWindowSummary) {
	if summary == nil {
		return
	}
	summary.EstimatedLimitUSD = roundCapacityUSD(summary.EstimatedLimitUSD)
	summary.EstimatedUsedUSD = roundCapacityUSD(summary.EstimatedUsedUSD)
	summary.EstimatedRemainingUSD = roundCapacityUSD(summary.EstimatedRemainingUSD)
	summary.ObservedRemainingUSD = roundCapacityUSD(summary.ObservedRemainingUSD)
	summary.UnobservedLimitUSD = roundCapacityUSD(summary.UnobservedLimitUSD)
}

func roundCapacityUSD(value float64) float64 {
	return math.Round(value*10000) / 10000
}
