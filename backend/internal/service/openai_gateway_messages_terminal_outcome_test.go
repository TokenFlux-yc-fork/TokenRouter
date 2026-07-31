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
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardAsAnthropic_StreamingCancelledUsesFailureBoundary(t *testing.T) {
	for _, eventType := range []string{"response.cancelled", "response.canceled"} {
		t.Run(eventType+" before output", func(t *testing.T) {
			rec, c, svc, body, account := newAnthropicTerminalTest(t, eventType, false, "")

			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Nil(t, result)
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})

		t.Run(eventType+" after output", func(t *testing.T) {
			rec, c, svc, body, account := newAnthropicTerminalTest(t, eventType, true, "")

			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

			require.Error(t, err)
			require.NotNil(t, result)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.True(t, IsOpenAIForwardErrorCommunicated(err))
			clientStream := rec.Body.String()
			require.Contains(t, clientStream, `"text":"partial"`)
			require.Equal(t, 1, strings.Count(clientStream, "event: error"))
			require.NotContains(t, clientStream, "event: message_stop")
		})
	}
}

func TestForwardAsAnthropic_StreamingIncompleteUsesCanonicalOutcome(t *testing.T) {
	t.Run("max output tokens completes normally", func(t *testing.T) {
		rec, c, svc, body, account := newAnthropicTerminalTest(t, "response.incomplete", true, "max_output_tokens")

		result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

		require.NoError(t, err)
		require.NotNil(t, result)
		clientStream := rec.Body.String()
		require.Contains(t, clientStream, `"stop_reason":"max_tokens"`)
		require.Contains(t, clientStream, "event: message_stop")
		require.NotContains(t, clientStream, "event: error")
	})

	for _, reason := range []string{"content_filter", "unknown_reason", ""} {
		t.Run("unsuccessful "+reason, func(t *testing.T) {
			rec, c, svc, body, account := newAnthropicTerminalTest(t, "response.incomplete", true, reason)

			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

			require.Error(t, err)
			require.NotNil(t, result)
			clientStream := rec.Body.String()
			require.Equal(t, 1, strings.Count(clientStream, "event: error"))
			require.NotContains(t, clientStream, "event: message_stop")
		})
	}
}

func newAnthropicTerminalTest(
	t *testing.T,
	eventType string,
	withOutput bool,
	incompleteReason string,
) (*httptest.ResponseRecorder, *gin.Context, *OpenAIGatewayService, []byte, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	frames := []string{
		`data: {"type":"response.created","response":{"id":"resp_terminal","object":"response","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
	}
	if withOutput {
		frames = append(frames,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
			"",
		)
	}
	terminal := fmt.Sprintf(`{"type":%q,"response":{"id":"resp_terminal","object":"response","model":"gpt-5.4","status":%q,"output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, eventType, terminalStatus(eventType))
	if eventType == "response.incomplete" {
		details := "{}"
		if incompleteReason != "" {
			details = fmt.Sprintf(`{"reason":%q}`, incompleteReason)
		}
		terminal = fmt.Sprintf(`{"type":"response.incomplete","response":{"id":"resp_terminal","object":"response","model":"gpt-5.4","status":"incomplete","incomplete_details":%s,"output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, details)
	}
	frames = append(frames, "data: "+terminal, "")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(frames, "\n"))),
	}}
	return rec, c, &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}, body, rawChatCompletionsTestAccount()
}

func terminalStatus(eventType string) string {
	switch eventType {
	case "response.cancelled", "response.canceled":
		return "cancelled"
	default:
		return "failed"
	}
}
