package service

import (
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

func TestQoderSiteForAccountRequiresCNReauthorizationForLegacyCredentials(t *testing.T) {
	for name, credentials := range map[string]map[string]any{
		"missing site": {},
		"global site":  {"site": "global"},
	} {
		t.Run(name, func(t *testing.T) {
			account := &Account{Platform: PlatformQoder, Type: AccountTypeCosy, Credentials: credentials}
			_, err := qoderSiteForAccount(account)
			require.ErrorIs(t, err, ErrQoderCNReauthorizationRequired)
		})
	}

	account := &Account{Platform: PlatformQoder, Type: AccountTypeCosy, Credentials: map[string]any{"site": "cn"}}
	site, err := qoderSiteForAccount(account)
	require.NoError(t, err)
	require.Equal(t, qoder.SiteCN, site)
}

func TestLegacyQoderAccountDoesNotSupportExplicitlyMappedModels(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site": "global",
			"model_mapping": map[string]any{
				"custom-model": "qmodel",
			},
		},
	}

	require.False(t, account.IsModelSupported("custom-model"))
}

func TestQoderMachineForAccountUsesQoderCLICNIdentity(t *testing.T) {
	account := &Account{
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":          "cn",
			"machine_id":    "machine-id",
			"machine_token": "legacy-random-token",
			"machine_type":  "legacy-random-type",
		},
	}

	machine := qoderMachineForAccount(account)
	require.Equal(t, "machine-id", machine.MachineID)
	require.Equal(t, "machine-id", machine.MachineToken)
	require.Equal(t, "5", machine.MachineType)
}
