package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

type OpenAINativeCompactionOutcome string

const (
	OpenAINativeCompactionPending             OpenAINativeCompactionOutcome = "pending"
	OpenAINativeCompactionValid               OpenAINativeCompactionOutcome = "valid"
	OpenAINativeCompactionZeroCompaction      OpenAINativeCompactionOutcome = "zero_compaction"
	OpenAINativeCompactionMultipleCompaction  OpenAINativeCompactionOutcome = "multiple_compaction"
	OpenAINativeCompactionMalformedCompaction OpenAINativeCompactionOutcome = "malformed_compaction"
	OpenAINativeCompactionFailedTerminal      OpenAINativeCompactionOutcome = "failed_terminal"
	OpenAINativeCompactionIncompleteStream    OpenAINativeCompactionOutcome = "incomplete_stream"
	OpenAINativeCompactionDuplicateTerminal   OpenAINativeCompactionOutcome = "duplicate_terminal"
	OpenAINativeCompactionPostTerminalFrame   OpenAINativeCompactionOutcome = "post_terminal_frame"
	OpenAINativeCompactionInvalidEvent        OpenAINativeCompactionOutcome = "invalid_event"
)

type OpenAINativeCompactionValidationResult struct {
	Outcome             OpenAINativeCompactionOutcome
	OutputItemDoneCount int
	CompactionItemCount int
	MalformedItemCount  int
	TerminalEvent       string
	TerminalCount       int
	SuccessfulTerminal  bool
	ItemTypeCounts      map[string]int
}

func (r OpenAINativeCompactionValidationResult) Valid() bool {
	return r.Outcome == OpenAINativeCompactionValid
}

// OpenAINativeCompactionValidator observes transport-independent Responses
// event payloads. It deliberately ignores output_item.added and terminal
// response.output reconstruction used by the legacy compact bridge.
const openAINativeCompactionEncryptedContentMaxBytes = 16 << 20

type OpenAINativeCompactionValidator struct {
	outputItemDoneCount     int
	compactionItemCount     int
	malformedItemCount      int
	terminalEvent           string
	terminalCount           int
	successfulTerminal      bool
	itemTypeCounts          map[string]int
	violation               OpenAINativeCompactionOutcome
	maxEncryptedContentSize int
	sawDoneSentinel         bool
}

func NewOpenAINativeCompactionValidator() *OpenAINativeCompactionValidator {
	return &OpenAINativeCompactionValidator{
		itemTypeCounts:          make(map[string]int),
		maxEncryptedContentSize: openAINativeCompactionEncryptedContentMaxBytes,
	}
}

func (v *OpenAINativeCompactionValidator) Observe(payload []byte) {
	if v == nil {
		return
	}
	if strings.TrimSpace(string(payload)) == "[DONE]" {
		v.sawDoneSentinel = true
		return
	}
	if v.sawDoneSentinel && v.terminalCount == 0 {
		v.recordViolation(OpenAINativeCompactionPostTerminalFrame)
		return
	}
	if !gjson.ValidBytes(payload) {
		v.recordViolation(OpenAINativeCompactionInvalidEvent)
		return
	}

	eventTypeValue := gjson.GetBytes(payload, "type")
	if eventTypeValue.Type != gjson.String {
		v.recordViolation(OpenAINativeCompactionInvalidEvent)
		return
	}
	eventType := strings.TrimSpace(eventTypeValue.String())
	if eventType == "" {
		v.recordViolation(OpenAINativeCompactionInvalidEvent)
		return
	}

	if v.terminalCount > 0 {
		if openAIStreamEventTypeIsTerminal(eventType) {
			v.terminalCount++
			v.recordViolation(OpenAINativeCompactionDuplicateTerminal)
		} else {
			v.recordViolation(OpenAINativeCompactionPostTerminalFrame)
		}
		return
	}

	if eventType == "response.output_item.done" {
		v.observeOutputItemDone(payload)
	}
	if openAINativeCompactionEventTypeIsTerminal(eventType) {
		v.terminalCount = 1
		v.terminalEvent = eventType
		v.successfulTerminal = eventType == "response.completed" && validOpenAINativeCompactionCompletedEvent(payload)
		if eventType == "response.completed" && !v.successfulTerminal {
			v.recordViolation(OpenAINativeCompactionInvalidEvent)
		}
	}
}

func (v *OpenAINativeCompactionValidator) observeOutputItemDone(payload []byte) {
	v.outputItemDoneCount++
	item := gjson.GetBytes(payload, "item")
	itemTypeValue := item.Get("type")
	itemType := strings.TrimSpace(itemTypeValue.String())
	if itemType == "" {
		itemType = "<missing>"
	}
	v.itemTypeCounts[itemType]++
	if !isResponsesCompactionItemType(itemType) {
		return
	}

	v.compactionItemCount++
	encryptedContent := item.Get("encrypted_content")
	encryptedContentValue := encryptedContent.String()
	if !item.IsObject() || itemTypeValue.Type != gjson.String ||
		encryptedContent.Type != gjson.String || strings.TrimSpace(encryptedContentValue) == "" ||
		len(encryptedContentValue) > v.maxEncryptedContentSize ||
		!validOpenAINativeCompactionItem(item.Raw) {
		v.malformedItemCount++
	}
}

