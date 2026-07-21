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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateQoderCosyCredentialsAcceptsDirectToken(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "dt-token",
			"refresh_token":        "refresh-1",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		}),
	}

	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
	require.Empty(t, account.GetCredential("machine_token"))
	require.Empty(t, account.GetCredential("machine_type"))
}

func TestValidateQoderCosyCredentialsRequiresRefreshTokenForQoderCN20(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		},
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "refresh_token")
	account.Credentials["refresh_token"] = "refresh-1"
	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
}

func TestQoderCNReauthorizationProvidedRequiresCompleteQoderCN20Credentials(t *testing.T) {
	credentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "access-1",
		"machine_id":           "machine-1",
		"uid":                  "uid-1",
	}
	require.False(t, QoderCNReauthorizationProvided(credentials))

	credentials["refresh_token"] = "refresh-1"
	require.True(t, QoderCNReauthorizationProvided(credentials))
	require.True(t, QoderCNReauthorizationProvided(map[string]any{
		"site": "cn",
		"pat":  "pat-1",
	}))
}

func TestCreateQoderDirectTokenAccountPersistsStableMachineIdentity(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "qoder-direct",
		Platform:             PlatformQoder,
		Type:                 AccountTypeCosy,
		SkipDefaultGroupBind: true,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "dt-token",
			"refresh_token":        "refresh-1",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
		}),
	})

	require.NoError(t, err)
	require.Equal(t, "machine-1", created.GetCredential("machine_id"))
	require.NotEmpty(t, created.GetCredential("machine_token"))
	require.NotEmpty(t, created.GetCredential("machine_type"))
}

func TestCreateQoderDirectTokenAccountRejectsMissingMachineID(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	credentials := completeQoderCN20TestCredentials(map[string]any{
		"security_oauth_token": "dt-token",
		"refresh_token":        "refresh-1",
		"machine_id":           nil,
		"uid":                  "uid-1",
	})

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
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  nil,
			"aid":                  "aid-1",
		}),
	}

	require.NoError(t, ValidateQoderCosyCredentials(context.Background(), account))
}

func TestValidateQoderCosyCredentialsRejectsDirectTokenWithoutIdentity(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "dt-token",
			"machine_id":           "machine-1",
			"uid":                  nil,
		}),
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "uid or aid")
}

func TestValidateQoderCosyCredentialsRejectsUnknownSiteAndRefreshMode(t *testing.T) {
	baseCredentials := completeQoderCN20TestCredentials(map[string]any{
		"security_oauth_token": "dt-token",
		"machine_id":           "machine-1",
		"uid":                  "uid-1",
	})
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
		Credentials: completeQoderCN20TestCredentials(map[string]any{"security_oauth_token": nil}),
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "security_oauth_token")
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
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": nil,
			"auth_dir":             "/tmp/qoder-auth",
		}),
	}

	require.ErrorContains(t, ValidateQoderCosyCredentials(context.Background(), account), "security_oauth_token")
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

func TestUpdateLegacyQoderAccountRequiresDedicatedAuthorizationEndpoint(t *testing.T) {
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

	require.ErrorIs(t, err, errQoderAuthorizationEndpointRequired)
	require.Equal(t, "global", baseRepo.accounts[accountID].GetCredential("site"))
}

func TestApplyQoderAuthorizationAcceptsFreshCNPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, pat string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		require.Equal(t, "new-cn-pat", pat)
		require.NotEmpty(t, machine.MachineID)
		return &qoder.AuthIdentity{UID: "uid-cn", SecurityOauthToken: "token-cn"}, nil
	}
	const accountID int64 = 1203
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:          accountID,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"site": "global", "pat": "old-global-pat"},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.ApplyQoderAuthorization(
		context.Background(), accountID, map[string]any{"site": "cn", "pat": "new-cn-pat"}, nil,
	)

	require.NoError(t, err)
	require.Equal(t, "cn", updated.GetCredential("site"))
	require.Equal(t, "new-cn-pat", updated.GetCredential("pat"))
	require.Equal(t, updated.GetCredential("machine_id"), updated.GetCredential("machine_token"))
	require.Equal(t, "5", updated.GetCredential("machine_type"))
}

