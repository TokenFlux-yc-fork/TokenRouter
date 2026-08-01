package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeFallbackStickyCache struct {
	service.GatewayCache
	accountID   int64
	deleteCalls int
}

func (c *nativeFallbackStickyCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return c.accountID, nil
}

func (c *nativeFallbackStickyCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

func (c *nativeFallbackStickyCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *nativeFallbackStickyCache) DeleteSessionAccountID(context.Context, int64, string) error {
	c.deleteCalls++
	return nil
}

func (c *nativeFallbackStickyCache) SetSessionOwnerGroupID(context.Context, int64, string, string, int64, time.Duration) (bool, error) {
	return true, nil
}

func (c *nativeFallbackStickyCache) GetSessionOwnerGroupID(context.Context, int64, string, string) (int64, error) {
	return 0, nil
}

func (c *nativeFallbackStickyCache) RefreshSessionOwnerTTL(context.Context, int64, string, string, time.Duration) error {
	return nil
}

func TestRemoveOpenAIRemoteCompactionV2FeaturePreservesOtherFeatures(t *testing.T) {
	header := http.Header{}
	header.Add("x-codex-beta-features", "feature_one, remote_compaction_v2, feature_two")
	header.Add("x-codex-beta-features", "feature_three")

	require.True(t, removeOpenAIRemoteCompactionV2Feature(header))
	require.Equal(t, []string{"feature_one, feature_two", "feature_three"}, header.Values("x-codex-beta-features"))
	require.False(t, hasOpenAIRemoteCompactionV2Feature(header.Values("x-codex-beta-features")))
}

func TestOpenAIResponsesNativeCompactionMissingDomainFallsBackToLegacyCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := nativeCompactionMixedAccount(t, 9980, service.AccountTypeAPIKey, 1)
	account.Extra["openai_compact_supported"] = true
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	stickyCache := &nativeFallbackStickyCache{accountID: account.ID}
	upstream := newLegacyCompactFallbackUpstream(t, "resp_domain_fallback", "", "feature_one")
	handler := newNativeCompactionMixedFailoverHandler(
		t,
		newOpenAIHTTPFailoverTestConfig(),
		accountRepo,
		upstream,
		&nativeCompactionCapabilityRepoStub{},
		newNativeCompactionAttemptRepoStub(),
		&nativeCompactionUsageLogRepoStub{},
		stickyCache,
	)

	body := []byte(`{"model":"gpt-5.1","stream":true,"prompt_cache_key":"domain-session","previous_response_id":"resp_without_domain","input":[{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("x-codex-beta-features", "feature_one, remote_compaction_v2")
	bindOpenAIHTTPFailoverTestAuth(c, 4230)

	handler.Responses(c)

	require.Equal(t, []int64{account.ID}, upstream.AccountIDs())
	require.Zero(t, stickyCache.deleteCalls)
	require.False(t, service.IsOpenAINativeRemoteCompactionV2(c))
	require.Equal(t, "/openai/v1/responses/compact", c.Request.URL.Path)
	requireLegacyCompactFallbackResponse(t, recorder, "resp_domain_fallback")
}

func TestOpenAIResponsesNativeCompactionEmptyCapabilityFallsBackToLegacyCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := nativeCompactionMixedAccount(t, 9981, service.AccountTypeAPIKey, 1)
	account.OpenAINativeCompactionCapabilities = nil
	account.Extra["openai_compact_supported"] = true
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	upstream := newLegacyCompactFallbackUpstream(t, "resp_capability_fallback", "", "")
	handler := newNativeCompactionMixedFailoverHandler(
		t,
		newOpenAIHTTPFailoverTestConfig(),
		accountRepo,
		upstream,
		&nativeCompactionCapabilityRepoStub{},
		newNativeCompactionAttemptRepoStub(),
		&nativeCompactionUsageLogRepoStub{},
	)

	body := []byte(`{"model":"gpt-5.1","stream":true,"prompt_cache_key":"capability-session","input":[{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	bindOpenAIHTTPFailoverTestAuth(c, 4231)

	handler.Responses(c)

	require.Equal(t, []int64{account.ID}, upstream.AccountIDs())
	require.False(t, service.IsOpenAINativeRemoteCompactionV2(c))
	require.Equal(t, "/openai/v1/responses/compact", c.Request.URL.Path)
	requireLegacyCompactFallbackResponse(t, recorder, "resp_capability_fallback")
}

func TestOpenAIResponsesNativeCompactionSlowLegacyFallbackEmitsObservablePing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := nativeCompactionMixedAccount(t, 9982, service.AccountTypeAPIKey, 1)
	account.OpenAINativeCompactionCapabilities = nil
	account.Extra["openai_compact_supported"] = true
	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{account}}
	upstream := newLegacyCompactFallbackUpstream(t, "resp_slow_fallback", "", "")
	originalDo := upstream.do
	upstream.do = func(req *http.Request, accountID int64) (*http.Response, error) {
		time.Sleep(1200 * time.Millisecond)
		return originalDo(req, accountID)
	}
	cfg := newOpenAIHTTPFailoverTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	handler := newNativeCompactionMixedFailoverHandler(
		t,
		cfg,
		accountRepo,
		upstream,
		&nativeCompactionCapabilityRepoStub{},
		newNativeCompactionAttemptRepoStub(),
		&nativeCompactionUsageLogRepoStub{},
	)

	body := []byte(`{"model":"gpt-5.1","stream":true,"prompt_cache_key":"slow-session","input":[{"type":"compaction_trigger"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	bindOpenAIHTTPFailoverTestAuth(c, 4232)

	handler.Responses(c)

	wire := recorder.Body.String()
	require.Contains(t, wire, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
	require.Contains(t, wire, ": keepalive\n\n")
	require.Equal(t, 1, strings.Count(wire, `"type":"response.output_item.done"`))
	require.Equal(t, 1, strings.Count(wire, `"type":"response.completed"`))
	require.NotContains(t, wire, `"type":"response.failed"`)
	require.Equal(t, []int64{account.ID}, upstream.AccountIDs())
}

func newLegacyCompactFallbackUpstream(t *testing.T, responseID, previousResponseID, preservedFeature string) *openAIHandlerHTTPUpstreamStub {
	t.Helper()
	return &openAIHandlerHTTPUpstreamStub{do: func(req *http.Request, _ int64) (*http.Response, error) {
		requestBody, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(req.URL.Path, "/responses/compact"), req.URL.Path)
		require.False(t, gjson.GetBytes(requestBody, "stream").Exists())
		require.False(t, gjson.GetBytes(requestBody, "prompt_cache_key").Exists())
		require.Equal(t, previousResponseID, gjson.GetBytes(requestBody, "previous_response_id").String())
		require.NotContains(t, strings.ToLower(req.Header.Get("x-codex-beta-features")), "remote_compaction_v2")
		if preservedFeature != "" {
			require.Contains(t, req.Header.Get("x-codex-beta-features"), preservedFeature)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"` + responseID + `","output":[{"id":"cmp_legacy","type":"compaction","status":"completed","encrypted_content":"legacy-state"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`,
			)),
		}, nil
	}}
}

func requireLegacyCompactFallbackResponse(t *testing.T, recorder *httptest.ResponseRecorder, responseID string) {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	wire := recorder.Body.String()
	require.Contains(t, wire, responseID)
	require.Contains(t, wire, `"type":"response.output_item.done"`)
	require.Contains(t, wire, `"type":"response.completed"`)
	require.NotContains(t, wire, `"type":"response.failed"`)
}
