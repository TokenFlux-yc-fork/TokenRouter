package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	openAINativeCompactionProbeTimeout           = 15 * time.Second
	openAINativeCompactionProbeMaxBytes          = 16 << 20
	openAINativeCompactionProbeMaxEventBytes     = 1 << 20
	openAINativeCompactionProbeMaxEvents         = 4096
	openAINativeCompactionProbeSettlementTimeout = 5 * time.Second
)

var (
	ErrOpenAINativeCompactionProbeEventBytes    = errors.New("native compaction probe SSE event byte limit exceeded")
	ErrOpenAINativeCompactionProbeResponseBytes = errors.New("native compaction probe raw response byte limit exceeded")
)

const (
	OpenAINativeCompactionHTTPFailure      OpenAINativeCompactionOutcome = "http_failure"
	OpenAINativeCompactionTransportFailure OpenAINativeCompactionOutcome = "transport_failure"
	OpenAINativeCompactionResourceLimit    OpenAINativeCompactionOutcome = "resource_limit"
)

// OpenAINativeCompactionProbeAttemptResult is deliberately payload-free. In
// particular, it must never grow fields containing an upstream event, request
// body, or encrypted_content.
type OpenAINativeCompactionProbeAttemptResult struct {
	Key                          OpenAINativeCompactionCapabilityKey
	Supported                    *bool
	SemanticOutcome              OpenAINativeCompactionOutcome
	StatusCode                   *int
	CheckedAt                    time.Time
	RetryAfterUntil              *time.Time
	AuthorizationPrincipalSHA256 string
}

// OpenAINativeCompactionProbeOptions contains attempt-local limits. Zero values
// select conservative defaults. It is exported so a scheduler can impose a
// shorter timeout without coupling the probe to scheduler wiring.
type OpenAINativeCompactionProbeOptions struct {
	Timeout                 time.Duration
	MaxBytes                int64
	MaxEventBytes           int
	MaxEvents               int
	MaxOutputTokens         int
	PerRunCostLimitMicroUSD int64
	DailyCostLimitMicroUSD  int64
	CostSafetyBPS           int64
	ReservationID           string
	DispatchFence           OpenAINativeCompactionProbeDispatchFence
	StageBudget             *OpenAIStageBudget
	Now                     func() time.Time
}

// ResolveOpenAINativeCompactionProbeKey resolves a candidate exactly as an
// ordinary, bare Responses request does. It intentionally does not apply the
// legacy /responses/compact model mapping.
func ResolveOpenAINativeCompactionProbeKey(account *Account, requestedModel string) (OpenAINativeCompactionCapabilityKey, error) {
	effectiveModel := resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, false, true)
	return ResolveOpenAINativeCompactionCapabilityKey(account, effectiveModel)
}

