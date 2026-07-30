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

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesRejectedFieldRetryStateRejectsDuplicateBodyAndCap(t *testing.T) {
	initialBody := []byte(`{"model":"gpt-5.5"}`)
	state := newOpenAIResponsesRejectedFieldRetryState(initialBody)

	require.False(t, state.Allow(initialBody))
	for attempt := 0; attempt < maxOpenAIResponsesRejectedFieldRetries; attempt++ {
		nextBody := []byte(fmt.Sprintf(`{"model":"gpt-5.5","variant":%d}`, attempt))
		require.True(t, state.Allow(nextBody))
		require.False(t, state.Allow(nextBody))
	}
	require.False(t, state.Allow([]byte(`{"model":"gpt-5.5","variant":"overflow"}`)))
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRejectsAmbiguousErrors(t *testing.T) {
	tests := []struct {
		name         string
		body         []byte
		responseBody []byte
	}{
		{
			name:         "namespace belongs to message",
			body:         []byte(`{"input":[{"type":"message","namespace":"keep"}]}`),
			responseBody: []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].namespace'.","param":"input[0].namespace"}}`),
		},
		{
			name:         "max output tokens only mentioned",
			body:         []byte(`{"max_output_tokens":4096}`),
			responseBody: []byte(`{"error":{"code":"invalid_request_error","message":"max_output_tokens must be positive","param":"max_output_tokens"}}`),
		},
		{
			name:         "structured param overrides namespace mention",
			body:         []byte(`{"input":[{"type":"function_call","namespace":"keep","arguments":"{}"}]}`),
			responseBody: []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].namespace'.","param":"tools"}}`),
		},
		{
			name:         "nested max output tokens param is not top level",
			body:         []byte(`{"max_output_tokens":4096,"input":[{"type":"message","content":{"max_output_tokens":"keep"}}]}`),
			responseBody: []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].content.max_output_tokens","param":"input[0].content.max_output_tokens"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, tt.body, tt.responseBody)
			require.NoError(t, err)
			require.False(t, changed)
			require.Nil(t, retryBody)
		})
	}
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyFindsNamespacePathInMessage(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","namespace":"keep","arguments":"{}"},{"type":"function_call","namespace":"remove","arguments":"{}"}]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"input[0] was accepted; Unknown parameter: 'input[1].namespace'."}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "input.0.namespace").String())
	require.False(t, gjson.GetBytes(retryBody, "input.1.namespace").Exists())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyBindsNamespacePathToRejectionPhrase(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","namespace":"keep","arguments":"{}"},{"type":"function_call","namespace":"remove","arguments":"{}"}]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"input[0].namespace is supported; Unknown parameter: input[1].namespace."}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "input.0.namespace").String())
	require.False(t, gjson.GetBytes(retryBody, "input.1.namespace").Exists())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyDoesNotTreatMaxOutputTokensSuggestionAsRejection(t *testing.T) {
	body := []byte(`{"max_tokens":4096,"max_output_tokens":2048}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: max_tokens. Use max_output_tokens instead."}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.False(t, changed)
	require.Nil(t, retryBody)
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyBindsMaxOutputTokensToRejectionPhrase(t *testing.T) {
	body := []byte(`{"max_output_tokens":2048}`)
	responseBody := []byte(`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens."}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "max_output_tokens").Exists())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesIndexedStatusOnly(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","status":"keep"},{"type":"function_call","status":"remove","arguments":"{}","content":{"status":"nested-keep"}}]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[1].status'.","param":"input[1].status"}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "input.0.status").String())
	require.False(t, gjson.GetBytes(retryBody, "input.1.status").Exists())
	require.Equal(t, "nested-keep", gjson.GetBytes(retryBody, "input.1.content.status").String())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesObservedReasoningModeRejection(t *testing.T) {
	body := []byte(`{"reasoning":{"mode":"remove","effort":"high"},"metadata":{"mode":"keep"}}`)
	responseBody := []byte("{\"detail\":\"`reasoning.mode` is not supported with this model.\"}")

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "reasoning.mode").Exists())
	require.Equal(t, "high", gjson.GetBytes(retryBody, "reasoning.effort").String())
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "metadata.mode").String())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesExplicitUnknownLeaf(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","client_trace":{"detail":"remove","id":"keep"}}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: client_trace.detail.","param":"client_trace.detail"}}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "client_trace.detail").Exists())
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "client_trace.id").String())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesObservedNestedFileField(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","content":[{"type":"input_text","text":"keep"},{"type":"input_file","file":"remove","filename":"keep.txt"}]}]}`)
	responseBody := []byte(`{"detail":"Unknown parameter: 'input[0].content[1].file'."}`)

	retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "input.0.content.1.file").Exists())
	require.Equal(t, "keep.txt", gjson.GetBytes(retryBody, "input.0.content.1.filename").String())
}

