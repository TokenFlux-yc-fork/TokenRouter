package apicompat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → ChatCompletionsResponse
// ---------------------------------------------------------------------------

// ResponsesToChatCompletions converts a Responses API response into a Chat
// Completions response. Text output items are concatenated into
// choices[0].message.content; function_call items become tool_calls.
func ResponsesToChatCompletions(resp *ResponsesResponse, model string) *ChatCompletionsResponse {
	id := resp.ID
	if id == "" {
		id = generateChatCmplID()
	}

	out := &ChatCompletionsResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
	}

	var contentText string
	var reasoningText string
	var toolCalls []ChatToolCall

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					contentText += part.Text
				}
			}
		case "function_call", "custom_tool_call", "tool_search_call":
			name, arguments, ok := responsesOutputChatToolFields(&item)
			if !ok {
				continue
			}
			toolCalls = append(toolCalls, ChatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			})
		case "reasoning":
			for _, s := range item.Summary {
				if s.Type == "summary_text" && s.Text != "" {
					reasoningText += s.Text
				}
			}
		case "web_search_call":
			// silently consumed — results already incorporated into text output
		}
	}

	msg := ChatMessage{Role: "assistant"}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if contentText != "" {
		raw, _ := json.Marshal(contentText)
		msg.Content = raw
	}
	if reasoningText != "" {
		msg.ReasoningContent = reasoningText
	}

	finishReason := responsesStatusToChatFinishReason(resp.Status, resp.IncompleteDetails, toolCalls)

	out.Choices = []ChatChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	}}

	out.Usage = chatUsageFromResponsesUsage(resp.Usage)

	return out
}

func responsesStatusToChatFinishReason(status string, details *ResponsesIncompleteDetails, toolCalls []ChatToolCall) string {
	switch status {
	case "incomplete":
		if details != nil {
			switch details.Reason {
			case "max_output_tokens":
				return "length"
			case "content_filter":
				return "content_filter"
			}
		}
		return "stop"
	case "completed":
		if len(toolCalls) > 0 {
			return "tool_calls"
		}
		return "stop"
	default:
		return "stop"
	}
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []ChatCompletionsChunk (stateful converter)
// ---------------------------------------------------------------------------

// ResponsesEventToChatState tracks state for converting a sequence of Responses
// SSE events into Chat Completions SSE chunks.
type ResponsesEventToChatState struct {
	ID                      string
	Model                   string
	Created                 int64
	SentRole                bool
	SawToolCall             bool
	SawText                 bool
	Finalized               bool        // true after finish chunk has been emitted
	NextToolCallIndex       int         // next sequential tool_call index to assign
	OutputIndexToToolIndex  map[int]int // Responses output_index → Chat tool_calls index
	OutputIndexToToolCallID map[int]string
	OutputIndexToToolName   map[int]string
	OutputIndexSawToolArgs  map[int]bool
	IncludeUsage            bool
	Usage                   *ChatUsage
}

// NewResponsesEventToChatState returns an initialised stream state.
func NewResponsesEventToChatState() *ResponsesEventToChatState {
	return &ResponsesEventToChatState{
		ID:                      generateChatCmplID(),
		Created:                 time.Now().Unix(),
		OutputIndexToToolIndex:  make(map[int]int),
		OutputIndexToToolCallID: make(map[int]string),
		OutputIndexToToolName:   make(map[int]string),
		OutputIndexSawToolArgs:  make(map[int]bool),
	}
}

// ResponsesEventToChatChunks converts a single Responses SSE event into zero
// or more Chat Completions chunks, updating state as it goes.
func ResponsesEventToChatChunks(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	switch evt.Type {
	case "response.created":
		return resToChatHandleCreated(evt, state)
	case "response.output_text.delta":
		return resToChatHandleTextDelta(evt, state)
	case "response.output_item.added":
		return resToChatHandleOutputItemAdded(evt, state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具（如新版 apply_patch）的输入增量与 function_call 参数增量同形，
		// 均按 OutputIndex 累加到对应工具调用。
		"response.custom_tool_call_input.delta":
		return resToChatHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done",
		"response.custom_tool_call_input.done":
		return resToChatHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToChatHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量（真实 Codex 客户端消费的 reasoning_text.delta），
		// 与 reasoning summary 一样映射为 reasoning_content。
		"response.reasoning_text.delta":
		return resToChatHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		return nil
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToChatHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesChatStream emits a final chunk with finish_reason if the
// stream ended without a proper completion event (e.g. upstream disconnect).
// It is idempotent: if a completion event already emitted the finish chunk,
// this returns nil.
func FinalizeResponsesChatStream(state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.Finalized {
		return nil
	}
	state.Finalized = true

	finishReason := "stop"
	if state.SawToolCall {
		finishReason = "tool_calls"
	}

	chunks := []ChatCompletionsChunk{makeChatFinishChunk(state, finishReason)}

	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:      state.ID,
			Object:  "chat.completion.chunk",
			Created: state.Created,
			Model:   state.Model,
			Choices: []ChatChunkChoice{},
			Usage:   state.Usage,
		})
	}

	return chunks
}

// ChatChunkToSSE formats a ChatCompletionsChunk as an SSE data line.
func ChatChunkToSSE(chunk ChatCompletionsChunk) (string, error) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("data: %s\n\n", data), nil
}

