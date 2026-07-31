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

func TestForwardAsChatCompletions_StreamingCancelledUsesFailureBoundary(t *testing.T) {
	for _, eventType := range []string{"response.cancelled", "response.canceled"} {
		t.Run(eventType+" before output", func(t *testing.T) {
			rec, c, svc, body, account := newChatTerminalTest(t, eventType, false, "")

			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Nil(t, result)
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})

		t.Run(eventType+" after output", func(t *testing.T) {
			rec, c, svc, body, account := newChatTerminalTest(t, eventType, true, "")

			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")

			require.Error(t, err)
			require.NotNil(t, result)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			clientStream := rec.Body.String()
			require.Contains(t, clientStream, "partial")
			require.Contains(t, clientStream, "cancelled by upstream")
			require.NotContains(t, clientStream, "[DONE]")
		})
	}
}

func TestForwardAsChatCompletions_StreamingIncompleteUsesCanonicalOutcome(t *testing.T) {
	t.Run("max output tokens completes normally", func(t *testing.T) {
		rec, c, svc, body, account := newChatTerminalTest(t, "response.incomplete", true, "max_output_tokens")

		result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Contains(t, rec.Body.String(), `"finish_reason":"length"`)
		require.Contains(t, rec.Body.String(), "[DONE]")
	})

	for _, reason := range []string{"content_filter", "unknown_reason", ""} {
		t.Run("unsuccessful "+reason, func(t *testing.T) {
			rec, c, svc, body, account := newChatTerminalTest(t, "response.incomplete", true, reason)

			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")

			require.Error(t, err)
			require.NotNil(t, result)
			require.Contains(t, rec.Body.String(), "partial")
			require.NotContains(t, rec.Body.String(), "[DONE]")
		})
	}
}

func newChatTerminalTest(
	t *testing.T,
	eventType string,
	withOutput bool,
	incompleteReason string,
) (*httptest.ResponseRecorder, *gin.Context, *OpenAIGatewayService, []byte, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	frames := []string{
		`data: {"type":"response.created","response":{"id":"resp_chat_terminal","object":"response","model":"gpt-5.5","status":"in_progress","output":[]}}`,
		"",
	}
	if withOutput {
		frames = append(frames,
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`,
			"",
		)
	}
	terminal := fmt.Sprintf(`{"type":%q,"response":{"id":"resp_chat_terminal","object":"response","model":"gpt-5.5","status":%q,"output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, eventType, terminalStatus(eventType))
	if eventType == "response.incomplete" {
		details := "{}"
		if incompleteReason != "" {
			details = fmt.Sprintf(`{"reason":%q}`, incompleteReason)
		}
		terminal = fmt.Sprintf(`{"type":"response.incomplete","response":{"id":"resp_chat_terminal","object":"response","model":"gpt-5.5","status":"incomplete","incomplete_details":%s,"output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, details)
	}
	frames = append(frames, "data: "+terminal, "")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(frames, "\n"))),
	}}
	account := &Account{
		ID: 1, Name: "chat-terminal-test", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
	}
	return rec, c, &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}, body, account
}
