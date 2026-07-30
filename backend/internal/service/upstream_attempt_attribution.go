package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

type UpstreamAttemptTransport string

type UpstreamAttemptState string

type UpstreamResponseID string

const (
	UpstreamAttemptTransportHTTP      UpstreamAttemptTransport = "http"
	UpstreamAttemptTransportWebSocket UpstreamAttemptTransport = "websocket"
	UpstreamAttemptTransportProbe     UpstreamAttemptTransport = "probe"

	UpstreamAttemptStateStarted    UpstreamAttemptState = "started"
	UpstreamAttemptStateInProgress UpstreamAttemptState = "in_progress"
	UpstreamAttemptStateTerminal   UpstreamAttemptState = "terminal"
)

var (
	ErrUpstreamAttemptInvalid  = errors.New("invalid upstream attempt attribution")
	ErrUpstreamAttemptConflict = errors.New("upstream attempt attribution conflicts with existing identity")
)

// UpstreamAttemptUsage keeps unknown observations distinct from observed zeroes.
// Callers must set Observed before providing token or cost fields.
type UpstreamAttemptUsage struct {
	Observed                 bool
	InputTokens              *int64
	OutputTokens             *int64
	CacheCreationInputTokens *int64
	CacheReadInputTokens     *int64
	ImageInputTokens         *int64
	ImageOutputTokens        *int64
	CostUSD                  *float64
}

type UpstreamAttemptSemantic struct {
	Outcome             string
	OutputItemDoneCount int64
	CompactionItemCount int64
	MalformedItemCount  int64
	TerminalEvent       string
	TerminalCount       int64
	SuccessfulTerminal  bool
}

