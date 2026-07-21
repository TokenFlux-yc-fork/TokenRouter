package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPollDeviceTokenTransportErrorRedactsPKCEQuery(t *testing.T) {
	client := NewOAuthClient("https://openapi.example", nil)
	transportErr := errors.New("transport failed")
	client.Doer = func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: transportErr}
	}

	_, _, err := client.PollDeviceToken(context.Background(), "nonce-secret", "verifier-secret")

	require.Error(t, err)
	require.NotContains(t, err.Error(), "nonce-secret")
	require.NotContains(t, err.Error(), "verifier-secret")
	require.NotContains(t, err.Error(), "nonce=")
	require.NotContains(t, err.Error(), "verifier=")
	require.ErrorIs(t, err, transportErr)
	require.NotContains(t, errors.Unwrap(err).Error(), "nonce-secret")
	require.NotContains(t, errors.Unwrap(err).Error(), "verifier-secret")
	var urlErr *url.Error
	require.False(t, errors.As(err, &urlErr))
}

func TestQoderOAuthTransportErrorsRedactRequestSecrets(t *testing.T) {
	transportErr := errors.New("transport failed")
	tests := []struct {
		name   string
		secret string
		call   func(*OAuthClient, string) error
	}{
		{
			name:   "userinfo bearer",
			secret: "userinfo-secret",
			call: func(client *OAuthClient, secret string) error {
				_, err := client.GetUserInfo(context.Background(), secret)
				return err
			},
		},
		{
			name:   "organization tags bearer",
			secret: "organization-secret",
			call: func(client *OAuthClient, secret string) error {
				_, err := client.GetOrganizationTags(context.Background(), secret, "org-1")
				return err
			},
		},
		{
			name:   "PAT request body",
			secret: "pat-secret",
			call: func(client *OAuthClient, secret string) error {
				_, err := client.ExchangeQoderCN20PAT(context.Background(), secret)
				return err
			},
		},
		{
			name:   "refresh request body",
			secret: "refresh-secret",
			call: func(client *OAuthClient, secret string) error {
				_, err := client.RefreshQoderCN20Token(context.Background(), secret)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewOAuthClient("https://openapi.example", nil)
			client.Doer = func(req *http.Request) (*http.Response, error) {
				var body []byte
				if req.Body != nil {
					body, _ = io.ReadAll(req.Body)
				}
				leakedRequest := req.URL.String() + " Authorization=" + req.Header.Get("Authorization") + " body=" + string(body)
				return nil, &url.Error{
					Op:  req.Method,
					URL: leakedRequest,
					Err: fmt.Errorf("request dump %s: %w", leakedRequest, transportErr),
				}
			}

			err := tt.call(client, tt.secret)
			require.Error(t, err)
			require.ErrorIs(t, err, transportErr)
			require.NotContains(t, err.Error(), tt.secret)
			require.NotContains(t, errors.Unwrap(err).Error(), tt.secret)
			var urlErr *url.Error
			require.False(t, errors.As(err, &urlErr))
		})
	}
}

