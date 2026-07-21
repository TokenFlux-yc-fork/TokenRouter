package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/stretchr/testify/require"
)

// Keep the existing gateway refresh stub on the same CAS path as production.
func (r *qoderRefreshAccountRepoStub) UpdateQoderOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	credentials map[string]any,
) (bool, error) {
	for i := range r.accounts {
		account := &r.accounts[i]
		if account.ID != id || account.Platform != PlatformQoder || account.Type != AccountTypeCosy ||
			!reflect.DeepEqual(QoderCredentialIdentitySnapshot(account.Credentials), QoderCredentialIdentitySnapshot(expectedCredentials)) {
			continue
		}
		r.updateCalls++
		account.Credentials = mergeQoderRefreshIdentity(account.Credentials, credentials)
		r.updatedCredentials = cloneCredentials(account.Credentials)
		return true, nil
	}
	return false, nil
}

type qoderRefreshCASRepo struct {
	AccountRepository
	mu        sync.Mutex
	account   *Account
	casCalls  int
	casWrites int
}

func (r *qoderRefreshCASRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, nil
	}
	return snapshotOAuthRefreshAccount(r.account), nil
}

func (r *qoderRefreshCASRepo) UpdateQoderOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedCredentials map[string]any,
	credentials map[string]any,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.casCalls++
	if r.account == nil || r.account.ID != id || r.account.Platform != PlatformQoder ||
		r.account.Type != AccountTypeCosy ||
		!reflect.DeepEqual(QoderCredentialIdentitySnapshot(r.account.Credentials), QoderCredentialIdentitySnapshot(expectedCredentials)) {
		return false, nil
	}
	r.casWrites++
	r.account.Credentials = mergeQoderRefreshIdentity(r.account.Credentials, credentials)
	return true, nil
}

func mergeQoderRefreshIdentity(current, refreshed map[string]any) map[string]any {
	out := shallowCopyMap(current)
	for _, key := range QoderCredentialIdentityKeys() {
		delete(out, key)
	}
	for key, value := range QoderCredentialIdentitySnapshot(refreshed) {
		out[key] = value
	}
	return out
}

func (r *qoderRefreshCASRepo) reauthorize(credentials map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Credentials = shallowCopyMap(credentials)
}

func (r *qoderRefreshCASRepo) changeProxy(proxyID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.ProxyID = &proxyID
}

func TestAdminQoderRefreshUsesIdentityCASAndPreservesConfiguration(t *testing.T) {
	futureVersion := time.Now().Add(time.Hour).UnixMilli()
	expected := map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       futureVersion,
		"model_mapping":        map[string]any{"alias": "attempt-route"},
	}
	repo := &qoderRefreshCASRepo{account: &Account{
		ID:          507,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "old-access",
			"refresh_token":        "old-refresh",
			"machine_id":           "machine",
			"uid":                  "user",
			"_token_version":       futureVersion,
			"model_mapping":        map[string]any{"alias": "concurrent-route"},
			"model_whitelist":      []any{"alias"},
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 507, &UpdateAccountInput{
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "rotated-access",
			"refresh_token":        "rotated-refresh",
			"machine_id":           "machine",
			"uid":                  "user",
			"model_mapping":        map[string]any{"alias": "stale-route"},
		},
		QoderRefreshExpectedCredentials: expected,
	})

	require.NoError(t, err)
	require.Equal(t, 1, repo.casWrites)
	require.Equal(t, "rotated-refresh", updated.GetCredential("refresh_token"))
	require.Equal(t, futureVersion+1, updated.GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"alias": "concurrent-route"}, updated.Credentials["model_mapping"])
	require.Equal(t, []any{"alias"}, updated.Credentials["model_whitelist"])
}

func TestAdminQoderRefreshRejectsConcurrentReauthorization(t *testing.T) {
	expected := map[string]any{
		"site":                 "cn",
		"refresh_mode":         qoder.RefreshModeQoderCN20,
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "old-machine",
		"uid":                  "old-user",
	}
	repo := &qoderRefreshCASRepo{account: &Account{
		ID:          508,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "reauthorized-access",
			"refresh_token":        "reauthorized-refresh",
			"machine_id":           "new-machine",
			"uid":                  "new-user",
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 508, &UpdateAccountInput{
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "stale-rotated-access",
			"refresh_token":        "stale-rotated-refresh",
			"machine_id":           "old-machine",
			"uid":                  "old-user",
		},
		QoderRefreshExpectedCredentials: expected,
	})

	require.Nil(t, updated)
	require.ErrorIs(t, err, ErrQoderAuthorizationConflict)
	require.Zero(t, repo.casWrites)
	require.Equal(t, "reauthorized-refresh", repo.account.GetCredential("refresh_token"))
}

