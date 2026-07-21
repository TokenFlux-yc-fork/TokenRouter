package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
)

var qoderValidateCNPAT = func(ctx context.Context, account *Account, pat string, machine *qoder.MachineIdentity, doer qoder.RequestDoer) (*qoder.AuthIdentity, error) {
	profile, err := qoder.ProfileForSite(qoder.SiteCN)
	if err != nil {
		return nil, err
	}
	identity, _, err := qoder.ExchangeQoderCN20PATContext(ctx, pat, machine, profile, doer)
	return identity, err
}

func ValidateQoderCosyCredentials(ctx context.Context, account *Account) error {
	return validateQoderCosyCredentials(ctx, account, nil, nil)
}

func validateQoderCosyCredentials(ctx context.Context, account *Account, httpUpstream HTTPUpstream, tlsFPProfileService *TLSFingerprintProfileService) error {
	if account == nil {
		return nil
	}
	if account.Platform != PlatformQoder {
		if account.Type == AccountTypeCosy {
			return fmt.Errorf("%s account type requires %s platform", AccountTypeCosy, PlatformQoder)
		}
		return nil
	}
	if account.Type != AccountTypeCosy {
		return fmt.Errorf("qoder accounts require %s account type", AccountTypeCosy)
	}
	if account.Credentials == nil {
		return errors.New("qoder cosy credentials are required")
	}
	if _, err := qoderSiteForAccount(account); err != nil {
		return err
	}
	if _, err := qoderRefreshModeForAccount(account); err != nil {
		return err
	}

	pat := strings.TrimSpace(account.GetCredential("pat"))
	if pat != "" {
		machine := qoderMachineForAccount(account)
		doer := newQoderRequestDoer(account, httpUpstream, tlsFPProfileService)
		if _, err := qoderValidateCNPAT(ctx, account, pat, machine, doer); err != nil {
			return fmt.Errorf("validate qoder cn pat: %w", err)
		}
		return nil
	}

	token := strings.TrimSpace(account.GetCredential("security_oauth_token"))
	machineID := strings.TrimSpace(account.GetCredential("machine_id"))
	if token != "" {
		if machineID == "" {
			return errors.New("qoder cosy credentials require machine_id with security_oauth_token")
		}
		if strings.TrimSpace(account.GetCredential("uid")) == "" && strings.TrimSpace(account.GetCredential("aid")) == "" {
			return errors.New("qoder cosy credentials require uid or aid with security_oauth_token")
		}
		return nil
	}
	return errors.New("qoder cosy credentials require pat or security_oauth_token+machine_id")
}
