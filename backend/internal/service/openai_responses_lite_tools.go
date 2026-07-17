package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const openAIResponsesLiteReasoningContext = "all_turns"

type openAIResponsesLiteValidationError struct {
	param   string
	message string
}

func (e *openAIResponsesLiteValidationError) Error() string { return e.message }

func openAIResponsesLiteValidationParam(err error) string {
	var validationErr *openAIResponsesLiteValidationError
	if errors.As(err, &validationErr) && validationErr.param != "" {
		return validationErr.param
	}
	return "tools"
}

// IsOpenAIResponsesLiteValidationError identifies client payload errors handled before upstream I/O.
func IsOpenAIResponsesLiteValidationError(err error) bool {
	var validationErr *openAIResponsesLiteValidationError
	return errors.As(err, &validationErr)
}

// normalizeOpenAIResponsesLiteTools 应用 Responses Lite 请求契约：reasoning 必须覆盖所有轮次，
// 私有 namespace 声明则移入 input.additional_tools 容器。其它顶层工具必须属于 Lite 接口支持的
// 有限集合；拒绝不支持的 hosted 工具是有意为之，静默丢弃会改变客户端请求语义。
func normalizeOpenAIResponsesLiteTools(reqBody map[string]any) (bool, error) {
	if reqBody == nil {
		return false, nil
	}
	if rawReasoning, exists := reqBody["reasoning"]; exists && rawReasoning != nil {
		if _, ok := rawReasoning.(map[string]any); !ok {
			return false, &openAIResponsesLiteValidationError{
				param:   "reasoning",
				message: "responses Lite requires reasoning to be an object",
			}
		}
	}
	rawTools, exists := reqBody["tools"]
	if !exists || rawTools == nil {
		return ensureOpenAIResponsesLiteReasoningContext(reqBody)
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false, fmt.Errorf("responses Lite requires tools to be an array")
	}

	topLevelTools := make([]any, 0, len(tools))
	namespaceTools := make([]any, 0, len(tools))
	for index, rawTool := range tools {
		if customTool, ok := rawTool.(string); ok {
			if strings.TrimSpace(customTool) == "" {
				return false, fmt.Errorf("responses Lite custom tool at index %d must not be empty", index)
			}
			topLevelTools = append(topLevelTools, rawTool)
			continue
		}
		tool, ok := rawTool.(map[string]any)
		if !ok {
			return false, fmt.Errorf("responses Lite tool at index %d must be an object", index)
		}
		toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
		switch toolType {
		case "function", "custom", "tool_search":
			topLevelTools = append(topLevelTools, rawTool)
		case "namespace":
			namespaceTools = append(namespaceTools, rawTool)
		case "":
			return false, fmt.Errorf("responses Lite tool at index %d is missing type", index)
		default:
			return false, fmt.Errorf("responses Lite does not support top-level tool type %q at index %d", toolType, index)
		}
	}
	if len(namespaceTools) == 0 {
		return ensureOpenAIResponsesLiteReasoningContext(reqBody)
	}

	input, err := appendOpenAIResponsesLiteAdditionalTools(reqBody["input"], namespaceTools)
	if err != nil {
		return false, err
	}
	if _, err := ensureOpenAIResponsesLiteReasoningContext(reqBody); err != nil {
		return false, err
	}
	reqBody["input"] = input
	if len(topLevelTools) == 0 {
		delete(reqBody, "tools")
	} else {
		reqBody["tools"] = topLevelTools
	}
	return true, nil
}

// ensureOpenAIResponsesLiteReasoningContext 强制 Lite 请求携带全轮次推理上下文，同时保留其它推理参数。
func ensureOpenAIResponsesLiteReasoningContext(reqBody map[string]any) (bool, error) {
	rawReasoning, exists := reqBody["reasoning"]
	if !exists || rawReasoning == nil {
		reqBody["reasoning"] = map[string]any{"context": openAIResponsesLiteReasoningContext}
		return true, nil
	}
	reasoning, ok := rawReasoning.(map[string]any)
	if !ok {
		return false, &openAIResponsesLiteValidationError{
			param:   "reasoning",
			message: "responses Lite requires reasoning to be an object",
		}
	}
	if context, ok := reasoning["context"].(string); ok && context == openAIResponsesLiteReasoningContext {
		return false, nil
	}
	reasoning["context"] = openAIResponsesLiteReasoningContext
	return true, nil
}

