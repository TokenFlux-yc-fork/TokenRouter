package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateQoderCosyCredentialsAcceptsDirectToken(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
	require.Empty(t, account.GetCredential("machine_token"))
	require.Empty(t, account.GetCredential("machine_type"))
}

func TestCreateQoderDirectTokenAccountPersistsStableMachineIdentity(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "qoder-direct",
		Platform:             PlatformQoder,
		Type:                 AccountTypeCosy,
		SkipDefaultGroupBind: true,
		Credentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	})

	require.NoError(t, err)
	require.Equal(t, "machine-1", created.GetCredential("machine_id"))
	require.NotEmpty(t, created.GetCredential("machine_token"))
	require.NotEmpty(t, created.GetCredential("machine_type"))
}

func TestCreateQoderDirectTokenAccountRejectsMissingMachineID(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	credentials := map[string]any{
		"site":                 "cn",
		"security_oauth_token": "dt-token",
		"uid":                  "uid-1",
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "qoder-direct",
		Platform:             PlatformQoder,
		Type:                 AccountTypeCosy,
		SkipDefaultGroupBind: true,
		Credentials:          credentials,
	})

	require.ErrorContains(t, err, "machine_id")
	require.Empty(t, credentials["machine_id"])
	require.Empty(t, credentials["machine_token"])
	require.Empty(t, credentials["machine_type"])
}

func TestCreateQoderCNPATAccountUsesOfficialMachineIdentity(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, _ string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		parsedMachineID, err := uuid.Parse(machine.MachineID)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(4), parsedMachineID.Version())
		require.Equal(t, machine.MachineID, machine.MachineToken)
		require.Equal(t, "5", machine.MachineType)
		return &qoder.AuthIdentity{UID: "uid-cn", SecurityOauthToken: "dt-cn"}, nil
	}
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "qoder-cn-pat",
		Platform:             PlatformQoder,
		Type:                 AccountTypeCosy,
		SkipDefaultGroupBind: true,
		Credentials: map[string]any{
			"site": "cn",
			"pat":  "pat-cn",
		},
	})

	require.NoError(t, err)
	require.Len(t, created.GetCredential("machine_id"), 36)
	require.Equal(t, created.GetCredential("machine_id"), created.GetCredential("machine_token"))
	require.Equal(t, "5", created.GetCredential("machine_type"))
}

func TestEnsureQoderCNMachineCredentialsNormalizesLegacyFields(t *testing.T) {
	account := &Account{Credentials: map[string]any{
		"site":                 "cn",
		"security_oauth_token": "cosy-token",
		"machine_id":           "machine-cn",
		"machine_token":        "legacy-machine-token",
		"machine_type":         "legacy-machine-type",
	}}

	ensureQoderMachineCredentials(account)

	require.Equal(t, "machine-cn", account.GetCredential("machine_id"))
	require.Equal(t, "machine-cn", account.GetCredential("machine_token"))
	require.Equal(t, "5", account.GetCredential("machine_type"))
}

func TestUpdateQoderDirectTokenAccountPreservesLegacyMachineFallback(t *testing.T) {
	const accountID int64 = 1202
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:          accountID,
			Name:        "legacy-qoder",
			Platform:    PlatformQoder,
			Type:        AccountTypeCosy,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"security_oauth_token": "dt-token",
				"machine_id":           "legacy-machine",
				"uid":                  "uid-1",
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	updatedName := "renamed-qoder"

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{Name: updatedName})

	require.NoError(t, err)
	require.Equal(t, updatedName, updated.Name)
	require.Equal(t, "legacy-machine", updated.GetCredential("machine_id"))
	require.Empty(t, updated.GetCredential("machine_token"))
	require.Empty(t, updated.GetCredential("machine_type"))
	require.False(t, updated.IsSchedulable())
}

func TestValidateQoderCosyCredentialsAcceptsDirectTokenWithAID(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"aid":                  "aid-1",
		},
	}

	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
}

func TestValidateQoderCosyCredentialsRejectsDirectTokenWithoutIdentity(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
		},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "uid or aid")
}

func TestValidateQoderCosyCredentialsRejectsUnknownSiteAndRefreshMode(t *testing.T) {
	baseCredentials := map[string]any{
		"security_oauth_token": "dt-token",
		"machine_id":           "machine-1",
		"uid":                  "uid-1",
	}
	account := &Account{Platform: PlatformQoder, Type: AccountTypeCosy, Credentials: baseCredentials}
	account.Credentials["site"] = "unknown"
	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "unsupported site")

	account.Credentials["site"] = "cn"
	account.Credentials["refresh_mode"] = "unknown"
	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "unsupported refresh_mode")
}

func TestValidateQoderCosyCredentialsRejectsMachineIDOnly(t *testing.T) {
	account := &Account{
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: map[string]any{"site": "cn", "machine_id": "machine-1"},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "pat or security_oauth_token")
}

