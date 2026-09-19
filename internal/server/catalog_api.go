package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/secret"
	"github.com/4xian/oneai-proxy/internal/storage"
	"golang.org/x/crypto/hkdf"
)

const (
	channelCredentialHKDFSalt = "oneai-proxy-channel-credential"
	channelCredentialHKDFInfo = "wrap-v1"
)

type credentialPayload struct {
	Type       string  `json:"type"`
	Secret     string  `json:"secret"`
	HeaderName *string `json:"headerName"`
	Prefix     *string `json:"prefix"`
}

// UnmarshalJSON 严格解析凭证字段，拒绝显式 null 和未声明字段。
func (payload *credentialPayload) UnmarshalJSON(data []byte) error {
	type wireCredential struct {
		Type       json.RawMessage `json:"type"`
		Secret     json.RawMessage `json:"secret"`
		HeaderName json.RawMessage `json:"headerName"`
		Prefix     json.RawMessage `json:"prefix"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wireCredential
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if wire.Type == nil || wire.Secret == nil || bytes.Equal(bytes.TrimSpace(wire.Type), []byte("null")) || bytes.Equal(bytes.TrimSpace(wire.Secret), []byte("null")) {
		return errors.New("credential 的 type 和 secret 必须是字符串")
	}
	if err := json.Unmarshal(wire.Type, &payload.Type); err != nil {
		return err
	}
	if err := json.Unmarshal(wire.Secret, &payload.Secret); err != nil {
		return err
	}
	if wire.HeaderName != nil {
		if bytes.Equal(bytes.TrimSpace(wire.HeaderName), []byte("null")) {
			return errors.New("credential.headerName 必须是字符串")
		}
		var value string
		if err := json.Unmarshal(wire.HeaderName, &value); err != nil {
			return err
		}
		payload.HeaderName = &value
	}
	if wire.Prefix != nil {
		if bytes.Equal(bytes.TrimSpace(wire.Prefix), []byte("null")) {
			return errors.New("credential.prefix 必须是字符串")
		}
		var value string
		if err := json.Unmarshal(wire.Prefix, &value); err != nil {
			return err
		}
		payload.Prefix = &value
	}
	return nil
}

type channelPayload struct {
	ID                     string             `json:"id"`
	Name                   string             `json:"name"`
	Note                   string             `json:"note"`
	Group                  string             `json:"group"`
	Protocol               storage.Protocol   `json:"protocol"`
	BaseURL                string             `json:"baseUrl"`
	Capabilities           []string           `json:"capabilities"`
	AdminState             string             `json:"adminState"`
	FailureAction          string             `json:"failureAction"`
	FailureThreshold       int                `json:"failureThreshold"`
	CustomHeaders          map[string]string  `json:"customHeaders"`
	ConcurrencyLimit       int                `json:"concurrencyLimit"`
	RequestTimeoutMs       int                `json:"requestTimeoutMs"`
	StreamIdleTimeoutMs    int                `json:"streamIdleTimeoutMs"`
	CooldownSeconds        int                `json:"cooldownSeconds"`
	Priority               int                `json:"priority"`
	FallbackModel          string             `json:"fallbackModel"`
	ReasoningEffort        string             `json:"reasoningEffort"`
	ServiceTierPassthrough bool               `json:"serviceTierPassthrough"`
	Credential             *credentialPayload `json:"credential"`
	credentialNull         bool
	priorityPresent        bool
}

type channelBundlePayload struct {
	Channel       channelPayload                `json:"channel"`
	Models        []string                      `json:"models"`
	ModelMappings []storage.ChannelModelMapping `json:"modelMappings"`
	ProbePolicy   storage.ProbePolicy           `json:"probePolicy"`
}

// channelModelsPayload 描述未保存渠道的临时模型发现请求。
type channelModelsPayload struct {
	Channel    channelPayload     `json:"channel"`
	Credential *credentialPayload `json:"credential"`
}

// channelInlineTestPayload 描述使用当前编辑值执行即时上游测试的请求。
type channelInlineTestPayload struct {
	Channel    channelPayload     `json:"channel"`
	Credential *credentialPayload `json:"credential"`
	Model      string             `json:"model"`
}

// UnmarshalJSON 严格解析渠道请求，并区分缺少 credential 与显式 null。
func (payload *channelPayload) UnmarshalJSON(data []byte) error {
	type wirePayload struct {
		ID                     string            `json:"id"`
		Name                   string            `json:"name"`
		Note                   string            `json:"note"`
		Group                  string            `json:"group"`
		Protocol               storage.Protocol  `json:"protocol"`
		BaseURL                string            `json:"baseUrl"`
		Capabilities           []string          `json:"capabilities"`
		AdminState             string            `json:"adminState"`
		FailureAction          string            `json:"failureAction"`
		FailureThreshold       int               `json:"failureThreshold"`
		CustomHeaders          map[string]string `json:"customHeaders"`
		ConcurrencyLimit       int               `json:"concurrencyLimit"`
		RequestTimeoutMs       int               `json:"requestTimeoutMs"`
		StreamIdleTimeoutMs    int               `json:"streamIdleTimeoutMs"`
		CooldownSeconds        int               `json:"cooldownSeconds"`
		Priority               json.RawMessage   `json:"priority"`
		FallbackModel          json.RawMessage   `json:"fallbackModel"`
		ReasoningEffort        string            `json:"reasoningEffort"`
		ServiceTierPassthrough json.RawMessage   `json:"serviceTierPassthrough"`
		Credential             json.RawMessage   `json:"credential"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wirePayload
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	*payload = channelPayload{ID: wire.ID, Name: wire.Name, Note: wire.Note, Group: wire.Group, Protocol: wire.Protocol, BaseURL: wire.BaseURL, Capabilities: wire.Capabilities, AdminState: wire.AdminState, FailureAction: wire.FailureAction, FailureThreshold: wire.FailureThreshold, CustomHeaders: wire.CustomHeaders, ConcurrencyLimit: wire.ConcurrencyLimit, RequestTimeoutMs: wire.RequestTimeoutMs, StreamIdleTimeoutMs: wire.StreamIdleTimeoutMs, CooldownSeconds: wire.CooldownSeconds, ReasoningEffort: wire.ReasoningEffort, ServiceTierPassthrough: true}
	if wire.ServiceTierPassthrough != nil {
		if bytes.Equal(bytes.TrimSpace(wire.ServiceTierPassthrough), []byte("null")) || json.Unmarshal(wire.ServiceTierPassthrough, &payload.ServiceTierPassthrough) != nil {
			return errors.New("serviceTierPassthrough 必须是布尔值")
		}
	}
	if payload.ReasoningEffort == "" {
		payload.ReasoningEffort = config.ReasoningEffortPassthrough
	}
	if wire.Priority != nil {
		if bytes.Equal(bytes.TrimSpace(wire.Priority), []byte("null")) {
			return errors.New("渠道优先级必须是整数")
		}
		if err := json.Unmarshal(wire.Priority, &payload.Priority); err != nil {
			return errors.New("渠道优先级必须是整数")
		}
		payload.priorityPresent = true
	} else {
		payload.Priority = 100
	}
	if wire.FallbackModel != nil {
		if bytes.Equal(bytes.TrimSpace(wire.FallbackModel), []byte("null")) {
			return errors.New("兜底模型必须是字符串")
		}
		if err := json.Unmarshal(wire.FallbackModel, &payload.FallbackModel); err != nil {
			return errors.New("兜底模型必须是字符串")
		}
	}
	if wire.Credential == nil {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(wire.Credential), []byte("null")) {
		payload.credentialNull = true
		return nil
	}
	credentialDecoder := json.NewDecoder(bytes.NewReader(wire.Credential))
	credentialDecoder.DisallowUnknownFields()
	var credential credentialPayload
	if err := credentialDecoder.Decode(&credential); err != nil {
		return err
	}
	if err := ensureJSONEOF(credentialDecoder); err != nil {
		return err
	}
	payload.Credential = &credential
	return nil
}

type storedCredential struct {
	Type       string `json:"type"`
	Secret     string `json:"secret"`
	HeaderName string `json:"headerName"`
	Prefix     string `json:"prefix"`
}

// storageChannel 将管理请求转换为不含明文凭证的存储模型。
func (payload channelPayload) storageChannel(secretRef string) storage.Channel {
	priority := payload.Priority
	if !payload.priorityPresent && priority == 0 {
		priority = 100
	}
	return storage.Channel{
		ID: payload.ID, Name: payload.Name, Note: payload.Note, Group: payload.Group, Protocol: payload.Protocol, BaseURL: payload.BaseURL,
		Capabilities: payload.Capabilities, AdminState: payload.AdminState, SecretRef: secretRef, FailureAction: payload.FailureAction, FailureThreshold: payload.FailureThreshold,
		CustomHeaders: payload.CustomHeaders, ConcurrencyLimit: payload.ConcurrencyLimit, RequestTimeoutMs: payload.RequestTimeoutMs, StreamIdleTimeoutMs: payload.StreamIdleTimeoutMs, CooldownSeconds: payload.CooldownSeconds, Priority: priority, FallbackModel: payload.FallbackModel, ReasoningEffort: payload.ReasoningEffort, ServiceTierPassthrough: payload.ServiceTierPassthrough,
	}
}

// normalizeCredential 校验凭证类型并补齐 Header 注入默认值。
func normalizeCredential(payload credentialPayload) (storedCredential, error) {
	credential := storedCredential{Type: strings.TrimSpace(payload.Type), Secret: payload.Secret}
	if payload.HeaderName != nil {
		credential.HeaderName = strings.TrimSpace(*payload.HeaderName)
	}
	if payload.Prefix != nil {
		credential.Prefix = *payload.Prefix
	}
	if strings.TrimSpace(credential.Secret) == "" {
		return storedCredential{}, errors.New("渠道凭证不能为空")
	}
	if strings.ContainsAny(credential.Secret, "\r\n") || strings.ContainsAny(credential.HeaderName, "\r\n") || strings.ContainsAny(credential.Prefix, "\r\n") {
		return storedCredential{}, errors.New("渠道凭证不能包含换行符")
	}
	switch credential.Type {
	case "bearer":
		if payload.HeaderName == nil {
			credential.HeaderName = "Authorization"
		}
		if payload.Prefix == nil {
			credential.Prefix = "Bearer "
		}
	case "api_key":
		if payload.HeaderName == nil {
			credential.HeaderName = "x-api-key"
		}
	case "custom":
		if payload.HeaderName == nil || credential.HeaderName == "" {
			return storedCredential{}, errors.New("custom 凭证必须提供 Header 名称")
		}
	default:
		return storedCredential{}, fmt.Errorf("凭证类型无效: %s", credential.Type)
	}
	if !isValidHeaderName(credential.HeaderName) {
		return storedCredential{}, errors.New("凭证 Header 名称无效")
	}
	return credential, nil
}

// isValidHeaderName 校验 HTTP Header 名称的 token 字符，避免注入非法字段。
func isValidHeaderName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !valid {
			switch character {
			case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
				valid = true
			}
		}
		if !valid {
			return false
		}
	}
	return true
}

