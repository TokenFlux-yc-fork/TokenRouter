package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type nativeCompactionProbeUpstreamRecorder struct {
	mu          sync.Mutex
	request     *http.Request
	body        []byte
	proxyURL    string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
	response    *http.Response
	err         error
	called      chan struct{}
}

func (u *nativeCompactionProbeUpstreamRecorder) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, concurrency, nil)
}

func (u *nativeCompactionProbeUpstreamRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	u.request = req
	u.body = body
	u.proxyURL = proxyURL
	u.accountID = accountID
	u.concurrency = concurrency
	u.profile = profile
	called := u.called
	response := u.response
	err := u.err
	u.mu.Unlock()
	if called != nil {
		select {
		case <-called:
		default:
			close(called)
		}
	}
	return response, err
}

type nativeCompactionProbePriceLookup struct {
	price OpenAIProviderTokenPrice
	err   error
}

func (l *nativeCompactionProbePriceLookup) LookupOpenAIProviderTokenPrice(string) (OpenAIProviderTokenPrice, error) {
	if l.err != nil {
		return OpenAIProviderTokenPrice{}, l.err
	}
	if l.price.MatchedModel == "" {
		return OpenAIProviderTokenPrice{
			MatchedModel:      "gpt-5.6-sol",
			InputUSDPerToken:  2e-6,
			OutputUSDPerToken: 10e-6,
		}, nil
	}
	return l.price, nil
}

type nativeCompactionProbeBudgetRecorder struct {
	mu                 sync.Mutex
	reserveErr         error
	markDispatchedErr  error
	commitErr          error
	releaseErr         error
	reservation        OpenAINativeCompactionProbeBudgetReservation
	reserved           int
	dispatched         int
	committed          int
	released           int
	amountMicroUSD     int64
	dailyLimitMicroUSD int64
	dispatchFence      OpenAINativeCompactionProbeDispatchFence
}

func (r *nativeCompactionProbeBudgetRecorder) Reserve(_ context.Context, reservationID string, amountMicroUSD, dailyLimitMicroUSD int64) (OpenAINativeCompactionProbeBudgetReservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserved++
	r.amountMicroUSD = amountMicroUSD
	r.dailyLimitMicroUSD = dailyLimitMicroUSD
	if r.reserveErr != nil {
		return OpenAINativeCompactionProbeBudgetReservation{}, r.reserveErr
	}
	reservation := r.reservation
	if !reservation.Valid() {
		reservation = OpenAINativeCompactionProbeBudgetReservation{
			ID:             reservationID,
			BudgetDay:      time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC),
			AmountMicroUSD: amountMicroUSD,
			State:          OpenAINativeCompactionProbeBudgetReserved,
		}
	}
	return reservation, nil
}

func (r *nativeCompactionProbeBudgetRecorder) MarkDispatched(
	_ context.Context,
	_ OpenAINativeCompactionProbeBudgetReservation,
	fence OpenAINativeCompactionProbeDispatchFence,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatched++
	r.dispatchFence = fence
	if r.markDispatchedErr != nil {
		return "", r.markDispatchedErr
	}
	return strings.Repeat("a", 64), nil
}

func (r *nativeCompactionProbeBudgetRecorder) CommitFull(_ context.Context, _ OpenAINativeCompactionProbeBudgetReservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.committed++
	return r.commitErr
}

func (r *nativeCompactionProbeBudgetRecorder) Release(_ context.Context, _ OpenAINativeCompactionProbeBudgetReservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released++
	return r.releaseErr
}

func (r *nativeCompactionProbeBudgetRecorder) ReapExpired(context.Context, int) (OpenAINativeCompactionProbeBudgetReapResult, error) {
	return OpenAINativeCompactionProbeBudgetReapResult{}, nil
}

func newNativeCompactionProbeService(upstream HTTPUpstream) (*OpenAIGatewayService, *nativeCompactionProbeBudgetRecorder) {
	budget := &nativeCompactionProbeBudgetRecorder{}
	return &OpenAIGatewayService{
		httpUpstream:           upstream,
		openAIProbePriceLookup: &nativeCompactionProbePriceLookup{},
		openAIProbeBudgetRepo:  budget,
	}, budget
}

