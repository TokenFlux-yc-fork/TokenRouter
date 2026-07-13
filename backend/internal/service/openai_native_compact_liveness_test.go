package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAINativeCompactErrorReader struct {
	reader *strings.Reader
	err    error
}

func (r *openAINativeCompactErrorReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF {
		return 0, r.err
	}
	return n, err
}

func parseNativeRemoteCompactionEventSource(body string) [][2]string {
	events := make([][2]string, 0)
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		eventType := "message"
		dataLines := make([]string, 0, 1)
		for _, line := range strings.Split(block, "\n") {
			if event, ok := extractOpenAISSEEventLine(line); ok {
				eventType = event
				continue
			}
			if data, ok := extractOpenAISSEDataLine(line); ok {
				dataLines = append(dataLines, data)
			}
		}
		// EventSource discards a block with no data lines. This is how a blank
		// line safely terminates an upstream frame that disconnected halfway.
		if len(dataLines) == 0 {
			continue
		}
		events = append(events, [2]string{eventType, strings.Join(dataLines, "\n")})
	}
	return events
}

func requireParsedNativeRemoteCompactionPing(t *testing.T, body string) [][2]string {
	t.Helper()
	events := parseNativeRemoteCompactionEventSource(body)
	pingCount := 0
	for _, event := range events {
		if event[0] != "ping" {
			continue
		}
		pingCount++
		require.True(t, gjson.Valid(event[1]), "ping data must be valid JSON")
		require.Equal(t, "ping", gjson.Get(event[1], "type").String())
	}
	require.Positive(t, pingCount, "stream must contain a parsed ping event")
	return events
}

func TestOpenAINativeRemoteCompactionPingKeepsFailoverAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Gateway: config.GatewayConfig{
				StreamKeepaliveInterval: 1,
				MaxLineSize:             defaultMaxLineSize,
			}}
			svc := &OpenAIGatewayService{cfg: cfg}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)

			pr, pw := io.Pipe()
			defer func() { _ = pr.Close() }()
			resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}
			go func() {
				defer func() { _ = pw.Close() }()
				_, _ = io.WriteString(pw, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_compact\"}}\n\n")
				time.Sleep(1200 * time.Millisecond)
				_, _ = io.WriteString(pw, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_compact\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n")
			}()

			account := &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Contains(t, string(failoverErr.ResponseBody), "Selected model is at capacity")
			events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
			require.Len(t, events, 1)
			require.NotContains(t, rec.Body.String(), "response.created")
			require.NotContains(t, rec.Body.String(), "response.failed")
			require.Equal(t, -1, OpenAISemanticWrittenSize(c))
		})
	}
}

func TestOpenAINativeRemoteCompactionHTTPErrorAfterPingEmitsFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			_, writeErr := writeOpenAINativeRemoteCompactionPing(c, c.Writer)
			require.NoError(t, writeErr)
			require.NoError(t, flushOpenAIResponseWriter(c.Writer))

			resp := &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid compact request"}}`)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
			}
			account := &Account{ID: 1, Name: "openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			svc := &OpenAIGatewayService{}

			var err error
			if passthrough {
				err = svc.handleErrorResponsePassthrough(
					context.Background(), resp, c, account,
					[]byte(`{"model":"gpt-5"}`),
					[]byte(`{"error":{"message":"invalid compact request"}}`),
				)
			} else {
				_, err = svc.handleErrorResponse(context.Background(), resp, c, account, []byte(`{"model":"gpt-5"}`))
			}

			require.Error(t, err)
			require.Equal(t, http.StatusOK, rec.Code)
			events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
			require.Len(t, events, 2)
			require.Equal(t, "response.failed", events[1][0])
			require.Equal(t, "response.failed", gjson.Get(events[1][1], "type").String())
			require.NotContains(t, rec.Body.String(), `"error":{"message":"invalid compact request"}`)
			require.Equal(t, "failed", gjson.Get(events[1][1], "response.status").String())
		})
	}
}

