package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t *testing.T, accountID int64) (*OpenAIGatewayService, *passiveAccountCircuitBreakerTempUnschedAccountRepo, *Account, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       accountID,
		Name:     "pool-api-key",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
		},
		Concurrency: 1,
		Status:      StatusActive,
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	return svc, accountRepo, account, ginCtx, rec
}

func newOpenAITestErrorResponse(statusCode int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"rid_custom_skipped"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded","type":"server_error"}}`)),
	}
}

func bindOpenAITestPassthroughRule(c *gin.Context) {
	msg := "passthrough upstream error"
	passthrough := &ErrorPassthroughService{}
	passthrough.setLocalCache([]*model.ErrorPassthroughRule{
		{
			Name:            "openai 503 passthrough",
			Enabled:         true,
			Priority:        1,
			ErrorCodes:      []int{http.StatusServiceUnavailable},
			MatchMode:       model.MatchModeAny,
			Platforms:       []string{PlatformOpenAI},
			PassthroughCode: true,
			PassthroughBody: false,
			CustomMessage:   &msg,
		},
	})
	BindErrorPassthroughService(c, passthrough)
}

func TestOpenAIHandleErrorResponseCustomSkippedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1101
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)

	result, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		[]byte(`{"model":"gpt-5"}`),
		"gpt-5",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIHandleErrorResponsePassthroughRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1104
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)
	bindOpenAITestPassthroughRule(ginCtx)

	result, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		[]byte(`{"model":"gpt-5"}`),
		"gpt-5",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIHandleCompatErrorResponseCustomSkippedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1102
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)
	writeError := func(c *gin.Context, statusCode int, errType, message string) {
		c.JSON(statusCode, gin.H{"error": gin.H{"type": errType, "message": message}})
	}

	result, err := svc.handleCompatErrorResponse(
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		writeError,
		"gpt-5",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIHandleCompatErrorResponsePassthroughRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1105
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)
	bindOpenAITestPassthroughRule(ginCtx)
	writeError := func(c *gin.Context, statusCode int, errType, message string) {
		c.JSON(statusCode, gin.H{"error": gin.H{"type": errType, "message": message}})
	}

	result, err := svc.handleCompatErrorResponse(
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		writeError,
		"gpt-5",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIImagesErrorResponseCustomSkippedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1103
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)

	result, err := svc.handleOpenAIImagesErrorResponse(
		context.Background(),
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		[]byte(`{"model":"gpt-image-1"}`),
		"gpt-image-1",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIImagesErrorResponsePassthroughRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	const accountID int64 = 1106
	svc, accountRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedAccountCircuitBreakerFixture(t, accountID)
	bindOpenAITestPassthroughRule(ginCtx)

	result, err := svc.handleOpenAIImagesErrorResponse(
		context.Background(),
		newOpenAITestErrorResponse(http.StatusServiceUnavailable),
		ginCtx,
		account,
		[]byte(`{"model":"gpt-image-1"}`),
		"gpt-image-1",
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}

func TestOpenAIImageRateLimitRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       1107,
		Name:     "openai-image-pool",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		[]byte(`{"error":{"message":"Rate limit reached for limit gpt-image"}}`),
		"gpt-image-1",
	)

	require.False(t, shouldDisable)
	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}
