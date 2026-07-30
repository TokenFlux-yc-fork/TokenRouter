package service

import (
	"context"
	"errors"
	"sync"
	"time"

	coderws "github.com/coder/websocket"
)

type openAIWSClientReadResult struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

type openAIWSClientReadPumpContextKey struct{}

// openAIWSClientReadPump owns the only client-side reader after the handler has
// consumed the first response.create frame. It remains shared while the handler
// retries a safe first-turn failover, so native compaction attempts can observe a
// peer close without racing a second Conn.Read call or consuming the next turn.
type openAIWSClientReadPump struct {
	conn               *coderws.Conn
	results            chan openAIWSClientReadResult
	done               chan struct{}
	stopCh             chan struct{}
	stopOnce           sync.Once
	disconnected       context.Context
	cancelDisconnected context.CancelCauseFunc
}

// WithOpenAIWSClientReadPump starts the session-scoped reader used after the
// first client frame. The returned stop function does not close conn; the owner
// must retain its existing websocket close lifecycle.
func WithOpenAIWSClientReadPump(ctx context.Context, conn *coderws.Conn) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	if conn == nil {
		return ctx, func() {}
	}
	if current := openAIWSClientReadPumpFromContext(ctx); current != nil && current.conn == conn {
		return ctx, func() {}
	}
	disconnected, cancelDisconnected := context.WithCancelCause(context.Background())
	pump := &openAIWSClientReadPump{
		conn:               conn,
		results:            make(chan openAIWSClientReadResult, 1),
		done:               make(chan struct{}),
		stopCh:             make(chan struct{}),
		disconnected:       disconnected,
		cancelDisconnected: cancelDisconnected,
	}
	go pump.run()
	return context.WithValue(ctx, openAIWSClientReadPumpContextKey{}, pump), pump.stop
}

func openAIWSClientReadPumpFromContext(ctx context.Context) *openAIWSClientReadPump {
	if ctx == nil {
		return nil
	}
	pump, _ := ctx.Value(openAIWSClientReadPumpContextKey{}).(*openAIWSClientReadPump)
	return pump
}

func (p *openAIWSClientReadPump) run() {
	defer close(p.done)
	for {
		messageType, payload, err := p.conn.Read(context.Background())
		if err != nil {
			p.cancelDisconnected(err)
		}
		result := openAIWSClientReadResult{messageType: messageType, payload: payload, err: err}
		select {
		case p.results <- result:
		case <-p.stopCh:
			return
		}
		if err != nil {
			return
		}
	}
}

func (p *openAIWSClientReadPump) stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		close(p.stopCh)
		p.cancelDisconnected(context.Canceled)
	})
}

func (p *openAIWSClientReadPump) readWithTimeoutStart(
	controlCtx context.Context,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
	timeoutStart <-chan struct{},
	timeoutActive func() bool,
) (coderws.MessageType, []byte, error) {
	var timer *time.Timer
	var timeoutCh <-chan time.Time
	startTimeout := func() {
		if timeout <= 0 || (timeoutActive != nil && !timeoutActive()) {
			return
		}
		if timer == nil {
			timer = time.NewTimer(timeout)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		}
		timeoutCh = timer.C
	}
	if timeoutActive == nil || timeoutActive() {
		startTimeout()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	closeAndJoin := func(status coderws.StatusCode, reason string, cause error) (coderws.MessageType, []byte, error) {
		p.stop()
		_ = p.conn.Close(status, reason)
		_ = p.conn.CloseNow()
		<-p.done
		return 0, nil, NewOpenAIWSClientCloseError(status, reason, cause)
	}

	for {
		select {
		case result := <-p.results:
			return result.messageType, result.payload, result.err
		case <-timeoutStart:
			startTimeout()
		case <-timeoutCh:
			return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
		case <-controlCtx.Done():
			cause := context.Cause(controlCtx)
			if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
				return closeAndJoin(
					coderws.StatusTryAgainLater,
					"websocket ingress capacity lease lost; please reconnect",
					cause,
				)
			}
			return closeAndJoin(coderws.StatusGoingAway, "websocket request canceled", cause)
		}
	}
}

