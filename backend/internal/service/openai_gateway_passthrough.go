package service

// 本文件承载 /v1/responses 透传转发及其流式、非流式响应与错误处理。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

func (s *OpenAIGatewayService) forwardOpenAIPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	canonicalImageIntentBody []byte,
	reqModel string,
	attemptImageIntentInvalidated bool,
	reasoningEffort *string,
	reqStream bool,
	startTime time.Time,
	tlsRouterMatch ...TLSFingerprintRouterMatchResult,
) (*OpenAIForwardResult, error) {
	upstreamPassthroughModel := ""
	if isOpenAIResponsesCompactPath(c) {
		compactMappedModel := resolveOpenAICompactForwardModel(account, reqModel)
		if compactMappedModel != "" && compactMappedModel != reqModel {
			nextBody, setErr := sjson.SetBytes(body, "model", compactMappedModel)
			if setErr != nil {
				return nil, fmt.Errorf("set compact passthrough model: %w", setErr)
			}
			body = nextBody
			upstreamPassthroughModel = compactMappedModel
			attemptImageIntentInvalidated = true
		}
	}

	if account != nil && account.Type == AccountTypeOAuth {
		if rejectReason := detectOpenAIPassthroughInstructionsRejectReason(reqModel, body); rejectReason != "" {
			rejectMsg := "OpenAI codex passthrough requires a non-empty instructions field"
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			logOpenAIPassthroughInstructionsRejected(ctx, c, account, reqModel, rejectReason, body)
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"type":    "forbidden_error",
					"message": rejectMsg,
				},
			})
			return nil, fmt.Errorf("openai passthrough rejected before upstream: %s", rejectReason)
		}

		normalizedBody, normalized, err := normalizeOpenAIPassthroughOAuthBody(body, isOpenAIResponsesCompactPath(c))
		if err != nil {
			return nil, err
		}
		if normalized {
			body = normalizedBody
		}
		reqStream = gjson.GetBytes(body, "stream").Bool()
	}

	sanitizedBody, sanitized, err := sanitizeEmptyBase64InputImagesInOpenAIBody(body)
	if err != nil {
		return nil, err
	}
	if sanitized {
		body = sanitizedBody
	}

	policyModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if policyModel == "" {
		policyModel = reqModel
	}
	updatedBody, policyErr := s.applyOpenAIFastPolicyToBody(ctx, account, policyModel, body)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, policyErr
	}
	body = updatedBody

	apiKey := getAPIKeyFromContext(c)
	// 宽泛意图保留给图片状态和计费，显式意图单独负责权限门禁。
	imageIntent := resolveOpenAIPassthroughImageIntent(
		c,
		reqModel,
		canonicalImageIntentBody,
		policyModel,
		body,
		attemptImageIntentInvalidated,
		IsImageGenerationIntent,
	)
	explicitImageIntent := IsExplicitImageGenerationIntent(openAIResponsesEndpoint, policyModel, body)
	if explicitImageIntent && !GroupAllowsImageGeneration(apiKeyGroup(apiKey)) {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "permission_error",
				"message": ImageGenerationPermissionMessage(),
			},
		})
		return nil, errors.New("image generation disabled for group")
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfgErr error
		imageCfg, imageCfgErr := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, reqModel)
		if imageCfgErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, imageCfgErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": imageCfgErr.Error(),
					"param":   "size",
				},
			})
			return nil, imageCfgErr
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}

	logger.LegacyPrintf("service.openai_gateway",
		"[OpenAI 自动透传] 命中自动透传分支: account=%d name=%s type=%s model=%s stream=%v",
		account.ID,
		account.Name,
		account.Type,
		reqModel,
		reqStream,
	)
	if reqStream && c != nil && c.Request != nil {
		if timeoutHeaders := collectOpenAIPassthroughTimeoutHeaders(c.Request.Header); len(timeoutHeaders) > 0 {
			streamWarnLogger := logger.FromContext(ctx).With(
				zap.String("component", "service.openai_gateway"),
				zap.Int64("account_id", account.ID),
				zap.Strings("timeout_headers", timeoutHeaders),
			)
			if s.isOpenAIPassthroughTimeoutHeadersAllowed() {
				streamWarnLogger.Warn("OpenAI passthrough 透传请求包含超时相关请求头，且当前配置为放行，可能导致上游提前断流")
			} else {
				streamWarnLogger.Warn("OpenAI passthrough 检测到超时相关请求头，将按配置过滤以降低断流风险")
			}
		}
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	if c != nil {
		c.Set("openai_passthrough", true)
	}

	nativeRemoteCompactionV2 := IsOpenAINativeRemoteCompactionV2(c)
	agentTaskRecoveryTried := false
	rejectedFieldRetryState := newOpenAIResponsesRejectedFieldRetryState(body)
	var resp *http.Response
	var attempt *openAIUpstreamAttemptCoordinator
	maxOutputCapabilityKey := OpenAIResponsesMaxOutputTokensCapabilityKey{}
	maxOutputCapabilityKeyOK := false
	for {
		upstreamCtx, releaseUpstreamCtx := openAIUpstreamContextForCompactionAttempt(ctx, nativeRemoteCompactionV2)
		upstreamReq, buildErr := s.buildUpstreamRequestOpenAIPassthrough(upstreamCtx, c, account, body, token, tlsRouterMatch...)
		releaseUpstreamCtx()
		if buildErr != nil {
			return nil, buildErr
		}

		maxOutputCapabilityKey = OpenAIResponsesMaxOutputTokensCapabilityKey{}
		maxOutputCapabilityKeyOK = false
		if !nativeRemoteCompactionV2 {
			maxOutputEffectiveModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
			if maxOutputEffectiveModel == "" {
				maxOutputEffectiveModel = reqModel
			}
			maxOutputCapabilityKey, maxOutputCapabilityKeyOK = s.prepareOpenAIResponsesMaxOutputTokensCapability(ctx, account, body, maxOutputEffectiveModel)
		}
		attempt = s.beginOpenAINativeHTTPAttempt(ctx, c, account, reqModel, openAIServiceTierIsPriority(extractOpenAIServiceTierFromBody(body)))
		upstreamStart := time.Now()
		resp, err = s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.resolveOpenAITLSProfile(account, tlsRouterMatch...))
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		attempt.observeTransport(resp)
		if err != nil {
			attempt.finishHTTPError(resp, string(OpenAINativeCompactionTransportFailure), true)
			// 未收到 HTTP 响应时交给外层切换账号，持久故障仍由统一处理器临时摘除。
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		}
		if resp.StatusCode < 400 {
			break
		}

		// 只读取一次响应体判断 task 是否失效；恢复失败时仍把原响应交给既有错误路径。
		probeBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(probeBody))
		attempt.finishHTTPError(resp, string(OpenAINativeCompactionHTTPFailure), true)
		if maxOutputCapabilityKeyOK && IsExplicitOpenAIResponsesMaxOutputTokensUnsupported(resp.StatusCode, probeBody) {
			s.observeOpenAIResponsesMaxOutputTokensCapability(ctx, account, maxOutputCapabilityKey, OpenAIResponsesMaxOutputTokensCapabilityUnsupported, resp.StatusCode, "explicit_unsupported_parameter")
			if s.cfg != nil && s.cfg.Gateway.StrictOutputLimit {
				return nil, NewOpenAIResponsesMaxOutputTokensUnsupportedFailoverError(resp.StatusCode)
			}
		}
		if !agentTaskRecoveryTried && s.isAgentIdentityAccount(ctx, account) && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, probeBody) {
			agentTaskRecoveryTried = true
			expectedTaskID := account.GetCredential("task_id")
			if recoveryErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoveryErr != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", recoveryErr)
			}
			continue
		}
		if !nativeRemoteCompactionV2 {
			retryBody, reason, changed, retryErr := normalizeOpenAIResponsesRejectedFieldRetryBodyWithPolicy(resp.StatusCode, body, probeBody, s.cfg != nil && s.cfg.Gateway.StrictOutputLimit)
			if retryErr != nil {
				return nil, fmt.Errorf("normalize rejected Responses field retry body: %w", retryErr)
			}
			if changed && rejectedFieldRetryState.Allow(retryBody) {
				body = retryBody
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI 透传] 上游拒绝字段后降级重试: account=%d reason=%s", account.ID, reason)
				continue
			}
		}

		// 透传模式默认保持原样代理；容量错误以及 API-key 上游的瞬时
		// 5xx 应先触发多账号 failover；probeBody 已在 task 探测时读取，不再重复消费响应体。
		if continuationCode := openAINativeCompactionContinuationErrorCode(extractUpstreamErrorCode(probeBody)); nativeRemoteCompactionV2 && continuationCode != "" {
			return nil, newOpenAINativeCompactionHTTPContinuationFailoverError(resp.StatusCode, resp.Header, continuationCode, false)
		}
		if shouldFailoverOpenAIPassthroughResponse(account, resp.StatusCode, probeBody) {
			return nil, s.handleFailoverErrorResponsePassthrough(ctx, resp, c, account, body, probeBody)
		}
		return nil, s.handleErrorResponsePassthrough(ctx, resp, c, account, body, probeBody)
	}
	defer func() { _ = resp.Body.Close() }()
	serviceTier := extractOpenAIServiceTierFromBody(body)

	var usage *OpenAIUsage
	var firstTokenMs *int
	responseID := ""
	clientDisconnect := false
	imageCount := 0
	var imageOutputSizes []string
	var responseBody []byte
	if reqStream {
		result, err := s.handleStreamingResponsePassthrough(ctx, resp, c, account, startTime, reqModel, upstreamPassthroughModel)
		attempt.finishStreamingPassthrough(result, err)
		if err != nil {
			s.quarantineOpenAINativeCompactionFailure(ctx, c, account, upstreamPassthroughModel, err)
			return nil, err
		}
		usage = result.usage
		firstTokenMs = result.firstTokenMs
		responseID = strings.TrimSpace(result.responseID)
		clientDisconnect = result.clientDisconnect
		imageCount = result.imageCount
		imageOutputSizes = result.imageOutputSizes
		responseBody = result.responseBody
	} else {
		result, err := s.handleNonStreamingResponsePassthrough(ctx, resp, c, account, reqModel, upstreamPassthroughModel)
		attempt.finishNonStreamingPassthrough(result, err)
		if err != nil {
			return nil, err
		}
		usage = result.usage
		responseID = strings.TrimSpace(result.responseID)
		clientDisconnect = result.clientDisconnect
		imageCount = result.imageCount
		imageOutputSizes = result.imageOutputSizes
		responseBody = result.responseBody
	}
	if maxOutputCapabilityKeyOK {
		s.observeOpenAIResponsesMaxOutputTokensCapability(ctx, account, maxOutputCapabilityKey, OpenAIResponsesMaxOutputTokensCapabilitySupported, resp.StatusCode, "successful_terminal")
	}
	s.bindHTTPResponseAccount(ctx, c, account, responseID)

	if !account.IsShadow() {
		if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
			s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
		}
	}

	if usage == nil {
		usage = &OpenAIUsage{}
	}

	forwardResult := &OpenAIForwardResult{
		RequestID:        resp.Header.Get("x-request-id"),
		ResponseID:       responseID,
		Usage:            *usage,
		Model:            reqModel,
		UpstreamModel:    upstreamPassthroughModel,
		ServiceTier:      serviceTier,
		ReasoningEffort:  reasoningEffort,
		Stream:           reqStream,
		OpenAIWSMode:     false,
		ResponseBody:     cloneDataSharingRequestBody(responseBody),
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: clientDisconnect,
	}
	if imageCount > 0 {
		forwardResult.ImageCount = imageCount
		forwardResult.ImageSize = imageSizeTier
		forwardResult.ImageInputSize = imageInputSize
		forwardResult.ImageOutputSizes = imageOutputSizes
		forwardResult.BillingModel = imageBillingModel
	}
	finalizeOpenAINativeHTTPForwardResult(c, forwardResult, account, attempt)
	return forwardResult, nil
}

