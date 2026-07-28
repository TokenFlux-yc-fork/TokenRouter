package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayServiceForwardImmediatelyRetriesNeutralEdgeBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		header     http.Header
		body       string
		blockCount int
	}{
		{
			name:       "consecutive structured request blocked",
			header:     http.Header{"Content-Type": []string{"application/json"}},
			body:       `{"error":{"message":"Your request was blocked."}}`,
			blockCount: 2,
		},
		{
			name: "html edge challenge",
			header: http.Header{
				"Content-Type": []string{"text/html; charset=UTF-8"},
				"Cf-Mitigated": []string{"challenge"},
				"Cf-Ray":       []string{"test-ray-NRT"},
			},
			body:       `<!doctype html><html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt={};</script></body></html>`,
			blockCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody := []byte(`{"model":"gpt-5.2","stream":false,"input":"hello"}`)
			rejectedBodies := make([]*passthroughCloseTrackingReadCloser, 0, tt.blockCount)
			responses := make([]*http.Response, 0, tt.blockCount+1)
			for range tt.blockCount {
				rejectedBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(tt.body)}
				rejectedBodies = append(rejectedBodies, rejectedBody)
				responses = append(responses, &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     tt.header,
					Body:       rejectedBody,
				})
			}
			responses = append(responses, openAIEdgeRetrySuccessResponse())
			upstream := &httpUpstreamRecorder{responses: responses}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
				httpUpstream: upstream,
			}
			account := openAIEdgeRetryTestAccount()

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

			result, err := svc.Forward(context.Background(), c, account, requestBody)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "resp_edge_retry", result.ResponseID)
			require.Len(t, upstream.requests, tt.blockCount+1)
			for _, attemptBody := range upstream.bodies[1:] {
				require.Equal(t, upstream.bodies[0], attemptBody)
			}
			require.Equal(t, "gpt-5.2", gjson.GetBytes(upstream.bodies[0], "model").String())
			require.Equal(t, "hello", gjson.GetBytes(upstream.bodies[0], "input").String())
			for _, rejectedBody := range rejectedBodies {
				require.True(t, rejectedBody.closed)
			}
			require.NotContains(t, rec.Body.String(), tt.body)
			require.Contains(t, rec.Body.String(), `"id":"resp_edge_retry"`)
			_, recorded := c.Get(OpsUpstreamErrorsKey)
			require.False(t, recorded)
		})
	}
}

func TestOpenAIGatewayServiceForwardStopsNeutralEdgeBlockRetryWhenContextCanceled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	requestBody := []byte(`{"model":"gpt-5.2","stream":false,"input":"hello"}`)
	rejectedBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(
		`{"error":{"message":"Your request was blocked."}}`,
	)}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       rejectedBody,
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := svc.Forward(ctx, c, openAIEdgeRetryTestAccount(), requestBody)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, upstream.requests, 1)
	require.True(t, rejectedBody.closed)
	require.False(t, c.Writer.Written())
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)
}

func TestOpenAIGatewayServiceForwardOrdinaryForbiddenStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	requestBody := []byte(`{"model":"gpt-5.2","stream":false,"input":"hello"}`)
	rejectedBody := &passthroughCloseTrackingReadCloser{Reader: strings.NewReader(
		`{"error":{"message":"API key does not have access to this resource."}}`,
	)}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       rejectedBody,
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	result, err := svc.Forward(context.Background(), c, openAIEdgeRetryTestAccount(), requestBody)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Len(t, upstream.requests, 1)
	require.True(t, rejectedBody.closed)
	require.False(t, c.Writer.Written())
}

func openAIEdgeRetryTestAccount() *Account {
	return &Account{
		ID:          181,
		Name:        "edge-retry-account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.example.test",
		},
		Extra: map[string]any{
			"openai_responses_supported": true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func openAIEdgeRetrySuccessResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-edge-retry"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_edge_retry","object":"response","model":"gpt-5.2","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}
}
