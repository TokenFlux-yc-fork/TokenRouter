package repository

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/pkg/tlsfingerprint"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/http2"
)

func TestHTTPUpstreamDoAppliesGrokCLIIdentityBeforeOAuthRoundTrip(t *testing.T) {
	t.Setenv("XAI_GROK_CLI_VERSION", "")

	for _, endpoint := range []string{"responses", "chat/completions"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := NewHTTPUpstream(nil)
			svc, ok := upstream.(*httpUpstreamService)
			require.True(t, ok)

			const accountID int64 = 4084
			isolation := svc.getIsolationMode()
			profile := service.HTTPUpstreamProfileDefault
			proxyKey := directProxyKey
			protocolMode := svc.resolveProtocolMode(profile, proxyKey, nil)
			settings := svc.resolvePoolSettings(isolation, 1)
			settings = svc.applyProfilePoolSettings(settings, profile)
			cacheKey := buildCacheKey(isolation, proxyKey, accountID, protocolMode)

			var capturedHeaders http.Header
			svc.clients[cacheKey] = &upstreamClientEntry{
				client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					capturedHeaders = req.Header.Clone()
					statusCode := http.StatusOK
					if req.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" {
						statusCode = http.StatusForbidden
					}
					return &http.Response{
						StatusCode: statusCode,
						Header:     make(http.Header),
						Body:       http.NoBody,
						Request:    req,
					}, nil
				})},
				proxyKey:     proxyKey,
				poolKey:      buildPoolKey(settings, protocolMode),
				protocolMode: protocolMode,
			}

			req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/"+endpoint, nil)
			require.NoError(t, err)
			req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

			resp, err := svc.Do(req, "", accountID, 1)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.NoError(t, resp.Body.Close())

			require.Equal(t, "0.2.93", capturedHeaders.Get("x-grok-client-version"))
			require.Equal(t, "xai-grok-cli", capturedHeaders.Get("X-XAI-Token-Auth"))
			require.Equal(t, "xai-grok-workspace/0.2.93", capturedHeaders.Get("User-Agent"))
		})
	}
}

func TestApplyGrokCLIProxyHeaders(t *testing.T) {
	t.Run("uses pinned stable version for the CLI proxy", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.93", req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
		require.Equal(t, "xai-grok-workspace/0.2.93", req.Header.Get("User-Agent"))
	})

	t.Run("accepts a valid operator override", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.95-alpha.1")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/chat/completions", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.95-alpha.1", req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-workspace/0.2.95-alpha.1", req.Header.Get("User-Agent"))
	})

	t.Run("rejects an unsafe override", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.95\r\nX-Injected: true")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.93", req.Header.Get("x-grok-client-version"))
		require.Empty(t, req.Header.Get("X-Injected"))
	})

	t.Run("rejects an override below the supported minimum", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.92")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.93", req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-workspace/0.2.93", req.Header.Get("User-Agent"))
	})

	t.Run("rejects a prerelease override at the minimum version", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.93-beta.1")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.93", req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-workspace/0.2.93", req.Header.Get("User-Agent"))
	})

	for _, version := range []string{
		"0.2.093",
		"0.2.94-alpha..1",
		"0.3",
		"1",
		"0.2.95+build.1",
	} {
		t.Run("rejects invalid semver "+version, func(t *testing.T) {
			t.Setenv("XAI_GROK_CLI_VERSION", version)
			req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
			require.NoError(t, err)

			applyGrokCLIProxyHeaders(req)

			require.Equal(t, "0.2.93", req.Header.Get("x-grok-client-version"))
			require.Equal(t, "xai-grok-workspace/0.2.93", req.Header.Get("User-Agent"))
		})
	}

	t.Run("leaves direct xAI API requests unchanged", func(t *testing.T) {
		t.Setenv("XAI_GROK_CLI_VERSION", "0.2.95")
		req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "tokenrouter-grok/1.0")

		applyGrokCLIProxyHeaders(req)

		require.Empty(t, req.Header.Get("x-grok-client-version"))
		require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
		require.Equal(t, "tokenrouter-grok/1.0", req.Header.Get("User-Agent"))
	})
}

