package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeQoderOAuthClient struct {
	token             *qoder.DeviceTokenResponse
	ready             bool
	pollErr           error
	pollCalls         int
	userInfo          *qoder.UserInfo
	userErr           error
	orgTags           *qoder.OrganizationTags
	orgErr            error
	orgCalls          int
	gotOrgUID         string
	dataPolicy        *bool
	dataPolicyErr     error
	dataPolicyCalls   int
	gotNonce          string
	gotVerifier       string
	completedIdentity *qoder.AuthIdentity
	completedExpiry   time.Time
	completionErr     error
	completionCalls   int
	completedMachine  *qoder.MachineIdentity
}

type blockingQoderOAuthClient struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	pollCalls atomic.Int32
}

func (f *fakeQoderOAuthClient) PollDeviceToken(ctx context.Context, nonce, verifier string) (*qoder.DeviceTokenResponse, bool, error) {
	f.pollCalls++
	f.gotNonce = nonce
	f.gotVerifier = verifier
	return f.token, f.ready, f.pollErr
}

func (f *fakeQoderOAuthClient) GetUserInfo(ctx context.Context, token string) (*qoder.UserInfo, error) {
	if f.userErr != nil {
		return nil, f.userErr
	}
	return f.userInfo, nil
}

func (f *fakeQoderOAuthClient) GetOrganizationTags(ctx context.Context, token, uid string) (*qoder.OrganizationTags, error) {
	f.orgCalls++
	f.gotOrgUID = uid
	if f.orgErr != nil {
		return nil, f.orgErr
	}
	return f.orgTags, nil
}

func (f *fakeQoderOAuthClient) GetDataPolicy(context.Context, *qoder.AuthIdentity, *qoder.MachineIdentity) (bool, error) {
	f.dataPolicyCalls++
	if f.dataPolicyErr != nil {
		return false, f.dataPolicyErr
	}
	if f.dataPolicy == nil {
		return false, errors.New("data policy unavailable")
	}
	return *f.dataPolicy, nil
}

func (f *fakeQoderOAuthClient) CompleteQoderCN20Identity(
	_ context.Context,
	_ *qoder.DeviceTokenResponse,
	_ *qoder.UserInfo,
	machine *qoder.MachineIdentity,
) (*qoder.AuthIdentity, time.Time, error) {
	f.completionCalls++
	if machine != nil {
		f.completedMachine = &qoder.MachineIdentity{
			MachineID:    machine.MachineID,
			MachineToken: machine.MachineToken,
			MachineType:  machine.MachineType,
		}
	}
	return f.completedIdentity, f.completedExpiry, f.completionErr
}

func (f *blockingQoderOAuthClient) PollDeviceToken(ctx context.Context, nonce, verifier string) (*qoder.DeviceTokenResponse, bool, error) {
	f.pollCalls.Add(1)
	f.startOnce.Do(func() { close(f.started) })
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-f.release:
		return &qoder.DeviceTokenResponse{
			Token:        "access-token",
			RefreshToken: "refresh-token",
			UserID:       "user-1",
			ExpiresIn:    3600,
		}, true, nil
	}
}

func (f *blockingQoderOAuthClient) GetUserInfo(ctx context.Context, token string) (*qoder.UserInfo, error) {
	return &qoder.UserInfo{ID: "user-1", Name: "Qoder User"}, nil
}

func (f *blockingQoderOAuthClient) GetOrganizationTags(ctx context.Context, token, uid string) (*qoder.OrganizationTags, error) {
	return nil, nil
}

func (f *blockingQoderOAuthClient) GetDataPolicy(context.Context, *qoder.AuthIdentity, *qoder.MachineIdentity) (bool, error) {
	return false, errors.New("data policy unavailable")
}

