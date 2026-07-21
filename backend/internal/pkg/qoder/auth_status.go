package qoder

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// QuotaUsagePath 是国内站 OpenAPI 额度查询路径。
	QuotaUsagePath = "/api/v2/quota/usage"
)

// ExchangeQoderCN20PATContext 完成国内现代 PAT 的 exchange 和 userinfo 链路。
func ExchangeQoderCN20PATContext(
	ctx context.Context,
	pat string,
	machine *MachineIdentity,
	profile Profile,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 PAT requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	openAPI := NewOAuthClientForProfile(normalized, nil)
	openAPI.Doer = doer
	token, err := openAPI.ExchangeQoderCN20PAT(ctx, pat)
	if err != nil {
		return nil, time.Time{}, err
	}
	expiresAt, err := token.ValidatePAT(time.Now())
	if err != nil {
		return nil, time.Time{}, err
	}
	accessToken := token.PATTokenValue()
	user, err := openAPI.GetUserInfo(ctx, accessToken)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("qoder: load PAT userinfo: %w", err)
	}
	identity := buildIdentityFromTokenValue(user, token, accessToken)
	if organizationID := strings.TrimSpace(identity.OrganizationID); organizationID != "" {
		if tags, tagsErr := openAPI.GetOrganizationTags(ctx, accessToken, organizationID); tagsErr == nil && tags != nil {
			identity.OrganizationTags = normalizeOrganizationTags(tags.Tags)
		}
	}
	if agreed, policyErr := openAPI.GetDataPolicy(ctx, identity, machine); policyErr == nil {
		identity.DataPolicyAgreed = &agreed
	}
	return identity, expiresAt, nil
}

// RefreshQoderCN20SessionContext 刷新国内 device token；账号身份由调用方保留。
func RefreshQoderCN20SessionContext(
	ctx context.Context,
	refreshToken string,
	machine *MachineIdentity,
	profile Profile,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 refresh requires cn site")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}
	openAPI := NewOAuthClientForProfile(normalized, nil)
	openAPI.Doer = doer
	token, err := openAPI.RefreshQoderCN20Token(ctx, refreshToken)
	if err != nil {
		return nil, time.Time{}, err
	}
	expiresAt, err := token.ValidateDeviceRefresh(time.Now())
	if err != nil {
		return nil, time.Time{}, err
	}
	return &AuthIdentity{
		SecurityOauthToken: token.DeviceRefreshTokenValue(),
		RefreshToken:       strings.TrimSpace(token.RefreshToken),
	}, expiresAt, nil
}

// CompleteQoderCN20IdentityContext 从 device poll 响应直接构造可用于推理的身份。
func CompleteQoderCN20IdentityContext(
	ctx context.Context,
	profile Profile,
	token *DeviceTokenResponse,
	user *UserInfo,
	machine *MachineIdentity,
	doer RequestDoer,
) (*AuthIdentity, time.Time, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, time.Time{}, err
	}
	if normalized.Site != SiteCN {
		return nil, time.Time{}, fmt.Errorf("qoder: QoderCN20 completion requires cn site")
	}
	expiresAt, err := token.ValidateDeviceLogin(time.Now())
	if err != nil {
		return nil, time.Time{}, err
	}
	return BuildIdentityFromDeviceToken(user, token), expiresAt, nil
}

func userIDFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.UserID, user.UID, user.ID)
}

func normalizeOrganizationTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	return normalized
}

func userNameFromInfo(user *UserInfo) string {
	if user == nil {
		return ""
	}
	return firstNonEmpty(user.Name, user.UserName)
}
