package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tiktoken-go/tokenizer"
	"go.uber.org/zap"
)

const (
	openAIResponsesInputItemTokenOverhead = 3
	openAIResponsesContentPartOverhead    = 1
	openAIInputTokensFallbackMinimum      = 1

	OpsInputTokensSourceKey        = "ops_input_tokens_source"
	OpsInputTokensSourceUpstream   = "upstream"
	OpsInputTokensSourceEstimate   = "local_estimate"
	inputTokensFallbackUnsupported = "endpoint_unsupported"
	inputTokensFallbackScopeDenied = "scope_denied"
)

type openAIInputTokensCountRequest struct {
	Model        string                    `json:"model"`
	Instructions string                    `json:"instructions,omitempty"`
	Input        json.RawMessage           `json:"input,omitempty"`
	Tools        []apicompat.ResponsesTool `json:"tools,omitempty"`
	ToolChoice   json.RawMessage           `json:"tool_choice,omitempty"`
}

type openAIInputTokensCountPrepared struct {
	Request         openAIInputTokensCountRequest
	OriginalModel   string
	NormalizedModel string
	BillingModel    string
	UpstreamModel   string
}

// EstimateGrokCountTokens 在本地估算 Anthropic 兼容的 count_tokens 请求。Grok 没有
// 兼容的 token 计数端点，因此该路径不选择账号、不读取凭据，也不调用上游。
func EstimateGrokCountTokens(body []byte) (int, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return 0, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}
	if strings.TrimSpace(anthropicReq.Model) == "" {
		return 0, fmt.Errorf("parse anthropic count_tokens request: model is required")
	}

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return 0, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	estimated, err := estimateOpenAIInputTokens(openAIInputTokensCountRequest{
		Model:        anthropicReq.Model,
		Instructions: responsesReq.Instructions,
		Input:        responsesReq.Input,
		Tools:        responsesReq.Tools,
		ToolChoice:   responsesReq.ToolChoice,
	})
	if err != nil {
		return 0, fmt.Errorf("estimate grok input tokens: %w", err)
	}
	if estimated < openAIInputTokensFallbackMinimum {
		estimated = openAIInputTokensFallbackMinimum
	}
	return estimated, nil
}

