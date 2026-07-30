package service

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPricingServiceGetExactOpenAIProviderPricing(t *testing.T) {
	service := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.6-sol": {
			InputCostPerToken:  5e-6,
			OutputCostPerToken: 30e-6,
			LiteLLMProvider:    "openai",
		},
		"custom-model": {
			InputCostPerToken:  1e-6,
			OutputCostPerToken: 2e-6,
			LiteLLMProvider:    "custom",
		},
		"image-only": {
			LiteLLMProvider:    "openai",
			TokenPricingAbsent: true,
		},
		"fallback-model": {
			InputCostPerToken:  1e-6,
			OutputCostPerToken: 2e-6,
			LiteLLMProvider:    "openai",
		},
	}}

	for _, alias := range []string{"gpt-5.6", "openai/gpt-5.6", "gpt-5.6-sol"} {
		t.Run(alias, func(t *testing.T) {
			pricing, matched, err := service.GetExactOpenAIProviderPricing(alias)
			require.NoError(t, err)
			require.Equal(t, "gpt-5.6-sol", matched)
			require.Equal(t, 5e-6, pricing.InputCostPerToken)

			pricing.InputCostPerToken = 99
			require.Equal(t, 5e-6, service.pricingData["gpt-5.6-sol"].InputCostPerToken, "lookup must not expose the shared map value")
		})
	}

	for _, model := range []string{"", "missing-model", "fallback-model-version", "custom-model", "image-only", "custom/gpt-5.6", "anthropic/gpt-5.6"} {
		t.Run("reject_"+model, func(t *testing.T) {
			_, _, err := service.GetExactOpenAIProviderPricing(model)
			require.ErrorIs(t, err, ErrModelPricingUnavailable)
		})
	}
}

func TestQuoteOpenAINativeCompactionProbeMicroUSDUsesConservativeRates(t *testing.T) {
	price := OpenAIProviderTokenPrice{
		MatchedModel:                     "gpt-test",
		InputUSDPerToken:                 1e-6,
		InputPriorityUSDPerToken:         2e-6,
		CacheCreationUSDPerToken:         3e-6,
		CacheCreationPriorityUSDPerToken: 4e-6,
		CacheReadUSDPerToken:             5e-6,
		CacheReadPriorityUSDPerToken:     6e-6,
		OutputUSDPerToken:                7e-6,
		OutputPriorityUSDPerToken:        8e-6,
	}

	quote, err := QuoteOpenAINativeCompactionProbeMicroUSD(price, 3, 5, 12_500)
	require.NoError(t, err)
	require.Equal(t, int64(73), quote, "ceil((3*6e-6 + 5*8e-6)*1.25*1e6)")

	larger, err := QuoteOpenAINativeCompactionProbeMicroUSD(price, 3, 6, 12_500)
	require.NoError(t, err)
	require.Greater(t, larger, quote)
}

func TestQuoteOpenAINativeCompactionProbeMicroUSDFailsClosed(t *testing.T) {
	valid := OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: 1e-6, OutputUSDPerToken: 2e-6}
	tests := []struct {
		name            string
		price           OpenAIProviderTokenPrice
		inputTokens     int64
		maxOutputTokens int
		safetyBPS       int64
	}{
		{name: "missing model", price: OpenAIProviderTokenPrice{InputUSDPerToken: 1e-6, OutputUSDPerToken: 2e-6}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "zero input tokens", price: valid, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "zero output tokens", price: valid, inputTokens: 1, safetyBPS: 10_000},
		{name: "unsafe safety factor", price: valid, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 9_999},
		{name: "zero input price", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", OutputUSDPerToken: 2e-6}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "zero output price", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: 1e-6}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "negative", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: -1, OutputUSDPerToken: 1}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "nan", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: math.NaN(), OutputUSDPerToken: 1}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "infinity", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: 1, OutputUSDPerToken: math.Inf(1)}, inputTokens: 1, maxOutputTokens: 1, safetyBPS: 10_000},
		{name: "overflow", price: OpenAIProviderTokenPrice{MatchedModel: "gpt-test", InputUSDPerToken: math.MaxFloat64, OutputUSDPerToken: math.MaxFloat64}, inputTokens: math.MaxInt64, maxOutputTokens: math.MaxInt, safetyBPS: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := QuoteOpenAINativeCompactionProbeMicroUSD(test.price, test.inputTokens, test.maxOutputTokens, test.safetyBPS)
			require.ErrorIs(t, err, ErrOpenAINativeCompactionProbePriceUnavailable)
		})
	}
}

func TestValidateOpenAINativeCompactionProbeEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		valid   bool
	}{
		{name: "official api key", account: probePriceDomainAccount(AccountTypeAPIKey, "https://api.openai.com/v1"), valid: true},
		{name: "official api key explicit 443", account: probePriceDomainAccount(AccountTypeAPIKey, "https://api.openai.com:443/v1"), valid: true},
		{name: "official oauth", account: probePriceDomainAccount(AccountTypeOAuth, "https://chatgpt.com/backend-api/codex"), valid: true},
		{name: "custom host", account: probePriceDomainAccount(AccountTypeAPIKey, "https://api.example/v1"), valid: true},
		{name: "custom path", account: probePriceDomainAccount(AccountTypeAPIKey, "https://api.example/tenant/v1"), valid: true},
		{name: "custom port", account: probePriceDomainAccount(AccountTypeAPIKey, "https://api.example:8443/v1"), valid: true},
		{name: "insecure", account: probePriceDomainAccount(AccountTypeAPIKey, "http://api.openai.com/v1")},
		{name: "malformed", account: probePriceDomainAccount(AccountTypeAPIKey, "://api.example/v1")},
		{name: "unknown account type", account: probePriceDomainAccount("unknown", "https://api.openai.com/v1")},
		{name: "wrong platform", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com/v1"}}},
		{name: "nil account"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateOpenAINativeCompactionProbeEndpoint(test.account)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrOpenAINativeCompactionProbeEndpoint)
			}
		})
	}
}

func probePriceDomainAccount(accountType string, baseURL string) *Account {
	return &Account{
		Platform: PlatformOpenAI,
		Type:     accountType,
		Credentials: map[string]any{
			"base_url": baseURL,
			"api_key":  "synthetic-api-key",
		},
	}
}

func TestPricingServiceLookupOpenAIProviderTokenPriceNormalizesError(t *testing.T) {
	service := &PricingService{}
	_, err := service.LookupOpenAIProviderTokenPrice("unknown")
	require.True(t, errors.Is(err, ErrOpenAINativeCompactionProbePriceUnavailable))
}
