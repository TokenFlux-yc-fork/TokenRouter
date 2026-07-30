package service

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 单次转发最多执行六次字段降级，避免异常上游持续诱导请求变形。
const maxOpenAIResponsesRejectedFieldRetries = 6

var (
	openAIResponsesRejectedNamespaceParamPattern = regexp.MustCompile(`(?i)^input\[(\d+)\]\.namespace$`)
	openAIResponsesRejectedStatusParamPattern    = regexp.MustCompile(`(?i)^input\[(\d+)\]\.status$`)
	openAIResponsesRejectedMessageParamPattern   = regexp.MustCompile(`(?i)(?:unknown|unsupported)[ _-]+parameter\s*(?::|=|is)?\s*["']?([a-z_][a-z0-9_-]*(?:\[(?:\d+)\])?(?:\.[a-z_][a-z0-9_-]*(?:\[(?:\d+)\])?)*)(?:["']|\b)`)
)

type openAIResponsesRejectedFieldRetryState struct {
	attempts       int
	seenBodyHashes map[[sha256.Size]byte]struct{}
}

func newOpenAIResponsesRejectedFieldRetryState(initialBody []byte) *openAIResponsesRejectedFieldRetryState {
	state := &openAIResponsesRejectedFieldRetryState{
		seenBodyHashes: make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1),
	}
	state.remember(initialBody)
	return state
}

func (s *openAIResponsesRejectedFieldRetryState) Allow(nextBody []byte) bool {
	if s == nil || len(nextBody) == 0 || s.attempts >= maxOpenAIResponsesRejectedFieldRetries {
		return false
	}
	bodyHash := sha256.Sum256(nextBody)
	if _, seen := s.seenBodyHashes[bodyHash]; seen {
		return false
	}
	s.seenBodyHashes[bodyHash] = struct{}{}
	s.attempts++
	return true
}

func (s *openAIResponsesRejectedFieldRetryState) remember(body []byte) {
	if s == nil || len(body) == 0 {
		return
	}
	if s.seenBodyHashes == nil {
		s.seenBodyHashes = make(map[[sha256.Size]byte]struct{}, maxOpenAIResponsesRejectedFieldRetries+1)
	}
	s.seenBodyHashes[sha256.Sum256(body)] = struct{}{}
}

// normalizeOpenAIResponsesRejectedFieldRetryBody 只处理上游明确指出的已知可选字段，
// 并且每次仅删除被拒绝的精确路径，避免根据模糊错误文案扩大请求变更范围。
func normalizeOpenAIResponsesRejectedFieldRetryBody(statusCode int, body, responseBody []byte) ([]byte, string, bool, error) {
	if statusCode != http.StatusBadRequest || len(body) == 0 || len(responseBody) == 0 {
		return nil, "", false, nil
	}

	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody)))
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
	if !isExplicitOpenAIResponsesFieldRejection(code, message) {
		return nil, "", false, nil
	}

	param := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.param").String()))
	if param == "" {
		param = openAIResponsesRejectedParamFromMessage(message)
		if param == "" && isOpenAIResponsesReasoningModeModelRejection(message) {
			param = "reasoning.mode"
		}
	}
	if index, ok := openAIResponsesRejectedNamespaceIndex(param); ok {
		return removeOpenAIResponsesRejectedNamespaceAtIndex(body, index)
	}
	if index, ok := openAIResponsesRejectedStatusIndex(param); ok {
		return removeOpenAIResponsesRejectedStatusesAtIndex(body, index)
	}
	if param == "max_output_tokens" && gjson.GetBytes(body, "max_output_tokens").Exists() {
		retryBody, err := sjson.DeleteBytes(body, "max_output_tokens")
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected max_output_tokens: %w", err)
		}
		return retryBody, "max_output_tokens parameter rejection", true, nil
	}
	if param == "reasoning.mode" && gjson.GetBytes(body, "reasoning.mode").Exists() {
		retryBody, err := sjson.DeleteBytes(body, "reasoning.mode")
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected reasoning.mode: %w", err)
		}
		return retryBody, "reasoning.mode parameter rejection", true, nil
	}
	if isSafeOpenAIResponsesUnknownLeaf(param) {
		retryBody, changed, err := stripOpenAIPassthroughRequestFields(body, []string{param})
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected parameter %s: %w", param, err)
		}
		if changed {
			return retryBody, "explicit unknown leaf parameter rejection", true, nil
		}
	}
	return nil, "", false, nil
}

func isExplicitOpenAIResponsesFieldRejection(code, message string) bool {
	switch strings.TrimSpace(code) {
	case "unknown_parameter", "unsupported_parameter":
		return true
	}
	return strings.Contains(message, "unknown parameter") ||
		strings.Contains(message, "unsupported parameter") ||
		isOpenAIResponsesReasoningModeModelRejection(message)
}

