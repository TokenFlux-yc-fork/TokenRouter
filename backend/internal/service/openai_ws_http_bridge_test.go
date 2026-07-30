package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/pkg/xai"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResolveOpenAIWSClientFirstMessageTimeout(t *testing.T) {
	defaultTimeout := time.Duration(config.DefaultOpenAIWSClientFirstMessageTimeoutSeconds) * time.Second
	require.Equal(t, defaultTimeout, ResolveOpenAIWSClientFirstMessageTimeout(nil))

	cfg := &config.Config{}
	require.Equal(t, defaultTimeout, ResolveOpenAIWSClientFirstMessageTimeout(cfg))

	cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds = 120
	require.Equal(t, 120*time.Second, ResolveOpenAIWSClientFirstMessageTimeout(cfg))
}

type openAIWSHTTPBridgeErrTailReader struct {
	data []byte
	off  int
	err  error
}

func (r *openAIWSHTTPBridgeErrTailReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

func (r *openAIWSHTTPBridgeErrTailReader) Close() error { return nil }

type openAIWSHTTPBridgeBlockingBody struct {
	ctx          context.Context
	readStarted  chan struct{}
	readCanceled chan struct{}
	startedOnce  sync.Once
	canceledOnce sync.Once
}

func (b *openAIWSHTTPBridgeBlockingBody) Read([]byte) (int, error) {
	b.startedOnce.Do(func() { close(b.readStarted) })
	<-b.ctx.Done()
	b.canceledOnce.Do(func() { close(b.readCanceled) })
	return 0, b.ctx.Err()
}

func (b *openAIWSHTTPBridgeBlockingBody) Close() error { return nil }

type openAIWSHTTPBridgeBlockingUpstream struct {
	readStarted  chan struct{}
	readCanceled chan struct{}
}

func (u *openAIWSHTTPBridgeBlockingUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &openAIWSHTTPBridgeBlockingBody{
			ctx:          req.Context(),
			readStarted:  u.readStarted,
			readCanceled: u.readCanceled,
		},
	}, nil
}

func (u *openAIWSHTTPBridgeBlockingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestPrepareOpenAIWSHTTPBridgeBodyStripsWSFields(t *testing.T) {
	body, err := prepareOpenAIWSHTTPBridgeBody([]byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":false,"previous_response_id":"resp_prev","input":"hi"}`), false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "type").Exists())
	require.False(t, gjson.GetBytes(body, "generate").Exists())
	require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
	require.Equal(t, "gpt-5", gjson.GetBytes(body, "model").String())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.Equal(t, "hi", gjson.GetBytes(body, "input").String())
}

func TestOpenAIWSHTTPBridgeDecisionKeepsSmallFramesOnWS(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 100,
				},
			},
		},
	}

	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 99, ""))
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(nil, 100, ""))
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 1000, "resp_existing"))

	svc.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = false
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 1000, ""))
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(&Account{Platform: PlatformGrok}, 1, "resp_existing"))
}