func (f *blockingQoderOAuthClient) CompleteQoderCN20Identity(
	_ context.Context,
	token *qoder.DeviceTokenResponse,
	user *qoder.UserInfo,
	_ *qoder.MachineIdentity,
) (*qoder.AuthIdentity, time.Time, error) {
	return &qoder.AuthIdentity{
		Name:               user.Name,
		UID:                token.UserID,
		AID:                token.UserID,
		SecurityOauthToken: token.AccessTokenValue(),
		RefreshToken:       token.RefreshToken,
	}, token.ExpiryTime(time.Now()), nil
}

func beginQoderOAuthCompletionWithWaiter(t *testing.T, store *qoderOAuthSessionStore, sessionID string) <-chan struct{} {
	t.Helper()
	require.True(t, store.Set(sessionID, &qoderOAuthSession{
		State:     "state",
		CreatedAt: time.Now(),
	}))

	session, cached, waitCh, err := store.BeginCompletion(sessionID, "state")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Nil(t, cached)
	require.Nil(t, waitCh)

	session, cached, waitCh, err = store.BeginCompletion(sessionID, "state")
	require.NoError(t, err)
	require.Nil(t, session)
	require.Nil(t, cached)
	require.NotNil(t, waitCh)
	return waitCh
}

func requireQoderOAuthWaiterReleased(t *testing.T, waitCh <-chan struct{}) {
	t.Helper()
	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("qoder oauth completion waiter was not released")
	}
}

func TestQoderOAuthSessionStoreBeginCompletionExpirationReleasesWaiter(t *testing.T) {
	store := newQoderOAuthSessionStore()
	defer store.Stop()
	waitCh := beginQoderOAuthCompletionWithWaiter(t, store, "expired-on-begin")

	store.mu.Lock()
	store.sessions["expired-on-begin"].CreatedAt = time.Now().Add(-qoderOAuthSessionTTL - time.Second)
	store.mu.Unlock()

	_, _, _, err := store.BeginCompletion("expired-on-begin", "state")
	require.ErrorContains(t, err, "session not found or expired")
	requireQoderOAuthWaiterReleased(t, waitCh)
	_, ok := store.Get("expired-on-begin")
	require.False(t, ok)
	require.NotPanics(t, func() { store.FinishCompletion("expired-on-begin", nil) })
}

func TestQoderOAuthSessionStoreCleanupExpirationReleasesWaiter(t *testing.T) {
	store := newQoderOAuthSessionStore()
	defer store.Stop()
	waitCh := beginQoderOAuthCompletionWithWaiter(t, store, "expired-on-cleanup")

	store.cleanupExpired(time.Now().Add(qoderOAuthSessionTTL + time.Second))

	requireQoderOAuthWaiterReleased(t, waitCh)
	_, ok := store.Get("expired-on-cleanup")
	require.False(t, ok)
}

func TestQoderOAuthSessionStoreStopReleasesWaiterAndRejectsNewSessions(t *testing.T) {
	store := newQoderOAuthSessionStore()
	waitCh := beginQoderOAuthCompletionWithWaiter(t, store, "stopped")

	store.Stop()

	requireQoderOAuthWaiterReleased(t, waitCh)
	_, ok := store.Get("stopped")
	require.False(t, ok)
	require.False(t, store.Set("after-stop", &qoderOAuthSession{CreatedAt: time.Now()}))
	_, _, _, err := store.BeginCompletion("stopped", "state")
	require.ErrorContains(t, err, "session not found or expired")
}

func TestQoderOAuthSessionStoreStopIsConcurrentSafe(t *testing.T) {
	store := newQoderOAuthSessionStore()
	waitCh := beginQoderOAuthCompletionWithWaiter(t, store, "concurrent-stop")
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Stop()
		}()
	}

	require.NotPanics(t, wg.Wait)
	requireQoderOAuthWaiterReleased(t, waitCh)
}

