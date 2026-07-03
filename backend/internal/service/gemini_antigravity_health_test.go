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

func newPassiveHealthRateLimitService(groupID int64) (*RateLimitService, *groupHealthRepoStub) {
	group := &Group{
		ID:                          groupID,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusHealthy,
		HealthCheckFailureThreshold: 1,
	}
	groupRepo := &groupHealthRepoStub{groups: map[int64]*Group{group.ID: group}}
	rateLimitService := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitService.SetGroupHealthMonitor(NewGroupHealthMonitor(groupRepo, nil))
	return rateLimitService, groupRepo
}

func requireSinglePassiveHealthUpdate(t *testing.T, groupRepo *groupHealthRepoStub, groupID int64) {
	t.Helper()
	require.Len(t, groupRepo.updates, 1)
	require.Equal(t, groupID, groupRepo.updates[0].groupID)
	require.Equal(t, HealthStatusUnhealthy, groupRepo.updates[0].update.HealthStatus)
}

type passiveHealthTempUnschedAccountRepo struct {
	AccountRepository
}

func (r *passiveHealthTempUnschedAccountRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func TestGeminiHandleUpstreamErrorHTTP5xxRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 401
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
	svc := &GeminiMessagesCompatService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       501,
		Name:     "gemini-pool",
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
		GroupIDs: []int64{groupID},
	}

	svc.handleGeminiUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":{"message":"upstream overloaded"}}`),
	)

	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}

func TestAntigravityHandleUpstreamErrorHTTP503RecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 402
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
	svc := &AntigravityGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       502,
		Name:     "antigravity-upstream",
		Platform: PlatformAntigravity,
		Type:     AccountTypeUpstream,
		GroupIDs: []int64{groupID},
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

	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}

func TestAntigravityTempUnscheduledPolicyRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 406
	group := &Group{
		ID:                          groupID,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusHealthy,
		HealthCheckFailureThreshold: 1,
	}
	groupRepo := &groupHealthRepoStub{groups: map[int64]*Group{group.ID: group}}
	rateLimitService := NewRateLimitService(&passiveHealthTempUnschedAccountRepo{}, nil, nil, nil, nil)
	rateLimitService.SetGroupHealthMonitor(NewGroupHealthMonitor(groupRepo, nil))
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
		GroupIDs: []int64{groupID},
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
	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}

func TestAntigravityForwardUpstreamHTTP5xxRecordsPassiveGroupHealthFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const groupID int64 = 405
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
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
		GroupIDs:    []int64{groupID},
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
	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}

func TestGeminiChatCompletionsCustomSkippedRecordsPassiveGroupHealthFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const groupID int64 = 403
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
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
		GroupIDs: []int64{groupID},
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
	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}