// putCredential 将凭证元数据与秘密值整体写入平台密钥环。
func (s *Service) putCredential(payload credentialPayload) (string, error) {
	credential, err := normalizeCredential(payload)
	if err != nil {
		return "", err
	}
	value, err := json.Marshal(credential)
	if err != nil {
		return "", fmt.Errorf("序列化渠道凭证失败: %w", err)
	}
	ref, err := secret.NewRef()
	if err != nil {
		return "", err
	}
	if err := s.secrets.Put(ref, value); err != nil {
		return "", err
	}
	return ref, nil
}

// saveChannelBundle 保存渠道及其模型、探针配置，返回聚合后的渠道记录。
func (s *Service) saveChannelBundle(request *http.Request, bundle channelBundlePayload, channelID string) (storage.Channel, error) {
	if bundle.Channel.credentialNull {
		return storage.Channel{}, errors.New("credential 必须是对象，不能为 null")
	}
	if channelID != "" {
		bundle.Channel.ID = channelID
	}
	existing, lookupErr := storage.GetChannel(s.database, bundle.Channel.ID)
	isCreate := errors.Is(lookupErr, storage.ErrNotFound)
	if lookupErr != nil && !isCreate {
		return storage.Channel{}, lookupErr
	}
	if channelID != "" && isCreate {
		return storage.Channel{}, storage.ErrNotFound
	}
	secretRef := ""
	if !isCreate {
		secretRef = existing.SecretRef
	}
	newSecretRef := ""
	if bundle.Channel.Credential != nil {
		var err error
		newSecretRef, err = s.putCredential(*bundle.Channel.Credential)
		if err != nil {
			return storage.Channel{}, err
		}
		secretRef = newSecretRef
	}
	channel := bundle.Channel.storageChannel(secretRef)
	saved, err := storage.SaveChannelBundle(s.database, channel, bundle.Models, bundle.ModelMappings, bundle.ProbePolicy)
	if err != nil {
		if newSecretRef != "" {
			_ = s.secrets.Delete(newSecretRef)
		}
		return storage.Channel{}, err
	}
	if newSecretRef != "" && existing.SecretRef != "" && existing.SecretRef != newSecretRef {
		if deleteErr := s.secrets.Delete(existing.SecretRef); deleteErr != nil {
			s.logger.Warn("删除旧渠道凭证失败", "channel", saved.ID, "error", deleteErr)
			if retainErr := s.retainPendingSecretRefs([]string{existing.SecretRef}); retainErr != nil {
				s.logger.Error("保存待清理渠道凭证引用失败", "channel", saved.ID, "error", retainErr)
				s.recordAudit("channels.bundle.save", saved.ID, request)
				return saved, fmt.Errorf("渠道已保存，但旧凭证待清理记录保存失败: %w", retainErr)
			}
		}
	}
	s.recordAudit("channels.bundle.save", saved.ID, request)
	return saved, nil
}