type blockingQoderRefreshExecutor struct {
	started     chan struct{}
	release     chan struct{}
	credentials map[string]any
	err         error
	refreshes   int
}

func (e *blockingQoderRefreshExecutor) CacheKey(account *Account) string {
	return QoderTokenCacheKey(account)
}

func (e *blockingQoderRefreshExecutor) CanRefresh(account *Account) bool {
	return account != nil && account.IsQoderCosy()
}

func (e *blockingQoderRefreshExecutor) NeedsRefresh(*Account, time.Duration) bool {
	return true
}

func (e *blockingQoderRefreshExecutor) Refresh(ctx context.Context, _ *Account) (map[string]any, error) {
	e.refreshes++
	if e.started != nil {
		close(e.started)
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return shallowCopyMap(e.credentials), e.err
}

func TestQoderRefreshSuccessCASLetsConcurrentReauthorizationWin(t *testing.T) {
	oldCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "old-machine",
		"uid":                  "old-user",
	}
	reauthorizedCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "reauthorized-access",
		"refresh_token":        "reauthorized-refresh",
		"machine_id":           "new-machine",
		"uid":                  "new-user",
	}
	account := &Account{
		ID:          501,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: shallowCopyMap(oldCredentials),
	}
	repo := &qoderRefreshCASRepo{account: account}
	executor := &blockingQoderRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "stale-provider-access",
			"refresh_token":        "stale-provider-refresh",
			"machine_id":           "old-machine",
			"uid":                  "old-user",
		},
	}

	type refreshOutcome struct {
		result *OAuthRefreshResult
		err    error
	}
	outcome := make(chan refreshOutcome, 1)
	go func() {
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Hour)
		outcome <- refreshOutcome{result: result, err: err}
	}()

	<-executor.started
	repo.reauthorize(reauthorizedCredentials)
	close(executor.release)
	got := <-outcome

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.False(t, got.result.Refreshed)
	require.Equal(t, "reauthorized-refresh", got.result.Account.GetCredential("refresh_token"))
	require.Equal(t, 1, repo.casCalls)
	require.Zero(t, repo.casWrites)
}

func TestQoderRefreshSuccessCASPreservesConcurrentConfigurationAndAdvancesVersion(t *testing.T) {
	futureVersion := time.Now().Add(time.Hour).UnixMilli()
	oldCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       futureVersion,
		"model_mapping":        map[string]any{"alias": "old-route"},
	}
	account := &Account{
		ID:          503,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: shallowCopyMap(oldCredentials),
	}
	repo := &qoderRefreshCASRepo{account: account}
	executor := &blockingQoderRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "rotated-access",
			"refresh_token":        "rotated-refresh",
			"machine_id":           "machine",
			"uid":                  "user",
		},
	}

	type refreshOutcome struct {
		result *OAuthRefreshResult
		err    error
	}
	outcome := make(chan refreshOutcome, 1)
	go func() {
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Hour)
		outcome <- refreshOutcome{result: result, err: err}
	}()

	<-executor.started
	repo.mu.Lock()
	repo.account.Credentials["model_mapping"] = map[string]any{"alias": "new-route"}
	repo.account.Credentials["model_whitelist"] = []any{"alias"}
	repo.mu.Unlock()
	close(executor.release)
	got := <-outcome

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.True(t, got.result.Refreshed)
	require.Equal(t, "rotated-access", got.result.Account.GetCredential("security_oauth_token"))
	require.Equal(t, "rotated-refresh", got.result.Account.GetCredential("refresh_token"))
	require.Equal(t, futureVersion+1, got.result.Account.GetCredentialAsInt64("_token_version"))
	require.Equal(t, map[string]any{"alias": "new-route"}, got.result.Account.Credentials["model_mapping"])
	require.Equal(t, []any{"alias"}, got.result.Account.Credentials["model_whitelist"])
	require.Equal(t, 1, repo.casWrites)
}

func TestQoderRefreshSuccessCASPreservesConcurrentProxyChange(t *testing.T) {
	oldProxyID := int64(31)
	newProxyID := int64(32)
	oldCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "machine",
		"uid":                  "user",
		"_token_version":       int64(1),
	}
	account := &Account{
		ID:          505,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		ProxyID:     &oldProxyID,
		Credentials: shallowCopyMap(oldCredentials),
	}
	repo := &qoderRefreshCASRepo{account: account}
	executor := &blockingQoderRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "rotated-access",
			"refresh_token":        "rotated-refresh",
			"machine_id":           "machine",
			"uid":                  "user",
		},
	}

	type refreshOutcome struct {
		result *OAuthRefreshResult
		err    error
	}
	outcome := make(chan refreshOutcome, 1)
	go func() {
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), account, executor, time.Hour)
		outcome <- refreshOutcome{result: result, err: err}
	}()

	<-executor.started
	repo.changeProxy(newProxyID)
	close(executor.release)
	got := <-outcome

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.True(t, got.result.Refreshed)
	require.Equal(t, "rotated-refresh", got.result.Account.GetCredential("refresh_token"))
	require.NotNil(t, got.result.Account.ProxyID)
	require.Equal(t, newProxyID, *got.result.Account.ProxyID)
	require.Equal(t, 1, repo.casWrites)
}