func logOpenAIPassthroughInstructionsRejected(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	reqModel string,
	rejectReason string,
	body []byte,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := int64(0)
	accountName := ""
	accountType := ""
	if account != nil {
		accountID = account.ID
		accountName = strings.TrimSpace(account.Name)
		accountType = strings.TrimSpace(string(account.Type))
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.String("account_name", accountName),
		zap.String("account_type", accountType),
		zap.String("request_model", strings.TrimSpace(reqModel)),
		zap.String("reject_reason", strings.TrimSpace(rejectReason)),
	}
	fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, body)
	logger.FromContext(ctx).With(fields...).Warn("OpenAI passthrough 本地拦截：Codex 请求缺少有效 instructions")
}

func (s *OpenAIGatewayService) buildUpstreamRequestOpenAIPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
	routerMatch ...TLSFingerprintRouterMatchResult,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIURL
	switch account.Type {
	case AccountTypeOAuth:
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		baseURL := account.GetOpenAIBaseURL()
		if baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesURL(validatedURL)
		}
	}
	targetURL = appendOpenAIResponsesRequestPathSuffix(targetURL, openAIResponsesRequestPathSuffix(c))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	allowTimeoutHeaders := s.isOpenAIPassthroughTimeoutHeadersAllowed()
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			if !isOpenAIPassthroughAllowedRequestHeader(lower, allowTimeoutHeaders) {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	if account.Type == AccountTypeOAuth {
		promptCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
		req.Host = "chatgpt.com"
		if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, req.Header, account); err != nil {
			return nil, fmt.Errorf("resolve chatgpt account headers: %w", err)
		}
		apiKeyID := getAPIKeyIDFromContext(c)

		clientSessionID := strings.TrimSpace(req.Header.Get("session_id"))
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", codexCLIVersion)
			}
			if clientSessionID == "" {
				clientSessionID = resolveOpenAICompactSessionID(c)
			}
		} else if req.Header.Get("accept") == "" {
			req.Header.Set("accept", "text/event-stream")
		}
		if req.Header.Get("OpenAI-Beta") == "" {
			req.Header.Set("OpenAI-Beta", "responses=experimental")
		}
		if req.Header.Get("originator") == "" {
			req.Header.Set("originator", "codex_cli_rs")
		}
		if len(routerMatch) > 0 && routerMatch[0].Matched {
			if originator := strings.TrimSpace(routerMatch[0].UpstreamOriginator); originator != "" {
				req.Header.Set("originator", originator)
			}
		}

		if clientSessionID == "" {
			clientSessionID = promptCacheKey
		}
		if clientConversationID == "" {
			clientConversationID = promptCacheKey
		}
		if clientSessionID != "" {
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, clientSessionID))
		}
		if clientConversationID != "" {
			req.Header.Set("conversation_id", isolateOpenAISessionID(apiKeyID, clientConversationID))
		}
	} else if isOpenAIResponsesCompactPath(c) {
		// 透传白名单会放行客户端的 Accept: text/event-stream；compact 上游是
		// unary JSON 协议，API-key 账号同样强制 Accept，避免上游按 SSE 返回
		// （#3777 期望行为 4）。
		req.Header.Set("accept", "application/json")
	}

	s.applyOpenAIUpstreamUserAgent(ctx, c, account, req, true, routerMatch...)

	// 终态收口：originator 必须与最终 User-Agent 首段配套且为官方身份，非官方 UA 整体回退为
	// 默认 Codex CLI 身份（承接原「非 Codex UA 安全兜底」，并修复其把 codex-tui 等官方 UA 改写为
	// codex_cli_rs 造成的 originator 错配 404），详见 issue #3901。
	if account.Type == AccountTypeOAuth {
		enforceCodexIdentityHeaders(req.Header)
	}

	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}

	account.ApplyHeaderOverrides(req.Header)

	return req, nil
}