// HTTPUpstreamSuite HTTP 上游服务测试套件
// 使用 testify/suite 组织测试，支持 SetupTest 初始化
type HTTPUpstreamSuite struct {
	suite.Suite
	cfg *config.Config // 测试用配置
}

// SetupTest 每个测试用例执行前的初始化
// 创建空配置，各测试用例可按需覆盖
func (s *HTTPUpstreamSuite) SetupTest() {
	s.cfg = &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				AllowPrivateHosts: true,
			},
		},
	}
}

// newService 创建测试用的 httpUpstreamService 实例
// 返回具体类型以便访问内部状态进行断言
func (s *HTTPUpstreamSuite) newService() *httpUpstreamService {
	up := NewHTTPUpstream(s.cfg)
	svc, ok := up.(*httpUpstreamService)
	require.True(s.T(), ok, "expected *httpUpstreamService")
	return svc
}

// TestDefaultResponseHeaderTimeout 测试默认响应头超时配置
// 验证显式 0 会禁用等待响应头超时
func (s *HTTPUpstreamSuite) TestDefaultResponseHeaderTimeout() {
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestNilConfigResponseHeaderTimeoutFallback 验证 nil 配置使用代码级兜底值。
func (s *HTTPUpstreamSuite) TestNilConfigResponseHeaderTimeoutFallback() {
	up := NewHTTPUpstream(nil)
	svc, ok := up.(*httpUpstreamService)
	require.True(s.T(), ok, "expected *httpUpstreamService")
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 300*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestCustomResponseHeaderTimeout 测试自定义响应头超时配置
// 验证配置值能正确应用到 Transport
func (s *HTTPUpstreamSuite) TestCustomResponseHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 7}
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 7*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestGetOrCreateClient_InvalidURLReturnsError 测试无效代理 URL 返回错误
// 验证解析失败时拒绝回退到直连模式
func (s *HTTPUpstreamSuite) TestGetOrCreateClient_InvalidURLReturnsError() {
	svc := s.newService()
	_, err := svc.getClientEntry("://bad-proxy-url", 1, 1, service.HTTPUpstreamProfileDefault, false, false)
	require.Error(s.T(), err, "expected error for invalid proxy URL")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileDefaultsToHTTP2AndNoHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "OpenAI profile should not inherit generic header timeout")
	require.True(s.T(), transport.ForceAttemptHTTP2, "OpenAI profile should prefer HTTP/2")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileCustomHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout:       600,
		OpenAIResponseHeaderTimeout: 1800,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintDoesNotInheritGenericHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{Name: "test"}, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "OpenAI TLS path should not inherit generic header timeout")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1, entry.protocolMode, "未声明 h2 的 TLS 模板应保持 HTTP/1.1")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintUsesHTTP2WhenALPNAllowsH2() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{
		Name:          "h2-profile",
		ALPNProtocols: []string{"h2", "http/1.1"},
	}, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http2.Transport)
	require.True(s.T(), ok, "声明 h2 的 TLS 模板应启用 HTTP/2 transport")
	require.NotNil(s.T(), transport.DialTLSContext)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintHTTP2HeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIResponseHeaderTimeout: 1,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{
		Name:              "h2-profile",
		ALPNProtocols:     []string{"h2", "http/1.1"},
		SupportedVersions: []uint16{0x0304},
	}, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*responseHeaderTimeoutRoundTripper)
	require.True(s.T(), ok, "TLS+h2 路径应包装响应头超时")
	require.Equal(s.T(), time.Second, transport.timeout)
	_, ok = transport.base.(*http2.Transport)
	require.True(s.T(), ok, "响应头超时包装内层应保持 HTTP/2 transport")
}

