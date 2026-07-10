//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// openAITransportAccountRepoStub 只记录临时不可调度调用，其他仓储方法不应被触达。
type openAITransportAccountRepoStub struct {
	AccountRepository
	tempUnschedCalls []tempUnschedCall
}

func (r *openAITransportAccountRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls = append(r.tempUnschedCalls, tempUnschedCall{accountID: id, until: until, reason: reason})
	return nil
}

func newOpenAITransportErrTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func TestHandleOpenAIUpstreamTransportError_PersistentEvictsAndFailsOver(t *testing.T) {
	repo := &openAITransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 4627, Name: "proxy-expired", Platform: PlatformOpenAI}
	c, rec := newOpenAITransportErrTestContext()

	before := time.Now()
	retErr := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account,
		errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`), false)
	after := time.Now()

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(retErr, &failoverErr), "persistent error must return *UpstreamFailoverError")
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Len(t, repo.tempUnschedCalls, 1)
	require.Equal(t, int64(4627), repo.tempUnschedCalls[0].accountID)
	require.Contains(t, repo.tempUnschedCalls[0].reason, "authentication failed")
	require.True(t, repo.tempUnschedCalls[0].until.After(before.Add(openAITransportErrorTempUnschedDuration-time.Second)))
	require.True(t, repo.tempUnschedCalls[0].until.Before(after.Add(openAITransportErrorTempUnschedDuration+time.Second)))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_TransientFailsOverWithoutEviction(t *testing.T) {
	repo := &openAITransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 99, Name: "flaky", Platform: PlatformOpenAI}
	c, rec := newOpenAITransportErrTestContext()

	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account,
		errors.New(`Post "https://chatgpt.com/...": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`), false)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "transient error must return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_ContextCanceledNoFailover(t *testing.T) {
	repo := &openAITransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 77, Name: "healthy", Platform: PlatformOpenAI}
	c, rec := newOpenAITransportErrTestContext()

	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, context.Canceled, false)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "context.Canceled must not return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 0, rec.Body.Len())
}

func TestHandleOpenAIUpstreamTransportError_WrappedContextCanceledNoFailover(t *testing.T) {
	repo := &openAITransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 78, Name: "healthy2", Platform: PlatformOpenAI}
	c, _ := newOpenAITransportErrTestContext()

	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, fmt.Errorf("http request failed: %w", context.Canceled), false)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "wrapped context.Canceled must not return *UpstreamFailoverError")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestHandleOpenAIUpstreamTransportError_CanceledRequestContextWithEOFNoFailover(t *testing.T) {
	repo := &openAITransportAccountRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 79, Name: "healthy3", Platform: PlatformOpenAI}
	c, _ := newOpenAITransportErrTestContext()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, io.EOF, false)

	require.ErrorIs(t, err, context.Canceled)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "canceled request context must suppress failover even when detached transport returns EOF")
	require.Empty(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded, "client cancellation must not be recorded as an upstream account failure")
}

func newOpenAITransportForwardTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
}

func newOpenAITransportForwardTestAccount(extra map[string]any) *Account {
	return &Account{
		ID:          91,
		Name:        "transport-test",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-transport-test"},
		Extra:       extra,
	}
}

func requireOpenAITransportForwardFailover(t *testing.T, err error, rec *httptest.ResponseRecorder) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, rec.Body.String(), "transport error must not commit a hard 502 before failover")
}

func TestOpenAIGatewayService_ForwardTransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"input_text","text":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{err: errors.New(`Post "https://api.openai.com/v1/responses": EOF`)}
	result, err := newOpenAITransportForwardTestService(upstream).Forward(
		context.Background(), c, newOpenAITransportForwardTestAccount(nil), body,
	)

	require.Nil(t, result)
	requireOpenAITransportForwardFailover(t, err, rec)
}

func TestOpenAIGatewayService_PassthroughTransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"input_text","text":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{err: errors.New(`Post "https://api.openai.com/v1/responses": EOF`)}
	result, err := newOpenAITransportForwardTestService(upstream).Forward(
		context.Background(), c, newOpenAITransportForwardTestAccount(map[string]any{"openai_passthrough": true}), body,
	)

	require.Nil(t, result)
	requireOpenAITransportForwardFailover(t, err, rec)
}

func TestOpenAIGatewayService_ForwardEmbeddingsTransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{err: errors.New(`Post "https://api.openai.com/v1/embeddings": EOF`)}
	result, err := newOpenAITransportForwardTestService(upstream).ForwardEmbeddings(
		context.Background(), c, newOpenAITransportForwardTestAccount(nil), body, "text-embedding-3-small",
	)

	require.Nil(t, result)
	requireOpenAITransportForwardFailover(t, err, rec)
}

func TestOpenAIGatewayService_ForwardImagesAPIKeyTransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-image-1","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{err: errors.New(`Post "https://api.openai.com/v1/images/generations": EOF`)}
	parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: "gpt-image-1", Prompt: "draw a cat"}
	result, err := newOpenAITransportForwardTestService(upstream).ForwardImages(
		context.Background(), c, newOpenAITransportForwardTestAccount(nil), body, parsed, "",
	)

	require.Nil(t, result)
	requireOpenAITransportForwardFailover(t, err, rec)
}

func TestOpenAIGatewayService_ForwardImagesOAuthTransportErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{err: errors.New(`Post "https://chatgpt.com/backend-api/images": EOF`)}
	account := &Account{
		ID:          92,
		Name:        "transport-test-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
	}
	parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: "gpt-image-2", Prompt: "draw a cat"}
	result, err := newOpenAITransportForwardTestService(upstream).ForwardImages(
		context.Background(), c, account, body, parsed, "",
	)

	require.Nil(t, result)
	requireOpenAITransportForwardFailover(t, err, rec)
}
