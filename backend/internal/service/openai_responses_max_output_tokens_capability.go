package service

import (
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// OpenAIResponsesMaxOutputTokensCapabilityVersion identifies the exact
// candidate-specific negative capability contract.
const OpenAIResponsesMaxOutputTokensCapabilityVersion = "responses_max_output_tokens_v1"

type OpenAIResponsesMaxOutputTokensCapabilityKey struct {
	AccountID           int64
	UpstreamFingerprint OpenAIUpstreamFingerprint
	EffectiveModel      string
	Version             string
}

func (k OpenAIResponsesMaxOutputTokensCapabilityKey) Valid() bool {
	return k.AccountID > 0 && strings.TrimSpace(string(k.UpstreamFingerprint)) != "" &&
		strings.TrimSpace(k.EffectiveModel) != "" && k.Version == OpenAIResponsesMaxOutputTokensCapabilityVersion
}

// ResolveOpenAIResponsesMaxOutputTokensCapabilityKey computes the exact
// account/upstream/effective-model/version identity used by this capability.
func ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account *Account, effectiveModel string) (OpenAIResponsesMaxOutputTokensCapabilityKey, error) {
	if account == nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens account is nil")
	}
	endpoint, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, err
	}
	fingerprint, err := NewOpenAIUpstreamFingerprint(endpoint, "responses")
	if err != nil {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, err
	}
	key := OpenAIResponsesMaxOutputTokensCapabilityKey{
		AccountID: account.ID, UpstreamFingerprint: fingerprint,
		EffectiveModel: strings.TrimSpace(effectiveModel), Version: OpenAIResponsesMaxOutputTokensCapabilityVersion,
	}
	if !key.Valid() {
		return OpenAIResponsesMaxOutputTokensCapabilityKey{}, errors.New("responses max_output_tokens capability key is invalid")
	}
	return key, nil
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