// UpstreamAttemptAttribution contains identifiers and bounded semantic telemetry
// only. Sensitive request or response content and endpoint details have no
// representation in this type.
type UpstreamAttemptAttribution struct {
	AttemptID          AttemptID
	ClientRequestID    ClientRequestID
	GatewayRequestID   GatewayRequestID
	UpstreamRequestID  UpstreamRequestID
	UpstreamResponseID UpstreamResponseID
	WSConnectionID     WSConnectionID
	WSTurnID           WSTurnID
	AccountID          int64
	Transport          UpstreamAttemptTransport
	Domain             OpenAICompatibilityDomain
	State              UpstreamAttemptState
	StateVersion       int64
	TransportObserved  bool
	HTTPObserved       bool
	HTTPStatus         *int
	SemanticObserved   bool
	Semantic           UpstreamAttemptSemantic
	Usage              UpstreamAttemptUsage
	DeliveryObserved   bool
	DeliveryCommitted  bool
	SafeToFailover     bool
	StartedAt          time.Time
	CompletedAt        *time.Time
	ObservedAt         time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (a *UpstreamAttemptAttribution) Normalize() {
	if a == nil {
		return
	}
	a.AttemptID = AttemptID(strings.TrimSpace(string(a.AttemptID)))
	a.ClientRequestID = ClientRequestID(strings.TrimSpace(string(a.ClientRequestID)))
	a.GatewayRequestID = GatewayRequestID(strings.TrimSpace(string(a.GatewayRequestID)))
	a.UpstreamRequestID = UpstreamRequestID(strings.TrimSpace(string(a.UpstreamRequestID)))
	a.UpstreamResponseID = UpstreamResponseID(strings.TrimSpace(string(a.UpstreamResponseID)))
	a.WSConnectionID = WSConnectionID(strings.TrimSpace(string(a.WSConnectionID)))
	a.WSTurnID = WSTurnID(strings.TrimSpace(string(a.WSTurnID)))
	a.Domain.Provider = OpenAIUpstreamProvider(strings.TrimSpace(string(a.Domain.Provider)))
	a.Domain.UpstreamFingerprint = OpenAIUpstreamFingerprint(strings.TrimSpace(string(a.Domain.UpstreamFingerprint)))
	a.Domain.EffectiveModel = strings.TrimSpace(a.Domain.EffectiveModel)
	a.Domain.ContractVersion = strings.TrimSpace(a.Domain.ContractVersion)
	a.Semantic.Outcome = strings.TrimSpace(a.Semantic.Outcome)
	a.Semantic.TerminalEvent = strings.TrimSpace(a.Semantic.TerminalEvent)
}

func (a UpstreamAttemptAttribution) Validate() error {
	if strings.TrimSpace(string(a.AttemptID)) == "" ||
		strings.TrimSpace(string(a.ClientRequestID)) == "" ||
		strings.TrimSpace(string(a.GatewayRequestID)) == "" ||
		a.AccountID <= 0 || !a.Domain.Valid() || a.StartedAt.IsZero() || a.ObservedAt.IsZero() || a.StateVersion <= 0 {
		return ErrUpstreamAttemptInvalid
	}
	switch a.Transport {
	case UpstreamAttemptTransportHTTP, UpstreamAttemptTransportWebSocket, UpstreamAttemptTransportProbe:
	default:
		return ErrUpstreamAttemptInvalid
	}
	switch a.State {
	case UpstreamAttemptStateStarted, UpstreamAttemptStateInProgress:
		if a.CompletedAt != nil || a.Semantic.SuccessfulTerminal {
			return ErrUpstreamAttemptInvalid
		}
	case UpstreamAttemptStateTerminal:
		if a.CompletedAt == nil || a.CompletedAt.Before(a.StartedAt) {
			return ErrUpstreamAttemptInvalid
		}
	default:
		return ErrUpstreamAttemptInvalid
	}
	if a.HTTPObserved != (a.HTTPStatus != nil) || (a.HTTPStatus != nil && (*a.HTTPStatus < 100 || *a.HTTPStatus > 999)) {
		return ErrUpstreamAttemptInvalid
	}
	if !a.TransportObserved && a.HTTPObserved {
		return ErrUpstreamAttemptInvalid
	}
	if !a.SemanticObserved && (a.Semantic.Outcome != "" || a.Semantic.OutputItemDoneCount != 0 || a.Semantic.CompactionItemCount != 0 ||
		a.Semantic.MalformedItemCount != 0 || a.Semantic.TerminalEvent != "" || a.Semantic.TerminalCount != 0 || a.Semantic.SuccessfulTerminal) {
		return ErrUpstreamAttemptInvalid
	}
	if !a.DeliveryObserved && a.DeliveryCommitted {
		return ErrUpstreamAttemptInvalid
	}
	if a.DeliveryCommitted && a.SafeToFailover {
		return ErrUpstreamAttemptInvalid
	}
	if a.Semantic.OutputItemDoneCount < 0 || a.Semantic.CompactionItemCount < 0 || a.Semantic.MalformedItemCount < 0 || a.Semantic.TerminalCount < 0 {
		return ErrUpstreamAttemptInvalid
	}
	if !a.Usage.Observed && upstreamAttemptUsageHasValues(a.Usage) {
		return ErrUpstreamAttemptInvalid
	}
	if a.Usage.Observed && !upstreamAttemptUsageHasValues(a.Usage) {
		return ErrUpstreamAttemptInvalid
	}
	if upstreamAttemptUsageHasNegative(a.Usage) {
		return ErrUpstreamAttemptInvalid
	}
	return nil
}

func upstreamAttemptUsageHasValues(usage UpstreamAttemptUsage) bool {
	return usage.InputTokens != nil || usage.OutputTokens != nil || usage.CacheCreationInputTokens != nil ||
		usage.CacheReadInputTokens != nil || usage.ImageInputTokens != nil || usage.ImageOutputTokens != nil || usage.CostUSD != nil
}

func upstreamAttemptUsageHasNegative(usage UpstreamAttemptUsage) bool {
	for _, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens, usage.ImageInputTokens, usage.ImageOutputTokens} {
		if value != nil && *value < 0 {
			return true
		}
	}
	return usage.CostUSD != nil && *usage.CostUSD < 0
}

// UpstreamAttemptAttributionRepository is an idempotent telemetry port. Upsert
// applies a monotonic CAS on StateVersion; equal-version retries are no-ops,
// older versions cannot overwrite newer state, and terminal rows are immutable.
type UpstreamAttemptAttributionRepository interface {
	Upsert(ctx context.Context, attribution UpstreamAttemptAttribution) (UpstreamAttemptAttribution, error)
	Get(ctx context.Context, attemptID AttemptID) (*UpstreamAttemptAttribution, error)
}
