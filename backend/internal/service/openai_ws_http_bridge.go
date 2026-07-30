package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIWSClientReadLimitBytesDefault     int64 = 64 * 1024 * 1024
	openAIWSHTTPBridgeThresholdBytesDefault int64 = 15 * 1024 * 1024
	openAIWSHTTPBridgeErrorBodyLimitBytes         = 64 * 1024
	openAIWSHTTPBridgeTurnRetryLimit              = 1
)

func splitOpenAIWSHTTPBridgeSSEEvent(data []byte, atEOF bool) (advance int, token []byte, err error) {
	lineStart := 0
	for index, value := range data {
		if value != '\n' {
			continue
		}
		lineEnd := index
		if lineEnd > lineStart && data[lineEnd-1] == '\r' {
			lineEnd--
		}
		if lineEnd == lineStart {
			return index + 1, data[:index+1], nil
		}
		lineStart = index + 1
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parseOpenAIWSHTTPBridgeSSEEvent(frame []byte) (eventType, data string, ok bool) {
	dataLines := make([]string, 0, 1)
	for _, rawLine := range strings.Split(string(frame), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if value, found := extractOpenAISSEEventLine(line); found {
			eventType = value
			continue
		}
		if value, found := extractOpenAISSEDataLine(line); found {
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return eventType, "", false
	}
	return eventType, strings.Join(dataLines, "\n"), true
}

// ResolveOpenAIWSClientFirstMessageTimeout 返回生效的客户端入站首消息截止时间。
func ResolveOpenAIWSClientFirstMessageTimeout(cfg *config.Config) time.Duration {
	seconds := config.DefaultOpenAIWSClientFirstMessageTimeoutSeconds
	if cfg != nil && cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds > 0 {
		seconds = cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// ResolveOpenAIWSClientReadLimitBytes 返回入站客户端 WS 单帧读取上限。
func ResolveOpenAIWSClientReadLimitBytes(cfg *config.Config) int64 {
	if cfg == nil || cfg.Gateway.OpenAIWS.ClientReadLimitBytes <= 0 {
		return openAIWSClientReadLimitBytesDefault
	}
	return cfg.Gateway.OpenAIWS.ClientReadLimitBytes
}

// openAIWSHTTPBridgeEnabled 判断是否允许过大首帧走 HTTP bridge。
func (s *OpenAIGatewayService) openAIWSHTTPBridgeEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled
}

// openAIWSHTTPBridgeThresholdBytes 返回触发 HTTP bridge 的 payload 阈值。
func (s *OpenAIGatewayService) openAIWSHTTPBridgeThresholdBytes() int64 {
	if s == nil || s.cfg == nil || s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes <= 0 {
		return openAIWSHTTPBridgeThresholdBytesDefault
	}
	return s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes
}

// shouldBridgeOpenAIWSHTTP 判断当前 WS 首帧是否应改用 HTTP Responses 上游。
func (s *OpenAIGatewayService) shouldBridgeOpenAIWSHTTP(account *Account, payloadBytes int, previousResponseID string) bool {
	if account != nil && account.Platform == PlatformGrok {
		return true
	}
	if !s.openAIWSHTTPBridgeEnabled() {
		return false
	}
	if strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	threshold := s.openAIWSHTTPBridgeThresholdBytes()
	return threshold > 0 && int64(payloadBytes) >= threshold
}

// prepareOpenAIWSHTTPBridgeBody 将 response.create WS payload 转成 HTTP Responses body。
func prepareOpenAIWSHTTPBridgeBody(payload []byte, preservePreviousResponseID bool) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("response.create payload must be a JSON object")
	}
	delete(body, "type")
	delete(body, "generate")
	if !preservePreviousResponseID {
		delete(body, "previous_response_id")
	}
	body["stream"] = true
	return json.Marshal(body)
}

// openAIWSToolCallReplayCollector 收集上游输出里的工具调用上下文，供后续 bridge turn 重放。
type openAIWSToolCallReplayCollector struct {
	items []json.RawMessage
	seen  map[string]struct{}
}

// AddEvent 从上游事件中提取可重放的 function_call 项。
func (c *openAIWSToolCallReplayCollector) AddEvent(eventType string, message []byte) {
	switch strings.TrimSpace(eventType) {
	case "response.output_item.done":
		c.addItem(gjson.GetBytes(message, "item"))
	case "response.completed", "response.done":
		output := gjson.GetBytes(message, "response.output")
		if !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			c.addItem(item)
		}
	}
}

// Items 返回已收集工具调用上下文的拷贝。
func (c *openAIWSToolCallReplayCollector) Items() []json.RawMessage {
	return cloneOpenAIWSRawMessages(c.items)
}

func (c *openAIWSToolCallReplayCollector) addItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return
	}
	if !isCodexToolCallContextItemType(item.Get("type").String()) {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.items = append(c.items, json.RawMessage(raw))
}

