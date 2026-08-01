package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const upstreamAttemptPersistTimeout = 2 * time.Second

type openAIUpstreamAttemptCoordinator struct {
	service           *OpenAIGatewayService
	ctx               context.Context
	attribution       UpstreamAttemptAttribution
	model             string
	priority          bool
	capabilitySource  string
	terminalPersisted bool
}

func (s *OpenAIGatewayService) beginOpenAINativeHTTPAttempt(ctx context.Context, c *gin.Context, account *Account, routingModel string, priority bool) *openAIUpstreamAttemptCoordinator {
	return s.beginOpenAINativeAttempt(ctx, account, routingModel, priority, IsOpenAINativeRemoteCompactionV2(c), UpstreamAttemptTransportHTTP, "", "")
}

func (s *OpenAIGatewayService) beginOpenAINativeWSAttempt(
	ctx context.Context,
	account *Account,
	routingModel string,
	priority bool,
	native bool,
	transport UpstreamAttemptTransport,
	connectionID WSConnectionID,
	turnID WSTurnID,
) *openAIUpstreamAttemptCoordinator {
	return s.beginOpenAINativeAttempt(ctx, account, routingModel, priority, native, transport, connectionID, turnID)
}

func (s *OpenAIGatewayService) beginOpenAINativeAttempt(
	ctx context.Context,
	account *Account,
	routingModel string,
	priority bool,
	native bool,
	transport UpstreamAttemptTransport,
	connectionID WSConnectionID,
	turnID WSTurnID,
) *openAIUpstreamAttemptCoordinator {
	if s == nil || account == nil || !native {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if transport != UpstreamAttemptTransportHTTP && transport != UpstreamAttemptTransportWebSocket {
		return nil
	}
	if transport == UpstreamAttemptTransportWebSocket {
		if connectionID == "" {
			connectionID = NewOpenAIWSConnectionID()
		}
		if turnID == "" {
			turnID = NewOpenAIWSTurnID()
		}
	}
	domain, err := ResolveOpenAINativeCompatibilityDomainForRequest(ctx, account, routingModel)
	if err != nil {
		logger.FromContext(ctx).Warn("openai native upstream attempt domain unavailable", zap.Int64("account_id", account.ID), zap.Error(err))
		return nil
	}
	clientID, _ := ctx.Value(ctxkey.ClientRequestID).(string)
	gatewayID, _ := ctx.Value(ctxkey.RequestID).(string)
	identity := NewOpenAIRequestIdentityWithGatewayRequestID(ClientRequestID(clientID), GatewayRequestID(gatewayID))
	if identity.ClientRequestID == "" {
		// Production requests receive this identity from ClientRequestID middleware.
		// Keep direct service invocations valid without conflating it with gateway ID.
		identity.ClientRequestID = ClientRequestID(NewOpenAIRequestIdentity("").GatewayRequestID)
	}
	now := time.Now().UTC()
	coordinator := &openAIUpstreamAttemptCoordinator{
		service:  s,
		ctx:      context.WithoutCancel(ctx),
		model:    domain.EffectiveModel,
		priority: priority,
		capabilitySource: account.openAINativeRemoteCompactionCapabilitySource(OpenAINativeCompactionCapabilityKey{
			AccountID:           account.ID,
			UpstreamFingerprint: domain.UpstreamFingerprint,
			EffectiveModel:      domain.EffectiveModel,
			ContractVersion:     domain.ContractVersion,
		}),
		attribution: UpstreamAttemptAttribution{
			AttemptID:        identity.NewAttempt(),
			ClientRequestID:  identity.ClientRequestID,
			GatewayRequestID: identity.GatewayRequestID,
			WSConnectionID:   connectionID,
			WSTurnID:         turnID,
			AccountID:        account.ID,
			Transport:        transport,
			Domain:           domain,
			State:            UpstreamAttemptStateStarted,
			StateVersion:     1,
			StartedAt:        now,
			ObservedAt:       now,
		},
	}
	coordinator.persist()
	return coordinator
}

func (a *openAIUpstreamAttemptCoordinator) observeTransport(resp *http.Response) {
	if a == nil {
		return
	}
	a.attribution.State = UpstreamAttemptStateInProgress
	a.attribution.StateVersion = 2
	a.attribution.TransportObserved = true
	a.attribution.ObservedAt = time.Now().UTC()
	if resp != nil {
		status := resp.StatusCode
		a.attribution.HTTPObserved = true
		a.attribution.HTTPStatus = &status
		a.attribution.UpstreamRequestID = UpstreamRequestID(strings.TrimSpace(resp.Header.Get("x-request-id")))
	}
	a.persist()
}

func (a *openAIUpstreamAttemptCoordinator) observeWebSocket(headers http.Header) {
	if a == nil {
		return
	}
	a.attribution.State = UpstreamAttemptStateInProgress
	a.attribution.StateVersion = 2
	a.attribution.TransportObserved = true
	a.attribution.ObservedAt = time.Now().UTC()
	if headers != nil {
		a.attribution.UpstreamRequestID = UpstreamRequestID(strings.TrimSpace(headers.Get("x-request-id")))
	}
	a.persist()
}

func (a *openAIUpstreamAttemptCoordinator) finishHTTPError(resp *http.Response, outcome string, safeToFailover bool) {
	if a == nil {
		return
	}
	if a.attribution.State == UpstreamAttemptStateStarted {
		a.observeTransport(resp)
	}
	a.finish(openAIUpstreamAttemptTerminal{
		outcome:          strings.TrimSpace(outcome),
		deliveryObserved: true,
		safeToFailover:   safeToFailover,
	})
}

func (a *openAIUpstreamAttemptCoordinator) finishStreaming(result *openaiStreamingResult, err error) {
	if a == nil {
		return
	}
	terminal := openAIUpstreamAttemptTerminal{}
	if result != nil {
		terminal.validation = result.nativeValidation
		terminal.responseID = result.responseID
		terminal.usage = result.usage
		terminal.usageObserved = result.usageObserved
		terminal.deliveryObserved = true
		terminal.deliveryCommitted = result.deliveryCommitted
	}
	a.finishStreamingTerminal(terminal, err)
}

func (a *openAIUpstreamAttemptCoordinator) finishStreamingPassthrough(result *openaiStreamingResultPassthrough, err error) {
	if a == nil {
		return
	}
	terminal := openAIUpstreamAttemptTerminal{deliveryObserved: true}
	if result != nil {
		terminal.validation = result.nativeValidation
		terminal.responseID = result.responseID
		terminal.usage = result.usage
		terminal.usageObserved = result.usageObserved
		terminal.deliveryCommitted = result.deliveryCommitted
	}
	a.finishStreamingTerminal(terminal, err)
}

func (a *openAIUpstreamAttemptCoordinator) finishNonStreaming(result *openaiNonStreamingResult, err error) {
	if a == nil {
		return
	}
	terminal := openAIUpstreamAttemptTerminal{
		outcome:          string(OpenAINativeCompactionIncompleteStream),
		deliveryObserved: true,
		safeToFailover:   err != nil,
	}
	if result != nil {
		terminal.responseID = result.responseID
		terminal.usage = result.usage
		terminal.usageObserved = result.usage != nil
		terminal.deliveryCommitted = err == nil && !result.clientDisconnect
	}
	if err == nil && terminal.deliveryCommitted {
		terminal.outcome = string(OpenAINativeCompactionValid)
	}
	a.finish(terminal)
}

func (a *openAIUpstreamAttemptCoordinator) finishNonStreamingPassthrough(result *openaiNonStreamingResultPassthrough, err error) {
	if a == nil {
		return
	}
	terminal := openAIUpstreamAttemptTerminal{
		outcome:          string(OpenAINativeCompactionIncompleteStream),
		deliveryObserved: true,
		safeToFailover:   err != nil,
	}
	if result != nil {
		terminal.responseID = result.responseID
		terminal.usage = result.usage
		terminal.usageObserved = result.usage != nil
		terminal.deliveryCommitted = err == nil && !result.clientDisconnect
	}
	if err == nil && terminal.deliveryCommitted {
		terminal.outcome = string(OpenAINativeCompactionValid)
	}
	a.finish(terminal)
}

func (a *openAIUpstreamAttemptCoordinator) finishStreamingTerminal(terminal openAIUpstreamAttemptTerminal, err error) {
	if a == nil {
		return
	}
	if terminal.validation.Outcome == "" {
		terminal.validation.Outcome = OpenAINativeCompactionIncompleteStream
	}
	terminal.outcome = string(terminal.validation.Outcome)
	terminal.safeToFailover = err != nil && !terminal.deliveryCommitted
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) && failoverErr != nil {
		terminal.safeToFailover = failoverErr.SafeToFailoverAfterWrite || !terminal.deliveryCommitted
	}
	a.finish(terminal)
}

