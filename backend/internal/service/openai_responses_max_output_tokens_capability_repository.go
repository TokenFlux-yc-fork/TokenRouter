package service

import (
	"context"
	"errors"
	"time"
)

// OpenAIResponsesMaxOutputTokensCapabilityState 是真实 Responses 请求被动学习的精确能力状态。
type OpenAIResponsesMaxOutputTokensCapabilityState string

const (
	OpenAIResponsesMaxOutputTokensCapabilityUnknown     OpenAIResponsesMaxOutputTokensCapabilityState = "unknown"
	OpenAIResponsesMaxOutputTokensCapabilitySupported   OpenAIResponsesMaxOutputTokensCapabilityState = "supported"
	OpenAIResponsesMaxOutputTokensCapabilityUnsupported OpenAIResponsesMaxOutputTokensCapabilityState = "unsupported"
)

type OpenAIResponsesMaxOutputTokensCapabilityRecord struct {
	ID          int64
	Key         OpenAIResponsesMaxOutputTokensCapabilityKey
	State       OpenAIResponsesMaxOutputTokensCapabilityState
	CheckedAt   *time.Time
	LastStatus  *int
	LastOutcome string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type OpenAIResponsesMaxOutputTokensCapabilityObservation struct {
	Key         OpenAIResponsesMaxOutputTokensCapabilityKey
	State       OpenAIResponsesMaxOutputTokensCapabilityState
	StatusCode  *int
	LastOutcome string
	CheckedAt   time.Time
}

var ErrOpenAIResponsesMaxOutputTokensCapabilityInvalidOutcome = errors.New("openai max_output_tokens capability outcome is not definitive")

type OpenAIResponsesMaxOutputTokensCapabilityRepository interface {
	GetExact(context.Context, OpenAIResponsesMaxOutputTokensCapabilityKey) (*OpenAIResponsesMaxOutputTokensCapabilityRecord, error)
	EnsureUnknown(context.Context, *Account, OpenAIResponsesMaxOutputTokensCapabilityKey) (bool, error)
	UpsertObservation(context.Context, *Account, OpenAIResponsesMaxOutputTokensCapabilityObservation) (bool, error)
}
