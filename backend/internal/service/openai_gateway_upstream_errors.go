package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/TokenFlux/TokenRouter/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func logOpenAIInstructionsRequiredDebug(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	upstreamStatusCode int,
	upstreamMsg string,
	requestBody []byte,
	upstreamBody []byte,
) {
	msg := strings.TrimSpace(upstreamMsg)
	if !isOpenAIInstructionsRequiredError(upstreamStatusCode, msg, upstreamBody) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	accountID := int64(0)
	accountName := ""
	if account != nil {
		accountID = account.ID
		accountName = strings.TrimSpace(account.Name)
	}

	userAgent := ""
	originator := ""
	if c != nil {
		userAgent = strings.TrimSpace(c.GetHeader("User-Agent"))
		originator = strings.TrimSpace(c.GetHeader("originator"))
	}

	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.String("account_name", accountName),
		zap.Int("upstream_status_code", upstreamStatusCode),
		zap.String("upstream_error_message", msg),
		zap.String("request_user_agent", userAgent),
		zap.Bool("codex_official_client_match", openai.IsCodexOfficialClientByHeaders(userAgent, originator)),
	}
	fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, requestBody)

	logger.FromContext(ctx).With(fields...).Warn("OpenAI 上游返回 Instructions are required，已记录请求详情用于排查")
}

func isOpenAIInstructionsRequiredError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest {
		return false
	}

	hasInstructionRequired := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "instructions are required") {
			return true
		}
		if strings.Contains(lower, "required parameter: 'instructions'") {
			return true
		}
		if strings.Contains(lower, "required parameter: instructions") {
			return true
		}
		if strings.Contains(lower, "missing required parameter") && strings.Contains(lower, "instructions") {
			return true
		}
		return strings.Contains(lower, "instruction") && strings.Contains(lower, "required")
	}

	if hasInstructionRequired(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}

	errMsg := gjson.GetBytes(upstreamBody, "error.message").String()
	errMsgLower := strings.ToLower(strings.TrimSpace(errMsg))
	errCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String()))
	errParam := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.param").String()))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String()))

	if errParam == "instructions" {
		return true
	}
	if hasInstructionRequired(errMsg) {
		return true
	}
	if strings.Contains(errCode, "missing_required_parameter") && strings.Contains(errMsgLower, "instructions") {
		return true
	}
	if strings.Contains(errType, "invalid_request") && strings.Contains(errMsgLower, "instructions") && strings.Contains(errMsgLower, "required") {
		return true
	}

	return false
}

func hasOpenAITransientOverloadCode(payload []byte) bool {
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(payload)))
	return code == "server_is_overloaded" || code == "slow_down"
}

func isOpenAITransientProcessingError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if len(upstreamBody) > 0 && hasOpenAITransientOverloadCode(upstreamBody) {
		authoritativeMsg := strings.TrimSpace(extractUpstreamErrorMessage(upstreamBody))
		if authoritativeMsg == "" {
			authoritativeMsg = upstreamMsg
		}
		if isOpenAIContextWindowError(authoritativeMsg, nil) || isOpenAIKnownCyberWarningError(authoritativeMsg, nil) {
			return false
		}
		return true
	}
	if upstreamStatusCode != http.StatusBadRequest && (upstreamStatusCode < 500 || upstreamStatusCode > 599) {
		return false
	}
	if isOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	if isOpenAIKnownCyberWarningError(upstreamMsg, upstreamBody) {
		return false
	}
	if hasAuthoritativeEmbeddedUpstreamErrorMessage(upstreamBody) {
		return false
	}

	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "an error occurred while processing your request") {
			return true
		}
		if strings.Contains(lower, "selected model is at capacity") {
			return true
		}
		if strings.Contains(lower, "experiencing high demand") {
			return true
		}
		if strings.Contains(lower, "server is overloaded") ||
			strings.Contains(lower, "model is overloaded") ||
			strings.Contains(lower, "temporarily overloaded") ||
			strings.Contains(lower, "currently overloaded") {
			return true
		}
		if strings.Contains(lower, "temporarily unavailable") {
			return strings.Contains(lower, "model") ||
				strings.Contains(lower, "service") ||
				strings.Contains(lower, "server")
		}
		return strings.Contains(lower, "you can retry your request") &&
			strings.Contains(lower, "help.openai.com") &&
			strings.Contains(lower, "request id")
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	for _, text := range []string{
		extractUpstreamErrorMessage(upstreamBody),
		gjson.GetBytes(upstreamBody, "error.message").String(),
		gjson.GetBytes(upstreamBody, "response.error.message").String(),
		gjson.GetBytes(upstreamBody, "response.status_details.error.message").String(),
		gjson.GetBytes(upstreamBody, "message").String(),
		gjson.GetBytes(upstreamBody, "detail").String(),
	} {
		if match(text) {
			return true
		}
	}
	if errorValue := gjson.GetBytes(upstreamBody, "error"); errorValue.Type == gjson.String && match(errorValue.String()) {
		return true
	}
	if gjson.ValidBytes(upstreamBody) {
		return false
	}
	return match(string(upstreamBody))
}

