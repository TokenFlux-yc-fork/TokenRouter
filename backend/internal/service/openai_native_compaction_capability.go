package service

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const OpenAINativeCompactionContractVersion = "remote_compaction_v2"

// A successful probe is an admission fact only until its normal successful
// reprobe deadline. The runner default uses the same 24-hour interval.
const openAINativeCompactionProbeFreshness = 24 * time.Hour

const (
	OpenAINativeCompactionCapabilityModeAuto     = "auto"
	OpenAINativeCompactionCapabilityModeForceOn  = "force_on"
	OpenAINativeCompactionCapabilityModeForceOff = "force_off"
)

const (
	OpenAINativeCompactionCapabilitySourceProbe           = "probe"
	OpenAINativeCompactionCapabilitySourceTrustedOfficial = "trusted_official"
	OpenAINativeCompactionCapabilitySourceManualOverride  = "manual_override"
)

type OpenAINativeCompactionCapabilityKey struct {
	AccountID           int64
	UpstreamFingerprint OpenAIUpstreamFingerprint
	EffectiveModel      string
	ContractVersion     string
}

func (k OpenAINativeCompactionCapabilityKey) Valid() bool {
	return k.AccountID > 0 &&
		strings.TrimSpace(string(k.UpstreamFingerprint)) != "" &&
		strings.TrimSpace(k.EffectiveModel) != "" &&
		strings.TrimSpace(k.ContractVersion) != ""
}

// ResolveOpenAINativeCompactionCapabilityKey builds the exact capability
// identity after the caller has resolved the effective upstream model.
func ResolveOpenAINativeCompactionCapabilityKey(
	account *Account,
	effectiveModel string,
) (OpenAINativeCompactionCapabilityKey, error) {
	if account == nil {
		return OpenAINativeCompactionCapabilityKey{}, errors.New("openai native compaction account is nil")
	}
	if !account.IsOpenAICompatible() {
		return OpenAINativeCompactionCapabilityKey{}, errors.New("openai native compaction requires an OpenAI-compatible account")
	}
	effectiveModel = strings.TrimSpace(effectiveModel)
	if effectiveModel == "" {
		return OpenAINativeCompactionCapabilityKey{}, errors.New("openai native compaction effective model is empty")
	}
	endpoint, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return OpenAINativeCompactionCapabilityKey{}, fmt.Errorf("resolve openai native compaction endpoint: %w", err)
	}
	fingerprint, err := NewOpenAIUpstreamFingerprint(endpoint, "responses")
	if err != nil {
		return OpenAINativeCompactionCapabilityKey{}, fmt.Errorf("fingerprint openai native compaction upstream: %w", err)
	}
	key := OpenAINativeCompactionCapabilityKey{
		AccountID:           account.ID,
		UpstreamFingerprint: fingerprint,
		EffectiveModel:      effectiveModel,
		ContractVersion:     OpenAINativeCompactionContractVersion,
	}
	if !key.Valid() {
		return OpenAINativeCompactionCapabilityKey{}, errors.New("openai native compaction capability key is invalid")
	}
	return key, nil
}

type OpenAINativeCompactionCapability struct {
	Key OpenAINativeCompactionCapabilityKey

	Supported bool
	Mode      string
	Source    string

	CheckedAt           *time.Time
	LastStatus          *int
	LastSemanticFailure string
	QuarantinedUntil    *time.Time
	OverrideActor       string
	OverrideReason      string
	OverrideCreatedAt   *time.Time
	OverrideExpiresAt   *time.Time
	OverrideRevokedAt   *time.Time
}

func normalizeOpenAINativeCompactionCapabilityMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", OpenAINativeCompactionCapabilityModeAuto:
		return OpenAINativeCompactionCapabilityModeAuto
	case OpenAINativeCompactionCapabilityModeForceOn:
		return OpenAINativeCompactionCapabilityModeForceOn
	case OpenAINativeCompactionCapabilityModeForceOff:
		return OpenAINativeCompactionCapabilityModeForceOff
	default:
		return ""
	}
}

