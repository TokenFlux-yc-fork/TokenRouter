package repository

import "testing"

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CompactCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_compact_supported":  true,
		"openai_compact_checked_at": "2026-04-10T10:00:00Z",
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected compact capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_OpenAIResponsesCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_responses_mode":      "force_chat_completions",
		"openai_responses_supported": false,
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected responses capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_QoderQuotaSnapshotIsNeutral(t *testing.T) {
	updates := map[string]any{
		"qoder_quota_snapshot": map[string]any{
			"user_type": "teams",
			"user_quota": map[string]any{
				"total":     2940,
				"used":      2,
				"remaining": 2938,
			},
		},
		"qoder_quota_updated_at": "2026-07-05T10:00:00Z",
	}

	if shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected qoder quota snapshot updates to skip scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CodexWindowTimestampsAreNeutral(t *testing.T) {
	updates := map[string]any{
		"codex_5h_updated_at":             "2026-07-14T00:00:00Z",
		"codex_window_timestamps_version": 1,
	}

	if shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected Codex window timestamp updates to skip scheduler outbox")
	}
}