func TestQoderOAuthSessionStoreConcurrentTeardownClosesWaiterOnce(t *testing.T) {
	for i := 0; i < 50; i++ {
		store := newQoderOAuthSessionStore()
		waitCh := beginQoderOAuthCompletionWithWaiter(t, store, "concurrent-teardown")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			store.FinishCompletion("concurrent-teardown", &QoderTokenInfo{SecurityOauthToken: "token"})
		}()
		go func() {
			defer wg.Done()
			<-start
			store.cleanupExpired(time.Now().Add(qoderOAuthSessionTTL + time.Second))
		}()
		go func() {
			defer wg.Done()
			<-start
			store.Stop()
		}()

		close(start)
		require.NotPanics(t, wg.Wait)
		requireQoderOAuthWaiterReleased(t, waitCh)
		store.Stop()
	}
}

func TestQoderOAuthServiceGenerateAuthURLCreatesSession(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, result.AuthURL)
	require.NotEmpty(t, result.SessionID)
	require.NotEmpty(t, result.State)
	require.Positive(t, result.ExpiresIn)
	require.Equal(t, qoderOAuthPollInterval, result.Interval)
	require.Equal(t, "cn", result.Site)

	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Equal(t, result.State, session.State)
	require.NotNil(t, session.Machine)
	require.Contains(t, result.AuthURL, "nonce="+session.Nonce)
	require.Contains(t, result.AuthURL, "challenge=")
	require.Contains(t, result.AuthURL, "client_id="+qoder.OAuthClientID)
	authURL, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, session.Machine.MachineID, authURL.Query().Get("machine_id"))
}

func TestQoderOAuthServiceCNFreezesSiteAndIgnoresPollProxyOverride(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	dataPolicyAgreed := true
	client := &fakeQoderOAuthClient{
		ready: true,
		token: &qoder.DeviceTokenResponse{
			Token:        "openapi-access",
			RefreshToken: "openapi-refresh",
			UserID:       "user-1",
			ExpiresIn:    3600,
		},
		userInfo: &qoder.UserInfo{
			UserID:           "user-1",
			UserName:         "CN User",
			OrganizationID:   "status-org",
			OrganizationName: "Status Org",
		},
		completedIdentity: &qoder.AuthIdentity{
			UID:                "cosy-uid",
			AID:                "cosy-aid",
			OrganizationID:     "status-org",
			OrganizationName:   "Status Org",
			SecurityOauthToken: "cosy-token",
			RefreshToken:       "openapi-refresh",
			UserType:           "personal_standard",
		},
		orgTags: &qoder.OrganizationTags{
			Tags: []string{"Enterprise", "CN"},
		},
		dataPolicy: &dataPolicyAgreed,
	}
	var capturedProfile qoder.Profile
	var capturedProxy string
	svc.clientFactory = func(profile qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		capturedProfile = profile
		capturedProxy = proxyURL
		return client, nil
	}

	result, err := svc.GenerateAuthURLForSite(context.Background(), qoder.SiteCN, nil)
	require.NoError(t, err)
	require.Equal(t, "cn", result.Site)
	parsed, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, "qoder.com.cn", parsed.Host)
	require.Equal(t, qoder.CNOAuthClientID, parsed.Query().Get("client_id"))
	_, err = uuid.Parse(parsed.Query().Get("nonce"))
	require.NoError(t, err)
	require.Len(t, parsed.Query().Get("machine_id"), 36)
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Equal(t, session.Machine.MachineID, session.Machine.MachineToken)
	require.Equal(t, "5", session.Machine.MachineType)

	ignoredProxyID := int64(9999)
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, &ignoredProxyID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, qoder.SiteCN, capturedProfile.Site)
	require.Empty(t, capturedProxy)
	require.Zero(t, client.completionCalls)
	require.Equal(t, "openapi-access", completed.TokenInfo.SecurityOauthToken)
	require.Equal(t, "cn", completed.TokenInfo.Site)
	require.Equal(t, qoder.RefreshModeQoderCN20, completed.TokenInfo.RefreshMode)
	require.NotEmpty(t, completed.TokenInfo.ExpiresAt)
	require.Equal(t, "status-org", completed.TokenInfo.OrganizationID)
	require.Equal(t, "Status Org", completed.TokenInfo.OrganizationName)
	require.Equal(t, []string{"Enterprise", "CN"}, completed.TokenInfo.OrganizationTags)
	require.Equal(t, "agree", completed.TokenInfo.DataPolicy)
	require.Equal(t, 1, client.orgCalls)
	require.Equal(t, 1, client.dataPolicyCalls)
	require.Equal(t, "status-org", client.gotOrgUID)
	require.Nil(t, client.completedMachine)
	require.Equal(t, completed.TokenInfo.MachineID, completed.TokenInfo.MachineToken)
	require.Equal(t, "5", completed.TokenInfo.MachineType)

	credentials := svc.BuildAccountCredentials(completed.TokenInfo)
	require.Equal(t, "cn", credentials["site"])
	require.Equal(t, qoder.RefreshModeQoderCN20, credentials["refresh_mode"])
	require.Equal(t, completed.TokenInfo.ExpiresAt, credentials["expires_at"])
	require.Equal(t, []string{"Enterprise", "CN"}, credentials["organization_tags"])
	require.Equal(t, "agree", credentials["data_policy"])
	require.Equal(t, credentials["machine_id"], credentials["machine_token"])
	require.Equal(t, "5", credentials["machine_type"])
}

