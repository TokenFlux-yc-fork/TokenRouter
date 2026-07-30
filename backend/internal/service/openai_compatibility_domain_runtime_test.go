package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeCompatibilityTestAccount(id int64, baseURL, mappedModel string) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Status:   StatusActive,
		Credentials: map[string]any{
			"api_key":       "fixture",
			"base_url":      baseURL,
			"model_mapping": map[string]any{"client-model": mappedModel},
		},
		Extra: map[string]any{
			"openai_responses_api_enabled": true,
		},
	}
}

func TestResolveOpenAINativeCompatibilityDomainForRequestUsesEffectiveModel(t *testing.T) {
	account := nativeCompatibilityTestAccount(1, "https://secret@api.example.test/tenant/v1", "effective-model")
	domain, err := ResolveOpenAINativeCompatibilityDomainForRequest(context.Background(), account, "client-model")
	require.NoError(t, err)
	require.Equal(t, OpenAIUpstreamProvider(PlatformOpenAI), domain.Provider)
	require.Equal(t, "effective-model", domain.EffectiveModel)
	require.Equal(t, OpenAINativeCompactionContractVersion, domain.ContractVersion)
	require.NotContains(t, string(domain.UpstreamFingerprint), "example.test")
	require.NotContains(t, string(domain.UpstreamFingerprint), "secret")
}

func TestOpenAINativeCompatibilityDomainAttemptAllowsSameAndRejectsDifferentDomain(t *testing.T) {
	svc := &OpenAIGatewayService{}
	attempt, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "new-session")
	require.NoError(t, err)

	first := nativeCompatibilityTestAccount(1, "https://api.example.test/tenant/v1", "effective-model")
	same := nativeCompatibilityTestAccount(2, "https://api.example.test/tenant/v1", "effective-model")
	otherHost := nativeCompatibilityTestAccount(3, "https://other.example.test/tenant/v1", "effective-model")
	otherModel := nativeCompatibilityTestAccount(4, "https://api.example.test/tenant/v1", "other-model")

	_, err = attempt.CheckCandidate(context.Background(), first, "client-model")
	require.NoError(t, err)
	_, err = attempt.CheckCandidate(context.Background(), same, "client-model")
	require.NoError(t, err, "failover within the same compatibility domain must be allowed")
	_, err = attempt.CheckCandidate(context.Background(), otherHost, "client-model")
	require.ErrorIs(t, err, ErrOpenAICompatibilityDomainMismatch)
	_, err = attempt.CheckCandidate(context.Background(), otherModel, "client-model")
	require.ErrorIs(t, err, ErrOpenAICompatibilityDomainMismatch)
}

func TestOpenAINativeCompatibilityDomainAttemptUnknownContinuationFailsClosed(t *testing.T) {
	svc := &OpenAIGatewayService{}
	_, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "resp_legacy_without_domain", "")
	require.ErrorIs(t, err, ErrOpenAICompatibilityDomainUnknown)

	cache := &stubGatewayCache{}
	svc = &OpenAIGatewayService{cache: cache}
	require.NoError(t, svc.BindStickySession(context.Background(), nil, "legacy-session", 99))
	_, err = svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "legacy-session")
	require.ErrorIs(t, err, ErrOpenAICompatibilityDomainUnknown)
}

func TestOpenAINativeCompatibilityDomainAttemptFailedDeliveryDoesNotBind(t *testing.T) {
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	attempt, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "session-failed")
	require.NoError(t, err)
	account := nativeCompatibilityTestAccount(11, "https://api.example.test/v1", "effective-model")
	_, err = attempt.CheckCandidate(context.Background(), account, "client-model")
	require.NoError(t, err)

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(context.Background(), 0, "resp_failed", account.ID, svc.openAIWSResponseStickyTTL()))
	require.NoError(t, attempt.CommitDelivery(context.Background(), account.ID, "resp_failed", false))

	_, ok := store.GetResponseDomain(context.Background(), 0, "resp_failed")
	require.False(t, ok)
	responseAccount, err := store.GetResponseAccount(context.Background(), 0, "resp_failed")
	require.Zero(t, responseAccount, "failed attempt must remove eager ResponseID binding")
	if err != nil {
		require.Contains(t, err.Error(), "not found")
	}
	_, ok = store.GetSessionDomain(context.Background(), 0, "session-failed")
	require.False(t, ok)
	stickyAccount, err := svc.getStickySessionAccountID(context.Background(), nil, "session-failed")
	if err != nil {
		require.Contains(t, err.Error(), "not found")
	}
	require.Zero(t, stickyAccount)
}