func TestNormalizeOpenAIPassthroughStripFields(t *testing.T) {
	fields, err := normalizeOpenAIPassthroughStripFields([]string{
		" max_output_tokens ",
		"input[].status",
		"reasoning.mode",
		"input[].status",
		"client_trace.detail",
	})

	require.NoError(t, err)
	require.Equal(t, []string{
		"max_output_tokens",
		"input[].status",
		"reasoning.mode",
		"client_trace.detail",
	}, fields)
}

func TestNormalizeOpenAIPassthroughStripFieldsRejectsCoreOrInvalidPaths(t *testing.T) {
	for _, fields := range [][]string{
		{"model"},
		{"input"},
		{"stream"},
		{"input[]"},
		{"input[*].status"},
		{"input[].content[]"},
	} {
		_, err := normalizeOpenAIPassthroughStripFields(fields)
		require.Error(t, err, fields)
	}
}

func TestNormalizeOpenAIPassthroughStripFieldsExtra(t *testing.T) {
	t.Run("JSON array is normalized", func(t *testing.T) {
		extra, err := normalizeOpenAIPassthroughStripFieldsExtra(PlatformOpenAI, map[string]any{
			openAIPassthroughStripFieldsExtraKey: []any{" max_output_tokens ", "input[].status", "input[].status"},
		})

		require.NoError(t, err)
		require.Equal(t, []string{"max_output_tokens", "input[].status"}, extra[openAIPassthroughStripFieldsExtraKey])
	})

	t.Run("missing key keeps group inheritance", func(t *testing.T) {
		extra := map[string]any{"openai_passthrough": true}
		normalized, err := normalizeOpenAIPassthroughStripFieldsExtra(PlatformOpenAI, extra)

		require.NoError(t, err)
		_, provided := normalized[openAIPassthroughStripFieldsExtraKey]
		require.False(t, provided)
	})

	t.Run("explicit empty array is retained", func(t *testing.T) {
		extra, err := normalizeOpenAIPassthroughStripFieldsExtra(PlatformOpenAI, map[string]any{
			openAIPassthroughStripFieldsExtraKey: []any{},
		})

		require.NoError(t, err)
		require.Equal(t, []string{}, extra[openAIPassthroughStripFieldsExtraKey])
	})

	t.Run("malformed value is rejected", func(t *testing.T) {
		_, err := normalizeOpenAIPassthroughStripFieldsExtra(PlatformOpenAI, map[string]any{
			openAIPassthroughStripFieldsExtraKey: "max_output_tokens",
		})

		require.Error(t, err)
	})
}

func TestResolveOpenAIPassthroughStripFieldsAccountOverridesGroup(t *testing.T) {
	group := &Group{OpenAIPassthroughStripFields: []string{"max_output_tokens", "input[].status"}}

	require.Empty(t,
		resolveOpenAIPassthroughStripFields(&Account{}, nil),
		"a missing request group must not invent a stripping policy",
	)
	require.Equal(t,
		[]string{"max_output_tokens"},
		resolveOpenAIPassthroughStripFields(&Account{}, &Group{}),
		"a missing legacy group value must use the default rather than disable stripping",
	)
	require.Equal(t,
		[]string{"max_output_tokens", "input[].status"},
		resolveOpenAIPassthroughStripFields(&Account{}, group),
	)
	require.Equal(t,
		[]string{"reasoning.mode"},
		resolveOpenAIPassthroughStripFields(&Account{Extra: map[string]any{
			openAIPassthroughStripFieldsExtraKey: []any{"reasoning.mode"},
		}}, group),
	)
	require.Empty(t, resolveOpenAIPassthroughStripFields(&Account{Extra: map[string]any{
		openAIPassthroughStripFieldsExtraKey: []any{},
	}}, group))
}

