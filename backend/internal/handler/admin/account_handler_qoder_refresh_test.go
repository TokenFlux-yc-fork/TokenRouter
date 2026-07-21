package admin

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

type qoderAdminTokenRefresherStub struct {
	credentials map[string]any
	err         error
}

func (s *qoderAdminTokenRefresherStub) Refresh(context.Context, *service.Account) (map[string]any, error) {
	return s.credentials, s.err
}

func TestAccountHandlerManualQoderRefreshUsesConditionalIdentityUpdate(t *testing.T) {
	previousFactory := newQoderTokenRefresherForAdmin
	t.Cleanup(func() { newQoderTokenRefresherForAdmin = previousFactory })

	expected := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       int64(7),
	}
	rotated := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "rotated-access",
		"refresh_token":        "rotated-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
	}
	newQoderTokenRefresherForAdmin = func(service.AdminService, *service.QoderOAuthService) qoderAdminTokenRefresher {
		return &qoderAdminTokenRefresherStub{credentials: rotated}
	}

	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{{
		ID:          71,
		Platform:    service.PlatformQoder,
		Type:        service.AccountTypeCosy,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: expected,
	}}
	invalidator := &applyOAuthTokenInvalidator{}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, invalidator)

	updated, warning, err := handler.refreshSingleAccount(context.Background(), &adminSvc.accounts[0])

	require.NoError(t, err)
	require.Empty(t, warning)
	require.NotNil(t, updated)
	require.NotNil(t, adminSvc.updateAccountInput)
	require.Equal(t, expected, adminSvc.updateAccountInput.QoderRefreshExpectedCredentials)
	require.Equal(t, rotated, adminSvc.updateAccountInput.Credentials)
	require.Len(t, invalidator.accounts, 1)
}
