package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestQoderTokenRefresherNeedsRefreshWhenExpiresAtWithinWindow(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	expiresAt := time.Now().Add(time.Minute).Format(time.RFC3339)
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"refresh_token": "refresh-1",
			"expires_at":    expiresAt,
		}),
	}

	require.True(t, refresher.CanRefresh(account))
	require.True(t, refresher.NeedsRefresh(account, time.Hour))
}

func TestQoderTokenRefresherDoesNotRefreshQuotaRateLimitedWithoutExpiresAt(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	resetAt := time.Now().Add(time.Minute)
	account := &Account{
		ID:               1,
		Platform:         PlatformQoder,
		Type:             AccountTypeCosy,
		RateLimitResetAt: &resetAt,
		Credentials:      completeQoderCN20TestCredentials(map[string]any{"refresh_token": "refresh-1"}),
	}

	require.False(t, refresher.NeedsRefresh(account, time.Hour))
}

func TestQoderTokenRefresherDoesNotRefreshWithoutRefreshToken(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	resetAt := time.Now().Add(time.Minute)
	account := &Account{
		ID:               1,
		Platform:         PlatformQoder,
		Type:             AccountTypeCosy,
		RateLimitResetAt: &resetAt,
	}

	require.False(t, refresher.NeedsRefresh(account, time.Hour))
}

func TestQoderTokenRefresherRefreshMergesCredentials(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(_ context.Context, refreshToken string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		require.Equal(t, "old-refresh", refreshToken)
		require.Equal(t, "machine-1", machine.MachineID)
		return &qoder.AuthIdentity{
			Name:               "Refreshed User",
			UID:                "user-1",
			AID:                "user-1",
			OrganizationID:     "org-1",
			OrganizationName:   "Org 1",
			UserType:           "personal_pro",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
		}, time.Time{}, nil
	}
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"data_policy":          "disagree",
			"accessToken":          "stale-access-alias",
			"securityOauthToken":   "stale-security-alias",
			"personal_token":       "stale-pat-alias",
			"quota_key":            "retired-quota-key",
			"custom":               "keep",
		}),
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "new-refresh", credentials["refresh_token"])
	require.Equal(t, "machine-1", credentials["machine_id"])
	require.Equal(t, "org-1", credentials["organization_id"])
	require.Equal(t, "disagree", credentials["data_policy"])
	require.Equal(t, "keep", credentials["custom"])
	require.NotContains(t, credentials, "accessToken")
	require.NotContains(t, credentials, "securityOauthToken")
	require.NotContains(t, credentials, "personal_token")
	require.NotContains(t, credentials, "quota_key")
}

func TestQoderTokenRefresherPreservesIdentityMetadataFromTokenOnlyRefresh(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(context.Context, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return &qoder.AuthIdentity{
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
		}, time.Time{}, nil
	}
	account := &Account{
		ID:       2,
		Name:     "Account Name",
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-2",
			"uid":                  "uid-2",
			"aid":                  "aid-2",
			"user_type":            nil,
			"userType":             "enterprise_standard",
			"organizationId":       "org-2",
			"organizationName":     "Organization 2",
			"organizationTags":     []any{" Enterprise ", "CN", "Enterprise"},
			"data_policy":          nil,
			"dataPolicyAgreed":     true,
		}),
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "uid-2", credentials["uid"])
	require.Equal(t, "aid-2", credentials["aid"])
	require.Equal(t, "enterprise_standard", credentials["user_type"])
	require.Equal(t, "org-2", credentials["organization_id"])
	require.Equal(t, "Organization 2", credentials["organization_name"])
	require.Equal(t, []string{"Enterprise", "CN"}, credentials["organization_tags"])
	require.Equal(t, "agree", credentials["data_policy"])
	require.NotContains(t, credentials, "userType")
	require.NotContains(t, credentials, "organizationId")
	require.NotContains(t, credentials, "organizationName")
	require.NotContains(t, credentials, "organizationTags")
	require.NotContains(t, credentials, "dataPolicyAgreed")
}

