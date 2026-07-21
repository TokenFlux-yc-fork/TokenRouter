package qoder

import "strings"

// Model 表示暴露给管理端和模型选择 API 的 Qoder 模型。
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// cnModels 是国内站当前客户端展示的稳定模型快照。
var cnModels = []Model{
	{ID: "auto", Type: "model", DisplayName: "Qoder Auto", CreatedAt: ""},
	{ID: "qwen3.8-max-preview", Type: "model", DisplayName: "Qwen3.8-Max-Preview", CreatedAt: ""},
	{ID: "qwen3.7-max", Type: "model", DisplayName: "Qwen3.7-Max", CreatedAt: ""},
	{ID: "qwen3.7-plus", Type: "model", DisplayName: "Qwen3.7-Plus", CreatedAt: ""},
	{ID: "qwen3.6-flash", Type: "model", DisplayName: "Qwen3.6-Flash", CreatedAt: ""},
	{ID: "deepseek-v4-pro", Type: "model", DisplayName: "DeepSeek-V4-Pro", CreatedAt: ""},
	{ID: "deepseek-v4-flash", Type: "model", DisplayName: "DeepSeek-V4-Flash", CreatedAt: ""},
	{ID: "glm-5.2", Type: "model", DisplayName: "GLM-5.2", CreatedAt: ""},
	{ID: "kimi-k2.7-code", Type: "model", DisplayName: "Kimi-K2.7-Code", CreatedAt: ""},
	{ID: "minimax-m2.7", Type: "model", DisplayName: "MiniMax-M2.7", CreatedAt: ""},
}

// legacyGlobalAliases 仅用于识别并拒绝不属于国内站的旧公开 alias/route key。
var legacyGlobalAliases = map[string]string{
	"claude-opus-4-6":     "ultimate",
	"auto":                "auto",
	"performance":         "performance",
	"efficient":           "efficient",
	"lite":                "lite",
	"qwen3.8-max-preview": "qmodel_preview",
	"qwen3.7-max":         "qmodel_latest",
	"qwen3.7-plus":        "qmodel",
	"kimi-k3":             "kmodel_latest",
	"kimi-k2.7-code":      "kmodel",
	"glm-5.2":             "gm51model",
	"deepseek-v4-pro":     "dmodel",
	"deepseek-v4-flash":   "dfmodel",
	"minimax-m3":          "mmodel",
}

var cnAliases = map[string]string{
	"auto":                "auto",
	"qwen3.8-max-preview": "qmodel_preview",
	"qwen3.7-max":         "qmodel_latest",
	"qwen3.7-plus":        "qmodel",
	"qwen3.6-flash":       "q36fmodel",
	"deepseek-v4-pro":     "dmodel",
	"deepseek-v4-flash":   "dfmodel",
	"glm-5.2":             "gm51model",
	"kimi-k2.7-code":      "kmodel",
	"minimax-m2.7":        "mmodel",
}

// DefaultModels 是 qoderclicn 当前稳定模型快照。
var DefaultModels = append([]Model(nil), cnModels...)

// DefaultModelsForSite 返回指定站点的模型快照副本。
func DefaultModelsForSite(site Site) []Model {
	if site != SiteCN {
		return nil
	}
	return append([]Model(nil), cnModels...)
}

// AliasesForSite 返回指定站点公开 alias 到内部 route key 的副本。
func AliasesForSite(site Site) map[string]string {
	if site != SiteCN {
		return nil
	}
	out := make(map[string]string, len(cnAliases))
	for alias, route := range cnAliases {
		out[alias] = route
	}
	return out
}

// AliasForSite 解析指定站点的公开 alias。
func AliasForSite(site Site, model string) (string, bool) {
	if site != SiteCN {
		return "", false
	}
	route, ok := cnAliases[model]
	return route, ok
}

// ModelCompatibleWithSite 判断已知 alias 或 route key 是否能由指定站点处理。
// 未知 raw key 保持透传，因此返回 true。
func ModelCompatibleWithSite(site Site, model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if site != SiteCN {
		return false
	}
	if _, ok := AliasForSite(site, model); ok {
		return true
	}
	if isKnownPublicAlias(model) {
		return false
	}
	if !isKnownRouteKey(model) {
		return true
	}
	for _, route := range AliasesForSite(site) {
		if route == model {
			return true
		}
	}
	return false
}

func isKnownPublicAlias(model string) bool {
	_, global := legacyGlobalAliases[model]
	_, cn := cnAliases[model]
	return global || cn
}

func isKnownRouteKey(model string) bool {
	for _, aliases := range []map[string]string{legacyGlobalAliases, cnAliases} {
		for _, route := range aliases {
			if route == model {
				return true
			}
		}
	}
	return false
}

// DefaultRequestModelIDsForSite 返回指定站点的公开模型 ID。
func DefaultRequestModelIDsForSite(site Site) []string {
	models := DefaultModelsForSite(site)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// DefaultRequestModelIDs 返回 Qoder CN 默认公开模型。
func DefaultRequestModelIDs() []string {
	ids := make([]string, 0, len(DefaultModels))
	for _, model := range DefaultModels {
		ids = append(ids, model.ID)
	}
	return ids
}

// AuthInfo 保存从本地 Qoder 认证存储解密出的用户信息。
type AuthInfo struct {
	UID                    string `json:"uid"`
	Name                   string `json:"name"`
	AccessToken            string `json:"access_token"`
	SecurityOauthToken     string `json:"security_oauth_token"`
	RefreshToken           string `json:"refresh_token"`
	ExpireTime             int64  `json:"expire_time"`
	RefreshTokenExpireTime int64  `json:"refresh_token_expire_time"`
	LoginMethod            string `json:"login_method"`
	LoginTimestamp         int64  `json:"login_timestamp"`
	EncryptUserInfo        string `json:"encrypt_user_info"`
	Key                    string `json:"key"`
	Email                  string `json:"email"`
	UserType               string `json:"userType"`
	MachineID              string `json:"_machine_id"`
	OrganizationID         string `json:"organization_id"`
	OrganizationName       string `json:"organization_name"`
}

// ToAuthIdentity 将本地认证信息转换为用于构建 session 的 AuthIdentity。
func (info *AuthInfo) ToAuthIdentity() *AuthIdentity {
	token := info.SecurityOauthToken
	if token == "" {
		token = info.AccessToken
	}
	userType := info.UserType
	if userType == "" {
		userType = "personal_standard"
	}
	return &AuthIdentity{
		Name:               info.Name,
		AID:                info.UID,
		UID:                info.UID,
		OrganizationID:     info.OrganizationID,
		OrganizationName:   info.OrganizationName,
		UserType:           userType,
		SecurityOauthToken: token,
		RefreshToken:       info.RefreshToken,
	}
}
