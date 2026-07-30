package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type semanticQuarantineRepoCall struct {
	key     OpenAINativeCompactionCapabilityKey
	until   *time.Time
	outcome string
}

type semanticQuarantineRepo struct {
	calls []semanticQuarantineRepoCall
	err   error
}

func (r *semanticQuarantineRepo) GetExact(context.Context, OpenAINativeCompactionCapabilityKey) (*OpenAINativeCompactionCapabilityRecord, error) {
	return nil, nil
}
func (r *semanticQuarantineRepo) UpsertTrustedOfficial(context.Context, *Account, OpenAINativeCompactionCapabilityKey) (bool, error) {
	return false, nil
}
func (r *semanticQuarantineRepo) EnsureAutoCandidate(context.Context, OpenAINativeCompactionCapabilityKey, time.Time) (bool, error) {
	return false, nil
}
func (r *semanticQuarantineRepo) UpsertProbeResult(context.Context, OpenAINativeCompactionProbeResult) (OpenAINativeCompactionProbeWriteResult, error) {
	return OpenAINativeCompactionProbeWriteResult{}, nil
}
func (r *semanticQuarantineRepo) SetQuarantine(_ context.Context, key OpenAINativeCompactionCapabilityKey, until *time.Time, outcome string) (bool, error) {
	r.calls = append(r.calls, semanticQuarantineRepoCall{key: key, until: until, outcome: outcome})
	return r.err == nil, r.err
}
func (r *semanticQuarantineRepo) ClaimDue(context.Context, time.Time, time.Time, string, int) ([]OpenAINativeCompactionProbeClaim, error) {
	return nil, nil
}
func (r *semanticQuarantineRepo) FenceProbeDispatch(context.Context, OpenAINativeCompactionProbeClaim) error {
	return nil
}
func (r *semanticQuarantineRepo) UpsertManualOverride(context.Context, OpenAINativeCompactionManualOverride) error {
	return nil
}
func (r *semanticQuarantineRepo) RevokeManualOverride(context.Context, OpenAINativeCompactionCapabilityKey, time.Time, time.Time) (bool, error) {
	return false, nil
}
func (r *semanticQuarantineRepo) ListAudit(context.Context, OpenAINativeCompactionCapabilityKey, int) ([]OpenAINativeCompactionProbeAudit, error) {
	return nil, nil
}

func TestOpenAINativeCompactionSemanticQuarantineUsesExactKey(t *testing.T) {
	repo := &semanticQuarantineRepo{}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{OpenAINativeCompaction: config.GatewayOpenAINativeCompactionConfig{
			SemanticQuarantineSeconds: 60,
		}}},
		openAINativeCompactionCapabilityRepo: repo,
	}
	account := &Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "fixture", "base_url": "https://user:secret@api.example.test/tenant-a/v1?token=secret#fragment",
	}}
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)

	svc.quarantineOpenAINativeCompactionFailure(ctx, nil, account, "mapped-model-a", newOpenAINativeCompactionSemanticError(OpenAINativeCompactionZeroCompaction))
	require.Len(t, repo.calls, 1)
	call := repo.calls[0]
	require.Equal(t, account.ID, call.key.AccountID)
	require.Equal(t, "mapped-model-a", call.key.EffectiveModel)
	require.Equal(t, OpenAINativeCompactionContractVersion, call.key.ContractVersion)
	require.Equal(t, string(OpenAINativeCompactionZeroCompaction), call.outcome)
	require.NotNil(t, call.until)
	require.Contains(t, string(call.key.UpstreamFingerprint), "upstream_v1_")
	require.NotContains(t, string(call.key.UpstreamFingerprint), "secret")

	otherModel := call.key
	otherModel.EffectiveModel = "mapped-model-b"
	otherFingerprint := call.key
	otherFingerprint.UpstreamFingerprint = OpenAIUpstreamFingerprint("upstream_v1_other")
	require.NotEqual(t, call.key, otherModel)
	require.NotEqual(t, call.key, otherFingerprint)
}

