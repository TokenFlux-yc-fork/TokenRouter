package handler

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/ctxkey"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newUsageRecordTestPool(t *testing.T) *service.UsageRecordWorkerPool {
	t.Helper()
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             8,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	return pool
}

func TestGatewayHandlerSubmitUsageRecordTask_WithPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := &GatewayHandler{usageRecordWorkerPool: pool}

	done := make(chan struct{})
	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

func TestGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback(t *testing.T) {
	h := &GatewayHandler{}
	var called atomic.Bool

	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected deadline in fallback context")
		}
		called.Store(true)
	})

	require.True(t, called.Load())
}

func TestGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := &GatewayHandler{}
	require.NotPanics(t, func() {
		h.submitUsageRecordTask(nil, nil)
	})
}

func TestGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := &GatewayHandler{}
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.submitUsageRecordTask(nil, func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithPool(t *testing.T) {
	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	done := make(chan struct{})
	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		close(done)
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPoolSyncFallback(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	var called atomic.Bool

	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected deadline in fallback context")
		}
		called.Store(true)
	})

	require.True(t, called.Load())
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_NilTask(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.submitUsageRecordTask(nil, nil)
	})
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_WithoutPool_TaskPanicRecovered(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	var called atomic.Bool

	require.NotPanics(t, func() {
		h.submitUsageRecordTask(nil, func(ctx context.Context) {
			panic("usage task panic")
		})
	})

	h.submitUsageRecordTask(nil, func(ctx context.Context) {
		called.Store(true)
	})
	require.True(t, called.Load(), "panic 后后续任务应仍可执行")
}

func TestOpenAIGatewayHandlerSubmitMandatoryUsageRecordTask_DroppedTaskSyncFallback(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	block := make(chan struct{})
	release := make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.submitMandatoryUsageRecordTask(nil, func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "mandatory usage task must run synchronously when async submit is dropped")
}

func TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_NativeSettlementGate(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	tests := []struct {
		name   string
		result *service.OpenAIForwardResult
		want   bool
	}{
		{name: "valid committed", result: &service.OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: service.OpenAINativeCompactionValid, DeliveryCommitted: true, AttributionPersisted: true}, want: true},
		{name: "valid committed without durable attribution", result: &service.OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: service.OpenAINativeCompactionValid, DeliveryCommitted: true}, want: false},
		{name: "valid uncommitted", result: &service.OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: service.OpenAINativeCompactionValid}, want: false},
		{name: "invalid committed", result: &service.OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: service.OpenAINativeCompactionZeroCompaction, DeliveryCommitted: true}, want: false},
		{name: "client canceled", result: &service.OpenAIForwardResult{NativeRemoteCompactionV2: true, SemanticOutcome: service.OpenAINativeCompactionValid, DeliveryCommitted: true, ClientDisconnect: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called atomic.Bool
			h.submitOpenAIUsageRecordTask(nil, tt.result, func(context.Context) { called.Store(true) })
			require.Equal(t, tt.want, called.Load())
		})
	}
}

func TestOpenAIGatewayHandlerSubmitOpenAIUsageRecordTask_ImageResultUsesMandatoryFallback(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	block := make(chan struct{})
	release := make(chan struct{})
	pool.Submit(func(ctx context.Context) {
		close(block)
		<-release
	})
	<-block
	pool.Submit(func(ctx context.Context) {})

	var called atomic.Bool
	h.submitOpenAIUsageRecordTask(nil, &service.OpenAIForwardResult{ImageCount: 1}, func(ctx context.Context) {
		called.Store(true)
	})
	close(release)

	require.True(t, called.Load(), "image usage task must be mandatory when async submit is dropped")
}

func TestOpenAIGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), ctxkey.RequestID, "req-123")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-456")
	c.Request = req.WithContext(ctx)

	got := make(chan [2]string, 1)
	h.submitUsageRecordTask(c, func(ctx context.Context) {
		requestID, _ := ctx.Value(ctxkey.RequestID).(string)
		clientRequestID, _ := ctx.Value(ctxkey.ClientRequestID).(string)
		got <- [2]string{requestID, clientRequestID}
	})

	select {
	case ids := <-got:
		require.Equal(t, [2]string{"req-123", "client-456"}, ids)
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}

func TestGatewayHandlerSubmitUsageRecordTask_PreservesRequestIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := newUsageRecordTestPool(t)
	h := &GatewayHandler{usageRecordWorkerPool: pool}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(req.Context(), ctxkey.RequestID, "req-gateway")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-gateway")
	c.Request = req.WithContext(ctx)

	got := make(chan [2]string, 1)
	h.submitUsageRecordTask(c, func(ctx context.Context) {
		requestID, _ := ctx.Value(ctxkey.RequestID).(string)
		clientRequestID, _ := ctx.Value(ctxkey.ClientRequestID).(string)
		got <- [2]string{requestID, clientRequestID}
	})

	select {
	case ids := <-got:
		require.Equal(t, [2]string{"req-gateway", "client-gateway"}, ids)
	case <-time.After(time.Second):
		t.Fatal("task not executed")
	}
}
