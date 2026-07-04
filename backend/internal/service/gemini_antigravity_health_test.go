package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type passiveAccountCircuitBreakerTempUnschedAccountRepo struct {
	AccountRepository
	calls []passiveAccountCircuitBreakerCall
}

type passiveAccountCircuitBreakerCall struct {
	accountID int64
	until     time.Time
	reason    string
}

func (r *passiveAccountCircuitBreakerTempUnschedAccountRepo) SetTempUnschedulable(_ context.Context, accountID int64, until time.Time, reason string) error {
	if len(r.calls) > 0 && !r.calls[len(r.calls)-1].until.Before(until) {
		return nil
	}
	r.calls = append(r.calls, passiveAccountCircuitBreakerCall{accountID: accountID, until: until, reason: reason})
	return nil
}

func (r *passiveAccountCircuitBreakerTempUnschedAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	return nil
}

func newPassiveAccountCircuitBreakerRateLimitService() (*RateLimitService, *passiveAccountCircuitBreakerTempUnschedAccountRepo) {
	accountRepo := &passiveAccountCircuitBreakerTempUnschedAccountRepo{}
	return NewRateLimitService(accountRepo, nil, nil, nil, nil), accountRepo
}

func requireSinglePassiveAccountCircuitBreaker(t *testing.T, accountRepo *passiveAccountCircuitBreakerTempUnschedAccountRepo, accountID int64) {
	t.Helper()
	require.Len(t, accountRepo.calls, 1)
	require.Equal(t, accountID, accountRepo.calls[0].accountID)
	require.True(t, accountRepo.calls[0].until.After(time.Now()))
	require.Contains(t, accountRepo.calls[0].reason, "passive_account_circuit_breaker")
}

func TestGeminiHandleUpstreamErrorHTTP5xxRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	svc := &GeminiMessagesCompatService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       501,
		Name:     "gemini-pool",
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	svc.handleGeminiUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"upstream overloaded"}}`),
	)

	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestAntigravityHandleUpstreamErrorHTTP503RecordsPassiveAccountCircuitBreaker(t *testing.T) {
	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	svc := &AntigravityGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       502,
		Name:     "antigravity-upstream",
		Platform: PlatformAntigravity,
		Type:     AccountTypeUpstream,
	}

	svc.handleUpstreamError(
		context.Background(),
		"test",
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"upstream overloaded"}}`),
		"claude-sonnet-4-5",
		0,
		"",
		false,
	)

	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestAntigravityTempUnscheduledPolicyKeepsConfiguredAccountCooldown(t *testing.T) {
	accountRepo := &passiveAccountCircuitBreakerTempUnschedAccountRepo{}
	rateLimitService := NewRateLimitService(accountRepo, nil, nil, nil, nil)
	svc := &AntigravityGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       506,
		Name:     "antigravity-upstream-temp",
		Platform: PlatformAntigravity,
		Type:     AccountTypeUpstream,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"overloaded"},
					"duration_minutes": float64(10),
				},
			},
		},
	}

	handled, status, err := svc.applyErrorPolicy(
		antigravityRetryLoopParams{
			ctx:     context.Background(),
			prefix:  "test",
			account: account,
		},
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`overloaded`),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusServiceUnavailable, status)
	var switchErr *AntigravityAccountSwitchError
	require.ErrorAs(t, err, &switchErr)
	require.Len(t, accountRepo.calls, 1)
	require.Equal(t, account.ID, accountRepo.calls[0].accountID)
	require.Contains(t, accountRepo.calls[0].reason, `"matched_keyword":"overloaded"`)
	require.NotContains(t, accountRepo.calls[0].reason, "passive_account_circuit_breaker")
}

func TestAntigravityForwardUpstreamHTTP5xxRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded"}}`)),
	}}
	svc := &AntigravityGatewayService{
		httpUpstream:     upstream,
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       505,
		Name:     "antigravity-upstream",
		Platform: PlatformAntigravity,
		Type:     AccountTypeUpstream,
		Credentials: map[string]any{
			"base_url": "https://upstream.example",
			"api_key":  "sk-upstream",
		},
		Concurrency: 1,
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	result, err := svc.ForwardUpstream(
		context.Background(),
		ginCtx,
		account,
		[]byte(`{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestGeminiChatCompletionsCustomSkippedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded"}}`)),
	}}
	svc := &GeminiMessagesCompatService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       503,
		Name:     "gemini-pool-custom",
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                    "gemini-test-key",
			"pool_mode":                  true,
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
		},
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	result, err := svc.ForwardAsChatCompletions(
		context.Background(),
		ginCtx,
		account,
		[]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`),
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}