func shouldFailoverOpenAIPassthroughResponse(account *Account, statusCode int, responseBody []byte) bool {
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	if isOpenAIContextWindowError(upstreamMsg, responseBody) {
		return false
	}
	if IsOpenAICyberWarningPayload(responseBody, upstreamMsg) {
		return false
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, responseBody) {
		return true
	}
	if isOpenAITransientProcessingError(statusCode, upstreamMsg, responseBody) {
		return true
	}
	switch statusCode {
	case http.StatusTooManyRequests, 529:
		return true
	}
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	switch statusCode {
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}

// writeOpenAIPassthroughErrorHeaders 仅保留可安全转发的错误响应头，避免泄露上游信息。
func writeOpenAIPassthroughErrorHeaders(dst, src http.Header) {
	if dst == nil {
		return
	}
	dst.Set("Content-Type", "application/json; charset=utf-8")
	dst.Set("Cache-Control", "no-store")
	dst.Del("Retry-After")
	if src == nil {
		return
	}
	rawRetryAfter := strings.TrimSpace(src.Get("Retry-After"))
	if validOpenAIPassthroughRetryAfter(rawRetryAfter, time.Now()) {
		dst.Set("Retry-After", rawRetryAfter)
	}
}

// validOpenAIPassthroughRetryAfter 校验 Retry-After 是否为正整数秒或未来的 HTTP 时间。
func validOpenAIPassthroughRetryAfter(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	delaySeconds := true
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			delaySeconds = false
			break
		}
	}
	if delaySeconds {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		return err == nil && seconds > 0
	}
	parsed, err := http.ParseTime(raw)
	return err == nil && parsed.After(now)
}

// writeSanitizedOpenAIPassthroughError 使用本地错误信封替换不可信的上游错误正文。
func writeSanitizedOpenAIPassthroughError(c *gin.Context, upstreamStatus int, upstreamHeaders http.Header) {
	downstreamStatus, message := sanitizedOpenAIPassthroughError(upstreamStatus)
	writeOpenAIPassthroughErrorEnvelope(c, downstreamStatus, upstreamHeaders, message)
}

func sanitizedOpenAIPassthroughError(upstreamStatus int) (int, string) {
	downstreamStatus := upstreamStatus
	message := "Upstream request failed"
	switch upstreamStatus {
	case http.StatusUnauthorized:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream authentication failed"
	case http.StatusForbidden:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream access denied"
	default:
		if upstreamStatus >= http.StatusInternalServerError {
			message = "Upstream service temporarily unavailable"
		}
	}
	return downstreamStatus, message
}

// writeOpenAIPassthroughErrorEnvelope 以本地 JSON 信封 + 净化后的头策略写出
// 错误响应；message 由调用方决定（净化通用文案或脱敏后的上游消息）。
func writeOpenAIPassthroughErrorEnvelope(c *gin.Context, downstreamStatus int, upstreamHeaders http.Header, message string) {
	if c == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	if handled, _ := writeOpenAICompactSSEBridge(c, downstreamStatus, body); handled {
		return
	}
	writeOpenAIPassthroughErrorHeaders(c.Writer.Header(), upstreamHeaders)
	c.Data(downstreamStatus, "application/json; charset=utf-8", body)
}

func (s *OpenAIGatewayService) handleFailoverErrorResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	responseBody []byte,
) error {
	body := s.redactAgentIdentitySensitiveBody(ctx, account, responseBody)

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)
	reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
	canonicalModel := canonicalOpenAIAccountSchedulingModel(account, reqModel)
	decision := s.applyOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, canonicalModel)
	if decision.ShouldReturnGenericError() {
		MarkResponseCommitted(c)
		writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
		return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "failover",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	return newOpenAIUpstreamFailoverError(
		resp.StatusCode,
		resp.Header,
		body,
		upstreamMsg,
		decision.RetryableOnSameAccount(account, resp.StatusCode),
	)
}

func (s *OpenAIGatewayService) handleErrorResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	responseBody []byte,
) error {
	body := s.redactAgentIdentitySensitiveBody(ctx, account, responseBody)

	// cyber_policy 仍按原始 body 打内部标记，供 handler 事后写风控/邮件；面向客户端的
	// 错误体在下方统一重建。cyber 是上游网络安全策略拦截，不冷却账号，
	// 故下方跳过 handleOpenAIAccountUpstreamError（避免自定义 temp-unschedulable 规则误冷却）。
	cyberHit, cyberCode, cyberMsg := detectOpenAICyberPolicy(body)
	if cyberHit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           cyberCode,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)
	clientInvalidRequest := isOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body)
	requestScopedError := cyberHit || clientInvalidRequest || isOpenAIContextWindowError(upstreamMsg, body) ||
		isOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, body)
	// 错误体虽不会原样透传，运行态账号状态仍需更新，避免粘性路由继续复用
	// 刚被限流的账号。请求级错误例外：不冷却账号，也不触发池模式重试。
	if !requestScopedError {
		reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
		canonicalModel := canonicalOpenAIAccountSchedulingModel(account, reqModel)
		decision := s.applyOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, canonicalModel)
		if decision.ShouldReturnGenericError() {
			MarkResponseCommitted(c)
			writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
			return fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		if decision.ShouldFailoverWithDefaults(account, resp.StatusCode, false, false) {
			return newOpenAIUpstreamFailoverError(
				resp.StatusCode,
				resp.Header,
				body,
				upstreamMsg,
				decision.RetryableOnSameAccount(account, resp.StatusCode),
			)
		}
	}
	MarkResponseCommitted(c)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "http_error",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	if clientInvalidRequest {
		// 参数型 400 使用安全响应头并透传完整脱敏错误对象，不再改写成 upstream_error。
		writeOpenAIPassthroughErrorHeaders(c.Writer.Header(), resp.Header)
		c.Data(http.StatusBadRequest, "application/json; charset=utf-8", body)
		return fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}
	downstreamStatus, clientMsg := sanitizedOpenAIPassthroughError(resp.StatusCode)
	if isOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		downstreamStatus = resp.StatusCode
		clientMsg = upstreamMsg
	}
	handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, downstreamStatus, "upstream_error", clientMsg)
	if writeErr != nil {
		return fmt.Errorf("write committed Responses failure: %w", writeErr)
	}
	if handled {
		return fmt.Errorf("upstream error: %d (client response sanitized)", resp.StatusCode)
	}
	// context-window 超限是确定性请求失败（shouldFailoverOpenAIPassthroughResponse
	// 已保证不切号），其文案对客户端可操作（如触发自动压缩）；在净化信封内保留
	// 脱敏后的上游消息，而不是抹成通用文案。
	if isOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		writeOpenAIPassthroughErrorEnvelope(c, resp.StatusCode, resp.Header, upstreamMsg)
	} else {
		writeSanitizedOpenAIPassthroughError(c, resp.StatusCode, resp.Header)
	}

	return fmt.Errorf("upstream error: %d (client response sanitized)", resp.StatusCode)
}