func TestQoderOAuthServiceCNLoadsTagsForUserInfoOrganization(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	client := &fakeQoderOAuthClient{
		ready: true,
		token: &qoder.DeviceTokenResponse{
			Token:        "openapi-access",
			RefreshToken: "openapi-refresh",
			UserID:       "user-1",
			ExpiresIn:    3600,
		},
		userInfo: &qoder.UserInfo{
			UserID:           "user-1",
			UserName:         "CN User",
			OrganizationID:   "org-1",
			OrganizationName: "Org 1",
		},
		completedIdentity: &qoder.AuthIdentity{
			UID:                "cosy-uid",
			AID:                "cosy-aid",
			SecurityOauthToken: "cosy-token",
			RefreshToken:       "openapi-refresh",
		},
		completedExpiry: time.Now().Add(time.Hour),
		orgTags: &qoder.OrganizationTags{
			Tags: []string{"Enterprise"},
		},
	}
	svc.clientFactory = func(_ qoder.Profile, _ string) (qoderOAuthClient, error) {
		return client, nil
	}

	result, err := svc.GenerateAuthURLForSite(context.Background(), qoder.SiteCN, nil)
	require.NoError(t, err)
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)
	require.NoError(t, err)
	require.Equal(t, "org-1", completed.TokenInfo.OrganizationID)
	require.Equal(t, "Org 1", completed.TokenInfo.OrganizationName)
	require.Equal(t, []string{"Enterprise"}, completed.TokenInfo.OrganizationTags)
	require.Equal(t, 1, client.orgCalls)
	require.Equal(t, "org-1", client.gotOrgUID)
}

func TestQoderOAuthServiceExchangeRejectsInvalidSessionState(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()

	_, err := svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
		SessionID: "missing",
		State:     "state",
	})
	require.ErrorContains(t, err, "session not found")

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	_, err = svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
		SessionID: result.SessionID,
		State:     "wrong-state",
	})
	require.ErrorContains(t, err, "state is invalid")
}

func TestQoderOAuthServiceExchangePendingKeepsSession(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	client := &fakeQoderOAuthClient{ready: false}
	svc.clientFactory = func(_ qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		return client, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	_, err = svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
		SessionID: result.SessionID,
		State:     result.State,
		Code:      "completed",
	})
	require.ErrorContains(t, err, "still pending")

	_, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok, "pending authorization should keep the session available")
	session, _ := svc.sessionStore.Get(result.SessionID)
	require.Equal(t, session.Nonce, client.gotNonce)
	require.Equal(t, session.CodeVerifier, client.gotVerifier)
}