// --- internal handlers ---

func resToChatHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Response != nil {
		if evt.Response.ID != "" {
			state.ID = evt.Response.ID
		}
		if state.Model == "" && evt.Response.Model != "" {
			state.Model = evt.Response.Model
		}
	}
	// Emit the role chunk.
	if state.SentRole {
		return nil
	}
	state.SentRole = true

	role := "assistant"
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Role: role})}
}

func resToChatHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	state.SawText = true
	content := evt.Delta
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Content: &content})}
}

func resToChatHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	name, _, ok := responsesOutputChatToolFields(evt.Item)
	if !ok {
		return nil
	}

	state.SawToolCall = true
	idx := state.NextToolCallIndex
	state.OutputIndexToToolIndex[evt.OutputIndex] = idx
	state.OutputIndexToToolCallID[evt.OutputIndex] = evt.Item.CallID
	state.OutputIndexToToolName[evt.OutputIndex] = name
	state.NextToolCallIndex++

	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			ID:    evt.Item.CallID,
			Type:  "function",
			Function: ChatFunctionCall{
				Name: name,
			},
		}},
	})}
}

func resToChatHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}

	idx, ok := state.OutputIndexToToolIndex[evt.OutputIndex]
	if !ok {
		return nil
	}
	state.OutputIndexSawToolArgs[evt.OutputIndex] = true

	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			Function: ChatFunctionCall{
				Arguments: evt.Delta,
			},
		}},
	})}
}

func resToChatHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	arguments := evt.Arguments
	if evt.Type == "response.custom_tool_call_input.done" {
		arguments = evt.Input
	}
	return resToChatHandleToolFinal(evt, state, evt.CallID, evt.Name, arguments)
}

func resToChatHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	name, arguments, ok := responsesOutputChatToolFields(evt.Item)
	if !ok {
		return nil
	}
	return resToChatHandleToolFinal(evt, state, evt.Item.CallID, name, arguments)
}

func responsesOutputChatToolFields(item *ResponsesOutput) (name, arguments string, ok bool) {
	if item == nil {
		return "", "", false
	}
	switch item.Type {
	case "function_call":
		name = item.Name
		if item.Namespace != "" {
			name = flattenNamespaceToolName(item.Namespace, name)
		}
		return name, item.Arguments, true
	case "custom_tool_call":
		return item.Name, item.Input, true
	case "tool_search_call":
		return toolSearchProxyName, item.Arguments, true
	default:
		return "", "", false
	}
}

