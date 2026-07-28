package service

import (
	"encoding/json"
	"net/http"
	"strings"
)

func isGrokResponsesTransientNotFound(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusNotFound || len(responseBody) == 0 {
		return false
	}

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return false
	}

	return strings.EqualFold(strings.TrimSpace(payload.Error.Message), "Not Found") &&
		strings.TrimSpace(payload.Error.Type) == "bad_response_status_code" &&
		strings.TrimSpace(payload.Error.Code) == "bad_response_status_code"
}

// isGrokContentPolicyRejection 识别 xAI 针对单次请求的内容安全拒绝。
// 这类失败由提示词或媒体内容引起，切换 OAuth 账号无法改变结果，反而会错误消耗账号池。
// 匹配条件必须保持严格：账号权益或封禁消息也可能提到策略，但仍应走正常的账号故障转移路径。
func isGrokContentPolicyRejection(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusForbidden || len(responseBody) == 0 {
		return false
	}
	if grokAccountAccessMessage(string(responseBody)) {
		return false
	}

	var payload any
	if json.Unmarshal(responseBody, &payload) == nil {
		if grokStructuredAccountAccessMarker(payload) {
			return false
		}
		if grokStructuredContentPolicyMarker(payload) {
			return true
		}
	}

	return grokContentPolicyMessage(string(responseBody))
}

func grokStructuredAccountAccessMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokAccountAccessCode(marker) {
					return true
				}
			}
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	}
	return false
}

func grokStructuredContentPolicyMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			switch normalizedKey {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokContentPolicyCode(marker) {
					return true
				}
			}
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	}
	return false
}

func normalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func isGrokContentPolicyCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "content_filter",
		"content_policy",
		"content_policy_violation",
		"content_moderation",
		"cyber_policy",
		"new_sensitive":
		return true
	default:
		return false
	}
}

func isGrokAccountAccessCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "account_suspended",
		"account_disabled",
		"user_suspended",
		"user_disabled",
		"subscription_required",
		"entitlement_required",
		"not_entitled",
		"plan_required",
		"permission_denied":
		return true
	default:
		return false
	}
}

func grokAccountAccessMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, phrase := range []string{
		"account suspended",
		"account has been suspended",
		"account disabled",
		"account has been disabled",
		"user suspended",
		"user has been suspended",
		"subscription required",
		"entitlement required",
		"not entitled",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func grokContentPolicyMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}

	// xAI 媒体安全响应会使用这些明确短语，不会与普通账号策略或权益消息混淆。
	for _, phrase := range []string{
		"the moderation feature is not available",
		"image is sensitive",
		"text is sensitive",
		"prohibited content",
		"forbidden content",
		"content policy violation",
		"content policy rejection",
		"content policy rejected",
		"content moderation rejection",
		"content moderation rejected",
		"content moderation blocked",
		"request blocked by content moderation",
		"request rejected by content moderation",
		"request blocked by policy",
		"request rejected by policy",
		"request violates policy",
		"prompt violates content policy",
		"prompt violates policy",
		"input violates content policy",
		"input violates policy",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	return false
}

func grokContentPolicyClientMessage(responseBody []byte) string {
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
	if message == "" {
		return "Request blocked by upstream content policy"
	}
	return message
}

// shouldFailoverGrokUpstreamError 在状态码之外结合响应体判断是否故障转移。
// Grok 内容拒绝必须留在当前账号并返回调用方，不能继续消耗账号池。
func (s *OpenAIGatewayService) shouldFailoverGrokUpstreamError(statusCode int, responseBody []byte) bool {
	if isGrokContentPolicyRejection(statusCode, responseBody) {
		return false
	}
	return s.shouldFailoverUpstreamError(statusCode)
}
