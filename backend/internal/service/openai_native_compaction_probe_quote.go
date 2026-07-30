package service

import (
	"errors"
	"math"
	"net/url"
	"strings"
)

const openAINativeCompactionProbeInputTokenCeiling int64 = 128

var (
	ErrOpenAINativeCompactionProbePriceUnavailable = errors.New("openai native compaction probe provider price is unavailable")
	ErrOpenAINativeCompactionProbeEndpoint         = errors.New("openai native compaction probe upstream is not an eligible HTTPS Responses endpoint")
	ErrOpenAINativeCompactionProbeCostLimit        = errors.New("openai native compaction probe per-run cost limit exceeded")
)

type OpenAIProviderPriceLookup interface {
	LookupOpenAIProviderTokenPrice(effectiveModel string) (OpenAIProviderTokenPrice, error)
}

type OpenAIProviderTokenPrice struct {
	MatchedModel                     string
	InputUSDPerToken                 float64
	InputPriorityUSDPerToken         float64
	OutputUSDPerToken                float64
	OutputPriorityUSDPerToken        float64
	CacheCreationUSDPerToken         float64
	CacheCreationPriorityUSDPerToken float64
	CacheReadUSDPerToken             float64
	CacheReadPriorityUSDPerToken     float64
}

func (s *PricingService) LookupOpenAIProviderTokenPrice(effectiveModel string) (OpenAIProviderTokenPrice, error) {
	pricing, matchedModel, err := s.GetExactOpenAIProviderPricing(effectiveModel)
	if err != nil {
		return OpenAIProviderTokenPrice{}, ErrOpenAINativeCompactionProbePriceUnavailable
	}
	return OpenAIProviderTokenPrice{
		MatchedModel:                     matchedModel,
		InputUSDPerToken:                 pricing.InputCostPerToken,
		InputPriorityUSDPerToken:         pricing.InputCostPerTokenPriority,
		OutputUSDPerToken:                pricing.OutputCostPerToken,
		OutputPriorityUSDPerToken:        pricing.OutputCostPerTokenPriority,
		CacheCreationUSDPerToken:         pricing.CacheCreationInputTokenCost,
		CacheCreationPriorityUSDPerToken: pricing.CacheCreationInputTokenCostPriority,
		CacheReadUSDPerToken:             pricing.CacheReadInputTokenCost,
		CacheReadPriorityUSDPerToken:     pricing.CacheReadInputTokenCostPriority,
	}, nil
}

func QuoteOpenAINativeCompactionProbeMicroUSD(
	price OpenAIProviderTokenPrice,
	inputTokens int64,
	maxOutputTokens int,
	safetyBPS int64,
) (int64, error) {
	if strings.TrimSpace(price.MatchedModel) == "" || inputTokens <= 0 || maxOutputTokens <= 0 || safetyBPS < 10_000 {
		return 0, ErrOpenAINativeCompactionProbePriceUnavailable
	}
	inputRate, err := maxFiniteNonNegativeRate(
		price.InputUSDPerToken,
		price.InputPriorityUSDPerToken,
		price.CacheCreationUSDPerToken,
		price.CacheCreationPriorityUSDPerToken,
		price.CacheReadUSDPerToken,
		price.CacheReadPriorityUSDPerToken,
	)
	if err != nil || inputRate <= 0 {
		return 0, ErrOpenAINativeCompactionProbePriceUnavailable
	}
	outputRate, err := maxFiniteNonNegativeRate(price.OutputUSDPerToken, price.OutputPriorityUSDPerToken)
	if err != nil || outputRate <= 0 {
		return 0, ErrOpenAINativeCompactionProbePriceUnavailable
	}
	usd := (float64(inputTokens)*inputRate + float64(maxOutputTokens)*outputRate) * float64(safetyBPS) / 10_000
	microUSD := math.Ceil(usd * 1_000_000)
	if math.IsNaN(microUSD) || math.IsInf(microUSD, 0) || microUSD <= 0 || microUSD > math.MaxInt64 {
		return 0, ErrOpenAINativeCompactionProbePriceUnavailable
	}
	return int64(microUSD), nil
}

func maxFiniteNonNegativeRate(rates ...float64) (float64, error) {
	maximum := float64(0)
	for _, rate := range rates {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return 0, ErrOpenAINativeCompactionProbePriceUnavailable
		}
		if rate > maximum {
			maximum = rate
		}
	}
	return maximum, nil
}

// ValidateOpenAINativeCompactionProbeEndpoint keeps OAuth probes on the fixed
// Codex endpoint while permitting explicitly configured API-key/custom HTTPS
// Responses endpoints. Candidate authorization remains bounded independently
// by the isolated group, sentinel key, model allowlist, URL policy, and budgets.
func ValidateOpenAINativeCompactionProbeEndpoint(account *Account) error {
	endpoint, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return ErrOpenAINativeCompactionProbeEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return ErrOpenAINativeCompactionProbeEndpoint
	}
	if account == nil || account.Platform != PlatformOpenAI {
		return ErrOpenAINativeCompactionProbeEndpoint
	}
	switch account.Type {
	case AccountTypeOAuth:
		if !strings.EqualFold(parsed.Hostname(), "chatgpt.com") ||
			(parsed.Port() != "" && parsed.Port() != "443") ||
			parsed.EscapedPath() != "/backend-api/codex/responses" {
			return ErrOpenAINativeCompactionProbeEndpoint
		}
	case AccountTypeAPIKey:
		if !strings.HasSuffix(parsed.EscapedPath(), "/responses") {
			return ErrOpenAINativeCompactionProbeEndpoint
		}
	default:
		return ErrOpenAINativeCompactionProbeEndpoint
	}
	return nil
}
