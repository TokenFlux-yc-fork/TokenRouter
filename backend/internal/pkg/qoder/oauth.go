package qoder

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// OpenAPIBaseURL 是国内站 OpenAPI 地址。
	OpenAPIBaseURL = CNOpenAPIBaseURL

	// OAuthClientID 是国内站公开 client ID。
	OAuthClientID = CNOAuthClientID

	// DeviceAuthorizationURL 是国内站授权地址。
	DeviceAuthorizationURL = CNDeviceAuthorizationURL

	// DevicePollPath 是浏览器授权后轮询 device token 的端点。
	DevicePollPath = "/api/v1/deviceToken/poll"

	// UserInfoPath 返回 access token 对应 Qoder 用户身份的端点。
	UserInfoPath = "/api/v1/userinfo"

	// OrganizationTagsPathPrefix 返回 Qoder 用户组织元数据的端点前缀。
	OrganizationTagsPathPrefix = "/api/v1/organizations/"

	// JobTokenExchangePath 是国内站现代 QODER_PAT 的交换端点。
	JobTokenExchangePath = "/api/v1/jobToken/exchange"

	// DeviceTokenRefreshPath 是国内站 QoderCN20 token 刷新端点。
	DeviceTokenRefreshPath = "/api/v1/deviceToken/refresh"
)

type DeviceAuthRequest struct {
	Nonce         string
	CodeVerifier  string
	CodeChallenge string
	MachineID     string
	ClientID      string
	Profile       Profile
}

// FlexibleInt64 兼容 Qoder API 中以 JSON 数字或字符串返回的整数。
type FlexibleInt64 int64

// UnmarshalJSON 容错解析 JSON 数字、整数样式浮点数和数字字符串。
func (v *FlexibleInt64) UnmarshalJSON(data []byte) error {
	parsed, err := parseFlexibleInt64(data)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

// parseFlexibleInt64 解析 Qoder API 常见的宽松整数表示。
func parseFlexibleInt64(data []byte) (FlexibleInt64, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" || trimmed == `""` {
		return 0, nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return 0, err
		}
		trimmed = strings.TrimSpace(text)
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		floatValue, floatErr := strconv.ParseFloat(trimmed, 64)
		if floatErr != nil {
			return 0, fmt.Errorf("qoder: invalid integer %q", trimmed)
		}
		parsed = int64(floatValue)
	}
	return FlexibleInt64(parsed), nil
}

// DeviceTokenResponse 兼容国际设备授权和国内 QoderCN20 token 响应。
type DeviceTokenResponse struct {
	ID           string        `json:"id"`
	Token        string        `json:"token"`
	DeviceToken  string        `json:"device_token"`
	AccessToken  string        `json:"access_token"`
	RefreshToken string        `json:"refresh_token"`
	UserID       string        `json:"user_id"`
	UserName     string        `json:"user_name"`
	Email        string        `json:"email"`
	AvatarURL    string        `json:"avatar_url"`
	ExpiresAt    FlexibleInt64 `json:"expires_at"`
	ExpiresIn    FlexibleInt64 `json:"expires_in"`
	Scope        string        `json:"scope"`
	TokenType    string        `json:"token_type"`
	Nonce        string        `json:"nonce"`
}

// UnmarshalJSON 单独兼容 expires_at 的日期格式，并允许由有效 expires_in 回退。
func (r *DeviceTokenResponse) UnmarshalJSON(data []byte) error {
	type responseAlias DeviceTokenResponse

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	rawExpiresAt, hasExpiresAt := fields["expires_at"]
	delete(fields, "expires_at")

	remaining, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var decoded responseAlias
	if err := json.Unmarshal(remaining, &decoded); err != nil {
		return err
	}
	*r = DeviceTokenResponse(decoded)
	if !hasExpiresAt {
		return nil
	}

	expiresAt, err := parseFlexibleExpiresAt(rawExpiresAt)
	if err != nil {
		if int64(r.ExpiresIn) > 0 {
			return nil
		}
		return fmt.Errorf("qoder: invalid expires_at: %w", err)
	}
	r.ExpiresAt = expiresAt
	return nil
}

