package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveOpenAICanonicalResponsesEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    string
	}{
		{
			name: "default API key endpoint",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "fixture"}},
			want: openaiPlatformAPIURL,
		},
		{
			name: "v1 base",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://api.example.test/v1"}},
			want: "https://api.example.test/v1/responses",
		},
		{
			name: "responses endpoint",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://api.example.test/responses"}},
			want: "https://api.example.test/responses",
		},
		{
			name: "v1 responses endpoint",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://api.example.test/v1/responses"}},
			want: "https://api.example.test/v1/responses",
		},
		{
			name: "compact endpoint",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://api.example.test/responses/compact"}},
			want: "https://api.example.test/responses",
		},
		{
			name: "tenant v1 compact endpoint drops sensitive URL components",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://user:secret@api.example.test/tenant/v1/responses/compact?token=secret#fragment"}},
			want: "https://api.example.test/tenant/v1/responses",
		},
		{
			name: "OAuth ignores configured base URL",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"base_url": "https://other.example.test/tenant"}},
			want: chatgptCodexURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveOpenAICanonicalResponsesEndpoint(tt.account)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIUpstreamFingerprintSanitizesSecretsAndTransport(t *testing.T) {
	httpsFingerprint, err := NewOpenAIUpstreamFingerprint(
		"https://user:secret@API.Example.test:443/tenant/v1/responses?token=secret#fragment",
		"responses",
	)
	require.NoError(t, err)
	require.NotEmpty(t, httpsFingerprint)
	require.NotContains(t, string(httpsFingerprint), "secret")
	require.NotContains(t, string(httpsFingerprint), "example.test")

	wsFingerprint, err := NewOpenAIUpstreamFingerprint(
		"wss://api.example.test/tenant/v1/responses?different=credential#ignored",
		"/responses/",
	)
	require.NoError(t, err)
	require.Equal(t, httpsFingerprint, wsFingerprint)

	otherTenant, err := NewOpenAIUpstreamFingerprint(
		"https://api.example.test/other-tenant/v1/responses",
		"responses",
	)
	require.NoError(t, err)
	require.NotEqual(t, httpsFingerprint, otherTenant)

	otherHost, err := NewOpenAIUpstreamFingerprint("https://other.example.test/tenant/v1/responses", "responses")
	require.NoError(t, err)
	require.NotEqual(t, httpsFingerprint, otherHost)
}

func TestOpenAICompatibilityDomainFailsClosed(t *testing.T) {
	fingerprint, err := NewOpenAIUpstreamFingerprint("https://api.example.test/v1/responses", "responses")
	require.NoError(t, err)

	domain := OpenAICompatibilityDomain{
		Provider:            OpenAIUpstreamProvider("openai"),
		UpstreamFingerprint: fingerprint,
		EffectiveModel:      "gpt-5.6-sol",
		ContractVersion:     "remote-compaction-v2",
	}
	require.True(t, domain.Valid())
	require.True(t, domain.CompatibleWith(domain))

	cases := []OpenAICompatibilityDomain{
		{},
		{Provider: domain.Provider, UpstreamFingerprint: domain.UpstreamFingerprint, EffectiveModel: "other", ContractVersion: domain.ContractVersion},
		{Provider: "custom", UpstreamFingerprint: domain.UpstreamFingerprint, EffectiveModel: domain.EffectiveModel, ContractVersion: domain.ContractVersion},
		{Provider: domain.Provider, UpstreamFingerprint: domain.UpstreamFingerprint, EffectiveModel: domain.EffectiveModel, ContractVersion: "future-contract"},
	}
	for _, candidate := range cases {
		require.False(t, domain.CompatibleWith(candidate))
		require.False(t, candidate.CompatibleWith(domain))
	}
}
