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
			Type:   "function_call",
			CallID: "call_getskill",
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
	require.Len(t, done, 2)
	assert.Equal(t, "getskill", done[0].Choices[0].Delta.ToolCalls[0].Function.Name)
	assert.Equal(t, `{"skill_name":"subagent-prompting"}`, done[1].Choices[0].Delta.ToolCalls[0].Function.Arguments)

	// A later duplicate terminal event must not append the full arguments again.
	duplicate := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Name:        "getskill",
		Arguments:   `{"skill_name":"subagent-prompting"}`,
	}, state)
	assert.Empty(t, duplicate)
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
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_getskill"},
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

func mustMarshalChatChunk(t *testing.T, chunk ChatCompletionsChunk) []byte {
	t.Helper()
	sse, err := ChatChunkToSSE(chunk)
	require.NoError(t, err)
	sse = strings.TrimSpace(strings.TrimPrefix(sse, "data:"))
	return []byte(sse)
}
