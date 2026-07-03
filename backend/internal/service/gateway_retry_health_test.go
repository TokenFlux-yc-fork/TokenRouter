package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayService_RetryExhaustedRecordsPassiveGroupHealthFailure(t *testing.T) {
	group := &Group{
		ID:                          201,
		HealthCheckEnabled:          true,
		HealthStatus:                HealthStatusHealthy,
		HealthCheckFailureThreshold: 1,
	}
	groupRepo := &groupHealthRepoStub{groups: map[int64]*Group{group.ID: group}}
	rateLimitService := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitService.SetGroupHealthMonitor(NewGroupHealthMonitor(groupRepo, nil))

	svc := &GatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID:       301,
		Name:     "pool-api-key",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  true,
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusUnauthorized)},
		},
		GroupIDs: []int64{group.ID},
	}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"x-request-id": []string{"rid_retry_exhausted"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded"}}`)),
	}

	svc.handleRetryExhaustedSideEffects(context.Background(), resp, account)

	require.Len(t, groupRepo.updates, 1)
	require.Equal(t, group.ID, groupRepo.updates[0].groupID)
	require.Equal(t, HealthStatusUnhealthy, groupRepo.updates[0].update.HealthStatus)
}
