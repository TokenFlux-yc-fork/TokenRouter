package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// custom_tool_call（custom/freeform 工具，如新版 apply_patch）应像 function_call 一样
// 注册为工具调用，其 *_input.delta 增量映射到正确的工具索引。
func TestResponsesEventToChatChunks_CustomToolCallInputDelta(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.Model = "gpt-5-codex"
	state.SentRole = true

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 1,
		Item: &ResponsesOutput{
			Type:   "custom_tool_call",
			CallID: "call_patch",
			Name:   "apply_patch",
		},
	}, state)
	require.Len(t, chunks, 1)
	require.Len(t, chunks[0].Choices[0].Delta.ToolCalls, 1)
	tc := chunks[0].Choices[0].Delta.ToolCalls[0]
	assert.Equal(t, "call_patch", tc.ID)
	assert.Equal(t, "apply_patch", tc.Function.Name)

	chunks = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.delta",
		OutputIndex: 1,
		Delta:       "*** Begin Patch",
	}, state)
	require.Len(t, chunks, 1)
	tc = chunks[0].Choices[0].Delta.ToolCalls[0]
	require.NotNil(t, tc.Index)
	assert.Equal(t, 0, *tc.Index)
	assert.Equal(t, "*** Begin Patch", tc.Function.Arguments)
}

func TestResponsesEventToChatChunks_OmitsEmptyFunctionNameOnArgumentDelta(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.Model = "gpt-5.5"
	state.SentRole = true

	_ = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item: &ResponsesOutput{
			Type:   "function_call",
			CallID: "call_getskill",
			Name:   "getskill",
		},
	}, state)

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"skill_name":"subagent-prompting"}`,
	}, state)
	require.Len(t, chunks, 1)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(mustMarshalChatChunk(t, chunks[0]), &wire))
	fn := wire["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	assert.NotContains(t, fn, "name")
	assert.Equal(t, `{"skill_name":"subagent-prompting"}`, fn["arguments"])
}

func TestResponsesEventToChatChunks_UsesDoneEventToolFieldsWhenAddedWasIncomplete(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.Model = "gpt-5.5"
	state.SentRole = true

	added := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item: &ResponsesOutput{
			Type: "function_call",
		},
	}, state)
	require.Len(t, added, 1)
	var addedWire map[string]any
	require.NoError(t, json.Unmarshal(mustMarshalChatChunk(t, added[0]), &addedWire))
	addedFn := addedWire["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	assert.NotContains(t, addedFn, "name")
	assert.NotContains(t, addedFn, "arguments")

	done := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: 0,
		Item: &ResponsesOutput{
			Type:      "function_call",
			CallID:    "call_getskill",
			Name:      "getskill",
			Arguments: `{"skill_name":"subagent-prompting"}`,
		},
	}, state)
	require.Len(t, done, 3)
	assert.Equal(t, "call_getskill", done[0].Choices[0].Delta.ToolCalls[0].ID)
	assert.Equal(t, "getskill", done[1].Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"skill_name":"subagent-prompting"}`, done[2].Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// A later duplicate terminal event must not append the full arguments again.
	duplicate := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Name:        "getskill",
		Arguments:   `{"skill_name":"subagent-prompting"}`,
	}, state)
	assert.Empty(t, duplicate)
}

func TestResponsesEventToChatChunks_DoneEventDoesNotOverwriteToolCallID(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.SentRole = true
	_ = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_original", Name: "exec"},
	}, state)

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_replacement", Name: "exec"},
	}, state)
	assert.Empty(t, chunks)
	assert.Equal(t, "call_original", state.OutputIndexToToolCallID[0])
}

func TestResponsesEventToChatChunks_DoneEventDoesNotRepeatArgumentDeltas(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.SentRole = true
	_ = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", Name: "exec"},
	}, state)
	_ = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"cmd":"true"}`,
	}, state)

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Name:        "exec",
		Arguments:   `{"cmd":"true"}`,
	}, state)
	assert.Empty(t, chunks)
}

func TestBufferedResponseAccumulator_UsesDoneEventToolFields(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call"},
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.done",
		OutputIndex: 0,
		Item: &ResponsesOutput{
			Type:      "function_call",
			CallID:    "call_getskill",
			Name:      "getskill",
			Arguments: `{"skill_name":"subagent-prompting"}`,
		},
	})

	output := acc.BuildOutput()
	require.Len(t, output, 1)
	assert.Equal(t, "call_getskill", output[0].CallID)
	assert.Equal(t, "getskill", output[0].Name)
	assert.Equal(t, `{"skill_name":"subagent-prompting"}`, output[0].Arguments)
}

func TestChatToolCall_NonStreamingMarshalKeepsEmptyFunctionFields(t *testing.T) {
	payload, err := json.Marshal(ChatToolCall{Function: ChatFunctionCall{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"function":{"name":"","arguments":""}}`, string(payload))
}

// 原始推理文本增量 reasoning_text.delta 应像 reasoning_summary_text.delta 一样
// 映射为 reasoning_content。
func TestResponsesEventToChatChunks_ReasoningTextDelta(t *testing.T) {
	state := NewResponsesEventToChatState()
	state.Model = "gpt-5-codex"
	state.SentRole = true

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:  "response.reasoning_text.delta",
		Delta: "thinking step",
	}, state)
	require.Len(t, chunks, 1)
	require.NotNil(t, chunks[0].Choices[0].Delta.ReasoningContent)
	assert.Equal(t, "thinking step", *chunks[0].Choices[0].Delta.ReasoningContent)
}