func TestProxyOpenAIWSHTTPBridgeTurnTransportErrorFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		turn         int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn_fails_over_before_downstream_event", turn: 1, wantFailover: true},
		{name: "later_turn_does_not_replay_completed_turns", turn: 2, wantWrites: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: io.EOF}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{},
				httpUpstream: upstream,
			}
			account := &Account{
				ID:          8,
				Name:        "api-key",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "gpt-5", "", "", "", "", tt.turn,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
				require.JSONEq(t, string(openAITransportFailoverBody), string(failoverErr.ResponseBody))
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
			if tt.wantWrites > 0 {
				require.Equal(t, "error", gjson.GetBytes(writes[0], "type").String())
				require.Equal(t, int64(http.StatusBadGateway), gjson.GetBytes(writes[0], "status").Int())
			}
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnHTTPStatusFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		turn         int
		status       int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first_turn_401", turn: 1, status: http.StatusUnauthorized, wantFailover: true},
		{name: "first_turn_429", turn: 1, status: http.StatusTooManyRequests, wantFailover: true},
		{name: "first_turn_500", turn: 1, status: http.StatusInternalServerError, wantFailover: true},
		{name: "later_turn_500_does_not_replay", turn: 2, status: http.StatusInternalServerError, wantWrites: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tt.status,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"temporary upstream failure"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "gpt-5", "", "", "", "", tt.turn,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tt.status, failoverErr.StatusCode)
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnSSEErrorFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, turn := range []int{1, 2} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "gpt-5", "", "", "", "", turn,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if turn == 1 {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
				require.Empty(t, writes)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
				require.Len(t, writes, 1)
			}
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnRequiresTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		body         string
		wantFailover bool
		wantWrites   int
	}{
		{name: "done_without_events_fails_over", body: "data: [DONE]\n\n", wantFailover: true},
		{
			name: "created_then_done_is_truncated_not_success",
			body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_truncated\"}}\n\n" +
				"data: [DONE]\n\n",
			wantFailover: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "gpt-5", "", "", "", "", 1,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestOpenAIWSHTTPBridgeRelaysSSEFramesAsWebSocketMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"msg_bridge","type":"message","role":"assistant","content":[{"type":"output_text","text":"seeded"}]}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge"},"delta":"ok"}`,
		"",
		`data: {"type":"response.output_item.done","item":{"id":"fc_bridge","type":"function_call","call_id":"call_bridge","name":"exec_command","arguments":"{\"cmd\":\"true\"}"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_bridge","model":"gpt-5","usage":{"input_tokens":3,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 1,
				},
			},
		},
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          7,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
	}
	payload := []byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hi"}`)

	type bridgeResult struct {
		result *OpenAIForwardResult
		err    error
	}
	resultCh := make(chan bridgeResult, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			resultCh <- bridgeResult{err: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		ginCtx.Request = req

		writeClient := func(message []byte) error {
			writeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			return conn.Write(writeCtx, coderws.MessageText, message)
		}
		result, bridgeErr := svc.proxyOpenAIWSHTTPBridgeTurn(
			r.Context(),
			ginCtx,
			account,
			"sk-test",
			payload,
			len(payload),
			"gpt-5",
			"gpt-5",
			"",
			"",
			"",
			"",
			1,
			writeClient,
		)
		resultCh <- bridgeResult{result: result, err: bridgeErr}
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	readEvent := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	created := readEvent()
	added := readEvent()
	delta := readEvent()
	done := readEvent()
	completed := readEvent()

	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_item.added", gjson.GetBytes(added, "type").String())
	require.Equal(t, "seeded", gjson.GetBytes(added, "item.content.0.text").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.output_item.done", gjson.GetBytes(done, "type").String())
	require.Equal(t, "call_bridge", gjson.GetBytes(done, "item.call_id").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())

	select {
	case bridge := <-resultCh:
		require.NoError(t, bridge.err)
		require.NotNil(t, bridge.result)
		require.Equal(t, "resp_bridge", bridge.result.RequestID)
		require.Equal(t, 3, bridge.result.Usage.InputTokens)
		require.Equal(t, 2, bridge.result.Usage.OutputTokens)
		require.True(t, bridge.result.OpenAIWSMode)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bridge result")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
}

func TestOpenAIWSHTTPBridgeNativeCompactionPersistsLogicalWSAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.output_item.done","response_id":"resp_bridge_native","item":{"id":"cmp_bridge_native","type":"compaction","status":"completed","encrypted_content":"fixture-state"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_bridge_native","status":"completed","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"rid_bridge_native"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	attemptRepo := &stubUpstreamAttemptAttributionRepository{}
	svc := &OpenAIGatewayService{
		cfg:                            &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:                   upstream,
		toolCorrector:                  NewCodexToolCorrector(),
		upstreamAttemptAttributionRepo: attemptRepo,
	}
	account := &Account{
		ID:          73,
		Name:        "api-key-native",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Credentials: map[string]any{
			"api_key":  "sk-fixture",
			"base_url": "https://api.openai.com/v1",
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"previous_response_id":"resp_bridge_previous","include":["reasoning.encrypted_content"],"input":[{"type":"reasoning","encrypted_content":"fixture-previous-state"},{"type":"compaction_trigger"}]}`)
	var writes [][]byte
	ctx := withOpenAIWSAttemptIdentity(
		context.Background(),
		WSConnectionID("wsconn_fixture"),
		WSTurnID("wsturn_fixture"),
	)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		ctx, c, account, "sk-fixture", payload, len(payload),
		"gpt-5", "gpt-5", "", "", "", "", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, writes, 2)
	require.Equal(t, "response.output_item.done", gjson.GetBytes(writes[0], "type").String())
	require.Equal(t, "response.completed", gjson.GetBytes(writes[1], "type").String())
	require.Equal(t, "resp_bridge_native", result.ResponseID)
	require.True(t, result.NativeRemoteCompactionV2)
	require.Equal(t, OpenAINativeCompactionValid, result.SemanticOutcome)
	require.Equal(t, UpstreamAttemptTransportHTTP, result.Transport)
	require.Equal(t, WSConnectionID("wsconn_fixture"), result.WSConnectionID)
	require.Equal(t, WSTurnID("wsturn_fixture"), result.WSTurnID)
	require.NotEmpty(t, result.AttemptID)
	require.Equal(t, UpstreamRequestID("rid_bridge_native"), result.UpstreamRequestID)
	require.True(t, result.DeliveryCommitted)
	require.True(t, result.AttributionPersisted)
	require.True(t, result.UpstreamUsageObserved)
	require.True(t, result.CustomerSettlementAllowed())

	var terminal *UpstreamAttemptAttribution
	for _, row := range attemptRepo.snapshot() {
		if row.State == UpstreamAttemptStateTerminal {
			rowCopy := row
			terminal = &rowCopy
		}
	}
	require.NotNil(t, terminal)
	require.Equal(t, UpstreamAttemptTransportHTTP, terminal.Transport)
	require.Equal(t, WSConnectionID("wsconn_fixture"), terminal.WSConnectionID)
	require.Equal(t, WSTurnID("wsturn_fixture"), terminal.WSTurnID)
	require.True(t, terminal.HTTPObserved)
	require.NotNil(t, terminal.HTTPStatus)
	require.Equal(t, http.StatusOK, *terminal.HTTPStatus)
	require.Equal(t, string(OpenAINativeCompactionValid), terminal.Semantic.Outcome)
	require.Equal(t, int64(1), terminal.Semantic.CompactionItemCount)
	require.True(t, terminal.DeliveryCommitted)
	require.True(t, terminal.Usage.Observed)
	require.Equal(t, "resp_bridge_previous", gjson.GetBytes(upstream.lastBody, "previous_response_id").String())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(upstream.lastBody, "include.0").String())
	require.Equal(t, "fixture-previous-state", gjson.GetBytes(upstream.lastBody, "input.0.encrypted_content").String())
	require.Equal(t, "compaction_trigger", gjson.GetBytes(upstream.lastBody, "input.1.type").String())
}

func TestOpenAIWSHTTPBridgeNativeCompactionSemanticFailureDiscardsAllMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.output_item.done","response_id":"resp_bridge_invalid","item":{"id":"msg_bridge_invalid","type":"message","status":"completed","content":[{"type":"output_text","text":"must not leak"}]}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_bridge_invalid","status":"completed","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_bridge_invalid"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}}
	attemptRepo := &stubUpstreamAttemptAttributionRepository{}
	processBudget := NewOpenAIStageBudget(1 << 20)
	svc := &OpenAIGatewayService{
		cfg:                               &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:                      upstream,
		toolCorrector:                     NewCodexToolCorrector(),
		openAINativeCompactionStageBudget: processBudget,
		upstreamAttemptAttributionRepo:    attemptRepo,
	}
	account := &Account{
		ID:          74,
		Name:        "api-key-native-invalid",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Credentials: map[string]any{"api_key": "sk-fixture", "base_url": "https://api.openai.com/v1"},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":[{"type":"compaction_trigger"}]}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		withOpenAIWSAttemptIdentity(context.Background(), WSConnectionID("wsconn_invalid"), WSTurnID("wsturn_invalid")),
		c, account, "sk-fixture", payload, len(payload), "gpt-5", "gpt-5", "", "", "", "", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, openAINativeCompactionFailureSafeToReplay(err))
	require.Empty(t, writes, "invalid account-specific semantic frames must remain uncommitted")
	require.Zero(t, processBudget.Reserved())

	var terminal *UpstreamAttemptAttribution
	for _, row := range attemptRepo.snapshot() {
		if row.State == UpstreamAttemptStateTerminal {
			rowCopy := row
			terminal = &rowCopy
		}
	}
	require.NotNil(t, terminal)
	require.Equal(t, string(OpenAINativeCompactionZeroCompaction), terminal.Semantic.Outcome)
	require.Equal(t, int64(1), terminal.Semantic.OutputItemDoneCount)
	require.Zero(t, terminal.Semantic.CompactionItemCount)
	require.False(t, terminal.DeliveryCommitted)
	require.True(t, terminal.SafeToFailover)
	require.True(t, terminal.Usage.Observed, "failed upstream attempt usage remains attributable without customer settlement")
}

func TestOpenAIWSHTTPBridgeNativeCompactionRejectsMultipleDocumentsInOneSSEEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"fixture-state"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_bridge_joined","status":"completed"}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}}
	attemptRepo := &stubUpstreamAttemptAttributionRepository{}
	processBudget := NewOpenAIStageBudget(1 << 20)
	svc := &OpenAIGatewayService{
		cfg:                               &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:                      upstream,
		toolCorrector:                     NewCodexToolCorrector(),
		openAINativeCompactionStageBudget: processBudget,
		upstreamAttemptAttributionRepo:    attemptRepo,
	}
	account := &Account{
		ID:          76,
		Name:        "api-key-native-joined-event",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Credentials: map[string]any{"api_key": "sk-fixture", "base_url": "https://api.openai.com/v1"},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	payload := []byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":[{"type":"compaction_trigger"}]}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		withOpenAIWSAttemptIdentity(context.Background(), WSConnectionID("wsconn_joined"), WSTurnID("wsturn_joined")),
		c, account, "sk-fixture", payload, len(payload), "gpt-5", "gpt-5", "", "", "", "", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, openAINativeCompactionFailureSafeToReplay(err))
	require.Empty(t, writes)
	require.Zero(t, processBudget.Reserved())

	var terminal *UpstreamAttemptAttribution
	for _, row := range attemptRepo.snapshot() {
		if row.State == UpstreamAttemptStateTerminal {
			rowCopy := row
			terminal = &rowCopy
		}
	}
	require.NotNil(t, terminal)
	require.Equal(t, string(OpenAINativeCompactionInvalidEvent), terminal.Semantic.Outcome)
	require.False(t, terminal.DeliveryCommitted)
}

func TestOpenAIWSHTTPBridgeNativeCompactionClientDisconnectCancelsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &openAIWSHTTPBridgeBlockingUpstream{
		readStarted:  make(chan struct{}),
		readCanceled: make(chan struct{}),
	}
	attemptRepo := &stubUpstreamAttemptAttributionRepository{}
	processBudget := NewOpenAIStageBudget(1 << 20)
	svc := &OpenAIGatewayService{
		cfg:                               &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:                      upstream,
		openAINativeCompactionStageBudget: processBudget,
		upstreamAttemptAttributionRepo:    attemptRepo,
	}
	account := &Account{
		ID:          75,
		Name:        "api-key-native-disconnect",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Credentials: map[string]any{"api_key": "sk-fixture", "base_url": "https://api.openai.com/v1"},
	}
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		pumpCtx, stopPump := WithOpenAIWSClientReadPump(r.Context(), conn)
		defer stopPump()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		request := r.Clone(pumpCtx)
		request.Header = request.Header.Clone()
		request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
		c.Request = request
		_, bridgeErr := svc.proxyOpenAIWSHTTPBridgeTurn(
			withOpenAIWSAttemptIdentity(pumpCtx, WSConnectionID("wsconn_disconnect"), WSTurnID("wsturn_disconnect")),
			c, account, "sk-fixture", firstMessage, len(firstMessage), "gpt-5", "gpt-5", "", "", "", "", 1,
			func(message []byte) error {
				writeCtx, cancelWrite := context.WithTimeout(pumpCtx, time.Second)
				defer cancelWrite()
				return conn.Write(writeCtx, coderws.MessageText, message)
			},
		)
		serverErrCh <- bridgeErr
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5","stream":true,"input":[{"type":"compaction_trigger"}]}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case <-upstream.readStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("native HTTP bridge upstream read did not start")
	}
	disconnectAt := time.Now()
	require.NoError(t, clientConn.CloseNow())
	select {
	case <-upstream.readCanceled:
		require.Less(t, time.Since(disconnectAt), time.Second)
	case <-time.After(time.Second):
		t.Fatal("client disconnect did not cancel native HTTP bridge upstream read")
	}
	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
	case <-time.After(3 * time.Second):
		t.Fatal("native HTTP bridge did not finish after client disconnect")
	}

	var terminal *UpstreamAttemptAttribution
	for _, row := range attemptRepo.snapshot() {
		if row.State == UpstreamAttemptStateTerminal {
			rowCopy := row
			terminal = &rowCopy
		}
	}
	require.NotNil(t, terminal)
	require.Equal(t, string(OpenAINativeCompactionIncompleteStream), terminal.Semantic.Outcome)
	require.False(t, terminal.DeliveryCommitted)
	require.False(t, terminal.SafeToFailover)
	require.False(t, terminal.Usage.Observed)
	require.Zero(t, processBudget.Reserved())
}

func TestProxyOpenAIWSHTTPBridgeTurnForGrokDefaultsEmptyModelTo45(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_default","model":"grok-4.5"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_grok_default","model":"grok-4.5","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          72,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"base_url": xai.DefaultCLIBaseURL},
	}
	payload := []byte(`{"type":"response.create","generate":true,"stream":true,"input":"hi"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "access-token", payload, len(payload),
		"", "", "", "", "", "", 1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, grokDefaultResponsesModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Len(t, events, 2)
}

func TestOpenAIWSHTTPBridgeCapacityBeforeOutputReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	testCases := []struct {
		name string
		resp *http.Response
	}{
		{
			name: "http_503",
			resp: &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"x-request-id": []string{"rid_bridge_http_capacity"}},
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`,
				)),
			},
		},
		{
			name: "response_failed_after_added_item",
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_bridge_failed_capacity"}},
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"resp_bridge_failed_capacity"}}`,
					"",
					`data: {"type":"response.metadata","response_id":"resp_bridge_failed_capacity","headers":{"x-codex-turn-state":"turn-state"}}`,
					"",
					`data: {"type":"codex.response.metadata","headers":{"openai-model":"gpt-5.1"}}`,
					"",
					`data: {"type":"codex.rate_limits","rate_limits":[]}`,
					"",
					`data: {"type":"response.output_item.added","item":{"id":"msg_capacity","type":"message","role":"assistant","content":[{"type":"output_text","text":"seeded but uncommitted"}]}}`,
					"",
					`data: {"type":"response.failed","response":{"id":"resp_bridge_failed_capacity","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`,
					"",
				}, "\n"))),
			},
		},
		{
			name: "top_level_error_after_structural_preamble",
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_bridge_error_capacity"}},
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"resp_bridge_error_capacity"}}`,
					"",
					`data: {"type":"response.content_part.added","item_id":"msg_capacity","part":{"type":"output_text","text":""}}`,
					"",
					`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}`,
					"",
				}, "\n"))),
			},
		},
		{
			name: "failed_completed_with_event_name_only",
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_bridge_failed_completed"}},
				Body: io.NopCloser(strings.NewReader(strings.Join([]string{
					"event: response.created",
					`data: {"response":{"id":"resp_bridge_failed_completed"}}`,
					"",
					"event: response.output_item.done",
					`data: {"item":{"id":"fc_bridge_failed_completed","type":"function_call","call_id":"call_must_not_execute","name":"exec_command","arguments":"{}"}}`,
					"",
					"event: response.completed",
					`data: {"response":{"id":"resp_bridge_failed_completed","status":"failed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`,
					"",
				}, "\n"))),
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: tc.resp}
			svc := &OpenAIGatewayService{
				cfg: &config.Config{
					Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
					Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
				},
				httpUpstream:  upstream,
				toolCorrector: NewCodexToolCorrector(),
			}
			account := &Account{
				ID:          73,
				Name:        "openai-http-bridge-capacity",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test", "pool_mode": true},
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
			var messages [][]byte

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
				func(message []byte) error {
					messages = append(messages, append([]byte(nil), message...))
					return nil
				},
			)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.Empty(t, messages)
		})
	}
}

