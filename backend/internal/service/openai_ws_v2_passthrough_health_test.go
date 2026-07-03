package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSErrorDialer struct {
	status  int
	headers http.Header
	err     error
}

func (d *openAIWSErrorDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	return nil, d.status, cloneHeader(d.headers), d.err
}

type openAIWSStaticDialer struct {
	conn    openAIWSClientConn
	status  int
	headers http.Header
}

func (d *openAIWSStaticDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
	profile *tlsfingerprint.Profile,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	_ = profile
	return d.conn, d.status, cloneHeader(d.headers), nil
}

type openAIWSReadErrorConn struct {
	err error
}

func (c *openAIWSReadErrorConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	_ = value
	return nil
}

func (c *openAIWSReadErrorConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	return nil, c.err
}

func (c *openAIWSReadErrorConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	_ = ctx
	return coderws.MessageText, nil, c.err
}

func (c *openAIWSReadErrorConn) WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error {
	_ = ctx
	_ = msgType
	_ = payload
	return nil
}

func (c *openAIWSReadErrorConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSReadErrorConn) Close() error {
	return nil
}

func TestOpenAIWSV2PassthroughDialErrorRecordsPassiveGroupHealthFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const groupID int64 = 404
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	svc := &OpenAIGatewayService{
		cfg: cfg,
		openaiWSPassthroughDialer: &openAIWSErrorDialer{
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"x-request-id": []string{"rid_ws_v2_dial"}},
			err:     errors.New("upstream websocket unavailable"),
		},
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       504,
		Name:     "openai-ws-pool",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "sk-upstream",
			"pool_mode": true,
		},
		GroupIDs:    []int64{groupID},
		Concurrency: 1,
		Status:      StatusActive,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())

		errCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-upstream",
			[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
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
	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}

func TestOpenAIWSV2PassthroughUpstreamReadErrorRecordsPassiveGroupHealthFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const groupID int64 = 406
	rateLimitService, groupRepo := newPassiveHealthRateLimitService(groupID)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 1
	svc := &OpenAIGatewayService{
		cfg: cfg,
		openaiWSPassthroughDialer: &openAIWSStaticDialer{
			conn:    &openAIWSReadErrorConn{err: errors.New("upstream read failed")},
			headers: http.Header{"x-request-id": []string{"rid_ws_v2_read"}},
		},
		rateLimitService: rateLimitService,
	}
	account := &Account{
		ID:       506,
		Name:     "openai-ws-pool",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "sk-upstream",
			"pool_mode": true,
		},
		GroupIDs:    []int64{groupID},
		Concurrency: 1,
		Status:      StatusActive,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())

		errCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-upstream",
			[]byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`),
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
	requireSinglePassiveHealthUpdate(t, groupRepo, groupID)
}
