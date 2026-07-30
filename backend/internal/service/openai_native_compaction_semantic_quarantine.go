package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const openAINativeCompactionQuarantineWriteTimeout = 2 * time.Second

// openAINativeCompactionSemanticError carries only the bounded semantic outcome.
// It deliberately cannot retain an upstream payload or encrypted_content.
type openAINativeCompactionSemanticError struct {
	outcome OpenAINativeCompactionOutcome
}

type openAINativeCompactionContinuationError struct {
	code string
}

func (e *openAINativeCompactionContinuationError) Error() string {
	if e == nil || e.code == "" {
		return "OpenAI native compaction continuation rejected"
	}
	return "OpenAI native compaction continuation rejected: " + e.code
}

func openAINativeCompactionContinuationErrorCode(code string) string {
	code = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(code)), "prewarm_")
	switch code {
	case "invalid_encrypted_content", "previous_response_not_found":
		return code
	default:
		return ""
	}
}

func newOpenAINativeCompactionContinuationError(code string) error {
	return &openAINativeCompactionContinuationError{
		code: openAINativeCompactionContinuationErrorCode(code),
	}
}

func newOpenAINativeCompactionHTTPContinuationFailoverError(statusCode int, headers http.Header, code string, safeToFailoverAfterWrite bool) error {
	continuationErr := newOpenAINativeCompactionContinuationError(code)
	failoverErr := &UpstreamFailoverError{
		StatusCode:               statusCode,
		ResponseHeaders:          cloneHeader(headers),
		SafeToFailoverAfterWrite: safeToFailoverAfterWrite,
	}
	return errors.Join(failoverErr, continuationErr)
}

func openAINativeCompactionWSContinuationErrorCode(err error) string {
	var fallbackErr *openAIWSFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return ""
	}
	return openAINativeCompactionContinuationErrorCode(fallbackErr.Reason)
}

func (e *openAINativeCompactionSemanticError) Error() string {
	if e == nil {
		return "OpenAI native compaction semantic validation failed"
	}
	return "OpenAI native compaction semantic validation failed: " + string(e.outcome)
}

func newOpenAINativeCompactionSemanticError(outcome OpenAINativeCompactionOutcome) error {
	return &openAINativeCompactionSemanticError{outcome: outcome}
}

func openAINativeCompactionQuarantineOutcome(err error) (OpenAINativeCompactionOutcome, bool) {
	var semanticErr *openAINativeCompactionSemanticError
	if !errors.As(err, &semanticErr) || semanticErr == nil {
		return "", false
	}
	switch semanticErr.outcome {
	case OpenAINativeCompactionZeroCompaction,
		OpenAINativeCompactionMultipleCompaction,
		OpenAINativeCompactionMalformedCompaction,
		OpenAINativeCompactionDuplicateTerminal,
		OpenAINativeCompactionPostTerminalFrame,
		OpenAINativeCompactionInvalidEvent,
		OpenAINativeCompactionResourceLimit:
		return semanticErr.outcome, true
	default:
		return "", false
	}
}

func openAINativeCompactionStageSemanticError(err error) error {
	switch {
	case errors.Is(err, ErrOpenAINativeCompactionStageBytes),
		errors.Is(err, ErrOpenAINativeCompactionStageEvents),
		errors.Is(err, ErrOpenAINativeCompactionStageBudget):
		return newOpenAINativeCompactionSemanticError(OpenAINativeCompactionResourceLimit)
	default:
		return nil
	}
}

func joinOpenAINativeCompactionStageSemanticError(err error) error {
	if semanticErr := openAINativeCompactionStageSemanticError(err); semanticErr != nil {
		return errors.Join(err, semanticErr)
	}
	return err
}

func openAINativeCompactionFailureSafeToReplay(err error) bool {
	var semanticErr *openAINativeCompactionSemanticError
	var continuationErr *openAINativeCompactionContinuationError
	return errors.As(err, &semanticErr) ||
		errors.As(err, &continuationErr) ||
		errors.Is(err, ErrOpenAINativeCompactionStageBytes) ||
		errors.Is(err, ErrOpenAINativeCompactionStageEvents) ||
		errors.Is(err, ErrOpenAINativeCompactionStageDuration) ||
		errors.Is(err, ErrOpenAINativeCompactionStageBudget)
}