func appendOpenAIResponsesLiteAdditionalTools(input any, namespaceTools []any) ([]any, error) {
	var items []any
	switch typed := input.(type) {
	case nil:
		items = make([]any, 0, 1)
	case string:
		items = []any{map[string]any{
			"type":    "message",
			"role":    "user",
			"content": typed,
		}}
	case []any:
		items = typed
	default:
		return nil, fmt.Errorf("responses Lite namespace tools require input to be a string or array")
	}

	var target map[string]any
	var targetTools []any
	var allAdditionalTools []any
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "additional_tools" {
			continue
		}
		rawAdditionalTools, exists := item["tools"]
		additionalTools := []any(nil)
		toolsOK := true
		if exists && rawAdditionalTools != nil {
			additionalTools, toolsOK = rawAdditionalTools.([]any)
		}
		if !toolsOK {
			return nil, fmt.Errorf("responses Lite input.additional_tools tools must be an array")
		}
		if target == nil {
			target = item
			targetTools = additionalTools
		}
		allAdditionalTools = append(allAdditionalTools, additionalTools...)
	}

	merged, err := mergeOpenAIResponsesLiteAdditionalTools(allAdditionalTools, namespaceTools)
	if err != nil {
		return nil, err
	}
	newTools := merged[len(allAdditionalTools):]
	if target != nil {
		if len(newTools) > 0 {
			target["tools"] = append(append([]any(nil), targetTools...), newTools...)
		}
		return items, nil
	}

	items = append(items, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": newTools,
	})
	return items, nil
}

func mergeOpenAIResponsesLiteAdditionalTools(existing []any, moved []any) ([]any, error) {
	merged := append([]any(nil), existing...)
	seen := make(map[string]any, len(existing)+len(moved))
	for _, rawTool := range existing {
		if identity := openAIResponsesLiteToolIdentity(rawTool); identity != "" {
			if previous, exists := seen[identity]; exists && !reflect.DeepEqual(previous, rawTool) {
				return nil, fmt.Errorf("responses Lite additional_tools contains conflicting definitions for %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
	}
	for _, rawTool := range moved {
		identity := openAIResponsesLiteToolIdentity(rawTool)
		if identity != "" {
			if previous, exists := seen[identity]; exists {
				if reflect.DeepEqual(previous, rawTool) {
					continue
				}
				return nil, fmt.Errorf("responses Lite additional_tools conflicts with migrated %s", openAIResponsesLiteToolIdentityForError(rawTool))
			}
			seen[identity] = rawTool
		}
		merged = append(merged, rawTool)
	}
	return merged, nil
}

func openAIResponsesLiteToolIdentity(rawTool any) string {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return ""
	}
	toolType := strings.TrimSpace(firstNonEmptyString(tool["type"]))
	name := strings.TrimSpace(firstNonEmptyString(tool["name"]))
	if toolType == "" || name == "" {
		return ""
	}
	return toolType + "\x00" + name
}

func openAIResponsesLiteToolIdentityForError(rawTool any) string {
	tool, _ := rawTool.(map[string]any)
	return fmt.Sprintf("tool type %q name %q", strings.TrimSpace(firstNonEmptyString(tool["type"])), strings.TrimSpace(firstNonEmptyString(tool["name"])))
}

func normalizeOpenAIResponsesLiteToolsPayload(body []byte) ([]byte, bool, error) {
	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		return body, false, &openAIResponsesLiteValidationError{
			param:   "tools",
			message: fmt.Sprintf("decode responses Lite request body: %v", err),
		}
	}
	if len(bytes.TrimSpace(body[decoder.InputOffset():])) > 0 {
		return body, false, &openAIResponsesLiteValidationError{
			param:   "tools",
			message: "decode responses Lite request body: invalid trailing data",
		}
	}
	changed, err := normalizeOpenAIResponsesLiteTools(requestBody)
	if err != nil {
		if IsOpenAIResponsesLiteValidationError(err) {
			return body, false, err
		}
		return body, false, &openAIResponsesLiteValidationError{param: "tools", message: err.Error()}
	}
	if !changed {
		return body, false, nil
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, false, fmt.Errorf("encode responses Lite request body: %w", err)
	}
	return rebuilt, true, nil
}
