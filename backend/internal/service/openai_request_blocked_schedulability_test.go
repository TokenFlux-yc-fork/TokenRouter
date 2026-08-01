package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewOpenAIUpstreamFailoverErrorRequestBlockedIsRequestScoped(t *testing.T) {
	body := []byte(`{"error":{"message":"Your request was blocked."}}`)

	failoverErr := newOpenAIUpstreamFailoverError(
		http.StatusForbidden,
		http.Header{},
		body,
		"Your request was blocked.",
		true,
	)

	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, GatewayFailureScopeRequest, failoverErr.Scope)
	require.Equal(t, GatewayFailureReason("openai_request_blocked"), failoverErr.Reason)
	require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
}

func TestNewOpenAIUpstreamFailoverErrorOrdinaryForbiddenKeepsAccountBehavior(t *testing.T) {
	body := []byte(`{"error":{"message":"API key does not have access to this resource."}}`)

	failoverErr := newOpenAIUpstreamFailoverError(
		http.StatusForbidden,
		http.Header{},
		body,
		"API key does not have access to this resource.",
		true,
	)

	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Empty(t, failoverErr.Scope)
	require.Empty(t, failoverErr.Reason)
	require.True(t, failoverErr.ShouldReportAccountScheduleFailure())
}

func TestShouldRetryPoolModeAccountTestSkipsRequestBlocked403(t *testing.T) {
	account := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusForbidden)},
		},
	}

	statusCode, retryable := shouldRetryPoolModeAccountTest(
		account,
		`API returned 403: {"error":{"message":"Your request was blocked."}}`,
	)
	require.Equal(t, http.StatusForbidden, statusCode)
	require.False(t, retryable)

	statusCode, retryable = shouldRetryPoolModeAccountTest(
		account,
		`API returned 403: {"error":{"message":"API key does not have access to this resource."}}`,
	)
	require.Equal(t, http.StatusForbidden, statusCode)
	require.True(t, retryable)
}

func TestShouldRetryPoolModeAccountTestSkipsObservedTerminalCredentialAndBillingErrors(t *testing.T) {
	account := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusUnauthorized), float64(http.StatusForbidden)},
		},
	}

	tests := []struct {
		name         string
		errorMessage string
		statusCode   int
		retryable    bool
	}{
		{
			name:         "insufficient balance code",
			errorMessage: `API returned 403: {"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
			statusCode:   http.StatusForbidden,
		},
		{
			name:         "nested insufficient user quota",
			errorMessage: `API returned 403: {"error":{"type":"new_api_error","code":"insufficient_user_quota","message":"用户额度不足"}}`,
			statusCode:   http.StatusForbidden,
		},
		{
			name:         "inactive virtual key",
			errorMessage: `API returned 403: {"type":"virtual_key_blocked","status_code":403,"error":{"message":"Virtual key is inactive"}}`,
			statusCode:   http.StatusForbidden,
		},
		{
			name:         "nested billing error",
			errorMessage: `API returned 403: {"error":{"type":"billing_error","message":"insufficient balance"}}`,
			statusCode:   http.StatusForbidden,
		},
		{
			name:         "invalid credentials",
			errorMessage: `Grok Responses API returned 401: {"error":{"type":"bad_response_status_code","code":"bad_response_status_code","message":"Invalid or expired credentials (auth_kind=bearer, upstream=PermissionDenied)"}}`,
			statusCode:   http.StatusUnauthorized,
		},
		{
			name:         "unknown forbidden stays retryable",
			errorMessage: `API returned 403: {"error":{"message":"temporarily denied"}}`,
			statusCode:   http.StatusForbidden,
			retryable:    true,
		},
		{
			name:         "unknown unauthorized stays retryable",
			errorMessage: `API returned 401: {"error":{"message":"temporary authentication backend failure"}}`,
			statusCode:   http.StatusUnauthorized,
			retryable:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statusCode, retryable := shouldRetryPoolModeAccountTest(account, tt.errorMessage)
			require.Equal(t, tt.statusCode, statusCode)
			require.Equal(t, tt.retryable, retryable)
		})
	}
}

func TestScheduledTestFailuresRequestBlockedBreaksAccountFailureSequence(t *testing.T) {
	blocked := scheduledResult("failed")
	blocked.ErrorMessage = `API returned 403: {"error":{"message":"Your request was blocked."}}`
	ordinary := scheduledResult("failed")
	ordinary.ErrorMessage = `API returned 403: {"error":{"message":"API key does not have access to this resource."}}`

	require.False(t, hasConsecutiveScheduledTestFailures(
		[]*ScheduledTestResult{blocked, scheduledResult("failed")},
		2,
	))
	require.True(t, hasConsecutiveScheduledTestFailures(
		[]*ScheduledTestResult{ordinary, scheduledResult("failed")},
		2,
	))
}