func TestQoderRequestRefreshInvalidGrantRecoversConcurrentPATReauthorization(t *testing.T) {
	oldCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "old-machine",
		"uid":                  "old-user",
		"_token_version":       int64(1),
	}
	account := &Account{
		ID:          504,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: shallowCopyMap(oldCredentials),
	}
	repo := &qoderRefreshCASRepo{account: account}
	executor := &blockingQoderRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     errors.New("invalid_grant: refresh token consumed"),
	}

	type refreshOutcome struct {
		result *OAuthRefreshResult
		err    error
	}
	outcome := make(chan refreshOutcome, 1)
	go func() {
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(
			withOAuthRefreshRequestPath(context.Background()),
			account,
			executor,
			time.Hour,
		)
		outcome <- refreshOutcome{result: result, err: err}
	}()

	<-executor.started
	repo.reauthorize(map[string]any{
		"site":           "cn",
		"pat":            "new-pat",
		"machine_id":     "new-machine",
		"_token_version": int64(2),
	})
	close(executor.release)
	got := <-outcome

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.False(t, got.result.Refreshed)
	require.Equal(t, "new-pat", got.result.Account.GetCredential("pat"))
	require.Empty(t, got.result.Account.GetCredential("refresh_token"))
	require.Zero(t, repo.casWrites)
}

func TestQoderRequestRefreshStructuredInvalidCredentialsRecoversConcurrentReauthorization(t *testing.T) {
	oldCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "old-access",
		"refresh_token":        "old-refresh",
		"machine_id":           "old-machine",
		"uid":                  "old-user",
		"_token_version":       int64(1),
	}
	account := &Account{
		ID:          506,
		Platform:    PlatformQoder,
		Type:        AccountTypeCosy,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: shallowCopyMap(oldCredentials),
	}
	repo := &qoderRefreshCASRepo{account: account}
	executor := &blockingQoderRefreshExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err: fmt.Errorf("qoder refresh token: %w", &qoder.OpenAPIError{
			Operation:  "token refresh",
			StatusCode: 401,
			Message:    "unauthorized",
		}),
	}

	type refreshOutcome struct {
		result *OAuthRefreshResult
		err    error
	}
	outcome := make(chan refreshOutcome, 1)
	go func() {
		result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(
			withOAuthRefreshRequestPath(context.Background()),
			account,
			executor,
			time.Hour,
		)
		outcome <- refreshOutcome{result: result, err: err}
	}()

	<-executor.started
	repo.reauthorize(map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "new-access",
		"refresh_token":        "new-refresh",
		"machine_id":           "new-machine",
		"uid":                  "new-user",
		"_token_version":       int64(2),
	})
	close(executor.release)
	got := <-outcome

	require.NoError(t, got.err)
	require.NotNil(t, got.result)
	require.False(t, got.result.Refreshed)
	require.Equal(t, "new-access", got.result.Account.GetCredential("security_oauth_token"))
	require.Equal(t, "new-refresh", got.result.Account.GetCredential("refresh_token"))
	require.Zero(t, repo.casWrites)
}

func TestQoderRequestRefreshRejectsChangedAccountState(t *testing.T) {
	baseCredentials := map[string]any{
		"site":                 "cn",
		"refresh_mode":         "qodercn20",
		"security_oauth_token": "access",
		"refresh_token":        "refresh",
		"machine_id":           "machine",
		"uid":                  "user",
	}

	for name, mutate := range map[string]func(*Account){
		"unschedulable": func(account *Account) { account.Schedulable = false },
		"legacy site": func(account *Account) {
			account.Credentials["site"] = "global"
		},
		"missing refresh token": func(account *Account) {
			delete(account.Credentials, "refresh_token")
		},
	} {
		t.Run(name, func(t *testing.T) {
			account := &Account{
				ID:          502,
				Platform:    PlatformQoder,
				Type:        AccountTypeCosy,
				Status:      StatusActive,
				Schedulable: true,
				Credentials: shallowCopyMap(baseCredentials),
			}
			mutate(account)
			repo := &qoderRefreshCASRepo{account: account}
			executor := &blockingQoderRefreshExecutor{credentials: baseCredentials}

			result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(
				withOAuthRefreshRequestPath(context.Background()),
				account,
				executor,
				time.Hour,
			)

			require.Nil(t, result)
			require.ErrorIs(t, err, errOAuthRefreshAccountStateChanged)
			require.Zero(t, executor.refreshes)
		})
	}
}