func TestQoderOAuthServiceExchangeParsesCallbackURLAndBuildsUsableCredentials(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	expiresAt := time.Unix(1_893_456_000, 0).UTC()
	svc.clientFactory = func(_ qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		return &fakeQoderOAuthClient{
			ready: true,
			token: &qoder.DeviceTokenResponse{
				Token:        "security-token",
				RefreshToken: "refresh-token",
				UserID:       "user-from-token",
				ExpiresAt:    qoder.FlexibleInt64(expiresAt.Unix()),
			},
			userInfo: &qoder.UserInfo{
				ID:               "user-from-token",
				Name:             "Qoder User",
				UserType:         "personal_pro",
				OrganizationID:   "org-from-info",
				OrganizationName: "Qoder Org",
			},
			completedIdentity: &qoder.AuthIdentity{
				Name:               "Qoder User",
				UID:                "user-from-token",
				AID:                "user-from-token",
				SecurityOauthToken: "security-token",
				RefreshToken:       "refresh-token",
				UserType:           "personal_pro",
			},
			completedExpiry: expiresAt,
			orgTags: &qoder.OrganizationTags{
				Tags: []string{"Enterprise"},
			},
		}, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	authURL, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	authMachineID := authURL.Query().Get("machine_id")
	require.NotEmpty(t, authMachineID)
	tokenInfo, err := svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
		SessionID:   result.SessionID,
		CallbackURL: "http://localhost:12345/callback?code=ignored-by-device-flow&state=" + result.State,
	})
	require.NoError(t, err)
	require.Equal(t, "security-token", tokenInfo.SecurityOauthToken)
	require.Equal(t, "refresh-token", tokenInfo.RefreshToken)
	require.Equal(t, "user-from-token", tokenInfo.UID)
	require.Equal(t, "user-from-token", tokenInfo.AID)
	require.Equal(t, "org-from-info", tokenInfo.OrganizationID)
	require.Equal(t, "Qoder Org", tokenInfo.OrganizationName)
	require.Equal(t, "Qoder User", tokenInfo.Name)
	require.Equal(t, "personal_pro", tokenInfo.UserType)
	require.Equal(t, []string{"Enterprise"}, tokenInfo.OrganizationTags)
	require.Equal(t, "cn", tokenInfo.Site)
	require.Equal(t, qoder.RefreshModeQoderCN20, tokenInfo.RefreshMode)
	require.Empty(t, tokenInfo.DataPolicy)
	require.Equal(t, expiresAt.Format(time.RFC3339), tokenInfo.ExpiresAt)
	require.Equal(t, authMachineID, tokenInfo.MachineID)
	require.NotEmpty(t, tokenInfo.MachineToken)
	require.NotEmpty(t, tokenInfo.MachineType)

	sessionAfterComplete, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok, "completed authorization should remain available for idempotent retry")
	require.NotNil(t, sessionAfterComplete.CompletedTokenInfo)

	credentials := svc.BuildAccountCredentials(tokenInfo)
	require.Equal(t, "security-token", credentials["security_oauth_token"])
	require.Equal(t, tokenInfo.MachineID, credentials["machine_id"])
	require.Equal(t, "org-from-info", credentials["organization_id"])
	require.Equal(t, "Qoder Org", credentials["organization_name"])
	require.Equal(t, expiresAt.Format(time.RFC3339), credentials["expires_at"])
	require.NotContains(t, credentials, "data_policy")

	provider := NewQoderTokenProvider()
	session, err := provider.GetSession(context.Background(), &Account{
		ID:          991,
		Name:        "qoder-oauth",
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Credentials: credentials,
	})
	require.NoError(t, err)
	require.Equal(t, "security-token", session.Identity.SecurityOauthToken)
	require.Equal(t, "org-from-info", session.Identity.OrganizationID)
	require.Equal(t, "Qoder Org", session.Identity.OrganizationName)
	require.Equal(t, tokenInfo.MachineID, session.Machine.MachineID)
	require.Equal(t, tokenInfo.MachineToken, session.Machine.MachineToken)
}