// IsOpenAITransientCapacityErrorBody reports capacity/overload failures while
// preserving authoritative invalid-request and policy classifications.
func IsOpenAITransientCapacityErrorBody(body []byte) bool {
	return isOpenAITransientProcessingError(http.StatusBadRequest, extractUpstreamErrorMessage(body), body)
}

func isOpenAIKnownCyberWarningError(upstreamMsg string, payload []byte) bool {
	if len(payload) > 0 {
		if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
			return true
		}
		if hasAuthoritativeEmbeddedUpstreamErrorMessage(payload) {
			return false
		}
	}
	if IsOpenAICyberWarningText(upstreamMsg) {
		return true
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"response.status_details.error.message",
		"message",
		"detail",
	} {
		if IsOpenAICyberWarningText(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	if errorValue := gjson.GetBytes(payload, "error"); errorValue.Type == gjson.String {
		return IsOpenAICyberWarningText(errorValue.String())
	}
	return false
}

// OpenAIPropertyNameAboveMaxLengthCode 是 OpenAI 对超长参数属性名返回的稳定错误码。
const OpenAIPropertyNameAboveMaxLengthCode = "property_name_above_max_length"

// isOpenAIClientInvalidRequestError 仅识别已确认由客户端参数触发的 OpenAI 400。
// 不能只判断 invalid_request_error，否则会把网关字段转换错误也排除出 SLA。
func isOpenAIClientInvalidRequestError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest || len(upstreamBody) == 0 {
		return false
	}
	if isOpenAITransientProcessingError(upstreamStatusCode, upstreamMsg, upstreamBody) {
		return false
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String()))
	errCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String()))
	return errType == "invalid_request_error" && errCode == OpenAIPropertyNameAboveMaxLengthCode
}

// isOpenAIOpaqueUpstreamBadRequest identifies a provider-side wrapper that
// reports an upstream failure with HTTP 400 but exposes no client-fixable
// detail. Another account can use a different upstream path, so this narrow
// shape should retain the handler's account failover opportunity.
func isOpenAIOpaqueUpstreamBadRequest(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest || !gjson.ValidBytes(upstreamBody) {
		return false
	}
	errType := strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String())
	if !strings.EqualFold(errType, "upstream_error") {
		return false
	}
	errCode := strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String())
	if errCode != "" && !strings.EqualFold(errCode, "upstream_error") {
		return false
	}
	if strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.param").String()) != "" {
		return false
	}
	message := strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = strings.TrimSpace(extractUpstreamErrorMessage(upstreamBody))
	}
	return strings.EqualFold(message, "Upstream request failed")
}

func isOpenAIContextWindowError(upstreamMsg string, upstreamBody []byte) bool {
	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "context_too_large") || strings.Contains(lower, "context_length_exceeded") {
			return true
		}
		if strings.Contains(lower, "maximum context length") || strings.Contains(lower, "max context length") {
			return true
		}
		hasExceeded := strings.Contains(lower, "exceed") || strings.Contains(lower, "too large") || strings.Contains(lower, "too long")
		if strings.Contains(lower, "context window") && hasExceeded {
			return true
		}
		if strings.Contains(lower, "context length") && hasExceeded {
			return true
		}
		return strings.Contains(lower, "token limit") &&
			strings.Contains(lower, "context") &&
			hasExceeded
	}

	if len(upstreamBody) == 0 {
		return match(upstreamMsg)
	}
	validJSON := gjson.ValidBytes(upstreamBody)
	if validJSON {
		for _, path := range []string{
			"error.code",
			"response.error.code",
			"response.status_details.error.code",
			"code",
		} {
			if match(gjson.GetBytes(upstreamBody, path).String()) {
				return true
			}
		}
		if hasAuthoritativeEmbeddedUpstreamErrorMessage(upstreamBody) {
			return false
		}
	}
	if match(upstreamMsg) {
		return true
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"response.status_details.error.message",
		"message",
		"detail",
	} {
		if match(gjson.GetBytes(upstreamBody, path).String()) {
			return true
		}
	}
	if errorValue := gjson.GetBytes(upstreamBody, "error"); errorValue.Type == gjson.String && match(errorValue.String()) {
		return true
	}
	if validJSON {
		return false
	}
	return match(string(upstreamBody))
}