func (c OpenAINativeCompactionCapability) Valid() bool {
	if !c.Key.Valid() {
		return false
	}
	switch normalizeOpenAINativeCompactionCapabilityMode(c.Mode) {
	case OpenAINativeCompactionCapabilityModeForceOn, OpenAINativeCompactionCapabilityModeForceOff:
		return c.Source == OpenAINativeCompactionCapabilitySourceManualOverride &&
			strings.TrimSpace(c.OverrideActor) != "" &&
			strings.TrimSpace(c.OverrideReason) != "" &&
			c.OverrideCreatedAt != nil &&
			c.OverrideExpiresAt != nil &&
			c.OverrideExpiresAt.After(*c.OverrideCreatedAt)
	case OpenAINativeCompactionCapabilityModeAuto:
		return c.Source == OpenAINativeCompactionCapabilitySourceProbe ||
			c.Source == OpenAINativeCompactionCapabilitySourceTrustedOfficial
	default:
		return false
	}
}

func (c OpenAINativeCompactionCapability) Allows(now time.Time) bool {
	if !c.Valid() {
		return false
	}
	if c.QuarantinedUntil != nil && now.Before(*c.QuarantinedUntil) {
		return false
	}

	switch normalizeOpenAINativeCompactionCapabilityMode(c.Mode) {
	case OpenAINativeCompactionCapabilityModeForceOff:
		return false
	case OpenAINativeCompactionCapabilityModeForceOn:
		// Force-on is an explicit admission policy, but delivery still runs the
		// native stream validator and fails closed on invalid semantics.
		return c.overrideActive(now)
	default:
		if !c.Supported {
			return false
		}
		switch c.Source {
		case OpenAINativeCompactionCapabilitySourceProbe:
			return c.CheckedAt != nil && !c.CheckedAt.After(now) && now.Sub(*c.CheckedAt) <= openAINativeCompactionProbeFreshness
		case OpenAINativeCompactionCapabilitySourceTrustedOfficial:
			// Account type and canonical official identity are checked by the
			// account-aware gate below. A row alone is never sufficient evidence.
			return false
		default:
			return false
		}
	}
}

func (c OpenAINativeCompactionCapability) overrideActive(now time.Time) bool {
	if c.OverrideRevokedAt != nil || c.OverrideExpiresAt == nil {
		return false
	}
	return now.Before(*c.OverrideExpiresAt)
}

func (a *Account) SupportsOpenAINativeRemoteCompactionV2(
	key OpenAINativeCompactionCapabilityKey,
	now time.Time,
) bool {
	if a == nil || a.ID != key.AccountID || !a.IsOpenAICompatible() ||
		!a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses) {
		return false
	}
	for _, capability := range a.OpenAINativeCompactionCapabilities {
		if capability.Key != key {
			continue
		}
		if capability.Source == OpenAINativeCompactionCapabilitySourceTrustedOfficial {
			return capability.Valid() && capability.Supported &&
				a.TrustedOfficialOpenAINativeCompactionKey(key) &&
				(capability.QuarantinedUntil == nil || !now.Before(*capability.QuarantinedUntil))
		}
		return capability.Allows(now)
	}
	return false
}

func (a *Account) openAINativeRemoteCompactionCapabilitySource(key OpenAINativeCompactionCapabilityKey) string {
	if a == nil || a.ID != key.AccountID {
		return ""
	}
	for _, capability := range a.OpenAINativeCompactionCapabilities {
		if capability.Key == key && capability.Valid() {
			return strings.TrimSpace(capability.Source)
		}
	}
	return ""
}

func OfficialOpenAINativeCompactionFingerprint() (OpenAIUpstreamFingerprint, error) {
	return NewOpenAIUpstreamFingerprint(chatgptCodexURL, "responses")
}

func (a *Account) TrustedOfficialOpenAINativeCompactionKey(key OpenAINativeCompactionCapabilityKey) bool {
	if a == nil || a.ID != key.AccountID || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth || key.ContractVersion != OpenAINativeCompactionContractVersion {
		return false
	}
	officialEndpoint, err := ResolveOpenAICanonicalResponsesEndpoint(a)
	if err != nil || officialEndpoint != chatgptCodexURL {
		return false
	}
	officialFingerprint, err := OfficialOpenAINativeCompactionFingerprint()
	return err == nil && key.UpstreamFingerprint == officialFingerprint
}
