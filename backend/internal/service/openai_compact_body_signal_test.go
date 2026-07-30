//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasCompactionTriggerInInput_DetectsCompactSignal(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"stream":true,
		"input":[
			{"type":"message","role":"user","content":"hello"},
			{"type":"compaction_trigger"}
		]
	}`)
	require.True(t, HasCompactionTriggerInInput(body))
}

func TestHasCompactionTriggerInInput_NoTrigger(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"input":[
			{"type":"message","role":"user","content":"hello"}
		]
	}`)
	require.False(t, HasCompactionTriggerInInput(body))
}

func TestHasCompactionTriggerInInput_EmptyInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[]}`)
	require.False(t, HasCompactionTriggerInInput(body))
}

func TestHasCompactionTriggerInInput_NoInputField(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5"}`)
	require.False(t, HasCompactionTriggerInInput(body))
}

func TestHasCompactionTriggerInInput_EmptyBody(t *testing.T) {
	require.False(t, HasCompactionTriggerInInput(nil))
	require.False(t, HasCompactionTriggerInInput([]byte{}))
}

func TestHasCompactionTriggerInInput_StringInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"compaction_trigger"}`)
	require.False(t, HasCompactionTriggerInInput(body))
}

func TestHasCompactionTriggerInInput_CompactTriggerOnly(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"compaction_trigger"}]}`)
	require.True(t, HasCompactionTriggerInInput(body))
}

func TestIsOpenAINativeRemoteCompactionV2Request_RequiresFinalTrigger(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.5",
		"stream":true,
		"input":[
			{"type":"compaction_trigger"},
			{"type":"message","role":"user","content":"must remain last"}
		]
	}`)
	require.True(t, HasCompactionTriggerInInput(body), "legacy bridge detection remains broad")
	require.False(t, IsOpenAINativeRemoteCompactionV2Request(body, []string{"remote_compaction_v2"}))
}

func TestIsOpenAINativeRemoteCompactionV2Request_StrictMatrix(t *testing.T) {
	validBody := []byte(`{"stream":true,"input":[{"type":"message"},{"type":"compaction_trigger"}]}`)
	tests := []struct {
		name        string
		body        []byte
		betaHeaders []string
		want        bool
	}{
		{
			name:        "valid",
			body:        validBody,
			betaHeaders: []string{"responses_websockets_v2, remote_compaction_v2"},
			want:        true,
		},
		{
			name:        "missing_input",
			body:        []byte(`{"stream":true}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "empty_input",
			body:        []byte(`{"stream":true,"input":[]}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "last_item_not_trigger",
			body:        []byte(`{"stream":true,"input":[{"type":"compaction_trigger"},{"type":"message"}]}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "last_item_not_object",
			body:        []byte(`{"stream":true,"input":[{"type":"message"},"compaction_trigger"]}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "stream_missing",
			body:        []byte(`{"input":[{"type":"compaction_trigger"}]}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "stream_string_false",
			body:        []byte(`{"stream":"false","input":[{"type":"compaction_trigger"}]}`),
			betaHeaders: []string{"remote_compaction_v2"},
		},
		{
			name:        "missing_beta",
			body:        validBody,
			betaHeaders: nil,
		},
		{
			name:        "wrong_case_beta",
			body:        validBody,
			betaHeaders: []string{"REMOTE_COMPACTION_V2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsOpenAINativeRemoteCompactionV2Request(tt.body, tt.betaHeaders))
		})
	}
}
