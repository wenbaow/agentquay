package mcpbridge

import (
	"crypto/rand"
	"encoding/hex"
)

// newRequestID 生成请求 ID：req-<32位hex>（示例：req-550e8400fe294ab6）。
func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "req-unknown"
	}
	return "req-" + hex.EncodeToString(buf)
}