func (s *OpenAIGatewayService) newOpenAINativeCompactionWSFailoverError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	headers http.Header,
	cause error,
) error {
	message := "OpenAI native compaction attempt failed semantic validation"
	if cause != nil {
		message = cause.Error()
	}
	failoverErr := s.newOpenAIStreamFailoverError(c, account, passthrough, strings.TrimSpace(headers.Get("x-request-id")), nil, message)
	failoverErr.ResponseHeaders = cloneHeader(headers)
	failoverErr.SafeToFailoverAfterWrite = true
	if cause == nil {
		return failoverErr
	}
	return errors.Join(failoverErr, cause)
}

func (s *OpenAIGatewayService) quarantineOpenAINativeCompactionFailureForRoutingModel(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	routingModel string,
	attemptErr error,
) {
	domain, err := ResolveOpenAINativeCompatibilityDomainForRequest(ctx, account, routingModel)
	if err != nil {
		return
	}
	s.quarantineOpenAINativeCompactionFailure(ctx, c, account, domain.EffectiveModel, attemptErr)
}

// quarantineOpenAINativeCompactionFailure persists an exact-key quarantine at
// the gateway error boundary. Persistence is best-effort: the original
// failover error always remains authoritative.
func (s *OpenAIGatewayService) quarantineOpenAINativeCompactionFailure(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	effectiveModel string,
	attemptErr error,
) {
	outcome, ok := openAINativeCompactionQuarantineOutcome(attemptErr)
	if !ok || s == nil || s.openAINativeCompactionCapabilityRepo == nil || account == nil {
		return
	}
	if native, _ := OpenAINativeRemoteCompactionV2FromContext(ctx); !native {
		return
	}
	key, err := ResolveOpenAINativeCompactionCapabilityKey(account, effectiveModel)
	if err != nil {
		s.recordOpenAINativeCompactionQuarantineWriteFailure(c, account, OpenAINativeCompactionCapabilityKey{}, outcome, "invalid_exact_key", err)
		return
	}
	seconds := 0
	if s.cfg != nil {
		seconds = s.cfg.Gateway.OpenAINativeCompaction.SemanticQuarantineSeconds
	}
	if seconds <= 0 {
		return
	}
	until := time.Now().Add(time.Duration(seconds) * time.Second)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAINativeCompactionQuarantineWriteTimeout)
	defer cancel()
	updated, err := s.openAINativeCompactionCapabilityRepo.SetQuarantine(writeCtx, key, &until, string(outcome))
	if err != nil || !updated {
		reason := "repository_error"
		if err == nil {
			reason = "exact_key_not_found"
			err = errors.New(reason)
		}
		s.recordOpenAINativeCompactionQuarantineWriteFailure(c, account, key, outcome, reason, err)
	}
}

func (s *OpenAIGatewayService) recordOpenAINativeCompactionQuarantineWriteFailure(
	c *gin.Context,
	account *Account,
	key OpenAINativeCompactionCapabilityKey,
	outcome OpenAINativeCompactionOutcome,
	reason string,
	err error,
) {
	accountID := int64(0)
	platform := PlatformOpenAI
	if account != nil {
		accountID = account.ID
		platform = account.Platform
	}
	slog.Error("openai_native_compaction_quarantine_write_failed",
		"account_id", accountID,
		"upstream_fingerprint", key.UpstreamFingerprint,
		"effective_model", strings.TrimSpace(key.EffectiveModel),
		"contract_version", strings.TrimSpace(key.ContractVersion),
		"semantic_outcome", outcome,
		"reason", reason,
		"error", err,
	)
	if c != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:  platform,
			AccountID: accountID,
			Kind:      "semantic_quarantine_persist_error",
			Message:   "native compaction semantic quarantine persistence failed",
			Detail:    reason,
		})
	}
}
