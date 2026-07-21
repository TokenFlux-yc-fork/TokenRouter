package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestQoderOAuthHandlerGenerateAuthURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	svc := service.NewQoderOAuthService(nil)
	defer svc.Stop()
	handler := NewQoderOAuthHandler(svc)
	router.POST("/api/v1/admin/qoder/oauth/auth-url", handler.GenerateAuthURL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/qoder/oauth/auth-url", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, data["auth_url"], "https://qoder.com.cn/device/selectAccounts")
	require.Equal(t, "cn", data["site"])
	require.NotEmpty(t, data["session_id"])
	require.NotEmpty(t, data["state"])
	require.NotZero(t, data["expires_in"])
	require.NotZero(t, data["interval"])
}

func TestQoderOAuthHandlerExchangeCodeValidatesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	svc := service.NewQoderOAuthService(nil)
	defer svc.Stop()
	handler := NewQoderOAuthHandler(svc)
	router.POST("/api/v1/admin/qoder/oauth/exchange-code", handler.ExchangeCode)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/qoder/oauth/exchange-code", bytes.NewBufferString(`{"state":"state"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "SessionID")
}

func TestQoderOAuthHandlerGenerateAuthURLAcceptsCNAndRejectsLegacyOrUnknownSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	svc := service.NewQoderOAuthService(nil)
	defer svc.Stop()
	handler := NewQoderOAuthHandler(svc)
	router.POST("/api/v1/admin/qoder/oauth/auth-url", handler.GenerateAuthURL)

	cnRecorder := httptest.NewRecorder()
	cnRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/qoder/oauth/auth-url", bytes.NewBufferString(`{"site":"cn"}`))
	cnRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(cnRecorder, cnRequest)
	require.Equal(t, http.StatusOK, cnRecorder.Code)
	require.Contains(t, cnRecorder.Body.String(), "qoder.com.cn")
	require.Contains(t, cnRecorder.Body.String(), `"site":"cn"`)

	globalRecorder := httptest.NewRecorder()
	globalRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/qoder/oauth/auth-url", bytes.NewBufferString(`{"site":"global"}`))
	globalRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(globalRecorder, globalRequest)
	require.Equal(t, http.StatusBadRequest, globalRecorder.Code)
	require.Contains(t, globalRecorder.Body.String(), "Qoder CN")

	invalidRecorder := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/v1/admin/qoder/oauth/auth-url", bytes.NewBufferString(`{"site":"invalid"}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(invalidRecorder, invalidRequest)
	require.Equal(t, http.StatusBadRequest, invalidRecorder.Code)
	require.Contains(t, invalidRecorder.Body.String(), "unsupported site")
}
