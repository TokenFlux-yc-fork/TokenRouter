package service

import "strings"

// OpenAIOpsTelemetry is a bounded, payload-free view of one upstream attempt.
// It intentionally has no fields for credentials, request input/context, raw
// stages, encrypted content, response payloads, or upstream URLs.
type OpenAIOpsTelemetry struct {
	ClientRequestID         ClientRequestID               `json:"client_request_id,omitempty"`
	GatewayRequestID        GatewayRequestID              `json:"gateway_request_id,omitempty"`
	AttemptID               AttemptID                     `json:"attempt_id,omitempty"`
	UpstreamRequestID       UpstreamRequestID             `json:"upstream_request_id,omitempty"`
	ResponseID              string                        `json:"response_id,omitempty"`
	WSConnectionID          WSConnectionID                `json:"ws_connection_id,omitempty"`
	WSTurnID                WSTurnID                      `json:"ws_turn_id,omitempty"`
	Transport               UpstreamAttemptTransport      `json:"transport,omitempty"`
	AccountType             string                        `json:"account_type,omitempty"`
	FinalAccountType        string                        `json:"final_account_type,omitempty"`
	CapabilitySource        string                        `json:"capability_source,omitempty"`
	UpstreamFingerprint     OpenAIUpstreamFingerprint     `json:"upstream_fingerprint,omitempty"`
	RequestedModel          string                        `json:"requested_model,omitempty"`
	MappedModel             string                        `json:"mapped_model,omitempty"`
	Source                  string                        `json:"source,omitempty"`
	OutputItemDoneCount     int                           `json:"output_item_done_count,omitempty"`
	CompactionItemCount     int                           `json:"compaction_item_count,omitempty"`
	MalformedItemCount      int                           `json:"malformed_item_count,omitempty"`
	TerminalEvent           string                        `json:"terminal_event,omitempty"`
	TerminalEventCount      int                           `json:"terminal_event_count,omitempty"`
	Outcome                 OpenAINativeCompactionOutcome `json:"outcome,omitempty"`
	SafeToFailover          bool                          `json:"safe_to_failover"`
	FailoverCount           int                           `json:"failover_count"`
	Committed               bool                          `json:"committed"`
	SemanticOutputCommitted bool                          `json:"semantic_output_committed"`
	AttributionPersisted    bool                          `json:"attribution_persisted"`
	UsageObserved           bool                          `json:"usage_observed"`
	Usage                   UpstreamAttemptUsage          `json:"usage"`
}

// OpsTelemetry returns only allowlisted scalar attribution. Fingerprints are
// accepted only in their opaque hashed form; raw or malformed values fail closed.
func (r *OpenAIForwardResult) OpsTelemetry() OpenAIOpsTelemetry {
	if r == nil {
		return OpenAIOpsTelemetry{}
	}
	fingerprint := r.UpstreamFingerprint
	if !strings.HasPrefix(string(fingerprint), "upstream_v1_") {
		fingerprint = ""
	}
	usage := UpstreamAttemptUsage{Observed: r.UpstreamUsageObserved}
	if r.UpstreamUsageObserved {
		usage.InputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.InputTokens))
		usage.OutputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.OutputTokens))
		usage.CacheCreationInputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.CacheCreationInputTokens))
		usage.CacheReadInputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.CacheReadInputTokens))
		usage.ImageInputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.ImageInputTokens))
		usage.ImageOutputTokens = openAIOpsObservedInt64Ptr(int64(r.Usage.ImageOutputTokens))
	}
	return OpenAIOpsTelemetry{
		ClientRequestID:         r.ClientRequestID,
		GatewayRequestID:        r.GatewayRequestID,
		AttemptID:               r.AttemptID,
		UpstreamRequestID:       r.UpstreamRequestID,
		ResponseID:              strings.TrimSpace(r.ResponseID),
		WSConnectionID:          r.WSConnectionID,
		WSTurnID:                r.WSTurnID,
		Transport:               r.Transport,
		AccountType:             strings.TrimSpace(r.AccountType),
		FinalAccountType:        strings.TrimSpace(r.AccountType),
		CapabilitySource:        strings.TrimSpace(r.CapabilitySource),
		UpstreamFingerprint:     fingerprint,
		RequestedModel:          strings.TrimSpace(r.Model),
		MappedModel:             strings.TrimSpace(r.UpstreamModel),
		Source:                  strings.TrimSpace(r.SemanticSource),
		OutputItemDoneCount:     r.OutputItemDoneCount,
		CompactionItemCount:     r.CompactionItemCount,
		MalformedItemCount:      r.MalformedItemCount,
		TerminalEvent:           strings.TrimSpace(r.UpstreamTerminalEvent),
		TerminalEventCount:      r.TerminalEventCount,
		Outcome:                 r.SemanticOutcome,
		SafeToFailover:          r.SafeToFailover,
		FailoverCount:           r.FailoverCount,
		Committed:               r.DeliveryCommitted,
		SemanticOutputCommitted: r.DeliveryCommitted,
		AttributionPersisted:    r.AttributionPersisted,
		UsageObserved:           r.UpstreamUsageObserved,
		Usage:                   usage,
	}
}

func openAIOpsObservedInt64Ptr(value int64) *int64 {
	return &value
}