func TestQoderOAuthHTTPErrorBodiesRedactEchoedRequestSecrets(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		call    func(*OAuthClient) error
	}{
		{
			name:    "device poll PKCE",
			secrets: []string{"nonce-free-text-secret", "verifier-free-text-secret"},
			call: func(client *OAuthClient) error {
				_, _, err := client.PollDeviceToken(context.Background(), "nonce-free-text-secret", "verifier-free-text-secret")
				return err
			},
		},
		{
			name:    "userinfo bearer",
			secrets: []string{"userinfo-free-text-secret"},
			call: func(client *OAuthClient) error {
				_, err := client.GetUserInfo(context.Background(), "userinfo-free-text-secret")
				return err
			},
		},
		{
			name:    "organization bearer",
			secrets: []string{"organization-free-text-secret"},
			call: func(client *OAuthClient) error {
				_, err := client.GetOrganizationTags(context.Background(), "organization-free-text-secret", "org-1")
				return err
			},
		},
		{
			name:    "PAT exchange body",
			secrets: []string{"pat-free-text-secret"},
			call: func(client *OAuthClient) error {
				_, err := client.ExchangeQoderCN20PAT(context.Background(), "pat-free-text-secret")
				return err
			},
		},
		{
			name:    "refresh body",
			secrets: []string{"refresh-free-text-secret"},
			call: func(client *OAuthClient) error {
				_, err := client.RefreshQoderCN20Token(context.Background(), "refresh-free-text-secret")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewOAuthClient("https://openapi.example", nil)
			client.Doer = func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"detail":"request values ` + strings.Join(tt.secrets, " ") + `"}`)),
					Request:    req,
				}, nil
			}

			err := tt.call(client)
			require.Error(t, err)
			require.Contains(t, err.Error(), "***")
			for _, secret := range tt.secrets {
				require.NotContains(t, err.Error(), secret)
			}
			var apiErr *OpenAPIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
		})
	}
}

func TestQoderDeviceAuthRequestAuthorizationURL(t *testing.T) {
	req := &DeviceAuthRequest{
		Nonce:         "nonce-1",
		CodeChallenge: "challenge-1",
		MachineID:     "machine-1",
		ClientID:      OAuthClientID,
	}

	rawURL := req.AuthorizationURL()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	require.Equal(t, DeviceAuthorizationURL, parsed.Scheme+"://"+parsed.Host+parsed.Path)

	values := parsed.Query()
	require.Equal(t, "nonce-1", values.Get("nonce"))
	require.Equal(t, "challenge-1", values.Get("challenge"))
	require.Equal(t, "S256", values.Get("challenge_method"))
	require.Equal(t, OAuthClientID, values.Get("client_id"))
	require.Equal(t, "machine-1", values.Get("machine_id"))
}

func TestQoderCNDeviceAuthRequestUsesCNProfileAndNonce(t *testing.T) {
	req, err := NewDeviceAuthRequestForSite(SiteCN)
	require.NoError(t, err)
	parsedNonce, err := uuid.Parse(req.Nonce)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsedNonce.Version())

	parsed, err := url.Parse(req.AuthorizationURL())
	require.NoError(t, err)
	require.Equal(t, CNDeviceAuthorizationURL, parsed.Scheme+"://"+parsed.Host+parsed.Path)
	require.Equal(t, CNOAuthClientID, parsed.Query().Get("client_id"))
	require.Empty(t, parsed.Query().Get("redirect_uri"))
	require.Equal(t, req.MachineID, parsed.Query().Get("machine_id"))
	require.Len(t, req.MachineID, 36)
	parsedMachineID, err := uuid.Parse(req.MachineID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsedMachineID.Version())
}

func TestExchangeQoderCN20PATUsesOpenAPIUserInfoWithoutGatewayStatus(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case JobTokenExchangePath:
			require.Equal(t, "qoder/"+CNClientVersion, r.Header.Get("User-Agent"))
			require.Equal(t, CNClientVersion, r.Header.Get("Cosy-Version"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "pat-cn", body["personal_token"])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":        "openapi-token",
				"device_token": "device-token",
				"access_token": "openapi-access",
			})
		case UserInfoPath:
			require.Equal(t, "Bearer openapi-token", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"uid":      "user-info",
				"username": "CN User",
				"organization": map[string]any{
					"id":   "org-cn",
					"name": "CN Org",
				},
			})
		case OrganizationTagsPathPrefix + "org-cn/tags":
			require.Equal(t, "Bearer openapi-token", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(OrganizationTags{Tags: []string{"Enterprise", "CN"}})
		case "/algo" + DataPolicyPath:
			require.Equal(t, "2", r.URL.Query().Get("version"))
			require.NotEmpty(t, r.URL.Query().Get("requestId"))
			require.Equal(t, "disagree", r.Header.Get("Cosy-Data-Policy"))
			require.Empty(t, r.Header.Get("Appcode"))
			require.Empty(t, r.Header.Get("Date"))
			require.Empty(t, r.Header.Get("Signature"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"status": "NO_RECORD"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	profile := MustProfileForSite(SiteCN)
	profile.OpenAPIBaseURL = server.URL
	profile.GatewayBaseURL = server.URL
	identity, expiresAt, err := ExchangeQoderCN20PATContext(context.Background(), "pat-cn", &MachineIdentity{
		MachineID:    "machine-id",
		MachineToken: "machine-token",
		MachineType:  "machine-type",
	}, profile, server.Client().Do)

	require.NoError(t, err)
	require.Equal(t, []string{JobTokenExchangePath, UserInfoPath, OrganizationTagsPathPrefix + "org-cn/tags", "/algo" + DataPolicyPath}, paths)
	require.Equal(t, "openapi-token", identity.SecurityOauthToken)
	require.Empty(t, identity.RefreshToken)
	require.Equal(t, "user-info", identity.UID)
	require.Equal(t, "user-info", identity.AID)
	require.Equal(t, "org-cn", identity.OrganizationID)
	require.Equal(t, []string{"Enterprise", "CN"}, identity.OrganizationTags)
	require.NotNil(t, identity.DataPolicyAgreed)
	require.True(t, *identity.DataPolicyAgreed)
	require.True(t, expiresAt.IsZero())
}

func TestQoderCN20PATErrorRedactsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"personal_token":"pat-secret","refresh_token":"refresh-secret","message":"invalid"}`)
	}))
	defer server.Close()
	profile := MustProfileForSite(SiteCN)
	profile.OpenAPIBaseURL = server.URL
	client := NewOAuthClientForProfile(profile, server.Client())

	_, err := client.ExchangeQoderCN20PAT(context.Background(), "pat-secret")
	require.Error(t, err)
	var apiErr *OpenAPIError
	require.ErrorAs(t, err, &apiErr)
	require.True(t, apiErr.InvalidCredentials())
	require.False(t, apiErr.UpstreamFailure())
	upstreamErr := &OpenAPIError{Operation: "PAT exchange", StatusCode: http.StatusServiceUnavailable}
	require.False(t, upstreamErr.InvalidCredentials())
	require.True(t, upstreamErr.UpstreamFailure())
	require.NotContains(t, err.Error(), "pat-secret")
	require.NotContains(t, err.Error(), "refresh-secret")
	require.True(t, strings.Contains(err.Error(), "status 401"))
}

