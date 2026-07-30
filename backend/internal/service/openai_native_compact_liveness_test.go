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
	"github.com/tidwall/gjson"
)

type openAINativeCompactErrorReader struct {
	reader *strings.Reader
	err    error
}

type openAINativeCompactPartialWriter struct {
	gin.ResponseWriter
	limit int
}

type openAINativeCompactDeadlineWriter struct {
	*httptest.ResponseRecorder
	writeAttempts atomic.Int32
	deadlines     atomic.Int32
}

type openAINativeCompactCancelBody struct {
	ctx     context.Context
	started chan struct{}
	once    atomic.Bool
}

func (b *openAINativeCompactCancelBody) Read([]byte) (int, error) {
	if b.once.CompareAndSwap(false, true) {
		close(b.started)
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *openAINativeCompactCancelBody) Close() error { return nil }

func (w *openAINativeCompactDeadlineWriter) Write([]byte) (int, error) {
	w.writeAttempts.Add(1)
	return 0, errors.New("synthetic ping write failure")
}

func (w *openAINativeCompactDeadlineWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func (w *openAINativeCompactDeadlineWriter) SetWriteDeadline(time.Time) error {
	w.deadlines.Add(1)
	return nil
}

type openAINativeCompactUnwrappingWriter struct {
	gin.ResponseWriter
	raw http.ResponseWriter
}

func (w *openAINativeCompactUnwrappingWriter) Unwrap() http.ResponseWriter { return w.raw }

func (w *openAINativeCompactPartialWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		n, _ := w.ResponseWriter.Write(p[:w.limit])
		return n, errors.New("partial client write")
	}
	return w.ResponseWriter.Write(p)
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
			resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"attempt-must-stay-private"},
			}}
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
			require.Contains(t, string(failoverErr.ResponseBody), "failed_terminal")
			require.NotContains(t, string(failoverErr.ResponseBody), "Selected model is at capacity")
			events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
			require.Len(t, events, 1)
			require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
			require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
			require.Equal(t, "keep-alive", rec.Header().Get("Connection"))
			require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
			require.Empty(t, rec.Header().Get("X-Request-Id"), "attempt-private headers must not commit on semantic failure")
			require.NotContains(t, rec.Body.String(), "response.created")
			require.NotContains(t, rec.Body.String(), "response.failed")
			require.Equal(t, -1, OpenAISemanticWrittenSize(c))
		})
	}
}

func TestOpenAINativeRemoteCompactionSSEContinuationFailureIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	events := map[string]string{
		"error": strings.Join([]string{
			"event: error",
			`data: {"type":"error","error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"fixture-sensitive-echo"}}`,
			"",
		}, "\n"),
		"response_failed": strings.Join([]string{
			"event: response.failed",
			`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"previous_response_not_found","type":"invalid_request_error","message":"fixture-sensitive-echo"}}}`,
			"",
		}, "\n"),
	}
	for eventName, upstreamBody := range events {
		for _, passthrough := range []bool{false, true} {
			name := eventName + "_main"
			if passthrough {
				name = eventName + "_passthrough"
			}
			t.Run(name, func(t *testing.T) {
				cfg := nativeCompactionHTTPTestConfig()
				cfg.Gateway.LogUpstreamErrorBody = true
				svc := &OpenAIGatewayService{cfg: cfg}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				MarkOpenAINativeRemoteCompactionV2(c)
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"X-Request-Id": []string{"attempt-private"}},
					Body:       io.NopCloser(strings.NewReader(upstreamBody)),
				}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}

				var validation OpenAINativeCompactionValidationResult
				var err error
				if passthrough {
					result, resultErr := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					err = resultErr
					require.NotNil(t, result)
					validation = result.nativeValidation
				} else {
					result, resultErr := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
					err = resultErr
					require.NotNil(t, result)
					validation = result.nativeValidation
				}

				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.True(t, failoverErr.SafeToFailoverAfterWrite)
				require.Empty(t, failoverErr.ResponseBody)
				require.NotContains(t, err.Error(), "fixture-sensitive-echo")
				require.Equal(t, OpenAINativeCompactionHTTPFailure, validation.Outcome)
				require.Empty(t, rec.Body.String())
				require.Empty(t, rec.Header().Get("X-Request-Id"))
				require.Equal(t, -1, OpenAISemanticWrittenSize(c))
			})
		}
	}
}