func TestResponseHeaderTimeoutRoundTripperTimesOut(t *testing.T) {
	transport := &responseHeaderTimeoutRoundTripper{
		base:    blockingHeaderRoundTripper{},
		timeout: 10 * time.Millisecond,
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/responses", nil)
	require.NoError(t, err)

	startedAt := time.Now()
	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout awaiting response headers")
	require.Less(t, time.Since(startedAt), time.Second)
}

type blockingHeaderRoundTripper struct{}

func (blockingHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// 模拟上游迟迟不返回响应头，直到请求上下文被取消。
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintProfileHashSplitsClientCache() {
	svc := s.newService()
	profileA := &tlsfingerprint.Profile{Name: "profile-a", CipherSuites: []uint16{0x1301}}
	profileB := &tlsfingerprint.Profile{Name: "profile-b", CipherSuites: []uint16{0x1302}}

	entryA, err := svc.getClientEntryWithTLS("", 1, 1, profileA, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	entryAAgain, err := svc.getClientEntryWithTLS("", 1, 1, profileA, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	entryB, err := svc.getClientEntryWithTLS("", 1, 1, profileB, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	require.Same(s.T(), entryA, entryAAgain, "相同 TLS profile 应复用缓存客户端")
	require.NotSame(s.T(), entryA, entryB, "不同 TLS profile hash 不应复用旧 Transport")
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintHTTP2FallbackSplitsClientCache() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	profile := &tlsfingerprint.Profile{Name: "h2-profile", ALPNProtocols: []string{"h2", "http/1.1"}}

	entryH2, err := svc.getClientEntryWithTLS(proxyURL, 1, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	routeKey := buildOpenAIHTTP2RouteKey(entryH2.proxyKey, nil)
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, errors.New("http2: protocol error"))
	entryH1, err := svc.getClientEntryWithTLS(proxyURL, 1, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	require.NotSame(s.T(), entryH2, entryH1, "H2 回退后应重建 TLS 指纹客户端")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entryH1.protocolMode)
	transport, ok := entryH1.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "回退到 HTTP/1.1 时应使用普通 http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.Contains(s.T(), entryH1.poolKey, "proto:"+upstreamProtocolModeOpenAIH1Fallback)
}

func (s *HTTPUpstreamSuite) TestTLSFingerprintDefaultProfileStripsH2ALPNOutsideOpenAIH2Mode() {
	svc := s.newService()
	profile := &tlsfingerprint.Profile{Name: "h2-profile", ALPNProtocols: []string{"h2", "http/1.1"}}

	entry, err := svc.getClientEntryWithTLS("", 1, 1, profile, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(s.T(), err)

	require.Equal(s.T(), upstreamProtocolModeDefault, entry.protocolMode)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "非 OpenAI H2 模式应保持 HTTP/1.1 transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.NotContains(s.T(), entry.poolKey, tlsfingerprint.CacheKey(profile), "缓存键应使用剥离 h2 后的 profile hash")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileHTTP2DisabledUsesHTTP1Transport() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: false},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2, "OpenAI HTTP/2 disabled should not force H2")
	require.NotNil(s.T(), transport.TLSNextProto, "HTTP/1 mode should disable automatic H2 negotiation")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIHeaderTimeoutChangeRebuildsClient() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: true},
	}
	svc := s.newService()
	entry1, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	s.cfg.Gateway.OpenAIResponseHeaderTimeout = 1800
	entry2, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	require.NotSame(s.T(), entry1, entry2, "OpenAI header timeout changes must rebuild cached client")
	transport, ok := entry2.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2TimeoutDoesNotActivateProxyFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	routeKey := buildOpenAIHTTP2RouteKey(proxyURL, nil)
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, errors.New("http2: timeout awaiting response headers"))
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey), "header timeout should not be treated as H2 compatibility failure")
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2NonCompatibilityErrorsDoNotActivateFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                true,
			FallbackErrorThreshold: 1,
			FallbackTTLSeconds:     600,
		},
	}
	routeKey := openAIHTTP2RouteKey{egressKey: directProxyKey, authority: "https://chatgpt.com"}
	tests := map[string]error{
		"stream_no_error":        http2.StreamError{StreamID: 1, Code: http2.ErrCodeNo},
		"stream_cancel":          http2.StreamError{StreamID: 1, Code: http2.ErrCodeCancel},
		"stream_refused":         http2.StreamError{StreamID: 1, Code: http2.ErrCodeRefusedStream},
		"stream_closed":          http2.StreamError{StreamID: 1, Code: http2.ErrCodeStreamClosed},
		"stream_enhance_calm":    http2.StreamError{StreamID: 1, Code: http2.ErrCodeEnhanceYourCalm},
		"connection_internal":    http2.ConnectionError(http2.ErrCodeInternal),
		"connection_connect":     http2.ConnectionError(http2.ErrCodeConnect),
		"connection_timeout":     http2.ConnectionError(http2.ErrCodeSettingsTimeout),
		"client_connection_lost": errors.New("http2: client connection lost"),
		"string_refused_stream":  errors.New("stream error: stream ID 1; REFUSED_STREAM"),
		"string_graceful_goaway": errors.New("http2: Transport received GOAWAY from server ErrCode:NO_ERROR"),
		"string_overload_goaway": errors.New("http2: Transport received GOAWAY from server ErrCode:ENHANCE_YOUR_CALM"),
		"goaway_no_error": http2.GoAwayError{
			ErrCode: http2.ErrCodeNo,
		},
		"goaway_refused": http2.GoAwayError{
			ErrCode: http2.ErrCodeRefusedStream,
		},
		"goaway_enhance_calm": http2.GoAwayError{
			ErrCode: http2.ErrCodeEnhanceYourCalm,
		},
		"connection_no_error": http2.ConnectionError(http2.ErrCodeNo),
	}
	for name, testErr := range tests {
		s.Run(name, func() {
			svc := s.newService()
			require.False(s.T(), isOpenAIHTTP2CompatibilityError(testErr))
			svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, testErr)
			svc.recordOpenAIHTTP2BodyFailure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, testErr)
			require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey))
		})
	}
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2CompatibilityErrorClassification() {
	tests := map[string]error{
		"unexpected_alpn":     errors.New(`http2: unexpected ALPN protocol "http/1.1"; want "h2"`),
		"no_application":      errors.New("remote error: tls: no application protocol"),
		"frame_too_large":     http2.ErrFrameTooLarge,
		"stream_protocol":     http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol},
		"goaway_flow_control": http2.GoAwayError{ErrCode: http2.ErrCodeFlowControl},
		"connection_frame":    http2.ConnectionError(http2.ErrCodeFrameSize),
		"compression":         http2.StreamError{StreamID: 1, Code: http2.ErrCodeCompression},
		"security":            http2.GoAwayError{ErrCode: http2.ErrCodeInadequateSecurity},
		"http11_required":     http2.GoAwayError{ErrCode: http2.ErrCodeHTTP11Required},
	}
	for name, testErr := range tests {
		s.Run(name, func() {
			require.True(s.T(), isOpenAIHTTP2CompatibilityError(testErr))
		})
	}
	require.False(s.T(), isOpenAIHTTP2CompatibilityError(io.ErrUnexpectedEOF), "unexpected EOF is only actionable while reading an H2 response body")
	require.True(s.T(), isOpenAIHTTP2BodyCompatibilityError(io.ErrUnexpectedEOF))
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2ProxyCompatibilityErrorActivatesFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	routeKey := buildOpenAIHTTP2RouteKey(proxyURL, nil)
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, errors.New("http2: protocol error"))
	require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey))

	entry, err := svc.getClientEntry(proxyURL, 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.NotNil(s.T(), transport.TLSNextProto)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2DirectBodyFailureActivatesFallbackImmediately() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                true,
			FallbackErrorThreshold: 99,
			FallbackTTLSeconds:     600,
		},
	}
	targetReq, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(s.T(), err)

	for name, readErr := range map[string]error{
		"unexpected_eof": io.ErrUnexpectedEOF,
		"goaway": http2.GoAwayError{
			ErrCode: http2.ErrCodeProtocol,
		},
	} {
		s.Run(name, func() {
			svc := s.newService()
			routeKey := buildOpenAIHTTP2RouteKey(directProxyKey, targetReq.URL)
			entry, entryErr := svc.getClientEntryForTarget("", 1, 1, service.HTTPUpstreamProfileOpenAI, targetReq.URL, false, false)
			require.NoError(s.T(), entryErr)
			entry.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					ProtoMajor: 2,
					Header:     make(http.Header),
					Body:       &fixedReadErrorCloser{err: readErr},
					Request:    req,
				}, nil
			})}
			req := targetReq.Clone(service.WithHTTPUpstreamProfile(targetReq.Context(), service.HTTPUpstreamProfileOpenAI))
			resp, doErr := svc.Do(req, "", 1, 1)
			require.NoError(s.T(), doErr)

			_, gotErr := resp.Body.Read(make([]byte, 1))
			require.ErrorIs(s.T(), gotErr, readErr)
			require.NoError(s.T(), resp.Body.Close())
			require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey))

			entry, entryErr = svc.getClientEntryForTarget("", 1, 1, service.HTTPUpstreamProfileOpenAI, targetReq.URL, false, false)
			require.NoError(s.T(), entryErr)
			require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)
			transport, ok := entry.client.Transport.(*http.Transport)
			require.True(s.T(), ok)
			require.False(s.T(), transport.ForceAttemptHTTP2)
		})
	}
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2FallbackIsScopedByEgressAndTargetAuthority() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	targetA, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(s.T(), err)
	targetB, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	require.NoError(s.T(), err)
	routeA := buildOpenAIHTTP2RouteKey(proxyURL, targetA.URL)
	routeOtherAuthority := buildOpenAIHTTP2RouteKey(proxyURL, targetB.URL)
	routeOtherEgress := buildOpenAIHTTP2RouteKey(directProxyKey, targetA.URL)

	svc.recordOpenAIHTTP2BodyFailure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeA, io.ErrUnexpectedEOF)
	require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(routeA))
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeOtherAuthority))
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeOtherEgress))

	entryA, err := svc.getClientEntryForTarget(proxyURL, 1, 1, service.HTTPUpstreamProfileOpenAI, targetA.URL, false, false)
	require.NoError(s.T(), err)
	entryOtherAuthority, err := svc.getClientEntryForTarget(proxyURL, 1, 1, service.HTTPUpstreamProfileOpenAI, targetB.URL, false, false)
	require.NoError(s.T(), err)
	entryOtherEgress, err := svc.getClientEntryForTarget("", 1, 1, service.HTTPUpstreamProfileOpenAI, targetA.URL, false, false)
	require.NoError(s.T(), err)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entryA.protocolMode)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entryOtherAuthority.protocolMode)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entryOtherEgress.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2OutcomeUsesBodyEOFNotHeadersOrClose() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                true,
			FallbackErrorThreshold: 2,
			FallbackWindowSeconds:  60,
			FallbackTTLSeconds:     600,
		},
	}
	routeKey := openAIHTTP2RouteKey{egressKey: directProxyKey, authority: "https://chatgpt.com"}
	compatErr := errors.New("http2: protocol error")

	s.Run("headers_and_close_are_neutral", func() {
		svc := s.newService()
		svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, compatErr)
		resp := &http.Response{ProtoMajor: 2, Body: io.NopCloser(strings.NewReader("ok"))}
		svc.observeOpenAIHTTP2ResponseBody(resp, service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey)
		require.NoError(s.T(), resp.Body.Close())

		svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, compatErr)
		require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey), "Close without EOF must not reset the first failure")
	})

	s.Run("complete_eof_resets_error_window", func() {
		svc := s.newService()
		svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, compatErr)
		resp := &http.Response{ProtoMajor: 2, Body: io.NopCloser(strings.NewReader("ok"))}
		svc.observeOpenAIHTTP2ResponseBody(resp, service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey)
		_, readErr := io.ReadAll(resp.Body)
		require.NoError(s.T(), readErr)

		svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey, compatErr)
		require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey), "a complete H2 body must reset the previous failure")
	})
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2OutcomeIgnoresHTTP1BodyErrors() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:            true,
			FallbackTTLSeconds: 600,
		},
	}
	svc := s.newService()
	routeKey := openAIHTTP2RouteKey{egressKey: directProxyKey, authority: "https://chatgpt.com"}
	resp := &http.Response{ProtoMajor: 1, Body: &fixedReadErrorCloser{err: io.ErrUnexpectedEOF}}
	svc.observeOpenAIHTTP2ResponseBody(resp, service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, routeKey)

	_, err := resp.Body.Read(make([]byte, 1))
	require.ErrorIs(s.T(), err, io.ErrUnexpectedEOF)
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(routeKey))
}

