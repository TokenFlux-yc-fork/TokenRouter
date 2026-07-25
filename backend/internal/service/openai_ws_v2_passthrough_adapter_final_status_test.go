package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSV2FinalStatusConn struct {
	*openAIWSCaptureConn
	finalReadErr error
}

func (c *openAIWSV2FinalStatusConn) ReadMessage(ctx context.Context) ([]byte, error) {
	payload, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if errors.Is(err, io.EOF) && c.finalReadErr != nil {
		return nil, c.finalReadErr
	}
	return payload, err
}

func (c *openAIWSV2FinalStatusConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	payload, err := c.ReadMessage(ctx)
	if err != nil {
		return coderws.MessageText, nil, err
	}
	return coderws.MessageText, payload, nil
}

type openAIWSV2FinalStatusDialer struct {
	mu    sync.Mutex
	conn  openAIWSClientConn
	calls int
}

func (d *openAIWSV2FinalStatusDialer) Dial(
	context.Context,
	string,
	http.Header,
	string,
	*tlsfingerprint.Profile,
) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return d.conn, 0, nil, nil
}

func (d *openAIWSV2FinalStatusDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func TestOpenAIWSV2PassthroughResponseFailedRateLimitReturns429Failover(t *testing.T) {
	upstream := newOpenAIWSV2FinalStatusConn([][]byte{
		[]byte(`{"type":"response.failed","response":{"id":"resp_rate_limit","status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}`),
	}, nil)
	dialer := &openAIWSV2FinalStatusDialer{conn: upstream}
	svc := newOpenAIWSV2FinalStatusService(dialer, nil)
	account := newOpenAIWSV2FinalStatusAccount(1301, true)

	proxyErr := runOpenAIWSV2FinalStatusProxy(t, svc, account)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, proxyErr, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
}

func TestOpenAIWSV2PassthroughContextWindowWithoutTypeDoesNotFailover(t *testing.T) {
	upstream := newOpenAIWSV2FinalStatusConn([][]byte{
		[]byte(`{"type":"response.failed","response":{"id":"resp_context","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`),
	}, nil)
	svc := newOpenAIWSV2FinalStatusService(&openAIWSV2FinalStatusDialer{conn: upstream}, nil)
	account := newOpenAIWSV2FinalStatusAccount(1302, true)

	proxyErr := runOpenAIWSV2FinalStatusProxy(t, svc, account)

	require.Error(t, proxyErr)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(proxyErr, &failoverErr))
}

func TestOpenAIWSV2PassthroughPoolCapacityRetriesSameAccount(t *testing.T) {
	upstream := newOpenAIWSV2FinalStatusConn([][]byte{
		[]byte(`{"type":"response.failed","response":{"id":"resp_capacity","status":"failed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`),
	}, nil)
	svc := newOpenAIWSV2FinalStatusService(&openAIWSV2FinalStatusDialer{conn: upstream}, nil)
	account := newOpenAIWSV2FinalStatusAccount(1303, true)

	proxyErr := runOpenAIWSV2FinalStatusProxy(t, svc, account)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, proxyErr, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
}

func TestOpenAIWSV2PassthroughFailedCompletedCapacityRetriesSameAccount(t *testing.T) {
	upstream := newOpenAIWSV2FinalStatusConn([][]byte{
		[]byte(`{"type":"response.output_item.done","item":{"id":"fc_capacity","type":"function_call","call_id":"call_must_not_execute","name":"exec_command","arguments":"{}"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_capacity","status":"failed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`),
	}, nil)
	svc := newOpenAIWSV2FinalStatusService(&openAIWSV2FinalStatusDialer{conn: upstream}, nil)
	account := newOpenAIWSV2FinalStatusAccount(1313, true)

	proxyErr := runOpenAIWSV2FinalStatusProxy(t, svc, account)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, proxyErr, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
}

func TestOpenAIWSV2PassthroughCapacityAfterOutputReturnsCloseErrorWithoutFailover(t *testing.T) {
	upstream := newOpenAIWSV2FinalStatusConn([][]byte{
		[]byte(`{"type":"response.output_text.delta","response_id":"resp_capacity_after_output","delta":"partial"}`),
		[]byte(`{"type":"response.failed","response":{"id":"resp_capacity_after_output","status":"failed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`),
	}, nil)
	dialer := &openAIWSV2FinalStatusDialer{conn: upstream}
	svc := newOpenAIWSV2FinalStatusService(dialer, nil)
	account := newOpenAIWSV2FinalStatusAccount(1304, true)

	proxyErr := runOpenAIWSV2FinalStatusProxy(t, svc, account)

	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, proxyErr, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(proxyErr, &failoverErr))
	require.Equal(t, 1, dialer.callCount())
}

func newOpenAIWSV2FinalStatusConn(events [][]byte, finalReadErr error) *openAIWSV2FinalStatusConn {
	return &openAIWSV2FinalStatusConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{events: events},
		finalReadErr:        finalReadErr,
	}
}

func newOpenAIWSV2FinalStatusService(
	dialer openAIWSClientDialer,
	rateLimitService *RateLimitService,
) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 1
	return &OpenAIGatewayService{
		cfg:                       cfg,
		openaiWSPassthroughDialer: dialer,
		rateLimitService:          rateLimitService,
	}
}

func newOpenAIWSV2FinalStatusAccount(id int64, poolMode bool) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-ws-v2-final-status",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":   "sk-test",
			"pool_mode": poolMode,
		},
	}
}

func runOpenAIWSV2FinalStatusProxy(t *testing.T, svc *OpenAIGatewayService, account *Account) error {
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
			[]byte(`{"type":"response.create","model":"gpt-5.1","input":"ping"}`),
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
	readCtx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	go func() {
		for {
			if _, _, readErr := clientConn.Read(readCtx); readErr != nil {
				return
			}
		}
	}()

	select {
	case proxyErr := <-errCh:
		return proxyErr
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for websocket passthrough proxy")
		return nil
	}
}