func TestStripOpenAIPassthroughRequestFieldsRemovesWildcardLeaves(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","max_output_tokens":4096,"reasoning":{"mode":"legacy","effort":"high"},"input":[{"type":"message","status":"remove","content":[{"type":"input_text","text":"hello","status":"nested-keep"}]},{"type":"function_call","status":"remove","arguments":"{}"}]}`)

	strippedBody, changed, err := stripOpenAIPassthroughRequestFields(body, []string{
		"max_output_tokens",
		"reasoning.mode",
		"input[].status",
	})

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(strippedBody, "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(strippedBody, "reasoning.mode").Exists())
	require.Equal(t, "high", gjson.GetBytes(strippedBody, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(strippedBody, "input.0.status").Exists())
	require.False(t, gjson.GetBytes(strippedBody, "input.1.status").Exists())
	require.Equal(t, "nested-keep", gjson.GetBytes(strippedBody, "input.0.content.0.status").String())
}

func TestOpenAIGatewayService_APIKeyStripsAllIndexedNamespacesBeforeFirstForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"function_call","name":"first","namespace":"remove-first","arguments":"{}"},{"type":"custom_tool_call","name":"second","namespace":"remove-second","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.0.namespace").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.1.namespace").Exists())
}

func TestOpenAIGatewayService_OpenAIHTTPStripsInputNamespacesBeforeFirstForward(t *testing.T) {
	accounts := []struct {
		name    string
		account *Account
	}{
		{name: "oauth", account: newOpenAIOAuthNamespaceTestAccount()},
		{name: "apikey", account: newOpenAIRejectedFieldTestAccount()},
	}
	for _, tt := range accounts {
		for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
			t.Run(tt.name+path, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.5","stream":false,"instructions":"test","input":[{"type":"message","role":"user","namespace":"remove","content":[{"type":"input_text","text":"hello","namespace":"nested-keep"}]}]}`)
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"id":"resp_namespace_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
				}}
				c := newOpenAIRejectedFieldTestContext(body)
				c.Request.URL.Path = path

				result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
					context.Background(),
					c,
					tt.account,
					body,
				)

				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1, "namespace must be removed before the first upstream request")
				require.False(t, gjson.GetBytes(upstream.bodies[0], "input.0.namespace").Exists())
				require.Equal(t, "nested-keep", gjson.GetBytes(upstream.bodies[0], "input.0.content.0.namespace").String())
			})
		}
	}
}

func TestOpenAIGatewayService_RetriesExplicitMaxOutputTokensRejection(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":4096,"input":[{"type":"message","role":"user","content":{"max_output_tokens":"keep"}}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens","type":"invalid_request_error"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	account := newOpenAIRejectedFieldTestAccount()
	account.Extra[openAIPassthroughStripFieldsExtraKey] = []any{}
	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		account,
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, int64(4096), gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Int())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
	require.Equal(t, "keep", gjson.GetBytes(upstream.bodies[1], "input.0.content.max_output_tokens").String())
}

func TestOpenAIGatewayService_APIKeyDefaultStripsMaxOutputTokensBeforeNormalForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":4096,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	c := newOpenAIRejectedFieldTestContext(body)
	c.Set("api_key", &APIKey{Group: &Group{
		OpenAIPassthroughStripFields: []string{"max_output_tokens"},
	}})

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		c,
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
}

func TestOpenAIGatewayService_APIKeyDefaultStripsNormalizedMaxTokensBeforeNormalForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_tokens":4096,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	c := newOpenAIRejectedFieldTestContext(body)
	c.Set("api_key", &APIKey{Group: &Group{
		OpenAIPassthroughStripFields: []string{"max_output_tokens"},
	}})

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		c,
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
}

func TestOpenAIGatewayService_ResponsesRetriesObservedRejectedFieldsSequentially(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"reasoning":{"mode":"legacy","effort":"high"},"input":[{"type":"message","role":"user","status":"remove","content":[{"type":"input_file","file":"remove","filename":"keep.txt"}]}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[0].status'.","param":"input[0].status"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, "{\"detail\":\"`reasoning.mode` is not supported with this model.\"}"),
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"detail":"Unknown parameter: 'input[0].content[0].file'."}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 4)
	require.Equal(t, "remove", gjson.GetBytes(upstream.bodies[0], "input.0.status").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.status").Exists())
	require.Equal(t, "legacy", gjson.GetBytes(upstream.bodies[1], "reasoning.mode").String())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "reasoning.mode").Exists())
	require.Equal(t, "remove", gjson.GetBytes(upstream.bodies[2], "input.0.content.0.file").String())
	require.False(t, gjson.GetBytes(upstream.bodies[3], "input.0.content.0.file").Exists())
	require.Equal(t, "keep.txt", gjson.GetBytes(upstream.bodies[3], "input.0.content.0.filename").String())
}

