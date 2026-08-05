package xai

import "strings"

// DefaultResponsesModel 是 Grok Responses 请求未指定模型时使用的默认模型。
const DefaultResponsesModel = "grok-4.5"

// modelIDAliases 只描述平台内置的上游 ID 标准化规则，不参与账号映射或模型发现。
var modelIDAliases = map[string]string{
	"grok":                    DefaultResponsesModel,
	"grok-latest":             DefaultResponsesModel,
	"grok-4.5-latest":         DefaultResponsesModel,
	"grok-build":              "grok-build-0.1",
	"grok-build-latest":       DefaultResponsesModel,
	"grok-composer":           "grok-composer-2.5-fast",
	"composer-2.5":            "grok-composer-2.5-fast",
	"grok-4.20-reasoning":     "grok-4.20-0309-reasoning",
	"grok-4.20-non-reasoning": "grok-4.20-0309-non-reasoning",
}

// Model 描述 xAI OpenAI 兼容 /models 响应里的模型。
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created,omitempty"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name,omitempty"`
}

var defaultModels = []Model{
	{ID: "grok-4.5", Object: "model", OwnedBy: "xai", DisplayName: "Grok 4.5"},
	{ID: "grok-4.3", Object: "model", OwnedBy: "xai", DisplayName: "Grok 4.3"},
	{ID: "grok-build-0.1", Object: "model", OwnedBy: "xai", DisplayName: "Grok Build 0.1"},
	{ID: "grok-composer-2.5-fast", Object: "model", OwnedBy: "xai", DisplayName: "Grok Composer 2.5 Fast"},
	{ID: "grok-4.20-0309-reasoning", Object: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Reasoning"},
	{ID: "grok-4.20-0309-non-reasoning", Object: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Non Reasoning"},
	{ID: "grok-4.20-multi-agent-0309", Object: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Multi Agent"},
	{ID: "grok-imagine", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine"},
	{ID: "grok-imagine-image", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Image"},
	{ID: "grok-imagine-image-quality", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Image Quality"},
	{ID: "grok-imagine-edit", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Edit"},
	{ID: "grok-imagine-video", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Video"},
	{ID: "grok-imagine-video-1.5", Object: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Video 1.5"},
}

func DefaultModels() []Model {
	out := make([]Model, len(defaultModels))
	copy(out, defaultModels)
	return out
}

func DefaultModelIDs() []string {
	models := DefaultModels()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// NormalizeModelID 在账号映射和白名单校验后，将 Grok 精确别名转换为上游模型 ID。
// 未知模型保持透传，避免限制自定义 xAI 兼容上游。
func NormalizeModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return DefaultResponsesModel
	}
	if mapped, ok := modelIDAliases[model]; ok {
		return mapped
	}
	return model
}