func nativeProbeService(upstream HTTPUpstream) *OpenAIGatewayService {
	service, _ := newNativeCompactionProbeService(upstream)
	return service
}

func TestOpenAINativeCompactionProbeBuildsBareResponsesContract(t *testing.T) {
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, "valid_one"))}
	service, _ := newNativeCompactionProbeService(upstream)
	account := &Account{
		ID:          73,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 4,
		ProxyID:     boolPointerInt64(17),
		Proxy: &Proxy{
			Protocol: "http",
			Host:     "proxy.example",
			Port:     8080,
		},
		Credentials: map[string]any{
			"api_key":               "synthetic-api-key",
			"base_url":              "https://custom.example/tenant/v1/responses/compact",
			"model_mapping":         map[string]any{"client-alias": "gpt-5.6"},
			"compact_model_mapping": map[string]any{"gpt-5.6": "must-not-be-used"},
		},
	}

	result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), account, "client-alias", OpenAINativeCompactionProbeOptions{})
	require.NoError(t, err)
	require.NotNil(t, result.Supported)
	require.True(t, *result.Supported)
	require.Equal(t, OpenAINativeCompactionValid, result.SemanticOutcome)
	require.NotNil(t, result.StatusCode)
	require.Equal(t, http.StatusOK, *result.StatusCode)
	require.Equal(t, "gpt-5.6", result.Key.EffectiveModel)

	require.Equal(t, http.MethodPost, upstream.request.Method)
	require.Equal(t, "https://custom.example/tenant/v1/responses", upstream.request.URL.String(), "probe must preserve a custom endpoint while stripping legacy /compact")
	require.Equal(t, "Bearer synthetic-api-key", upstream.request.Header.Get("Authorization"))
	require.Equal(t, "text/event-stream", upstream.request.Header.Get("Accept"))
	require.Equal(t, OpenAINativeCompactionContractVersion, upstream.request.Header.Get("x-codex-beta-features"))
	require.Equal(t, int64(73), upstream.accountID)
	require.Equal(t, 4, upstream.concurrency)
	require.Equal(t, "http://proxy.example:8080", upstream.proxyURL)

	var payload struct {
		Model           string `json:"model"`
		Stream          bool   `json:"stream"`
		Store           bool   `json:"store"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		Input           []struct {
			Type string `json:"type"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(upstream.body, &payload))
	require.Equal(t, "gpt-5.6", payload.Model)
	require.True(t, payload.Stream)
	require.False(t, payload.Store)
	require.Equal(t, 256, payload.MaxOutputTokens)
	require.NotEmpty(t, payload.Input)
	require.Equal(t, "compaction_trigger", payload.Input[len(payload.Input)-1].Type)
	require.NotContains(t, string(upstream.body), "encrypted_content")
	require.NotContains(t, string(upstream.body), "must-not-be-used")
}

func TestOpenAINativeCompactionProbeCompletedWithoutCompactionIsUnsupported(t *testing.T) {
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, "zero_compaction"))}
	service, _ := newNativeCompactionProbeService(upstream)
	account := nativeProbeAPIKeyAccount()

	result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), account, "gpt-5.6", OpenAINativeCompactionProbeOptions{})
	require.NoError(t, err)
	require.NotNil(t, result.Supported)
	require.False(t, *result.Supported, "2xx plus response.completed is not capability proof")
	require.Equal(t, OpenAINativeCompactionZeroCompaction, result.SemanticOutcome)
}

func TestOpenAINativeCompactionProbeHTTPFailureIsPayloadFree(t *testing.T) {
	secret := "synthetic-encrypted-content-must-not-escape"
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusBadRequest, `{"error":{"message":"`+secret+`"}}`)}
	service, _ := newNativeCompactionProbeService(upstream)

	result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
	require.NoError(t, err)
	require.Nil(t, result.Supported, "ordinary 400 is inconclusive")
	require.Equal(t, OpenAINativeCompactionHTTPFailure, result.SemanticOutcome)
	encoded, encodeErr := json.Marshal(result)
	require.NoError(t, encodeErr)
	require.NotContains(t, string(encoded), secret)
	require.NotContains(t, string(encoded), "payload")
	require.NotContains(t, string(encoded), "encrypted_content")
}

