package service

import (
	"io"

	"github.com/gin-gonic/gin"
)

const openAINativeRemoteCompactionPing = "event: ping\ndata: {\"type\":\"ping\"}\n\n"

// writeOpenAINativeRemoteCompactionPing emits an EventSource item that Codex
// can observe while excluding the protocol-only bytes from failover guards.
func writeOpenAINativeRemoteCompactionPing(c *gin.Context, writer io.Writer) (int, error) {
	n, err := io.WriteString(writer, openAINativeRemoteCompactionPing)
	recordOpenAIProtocolKeepaliveBytes(c, n)
	return n, err
}