func TestQoderCN20RefreshPostsRefreshTokenAndValidatesExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, DeviceTokenRefreshPath, r.URL.Path)
		require.Equal(t, "qoder/"+CNClientVersion, r.Header.Get("User-Agent"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "old-refresh", body["refresh_token"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_token":  "new-device-token",
			"refresh_token": "new-refresh",
		})
	}))
	defer server.Close()
	profile := MustProfileForSite(SiteCN)
	profile.OpenAPIBaseURL = server.URL
	client := NewOAuthClientForProfile(profile, server.Client())

	token, err := client.RefreshQoderCN20Token(context.Background(), "old-refresh")
	require.NoError(t, err)
	require.Equal(t, "new-device-token", token.DeviceRefreshTokenValue())
	require.Equal(t, "new-refresh", token.RefreshToken)
}

func TestValidateDeviceRefreshRequiresRotatedDeviceAndRefreshTokens(t *testing.T) {
	now := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name  string
		token *DeviceTokenResponse
		want  string
	}{
		{name: "empty response", want: "token response is empty"},
		{name: "missing device token", token: &DeviceTokenResponse{RefreshToken: "new-refresh"}, want: "device token is missing"},
		{name: "missing refresh token", token: &DeviceTokenResponse{DeviceToken: "new-device"}, want: "refresh token is missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.token.ValidateDeviceRefresh(now)
			require.ErrorContains(t, err, tt.want)
		})
	}

	expiresAt, err := (&DeviceTokenResponse{
		DeviceToken:  "new-device",
		RefreshToken: "new-refresh",
	}).ValidateDeviceRefresh(now)
	require.NoError(t, err)
	require.True(t, expiresAt.IsZero())
}

