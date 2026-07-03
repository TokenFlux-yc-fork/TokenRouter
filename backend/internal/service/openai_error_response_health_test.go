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

func newOpenAICustomErrorSkippedHealthFixture(t *testing.T, groupID int64) (*OpenAIGatewayService, *groupHealthRepoStub, *Account, *gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	group := &Group{
		ID:                          groupID,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusHealthy,
		HealthCheckFailureThreshold: 1,
	}
	groupRepo := &groupHealthRepoStub{groups: map[int64]*Group{group.ID: group}}
	rateLimitService := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitService.SetGroupHealthMonitor(NewGroupHealthMonitor(groupRepo, nil))

	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       groupID + 1000,
		Name:     "pool-api-key",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
		},
		GroupIDs:    []int64{group.ID},
		Concurrency: 1,
		Status:      StatusActive,
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	return svc, groupRepo, account, ginCtx, rec
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

func requirePassiveGroupHealthFailure(t *testing.T, groupRepo *groupHealthRepoStub, groupID int64) {
	t.Helper()
	require.Len(t, groupRepo.updates, 1)
	require.Equal(t, groupID, groupRepo.updates[0].groupID)
	require.Equal(t, HealthStatusUnhealthy, groupRepo.updates[0].update.HealthStatus)
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

func TestOpenAIHandleErrorResponseCustomSkippedRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 101
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)

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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIHandleErrorResponsePassthroughRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 104
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)
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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIHandleCompatErrorResponseCustomSkippedRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 102
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)
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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIHandleCompatErrorResponsePassthroughRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 105
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)
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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIImagesErrorResponseCustomSkippedRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 103
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)

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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIImagesErrorResponsePassthroughRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 106
	svc, groupRepo, account, ginCtx, rec := newOpenAICustomErrorSkippedHealthFixture(t, groupID)
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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}

func TestOpenAIImageRateLimitRecordsPassiveGroupHealthFailure(t *testing.T) {
	const groupID int64 = 107
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
	svc := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       1107,
		Name:     "openai-image-pool",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
		GroupIDs: []int64{groupID},
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
	requirePassiveGroupHealthFailure(t, groupRepo, groupID)
}
