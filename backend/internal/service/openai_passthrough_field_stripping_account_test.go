//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminServiceCreateAccountNormalizesPassthroughStripFields(t *testing.T) {
	repo := &longContextBillingRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "openai-account",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"api_key": "test"},
		Extra:                map[string]any{openAIPassthroughStripFieldsExtraKey: []any{" input[].status ", "reasoning.mode", "input[].status"}},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Same(t, account, repo.createdAccount)
	require.Equal(t, []string{"input[].status", "reasoning.mode"}, account.Extra[openAIPassthroughStripFieldsExtraKey])
}

func TestAdminServiceUpdateAccountRetainsExplicitEmptyPassthroughStripOverride(t *testing.T) {
	repo := &longContextBillingRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			"openai_passthrough":                 true,
			openAIPassthroughStripFieldsExtraKey: []string{"reasoning.mode"},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{
		"openai_passthrough":                 true,
		openAIPassthroughStripFieldsExtraKey: []any{},
	}})

	require.NoError(t, err)
	require.Equal(t, []string{}, account.Extra[openAIPassthroughStripFieldsExtraKey])
}

func TestAdminServiceUpdateAccountKeepsPassthroughStripInheritanceWhenKeyIsAbsent(t *testing.T) {
	repo := &longContextBillingRepoStub{account: &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			"openai_passthrough":                 true,
			openAIPassthroughStripFieldsExtraKey: []string{"reasoning.mode"},
		},
	}}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{
		"openai_passthrough": true,
	}})

	require.NoError(t, err)
	_, overridden := account.Extra[openAIPassthroughStripFieldsExtraKey]
	require.False(t, overridden)
}
