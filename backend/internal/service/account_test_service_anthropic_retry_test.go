//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountTestService_AnthropicPoolModeRetryable403ThenSuccessDoesNotSetError(t *testing.T) {
	success := newJSONResponse(http.StatusOK, "")
	success.Body = io.NopCloser(strings.NewReader("data: [DONE]\n\n"))
	account := &Account{
		ID:          903,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                      "sk-ant-test",
			"base_url":                     "https://compat-anthropic.example",
			"pool_mode":                    true,
			"pool_mode_retry_count":        1,
			"pool_mode_retry_status_codes": []any{float64(http.StatusForbidden)},
		},
	}
	repo := &openAIAccountTestRepo{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusForbidden, `{"error":{"message":"temporarily denied"}}`),
		success,
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg: &config.Config{
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
		},
	}

	result, err := svc.RunTestBackground(context.Background(), account.ID, "claude-sonnet-4-6")

	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Len(t, upstream.requests, 2)
	require.Zero(t, repo.setErrorID, "retryable probe failure must not persist StatusError before retries are exhausted")
	require.Equal(t, StatusActive, account.Status)
}