func TestOpenAINativeCompactionProbeUsesConfiguredMaxOutputTokens(t *testing.T) {
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, "valid_one"))}
	service, _ := newNativeCompactionProbeService(upstream)
	_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{MaxOutputTokens: 17})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(upstream.body, &payload))
	require.EqualValues(t, 17, payload["max_output_tokens"])
	require.Equal(t, false, payload["store"])
}

func TestOpenAINativeCompactionProbeStatusClassification(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(status, `{"secret":"discarded"}`)}
			result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
			require.NoError(t, err)
			require.NotNil(t, result.Supported)
			require.False(t, *result.Supported)
		})
	}
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusUnprocessableEntity,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(status, `{"secret":"discarded"}`)}
			result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
			require.NoError(t, err)
			require.Nil(t, result.Supported)
		})
	}
}

func TestOpenAINativeCompactionProbeSanitizesRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	response := nativeProbeResponse(http.StatusTooManyRequests, `{"secret":"discarded"}`)
	response.Header.Set("Retry-After", "90")
	upstream := &nativeCompactionProbeUpstreamRecorder{response: response}

	result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(
		context.Background(),
		nativeProbeAPIKeyAccount(),
		"gpt-5.6",
		OpenAINativeCompactionProbeOptions{Now: func() time.Time { return now }},
	)
	require.NoError(t, err)
	require.Nil(t, result.Supported)
	require.NotNil(t, result.RetryAfterUntil)
	require.Equal(t, now.Add(90*time.Second), *result.RetryAfterUntil)

	encoded, encodeErr := json.Marshal(result)
	require.NoError(t, encodeErr)
	require.NotContains(t, string(encoded), "Retry-After")
	require.NotContains(t, string(encoded), "secret")
}

func TestOpenAINativeCompactionProbeSemanticClassification(t *testing.T) {
	tests := []struct {
		fixture string
		want    *bool
	}{
		{fixture: "valid_one", want: boolPointer(true)},
		{fixture: "zero_compaction", want: boolPointer(false)},
		{fixture: "two_compactions", want: boolPointer(false)},
		{fixture: "malformed_compaction", want: boolPointer(false)},
		{fixture: "duplicate_terminal", want: boolPointer(false)},
		{fixture: "post_terminal_frame", want: boolPointer(false)},
		{fixture: "failed_terminal"},
		{fixture: "incomplete_eof"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, tt.fixture))}
			result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
			require.NoError(t, err)
			if tt.want == nil {
				require.Nil(t, result.Supported)
			} else {
				require.NotNil(t, result.Supported)
				require.Equal(t, *tt.want, *result.Supported)
			}
		})
	}
}

func TestOpenAINativeCompactionProbeRejectsMultipleDocumentsInOneSSEEvent(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_same_event","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, body)}

	result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(
		context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{},
	)

	require.NoError(t, err)
	require.NotNil(t, result.Supported)
	require.False(t, *result.Supported)
	require.Equal(t, OpenAINativeCompactionInvalidEvent, result.SemanticOutcome)
}

func TestOpenAINativeCompactionProbeRejectsFramesAfterDoneSentinel(t *testing.T) {
	body := strings.Join([]string{
		`data: [DONE]`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_after_done","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, body)}

	result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(
		context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{},
	)

	require.NoError(t, err)
	require.NotNil(t, result.Supported)
	require.False(t, *result.Supported)
	require.Equal(t, OpenAINativeCompactionPostTerminalFrame, result.SemanticOutcome)
}

func TestOpenAINativeCompactionProbeNilBodyAndStageLimitAreInconclusive(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		upstream := &nativeCompactionProbeUpstreamRecorder{response: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}}
		result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.Error(t, err)
		require.Nil(t, result.Supported)
		require.Equal(t, OpenAINativeCompactionIncompleteStream, result.SemanticOutcome)
	})
	t.Run("stage event limit", func(t *testing.T) {
		upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, "valid_one"))}
		result, err := nativeProbeService(upstream).RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{MaxEvents: 1})
		require.Error(t, err)
		require.Nil(t, result.Supported)
		require.Equal(t, OpenAINativeCompactionResourceLimit, result.SemanticOutcome)
	})
}