func TestOpenAINativeRemoteCompactionPingSetsDeadlineAndStopsAfterFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := nativeCompactionHTTPTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	svc := &OpenAIGatewayService{cfg: cfg}
	raw := &openAINativeCompactDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(raw)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	c.Writer = &openAINativeCompactUnwrappingWriter{ResponseWriter: c.Writer, raw: raw}
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}
	go func() {
		time.Sleep(2500 * time.Millisecond)
		_ = pw.Close()
	}()

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, int32(1), raw.writeAttempts.Load(), "failed keepalive must stop later pings")
	require.GreaterOrEqual(t, raw.deadlines.Load(), int32(2), "ping sets and clears its write deadline")
}

func TestOpenAINativeRemoteCompactionSemanticOutputDoesNotDisableFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
			}}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)

			upstreamBody := strings.Join([]string{
				`event: response.output_text.delta`,
				`data: {"type":"response.output_text.delta","delta":"must-not-leak"}`,
				``,
				`event: response.failed`,
				`data: {"type":"response.failed","response":{"id":"resp_semantic_then_capacity","error":{"code":"server_is_overloaded","message":"Selected model is at capacity. Please try a different model."}}}`,
				``,
			}, "\n")
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
				Header:     http.Header{"X-Request-Id": []string{"attempt-must-not-commit"}},
			}

			account := &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}
			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.NotContains(t, rec.Body.String(), "must-not-leak")
			require.NotContains(t, rec.Body.String(), "response.failed")
			require.Equal(t, "", rec.Header().Get("X-Request-Id"))
			require.Equal(t, -1, OpenAISemanticWrittenSize(c))
		})
	}
}

func nativeCompactionHTTPTestConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize: defaultMaxLineSize,
		OpenAINativeCompaction: config.GatewayOpenAINativeCompactionConfig{
			MemoryThresholdBytes:      1024,
			MaxAttemptBytes:           1 << 20,
			MaxAttemptEvents:          128,
			MaxAttemptDurationSeconds: 30,
			MaxProcessStagedBytes:     4 << 20,
			MaxRequestCumulativeBytes: 4 << 20,
		},
	}}
}