type fixedReadErrorCloser struct {
	err error
}

func (r *fixedReadErrorCloser) Read([]byte) (int, error) { return 0, r.err }
func (r *fixedReadErrorCloser) Close() error             { return nil }

// TestNormalizeProxyURL_Canonicalizes 测试代理 URL 规范化
// 验证等价地址能够映射到同一缓存键
func (s *HTTPUpstreamSuite) TestNormalizeProxyURL_Canonicalizes() {
	key1, _, err1 := normalizeProxyURL("http://proxy.local:8080")
	require.NoError(s.T(), err1)
	key2, _, err2 := normalizeProxyURL("http://proxy.local:8080/")
	require.NoError(s.T(), err2)
	require.Equal(s.T(), key1, key2, "expected normalized proxy keys to match")
}

// TestAcquireClient_OverLimitReturnsError 测试连接池缓存上限保护
// 验证超限且无可淘汰条目时返回错误
func (s *HTTPUpstreamSuite) TestAcquireClient_OverLimitReturnsError() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      1,
	}
	svc := s.newService()
	entry1, err := svc.acquireClient("http://proxy-a:8080", 1, 1)
	require.NoError(s.T(), err, "expected first acquire to succeed")
	require.NotNil(s.T(), entry1, "expected entry")

	entry2, err := svc.acquireClient("http://proxy-b:8080", 2, 1)
	require.Error(s.T(), err, "expected error when cache limit reached")
	require.Nil(s.T(), entry2, "expected nil entry when cache limit reached")
}

