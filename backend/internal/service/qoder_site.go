package service

import (
	"fmt"
	"reflect"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
)

var ErrQoderCNReauthorizationRequired = infraerrors.BadRequest(
	"QODER_CN_REAUTH_REQUIRED",
	"complete Qoder CN OAuth or PAT credentials are required",
)
var ErrQoderAuthorizationConflict = infraerrors.Conflict(
	"QODER_AUTHORIZATION_CONFLICT",
	"Qoder authorization changed concurrently; retry with the latest account state",
)
var ErrQoderAccountUpdateConflict = infraerrors.Conflict(
	"QODER_ACCOUNT_UPDATE_CONFLICT",
	"Qoder account changed concurrently; retry with the latest account state",
)

var qoderCredentialIdentityKeyList = []string{
	"pat", "personal_access_token", "personal_token", "personalAccessToken", "personalToken",
	"access_token", "accessToken", "device_token", "deviceToken",
	"security_oauth_token", "securityOauthToken", "refresh_token", "refreshToken",
	"machine_id", "machineId", "machine_token", "machineToken", "machine_type", "machineType",
	"uid", "aid", "organization_id", "organizationId", "organization_name", "organizationName",
	"organization_tags", "organizationTags", "name", "user_type", "userType", "site", "refresh_mode",
	"quota_key",
	"data_policy", "dataPolicy", "data_policy_agreed", "dataPolicyAgreed",
	"expires_at", "expiresAt", "extra", "auth_dir", "authDir", "_token_version", "tokenVersion",
	"nonce", "verifier", "code_verifier", "codeVerifier",
}

var qoderCredentialIdentityKeys = func() map[string]struct{} {
	keys := make(map[string]struct{}, len(qoderCredentialIdentityKeyList))
	for _, key := range qoderCredentialIdentityKeyList {
		keys[key] = struct{}{}
	}
	return keys
}()

// replaceQoderAuthorizationCredentials replaces the complete Qoder identity while
// preserving account-scoped routing/configuration keys such as model mappings.
func replaceQoderAuthorizationCredentials(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	for key, value := range existing {
		if _, identity := qoderCredentialIdentityKeys[key]; !identity {
			out[key] = value
		}
	}
	for key, value := range incoming {
		if _, identity := qoderCredentialIdentityKeys[key]; identity {
			out[key] = value
		}
	}
	// Derive the next version from the state that was read. Concurrent
	// reauthorizations then propose the same version, so the repository row lock
	// can reject the stale writer instead of turning wall-clock order into LWW.
	out["_token_version"] = (&Account{Credentials: existing}).GetCredentialAsInt64("_token_version") + 1
	return out
}

// QoderCredentialIdentityKeys returns the credential keys that belong to one
// Qoder authorization identity rather than account-local routing configuration.
func QoderCredentialIdentityKeys() []string {
	return append([]string(nil), qoderCredentialIdentityKeyList...)
}

// QoderCredentialIdentitySnapshot projects credentials onto the Qoder identity
// fields. Repository compare-and-set operations use this to preserve concurrent
// model and header configuration updates without accepting a stale authorization.
func QoderCredentialIdentitySnapshot(credentials map[string]any) map[string]any {
	identity := make(map[string]any, len(qoderCredentialIdentityKeyList))
	for _, key := range qoderCredentialIdentityKeyList {
		if value, ok := credentials[key]; ok {
			identity[key] = value
		}
	}
	return identity
}

func qoderCredentialsContainIdentityChange(existing, incoming map[string]any) bool {
	for key, incomingValue := range incoming {
		if _, identity := qoderCredentialIdentityKeys[key]; !identity {
			continue
		}
		existingValue, exists := existing[key]
		if !exists || !reflect.DeepEqual(existingValue, incomingValue) {
			return true
		}
	}
	return false
}

var qoderConfigurationTombstoneKeys = map[string]struct{}{
	"intercept_warmup_requests":  {},
	"model_mapping":              {},
	"temp_unschedulable_enabled": {},
	"temp_unschedulable_rules":   {},
}

