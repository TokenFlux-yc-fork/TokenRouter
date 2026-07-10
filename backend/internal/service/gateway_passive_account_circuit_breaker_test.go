package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayPassiveBreakerCall struct {
	accountID int64
	until     time.Time
	reason    string
}

type gatewayPassiveBreakerAccountRepo struct {
	AccountRepository
	calls []gatewayPassiveBreakerCall
}

func (r *gatewayPassiveBreakerAccountRepo) SetTempUnschedulable(_ context.Context, accountID int64, until time.Time, reason string) error {
	r.calls = append(r.calls, gatewayPassiveBreakerCall{
		accountID: accountID,
		until:     until,
		reason:    reason,
	})
	return nil
}

type gatewayPassiveBreakerUpstream struct {
	statusCode int
	body       string
	calls      int
}

type gatewayPassiveBreakerRetryThenSuccessUpstream struct {
	calls int
}

func (u *gatewayPassiveBreakerRetryThenSuccessUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	if u.calls == 1 {
		return nil, errors.New("temporary upstream connection reset")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"candidates":[{"content":{"parts":[{"text":"recovered"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
		)),
	}, nil
}

func (u *gatewayPassiveBreakerRetryThenSuccessUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *gatewayPassiveBreakerUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: u.statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func (u *gatewayPassiveBreakerUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func newGatewayPassiveBreakerAccount(id int64, platform string) *Account {
	return &Account{
		ID:          id,
		Name:        "pool-account",
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":   "test-key",
			"pool_mode": true,
		},
	}
}

func newGatewayPassiveBreakerContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
	return c
}

func requireGatewayPassiveAccountBreaker(t *testing.T, repo *gatewayPassiveBreakerAccountRepo, account *Account, statusCode int) {
	t.Helper()
	require.Len(t, repo.calls, 1)
	call := repo.calls[0]
	require.Equal(t, account.ID, call.accountID)
	require.True(t, call.until.After(time.Now()))

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(call.reason), &state))
	require.Equal(t, statusCode, state.StatusCode)
	require.Equal(t, "passive_account_circuit_breaker", state.MatchedKeyword)
	require.Equal(t, passiveAccountCircuitBreakerRuleIndex, state.RuleIndex)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.Equal(t, call.reason, account.TempUnschedulableReason)
}

func TestGatewayRetryExhaustedRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(701, PlatformAnthropic)
	svc := &GatewayService{
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"service temporarily unavailable"}}`)),
	}

	svc.handleRetryExhaustedSideEffects(context.Background(), resp, account)

	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusServiceUnavailable)
}

func TestGeminiNative503FinalExitRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(702, PlatformGemini)
	account.Credentials["base_url"] = "https://gemini-breaker.test"
	upstream := &gatewayPassiveBreakerUpstream{
		statusCode: http.StatusServiceUnavailable,
		body:       `{"error":{"message":"service temporarily unavailable"}}`,
	}
	svc := &GeminiMessagesCompatService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	c := newGatewayPassiveBreakerContext()

	result, err := svc.ForwardNative(
		context.Background(),
		c,
		account,
		"gemini-test-model",
		"generateContent",
		false,
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`),
	)

	require.Nil(t, result)
	require.ErrorContains(t, err, "skipped by error policy")
	require.Equal(t, 1, upstream.calls)
	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusServiceUnavailable)
}

func TestGeminiChatCompletionsTransportRetrySuccessDoesNotRecordPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(704, PlatformGemini)
	upstream := &gatewayPassiveBreakerRetryThenSuccessUpstream{}
	svc := &GeminiMessagesCompatService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	c := newGatewayPassiveBreakerContext()
	body := []byte(`{"model":"gemini-test-model","messages":[{"role":"user","content":"ping"}]}`)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

func TestAntigravity503FinalExitRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(antigravityForwardBaseURLEnv, "")
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(703, PlatformAntigravity)
	upstream := &gatewayPassiveBreakerUpstream{
		statusCode: http.StatusServiceUnavailable,
		body:       `{"error":{"message":"service temporarily unavailable","status":"UNAVAILABLE"}}`,
	}
	svc := &AntigravityGatewayService{
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	c := newGatewayPassiveBreakerContext()

	result, err := svc.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx:            context.Background(),
		c:              c,
		prefix:         "[passive-breaker-test]",
		account:        account,
		accessToken:    "test-token",
		action:         "generateContent",
		body:           []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}}`),
		httpUpstream:   upstream,
		requestedModel: "gemini-test-model",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.resp)
	defer func() { _ = result.resp.Body.Close() }()
	require.Equal(t, http.StatusInternalServerError, result.resp.StatusCode)
	require.Equal(t, 1, upstream.calls)
	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusServiceUnavailable)
}