type discoveredModel struct {
	ID string `json:"id"`
}

type channelTestPayload struct {
	Model string `json:"model"`
}

type channelStatePayload struct {
	AdminState string `json:"adminState"`
}

// readChannelTestModel 读取渠道测试请求中的可选模型字段。
func readChannelTestModel(request *http.Request) (string, error) {
	if request.Body == nil {
		return "", nil
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 16<<10+1))
	if err != nil {
		return "", errors.New("读取测试模型失败")
	}
	if len(body) > 16<<10 {
		return "", errors.New("测试请求过大")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var payload channelTestPayload
	if err := decoder.Decode(&payload); err != nil || ensureJSONEOF(decoder) != nil {
		return "", errors.New("测试请求必须是包含 model 字段的 JSON")
	}
	return strings.TrimSpace(payload.Model), nil
}

// discoverChannelModels 从已保存渠道的上游 /v1/models 读取可用模型 ID。
func (s *Service) discoverChannelModels(parent *http.Request, channelID string) ([]string, error) {
	s.dataMu.RLock()
	target, err := storage.GetProbeTarget(s.database, channelID)
	if err != nil {
		s.dataMu.RUnlock()
		return nil, err
	}
	target = cloneRouteTarget(target)
	credential, credErr := s.loadStoredCredential(target.SecretRef)
	s.dataMu.RUnlock()
	if credErr != nil {
		return nil, credErr
	}
	return s.discoverModelsWithTarget(parent, target, &credential)
}