func withOpenAIWSNativeClientDisconnect(ctx context.Context, native bool) (context.Context, context.CancelFunc) {
	if !native {
		return ctx, func() {}
	}
	pump := openAIWSClientReadPumpFromContext(ctx)
	if pump == nil {
		return ctx, func() {}
	}
	attemptCtx, cancelAttempt := context.WithCancelCause(ctx)
	stop := context.AfterFunc(pump.disconnected, func() {
		cancelAttempt(context.Cause(pump.disconnected))
	})
	return attemptCtx, func() {
		stop()
		cancelAttempt(context.Canceled)
	}
}

func openAIWSClientReadPumpDisconnectCause(ctx context.Context) error {
	pump := openAIWSClientReadPumpFromContext(ctx)
	if pump == nil || pump.disconnected.Err() == nil {
		return nil
	}
	return context.Cause(pump.disconnected)
}

func openAIWSClientReadPumpDisconnectError(ctx context.Context) error {
	cause := openAIWSClientReadPumpDisconnectCause(ctx)
	if cause == nil {
		return nil
	}
	return NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "client disconnected", cause)
}

// ReadOpenAIWSClientMessage 在控制事件发送关闭帧期间保留唯一读协程，
// 随后强制关闭底层连接并等待读协程退出。
func ReadOpenAIWSClientMessage(
	controlCtx context.Context,
	conn *coderws.Conn,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
) (coderws.MessageType, []byte, error) {
	return readOpenAIWSClientMessageWithTimeoutStart(
		controlCtx,
		conn,
		timeout,
		timeoutStatus,
		timeoutReason,
		nil,
		nil,
	)
}

// readOpenAIWSClientMessageWithTimeoutStart 支持在状态转换后才开始计时，
// 例如 passthrough 一轮完成后等待下一轮。timeoutActive 为 nil 时，正数超时会立即起算。
func readOpenAIWSClientMessageWithTimeoutStart(
	controlCtx context.Context,
	conn *coderws.Conn,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
	timeoutStart <-chan struct{},
	timeoutActive func() bool,
) (coderws.MessageType, []byte, error) {
	if conn == nil {
		return 0, nil, errors.New("openai websocket client connection is nil")
	}
	if controlCtx == nil {
		controlCtx = context.Background()
	}
	if pump := openAIWSClientReadPumpFromContext(controlCtx); pump != nil && pump.conn == conn {
		return pump.readWithTimeoutStart(
			controlCtx,
			timeout,
			timeoutStatus,
			timeoutReason,
			timeoutStart,
			timeoutActive,
		)
	}

	readDone := make(chan openAIWSClientReadResult, 1)
	go func() {
		messageType, payload, err := conn.Read(context.Background())
		readDone <- openAIWSClientReadResult{messageType: messageType, payload: payload, err: err}
	}()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	startTimeout := func() {
		if timeout <= 0 || (timeoutActive != nil && !timeoutActive()) {
			return
		}
		if timer == nil {
			timer = time.NewTimer(timeout)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		}
		timeoutCh = timer.C
	}
	if timeoutActive == nil || timeoutActive() {
		startTimeout()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	closeAndJoin := func(status coderws.StatusCode, reason string, cause error) (coderws.MessageType, []byte, error) {
		_ = conn.Close(status, reason)
		_ = conn.CloseNow()
		<-readDone
		return 0, nil, NewOpenAIWSClientCloseError(status, reason, cause)
	}

	for {
		select {
		case result := <-readDone:
			return result.messageType, result.payload, result.err
		case <-timeoutStart:
			startTimeout()
		case <-timeoutCh:
			return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
		case <-controlCtx.Done():
			cause := context.Cause(controlCtx)
			if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
				return closeAndJoin(
					coderws.StatusTryAgainLater,
					"websocket ingress capacity lease lost; please reconnect",
					cause,
				)
			}
			return closeAndJoin(coderws.StatusGoingAway, "websocket request canceled", cause)
		}
	}
}
