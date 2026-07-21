package qoder

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testSession() *SessionContext {
	return &SessionContext{
		CosyKey: "test-cosy-key",
		Info:    "test-info",
		Identity: &AuthIdentity{
			Name:           "test",
			UID:            "u-123",
			AID:            "a-456",
			OrganizationID: "org-789",
		},
		Machine: &MachineIdentity{
			MachineID:    "mid-abc",
			MachineToken: "mytoken",
			MachineType:  "5",
		},
	}
}

func TestCNClientUsesGatewayEndpointVersionAndCanonicalSignaturePath(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	client.MachineOS = "aarch64_darwin"
	session := testSession()
	session.Site = SiteCN
	session.ClientVersion = CNClientVersion
	var captured *http.Request
	var encodedBody string
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		encodedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
			Request:    req,
		}, nil
	}

	resp, err := client.StreamRequestContextWithDoer(context.Background(), session, "", []byte(`{"model":"auto"}`), nil, doer)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "gateway.example", captured.URL.Host)
	require.Equal(t, "/algo/api/v2/service/pro/sse/agent_chat_generation", captured.URL.Path)
	require.Equal(t, CNClientVersion, captured.Header.Get("Cosy-Version"))
	require.Equal(t, "aarch64_darwin", captured.Header.Get("Cosy-Machineos"))
	require.Equal(t, "mid-abc", captured.Header.Get("Cosy-Machineid"))
	require.Equal(t, "mid-abc", captured.Header.Get("Cosy-Machinetoken"))
	require.Equal(t, "5", captured.Header.Get("Cosy-Machinetype"))
	require.NotContains(t, captured.Header, "Cosy-Machinecode")
	require.Equal(t, "5", captured.Header.Get("Cosy-Clienttype"))
	require.Equal(t, "agree", captured.Header.Get("Cosy-Data-Policy"))
	require.Equal(t, "assistant", captured.Header.Get("Cosy-Scene"))
	require.Equal(t, "cli", captured.Header.Get("Cosy-Business-Product"))
	require.Equal(t, "agent", captured.Header.Get("Cosy-Business-Type"))
	require.NotContains(t, captured.Header, "Cosy-Clientip")
	require.NotContains(t, captured.Header, "Date")
	require.NotContains(t, captured.Header, "Signature")
	require.NotContains(t, captured.Header, "Appcode")
	require.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, captured.Header.Get("traceparent"))

	authorization := strings.TrimPrefix(captured.Header.Get("Authorization"), "Bearer COSY.")
	parts := strings.Split(authorization, ".")
	require.Len(t, parts, 2)
	expectedSignature := SignQoderRequest(
		parts[0],
		session.CosyKey,
		captured.Header.Get("Cosy-Date"),
		encodedBody,
		"/api/v2/service/pro/sse/agent_chat_generation",
	)
	require.Equal(t, expectedSignature, parts[1])
}

func TestJSONRequestAddsEncodeQueryWithoutSigningIt(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	session := testSession()
	session.Site = SiteCN
	var captured *http.Request
	var encodedBody string
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		encodedBody = string(body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}

	err := client.JSONRequestContextWithDoer(
		context.Background(),
		http.MethodPost,
		session,
		"/api/v3/user/status?region=cn",
		[]byte(`{"userId":"user-1"}`),
		nil,
		doer,
		&map[string]any{},
	)
	require.NoError(t, err)
	require.Equal(t, "/algo/api/v3/user/status", captured.URL.Path)
	require.Equal(t, "1", captured.URL.Query().Get("Encode"))
	require.Equal(t, "cn", captured.URL.Query().Get("region"))

	authorization := strings.TrimPrefix(captured.Header.Get("Authorization"), "Bearer COSY.")
	parts := strings.Split(authorization, ".")
	require.Len(t, parts, 2)
	expectedSignature := SignQoderRequest(
		parts[0],
		session.CosyKey,
		captured.Header.Get("Cosy-Date"),
		encodedBody,
		"/api/v3/user/status",
	)
	require.Equal(t, expectedSignature, parts[1])
}

func TestSignatureJSONRequestUsesAppcodeHeadersWithoutAuthorization(t *testing.T) {
	profile := MustProfileForSite(SiteCN)
	profile.GatewayBaseURL = "https://gateway.example"
	client := NewClientForProfile(profile)
	client.ClientIP = "172.18.0.1"
	session := testSession()
	session.Identity = nil
	session.Site = SiteCN
	var captured *http.Request
	doer := func(req *http.Request) (*http.Response, error) {
		captured = req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	}

	err := client.SignatureJSONRequestContextWithDoer(
		context.Background(),
		http.MethodPost,
		session,
		AuthStatusPath,
		[]byte(`{"userId":"user-1"}`),
		nil,
		doer,
		&map[string]any{},
	)

	require.NoError(t, err)
	require.Equal(t, "1", captured.URL.Query().Get("Encode"))
	require.Equal(t, "5", captured.Header.Get("Cosy-Clienttype"))
	require.NotEmpty(t, captured.Header.Get("Date"))
	require.Equal(t, AppCode, captured.Header.Get("Appcode"))
	require.NotEmpty(t, captured.Header.Get("Signature"))
	require.NotContains(t, captured.Header, "Authorization")
	require.NotContains(t, captured.Header, "Cosy-Key")
	require.NotContains(t, captured.Header, "Cosy-User")
	require.NotContains(t, captured.Header, "Cosy-Date")
	require.NotContains(t, captured.Header, "Cosy-Data-Policy")
	require.NotContains(t, captured.Header, "Cosy-Organization-Id")
	require.NotContains(t, captured.Header, "Cosy-Organization-Tags")
}

