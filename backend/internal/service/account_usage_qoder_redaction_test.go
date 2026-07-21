package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/pkg/qoder"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type qoderQuotaTransportErrorStub struct {
	cause error
}

func (s *qoderQuotaTransportErrorStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *qoderQuotaTransportErrorStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return nil, fmt.Errorf("request dump Authorization: %s: %w", req.Header.Get("Authorization"), s.cause)
}

func TestFetchQoderQuotaUsageRedactsTransportRequestDump(t *testing.T) {
	transportCause := errors.New("connection reset")
	account := &Account{
		ID:       91,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         qoder.RefreshModeQoderCN20,
			"security_oauth_token": "quota-secret-token",
			"refresh_token":        "refresh-secret-token",
			"machine_id":           "machine-1",
			"uid":                  "uid-1",
			"organization_id":      "org-1",
			"organization_tags":    []string{"Normal"},
		},
	}
	svc := &AccountUsageService{
		httpUpstream:         &qoderQuotaTransportErrorStub{cause: transportCause},
		qoderSessionProvider: NewQoderTokenProvider(),
	}

	_, err := svc.fetchQoderQuotaUsage(context.Background(), account)
	require.Error(t, err)
	require.ErrorIs(t, err, transportCause)
	require.NotContains(t, err.Error(), "quota-secret-token")
	require.NotContains(t, err.Error(), "refresh-secret-token")

	degraded := buildQoderDegradedUsage(err, account)
	require.NotContains(t, degraded.Error, "quota-secret-token")
	require.False(t, strings.Contains(strings.ToLower(degraded.Error), "authorization: bearer"))
	require.Equal(t, errorCodeNetworkError, degraded.ErrorCode)
}

func TestBuildQoderDegradedUsageClassifiesStructuredOpenAPIError(t *testing.T) {
	info := buildQoderDegradedUsage(fmt.Errorf("quota failed: %w", &qoder.OpenAPIError{
		Operation:  "quota usage",
		StatusCode: http.StatusForbidden,
		Message:    "access denied",
	}), nil)

	require.Equal(t, errorCodeUnauthenticated, info.ErrorCode)
	require.True(t, info.NeedsReauth)
}

type qoderQuotaHTTPErrorStub struct{}

func (s *qoderQuotaHTTPErrorStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return s.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (s *qoderQuotaHTTPErrorStub) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"detail":"rejected value ` + token + `"}`)),
		Request:    req,
	}, nil
}

func TestFetchQoderQuotaUsageRedactsBearerEchoedInUnknownField(t *testing.T) {
	account := &Account{
		ID:       92,
		Platform: PlatformQoder,
		Type:     AccountTypeCosy,
		Credentials: completeQoderCN20TestCredentials(map[string]any{
			"security_oauth_token": "quota-free-text-secret",
		}),
	}
	svc := &AccountUsageService{
		httpUpstream:         &qoderQuotaHTTPErrorStub{},
		qoderSessionProvider: NewQoderTokenProvider(),
	}

	_, err := svc.fetchQoderQuotaUsage(context.Background(), account)

	require.Error(t, err)
	require.Contains(t, err.Error(), "***")
	require.NotContains(t, err.Error(), "quota-free-text-secret")
}