func TestOpenAIGatewayService_ComposesProactiveNamespaceStripWithRejectedFieldRetry(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":2048,"input":[{"type":"function_call","name":"first","namespace":"remove-first","arguments":"{}"},{"type":"custom_tool_call","name":"second","namespace":"remove-second","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		newOpenAIRejectedFieldTestAccount(),
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	for _, forwardedBody := range upstream.bodies {
		require.False(t, gjson.GetBytes(forwardedBody, "input.0.namespace").Exists())
		require.False(t, gjson.GetBytes(forwardedBody, "input.1.namespace").Exists())
	}
	require.Equal(t, int64(2048), gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Int())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
}

func TestOpenAIGatewayService_APIKeyPassthroughUsesAccountStripOverrideBeforeFirstForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":2048,"reasoning":{"mode":"legacy","effort":"high"},"input":[{"type":"message","role":"user","status":"remove","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	account := newOpenAIRejectedFieldTestAccount()
	account.Extra["openai_passthrough"] = true
	account.Extra[openAIPassthroughStripFieldsExtraKey] = []any{"max_output_tokens", "input[].status"}
	c := newOpenAIRejectedFieldTestContext(body)
	c.Set("api_key", &APIKey{Group: &Group{
		OpenAIPassthroughStripFields: []string{"reasoning.mode"},
	}})

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		c,
		account,
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.0.status").Exists())
	require.Equal(t, "legacy", gjson.GetBytes(upstream.bodies[0], "reasoning.mode").String(), "account override must replace, not merge with, the group policy")
}

func TestOpenAIGatewayService_APIKeyPassthroughUsesGroupStripFieldsBeforeFirstForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":2048,"reasoning":{"mode":"legacy","effort":"high"},"input":[{"type":"message","role":"user","status":"keep","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	account := newOpenAIRejectedFieldTestAccount()
	account.Extra["openai_passthrough"] = true
	c := newOpenAIRejectedFieldTestContext(body)
	c.Set("api_key", &APIKey{Group: &Group{
		OpenAIPassthroughStripFields: []string{"max_output_tokens", "reasoning.mode"},
	}})

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		c,
		account,
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "reasoning.mode").Exists())
	require.Equal(t, "high", gjson.GetBytes(upstream.bodies[0], "reasoning.effort").String())
	require.Equal(t, "keep", gjson.GetBytes(upstream.bodies[0], "input.0.status").String())
}

func TestOpenAIGatewayService_APIKeyPassthroughRetriesRejectedFieldsSequentially(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"reasoning":{"mode":"legacy","effort":"high"},"input":[{"type":"message","role":"user","status":"remove","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].status","param":"input[0].status"}}`),
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, "{\"detail\":\"`reasoning.mode` is not supported with this model.\"}"),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}
	account := newOpenAIRejectedFieldTestAccount()
	account.Extra["openai_passthrough"] = true
	// 显式空数组关闭首包剥离，用于验证上游拒绝后的有界降级重试。
	account.Extra[openAIPassthroughStripFieldsExtraKey] = []any{}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		account,
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 3)
	require.Equal(t, "remove", gjson.GetBytes(upstream.bodies[0], "input.0.status").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.status").Exists())
	require.Equal(t, "legacy", gjson.GetBytes(upstream.bodies[1], "reasoning.mode").String())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "reasoning.mode").Exists())
	require.Equal(t, "high", gjson.GetBytes(upstream.bodies[2], "reasoning.effort").String())
}

func TestOpenAIGatewayService_OAuthPassthroughRetriesExplicitRejectedField(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"max_output_tokens":2048,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(openAITestCompletedSSE)),
		},
	}}
	account := newOpenAIOAuthNamespaceTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(),
		newOpenAIRejectedFieldTestContext(body),
		account,
		body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, int64(2048), gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Int())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
}

func newOpenAIRejectedFieldTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
}

func newOpenAIRejectedFieldTestContext(body []byte) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "curl/8.0")
	return c
}

func newOpenAIRejectedFieldTestAccount() *Account {
	return &Account{
		ID:          5107,
		Name:        "responses-compatible",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
			openai_compat.ExtraKeyResponsesSupported: true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newOpenAIOAuthNamespaceTestAccount() *Account {
	return &Account{
		ID:          5108,
		Name:        "openai-oauth-namespace",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newOpenAIRejectedFieldTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