func TestOpenAINativeCompactionSemanticQuarantineClassification(t *testing.T) {
	repo := &semanticQuarantineRepo{}
	svc := &OpenAIGatewayService{
		cfg:                                  &config.Config{Gateway: config.GatewayConfig{OpenAINativeCompaction: config.GatewayOpenAINativeCompactionConfig{SemanticQuarantineSeconds: 60}}},
		openAINativeCompactionCapabilityRepo: repo,
	}
	account := &Account{ID: 88, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)

	for _, err := range []error{
		errors.New("transport failed"),
		context.DeadlineExceeded,
		newOpenAINativeCompactionSemanticError(OpenAINativeCompactionFailedTerminal),
		newOpenAINativeCompactionSemanticError(OpenAINativeCompactionIncompleteStream),
		newOpenAINativeCompactionSemanticError(OpenAINativeCompactionHTTPFailure),
		newOpenAINativeCompactionSemanticError(OpenAINativeCompactionTransportFailure),
	} {
		svc.quarantineOpenAINativeCompactionFailure(ctx, nil, account, "mapped-model", err)
	}
	require.Empty(t, repo.calls)

	quarantinedOutcomes := []OpenAINativeCompactionOutcome{
		OpenAINativeCompactionZeroCompaction,
		OpenAINativeCompactionMultipleCompaction,
		OpenAINativeCompactionMalformedCompaction,
		OpenAINativeCompactionInvalidEvent,
		OpenAINativeCompactionDuplicateTerminal,
		OpenAINativeCompactionPostTerminalFrame,
		OpenAINativeCompactionResourceLimit,
	}
	for _, outcome := range quarantinedOutcomes {
		svc.quarantineOpenAINativeCompactionFailure(ctx, nil, account, "mapped-model", errors.Join(
			&UpstreamFailoverError{StatusCode: 502, ResponseBody: []byte(`{"encrypted_content":"must-not-persist"}`)},
			newOpenAINativeCompactionSemanticError(outcome),
		))
	}
	require.Len(t, repo.calls, len(quarantinedOutcomes))
	for i, outcome := range quarantinedOutcomes {
		require.Equal(t, string(outcome), repo.calls[i].outcome)
		require.NotContains(t, repo.calls[i].outcome, "encrypted_content")
		require.NotContains(t, repo.calls[i].key.EffectiveModel, "encrypted_content")
	}
}

func TestOpenAINativeCompactionStageSemanticClassification(t *testing.T) {
	for _, err := range []error{
		ErrOpenAINativeCompactionStageBytes,
		ErrOpenAINativeCompactionStageEvents,
		ErrOpenAINativeCompactionStageBudget,
	} {
		outcome, ok := openAINativeCompactionQuarantineOutcome(openAINativeCompactionStageSemanticError(err))
		require.True(t, ok)
		require.Equal(t, OpenAINativeCompactionResourceLimit, outcome)
	}
	require.Nil(t, openAINativeCompactionStageSemanticError(ErrOpenAINativeCompactionStageDuration))
	require.Nil(t, openAINativeCompactionStageSemanticError(context.DeadlineExceeded))
}

func TestOpenAINativeCompactionContinuationErrorClassification(t *testing.T) {
	for _, code := range []string{"invalid_encrypted_content", "previous_response_not_found", "prewarm_previous_response_not_found"} {
		t.Run(code, func(t *testing.T) {
			normalized := openAINativeCompactionContinuationErrorCode(code)
			require.NotEmpty(t, normalized)
			err := newOpenAINativeCompactionContinuationError(code)
			require.True(t, openAINativeCompactionFailureSafeToReplay(err))
		})
	}

	require.Empty(t, openAINativeCompactionContinuationErrorCode("invalid_request_error"))
	require.Equal(t, "invalid_encrypted_content", openAINativeCompactionWSContinuationErrorCode(
		wrapOpenAIWSFallback("prewarm_invalid_encrypted_content", errors.New("fixture")),
	))
}

