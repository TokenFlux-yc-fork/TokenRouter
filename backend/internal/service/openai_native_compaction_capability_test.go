package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionCapabilityAllowsMatrix(t *testing.T) {
	now := time.Unix(1000, 0)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	older := now.Add(-2 * time.Hour)
	stale := now.Add(-openAINativeCompactionProbeFreshness - time.Second)
	key := OpenAINativeCompactionCapabilityKey{
		AccountID:           7,
		UpstreamFingerprint: "upstream_v1_synthetic",
		EffectiveModel:      "gpt-synthetic",
		ContractVersion:     OpenAINativeCompactionContractVersion,
	}

	tests := []struct {
		name       string
		capability OpenAINativeCompactionCapability
		want       bool
	}{
		{name: "fresh probe supported", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "auto", Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now}, want: true},
		{name: "probe supported without observation", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "auto", Source: OpenAINativeCompactionCapabilitySourceProbe}},
		{name: "stale probe supported", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "auto", Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &stale}},
		{name: "probe unsupported", capability: OpenAINativeCompactionCapability{Key: key, Mode: "auto", Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now}},
		{name: "trusted official requires account gate", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Source: OpenAINativeCompactionCapabilitySourceTrustedOfficial}},
		{name: "invalid source", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Source: "oauth"}},
		{name: "invalid mode", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "forced", Source: OpenAINativeCompactionCapabilitySourceProbe}},
		{name: "active force on", capability: OpenAINativeCompactionCapability{Key: key, Mode: "force_on", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &past, OverrideExpiresAt: &future}, want: true},
		{name: "permanent force on rejected", capability: OpenAINativeCompactionCapability{Key: key, Mode: "force_on", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &past}},
		{name: "blank reason force on rejected", capability: OpenAINativeCompactionCapability{Key: key, Mode: "force_on", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideCreatedAt: &past, OverrideExpiresAt: &future}},
		{name: "expired force on", capability: OpenAINativeCompactionCapability{Key: key, Mode: "force_on", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &older, OverrideExpiresAt: &past}},
		{name: "revoked force on", capability: OpenAINativeCompactionCapability{Key: key, Mode: "force_on", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &older, OverrideExpiresAt: &future, OverrideRevokedAt: &past}},
		{name: "active force off", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "force_off", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &past, OverrideExpiresAt: &future}},
		{name: "expired force off fails closed until reconciled probe", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "force_off", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &older, OverrideExpiresAt: &past}},
		{name: "revoked force off fails closed until reconciled probe", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: "force_off", Source: OpenAINativeCompactionCapabilitySourceManualOverride, OverrideActor: "operator", OverrideReason: "incident", OverrideCreatedAt: &older, OverrideExpiresAt: &future, OverrideRevokedAt: &past}},
		{name: "quarantined", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now, QuarantinedUntil: &future}},
		{name: "expired quarantine", capability: OpenAINativeCompactionCapability{Key: key, Supported: true, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now, QuarantinedUntil: &past}, want: true},
		{name: "invalid key", capability: OpenAINativeCompactionCapability{Supported: true, Source: OpenAINativeCompactionCapabilitySourceProbe}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.capability.Allows(now))
		})
	}
}

func TestAccountSupportsOpenAINativeRemoteCompactionV2TrustedOfficialContract(t *testing.T) {
	now := time.Now().UTC()
	officialFingerprint, err := NewOpenAIUpstreamFingerprint(chatgptCodexURL, "responses")
	require.NoError(t, err)
	customFingerprint, err := NewOpenAIUpstreamFingerprint("https://oauth-proxy.example.test/v1/responses", "responses")
	require.NoError(t, err)

	tests := []struct {
		name        string
		accountID   int64
		accountType string
		platform    string
		fingerprint OpenAIUpstreamFingerprint
		want        bool
	}{
		{name: "official OAuth", accountID: 42, accountType: AccountTypeOAuth, platform: PlatformOpenAI, fingerprint: officialFingerprint, want: true},
		{name: "mismatched account ID", accountID: 43, accountType: AccountTypeOAuth, platform: PlatformOpenAI, fingerprint: officialFingerprint},
		{name: "API key official endpoint", accountID: 42, accountType: AccountTypeAPIKey, platform: PlatformOpenAI, fingerprint: officialFingerprint},
		{name: "custom OAuth fingerprint", accountID: 42, accountType: AccountTypeOAuth, platform: PlatformOpenAI, fingerprint: customFingerprint},
		{name: "other provider OAuth", accountID: 42, accountType: AccountTypeOAuth, platform: PlatformGrok, fingerprint: officialFingerprint},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := OpenAINativeCompactionCapabilityKey{
				AccountID: 42, UpstreamFingerprint: test.fingerprint,
				EffectiveModel: "gpt-5", ContractVersion: OpenAINativeCompactionContractVersion,
			}
			account := &Account{
				ID: test.accountID, Platform: test.platform, Type: test.accountType,
				Extra: map[string]any{"openai_responses_supported": true},
				OpenAINativeCompactionCapabilities: []OpenAINativeCompactionCapability{{
					Key: key, Supported: true, Mode: OpenAINativeCompactionCapabilityModeAuto,
					Source: OpenAINativeCompactionCapabilitySourceTrustedOfficial,
				}},
			}
			require.Equal(t, test.want, account.SupportsOpenAINativeRemoteCompactionV2(key, now))
		})
	}
}