func TestOpenAINativeCompactionProbeUsesDoWithTLSOnlyOnceAndNoFailover(t *testing.T) {
	transportErr := errors.New("synthetic transport failure")
	upstream := &nativeCompactionProbeUpstreamRecorder{err: transportErr}
	service, _ := newNativeCompactionProbeService(upstream)

	result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
	require.ErrorIs(t, err, transportErr)
	require.Nil(t, result.Supported)
	require.Equal(t, OpenAINativeCompactionTransportFailure, result.SemanticOutcome)
	require.NotNil(t, upstream.request, "the single candidate attempt reached DoWithTLS")
}

func TestOpenAINativeCompactionProbeHonorsTimeoutAndBounds(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		upstream := &nativeCompactionProbeUpstreamRecorder{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &contextBlockingReadCloser{},
		}}
		service, _ := newNativeCompactionProbeService(upstream)
		started := time.Now()
		result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{Timeout: 20 * time.Millisecond})
		require.Error(t, err)
		require.Less(t, time.Since(started), time.Second)
		require.Nil(t, result.Supported)
	})

	t.Run("event bytes", func(t *testing.T) {
		large := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 256) + "\"}\n\n"
		upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, large)}
		service, _ := newNativeCompactionProbeService(upstream)
		result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{MaxEventBytes: 128})
		require.Error(t, err)
		require.Nil(t, result.Supported)
		require.Equal(t, OpenAINativeCompactionInvalidEvent, result.SemanticOutcome)
	})

	t.Run("raw response bytes include SSE comments", func(t *testing.T) {
		body := strings.Repeat(": framing-padding\n", 32) + readOpenAINativeCompactionFixture(t, "valid_one")
		upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, body)}
		service, _ := newNativeCompactionProbeService(upstream)
		result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{MaxBytes: 128})
		require.ErrorIs(t, err, ErrOpenAINativeCompactionProbeResponseBytes)
		require.Nil(t, result.Supported)
		require.Equal(t, OpenAINativeCompactionResourceLimit, result.SemanticOutcome)
	})
}

func TestOpenAINativeCompactionProbeCostGateFailsBeforeHTTP(t *testing.T) {
	tests := []struct {
		name        string
		account     *Account
		lookup      *nativeCompactionProbePriceLookup
		budget      *nativeCompactionProbeBudgetRecorder
		options     OpenAINativeCompactionProbeOptions
		wantErr     error
		wantReserve int
	}{
		{
			name:    "provider price unavailable",
			account: nativeProbeAPIKeyAccount(),
			lookup:  &nativeCompactionProbePriceLookup{err: ErrOpenAINativeCompactionProbePriceUnavailable},
			budget:  &nativeCompactionProbeBudgetRecorder{},
			wantErr: ErrOpenAINativeCompactionProbePriceUnavailable,
		},
		{
			name:    "per run limit",
			account: nativeProbeAPIKeyAccount(),
			lookup: &nativeCompactionProbePriceLookup{price: OpenAIProviderTokenPrice{
				MatchedModel: "gpt-5.6-sol", InputUSDPerToken: 5e-6, OutputUSDPerToken: 30e-6,
			}},
			budget:  &nativeCompactionProbeBudgetRecorder{},
			options: OpenAINativeCompactionProbeOptions{PerRunCostLimitMicroUSD: 1},
			wantErr: ErrOpenAINativeCompactionProbeCostLimit,
		},
		{
			name:        "daily reserve rejected",
			account:     nativeProbeAPIKeyAccount(),
			lookup:      &nativeCompactionProbePriceLookup{},
			budget:      &nativeCompactionProbeBudgetRecorder{reserveErr: ErrOpenAINativeCompactionProbeBudgetExceeded},
			wantErr:     ErrOpenAINativeCompactionProbeBudgetExceeded,
			wantReserve: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := &nativeCompactionProbeUpstreamRecorder{}
			service := &OpenAIGatewayService{
				httpUpstream:           upstream,
				openAIProbePriceLookup: test.lookup,
				openAIProbeBudgetRepo:  test.budget,
			}
			_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), test.account, "gpt-5.6", test.options)
			require.ErrorIs(t, err, test.wantErr)
			require.Nil(t, upstream.request)
			require.Equal(t, test.wantReserve, test.budget.reserved)
			require.Zero(t, test.budget.dispatched)
			require.Zero(t, test.budget.committed)
			require.Zero(t, test.budget.released)
		})
	}
}

