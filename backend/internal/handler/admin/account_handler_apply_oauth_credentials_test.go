package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

type applyOAuthTokenInvalidator struct {
	accounts []*service.Account
}

func (i *applyOAuthTokenInvalidator) InvalidateToken(ctx context.Context, account *service.Account) error {
	i.accounts = append(i.accounts, account)
	return nil
}

func TestAccountHandlerApplyOAuthCredentials_MergesExtraAndInvalidatesToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	invalidator := &applyOAuthTokenInvalidator{}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, invalidator)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	payload := map[string]any{
		"type": "oauth",
		"credentials": map[string]any{
			"access_token": "new-access-token",
			"expires_at":   "1893456000",
		},
		"extra": map[string]any{
			"account_uuid": "new-account-uuid",
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodPost, "/api/v1/admin/accounts/3/apply-oauth-credentials", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, adminSvc.updateAccountInput)
	require.Equal(t, service.AccountTypeOAuth, adminSvc.updateAccountInput.Type)
	require.Equal(t, "new-access-token", adminSvc.updateAccountInput.Credentials["access_token"])
	require.Nil(t, adminSvc.updateAccountInput.Extra, "凭据更新不应全量覆盖 Extra")
	require.Len(t, adminSvc.updateExtraCalls, 1)
	require.Equal(t, "new-account-uuid", adminSvc.updateExtraCalls[0]["account_uuid"])
	require.Equal(t, []int64{int64(3)}, adminSvc.clearAccountErrorIDs)
	require.Len(t, invalidator.accounts, 1)
	require.Equal(t, int64(3), invalidator.accounts[0].ID)
}

func TestAccountHandlerApplyOAuthCredentials_AcceptsQoderCosyReauthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{{
		ID:       9,
		Name:     "qoder-cn",
		Platform: service.PlatformQoder,
		Type:     service.AccountTypeCosy,
		Status:   service.StatusError,
		Credentials: map[string]any{
			"site": "global",
			"pat":  "legacy-pat",
		},
	}}
	invalidator := &applyOAuthTokenInvalidator{}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, invalidator)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	payload := map[string]any{
		"type": service.AccountTypeCosy,
		"credentials": map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "new-token",
			"refresh_token":        "new-refresh",
			"machine_id":           "new-machine",
			"uid":                  "new-uid",
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/9/apply-oauth-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, adminSvc.updateAccountInput)
	require.Equal(t, "new-token", adminSvc.applyQoderCredentials["security_oauth_token"])
	require.Equal(t, "new-refresh", adminSvc.applyQoderCredentials["refresh_token"])
	require.Empty(t, adminSvc.updateExtraCalls)
	require.Empty(t, adminSvc.clearAccountErrorIDs)
	require.Len(t, invalidator.accounts, 1)
}

func TestAccountHandlerApplyOAuthCredentials_QoderAtomicFailureDoesNotRunFollowUpMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{{
		ID:       9,
		Platform: service.PlatformQoder,
		Type:     service.AccountTypeCosy,
		Status:   service.StatusError,
	}}
	adminSvc.applyQoderErr = errors.New("atomic outbox write failed")
	invalidator := &applyOAuthTokenInvalidator{}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, invalidator)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/9/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"cosy","credentials":{"site":"cn","pat":"new-pat"}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Empty(t, adminSvc.updateExtraCalls)
	require.Empty(t, adminSvc.clearAccountErrorIDs)
	require.Empty(t, invalidator.accounts)
}

func TestAccountHandlerApplyOAuthCredentials_PreservesAccountLookupErrorStatus(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		statusCode int
	}{
		{name: "not found", err: service.ErrAccountNotFound, statusCode: http.StatusNotFound},
		{name: "repository failure", err: errors.New("database unavailable"), statusCode: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			adminSvc := newStubAdminService()
			adminSvc.getAccountErr = tt.err
			handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/9/apply-oauth-credentials", bytes.NewBufferString(
				`{"type":"cosy","credentials":{"site":"cn","pat":"new-pat"}}`,
			))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.statusCode, w.Code)
		})
	}
}

func TestAccountHandlerApplyOAuthCredentials_RejectsCosyTypeForNonQoderAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/3/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"cosy","credentials":{"site":"cn","pat":"new-pat"}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Nil(t, adminSvc.updateAccountInput)
}

func TestAccountHandlerApplyOAuthCredentials_RejectsIncompleteQoderReauthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{{
		ID:          9,
		Platform:    service.PlatformQoder,
		Type:        service.AccountTypeCosy,
		Status:      service.StatusError,
		Schedulable: false,
		Credentials: map[string]any{
			"site":                 "cn",
			"refresh_mode":         "qodercn20",
			"security_oauth_token": "old-token",
			"refresh_token":        "old-refresh",
			"machine_id":           "old-machine",
			"uid":                  "old-user",
		},
	}}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/apply-oauth-credentials", handler.ApplyOAuthCredentials)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/9/apply-oauth-credentials", bytes.NewBufferString(
		`{"type":"cosy","credentials":{"site":"cn","refresh_mode":"qodercn20","machine_id":"new-machine","uid":"new-user"}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Nil(t, adminSvc.updateAccountInput)
	require.Empty(t, adminSvc.clearAccountErrorIDs)
}

func TestAccountHandlerUpdate_QoderAuthorizationEndpointErrorDoesNotInvalidateToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.updateAccountErr = infraerrors.BadRequest(
		"QODER_AUTHORIZATION_ENDPOINT_REQUIRED",
		"Qoder authorization credentials must be applied through the reauthorization endpoint",
	)
	invalidator := &applyOAuthTokenInvalidator{}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, invalidator)
	router := gin.New()
	router.PUT("/api/v1/admin/accounts/:id", handler.Update)
	payload := `{"credentials":{"site":"cn","pat":"new-pat","machine_id":"new-machine","_token_version":12}}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/9", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, invalidator.accounts)
}

func TestAccountHandlerUpdate_QoderAuthorizationErrorsPreserveHTTPStatus(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		statusCode int
		reason     string
	}{
		{
			name:       "incomplete reauthorization",
			err:        service.ErrQoderCNReauthorizationRequired,
			statusCode: http.StatusBadRequest,
			reason:     "QODER_CN_REAUTH_REQUIRED",
		},
		{
			name:       "concurrent authorization conflict",
			err:        service.ErrQoderAuthorizationConflict,
			statusCode: http.StatusConflict,
			reason:     "QODER_AUTHORIZATION_CONFLICT",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			adminSvc := newStubAdminService()
			adminSvc.updateAccountErr = tt.err
			handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			router := gin.New()
			router.PUT("/api/v1/admin/accounts/:id", handler.Update)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/9", bytes.NewBufferString(`{}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, tt.statusCode, w.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, tt.reason, body["reason"])
		})
	}
}