func openAINativeCompactionEventTypeIsTerminal(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func validOpenAINativeCompactionItem(raw string) bool {
	var item struct {
		ID               *string `json:"id"`
		EncryptedContent *string `json:"encrypted_content"`
		Metadata         *struct {
			TurnID *string `json:"turn_id"`
		} `json:"internal_chat_message_metadata_passthrough"`
	}
	if err := json.Unmarshal([]byte(raw), &item); err != nil || item.EncryptedContent == nil {
		return false
	}
	return true
}

func validOpenAINativeCompactionCompletedEvent(payload []byte) bool {
	type usageDetails struct {
		CachedTokens *int64 `json:"cached_tokens"`
	}
	type outputDetails struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	}
	type usage struct {
		InputTokens        *int64         `json:"input_tokens"`
		InputTokensDetails *usageDetails  `json:"input_tokens_details"`
		OutputTokens       *int64         `json:"output_tokens"`
		OutputTokenDetails *outputDetails `json:"output_tokens_details"`
		TotalTokens        *int64         `json:"total_tokens"`
	}
	var event struct {
		Response *struct {
			ID      *string `json:"id"`
			Status  *string `json:"status"`
			Error   any     `json:"error"`
			Usage   *usage  `json:"usage"`
			EndTurn *bool   `json:"end_turn"`
		} `json:"response"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Response == nil ||
		event.Response.ID == nil || strings.TrimSpace(*event.Response.ID) == "" ||
		event.Error != nil || event.Response.Error != nil {
		return false
	}
	if event.Response.Status != nil && !strings.EqualFold(strings.TrimSpace(*event.Response.Status), "completed") {
		return false
	}
	if event.Response.Usage == nil {
		return true
	}
	u := event.Response.Usage
	if u.InputTokens == nil || u.OutputTokens == nil || u.TotalTokens == nil {
		return false
	}
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens == nil {
		return false
	}
	return u.OutputTokenDetails == nil || u.OutputTokenDetails.ReasoningTokens != nil
}

func (v *OpenAINativeCompactionValidator) Finish() OpenAINativeCompactionValidationResult {
	if v == nil {
		return OpenAINativeCompactionValidationResult{Outcome: OpenAINativeCompactionIncompleteStream}
	}
	result := v.result()
	if v.violation != "" {
		result.Outcome = v.violation
		return result
	}
	if v.malformedItemCount > 0 {
		result.Outcome = OpenAINativeCompactionMalformedCompaction
	} else if v.compactionItemCount > 1 {
		result.Outcome = OpenAINativeCompactionMultipleCompaction
	} else if v.terminalCount == 0 {
		result.Outcome = OpenAINativeCompactionIncompleteStream
	} else if !v.successfulTerminal {
		result.Outcome = OpenAINativeCompactionFailedTerminal
	} else if v.compactionItemCount == 0 {
		result.Outcome = OpenAINativeCompactionZeroCompaction
	} else {
		result.Outcome = OpenAINativeCompactionValid
	}
	return result
}

func (v *OpenAINativeCompactionValidator) Result() OpenAINativeCompactionValidationResult {
	if v == nil {
		return OpenAINativeCompactionValidationResult{Outcome: OpenAINativeCompactionIncompleteStream}
	}
	if v.terminalCount == 0 && v.violation == "" {
		result := v.result()
		result.Outcome = OpenAINativeCompactionPending
		return result
	}
	return v.Finish()
}

func (v *OpenAINativeCompactionValidator) result() OpenAINativeCompactionValidationResult {
	itemTypeCounts := make(map[string]int, len(v.itemTypeCounts))
	for itemType, count := range v.itemTypeCounts {
		itemTypeCounts[itemType] = count
	}
	return OpenAINativeCompactionValidationResult{
		OutputItemDoneCount: v.outputItemDoneCount,
		CompactionItemCount: v.compactionItemCount,
		MalformedItemCount:  v.malformedItemCount,
		TerminalEvent:       v.terminalEvent,
		TerminalCount:       v.terminalCount,
		SuccessfulTerminal:  v.successfulTerminal,
		ItemTypeCounts:      itemTypeCounts,
	}
}

func (v *OpenAINativeCompactionValidator) recordViolation(outcome OpenAINativeCompactionOutcome) {
	if v.violation == "" {
		v.violation = outcome
	}
}
