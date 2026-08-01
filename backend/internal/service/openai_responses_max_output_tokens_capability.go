package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAIResponsesMaxOutputTokensCapabilityVersion identifies the exact
// candidate-specific negative capability contract.
const OpenAIResponsesMaxOutputTokensCapabilityVersion = "responses_max_output_tokens_v1"

const (
	OpenAIResponsesMaxOutputTokensUnsupportedReason        GatewayFailureReason = "openai_responses_max_output_tokens_unsupported"
	OpenAIResponsesMaxOutputTokensUnsupportedClientMessage                      = "No available upstream account supports max_output_tokens"
)

type OpenAIResponsesMaxOutputTokensCapabilityKey struct {
	AccountID           int64
	UpstreamFingerprint OpenAIUpstreamFingerprint
	EffectiveModel      string
	Version             string
	ConfigGeneration    string
}

func (k OpenAIResponsesMaxOutputTokensCapabilityKey) Valid() bool {
	return k.AccountID > 0 && strings.TrimSpace(string(k.UpstreamFingerprint)) != "" &&
		strings.TrimSpace(k.EffectiveModel) != "" && k.Version == OpenAIResponsesMaxOutputTokensCapabilityVersion &&
		strings.TrimSpace(k.ConfigGeneration) != ""
}

// ResolveOpenAIResponsesMaxOutputTokensCapabilityKey computes the exact
// account/upstream/effective-model/version identity used by this capability.
func ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account *Account, effectiveModel string) (OpenAIResponsesMaxOutputTokensCapabilityKey, error) {
	if account == nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens account is nil")
	}
	if !account.IsOpenAICompatible() {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens requires an OpenAI-compatible account")
	}
	effectiveModel = strings.TrimSpace(effectiveModel)
	if effectiveModel == "" {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens effective model is empty")
	}
	endpoint, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, err
	}
	fingerprint, err := NewOpenAIUpstreamFingerprint(endpoint, "responses")
	if err != nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, err
	}
	configGeneration, err := openAIResponsesInputTokensConfigGeneration(account)
	if err != nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, fmt.Errorf("derive responses max_output_tokens config generation: %w", err)
	}
	key := OpenAIResponsesMaxOutputTokensCapabilityKey{
		AccountID: account.ID, UpstreamFingerprint: fingerprint,
		EffectiveModel: effectiveModel, Version: OpenAIResponsesMaxOutputTokensCapabilityVersion,
		ConfigGeneration: configGeneration,
	}
	if !key.Valid() {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens capability key is invalid")
	}
	return key, nil
}

func (s *OpenAIGatewayService) prepareOpenAIResponsesMaxOutputTokensCapability(ctx context.Context, account *Account, body []byte, effectiveModel string) (OpenAIResponsesMaxOutputTokensCapabilityKey, bool) {
	if s == nil || s.openAIResponsesMaxOutputTokensCapabilityRepo == nil || !gjson.GetBytes(body, "max_output_tokens").Exists() {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, false
	}
	key, err := ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, effectiveModel)
	if err != nil {
		slog.Warn("openai_max_output_tokens_capability_identity_failed", "account_id", account.ID, "error", err)
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, false
	}
	if _, err := s.openAIResponsesMaxOutputTokensCapabilityRepo.EnsureUnknown(ctx, account, key); err != nil {
		slog.Warn("openai_max_output_tokens_capability_ensure_failed", "account_id", account.ID, "error", err)
	}
	return key, true
}

func (s *OpenAIGatewayService) observeOpenAIResponsesMaxOutputTokensCapability(ctx context.Context, account *Account, key OpenAIResponsesMaxOutputTokensCapabilityKey, state OpenAIResponsesMaxOutputTokensCapabilityState, statusCode int, outcome string) {
	if s == nil || s.openAIResponsesMaxOutputTokensCapabilityRepo == nil || !key.Valid() {
		return
	}
	observation := OpenAIResponsesMaxOutputTokensCapabilityObservation{
		Key: key, State: state, StatusCode: &statusCode,
		LastOutcome: outcome, CheckedAt: time.Now().UTC(),
	}
	if _, err := s.openAIResponsesMaxOutputTokensCapabilityRepo.UpsertObservation(ctx, account, observation); err != nil {
		slog.Warn("openai_max_output_tokens_capability_observe_failed", "account_id", account.ID, "state", state, "status_code", statusCode, "error", err)
	}
}

func (s *OpenAIGatewayService) maxOutputTokensCapabilityKnownUnsupported(ctx context.Context, key OpenAIResponsesMaxOutputTokensCapabilityKey) bool {
	if s == nil || s.openAIResponsesMaxOutputTokensCapabilityRepo == nil || !key.Valid() {
		return false
	}
	record, err := s.openAIResponsesMaxOutputTokensCapabilityRepo.GetExact(ctx, key)
	if err != nil {
		slog.Warn("openai_max_output_tokens_capability_lookup_failed", "account_id", key.AccountID, "error", err)
		return false
	}
	return record != nil && record.State == OpenAIResponsesMaxOutputTokensCapabilityUnsupported
}

// OpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported performs an exact,
// fail-open lookup for handler-side candidate filtering in strict mode.
func (s *OpenAIGatewayService) OpenAIResponsesMaxOutputTokensCapabilityKnownUnsupported(ctx context.Context, account *Account, body []byte, requestedModel string, requireCompact bool) bool {
	if s == nil || account == nil || !gjson.GetBytes(body, "max_output_tokens").Exists() {
		return false
	}
	effectiveModel := resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, requireCompact, openAIHTTPPassthroughRoutingFromContext(ctx))
	key, err := ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, effectiveModel)
	if err != nil {
		slog.Warn("openai_max_output_tokens_capability_identity_failed", "account_id", account.ID, "error", err)
		return false
	}
	return s.maxOutputTokensCapabilityKnownUnsupported(ctx, key)
}

func NewOpenAIResponsesMaxOutputTokensUnsupportedFailoverError(statusCode int) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:                     statusCode,
		Stage:                          GatewayFailureStageInference,
		Scope:                          GatewayFailureScopeProvider,
		Reason:                         OpenAIResponsesMaxOutputTokensUnsupportedReason,
		NextAccountAction:              NextAccountRetry,
		SuppressAccountScheduleFailure: true,
		ClientStatusCode:               http.StatusBadGateway,
		ClientMessage:                  OpenAIResponsesMaxOutputTokensUnsupportedClientMessage,
	}
}

func (e *UpstreamFailoverError) IsOpenAIResponsesMaxOutputTokensUnsupported() bool {
	return e != nil && e.Reason == OpenAIResponsesMaxOutputTokensUnsupportedReason
}

// IsExplicitOpenAIResponsesMaxOutputTokensUnsupported accepts only a top-level
// structured unsupported-parameter rejection. It deliberately rejects nested
// fields, free-form detail text, and all non-400 statuses.
func IsExplicitOpenAIResponsesMaxOutputTokensUnsupported(statusCode int, responseBody []byte) bool {
	if statusCode != 400 || !gjson.ValidBytes(responseBody) {
		return false
	}
	error := gjson.GetBytes(responseBody, "error")
	if !error.Exists() || !error.IsObject() {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(error.Get("code").String()))
	param := strings.ToLower(strings.TrimSpace(error.Get("param").String()))
	message := strings.ToLower(strings.TrimSpace(error.Get("message").String()))
	if param != "max_output_tokens" || (code != "unknown_parameter" && code != "unsupported_parameter") {
		return false
	}
	return strings.Contains(message, "unsupported parameter") || strings.Contains(message, "unknown parameter")
}
