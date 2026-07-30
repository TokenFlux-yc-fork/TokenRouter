package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

type ClientRequestID string
type GatewayRequestID string
type AttemptID string
type UpstreamRequestID string
type WSConnectionID string
type WSTurnID string

type openAIWSAttemptIdentityContextKey struct{}

type openAIWSAttemptIdentity struct {
	ConnectionID WSConnectionID
	TurnID       WSTurnID
}

// OpenAIRequestIdentity keeps the client-visible request identity separate from
// the gateway request and each real upstream attempt.
type OpenAIRequestIdentity struct {
	ClientRequestID  ClientRequestID
	GatewayRequestID GatewayRequestID
}

func NewOpenAIRequestIdentity(clientRequestID ClientRequestID) OpenAIRequestIdentity {
	return NewOpenAIRequestIdentityWithGatewayRequestID(clientRequestID, "")
}

// NewOpenAIRequestIdentityWithGatewayRequestID reuses the gateway's existing
// request-level identity when available while keeping it separate from the
// client-provided identity and all upstream/response identities.
func NewOpenAIRequestIdentityWithGatewayRequestID(clientRequestID ClientRequestID, gatewayRequestID GatewayRequestID) OpenAIRequestIdentity {
	gatewayRequestID = GatewayRequestID(strings.TrimSpace(string(gatewayRequestID)))
	if gatewayRequestID == "" {
		gatewayRequestID = GatewayRequestID("gwreq_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	}
	return OpenAIRequestIdentity{
		ClientRequestID:  ClientRequestID(strings.TrimSpace(string(clientRequestID))),
		GatewayRequestID: gatewayRequestID,
	}
}

func (OpenAIRequestIdentity) NewAttempt() AttemptID {
	return AttemptID("attempt_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func NewOpenAIWSConnectionID() WSConnectionID {
	return WSConnectionID("wsconn_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func NewOpenAIWSTurnID() WSTurnID {
	return WSTurnID("wsturn_" + strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func withOpenAIWSAttemptIdentity(ctx context.Context, connectionID WSConnectionID, turnID WSTurnID) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIWSAttemptIdentityContextKey{}, openAIWSAttemptIdentity{
		ConnectionID: connectionID,
		TurnID:       turnID,
	})
}

func openAIWSAttemptIdentityFromContext(ctx context.Context) openAIWSAttemptIdentity {
	if ctx == nil {
		return openAIWSAttemptIdentity{}
	}
	identity, _ := ctx.Value(openAIWSAttemptIdentityContextKey{}).(openAIWSAttemptIdentity)
	return identity
}
