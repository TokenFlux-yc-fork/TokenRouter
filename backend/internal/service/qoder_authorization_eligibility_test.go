package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

func completeQoderCN20TestCredentials(overrides map[string]any) map[string]any {
	credentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "security-token",
		"refresh_token":        "refresh-token",
		"machine_id":           "machine-id",
		"uid":                  "user-id",
	}
	for key, value := range overrides {
		if value == nil {
			delete(credentials, key)
			continue
		}
		credentials[key] = value
	}
	return credentials
}

func TestValidateQoderCNAuthorizationCredentials(t *testing.T) {
	for _, refreshMode := range []any{nil, "", "cosy", "unknown"} {
		credentials := map[string]any{"site": "cn", "pat": "pat-token"}
		if refreshMode != nil {
			credentials["refresh_mode"] = refreshMode
		}
		require.NoError(t, validateQoderCNAuthorizationCredentials(credentials))
	}

	for _, test := range []struct {
		name        string
		overrides   map[string]any
		errorSubstr string
	}{
		{name: "complete uid", overrides: nil},
		{name: "complete aid", overrides: map[string]any{"uid": nil, "aid": "account-id"}},
		{name: "missing site", overrides: map[string]any{"site": nil}, errorSubstr: "Qoder CN"},
		{name: "global site", overrides: map[string]any{"site": "global"}, errorSubstr: "Qoder CN"},
		{name: "empty mode", overrides: map[string]any{"refresh_mode": nil}, errorSubstr: "refresh_mode"},
		{name: "legacy mode", overrides: map[string]any{"refresh_mode": "cosy"}, errorSubstr: "refresh_mode"},
		{name: "missing security token", overrides: map[string]any{"security_oauth_token": nil}, errorSubstr: "security_oauth_token"},
		{name: "missing refresh token", overrides: map[string]any{"refresh_token": nil}, errorSubstr: "refresh_token"},
		{name: "missing machine", overrides: map[string]any{"machine_id": nil}, errorSubstr: "machine_id"},
		{name: "missing identity", overrides: map[string]any{"uid": nil}, errorSubstr: "uid or aid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateQoderCNAuthorizationCredentials(completeQoderCN20TestCredentials(test.overrides))
			if test.errorSubstr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.errorSubstr)
		})
	}
}

func TestQoderIsSchedulableRequiresCompleteCNAuthorization(t *testing.T) {
	account := &Account{
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
	}

	account.Credentials = map[string]any{
		"site":         "cn",
		"pat":          "pat-token",
		"refresh_mode": "cosy",
	}
	require.True(t, account.IsSchedulable(), "PAT eligibility must ignore refresh_mode")

	account.Credentials = completeQoderCN20TestCredentials(nil)
	require.True(t, account.IsSchedulable())

	for _, overrides := range []map[string]any{
		{"site": "global"},
		{"refresh_mode": "cosy"},
		{"security_oauth_token": nil},
		{"refresh_token": nil},
		{"machine_id": nil},
		{"uid": nil},
	} {
		account.Credentials = completeQoderCN20TestCredentials(overrides)
		require.False(t, account.IsSchedulable())
	}
}
