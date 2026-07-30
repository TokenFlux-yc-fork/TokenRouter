package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAINativeCompactionTurnUsesEachWebSocketPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("x-codex-beta-features", "responses_websockets_v2,remote_compaction_v2")
	MarkOpenAINativeRemoteCompactionV2(c)

	require.True(t, isOpenAINativeCompactionTurn(c, []byte(`{"type":"response.create","input":[{"type":"compaction_trigger"}]}`)))
	require.False(t, isOpenAINativeCompactionTurn(c, []byte(`{"type":"response.create","input":[{"type":"message","role":"user"}]}`)))
	require.True(t, isOpenAINativeCompactionTurn(c, nil), "empty payload retains the HTTP request marker fallback")
}

func TestOpenAINativeCompactionAttemptStageCommitAndDiscard(t *testing.T) {
	budget := NewOpenAIStageBudget(64)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes:    32,
		MaxEvents:   2,
		MaxDuration: time.Second,
		Budget:      budget,
	})
	require.NoError(t, err)

	require.NoError(t, stage.StageEvent([]byte("one")))
	require.NoError(t, stage.StageEvent([]byte("two")))
	require.EqualValues(t, 6, budget.Reserved())

	var downstream bytes.Buffer
	require.NoError(t, stage.CommitTo(&downstream))
	require.Equal(t, "onetwo", downstream.String())
	require.True(t, stage.Committed())
	require.Zero(t, budget.Reserved())
	require.NoError(t, stage.Close())
}

func TestOpenAINativeCompactionAttemptStageLimitsAreAtomic(t *testing.T) {
	tests := []struct {
		name      string
		config    OpenAINativeCompactionStageConfig
		first     []byte
		second    []byte
		wantError error
	}{
		{
			name:   "bytes",
			config: OpenAINativeCompactionStageConfig{MaxBytes: 4, MaxEvents: 2, MaxDuration: time.Second},
			first:  []byte("1234"), second: []byte("5"), wantError: ErrOpenAINativeCompactionStageBytes,
		},
		{
			name:   "events",
			config: OpenAINativeCompactionStageConfig{MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second},
			first:  []byte("1"), second: []byte("2"), wantError: ErrOpenAINativeCompactionStageEvents,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), tt.config)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stage.Close() })
			require.NoError(t, stage.StageEvent(tt.first))
			require.ErrorIs(t, stage.StageEvent(tt.second), tt.wantError)
			require.EqualValues(t, len(tt.first), stage.Buffered())
			require.Equal(t, 1, stage.Events())
		})
	}
}

func TestOpenAINativeCompactionAttemptStageBudgetAndContext(t *testing.T) {
	budget := NewOpenAIStageBudget(4)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 2, MaxDuration: time.Second, Budget: budget,
	})
	require.NoError(t, err)
	require.NoError(t, stage.StageEvent([]byte("1234")))
	require.ErrorIs(t, stage.StageEvent([]byte("5")), ErrOpenAINativeCompactionStageBudget)
	require.EqualValues(t, 4, stage.Buffered())
	require.NoError(t, stage.Discard())
	require.Zero(t, budget.Reserved())

	requestBudget := NewOpenAIStageBudget(4)
	first, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 4, MaxEvents: 1, MaxDuration: time.Second, RequestBudget: requestBudget,
	})
	require.NoError(t, err)
	require.NoError(t, first.StageEvent([]byte("1234")))
	require.NoError(t, first.Discard())
	require.EqualValues(t, 4, requestBudget.Reserved(), "failed attempt bytes remain charged to the request budget")
	second, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 4, MaxEvents: 1, MaxDuration: time.Second, RequestBudget: requestBudget,
	})
	require.NoError(t, err)
	require.ErrorIs(t, second.StageEvent([]byte("x")), ErrOpenAINativeCompactionStageBudget)
	require.NoError(t, second.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := NewOpenAINativeCompactionAttemptStage(ctx, OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second,
	})
	require.NoError(t, err)
	require.ErrorIs(t, cancelled.StageEvent([]byte("x")), context.Canceled)
	require.NoError(t, cancelled.Close())
}

func TestOpenAINativeCompactionAttemptStageMemoryOnlyFailsClosedAboveThresholdWithoutLeakingBudget(t *testing.T) {
	budget := NewOpenAIStageBudget(8)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MemoryThreshold: 4,
		MaxBytes:        8,
		MaxEvents:       2,
		MaxDuration:     time.Second,
		Budget:          budget,
	})
	require.NoError(t, err)
	stage.buffer.memoryOnly = true

	err = stage.StageEvent([]byte("12345"))
	require.ErrorContains(t, err, "staging limit exceeded")
	require.Zero(t, stage.Buffered())
	require.Zero(t, stage.Events())
	require.Zero(t, budget.Reserved())
	require.True(t, stage.closed)
	require.Zero(t, stage.buffer.memory.Len())
	require.Nil(t, stage.buffer.tempFile)
	require.Empty(t, stage.buffer.tempPath)
	require.NoError(t, stage.Close())
}