func (s *OpenAIGatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 402, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func (s *OpenAIGatewayService) shouldFailoverOpenAIUpstreamResponse(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if hasOpenAITransientOverloadCode(upstreamBody) {
		return isOpenAITransientProcessingError(statusCode, upstreamMsg, upstreamBody)
	}
	if isOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	if IsOpenAICyberWarningPayload(upstreamBody, upstreamMsg) {
		return false
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	if isOpenAIOpaqueUpstreamBadRequest(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	if isOpenAITransientProcessingError(statusCode, upstreamMsg, upstreamBody) {
		return true
	}
	if s.shouldFailoverUpstreamError(statusCode) {
		return true
	}
	return false
}

// OpenAIRequestBodyTooLargeClientMessage 是账号级请求体限制切号耗尽后使用的固定下游文案。
const OpenAIRequestBodyTooLargeClientMessage = "Request payload is too large"

const openAIRequestBodyTooLargeReason = GatewayFailureReason("openai_request_body_too_large")

const openAIRequestBlockedReason = GatewayFailureReason("openai_request_blocked")

func isOpenAIRequestBlockedError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	if isOpenAIRequestBlockedMessage(upstreamMsg) {
		return true
	}
	if isOpenAIRequestBlockedMessage(extractUpstreamErrorMessage(upstreamBody)) {
		return true
	}
	if isOpenAIRequestBlockedMessage(extractUpstreamErrorMessageFromEmbeddedJSON(upstreamMsg)) {
		return true
	}
	return !gjson.ValidBytes(upstreamBody) && isOpenAIRequestBlockedMessage(string(upstreamBody))
}

func isOpenAIRequestBlockedMessage(message string) bool {
	message = strings.TrimSpace(message)
	message = strings.TrimSuffix(message, ".")
	return strings.EqualFold(message, "Your request was blocked")
}

func isOpenAIRequestBodyTooLargeError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	return statusCode == http.StatusRequestEntityTooLarge && !isOpenAIContextWindowError(upstreamMsg, upstreamBody)
}

func newOpenAIUpstreamFailoverError(
	statusCode int,
	responseHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	retryableOnSameAccount bool,
) *UpstreamFailoverError {
	failoverErr := &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           responseBody,
		ResponseHeaders:        responseHeaders.Clone(),
		RetryableOnSameAccount: retryableOnSameAccount,
	}
	if isOpenAIRequestBlockedError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameAccount = false
		failoverErr.Scope = GatewayFailureScopeRequest
		failoverErr.Reason = openAIRequestBlockedReason
		failoverErr.NextAccountAction = NextAccountRetry
		return failoverErr
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameAccount = false
		failoverErr.Scope = GatewayFailureScopeAccount
		failoverErr.Reason = openAIRequestBodyTooLargeReason
		failoverErr.NextAccountAction = NextAccountRetry
		failoverErr.ClientStatusCode = http.StatusRequestEntityTooLarge
		failoverErr.ClientMessage = OpenAIRequestBodyTooLargeClientMessage
	}
	return failoverErr
}

// IsOpenAIRequestBodyTooLarge 表示当前账号因序列化请求大小拒绝请求，但其他账号仍可能接受。
func (e *UpstreamFailoverError) IsOpenAIRequestBodyTooLarge() bool {
	return e != nil && e.Reason == openAIRequestBodyTooLargeReason
}

func marshalOpenAIUpstreamJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

func openAIUpstreamErrorBodyReadLimitForConfig(cfg *config.Config) int64 {
	limit := openAIUpstreamErrorBodyReadLimit
	if cfg != nil && cfg.Gateway.LogUpstreamErrorBody && cfg.Gateway.LogUpstreamErrorBodyMaxBytes > int(limit) {
		limit = int64(cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
	}
	return limit
}

func (s *OpenAIGatewayService) readUpstreamErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	cfg := (*config.Config)(nil)
	if s != nil {
		cfg = s.cfg
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimitForConfig(cfg)))
	return body
}