// TestDo_WithoutProxy_GoesDirect 测试无代理时直连
// 验证空代理 URL 时请求直接发送到目标服务器
func (s *HTTPUpstreamSuite) TestDo_WithoutProxy_GoesDirect() {
	// 创建模拟上游服务器
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/x", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct", string(b), "unexpected body")
}

// TestDo_WithHTTPProxy_UsesProxy 测试 HTTP 代理功能
// 验证请求通过代理服务器转发，使用绝对 URI 格式
func (s *HTTPUpstreamSuite) TestDo_WithHTTPProxy_UsesProxy() {
	// 用于接收代理请求的通道
	seen := make(chan string, 1)
	// 创建模拟代理服务器
	proxySrv := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.RequestURI // 记录请求 URI
		_, _ = io.WriteString(w, "proxied")
	}))
	s.T().Cleanup(proxySrv.Close)

	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 1}
	up := NewHTTPUpstream(s.cfg)

	// 发送请求到外部地址，应通过代理
	req, err := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, proxySrv.URL, 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "proxied", string(b), "unexpected body")

	// 验证代理收到的是绝对 URI 格式（HTTP 代理规范要求）
	select {
	case uri := <-seen:
		require.Equal(s.T(), "http://example.com/test", uri, "expected absolute-form request URI")
	default:
		require.Fail(s.T(), "expected proxy to receive request")
	}
}