func TestValidateQoderCosyCredentialsRejectsNonCosyQoderAccountType(t *testing.T) {
	account := &Account{
		Platform:    PlatformQoder,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "key"},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "require cosy")
}

func TestValidateQoderCosyCredentialsRejectsCosyNonQoderPlatform(t *testing.T) {
	account := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeCosy,
		Credentials: map[string]any{"pat": "pat"},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "requires qoder platform")
}

func TestValidateQoderCosyCredentialsRejectsMachineIDWithAuthDir(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":       "cn",
			"machine_id": "machine-1",
			"auth_dir":   "/tmp/qoder-auth",
		},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "pat or security_oauth_token")
}

func TestValidateQoderCosyCredentialsExchangesPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	calls := 0
	qoderValidateCNPAT = func(ctx context.Context, account *Account, pat string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		calls++
		require.Equal(t, "pat-123", pat)
		return &qoder.AuthIdentity{UID: "uid", SecurityOauthToken: "dt-token"}, nil
	}

	account := &Account{
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: map[string]any{"site": "cn", "pat": "pat-123"},
	}

	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
	require.Equal(t, 1, calls)
	require.Empty(t, account.GetCredential("machine_id"))
	require.Empty(t, account.GetCredential("machine_token"))
	require.Empty(t, account.GetCredential("machine_type"))
}

func TestUpdateLegacyQoderAccountRejectsSiteOnlyMigration(t *testing.T) {
	const accountID int64 = 1201
	baseRepo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:       accountID,
			Platform: PlatformQoder,
			Type:     AccountTypeCosy,
			Status:   StatusActive,
			Credentials: map[string]any{
				"site": "global",
				"pat":  "pat-123",
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{baseRepo}}

	_, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{"site": "cn"},
	})

	require.ErrorIs(t, err, ErrQoderCNReauthorizationRequired)
	require.Equal(t, "global", baseRepo.accounts[accountID].GetCredential("site"))
}

func TestUpdateLegacyQoderAccountAcceptsFreshCNPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, pat string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		require.Equal(t, "new-cn-pat", pat)
		require.NotEmpty(t, machine.MachineID)
		return &qoder.AuthIdentity{UID: "uid-cn", SecurityOauthToken: "token-cn"}, nil
	}
	const accountID int64 = 1203
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:          accountID,
			Platform:    PlatformQoder,
			Type:        AccountTypeCosy,
			Status:      StatusActive,
			Credentials: map[string]any{"site": "global", "pat": "old-global-pat"},
		},
	}}
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{repo}}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{"site": "cn", "pat": "new-cn-pat"},
	})

	require.NoError(t, err)
	require.Equal(t, "cn", updated.GetCredential("site"))
	require.Equal(t, "new-cn-pat", updated.GetCredential("pat"))
	require.Equal(t, updated.GetCredential("machine_id"), updated.GetCredential("machine_token"))
	require.Equal(t, "5", updated.GetCredential("machine_type"))
}

func TestValidateQoderCosyCredentialsRejectsBadPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(ctx context.Context, account *Account, pat string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		return nil, errors.New("bad pat")
	}

	account := &Account{
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: map[string]any{"site": "cn", "pat": "pat-123"},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "bad pat")
}

func TestValidateQoderCosyCredentialsPATUsesAccountDoer(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(ctx context.Context, _ *Account, _ string, _ *qoder.MachineIdentity, doer qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://gateway.qoder.com.cn/test", nil)
		require.NoError(t, err)
		resp, err := doer(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return &qoder.AuthIdentity{UID: "uid-1", SecurityOauthToken: "dt-token"}, nil
	}
	upstream := &qoderValidationHTTPUpstreamStub{}
	proxyID := int64(9)
	account := &Account{
		ID:          109,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Concurrency: 5,
		ProxyID:     &proxyID,
		Proxy:       &Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080},
		Credentials: map[string]any{"site": "cn", "pat": "pat-123"},
	}

	err := validateQoderCosyCredentials(context.Background(), account, upstream, nil)

	require.NoError(t, err)
	require.Equal(t, "http://proxy.example.com:8080", upstream.proxyURL)
	require.Equal(t, int64(109), upstream.accountID)
	require.Equal(t, 5, upstream.accountConcurrency)
}

type qoderValidationHTTPUpstreamStub struct {
	proxyURL           string
	accountID          int64
	accountConcurrency int
}

func (s *qoderValidationHTTPUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *qoderValidationHTTPUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	s.accountID = accountID
	s.accountConcurrency = accountConcurrency
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{
			"id":"user-1",
			"name":"User",
			"userType":"personal_standard",
			"securityOauthToken":"dt-from-center",
			"refreshToken":"rt-from-center"
		}`)),
		Request: req,
	}, nil
}