func TestApplyQoderAuthorizationReplacesPATWithFreshCNOAuth(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, _ string, _ *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		return nil, errors.New("stale PAT must not be used after OAuth reauthorization")
	}

	const accountID int64 = 1204
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:          accountID,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"site":                    "global",
			"pat":                     "old-global-pat",
			"access_token":            "old-global-access",
			"security_oauth_token":    "old-global-token",
			"refresh_token":           "old-global-refresh",
			"machine_id":              "old-machine",
			"uid":                     "old-uid",
			"extra":                   map[string]any{"legacy": true},
			"auth_dir":                "/legacy/qoder/auth",
			"_token_version":          int64(7),
			"model_mapping":           map[string]any{"custom": "qwen3.7-plus"},
			"model_whitelist":         []any{"qwen3.7-plus"},
			"data_policy":             "disagree",
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"x-test-header": "kept",
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.ApplyQoderAuthorization(context.Background(), accountID, map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "new-cn-token",
		"refresh_token":        "new-cn-refresh",
		"machine_id":           "new-machine",
		"uid":                  "new-uid",
		"extra":                map[string]any{"current": true},
	}, nil)

	require.NoError(t, err)
	require.NotContains(t, updated.Credentials, "pat")
	require.NotContains(t, updated.Credentials, "access_token")
	require.NotContains(t, updated.Credentials, "auth_dir")
	require.Greater(t, updated.GetCredentialAsInt64("_token_version"), int64(7))
	require.Equal(t, "new-cn-token", updated.GetCredential("security_oauth_token"))
	require.Equal(t, "new-cn-refresh", updated.GetCredential("refresh_token"))
	require.Equal(t, "new-machine", updated.GetCredential("machine_id"))
	require.Equal(t, "new-uid", updated.GetCredential("uid"))
	require.Equal(t, map[string]any{"current": true}, updated.Credentials["extra"])
	require.Equal(t, map[string]any{"custom": "qwen3.7-plus"}, updated.Credentials["model_mapping"])
	require.Equal(t, []any{"qwen3.7-plus"}, updated.Credentials["model_whitelist"])
	require.NotContains(t, updated.Credentials, "data_policy")
	require.Equal(t, true, updated.Credentials["header_override_enabled"])
	require.Equal(t, map[string]any{"x-test-header": "kept"}, updated.Credentials["header_overrides"])
}

func TestApplyQoderAuthorizationReplacesOAuthWithFreshCNPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, pat string, machine *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		require.Equal(t, "new-cn-pat", pat)
		require.NotEqual(t, "old-machine", machine.MachineID)
		return &qoder.AuthIdentity{UID: "new-uid", SecurityOauthToken: "new-token"}, nil
	}

	const accountID int64 = 1205
	repo := &qoderAuthorizationRepoStub{account: &Account{
		ID:          accountID,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "old-machine",
			"machine_token":        "old-machine",
			"machine_type":         "5",
			"uid":                  "old-uid",
			"organization_id":      "old-org",
			"expires_at":           "2099-01-01T00:00:00Z",
			"model_mapping":        map[string]any{"custom": "qwen3.7-plus"},
			"data_policy":          "agree",
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.ApplyQoderAuthorization(context.Background(), accountID, map[string]any{
		"site": "cn",
		"pat":  "new-cn-pat",
	}, nil)

	require.NoError(t, err)
	require.Equal(t, "new-cn-pat", updated.GetCredential("pat"))
	require.NotContains(t, updated.Credentials, "security_oauth_token")
	require.NotContains(t, updated.Credentials, "refresh_token")
	require.NotContains(t, updated.Credentials, "uid")
	require.NotContains(t, updated.Credentials, "organization_id")
	require.NotContains(t, updated.Credentials, "expires_at")
	require.Equal(t, updated.GetCredential("machine_id"), updated.GetCredential("machine_token"))
	require.Equal(t, "5", updated.GetCredential("machine_type"))
	require.Positive(t, updated.GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"custom": "qwen3.7-plus"}, updated.Credentials["model_mapping"])
	require.NotContains(t, updated.Credentials, "data_policy")
}

