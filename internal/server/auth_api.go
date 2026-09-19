package server

import (
	"net/http"
	"strings"

	"github.com/4xian/oneai-proxy/internal/storage"
)

// authStatusPayload 描述管理/代理令牌是否已配置，以及是否为自定义令牌。
type authStatusPayload struct {
	AdminConfigured bool `json:"adminConfigured"`
	ProxyConfigured bool `json:"proxyConfigured"`
	AdminCustom     bool `json:"adminCustom"`
	ProxyCustom     bool `json:"proxyCustom"`
}

type rotateTokenPayload struct {
	TokenType string `json:"tokenType"`
	Token     string `json:"token"`
}

// authAPI 返回令牌配置状态，并支持在管理令牌认证后设置或轮换任一令牌。
func (s *Service) authAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		s.dataMu.RLock()
		defer s.dataMu.RUnlock()
		s.authMu.RLock()
		status := authStatusPayload{AdminConfigured: s.auth.AdminHash != "", ProxyConfigured: s.auth.ProxyHash != "", AdminCustom: s.auth.AdminCustom, ProxyCustom: s.auth.ProxyCustom}
		s.authMu.RUnlock()
		writeJSON(writer, http.StatusOK, status)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "令牌接口仅支持 GET 和 POST"})
		return
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	var payload rotateTokenPayload
	if !decodeJSON(writer, request, &payload) {
		return
	}
	payload.TokenType = strings.TrimSpace(payload.TokenType)
	if payload.TokenType != "admin" && payload.TokenType != "proxy" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "tokenType 只能是 admin 或 proxy"})
		return
	}
	var token string
	var tokens storage.AuthTokens
	var err error
	if strings.TrimSpace(payload.Token) != "" {
		token = strings.TrimSpace(payload.Token)
		tokens, err = storage.SetAuthToken(s.database, s.secrets, payload.TokenType, token)
	} else {
		token, tokens, err = storage.RotateAuthToken(s.database, s.secrets, payload.TokenType)
	}
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.authMu.Lock()
	s.auth = tokens
	s.authMu.Unlock()
	s.recordAudit("auth.token.rotate", payload.TokenType, request)
	writeJSON(writer, http.StatusOK, map[string]string{"tokenType": payload.TokenType, "token": token})
}
