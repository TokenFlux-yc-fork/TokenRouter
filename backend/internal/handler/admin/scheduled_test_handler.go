package admin

import (
	"net/http"
	"strconv"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// ScheduledTestHandler handles admin scheduled-test-plan management.
type ScheduledTestHandler struct {
	scheduledTestSvc *service.ScheduledTestService
}

// NewScheduledTestHandler creates a new ScheduledTestHandler.
func NewScheduledTestHandler(scheduledTestSvc *service.ScheduledTestService) *ScheduledTestHandler {
	return &ScheduledTestHandler{scheduledTestSvc: scheduledTestSvc}
}

type createScheduledTestPlanRequest struct {
	AccountID                    int64  `json:"account_id" binding:"required"`
	ModelID                      string `json:"model_id"`
	CronExpression               string `json:"cron_expression" binding:"required"`
	Enabled                      *bool  `json:"enabled"`
	MaxResults                   int    `json:"max_results"`
	AutoRecover                  *bool  `json:"auto_recover"`
	AccountCircuitBreakerEnabled *bool  `json:"account_circuit_breaker_enabled"`
	FailureThreshold             int    `json:"failure_threshold"`
	SuccessThreshold             int    `json:"success_threshold"`
	FailureCooldownMinutes       int    `json:"failure_cooldown_minutes"`
	TimeoutSeconds               int    `json:"timeout_seconds"`
}

type updateScheduledTestPlanRequest struct {
	ModelID                      string `json:"model_id"`
	CronExpression               string `json:"cron_expression"`
	Enabled                      *bool  `json:"enabled"`
	MaxResults                   int    `json:"max_results"`
	AutoRecover                  *bool  `json:"auto_recover"`
	AccountCircuitBreakerEnabled *bool  `json:"account_circuit_breaker_enabled"`
	FailureThreshold             int    `json:"failure_threshold"`
	SuccessThreshold             int    `json:"success_threshold"`
	FailureCooldownMinutes       int    `json:"failure_cooldown_minutes"`
	TimeoutSeconds               int    `json:"timeout_seconds"`
}

// ListByAccount GET /admin/accounts/:id/scheduled-test-plans
func (h *ScheduledTestHandler) ListByAccount(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid account id")
		return
	}

	plans, err := h.scheduledTestSvc.ListPlansByAccount(c.Request.Context(), accountID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, plans)
}

// Create POST /admin/scheduled-test-plans
func (h *ScheduledTestHandler) Create(c *gin.Context) {
	var req createScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	plan := &service.ScheduledTestPlan{
		AccountID:              req.AccountID,
		ModelID:                req.ModelID,
		CronExpression:         req.CronExpression,
		Enabled:                true,
		MaxResults:             req.MaxResults,
		FailureThreshold:       req.FailureThreshold,
		SuccessThreshold:       req.SuccessThreshold,
		FailureCooldownMinutes: req.FailureCooldownMinutes,
		TimeoutSeconds:         req.TimeoutSeconds,
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AutoRecover != nil {
		plan.AutoRecover = *req.AutoRecover
	}
	if req.AccountCircuitBreakerEnabled != nil {
		plan.AccountCircuitBreakerEnabled = *req.AccountCircuitBreakerEnabled
	}

	created, err := h.scheduledTestSvc.CreatePlan(c.Request.Context(), plan)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, created)
}

// Update PUT /admin/scheduled-test-plans/:id
func (h *ScheduledTestHandler) Update(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	existing, err := h.scheduledTestSvc.GetPlan(c.Request.Context(), planID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}

	var req updateScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if req.ModelID != "" {
		existing.ModelID = req.ModelID
	}
	if req.CronExpression != "" {
		existing.CronExpression = req.CronExpression
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.MaxResults > 0 {
		existing.MaxResults = req.MaxResults
	}
	if req.AutoRecover != nil {
		existing.AutoRecover = *req.AutoRecover
	}
	if req.AccountCircuitBreakerEnabled != nil {
		existing.AccountCircuitBreakerEnabled = *req.AccountCircuitBreakerEnabled
	}
	if req.FailureThreshold > 0 {
		existing.FailureThreshold = req.FailureThreshold
	}
	if req.SuccessThreshold > 0 {
		existing.SuccessThreshold = req.SuccessThreshold
	}
	if req.FailureCooldownMinutes > 0 {
		existing.FailureCooldownMinutes = req.FailureCooldownMinutes
	}
	if req.TimeoutSeconds > 0 {
		existing.TimeoutSeconds = req.TimeoutSeconds
	}

	updated, err := h.scheduledTestSvc.UpdatePlan(c.Request.Context(), existing)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete DELETE /admin/scheduled-test-plans/:id
func (h *ScheduledTestHandler) Delete(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	if err := h.scheduledTestSvc.DeletePlan(c.Request.Context(), planID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ListResults GET /admin/scheduled-test-plans/:id/results
func (h *ScheduledTestHandler) ListResults(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}

	results, err := h.scheduledTestSvc.ListResults(c.Request.Context(), planID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}

// ListAccountResults GET /admin/accounts/:id/scheduled-test-results
func (h *ScheduledTestHandler) ListAccountResults(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid account id")
		return
	}

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}

	results, err := h.scheduledTestSvc.ListResultsByAccount(c.Request.Context(), accountID, limit)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}
