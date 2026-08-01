package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type attemptTimelineCaptureRepo struct {
	service.OpsRepository
	requestID string
}

func (r *attemptTimelineCaptureRepo) ListAttemptTimeline(_ context.Context, requestID string) ([]*service.OpsAttemptTimelineItem, error) {
	r.requestID = requestID
	return []*service.OpsAttemptTimelineItem{{AtUnixMs: 2, Index: 1, Platform: "openai"}, {AtUnixMs: 1, Index: 0}}, nil
}

func TestOpsHandlerGetAttemptTimeline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &attemptTimelineCaptureRepo{}
	h := NewOpsHandler(service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/requests/:request_id/attempt-timeline", h.GetAttemptTimeline)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/requests/request-1/attempt-timeline", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "request-1", repo.requestID)

	var response struct {
		Code int                               `json:"code"`
		Data []*service.OpsAttemptTimelineItem `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, 0, response.Code)
	require.Equal(t, []int64{1, 2}, []int64{response.Data[0].AtUnixMs, response.Data[1].AtUnixMs})
}

func TestOpsHandlerGetAttemptTimelineRequiresRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/attempt-timeline", h.GetAttemptTimeline)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/attempt-timeline", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOpsHandlerGetAttemptTimelineEmptyDataIsArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpsHandler(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	r := gin.New()
	r.GET("/requests/:request_id/attempt-timeline", h.GetAttemptTimeline)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/requests/request-1/attempt-timeline", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "[]", string(response.Data))
}