func TestQoderTokenRefresherRejectsLegacyCosyRefresh(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "cosy",
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "user-1",
		},
	}

	require.False(t, refresher.CanRefresh(account))
	credentials, err := refresher.Refresh(context.Background(), account)

	require.Nil(t, credentials)
	require.ErrorContains(t, err, "refresh_mode")
}

func TestQoderTokenRefresherQoderCN20RejectsMissingRotatedRefreshToken(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(context.Context, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return &qoder.AuthIdentity{
			UID:                "user-1",
			AID:                "user-1",
			SecurityOauthToken: "new-device-token",
		}, time.Time{}, nil
	}
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-device-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "user-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.Nil(t, credentials)
	require.ErrorContains(t, err, "empty refresh_token")
}

func TestQoderTokenRefresherRefreshDropsStaleExpiresAt(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(context.Context, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return &qoder.AuthIdentity{
			UID:                "user-1",
			AID:                "user-1",
			SecurityOauthToken: "new-token",
			RefreshToken:       "new-refresh",
		}, time.Time{}, nil
	}
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"expires_at":           time.Now().Add(-time.Hour).Format(time.RFC3339),
		}),
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "new-refresh", credentials["refresh_token"])
	require.NotContains(t, credentials, "expires_at")
}

func TestQoderTokenRefresherRefreshRejectsEmptySecurityOauthToken(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(context.Context, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return &qoder.AuthIdentity{
			UID:          "user-1",
			AID:          "user-1",
			RefreshToken: "new-refresh",
		}, time.Time{}, nil
	}
	account := &Account{
		ID:       1,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
		}),
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.Nil(t, credentials)
	require.ErrorContains(t, err, "empty security_oauth_token")
}

func TestQoderTokenRefresherRefreshRequiresMachineID(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	account := &Account{
		ID:          1,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{"machine_id": nil}),
	}

	_, err := refresher.Refresh(context.Background(), account)

	require.ErrorContains(t, err, "machine_id")
}

func TestQoderTokenRefresherRoutesCN20RefreshAndPersistsExpiry(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	refresher.refreshCN20 = func(_ context.Context, refreshToken string, machine *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		require.Equal(t, "cn-refresh", refreshToken)
		require.Equal(t, "machine-1", machine.MachineID)
		require.Equal(t, "machine-1", machine.MachineToken)
		require.Equal(t, "5", machine.MachineType)
		return &qoder.AuthIdentity{
			UID:                "uid-1",
			AID:                "aid-1",
			SecurityOauthToken: "new-cosy-token",
			RefreshToken:       "rotated-cn-refresh",
			UserType:           "personal_standard",
		}, expiresAt, nil
	}
	account := &Account{
		ID:       20,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-cosy-token",
			"refresh_token":        "cn-refresh",
			"machine_id":           "machine-1",
			"machine_token":        "legacy-machine-token",
			"machine_type":         "legacy-machine-type",
			"uid":                  "uid-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "new-cosy-token", credentials["security_oauth_token"])
	require.Equal(t, "rotated-cn-refresh", credentials["refresh_token"])
	require.Equal(t, "cn", credentials["site"])
	require.Equal(t, qoder.RefreshModeQoderCN20, credentials["refresh_mode"])
	require.Equal(t, expiresAt.Format(time.RFC3339), credentials["expires_at"])
	require.Equal(t, "machine-1", credentials["machine_token"])
	require.Equal(t, "5", credentials["machine_type"])
}

func TestQoderTokenRefresherDoesNotRouteCNManualCosyRefresh(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	account := &Account{
		ID:       21,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "cosy",
			"security_oauth_token": "old-token",
			"refresh_token":        "cosy-refresh",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
			"organization_id":      "org-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)
	require.Nil(t, credentials)
	require.ErrorContains(t, err, "refresh_mode")
}

func TestQoderTokenRefresherRefreshWrapsError(t *testing.T) {
	refresher := NewQoderTokenRefresher(nil)
	refresher.refreshCN20 = func(context.Context, string, *qoder.MachineIdentity) (*qoder.AuthIdentity, time.Time, error) {
		return nil, time.Time{}, errors.New("invalid_grant")
	}
	account := &Account{
		ID:          1,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{"refresh_token": "old-refresh", "machine_id": "machine-1", "uid": "uid-1"}),
	}

	_, err := refresher.Refresh(context.Background(), account)

	require.ErrorContains(t, err, "invalid_grant")
}

func TestQoderTokenRefresherUsesAccountDoer(t *testing.T) {
	upstream := &qoderRefreshHTTPUpstreamStub{}
	refresher := NewQoderTokenRefresherWithHTTPUpstream(nil, upstream, nil)
	proxyID := int64(9)
	account := &Account{
		ID:          108,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Concurrency: 4,
		ProxyID:     &proxyID,
		Proxy:       &Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "new-token", credentials["security_oauth_token"])
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(108), upstream.accountID)
	require.Equal(t, 4, upstream.accountConcurrency)
}