func TestDeviceTokenResponseNormalizesExpiryFormats(t *testing.T) {
	now := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		payload  string
		expected time.Time
	}{
		{
			name:     "RFC3339 日期",
			payload:  `{"expires_at":"2026-07-20T12:34:56+09:00"}`,
			expected: time.Date(2026, time.July, 20, 3, 34, 56, 0, time.UTC),
		},
		{
			name:     "无时区 ISO 日期按 UTC 解析",
			payload:  `{"expires_at":"2026-07-20T12:34:56.123"}`,
			expected: time.Date(2026, time.July, 20, 12, 34, 56, 0, time.UTC),
		},
		{
			name:     "Unix 秒字符串",
			payload:  `{"expires_at":"1784522096"}`,
			expected: time.Unix(1784522096, 0).UTC(),
		},
		{
			name:     "Unix 毫秒数字",
			payload:  `{"expires_at":1784522096000}`,
			expected: time.UnixMilli(1784522096000).UTC(),
		},
		{
			name:     "相对秒数",
			payload:  `{"expires_at":3600}`,
			expected: now.Add(time.Hour),
		},
		{
			name:     "无效 expires_at 回退数字字符串 expires_in",
			payload:  `{"expires_at":"not-a-date","expires_in":"7200"}`,
			expected: now.Add(2 * time.Hour),
		},
		{
			name:     "无效 expires_at 回退整数浮点 expires_in",
			payload:  `{"expires_at":"not-a-date","expires_in":3600.0}`,
			expected: now.Add(time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var token DeviceTokenResponse
			require.NoError(t, json.Unmarshal([]byte(tt.payload), &token))
			require.Equal(t, tt.expected, token.ExpiryTime(now))
		})
	}

	var token DeviceTokenResponse
	err := json.Unmarshal([]byte(`{"expires_at":"not-a-date"}`), &token)
	require.ErrorContains(t, err, "invalid expires_at")
}

func TestQoderOAuthClientUsesSiteUserAgent(t *testing.T) {
	for _, tt := range []struct {
		name string
		site Site
		want string
	}{
		{name: "国内站", site: SiteCN, want: "qoder/" + CNClientVersion},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.want, r.Header.Get("User-Agent"))
				switch r.URL.Path {
				case DevicePollPath:
					_ = json.NewEncoder(w).Encode(DeviceTokenResponse{Token: "access-token"})
				case UserInfoPath:
					_ = json.NewEncoder(w).Encode(UserInfo{ID: "user-1"})
				case OrganizationTagsPathPrefix + "org-1/tags":
					_ = json.NewEncoder(w).Encode(OrganizationTags{Tags: []string{"Normal"}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			profile := MustProfileForSite(tt.site)
			profile.OpenAPIBaseURL = server.URL
			client := NewOAuthClientForProfile(profile, server.Client())

			_, ready, err := client.PollDeviceToken(context.Background(), "nonce", "verifier")
			require.NoError(t, err)
			require.True(t, ready)
			_, err = client.GetUserInfo(context.Background(), "access-token")
			require.NoError(t, err)
			_, err = client.GetOrganizationTags(context.Background(), "access-token", "org-1")
			require.NoError(t, err)
		})
	}
}

func TestQoderGenerateCodeChallenge(t *testing.T) {
	require.Equal(t, "iMnq5o6zALKXGivsnlom_0F5_WYda32GHkxlV7mq7hQ", GenerateCodeChallenge("verifier"))
}

func TestQoderOAuthClientPollDeviceTokenPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, DevicePollPath, r.URL.Path)
		require.Equal(t, "nonce-1", r.URL.Query().Get("nonce"))
		require.Equal(t, "verifier-1", r.URL.Query().Get("verifier"))
		require.Equal(t, "S256", r.URL.Query().Get("challenge_method"))
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := NewOAuthClient(server.URL, server.Client())
	token, ready, err := client.PollDeviceToken(context.Background(), "nonce-1", "verifier-1")
	require.NoError(t, err)
	require.False(t, ready)
	require.Nil(t, token)
}

