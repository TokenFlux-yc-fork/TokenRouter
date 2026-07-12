package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func newCodexModelsTestAccount() *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "test-access-token",
			"chatgpt_account_id": "acc-123",
		},
	}
}

// codexModelsSchedulerCache 模拟候选快照不含敏感凭据、单账号快照包含完整凭据的生产结构。
type codexModelsSchedulerCache struct {
	SchedulerCache
	snapshot []*Account
	accounts map[int64]*Account
}

func (c *codexModelsSchedulerCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return c.snapshot, true, nil
}

func (c *codexModelsSchedulerCache) GetAccount(_ context.Context, accountID int64) (*Account, error) {
	return c.accounts[accountID], nil
}

// codexModelsTokenCache 模拟凭据中无明文 token、TokenProvider 缓存中存在有效 token 的场景。
type codexModelsTokenCache struct {
	OpenAITokenCache
	token string
}

func (c *codexModelsTokenCache) GetAccessToken(context.Context, string) (string, error) {
	return c.token, nil
}

// 混合分组中只有具备 access token 的 OAuth 账号可用于模型清单请求。
func TestSelectCodexModelsAccountSkipsNonOAuthAndMissingTokenAccounts(t *testing.T) {
	groupID := int64(1)
	repo := stubOpenAIAccountRepo{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    0,
				Credentials: map[string]any{
					"api_key":      "test-api-key",
					"access_token": "stale-token-field",
				},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    1,
			},
			{
				ID:          3,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Priority:    2,
				Credentials: map[string]any{
					"access_token": "oauth-access-token",
				},
			},
		},
	}

	svc := &OpenAIGatewayService{accountRepo: repo}
	account, err := svc.SelectCodexModelsAccount(context.Background(), &groupID)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(3), account.ID)
}

// 轻量快照的类型和凭据均可能滞后，筛选必须以补全后的账号为准。
func TestSelectCodexModelsAccountUsesHydratedSchedulerAccountTypeAndToken(t *testing.T) {
	groupID := int64(2)
	apiKeyAccount := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    0,
		Credentials: map[string]any{"api_key": "test-api-key"},
	}
	oauthAccount := Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    1,
		Credentials: map[string]any{"access_token": "oauth-access-token"},
	}
	apiKeyMetadata := apiKeyAccount
	apiKeyMetadata.Type = AccountTypeOAuth
	apiKeyMetadata.Credentials = nil
	oauthMetadata := oauthAccount
	oauthMetadata.Type = AccountTypeAPIKey
	oauthMetadata.Credentials = nil

	repo := stubOpenAIAccountRepo{accounts: []Account{apiKeyAccount, oauthAccount}}
	cache := &codexModelsSchedulerCache{
		snapshot: []*Account{&apiKeyMetadata, &oauthMetadata},
		accounts: map[int64]*Account{
			apiKeyAccount.ID: &apiKeyAccount,
			oauthAccount.ID:  &oauthAccount,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:       repo,
		schedulerSnapshot: NewSchedulerSnapshotService(cache, nil, repo, nil, nil),
	}

	account, err := svc.SelectCodexModelsAccount(context.Background(), &groupID)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, oauthAccount.ID, account.ID)
}

// TokenProvider 能提供有效缓存 token 时，不应要求账号快照携带明文 token。
func TestSelectCodexModelsAccountUsesTokenProviderCache(t *testing.T) {
	groupID := int64(4)
	oauthAccount := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	repo := stubOpenAIAccountRepo{accounts: []Account{oauthAccount}}
	tokenProvider := NewOpenAITokenProvider(repo, &codexModelsTokenCache{token: "cached-access-token"}, nil)
	svc := &OpenAIGatewayService{
		accountRepo:         repo,
		openAITokenProvider: tokenProvider,
	}

	account, err := svc.SelectCodexModelsAccount(context.Background(), &groupID)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, oauthAccount.ID, account.ID)
}

// 影子账号应通过母账号的 OAuth 凭据参与模型清单调度。
func TestSelectCodexModelsAccountUsesResolvedShadowCredentials(t *testing.T) {
	groupID := int64(7)
	parentID := int64(10)
	repo := groupAwareStubOpenAIAccountRepo{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{
			accounts: []Account{
				{
					ID:          parentID,
					Platform:    PlatformOpenAI,
					Type:        AccountTypeOAuth,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{
						"access_token": "parent-access-token",
					},
				},
				{
					ID:              11,
					Platform:        PlatformOpenAI,
					Type:            AccountTypeOAuth,
					Status:          StatusActive,
					Schedulable:     true,
					Concurrency:     1,
					ParentAccountID: &parentID,
					AccountGroups:   []AccountGroup{{GroupID: groupID}},
				},
			},
		},
	}

	svc := &OpenAIGatewayService{accountRepo: repo}
	account, err := svc.SelectCodexModelsAccount(context.Background(), &groupID)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(11), account.ID)
}

