package service

import (
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
			if passthrough {
				result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
			} else {
				result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				require.NoError(t, err)
				require.NotNil(t, result)
				usage = result.usage
			}

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