func TestOpenAINativeCompatibilityDomainAttemptFailedDeliveryDoesNotPublishCandidateAccount(t *testing.T) {
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	attempt, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "session-account-check")
	require.NoError(t, err)
	first := nativeCompatibilityTestAccount(21, "https://api.example.test/v1", "effective-model")
	second := nativeCompatibilityTestAccount(22, "https://api.example.test/v1", "effective-model")
	_, err = attempt.CheckCandidate(context.Background(), first, "client-model")
	require.NoError(t, err)
	_, err = attempt.CheckCandidate(context.Background(), second, "client-model")
	require.NoError(t, err)

	require.NoError(t, attempt.CommitDelivery(context.Background(), first.ID, "resp_wrong_account", true))
	store := svc.getOpenAIWSStateStore()
	_, ok := store.GetResponseDomain(context.Background(), 0, "resp_wrong_account")
	require.False(t, ok, "delivery from a stale candidate must not publish continuation state")
	stickyAccount, err := svc.getStickySessionAccountID(context.Background(), nil, "session-account-check")
	if err != nil {
		require.Contains(t, err.Error(), "not found")
	}
	require.Zero(t, stickyAccount)
}

func TestOpenAINativeCompatibilityDomainAttemptSuccessfulDeliveryBindsContinuation(t *testing.T) {
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	attempt, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "session-ok")
	require.NoError(t, err)
	account := nativeCompatibilityTestAccount(12, "https://api.example.test/v1", "effective-model")
	domain, err := attempt.CheckCandidate(context.Background(), account, "client-model")
	require.NoError(t, err)
	require.NoError(t, attempt.CommitDelivery(context.Background(), account.ID, "resp_ok", true))

	store := svc.getOpenAIWSStateStore()
	responseDomain, ok := store.GetResponseDomain(context.Background(), 0, "resp_ok")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(responseDomain))
	sessionDomain, ok := store.GetSessionDomain(context.Background(), 0, "session-ok")
	require.True(t, ok)
	require.True(t, domain.CompatibleWith(sessionDomain))

	continued, err := svc.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "resp_ok", "session-ok")
	require.NoError(t, err)
	_, err = continued.CheckCandidate(context.Background(), nativeCompatibilityTestAccount(13, "https://api.example.test/v1", "effective-model"), "client-model")
	require.NoError(t, err)
	_, err = continued.CheckCandidate(context.Background(), nativeCompatibilityTestAccount(14, "https://other.example.test/v1", "effective-model"), "client-model")
	require.True(t, errors.Is(err, ErrOpenAICompatibilityDomainMismatch))
}

func TestOpenAINativeCompatibilityDomainAttemptSurvivesInstanceHandoff(t *testing.T) {
	firstCache, secondCache := newDistributedOpenAICompatibilityDomainCachePair()
	firstService := &OpenAIGatewayService{cache: firstCache}
	secondService := &OpenAIGatewayService{cache: secondCache}
	account := nativeCompatibilityTestAccount(31, "https://api.example.test/v1", "effective-model")

	firstAttempt, err := firstService.NewOpenAINativeCompatibilityDomainAttempt(context.Background(), nil, "", "session-handoff")
	require.NoError(t, err)
	_, err = firstAttempt.CheckCandidate(context.Background(), account, "client-model")
	require.NoError(t, err)
	require.NoError(t, firstAttempt.CommitDelivery(context.Background(), account.ID, "resp_handoff", true))

	continued, err := secondService.NewOpenAINativeCompatibilityDomainAttempt(
		context.Background(), nil, "resp_handoff", "session-handoff",
	)
	require.NoError(t, err)
	_, err = continued.CheckCandidate(
		context.Background(),
		nativeCompatibilityTestAccount(32, "https://api.example.test/v1", "effective-model"),
		"client-model",
	)
	require.NoError(t, err)
	_, err = continued.CheckCandidate(
		context.Background(),
		nativeCompatibilityTestAccount(33, "https://other.example.test/v1", "effective-model"),
		"client-model",
	)
	require.ErrorIs(t, err, ErrOpenAICompatibilityDomainMismatch)
}