func TestOpenAIWSHTTPBridgeTransportErrorBeforeOutputReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{err: errors.New("dial failed")}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
		httpUpstream: upstream,
	}
	account := &Account{ID: 75, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
	var messages [][]byte

	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
		func(message []byte) error {
			messages = append(messages, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Empty(t, messages)
}

func TestOpenAIWSHTTPBridgeIncompleteStreamBeforeOutputReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	testCases := []struct {
		name string
		body func() io.ReadCloser
	}{
		{
			name: "scanner_error_after_preamble",
			body: func() io.ReadCloser {
				return &openAIWSHTTPBridgeErrTailReader{
					data: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bridge_scan_error\"}}\n\n"),
					err:  io.ErrUnexpectedEOF,
				}
			},
		},
		{
			name: "done_without_terminal_after_preamble",
			body: func() io.ReadCloser {
				return io.NopCloser(strings.NewReader(strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"resp_bridge_done_only"}}`,
					"",
					"data: [DONE]",
					"",
				}, "\n")))
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       tc.body(),
			}}
			svc := &OpenAIGatewayService{
				cfg: &config.Config{
					Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
					Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
				},
				httpUpstream: upstream,
			}
			account := &Account{ID: 75, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
			var messages [][]byte

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
				func(message []byte) error {
					messages = append(messages, append([]byte(nil), message...))
					return nil
				},
			)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, messages, "仅收到 structural preamble 时不得提交下游")
		})
	}
}

func TestOpenAIWSHTTPBridgeScannerErrorAfterOutputClosesWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge_partial"}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge_partial"},"delta":"partial"}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &openAIWSHTTPBridgeErrTailReader{data: []byte(body), err: io.ErrUnexpectedEOF},
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
		httpUpstream: upstream,
	}
	account := &Account{ID: 76, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
	var messages [][]byte

	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
		func(message []byte) error {
			messages = append(messages, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Len(t, messages, 2)
	require.Equal(t, "response.created", gjson.GetBytes(messages[0], "type").String())
	require.Equal(t, "partial", gjson.GetBytes(messages[1], "delta").String())
}

func TestOpenAIWSHTTPBridgeDoneWithoutTerminalAfterOutputReturnsRetryableClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge_done_after_output"}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge_done_after_output"},"delta":"partial"}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
		httpUpstream: upstream,
	}
	account := &Account{ID: 76, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
	var messages [][]byte

	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
		func(message []byte) error {
			messages = append(messages, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Len(t, messages, 2)
	require.Equal(t, "response.created", gjson.GetBytes(messages[0], "type").String())
	require.Equal(t, "partial", gjson.GetBytes(messages[1], "delta").String())
}

func TestOpenAIWSHTTPBridgeCapacityAfterDeltaIsSanitized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge_capacity_after_delta"}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge_capacity_after_delta"},"delta":"partial"}`,
		"",
		`data: {"type":"response.output_item.done","item":{"id":"fc_bridge_capacity","type":"function_call","call_id":"call_must_not_execute","name":"exec_command","arguments":"{\"cmd\":\"true\"}"}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_bridge_capacity_after_delta","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{ID: 74, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
	var messages [][]byte

	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
		func(message []byte) error {
			messages = append(messages, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Len(t, messages, 3)
	require.Equal(t, "response.created", gjson.GetBytes(messages[0], "type").String())
	require.Equal(t, "partial", gjson.GetBytes(messages[1], "delta").String())
	require.Equal(t, "upstream_error", gjson.GetBytes(messages[2], "response.error.code").String())
	for _, message := range messages {
		require.NotEqual(t, "response.output_item.done", gjson.GetBytes(message, "type").String())
		require.NotContains(t, string(message), "call_must_not_execute")
	}
	require.NotContains(t, string(messages[2]), "server_is_overloaded")
	require.NotContains(t, string(messages[2]), "Selected model is at capacity")
}

func TestOpenAIWSHTTPBridgeServerErrorAfterPreambleReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge_error","model":"gpt-5.1"}}`,
		"",
		`data: {"type":"error","error":{"type":"server_error","message":"temporary upstream error"}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}}
	account := &Account{
		ID:          72,
		Name:        "openai-http-bridge-error",
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
	repo := &openAIWSRateLimitSignalRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{*account}}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway:  config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
		accountRepo:      repo,
		rateLimitService: &RateLimitService{accountRepo: repo},
		httpUpstream:     upstream,
		toolCorrector:    NewCodexToolCorrector(),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"hi"}`)
	var messages [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "gpt-5.1", "", "", "", "", 1,
		func(message []byte) error {
			messages = append(messages, append([]byte(nil), message...))
			return nil
		},
	)

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, messages, "buffered response.created must remain hidden before failover")
	require.Empty(t, repo.tempCalls, "the first model-scoped transient failure must not persist an account-wide cooldown")
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.1"))
}

// TestProxyOpenAIWSHTTPBridgeTurnForGrokFreeFunctionToolsUsesMixedRoute 验证 Grok WS HTTP bridge
// 与原生 Responses 使用相同的 Free 函数工具缓存路由。
func TestProxyOpenAIWSHTTPBridgeTurnForGrokFreeFunctionToolsUsesMixedRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_tools","model":"grok-4.5"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_grok_tools","model":"grok-4.5","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          73,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"base_url":          xai.DefaultCLIBaseURL,
			"subscription_tier": "free",
		},
	}
	payload := []byte(`{
		"type":"response.create","generate":true,"stream":true,"input":"look up alpha",
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"function","name":"web_search","parameters":{"type":"object"}}
		],
		"tool_choice":"auto"
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "access-token", payload, len(payload),
		"", "", "", "", "", "cache-id", 1,
		func([]byte) error { return nil },
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
}

func TestProxyOpenAIWSHTTPBridgeTurnPromotesCodexAdditionalToolsForMixedCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_grok_codex_lite","model":"grok-4.5"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_grok_codex_lite","model":"grok-4.5","usage":{"input_tokens":4,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          73,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"base_url":          xai.DefaultCLIBaseURL,
			"subscription_tier": "free",
		},
	}
	payload := []byte(`{
		"type":"response.create","generate":true,"model":"grok","stream":true,
		"input":[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"function","name":"lookup","parameters":{"type":"object"}},
				{"type":"function","name":"web_search","parameters":{"type":"object"}},
				{"type":"custom","name":"apply_patch"},
				{"type":"namespace","name":"collaboration"}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set(grokClientToolCacheOptInHeader, "prefer-cache")
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "access-token", payload, len(payload),
		"grok", "grok", "", "", "", "isolated-ws-cache-id", 1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, events, 2)
	require.False(t, gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools")`).Exists())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="custom")`).Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "isolated-ws-cache-id", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "isolated-ws-cache-id", upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Empty(t, upstream.lastReq.Header.Get(grokClientToolCacheOptInHeader))
}

func TestProxyResponsesWebSocketFromClientForGrokUsesXAIHTTPBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	bridgeResponse := func(responseID, requestID string, cachedTokens int) *http.Response {
		sseBody := strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"` + responseID + `","model":"grok-4.3"}}`,
			"",
			`data: {"type":"response.output_text.delta","response":{"id":"` + responseID + `"},"delta":"ok"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"` + responseID + `","model":"grok-4.3","usage":{"input_tokens":4,"output_tokens":2,"input_tokens_details":{"cached_tokens":` + fmt.Sprintf("%d", cachedTokens) + `}}}}`,
			"",
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":   []string{"text/event-stream"},
				"Xai-Request-Id": []string{requestID},
			},
			Body: io.NopCloser(strings.NewReader(sseBody)),
		}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		bridgeResponse("resp_grok_ws_1", "xai-ws-req-1", 0),
		bridgeResponse("resp_grok_ws_2", "xai-ws-req-2", 3),
		bridgeResponse("resp_grok_ws_3", "xai-ws-req-3", 0),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
			},
		},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          71,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Status:      StatusActive,
		Credentials: map[string]any{
			"base_url": xai.DefaultCLIBaseURL,
		},
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText {
			errCh <- errors.New("first message was not text")
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		ginCtx.Request = req
		ginCtx.Set("api_key", &APIKey{ID: 7101})

		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "access-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","generate":true,"model":"grok","stream":true,"input":"hi","prompt_cache_retention":"24h"}`))
	cancelWrite()
	require.NoError(t, err)

	readEvent := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	created := readEvent()
	delta := readEvent()
	completed := readEvent()
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "resp_grok_ws_1", gjson.GetBytes(completed, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","generate":true,"model":"grok","stream":true,"previous_response_id":"resp_grok_ws_1","input":"second turn"}`))
	cancelWrite()
	require.NoError(t, err)

	created = readEvent()
	delta = readEvent()
	completed = readEvent()
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "resp_grok_ws_2", gjson.GetBytes(completed, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","generate":true,"model":"grok-4.3","stream":true,"previous_response_id":"resp_grok_ws_2","input":"third turn with a different model"}`))
	cancelWrite()
	require.NoError(t, err)

	created = readEvent()
	delta = readEvent()
	completed = readEvent()
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "resp_grok_ws_3", gjson.GetBytes(completed, "response.id").String())

	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(3 * time.Second):
		require.Fail(t, "proxy did not finish after client close")
	}

	require.Len(t, upstream.requests, 3)
	require.Len(t, upstream.bodies, 3)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, grokGatewayUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, grokCLIVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.bodies[1], "model").String())
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.bodies[2], "model").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String(), upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
	firstIdentity := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	secondIdentity := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	thirdIdentity := gjson.GetBytes(upstream.bodies[2], "prompt_cache_key").String()
	require.NotEmpty(t, firstIdentity)
	require.Equal(t, firstIdentity, secondIdentity)
	require.NotEmpty(t, thirdIdentity)
	require.NotEqual(t, firstIdentity, thirdIdentity)
	require.Equal(t, firstIdentity, upstream.requests[0].Header.Get(grokConversationIDHeader))
	require.Equal(t, secondIdentity, upstream.requests[1].Header.Get(grokConversationIDHeader))
	require.Equal(t, thirdIdentity, upstream.requests[2].Header.Get(grokConversationIDHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_retention").Exists())
}

func TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_large_bridge","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large_bridge","model":"gpt-5","usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_large_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
			OpenAIWS: config.GatewayOpenAIWSConfig{
				Enabled:                  true,
				APIKeyEnabled:            true,
				ResponsesWebsocketsV2:    true,
				ClientReadLimitBytes:     64 * 1024 * 1024,
				HTTPBridgeEnabled:        true,
				HTTPBridgeThresholdBytes: 15 * 1024 * 1024,
			},
		},
	}
	svc := &OpenAIGatewayService{
		cfg:           cfg,
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          9,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
		Concurrency: 1,
		Status:      StatusActive,
	}

	payload := []byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":true,"input":"` + strings.Repeat("x", 17*1024*1024) + `"}`)
	require.Greater(t, len(payload), 16*1024*1024)
	require.Less(t, int64(len(payload)), ResolveOpenAIWSClientReadLimitBytes(cfg))

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(ResolveOpenAIWSClientReadLimitBytes(cfg))

		readCtx, cancelRead := context.WithTimeout(r.Context(), 10*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		proxyCtx, cancelProxy := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancelProxy()
		errCh <- svc.ProxyResponsesWebSocketFromClient(proxyCtx, ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 20*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, payload)
	cancelWrite()
	require.NoError(t, err)

	var eventTypes []string
	for {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)

		eventType := gjson.GetBytes(event, "type").String()
		eventTypes = append(eventTypes, eventType)
		if eventType == "response.completed" {
			break
		}
	}
	require.Contains(t, eventTypes, "response.created")
	require.Contains(t, eventTypes, "response.completed")

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Greater(t, len(upstream.lastBody), 16*1024*1024)
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "gpt-5", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestOpenAIWSHTTPBridgeKeepsContinuationFramesOnHTTPWithoutPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_first","model":"gpt-5.1","output":[{"type":"function_call","id":"fc_bridge_1","call_id":"call_bridge_1","name":"shell","arguments":"{}"}],"usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	secondCapacitySSEBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge_second_capacity","model":"gpt-5.1"}}`,
		"",
		`data: {"type":"response.output_item.added","item":{"id":"msg_bridge_second_capacity","type":"message","role":"assistant","content":[]}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_bridge_second_capacity","status":"failed","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`,
		"",
	}, "\n")
	secondSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(firstSSEBody)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(secondCapacitySSEBody)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(secondSSEBody)),
		},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.RetryBackoffInitialMS = 1
	cfg.Gateway.OpenAIWS.RetryBackoffMaxMS = 1

	captureConn := &openAIWSCaptureConn{}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          19,
		Name:        "api-key-bridge-handoff",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
	}
	beforeRequestCalls := make(map[int]int)
	beforeTurnCalls := make(map[int]int)
	afterTurnCalls := make(map[int]int)
	hooks := &OpenAIWSIngressHooks{
		BeforeRequest: func(turn int, payload []byte, _, _ string) ([]byte, error) {
			beforeRequestCalls[turn]++
			return payload, nil
		},
		BeforeTurn: func(turn int) error {
			beforeTurnCalls[turn]++
			return nil
		},
		AfterTurn: func(capture OpenAIWSTurnCapture) {
			afterTurnCalls[capture.Turn]++
		},
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeMessage := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelWrite()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		msgType, event, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"first"}`)
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_first", gjson.GetBytes(firstTurnEvent, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_bridge_first","input":[{"type":"function_call_output","call_id":"call_bridge_1","output":"ok"}]}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_second", gjson.GetBytes(secondTurnEvent, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.Len(t, upstream.bodies, 3, "第二轮 capacity 应只在 HTTP bridge 内重试当前 turn")
	require.False(t, gjson.GetBytes(upstream.bodies[0], "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists())
	require.JSONEq(t, string(upstream.bodies[1]), string(upstream.bodies[2]), "重试不得改写或重放第一轮 payload")
	secondInput := gjson.GetBytes(upstream.bodies[1], "input").Array()
	require.Len(t, secondInput, 3)
	require.Equal(t, "first", secondInput[0].String())
	require.Equal(t, "function_call", secondInput[1].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[1].Get("call_id").String())
	require.Equal(t, "function_call_output", secondInput[2].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[2].Get("call_id").String())
	require.Equal(t, map[int]int{2: 1}, beforeRequestCalls)
	require.Equal(t, map[int]int{1: 1, 2: 1}, beforeTurnCalls)
	require.Equal(t, map[int]int{1: 1, 2: 1}, afterTurnCalls)
	require.Equal(t, 0, captureDialer.DialCount())
	require.Empty(t, captureConn.writes)
}

func TestOpenAIWSHTTPBridge_IdleTimeoutClosesClientSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_idle","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sseBody)),
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          20,
		Name:        "api-key-bridge-idle-timeout",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"input":"hello"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	closeReadCtx, cancelCloseRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(closeReadCtx)
	cancelCloseRead()
	var clientClose coderws.CloseError
	require.ErrorAs(t, err, &clientClose)
	require.Equal(t, coderws.StatusNormalClosure, clientClose.Code)
	require.Equal(t, "websocket idle timeout", clientClose.Reason)

	select {
	case proxyErr := <-errCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for idle HTTP bridge session to close")
	}
	require.Len(t, upstream.bodies, 1, "an idle client must not leave a continuation request running")
}
