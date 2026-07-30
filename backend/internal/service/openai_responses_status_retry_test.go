package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyStripsSameTypeStatusesInOneRetry(t *testing.T) {
	body := []byte(`{
		"input":[
			{"type":"custom_tool_call","status":"completed","name":"one","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"two","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"three","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"four","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"five","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"six","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"seven","input":"{}"},
			{"type":"custom_tool_call","status":"completed","name":"eight","input":"{}"},
			{"type":"function_call","status":"completed","name":"preserve-function","arguments":"{}"},
			{"type":"message","role":"assistant","status":"completed","content":{"status":"nested-keep"}}
		]
	}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[7].status'.","param":"input[7].status"}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	for index := 0; index < 8; index++ {
		require.False(t, gjson.GetBytes(retryBody, fmt.Sprintf("input.%d.status", index)).Exists())
	}
	require.Equal(t, "completed", gjson.GetBytes(retryBody, "input.8.status").String())
	require.Equal(t, "completed", gjson.GetBytes(retryBody, "input.9.status").String())
	require.Equal(t, "nested-keep", gjson.GetBytes(retryBody, "input.9.content.status").String())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodySeparatesMessageRoles(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","role":"user","status":"completed","content":"first"},{"type":"message","role":"assistant","status":"completed","content":"preserve"},{"type":"message","role":"user","status":"completed","content":"second"}]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[2].status","param":"input[2].status"}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "input.0.status").Exists())
	require.Equal(t, "completed", gjson.GetBytes(retryBody, "input.1.status").String())
	require.False(t, gjson.GetBytes(retryBody, "input.2.status").Exists())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyDoesNotStripStatusForValidationError(t *testing.T) {
	body := []byte(`{"input":[{"type":"custom_tool_call","status":"keep","input":"{}"}]}`)
	responseBody := []byte(`{"error":{"code":"invalid_request_error","message":"input[0].status must be completed","param":"input[0].status"}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.False(t, changed)
	require.Nil(t, retryBody)
}

func TestOpenAIGatewayServiceNormalForwardRetriesRejectedStatuses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"custom_tool_call","status":"completed","name":"first","input":"{}"},{"type":"custom_tool_call","status":"completed","name":"second","input":"{}"},{"type":"function_call","status":"completed","name":"preserve","arguments":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[1].status'.","param":"input[1].status"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"id":"resp_status_ok","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIRejectedFieldTestAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.status").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.1.status").Exists())
	require.Equal(t, "completed", gjson.GetBytes(upstream.bodies[1], "input.2.status").String())
}

func TestOpenAIGatewayServiceOAuthPassthroughRetriesRejectedStatusesBeforeStreaming(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"custom_tool_call","status":"completed","name":"first","input":"{}"},{"type":"custom_tool_call","status":"completed","name":"second","input":"{}"},{"type":"function_call","status":"completed","name":"preserve","arguments":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[1].status'.","param":"input[1].status"}}`),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(openAITestCompletedSSE)),
		},
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex-tui/0.121.0")
	account := newOpenAIOAuthNamespaceTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.status").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.1.status").Exists())
	require.Equal(t, "completed", gjson.GetBytes(upstream.bodies[1], "input.2.status").String())
}

func TestOpenAIGatewayServiceOAuthPassthroughDoesNotRetryRejectedStatusForNativeCompactionV2(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":true,"store":false,"input":[{"type":"custom_tool_call","status":"completed","name":"preserve","input":"{}"},{"type":"compaction_trigger"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].status'.","param":"input[0].status"}}`),
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex-tui/0.121.0")
	MarkOpenAINativeRemoteCompactionV2(c)
	account := newOpenAIOAuthNamespaceTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), c, account, body)

	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.Equal(t, "completed", gjson.GetBytes(upstream.bodies[0], "input.0.status").String())
}
