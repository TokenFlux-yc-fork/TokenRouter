package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyOpenAIWSRetryPayloadStrategy_KeepPromptCacheKey(t *testing.T) {
	payload := map[string]any{
		"model":            "gpt-5.3-codex",
		"prompt_cache_key": "pcache_123",
		"include":          []any{"reasoning.encrypted_content"},
		"text": map[string]any{
			"verbosity": "low",
		},
		"tools": []any{map[string]any{"type": "function"}},
	}

	strategy, removed := applyOpenAIWSRetryPayloadStrategy(payload, 3)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_123", payload["prompt_cache_key"])
	require.NotContains(t, payload, "include")
	require.Contains(t, payload, "text")
}

func TestApplyOpenAIWSRetryPayloadStrategy_AttemptSixKeepsSemanticFields(t *testing.T) {
	payload := map[string]any{
		"prompt_cache_key":    "pcache_456",
		"instructions":        "long instructions",
		"tools":               []any{map[string]any{"type": "function"}},
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"include":             []any{"reasoning.encrypted_content"},
		"text":                map[string]any{"verbosity": "high"},
	}

	strategy, removed := applyOpenAIWSRetryPayloadStrategy(payload, 6)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_456", payload["prompt_cache_key"])
	require.Contains(t, payload, "instructions")
	require.Contains(t, payload, "tools")
	require.Contains(t, payload, "tool_choice")
	require.Contains(t, payload, "parallel_tool_calls")
	require.Contains(t, payload, "text")
}

func TestApplyOpenAIWSRetryPayloadStrategyForRequest_NativeCompactionKeepsInclude(t *testing.T) {
	payload := map[string]any{
		"previous_response_id": "resp_native",
		"include":              []any{"reasoning.encrypted_content"},
		"input": []any{
			map[string]any{"type": "reasoning", "encrypted_content": "fixture-state"},
			map[string]any{"type": "compaction_trigger"},
		},
	}

	strategy, removed := applyOpenAIWSRetryPayloadStrategyForRequest(payload, 3, true)
	require.Equal(t, "full", strategy)
	require.Empty(t, removed)
	require.Equal(t, "resp_native", payload["previous_response_id"])
	require.Contains(t, payload, "include")
	input, ok := payload["input"].([]any)
	require.True(t, ok)
	firstInput, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fixture-state", firstInput["encrypted_content"])
}