func TestOpenAINativeRemoteCompactionWholeAttemptContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_valid"}}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_valid","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		``,
	}, "\n")
	cases := []struct {
		name     string
		body     string
		wantFail bool
	}{
		{name: "valid exactly one", body: valid},
		{name: "zero", body: strings.Replace(valid, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"ciphertext\"}}\n\n", "", 1), wantFail: true},
		{name: "multiple", body: strings.Replace(valid, "event: response.completed", "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"second\"}}\n\nevent: response.completed", 1), wantFail: true},
		{name: "malformed", body: strings.Replace(valid, `"encrypted_content":"ciphertext"`, `"encrypted_content":""`, 1), wantFail: true},
		{name: "incomplete eof", body: strings.Replace(valid, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_valid\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n", "", 1), wantFail: true},
	}

	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		pathName := "main"
		if passthrough {
			pathName = "passthrough"
		}
		for _, tc := range cases {
			tc := tc
			t.Run(pathName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				cfg := nativeCompactionHTTPTestConfig()
				svc := &OpenAIGatewayService{
					cfg:                               cfg,
					openAINativeCompactionStageBudget: NewOpenAIStageBudget(cfg.Gateway.OpenAINativeCompaction.MaxProcessStagedBytes),
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				requestBudget := NewOpenAIStageBudget(cfg.Gateway.OpenAINativeCompaction.MaxRequestCumulativeBytes)
				c.Request = c.Request.WithContext(WithOpenAINativeCompactionRequestStageBudget(c.Request.Context(), requestBudget))
				MarkOpenAINativeRemoteCompactionV2(c)
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header: http.Header{
						"X-Request-Id":                 []string{"attempt-contract"},
						"X-Codex-Primary-Used-Percent": []string{"17"},
					},
				}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				} else {
					_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
				}
				if tc.wantFail {
					var failoverErr *UpstreamFailoverError
					require.ErrorAs(t, err, &failoverErr)
					require.Empty(t, rec.Body.String())
					require.Empty(t, rec.Header().Get("X-Request-Id"))
					require.Empty(t, rec.Header().Get("X-Codex-Primary-Used-Percent"))
					require.Equal(t, -1, OpenAISemanticWrittenSize(c))
					return
				}
				require.NoError(t, err)
				require.Equal(t, tc.body, rec.Body.String())
				if passthrough {
					require.Equal(t, "attempt-contract", rec.Header().Get("X-Request-Id"))
					require.Equal(t, "17", rec.Header().Get("X-Codex-Primary-Used-Percent"))
				} else {
					require.Equal(t, "attempt-contract", rec.Header().Get("X-Request-Id"))
					require.Empty(t, rec.Header().Get("X-Codex-Primary-Used-Percent"))
				}
				require.NotEqual(t, -1, OpenAISemanticWrittenSize(c))
			})
		}
	}
}

func TestOpenAINativeCompactionAttemptHeadersWriteAllowedValuesOnce(t *testing.T) {
	filter := compileResponseHeaderFilter(&config.Config{})
	src := http.Header{
		"X-Request-Id":                 []string{"request-one", "request-two"},
		"X-Ratelimit-Limit-Requests":   []string{"100"},
		"X-Codex-Primary-Used-Percent": []string{"17"},
		"X-Blocked":                    []string{"must-not-pass"},
	}

	headers := openAINativeCompactionAttemptHeaders(src, filter, true)
	require.Equal(t, []string{"request-one", "request-two"}, headers.Values("X-Request-Id"))
	require.Equal(t, []string{"100"}, headers.Values("X-Ratelimit-Limit-Requests"))
	require.Equal(t, []string{"17"}, headers.Values("X-Codex-Primary-Used-Percent"))
	require.Empty(t, headers.Values("X-Blocked"))
}

