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

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndKeepsSupportedTools(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "spawn_agent"},
			}},
		},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hello"},
			map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{
				map[string]any{"type": "namespace", "name": "image_gen"},
				map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
					map[string]any{"type": "function", "name": "spawn_agent"},
				}},
			}},
		},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	tools := reqBody["tools"].([]any)
	require.Len(t, tools, 3)
	require.Equal(t, "function", tools[0].(map[string]any)["type"])
	require.Equal(t, "custom", tools[1].(map[string]any)["type"])
	require.Equal(t, "tool_search", tools[2].(map[string]any)["type"])
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	additional := input[1].(map[string]any)["tools"].([]any)
	require.Len(t, additional, 2)
	require.Equal(t, "image_gen", additional[0].(map[string]any)["name"])
	require.Equal(t, "collaboration", additional[1].(map[string]any)["name"], "existing namespace must not be duplicated")
	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsConflictingAdditionalTool(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{map[string]any{
			"type":  "namespace",
			"name":  "collaboration",
			"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
		}},
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type":  "namespace",
				"name":  "collaboration",
				"tools": []any{map[string]any{"type": "function", "name": "send_message"}},
			}},
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.ErrorContains(t, err, `conflicts with migrated tool type "namespace" name "collaboration"`)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 1, "conflicts must not partially remove top-level tools")
}

func TestNormalizeOpenAIResponsesLiteTools_DeduplicatesAcrossAdditionalToolItems(t *testing.T) {
	namespace := map[string]any{
		"type":  "namespace",
		"name":  "collaboration",
		"tools": []any{map[string]any{"type": "function", "name": "spawn_agent"}},
	}
	reqBody := map[string]any{
		"tools": []any{namespace},
		"input": []any{
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{map[string]any{"type": "custom", "name": "exec"}},
			},
			map[string]any{
				"type":  "additional_tools",
				"tools": []any{namespace},
			},
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input[0].(map[string]any)["tools"], 1)
	require.Len(t, input[1].(map[string]any)["tools"], 1)
}

func TestNormalizeOpenAIResponsesLiteTools_ConvertsStringInput(t *testing.T) {
	reqBody := map[string]any{
		"input": "hello",
		"tools": []any{map[string]any{
			"type": "namespace",
			"name": "collaboration",
		}},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, reqBody, "tools")
	input := reqBody["input"].([]any)
	require.Len(t, input, 2)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
	require.Equal(t, "hello", input[0].(map[string]any)["content"])
	require.Equal(t, "additional_tools", input[1].(map[string]any)["type"])
}

