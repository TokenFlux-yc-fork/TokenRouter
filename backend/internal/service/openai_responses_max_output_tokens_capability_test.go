package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesMaxOutputTokensCapabilityIdentityAndStructuredRejection(t *testing.T) {
	account := newOpenAIRejectedFieldTestAccount()
	key, err := ResolveOpenAIResponsesMaxOutputTokensCapabilityKey(account, "gpt-5.5")
	require.NoError(t, err)
	require.True(t, key.Valid())
	require.NotEqual(t, key, func() OpenAIResponsesMaxOutputTokensCapabilityKey { k := key; k.EffectiveModel = "other"; return k }())
	require.True(t, IsExplicitOpenAIResponsesMaxOutputTokensUnsupported(http.StatusBadRequest,
		[]byte(`{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}`)))
	for _, body := range []string{
		`{"error":{"code":"invalid_request_error","param":"max_output_tokens","message":"invalid value"}}`,
		`{"detail":"Unsupported parameter: max_output_tokens"}`,
		`{"error":{"code":"unsupported_parameter","param":"input.max_output_tokens","message":"Unsupported parameter: input.max_output_tokens"}}`,
	} {
		require.False(t, IsExplicitOpenAIResponsesMaxOutputTokensUnsupported(http.StatusBadRequest, []byte(body)))
	}
	require.False(t, IsExplicitOpenAIResponsesMaxOutputTokensUnsupported(http.StatusTooManyRequests, []byte(`{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}`)))
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyStrictOutputLimit(t *testing.T) {
	body := []byte(`{"max_output_tokens":100}`)
	response := []byte(`{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"Unsupported parameter: max_output_tokens"}}`)
	retry, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBodyWithPolicy(http.StatusBadRequest, body, response, true)
	require.NoError(t, err)
	require.False(t, changed)
	require.Nil(t, retry)
}