// RunOpenAINativeCompactionV2Probe performs one candidate-specific attempt.
// It bypasses the normal account scheduler/failover and all customer usage and
// billing paths. The caller owns persistence and subsequent scheduling.
func (s *OpenAIGatewayService) RunOpenAINativeCompactionV2Probe(
	ctx context.Context,
	account *Account,
	requestedModel string,
	options OpenAINativeCompactionProbeOptions,
) (OpenAINativeCompactionProbeAttemptResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.httpUpstream == nil {
		return OpenAINativeCompactionProbeAttemptResult{}, errors.New("openai native compaction probe upstream is unavailable")
	}
	options = normalizeOpenAINativeCompactionProbeOptions(options)
	targetURL, err := s.openAINativeCompactionProbeResponsesURL(account)
	if err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	key, err := ResolveOpenAINativeCompactionProbeKey(account, requestedModel)
	if err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	if err := ValidateOpenAINativeCompactionProbeEndpoint(account); err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	if options.DispatchFence.Valid() && options.DispatchFence.Claim.Capability.Key != key {
		return OpenAINativeCompactionProbeAttemptResult{}, ErrOpenAINativeCompactionProbeClaimLost
	}
	if s.openAIProbePriceLookup == nil || s.openAIProbeBudgetRepo == nil {
		return OpenAINativeCompactionProbeAttemptResult{}, ErrOpenAINativeCompactionProbeBudgetUnavailable
	}
	price, err := s.openAIProbePriceLookup.LookupOpenAIProviderTokenPrice(key.EffectiveModel)
	if err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	quoteMicroUSD, err := QuoteOpenAINativeCompactionProbeMicroUSD(
		price,
		openAINativeCompactionProbeInputTokenCeiling,
		options.MaxOutputTokens,
		options.CostSafetyBPS,
	)
	if err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	if quoteMicroUSD > options.PerRunCostLimitMicroUSD {
		return OpenAINativeCompactionProbeAttemptResult{}, ErrOpenAINativeCompactionProbeCostLimit
	}
	reservationID := strings.TrimSpace(options.ReservationID)
	if reservationID == "" {
		reservationID = uuid.NewString()
	}
	reservation, err := s.openAIProbeBudgetRepo.Reserve(
		ctx,
		reservationID,
		quoteMicroUSD,
		options.DailyCostLimitMicroUSD,
	)
	if err != nil {
		return OpenAINativeCompactionProbeAttemptResult{}, err
	}
	dispatched := false
	defer func() {
		if !dispatched {
			// Best-effort immediate release; a failed/ambiguous settlement remains
			// durably reserved and is recovered by the bounded expiry reconciler.
			_ = runOpenAINativeCompactionProbeSettlement(ctx, func(settlementCtx context.Context) error {
				return s.openAIProbeBudgetRepo.Release(settlementCtx, reservation)
			})
		}
	}()

	now := options.Now
	if now == nil {
		now = time.Now
	}
	result := OpenAINativeCompactionProbeAttemptResult{Key: key, CheckedAt: now()}
	probeCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	token, _, err := s.GetAccessToken(probeCtx, account)
	if err != nil {
		result.SemanticOutcome = OpenAINativeCompactionTransportFailure
		return result, fmt.Errorf("resolve openai native compaction probe credential: %w", err)
	}
	req, err := s.buildOpenAINativeCompactionProbeRequest(probeCtx, account, token, targetURL, key.EffectiveModel, options.MaxOutputTokens)
	if err != nil {
		result.SemanticOutcome = OpenAINativeCompactionTransportFailure
		return result, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	principalSHA256, err := s.openAIProbeBudgetRepo.MarkDispatched(probeCtx, reservation, options.DispatchFence)
	if err != nil {
		return result, fmt.Errorf("mark openai native compaction probe dispatched: %w", err)
	}
	result.AuthorizationPrincipalSHA256 = principalSHA256
	dispatched = true
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolveOpenAITLSProfile(account))
	if commitErr := runOpenAINativeCompactionProbeSettlement(ctx, func(settlementCtx context.Context) error {
		return s.openAIProbeBudgetRepo.CommitFull(settlementCtx, reservation)
	}); commitErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return result, fmt.Errorf("commit openai native compaction probe budget: %w", commitErr)
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		result.SemanticOutcome = OpenAINativeCompactionTransportFailure
		return result, fmt.Errorf("run openai native compaction probe: %w", err)
	}
	if resp == nil {
		result.SemanticOutcome = OpenAINativeCompactionTransportFailure
		return result, errors.New("run openai native compaction probe: nil response")
	}
	statusCode := resp.StatusCode
	result.StatusCode = &statusCode
	if resp.Body == nil {
		result.SemanticOutcome = OpenAINativeCompactionIncompleteStream
		return result, errors.New("run openai native compaction probe: nil response body")
	}
	defer func() { _ = resp.Body.Close() }()

	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		// Never retain or return an upstream error body. A small bounded drain is
		// sufficient for connection reuse while keeping the result payload-free.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		result.SemanticOutcome = OpenAINativeCompactionHTTPFailure
		if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed {
			result.Supported = boolPointer(false)
		}
		if delay := retryAfter(resp.Header, result.CheckedAt); delay > 0 {
			retryAt := result.CheckedAt.Add(delay)
			result.RetryAfterUntil = &retryAt
		}
		return result, nil
	}

	stage, err := NewOpenAINativeCompactionAttemptStage(probeCtx, OpenAINativeCompactionStageConfig{
		MemoryThreshold: minInt64(options.MaxBytes, openAIFirstOutputStageMemoryLimit),
		MaxBytes:        options.MaxBytes,
		MaxEvents:       options.MaxEvents,
		MaxDuration:     options.Timeout,
		Budget:          options.StageBudget,
		Now:             now,
	})
	if err != nil {
		return result, err
	}
	defer func() { _ = stage.Discard() }()

	rawReadLimit := options.MaxBytes
	if rawReadLimit < int64(^uint64(0)>>1) {
		rawReadLimit++
	}
	limitedBody := &io.LimitedReader{R: resp.Body, N: rawReadLimit}
	validation, readErr := readOpenAINativeCompactionProbeSSE(limitedBody, stage, options.MaxEventBytes)
	if limitedBody.N == 0 {
		readErr = ErrOpenAINativeCompactionProbeResponseBytes
	}
	result.SemanticOutcome = validation.Outcome
	if readErr != nil {
		if errors.Is(readErr, ErrOpenAINativeCompactionStageBytes) ||
			errors.Is(readErr, ErrOpenAINativeCompactionStageEvents) ||
			errors.Is(readErr, ErrOpenAINativeCompactionStageDuration) ||
			errors.Is(readErr, ErrOpenAINativeCompactionStageBudget) ||
			errors.Is(readErr, ErrOpenAINativeCompactionProbeResponseBytes) {
			result.SemanticOutcome = OpenAINativeCompactionResourceLimit
		} else if probeCtx.Err() != nil {
			result.SemanticOutcome = OpenAINativeCompactionTransportFailure
		} else if errors.Is(readErr, ErrOpenAINativeCompactionProbeEventBytes) {
			result.SemanticOutcome = OpenAINativeCompactionInvalidEvent
		}
		return result, fmt.Errorf("read openai native compaction probe stream: %w", readErr)
	}
	switch validation.Outcome {
	case OpenAINativeCompactionValid:
		result.Supported = boolPointer(true)
	case OpenAINativeCompactionZeroCompaction,
		OpenAINativeCompactionMultipleCompaction,
		OpenAINativeCompactionMalformedCompaction,
		OpenAINativeCompactionDuplicateTerminal,
		OpenAINativeCompactionPostTerminalFrame,
		OpenAINativeCompactionInvalidEvent:
		result.Supported = boolPointer(false)
	}
	return result, nil
}