func (a *openAIUpstreamAttemptCoordinator) finishWebSocket(
	validation OpenAINativeCompactionValidationResult,
	responseID string,
	usage *OpenAIUsage,
	usageObserved bool,
	deliveryCommitted bool,
	safeToFailover bool,
	err error,
) {
	if a == nil {
		return
	}
	if validation.Outcome == "" {
		validation.Outcome = OpenAINativeCompactionIncompleteStream
	}
	a.finish(openAIUpstreamAttemptTerminal{
		outcome:           string(validation.Outcome),
		validation:        validation,
		responseID:        responseID,
		usage:             usage,
		usageObserved:     usageObserved,
		deliveryObserved:  true,
		deliveryCommitted: deliveryCommitted,
		safeToFailover:    err != nil && safeToFailover,
	})
}

type openAIUpstreamAttemptTerminal struct {
	outcome           string
	validation        OpenAINativeCompactionValidationResult
	responseID        string
	usage             *OpenAIUsage
	usageObserved     bool
	deliveryObserved  bool
	deliveryCommitted bool
	safeToFailover    bool
}

func (a *openAIUpstreamAttemptCoordinator) finish(terminal openAIUpstreamAttemptTerminal) {
	if a == nil {
		return
	}
	now := time.Now().UTC()
	a.attribution.State = UpstreamAttemptStateTerminal
	a.attribution.StateVersion = 3
	a.attribution.ObservedAt = now
	a.attribution.CompletedAt = &now
	a.attribution.UpstreamResponseID = UpstreamResponseID(strings.TrimSpace(terminal.responseID))
	a.attribution.SemanticObserved = strings.TrimSpace(terminal.outcome) != ""
	a.attribution.DeliveryObserved = terminal.deliveryObserved
	a.attribution.DeliveryCommitted = terminal.deliveryCommitted
	a.attribution.SafeToFailover = terminal.safeToFailover && !terminal.deliveryCommitted
	a.attribution.Semantic = UpstreamAttemptSemantic{
		Outcome:             terminal.outcome,
		OutputItemDoneCount: int64(terminal.validation.OutputItemDoneCount),
		CompactionItemCount: int64(terminal.validation.CompactionItemCount),
		MalformedItemCount:  int64(terminal.validation.MalformedItemCount),
		TerminalEvent:       terminal.validation.TerminalEvent,
		TerminalCount:       int64(terminal.validation.TerminalCount),
		SuccessfulTerminal:  terminal.validation.SuccessfulTerminal,
	}
	a.attribution.Usage = a.usage(terminal.usage, terminal.usageObserved)
	a.terminalPersisted = a.persist()
}