func buildOpenAIWSHTTPBridgeErrorEvent(statusCode int, message string) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		message = "upstream request failed"
	}
	event := map[string]any{
		"type":   "error",
		"status": statusCode,
		"error": map[string]any{
			"type":    "upstream_error",
			"message": message,
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"upstream_error","message":"upstream request failed"}}`)
	}
	return body
}

// proxyOpenAIWSHTTPBridgeTurn 使用 HTTP Responses 上游完成一个 WS ingress turn，并把 SSE 事件转回 WS 消息。
func (s *OpenAIGatewayService) proxyOpenAIWSHTTPBridgeTurn(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token string,
	payload []byte,
	payloadBytes int,
	originalModel string,
	routingModel string,
	imageBillingModel string,
	imageSizeTier string,
	imageInputSize string,
	grokCacheIdentity string,
	turn int,
	writeClientMessage func([]byte) error,
	routerMatch ...TLSFingerprintRouterMatchResult,
) (forwardResult *OpenAIForwardResult, forwardErr error) {
	if s == nil {
		return nil, errors.New("service is nil")
	}
	if s.httpUpstream == nil {
		return nil, errors.New("openai http upstream is nil")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if writeClientMessage == nil {
		return nil, errors.New("client websocket writer is nil")
	}

	nativeCompaction := isOpenAINativeCompactionTurn(c, payload)
	body, err := prepareOpenAIWSHTTPBridgeBody(payload, nativeCompaction)
	if err != nil {
		return nil, fmt.Errorf("prepare http bridge body: %w", err)
	}
	nativeAttemptCtx, releaseNativeAttemptCtx := withOpenAIWSNativeClientDisconnect(ctx, nativeCompaction)
	defer releaseNativeAttemptCtx()

	upstreamCtx, releaseUpstreamCtx := openAIUpstreamContextForCompactionAttempt(nativeAttemptCtx, nativeCompaction)
	var upstreamReq *http.Request
	if account.Platform == PlatformGrok {
		upstreamModel := resolveGrokWSUpstreamModel(account, body, routingModel)
		grokIntentSourceBody := body
		body, err = patchGrokResponsesBody(body, upstreamModel)
		if err != nil {
			releaseUpstreamCtx()
			return nil, err
		}
		grokMixedCacheIntentBody := append([]byte(nil), body...)
		body, err = applyGrokResponsesCacheIdentity(body, grokIntentSourceBody, grokCacheIdentity, account.IsGrokOAuth())
		if err != nil {
			releaseUpstreamCtx()
			return nil, fmt.Errorf("apply grok prompt cache identity: %w", err)
		}
		body, err = applyGrokFreeRequestToolCacheRoute(c, body, grokMixedCacheIntentBody, account, grokCacheIdentity)
		if err != nil {
			releaseUpstreamCtx()
			return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", err)
		}
		upstreamReq, err = buildGrokResponsesRequest(upstreamCtx, c, account, body, token, grokCacheIdentity, s.cfg)
	} else {
		upstreamReq, err = s.buildUpstreamRequestOpenAIPassthrough(upstreamCtx, c, account, body, token, routerMatch...)
	}
	releaseUpstreamCtx()
	if err != nil {
		return nil, err
	}
	if account.Platform != PlatformGrok && isOpenAIResponsesLiteWebSocketPayload(payload) {
		upstreamReq.Header.Set(responsesLiteHeader, "true")
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if c != nil {
		c.Set("openai_passthrough", true)
		c.Set("openai_ws_http_bridge", true)
	}

	turnStart := time.Now()
	wsIdentity := openAIWSAttemptIdentityFromContext(ctx)
	if wsIdentity.ConnectionID == "" {
		wsIdentity.ConnectionID = NewOpenAIWSConnectionID()
	}
	if wsIdentity.TurnID == "" {
		wsIdentity.TurnID = NewOpenAIWSTurnID()
	}
	var nativeStage *OpenAINativeCompactionAttemptStage
	var nativeValidator *OpenAINativeCompactionValidator
	var nativeValidation OpenAINativeCompactionValidationResult
	nativeDeliveryCommitted := false
	nativeDeliveryStarted := false
	responseID := ""
	usage := OpenAIUsage{}
	usageObserved := false
	clientDisconnected := false
	if nativeCompaction {
		nativeStage, err = newOpenAINativeCompactionAttemptStage(nativeAttemptCtx, s)
		if err != nil {
			return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, nil, joinOpenAINativeCompactionStageSemanticError(err))
		}
		defer func() { _ = nativeStage.Close() }()
		nativeValidator = NewOpenAINativeCompactionValidator()
	}
	upstreamAttempt := s.beginOpenAINativeWSAttempt(
		ctx,
		account,
		routingModel,
		openAIServiceTierIsPriority(extractOpenAIServiceTierFromBody(body)),
		nativeCompaction,
		UpstreamAttemptTransportHTTP,
		wsIdentity.ConnectionID,
		wsIdentity.TurnID,
	)
	defer func() {
		if upstreamAttempt == nil {
			return
		}
		if nativeValidation.Outcome == "" || nativeValidation.Outcome == OpenAINativeCompactionPending {
			nativeValidation = nativeValidator.Finish()
		}
		upstreamAttempt.finishWebSocket(nativeValidation, responseID, &usage, usageObserved, nativeDeliveryCommitted && !clientDisconnected, !nativeDeliveryStarted && !clientDisconnected, forwardErr)
		finalizeOpenAINativeForwardResult(forwardResult, account, upstreamAttempt, nativeCompaction, UpstreamAttemptTransportHTTP)
	}()
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.resolveOpenAITLSProfile(account, routerMatch...))
	if err != nil {
		if disconnectErr := openAIWSClientReadPumpDisconnectError(ctx); nativeCompaction && disconnectErr != nil {
			clientDisconnected = true
			return nil, disconnectErr
		}
		if turn == 1 {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		}
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "Upstream request failed"))
		return nil, fmt.Errorf("upstream http bridge request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()
	if upstreamAttempt != nil {
		upstreamAttempt.observeTransport(resp)
	}

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, openAIWSHTTPBridgeErrorBodyLimitBytes))
		if nativeCompaction {
			if continuationCode := openAINativeCompactionContinuationErrorCode(extractUpstreamErrorCode(respBody)); continuationCode != "" {
				return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, newOpenAINativeCompactionContinuationError(continuationCode))
			}
		}
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		if upstreamMsg == "" {
			upstreamMsg = http.StatusText(resp.StatusCode)
		}
		shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
		if account.Platform == PlatformGrok {
			shouldFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody)
			s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			if turn == 1 && shouldFailover {
				return nil, newOpenAIUpstreamFailoverError(resp.StatusCode, resp.Header, respBody, upstreamMsg, false)
			}
		} else if turn == 1 && shouldFailover {
			return nil, s.handleFailoverErrorResponsePassthrough(ctx, resp, c, account, body, respBody)
		}
		if account.Platform != PlatformGrok && (shouldFailover || shouldCooldownOpenAITransientUpstreamError(resp.StatusCode, respBody)) {
			canonicalModel := canonicalOpenAIAccountSchedulingModel(account, routingModel)
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, canonicalModel)
		}
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(resp.StatusCode, upstreamMsg))
		return nil, fmt.Errorf("upstream http bridge error: status=%d message=%s", resp.StatusCode, upstreamMsg)
	}
	if account.Platform == PlatformGrok {
		s.updateGrokUsageFromResponse(ctx, account, resp.Header, resp.StatusCode)
	}

	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	reqStream := openAIWSPayloadBoolFromRaw(body, "stream", true)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	replayCollector := &openAIWSToolCallReplayCollector{}
	firstEventType := ""
	lastEventType := ""
	upstreamTerminalEvent := ""
	sawDone := false
	wroteDownstream := false
	pendingPreamble := make([][]byte, 0, 4)
	pendingTerminalTail := make([][]byte, 0, 4)
	holdingTerminalTail := false
	emitClientMessage := func(message []byte) error {
		if clientDisconnected {
			return nil
		}
		if err := writeClientMessage(message); err != nil {
			if isOpenAIWSClientDisconnectError(err) {
				clientDisconnected = true
				closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
				logOpenAIWSModeInfo(
					"ingress_ws_http_bridge_client_disconnected_drain account_id=%d turn=%d close_status=%s close_reason=%s",
					account.ID,
					turn,
					closeStatus,
					truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
				)
				return nil
			}
			return wrapOpenAIWSIngressTurnError(
				"write_client",
				fmt.Errorf("write client websocket event: %w", err),
				wroteDownstream,
			)
		}
		wroteDownstream = true
		return nil
	}
	mappedModel := ""
	needModelReplace := false
	var mappedModelBytes []byte
	if routingModel != "" {
		mappedModel = normalizeOpenAIModelForUpstream(account, resolveAccountMappedModelForForward(account, routingModel))
		needModelReplace = mappedModel != "" && mappedModel != originalModel
		if needModelReplace {
			mappedModelBytes = []byte(mappedModel)
		}
	}

	resultWithUsage := func() *OpenAIForwardResult {
		imageCount := imageCounter.Count()
		result := &OpenAIForwardResult{
			RequestID:             responseID,
			ResponseID:            responseID,
			Usage:                 usage,
			Model:                 originalModel,
			UpstreamModel:         mappedModel,
			ServiceTier:           extractOpenAIServiceTierFromBody(body),
			ReasoningEffort:       ApplyThinkingEnabledFallback(extractOpenAIReasoningEffortFromBody(body, mappedModel, originalModel), body, mappedModel),
			Stream:                reqStream,
			OpenAIWSMode:          true,
			UpstreamTerminalEvent: upstreamTerminalEvent,
			ResponseHeaders:       cloneHeader(resp.Header),
			Duration:              time.Since(turnStart),
			FirstTokenMs:          firstTokenMs,
			ClientDisconnect:      clientDisconnected,
		}
		if replayInput := replayCollector.Items(); len(replayInput) > 0 {
			result.wsReplayInput = replayInput
			result.wsReplayInputExists = true
		}
		if imageCount > 0 {
			result.ImageCount = imageCount
			result.ImageSize = imageSizeTier
			result.ImageInputSize = imageInputSize
			result.ImageOutputSizes = imageCounter.Sizes()
			result.BillingModel = imageBillingModel
		}
		return result
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	if nativeValidator != nil {
		scanner.Split(splitOpenAIWSHTTPBridgeSSEEvent)
		maxLineSize = openAINativeCompactionScannerLimit(maxLineSize, nativeStage)
	}
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer putSSEScannerBuf64K(scanBuf)
	pendingSSEEventType := ""

	for scanner.Scan() {
		var data string
		var ok bool
		if nativeValidator != nil {
			pendingSSEEventType, data, ok = parseOpenAIWSHTTPBridgeSSEEvent(scanner.Bytes())
			if !ok {
				continue
			}
		} else {
			line := scanner.Text()
			data, ok = extractOpenAISSEDataLine(line)
			if !ok {
				if eventName, eventOK := extractOpenAISSEEventLine(line); eventOK {
					pendingSSEEventType = eventName
				} else if strings.TrimSpace(line) == "" {
					pendingSSEEventType = ""
				}
				continue
			}
		}
		trimmedData := strings.TrimSpace(data)
		if trimmedData == "" {
			continue
		}
		if trimmedData == "[DONE]" {
			sawDone = true
			if nativeValidator != nil {
				nativeValidator.Observe([]byte("[DONE]"))
			}
			pendingSSEEventType = ""
			continue
		}

		upstreamMessage := []byte(trimmedData)
		if nativeValidator != nil {
			upstreamMessage = []byte(data)
			nativeValidator.Observe(upstreamMessage)
			if stageErr := openAINativeCompactionStageWSMessage(nativeStage, upstreamMessage); stageErr != nil {
				_ = nativeStage.Discard()
				if disconnectErr := openAIWSClientReadPumpDisconnectError(ctx); disconnectErr != nil {
					clientDisconnected = true
					nativeValidation = nativeValidator.Finish()
					return nil, disconnectErr
				}
				return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, joinOpenAINativeCompactionStageSemanticError(stageErr))
			}
		}
		if nativeValidator == nil {
			if normalized, changed := normalizeCompletedImageGenerationStatus(upstreamMessage); changed {
				upstreamMessage = normalized
			}
		}
		eventType, eventResponseID, _ := parseOpenAIWSEventEnvelope(upstreamMessage)
		if nativeValidator == nil && eventType == "" && pendingSSEEventType != "" {
			trimmedData = openAICompatPayloadWithEventType(trimmedData, pendingSSEEventType)
			upstreamMessage = []byte(trimmedData)
			eventType = pendingSSEEventType
		}
		pendingSSEEventType = ""
		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}
		if eventType != "" {
			eventCount++
			if firstEventType == "" {
				firstEventType = eventType
			}
			lastEventType = eventType
		}
		if isOpenAIWSTokenEvent(eventType) {
			tokenEventCount++
			if firstTokenMs == nil {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
		}
		if openAIWSEventShouldParseUsage(eventType) {
			if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(upstreamMessage); ok {
				usage = parsedUsage
				usageObserved = true
			}
		}
		imageCounter.AddSSEData(upstreamMessage)
		if needModelReplace && len(mappedModelBytes) > 0 && openAIWSEventMayContainModel(eventType) && strings.Contains(trimmedData, mappedModel) {
			upstreamMessage = replaceOpenAIWSMessageModel(upstreamMessage, mappedModel, originalModel)
		}
		if s.toolCorrector != nil && openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
			if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
				upstreamMessage = corrected
			}
		}
		replayCollector.AddEvent(eventType, upstreamMessage)
		retryableFailureAfterOutput := false
		capacityMessage := extractOpenAISSEErrorMessage(upstreamMessage)
		if isOpenAITransientProcessingError(http.StatusBadRequest, capacityMessage, upstreamMessage) {
			if turn == 1 && !wroteDownstream && !clientDisconnected {
				return nil, s.newOpenAIStreamFailoverError(
					c,
					account,
					true,
					strings.TrimSpace(resp.Header.Get("x-request-id")),
					upstreamMessage,
					capacityMessage,
				)
			}
			if sanitized, changed := sanitizeOpenAIStreamErrorEventForClient(upstreamMessage, eventType, true); changed {
				upstreamMessage = sanitized
				eventType, _, _ = parseOpenAIWSEventEnvelope(upstreamMessage)
			}
			retryableFailureAfterOutput = true
		}

		if eventType == "error" {
			errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(upstreamMessage)
			if nativeCompaction {
				if continuationCode := openAINativeCompactionContinuationErrorCode(errCodeRaw); continuationCode != "" {
					_ = nativeStage.Discard()
					return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, newOpenAINativeCompactionContinuationError(continuationCode))
				}
			}
			errMessage := strings.TrimSpace(errMsgRaw)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			statusCode := openAIWSErrorHTTPStatusFromRaw(errCodeRaw, errTypeRaw)
			shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(statusCode, errMessage, upstreamMessage)
			if account.Platform == PlatformGrok {
				if isGrokContentPolicyRejection(http.StatusForbidden, upstreamMessage) {
					shouldFailover = false
				} else {
					shouldFailover = s.shouldFailoverGrokUpstreamError(statusCode, upstreamMessage)
					s.handleGrokAccountUpstreamError(ctx, account, statusCode, resp.Header, upstreamMessage)
				}
				if turn == 1 && !wroteDownstream && !clientDisconnected && shouldFailover {
					return nil, newOpenAIUpstreamFailoverError(statusCode, resp.Header, upstreamMessage, errMessage, false)
				}
			} else {
				if shouldFailover {
					accountStatus := statusCode
					if transientStatus := openAIWSPayloadTransientStatus(upstreamMessage); transientStatus != 0 {
						accountStatus = transientStatus
					}
					signalPersisted := isOpenAIWSRateLimitError(errCodeRaw, errTypeRaw, errMsgRaw) || statusCode == http.StatusForbidden
					if !signalPersisted {
						s.handleOpenAIAccountUpstreamError(ctx, account, accountStatus, resp.Header, upstreamMessage, canonicalOpenAIAccountSchedulingModel(account, routingModel))
					}
				}
				s.persistOpenAIWSErrorSignal(ctx, account, resp.Header, upstreamMessage, errCodeRaw, errTypeRaw, errMsgRaw)
				if turn == 1 && !wroteDownstream && !clientDisconnected && shouldFailover {
					return nil, newOpenAIUpstreamFailoverError(statusCode, resp.Header, upstreamMessage, errMessage, false)
				}
			}
			if shouldFailover {
				if sanitized, changed := sanitizeOpenAIStreamErrorEventForClient(upstreamMessage, eventType, true); changed {
					upstreamMessage = sanitized
				}
				if account.Platform != PlatformGrok {
					s.recordOpenAIWSFinalErrorEventPassiveAccountFailure(ctx, account, upstreamMessage, errCodeRaw, errTypeRaw, errMsgRaw)
				}
				retryableFailureAfterOutput = true
			} else if account.Platform != PlatformGrok {
				s.recordOpenAIWSFinalErrorEventPassiveAccountFailure(ctx, account, upstreamMessage, errCodeRaw, errTypeRaw, errMsgRaw)
			}
		}
		if eventType == "response.failed" {
			failedMessage := extractOpenAISSEErrorMessage(upstreamMessage)
			if nativeCompaction {
				if continuationCode := openAINativeCompactionContinuationErrorCode(extractUpstreamErrorCode(upstreamMessage)); continuationCode != "" {
					_ = nativeStage.Discard()
					return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, newOpenAINativeCompactionContinuationError(continuationCode))
				}
			}
			if openAIStreamEventShouldFailover(upstreamMessage, eventType, failedMessage) {
				if !wroteDownstream && !clientDisconnected {
					return resultWithUsage(), s.newOpenAIStreamFailoverError(
						c,
						account,
						true,
						strings.TrimSpace(resp.Header.Get("x-request-id")),
						upstreamMessage,
						failedMessage,
					)
				}
				if sanitized, changed := sanitizeOpenAIStreamErrorEventForClient(upstreamMessage, eventType, true); changed {
					upstreamMessage = sanitized
				}
				retryableFailureAfterOutput = true
			}
		}

		if !clientDisconnected && nativeStage == nil {
			if openAIStreamEventDefersUntilSuccessfulTerminal(eventType) {
				holdingTerminalTail = true
				pendingTerminalTail = append(pendingTerminalTail, append([]byte(nil), upstreamMessage...))
				continue
			}
			flushTerminalTail := false
			if holdingTerminalTail {
				switch {
				case openAIStreamEventIsSuccessfulTerminal(upstreamMessage, eventType):
					flushTerminalTail = true
					holdingTerminalTail = false
				case openAIStreamEventDropsDeferredTail(upstreamMessage, eventType):
					pendingTerminalTail = nil
					holdingTerminalTail = false
				default:
					pendingTerminalTail = append(pendingTerminalTail, append([]byte(nil), upstreamMessage...))
					continue
				}
			}
			if !wroteDownstream && openAIStreamEventIsPreamble(eventType) {
				pendingPreamble = append(pendingPreamble, append([]byte(nil), upstreamMessage...))
				continue
			}
			for _, pendingMessage := range pendingPreamble {
				if err := emitClientMessage(pendingMessage); err != nil {
					return nil, err
				}
				if clientDisconnected {
					break
				}
			}
			pendingPreamble = nil
			if flushTerminalTail {
				for _, pendingMessage := range pendingTerminalTail {
					if err := emitClientMessage(pendingMessage); err != nil {
						return nil, err
					}
					if clientDisconnected {
						break
					}
				}
				pendingTerminalTail = nil
			}
			if !clientDisconnected {
				if err := emitClientMessage(upstreamMessage); err != nil {
					return nil, err
				}
			}
		}

		if retryableFailureAfterOutput {
			return resultWithUsage(), newOpenAIWSRetryableCloseError("upstream retryable failure after downstream output")
		}
		if eventType == "error" {
			_, _, errMsgRaw := parseOpenAIWSErrorEventFields(upstreamMessage)
			errMessage := strings.TrimSpace(errMsgRaw)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			return resultWithUsage(), errors.New(errMessage)
		}
		if isOpenAIWSTerminalEvent(eventType) {
			if nativeValidator != nil {
				validation := nativeValidator.Finish()
				nativeValidation = validation
				if !validation.Valid() {
					_ = nativeStage.Discard()
					semanticErr := newOpenAINativeCompactionSemanticError(validation.Outcome)
					return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, semanticErr)
				}
				nativeDeliveryStarted = true
				if commitErr := commitOpenAINativeCompactionWSMessages(nativeStage, emitClientMessage); commitErr != nil {
					return nil, wrapOpenAIWSIngressTurnError("native_compaction_commit", commitErr, true)
				}
				nativeDeliveryCommitted = !clientDisconnected
			}
			upstreamTerminalEvent = s.handleOpenAIWSTerminalTransientFailure(ctx, account, canonicalOpenAIAccountSchedulingModel(account, routingModel), resp.Header, upstreamMessage)
			terminalEventCount++
			firstTokenMsValue := -1
			if firstTokenMs != nil {
				firstTokenMsValue = *firstTokenMs
			}
			logOpenAIWSModeInfo(
				"ingress_ws_http_bridge_turn_completed account_id=%d turn=%d response_id=%s payload_bytes=%d duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
				payloadBytes,
				time.Since(turnStart).Milliseconds(),
				eventCount,
				tokenEventCount,
				terminalEventCount,
				truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
				firstTokenMsValue,
				clientDisconnected,
			)
			return resultWithUsage(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		if nativeValidator != nil {
			validation := nativeValidator.Finish()
			nativeValidation = validation
			_ = nativeStage.Discard()
			if disconnectErr := openAIWSClientReadPumpDisconnectError(ctx); disconnectErr != nil {
				clientDisconnected = true
				return nil, disconnectErr
			}
			semanticErr := newOpenAINativeCompactionSemanticError(validation.Outcome)
			return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, semanticErr)
		}
		streamErr := fmt.Errorf("read upstream http bridge stream: %w", err)
		if !wroteDownstream && !clientDisconnected {
			if turn == 1 {
				return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, streamErr, true)
			}
			return resultWithUsage(), streamErr
		}
		if wroteDownstream && !clientDisconnected {
			return resultWithUsage(), newOpenAIWSRetryableCloseError("upstream stream interrupted after downstream output")
		}
		return resultWithUsage(), streamErr
	}
	if nativeValidator != nil {
		validation := nativeValidator.Finish()
		nativeValidation = validation
		_ = nativeStage.Discard()
		if disconnectErr := openAIWSClientReadPumpDisconnectError(ctx); disconnectErr != nil {
			clientDisconnected = true
			return nil, disconnectErr
		}
		semanticErr := newOpenAINativeCompactionSemanticError(validation.Outcome)
		return nil, s.newOpenAINativeCompactionWSFailoverError(c, account, true, resp.Header, semanticErr)
	}
	terminalErr := errors.New("upstream http bridge stream ended before terminal event")
	if sawDone {
		terminalErr = errors.New("upstream http bridge stream sent [DONE] before terminal event")
	}
	if !wroteDownstream && !clientDisconnected {
		if turn == 1 {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, terminalErr, true)
		}
		return resultWithUsage(), terminalErr
	}
	if wroteDownstream && !clientDisconnected {
		return resultWithUsage(), newOpenAIWSRetryableCloseError("upstream stream ended before terminal event")
	}
	return resultWithUsage(), terminalErr
}

func resolveGrokWSCacheIdentity(c *gin.Context, account *Account, payload []byte, routingModel string) (string, error) {
	body, err := prepareOpenAIWSHTTPBridgeBody(payload, false)
	if err != nil {
		return "", err
	}
	upstreamModel := resolveGrokWSUpstreamModel(account, body, routingModel)
	body, err = patchGrokResponsesBody(body, upstreamModel)
	if err != nil {
		return "", err
	}
	return resolveGrokCacheIdentity(c, body, "", upstreamModel), nil
}

func resolveGrokWSUpstreamModel(account *Account, body []byte, originalModel string) string {
	upstreamModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if account != nil && originalModel != "" {
		if mappedModel := normalizeOpenAIModelForUpstream(account, account.GetMappedModel(originalModel)); mappedModel != "" {
			upstreamModel = mappedModel
		}
	}
	if upstreamModel == "" {
		upstreamModel = grokDefaultResponsesModel
	}
	return upstreamModel
}
