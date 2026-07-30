package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apicompat"
	"github.com/TokenFlux/TokenRouter/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAINativeCompactionTimedReadCloser struct {
	io.ReadCloser
	timer    *time.Timer
	timedOut atomic.Bool
}

// splitOpenAINativeCompactionSSEWireLine retains the transport delimiter in
// each token. Native-v2 validates a parsed view but commits these bytes only
// after the whole attempt succeeds.
func splitOpenAINativeCompactionSSEWireLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (r *openAINativeCompactionTimedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && r.timedOut.Load() {
		return n, ErrOpenAINativeCompactionStageDuration
	}
	return n, err
}

func (s *OpenAIGatewayService) handleOpenAINativeCompactionStreamingResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	originalModel string,
	mappedModel string,
	passthrough bool,
) (*openaiStreamingResult, error) {
	result := &openaiStreamingResult{usage: &OpenAIUsage{}}
	stage, err := newOpenAINativeCompactionHTTPAttemptStage(ctx, s)
	if err != nil {
		return result, s.newOpenAIStreamFailoverError(c, account, passthrough, resp.Header.Get("x-request-id"), nil, err.Error())
	}
	defer func() { _ = stage.Close() }()
	validator := NewOpenAINativeCompactionValidator()
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	attemptHeaders := openAINativeCompactionAttemptHeaders(resp.Header, s.responseHeaderFilter, passthrough)

	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scannerLimit := openAINativeCompactionScannerLimit(maxLineSize+2, stage)
	scanner.Buffer(scanBuf[:0], scannerLimit)
	scanner.Split(splitOpenAINativeCompactionSSEWireLine)
	defer putSSEScannerBuf64K(scanBuf)
	if stage.maxDuration > 0 && resp.Body != nil {
		body := resp.Body
		timedBody := &openAINativeCompactionTimedReadCloser{ReadCloser: body}
		timedBody.timer = time.AfterFunc(stage.maxDuration, func() {
			timedBody.timedOut.Store(true)
			_ = body.Close()
		})
		resp.Body = timedBody
		scanner = bufio.NewScanner(resp.Body)
		scanner.Buffer(scanBuf[:0], scannerLimit)
		scanner.Split(splitOpenAINativeCompactionSSEWireLine)
		defer timedBody.timer.Stop()
	}

	needModelReplace := strings.TrimSpace(originalModel) != "" && strings.TrimSpace(mappedModel) != "" && strings.TrimSpace(originalModel) != strings.TrimSpace(mappedModel)
	responseAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	imageCounter := newOpenAIImageOutputCounter()
	var finalResponseBody []byte
	var firstTokenMs *int
	responseID := ""
	frameWire := make([]byte, 0, 4096)
	frameData := make([]string, 0, 1)

	pingInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		pingInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// Keepalive may be the first downstream write, so install transport-stable
	// SSE headers before the goroutine can commit the HTTP response. Headers
	// identifying a particular upstream attempt remain staged until validation.
	setOpenAINativeCompactionSSEHeaders(c)

	var writerMu sync.Mutex
	stopPing := func() {}
	if pingInterval > 0 {
		stopCh := make(chan struct{})
		doneCh := make(chan struct{})
		go func() {
			defer close(doneCh)
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					writerMu.Lock()
					deadlineErr := setOpenAIResponseWriteDeadline(c.Writer, time.Now().Add(pingInterval))
					if deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
						MarkOpsStreamError(c, "downstream_write_deadline_error", deadlineErr.Error(), 0)
					}
					_, pingErr := writeOpenAINativeRemoteCompactionPing(c, c.Writer)
					if pingErr != nil {
						MarkOpsStreamError(c, "downstream_write_error", pingErr.Error(), 0)
					} else if flushErr := flushOpenAIResponseWriter(c.Writer); flushErr != nil {
						pingErr = flushErr
						MarkOpsStreamError(c, "downstream_flush_error", flushErr.Error(), 0)
					}
					_ = setOpenAIResponseWriteDeadline(c.Writer, time.Time{})
					writerMu.Unlock()
					if pingErr != nil {
						return
					}
				}
			}
		}()
		stopPing = func() {
			close(stopCh)
			<-doneCh
		}
	}
	defer func() { stopPing() }()

	stageFailure := func(stageErr error) (*openaiStreamingResult, error) {
		result.nativeValidation = validator.Result()
		_ = stage.Discard()
		if ctx != nil && ctx.Err() != nil {
			result.clientDisconnect = true
			return result, ctx.Err()
		}
		message := "OpenAI native compaction staging failed"
		if stageErr != nil {
			message += ": " + stageErr.Error()
		}
		failoverErr := s.newOpenAIStreamFailoverError(c, account, passthrough, upstreamRequestID, nil, message)
		failoverErr.SafeToFailoverAfterWrite = true
		if semanticErr := openAINativeCompactionStageSemanticError(stageErr); semanticErr != nil {
			return result, errors.Join(failoverErr, semanticErr)
		}
		return result, failoverErr
	}
	semanticFailure := func(validation OpenAINativeCompactionValidationResult) (*openaiStreamingResult, error) {
		result.nativeValidation = validation
		_ = stage.Discard()
		message := "OpenAI native compaction semantic validation failed: " + string(validation.Outcome)
		failoverErr := s.newOpenAIStreamFailoverError(c, account, passthrough, upstreamRequestID, nil, message)
		failoverErr.SafeToFailoverAfterWrite = true
		return result, errors.Join(failoverErr, newOpenAINativeCompactionSemanticError(validation.Outcome))
	}
	processPayload := func(dataBytes []byte) ([]byte, string, error) {
		if !gjson.ValidBytes(dataBytes) {
			return dataBytes, "", nil
		}
		if needModelReplace && strings.Contains(string(dataBytes), mappedModel) {
			if model := gjson.GetBytes(dataBytes, "response.model"); model.Type == gjson.String && model.String() == mappedModel {
				if replaced, replaceErr := sjson.SetBytes(dataBytes, "response.model", originalModel); replaceErr == nil {
					dataBytes = replaced
				}
			}
		}
		if normalized, ok := normalizeOpenAIResponsesFunctionCallArguments(dataBytes); ok {
			dataBytes = normalized
		}
		if normalized, ok := normalizeCompletedImageGenerationStatus(dataBytes); ok {
			dataBytes = normalized
		}
		restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
		if restoreErr != nil {
			return nil, "", restoreErr
		}
		restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
		if restoreErr != nil {
			return nil, "", restoreErr
		}
		dataBytes = restoredData
		if s.toolCorrector != nil {
			if corrected, ok := s.toolCorrector.CorrectToolCallsInSSEBytes(dataBytes); ok {
				dataBytes = corrected
			}
		}
		eventType := strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
		var responseEvent apicompat.ResponsesStreamEvent
		if json.Unmarshal(dataBytes, &responseEvent) == nil {
			responseAccumulator.ProcessEvent(&responseEvent)
		}
		if imageOutput, ok := extractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
			streamImageOutputs = append(streamImageOutputs, imageOutput)
		}
		if normalized, ok := normalizeResponsesStreamingTerminalOutput(dataBytes, responseAccumulator, streamImageOutputs); ok {
			dataBytes = normalized
			eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
		}
		return dataBytes, eventType, nil
	}
	dispatchFrame := func() (*openaiStreamingResult, error, bool) {
		if len(frameWire) == 0 {
			return nil, nil, false
		}
		if len(frameData) == 0 {
			if err := stage.StageEvent(frameWire); err != nil {
				res, failErr := stageFailure(err)
				return res, failErr, true
			}
			frameWire = frameWire[:0]
			return nil, nil, false
		}

		data := strings.Join(frameData, "\n")
		if strings.TrimSpace(data) == "[DONE]" {
			validator.Observe([]byte("[DONE]"))
			if err := stage.StageEvent(frameWire); err != nil {
				res, failErr := stageFailure(err)
				return res, failErr, true
			}
			frameWire = frameWire[:0]
			frameData = frameData[:0]
			return nil, nil, false
		}

		validationPayload := []byte(data)
		rawEventType := strings.TrimSpace(gjson.GetBytes(validationPayload, "type").String())
		if rawEventType == "error" || rawEventType == "response.failed" {
			if continuationCode := openAINativeCompactionContinuationErrorCode(extractUpstreamErrorCode(validationPayload)); continuationCode != "" {
				result.nativeValidation = OpenAINativeCompactionValidationResult{
					Outcome:       OpenAINativeCompactionHTTPFailure,
					TerminalEvent: rawEventType,
				}
				_ = stage.Discard()
				return result, newOpenAINativeCompactionHTTPContinuationFailoverError(
					http.StatusBadGateway,
					resp.Header,
					continuationCode,
					true,
				), true
			}
		}
		validator.Observe(validationPayload)
		payload := append([]byte(nil), validationPayload...)
		payload, eventType, processErr := processPayload(payload)
		if processErr != nil {
			res, failErr := stageFailure(processErr)
			return res, failErr, true
		}
		if responseID == "" {
			responseID = extractOpenAIResponseIDFromJSONBytes(payload)
		}
		if firstTokenMs == nil && openAIStreamDataStartsClientOutput(string(payload), eventType) {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(payload); ok {
			*result.usage = parsedUsage
			result.usageObserved = true
		}
		imageCounter.AddSSEData(payload)
		if eventType == "response.completed" {
			if response := gjson.GetBytes(payload, "response"); response.Exists() && response.Type == gjson.JSON && response.Raw != "" {
				finalResponseBody = []byte(response.Raw)
			}
		}
		// Parsing and compatibility normalization above are used only for local
		// accounting. Validation and delivery both use the payload Codex sees.
		// Native-v2 delivery must retain the exact upstream SSE bytes so opaque
		// compaction state and frame ordering cannot be changed by the gateway.
		if err := stage.StageEvent(frameWire); err != nil {
			res, failErr := stageFailure(err)
			return res, failErr, true
		}
		frameWire = frameWire[:0]
		frameData = frameData[:0]
		return nil, nil, false
	}

	appendFrameLine := func(wireLine []byte) error {
		if err := stage.CheckPending(int64(len(frameWire) + len(wireLine))); err != nil {
			return err
		}
		frameWire = append(frameWire, wireLine...)
		return nil
	}

	for scanner.Scan() {
		wireLine := scanner.Bytes()
		line := string(bytes.TrimSuffix(bytes.TrimSuffix(wireLine, []byte{'\n'}), []byte{'\r'}))
		if _, ok := extractOpenAISSEEventLine(line); ok {
			if appendErr := appendFrameLine(wireLine); appendErr != nil {
				return stageFailure(appendErr)
			}
			continue
		}
		if data, ok := extractOpenAISSEDataLine(line); ok {
			if appendErr := appendFrameLine(wireLine); appendErr != nil {
				return stageFailure(appendErr)
			}
			frameData = append(frameData, data)
			continue
		}
		if appendErr := appendFrameLine(wireLine); appendErr != nil {
			return stageFailure(appendErr)
		}
		if strings.TrimSpace(line) == "" {
			if res, dispatchErr, done := dispatchFrame(); done {
				return res, dispatchErr
			}
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		result.nativeValidation = validator.Finish()
		_ = stage.Discard()
		if ctx != nil && ctx.Err() != nil {
			result.clientDisconnect = true
			return result, ctx.Err()
		}
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			return result, fmt.Errorf("stream usage incomplete: %w", scanErr)
		}
		message := "OpenAI stream disconnected before completion"
		if errors.Is(scanErr, bufio.ErrTooLong) {
			message = "OpenAI SSE line exceeds native compaction staging limit"
		} else if text := strings.TrimSpace(scanErr.Error()); text != "" {
			message += ": " + text
		}
		failoverErr := s.newOpenAIStreamFailoverError(c, account, passthrough, upstreamRequestID, nil, message)
		failoverErr.SafeToFailoverAfterWrite = true
		return result, failoverErr
	}
	if len(frameWire) > 0 {
		if res, dispatchErr, done := dispatchFrame(); done {
			return res, dispatchErr
		}
	}
	if ctx != nil && ctx.Err() != nil {
		result.nativeValidation = validator.Finish()
		result.clientDisconnect = true
		_ = stage.Discard()
		return result, ctx.Err()
	}

	validation := validator.Finish()
	result.nativeValidation = validation
	if !validation.Valid() {
		return semanticFailure(validation)
	}

	stopPing()
	stopPing = func() {}
	writerMu.Lock()
	defer writerMu.Unlock()
	applyOpenAINativeCompactionAttemptHeaders(c.Writer.Header(), attemptHeaders)
	setOpenAINativeCompactionSSEHeaders(c)
	if err := stage.CommitTo(c.Writer); err != nil {
		MarkOpsStreamError(c, "downstream_write_error", err.Error(), 0)
		if OpenAISemanticWrittenSize(c) >= 0 {
			MarkResponseCommitted(c)
		}
		result.deliveryCommitted = false
		result.clientDisconnect = true
		result.firstTokenMs = firstTokenMs
		result.responseID = responseID
		result.imageCount = imageCounter.Count()
		result.imageOutputSizes = imageCounter.Sizes()
		result.responseBody = cloneDataSharingRequestBody(finalResponseBody)
		return result, nil
	}
	MarkResponseCommitted(c)
	if err := flushOpenAIResponseWriter(c.Writer); err != nil {
		MarkOpsStreamError(c, "downstream_flush_error", err.Error(), 0)
		result.clientDisconnect = true
		result.firstTokenMs = firstTokenMs
		result.responseID = responseID
		result.imageCount = imageCounter.Count()
		result.imageOutputSizes = imageCounter.Sizes()
		result.responseBody = cloneDataSharingRequestBody(finalResponseBody)
		return result, nil
	}
	result.deliveryCommitted = true
	s.clearOpenAIProxyStreamDisconnect(account)
	result.firstTokenMs = firstTokenMs
	result.responseID = responseID
	result.imageCount = imageCounter.Count()
	result.imageOutputSizes = imageCounter.Sizes()
	result.responseBody = cloneDataSharingRequestBody(finalResponseBody)
	return result, nil
}

func setOpenAINativeCompactionSSEHeaders(c *gin.Context) {
	if c == nil {
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}

func openAINativeCompactionAttemptHeaders(src http.Header, filter *responseheaders.CompiledHeaderFilter, passthrough bool) http.Header {
	headers := responseheaders.FilterHeaders(src, filter)
	if !passthrough {
		return headers
	}
	for _, rawKey := range []string{
		"x-codex-primary-used-percent",
		"x-codex-primary-reset-after-seconds",
		"x-codex-primary-window-minutes",
		"x-codex-secondary-used-percent",
		"x-codex-secondary-reset-after-seconds",
		"x-codex-secondary-window-minutes",
		"x-codex-primary-over-secondary-limit-percent",
	} {
		for key, values := range src {
			if !strings.EqualFold(key, rawKey) {
				continue
			}
			canonicalKey := http.CanonicalHeaderKey(rawKey)
			headers.Del(canonicalKey)
			for _, value := range values {
				headers.Add(canonicalKey, value)
			}
			break
		}
	}
	return headers
}

func applyOpenAINativeCompactionAttemptHeaders(dst, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