func isOpenAIPassthroughAllowedRequestHeader(lowerKey string, allowTimeoutHeaders bool) bool {
	if lowerKey == "" {
		return false
	}
	if isOpenAIPassthroughTimeoutHeader(lowerKey) {
		return allowTimeoutHeaders
	}
	return openaiPassthroughAllowedHeaders[lowerKey]
}

func isOpenAIPassthroughTimeoutHeader(lowerKey string) bool {
	switch lowerKey {
	case "x-stainless-timeout", "x-stainless-read-timeout", "x-stainless-connect-timeout", "x-request-timeout", "request-timeout", "grpc-timeout":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) isOpenAIPassthroughTimeoutHeadersAllowed() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIPassthroughAllowTimeoutHeaders
}

func collectOpenAIPassthroughTimeoutHeaders(h http.Header) []string {
	if h == nil {
		return nil
	}
	var matched []string
	for key, values := range h {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if isOpenAIPassthroughTimeoutHeader(lowerKey) {
			entry := lowerKey
			if len(values) > 0 {
				entry = fmt.Sprintf("%s=%s", lowerKey, strings.Join(values, "|"))
			}
			matched = append(matched, entry)
		}
	}
	sort.Strings(matched)
	return matched
}

type openaiStreamingResultPassthrough struct {
	usage             *OpenAIUsage
	usageObserved     bool
	firstTokenMs      *int
	responseID        string
	clientDisconnect  bool
	deliveryCommitted bool
	nativeValidation  OpenAINativeCompactionValidationResult
	imageCount        int
	imageOutputSizes  []string
	responseBody      []byte
}

type openaiNonStreamingResultPassthrough struct {
	*OpenAIUsage
	usage            *OpenAIUsage
	responseID       string
	clientDisconnect bool
	imageCount       int
	imageOutputSizes []string
	responseBody     []byte
}

type openAIStreamOutputBaseline struct {
	size int
}

func captureOpenAIStreamOutputBaseline(c *gin.Context) openAIStreamOutputBaseline {
	if c == nil || c.Writer == nil {
		return openAIStreamOutputBaseline{size: -1}
	}
	return openAIStreamOutputBaseline{size: OpenAISemanticWrittenSize(c)}
}

func openAIStreamClientOutputStarted(c *gin.Context, localStarted bool, baselines ...openAIStreamOutputBaseline) bool {
	if localStarted {
		return true
	}
	if c == nil || c.Writer == nil {
		return false
	}
	if len(baselines) == 0 {
		return c.Writer.Written()
	}
	baseline := baselines[0]
	return OpenAISemanticWrittenSize(c) > baseline.size
}

func openAIStreamEventIsPreamble(eventType string) bool {
	// Keep state-establishing events uncommitted until the first semantic event.
	// response.output_item.done is handled separately because Codex may execute a
	// tool as soon as that event is consumed.
	switch strings.TrimSpace(eventType) {
	case "response.created",
		"response.in_progress",
		"response.metadata",
		"codex.response.metadata",
		"codex.rate_limits",
		"response.output_item.added",
		"response.content_part.added",
		"response.content_part.done",
		"response.function_call_arguments.done",
		"response.custom_tool_call_input.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done":
		return true
	default:
		return false
	}
}

func openAIStreamEventDefersUntilSuccessfulTerminal(eventType string) bool {
	return strings.TrimSpace(eventType) == "response.output_item.done"
}

func openAIStreamEventIsSuccessfulTerminal(payload []byte, eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done":
		status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.status").String()))
		if status != "" && status != "completed" {
			return false
		}
		for _, path := range []string{"response.error", "response.status_details.error", "error"} {
			errorValue := gjson.GetBytes(payload, path)
			if !errorValue.Exists() || errorValue.Type == gjson.Null || strings.TrimSpace(errorValue.Raw) == "null" {
				continue
			}
			return false
		}
		return true
	default:
		return false
	}
}

func openAIStreamEventDropsDeferredTail(payload []byte, eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "error",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled":
		return true
	case "response.completed", "response.done":
		return !openAIStreamEventIsSuccessfulTerminal(payload, eventType)
	default:
		return false
	}
}

func openAIStreamDataStartsClientOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	if trimmed == "[DONE]" {
		return false
	}
	if strings.TrimSpace(eventType) == "response.failed" {
		return false
	}
	if openAIStreamEventDefersUntilSuccessfulTerminal(eventType) {
		return false
	}
	return !openAIStreamEventIsPreamble(eventType)
}

func openAIStreamEventShouldFailover(payload []byte, eventType, message string) bool {
	if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	switch strings.TrimSpace(eventType) {
	case "response.failed":
		return openAIStreamFailedEventShouldFailover(payload, message)
	default:
		return false
	}
}