// finalizeOpenAINativeHTTPForwardResult is the single projection point from the
// per-attempt ledger into the customer settlement and scheduling result. The
// baseline is deliberately incomplete so missing attribution cannot fail open.
func finalizeOpenAINativeHTTPForwardResult(
	c *gin.Context,
	result *OpenAIForwardResult,
	account *Account,
	attempt *openAIUpstreamAttemptCoordinator,
) {
	finalizeOpenAINativeForwardResult(result, account, attempt, IsOpenAINativeRemoteCompactionV2(c), UpstreamAttemptTransportHTTP)
}

func finalizeOpenAINativeForwardResult(
	result *OpenAIForwardResult,
	account *Account,
	attempt *openAIUpstreamAttemptCoordinator,
	native bool,
	transport UpstreamAttemptTransport,
) {
	if result == nil || !native {
		return
	}

	result.NativeRemoteCompactionV2 = true
	result.SemanticSource = "validator"
	result.SemanticOutcome = OpenAINativeCompactionIncompleteStream
	result.Transport = transport
	if account != nil {
		result.AccountType = strings.TrimSpace(string(account.Type))
	}
	if attempt == nil {
		return
	}

	attribution := attempt.attribution
	result.ClientRequestID = attribution.ClientRequestID
	result.GatewayRequestID = attribution.GatewayRequestID
	result.AttemptID = attribution.AttemptID
	result.UpstreamRequestID = attribution.UpstreamRequestID
	if result.ResponseID == "" {
		result.ResponseID = strings.TrimSpace(string(attribution.UpstreamResponseID))
	}
	result.WSConnectionID = attribution.WSConnectionID
	result.WSTurnID = attribution.WSTurnID
	result.Transport = attribution.Transport
	result.UpstreamFingerprint = attribution.Domain.UpstreamFingerprint
	result.CapabilitySource = attempt.capabilitySource
	if outcome := strings.TrimSpace(attribution.Semantic.Outcome); outcome != "" {
		result.SemanticOutcome = OpenAINativeCompactionOutcome(outcome)
	}
	result.OutputItemDoneCount = int(attribution.Semantic.OutputItemDoneCount)
	result.CompactionItemCount = int(attribution.Semantic.CompactionItemCount)
	result.MalformedItemCount = int(attribution.Semantic.MalformedItemCount)
	result.UpstreamTerminalEvent = strings.TrimSpace(attribution.Semantic.TerminalEvent)
	result.TerminalEventCount = int(attribution.Semantic.TerminalCount)
	result.SafeToFailover = attribution.SafeToFailover
	result.DeliveryCommitted = attribution.DeliveryCommitted
	result.AttributionPersisted = attempt.terminalPersisted
	result.UpstreamUsageObserved = attribution.Usage.Observed
}

