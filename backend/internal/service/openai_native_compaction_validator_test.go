package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAINativeCompactionValidatorFixtures(t *testing.T) {
	tests := []struct {
		name    string
		outcome OpenAINativeCompactionOutcome
		valid   bool
	}{
		{name: "valid_one", outcome: OpenAINativeCompactionValid, valid: true},
		{name: "zero_compaction", outcome: OpenAINativeCompactionZeroCompaction},
		{name: "two_compactions", outcome: OpenAINativeCompactionMultipleCompaction},
		{name: "malformed_compaction", outcome: OpenAINativeCompactionMalformedCompaction},
		{name: "added_only", outcome: OpenAINativeCompactionZeroCompaction},
		{name: "terminal_output_only", outcome: OpenAINativeCompactionZeroCompaction},
		{name: "failed_terminal", outcome: OpenAINativeCompactionFailedTerminal},
		{name: "incomplete_eof", outcome: OpenAINativeCompactionIncompleteStream},
		{name: "duplicate_terminal", outcome: OpenAINativeCompactionDuplicateTerminal},
		{name: "post_terminal_frame", outcome: OpenAINativeCompactionPostTerminalFrame},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := NewOpenAINativeCompactionValidator()
			forEachOpenAISSEDataPayload(readOpenAINativeCompactionFixture(t, tt.name), func(payload []byte) {
				validator.Observe(payload)
			})
			result := validator.Finish()
			require.Equal(t, tt.outcome, result.Outcome)
			require.Equal(t, tt.valid, result.Valid())
			if tt.valid {
				require.Equal(t, 1, result.CompactionItemCount)
				require.Zero(t, result.MalformedItemCount)
				require.Equal(t, "response.completed", result.TerminalEvent)
			}
		})
	}
}

func TestOpenAINativeCompactionValidatorHandlesDoneSentinelOrdering(t *testing.T) {
	compaction := []byte(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"x"}}`)
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_done_order","status":"completed"}}`)

	tests := []struct {
		name     string
		payloads [][]byte
		outcome  OpenAINativeCompactionOutcome
	}{
		{name: "done_only", payloads: [][]byte{[]byte("[DONE]")}, outcome: OpenAINativeCompactionIncompleteStream},
		{name: "done_before_output", payloads: [][]byte{[]byte("[DONE]"), compaction, completed}, outcome: OpenAINativeCompactionPostTerminalFrame},
		{name: "done_before_terminal", payloads: [][]byte{compaction, []byte("[DONE]"), completed}, outcome: OpenAINativeCompactionPostTerminalFrame},
		{name: "done_after_terminal", payloads: [][]byte{compaction, completed, []byte("[DONE]")}, outcome: OpenAINativeCompactionValid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := NewOpenAINativeCompactionValidator()
			for _, payload := range tt.payloads {
				validator.Observe(payload)
			}
			result := validator.Finish()
			require.Equal(t, tt.outcome, result.Outcome)
			if result.Valid() {
				require.Equal(t, 1, result.TerminalCount)
				require.Equal(t, "response.completed", result.TerminalEvent)
			}
		})
	}
}

func TestOpenAINativeCompactionValidatorIgnoresAddedAndTerminalOutput(t *testing.T) {
	for _, name := range []string{"added_only", "terminal_output_only"} {
		t.Run(name, func(t *testing.T) {
			validator := NewOpenAINativeCompactionValidator()
			forEachOpenAISSEDataPayload(readOpenAINativeCompactionFixture(t, name), func(payload []byte) {
				validator.Observe(payload)
			})
			result := validator.Finish()
			require.Equal(t, OpenAINativeCompactionZeroCompaction, result.Outcome)
			require.Zero(t, result.CompactionItemCount)
		})
	}
}

func TestOpenAINativeCompactionValidatorAcceptsAliasAndOrdinaryDoneItems(t *testing.T) {
	validator := NewOpenAINativeCompactionValidator()
	validator.Observe([]byte(`{"type":"response.output_item.done","item":{"type":"message","status":"completed"}}`))
	validator.Observe([]byte(`{"type":"response.output_item.done","item":{"type":"compaction_summary","status":"completed","encrypted_content":"synthetic-state"}}`))
	validator.Observe([]byte(`{"type":"response.completed","response":{"id":"resp_alias","status":"completed"}}`))

	result := validator.Finish()
	require.True(t, result.Valid())
	require.Equal(t, 2, result.OutputItemDoneCount)
	require.Equal(t, 1, result.CompactionItemCount)
	require.Equal(t, 1, result.ItemTypeCounts["message"])
	require.Equal(t, 1, result.ItemTypeCounts["compaction_summary"])
}

func TestOpenAINativeCompactionValidatorRejectsOversizedEncryptedContent(t *testing.T) {
	validator := NewOpenAINativeCompactionValidator()
	validator.maxEncryptedContentSize = 4
	validator.Observe([]byte(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"12345"}}`))
	validator.Observe([]byte(`{"type":"response.completed","response":{"id":"resp_oversized","status":"completed"}}`))

	result := validator.Finish()
	require.Equal(t, OpenAINativeCompactionMalformedCompaction, result.Outcome)
	require.Equal(t, 1, result.CompactionItemCount)
	require.Equal(t, 1, result.MalformedItemCount)
}

func TestOpenAINativeCompactionValidatorRequiresCodexCompletedTerminal(t *testing.T) {
	compaction := []byte(`{"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"x"}}`)
	tests := []struct {
		name     string
		terminal string
		outcome  OpenAINativeCompactionOutcome
	}{
		{
			name:     "response done is not a Codex Responses terminal",
			terminal: `{"type":"response.done","response":{"id":"resp_done","status":"completed"}}`,
			outcome:  OpenAINativeCompactionIncompleteStream,
		},
		{
			name:     "completed response id is required",
			terminal: `{"type":"response.completed","response":{"status":"completed"}}`,
			outcome:  OpenAINativeCompactionInvalidEvent,
		},
		{
			name:     "completed usage must match Codex shape",
			terminal: `{"type":"response.completed","response":{"id":"resp_bad_usage","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`,
			outcome:  OpenAINativeCompactionInvalidEvent,
		},
		{
			name:     "completed without optional usage",
			terminal: `{"type":"response.completed","response":{"id":"resp_no_usage","status":"completed"}}`,
			outcome:  OpenAINativeCompactionValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := NewOpenAINativeCompactionValidator()
			validator.Observe(compaction)
			validator.Observe([]byte(tt.terminal))
			require.Equal(t, tt.outcome, validator.Finish().Outcome)
		})
	}
}

func TestOpenAINativeCompactionValidatorRejectsCompactionCodexCannotDecode(t *testing.T) {
	for _, item := range []string{
		`{"type":"compaction","id":42,"encrypted_content":"x"}`,
		`{"type":"compaction","encrypted_content":"x","internal_chat_message_metadata_passthrough":"invalid"}`,
		`{"type":"compaction","encrypted_content":"x","internal_chat_message_metadata_passthrough":{"turn_id":42}}`,
	} {
		validator := NewOpenAINativeCompactionValidator()
		validator.Observe([]byte(`{"type":"response.output_item.done","item":` + item + `}`))
		validator.Observe([]byte(`{"type":"response.completed","response":{"id":"resp_bad_item","status":"completed"}}`))
		result := validator.Finish()
		require.Equal(t, OpenAINativeCompactionMalformedCompaction, result.Outcome)
		require.Equal(t, 1, result.MalformedItemCount)
	}
}
