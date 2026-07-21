package service

import (
	"context"
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
	if err := validateQoderCNAuthorizationCredentials(account.Credentials); err != nil {
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

	return nil
}