func TestNormalizeOpenAIResponsesLiteTools_KeepsSupportedTopLevelTools(t *testing.T) {
	reqBody := map[string]any{
		"tools": []any{
			map[string]any{"type": "function", "name": "shell"},
			map[string]any{"type": "custom", "name": "exec"},
			map[string]any{"type": "tool_search"},
			"custom shorthand",
		},
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, reqBody["tools"], 4)
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsUnsupportedTools(t *testing.T) {
	tests := []struct {
		name string
		tool map[string]any
		want string
	}{
		{name: "hosted web search", tool: map[string]any{"type": "web_search"}, want: `top-level tool type "web_search"`},
		{name: "hosted image generation", tool: map[string]any{"type": "image_generation"}, want: `top-level tool type "image_generation"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := map[string]any{"tools": []any{tt.tool}}
			changed, err := normalizeOpenAIResponsesLiteTools(reqBody)
			require.ErrorContains(t, err, tt.want)
			require.False(t, changed)
			require.Len(t, reqBody["tools"], 1, "validation errors must not partially mutate tools")
		})
	}
}

func TestNormalizeOpenAIResponsesLitePayload_EnforcesAllTurnsReasoningContext(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantChanged bool
		wantEffort  string
		wantSummary string
	}{
		{
			name:        "missing reasoning without namespace tools",
			body:        `{"model":"gpt-5.6-terra","tools":[{"type":"function","name":"shell"}]}`,
			wantChanged: true,
		},
		{
			name:        "null reasoning",
			body:        `{"reasoning":null}`,
			wantChanged: true,
		},
		{
			name:        "missing context preserves other reasoning fields",
			body:        `{"reasoning":{"effort":"high","summary":"auto"}}`,
			wantChanged: true,
			wantEffort:  "high",
			wantSummary: "auto",
		},
		{
			name:        "different context is replaced",
			body:        `{"reasoning":{"effort":"medium","context":"current_turn"}}`,
			wantChanged: true,
			wantEffort:  "medium",
		},
		{
			name:        "all turns is unchanged",
			body:        `{"reasoning":{"context":"all_turns"}}`,
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			updated, changed, err := normalizeOpenAIResponsesLitePayload(body)

			require.NoError(t, err)
			require.Equal(t, tt.wantChanged, changed)
			require.Equal(t, openAIResponsesLiteReasoningContext, gjson.GetBytes(updated, "reasoning.context").String())
			require.Equal(t, tt.wantEffort, gjson.GetBytes(updated, "reasoning.effort").String())
			require.Equal(t, tt.wantSummary, gjson.GetBytes(updated, "reasoning.summary").String())
			if !tt.wantChanged {
				require.Equal(t, body, updated)
			}
		})
	}
}

func TestNormalizeOpenAIResponsesLitePayload_ValidationErrorReturnsOriginalBody(t *testing.T) {
	body := []byte(`{
		"reasoning":"high",
		"tools":[{"type":"namespace","name":"collaboration"}],
		"input":"hello"
	}`)

	updated, changed, err := normalizeOpenAIResponsesLitePayload(body)

	require.ErrorContains(t, err, "reasoning to be an object")
	require.Equal(t, "reasoning", openAIResponsesLiteValidationParam(err))
	require.False(t, changed)
	require.Equal(t, body, updated)
}

func TestIsOpenAIResponsesLiteValidationError_CoversHTTPAndWrappedWSErrors(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "invalid JSON", body: []byte(`{"reasoning":`)},
		{name: "invalid tools", body: []byte(`{"tools":[{"type":"web_search"}]}`)},
		{name: "invalid reasoning", body: []byte(`{"reasoning":"high"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, changed, err := normalizeOpenAIResponsesLitePayload(tt.body)
			require.Error(t, err)
			require.False(t, changed)
			require.True(t, IsOpenAIResponsesLiteValidationError(err))
			require.True(t, IsOpenAIResponsesLiteValidationError(fmt.Errorf("ws close: %w", err)))
		})
	}

	require.False(t, IsOpenAIResponsesLiteValidationError(errors.New("upstream EOF")))
}

func TestReportOpenAIAccountScheduleFailure_SkipsLiteClientValidation(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	svc := &OpenAIGatewayService{rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("true")}
	_, _, validationErr := normalizeOpenAIResponsesLitePayload([]byte(`{"reasoning":"high"}`))
	require.Error(t, validationErr)

	svc.ReportOpenAIAccountScheduleFailure(701, validationErr)
	svc.ReportOpenAIAccountScheduleFailure(701, fmt.Errorf("ws policy close: %w", validationErr))
	require.Zero(t, svc.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)

	svc.ReportOpenAIAccountScheduleFailure(701, errors.New("upstream EOF"))
	require.Equal(t, 1, svc.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount)
}

func TestNormalizeOpenAIResponsesLitePayload_PreservesLargeIntegers(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-terra",
		"tools":[{
			"type":"function",
			"name":"seek",
			"parameters":{"type":"object","properties":{"offset":{"type":"integer","default":9007199254740993}}}
		}]
	}`)

	updated, changed, err := normalizeOpenAIResponsesLitePayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "9007199254740993", gjson.GetBytes(updated, "tools.0.parameters.properties.offset.default").Raw)
	require.Equal(t, openAIResponsesLiteReasoningContext, gjson.GetBytes(updated, "reasoning.context").String())
}

func TestNormalizeOpenAIResponsesLitePayload_PreservesResponseCreateShape(t *testing.T) {
	body := []byte(`{
		"type":"response.create",
		"model":"gpt-5.6-terra",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`)

	updated, changed, err := normalizeOpenAIResponsesLitePayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "response.create", gjson.GetBytes(updated, "type").String())
	require.False(t, gjson.GetBytes(updated, "tools").Exists())
	require.Equal(t, "collaboration", gjson.GetBytes(updated, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.GetBytes(updated, "tool_choice.type").String())
	require.Equal(t, openAIResponsesLiteReasoningContext, gjson.GetBytes(updated, "reasoning.context").String())
}

func TestApplyCodexOAuthTransform_PreservesLiteNamespaceToolChoice(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.6-terra",
		"input": []any{map[string]any{
			"type": "additional_tools",
			"tools": []any{map[string]any{
				"type": "namespace",
				"name": "collaboration",
			}},
		}},
		"tool_choice": map[string]any{"type": "namespace", "name": "collaboration"},
	}

	applyCodexOAuthTransform(reqBody, true, false)

	require.Equal(t, map[string]any{"type": "namespace", "name": "collaboration"}, reqBody["tool_choice"])
}

func TestOpenAIGatewayServiceForward_NormalizesResponsesLitePayloadForOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		path        string
		passthrough bool
	}{
		{name: "managed responses", path: "/v1/responses"},
		{name: "passthrough responses", path: "/v1/responses", passthrough: true},
		{name: "managed input tokens", path: "/v1/responses/input_tokens"},
		{name: "passthrough input tokens", path: "/v1/responses/input_tokens", passthrough: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(nil))
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			c.Request.Header.Set(responsesLiteHeader, "true")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_lite\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
						"data: [DONE]\n\n",
				)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{
				ID: 501, Name: "responses-lite", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Concurrency: 1, Status: StatusActive, Schedulable: true, RateMultiplier: f64p(1),
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
				Extra:       map[string]any{"openai_passthrough": tt.passthrough},
			}
			body := []byte(`{
				"model":"gpt-5.6-terra","stream":true,"instructions":"test",
				"tools":[
					{"type":"function","name":"shell","parameters":{"type":"object"}},
					{"type":"custom","name":"exec"},
					{"type":"tool_search"},
					{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
				],
				"input":[{"type":"message","role":"user","content":"hello"}],
				"tool_choice":{"type":"namespace","name":"collaboration"}
			}`)

			result, err := svc.Forward(context.Background(), c, account, body)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
			require.Equal(t, chatgptCodexURL+openAIResponsesRequestPathSuffix(c), upstream.lastReq.URL.String())
			require.Equal(t, openAIResponsesLiteReasoningContext, gjson.GetBytes(upstream.lastBody, "reasoning.context").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
			require.Equal(t, "shell", gjson.GetBytes(upstream.lastBody, `tools.#(type=="function").name`).String())
			require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, `tools.#(type=="custom").name`).String())
			require.True(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="tool_search")`).Exists())
			require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.0.name`).String())
			require.Equal(t, "namespace", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String())
			require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, "tool_choice.name").String())
		})
	}
}

func TestOpenAIGatewayServiceForward_ResponsesLiteValidationErrorCommitsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	c.Request.Header.Set(responsesLiteHeader, "true")
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{}}
	account := &Account{
		ID: 502, Name: "responses-lite-invalid", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.6-terra","reasoning":"high"}`))

	require.ErrorContains(t, err, "reasoning to be an object")
	require.Nil(t, result)
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.True(t, gjson.ValidBytes(rec.Body.Bytes()))
	require.Equal(t, "reasoning", gjson.GetBytes(rec.Body.Bytes(), "error.param").String())
	require.NotContains(t, rec.Body.String(), "event:")
}