func TestOpenAINativeRemoteCompactionMultiDataFrameUsesCombinedValidatorPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done",`,
		`data: "item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		`: preserved-comment`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed",`,
		`data: "response":{"id":"resp_multi_data","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
		``,
	}, "\n")

	for _, passthrough := range []bool{false, true} {
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}

			var usage *OpenAIUsage
			var err error
			if passthrough {
				result, handleErr := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
				err = handleErr
				if result != nil {
					usage = result.usage
				}
			} else {
				result, handleErr := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
				err = handleErr
				if result != nil {
					usage = result.usage
				}
			}
			require.NoError(t, err)
			require.NotNil(t, usage)
			events := parseNativeRemoteCompactionEventSource(rec.Body.String())
			require.Len(t, events, 2)
			require.Equal(t, "response.output_item.done", events[0][0])
			require.True(t, gjson.Valid(events[0][1]))
			require.Equal(t, "ciphertext", gjson.Get(events[0][1], "item.encrypted_content").String())
			require.Equal(t, "response.completed", events[1][0])
			require.Equal(t, 3, usage.InputTokens)
			require.Contains(t, rec.Body.String(), ": preserved-comment")
		})
	}
}

func TestOpenAINativeRemoteCompactionPreservesOriginalSSEWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		": upstream-comment\r\n",
		"\r\n",
		"event: response.output_item.done\r\n",
		"data: {\"type\":\"response.output_item.done\",\r\n",
		"data: \"item\":{\"type\":\"compaction\",\"encrypted_content\":\"ciphertext\"}}\r\n",
		"\r\n",
		"event: response.completed\r\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_wire\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\r\n",
		"\r\n",
	}, "")

	for _, passthrough := range []bool{false, true} {
		name := "main"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}

			var err error
			if passthrough {
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "client-model", "upstream-model")
			} else {
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "client-model", "upstream-model")
			}

			require.NoError(t, err)
			require.Equal(t, body, recorder.Body.String())
		})
	}
}

func TestOpenAINativeRemoteCompactionRejectsConcatenatedJSONWithoutWireRepair(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := "data: " +
		`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}` +
		`{"type":"response.completed","response":{"id":"resp_joined","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"
	svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.NotNil(t, result)
	require.Equal(t, OpenAINativeCompactionInvalidEvent, result.nativeValidation.Outcome)
	require.Empty(t, recorder.Body.String())
}

func TestOpenAINativeRemoteCompactionRejectsEventHeaderTypeInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		"event: response.output_item.done",
		`data: {"item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_event_header","status":"completed"}}`,
		"",
	}, "\n")
	svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.NotNil(t, result)
	require.Equal(t, OpenAINativeCompactionInvalidEvent, result.nativeValidation.Outcome)
	require.Empty(t, recorder.Body.String())
}

func TestOpenAINativeRemoteCompactionRejectsOversizedUnterminatedFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, linePrefix := range []string{":", "data: "} {
		linePrefix := linePrefix
		t.Run(strings.TrimSpace(strings.TrimSuffix(linePrefix, ":")), func(t *testing.T) {
			cfg := nativeCompactionHTTPTestConfig()
			cfg.Gateway.OpenAINativeCompaction.MaxAttemptBytes = 64
			svc := &OpenAIGatewayService{cfg: cfg}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			MarkOpenAINativeRemoteCompactionV2(c)
			body := "event: response.output_item.done\n" + linePrefix + strings.Repeat("x", 96)
			resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}

			_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Contains(t, failoverErr.Error(), "failover")
			require.Empty(t, rec.Body.String())
		})
	}
}

func TestOpenAINativeRemoteCompactionSlowUnterminatedFrameHonorsAttemptDuration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := nativeCompactionHTTPTestConfig()
	cfg.Gateway.OpenAINativeCompaction.MaxAttemptDurationSeconds = 1
	svc := &OpenAIGatewayService{cfg: cfg}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()
	resp := &http.Response{StatusCode: http.StatusOK, Body: pr, Header: http.Header{}}
	go func() {
		_, _ = io.WriteString(pw, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\"")
	}()

	started := time.Now()
	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, started, "model", "model")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Less(t, time.Since(started), 3*time.Second)
	require.Empty(t, rec.Body.String())
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

func TestOpenAINativeRemoteCompactionIncompleteContractDoesNotCommitUpstreamFrames(t *testing.T) {
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
				_, _ = io.WriteString(pw, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_done\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
			}()

			account := &Account{ID: 1}
			var result any
			var err error
			if passthrough {
				result, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			} else {
				result, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "model", "model")
			}
			require.NotNil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
			for _, event := range events {
				require.Equal(t, "ping", event[0])
			}
			require.NotContains(t, rec.Body.String(), "first")
			require.NotContains(t, rec.Body.String(), "second")
		})
	}
}

func TestOpenAINativeRemoteCompactionProtocolErrorAfterPingDiscardsAttempt(t *testing.T) {
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
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	events := requireParsedNativeRemoteCompactionPing(t, rec.Body.String())
	for _, event := range events {
		require.Equal(t, "ping", event[0])
	}
	require.NotContains(t, rec.Body.String(), "response.failed")
}