func mergeQoderConfigurationCredentials(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	for key, value := range existing {
		out[key] = value
	}
	for key, value := range incoming {
		if _, identity := qoderCredentialIdentityKeys[key]; identity {
			continue
		}
		if value == nil {
			if _, tombstone := qoderConfigurationTombstoneKeys[key]; tombstone {
				delete(out, key)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func qoderBulkCredentialsContainIdentityMutation(credentials map[string]any) bool {
	for key := range credentials {
		if _, identity := qoderCredentialIdentityKeys[key]; identity {
			return true
		}
	}
	return false
}

// qoderSiteForAccount 只接受明确标记为 CN 的凭据，避免静默复用国际站 token。
func qoderSiteForAccount(account *Account) (qoder.Site, error) {
	if account == nil {
		return "", fmt.Errorf("qoder: account is nil")
	}
	site := strings.ToLower(strings.TrimSpace(account.GetCredential("site")))
	if site == "" || site == string(qoder.SiteGlobal) {
		return "", ErrQoderCNReauthorizationRequired
	}
	return qoder.ParseSite(site)
}

func validateQoderCNAuthorizationCredentials(credentials map[string]any) error {
	if credentials == nil {
		return fmt.Errorf("qoder cosy credentials are required")
	}

	siteValue := strings.ToLower(strings.TrimSpace(stringFromCredentialValue(credentials["site"])))
	if siteValue == "" || siteValue == string(qoder.SiteGlobal) {
		return ErrQoderCNReauthorizationRequired
	}
	site, err := qoder.ParseSite(siteValue)
	if err != nil {
		return err
	}
	if site != qoder.SiteCN {
		return ErrQoderCNReauthorizationRequired
	}

	if strings.TrimSpace(stringFromCredentialValue(credentials["pat"])) != "" {
		return nil
	}
	if _, err := qoder.ParseRefreshMode(stringFromCredentialValue(credentials["refresh_mode"])); err != nil {
		return err
	}
	if strings.TrimSpace(stringFromCredentialValue(credentials["security_oauth_token"])) == "" {
		return fmt.Errorf("qoder qodercn20 credentials require security_oauth_token")
	}
	if strings.TrimSpace(stringFromCredentialValue(credentials["refresh_token"])) == "" {
		return fmt.Errorf("qoder qodercn20 credentials require refresh_token")
	}
	if strings.TrimSpace(stringFromCredentialValue(credentials["machine_id"])) == "" {
		return fmt.Errorf("qoder qodercn20 credentials require machine_id")
	}
	if firstNonEmptyQoder(
		stringFromCredentialValue(credentials["uid"]),
		stringFromCredentialValue(credentials["aid"]),
	) == "" {
		return fmt.Errorf("qoder qodercn20 credentials require uid or aid")
	}
	return nil
}

func QoderCNReauthorizationProvided(credentials map[string]any) bool {
	return validateQoderCNAuthorizationCredentials(credentials) == nil
}

// qoderProfileForAccount 返回账号站点对应的集中式协议 profile。
func qoderProfileForAccount(account *Account) (qoder.Profile, error) {
	site, err := qoderSiteForAccount(account)
	if err != nil {
		return qoder.Profile{}, err
	}
	return qoder.ProfileForSite(site)
}

// qoderRefreshModeForAccount 严格读取 Qoder CN device OAuth 刷新模式。
func qoderRefreshModeForAccount(account *Account) (string, error) {
	if account == nil {
		return "", fmt.Errorf("qoder: account is nil")
	}
	return qoder.ParseRefreshMode(account.GetCredential("refresh_mode"))
}

// ensureQoderMachineCredentials 为新建 Qoder 账号补齐并持久化站点对应的稳定机器身份。
// direct token 账号必须由调用方提供 machine_id。
func ensureQoderMachineCredentials(account *Account) {
	if account == nil {
		return
	}
	if account.Credentials == nil {
		account.Credentials = make(map[string]any)
	}
	pat := strings.TrimSpace(account.GetCredential("pat"))
	directToken := strings.TrimSpace(account.GetCredential("security_oauth_token"))
	machineID := strings.TrimSpace(account.GetCredential("machine_id"))
	if pat == "" && (directToken == "" || machineID == "") {
		return
	}
	site, err := qoderSiteForAccount(account)
	if err != nil {
		return
	}
	if pat != "" && machineID == "" {
		machineID = qoder.NewMachineForSite(site).MachineID
		account.Credentials["machine_id"] = machineID
	}
	if machineID != "" {
		account.Credentials["machine_token"] = machineID
		account.Credentials["machine_type"] = "5"
	}
}

// qoderMachineForAccount 读取持久化机器身份。
func qoderMachineForAccount(account *Account) *qoder.MachineIdentity {
	if account == nil {
		return qoder.NewMachine()
	}
	site, err := qoderSiteForAccount(account)
	if err != nil {
		return qoder.NewMachineForSite(qoder.SiteCN)
	}
	machineID := strings.TrimSpace(account.GetCredential("machine_id"))
	if machineID == "" {
		machineID = qoder.NewMachineForSite(site).MachineID
	}
	return &qoder.MachineIdentity{
		MachineID:    machineID,
		MachineToken: machineID,
		MachineType:  "5",
	}
}

func qoderOrganizationTags(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	tags := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		tags = append(tags, value)
	}
	return tags
}

func qoderOrganizationTagsFromCredentials(credentials map[string]any) []string {
	if credentials == nil {
		return nil
	}
	for _, key := range []string{"organization_tags", "organizationTags"} {
		switch value := credentials[key].(type) {
		case []string:
			if tags := qoderOrganizationTags(value); len(tags) > 0 {
				return tags
			}
		case []any:
			tags := make([]string, 0, len(value))
			for _, item := range value {
				if tag, ok := item.(string); ok {
					tags = append(tags, tag)
				}
			}
			if tags = qoderOrganizationTags(tags); len(tags) > 0 {
				return tags
			}
		case string:
			if tags := qoderOrganizationTags(strings.Split(value, ",")); len(tags) > 0 {
				return tags
			}
		}
	}
	return nil
}

// qoderStreamClientForAccount 保留测试注入客户端，生产客户端则按账号站点重建端点和版本。
func qoderStreamClientForAccount(configured qoderStreamClient, account *Account) (qoderStreamClient, error) {
	if configured != nil {
		if _, productionClient := configured.(*qoder.Client); !productionClient {
			return configured, nil
		}
	}
	profile, err := qoderProfileForAccount(account)
	if err != nil {
		return nil, err
	}
	return qoder.NewClientForProfile(profile), nil
}
