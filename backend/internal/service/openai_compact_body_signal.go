package service

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAINativeRemoteCompactionV2Key = "openai_native_remote_compaction_v2"

// MarkOpenAINativeRemoteCompactionV2 records that Codex explicitly requested
// the native streaming remote compaction v2 protocol on /responses.
func MarkOpenAINativeRemoteCompactionV2(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(openAINativeRemoteCompactionV2Key, true)
}

// IsOpenAINativeRemoteCompactionV2 reports whether the current request must
// remain on the native streaming /responses protocol.
func IsOpenAINativeRemoteCompactionV2(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(openAINativeRemoteCompactionV2Key)
	if !ok {
		return false
	}
	native, _ := value.(bool)
	return native
}

// HasCompactionTriggerInInput detects an input item with
// type="compaction_trigger". The handler combines this body signal with the
// request path, stream flag, and Codex beta feature header to distinguish the
// native remote compaction v2 wire from the legacy /responses/compact bridge.
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}