func TestUpdateQoderAccountRejectsIncompleteSecretRotationWithoutFallingBackToOldPAT(t *testing.T) {
	old := qoderValidateCNPAT
	defer func() { qoderValidateCNPAT = old }()
	qoderValidateCNPAT = func(_ context.Context, _ *Account, _ string, _ *qoder.MachineIdentity, _ qoder.RequestDoer) (*qoder.AuthIdentity, error) {
		t.Fatal("incomplete OAuth rotation must not validate or reuse the old PAT")
		return nil, nil
	}

	const accountID int64 = 1206
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:       accountID,
			Platform: PlatformQoder,
			Type:     AccountTypeCosy,
			Status:   StatusActive,
			Credentials: map[string]any{
				"site": "cn",
				"pat":  "old-pat",
			},
		},
	}}
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{repo}}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"site":                 "cn",
			"security_oauth_token": "incomplete-token",
			"machine_id":           "machine-id",
			"uid":                  "user-id",
		},
	})

	require.Nil(t, updated)
	require.ErrorIs(t, err, errQoderAuthorizationEndpointRequired)
	require.Equal(t, "old-pat", repo.accounts[accountID].GetCredential("pat"))
}

func TestUpdateQoderAccountRejectsPartialIdentityEditsAndAllowsConfigurationEdits(t *testing.T) {
	const accountID int64 = 1207
	baseCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "access-v1",
		"refresh_token":        "refresh-v1",
		"machine_id":           "machine-v1",
		"uid":                  "uid-v1",
		"_token_version":       int64(11),
		"model_mapping":        map[string]any{"alias": "old-route"},
		"model_whitelist":      []any{"existing-alias"},
		"data_policy":          "disagree",
		"header_overrides":     map[string]any{"x-existing": "kept"},
	}

	for name, credentials := range map[string]map[string]any{
		"machine id":    {"machine_id": "machine-v2"},
		"uid":           {"uid": "uid-v2"},
		"data policy":   {"data_policy": "agree"},
		"refresh mode":  {"refresh_mode": "cosy"},
		"token version": {"_token_version": int64(12)},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				accountID: {
					ID:          accountID,
					Platform:    PlatformQoder,
					Type:        AccountTypeCosy,
					Status:      StatusActive,
					Schedulable: true,
					Credentials: shallowCopyMap(baseCredentials),
				},
			}}
			svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{repo}}

			updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{Credentials: credentials})

			require.Nil(t, updated)
			require.ErrorIs(t, err, errQoderAuthorizationEndpointRequired)
			require.Equal(t, baseCredentials, repo.accounts[accountID].Credentials)
		})
	}

	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:          accountID,
			Platform:    PlatformQoder,
			Type:        AccountTypeCosy,
			Status:      StatusActive,
			Schedulable: true,
			Credentials: shallowCopyMap(baseCredentials),
		},
	}}
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAdminRepo{repo}}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"model_mapping": map[string]any{"alias": "new-route"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "access-v1", updated.GetCredential("security_oauth_token"))
	require.Equal(t, int64(11), updated.GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"alias": "new-route"}, updated.Credentials["model_mapping"])
	require.Equal(t, []any{"existing-alias"}, updated.Credentials["model_whitelist"])
	require.Equal(t, "disagree", updated.GetCredential("data_policy"))
	require.Equal(t, map[string]any{"x-existing": "kept"}, updated.Credentials["header_overrides"])
}

func TestReplaceQoderAuthorizationCredentialsAdvancesVersionMonotonically(t *testing.T) {
	futureVersion := time.Now().Add(time.Hour).UnixMilli()
	current := map[string]any{
		"site":                  "cn",
		"pat":                   "pat-v1",
		"device_token":          "device-v1",
		"personal_access_token": "personal-v1",
		"_token_version":        futureVersion,
		"model_mapping":         map[string]any{"alias": "route"},
		"data_policy":           "disagree",
	}

	first := replaceQoderAuthorizationCredentials(current, map[string]any{
		"site":          "cn",
		"pat":           "pat-v2",
		"model_mapping": map[string]any{"alias": "untrusted-route"},
		"data_policy":   "agree",
	})
	competing := replaceQoderAuthorizationCredentials(current, map[string]any{"site": "cn", "pat": "pat-competing"})
	second := replaceQoderAuthorizationCredentials(first, map[string]any{"site": "cn", "pat": "pat-v3"})

	require.Equal(t, futureVersion+1, (&Account{Credentials: first}).GetCredentialAsInt64("_token_version"))
	require.Equal(t, futureVersion+1, (&Account{Credentials: competing}).GetCredentialAsInt64("_token_version"))
	require.Equal(t, futureVersion+2, (&Account{Credentials: second}).GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"alias": "route"}, second["model_mapping"])
	require.Equal(t, "agree", first["data_policy"])
	require.NotContains(t, competing, "data_policy")
	require.NotContains(t, second, "data_policy")
	require.NotContains(t, first, "device_token")
	require.NotContains(t, first, "personal_access_token")
}

