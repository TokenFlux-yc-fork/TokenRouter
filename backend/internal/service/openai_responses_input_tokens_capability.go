package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const OpenAIResponsesInputTokensContractVersion = "responses_input_tokens_v1"

// OpenAIResponsesInputTokensCapabilityState is the passive-learned state for
// the /v1/responses/input_tokens endpoint. Unknown is intentionally fail-open
// for a single real request; negative states must never be inferred from
// transient upstream failures.
type OpenAIResponsesInputTokensCapabilityState string

const (
	OpenAIResponsesInputTokensCapabilityUnknown     OpenAIResponsesInputTokensCapabilityState = "unknown"
	OpenAIResponsesInputTokensCapabilitySupported   OpenAIResponsesInputTokensCapabilityState = "supported"
	OpenAIResponsesInputTokensCapabilityUnsupported OpenAIResponsesInputTokensCapabilityState = "unsupported"
	OpenAIResponsesInputTokensCapabilityScopeDenied OpenAIResponsesInputTokensCapabilityState = "scope_denied"
)

type OpenAIResponsesInputTokensCapabilityKey struct {
	AccountID           int64
	UpstreamFingerprint OpenAIUpstreamFingerprint
	EffectiveModel      string
	ContractVersion     string
	ConfigGeneration    string
}

func (k OpenAIResponsesInputTokensCapabilityKey) Valid() bool {
	return k.AccountID > 0 && strings.TrimSpace(string(k.UpstreamFingerprint)) != "" &&
		strings.TrimSpace(k.EffectiveModel) != "" && strings.TrimSpace(k.ContractVersion) != "" &&
		strings.TrimSpace(k.ConfigGeneration) != ""
}

func ResolveOpenAIResponsesInputTokensCapabilityKey(account *Account, effectiveModel string) (OpenAIResponsesInputTokensCapabilityKey, error) {
	if account == nil {
		return OpenAIResponsesInputTokensCapabilityKey{}, errors.New("openai input tokens account is nil")
	}
	if !account.IsOpenAICompatible() {
		return OpenAIResponsesInputTokensCapabilityKey{}, errors.New("openai input tokens requires an OpenAI-compatible account")
	}
	effectiveModel = strings.TrimSpace(effectiveModel)
	if effectiveModel == "" {
		return OpenAIResponsesInputTokensCapabilityKey{}, errors.New("openai input tokens effective model is empty")
	}
	endpoint, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return OpenAIResponsesInputTokensCapabilityKey{}, fmt.Errorf("resolve openai input tokens endpoint: %w", err)
	}
	fingerprint, err := NewOpenAIUpstreamFingerprint(endpoint, "responses_input_tokens")
	if err != nil {
		return OpenAIResponsesInputTokensCapabilityKey{}, fmt.Errorf("fingerprint openai input tokens upstream: %w", err)
	}
	configGeneration, err := openAIResponsesInputTokensConfigGeneration(account)
	if err != nil {
		return OpenAIResponsesInputTokensCapabilityKey{}, fmt.Errorf("derive openai input tokens config generation: %w", err)
	}
	key := OpenAIResponsesInputTokensCapabilityKey{AccountID: account.ID, UpstreamFingerprint: fingerprint, EffectiveModel: effectiveModel, ContractVersion: OpenAIResponsesInputTokensContractVersion, ConfigGeneration: configGeneration}
	if !key.Valid() {
		return OpenAIResponsesInputTokensCapabilityKey{}, errors.New("openai input tokens capability key is invalid")
	}
	return key, nil
}

func openAIResponsesInputTokensConfigGeneration(account *Account) (string, error) {
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(credentials)
	return "credentials_sha256:" + hex.EncodeToString(sum[:]), nil
}

// OpenAIResponsesInputTokensCapability is an account-projected capability row.
type OpenAIResponsesInputTokensCapability struct {
	Key   OpenAIResponsesInputTokensCapabilityKey
	State OpenAIResponsesInputTokensCapabilityState
}

func (c OpenAIResponsesInputTokensCapability) Valid() bool {
	if !c.Key.Valid() {
		return false
	}
	switch c.State {
	case OpenAIResponsesInputTokensCapabilityUnknown,
		OpenAIResponsesInputTokensCapabilitySupported,
		OpenAIResponsesInputTokensCapabilityUnsupported,
		OpenAIResponsesInputTokensCapabilityScopeDenied:
		return true
	default:
		return false
	}
}

func (a *Account) SupportsOpenAIResponsesInputTokens(key OpenAIResponsesInputTokensCapabilityKey) bool {
	if a == nil || a.ID != key.AccountID || !key.Valid() || !a.IsOpenAICompatible() ||
		!a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses) {
		return false
	}
	for _, capability := range a.OpenAIResponsesInputTokensCapabilities {
		if capability.Key != key || !capability.Valid() {
			continue
		}
		return capability.State == OpenAIResponsesInputTokensCapabilitySupported ||
			capability.State == OpenAIResponsesInputTokensCapabilityUnknown
	}
	return true // no row means unknown and permits one passive probe
}
