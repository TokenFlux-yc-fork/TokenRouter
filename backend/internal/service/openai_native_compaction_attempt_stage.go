package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

var (
	ErrOpenAINativeCompactionStageBytes    = errors.New("native compaction attempt byte limit exceeded")
	ErrOpenAINativeCompactionStageEvents   = errors.New("native compaction attempt event limit exceeded")
	ErrOpenAINativeCompactionStageDuration = errors.New("native compaction attempt duration limit exceeded")
	ErrOpenAINativeCompactionStageBudget   = errors.New("native compaction staging budget exceeded")
)

type OpenAIStageBudget struct {
	limit    int64
	reserved atomic.Int64
}

// Close resets a request-scoped cumulative budget after all attempts for that
// request have finished. It must not be used on a shared process budget.
func (b *OpenAIStageBudget) Close() {
	if b != nil {
		b.reserved.Store(0)
	}
}

func NewOpenAIStageBudget(limit int64) *OpenAIStageBudget {
	if limit < 1 {
		limit = 1
	}
	return &OpenAIStageBudget{limit: limit}
}

func (b *OpenAIStageBudget) Reserved() int64 {
	if b == nil {
		return 0
	}
	return b.reserved.Load()
}

func (b *OpenAIStageBudget) reserve(size int64) bool {
	if b == nil || size <= 0 {
		return true
	}
	for {
		current := b.reserved.Load()
		if size > b.limit-current {
			return false
		}
		if b.reserved.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func (b *OpenAIStageBudget) release(size int64) {
	if b == nil || size <= 0 {
		return
	}
	if remaining := b.reserved.Add(-size); remaining < 0 {
		b.reserved.Store(0)
	}
}

type OpenAINativeCompactionStageConfig struct {
	MemoryThreshold int64
	MaxBytes        int64
	MaxEvents       int
	MaxDuration     time.Duration
	Budget          *OpenAIStageBudget
	RequestBudget   *OpenAIStageBudget
	Now             func() time.Time
}

type OpenAINativeCompactionAttemptStage struct {
	mu            sync.Mutex
	ctx           context.Context
	startedAt     time.Time
	now           func() time.Time
	maxEvents     int
	maxDuration   time.Duration
	budget        *OpenAIStageBudget
	requestBudget *OpenAIStageBudget
	buffer        *openAIStagedBuffer
	events        int
	reserved      int64
	committed     bool
	closed        bool
}

func NewOpenAINativeCompactionAttemptStage(ctx context.Context, cfg OpenAINativeCompactionStageConfig) (*OpenAINativeCompactionAttemptStage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.MaxBytes < 1 || cfg.MaxEvents < 1 || cfg.MaxDuration <= 0 {
		return nil, errors.New("native compaction stage limits must be positive")
	}
	if cfg.MemoryThreshold < 1 {
		cfg.MemoryThreshold = openAIFirstOutputStageMemoryLimit
	}
	if cfg.MemoryThreshold > cfg.MaxBytes {
		cfg.MemoryThreshold = cfg.MaxBytes
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &OpenAINativeCompactionAttemptStage{
		ctx:           ctx,
		startedAt:     cfg.Now(),
		now:           cfg.Now,
		maxEvents:     cfg.MaxEvents,
		maxDuration:   cfg.MaxDuration,
		budget:        cfg.Budget,
		requestBudget: cfg.RequestBudget,
		buffer:        newOpenAIStagedBuffer(cfg.MaxBytes, cfg.MemoryThreshold, "tokenrouter-openai-native-compaction-*"),
	}, nil
}

func newOpenAINativeCompactionHTTPAttemptStage(ctx context.Context, svc *OpenAIGatewayService) (*OpenAINativeCompactionAttemptStage, error) {
	return newOpenAINativeCompactionAttemptStage(ctx, svc)
}

func openAIUpstreamContextForCompactionAttempt(ctx context.Context, nativeCompaction bool) (context.Context, context.CancelFunc) {
	if !nativeCompaction {
		return detachUpstreamContext(ctx)
	}
	if ctx == nil {
		return context.Background(), func() {}
	}
	return ctx, func() {}
}

func newOpenAINativeCompactionAttemptStage(ctx context.Context, svc *OpenAIGatewayService) (*OpenAINativeCompactionAttemptStage, error) {
	if svc == nil || svc.cfg == nil {
		return nil, errors.New("native compaction stage configuration is unavailable")
	}
	cfg := svc.cfg.Gateway.OpenAINativeCompaction
	// Config loading supplies these defaults in production. Preserve direct
	// service construction in focused tests without weakening explicit limits.
	if cfg.MemoryThresholdBytes <= 0 {
		cfg.MemoryThresholdBytes = 1 << 20
	}
	if cfg.MaxAttemptBytes <= 0 {
		cfg.MaxAttemptBytes = 64 << 20
	}
	if cfg.MaxAttemptEvents <= 0 {
		cfg.MaxAttemptEvents = 8192
	}
	if cfg.MaxAttemptDurationSeconds <= 0 {
		cfg.MaxAttemptDurationSeconds = 900
	}
	processBudget := svc.openAINativeCompactionStageBudget
	if processBudget == nil {
		processBudget = NewOpenAIStageBudget(512 << 20)
	}
	return NewOpenAINativeCompactionAttemptStage(ctx, OpenAINativeCompactionStageConfig{
		MemoryThreshold: cfg.MemoryThresholdBytes,
		MaxBytes:        cfg.MaxAttemptBytes,
		MaxEvents:       cfg.MaxAttemptEvents,
		MaxDuration:     time.Duration(cfg.MaxAttemptDurationSeconds) * time.Second,
		Budget:          processBudget,
		RequestBudget:   OpenAINativeCompactionRequestStageBudgetFromContext(ctx),
	})
}

func openAINativeCompactionScannerLimit(configured int, stage *OpenAINativeCompactionAttemptStage) int {
	if configured < 1 {
		configured = defaultMaxLineSize
	}
	if stage == nil || stage.buffer == nil || stage.buffer.limit < 1 {
		return configured
	}
	maxInt := int64(^uint(0) >> 1)
	limit := stage.buffer.limit
	if limit > maxInt-2 {
		limit = maxInt - 2
	}
	if attemptLimit := int(limit) + 2; attemptLimit < configured {
		return attemptLimit
	}
	return configured
}

func ensureOpenAINativeCompactionRequestStageBudget(ctx context.Context, svc *OpenAIGatewayService) (context.Context, func()) {
	if OpenAINativeCompactionRequestStageBudgetFromContext(ctx) != nil || svc == nil || svc.cfg == nil {
		return ctx, func() {}
	}
	limit := svc.cfg.Gateway.OpenAINativeCompaction.MaxRequestCumulativeBytes
	if limit <= 0 {
		limit = 128 << 20
	}
	budget := NewOpenAIStageBudget(limit)
	return WithOpenAINativeCompactionRequestStageBudget(ctx, budget), budget.Close
}

func isOpenAINativeCompactionTurn(c *gin.Context, payload []byte) bool {
	betaHeaders := []string(nil)
	if c != nil && c.Request != nil {
		betaHeaders = c.Request.Header.Values("x-codex-beta-features")
	}
	if strings.TrimSpace(string(payload)) == "" {
		return IsOpenAINativeRemoteCompactionV2(c)
	}
	return IsOpenAINativeRemoteCompactionV2Turn(payload, betaHeaders)
}

func openAINativeCompactionStageWSMessage(stage *OpenAINativeCompactionAttemptStage, message []byte) error {
	return openAINativeCompactionStageWSFrame(stage, websocket.MessageText, message)
}

func openAINativeCompactionStageWSFrame(stage *OpenAINativeCompactionAttemptStage, messageType websocket.MessageType, message []byte) error {
	if stage == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	if uint64(len(message)) > uint64(^uint32(0)) {
		return ErrOpenAINativeCompactionStageBytes
	}
	frame := make([]byte, 5+len(message))
	frame[0] = byte(messageType)
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(message)))
	copy(frame[5:], message)
	return stage.StageEvent(frame)
}

func commitOpenAINativeCompactionWSMessages(stage *OpenAINativeCompactionAttemptStage, writeMessage func([]byte) error) error {
	return commitOpenAINativeCompactionWSFrames(stage, func(_ websocket.MessageType, message []byte) error {
		return writeMessage(message)
	})
}

func commitOpenAINativeCompactionWSFrames(stage *OpenAINativeCompactionAttemptStage, writeFrame func(websocket.MessageType, []byte) error) error {
	if stage == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	if writeFrame == nil {
		return errors.New("native compaction websocket writer is nil")
	}
	return stage.CommitTo(&openAINativeCompactionWSFrameWriter{writeFrame: writeFrame})
}

type openAINativeCompactionWSFrameWriter struct {
	writeFrame func(websocket.MessageType, []byte) error
	pending    []byte
}

func (w *openAINativeCompactionWSFrameWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	consumed := len(p)
	for len(w.pending) >= 5 {
		messageType := websocket.MessageType(w.pending[0])
		size := int(binary.BigEndian.Uint32(w.pending[1:5]))
		if size < 0 || len(w.pending) < 5+size {
			break
		}
		if err := w.writeFrame(messageType, w.pending[5:5+size]); err != nil {
			return 0, err
		}
		w.pending = w.pending[5+size:]
	}
	return consumed, nil
}

func (s *OpenAINativeCompactionAttemptStage) StageEvent(payload []byte) error {
	if s == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native compaction attempt stage is closed")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.now().Sub(s.startedAt) > s.maxDuration {
		return ErrOpenAINativeCompactionStageDuration
	}
	if s.events >= s.maxEvents {
		return ErrOpenAINativeCompactionStageEvents
	}
	incoming := int64(len(payload))
	if incoming > s.buffer.limit-s.buffer.Buffered() {
		return ErrOpenAINativeCompactionStageBytes
	}
	if !s.budget.reserve(incoming) {
		return ErrOpenAINativeCompactionStageBudget
	}
	if !s.requestBudget.reserve(incoming) {
		s.budget.release(incoming)
		return ErrOpenAINativeCompactionStageBudget
	}
	n, err := s.buffer.Write(payload)
	if err != nil || int64(n) != incoming {
		writeErr := err
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		cleanupErr := s.buffer.Close()
		s.closed = true
		s.budget.release(incoming)
		s.releaseLocked()
		return errors.Join(fmt.Errorf("stage native compaction event: %w", writeErr), cleanupErr)
	}
	s.events++
	s.reserved += incoming
	return nil
}