func TestQoderCredentialIdentitySnapshotIncludesTokenAliasesAndDataPolicy(t *testing.T) {
	snapshot := QoderCredentialIdentitySnapshot(map[string]any{
		"device_token":          "device-token",
		"personal_access_token": "personal-token",
		"personal_token":        "legacy-personal-token",
		"securityOauthToken":    "camel-security-token",
		"refreshToken":          "camel-refresh-token",
		"codeVerifier":          "camel-code-verifier",
		"data_policy":           "disagree",
	})

	require.Equal(t, "device-token", snapshot["device_token"])
	require.Equal(t, "personal-token", snapshot["personal_access_token"])
	require.Equal(t, "legacy-personal-token", snapshot["personal_token"])
	require.Equal(t, "camel-security-token", snapshot["securityOauthToken"])
	require.Equal(t, "camel-refresh-token", snapshot["refreshToken"])
	require.Equal(t, "camel-code-verifier", snapshot["codeVerifier"])
	require.Equal(t, "disagree", snapshot["data_policy"])
}

func TestReplaceQoderAuthorizationCredentialsClearsLegacyIdentityAliases(t *testing.T) {
	legacyKeys := []string{
		"personal_token", "personalAccessToken", "personalToken", "accessToken", "deviceToken",
		"securityOauthToken", "refreshToken", "machineId", "machineToken", "machineType",
		"organizationId", "organizationName", "organizationTags", "userType", "expiresAt",
		"dataPolicy", "data_policy_agreed", "dataPolicyAgreed",
		"authDir", "tokenVersion", "quota_key", "nonce", "verifier", "code_verifier", "codeVerifier",
	}
	existing := map[string]any{
		"site":          "cn",
		"pat":           "old-pat",
		"model_mapping": map[string]any{"alias": "route"},
	}
	for _, key := range legacyKeys {
		existing[key] = "stale-value"
	}

	replaced := replaceQoderAuthorizationCredentials(existing, map[string]any{
		"site": "cn",
		"pat":  "new-pat",
	})

	for _, key := range legacyKeys {
		require.NotContains(t, replaced, key)
	}
	require.Equal(t, "new-pat", replaced["pat"])
	require.Equal(t, map[string]any{"alias": "route"}, replaced["model_mapping"])
}

func TestMergeQoderConfigurationCredentialsDistinguishesOmittedAndDeletedModelMapping(t *testing.T) {
	existing := map[string]any{
		"site":                       "cn",
		"pat":                        "secret",
		"intercept_warmup_requests":  true,
		"model_mapping":              map[string]any{"alias": "route"},
		"temp_unschedulable_enabled": true,
		"temp_unschedulable_rules": []any{
			map[string]any{"error_code": float64(429), "duration_minutes": float64(10)},
		},
	}

	omitted := mergeQoderConfigurationCredentials(existing, map[string]any{"model_whitelist": []any{}})
	require.Equal(t, map[string]any{"alias": "route"}, omitted["model_mapping"])
	require.Equal(t, true, omitted["intercept_warmup_requests"])
	require.Equal(t, true, omitted["temp_unschedulable_enabled"])
	require.Contains(t, omitted, "temp_unschedulable_rules")

	deleted := mergeQoderConfigurationCredentials(existing, map[string]any{
		"intercept_warmup_requests":  nil,
		"model_mapping":              nil,
		"model_whitelist":            []any{},
		"temp_unschedulable_enabled": nil,
		"temp_unschedulable_rules":   nil,
	})
	require.NotContains(t, deleted, "model_mapping")
	require.NotContains(t, deleted, "intercept_warmup_requests")
	require.NotContains(t, deleted, "temp_unschedulable_enabled")
	require.NotContains(t, deleted, "temp_unschedulable_rules")
	require.Equal(t, []any{}, deleted["model_whitelist"])
	require.Equal(t, "secret", deleted["pat"])
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