func resToChatHandleToolFinal(evt *ResponsesStreamEvent, state *ResponsesEventToChatState, callID, name, arguments string) []ChatCompletionsChunk {
	idx, ok := state.OutputIndexToToolIndex[evt.OutputIndex]
	if !ok {
		return nil
	}

	var chunks []ChatCompletionsChunk
	if callID != "" && state.OutputIndexToToolCallID[evt.OutputIndex] == "" {
		state.OutputIndexToToolCallID[evt.OutputIndex] = callID
		chunks = append(chunks, makeChatDeltaChunk(state, ChatDelta{
			ToolCalls: []ChatToolCall{{
				Index: &idx,
				ID:    callID,
			}},
		}))
	}
	if name != "" && state.OutputIndexToToolName[evt.OutputIndex] == "" {
		state.OutputIndexToToolName[evt.OutputIndex] = name
		chunks = append(chunks, makeChatDeltaChunk(state, ChatDelta{
			ToolCalls: []ChatToolCall{{
				Index: &idx,
				Function: ChatFunctionCall{
					Name: name,
				},
			}},
		}))
	}

	// Responses done events carry the complete arguments. Chat Completions
	// deltas are append-only, so use the complete payload only as a fallback.
	if arguments != "" && !state.OutputIndexSawToolArgs[evt.OutputIndex] {
		state.OutputIndexSawToolArgs[evt.OutputIndex] = true
		chunks = append(chunks, makeChatDeltaChunk(state, ChatDelta{
			ToolCalls: []ChatToolCall{{
				Index: &idx,
				Function: ChatFunctionCall{
					Arguments: arguments,
				},
			}},
		}))
	}

	return chunks
}

func resToChatHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	reasoning := evt.Delta
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{ReasoningContent: &reasoning})}
}

func resToChatHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	state.Finalized = true
	finishReason := "stop"

	if evt.Usage != nil {
		state.Usage = chatUsageFromResponsesUsage(evt.Usage)
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			state.Usage = chatUsageFromResponsesUsage(evt.Response.Usage)
		}

		switch evt.Response.Status {
		case "incomplete":
			if evt.Response.IncompleteDetails != nil {
				switch evt.Response.IncompleteDetails.Reason {
				case "max_output_tokens":
					finishReason = "length"
				case "content_filter":
					finishReason = "content_filter"
				}
			}
		case "completed":
			if state.SawToolCall {
				finishReason = "tool_calls"
			}
		}
	} else if state.SawToolCall {
		finishReason = "tool_calls"
	}

	var chunks []ChatCompletionsChunk
	chunks = append(chunks, makeChatFinishChunk(state, finishReason))

	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:      state.ID,
			Object:  "chat.completion.chunk",
			Created: state.Created,
			Model:   state.Model,
			Choices: []ChatChunkChoice{},
			Usage:   state.Usage,
		})
	}

	return chunks
}

func chatUsageFromResponsesUsage(u *ResponsesUsage) *ChatUsage {
	if u == nil {
		return nil
	}
	usage := &ChatUsage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
	usage.PromptTokensDetails = promptDetailsFromResponses(u.InputTokensDetails)
	if u.CacheCreationInputTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &ChatTokenDetails{}
		}
		if usage.PromptTokensDetails.CacheWriteTokens == 0 && usage.PromptTokensDetails.CacheCreationTokens == 0 {
			usage.PromptTokensDetails.CacheCreationTokens = u.CacheCreationInputTokens
		}
	}
	usage.CompletionTokensDetails = completionDetailsFromResponses(u.OutputTokensDetails)
	return usage
}

// promptDetailsFromResponses 将 Responses API 的 input_tokens_details 映射为
// Chat Completions 的 prompt_tokens_details；没有可输出字段时返回 nil，
// 避免不拆分 prompt usage 的上游输出空明细。
func promptDetailsFromResponses(src *ResponsesInputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	if src.CachedTokens == 0 && src.AudioTokens == 0 && src.CacheCreationTokens == 0 && src.CacheWriteTokens == 0 {
		return nil
	}
	return &ChatTokenDetails{
		CachedTokens:        src.CachedTokens,
		AudioTokens:         src.AudioTokens,
		CacheCreationTokens: src.CacheCreationTokens,
		CacheWriteTokens:    src.CacheWriteTokens,
	}
}