func (s *OpenAINativeCompactionAttemptStage) CommitTo(dst io.Writer) error {
	if s == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native compaction attempt stage is closed")
	}
	if dst == nil {
		cleanupErr := s.buffer.Close()
		s.closed = true
		s.releaseLocked()
		return errors.Join(errors.New("native compaction attempt destination is nil"), cleanupErr)
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.now().Sub(s.startedAt) > s.maxDuration {
		return ErrOpenAINativeCompactionStageDuration
	}
	if err := s.buffer.CommitTo(dst); err != nil {
		cleanupErr := s.buffer.Close()
		s.closed = true
		s.releaseLocked()
		return errors.Join(fmt.Errorf("commit native compaction attempt: %w", err), cleanupErr)
	}
	s.committed = true
	s.closed = true
	s.releaseLocked()
	return nil
}

func (s *OpenAINativeCompactionAttemptStage) Discard() error {
	return s.Close()
}

func (s *OpenAINativeCompactionAttemptStage) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.buffer.Close()
	}
	s.closed = true
	err := s.buffer.Close()
	s.releaseLocked()
	return err
}

func (s *OpenAINativeCompactionAttemptStage) CheckPending(incoming int64) error {
	if s == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native compaction attempt stage is closed")
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.now().Sub(s.startedAt) > s.maxDuration {
		return ErrOpenAINativeCompactionStageDuration
	}
	if incoming < 0 || incoming > s.buffer.limit-s.buffer.Buffered() {
		return ErrOpenAINativeCompactionStageBytes
	}
	return nil
}

func (s *OpenAINativeCompactionAttemptStage) Buffered() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.Buffered()
}

func (s *OpenAINativeCompactionAttemptStage) Events() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

func openAINativeCompactionStageFrame(stage *OpenAINativeCompactionAttemptStage, lines []string) error {
	if stage == nil {
		return errors.New("native compaction attempt stage is nil")
	}
	var frame []byte
	for _, line := range lines {
		frame = append(frame, line...)
		frame = append(frame, '\n')
	}
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != "" {
		frame = append(frame, '\n')
	}
	return stage.StageEvent(frame)
}

func (s *OpenAINativeCompactionAttemptStage) Committed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

func (s *OpenAINativeCompactionAttemptStage) releaseLocked() {
	// The process-wide reservation is released with the attempt. The request
	// budget is cumulative by design: failed attempts still count against the
	// request's I/O amplification limit until the request object is discarded.
	s.budget.release(s.reserved)
	s.reserved = 0
}