func TestOpenAINativeRemoteCompactionPingDoesNotSplitUpstreamFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Gateway: config.GatewayConfig{
				StreamKeepaliveInterval: 1,
				MaxLineSize:             defaultMaxLineSize,
			}}
			svc := &OpenAIGatewayService{cfg: cfg}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)

			pr, pw := io.Pipe()
			defer func() { _ = pr.Close() }()
			resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}
			go func() {
				defer func() { _ = pw.Close() }()
				_, _ = io.WriteString(pw, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n")
				_, _ = io.WriteString(pw, "event: response.output_text.delta\n")
				time.Sleep(1200 * time.Millisecond)
				_, _ = io.WriteString(pw, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"second\"}\n\n")
				time.Sleep(2100 * time.Millisecond)
				_, _ = io.WriteString(pw, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}()

			account := &Account{ID: 1}
			var result any
			var err error
			if passthrough {
				result, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				result, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}
			require.NoError(t, err)
			require.NotNil(t, result)

			events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
			secondDeltaIndex := -1
			pingIndex := -1
			for i, event := range events {
				if event[0] == "response.output_text.delta" && gjson.Get(event[1], "delta").String() == "second" {
					secondDeltaIndex = i
				}
				if event[0] == "ping" && pingIndex < 0 {
					pingIndex = i
				}
			}
			require.Greater(t, secondDeltaIndex, 0)
			require.Greater(t, pingIndex, secondDeltaIndex, "ping must follow the complete second delta frame")
		})
	}
}

func TestOpenAINativeRemoteCompactionProtocolErrorAfterPingUsesFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             64 * 1024,
	}}
	svc := &OpenAIGatewayService{cfg: cfg}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}
	go func() {
		defer func() { _ = pw.Close() }()
		time.Sleep(1200 * time.Millisecond)
		_, _ = io.WriteString(pw, strings.Repeat("x", 70*1024)+"\n")
	}()

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	require.Error(t, err)
	events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
	require.Len(t, events, 2)
	require.Equal(t, "response.failed", events[1][0])
	require.Equal(t, "response.failed", gjson.Get(events[1][1], "type").String())
}

func TestOpenAINativeRemoteCompactionHalfFrameReadErrorClosesFrameBeforeFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamErr := errors.New("upstream connection reset")
	upstreamBody := strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"first"}`,
		"",
		"event: response.output_text.delta",
	}, "\n")

	for _, passthrough := range []bool{false, true} {
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(&openAINativeCompactErrorReader{
					reader: strings.NewReader(upstreamBody),
					err:    upstreamErr,
				}),
				Header: http.Header{},
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}

			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)

			body := rec.Body.String()
			require.Contains(t, body, "event: response.output_text.delta\n\nevent: response.failed")
			events := parseNativeRemoteCompactionEventSource(body)
			require.Len(t, events, 2)
			require.Equal(t, "response.output_text.delta", events[0][0])
			require.Equal(t, "first", gjson.Get(events[0][1], "delta").String())
			require.Equal(t, "response.failed", events[1][0])
			require.Equal(t, "response.failed", gjson.Get(events[1][1], "type").String())
		})
	}
}

func TestOpenAINativeRemoteCompactionCleanEOFAfterOutputEmitsFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamBody := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"

	for _, passthrough := range []bool{false, true} {
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
				Header:     http.Header{},
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}

			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
			}
			require.ErrorContains(t, err, "missing terminal event")
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)

			events := parseNativeRemoteCompactionEventSource(rec.Body.String())
			require.Len(t, events, 2)
			require.Equal(t, "response.output_text.delta", events[0][0])
			require.Equal(t, "partial", gjson.Get(events[0][1], "delta").String())
			require.Equal(t, "response.failed", events[1][0])
			require.Equal(t, "response.failed", gjson.Get(events[1][1], "type").String())
		})
	}
}

func TestOpenAINativeRemoteCompactionFailureWriterErrorIsReturned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	_, err := writeOpenAINativeRemoteCompactionPing(c, c.Writer)
	require.NoError(t, err)
	require.NoError(t, flushOpenAIResponseWriter(c.Writer))

	c.Writer = &failingGinWriter{ResponseWriter: c.Writer, failAfter: 0}
	handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, http.StatusBadGateway, "upstream_error", "failed")
	require.True(t, handled)
	require.ErrorContains(t, writeErr, "write failed")
}