// completionDetailsFromResponses 将 Responses API 的 output_tokens_details 映射为
// Chat Completions 的 completion_tokens_details；字段集合对齐 OpenAI 官方
// CompletionUsage schema，包括 reasoning_tokens、audio_tokens 以及预测输出的
// accepted/rejected 计数。没有可输出字段时返回 nil，避免污染非推理、非音频响应。
func completionDetailsFromResponses(src *ResponsesOutputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	if src.ReasoningTokens == 0 && src.AudioTokens == 0 &&
		src.AcceptedPredictionTokens == 0 && src.RejectedPredictionTokens == 0 {
		return nil
	}
	return &ChatTokenDetails{
		ReasoningTokens:          src.ReasoningTokens,
		AudioTokens:              src.AudioTokens,
		AcceptedPredictionTokens: src.AcceptedPredictionTokens,
		RejectedPredictionTokens: src.RejectedPredictionTokens,
	}
}

func makeChatDeltaChunk(state *ResponsesEventToChatState, delta ChatDelta) ChatCompletionsChunk {
	return ChatCompletionsChunk{
		ID:      state.ID,
		Object:  "chat.completion.chunk",
		Created: state.Created,
		Model:   state.Model,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: nil,
		}},
	}
}

func makeChatFinishChunk(state *ResponsesEventToChatState, finishReason string) ChatCompletionsChunk {
	empty := ""
	return ChatCompletionsChunk{
		ID:      state.ID,
		Object:  "chat.completion.chunk",
		Created: state.Created,
		Model:   state.Model,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        ChatDelta{Content: &empty},
			FinishReason: &finishReason,
		}},
	}
}

// generateChatCmplID returns a "chatcmpl-" prefixed random hex ID.
func generateChatCmplID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// BufferedResponseAccumulator: accumulates SSE delta events for non-streaming
// paths where the terminal event may have empty output.
// ---------------------------------------------------------------------------

type bufferedFuncCall struct {
	Type      string
	CallID    string
	Name      string
	Namespace string
	Args      strings.Builder
}

// BufferedResponseAccumulator collects content from Responses SSE delta events
// so that non-streaming handlers can reconstruct output when the terminal event
// (response.completed / response.done) carries an empty output array.
type BufferedResponseAccumulator struct {
	text                 strings.Builder
	reasoning            strings.Builder
	funcCalls            []bufferedFuncCall
	outputIndexToFuncIdx map[int]int
}

// NewBufferedResponseAccumulator returns an initialised accumulator.
func NewBufferedResponseAccumulator() *BufferedResponseAccumulator {
	return &BufferedResponseAccumulator{
		outputIndexToFuncIdx: make(map[int]int),
	}
}

