package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

type openAIStagedBuffer struct {
	limit               int64
	memoryLimit         int64
	fallbackMemoryLimit int64
	size                int64
	memory              bytes.Buffer
	tempFile            *os.File
	tempPath            string
	createTemp          func() (*os.File, error)
	removeFile          func(string) error
	writeData           func(io.Writer, []byte) (int, error)
	memoryOnly          bool
	cleanupErr          error
	closed              bool
}

func newOpenAIStagedBuffer(limit, memoryLimit int64, prefix string) *openAIStagedBuffer {
	if limit < 1 {
		limit = 1
	}
	if memoryLimit < 1 || memoryLimit > limit {
		memoryLimit = limit
	}
	if prefix == "" {
		prefix = "tokenrouter-openai-stage-*"
	}
	return &openAIStagedBuffer{
		limit:               limit,
		memoryLimit:         memoryLimit,
		fallbackMemoryLimit: memoryLimit,
		createTemp:          func() (*os.File, error) { return os.CreateTemp("", prefix) },
		removeFile:          os.Remove,
		writeData:           func(dst io.Writer, data []byte) (int, error) { return dst.Write(data) },
		memoryOnly:          runtime.GOOS == "windows",
	}
}

func (s *openAIStagedBuffer) Buffered() int64 {
	if s == nil {
		return 0
	}
	return s.size
}

func (s *openAIStagedBuffer) WriteString(value string) (int, error) {
	return s.Write([]byte(value))
}

func (s *openAIStagedBuffer) Write(p []byte) (int, error) {
	if err := s.prepareWrite(len(p)); err != nil {
		return 0, err
	}
	var dst io.Writer = &s.memory
	if s.tempFile != nil {
		dst = s.tempFile
	}
	n, err := s.writeData(dst, p)
	s.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write OpenAI stage: %w", err)
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (s *openAIStagedBuffer) prepareWrite(incoming int) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	if int64(incoming) > s.limit-s.size {
		return fmt.Errorf("staging limit exceeded: buffered=%d incoming=%d limit=%d", s.size, incoming, s.limit)
	}
	if s.tempFile != nil {
		return nil
	}
	if s.memoryOnly {
		if int64(incoming) > s.fallbackMemoryLimit-s.size {
			return fmt.Errorf("staging limit exceeded: buffered=%d incoming=%d limit=%d", s.size, incoming, s.fallbackMemoryLimit)
		}
		return nil
	}
	if s.size+int64(incoming) <= s.memoryLimit {
		return nil
	}
	file, err := s.createTemp()
	if err != nil {
		return fmt.Errorf("create OpenAI spool: %w", err)
	}
	path := file.Name()
	// Unlink before writing request data. Unix retains the descriptor while
	// crash and SIGKILL cannot leave a named plaintext spool behind.
	if unlinkErr := s.removeFile(path); unlinkErr != nil {
		closeErr := file.Close()
		removeErr := s.removeFile(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		s.memoryOnly = true
		if removeErr != nil {
			s.tempPath = path
		}
		s.cleanupErr = errors.Join(s.cleanupErr, fmt.Errorf("unlink OpenAI spool before use: %w", unlinkErr), closeErr, removeErr)
		if s.size+int64(incoming) > s.fallbackMemoryLimit {
			return s.cleanupErr
		}
		return nil
	}
	if _, err := file.Write(s.memory.Bytes()); err != nil {
		_ = file.Close()
		return fmt.Errorf("initialize OpenAI spool: %w", err)
	}
	s.tempFile = file
	s.tempPath = path
	s.memory.Reset()
	return nil
}

func (s *openAIStagedBuffer) CommitTo(dst io.Writer) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	if s.tempFile == nil {
		if _, err := io.Copy(dst, bytes.NewReader(s.memory.Bytes())); err != nil {
			return err
		}
	} else {
		if _, err := s.tempFile.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek OpenAI spool: %w", err)
		}
		if _, err := io.CopyN(dst, s.tempFile, s.size); err != nil {
			return err
		}
	}
	if err := s.Close(); err != nil {
		// Bytes have already been delivered. Preserve only retryable cleanup
		// state; re-storing historical cleanup errors would make Close non-idempotent.
		if s.tempFile != nil || s.tempPath != "" {
			s.cleanupErr = errors.Join(s.cleanupErr, err)
		}
	}
	return nil
}

func (s *openAIStagedBuffer) Close() error {
	if s == nil {
		return nil
	}
	if s.closed && s.tempFile == nil && s.tempPath == "" && s.cleanupErr == nil {
		return nil
	}
	s.closed = true
	s.size = 0
	s.memory.Reset()
	closeErr := s.cleanupErr
	s.cleanupErr = nil
	if s.tempFile != nil {
		closeErr = errors.Join(closeErr, s.tempFile.Close())
		s.tempFile = nil
	}
	if s.tempPath != "" {
		removeErr := s.removeFile(s.tempPath)
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			s.tempPath = ""
		} else {
			closeErr = errors.Join(closeErr, removeErr)
		}
	}
	return closeErr
}