// parseFlexibleExpiresAt 将数字或常见 ISO 日期统一转换为 Unix 秒。
func parseFlexibleExpiresAt(data []byte) (FlexibleInt64, error) {
	parsed, err := parseFlexibleInt64(data)
	if err == nil {
		return parsed, nil
	}

	var text string
	if unmarshalErr := json.Unmarshal(data, &text); unmarshalErr != nil {
		return 0, err
	}
	text = strings.TrimSpace(text)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		time.DateOnly,
	} {
		var parsedTime time.Time
		if strings.Contains(layout, "Z07:00") {
			parsedTime, err = time.Parse(layout, text)
		} else {
			parsedTime, err = time.ParseInLocation(layout, text, time.UTC)
		}
		if err == nil {
			return FlexibleInt64(parsedTime.Unix()), nil
		}
	}
	return 0, fmt.Errorf("qoder: invalid expiry %q", text)
}

// UserInfo 兼容国内站 userinfo 的 snake_case 与 camelCase 字段。
type UserInfo struct {
	ID                     string   `json:"id"`
	UserID                 string   `json:"user_id"`
	UID                    string   `json:"uid"`
	Name                   string   `json:"name"`
	Username               string   `json:"username"`
	UserName               string   `json:"user_name"`
	UserType               string   `json:"userType"`
	Email                  string   `json:"email"`
	AvatarURL              string   `json:"avatar_url"`
	OrganizationID         string   `json:"organization_id"`
	OrganizationName       string   `json:"organization_name"`
	OrganizationTags       []string `json:"organization_tags"`
	IsDataPolicyModifiable *bool    `json:"is_data_policy_modifiable,omitempty"`
}

type OrganizationTags struct {
	Tags []string `json:"tags"`
}

