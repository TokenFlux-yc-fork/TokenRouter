package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGatewayService_RetryExhaustedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	rateLimitService, accountRepo := newPassiveAccountCircuitBreakerRateLimitService()

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
	}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{"x-request-id": []string{"rid_retry_exhausted"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded"}}`)),
	}

	svc.handleRetryExhaustedSideEffects(context.Background(), resp, account)

	requireSinglePassiveAccountCircuitBreaker(t, accountRepo, account.ID)
}
