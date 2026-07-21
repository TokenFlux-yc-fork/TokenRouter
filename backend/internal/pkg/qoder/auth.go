package qoder

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// APIBaseURL 是国内站推理地址。
const APIBaseURL = CNGatewayBaseURL

// GenerateRequestID 生成随机请求 ID。
func GenerateRequestID() string {
	return hex.EncodeToString(mustRandomBytes(16))
}

// RandomToken 生成指定长度的 URL-safe 随机 token。
func RandomToken(length int) string {
	return base64URLEncode(mustRandomBytes(length))
}

// RandomHex 生成指定长度的十六进制随机字符串。
func RandomHex(length int) string {
	b := mustRandomBytes((length + 1) / 2)
	return hex.EncodeToString(b)[:length]
}

func mustRandomBytes(length int) []byte {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("qoder random bytes: %w", err))
	}
	return b
}

func base64URLEncode(b []byte) string {
	// 手动实现不带 padding 的 URL-safe base64 编码。
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var result strings.Builder
	result.Grow((len(b)*8 + 5) / 6)

	for i := 0; i < len(b); i += 3 {
		var val uint32
		remaining := len(b) - i

		val |= uint32(b[i]) << 16
		if remaining > 1 {
			val |= uint32(b[i+1]) << 8
		}
		if remaining > 2 {
			val |= uint32(b[i+2])
		}

		_ = result.WriteByte(alphabet[(val>>18)&0x3f])
		_ = result.WriteByte(alphabet[(val>>12)&0x3f])
		if remaining > 1 {
			_ = result.WriteByte(alphabet[(val>>6)&0x3f])
		}
		if remaining > 2 {
			_ = result.WriteByte(alphabet[val&0x3f])
		}
	}
	return result.String()
}

// NewMachine 创建 qoderclicn 机器身份。
func NewMachine() *MachineIdentity {
	return NewMachineForSite(SiteCN)
}

// NewMachineForSite 按官方站点协议创建机器身份。
func NewMachineForSite(site Site) *MachineIdentity {
	machineID := RandomUUIDLike()
	return &MachineIdentity{MachineID: machineID, MachineToken: machineID, MachineType: "5"}
}

// pathWithoutAlgo 移除 URL path 中用于签名计算之外的 "/algo" 前缀。
func pathWithoutAlgo(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return strings.TrimPrefix(u.Path, "/algo")
}