// ForwardCountTokensAsAnthropic 将 Anthropic /v1/messages/count_tokens 桥接到
// OpenAI POST /v1/responses/input_tokens，并返回 Anthropic 兼容结果。
func (s *OpenAIGatewayService) ForwardCountTokensAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) error {
	if account == nil {
		writeAnthropicCountTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI accounts")
		return fmt.Errorf("count_tokens: missing account")
	}

	prepared, err := prepareOpenAIInputTokensCountRequest(body, account, defaultMappedModel)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}
	capabilityKey, capabilityKeyOK := s.ensureOpenAIInputTokensCapabilityUnknown(ctx, account, prepared.UpstreamModel)

	upstreamBody, err := marshalOpenAIUpstreamJSON(prepared.Request)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("marshal openai input_tokens body: %w", err)
	}

	logger.L().Debug("openai count_tokens: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", prepared.OriginalModel),
		zap.String("normalized_model", prepared.NormalizedModel),
		zap.String("billing_model", prepared.BillingModel),
		zap.String("upstream_model", prepared.UpstreamModel),
	)

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("get access token: %w", err)
	}

	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("build input_tokens request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	attempt := s.beginOpenAINativeHTTPAttempt(ctx, c, account, prepared.UpstreamModel, false)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	attempt.observeTransport(resp)
	if err != nil {
		attempt.finishHTTPError(resp, string(OpenAINativeCompactionTransportFailure), true)
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		return &UpstreamFailoverError{
			StatusCode:                     http.StatusBadGateway,
			SuppressAccountScheduleFailure: true,
			Stage:                          GatewayFailureStageInference,
			Scope:                          GatewayFailureScopeRequest,
			Reason:                         GatewayFailureReason("input_tokens_transport_error"),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, nil)
	if err != nil {
		reason := "input_tokens_response_read_error"
		if errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			reason = "input_tokens_response_too_large"
		}
		attempt.finish(openAIUpstreamAttemptTerminal{
			outcome:          string(OpenAINativeCompactionIncompleteStream),
			deliveryObserved: true,
			safeToFailover:   true,
		})
		return newOpenAIInputTokensProtocolFailoverError(resp, reason)
	}

	if resp.StatusCode >= 400 {
		attempt.finishHTTPError(resp, string(OpenAINativeCompactionHTTPFailure), true)
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		if fallbackReason, ok := classifyOpenAIInputTokensLocalFallback(account, resp.StatusCode, respBody); ok {
			if capabilityKeyOK {
				state := OpenAIResponsesInputTokensCapabilityUnsupported
				if fallbackReason == inputTokensFallbackScopeDenied {
					state = OpenAIResponsesInputTokensCapabilityScopeDenied
				}
				s.observeOpenAIInputTokensCapability(ctx, account, capabilityKey, state, resp.StatusCode, fallbackReason)
			}
			writeOpenAIInputTokensFallback(c, account, prepared, resp.StatusCode, fallbackReason)
			return nil
		}
		var decision UpstreamErrorDecision
		if account.Platform == PlatformGrok {
			decision = s.applyGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, prepared.UpstreamModel)
		} else {
			decision = s.applyOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, prepared.UpstreamModel)
		}
		if decision.ShouldReturnGenericError() {
			writeAnthropicCountTokensError(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
			return fmt.Errorf("input_tokens upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		defaultFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
		if account.Platform == PlatformGrok {
			defaultFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody)
		}
		if decision.ShouldFailover(account, resp.StatusCode, defaultFailover) {
			return &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				ResponseHeaders:        resp.Header.Clone(),
				RetryableOnSameAccount: decision.RetryableOnSameAccount(account, resp.StatusCode),
			}
		}

		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = truncateString(string(respBody), maxBytes)
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "request_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})

		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			errMsg = "Rate limit exceeded"
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
			errMsg = "Upstream service temporarily unavailable"
		}
		writeAnthropicCountTokensError(c, resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("input_tokens upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("input_tokens upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	inputTokens, err := parseOpenAIInputTokensResponse(respBody)
	if err != nil {
		attempt.finish(openAIUpstreamAttemptTerminal{
			outcome:          string(OpenAINativeCompactionIncompleteStream),
			deliveryObserved: true,
			safeToFailover:   true,
		})
		return newOpenAIInputTokensProtocolFailoverError(resp, "input_tokens_invalid_response_schema")
	}
	attempt.finish(openAIUpstreamAttemptTerminal{
		outcome:           string(OpenAINativeCompactionValid),
		usage:             &OpenAIUsage{InputTokens: inputTokens},
		usageObserved:     true,
		deliveryObserved:  true,
		deliveryCommitted: true,
	})
	if capabilityKeyOK {
		s.observeOpenAIInputTokensCapability(ctx, account, capabilityKey, OpenAIResponsesInputTokensCapabilitySupported, resp.StatusCode, "upstream_success")
	}

	if c != nil {
		c.Set(OpsInputTokensSourceKey, OpsInputTokensSourceUpstream)
	}
	c.JSON(http.StatusOK, gin.H{
		"input_tokens": inputTokens,
	})
	return nil
}

func (s *OpenAIGatewayService) ensureOpenAIInputTokensCapabilityUnknown(
	ctx context.Context,
	account *Account,
	effectiveModel string,
) (OpenAIResponsesInputTokensCapabilityKey, bool) {
	if s == nil || s.openAIResponsesInputTokensCapabilityRepo == nil {
		return OpenAIResponsesInputTokensCapabilityKey{}, false
	}
	key, err := ResolveOpenAIResponsesInputTokensCapabilityKey(account, effectiveModel)
	if err != nil {
		logger.L().Warn("openai count_tokens: capability key unavailable",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
		return OpenAIResponsesInputTokensCapabilityKey{}, false
	}
	if _, err := s.openAIResponsesInputTokensCapabilityRepo.EnsureUnknown(ctx, account, key); err != nil {
		logger.L().Warn("openai count_tokens: capability initialization failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
	}
	return key, true
}

func (s *OpenAIGatewayService) observeOpenAIInputTokensCapability(
	ctx context.Context,
	account *Account,
	key OpenAIResponsesInputTokensCapabilityKey,
	state OpenAIResponsesInputTokensCapabilityState,
	statusCode int,
	outcome string,
) {
	if s == nil || s.openAIResponsesInputTokensCapabilityRepo == nil {
		return
	}
	if _, err := s.openAIResponsesInputTokensCapabilityRepo.UpsertObservation(ctx, account, OpenAIResponsesInputTokensCapabilityObservation{
		Key:         key,
		State:       state,
		StatusCode:  &statusCode,
		LastOutcome: outcome,
		CheckedAt:   time.Now().UTC(),
	}); err != nil {
		logger.L().Warn("openai count_tokens: capability observation failed",
			zap.Int64("account_id", account.ID),
			zap.String("state", string(state)),
			zap.Int("upstream_status", statusCode),
			zap.Error(err),
		)
	}
}

func prepareOpenAIInputTokensCountRequest(
	body []byte,
	account *Account,
	defaultMappedModel string,
) (*openAIInputTokensCountPrepared, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}

	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	billingModel := resolveOpenAIForwardModel(account, normalizedModel, strings.TrimSpace(defaultMappedModel))
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	return &openAIInputTokensCountPrepared{
		Request: openAIInputTokensCountRequest{
			Model:        upstreamModel,
			Instructions: responsesReq.Instructions,
			Input:        responsesReq.Input,
			Tools:        responsesReq.Tools,
			ToolChoice:   responsesReq.ToolChoice,
		},
		OriginalModel:   originalModel,
		NormalizedModel: normalizedModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func (s *OpenAIGatewayService) buildInputTokensUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIInputTokensURL
	if account.Type == AccountTypeAPIKey {
		if baseURL := account.GetOpenAIBaseURL(); strings.TrimSpace(baseURL) != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesInputTokensURL(validatedURL)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower != "user-agent" && lower != "accept-language" {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	if customUA := account.GetOpenAIUserAgent(); customUA != "" {
		req.Header.Set("user-agent", customUA)
	}

	// 账号级请求头覆写（仅 openai api_key 账号启用时生效；OAuth 路径 no-op）
	account.ApplyHeaderOverrides(req.Header)

	return req, nil
}

func writeAnthropicCountTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func classifyOpenAIInputTokensLocalFallback(account *Account, statusCode int, body []byte) (string, bool) {
	if account == nil || account.Platform != PlatformOpenAI ||
		(account.Type != AccountTypeAPIKey && account.Type != AccountTypeOAuth) {
		return "", false
	}

	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body)))
	bodyLower := strings.ToLower(string(body))

	if account.Type == AccountTypeOAuth && (statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden) {
		explicitMissingScope := code == "missing_scope" || code == "insufficient_scope" ||
			strings.Contains(bodyLower, "missing scopes") ||
			strings.Contains(bodyLower, "insufficient_scope") ||
			strings.Contains(bodyLower, "api.responses.write")
		if explicitMissingScope {
			return inputTokensFallbackScopeDenied, true
		}
	}

	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound &&
		statusCode != http.StatusMethodNotAllowed && statusCode != http.StatusNotImplemented {
		return "", false
	}
	mentionsEndpoint := strings.Contains(msg, "input_tokens") ||
		strings.Contains(msg, "/v1/responses/input_tokens") ||
		strings.Contains(bodyLower, "/v1/responses/input_tokens")
	unsupported := strings.Contains(msg, "not found") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "unknown endpoint") ||
		strings.Contains(msg, "unknown route") ||
		statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented
	if mentionsEndpoint && unsupported {
		return inputTokensFallbackUnsupported, true
	}
	return "", false
}

func writeOpenAIInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int, reason string) {
	estimated := openAIInputTokensFallbackMinimum
	got, estimateErr := estimateOpenAIInputTokens(prepared.Request)
	if estimateErr == nil && got > 0 {
		estimated = got
	}
	if c != nil {
		c.Set(OpsInputTokensSourceKey, OpsInputTokensSourceEstimate)
	}

	fields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.Int("upstream_status", statusCode),
		zap.String("fallback_reason", reason),
		zap.Int("estimated_input_tokens", estimated),
		zap.String("upstream_model", prepared.UpstreamModel),
	}
	if estimateErr == nil {
		logger.L().Info("openai count_tokens: fallback to local tiktoken estimate", fields...)
	} else {
		fields = append(fields, zap.Error(estimateErr))
		logger.L().Warn("openai count_tokens: local tiktoken fallback failed, using minimum estimate", fields...)
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": estimated})
}