// 缓冲（非流式）累加器同样需识别两类新事件。
func TestBufferedResponseAccumulator_CodexEvents(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "custom_tool_call", CallID: "c1", Name: "apply_patch"},
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:        "response.custom_tool_call_input.delta",
		OutputIndex: 0,
		Delta:       "patch-body",
	})
	acc.ProcessEvent(&ResponsesStreamEvent{
		Type:  "response.reasoning_text.delta",
		Delta: "raw-reasoning",
	})
	require.True(t, acc.HasContent())
}

func TestResponsesToChatCompletions_MapsExtendedToolTypes(t *testing.T) {
	resp := ResponsesToChatCompletions(&ResponsesResponse{
		Status: "completed",
		Output: []ResponsesOutput{
			{Type: "custom_tool_call", CallID: "call_custom", Name: "apply_patch", Input: "*** Begin Patch"},
			{Type: "tool_search_call", CallID: "call_search", Arguments: `{"query":"mail"}`},
			{Type: "function_call", CallID: "call_ns", Name: "send", Namespace: "gmail", Arguments: `{"to":"a@example.com"}`},
		},
	}, "gpt-5.5")

	require.Len(t, resp.Choices, 1)
	toolCalls := resp.Choices[0].Message.ToolCalls
	require.Len(t, toolCalls, 3)
	assert.Equal(t, "apply_patch", toolCalls[0].Function.Name)
	assert.Equal(t, "*** Begin Patch", toolCalls[0].Function.Arguments)
	assert.Equal(t, toolSearchProxyName, toolCalls[1].Function.Name)
	assert.JSONEq(t, `{"query":"mail"}`, toolCalls[1].Function.Arguments)
	assert.Equal(t, "gmail__send", toolCalls[2].Function.Name)
}

func TestResponsesEventToChatChunks_RecoversExtendedToolDoneFields(t *testing.T) {
	tests := []struct {
		name          string
		added         ResponsesOutput
		done          ResponsesOutput
		wantName      string
		wantArguments string
	}{
		{
			name:          "custom",
			added:         ResponsesOutput{Type: "custom_tool_call", Name: "apply_patch"},
			done:          ResponsesOutput{Type: "custom_tool_call", CallID: "call_custom", Name: "apply_patch", Input: "*** Begin Patch"},
			wantName:      "apply_patch",
			wantArguments: "*** Begin Patch",
		},
		{
			name:          "tool_search",
			added:         ResponsesOutput{Type: "tool_search_call"},
			done:          ResponsesOutput{Type: "tool_search_call", CallID: "call_search", Arguments: `{"query":"mail"}`},
			wantName:      toolSearchProxyName,
			wantArguments: `{"query":"mail"}`,
		},
		{
			name:          "namespace",
			added:         ResponsesOutput{Type: "function_call", Name: "send", Namespace: "gmail"},
			done:          ResponsesOutput{Type: "function_call", CallID: "call_ns", Name: "send", Namespace: "gmail", Arguments: `{"to":"a@example.com"}`},
			wantName:      "gmail__send",
			wantArguments: `{"to":"a@example.com"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := NewResponsesEventToChatState()
			state.SentRole = true
			added := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.output_item.added", OutputIndex: 2, Item: &tt.added}, state)
			require.Len(t, added, 1)
			assert.Equal(t, tt.wantName, added[0].Choices[0].Delta.ToolCalls[0].Function.Name)

			done := ResponsesEventToChatChunks(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: 2, Item: &tt.done}, state)
			require.Len(t, done, 2)
			assert.Equal(t, tt.done.CallID, done[0].Choices[0].Delta.ToolCalls[0].ID)
			assert.Equal(t, tt.wantArguments, done[1].Choices[0].Delta.ToolCalls[0].Function.Arguments)
			assert.Equal(t, tt.wantName, state.OutputIndexToToolName[2])
		})
	}
}

func TestBufferedResponseAccumulator_PreservesExtendedToolDoneTypes(t *testing.T) {
	acc := NewBufferedResponseAccumulator()
	items := []ResponsesOutput{
		{Type: "custom_tool_call", CallID: "call_custom", Name: "apply_patch", Input: "patch"},
		{Type: "tool_search_call", CallID: "call_search", Arguments: `{"query":"mail"}`},
		{Type: "function_call", CallID: "call_ns", Name: "send", Namespace: "gmail", Arguments: `{}`},
	}
	for i := range items {
		acc.ProcessEvent(&ResponsesStreamEvent{Type: "response.output_item.done", OutputIndex: i, Item: &items[i]})
	}

	output := acc.BuildOutput()
	require.Len(t, output, 3)
	assert.Equal(t, "custom_tool_call", output[0].Type)
	assert.Equal(t, "patch", output[0].Input)
	assert.Equal(t, "tool_search_call", output[1].Type)
	assert.JSONEq(t, `{"query":"mail"}`, output[1].Arguments)
	assert.Equal(t, "function_call", output[2].Type)
	assert.Equal(t, "gmail", output[2].Namespace)
}

func mustMarshalChatChunk(t *testing.T, chunk ChatCompletionsChunk) []byte {
	t.Helper()
	sse, err := ChatChunkToSSE(chunk)
	require.NoError(t, err)
	sse = strings.TrimSpace(strings.TrimPrefix(sse, "data:"))
	return []byte(sse)
}
