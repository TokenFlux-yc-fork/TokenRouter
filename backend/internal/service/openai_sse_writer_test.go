package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type flushErrorResponseWriter struct {
	*httptest.ResponseRecorder
	err        error
	flushCalls atomic.Int32
}

// Mirrors middleware adapters such as opsCaptureWriter that sit outside Gin's
// writer and must expose the next layer for FlushError discovery.
type unwrappingGinWriter struct {
	gin.ResponseWriter
}

func (w *unwrappingGinWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *flushErrorResponseWriter) FlushError() error {
	w.flushCalls.Add(1)
	return w.err
}

func newFlushErrorContext(t *testing.T, flushErr error) (*gin.Context, *flushErrorResponseWriter) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	raw := &flushErrorResponseWriter{
		ResponseRecorder: httptest.NewRecorder(),
		err:              flushErr,
	}
	c, _ := gin.CreateTestContext(raw)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, raw
}

func TestWriteOpenAICompactSSEBridge_PropagatesDownstreamWriteError(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	c.Writer = &failingGinWriter{ResponseWriter: c.Writer, failAfter: 0}
	finalResponse := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}]}`)

	handled, err := writeOpenAICompactSSEBridge(c, http.StatusOK, finalResponse)

	require.True(t, handled)
	require.ErrorContains(t, err, "write compact SSE response")
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "downstream_write_error", streamErr.ErrType)
}

func TestWriteOpenAICompactSSEBridge_PropagatesFlushErrorThroughGinWriter(t *testing.T) {
	flushErr := errors.New("flush failed")
	c, raw := newFlushErrorContext(t, flushErr)
	c.Writer = &unwrappingGinWriter{ResponseWriter: c.Writer}
	MarkOpenAICompactClientStream(c)
	finalResponse := []byte(`{"id":"resp_1","output":[{"type":"compaction","encrypted_content":"x"}]}`)

	handled, err := writeOpenAICompactSSEBridge(c, http.StatusOK, finalResponse)

	require.True(t, handled)
	require.ErrorContains(t, err, "flush compact SSE response")
	require.ErrorIs(t, err, flushErr)
	require.Equal(t, int32(1), raw.flushCalls.Load(), "must reach FlushError below Gin's error-less Flush")
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "downstream_flush_error", streamErr.ErrType)
}

func TestOpenAICompactFlushErrorPreservesUsageAndMarksClientDisconnect(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "normal"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			flushErr := errors.New(name + " compact flush failed")
			c, raw := newFlushErrorContext(t, flushErr)
			MarkOpenAICompactClientStream(c)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
					"id":"resp_compact_disconnect",
					"output":[{"type":"compaction","encrypted_content":"x"}],
					"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3}}
				}`)),
			}
			svc := newCompactBridgeTestService()

			var usage *OpenAIUsage
			var clientDisconnect bool
			if passthrough {
				result, err := svc.handleNonStreamingResponsePassthrough(c.Request.Context(), resp, c, "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
				clientDisconnect = result.clientDisconnect
			} else {
				result, err := svc.handleNonStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
				clientDisconnect = result.clientDisconnect
			}

			require.True(t, clientDisconnect)
			require.NotNil(t, usage)
			require.Equal(t, 11, usage.InputTokens)
			require.Equal(t, 7, usage.OutputTokens)
			require.Equal(t, 3, usage.CacheReadInputTokens)
			streamErr, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, "downstream_flush_error", streamErr.ErrType)
			require.Equal(t, flushErr.Error(), streamErr.Message)
			require.Equal(t, int32(1), raw.flushCalls.Load())
		})
	}
}

func TestOpenAICompactFlushErrorReturnsSuccessfulForwardResult(t *testing.T) {
	flushErr := errors.New("compact client disconnected")
	c, raw := newFlushErrorContext(t, flushErr)
	c.Request.URL.Path = "/v1/responses/compact"
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	MarkOpenAICompactClientStream(c)
	body := []byte(`{"model":"gpt-5.2","instructions":"compact","stream":false,"input":[]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_compact_forward","output":[{"type":"compaction","encrypted_content":"x"}],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          123,
		Name:        "compact-account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
		Extra:       map[string]any{"openai_passthrough": true},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 13, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.Equal(t, int32(1), raw.flushCalls.Load())
}

func TestOpenAICompactSSEKeepalive_StopsAndRecordsFlushError(t *testing.T) {
	flushErr := errors.New("keepalive flush failed")
	c, raw := newFlushErrorContext(t, flushErr)
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()

	require.Eventually(t, func() bool {
		return raw.flushCalls.Load() > 0
	}, time.Second, time.Millisecond)
	stop()
	flushCalls := raw.flushCalls.Load()
	time.Sleep(3 * keepaliveTestInterval)

	require.Equal(t, int32(1), flushCalls)
	require.Equal(t, flushCalls, raw.flushCalls.Load(), "flush failure must stop later keepalives")
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "downstream_flush_error", streamErr.ErrType)
	require.Equal(t, flushErr.Error(), streamErr.Message)
}

func TestOpenAIStreamingFlushErrorIsRecordedAndStillDrainsUsage(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "normal"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			flushErr := errors.New(name + " flush failed")
			c, raw := newFlushErrorContext(t, flushErr)
			upstreamSSE := strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"hello"}`,
				"",
				`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3}}}}`,
				"",
			}, "\n")
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
			}
			svc := &OpenAIGatewayService{
				cfg:           &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
				toolCorrector: NewCodexToolCorrector(),
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI}

			var usage *OpenAIUsage
			var clientDisconnect bool
			if passthrough {
				result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
				clientDisconnect = result.clientDisconnect
			} else {
				result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
				clientDisconnect = result.clientDisconnect
			}

			require.True(t, clientDisconnect)
			require.NotNil(t, usage)
			require.Equal(t, 11, usage.InputTokens)
			require.Equal(t, 7, usage.OutputTokens)
			require.Equal(t, 3, usage.CacheReadInputTokens)
			require.Equal(t, int32(1), raw.flushCalls.Load())
			streamErr, ok := GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, "downstream_flush_error", streamErr.ErrType)
			require.Equal(t, flushErr.Error(), streamErr.Message)
		})
	}
}