func TestOpenAINativeCompactionQuarantineAffectsOnlyNextExactKeySchedule(t *testing.T) {
	now := time.Now()
	until := now.Add(time.Minute)
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)
	account := &Account{
		ID:          9,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":       "fixture",
			"base_url":      "https://api.example.test/tenant-a/v1",
			"model_mapping": map[string]any{"client-a": "mapped-a", "client-b": "mapped-b"},
		},
		Extra: map[string]any{"openai_responses_supported": true},
	}
	exactKey, err := ResolveOpenAINativeCompactionCapabilityKey(account, "mapped-a")
	require.NoError(t, err)
	otherModelKey, err := ResolveOpenAINativeCompactionCapabilityKey(account, "mapped-b")
	require.NoError(t, err)
	otherUpstream := *account
	otherUpstream.Credentials = map[string]any{"api_key": "fixture", "base_url": "https://api.example.test/tenant-b/v1"}
	otherFingerprintKey, err := ResolveOpenAINativeCompactionCapabilityKey(&otherUpstream, "mapped-a")
	require.NoError(t, err)

	account.OpenAINativeCompactionCapabilities = []OpenAINativeCompactionCapability{
		{Key: exactKey, Supported: true, Mode: OpenAINativeCompactionCapabilityModeAuto, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now, QuarantinedUntil: &until},
		{Key: otherModelKey, Supported: true, Mode: OpenAINativeCompactionCapabilityModeAuto, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now},
		{Key: otherFingerprintKey, Supported: true, Mode: OpenAINativeCompactionCapabilityModeAuto, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now},
	}

	require.False(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "client-a", false, OpenAIEndpointCapabilityResponses))
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "client-b", false, OpenAIEndpointCapabilityResponses))
	otherUpstream.OpenAINativeCompactionCapabilities = account.OpenAINativeCompactionCapabilities
	require.True(t, isOpenAICompatibleAccountEligibleForRequest(ctx, &otherUpstream, PlatformOpenAI, "mapped-a", false, OpenAIEndpointCapabilityResponses))
}

func TestOpenAINativeCompactionQuarantineExpiryRestoresAllows(t *testing.T) {
	now := time.Unix(1000, 0)
	key := OpenAINativeCompactionCapabilityKey{AccountID: 9, UpstreamFingerprint: "upstream_v1_fixture", EffectiveModel: "mapped-model", ContractVersion: OpenAINativeCompactionContractVersion}
	until := now.Add(time.Minute)
	capability := OpenAINativeCompactionCapability{Key: key, Supported: true, Mode: OpenAINativeCompactionCapabilityModeAuto, Source: OpenAINativeCompactionCapabilitySourceProbe, CheckedAt: &now, QuarantinedUntil: &until}
	require.False(t, capability.Allows(now))
	require.True(t, capability.Allows(until))
}

func TestOpenAINativeCompactionQuarantinePersistenceFailurePreservesFailover(t *testing.T) {
	repo := &semanticQuarantineRepo{err: errors.New("database unavailable")}
	svc := &OpenAIGatewayService{
		cfg:                                  &config.Config{Gateway: config.GatewayConfig{OpenAINativeCompaction: config.GatewayOpenAINativeCompactionConfig{SemanticQuarantineSeconds: 60}}},
		openAINativeCompactionCapabilityRepo: repo,
	}
	account := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	ctx := WithOpenAINativeRemoteCompactionV2(t.Context(), true)
	c, _ := gin.CreateTestContext(nil)
	original := errors.Join(&UpstreamFailoverError{StatusCode: 502}, newOpenAINativeCompactionSemanticError(OpenAINativeCompactionDuplicateTerminal))

	require.NotPanics(t, func() { svc.quarantineOpenAINativeCompactionFailure(ctx, c, account, "mapped-model", original) })
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, original, &failoverErr)
	require.Len(t, repo.calls, 1)
	require.Equal(t, "duplicate_terminal", repo.calls[0].outcome)
}
