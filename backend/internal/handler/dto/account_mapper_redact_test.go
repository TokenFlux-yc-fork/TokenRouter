package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

func TestAccountFromServiceShallow_RedactsSensitiveCredentials(t *testing.T) {
	src := &service.Account{
		ID:       42,
		Name:     "demo",
		Platform: "anthropic",
		Type:     "oauth",
		Credentials: map[string]any{
			"access_token":          "at-secret",
			"refresh_token":         "rt-secret",
			"id_token":              "id-secret",
			"api_key":               "sk-secret",
			"device_token":          "device-secret",
			"personal_access_token": "personal-secret",
			"personal_token":        "legacy-personal-secret",
			"securityOauthToken":    "camel-security-secret",
			"refreshToken":          "camel-refresh-secret",
			"deviceToken":           "camel-device-secret",
			"codeVerifier":          "camel-verifier-secret",
			"nonce":                 "nonce-secret",
			"verifier":              "verifier-secret",
			"base_url":              "https://api.example.com",
			"model_mapping":         map[string]any{"foo": "bar"},
		},
	}

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)

	// 敏感键不在 Credentials 里
	require.NotContains(t, got.Credentials, "access_token")
	require.NotContains(t, got.Credentials, "refresh_token")
	require.NotContains(t, got.Credentials, "id_token")
	require.NotContains(t, got.Credentials, "api_key")
	require.NotContains(t, got.Credentials, "device_token")
	require.NotContains(t, got.Credentials, "personal_access_token")
	require.NotContains(t, got.Credentials, "personal_token")
	require.NotContains(t, got.Credentials, "securityOauthToken")
	require.NotContains(t, got.Credentials, "refreshToken")
	require.NotContains(t, got.Credentials, "deviceToken")
	require.NotContains(t, got.Credentials, "codeVerifier")
	require.NotContains(t, got.Credentials, "nonce")
	require.NotContains(t, got.Credentials, "verifier")
	// 非敏感键保留
	require.Equal(t, "https://api.example.com", got.Credentials["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, got.Credentials["model_mapping"])

	// 状态 map 标记敏感键存在
	require.True(t, got.CredentialsStatus["has_access_token"])
	require.True(t, got.CredentialsStatus["has_refresh_token"])
	require.True(t, got.CredentialsStatus["has_id_token"])
	require.True(t, got.CredentialsStatus["has_api_key"])
	require.True(t, got.CredentialsStatus["has_device_token"])
	require.True(t, got.CredentialsStatus["has_personal_access_token"])

	// JSON 序列化校验：响应体里不会出现敏感子串
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "rt-secret")
	require.NotContains(t, string(raw), "at-secret")
	require.NotContains(t, string(raw), "sk-secret")
	require.NotContains(t, string(raw), "id-secret")
	require.NotContains(t, string(raw), "device-secret")
	require.NotContains(t, string(raw), "personal-secret")
	require.NotContains(t, string(raw), "legacy-personal-secret")
	require.NotContains(t, string(raw), "camel-security-secret")
	require.NotContains(t, string(raw), "camel-refresh-secret")
	require.NotContains(t, string(raw), "camel-device-secret")
	require.NotContains(t, string(raw), "camel-verifier-secret")
	require.NotContains(t, string(raw), "nonce-secret")
	require.NotContains(t, string(raw), "verifier-secret")
	// 状态标识应序列化进 JSON
	require.Contains(t, string(raw), "credentials_status")
	require.Contains(t, string(raw), "has_refresh_token")

	// 原始 service.Account 不应被改动
	require.Equal(t, "rt-secret", src.Credentials["refresh_token"])
}

func TestAccountFromServiceShallow_NilCredentialsOmitsStatus(t *testing.T) {
	src := &service.Account{ID: 1, Name: "n", Platform: "anthropic", Type: "oauth"}
	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)
	require.Nil(t, got.Credentials)
	require.Nil(t, got.CredentialsStatus)
}

func TestAccountFromServiceShallow_OpenAIOAuthTLSFingerprint(t *testing.T) {
	src := &service.Account{
		ID:       3,
		Name:     "openai-oauth",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"enable_tls_fingerprint":     true,
			"tls_fingerprint_profile_id": -1,
		},
	}

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)
	require.NotNil(t, got.EnableTLSFingerprint)
	require.True(t, *got.EnableTLSFingerprint)
	require.NotNil(t, got.TLSFingerprintProfileID)
	require.Equal(t, int64(-1), *got.TLSFingerprintProfileID)
}