func runOpenAINativeCompactionProbeSettlement(ctx context.Context, settle func(context.Context) error) error {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	settlementCtx, cancel := context.WithTimeout(base, openAINativeCompactionProbeSettlementTimeout)
	defer cancel()
	return settle(settlementCtx)
}

func normalizeOpenAINativeCompactionProbeOptions(options OpenAINativeCompactionProbeOptions) OpenAINativeCompactionProbeOptions {
	if options.Timeout <= 0 {
		options.Timeout = openAINativeCompactionProbeTimeout
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = openAINativeCompactionProbeMaxBytes
	}
	if options.MaxEventBytes <= 0 {
		options.MaxEventBytes = openAINativeCompactionProbeMaxEventBytes
	}
	if options.MaxEvents <= 0 {
		options.MaxEvents = openAINativeCompactionProbeMaxEvents
	}
	if options.MaxOutputTokens <= 0 {
		options.MaxOutputTokens = 256
	}
	if options.PerRunCostLimitMicroUSD <= 0 {
		options.PerRunCostLimitMicroUSD = 10_000
	}
	if options.DailyCostLimitMicroUSD <= 0 {
		options.DailyCostLimitMicroUSD = 100_000
	}
	if options.CostSafetyBPS < 10_000 {
		options.CostSafetyBPS = 12_500
	}
	return options
}

