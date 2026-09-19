package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

const authTokenBytes = 32

// LocalDefaultAdminToken 是开发模式首次启动使用的默认管理令牌。
const LocalDefaultAdminToken = "oneai-local-admin"

// LocalDefaultProxyToken 是开发模式首次启动使用的默认代理令牌。
const LocalDefaultProxyToken = "oneai-local-proxy"

func newAuthToken() (string, error) {
	buffer := make([]byte, authTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成鉴权令牌失败: %w", err)
	}
	return "oneai_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashAuthToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func equalAuthHash(expected, actual string) bool {
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