func newOpenAIInputTokensProtocolFailoverError(resp *http.Response, reason string) error {
	headers := make(http.Header)
	if resp != nil && resp.Header != nil {
		headers = resp.Header.Clone()
	}
	return &UpstreamFailoverError{
		StatusCode:                     http.StatusBadGateway,
		ResponseHeaders:                headers,
		SuppressAccountScheduleFailure: true,
		Stage:                          GatewayFailureStageInference,
		Scope:                          GatewayFailureScopeRequest,
		Reason:                         GatewayFailureReason(reason),
	}
}

func parseOpenAIInputTokensResponse(body []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return 0, fmt.Errorf("decode input_tokens response: %w", err)
	}
	if root == nil {
		return 0, fmt.Errorf("input_tokens response must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return 0, fmt.Errorf("input_tokens response has trailing JSON")
		}
		return 0, fmt.Errorf("decode trailing input_tokens response: %w", err)
	}
	raw, ok := root["input_tokens"]
	if !ok {
		return 0, fmt.Errorf("input_tokens field is missing")
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0, fmt.Errorf("input_tokens must be a non-negative JSON integer")
	}
	for _, ch := range trimmed {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("input_tokens must be a non-negative JSON integer")
		}
	}
	value, err := strconv.ParseUint(trimmed, 10, strconv.IntSize)
	if err != nil {
		return 0, fmt.Errorf("input_tokens must be a representable non-negative JSON integer: %w", err)
	}
	return int(value), nil
}

