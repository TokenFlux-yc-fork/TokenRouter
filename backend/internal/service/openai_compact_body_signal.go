package service

import (
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAINativeRemoteCompactionV2Key = "openai_native_remote_compaction_v2"

// MarkOpenAINativeRemoteCompactionV2 records that this request uses the native
// streaming remote-compaction wire retained by the upstream route normalizer.
func MarkOpenAINativeRemoteCompactionV2(c *gin.Context) {
	if c != nil {
		c.Set(openAINativeRemoteCompactionV2Key, true)
	}
}

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

// HasCompactionTriggerInInput 检测 input 中 type="compaction_trigger" 的条目。
// handler 会结合请求路径、stream 字段和 Codex beta feature 请求头，区分原生
// remote compaction v2 流式协议与旧的 /responses/compact 桥接协议。
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