func TestOpenAINativeRemoteCompactionHalfFrameReadErrorDoesNotLeak(t *testing.T) {
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
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, rec.Body.String())
			require.Equal(t, -1, OpenAISemanticWrittenSize(c))
		})
	}
}

func TestOpenAINativeRemoteCompactionCleanEOFAfterOutputDoesNotLeak(t *testing.T) {
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
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, rec.Body.String())
			require.Equal(t, -1, OpenAISemanticWrittenSize(c))
		})
	}
}

func TestOpenAINativeRemoteCompactionCommitFailureReturnsUsageForSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_settle","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":4,"total_tokens":11}}}`,
		``,
	}, "\n")
	svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	c.Writer = &failingGinWriter{ResponseWriter: c.Writer, failAfter: 0}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(valid)), Header: http.Header{}}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.False(t, IsResponseCommitted(c), "a zero-byte failure must leave fallback available")
	require.Equal(t, 7, result.usage.InputTokens)
	require.Equal(t, 4, result.usage.OutputTokens)
	require.Equal(t, "resp_settle", result.responseID)
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "downstream_write_error", streamErr.ErrType)
}

func TestOpenAINativeRemoteCompactionClientCancellationDiscardsStageImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := nativeCompactionHTTPTestConfig()
	processBudget := NewOpenAIStageBudget(cfg.Gateway.OpenAINativeCompaction.MaxProcessStagedBytes)
	svc := &OpenAIGatewayService{cfg: cfg, openAINativeCompactionStageBudget: processBudget}
	requestCtx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	MarkOpenAINativeRemoteCompactionV2(c)
	body := &openAINativeCompactCancelBody{ctx: requestCtx, started: make(chan struct{})}
	resp := &http.Response{StatusCode: http.StatusOK, Body: body, Header: http.Header{}}

	type outcome struct {
		result *openaiStreamingResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := svc.handleStreamingResponse(requestCtx, resp, c, &Account{ID: 1}, time.Now(), "model", "model")
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("native compaction upstream read did not start")
	}
	cancel()

	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.Canceled)
		require.NotNil(t, got.result)
		require.True(t, got.result.clientDisconnect)
	case <-time.After(time.Second):
		t.Fatal("native compaction cancellation did not stop the upstream read")
	}
	require.Zero(t, processBudget.Reserved())
	require.Empty(t, rec.Body.String())
	require.Equal(t, -1, OpenAISemanticWrittenSize(c))
}

func TestOpenAINativeRemoteCompactionPartialCommitPreventsFailoverWithoutCommittingDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_partial","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":4,"total_tokens":11}}}`,
		``,
	}, "\n")
	svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	MarkOpenAINativeRemoteCompactionV2(c)
	c.Writer = &openAINativeCompactPartialWriter{ResponseWriter: c.Writer, limit: 16}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(valid)), Header: http.Header{}}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.True(t, IsResponseCommitted(c))
	require.False(t, result.deliveryCommitted)
	require.Equal(t, 7, result.usage.InputTokens)
	require.NotEmpty(t, rec.Body.String())
}

func TestOpenAINativeRemoteCompactionFlushFailureDoesNotCommitDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	valid := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"ciphertext"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_flush","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":4,"total_tokens":11}}}`,
		``,
	}, "\n")
	flushErr := errors.New("native compaction flush failed")
	c, raw := newFlushErrorContext(t, flushErr)
	MarkOpenAINativeRemoteCompactionV2(c)
	svc := &OpenAIGatewayService{cfg: nativeCompactionHTTPTestConfig()}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(valid)), Header: http.Header{}}

	result, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.clientDisconnect)
	require.True(t, IsResponseCommitted(c))
	require.False(t, result.deliveryCommitted)
	require.Equal(t, 7, result.usage.InputTokens)
	require.Equal(t, int32(1), raw.flushCalls.Load())
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "downstream_flush_error", streamErr.ErrType)
	require.Equal(t, flushErr.Error(), streamErr.Message)
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
