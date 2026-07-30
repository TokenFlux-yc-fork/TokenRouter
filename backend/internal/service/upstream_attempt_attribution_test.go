package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAttemptAttributionUnknownUsageStaysUnknown(t *testing.T) {
	attempt := validUpstreamAttemptAttribution()
	require.NoError(t, attempt.Validate())
	require.False(t, attempt.Usage.Observed)
	require.Nil(t, attempt.Usage.InputTokens)
	require.Nil(t, attempt.Usage.CostUSD)

	zero := int64(0)
	zeroCost := 0.0
	attempt.Usage = UpstreamAttemptUsage{Observed: true, InputTokens: &zero, CostUSD: &zeroCost}
	require.NoError(t, attempt.Validate())
	require.NotNil(t, attempt.Usage.InputTokens)
	require.NotNil(t, attempt.Usage.CostUSD)
}

func TestUpstreamAttemptAttributionRejectsObservedUsageWithoutMeasurements(t *testing.T) {
	attempt := validUpstreamAttemptAttribution()
	attempt.Usage.Observed = true
	require.ErrorIs(t, attempt.Validate(), ErrUpstreamAttemptInvalid)
}

func TestUpstreamAttemptAttributionRejectsUnsafeStates(t *testing.T) {
	attempt := validUpstreamAttemptAttribution()
	zero := int64(0)
	attempt.Usage.InputTokens = &zero
	require.ErrorIs(t, attempt.Validate(), ErrUpstreamAttemptInvalid)

	attempt = validUpstreamAttemptAttribution()
	attempt.DeliveryCommitted = true
	attempt.SafeToFailover = true
	require.ErrorIs(t, attempt.Validate(), ErrUpstreamAttemptInvalid)

	attempt = validUpstreamAttemptAttribution()
	attempt.State = UpstreamAttemptStateTerminal
	require.ErrorIs(t, attempt.Validate(), ErrUpstreamAttemptInvalid)
}

func TestUpstreamAttemptAttributionTelemetryHasNoSensitiveFields(t *testing.T) {
	typeOfJSON, err := json.Marshal(validUpstreamAttemptAttribution())
	require.NoError(t, err)
	lower := strings.ToLower(string(typeOfJSON))
	for _, forbidden := range []string{"payload", "encrypted_content", "authorization", "api_key", "access_token", "refresh_token", "raw_url", "base_url"} {
		require.NotContains(t, lower, forbidden)
	}
}

func validUpstreamAttemptAttribution() UpstreamAttemptAttribution {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return UpstreamAttemptAttribution{
		AttemptID:        "attempt_test",
		ClientRequestID:  "client_test",
		GatewayRequestID: "gateway_test",
		AccountID:        42,
		Transport:        UpstreamAttemptTransportHTTP,
		Domain: OpenAICompatibilityDomain{
			Provider:            "openai",
			UpstreamFingerprint: "upstream_v1_deadbeef",
			EffectiveModel:      "gpt-test",
			ContractVersion:     "v2",
		},
		State:        UpstreamAttemptStateStarted,
		StateVersion: 1,
		StartedAt:    now,
		ObservedAt:   now,
	}
}