// ProcessEvent inspects a single Responses SSE event and accumulates any
// content it carries. Only delta events that contribute to the final output
// are handled; all other event types are silently ignored.
func (a *BufferedResponseAccumulator) ProcessEvent(event *ResponsesStreamEvent) {
	switch event.Type {
	case "response.output_text.delta":
		if event.Delta != "" {
			_, _ = a.text.WriteString(event.Delta)
		}
	case "response.output_item.added":
		if _, _, ok := responsesOutputChatToolFields(event.Item); ok {
			idx := len(a.funcCalls)
			a.outputIndexToFuncIdx[event.OutputIndex] = idx
			a.funcCalls = append(a.funcCalls, bufferedFuncCall{
				Type:      event.Item.Type,
				CallID:    event.Item.CallID,
				Name:      event.Item.Name,
				Namespace: event.Item.Namespace,
			})
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if event.Delta != "" {
			if idx, ok := a.outputIndexToFuncIdx[event.OutputIndex]; ok {
				_, _ = a.funcCalls[idx].Args.WriteString(event.Delta)
			}
		}
	case "response.function_call_arguments.done":
		a.processToolDone(event, "function_call", "", event.Name, event.CallID, event.Arguments)
	case "response.custom_tool_call_input.done":
		a.processToolDone(event, "custom_tool_call", "", event.Name, event.CallID, event.Input)
	case "response.output_item.done":
		if _, arguments, ok := responsesOutputChatToolFields(event.Item); ok {
			a.processToolDone(event, event.Item.Type, event.Item.Namespace, event.Item.Name, event.Item.CallID, arguments)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if event.Delta != "" {
			_, _ = a.reasoning.WriteString(event.Delta)
		}
	}
}

func (a *BufferedResponseAccumulator) processToolDone(event *ResponsesStreamEvent, itemType, namespace, name, callID, arguments string) {
	idx, ok := a.outputIndexToFuncIdx[event.OutputIndex]
	if !ok {
		idx = len(a.funcCalls)
		a.outputIndexToFuncIdx[event.OutputIndex] = idx
		a.funcCalls = append(a.funcCalls, bufferedFuncCall{Type: itemType})
	}
	if a.funcCalls[idx].Type == "" {
		a.funcCalls[idx].Type = itemType
	}
	if a.funcCalls[idx].Namespace == "" {
		a.funcCalls[idx].Namespace = namespace
	}
	if callID != "" && a.funcCalls[idx].CallID == "" {
		a.funcCalls[idx].CallID = callID
	}
	if name != "" && a.funcCalls[idx].Name == "" {
		a.funcCalls[idx].Name = name
	}
	if arguments != "" && a.funcCalls[idx].Args.Len() == 0 {
		_, _ = a.funcCalls[idx].Args.WriteString(arguments)
	}
}

// HasContent reports whether any content has been accumulated.
func (a *BufferedResponseAccumulator) HasContent() bool {
	return a.text.Len() > 0 || len(a.funcCalls) > 0 || a.reasoning.Len() > 0
}

// BuildOutput constructs a []ResponsesOutput from the accumulated delta
// content. The order matches what ResponsesToChatCompletions expects:
// reasoning → message → function_calls.
func (a *BufferedResponseAccumulator) BuildOutput() []ResponsesOutput {
	var out []ResponsesOutput

	if a.reasoning.Len() > 0 {
		out = append(out, ResponsesOutput{
			Type: "reasoning",
			Summary: []ResponsesSummary{{
				Type: "summary_text",
				Text: a.reasoning.String(),
			}},
		})
	}

	if a.text.Len() > 0 {
		out = append(out, ResponsesOutput{
			Type: "message",
			Role: "assistant",
			Content: []ResponsesContentPart{{
				Type: "output_text",
				Text: a.text.String(),
			}},
		})
	}

	for i := range a.funcCalls {
		call := a.funcCalls[i]
		switch call.Type {
		case "custom_tool_call":
			out = append(out, ResponsesOutput{Type: call.Type, CallID: call.CallID, Name: call.Name, Input: call.Args.String()})
		case "tool_search_call":
			out = append(out, ResponsesOutput{Type: call.Type, CallID: call.CallID, Arguments: call.Args.String()})
		default:
			out = append(out, ResponsesOutput{Type: "function_call", CallID: call.CallID, Name: call.Name, Namespace: call.Namespace, Arguments: call.Args.String()})
		}
	}

	return out
}

// SupplementResponseOutput fills resp.Output from accumulated delta content
// when the terminal event delivered an empty output array. If resp.Output is
// already populated, this is a no-op (preserves backward compatibility).
func (a *BufferedResponseAccumulator) SupplementResponseOutput(resp *ResponsesResponse) {
	if resp == nil || len(resp.Output) > 0 {
		return
	}
	if !a.HasContent() {
		return
	}
	resp.Output = a.BuildOutput()
}