// TestDo_EmptyProxy_UsesDirect 测试空代理字符串
// 验证空字符串代理等同于直连
func (s *HTTPUpstreamSuite) TestDo_EmptyProxy_UsesDirect() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct-empty")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/y", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do with empty proxy")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct-empty", string(b))
}

// TestAccountIsolation_DifferentAccounts 测试账户隔离模式
// 验证不同账户使用独立的连接池
func (s *HTTPUpstreamSuite) TestAccountIsolation_DifferentAccounts() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一代理，不同账户
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 2, 3)
	require.NotSame(s.T(), entry1, entry2, "不同账号不应共享连接池")
	require.Equal(s.T(), 2, len(svc.clients), "账号隔离应缓存两个客户端")
}

// TestAccountProxyIsolation_DifferentProxy 测试账户+代理组合隔离模式
// 验证同一账户使用不同代理时创建独立连接池
func (s *HTTPUpstreamSuite) TestAccountProxyIsolation_DifferentProxy() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy}
	svc := s.newService()
	// 同一账户，不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号+代理隔离应区分不同代理")
	require.Equal(s.T(), 2, len(svc.clients), "账号+代理隔离应缓存两个客户端")
}

// TestAccountModeProxyChangeClearsPool 测试账户模式下代理变更
// 验证账户切换代理时清理旧连接池，避免复用错误代理
func (s *HTTPUpstreamSuite) TestAccountModeProxyChangeClearsPool() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一账户，先后使用不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号切换代理应创建新连接池")
	require.Equal(s.T(), 1, len(svc.clients), "账号模式下应仅保留一个连接池")
	require.False(s.T(), hasEntry(svc, entry1), "旧连接池应被清理")
}