func TestQoderOAuthServicePollReturnsPendingAndCompleted(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	client := &fakeQoderOAuthClient{ready: false}
	svc.clientFactory = func(_ qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		return client, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	authURL, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	pending, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)
	require.NoError(t, err)
	require.Equal(t, "pending", pending.Status)
	require.Nil(t, pending.TokenInfo)

	client.ready = true
	client.token = &qoder.DeviceTokenResponse{Token: "access-token", RefreshToken: "refresh-token", UserID: "user-1", ExpiresIn: 3600}
	client.userInfo = &qoder.UserInfo{ID: "user-1", Name: "Qoder User", OrganizationID: "org-1"}
	client.completedIdentity = &qoder.AuthIdentity{
		UID:                "user-1",
		AID:                "user-1",
		SecurityOauthToken: "access-token",
		RefreshToken:       "refresh-token",
	}
	client.completedExpiry = time.Now().Add(time.Hour)
	client.orgErr = errors.New("organization unavailable")
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, "access-token", completed.TokenInfo.SecurityOauthToken)
	require.Equal(t, authURL.Query().Get("machine_id"), completed.TokenInfo.MachineID)
	require.Equal(t, "user-1", completed.TokenInfo.UID)
	require.Equal(t, map[string]string{
		"code":    "organization_unavailable",
		"message": "Qoder organization info could not be loaded",
	}, completed.TokenInfo.Extra["organization_warning"])
}

func TestQoderOAuthServiceDoesNotCompleteWithoutRefreshToken(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	svc.clientFactory = func(_ qoder.Profile, _ string) (qoderOAuthClient, error) {
		return &fakeQoderOAuthClient{
			ready:    true,
			token:    &qoder.DeviceTokenResponse{Token: "access-token", UserID: "user-1"},
			userInfo: &qoder.UserInfo{ID: "user-1"},
		}, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)

	require.Nil(t, completed)
	require.ErrorContains(t, err, "refresh token is missing")
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, "QODER_OAUTH_RESPONSE_INVALID", infraerrors.Reason(err))
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Nil(t, session.CompletedTokenInfo)
}

func TestQoderOAuthServiceDoesNotCompleteWithoutStableUserIdentity(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	svc.clientFactory = func(_ qoder.Profile, _ string) (qoderOAuthClient, error) {
		return &fakeQoderOAuthClient{
			ready:   true,
			token:   &qoder.DeviceTokenResponse{Token: "access-token", RefreshToken: "refresh-token"},
			userErr: errors.New("userinfo unavailable"),
		}, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)

	require.Nil(t, completed)
	require.ErrorContains(t, err, "user identity is missing")
	require.Equal(t, 400, infraerrors.Code(err))
	require.Equal(t, "QODER_OAUTH_RESPONSE_INVALID", infraerrors.Reason(err))
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Nil(t, session.CompletedTokenInfo)
}

func TestQoderOAuthServiceTreatsExplicitPollRejectionAsTerminal(t *testing.T) {
	for _, status := range []int{400, 401, 403} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			svc := NewQoderOAuthService(nil)
			defer svc.Stop()
			svc.clientFactory = func(_ qoder.Profile, _ string) (qoderOAuthClient, error) {
				return &fakeQoderOAuthClient{pollErr: &qoder.OpenAPIError{
					Operation:  "device token poll",
					StatusCode: status,
					Message:    "rejected",
				}}, nil
			}

			result, err := svc.GenerateAuthURL(context.Background(), nil)
			require.NoError(t, err)
			completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)

			require.Nil(t, completed)
			require.Equal(t, 400, infraerrors.Code(err))
			require.Equal(t, "QODER_OAUTH_POLL_REJECTED", infraerrors.Reason(err))
		})
	}
}