// 分组内没有可用 OAuth access token 时应在请求上游前明确失败。
func TestSelectCodexModelsAccountReturnsErrorWithoutEligibleOAuthAccount(t *testing.T) {
	groupID := int64(1)
	repo := stubOpenAIAccountRepo{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-api-key"},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
			},
		},
	}

	svc := &OpenAIGatewayService{accountRepo: repo}
	account, err := svc.SelectCodexModelsAccount(context.Background(), &groupID)
	require.Error(t, err)
	require.Nil(t, account)
}

func withCodexModelsTestServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	original := chatgptCodexModelsURL
	chatgptCodexModelsURL = server.URL
	t.Cleanup(func() { chatgptCodexModelsURL = original })
}

func TestFetchCodexModelsManifestPassthrough(t *testing.T) {
	manifestBody := `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5"}]}`
	var gotAuth, gotAccountID, gotOriginator, gotClientVersion, gotVersion string

	withCodexModelsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("chatgpt-account-id")
		gotOriginator = r.Header.Get("Originator")
		gotClientVersion = r.URL.Query().Get("client_version")
		gotVersion = r.Header.Get("Version")
		w.Header().Set("ETag", `W/"abc123"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(manifestBody))
	}))

	svc := &OpenAIGatewayService{}
	manifest, err := svc.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	require.NoError(t, err)
	require.Equal(t, manifestBody, string(manifest.Body))
	require.Equal(t, `W/"abc123"`, manifest.ETag)
	require.Equal(t, "Bearer test-access-token", gotAuth)
	require.Equal(t, "acc-123", gotAccountID)
	require.Equal(t, "codex_cli_rs", gotOriginator)
	require.Equal(t, "0.137.0", gotClientVersion)
	require.Equal(t, "0.137.0", gotVersion)
}

func TestFetchCodexModelsManifestDefaultClientVersion(t *testing.T) {
	var gotClientVersion string
	withCodexModelsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClientVersion = r.URL.Query().Get("client_version")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))

	svc := &OpenAIGatewayService{}
	_, err := svc.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "", "")
	require.NoError(t, err)
	require.Equal(t, openAICodexProbeVersion, gotClientVersion)
}

func TestFetchCodexModelsManifestNotModified(t *testing.T) {
	var gotIfNoneMatch string
	withCodexModelsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `W/"abc123"`)
		w.WriteHeader(http.StatusNotModified)
	}))

	svc := &OpenAIGatewayService{}
	manifest, err := svc.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", `W/"abc123"`)
	require.NoError(t, err)
	require.True(t, manifest.NotModified)
	require.Equal(t, `W/"abc123"`, manifest.ETag)
	require.Equal(t, `W/"abc123"`, gotIfNoneMatch)
}

func TestFetchCodexModelsManifestRejectsUpstreamError(t *testing.T) {
	withCodexModelsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"boom"}`, http.StatusInternalServerError)
	}))

	svc := &OpenAIGatewayService{}
	_, err := svc.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
}

func TestFetchCodexModelsManifestRejectsMissingToken(t *testing.T) {
	account := newCodexModelsTestAccount()
	delete(account.Credentials, "access_token")

	svc := &OpenAIGatewayService{}
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
}

func TestFetchCodexModelsManifestRejectsAPIKeyAccount(t *testing.T) {
	account := newCodexModelsTestAccount()
	account.Type = AccountTypeAPIKey
	account.Credentials = map[string]any{"api_key": "sk-test"}

	svc := &OpenAIGatewayService{}
	_, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.137.0", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	require.Equal(t, "OPENAI_CODEX_MODELS_ACCOUNT_UNSUPPORTED", infraerrors.Reason(err))
}

func TestFetchCodexModelsManifestRejectsOversizedResponse(t *testing.T) {
	withCodexModelsTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), int(codexModelsManifestBodyLimit+1)))
	}))

	svc := &OpenAIGatewayService{}
	_, err := svc.FetchCodexModelsManifest(context.Background(), newCodexModelsTestAccount(), "0.137.0", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, infraerrors.Code(err))
	require.Contains(t, err.Error(), ErrUpstreamResponseBodyTooLarge.Error())
}