func (u *UserInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID                        string   `json:"id"`
		UserID                    string   `json:"user_id"`
		UID                       string   `json:"uid"`
		Name                      string   `json:"name"`
		Username                  string   `json:"username"`
		UserName                  string   `json:"user_name"`
		Email                     string   `json:"email"`
		Avatar                    string   `json:"avatar"`
		AvatarURL                 string   `json:"avatar_url"`
		AvatarURLCamel            string   `json:"avatarUrl"`
		UserType                  string   `json:"userType"`
		UserTypeSnake             string   `json:"user_type"`
		OrganizationID            string   `json:"organization_id"`
		OrganizationName          string   `json:"organization_name"`
		OrganizationIDCamel       string   `json:"organizationId"`
		OrganizationNameCamel     string   `json:"organizationName"`
		OrgID                     string   `json:"orgId"`
		OrgName                   string   `json:"orgName"`
		OrganizationTags          []string `json:"organization_tags"`
		OrganizationTagsCamel     []string `json:"organizationTags"`
		IsPrivacyPolicyModifiable *bool    `json:"isPrivacyPolicyModifiable"`
		IsDataPolicyModifiable    *bool    `json:"is_data_policy_modifiable"`
		Organization              struct {
			ID                    string   `json:"id"`
			Name                  string   `json:"name"`
			OrgID                 string   `json:"org_id"`
			OrgName               string   `json:"org_name"`
			OrgIDCamel            string   `json:"orgId"`
			OrgNameCamel          string   `json:"orgName"`
			OrganizationID        string   `json:"organization_id"`
			OrganizationName      string   `json:"organization_name"`
			OrganizationIDCamel   string   `json:"organizationId"`
			OrganizationNameCamel string   `json:"organizationName"`
			OrganizationTags      []string `json:"organization_tags"`
			OrganizationTagsCamel []string `json:"organizationTags"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	organizationTags := normalizeOrganizationTags(raw.OrganizationTags)
	if len(organizationTags) == 0 {
		organizationTags = normalizeOrganizationTags(raw.OrganizationTagsCamel)
	}
	if len(organizationTags) == 0 {
		organizationTags = normalizeOrganizationTags(raw.Organization.OrganizationTags)
	}
	if len(organizationTags) == 0 {
		organizationTags = normalizeOrganizationTags(raw.Organization.OrganizationTagsCamel)
	}
	isDataPolicyModifiable := raw.IsPrivacyPolicyModifiable
	if isDataPolicyModifiable == nil {
		isDataPolicyModifiable = raw.IsDataPolicyModifiable
	}
	*u = UserInfo{
		ID:        raw.ID,
		UserID:    firstNonEmpty(raw.UserID, raw.UID),
		UID:       raw.UID,
		Name:      firstNonEmpty(raw.Name, raw.Username, raw.UserName),
		Username:  raw.Username,
		UserName:  firstNonEmpty(raw.UserName, raw.Username),
		UserType:  firstNonEmpty(raw.UserType, raw.UserTypeSnake),
		Email:     raw.Email,
		AvatarURL: firstNonEmpty(raw.AvatarURL, raw.AvatarURLCamel, raw.Avatar),
		OrganizationID: firstNonEmpty(
			raw.OrganizationID,
			raw.OrganizationIDCamel,
			raw.OrgID,
			raw.Organization.OrganizationID,
			raw.Organization.OrganizationIDCamel,
			raw.Organization.OrgID,
			raw.Organization.OrgIDCamel,
			raw.Organization.ID,
		),
		OrganizationName: firstNonEmpty(
			raw.OrganizationName,
			raw.OrganizationNameCamel,
			raw.OrgName,
			raw.Organization.OrganizationName,
			raw.Organization.OrganizationNameCamel,
			raw.Organization.OrgName,
			raw.Organization.OrgNameCamel,
			raw.Organization.Name,
		),
		OrganizationTags:       organizationTags,
		IsDataPolicyModifiable: isDataPolicyModifiable,
	}
	return nil
}

type OAuthClient struct {
	BaseURL    string
	HTTPClient *http.Client
	Profile    Profile
	Doer       RequestDoer
}

type oauthTransportError struct {
	operation string
	message   string
	cause     error
}

type redactedOAuthTransportCause struct {
	message string
	cause   error
}

func (e *oauthTransportError) Error() string {
	return fmt.Sprintf("qoder: %s: %s", e.operation, e.message)
}

func (e *oauthTransportError) Unwrap() error {
	return e.cause
}

func (e *redactedOAuthTransportCause) Error() string {
	return e.message
}

func (e *redactedOAuthTransportCause) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func newOAuthTransportError(operation string, err error, secrets ...string) error {
	return NewRedactedTransportError(operation, err, secrets...)
}

// NewRedactedTransportError preserves errors.Is classification while preventing
// request dumps and explicitly supplied credentials from entering error text.
func NewRedactedTransportError(operation string, err error, secrets ...string) error {
	message := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		message = urlErr.Err.Error()
	}
	message = RedactSensitiveTextWithSecrets(message, secrets...)
	if message == "" {
		message = "transport failed"
	}
	return &oauthTransportError{
		operation: operation,
		message:   message,
		cause:     &redactedOAuthTransportCause{message: message, cause: err},
	}
}

func NewOAuthClient(baseURL string, httpClient *http.Client) *OAuthClient {
	profile := MustProfileForSite(SiteCN)
	if strings.TrimSpace(baseURL) != "" {
		profile.OpenAPIBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	return NewOAuthClientForProfile(profile, httpClient)
}

// NewOAuthClientForProfile 使用站点 profile 创建 OpenAPI 客户端。
func NewOAuthClientForProfile(profile Profile, httpClient *http.Client) *OAuthClient {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		normalized = MustProfileForSite(SiteCN)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &OAuthClient{
		BaseURL:    strings.TrimRight(normalized.OpenAPIBaseURL, "/"),
		HTTPClient: httpClient,
		Profile:    normalized,
	}
}

func NewDeviceAuthRequest() (*DeviceAuthRequest, error) {
	return NewDeviceAuthRequestForProfile(MustProfileForSite(SiteCN))
}

// NewDeviceAuthRequestForSite 为指定站点生成设备授权参数。
func NewDeviceAuthRequestForSite(site Site) (*DeviceAuthRequest, error) {
	profile, err := ProfileForSite(site)
	if err != nil {
		return nil, err
	}
	return NewDeviceAuthRequestForProfile(profile)
}

// NewDeviceAuthRequestForProfile 使用冻结的站点 profile 生成设备授权参数。
func NewDeviceAuthRequestForProfile(profile Profile) (*DeviceAuthRequest, error) {
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return nil, err
	}
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		return nil, err
	}
	nonce := RandomUUIDLike()
	machineID := RandomToken(50)
	if normalized.Site == SiteCN {
		machineID = RandomUUIDLike()
	}
	return &DeviceAuthRequest{
		Nonce:         nonce,
		CodeVerifier:  verifier,
		CodeChallenge: GenerateCodeChallenge(verifier),
		MachineID:     machineID,
		ClientID:      normalized.OAuthClientID,
		Profile:       normalized,
	}, nil
}

func (r *DeviceAuthRequest) AuthorizationURL() string {
	profile := r.Profile
	if strings.TrimSpace(profile.DeviceAuthorizationURL) == "" {
		profile = MustProfileForSite(SiteCN)
	}
	clientID := strings.TrimSpace(r.ClientID)
	if clientID == "" {
		clientID = profile.OAuthClientID
	}
	params := url.Values{}
	params.Set("nonce", r.Nonce)
	params.Set("challenge", r.CodeChallenge)
	params.Set("challenge_method", "S256")
	params.Set("client_id", clientID)
	params.Set("machine_id", r.MachineID)
	return profile.DeviceAuthorizationURL + "?" + params.Encode()
}

func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func GenerateCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomUUIDLike 生成官方客户端使用的 UUID v4 字符串。
func RandomUUIDLike() string {
	return uuid.NewString()
}

func (c *OAuthClient) PollDeviceToken(ctx context.Context, nonce, verifier string) (*DeviceTokenResponse, bool, error) {
	if c == nil {
		c = NewOAuthClient("", nil)
	}
	values := url.Values{}
	values.Set("nonce", strings.TrimSpace(nonce))
	values.Set("verifier", strings.TrimSpace(verifier))
	values.Set("challenge_method", "S256")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+DevicePollPath+"?"+values.Encode(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("qoder: create device token poll request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.do(req)
	if err != nil {
		return nil, false, newOAuthTransportError("device token poll request", err, nonce, verifier)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, &OpenAPIError{
			Operation:  "device token poll",
			StatusCode: resp.StatusCode,
			Message:    RedactSensitiveTextWithSecrets(string(body), nonce, verifier),
		}
	}

	var token DeviceTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, false, fmt.Errorf("qoder: parse device token poll response: %w", err)
	}
	if token.DeviceLoginTokenValue() == "" {
		return nil, false, nil
	}
	return &token, true, nil
}

func (c *OAuthClient) GetUserInfo(ctx context.Context, token string) (*UserInfo, error) {
	if c == nil {
		c = NewOAuthClient("", nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+UserInfoPath, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder: create userinfo request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.do(req)
	if err != nil {
		return nil, newOAuthTransportError("userinfo request", err, token)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &OpenAPIError{
			Operation:  "userinfo",
			StatusCode: resp.StatusCode,
			Message:    RedactSensitiveTextWithSecrets(string(body), token),
		}
	}

	var info UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("qoder: parse userinfo response: %w", err)
	}
	return &info, nil
}

func (c *OAuthClient) GetOrganizationTags(ctx context.Context, token, organizationID string) (*OrganizationTags, error) {
	if c == nil {
		c = NewOAuthClient("", nil)
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return nil, fmt.Errorf("qoder: organization tags require organization_id")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+OrganizationTagsPathPrefix+url.PathEscape(organizationID)+"/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("qoder: create organization tags request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", c.userAgent())

	resp, err := c.do(req)
	if err != nil {
		return nil, newOAuthTransportError("organization tags request", err, token)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &OpenAPIError{
			Operation:  "organization tags",
			StatusCode: resp.StatusCode,
			Message:    RedactSensitiveTextWithSecrets(string(body), token),
		}
	}

	var tags OrganizationTags
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("qoder: parse organization tags response: %w", err)
	}
	return &tags, nil
}

// ExchangeQoderCN20PAT 使用国内站现代 QODER_PAT 换取 OpenAPI token。
func (c *OAuthClient) ExchangeQoderCN20PAT(ctx context.Context, pat string) (*DeviceTokenResponse, error) {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return nil, fmt.Errorf("qoder: PAT is required")
	}
	body, err := json.Marshal(map[string]string{"personal_token": pat})
	if err != nil {
		return nil, fmt.Errorf("qoder: encode PAT exchange request: %w", err)
	}
	token, err := c.postTokenRequest(ctx, JobTokenExchangePath, body, "PAT exchange", pat)
	if err != nil {
		return nil, err
	}
	if _, err := token.ValidatePAT(time.Now()); err != nil {
		return nil, fmt.Errorf("qoder: invalid PAT exchange response: %w", err)
	}
	return token, nil
}

// RefreshQoderCN20Token 使用国内站 refresh token 换取新的 OpenAPI token。
func (c *OAuthClient) RefreshQoderCN20Token(ctx context.Context, refreshToken string) (*DeviceTokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("qoder: refresh token is required")
	}
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("qoder: encode token refresh request: %w", err)
	}
	token, err := c.postTokenRequest(ctx, DeviceTokenRefreshPath, body, "token refresh", refreshToken)
	if err != nil {
		return nil, err
	}
	if _, err := token.ValidateDeviceRefresh(time.Now()); err != nil {
		return nil, fmt.Errorf("qoder: invalid token refresh response: %w", err)
	}
	return token, nil
}

// CompleteQoderCN20Identity 使用本客户端冻结的 profile 与 transport 完成国内 COSY 身份交换。
func (c *OAuthClient) CompleteQoderCN20Identity(
	ctx context.Context,
	token *DeviceTokenResponse,
	user *UserInfo,
	machine *MachineIdentity,
) (*AuthIdentity, time.Time, error) {
	return CompleteQoderCN20IdentityContext(ctx, c.profile(), token, user, machine, c.do)
}

func (c *OAuthClient) postTokenRequest(ctx context.Context, path string, body []byte, operation string, secrets ...string) (*DeviceTokenResponse, error) {
	if c == nil {
		c = NewOAuthClientForProfile(MustProfileForSite(SiteCN), nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("qoder: create %s request: %w", operation, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Cosy-Version", c.profile().ClientVersion)
	req.Header.Set("Cosy-ClientType", "5")
	req.Header.Set("Cosy-MachineOS", MachineOS())
	resp, err := c.do(req)
	if err != nil {
		return nil, newOAuthTransportError(operation+" request", err, secrets...)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &OpenAPIError{
			Operation:  operation,
			StatusCode: resp.StatusCode,
			Message:    RedactSensitiveTextWithSecrets(string(responseBody), secrets...),
		}
	}
	var token DeviceTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("qoder: parse %s response: %w", operation, err)
	}
	return &token, nil
}

// OpenAPIError 表示 OpenAPI 返回的脱敏 HTTP 错误。
type OpenAPIError struct {
	Operation  string
	StatusCode int
	Message    string
}

// Error 返回不包含原始凭据的错误文本。
func (e *OpenAPIError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		return fmt.Sprintf("qoder: %s failed with status %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("qoder: %s failed with status %d: %s", e.Operation, e.StatusCode, message)
}

// InvalidCredentials 判断 OpenAPI 错误是否明确表示 PAT 或 refresh token 已失效。
func (e *OpenAPIError) InvalidCredentials() bool {
	if e == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Operation)) {
	case "pat exchange":
		return e.StatusCode == http.StatusBadRequest ||
			e.StatusCode == http.StatusUnauthorized ||
			e.StatusCode == http.StatusForbidden
	case "token refresh":
		return e.StatusCode == http.StatusBadRequest || e.StatusCode == http.StatusUnauthorized
	default:
		return false
	}
}

// UpstreamFailure 判断 OpenAPI 错误是否为可重试的服务端故障。
func (e *OpenAPIError) UpstreamFailure() bool {
	return e != nil && e.StatusCode >= http.StatusInternalServerError
}

func (c *OAuthClient) profile() Profile {
	if c == nil {
		return MustProfileForSite(SiteCN)
	}
	profile, err := NormalizeProfile(c.Profile)
	if err != nil {
		profile = MustProfileForSite(SiteCN)
	}
	if strings.TrimSpace(c.BaseURL) != "" {
		profile.OpenAPIBaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	}
	return profile
}

func (c *OAuthClient) userAgent() string {
	return c.profile().OpenAPIUserAgent()
}

func (c *OAuthClient) do(req *http.Request) (*http.Response, error) {
	if c != nil && c.Doer != nil {
		return c.Doer(req)
	}
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient.Do(req)
	}
	return http.DefaultClient.Do(req)
}

func (r *DeviceTokenResponse) AccessTokenValue() string {
	if r == nil {
		return ""
	}
	return r.PATTokenValue()
}

// PATTokenValue 按 qoderclicn 的 job-token exchange 优先级选择 bearer token。
func (r *DeviceTokenResponse) PATTokenValue() string {
	if r == nil {
		return ""
	}
	return firstNonEmpty(r.Token, r.DeviceToken, r.AccessToken)
}

// DeviceLoginTokenValue 返回 device poll 唯一认可的 token 字段。
func (r *DeviceTokenResponse) DeviceLoginTokenValue() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Token)
}

// DeviceRefreshTokenValue 返回 device refresh 唯一认可的 device_token 字段。
func (r *DeviceTokenResponse) DeviceRefreshTokenValue() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.DeviceToken)
}

// ExpiryTime 将 token 响应中的绝对或相对有效期规范化为 UTC 时间。
func (r *DeviceTokenResponse) ExpiryTime(now time.Time) time.Time {
	if r == nil {
		return time.Time{}
	}
	expiresAt := int64(r.ExpiresAt)
	if expiresAt > 0 {
		switch {
		case expiresAt >= 1_000_000_000_000:
			return time.UnixMilli(expiresAt).UTC()
		case expiresAt >= 1_000_000_000:
			return time.Unix(expiresAt, 0).UTC()
		default:
			return now.Add(time.Duration(expiresAt) * time.Second).UTC()
		}
	}
	expiresIn := int64(r.ExpiresIn)
	if expiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(expiresIn) * time.Second).UTC()
}

// ValidateQoderCN20 保留旧调用名称；Qoder CN token 的 refresh 与 expiry 均可缺省。
func (r *DeviceTokenResponse) ValidateQoderCN20(now time.Time) (time.Time, error) {
	return r.ValidatePAT(now)
}

// ValidatePAT 校验 PAT exchange 的有效 token，过期时间为可选元数据。
func (r *DeviceTokenResponse) ValidatePAT(now time.Time) (time.Time, error) {
	if r == nil {
		return time.Time{}, fmt.Errorf("token response is empty")
	}
	if r.PATTokenValue() == "" {
		return time.Time{}, fmt.Errorf("access token is missing")
	}
	return r.ExpiryTime(now), nil
}

// ValidateDeviceLogin 校验 device poll 响应；身份补充字段和过期时间均可缺省。
func (r *DeviceTokenResponse) ValidateDeviceLogin(now time.Time) (time.Time, error) {
	if r == nil {
		return time.Time{}, fmt.Errorf("token response is empty")
	}
	if r.DeviceLoginTokenValue() == "" {
		return time.Time{}, fmt.Errorf("device login token is missing")
	}
	return r.ExpiryTime(now), nil
}

// ValidateDeviceRefresh 校验 device refresh 响应，严格要求轮换后的两个 token。
func (r *DeviceTokenResponse) ValidateDeviceRefresh(now time.Time) (time.Time, error) {
	if r == nil {
		return time.Time{}, fmt.Errorf("token response is empty")
	}
	if r.DeviceRefreshTokenValue() == "" {
		return time.Time{}, fmt.Errorf("device token is missing")
	}
	if strings.TrimSpace(r.RefreshToken) == "" {
		return time.Time{}, fmt.Errorf("refresh token is missing")
	}
	return r.ExpiryTime(now), nil
}

func BuildIdentityFromDeviceToken(user *UserInfo, token *DeviceTokenResponse) *AuthIdentity {
	if user != nil && token != nil {
		pollUserID := strings.TrimSpace(token.UserID)
		userinfoUserID := userIDFromInfo(user)
		if pollUserID != "" && userinfoUserID != "" && pollUserID != userinfoUserID {
			slog.Warn("qoder device login ignored mismatched userinfo enrichment")
			user = nil
		}
	}
	accessToken := token.DeviceLoginTokenValue()
	return buildIdentityFromTokenValue(user, token, accessToken)
}

func buildIdentityFromTokenValue(user *UserInfo, token *DeviceTokenResponse, accessToken string) *AuthIdentity {
	if token == nil {
		token = &DeviceTokenResponse{}
	}
	userID := strings.TrimSpace(token.UserID)
	name := strings.TrimSpace(token.UserName)
	userType := "personal_standard"
	organizationID := ""
	organizationName := ""
	var organizationTags []string
	if user != nil {
		if resolvedUserID := firstNonEmpty(user.UserID, user.UID, user.ID); resolvedUserID != "" {
			userID = resolvedUserID
		}
		name = firstNonEmpty(user.Name, user.UserName, token.UserName)
		if strings.TrimSpace(user.UserType) != "" {
			userType = strings.TrimSpace(user.UserType)
		}
		organizationID = strings.TrimSpace(user.OrganizationID)
		organizationName = strings.TrimSpace(user.OrganizationName)
		organizationTags = normalizeOrganizationTags(user.OrganizationTags)
	}
	return &AuthIdentity{
		Name:               name,
		AID:                userID,
		UID:                userID,
		OrganizationID:     organizationID,
		OrganizationName:   organizationName,
		OrganizationTags:   organizationTags,
		UserType:           userType,
		SecurityOauthToken: accessToken,
		RefreshToken:       strings.TrimSpace(token.RefreshToken),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
