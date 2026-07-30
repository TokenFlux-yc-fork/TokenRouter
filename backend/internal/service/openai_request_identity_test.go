package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestIdentityUsesExplicitGatewayRequestID(t *testing.T) {
	identity := NewOpenAIRequestIdentityWithGatewayRequestID(" client ", " gateway ")
	require.Equal(t, ClientRequestID("client"), identity.ClientRequestID)
	require.Equal(t, GatewayRequestID("gateway"), identity.GatewayRequestID)
}

func TestOpenAIRequestIdentitySeparatesRequestAttemptAndResponseIDs(t *testing.T) {
	request := NewOpenAIRequestIdentity(ClientRequestID("client-request"))
	require.Equal(t, ClientRequestID("client-request"), request.ClientRequestID)
	require.NotEmpty(t, request.GatewayRequestID)

	first := request.NewAttempt()
	second := request.NewAttempt()
	require.NotEmpty(t, first)
	require.NotEqual(t, first, second)

	result := OpenAIForwardResult{
		ClientRequestID:   request.ClientRequestID,
		GatewayRequestID:  request.GatewayRequestID,
		AttemptID:         second,
		UpstreamRequestID: UpstreamRequestID("upstream-request"),
		ResponseID:        "response-id",
		WSConnectionID:    WSConnectionID("ws-connection"),
		WSTurnID:          WSTurnID("ws-turn"),
	}
	require.NotEqual(t, string(result.AttemptID), result.ResponseID)
	require.NotEqual(t, string(result.UpstreamRequestID), result.ResponseID)
}
