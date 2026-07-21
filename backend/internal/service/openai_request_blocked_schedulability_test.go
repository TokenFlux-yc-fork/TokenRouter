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