func TestOpenAINativeCompactionSchedulingRequiresExactCandidateCapability(t *testing.T) {
	now := time.Now()
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)
	account := &Account{
		ID:          17,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture", "model_mapping": map[string]any{"client-model": "upstream-model"}},
		Extra:       map[string]any{"openai_responses_supported": true},
	}

	require.False(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "client-model", false, OpenAIEndpointCapabilityResponses))
	fingerprint, err := NewOpenAIUpstreamFingerprint(openaiPlatformAPIURL, "responses")
	require.NoError(t, err)
	account.OpenAINativeCompactionCapabilities = []OpenAINativeCompactionCapability{{
		Key: OpenAINativeCompactionCapabilityKey{
			AccountID:           account.ID,
			UpstreamFingerprint: fingerprint,
			EffectiveModel:      "upstream-model",
			ContractVersion:     OpenAINativeCompactionContractVersion,
		},
		Supported: true,
		Mode:      OpenAINativeCompactionCapabilityModeAuto,
		Source:    OpenAINativeCompactionCapabilitySourceProbe,
		CheckedAt: &now,
	}}
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "client-model", false, OpenAIEndpointCapabilityResponses))

	account.OpenAINativeCompactionCapabilities[0].Key.EffectiveModel = "other-model"
	require.False(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "client-model", false, OpenAIEndpointCapabilityResponses))
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(t.Context(), account, PlatformOpenAI, "client-model", false, OpenAIEndpointCapabilityResponses), "ordinary Responses routing must not require native capability")
}

func TestOpenAINativeCompactionCapabilityKeyIsSharedBySchedulerAndProbe(t *testing.T) {
	account := &Account{
		ID:       29,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "fixture",
			"base_url":      "https://user:secret@api.example.test/tenant/v1/responses/compact?token=secret#fragment",
			"model_mapping": map[string]any{"client-model": "upstream-model"},
		},
	}

	probeKey, err := ResolveOpenAINativeCompactionProbeKey(account, "client-model")
	require.NoError(t, err)
	effectiveModel := resolveOpenAIAccountUpstreamModelForRequest(account, "client-model", false, false)
	schedulerKey, err := ResolveOpenAINativeCompactionCapabilityKey(account, effectiveModel)
	require.NoError(t, err)
	require.Equal(t, schedulerKey, probeKey)
	require.Equal(t, "upstream-model", probeKey.EffectiveModel)
	require.Equal(t, OpenAINativeCompactionContractVersion, probeKey.ContractVersion)
}

func TestOpenAINativeCompactionSchedulingDoesNotReuseCapabilityAcrossTenantPaths(t *testing.T) {
	now := time.Now()
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)
	account := &Account{
		ID:          31,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "fixture", "base_url": "https://api.example.test/tenant-b/v1"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	otherTenant := *account
	otherTenant.Credentials = map[string]any{"api_key": "fixture", "base_url": "https://api.example.test/tenant-a/v1"}
	otherTenantKey, err := ResolveOpenAINativeCompactionCapabilityKey(&otherTenant, "gpt-synthetic")
	require.NoError(t, err)
	account.OpenAINativeCompactionCapabilities = []OpenAINativeCompactionCapability{{
		Key:       otherTenantKey,
		Supported: true,
		Mode:      OpenAINativeCompactionCapabilityModeAuto,
		Source:    OpenAINativeCompactionCapabilitySourceProbe,
		CheckedAt: &now,
	}}

	require.False(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "gpt-synthetic", false, OpenAIEndpointCapabilityResponses))
	matchingKey, err := ResolveOpenAINativeCompactionCapabilityKey(account, "gpt-synthetic")
	require.NoError(t, err)
	require.NotEqual(t, otherTenantKey.UpstreamFingerprint, matchingKey.UpstreamFingerprint)
	account.OpenAINativeCompactionCapabilities[0].Key = matchingKey
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "gpt-synthetic", false, OpenAIEndpointCapabilityResponses))
}

func TestAccountSupportsOpenAINativeRemoteCompactionV2ExactKeyOnly(t *testing.T) {
	now := time.Unix(1000, 0)
	key := OpenAINativeCompactionCapabilityKey{
		AccountID:           7,
		UpstreamFingerprint: "upstream_v1_synthetic",
		EffectiveModel:      "gpt-synthetic",
		ContractVersion:     OpenAINativeCompactionContractVersion,
	}
	account := &Account{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{"openai_responses_supported": true},
		OpenAINativeCompactionCapabilities: []OpenAINativeCompactionCapability{{
			Key: key, Supported: true, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now,
		}},
	}

	require.True(t, account.SupportsOpenAINativeRemoteCompactionV2(key, now))
	mismatch := key
	mismatch.EffectiveModel = "other-model"
	require.False(t, account.SupportsOpenAINativeRemoteCompactionV2(mismatch, now))
	mismatch = key
	mismatch.UpstreamFingerprint = "upstream_v1_other"
	require.False(t, account.SupportsOpenAINativeRemoteCompactionV2(mismatch, now))
	mismatch = key
	mismatch.ContractVersion = "other-contract"
	require.False(t, account.SupportsOpenAINativeRemoteCompactionV2(mismatch, now))
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityNativeRemoteCompactionV2), "account-only API must fail closed without candidate key")
}