func openAIStreamFailedEventSemanticStatus(payload []byte, message string) int {
	if isOpenAIContextWindowError(message, payload) {
		return http.StatusBadRequest
	}
	// 聚合上游可能在 HTTP 200 的 response.failed 中携带真实状态码；
	// 必须优先保留，才能让任意自定义错误码命中统一账号策略。
	for _, path := range []string{
		"response.error.status_code",
		"response.error.status",
		"error.status_code",
		"error.status",
	} {
		status := int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			return status
		}
	}

	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(payload)))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.status_details.error.type").String()))
	}
	combined := strings.TrimSpace(errType + " " + code + " " + strings.ToLower(strings.TrimSpace(message)))
	switch {
	case strings.Contains(errType, "invalid_request"):
		return http.StatusBadRequest
	case strings.Contains(combined, "rate_limit"):
		return http.StatusTooManyRequests
	case strings.Contains(combined, "authentication") || strings.Contains(combined, "unauthorized") || strings.Contains(combined, "invalid_api_key"):
		return http.StatusUnauthorized
	case strings.Contains(combined, "permission") || strings.Contains(combined, "forbidden") || strings.Contains(combined, "access denied"):
		return http.StatusForbidden
	case code == "server_is_overloaded" || code == "slow_down":
		return http.StatusServiceUnavailable
	case isOpenAITransientProcessingError(http.StatusBadRequest, message, payload):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// openAIStreamGenericFailedEventPayload 构造流已经提交后使用的通用失败终止事件。
func openAIStreamGenericFailedEventPayload() []byte {
	return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"type":"upstream_error","message":"Upstream gateway error"}}}`)
}

// applyOpenAIStreamFailedAccountPolicy 将 HTTP 200 流内失败接入统一账号策略。
// status 是事件推断出的语义状态码，仅用于策略、故障转移和最终错误分类。
func (s *OpenAIGatewayService) applyOpenAIStreamFailedAccountPolicy(
	ctx context.Context,
	account *Account,
	canonicalModel string,
	headers http.Header,
	payload []byte,
	message string,
) (int, UpstreamErrorDecision) {
	status := openAIStreamFailedEventSemanticStatus(payload, message)
	if account != nil && account.Platform == PlatformGrok {
		return status, s.applyGrokAccountUpstreamError(ctx, account, status, headers, payload, canonicalModel)
	}
	return status, s.applyOpenAIAccountUpstreamError(ctx, account, status, headers, payload, canonicalModel)
}

func openAIStreamFailedEventPassthroughBody(payload []byte, failedMessage string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if gjson.GetBytes(payload, "error").Exists() {
		return payload
	}
	responseError := gjson.GetBytes(payload, "response.error")
	if !responseError.Exists() {
		if strings.TrimSpace(failedMessage) == "" {
			return payload
		}
		body, err := marshalOpenAIUpstreamJSON(gin.H{
			"error": gin.H{
				"message": failedMessage,
			},
		})
		if err != nil {
			return payload
		}
		return body
	}

	errorPayload := gin.H{}
	if errType := strings.TrimSpace(gjson.Get(responseError.Raw, "type").String()); errType != "" {
		errorPayload["type"] = errType
	}
	if code := strings.TrimSpace(gjson.Get(responseError.Raw, "code").String()); code != "" {
		errorPayload["code"] = code
	}
	if param := strings.TrimSpace(gjson.Get(responseError.Raw, "param").String()); param != "" {
		errorPayload["param"] = param
	}
	message := strings.TrimSpace(gjson.Get(responseError.Raw, "message").String())
	if message == "" {
		message = strings.TrimSpace(failedMessage)
	}
	if message != "" {
		errorPayload["message"] = message
	}
	if len(errorPayload) == 0 {
		return payload
	}
	body, err := marshalOpenAIUpstreamJSON(gin.H{"error": errorPayload})
	if err != nil {
		return payload
	}
	return body
}

// applyOpenAIStreamFailedErrorPassthroughRule 对 response.failed 事件应用错误透传规则：
// 归一化 body 供关键词匹配/消息提取，并推断语义状态码使按错误码配置的规则可以命中。
// platform 必须传 account.Platform——本服务同时承载 openai 与 grok 平台账号，规则按平台匹配。
func applyOpenAIStreamFailedErrorPassthroughRule(
	c *gin.Context,
	platform string,
	payload []byte,
	failedMessage string,
) (status int, errType string, errMsg string, matched bool) {
	ruleBody := openAIStreamFailedEventPassthroughBody(payload, failedMessage)
	upstreamStatus := openAIStreamFailedEventSemanticStatus(payload, failedMessage)
	return applyErrorPassthroughRule(
		c,
		platform,
		upstreamStatus,
		ruleBody,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)
}

func openAIStreamFailedEventShouldFailover(payload []byte, message string) bool {
	if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	if isOpenAIContextWindowError(message, payload) {
		return false
	}
	if isOpenAIKnownCyberWarningError(message, payload) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(payload)))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.status_details.error.type").String()))
	}
	combined := strings.ToLower(strings.TrimSpace(message + " " + code + " " + errType))
	if combined == "" {
		return true
	}
	nonRetryableMarkers := []string{
		"invalid_request",
		"bad_request",
		"bad request",
		"content_policy",
		"policy",
		"safety",
		"high-risk cyber",
		"not allowed",
		"violat",
	}
	for _, marker := range nonRetryableMarkers {
		if strings.Contains(combined, marker) {
			return false
		}
	}
	return true
}

func (s *OpenAIGatewayService) recordOpenAIStreamUpstreamError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	kind string,
	payload []byte,
	message string,
) string {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI upstream response failed"
	}
	detail := ""
	if len(payload) > 0 && s != nil && s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(payload), maxBytes)
	}
	if c != nil {
		setOpsUpstreamError(c, http.StatusBadGateway, message, detail)
		event := OpsUpstreamErrorEvent{
			Platform:           PlatformOpenAI,
			UpstreamStatusCode: http.StatusBadGateway,
			UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
			Passthrough:        passthrough,
			Kind:               kind,
			Message:            message,
			Detail:             detail,
		}
		if account != nil {
			event.Platform = account.Platform
			event.AccountID = account.ID
			event.AccountName = account.Name
		}
		appendOpsUpstreamError(c, event)
	}
	return message
}

func (s *OpenAIGatewayService) newOpenAIStreamFailoverError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	payload []byte,
	message string,
) *UpstreamFailoverError {
	statusCode := http.StatusBadGateway
	if openAIStreamEventShouldFailover(payload, "response.failed", message) {
		statusCode = openAIStreamFailedEventSemanticStatus(payload, message)
	}
	return s.newOpenAIStreamPolicyFailoverError(
		c, account, passthrough, upstreamRequestID, nil, statusCode, payload, message, false,
	)
}

// newOpenAIStreamPolicyFailoverError 构造应用账号策略后的流内故障转移错误。
// 下游错误体保持统一封装，同时保留语义状态和上游响应头供 handler 最终处理。
func (s *OpenAIGatewayService) newOpenAIStreamPolicyFailoverError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	responseHeaders http.Header,
	statusCode int,
	payload []byte,
	message string,
	retryableOnSameAccount bool,
) *UpstreamFailoverError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI stream disconnected before completion"
	}
	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, upstreamRequestID, "failover", payload, message)
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	return newOpenAIUpstreamFailoverError(statusCode, responseHeaders, body, message, retryableOnSameAccount)
}

func (s *OpenAIGatewayService) handleStreamingResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	originalModel string,
	mappedModel string,
) (*openaiStreamingResultPassthrough, error) {
	if IsOpenAINativeRemoteCompactionV2(c) {
		result, err := s.handleOpenAINativeCompactionStreamingResponse(ctx, resp, c, account, startTime, originalModel, mappedModel, true)
		if result == nil {
			return nil, err
		}
		return &openaiStreamingResultPassthrough{
			usage:             result.usage,
			usageObserved:     result.usageObserved,
			firstTokenMs:      result.firstTokenMs,
			responseID:        result.responseID,
			clientDisconnect:  result.clientDisconnect,
			deliveryCommitted: result.deliveryCommitted,
			nativeValidation:  result.nativeValidation,
			imageCount:        result.imageCount,
			imageOutputSizes:  result.imageOutputSizes,
			responseBody:      result.responseBody,
		}, err
	}
	outputBaseline := captureOpenAIStreamOutputBaseline(c)
	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	_, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	responseID := ""
	clientDisconnected := false
	sawTerminalEvent := false
	sawFailedEvent := false
	failedMessage := ""
	var failedPayload []byte
	clientOutputStarted := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	// pendingLines 在首个可见输出前保留前导事件，确保无输出失败仍可安全 failover。
	pendingLines := make([]string, 0, 8)
	// flushPending 表示已写入但未到 SSE 空行边界的脏状态；defer 兜底函数退出前的残留，断连后不再 Flush。
	flushPending := false
	flushPendingOutput := func() {
		if clientDisconnected || !flushPending {
			return
		}
		if err := flushOpenAIResponseWriter(w); err != nil {
			MarkOpsStreamError(c, "downstream_flush_error", err.Error(), 0)
			clientDisconnected = true
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming flush, continue draining upstream for usage: account=%d", account.ID)
			return
		}
		flushPending = false
	}
	defer flushPendingOutput()
	pendingTerminalLines := make([]string, 0, 8)
	holdingTerminalTail := false
	pendingSSEEventType := ""
	writePendingLines := func() bool {
		for _, pending := range pendingLines {
			if _, err := fmt.Fprintln(w, pending); err != nil {
				MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: account=%d", account.ID)
				return false
			}
		}
		pendingLines = pendingLines[:0]
		return true
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer putSSEScannerBuf64K(scanBuf)
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)

	needModelReplace := strings.TrimSpace(originalModel) != "" && strings.TrimSpace(mappedModel) != "" && strings.TrimSpace(originalModel) != strings.TrimSpace(mappedModel)
	var finalResponseBody []byte
	responseAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	resultWithUsage := func() *openaiStreamingResultPassthrough {
		return &openaiStreamingResultPassthrough{
			usage:            usage,
			firstTokenMs:     firstTokenMs,
			responseID:       responseID,
			clientDisconnect: clientDisconnected,
			imageCount:       imageCounter.Count(),
			imageOutputSizes: imageCounter.Sizes(),
			responseBody:     cloneDataSharingRequestBody(finalResponseBody),
		}
	}

	nativeRemoteCompactionV2 := IsOpenAINativeRemoteCompactionV2(c)
	nativePingInterval := time.Duration(0)
	if nativeRemoteCompactionV2 && s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		nativePingInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	var nativePingWriterMu sync.Mutex
	nativePingLastWriteAt := time.Now()
	nativePingFrameComplete := true
	nativePingFinished := false
	nativePingWriteFailed := false
	stopNativePing := func() {}
	if nativePingInterval > 0 {
		stopCh := make(chan struct{})
		doneCh := make(chan struct{})
		go func() {
			defer close(doneCh)
			ticker := time.NewTicker(nativePingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
				}

				nativePingWriterMu.Lock()
				if nativePingFinished || nativePingWriteFailed {
					nativePingWriterMu.Unlock()
					return
				}
				if !nativePingFrameComplete || time.Since(nativePingLastWriteAt) < nativePingInterval {
					nativePingWriterMu.Unlock()
					continue
				}
				if _, err := writeOpenAINativeRemoteCompactionPing(c, w); err != nil {
					MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
					nativePingWriteFailed = true
					nativePingWriterMu.Unlock()
					return
				}
				if err := flushOpenAIResponseWriter(w); err != nil {
					MarkOpsStreamError(c, "downstream_flush_error", err.Error(), 0)
					nativePingWriteFailed = true
					nativePingWriterMu.Unlock()
					return
				}
				nativePingLastWriteAt = time.Now()
				nativePingWriterMu.Unlock()
			}
		}()
		stopNativePing = func() {
			close(stopCh)
			<-doneCh
		}
	}
	defer stopNativePing()

	streamOutputStarted := func() bool {
		if nativePingInterval <= 0 {
			return openAIStreamClientOutputStarted(c, clientOutputStarted, outputBaseline)
		}
		nativePingWriterMu.Lock()
		defer nativePingWriterMu.Unlock()
		return openAIStreamClientOutputStarted(c, clientOutputStarted, outputBaseline)
	}
	lockNativePingWriter := func() {
		if nativePingInterval > 0 {
			nativePingWriterMu.Lock()
			if nativePingWriteFailed {
				clientDisconnected = true
			}
		}
	}
	unlockNativePingWriter := func() {
		if nativePingInterval > 0 {
			if clientDisconnected {
				nativePingWriteFailed = true
			}
			nativePingWriterMu.Unlock()
		}
	}
	writeNativeTerminalFailure := func(message string) error {
		lockNativePingWriter()
		nativePingFinished = true
		if !nativePingFrameComplete {
			if _, err := fmt.Fprintln(w); err != nil {
				MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
				clientDisconnected = true
				unlockNativePingWriter()
				return err
			}
			nativePingFrameComplete = true
		}
		MarkResponseCommitted(c)
		err := writeOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", message)
		if err != nil {
			clientDisconnected = true
		}
		unlockNativePingWriter()
		return err
	}

	for documentScanner.Scan() {
		line := documentScanner.Text()
		if eventName, ok := extractOpenAISSEEventLine(line); ok {
			pendingSSEEventType = eventName
		} else if strings.TrimSpace(line) == "" {
			pendingSSEEventType = ""
		}
		lineStartsClientOutput := false
		forceFlushFailedEvent := false
		eventType := ""
		var eventPayload []byte
		if data, ok := extractOpenAISSEDataLine(line); ok {
			dataBytes := []byte(data)
			trimmedData := strings.TrimSpace(data)
			if trimmedData == "[DONE]" {
				pendingSSEEventType = ""
				continue
			}
			if needModelReplace && strings.Contains(data, mappedModel) {
				line = s.replaceModelInSSELine(line, mappedModel, originalModel)
				if replacedData, replaced := extractOpenAISSEDataLine(line); replaced {
					dataBytes = []byte(replacedData)
					trimmedData = strings.TrimSpace(replacedData)
				}
			}
			eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			if eventType == "" && pendingSSEEventType != "" {
				data = openAICompatPayloadWithEventType(string(dataBytes), pendingSSEEventType)
				dataBytes = []byte(data)
				trimmedData = strings.TrimSpace(data)
				line = "data: " + data
			}
			pendingSSEEventType = ""
			if normalizedData, normalized := normalizeOpenAIResponsesFunctionCallArguments(dataBytes); normalized {
				dataBytes = normalizedData
				trimmedData = strings.TrimSpace(string(normalizedData))
				line = "data: " + string(normalizedData)
			}
			if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				trimmedData = strings.TrimSpace(string(normalizedData))
				line = "data: " + string(normalizedData)
			}
			if trimmedData != "[DONE]" {
				restoredData, restoreErr := restoreOpenAIResponsesNamespacePayload(c, dataBytes)
				if restoreErr != nil {
					return resultWithUsage(), fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
				}
				if !bytes.Equal(restoredData, dataBytes) {
					dataBytes = restoredData
					trimmedData = strings.TrimSpace(string(restoredData))
					line = "data: " + string(restoredData)
				}
			}
			eventType = strings.TrimSpace(gjson.Get(trimmedData, "type").String())
			eventMessage := extractOpenAISSEErrorMessage(dataBytes)
			if !streamOutputStarted() &&
				isOpenAITransientProcessingError(http.StatusBadRequest, eventMessage, dataBytes) {
				return resultWithUsage(),
					s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, dataBytes, eventMessage)
			}
			if eventType == "response.failed" {
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				failedPayload = append(failedPayload[:0], dataBytes...)
				s.parseSSEUsageBytes(dataBytes, usage)
				if hit, code, msg := detectOpenAICyberPolicy(dataBytes); hit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				policyStatus, decision := s.applyOpenAIStreamFailedAccountPolicy(
					ctx, account, mappedModel, resp.Header, dataBytes, failedMessage,
				)
				outputStarted := streamOutputStarted()
				if !outputStarted && decision.ShouldReturnGenericError() {
					MarkResponseCommitted(c)
					writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
					return resultWithUsage(), fmt.Errorf("upstream response failed: status=%d (not in custom error codes)", policyStatus)
				}
				if !outputStarted && decision.ShouldFailover(
					account, policyStatus, openAIStreamFailedEventShouldFailover(dataBytes, failedMessage),
				) {
					return resultWithUsage(), s.newOpenAIStreamPolicyFailoverError(
						c, account, true, upstreamRequestID, resp.Header, policyStatus, dataBytes, failedMessage,
						decision.RetryableOnSameAccount(account, policyStatus),
					)
				}
				if outputStarted && decision.ShouldReturnGenericError() {
					dataBytes = openAIStreamGenericFailedEventPayload()
					trimmedData = string(dataBytes)
					line = "data: " + string(dataBytes)
					failedMessage = "Upstream gateway error"
				}
				if !outputStarted && !decision.ShouldReturnGenericError() {
					if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, account.Platform, dataBytes, failedMessage); matched {
						// 命中透传规则也要记录 ops 上游错误事件（对齐 CC/Messages 与
						// antigravity 先例），否则透传命中的 failed 在监控中不可见。
						s.recordOpenAIStreamUpstreamError(c, account, true, upstreamRequestID, "http_error", dataBytes, failedMessage)
						lockNativePingWriter()
						nativePingFinished = true
						handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, status, errType, errMsg)
						if !handled && writeErr == nil {
							MarkResponseCommitted(c)
							c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
							c.JSON(status, gin.H{
								"error": gin.H{
									"type":    errType,
									"message": errMsg,
								},
							})
						}
						unlockNativePingWriter()
						if writeErr != nil {
							return resultWithUsage(), fmt.Errorf("write committed Responses failure: %w", writeErr)
						}
						return resultWithUsage(), fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
			}
			if eventType == "error" {
				errorMessage := extractOpenAISSEErrorMessage(dataBytes)
				if !streamOutputStarted() &&
					openAIStreamEventShouldFailover(dataBytes, eventType, errorMessage) {
					return resultWithUsage(),
						s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, dataBytes, errorMessage)
				}
			}
			if openAIStreamEventIsTerminal(trimmedData) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			var responseEvent apicompat.ResponsesStreamEvent
			if err := json.Unmarshal(dataBytes, &responseEvent); err == nil {
				responseAccumulator.ProcessEvent(&responseEvent)
			}
			if imageOutput, ok := extractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			if normalizedData, normalized := normalizeResponsesStreamingTerminalOutput(dataBytes, responseAccumulator, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				trimmedData = strings.TrimSpace(data)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			if eventType == "response.completed" || eventType == "response.done" {
				if response := gjson.GetBytes(dataBytes, "response"); response.Exists() && response.Type == gjson.JSON && response.Raw != "" {
					finalResponseBody = []byte(response.Raw)

					if len(gjson.GetBytes(finalResponseBody, "output").Array()) == 0 {
						if outputJSON, reconstructed := buildResponsesOutputJSON(responseAccumulator, streamImageOutputs); reconstructed {
							if patched, err := sjson.SetRawBytes(finalResponseBody, "output", outputJSON); err == nil {
								finalResponseBody = patched
							}
						}
					}
				}
			}
			imageCounter.AddSSEData(dataBytes)
			if sanitizedData, sanitized := sanitizeOpenAIStreamErrorEventForClient(
				dataBytes,
				eventType,
				streamOutputStarted(),
			); sanitized {
				dataBytes = sanitizedData
				trimmedData = strings.TrimSpace(string(sanitizedData))
				line = "data: " + string(sanitizedData)
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
				if eventType == "response.failed" {
					forceFlushFailedEvent = true
					sawFailedEvent = true
					failedMessage = extractOpenAISSEErrorMessage(dataBytes)
					failedPayload = append(failedPayload[:0], dataBytes...)
				}
			}
			lineStartsClientOutput = forceFlushFailedEvent || openAIStreamDataStartsClientOutput(trimmedData, eventType)
			if firstTokenMs == nil && lineStartsClientOutput && trimmedData != "[DONE]" {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			if eventType != "response.failed" {
				s.parseSSEUsageBytes(dataBytes, usage)
			}
			eventPayload = dataBytes
		}

		if !clientDisconnected {
			if openAIStreamEventDefersUntilSuccessfulTerminal(eventType) {
				holdingTerminalTail = true
				pendingTerminalLines = append(pendingTerminalLines, line)
				continue
			}
			flushTerminalTail := false
			if holdingTerminalTail {
				switch {
				case openAIStreamEventIsSuccessfulTerminal(eventPayload, eventType):
					flushTerminalTail = true
					holdingTerminalTail = false
				case openAIStreamEventDropsDeferredTail(eventPayload, eventType):
					pendingTerminalLines = pendingTerminalLines[:0]
					holdingTerminalTail = false
				case eventType != "":
					pendingTerminalLines = append(pendingTerminalLines, line)
					continue
				default:
					if eventName, ok := strings.CutPrefix(line, "event:"); ok &&
						openAIStreamEventDefersUntilSuccessfulTerminal(strings.TrimSpace(eventName)) {
						holdingTerminalTail = true
					}
					pendingTerminalLines = append(pendingTerminalLines, line)
					continue
				}
			} else if eventName, ok := strings.CutPrefix(line, "event:"); ok &&
				openAIStreamEventDefersUntilSuccessfulTerminal(strings.TrimSpace(eventName)) {
				holdingTerminalTail = true
				pendingTerminalLines = append(pendingTerminalLines, line)
				continue
			}
			if !clientOutputStarted && !lineStartsClientOutput {
				pendingLines = append(pendingLines, line)
				continue
			}
			lockNativePingWriter()
			if clientDisconnected {
				unlockNativePingWriter()
				continue
			}
			if !clientOutputStarted && len(pendingLines) > 0 {
				if !writePendingLines() {
					unlockNativePingWriter()
					continue
				}
			}
			if flushTerminalTail {
				for _, pending := range pendingTerminalLines {
					if _, err := fmt.Fprintln(w, pending); err != nil {
						MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
						clientDisconnected = true
						break
					}
				}
				pendingTerminalLines = pendingTerminalLines[:0]
				if clientDisconnected {
					unlockNativePingWriter()
					continue
				}
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: account=%d", account.ID)
			} else {
				clientOutputStarted = true
				flushPending = true
				nativePingFrameComplete = strings.TrimSpace(line) == ""
				if nativePingInterval > 0 {
					nativePingLastWriteAt = time.Now()
					nativePingFinished = sawTerminalEvent
				}
				if line == "" {
					flushPendingOutput()
				}
			}
			unlockNativePingWriter()
		}
	}
	if err := documentScanner.Err(); err != nil {
		if sawTerminalEvent && !sawFailedEvent {
			s.clearOpenAIProxyStreamDisconnect(account)
			return resultWithUsage(), nil
		}
		if sawFailedEvent {
			err := fmt.Errorf("upstream response failed: %s", failedMessage)
			return resultWithUsage(), wrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, failedPayload, failedMessage, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
		}
		if errors.Is(err, bufio.ErrTooLong) {
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, err)
			if nativeRemoteCompactionV2 && streamOutputStarted() && !clientDisconnected {
				if writeErr := writeNativeTerminalFailure("response_too_large"); writeErr != nil {
					return resultWithUsage(), fmt.Errorf("SSE line too long: %v; write terminal failure: %w", err, writeErr)
				}
			}
			return resultWithUsage(), err
		}
		if !streamOutputStarted() {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(err.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(),
				s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, nil, msg)
		}
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", err)
		}
		s.recordOpenAIProxyStreamDisconnect(account, err, upstreamRequestID)
		if nativeRemoteCompactionV2 {
			if writeErr := writeNativeTerminalFailure("OpenAI stream disconnected before completion"); writeErr != nil {
				return resultWithUsage(), fmt.Errorf("stream read error: %v; write terminal failure: %w", err, writeErr)
			}
		}
		logger.LegacyPrintf("service.openai_gateway",
			"[OpenAI passthrough] 流读取异常中断: account=%d request_id=%s err=%v",
			account.ID,
			upstreamRequestID,
			err,
		)
		return resultWithUsage(), fmt.Errorf("stream read error: %w", err)
	}
	if sawFailedEvent {
		err := fmt.Errorf("upstream response failed: %s", failedMessage)
		return resultWithUsage(), wrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, failedPayload, failedMessage, err)
	}
	if !clientDisconnected && !sawTerminalEvent && ctx.Err() == nil {
		logger.FromContext(ctx).With(
			zap.String("component", "service.openai_gateway"),
			zap.Int64("account_id", account.ID),
			zap.String("upstream_request_id", upstreamRequestID),
		).Info("OpenAI passthrough 上游流在未收到 [DONE] 时结束，疑似断流")
		if !streamOutputStarted() {
			return resultWithUsage(),
				s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, nil, "OpenAI stream ended before a terminal event")
		}
		s.recordOpenAIProxyStreamDisconnect(account, errors.New("stream ended before terminal event"), upstreamRequestID)
		if nativeRemoteCompactionV2 {
			if writeErr := writeNativeTerminalFailure("OpenAI stream ended before a terminal event"); writeErr != nil {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event; write terminal failure: %w", writeErr)
			}
		}
		return resultWithUsage(), errors.New("stream usage incomplete: missing terminal event")
	}
	if sawTerminalEvent && !sawFailedEvent {
		s.clearOpenAIProxyStreamDisconnect(account)
	}

	return resultWithUsage(), nil
}

func (s *OpenAIGatewayService) handleNonStreamingResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	mappedModel string,
) (*openaiNonStreamingResultPassthrough, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}

	bodyLooksLikeSSE := bodyHasSSEFraming(body)
	if isEventStreamResponse(resp.Header) || bodyLooksLikeSSE {
		return s.handlePassthroughSSEToJSON(ctx, resp, c, account, body, originalModel, mappedModel)
	}
	if IsOpenAITransientCapacityErrorBody(body) {
		return nil, s.newOpenAIStreamFailoverError(c, account, true, resp.Header.Get("x-request-id"), body, extractUpstreamErrorMessage(body))
	}

	usage := &OpenAIUsage{}
	usageParsed := false
	if len(body) > 0 {
		if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(body); ok {
			*usage = parsedUsage
			usageParsed = true
		}
	}
	if !usageParsed {

		usage = s.parseSSEUsageFromBody(string(body))
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
		body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
	}
	body, err = restoreOpenAIResponsesNamespacePayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", err)
	}
	compactHandled, compactWriteErr := writeOpenAICompactSSEBridge(c, resp.StatusCode, body)
	if !compactHandled {
		c.Data(resp.StatusCode, contentType, body)
	}
	return &openaiNonStreamingResultPassthrough{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		clientDisconnect: compactWriteErr != nil,
		imageCount:       countOpenAIResponseImageOutputsFromJSONBytes(body),
		imageOutputSizes: collectOpenAIResponseImageOutputSizesFromJSONBytes(body),
		responseBody:     cloneDataSharingRequestBody(body),
	}, nil
}

// handlePassthroughSSEToJSON converts an SSE response body into a JSON
// response for the passthrough path. It mirrors handleSSEToJSON while
// preserving passthrough payloads, except compact-only model remapping may
// rewrite model fields back to the original requested model.
func (s *OpenAIGatewayService) handlePassthroughSSEToJSON(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, body []byte, originalModel string, mappedModel string) (*openaiNonStreamingResultPassthrough, error) {
	bodyText := string(body)
	terminalType, terminalPayload, terminalOK := extractOpenAISSETerminalEvent(bodyText)
	if terminalOK {
		msg := extractOpenAISSEErrorMessage(terminalPayload)
		if openAIStreamEventShouldFailover(terminalPayload, terminalType, msg) {
			return nil, s.newOpenAIStreamFailoverError(c, account, true, resp.Header.Get("x-request-id"), terminalPayload, msg)
		}
	}
	finalResponse, ok := extractCodexFinalResponse(bodyText)

	usage := &OpenAIUsage{}
	if ok {
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse); parsed {
			*usage = parsedUsage
		}

		if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
			if outputJSON, reconstructed := reconstructResponseOutputFromSSE(bodyText); reconstructed {
				if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
					finalResponse = patched
				}
			}
		}
		finalResponse = supplementCompactionItemFromSSE(c, finalResponse, bodyText)
		body = finalResponse
		if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
			body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
		}

		body = s.correctToolCallsInResponseBody(body)
		restoredBody, restoreErr := restoreOpenAIResponsesNamespacePayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
		}
		body = restoredBody
	} else {
		if !terminalOK {
			return nil, s.newOpenAIStreamFailoverError(c, account, true, resp.Header.Get("x-request-id"), nil, "OpenAI stream ended before a terminal event")
		}
		msg := extractOpenAISSEErrorMessage(terminalPayload)
		if openAIStreamEventShouldFailover(terminalPayload, terminalType, msg) {
			return nil, s.newOpenAIStreamFailoverError(c, account, true, resp.Header.Get("x-request-id"), terminalPayload, msg)
		}
		if terminalType == "response.failed" {
			if msg == "" {
				msg = "Upstream compact response failed"
			}
			policyStatus, decision := s.applyOpenAIStreamFailedAccountPolicy(
				ctx, account, mappedModel, resp.Header, terminalPayload, msg,
			)
			if decision.ShouldReturnGenericError() {
				MarkResponseCommitted(c)
				writeOpenAIPassthroughErrorEnvelope(c, http.StatusInternalServerError, resp.Header, "Upstream gateway error")
				return nil, fmt.Errorf("upstream compact response failed: status=%d (not in custom error codes)", policyStatus)
			}
			if decision.ShouldFailover(account, policyStatus, openAIStreamFailedEventShouldFailover(terminalPayload, msg)) {
				return nil, s.newOpenAIStreamPolicyFailoverError(
					c, account, true, strings.TrimSpace(resp.Header.Get("x-request-id")), resp.Header,
					policyStatus, terminalPayload, msg, decision.RetryableOnSameAccount(account, policyStatus),
				)
			}
			err := s.writeOpenAINonStreamingProtocolError(resp, c, msg)
			return nil, wrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, terminalPayload, msg, err)
		}
		usage = s.parseSSEUsageFromBody(bodyText)
		if originalModel != "" && mappedModel != "" && originalModel != mappedModel {
			bodyText = s.replaceModelInSSEBody(bodyText, mappedModel, originalModel)
		}
		body = []byte(bodyText)
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	compactHandled, compactWriteErr := writeOpenAICompactSSEBridge(c, resp.StatusCode, body)
	if !compactHandled {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResultPassthrough{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		clientDisconnect: compactWriteErr != nil,
		imageCount:       countOpenAIImageOutputsFromSSEBody(bodyText),
		imageOutputSizes: collectOpenAIImageOutputSizesFromSSEBody(bodyText),
		responseBody:     cloneDataSharingRequestBody(body),
	}, nil
}

func writeOpenAIPassthroughResponseHeaders(dst http.Header, src http.Header, filter *responseheaders.CompiledHeaderFilter) {
	if dst == nil || src == nil {
		return
	}
	if filter != nil {
		responseheaders.WriteFilteredHeaders(dst, src, filter)
	} else {
		// 兜底：尽量保留最基础的 content-type
		if v := strings.TrimSpace(src.Get("Content-Type")); v != "" {
			dst.Set("Content-Type", v)
		}
	}
	// 透传模式强制放行 x-codex-* 响应头（若上游返回）。
	// 注意：真实 http.Response.Header 的 key 一般会被 canonicalize；但为了兼容测试/自建响应，
	// 这里用 EqualFold 做一次大小写不敏感的查找。
	getCaseInsensitiveValues := func(h http.Header, want string) []string {
		if h == nil {
			return nil
		}
		for k, vals := range h {
			if strings.EqualFold(k, want) {
				return vals
			}
		}
		return nil
	}

	for _, rawKey := range []string{
		"x-codex-primary-used-percent",
		"x-codex-primary-reset-after-seconds",
		"x-codex-primary-window-minutes",
		"x-codex-secondary-used-percent",
		"x-codex-secondary-reset-after-seconds",
		"x-codex-secondary-window-minutes",
		"x-codex-primary-over-secondary-limit-percent",
	} {
		vals := getCaseInsensitiveValues(src, rawKey)
		if len(vals) == 0 {
			continue
		}
		key := http.CanonicalHeaderKey(rawKey)
		dst.Del(key)
		for _, v := range vals {
			dst.Add(key, v)
		}
	}
}
