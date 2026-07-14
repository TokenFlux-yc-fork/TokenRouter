package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIOAuthCapacityAdminServiceStub struct {
	service.AdminService
	result *service.OpenAIOAuthPoolCapacitySummary
	err    error
}

func (s *openAIOAuthCapacityAdminServiceStub) GetOpenAIOAuthPoolCapacity(context.Context) (*service.OpenAIOAuthPoolCapacitySummary, error) {
	return s.result, s.err
}

func TestAccountHandlerGetOpenAIOAuthPoolCapacity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := &openAIOAuthCapacityAdminServiceStub{
		result: &service.OpenAIOAuthPoolCapacitySummary{
			GeneratedAt:          "2026-07-14T12:00:00Z",
			FiveHourRatio:        0.15,
			IncludedAccountCount: 3,
			Groups: []service.OpenAIOAuthPoolCapacityGroupSummary{{
				GroupID:   7,
				GroupName: "Primary",
				OpenAIOAuthPoolCapacityBreakdown: service.OpenAIOAuthPoolCapacityBreakdown{
					IncludedAccountCount: 2,
				},
			}},
		},
	}
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/accounts/openai-oauth-capacity", handler.GetOpenAIOAuthPoolCapacity)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/openai-oauth-capacity", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data service.OpenAIOAuthPoolCapacitySummary `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "2026-07-14T12:00:00Z", payload.Data.GeneratedAt)
	require.Equal(t, 0.15, payload.Data.FiveHourRatio)
	require.Equal(t, 3, payload.Data.IncludedAccountCount)
	require.Len(t, payload.Data.Groups, 1)
	require.Equal(t, int64(7), payload.Data.Groups[0].GroupID)
	require.Equal(t, 2, payload.Data.Groups[0].IncludedAccountCount)
}

func TestAccountHandlerGetOpenAIOAuthPoolCapacityFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := &openAIOAuthCapacityAdminServiceStub{err: errors.New("query failed")}
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/accounts/openai-oauth-capacity", handler.GetOpenAIOAuthPoolCapacity)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/openai-oauth-capacity", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}