func (a *openAIUpstreamAttemptCoordinator) usage(usage *OpenAIUsage, observed bool) UpstreamAttemptUsage {
	if !observed || usage == nil {
		return UpstreamAttemptUsage{}
	}
	input := int64(usage.InputTokens)
	output := int64(usage.OutputTokens)
	cacheCreation := int64(usage.CacheCreationInputTokens)
	cacheRead := int64(usage.CacheReadInputTokens)
	imageInput := int64(usage.ImageInputTokens)
	imageOutput := int64(usage.ImageOutputTokens)
	result := UpstreamAttemptUsage{
		Observed:                 true,
		InputTokens:              &input,
		OutputTokens:             &output,
		CacheCreationInputTokens: &cacheCreation,
		CacheReadInputTokens:     &cacheRead,
		ImageInputTokens:         &imageInput,
		ImageOutputTokens:        &imageOutput,
	}
	if cost, ok := a.providerCostUSD(usage); ok {
		result.CostUSD = &cost
	}
	return result
}

func (a *openAIUpstreamAttemptCoordinator) providerCostUSD(usage *OpenAIUsage) (float64, bool) {
	if a == nil || a.service == nil || a.service.openAIProbePriceLookup == nil || usage == nil {
		return 0, false
	}
	price, err := a.service.openAIProbePriceLookup.LookupOpenAIProviderTokenPrice(a.model)
	if err != nil {
		return 0, false
	}
	inputRate := price.InputUSDPerToken
	outputRate := price.OutputUSDPerToken
	cacheCreationRate := price.CacheCreationUSDPerToken
	cacheReadRate := price.CacheReadUSDPerToken
	if a.priority {
		inputRate = price.InputPriorityUSDPerToken
		outputRate = price.OutputPriorityUSDPerToken
		cacheCreationRate = price.CacheCreationPriorityUSDPerToken
		cacheReadRate = price.CacheReadPriorityUSDPerToken
	}
	actualInput := usage.InputTokens - usage.CacheCreationInputTokens - usage.CacheReadInputTokens
	if actualInput < 0 {
		actualInput = 0
	}
	cost := float64(actualInput)*inputRate +
		float64(usage.OutputTokens)*outputRate +
		float64(usage.CacheCreationInputTokens)*cacheCreationRate +
		float64(usage.CacheReadInputTokens)*cacheReadRate
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, false
	}
	return cost, true
}

func (a *openAIUpstreamAttemptCoordinator) persist() bool {
	if a == nil || a.service == nil || a.service.upstreamAttemptAttributionRepo == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(a.ctx, upstreamAttemptPersistTimeout)
	defer cancel()
	if _, err := a.service.upstreamAttemptAttributionRepo.Upsert(ctx, a.attribution); err != nil {
		logger.FromContext(a.ctx).Warn("persist openai native upstream attempt attribution failed",
			zap.String("attempt_id", string(a.attribution.AttemptID)),
			zap.String("state", string(a.attribution.State)),
			zap.Int64("account_id", a.attribution.AccountID),
			zap.Error(err),
		)
		return false
	}
	return true
}