func (s *OpenAIGatewayService) handleFailoverSideEffects(ctx context.Context, resp *http.Response, account *Account, responseBody []byte, canonicalModel ...string) bool {
	if len(canonicalModel) > 0 {
		return s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody, canonicalModel[0])
	}
	return s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody)
}

func (s *OpenAIGatewayService) recordOpenAIPassiveAccountFailure(ctx context.Context, account *Account, statusCode int, responseBody []byte) {
	if s == nil || s.rateLimitService == nil || account == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.rateLimitService.recordPassiveAccountFailure(ctx, account, statusCode, responseBody)
}

func openAIRequestContextOrBackground(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}

func (s *OpenAIGatewayService) handleErrorResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	body := s.readUpstreamErrorBody(resp)
	body = s.redactAgentIdentitySensitiveBody(ctx, account, body)

	if hit, code, cyberMsg := detectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           code,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		setOpsUpstreamError(c, resp.StatusCode, cyberMsg, truncateString(string(body), 2048))
		clientMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(cyberMsg))
		if clientMsg == "" {
			clientMsg = "Upstream request failed"
		}
		handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, resp.StatusCode, "upstream_error", clientMsg)
		if writeErr != nil {
			return nil, fmt.Errorf("write committed Responses failure: %w", writeErr)
		}
		if !handled {
			writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "application/json"
			}
			MarkResponseCommitted(c)
			c.Data(resp.StatusCode, contentType, body)
		}
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}
	if account != nil && account.Platform == PlatformGrok && isGrokContentPolicyRejection(resp.StatusCode, body) {
		clientMsg := grokContentPolicyClientMessage(body)
		setOpsUpstreamError(c, resp.StatusCode, clientMsg, truncateString(string(body), 2048))
		writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		MarkResponseCommitted(c)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": clientMsg,
			},
		})
		return nil, fmt.Errorf("grok content policy rejection: %s", clientMsg)
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

	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		logger.LegacyPrintf("service.openai_gateway",
			"OpenAI upstream error %d (account=%d platform=%s type=%s): %s",
			resp.StatusCode,
			account.ID,
			account.Platform,
			account.Type,
			truncateForLog(body, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
		)
	}

	if IsOpenAICyberWarningPayload(body, upstreamMsg) {
		errMsg := ExtractOpenAICyberWarningMessage(body, upstreamMsg)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            errMsg,
			Detail:             upstreamDetail,
		})
		handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, resp.StatusCode, "invalid_request_error", errMsg)
		if writeErr != nil {
			return nil, fmt.Errorf("write committed Responses failure: %w", writeErr)
		}
		if !handled {
			c.JSON(resp.StatusCode, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": errMsg,
				},
			})
		}
		return nil, wrapOpenAIUpstreamWarningIfCyber(resp.StatusCode, body, errMsg, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, errMsg))
	}

	if isOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, body) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "failover",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, requestedModel...)
		return nil, newOpenAIUpstreamFailoverError(
			resp.StatusCode,
			resp.Header,
			body,
			upstreamMsg,
			false,
		)
	}

	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c,
		PlatformOpenAI,
		resp.StatusCode,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		s.recordOpenAIPassiveAccountFailure(ctx, account, resp.StatusCode, body)
		handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, status, errType, errMsg)
		if writeErr != nil {
			return nil, fmt.Errorf("write committed Responses failure: %w", writeErr)
		}
		if !handled {
			MarkResponseCommitted(c)
			c.JSON(status, gin.H{
				"error": gin.H{
					"type":    errType,
					"message": errMsg,
				},
			})
		}
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	if isOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body) {
		// 参数型 400 不影响账号健康；保留上游上下文供 Ops 排障，并向客户端透传完整错误结构。
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		MarkResponseCommitted(c)
		c.Data(http.StatusBadRequest, contentType, body)
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream invalid request: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// Check custom error codes
	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		s.recordOpenAIPassiveAccountFailure(ctx, account, resp.StatusCode, body)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
		if writeErr != nil {
			return nil, fmt.Errorf("write committed Responses failure: %w", writeErr)
		}
		if !handled {
			MarkResponseCommitted(c)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"type":    "upstream_error",
					"message": "Upstream gateway error",
				},
			})
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Handle upstream error (mark account status)
	var reqModel string
	if len(requestedModel) > 0 {
		reqModel = strings.TrimSpace(requestedModel[0])
	}
	if reqModel == "" {
		reqModel, _, _ = extractOpenAIRequestMetaFromBody(requestBody)
		reqModel = canonicalOpenAIAccountSchedulingModel(account, reqModel)
	}
	shouldDisable := s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, reqModel)
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			RetryableOnSameAccount: false,
		}
	}

	// Return appropriate error response
	var errType, errMsg string
	var statusCode int

	switch resp.StatusCode {
	case 401:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case 402:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream payment required: insufficient balance or billing issue"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}
	if isOpenAIContextWindowError(upstreamMsg, body) && upstreamMsg != "" {
		errMsg = upstreamMsg
	}

	handled, writeErr := writeOpenAIResponsesFailureIfCommitted(c, statusCode, errType, errMsg)
	if writeErr != nil {
		return nil, fmt.Errorf("write committed Responses failure: %w", writeErr)
	}
	if !handled {
		MarkResponseCommitted(c)
		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": errMsg,
			},
		})
	}

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// compatErrorWriter 由兼容协议路径写入普通错误信封。
type compatErrorWriter func(c *gin.Context, statusCode int, errType, message string)

