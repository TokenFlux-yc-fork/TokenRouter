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
	account *Account
	calls   []gatewayPassiveBreakerCall
}

func (r *gatewayPassiveBreakerAccountRepo) GetByID(_ context.Context, accountID int64) (*Account, error) {
	if r.account == nil || r.account.ID != accountID {
		return nil, errors.New("account not found")
	}
	return r.account, nil
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

func TestGatewayRetryablePoolFailureDefersPassiveAccountCircuitBreakerUntilHandlerRetryExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(705, PlatformAnthropic)
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &GatewayService{
		accountRepo:      repo,
		rateLimitService: rateLimitService,
	}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily busy"}}`)),
	}

	svc.handleRetryExhaustedSideEffects(context.Background(), resp, account)

	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
	require.True(t, account.IsSchedulable(), "same-account retry must remain selectable")

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusTooManyRequests,
		ResponseBody:           []byte(`{"error":{"message":"temporarily busy"}}`),
		RetryableOnSameAccount: true,
	})

	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusTooManyRequests)
	require.False(t, account.IsSchedulable())
}

func TestOpenAIRetryableSynthetic502RecordsPassiveAccountCircuitBreakerAfterHandlerRetryExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(708, PlatformOpenAI)
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`),
		RetryableOnSameAccount: true,
	})

	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusBadGateway)
}

func TestOpenAIRetryableCapacity400RecordsPassiveAccountCircuitBreakerAfterHandlerRetryExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(709, PlatformOpenAI)
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusBadRequest,
		ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`),
		RetryableOnSameAccount: true,
	})

	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusBadRequest)
}

func TestOpenAIRetryableOrdinary400DoesNotRecordPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(710, PlatformOpenAI)
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusBadRequest,
		ResponseBody:           []byte(`{"error":{"code":"invalid_request_error","message":"Missing required parameter: input."}}`),
		RetryableOnSameAccount: true,
	})

	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

func TestOpenAIRequestBlocked403DoesNotRecordPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(711, PlatformOpenAI)
	account.Credentials["pool_mode_retry_status_codes"] = []any{float64(http.StatusForbidden)}
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusForbidden,
		ResponseBody:           []byte(`{"error":{"message":"Your request was blocked."}}`),
		RetryableOnSameAccount: true,
	})

	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

func TestOpenAIOrdinary403StillRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGatewayPassiveBreakerAccount(712, PlatformOpenAI)
	account.Credentials["pool_mode_retry_status_codes"] = []any{float64(http.StatusForbidden)}
	repo := &gatewayPassiveBreakerAccountRepo{account: account}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}

	svc.TempUnscheduleRetryableError(context.Background(), account.ID, &UpstreamFailoverError{
		StatusCode:             http.StatusForbidden,
		ResponseBody:           []byte(`{"error":{"message":"API key does not have access to this resource."}}`),
		RetryableOnSameAccount: true,
	})

	requireGatewayPassiveAccountBreaker(t, repo, account, http.StatusForbidden)
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

func TestGeminiMessagesTransportRetrySuccessDoesNotRecordPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(706, PlatformGemini)
	upstream := &gatewayPassiveBreakerRetryThenSuccessUpstream{}
	svc := &GeminiMessagesCompatService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	c := newGatewayPassiveBreakerContext()
	body := []byte(`{"model":"gemini-test-model","messages":[{"role":"user","content":"ping"}]}`)

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Empty(t, repo.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

func TestGeminiNativeTransportRetrySuccessDoesNotRecordPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayPassiveBreakerAccountRepo{}
	account := newGatewayPassiveBreakerAccount(707, PlatformGemini)
	account.Credentials["base_url"] = "https://gemini-breaker.test"
	upstream := &gatewayPassiveBreakerRetryThenSuccessUpstream{}
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