func estimateOpenAIInputTokens(req openAIInputTokensCountRequest) (int, error) {
	codec, err := openAIInputTokensCodecForModel(req.Model)
	if err != nil {
		return 0, err
	}

	total := 0
	addCount := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	if err := addCount(req.Instructions); err != nil {
		return 0, err
	}
	inputTokens, err := estimateOpenAIInputTokensForInput(codec, req.Input)
	if err != nil {
		return 0, err
	}
	total += inputTokens

	for _, tool := range req.Tools {
		raw, err := marshalOpenAIUpstreamJSON(tool)
		if err != nil {
			return 0, err
		}
		if err := addCount(string(raw)); err != nil {
			return 0, err
		}
	}
	if len(req.ToolChoice) > 0 {
		compacted, err := compactOpenAIInputTokensJSON(req.ToolChoice)
		if err != nil {
			return 0, err
		}
		if err := addCount(compacted); err != nil {
			return 0, err
		}
	}

	if total < 0 {
		return 0, nil
	}
	return total, nil
}

func estimateOpenAIInputTokensForInput(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}

	var plainText string
	if err := json.Unmarshal(raw, &plainText); err == nil {
		return codec.Count(plainText)
	}

	var items []apicompat.ResponsesInputItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return estimateOpenAIInputTokensForInputItems(codec, items)
	}

	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return 0, err
	}
	return codec.Count(compacted)
}

func estimateOpenAIInputTokensForInputItems(codec tokenizer.Codec, items []apicompat.ResponsesInputItem) (int, error) {
	total := 0
	countText := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	for _, item := range items {
		total += openAIResponsesInputItemTokenOverhead
		if err := countText(item.Role); err != nil {
			return 0, err
		}
		if item.Type != "" && item.Type != "message" {
			if err := countText(item.Type); err != nil {
				return 0, err
			}
		}
		if err := countText(item.Name); err != nil {
			return 0, err
		}
		if err := countText(item.Arguments); err != nil {
			return 0, err
		}
		if err := countText(item.Output); err != nil {
			return 0, err
		}
		if err := countText(item.CallID); err != nil {
			return 0, err
		}
		if err := countText(item.ID); err != nil {
			return 0, err
		}

		if len(bytes.TrimSpace(item.Content)) == 0 {
			continue
		}

		var contentText string
		if err := json.Unmarshal(item.Content, &contentText); err == nil {
			if err := countText(contentText); err != nil {
				return 0, err
			}
			continue
		}

		var parts []apicompat.ResponsesContentPart
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			for _, part := range parts {
				total += openAIResponsesContentPartOverhead
				switch part.Type {
				case "input_text", "output_text", "text":
					if err := countText(part.Text); err != nil {
						return 0, err
					}
				case "input_image":
					if err := countText(estimateOpenAIInputImageText(part.ImageURL)); err != nil {
						return 0, err
					}
				default:
					if err := countText(part.Type); err != nil {
						return 0, err
					}
				}
			}
			continue
		}

		compacted, err := compactOpenAIInputTokensJSON(item.Content)
		if err != nil {
			return 0, err
		}
		if err := countText(compacted); err != nil {
			return 0, err
		}
	}

	return total, nil
}

func estimateOpenAIInputImageText(imageURL string) string {
	trimmed := strings.TrimSpace(imageURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if comma := strings.Index(trimmed, ","); comma > 0 {
			return trimmed[:comma]
		}
	}
	return trimmed
}

func compactOpenAIInputTokensJSON(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func openAIInputTokensCodecForModel(model string) (tokenizer.Codec, error) {
	switch openAIInputTokensEncodingForModel(model) {
	case tokenizer.Cl100kBase:
		return tokenizer.Get(tokenizer.Cl100kBase)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

func openAIInputTokensEncodingForModel(model string) tokenizer.Encoding {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-3.5"),
		(strings.HasPrefix(normalized, "gpt-4") &&
			!strings.HasPrefix(normalized, "gpt-4o") &&
			!strings.HasPrefix(normalized, "gpt-4.1")),
		strings.HasPrefix(normalized, "text-embedding-"):
		return tokenizer.Cl100kBase
	default:
		return tokenizer.O200kBase
	}
}