// TestAccountConcurrencyOverridesPoolSettings 测试账户并发数覆盖连接池配置
// 验证账户隔离模式下，连接池大小与账户并发数对应
func (s *HTTPUpstreamSuite) TestAccountConcurrencyOverridesPoolSettings() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 账户并发数为 12
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 12)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	// 连接池参数应与并发数一致
	require.Equal(s.T(), 12, transport.MaxConnsPerHost, "MaxConnsPerHost mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConns, "MaxIdleConns mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost mismatch")
}

// TestAccountConcurrencyFallbackToDefault 测试账户并发数为 0 时回退到默认配置
// 验证未指定并发数时使用全局配置值
func (s *HTTPUpstreamSuite) TestAccountConcurrencyFallbackToDefault() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		MaxIdleConns:            77,
		MaxIdleConnsPerHost:     55,
		MaxConnsPerHost:         66,
	}
	svc := s.newService()
	// 账户并发数为 0，应使用全局配置
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 66, transport.MaxConnsPerHost, "MaxConnsPerHost fallback mismatch")
	require.Equal(s.T(), 77, transport.MaxIdleConns, "MaxIdleConns fallback mismatch")
	require.Equal(s.T(), 55, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost fallback mismatch")
}

// TestEvictOverLimitRemovesOldestIdle 测试超出数量限制时的 LRU 淘汰
// 验证优先淘汰最久未使用的空闲客户端
func (s *HTTPUpstreamSuite) TestEvictOverLimitRemovesOldestIdle() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      2, // 最多缓存 2 个客户端
	}
	svc := s.newService()
	// 创建两个客户端，设置不同的最后使用时间
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 1)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 2, 1)
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Hour).UnixNano()) // 最久
	atomic.StoreInt64(&entry2.lastUsed, time.Now().Add(-time.Hour).UnixNano())
	// 创建第三个客户端，触发淘汰
	_ = mustGetOrCreateClient(s.T(), svc, "http://proxy-c:8080", 3, 1)

	require.LessOrEqual(s.T(), len(svc.clients), 2, "应保持在缓存上限内")
	require.False(s.T(), hasEntry(svc, entry1), "最久未使用的连接池应被清理")
}

// TestIdleTTLDoesNotEvictActive 测试活跃请求保护
// 验证有进行中请求的客户端不会被空闲超时淘汰
func (s *HTTPUpstreamSuite) TestIdleTTLDoesNotEvictActive() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		ClientIdleTTLSeconds:    1, // 1 秒空闲超时
	}
	svc := s.newService()
	entry1 := mustGetOrCreateClient(s.T(), svc, "", 1, 1)
	// 设置为很久之前使用，但有活跃请求
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Minute).UnixNano())
	atomic.StoreInt64(&entry1.inFlight, 1) // 模拟有活跃请求
	// 创建新客户端，触发淘汰检查
	_, _ = svc.getOrCreateClient("", 2, 1)

	require.True(s.T(), hasEntry(svc, entry1), "有活跃请求时不应回收")
}

// TestHTTPUpstreamSuite 运行测试套件
func TestHTTPUpstreamSuite(t *testing.T) {
	suite.Run(t, new(HTTPUpstreamSuite))
}

// mustGetOrCreateClient 测试辅助函数，调用 getOrCreateClient 并断言无错误
func mustGetOrCreateClient(t *testing.T, svc *httpUpstreamService, proxyURL string, accountID int64, concurrency int) *upstreamClientEntry {
	t.Helper()
	entry, err := svc.getOrCreateClient(proxyURL, accountID, concurrency)
	require.NoError(t, err, "getOrCreateClient(%q, %d, %d)", proxyURL, accountID, concurrency)
	return entry
}

// hasEntry 检查客户端是否存在于缓存中
// 辅助函数，用于验证淘汰逻辑
func hasEntry(svc *httpUpstreamService, target *upstreamClientEntry) bool {
	for _, entry := range svc.clients {
		if entry == target {
			return true
		}
	}
	return false
}
