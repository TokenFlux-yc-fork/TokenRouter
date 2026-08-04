package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseOptionsUsesExclusiveEastEightCutoff(t *testing.T) {
	opts, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if got, want := opts.Before.Format(time.RFC3339), defaultBefore; got != want {
		t.Fatalf("Before = %q, want %q", got, want)
	}
	if got, want := opts.Before.UTC().Format(time.RFC3339), "2026-08-03T16:00:00Z"; got != want {
		t.Fatalf("Before UTC = %q, want %q", got, want)
	}
	if opts.AccountCount != 90 {
		t.Fatalf("AccountCount = %d, want 90", opts.AccountCount)
	}
	if opts.ChunkSize != defaultChunkSize {
		t.Fatalf("ChunkSize = %d, want %d", opts.ChunkSize, defaultChunkSize)
	}
}

func TestParseOptionsRejectsWrongTimezoneAndBatchSize(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "wrong timezone", args: []string{"--before", "2026-08-04T00:00:00Z"}, want: "+08:00"},
		{name: "too few accounts", args: []string{"--account-count", "79"}, want: "between 80 and 100"},
		{name: "too many accounts", args: []string{"--account-count", "101"}, want: "between 80 and 100"},
		{name: "non-finite team ratio", args: []string{"--team-ratio", "NaN"}, want: "greater than 0 and less than 1"},
		{name: "invalid digest", args: []string{"--expected-plan-digest", "xyz"}, want: "64-character SHA-256"},
		{name: "invalid chunk size", args: []string{"--chunk-size", "0"}, want: "between 1 and 100000"},
		{name: "execute requires digest", args: []string{"--execute"}, want: "requires --expected-plan-digest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOptions(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseOptions() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseIDListDeduplicatesAndSorts(t *testing.T) {
	got, err := parseIDList("9, 3,9, 5")
	if err != nil {
		t.Fatalf("parseIDList() error = %v", err)
	}
	want := []int64{3, 5, 9}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseIDList() = %v, want %v", got, want)
	}
}

func TestAllocateTargetCountsFollowsHistoricalVolume(t *testing.T) {
	sources := []sourceAccount{
		{ID: 10, UsageRows: 300},
		{ID: 20, UsageRows: 100},
	}
	if err := allocateTargetCounts(sources, 80); err != nil {
		t.Fatalf("allocateTargetCounts() error = %v", err)
	}
	if got := sources[0].TargetCount + sources[1].TargetCount; got != 80 {
		t.Fatalf("target total = %d, want 80", got)
	}
	if sources[0].TargetCount <= sources[1].TargetCount {
		t.Fatalf("larger source received %d targets, smaller source received %d", sources[0].TargetCount, sources[1].TargetCount)
	}
	if int64(sources[0].TargetCount) > sources[0].UsageRows || int64(sources[1].TargetCount) > sources[1].UsageRows {
		t.Fatalf("target count exceeded available history: %+v", sources)
	}
}

func TestGenerateAccountSpecsIsDeterministicAndMatchesOAuthShape(t *testing.T) {
	firstUsage := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sources := []sourceAccount{
		{ID: 10, UsageRows: 300, TargetCount: 60, FirstUsage: firstUsage, Concurrency: 8, Priority: 25, RateMultiplier: 1.2},
		{ID: 20, UsageRows: 100, TargetCount: 20, FirstUsage: firstUsage.Add(24 * time.Hour), Concurrency: 5, Priority: 50, RateMultiplier: 1},
	}
	opts := options{AccountCount: 80, TeamRatio: 0.25, Seed: 42, BatchID: "fixture-batch"}

	first, plusCount, teamCount, err := generateAccountSpecs(sources, opts)
	if err != nil {
		t.Fatalf("generateAccountSpecs() error = %v", err)
	}
	second, _, _, err := generateAccountSpecs(sources, opts)
	if err != nil {
		t.Fatalf("second generateAccountSpecs() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed generated different account specs")
	}
	if len(first) != 80 || plusCount != 60 || teamCount != 20 {
		t.Fatalf("counts = accounts:%d plus:%d team:%d, want 80/60/20", len(first), plusCount, teamCount)
	}

	names := make(map[string]struct{}, len(first))
	domains := make(map[string]int)
	for _, account := range first {
		if account.CreatedAt.Before(createdAtWindowStart) || !account.CreatedAt.Before(createdAtWindowEnd) {
			t.Fatalf("account %q created at %s, outside [%s, %s)", account.Name, account.CreatedAt, createdAtWindowStart, createdAtWindowEnd)
		}
		if _, exists := names[account.Name]; exists {
			t.Fatalf("duplicate generated name %q", account.Name)
		}
		names[account.Name] = struct{}{}

		var credentials map[string]any
		if err := json.Unmarshal(account.Credentials, &credentials); err != nil {
			t.Fatalf("decode credentials: %v", err)
		}
		if credentials["plan_type"] != account.PlanType {
			t.Fatalf("credential plan = %v, spec plan = %s", credentials["plan_type"], account.PlanType)
		}
		if token, _ := credentials["access_token"].(string); !strings.HasPrefix(token, "fixture-at-") {
			t.Fatalf("access token = %q, want fixture prefix", token)
		}
		email, _ := credentials["email"].(string)
		domain := email[strings.LastIndexByte(email, '@')+1:]
		if domain != "gmail.com" && domain != "outlook.com" {
			t.Fatalf("email = %q, want gmail.com or outlook.com", email)
		}
		domains[domain]++

		var extra map[string]any
		if err := json.Unmarshal(account.Extra, &extra); err != nil {
			t.Fatalf("decode extra: %v", err)
		}
		if extra[batchIDExtraKey] != opts.BatchID {
			t.Fatalf("batch ID = %v, want %q", extra[batchIDExtraKey], opts.BatchID)
		}
		if got := int64(extra[sourceIDExtraKey].(float64)); got != account.SourceID {
			t.Fatalf("source ID = %d, want %d", got, account.SourceID)
		}
	}
	if domains["gmail.com"] == 0 || domains["outlook.com"] == 0 {
		t.Fatalf("email domains = %v, want both gmail.com and outlook.com", domains)
	}
}