func isOpenAIResponsesReasoningModeModelRejection(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "reasoning.mode") &&
		strings.Contains(message, "is not supported with this model")
}

func openAIResponsesRejectedParamFromMessage(message string) string {
	match := openAIResponsesRejectedMessageParamPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(match[1]))
}

func openAIResponsesRejectedNamespaceIndex(param string) (int, bool) {
	match := openAIResponsesRejectedNamespaceParamPattern.FindStringSubmatch(strings.TrimSpace(param))
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err == nil && index >= 0 {
		return index, true
	}
	return 0, false
}

func openAIResponsesRejectedStatusIndex(param string) (int, bool) {
	match := openAIResponsesRejectedStatusParamPattern.FindStringSubmatch(strings.TrimSpace(param))
	if len(match) != 2 {
		return 0, false
	}
	index, err := strconv.Atoi(match[1])
	if err == nil && index >= 0 {
		return index, true
	}
	return 0, false
}

func isSafeOpenAIResponsesUnknownLeaf(param string) bool {
	param = strings.TrimSpace(param)
	if param == "" {
		return false
	}
	segments, err := parseOpenAIPassthroughStripPath(param)
	if err != nil || len(segments) == 0 || segments[len(segments)-1].arraySelected {
		return false
	}
	root := segments[0].name
	if len(segments) == 1 {
		switch root {
		case "model", "input", "stream", "instructions", "tools", "tool_choice", "reasoning":
			return false
		}
	}
	leaf := segments[len(segments)-1].name
	// Known compatibility fields are accepted only in their exact supported shape.
	switch leaf {
	case "max_output_tokens", "max_tokens", "namespace", "status":
		return false
	}
	if root == "reasoning" && leaf == "effort" {
		return false
	}
	if root == "input" {
		switch leaf {
		case "type", "role", "content", "name", "arguments", "call_id", "output", "id":
			return false
		}
	}
	_, err = normalizeOpenAIPassthroughStripFields([]string{param})
	return err == nil
}

func removeOpenAIResponsesRejectedNamespaceAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	itemPath := fmt.Sprintf("input.%d", index)
	itemType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, itemPath+".type").String()))
	// namespace 只允许从已知工具调用项删除，普通消息中的同名业务字段必须保留。
	switch itemType {
	case "function_call", "tool_call", "custom_tool_call", "mcp_tool_call":
	default:
		return nil, "", false, nil
	}

	namespacePath := itemPath + ".namespace"
	if !gjson.GetBytes(body, namespacePath).Exists() {
		return nil, "", false, nil
	}
	retryBody, err := sjson.DeleteBytes(body, namespacePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("delete rejected namespace at input[%d]: %w", index, err)
	}
	return retryBody, "indexed namespace parameter rejection", true, nil
}

func removeOpenAIResponsesRejectedStatusesAtIndex(body []byte, index int) ([]byte, string, bool, error) {
	statusPath := fmt.Sprintf("input.%d.status", index)
	if !gjson.GetBytes(body, statusPath).Exists() {
		return nil, "", false, nil
	}

	// 同一 type 的 input item 通常共享请求 schema；message 还需按 role 区分输入与
	// assistant 输出形态。上游已通过精确 param 明确拒绝其中一个 status 后，一次
	// 清理同形态 item，避免长历史逐项触发超过重试上限；其他形态保持原样。
	rejectedType := strings.TrimSpace(gjson.GetBytes(body, fmt.Sprintf("input.%d.type", index)).String())
	rejectedRole := ""
	if rejectedType == "message" {
		rejectedRole = strings.TrimSpace(gjson.GetBytes(body, fmt.Sprintf("input.%d.role", index)).String())
		if rejectedRole == "" {
			rejectedType = ""
		}
	}
	retryBody := append([]byte(nil), body...)
	removed := 0
	if rejectedType != "" {
		for itemIndex, item := range gjson.GetBytes(body, "input").Array() {
			if strings.TrimSpace(item.Get("type").String()) != rejectedType || !item.Get("status").Exists() {
				continue
			}
			if rejectedType == "message" && strings.TrimSpace(item.Get("role").String()) != rejectedRole {
				continue
			}
			var err error
			retryBody, err = sjson.DeleteBytes(retryBody, fmt.Sprintf("input.%d.status", itemIndex))
			if err != nil {
				return nil, "", false, fmt.Errorf("delete rejected status at input[%d]: %w", itemIndex, err)
			}
			removed++
		}
	}
	if removed == 0 {
		var err error
		retryBody, err = sjson.DeleteBytes(retryBody, statusPath)
		if err != nil {
			return nil, "", false, fmt.Errorf("delete rejected status at input[%d]: %w", index, err)
		}
	}
	reason := "indexed status parameter rejection"
	if rejectedType != "" {
		reason = fmt.Sprintf("indexed status parameter rejection for input type %s", rejectedType)
	}
	return retryBody, reason, true, nil
}
