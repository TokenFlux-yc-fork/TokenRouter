package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// MarkOpenAINativeRemoteCompactionV2 records that this request uses the native
// streaming remote-compaction wire retained by the upstream route normalizer.
func MarkOpenAINativeRemoteCompactionV2(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	ctx := WithOpenAINativeRemoteCompactionV2(c.Request.Context(), true)
	c.Request = c.Request.WithContext(ctx)
}

func IsOpenAINativeRemoteCompactionV2(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	native, _ := OpenAINativeRemoteCompactionV2FromContext(c.Request.Context())
	return native
}

func IsOpenAINativeRemoteCompactionV2Request(body []byte, betaFeatureHeaders []string) bool {
	stream := gjson.GetBytes(body, "stream")
	if stream.Type != gjson.True {
		return false
	}
	return IsOpenAINativeRemoteCompactionV2Turn(body, betaFeatureHeaders)
}

// IsOpenAINativeRemoteCompactionV2Turn recognizes a compaction response.create
// payload. WebSocket ingress is intrinsically streaming, so it need not carry
// the HTTP-only stream=true field on every turn.
func IsOpenAINativeRemoteCompactionV2Turn(body []byte, betaFeatureHeaders []string) bool {
	betaEnabled := false
	for _, header := range betaFeatureHeaders {
		for _, feature := range strings.Split(header, ",") {
			if strings.TrimSpace(feature) == "remote_compaction_v2" {
				betaEnabled = true
				break
			}
		}
		if betaEnabled {
			break
		}
	}
	if !betaEnabled {
		return false
	}

	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	if len(items) == 0 || !items[len(items)-1].IsObject() {
		return false
	}
	itemType := items[len(items)-1].Get("type")
	return itemType.Type == gjson.String && itemType.String() == "compaction_trigger"
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
