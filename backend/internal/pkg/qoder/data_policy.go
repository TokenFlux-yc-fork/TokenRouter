package qoder

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const DataPolicyPath = "/api/v2/config/getDataPolicy"

type dataPolicyResponse struct {
	Success *bool `json:"success"`
	Result  struct {
		Status string `json:"status"`
	} `json:"result"`
}

// GetDataPolicyContext reads the policy state used by qoderclicn when building
// authenticated Gateway requests. NO_RECORD has the same wire behavior as AGREE.
func GetDataPolicyContext(
	ctx context.Context,
	identity *AuthIdentity,
	machine *MachineIdentity,
	profile Profile,
	doer RequestDoer,
) (bool, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return false, err
	}
	if normalized.Site != SiteCN {
		return false, fmt.Errorf("qoder: data policy requires cn site")
	}
	if identity == nil || strings.TrimSpace(identity.UID) == "" {
		return false, fmt.Errorf("qoder: data policy requires user identity")
	}
	if machine == nil {
		machine = NewMachineForSite(normalized.Site)
	}

	session, err := NewSessionForProfileWithKey(identity, machine, normalized, nil)
	if err != nil {
		return false, err
	}
	requestID := RandomUUIDLike()
	query := url.Values{}
	query.Set("requestId", requestID)
	query.Set("version", "2")
	logicalPath := DataPolicyPath + "?" + query.Encode()

	var response dataPolicyResponse
	client := NewClientForProfile(normalized)
	if err := client.JSONRequestContextWithDoer(
		ctx,
		http.MethodGet,
		session,
		logicalPath,
		nil,
		nil,
		doer,
		&response,
	); err != nil {
		return false, fmt.Errorf("qoder: get data policy: %w", err)
	}
	if response.Success != nil && !*response.Success {
		return false, fmt.Errorf("qoder: get data policy was rejected")
	}
	switch strings.ToUpper(strings.TrimSpace(response.Result.Status)) {
	case "AGREE", "NO_RECORD":
		return true, nil
	case "DISAGREE":
		return false, nil
	default:
		return false, fmt.Errorf("qoder: get data policy returned an unknown status")
	}
}

// GetDataPolicy reuses the OAuth client's frozen profile and transport so the
// Gateway probe follows the same proxy and TLS path as login.
func (c *OAuthClient) GetDataPolicy(ctx context.Context, identity *AuthIdentity, machine *MachineIdentity) (bool, error) {
	if c == nil {
		c = NewOAuthClient("", nil)
	}
	return GetDataPolicyContext(ctx, identity, machine, c.profile(), c.do)
}
