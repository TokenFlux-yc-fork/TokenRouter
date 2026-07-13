package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/model"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIExitBreakerCall struct {
	accountID int64
	until     time.Time
	reason    string
}

type openAIExitBreakerAccountRepo struct {
	AccountRepository
	mu    sync.Mutex
	calls []openAIExitBreakerCall
}

func (r *openAIExitBreakerAccountRepo) SetTempUnschedulable(_ context.Context, accountID int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, openAIExitBreakerCall{
		accountID: accountID,
		until:     until,
		reason:    reason,
	})
	return nil
}

func (r *openAIExitBreakerAccountRepo) snapshotCalls() []openAIExitBreakerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]openAIExitBreakerCall(nil), r.calls...)
}

type openAIExitBreakerHTTPUpstream struct {
	err error
}

func (u *openAIExitBreakerHTTPUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, u.err
}

func (u *openAIExitBreakerHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type openAIExitBreakerErrorDialer struct {
	status  int
	headers http.Header
	err     error
}

func (d *openAIExitBreakerErrorDialer) Dial(context.Context, string, http.Header, string, *tlsfingerprint.Profile) (openAIWSClientConn, int, http.Header, error) {
	return nil, d.status, cloneHeader(d.headers), d.err
}

type openAIExitBreakerStaticDialer struct {
	conn    openAIWSClientConn
	status  int
	headers http.Header
}

func (d *openAIExitBreakerStaticDialer) Dial(context.Context, string, http.Header, string, *tlsfingerprint.Profile) (openAIWSClientConn, int, http.Header, error) {
	return d.conn, d.status, cloneHeader(d.headers), nil
}

type openAIExitBreakerReadErrorConn struct {
	err error
}

func (c *openAIExitBreakerReadErrorConn) WriteJSON(context.Context, any) error {
	return nil
}

func (c *openAIExitBreakerReadErrorConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, c.err
}

func (c *openAIExitBreakerReadErrorConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	return coderws.MessageText, nil, c.err
}

func (c *openAIExitBreakerReadErrorConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return nil
}

func (c *openAIExitBreakerReadErrorConn) Ping(context.Context) error {
	return nil
}

func (c *openAIExitBreakerReadErrorConn) Close() error {
	return nil
}

func newOpenAIExitBreakerAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-pool",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":   "sk-test",
			"pool_mode": true,
		},
	}
}

func newOpenAIExitBreakerService() (*RateLimitService, *openAIExitBreakerAccountRepo) {
	repo := &openAIExitBreakerAccountRepo{}
	return NewRateLimitService(repo, nil, &config.Config{}, nil, nil), repo
}

func newOpenAIExitBreakerContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func newOpenAIExitBreaker503Response() *http.Response {
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"rid_openai_exit_breaker"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"upstream overloaded","type":"server_error"}}`)),
	}
}

func requireOpenAIExitBreakerCall(t *testing.T, repo *openAIExitBreakerAccountRepo, account *Account, statusCode int) {
	t.Helper()
	calls := repo.snapshotCalls()
	require.Len(t, calls, 1)
	require.Equal(t, account.ID, calls[0].accountID)
	require.True(t, calls[0].until.After(time.Now()))

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(calls[0].reason), &state))
	require.Equal(t, statusCode, state.StatusCode)
	require.Equal(t, "passive_account_circuit_breaker", state.MatchedKeyword)
	require.Equal(t, passiveAccountCircuitBreakerRuleIndex, state.RuleIndex)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.Equal(t, calls[0].reason, account.TempUnschedulableReason)
}

func TestOpenAIPassthroughTransportErrorRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newOpenAIExitBreakerAccount(1201)
	account.Extra = map[string]any{"openai_passthrough": true}
	rateLimitService, repo := newOpenAIExitBreakerService()
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		httpUpstream:     &openAIExitBreakerHTTPUpstream{err: errors.New("upstream connection reset")},
		rateLimitService: rateLimitService,
	}
	c, rec := newOpenAIExitBreakerContext()

	result, err := svc.Forward(
		context.Background(),
		c,
		account,
		[]byte(`{"model":"gpt-5","stream":false,"input":"ping"}`),
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Zero(t, rec.Body.Len(), "transport failure must remain uncommitted for account failover")
	requireOpenAIExitBreakerCall(t, repo, account, 0)
}

func TestOpenAI503ErrorPassthroughRuleRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newOpenAIExitBreakerAccount(1202)
	rateLimitService, repo := newOpenAIExitBreakerService()
	svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rateLimitService}
	c, rec := newOpenAIExitBreakerContext()
	message := "passthrough upstream error"
	ruleService := &ErrorPassthroughService{}
	ruleService.setLocalCache([]*model.ErrorPassthroughRule{{
		Name:            "openai 503 passthrough",
		Enabled:         true,
		Priority:        1,
		ErrorCodes:      []int{http.StatusServiceUnavailable},
		MatchMode:       model.MatchModeAny,
		Platforms:       []string{PlatformOpenAI},
		PassthroughCode: true,
		PassthroughBody: false,
		CustomMessage:   &message,
	}})
	BindErrorPassthroughService(c, ruleService)

	result, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIExitBreaker503Response(),
		c,
		account,
		[]byte(`{"model":"gpt-5"}`),
		"gpt-5",
	)

	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	requireOpenAIExitBreakerCall(t, repo, account, http.StatusServiceUnavailable)
}

func TestOpenAI503CustomErrorCodeSkipRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newOpenAIExitBreakerAccount(1203)
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusUnauthorized)}
	rateLimitService, repo := newOpenAIExitBreakerService()
	svc := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: rateLimitService}
	c, rec := newOpenAIExitBreakerContext()

	result, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIExitBreaker503Response(),
		c,
		account,
		[]byte(`{"model":"gpt-5"}`),
		"gpt-5",
	)

	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	requireOpenAIExitBreakerCall(t, repo, account, http.StatusServiceUnavailable)
}

func TestOpenAIWSV2PassthroughDialErrorRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	account := newOpenAIExitBreakerAccount(1204)
	rateLimitService, repo := newOpenAIExitBreakerService()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	svc := &OpenAIGatewayService{
		cfg: cfg,
		openaiWSPassthroughDialer: &openAIExitBreakerErrorDialer{
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"x-request-id": []string{"rid_ws_v2_dial"}},
			err:     errors.New("upstream websocket unavailable"),
		},
		rateLimitService: rateLimitService,
	}

	runOpenAIExitBreakerWSPassthrough(t, svc, account)
	requireOpenAIExitBreakerCall(t, repo, account, http.StatusServiceUnavailable)
}

func TestOpenAIWSV2PassthroughReadErrorRecordsPassiveAccountCircuitBreaker(t *testing.T) {
	account := newOpenAIExitBreakerAccount(1205)
	rateLimitService, repo := newOpenAIExitBreakerService()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 1
	svc := &OpenAIGatewayService{
		cfg: cfg,
		openaiWSPassthroughDialer: &openAIExitBreakerStaticDialer{
			conn:    &openAIExitBreakerReadErrorConn{err: errors.New("upstream read failed")},
			headers: http.Header{"x-request-id": []string{"rid_ws_v2_read"}},
		},
		rateLimitService: rateLimitService,
	}

	runOpenAIExitBreakerWSPassthrough(t, svc, account)
	requireOpenAIExitBreakerCall(t, repo, account, 0)
}

func runOpenAIExitBreakerWSPassthrough(t *testing.T, svc *OpenAIGatewayService, account *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		errCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			[]byte(`{"type":"response.create","model":"gpt-5","input":"ping"}`),
			nil,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
			TLSFingerprintRouterMatchResult{},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	select {
	case proxyErr := <-errCh:
		require.Error(t, proxyErr)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for passthrough proxy")
	}
}