func TestOpenAINativeCompactionProbeBudgetLifecycle(t *testing.T) {
	t.Run("credential failure releases before dispatch", func(t *testing.T) {
		account := nativeProbeAPIKeyAccount()
		delete(account.Credentials, "api_key")
		upstream := &nativeCompactionProbeUpstreamRecorder{}
		service, budget := newNativeCompactionProbeService(upstream)

		_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), account, "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.Error(t, err)
		require.Nil(t, upstream.request)
		require.Equal(t, 1, budget.reserved)
		require.Zero(t, budget.dispatched)
		require.Zero(t, budget.committed)
		require.Equal(t, 1, budget.released)
	})

	t.Run("dispatch marker failure releases and skips HTTP", func(t *testing.T) {
		upstream := &nativeCompactionProbeUpstreamRecorder{}
		service, budget := newNativeCompactionProbeService(upstream)
		budget.markDispatchedErr = ErrOpenAINativeCompactionProbeBudgetUnavailable

		_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.ErrorIs(t, err, ErrOpenAINativeCompactionProbeBudgetUnavailable)
		require.Nil(t, upstream.request)
		require.Equal(t, 1, budget.reserved)
		require.Equal(t, 1, budget.dispatched)
		require.Zero(t, budget.committed)
		require.Equal(t, 1, budget.released)
	})

	t.Run("transport failure commits after dispatch", func(t *testing.T) {
		transportErr := errors.New("synthetic transport failure")
		upstream := &nativeCompactionProbeUpstreamRecorder{err: transportErr}
		service, budget := newNativeCompactionProbeService(upstream)

		_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.ErrorIs(t, err, transportErr)
		require.NotNil(t, upstream.request)
		require.Equal(t, 1, budget.reserved)
		require.Equal(t, 1, budget.dispatched)
		require.Equal(t, 1, budget.committed)
		require.Zero(t, budget.released)
	})

	t.Run("semantic failure still commits", func(t *testing.T) {
		upstream := &nativeCompactionProbeUpstreamRecorder{response: nativeProbeResponse(http.StatusOK, readOpenAINativeCompactionFixture(t, "malformed_compaction"))}
		service, budget := newNativeCompactionProbeService(upstream)

		result, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.NoError(t, err)
		require.Equal(t, OpenAINativeCompactionMalformedCompaction, result.SemanticOutcome)
		require.Equal(t, 1, budget.committed)
		require.Zero(t, budget.released)
	})

	t.Run("commit failure closes response and does not retry", func(t *testing.T) {
		body := &nativeProbeTrackingBody{Reader: strings.NewReader(readOpenAINativeCompactionFixture(t, "valid_one"))}
		upstream := &nativeCompactionProbeUpstreamRecorder{response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}}
		service, budget := newNativeCompactionProbeService(upstream)
		budget.commitErr = ErrOpenAINativeCompactionProbeBudgetUnavailable

		_, err := service.RunOpenAINativeCompactionV2Probe(context.Background(), nativeProbeAPIKeyAccount(), "gpt-5.6", OpenAINativeCompactionProbeOptions{})
		require.ErrorIs(t, err, ErrOpenAINativeCompactionProbeBudgetUnavailable)
		require.True(t, body.closed)
		require.Equal(t, 1, budget.committed)
		require.Zero(t, budget.released)
	})
}

type nativeProbeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *nativeProbeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func nativeProbeAPIKeyAccount() *Account {
	return &Account{
		ID:          91,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 2,
		Credentials: map[string]any{
			"api_key":  "synthetic-api-key",
			"base_url": "https://api.openai.com/v1",
		},
	}
}

func boolPointerInt64(value int64) *int64 {
	return &value
}

func nativeProbeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type contextBlockingReadCloser struct{}

func (r *contextBlockingReadCloser) Read(_ []byte) (int, error) {
	return 0, context.DeadlineExceeded
}

func (r *contextBlockingReadCloser) Close() error { return nil }
