package service

import (
	"io"

	"github.com/gin-gonic/gin"
)

const openAINativeRemoteCompactionPing = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

// writeOpenAINativeRemoteCompactionPingFrame writes an observable EventSource
// item without assigning ownership of its bytes to any accounting layer.
func writeOpenAINativeRemoteCompactionPingFrame(writer io.Writer) (int, error) {
	return io.WriteString(writer, openAINativeRemoteCompactionPing)
}

// writeOpenAINativeRemoteCompactionPing emits an EventSource item that Codex
// can observe while excluding the protocol-only bytes from failover guards.
func writeOpenAINativeRemoteCompactionPing(c *gin.Context, writer io.Writer) (int, error) {
	n, err := writeOpenAINativeRemoteCompactionPingFrame(writer)
	recordOpenAIProtocolKeepaliveBytes(c, n)
	return n, err
}
