//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

type rateLimit429AccountRepoStub struct {
	mockAccountRepoForGemini
	rateLimitCalls        int
	rateLimitIfLaterCalls int
	lastRateLimitID       int64
	lastRateLimitReset    time.Time
}

func (r *rateLimit429AccountRepoStub) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.lastRateLimitID = id
	r.lastRateLimitReset = resetAt
	return nil
}

func (r *rateLimit429AccountRepoStub) SetRateLimitedIfLater(_ context.Context, id int64, resetAt time.Time) error {
	r.rateLimitIfLaterCalls++
	r.lastRateLimitID = id
	if resetAt.After(r.lastRateLimitReset) {
		r.lastRateLimitReset = resetAt
	}
	return nil
}

func TestGetRateLimit429CooldownSettings_DefaultsWhenNotSet(t *testing.T) {
	repo := newMockSettingRepo()
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, 5, settings.CooldownSeconds)
}

func TestGetRateLimit429CooldownSettings_ReadsFromDB(t *testing.T) {
	repo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	repo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetRateLimit429CooldownSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 12, settings.CooldownSeconds)
}

func TestSetRateLimit429CooldownSettings_EnabledRejectsOutOfRange(t *testing.T) {
	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	for _, seconds := range []int{0, -1, 7201, 99999} {
		err := svc.SetRateLimit429CooldownSettings(context.Background(), &RateLimit429CooldownSettings{
			Enabled: true, CooldownSeconds: seconds,
		})
		require.Error(t, err, "should reject enabled=true + cooldown_seconds=%d", seconds)
		require.Contains(t, err.Error(), "cooldown_seconds must be between 1-7200")
	}
}

func TestHandle429_FallbackUsesDBSeconds(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.Equal(t, int64(42), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(12*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(12*time.Second)))
}

func TestHandle429_FallbackDisabledSkipsLocalMark(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))

	require.Zero(t, accountRepo.rateLimitIfLaterCalls)
}

// Anthropic 缺少 reset 头的 429 也应进入短期兜底冷却，避免持续消耗故障转移预算。
func TestHandle429_AnthropicNoResetTimeUsesFallbackCooldown(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 45, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"Extra usage required"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.Equal(t, int64(45), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(12*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(12*time.Second)))
}

// 管理端关闭兜底冷却后，Anthropic 缺少 reset 头的 429 不应标记账号。
func TestHandle429_AnthropicNoResetTimeFallbackDisabledSkipsMark(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: false, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)

	settingSvc := NewSettingService(settingRepo, &config.Config{})
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	svc.SetSettingService(settingSvc)

	account := &Account{ID: 46, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"Extra usage required"}}`))

	require.Zero(t, accountRepo.rateLimitIfLaterCalls)
}

func TestHandle429_RetryAfterSetsOpenAICooldown(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{"Retry-After": []string{"45"}}, nil)
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.Equal(t, int64(47), accountRepo.lastRateLimitID)
	require.False(t, accountRepo.lastRateLimitReset.Before(before.Add(45*time.Second)))
	require.False(t, accountRepo.lastRateLimitReset.After(after.Add(45*time.Second)))
}

func TestHandle429_RetryAfterHTTPDateSetsOpenAICooldown(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 48, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	resetAt := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)

	svc.handle429(context.Background(), account, http.Header{"Retry-After": []string{resetAt.Format(http.TimeFormat)}}, nil)

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.WithinDuration(t, resetAt, accountRepo.lastRateLimitReset, time.Second)
}

func TestParseRetryAfterResetTimeRejectsUnsafeValues(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, raw := range []string{
		"", "0", "-1", "1.5", "NaN", "+Inf", "604801",
		now.Add(-time.Minute).Format(http.TimeFormat),
		now.Add(8 * 24 * time.Hour).Format(http.TimeFormat),
	} {
		require.Nil(t, parseRetryAfterResetTime(http.Header{"Retry-After": []string{raw}}, now), raw)
	}
}

func TestResolveOpenAI429ResetTimeUsesLatestValidSource(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":  []string{"100"},
		"X-Codex-Primary-Reset-Seconds": []string{"60"},
		"Retry-After":                    []string{"300"},
	}
	body := []byte(`{"error":{"type":"rate_limit_exceeded","resets_at":` + strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10) + `}}`)

	resetAt := resolveOpenAI429ResetTime(headers, body, now)

	require.NotNil(t, resetAt)
	require.WithinDuration(t, now.Add(10*time.Minute), *resetAt, time.Second)
}

func TestResolveOpenAI429ResetTimeRejectsUnsafeBodyDeadline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	body := []byte(`{"error":{"type":"rate_limit_exceeded","resets_at":` + strconv.FormatInt(now.Add(8*24*time.Hour).Unix(), 10) + `}}`)

	resetAt := resolveOpenAI429ResetTime(http.Header{"Retry-After": []string{"120"}}, body, now)

	require.NotNil(t, resetAt)
	require.WithinDuration(t, now.Add(2*time.Minute), *resetAt, time.Second)
}

func TestHandle429_DoesNotShortenExistingCooldown(t *testing.T) {
	later := time.Now().Add(10 * time.Minute)
	accountRepo := &rateLimit429AccountRepoStub{lastRateLimitReset: later}
	svc := NewRateLimitService(accountRepo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 49, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	svc.handle429(context.Background(), account, http.Header{"Retry-After": []string{"30"}}, nil)

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.Equal(t, later, accountRepo.lastRateLimitReset)
}

func TestHandle429_FallbackUsesDefaultSecondsWhenSettingServiceMissing(t *testing.T) {
	accountRepo := &rateLimit429AccountRepoStub{}
	cfg := &config.Config{}
	svc := NewRateLimitService(accountRepo, nil, cfg, nil, nil)

	account := &Account{ID: 44, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, []byte(`{"error":{"message":"slow down"}}`))
	after := time.Now()

	require.Equal(t, 1, accountRepo.rateLimitIfLaterCalls)
	require.Equal(t, int64(44), accountRepo.lastRateLimitID)
	require.True(t, !accountRepo.lastRateLimitReset.Before(before.Add(5*time.Second)) && !accountRepo.lastRateLimitReset.After(after.Add(5*time.Second)))
}
