package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIForwardResultCustomerSettlementGate(t *testing.T) {
	tests := []struct {
		name   string
		result *OpenAIForwardResult
		want   bool
	}{
		{name: "legacy remains compatible", result: &OpenAIForwardResult{}, want: true},
		{name: "native valid, committed, and attributed", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionValid, DeliveryCommitted: true, AttributionPersisted: true}, want: true},
		{name: "native valid and committed without durable attribution", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionValid, DeliveryCommitted: true}, want: false},
		{name: "native valid but commit failed", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionValid}, want: false},
		{name: "native zero compaction", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionZeroCompaction, DeliveryCommitted: true}, want: false},
		{name: "native multiple compaction", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionMultipleCompaction, DeliveryCommitted: true}, want: false},
		{name: "native malformed compaction", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionMalformedCompaction, DeliveryCommitted: true}, want: false},
		{name: "native eof", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionIncompleteStream, DeliveryCommitted: true}, want: false},
		{name: "native resource limit", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionResourceLimit, DeliveryCommitted: true}, want: false},
		{name: "native client cancel", result: &OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: OpenAINativeCompactionValid, DeliveryCommitted: true, ClientDisconnect: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.result.CustomerSettlementAllowed())
			require.Equal(t, tt.want, tt.result.SucceededForScheduling())
		})
	}
}

func TestOpenAIAttemptUsageUnknownIsNotObservedZero(t *testing.T) {
	result := &OpenAIForwardResult{}
	require.False(t, result.UpstreamUsageObserved)

	result.UpstreamUsageObserved = true
	require.True(t, result.UpstreamUsageObserved)
	require.Equal(t, OpenAIUsage{}, result.Usage)
}

func TestOpenAIOpsTelemetryIsBoundedAndRedacted(t *testing.T) {
	result := &OpenAIForwardResult{
		ClientRequestID:          "client-1",
		GatewayRequestID:         "gateway-1",
		AttemptID:                "attempt-1",
		UpstreamRequestID:        "upstream-1",
		ResponseID:               "response-1",
		WSConnectionID:           "connection-1",
		WSTurnID:                 "turn-1",
		Transport:                UpstreamAttemptTransportHTTP,
		AccountType:              "oauth",
		CapabilitySource:         OpenAINativeCompactionCapabilitySourceTrustedOfficial,
		UpstreamFingerprint:      OpenAIUpstreamFingerprint("https://user:oauth-token@example.test/v1/responses?api_key=secret"),
		Model:                    "requested-model",
		UpstreamModel:            "mapped-model",
		NativeRemoteCompactionV2: true,
		SemanticSource:           "validator",
		SemanticOutcome:          OpenAINativeCompactionValid,
		OutputItemDoneCount:      1,
		CompactionItemCount:      1,
		TerminalEventCount:       1,
		UpstreamTerminalEvent:    "response.completed",
		SafeToFailover:           false,
		FailoverCount:            2,
		DeliveryCommitted:        true,
		AttributionPersisted:     true,
		UpstreamUsageObserved:    true,
	}

	telemetry := result.OpsTelemetry()
	require.Equal(t, AttemptID("attempt-1"), telemetry.AttemptID)
	require.Equal(t, UpstreamAttemptTransportHTTP, telemetry.Transport)
	require.Equal(t, "oauth", telemetry.AccountType)
	require.Equal(t, "oauth", telemetry.FinalAccountType)
	require.Equal(t, OpenAINativeCompactionCapabilitySourceTrustedOfficial, telemetry.CapabilitySource)
	require.Equal(t, "requested-model", telemetry.RequestedModel)
	require.Equal(t, "mapped-model", telemetry.MappedModel)
	require.Equal(t, OpenAINativeCompactionValid, telemetry.Outcome)
	require.True(t, telemetry.Committed)
	require.True(t, telemetry.SemanticOutputCommitted)
	require.True(t, telemetry.AttributionPersisted)
	require.True(t, telemetry.UsageObserved)
	require.True(t, telemetry.Usage.Observed)
	require.NotNil(t, telemetry.Usage.InputTokens)
	require.Zero(t, *telemetry.Usage.InputTokens)

	unknown := (&OpenAIForwardResult{}).OpsTelemetry()
	require.False(t, unknown.Usage.Observed)
	require.Nil(t, unknown.Usage.InputTokens)

	raw, err := json.Marshal(telemetry)
	require.NoError(t, err)
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"authorization", "api_key", "oauth-token", `"context"`, `"stage"`,
		"encrypted_content", "example.test", "https://", "secret",
	} {
		require.NotContains(t, lower, forbidden)
	}
}