func getHeaders(t *testing.T) http.Header {
	t.Helper()
	c := NewClient("https://test.qoder.sh")
	req, _ := http.NewRequest("POST", "https://test.qoder.sh/test", nil)
	c.setHeaders(req, testSession(), "/test", "encoded-body")
	return req.Header
}

func TestHeadersOmitClientIPForQoderCLICN(t *testing.T) {
	h := getHeaders(t)
	require.NotContains(t, h, "cosy-clientip")
}

func TestHeadersMachineTypeIs5(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-machinetype") != "5" {
		t.Errorf("cosy-machinetype = %q, want 5", h.Get("cosy-machinetype"))
	}
}

func TestHeadersDeriveMachineTokenFromMachineID(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-machinetoken") != "mid-abc" {
		t.Errorf("cosy-machinetoken = %q, want mid-abc", h.Get("cosy-machinetoken"))
	}
}

func TestHeadersUseSessionDataPolicy(t *testing.T) {
	session := testSession()
	session.DataPolicy = "DISAGREE"
	client := NewClient("")
	req, _ := http.NewRequest("POST", "https://gateway.qoder.com.cn/test", nil)
	client.setHeaders(req, session, "/test", "encoded-body")
	h := req.Header
	if h.Get("cosy-data-policy") != "disagree" {
		t.Errorf("cosy-data-policy = %q, want disagree", h.Get("cosy-data-policy"))
	}
}

func TestHeadersUseQoderCLICNVersion(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-version") != CNClientVersion {
		t.Errorf("cosy-version = %q, want %s", h.Get("cosy-version"), CNClientVersion)
	}
}

func TestHeadersIncludeMachineOSAndLegacyFallbacks(t *testing.T) {
	session := testSession()
	session.Machine.MachineToken = ""
	session.Machine.MachineType = ""
	client := NewClient("https://test.qoder.sh")
	client.MachineOS = "aarch64_darwin"
	req, _ := http.NewRequest("POST", "https://test.qoder.sh/test", nil)
	client.setHeaders(req, session, "/test", "encoded-body")
	require.Equal(t, session.Machine.MachineID, req.Header.Get("cosy-machinetoken"))
	require.Equal(t, "5", req.Header.Get("cosy-machinetype"))
	require.Equal(t, "aarch64_darwin", req.Header.Get("cosy-machineos"))
}

func TestCNHeadersUseQoderCLICNWireIdentity(t *testing.T) {
	session := testSession()
	session.Site = SiteCN
	client := NewClientForProfile(MustProfileForSite(SiteCN))
	client.ClientIP = "172.18.0.1"
	req, _ := http.NewRequest("POST", "https://gateway.qoder.com.cn/test", nil)

	client.setHeaders(req, session, "/test", "encoded-body")

	require.Equal(t, "mid-abc", req.Header.Get("cosy-machineid"))
	require.Equal(t, "mid-abc", req.Header.Get("cosy-machinetoken"))
	require.Equal(t, "5", req.Header.Get("cosy-machinetype"))
	require.NotContains(t, req.Header, "cosy-machinecode")
	require.NotContains(t, req.Header, "cosy-clientip")
	require.Equal(t, "5", req.Header.Get("cosy-clienttype"))
	require.Equal(t, "agree", req.Header.Get("cosy-data-policy"))
	require.Equal(t, "assistant", req.Header.Get("Cosy-Scene"))
	require.Equal(t, "cli", req.Header.Get("Cosy-Business-Product"))
	require.Equal(t, "agent", req.Header.Get("Cosy-Business-Type"))
}

func TestHeadersOrganizationID(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-organization-id") != "org-789" {
		t.Errorf("cosy-organization-id = %q, want org-789", h.Get("cosy-organization-id"))
	}
}

func TestHeadersOmitOrganizationTagsForQoderCLICN(t *testing.T) {
	h := getHeaders(t)
	require.NotContains(t, h, "cosy-organization-tags")
}

func TestHeadersScene(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-scene") != "assistant" {
		t.Errorf("cosy-scene = %q, want assistant", h.Get("cosy-scene"))
	}
}

func TestHeadersBusinessProduct(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-business-product") != "cli" {
		t.Errorf("cosy-business-product = %q, want cli", h.Get("cosy-business-product"))
	}
}

func TestHeadersBusinessType(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-business-type") != "agent" {
		t.Errorf("cosy-business-type = %q, want agent", h.Get("cosy-business-type"))
	}
}

func TestHeadersNoHardcodedIP(t *testing.T) {
	h := getHeaders(t)
	if h.Get("cosy-clientip") == "169.254.198.161" {
		t.Error("cosy-clientip should not be hardcoded 169.254.198.161")
	}
}