func (s *OpenAIGatewayService) buildOpenAINativeCompactionProbeRequest(
	ctx context.Context,
	account *Account,
	token string,
	targetURL string,
	effectiveModel string,
	maxOutputTokens int,
) (*http.Request, error) {
	body, err := json.Marshal(map[string]any{
		"model":             effectiveModel,
		"stream":            true,
		"store":             false,
		"max_output_tokens": maxOutputTokens,
		"input": []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{{
					"type": "input_text",
					"text": "Capability probe. Compact this synthetic input.",
				}},
			},
			{"type": "compaction_trigger"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal openai native compaction probe: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build openai native compaction probe request: %w", err)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build openai native compaction probe authentication: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	if account.Type == AccountTypeOAuth {
		req.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
			return nil, fmt.Errorf("resolve openai native compaction probe account headers: %w", err)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "codex_cli_rs")
		s.applyOpenAIUpstreamUserAgent(ctx, nil, account, req, false)
		enforceCodexIdentityHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	// These are contract headers rather than caller preferences. Reassert them
	// after account overrides so every probe tests the same v2 protocol.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-codex-beta-features", OpenAINativeCompactionContractVersion)
	return req, nil
}

func (s *OpenAIGatewayService) openAINativeCompactionProbeResponsesURL(account *Account) (string, error) {
	rawURL, err := ResolveOpenAICanonicalResponsesEndpoint(account)
	if err != nil {
		return "", fmt.Errorf("resolve openai native compaction probe URL: %w", err)
	}
	if account.Type == AccountTypeOAuth {
		return rawURL, nil
	}
	if s == nil || s.cfg == nil {
		parsed, err := url.ParseRequestURI(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return "", errors.New("validate openai native compaction probe URL: invalid HTTPS URL")
		}
		return parsed.String(), nil
	}
	validated, err := s.validateUpstreamBaseURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("validate openai native compaction probe URL: %w", err)
	}
	return validated, nil
}

func readOpenAINativeCompactionProbeSSE(
	source io.Reader,
	stage *OpenAINativeCompactionAttemptStage,
	maxEventBytes int,
) (OpenAINativeCompactionValidationResult, error) {
	validator := NewOpenAINativeCompactionValidator()
	if source == nil {
		return validator.Finish(), errors.New("native compaction probe stream is nil")
	}
	scanner := bufio.NewScanner(source)
	initialBufferBytes := maxEventBytes
	if initialBufferBytes > 64<<10 {
		initialBufferBytes = 64 << 10
	}
	if initialBufferBytes < 1 {
		initialBufferBytes = 1
	}
	scanner.Buffer(make([]byte, 0, initialBufferBytes), maxEventBytes)
	var eventType string
	dataLines := make([]string, 0, 1)
	eventBytes := 0
	var eventErr error
	flush := func() {
		if eventErr != nil {
			return
		}
		if len(dataLines) == 0 {
			eventType = ""
			eventBytes = 0
			return
		}
		data := strings.Join(dataLines, "\n")
		payload := []byte(openAICompatPayloadWithEventType(data, eventType))
		if strings.TrimSpace(data) == "[DONE]" {
			payload = []byte("[DONE]")
		}
		if err := stage.StageEvent(payload); err != nil {
			eventErr = err
			return
		}
		validator.Observe(payload)
		eventType = ""
		dataLines = dataLines[:0]
		eventBytes = 0
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if nextEventType, ok := extractOpenAISSEEventLine(line); ok {
			eventType = nextEventType
			continue
		}
		if data, ok := extractOpenAISSEDataLine(line); ok {
			incoming := len(data)
			if len(dataLines) > 0 {
				incoming++
			}
			if incoming > maxEventBytes-eventBytes {
				return validator.Finish(), ErrOpenAINativeCompactionProbeEventBytes
			}
			dataLines = append(dataLines, data)
			eventBytes += incoming
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
		}
		if eventErr != nil {
			return validator.Finish(), eventErr
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			return validator.Finish(), fmt.Errorf("%w: %v", ErrOpenAINativeCompactionProbeEventBytes, err)
		}
		return validator.Finish(), err
	}
	flush()
	if eventErr != nil {
		return validator.Finish(), eventErr
	}
	return validator.Finish(), nil
}

func boolPointer(value bool) *bool {
	return &value
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