// discoverChannelModelsFromPayload 使用当前编辑表单的地址、请求头和凭证发现模型，不要求先保存渠道。
func (s *Service) discoverChannelModelsFromPayload(parent *http.Request, payload channelModelsPayload) ([]string, error) {
	if payload.Credential == nil {
		return nil, errors.New("从上游发现模型前必须提供访问凭证")
	}
	credential, err := normalizeCredential(*payload.Credential)
	if err != nil {
		return nil, err
	}
	target := storage.RouteTarget{
		Protocol:         payload.Channel.Protocol,
		BaseURL:          strings.TrimSpace(payload.Channel.BaseURL),
		CustomHeaders:    cloneStringMap(payload.Channel.CustomHeaders),
		RequestTimeoutMs: payload.Channel.RequestTimeoutMs,
	}
	return s.discoverModelsWithTarget(parent, target, &credential)
}

// discoverModelsWithTarget 通过给定目标执行一次只读模型目录请求。
func (s *Service) discoverModelsWithTarget(parent *http.Request, target storage.RouteTarget, credential *storedCredential) ([]string, error) {
	endpoint, err := joinUpstreamURL(target.BaseURL, "/v1/models", "")
	if err != nil {
		return nil, err
	}
	ctx := parent.Context()
	timeoutMs := target.RequestTimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 120000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for name, value := range target.CustomHeaders {
		if strings.TrimSpace(name) != "" && !strings.ContainsAny(name+value, "\r\n") {
			request.Header.Set(name, value)
		}
	}
	applyProtocolRequestHeaders(request, target.Protocol)
	appliedCredential := credential
	if appliedCredential != nil {
		if err := applyCredentialValue(request, *appliedCredential); err != nil {
			return nil, err
		}
	} else {
		stored, err := s.loadStoredCredential(target.SecretRef)
		if err != nil {
			return nil, err
		}
		if err := applyCredentialValue(request, stored); err != nil {
			return nil, err
		}
		appliedCredential = &stored
	}
	ensureAnthropicAPIKeyHeader(request, target.Protocol, appliedCredential)
	response, err := (&http.Client{CheckRedirect: rejectUpstreamRedirect}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("读取上游模型失败: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取上游模型响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("上游模型接口返回 HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data   []discoveredModel `json:"data"`
		Models []discoveredModel `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("上游模型响应不是有效 JSON")
	}
	items := envelope.Data
	if len(items) == 0 {
		items = envelope.Models
	}
	seen := make(map[string]struct{}, len(items))
	models := make([]string, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	if len(models) == 0 {
		return nil, errors.New("上游未返回可用模型")
	}
	return models, nil
}

// testChannelFromPayload 使用未保存的渠道地址、凭证和模型执行一次最小推理测试。
func (s *Service) testChannelFromPayload(parent *http.Request, payload channelInlineTestPayload) (map[string]any, error) {
	if payload.Credential == nil {
		return nil, errors.New("渠道测试前必须提供访问凭证")
	}
	model := strings.TrimSpace(payload.Model)
	if model == "" {
		return nil, errors.New("渠道测试前必须提供模型")
	}
	credential, err := normalizeCredential(*payload.Credential)
	if err != nil {
		return nil, err
	}
	target := storage.RouteTarget{
		Protocol:         payload.Channel.Protocol,
		BaseURL:          strings.TrimSpace(payload.Channel.BaseURL),
		UpstreamModel:    model,
		CustomHeaders:    payload.Channel.CustomHeaders,
		RequestTimeoutMs: payload.Channel.RequestTimeoutMs,
	}
	if target.Protocol == "" {
		return nil, errors.New("渠道测试前必须选择协议")
	}
	if target.BaseURL == "" {
		return nil, errors.New("渠道测试前必须提供上游 Base URL")
	}
	started := time.Now()
	if err := s.sendProbeWithCredential(parent.Context(), target, storage.ProbePolicy{Mode: "minimal_inference", RequestTimeoutMs: target.RequestTimeoutMs}, &credential); err != nil {
		return nil, err
	}
	return map[string]any{"status": "success", "latencyMs": time.Since(started).Milliseconds()}, nil
}

// channelsAPI 提供渠道目录的增删改查接口。
func (s *Service) channelsAPI(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/api/admin/v1/channels")
	id = strings.Trim(id, "/")
	probeRequest := request.Method == http.MethodPost && (id == "test" || strings.HasSuffix(id, "/test") || strings.HasSuffix(id, "/probe"))
	discoverRequest := request.Method == http.MethodPost && (id == "models" || strings.HasSuffix(id, "/models"))
	directoryRequest := request.Method == http.MethodGet && id == "directory"
	if !probeRequest && !discoverRequest && !directoryRequest {
		if request.Method == http.MethodGet {
			s.dataMu.RLock()
			defer s.dataMu.RUnlock()
		} else {
			s.dataMu.Lock()
			defer s.dataMu.Unlock()
		}
	}
	if id == "directory" {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道目录仅支持 GET"})
			return
		}
		s.dataMu.RLock()
		directory, err := storage.ListChannelDirectory(s.database)
		s.dataMu.RUnlock()
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		s.channelMu.Lock()
		for index := range directory.Channels {
			directory.Channels[index].InFlight = s.channelInFlight[directory.Channels[index].ID]
		}
		s.channelMu.Unlock()
		writeJSON(writer, http.StatusOK, directory)
		return
	}
	if id == "test" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道即时测试仅支持 POST"})
			return
		}
		var payload channelInlineTestPayload
		if !decodeJSON(writer, request, &payload) {
			return
		}
		result, err := s.testChannelFromPayload(request, payload)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	if id == "bundle" || strings.HasSuffix(id, "/bundle") {
		channelID := strings.TrimSuffix(id, "/bundle")
		if id == "bundle" {
			channelID = ""
		}
		if request.Method != http.MethodPost && request.Method != http.MethodPut {
			writer.Header().Set("Allow", "POST, PUT")
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道聚合配置仅支持 POST 和 PUT"})
			return
		}
		if request.Method == http.MethodPost && channelID != "" {
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "新增渠道聚合配置请使用 /channels/bundle"})
			return
		}
		var bundle channelBundlePayload
		if !decodeJSON(writer, request, &bundle) {
			return
		}
		if request.Method == http.MethodPost && strings.TrimSpace(bundle.Channel.ID) != "" {
			if _, lookupErr := storage.GetChannel(s.database, bundle.Channel.ID); lookupErr == nil {
				writeJSON(writer, http.StatusConflict, map[string]string{"error": "渠道 ID 已存在"})
				return
			} else if !errors.Is(lookupErr, storage.ErrNotFound) {
				writeStorageError(writer, lookupErr)
				return
			}
		}
		saved, err := s.saveChannelBundle(request, bundle, channelID)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		status := http.StatusOK
		if request.Method == http.MethodPost {
			status = http.StatusCreated
		}
		writeJSON(writer, status, saved)
		return
	}
	if id == "models" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型发现仅支持 POST"})
			return
		}
		var payload channelModelsPayload
		if !decodeJSON(writer, request, &payload) {
			return
		}
		models, err := s.discoverChannelModelsFromPayload(request, payload)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"models": models})
		return
	}
	if strings.HasSuffix(id, "/models") {
		channelID := strings.TrimSuffix(id, "/models")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "模型发现仅支持 POST"})
			return
		}
		models, err := s.discoverChannelModels(request, channelID)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"channelId": channelID, "models": models})
		return
	}
	if strings.HasSuffix(id, "/credential") {
		channelID := strings.TrimSuffix(id, "/credential")
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "凭证仅支持 GET"})
			return
		}
		channel, err := storage.GetChannel(s.database, channelID)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		if channel.SecretRef == "" {
			writeJSON(writer, http.StatusNotFound, map[string]string{"error": "渠道尚未配置凭证"})
			return
		}
		value, err := s.secrets.Get(channel.SecretRef)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "读取渠道凭证失败"})
			return
		}
		var credential storedCredential
		if err := json.Unmarshal(value, &credential); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "解析渠道凭证失败"})
			return
		}
		token := requestBearerToken(request)
		if token == "" {
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "需要有效的管理令牌"})
			return
		}
		ciphertext, nonce, wrapErr := wrapChannelCredentialSecret(token, credential.Secret)
		if wrapErr != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "包装渠道凭证失败"})
			return
		}
		// 持有有效管理令牌的人在浏览器里仍能解密。目标是避免响应正文被日志或中间层直接看到密钥，不是对管理员本人保密。
		writeJSON(writer, http.StatusOK, map[string]any{
			"type":       credential.Type,
			"headerName": credential.HeaderName,
			"prefix":     credential.Prefix,
			"ciphertext": ciphertext,
			"nonce":      nonce,
		})
		return
	}
	if strings.HasSuffix(id, "/test") {
		channelID := strings.TrimSuffix(id, "/test")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道测试仅支持 POST"})
			return
		}
		model, err := readChannelTestModel(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		run, err := s.executeProbe(request.Context(), channelID, true, model)
		if err != nil {
			s.recordAuditRequest(request, "probes.run", channelID, "failed")
			writeStorageError(writer, err)
			return
		}
		s.recordAuditRequest(request, "probes.run", channelID, "success")
		writeJSON(writer, http.StatusOK, run)
		return
	}
	if strings.HasSuffix(id, "/copy") {
		channelID := strings.TrimSuffix(id, "/copy")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道复制仅支持 POST"})
			return
		}
		existing, err := storage.GetChannel(s.database, channelID)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		copyChannel := existing
		copyChannel.ID = ""
		copyChannel.HealthState = "healthy"
		copyChannel.HealthVersion = 0
		copyName, nameErr := storage.NextChannelCopyName(s.database, existing.Name)
		if nameErr != nil {
			writeStorageError(writer, nameErr)
			return
		}
		copyChannel.Name = copyName
		copyChannel.CreatedAt, copyChannel.UpdatedAt = "", ""
		mappings, mappingErr := storage.ListChannelModelMappings(s.database)
		if mappingErr != nil {
			writeStorageError(writer, mappingErr)
			return
		}
		copiedMappings := make([]storage.ChannelModelMapping, 0)
		for _, mapping := range mappings {
			if mapping.ChannelID == channelID {
				mapping.ChannelID = ""
				copiedMappings = append(copiedMappings, mapping)
			}
		}
		models, modelErr := storage.ListChannelModels(s.database, channelID)
		if modelErr != nil {
			writeStorageError(writer, modelErr)
			return
		}
		policy, policyErr := storage.GetProbePolicy(s.database, channelID)
		if policyErr != nil {
			writeStorageError(writer, policyErr)
			return
		}
		policy.ChannelID = ""
		if existing.SecretRef != "" {
			secretValue, getErr := s.secrets.Get(existing.SecretRef)
			if getErr != nil {
				writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "复制渠道凭证读取失败"})
				return
			}
			copyChannel.SecretRef, err = secret.NewRef()
			if err == nil {
				err = s.secrets.Put(copyChannel.SecretRef, secretValue)
			}
		}
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		created, err := storage.SaveChannelBundle(s.database, copyChannel, models, copiedMappings, policy)
		if err != nil {
			if copyChannel.SecretRef != "" {
				_ = s.secrets.Delete(copyChannel.SecretRef)
			}
			writeStorageError(writer, err)
			return
		}
		s.recordAudit("channels.copy", created.ID, request)
		writeJSON(writer, http.StatusCreated, created)
		return
	}
	if strings.HasSuffix(id, "/probe") {
		channelID := strings.TrimSuffix(id, "/probe")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "手动探针仅支持 POST"})
			return
		}
		model, err := readChannelTestModel(request)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		run, err := s.executeProbe(request.Context(), channelID, true, model)
		if err != nil {
			s.recordAuditRequest(request, "probes.run", channelID, "failed")
			writeStorageError(writer, err)
			return
		}
		s.recordAuditRequest(request, "probes.run", channelID, "success")
		writeJSON(writer, http.StatusOK, run)
		return
	}
	if strings.HasSuffix(id, "/health") {
		channelID := strings.TrimSuffix(id, "/health")
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "人工恢复仅支持 POST"})
			return
		}
		if err := storage.ResetChannelHealth(s.database, channelID, time.Now()); err != nil {
			s.recordAuditRequest(request, "channels.health.manual_recover", channelID, "failed")
			writeStorageError(writer, err)
			return
		}
		channel, err := storage.GetChannel(s.database, channelID)
		if err != nil {
			s.recordAuditRequest(request, "channels.health.manual_recover", channelID, "failed")
			writeStorageError(writer, err)
			return
		}
		s.recordAuditRequest(request, "channels.health.manual_recover", channelID, "success")
		writeJSON(writer, http.StatusOK, channel)
		return
	}
	if strings.HasSuffix(id, "/state") {
		channelID := strings.TrimSuffix(id, "/state")
		if request.Method != http.MethodPut {
			writer.Header().Set("Allow", http.MethodPut)
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道人工状态仅支持 PUT"})
			return
		}
		var payload channelStatePayload
		if !decodeJSON(writer, request, &payload) {
			return
		}
		channel, err := storage.UpdateChannelAdminState(s.database, channelID, payload.AdminState)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		s.recordAudit("channels.state.update", channelID, request)
		writeJSON(writer, http.StatusOK, channel)
		return
	}
	if id != "" {
		if request.Method == http.MethodGet {
			channel, err := storage.GetChannel(s.database, id)
			if err != nil {
				writeStorageError(writer, err)
				return
			}
			mappings, err := storage.ListChannelModelMappings(s.database)
			if err != nil {
				writeStorageError(writer, err)
				return
			}
			filtered := make([]storage.ChannelModelMapping, 0)
			for _, mapping := range mappings {
				if mapping.ChannelID == id {
					filtered = append(filtered, mapping)
				}
			}
			models, err := storage.ListChannelModels(s.database, id)
			if err != nil {
				writeStorageError(writer, err)
				return
			}
			policy, err := storage.GetProbePolicy(s.database, id)
			if err != nil {
				writeStorageError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"channel": channel, "models": models, "modelMappings": filtered, "probePolicy": policy})
			return
		}
		if request.Method != http.MethodDelete {
			writer.Header().Set("Allow", "GET, DELETE")
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道详情仅支持 GET 和 DELETE；保存请使用聚合配置接口"})
			return
		}
		existing, err := storage.GetChannel(s.database, id)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		inFlight, _, err := s.channelConcurrencySnapshot(id)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		if inFlight > 0 {
			writeJSON(writer, http.StatusConflict, map[string]string{"error": "渠道仍有在途请求，请稍后重试"})
			return
		}
		if err := storage.DeleteChannel(s.database, id); err != nil {
			writeStorageError(writer, err)
			return
		}
		if existing.SecretRef != "" {
			if err := s.secrets.Delete(existing.SecretRef); err != nil {
				s.logger.Warn("渠道已删除，旧凭证等待后续清理", "channel", id, "error", err)
				if retainErr := s.retainPendingSecretRefs([]string{existing.SecretRef}); retainErr != nil {
					s.logger.Error("保存待清理渠道凭证引用失败", "channel", id, "error", retainErr)
					writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "渠道已删除，但旧凭证待清理记录保存失败"})
					return
				}
			}
		}
		s.recordAudit("channels.delete", id, request)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method == http.MethodGet {
		channels, err := storage.ListChannels(s.database)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, channels)
		return
	}
	writer.Header().Set("Allow", http.MethodGet)
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "渠道列表仅支持 GET；新建请使用 /channels/bundle"})
}

// cloneStringMap 复制可变请求头，避免发现快照与后续编辑共享同一 map。
func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

// cloneRouteTarget 复制渠道目标快照，使网络调用不依赖持锁期间的可变字段。
func cloneRouteTarget(target storage.RouteTarget) storage.RouteTarget {
	target.CustomHeaders = cloneStringMap(target.CustomHeaders)
	return target
}

// decodeJSON 读取严格 JSON 请求体并统一返回错误。
func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, parseErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "application/json" {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "请求 Content-Type 必须是 application/json"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求 JSON 无效"})
		return false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求 JSON 无效"})
		return false
	}
	return true
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("请求 JSON 只能包含一个值")
		}
		return err
	}
	return nil
}

// writeStorageError 将存储层错误映射为管理 API 状态码。
func writeStorageError(writer http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, storage.ErrChannelHasHistory) {
		writeJSON(writer, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "记录不存在"})
		return
	}
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "FOREIGN KEY constraint") {
		status = http.StatusConflict
	}
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

// wrapChannelCredentialSecret 用当前管理 Bearer 经 HKDF-SHA256 派生 AES-256-GCM 密钥后加密渠道秘密。
func wrapChannelCredentialSecret(adminToken, secretValue string) (string, string, error) {
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(adminToken), []byte(channelCredentialHKDFSalt), []byte(channelCredentialHKDFInfo))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return "", "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	sealed := aead.Seal(nil, nonce, []byte(secretValue), nil)
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonce), nil
}