func TestQoderOAuthClientPollDeviceTokenCompletedAndUserInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case DevicePollPath:
			require.Equal(t, "nonce-2", r.URL.Query().Get("nonce"))
			require.Equal(t, "verifier-2", r.URL.Query().Get("verifier"))
			_ = json.NewEncoder(w).Encode(DeviceTokenResponse{
				Token:        "legacy-token",
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				UserID:       "user-from-token",
			})
		case UserInfoPath:
			require.Equal(t, "Bearer legacy-token", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(UserInfo{
				ID:             "user-from-info",
				Name:           "Qoder User",
				UserType:       "personal_pro",
				OrganizationID: "org-1",
			})
		case OrganizationTagsPathPrefix + "org-1/tags":
			require.Equal(t, "Bearer legacy-token", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(OrganizationTags{Tags: []string{"Enterprise"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewOAuthClient(server.URL, server.Client())
	token, ready, err := client.PollDeviceToken(context.Background(), "nonce-2", "verifier-2")
	require.NoError(t, err)
	require.True(t, ready)
	require.Equal(t, "legacy-token", token.DeviceLoginTokenValue())
	require.Equal(t, "refresh-token", token.RefreshToken)

	user, err := client.GetUserInfo(context.Background(), token.DeviceLoginTokenValue())
	require.NoError(t, err)
	require.Equal(t, "user-from-info", user.ID)
	require.Equal(t, "Qoder User", user.Name)
	require.Equal(t, "personal_pro", user.UserType)

	tags, err := client.GetOrganizationTags(context.Background(), token.DeviceLoginTokenValue(), user.OrganizationID)
	require.NoError(t, err)
	require.Equal(t, []string{"Enterprise"}, tags.Tags)
}

func TestDeviceTokenResponseUsesEndpointSpecificTokenPriority(t *testing.T) {
	token := &DeviceTokenResponse{
		Token:       "token",
		DeviceToken: "device-token",
		AccessToken: "access-token",
	}

	require.Equal(t, "token", token.PATTokenValue())
	require.Equal(t, "token", token.DeviceLoginTokenValue())
	require.Equal(t, "device-token", token.DeviceRefreshTokenValue())
	token.Token = ""
	require.Equal(t, "device-token", token.PATTokenValue())
	require.Empty(t, token.DeviceLoginTokenValue())
	token.DeviceToken = ""
	require.Equal(t, "access-token", token.PATTokenValue())
}

func TestQoderOAuthClientRedactsSensitiveErrorBodies(t *testing.T) {
	sensitiveBody := `{"message":"failed","token":"cn-access-secret","securityOauthToken":"sec-secret","refresh_token":"rt-secret","uid":"uid-secret","cookie":"sid=secret"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, sensitiveBody, http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewOAuthClient(server.URL, server.Client())

	_, _, err := client.PollDeviceToken(context.Background(), "nonce", "verifier")
	require.Error(t, err)
	assertQoderOAuthErrorRedacted(t, err.Error())

	_, err = client.GetUserInfo(context.Background(), "sec-token")
	require.Error(t, err)
	assertQoderOAuthErrorRedacted(t, err.Error())

	_, err = client.GetOrganizationTags(context.Background(), "sec-token", "uid-1")
	require.Error(t, err)
	assertQoderOAuthErrorRedacted(t, err.Error())
}

func TestUserInfoUnmarshalSupportsQoderCNAliases(t *testing.T) {
	for _, tt := range []struct {
		name              string
		payload           string
		wantID            string
		wantName          string
		wantOrgID         string
		wantOrgName       string
		wantAvatar        string
		wantTags          []string
		wantModifiable    bool
		wantModifiableSet bool
	}{
		{
			name:              "top-level camel aliases",
			payload:           `{"id":"user-id","username":"camel-user","orgId":"org-camel","orgName":"Camel Org","avatar":"avatar-camel","organizationTags":[" Enterprise ","CN","Enterprise"],"isPrivacyPolicyModifiable":false,"is_data_policy_modifiable":true}`,
			wantID:            "user-id",
			wantName:          "camel-user",
			wantOrgID:         "org-camel",
			wantOrgName:       "Camel Org",
			wantAvatar:        "avatar-camel",
			wantTags:          []string{"Enterprise", "CN"},
			wantModifiableSet: true,
		},
		{
			name:              "nested snake aliases",
			payload:           `{"user_id":"user-snake","user_name":"snake-user","avatar_url":"avatar-snake","organization":{"org_id":"org-snake","org_name":"Snake Org","organization_tags":["Normal"]},"is_data_policy_modifiable":true}`,
			wantID:            "user-snake",
			wantName:          "snake-user",
			wantOrgID:         "org-snake",
			wantOrgName:       "Snake Org",
			wantAvatar:        "avatar-snake",
			wantTags:          []string{"Normal"},
			wantModifiable:    true,
			wantModifiableSet: true,
		},
		{
			name:        "uid and nested camel aliases",
			payload:     `{"uid":"user-uid","name":"named-user","avatarUrl":"avatar-url","organization":{"orgId":"org-nested","orgName":"Nested Org","organizationTags":["Team"]}}`,
			wantID:      "user-uid",
			wantName:    "named-user",
			wantOrgID:   "org-nested",
			wantOrgName: "Nested Org",
			wantAvatar:  "avatar-url",
			wantTags:    []string{"Team"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var info UserInfo
			require.NoError(t, json.Unmarshal([]byte(tt.payload), &info))
			require.Equal(t, tt.wantID, userIDFromInfo(&info))
			require.Equal(t, tt.wantName, info.Name)
			require.Equal(t, tt.wantOrgID, info.OrganizationID)
			require.Equal(t, tt.wantOrgName, info.OrganizationName)
			require.Equal(t, tt.wantAvatar, info.AvatarURL)
			require.Equal(t, tt.wantTags, info.OrganizationTags)
			if tt.wantModifiableSet {
				require.NotNil(t, info.IsDataPolicyModifiable)
				require.Equal(t, tt.wantModifiable, *info.IsDataPolicyModifiable)
			} else {
				require.Nil(t, info.IsDataPolicyModifiable)
			}
		})
	}
}

func TestBuildIdentityFromDeviceToken(t *testing.T) {
	identity := BuildIdentityFromDeviceToken(&UserInfo{
		ID:       "user-1",
		Name:     "Qoder User",
		UserType: "personal_pro",
	}, &DeviceTokenResponse{
		Token:        "token-1",
		RefreshToken: "refresh-1",
		UserID:       "user-1",
	})

	require.Equal(t, "Qoder User", identity.Name)
	require.Equal(t, "user-1", identity.UID)
	require.Equal(t, "user-1", identity.AID)
	require.Equal(t, "personal_pro", identity.UserType)
	require.Equal(t, "token-1", identity.SecurityOauthToken)
	require.Equal(t, "refresh-1", identity.RefreshToken)
}

func TestBuildIdentityFromDeviceTokenIgnoresMismatchedUserInfo(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	identity := BuildIdentityFromDeviceToken(&UserInfo{
		ID:               "userinfo-user",
		Name:             "Wrong User",
		UserType:         "enterprise_standard",
		OrganizationID:   "wrong-org",
		OrganizationName: "Wrong Org",
		OrganizationTags: []string{"Wrong"},
	}, &DeviceTokenResponse{
		Token:        "device-token",
		RefreshToken: "refresh-token",
		UserID:       "poll-user",
		UserName:     "Poll User",
	})

	require.Equal(t, "poll-user", identity.UID)
	require.Equal(t, "poll-user", identity.AID)
	require.Equal(t, "Poll User", identity.Name)
	require.Equal(t, "personal_standard", identity.UserType)
	require.Empty(t, identity.OrganizationID)
	require.Empty(t, identity.OrganizationName)
	require.Empty(t, identity.OrganizationTags)
	require.Contains(t, logs.String(), "ignored mismatched userinfo enrichment")
	require.NotContains(t, logs.String(), "poll-user")
	require.NotContains(t, logs.String(), "userinfo-user")
}

func assertQoderOAuthErrorRedacted(t *testing.T, errText string) {
	t.Helper()
	require.Contains(t, errText, "status 500")
	require.NotContains(t, errText, "sec-secret")
	require.NotContains(t, errText, "cn-access-secret")
	require.NotContains(t, errText, "rt-secret")
	require.NotContains(t, errText, "uid-secret")
	require.NotContains(t, errText, "sid=secret")
	require.Contains(t, errText, "***")
}

func TestBuildIdentityFromDeviceTokenCopiesOrganizationFromUserInfo(t *testing.T) {
	identity := BuildIdentityFromDeviceToken(&UserInfo{
		ID:               "user-1",
		OrganizationID:   "org-1",
		OrganizationName: "Org 1",
		OrganizationTags: []string{" Enterprise ", "CN", "Enterprise"},
	}, &DeviceTokenResponse{
		Token: "token-1",
	})

	require.Equal(t, "org-1", identity.OrganizationID)
	require.Equal(t, "Org 1", identity.OrganizationName)
	require.Equal(t, []string{"Enterprise", "CN"}, identity.OrganizationTags)
}