// compatErrorBodyWriter 由兼容协议路径写入包含 code/param 的完整脱敏错误对象。
type compatErrorBodyWriter func(c *gin.Context, statusCode int, body []byte)

// handleCompatErrorResponse 是 Chat Completions 与 Anthropic Messages 共用的非故障转移错误处理器。
func (s *OpenAIGatewayService) handleCompatErrorResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	writeError compatErrorWriter,
	writeErrorBody compatErrorBodyWriter,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	body := s.readUpstreamErrorBody(resp)
	body = s.redactAgentIdentitySensitiveBody(context.Background(), account, body)

	if hit, code, cyberMsg := detectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           code,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		setOpsUpstreamError(c, resp.StatusCode, cyberMsg, truncateString(string(body), 2048))
		clientMsg := cyberMsg
		if clientMsg == "" {
			clientMsg = "Request blocked by upstream cyber-security policy"
		}
		MarkResponseCommitted(c)
		writeError(c, resp.StatusCode, "invalid_request_error", clientMsg)
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}
	if account != nil && account.Platform == PlatformGrok && isGrokContentPolicyRejection(resp.StatusCode, body) {
		clientMsg := grokContentPolicyClientMessage(body)
		setOpsUpstreamError(c, resp.StatusCode, clientMsg, truncateString(string(body), 2048))
		MarkResponseCommitted(c)
		writeError(c, http.StatusForbidden, "invalid_request_error", clientMsg)
		return nil, fmt.Errorf("grok content policy rejection: %s", clientMsg)
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if upstreamMsg == "" {
		upstreamMsg = fmt.Sprintf("Upstream error: %d", resp.StatusCode)
	}
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

	// Apply error passthrough rules
	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c, account.Platform, resp.StatusCode, body,
		http.StatusBadGateway, "api_error", "Upstream request failed",
	); matched {
		s.recordOpenAIPassiveAccountFailure(openAIRequestContextOrBackground(c), account, resp.StatusCode, body)
		MarkResponseCommitted(c)
		writeError(c, status, errType, errMsg)
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	if isOpenAIClientInvalidRequestError(resp.StatusCode, upstreamMsg, body) {
		// 兼容协议也必须保留上游 error 对象中的 code、param 等结构化详情。
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeErrorBody(c, http.StatusBadRequest, body)
		return nil, fmt.Errorf("upstream invalid request: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// Check custom error codes — if the account does not handle this status,
	// return a generic error without exposing upstream details.
	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		s.recordOpenAIPassiveAccountFailure(openAIRequestContextOrBackground(c), account, resp.StatusCode, body)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Track rate limits and decide whether to trigger secondary failover.
	var modelForCooldown string
	if len(requestedModel) > 0 {
		modelForCooldown = requestedModel[0]
	}
	shouldDisable := s.handleOpenAIAccountUpstreamError(
		c.Request.Context(), account, resp.StatusCode, resp.Header, body, modelForCooldown,
	)
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			RetryableOnSameAccount: false,
		}
	}

	MarkResponseCommitted(c)

	// Map status code to error type and write response
	errType := "api_error"
	switch {
	case resp.StatusCode == 400:
		errType = "invalid_request_error"
	case resp.StatusCode == 404:
		errType = "not_found_error"
	case resp.StatusCode == 429:
		errType = "rate_limit_error"
	case resp.StatusCode >= 500:
		errType = "api_error"
	}

	writeError(c, resp.StatusCode, errType, upstreamMsg)
	return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
}
