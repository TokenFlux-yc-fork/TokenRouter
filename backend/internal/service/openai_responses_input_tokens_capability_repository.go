package service

import (
	"context"
	"errors"
	"time"
)

// OpenAIResponsesInputTokensCapabilityRecord is the persisted exact-key state.
type OpenAIResponsesInputTokensCapabilityRecord struct {
	ID int64
	OpenAIResponsesInputTokensCapability
	CheckedAt   *time.Time
	LastStatus  *int
	LastOutcome string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OpenAIResponsesInputTokensCapabilityObservation is accepted only for
// definitive upstream outcomes. Transport, transient, and malformed responses
// must not be represented as unsupported states by callers.
type OpenAIResponsesInputTokensCapabilityObservation struct {
	Key         OpenAIResponsesInputTokensCapabilityKey
	State       OpenAIResponsesInputTokensCapabilityState
	StatusCode  *int
	LastOutcome string
	CheckedAt   time.Time
}

var (
	ErrOpenAIResponsesInputTokensCapabilityIdentityChanged = errors.New("openai input tokens capability identity changed")
	ErrOpenAIResponsesInputTokensCapabilityInvalidOutcome  = errors.New("openai input tokens capability outcome is not definitive")
)

type OpenAIResponsesInputTokensCapabilityRepository interface {
	GetExact(context.Context, OpenAIResponsesInputTokensCapabilityKey) (*OpenAIResponsesInputTokensCapabilityRecord, error)
	EnsureUnknown(context.Context, *Account, OpenAIResponsesInputTokensCapabilityKey) (bool, error)
	UpsertObservation(context.Context, *Account, OpenAIResponsesInputTokensCapabilityObservation) (bool, error)
}
