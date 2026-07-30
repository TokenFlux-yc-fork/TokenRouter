package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubUpstreamAttemptAttributionRepository struct {
	mu   sync.Mutex
	err  error
	rows []UpstreamAttemptAttribution
}

func (r *stubUpstreamAttemptAttributionRepository) Upsert(_ context.Context, attribution UpstreamAttemptAttribution) (UpstreamAttemptAttribution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return UpstreamAttemptAttribution{}, r.err
	}
	r.rows = append(r.rows, attribution)
	return attribution, nil
}

func (r *stubUpstreamAttemptAttributionRepository) snapshot() []UpstreamAttemptAttribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]UpstreamAttemptAttribution(nil), r.rows...)
}

func (r *stubUpstreamAttemptAttributionRepository) Get(context.Context, AttemptID) (*UpstreamAttemptAttribution, error) {
	return nil, nil
}

func TestFinalizeOpenAINativeHTTPForwardResultFailsClosedWithoutLedger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)

	result := &OpenAIForwardResult{}
	finalizeOpenAINativeHTTPForwardResult(c, result, &Account{Type: AccountTypeOAuth}, nil)

	require.True(t, result.NativeRemoteCompactionV2)
	require.Equal(t, OpenAINativeCompactionIncompleteStream, result.SemanticOutcome)
	require.Equal(t, "validator", result.SemanticSource)
	require.Equal(t, UpstreamAttemptTransportHTTP, result.Transport)
	require.Equal(t, string(AccountTypeOAuth), result.AccountType)
	require.False(t, result.CustomerSettlementAllowed())
	require.False(t, result.SucceededForScheduling())
}

func TestFinalizeOpenAINativeHTTPForwardResultProjectsTerminalAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)

	attempt := &openAIUpstreamAttemptCoordinator{terminalPersisted: true, attribution: UpstreamAttemptAttribution{
		AttemptID:         "attempt-1",
		ClientRequestID:   "client-1",
		GatewayRequestID:  "gateway-1",
		UpstreamRequestID: "upstream-1",
		Domain: OpenAICompatibilityDomain{
			UpstreamFingerprint: "upstream_v1_0123456789abcdef0123456789abcdef",
		},
		Semantic: UpstreamAttemptSemantic{
			Outcome:             string(OpenAINativeCompactionValid),
			OutputItemDoneCount: 2,
			CompactionItemCount: 1,
			TerminalEvent:       "response.completed",
			TerminalCount:       1,
		},
		Usage:             UpstreamAttemptUsage{Observed: true},
		DeliveryCommitted: true,
	}}
	result := &OpenAIForwardResult{}
	finalizeOpenAINativeHTTPForwardResult(c, result, &Account{Type: AccountTypeAPIKey}, attempt)

	require.Equal(t, AttemptID("attempt-1"), result.AttemptID)
	require.Equal(t, ClientRequestID("client-1"), result.ClientRequestID)
	require.Equal(t, GatewayRequestID("gateway-1"), result.GatewayRequestID)
	require.Equal(t, UpstreamRequestID("upstream-1"), result.UpstreamRequestID)
	require.Equal(t, OpenAINativeCompactionValid, result.SemanticOutcome)
	require.Equal(t, 2, result.OutputItemDoneCount)
	require.Equal(t, 1, result.CompactionItemCount)
	require.Equal(t, "response.completed", result.UpstreamTerminalEvent)
	require.Equal(t, 1, result.TerminalEventCount)
	require.True(t, result.DeliveryCommitted)
	require.True(t, result.AttributionPersisted)
	require.True(t, result.UpstreamUsageObserved)
	require.True(t, result.CustomerSettlementAllowed())
	require.True(t, result.SucceededForScheduling())
}

func TestFinalizeOpenAINativeHTTPForwardResultRequiresDurableTerminalAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name       string
		persistErr error
		want       bool
	}{
		{name: "terminal persisted", want: true},
		{name: "terminal persistence failed", persistErr: errors.New("fixture persistence failure"), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &stubUpstreamAttemptAttributionRepository{err: tt.persistErr}
			now := time.Now().UTC()
			attempt := &openAIUpstreamAttemptCoordinator{
				service: &OpenAIGatewayService{upstreamAttemptAttributionRepo: repo},
				ctx:     context.Background(),
				attribution: UpstreamAttemptAttribution{
					AttemptID:        "attempt-durable",
					ClientRequestID:  "client-durable",
					GatewayRequestID: "gateway-durable",
					AccountID:        42,
					Transport:        UpstreamAttemptTransportHTTP,
					Domain: OpenAICompatibilityDomain{
						Provider:            OpenAIUpstreamProvider(PlatformOpenAI),
						UpstreamFingerprint: "upstream_v1_0123456789abcdef0123456789abcdef",
						EffectiveModel:      "fixture-model",
						ContractVersion:     OpenAINativeCompactionContractVersion,
					},
					State:        UpstreamAttemptStateStarted,
					StateVersion: 1,
					StartedAt:    now,
					ObservedAt:   now,
				},
			}
			attempt.finish(openAIUpstreamAttemptTerminal{
				outcome: string(OpenAINativeCompactionValid),
				validation: OpenAINativeCompactionValidationResult{
					Outcome:             OpenAINativeCompactionValid,
					OutputItemDoneCount: 1,
					CompactionItemCount: 1,
					TerminalEvent:       "response.completed",
					TerminalCount:       1,
					SuccessfulTerminal:  true,
				},
				deliveryObserved:  true,
				deliveryCommitted: true,
			})

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			result := &OpenAIForwardResult{}
			finalizeOpenAINativeHTTPForwardResult(c, result, &Account{Type: AccountTypeAPIKey}, attempt)

			require.Equal(t, tt.want, result.AttributionPersisted)
			require.Equal(t, tt.want, result.CustomerSettlementAllowed())
			if tt.want {
				require.Len(t, repo.rows, 1)
				require.Equal(t, UpstreamAttemptStateTerminal, repo.rows[0].State)
			} else {
				require.Empty(t, repo.rows)
			}
		})
	}
}