func TestOpenAINativeCompactionAttemptStageFailsClosedWhenSpoolCannotUnlink(t *testing.T) {
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MemoryThreshold: 4,
		MaxBytes:        8,
		MaxEvents:       1,
		MaxDuration:     time.Second,
	})
	require.NoError(t, err)
	stage.buffer.memoryOnly = false
	stage.buffer.removeFile = func(string) error { return errors.New("synthetic unlink failure") }
	t.Cleanup(func() { stage.buffer.removeFile = os.Remove; _ = stage.Close() })

	require.Error(t, stage.StageEvent([]byte("12345")))
	require.Zero(t, stage.Buffered())
	require.Zero(t, stage.Events())
}

func TestOpenAINativeCompactionAttemptStagePartialWritePoisonsAndCleansStage(t *testing.T) {
	processBudget := NewOpenAIStageBudget(8)
	requestBudget := NewOpenAIStageBudget(8)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 2, MaxDuration: time.Second,
		Budget: processBudget, RequestBudget: requestBudget,
	})
	require.NoError(t, err)
	require.NoError(t, stage.StageEvent([]byte("ok")))
	stage.buffer.writeData = func(dst io.Writer, data []byte) (int, error) {
		n, writeErr := dst.Write(data[:2])
		require.NoError(t, writeErr)
		return n, errors.New("synthetic partial write")
	}

	require.ErrorContains(t, stage.StageEvent([]byte("1234")), "synthetic partial write")
	require.Zero(t, stage.Buffered())
	require.Equal(t, 1, stage.Events())
	require.Zero(t, processBudget.Reserved())
	require.EqualValues(t, 6, requestBudget.Reserved(), "successful and failed-attempt bytes remain charged cumulatively")
	require.ErrorContains(t, stage.StageEvent([]byte("x")), "closed")
	require.ErrorContains(t, stage.CommitTo(&bytes.Buffer{}), "closed")
	require.NoError(t, stage.Close())
}

func TestOpenAINativeCompactionHTTPAttemptStageHonorsClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAINativeCompaction: config.GatewayOpenAINativeCompactionConfig{
		MemoryThresholdBytes: 8, MaxAttemptBytes: 8, MaxAttemptEvents: 1, MaxAttemptDurationSeconds: 1,
	}}}
	stage, err := newOpenAINativeCompactionHTTPAttemptStage(ctx, &OpenAIGatewayService{cfg: cfg})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stage.Close() })
	cancel()
	require.ErrorIs(t, stage.CheckPending(1), context.Canceled)
	require.ErrorIs(t, stage.StageEvent([]byte("x")), context.Canceled)
}

func TestOpenAIUpstreamContextForCompactionAttemptCancellationPolicy(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	nativeCtx, releaseNative := openAIUpstreamContextForCompactionAttempt(parent, true)
	detachedCtx, releaseDetached := openAIUpstreamContextForCompactionAttempt(parent, false)
	t.Cleanup(releaseNative)
	t.Cleanup(releaseDetached)

	cancel()
	require.ErrorIs(t, nativeCtx.Err(), context.Canceled)
	require.NoError(t, detachedCtx.Err(), "ordinary streaming retains the existing usage-drain policy")
}

func TestOpenAINativeCompactionAttemptStageCheckPendingEnforcesDurationAndBytes(t *testing.T) {
	now := time.Unix(100, 0)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = stage.Close() })
	require.NoError(t, stage.CheckPending(8))
	require.ErrorIs(t, stage.CheckPending(9), ErrOpenAINativeCompactionStageBytes)
	now = now.Add(2 * time.Second)
	require.ErrorIs(t, stage.CheckPending(1), ErrOpenAINativeCompactionStageDuration)
}

func TestOpenAINativeCompactionAttemptStageNilCommitFailsClosed(t *testing.T) {
	budget := NewOpenAIStageBudget(8)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second, Budget: budget,
	})
	require.NoError(t, err)
	require.NoError(t, stage.StageEvent([]byte("xyz")))
	require.NotPanics(t, func() { err = stage.CommitTo(nil) })
	require.ErrorContains(t, err, "destination is nil")
	require.Zero(t, budget.Reserved())
	require.Zero(t, stage.Buffered())
	require.ErrorContains(t, stage.StageEvent([]byte("x")), "closed")
}

func TestOpenAINativeCompactionAttemptStageDurationAndCommitFailure(t *testing.T) {
	now := time.Unix(100, 0)
	stage, err := NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	now = now.Add(2 * time.Second)
	require.ErrorIs(t, stage.StageEvent([]byte("x")), ErrOpenAINativeCompactionStageDuration)
	require.NoError(t, stage.Close())

	budget := NewOpenAIStageBudget(8)
	stage, err = NewOpenAINativeCompactionAttemptStage(context.Background(), OpenAINativeCompactionStageConfig{
		MaxBytes: 8, MaxEvents: 1, MaxDuration: time.Second, Budget: budget,
	})
	require.NoError(t, err)
	require.NoError(t, stage.StageEvent([]byte("xyz")))
	writeErr := errors.New("synthetic write failure")
	require.ErrorIs(t, stage.CommitTo(&partialFailingWriter{limit: 1, err: writeErr}), writeErr)
	require.False(t, stage.Committed())
	require.Zero(t, budget.Reserved())
	require.Zero(t, stage.Buffered())
	require.ErrorContains(t, stage.CommitTo(&bytes.Buffer{}), "closed")
	require.NoError(t, stage.Close())
}

type partialFailingWriter struct {
	limit int
	err   error
}

func (w *partialFailingWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		return w.limit, w.err
	}
	return len(p), nil
}