func TestQoderTokenRefresherPATRefreshRetainsCanonicalPAT(t *testing.T) {
	refresher := NewQoderTokenRefresherWithHTTPUpstream(nil, &qoderPATRefreshHTTPUpstreamStub{}, nil)
	account := &Account{
		ID:       109,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":           "cn",
			"pat":            "pat-original",
			"machine_id":     "machine-pat",
			"personal_token": "stale-pat-alias",
			"quota_key":      "retired-quota-key",
			"custom":         "keep",
		},
	}

	credentials, err := refresher.Refresh(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, "pat-original", credentials["pat"])
	require.Equal(t, "pat-session-token", credentials["security_oauth_token"])
	require.Equal(t, "pat-user", credentials["uid"])
	require.Equal(t, "disagree", credentials["data_policy"])
	require.Equal(t, "keep", credentials["custom"])
	require.NotContains(t, credentials, "refresh_token")
	require.NotContains(t, credentials, "refresh_mode")
	require.NotContains(t, credentials, "personal_token")
	require.NotContains(t, credentials, "quota_key")
}

func TestNewQoderTokenRefresherForAdminUsesAdminTransport(t *testing.T) {
	upstream := &qoderRefreshHTTPUpstreamStub{}
	tlsProfileService := &TLSFingerprintProfileService{}
	adminSvc := &adminServiceImpl{
		httpUpstream:        upstream,
		tlsFPProfileService: tlsProfileService,
	}

	refresher := NewQoderTokenRefresherForAdmin(adminSvc, nil)

	require.Same(t, upstream, refresher.httpUpstream)
	require.Same(t, tlsProfileService, refresher.tlsFPProfileSvc)
}

type qoderRefreshHTTPUpstreamStub struct {
	proxyURL           string
	accountID          int64
	accountConcurrency int
}

type qoderPATRefreshHTTPUpstreamStub struct{}

func (s *qoderPATRefreshHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *qoderPATRefreshHTTPUpstreamStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	body := ""
	status := http.StatusOK
	switch req.URL.Path {
	case qoder.JobTokenExchangePath:
		body = `{"token":"pat-session-token","refresh_token":"incidental-refresh"}`
	case qoder.UserInfoPath:
		body = `{"uid":"pat-user","name":"PAT User","userType":"personal_pro"}`
	case "/algo" + qoder.DataPolicyPath:
		body = `{"success":true,"result":{"status":"DISAGREE"}}`
	default:
		status = http.StatusNotFound
		body = `{"message":"not found"}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (s *qoderRefreshHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *qoderRefreshHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	s.accountID = accountID
	s.accountConcurrency = accountConcurrency
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
				"device_token":"new-token",
				"refresh_token":"new-refresh",
				"expires_in":3600
			}`)),
		Request: req,
	}, nil
}
