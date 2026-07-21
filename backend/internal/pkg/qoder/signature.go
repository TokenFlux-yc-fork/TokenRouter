package qoder

import (
	"crypto/md5"
	"fmt"
)

// SignQoderRequest 生成 COSY Gateway 请求使用的 MD5 签名。
func SignQoderRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	return md5Hex(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo))
}

// ComposeBearer 组合 Authorization header 值。
func ComposeBearer(payloadB64, signature string) string {
	return fmt.Sprintf("Bearer COSY.%s.%s", payloadB64, signature)
}

func md5Hex(value string) string {
	h := md5.Sum([]byte(value))
	return fmt.Sprintf("%x", h[:])
}
