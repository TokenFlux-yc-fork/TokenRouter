package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
)

var ErrQoderCNReauthorizationRequired = errors.New("qoder credentials require Qoder CN reauthorization")

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

func qoderCNReauthorizationProvided(credentials map[string]any) bool {
	if strings.ToLower(strings.TrimSpace(stringFromCredentialValue(credentials["site"]))) != string(qoder.SiteCN) {
		return false
	}
	if strings.TrimSpace(stringFromCredentialValue(credentials["pat"])) != "" {
		return true
	}
	if strings.ToLower(strings.TrimSpace(stringFromCredentialValue(credentials["refresh_mode"]))) != qoder.RefreshModeQoderCN20 {
		return false
	}
	return strings.TrimSpace(stringFromCredentialValue(credentials["security_oauth_token"])) != "" &&
		strings.TrimSpace(stringFromCredentialValue(credentials["machine_id"])) != "" &&
		firstNonEmptyQoder(
			stringFromCredentialValue(credentials["uid"]),
			stringFromCredentialValue(credentials["aid"]),
		) != ""
}

// qoderProfileForAccount 返回账号站点对应的集中式协议 profile。
func qoderProfileForAccount(account *Account) (qoder.Profile, error) {
	site, err := qoderSiteForAccount(account)
	if err != nil {
		return qoder.Profile{}, err
	}
	return qoder.ProfileForSite(site)
}

// qoderRefreshModeForAccount 严格读取刷新模式；旧账号默认旧式 COSY。
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