func TestQoderOAuthServiceCompletedSessionIsIdempotent(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	client := &fakeQoderOAuthClient{
		ready: true,
		token: &qoder.DeviceTokenResponse{
			Token:        "access-token",
			RefreshToken: "refresh-token",
			UserID:       "user-1",
			ExpiresIn:    3600,
		},
		userInfo: &qoder.UserInfo{ID: "user-1", Name: "Qoder User"},
		completedIdentity: &qoder.AuthIdentity{
			UID:                "user-1",
			AID:                "user-1",
			SecurityOauthToken: "access-token",
			RefreshToken:       "refresh-token",
		},
		completedExpiry: time.Now().Add(time.Hour),
	}
	svc.clientFactory = func(_ qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		return client, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	completed, err := svc.Poll(context.Background(), result.SessionID, result.State, nil)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, "access-token", completed.TokenInfo.SecurityOauthToken)

	tokenInfo, err := svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
		SessionID: result.SessionID,
		State:     result.State,
		Code:      "ignored-by-device-flow",
	})
	require.NoError(t, err)
	require.Equal(t, "access-token", tokenInfo.SecurityOauthToken)
	require.Equal(t, 1, client.pollCalls, "completed session should return cached token info")
}

func TestQoderOAuthServiceConcurrentCompletionReusesSingleResult(t *testing.T) {
	svc := NewQoderOAuthService(nil)
	defer svc.Stop()
	client := &blockingQoderOAuthClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc.clientFactory = func(_ qoder.Profile, proxyURL string) (qoderOAuthClient, error) {
		return client, nil
	}

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)

	pollDone := make(chan *QoderPollResult, 1)
	pollErr := make(chan error, 1)
	go func() {
		pollResult, pollErrValue := svc.Poll(context.Background(), result.SessionID, result.State, nil)
		pollDone <- pollResult
		pollErr <- pollErrValue
	}()
	<-client.started

	exchangeDone := make(chan *QoderTokenInfo, 1)
	exchangeErr := make(chan error, 1)
	go func() {
		tokenInfo, exchangeErrValue := svc.ExchangeCode(context.Background(), &QoderExchangeCodeInput{
			SessionID: result.SessionID,
			State:     result.State,
			Code:      "ignored-by-device-flow",
		})
		exchangeDone <- tokenInfo
		exchangeErr <- exchangeErrValue
	}()

	select {
	case <-exchangeDone:
		t.Fatal("exchange should wait for the in-flight completion")
	case <-time.After(20 * time.Millisecond):
	}

	close(client.release)
	pollResult := <-pollDone
	require.NoError(t, <-pollErr)
	require.Equal(t, "completed", pollResult.Status)

	tokenInfo := <-exchangeDone
	require.NoError(t, <-exchangeErr)
	require.Equal(t, "access-token", tokenInfo.SecurityOauthToken)
	require.Equal(t, int32(1), client.pollCalls.Load())
}

func TestQoderOAuthServiceWarningsDoNotPersistRawUpstreamErrors(t *testing.T) {
	rawErr := errors.New(`upstream 500 {"access_token":"secret-token","email":"user@example.com","account_id":"aid-123"}`)

	tokenInfo := buildQoderTokenInfo(&qoder.AuthIdentity{
		SecurityOauthToken: "security-token",
		UID:                "user-1",
	}, &qoder.MachineIdentity{MachineID: "machine-1"}, rawErr, rawErr)
	body, err := json.Marshal(tokenInfo.Extra)
	require.NoError(t, err)

	require.NotContains(t, string(body), "secret-token")
	require.NotContains(t, string(body), "user@example.com")
	require.NotContains(t, string(body), "aid-123")
	require.Contains(t, string(body), "userinfo_unavailable")
	require.Contains(t, string(body), "organization_unavailable")
}

func TestQoderParseCallbackSupportsQueryFragmentAndPlainCode(t *testing.T) {
	state, code := parseQoderCallback("http://localhost/callback?code=query-code&state=query-state")
	require.Equal(t, "query-state", state)
	require.Equal(t, "query-code", code)

	state, code = parseQoderCallback("http://localhost/callback#code=fragment-code&state=fragment-state")
	require.Equal(t, "fragment-state", state)
	require.Equal(t, "fragment-code", code)

	state, code = parseQoderCallback("plain-code")
	require.Empty(t, state)
	require.Equal(t, "plain-code", code)
}
