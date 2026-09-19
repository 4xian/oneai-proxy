package server

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/secret"
	"github.com/4xian/oneai-proxy/internal/storage"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	exportFormat        = "oneai-proxy-export"
	exportSchemaVersion = 8
	legacyExportSchema  = 7
	exportAppVersion    = "0.1.0"
	kdfMemoryKiB        = 65536
	kdfIterations       = 3
	kdfParallelism      = 2
)

// supportedExportSchemaVersion 接受当前导出版本，以及仍可忽略 routeGroups 的上一版。
func supportedExportSchemaVersion(version int) bool {
	return version == exportSchemaVersion || version == legacyExportSchema
}

type exportCredential struct {
	Type              string `json:"type"`
	Secret            string `json:"secret"`
	HeaderName        string `json:"headerName"`
	Prefix            string `json:"prefix"`
	headerNameNull    bool
	prefixNull        bool
	headerNamePresent bool
	prefixPresent     bool
}

// UnmarshalJSON 严格解析导出凭证，拒绝显式 null 和未声明字段。
func (credential *exportCredential) UnmarshalJSON(data []byte) error {
	type wireCredential struct {
		Type       json.RawMessage `json:"type"`
		Secret     json.RawMessage `json:"secret"`
		HeaderName json.RawMessage `json:"headerName"`
		Prefix     json.RawMessage `json:"prefix"`
	}
	var wire wireCredential
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "credential", "type", "secret"); err != nil {
		return err
	}
	if err := json.Unmarshal(wire.Type, &credential.Type); err != nil {
		return errors.New("credential.type 必须是字符串")
	}
	if err := json.Unmarshal(wire.Secret, &credential.Secret); err != nil {
		return errors.New("credential.secret 必须是字符串")
	}
	*credential = exportCredential{Type: credential.Type, Secret: credential.Secret}
	if wire.HeaderName != nil {
		credential.headerNamePresent = true
		if bytes.Equal(bytes.TrimSpace(wire.HeaderName), []byte("null")) {
			credential.headerNameNull = true
		} else if err := json.Unmarshal(wire.HeaderName, &credential.HeaderName); err != nil {
			return err
		}
	}
	if wire.Prefix != nil {
		credential.prefixPresent = true
		if bytes.Equal(bytes.TrimSpace(wire.Prefix), []byte("null")) {
			credential.prefixNull = true
		} else if err := json.Unmarshal(wire.Prefix, &credential.Prefix); err != nil {
			return err
		}
	}
	return nil
}

type exportChannel struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Note                   string            `json:"note,omitempty"`
	Group                  string            `json:"group"`
	Protocol               storage.Protocol  `json:"protocol"`
	BaseURL                string            `json:"baseUrl"`
	Capabilities           []string          `json:"capabilities"`
	AdminState             string            `json:"adminState"`
	FailureAction          string            `json:"failureAction"`
	FailureThreshold       int               `json:"failureThreshold"`
	CustomHeaders          map[string]string `json:"customHeaders,omitempty"`
	ConcurrencyLimit       int               `json:"concurrencyLimit,omitempty"`
	RequestTimeoutMs       int               `json:"requestTimeoutMs,omitempty"`
	StreamIdleTimeoutMs    int               `json:"streamIdleTimeoutMs,omitempty"`
	CooldownSeconds        int               `json:"cooldownSeconds,omitempty"`
	Priority               int               `json:"priority"`
	FallbackModel          string            `json:"fallbackModel"`
	ReasoningEffort        string            `json:"reasoningEffort"`
	ServiceTierPassthrough bool              `json:"serviceTierPassthrough"`
	CreatedAt              string            `json:"createdAt,omitempty"`
	UpdatedAt              string            `json:"updatedAt,omitempty"`
	Credential             *exportCredential `json:"credential,omitempty"`
	credentialPresent      bool
	credentialNull         bool
}

type exportRequestPolicy struct {
	ConnectTimeoutMs    int `json:"connectTimeoutMs"`
	FirstByteTimeoutMs  int `json:"firstByteTimeoutMs"`
	StreamIdleTimeoutMs int `json:"streamIdleTimeoutMs"`
	TotalTimeoutMs      int `json:"totalTimeoutMs"`
	MaxChannelAttempts  int `json:"maxChannelAttempts"`
}

type exportLogging struct {
	ContentPolicy           string `json:"contentPolicy"`
	RetentionDays           int    `json:"retentionDays,omitempty"`
	RequestRetentionDays    int    `json:"requestRetentionDays"`
	AuditRetentionDays      int    `json:"auditRetentionDays"`
	RuntimeRetentionDays    int    `json:"runtimeRetentionDays"`
	MaxContentBytes         int    `json:"maxContentBytes,omitempty"`
	MaxRequestContentBytes  int    `json:"maxRequestContentBytes"`
	MaxResponseContentBytes int    `json:"maxResponseContentBytes"`
	DiskQuotaBytes          int64  `json:"diskQuotaBytes,omitempty"`
	RuntimeLogMaxBytes      int64  `json:"runtimeLogMaxBytes"`
}

type exportSettings struct {
	ProxyListener         listenerPayload        `json:"proxyListener"`
	AdminListener         listenerPayload        `json:"adminListener"`
	RequestPolicy         exportRequestPolicy    `json:"requestPolicy"`
	ChannelSettings       config.ChannelSettings `json:"channelSettings"`
	Logging               exportLogging          `json:"logging"`
	Timezone              string                 `json:"timezone"`
	ModelCatalogSourceURL string                 `json:"modelCatalogSourceUrl"`
}

type exportAuthTokens struct {
	AdminToken string `json:"adminToken"`
	ProxyToken string `json:"proxyToken"`
}

type exportDocument struct {
	Format               string                        `json:"format"`
	SchemaVersion        int                           `json:"schemaVersion"`
	ExportMode           string                        `json:"exportMode"`
	ExportedAt           string                        `json:"exportedAt"`
	AppVersion           string                        `json:"appVersion"`
	Channels             []exportChannel               `json:"channels"`
	ChannelModels        []storage.ChannelModel        `json:"channelModels"`
	GlobalModelMappings  storage.GlobalModelMappingSet `json:"globalModelMappings"`
	ModelCatalog         []storage.ModelCatalogEntry   `json:"modelCatalog"`
	ChannelModelMappings []storage.ChannelModelMapping `json:"channelModelMappings"`
	ProbePolicies        []storage.ProbePolicy         `json:"probePolicies"`
	Settings             exportSettings                `json:"settings"`
	Tokens               *exportAuthTokens             `json:"tokens,omitempty"`
}

// UnmarshalJSON 严格解析导出文档的顶层必填字段；旧包中的 routeGroups 只忽略不写入。
func (document *exportDocument) UnmarshalJSON(data []byte) error {
	type wireDocument struct {
		Format               string                        `json:"format"`
		SchemaVersion        int                           `json:"schemaVersion"`
		ExportMode           string                        `json:"exportMode"`
		ExportedAt           string                        `json:"exportedAt"`
		AppVersion           string                        `json:"appVersion"`
		Channels             []exportChannel               `json:"channels"`
		ChannelModels        []storage.ChannelModel        `json:"channelModels"`
		RouteGroups          json.RawMessage               `json:"routeGroups"`
		GlobalModelMappings  storage.GlobalModelMappingSet `json:"globalModelMappings"`
		ModelCatalog         []storage.ModelCatalogEntry   `json:"modelCatalog"`
		ChannelModelMappings []storage.ChannelModelMapping `json:"channelModelMappings"`
		ProbePolicies        []storage.ProbePolicy         `json:"probePolicies"`
		Settings             exportSettings                `json:"settings"`
		Tokens               *exportAuthTokens             `json:"tokens"`
	}
	var value wireDocument
	fields, err := decodeStrictObject(data, &value)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "导出文档", "format", "schemaVersion", "exportMode", "exportedAt", "appVersion", "channels", "channelModels", "globalModelMappings", "modelCatalog", "channelModelMappings", "probePolicies", "settings"); err != nil {
		return err
	}
	for _, field := range []string{"channels", "channelModels", "modelCatalog", "channelModelMappings", "probePolicies"} {
		if err := requireJSONArray(fields[field], field); err != nil {
			return err
		}
	}
	if err := validateArrayObjectsOptional(fields["channels"], "channels", []string{"id", "name", "group", "protocol", "baseUrl", "capabilities", "adminState", "failureAction", "failureThreshold", "priority", "fallbackModel", "reasoningEffort", "serviceTierPassthrough"}, []string{"note", "credential", "customHeaders", "concurrencyLimit", "requestTimeoutMs", "streamIdleTimeoutMs", "cooldownSeconds", "createdAt", "updatedAt"}); err != nil {
		return err
	}
	if err := validateArrayObjects(fields["channelModels"], "channelModels", []string{"channelId", "model"}); err != nil {
		return err
	}
	if err := validateGlobalMappingSetJSON(fields["globalModelMappings"]); err != nil {
		return err
	}
	if err := validateArrayObjectsOptional(fields["modelCatalog"], "modelCatalog", []string{"stableKey", "modelId", "modalities", "capabilities", "pricing", "protocol", "sourceType", "sourceStatus"}, []string{"baseModelId", "displayName", "vendor", "provider", "modelType", "description", "contextWindow", "maxInputTokens", "maxOutputTokens", "currency", "billingUnit", "sourceUrl", "sourceAdapter", "sourceVersion", "syncedAt", "publishedAt", "createdAt", "updatedAt"}); err != nil {
		return err
	}
	if err := validateArrayObjects(fields["channelModelMappings"], "channelModelMappings", []string{"channelId", "protocol", "logicalModel", "upstreamModel"}); err != nil {
		return err
	}
	if err := validateArrayObjectsOptional(fields["probePolicies"], "probePolicies", []string{"channelId", "enabled", "mode", "autoRecover", "recoverySuccessThreshold"}, []string{"intervalSeconds", "model", "path", "failureThreshold", "requestTimeoutMs"}); err != nil {
		return err
	}
	*document = exportDocument{
		Format: value.Format, SchemaVersion: value.SchemaVersion, ExportMode: value.ExportMode,
		ExportedAt: value.ExportedAt, AppVersion: value.AppVersion, Channels: value.Channels,
		ChannelModels: value.ChannelModels, GlobalModelMappings: value.GlobalModelMappings,
		ModelCatalog: value.ModelCatalog, ChannelModelMappings: value.ChannelModelMappings,
		ProbePolicies: value.ProbePolicies, Settings: value.Settings, Tokens: value.Tokens,
	}
	return nil
}

// UnmarshalJSON 严格解析渠道字段，并区分缺少 credential 与显式 null。
func (channel *exportChannel) UnmarshalJSON(data []byte) error {
	type wireChannel struct {
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
		Priority               int               `json:"priority"`
		FallbackModel          string            `json:"fallbackModel"`
		ReasoningEffort        string            `json:"reasoningEffort"`
		ServiceTierPassthrough bool              `json:"serviceTierPassthrough"`
		CreatedAt              string            `json:"createdAt"`
		UpdatedAt              string            `json:"updatedAt"`
		Credential             json.RawMessage   `json:"credential"`
	}
	var wire wireChannel
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "渠道", "id", "name", "group", "protocol", "baseUrl", "capabilities", "adminState", "failureAction", "failureThreshold", "priority", "fallbackModel", "reasoningEffort", "serviceTierPassthrough"); err != nil {
		return err
	}
	if err := requireJSONArray(fields["capabilities"], "channels[].capabilities"); err != nil {
		return err
	}
	*channel = exportChannel{ID: wire.ID, Name: wire.Name, Note: wire.Note, Group: wire.Group, Protocol: wire.Protocol, BaseURL: wire.BaseURL, Capabilities: wire.Capabilities, AdminState: wire.AdminState, FailureAction: wire.FailureAction, FailureThreshold: wire.FailureThreshold, CustomHeaders: wire.CustomHeaders, ConcurrencyLimit: wire.ConcurrencyLimit, RequestTimeoutMs: wire.RequestTimeoutMs, StreamIdleTimeoutMs: wire.StreamIdleTimeoutMs, CooldownSeconds: wire.CooldownSeconds, Priority: wire.Priority, FallbackModel: wire.FallbackModel, ReasoningEffort: wire.ReasoningEffort, ServiceTierPassthrough: wire.ServiceTierPassthrough, CreatedAt: wire.CreatedAt, UpdatedAt: wire.UpdatedAt}
	if wire.Credential == nil {
		return nil
	}
	channel.credentialPresent = true
	if bytes.Equal(bytes.TrimSpace(wire.Credential), []byte("null")) {
		channel.credentialNull = true
		return nil
	}
	var credential exportCredential
	if err := decodeStrictBytes(wire.Credential, &credential); err != nil {
		return err
	}
	channel.Credential = &credential
	return nil
}

// UnmarshalJSON 严格解析运行设置及所有嵌套策略字段。
func (settings *exportSettings) UnmarshalJSON(data []byte) error {
	type wireSettings struct {
		ProxyListener         json.RawMessage `json:"proxyListener"`
		AdminListener         json.RawMessage `json:"adminListener"`
		RequestPolicy         json.RawMessage `json:"requestPolicy"`
		Logging               json.RawMessage `json:"logging"`
		ChannelSettings       json.RawMessage `json:"channelSettings"`
		Timezone              json.RawMessage `json:"timezone"`
		ModelCatalogSourceURL json.RawMessage `json:"modelCatalogSourceUrl"`
	}
	var wire wireSettings
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "settings", "proxyListener", "adminListener", "requestPolicy", "channelSettings", "logging", "timezone", "modelCatalogSourceUrl"); err != nil {
		return err
	}
	var proxyListener, adminListener listenerPayload
	if err := decodeStrictRequiredObject(wire.ProxyListener, &proxyListener, "proxyListener", []string{"host", "port"}); err != nil {
		return err
	}
	if err := decodeStrictRequiredObject(wire.AdminListener, &adminListener, "adminListener", []string{"host", "port"}); err != nil {
		return err
	}
	var requestPolicy exportRequestPolicy
	if err := decodeStrictRequiredObject(wire.RequestPolicy, &requestPolicy, "requestPolicy", []string{"connectTimeoutMs", "firstByteTimeoutMs", "streamIdleTimeoutMs", "totalTimeoutMs", "maxChannelAttempts"}); err != nil {
		return err
	}
	var channelSettings config.ChannelSettings
	if err := decodeStrictRequiredObject(wire.ChannelSettings, &channelSettings, "channelSettings", []string{"reasoningEffort", "serviceTierPassthrough"}); err != nil {
		return err
	}
	var logging exportLogging
	if err := decodeStrictRequiredObject(wire.Logging, &logging, "logging", []string{"contentPolicy", "retentionDays"}); err != nil {
		return err
	}
	var timezone string
	if err := json.Unmarshal(wire.Timezone, &timezone); err != nil {
		return errors.New("settings.timezone 必须是字符串")
	}
	var modelCatalogSourceURL string
	if err := json.Unmarshal(wire.ModelCatalogSourceURL, &modelCatalogSourceURL); err != nil {
		return errors.New("settings.modelCatalogSourceUrl 必须是字符串")
	}
	*settings = exportSettings{ProxyListener: proxyListener, AdminListener: adminListener, RequestPolicy: requestPolicy, ChannelSettings: channelSettings, Logging: logging, Timezone: timezone, ModelCatalogSourceURL: modelCatalogSourceURL}
	return nil
}

type exportKDF struct {
	Algorithm   string `json:"algorithm"`
	Salt        string `json:"salt"`
	MemoryKiB   int    `json:"memoryKiB"`
	Iterations  int    `json:"iterations"`
	Parallelism int    `json:"parallelism"`
}

type exportCipher struct {
	Algorithm string `json:"algorithm"`
	Nonce     string `json:"nonce"`
}

type exportEnvelope struct {
	Format        string       `json:"format"`
	SchemaVersion int          `json:"schemaVersion"`
	ExportMode    string       `json:"exportMode"`
	KDF           exportKDF    `json:"kdf"`
	Cipher        exportCipher `json:"cipher"`
	Ciphertext    string       `json:"ciphertext"`
}

// UnmarshalJSON 严格解析完整导出的加密信封及嵌套参数。
func (envelope *exportEnvelope) UnmarshalJSON(data []byte) error {
	type wireEnvelope struct {
		Format        string          `json:"format"`
		SchemaVersion int             `json:"schemaVersion"`
		ExportMode    string          `json:"exportMode"`
		KDF           json.RawMessage `json:"kdf"`
		Cipher        json.RawMessage `json:"cipher"`
		Ciphertext    string          `json:"ciphertext"`
	}
	var wire wireEnvelope
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "完整导出信封", "format", "schemaVersion", "exportMode", "kdf", "cipher", "ciphertext"); err != nil {
		return err
	}
	var kdf exportKDF
	if err := decodeStrictRequiredObject(wire.KDF, &kdf, "kdf", []string{"algorithm", "salt", "memoryKiB", "iterations", "parallelism"}); err != nil {
		return err
	}
	var cipher exportCipher
	if err := decodeStrictRequiredObject(wire.Cipher, &cipher, "cipher", []string{"algorithm", "nonce"}); err != nil {
		return err
	}
	*envelope = exportEnvelope{Format: wire.Format, SchemaVersion: wire.SchemaVersion, ExportMode: wire.ExportMode, KDF: kdf, Cipher: cipher, Ciphertext: wire.Ciphertext}
	return nil
}

// configExportAPI 导出不含运行态和令牌的配置；完整模式只在口令加密后携带渠道凭证。
func (s *Service) configExportAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if request.Method == http.MethodGet {
		mode := request.URL.Query().Get("mode")
		if mode == "" {
			mode = "safe"
		}
		if mode != "safe" {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "完整导出必须使用 POST 并提供一次性口令"})
			return
		}
		document, err := s.buildExportDocument("safe")
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		if err := validateExportDocument(document, "safe"); err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, document)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "配置导出仅支持 GET 和 POST"})
		return
	}
	var payload struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	mode := strings.TrimSpace(payload.Mode)
	if mode == "" {
		mode = "safe"
	}
	document, err := s.buildExportDocument(mode)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	if err := validateExportDocument(document, mode); err != nil {
		writeStorageError(writer, err)
		return
	}
	if mode == "safe" {
		s.recordAudit("config.export", "safe", request)
		writeJSON(writer, http.StatusOK, document)
		return
	}
	if mode != "complete_encrypted" || strings.TrimSpace(payload.Password) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "完整导出必须提供非空口令"})
		return
	}
	envelope, err := encryptExport(document, payload.Password)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("config.export", "complete_encrypted", request)
	writeJSON(writer, http.StatusOK, envelope)
}

// configImportAPI 导入完整配置，解析、校验、备份和数据库替换均在成功提交前完成。
func (s *Service) configImportAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "配置导入仅支持 POST"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(request.Header.Get("X-OneAI-Import-Confirm")), "true") {
		writeJSON(writer, http.StatusPreconditionRequired, map[string]string{"error": "请先调用配置预览并确认差异，再导入"})
		return
	}
	mediaType, _, parseErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "application/json" {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "请求 Content-Type 必须是 application/json"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 8<<20))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "读取导入文件失败"})
		return
	}
	document, mode, err := parseImportDocument(body, request.Header.Get("X-OneAI-Export-Password"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateExportDocument(document, mode); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	current := s.snapshotSettings()
	backupPath, err := storage.CreateCompleteBackup(s.database, current.DataDirectory)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	channels, stagedRefs, err := s.prepareImportedChannels(document.Channels, mode)
	cleanupStaged := func(cause error) error {
		_, cleanupErr := s.cleanupConfigImportSecretRefs(stagedRefs)
		return errors.Join(cause, cleanupErr)
	}
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": cleanupStaged(err).Error()})
		return
	}
	settings, err := importSettings(document.Settings, current.DataDirectory)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": cleanupStaged(err).Error()})
		return
	}
	var importedAuth *storage.AuthTokens
	if mode == "complete_encrypted" {
		if document.Tokens == nil || strings.TrimSpace(document.Tokens.AdminToken) == "" || strings.TrimSpace(document.Tokens.ProxyToken) == "" {
			importErr := cleanupStaged(errors.New("完整导入必须包含管理令牌和代理令牌"))
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": importErr.Error()})
			return
		}
		tokens, tokenErr := storage.NewAuthTokens(document.Tokens.AdminToken, document.Tokens.ProxyToken)
		if tokenErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": cleanupStaged(tokenErr).Error()})
			return
		}
		importedAuth = &tokens
	}
	oldChannels, err := storage.ListChannels(s.database)
	if err != nil {
		writeStorageError(writer, cleanupStaged(err))
		return
	}
	oldSecrets := make(map[string][]byte)
	for _, channel := range oldChannels {
		if channel.SecretRef == "" {
			continue
		}
		value, getErr := s.secrets.Get(channel.SecretRef)
		if getErr != nil {
			importErr := cleanupStaged(errors.New("替换配置前无法读取旧渠道凭证，请检查系统密钥环"))
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": importErr.Error()})
			return
		}
		oldSecrets[channel.SecretRef] = value
	}
	channelModels := make(map[string][]string)
	for _, model := range document.ChannelModels {
		channelModels[model.ChannelID] = append(channelModels[model.ChannelID], model.Model)
	}
	previousAuth := s.auth
	if importedAuth != nil {
		if secretErr := storage.PersistAuthTokenSecrets(s.secrets, *importedAuth); secretErr != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": cleanupStaged(secretErr).Error()})
			return
		}
	}
	if err := storage.ReplaceConfigurationWithAuthAndCatalog(s.database, channels, channelModels, document.GlobalModelMappings, document.ChannelModelMappings, document.ProbePolicies, settings, document.ModelCatalog, document.Settings.ModelCatalogSourceURL, importedAuth); err != nil {
		importErr := cleanupStaged(err)
		for ref, value := range oldSecrets {
			if restoreErr := s.secrets.Put(ref, value); restoreErr != nil {
				s.logger.Error("恢复旧渠道密钥环值失败", "secretRef", ref, "error", restoreErr)
			}
		}
		if importedAuth != nil && previousAuth.AdminToken != "" && previousAuth.ProxyToken != "" {
			if restoreErr := storage.PersistAuthTokenSecrets(s.secrets, previousAuth); restoreErr != nil {
				s.logger.Error("恢复旧鉴权令牌失败", "error", restoreErr)
			}
		}
		writeStorageError(writer, importErr)
		return
	}
	// 导入成功后一次性发布完整设置快照；实际 Listener 仍使用启动时绑定地址。
	next := current
	next.RequestPolicy = settings.RequestPolicy
	next.ChannelSettings = settings.ChannelSettings
	next.Logging = settings.Logging
	next.Timezone = settings.Timezone
	next.ProxyListen = settings.ProxyListen
	next.AdminListen = settings.AdminListen
	s.publishSettings(next)
	if importedAuth != nil {
		s.authMu.Lock()
		s.auth = *importedAuth
		s.authMu.Unlock()
	}
	cleanupPending, cleanupErr := s.cleanupConfigImportSecretRefs(migrationSecretRefs(oldSecrets))
	s.recordAudit("config.import", mode, request)
	response := map[string]any{"backupPath": backupPath, "mode": mode, "channelCount": len(channels), "cleanupPending": cleanupPending, "restartRequired": true}
	if importedAuth != nil {
		response["adminToken"] = importedAuth.AdminToken
		response["proxyToken"] = importedAuth.ProxyToken
	}
	if cleanupErr != nil {
		s.logger.Error("保存导入待清理凭证引用失败", "error", cleanupErr)
		response["error"] = "配置已导入，但旧凭证待清理记录保存失败"
		writeJSON(writer, http.StatusInternalServerError, response)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

// cleanupConfigImportSecretRefs 清理配置导入产生的孤立凭证，并持久化删除失败的引用。
func (s *Service) cleanupConfigImportSecretRefs(refs []string) ([]string, error) {
	failed := make([]string, 0)
	for _, ref := range uniqueMigrationSecretRefs(refs) {
		if err := s.secrets.Delete(ref); err != nil {
			s.logger.Warn("清理配置导入凭证失败，将保留待清理引用", "secretRef", ref, "error", err)
			failed = append(failed, ref)
		}
	}
	if err := s.retainPendingSecretRefs(failed); err != nil {
		return failed, fmt.Errorf("保存配置导入待清理凭证引用失败: %w", err)
	}
	return failed, nil
}

// configPreviewAPI 只解析并校验导入内容，返回当前配置与新配置的数量差异，不写入数据库。
func (s *Service) configPreviewAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "POST")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "配置预览仅支持 POST"})
		return
	}
	mediaType, _, parseErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "application/json" {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "请求 Content-Type 必须是 application/json"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 8<<20))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "读取导入文件失败"})
		return
	}
	document, mode, err := parseImportDocument(body, request.Header.Get("X-OneAI-Export-Password"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateExportDocument(document, mode); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	channels, err := storage.ListChannels(s.database)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"mode":            mode,
		"current":         map[string]int{"channels": len(channels)},
		"incoming":        map[string]int{"channels": len(document.Channels), "globalModelMappings": len(document.GlobalModelMappings.OpenAI) + len(document.GlobalModelMappings.Anthropic), "modelCatalog": len(document.ModelCatalog), "channelModelMappings": len(document.ChannelModelMappings), "probePolicies": len(document.ProbePolicies)},
		"restartRequired": true,
	})
}

// configDiagnosticsAPI 返回不含秘密、正文和请求内容的运行诊断摘要。
func (s *Service) configDiagnosticsAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "诊断接口仅支持 GET"})
		return
	}
	var channels, requests, attempts int
	_ = s.database.QueryRow("SELECT COUNT(*) FROM channels").Scan(&channels)
	_ = s.database.QueryRow("SELECT COUNT(*) FROM requests").Scan(&requests)
	_ = s.database.QueryRow("SELECT COUNT(*) FROM attempts").Scan(&attempts)
	settings := s.snapshotSettings()
	writeJSON(writer, http.StatusOK, map[string]any{
		"appVersion": "0.1.0", "schemaVersion": storage.CurrentSchemaVersion(), "proxyListener": s.boundProxyAddress(), "adminListener": s.boundAdminAddress(),
		"dataDirectory": settings.DataDirectory, "timezone": settings.Timezone,
		"counts": map[string]int{"channels": channels, "requests": requests, "attempts": attempts},
	})
}

// configBackupAPI 创建带正文清单的完整本地备份，不返回任何秘密值。
func (s *Service) configBackupAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "备份接口仅支持 POST"})
		return
	}
	path, err := storage.CreateCompleteBackup(s.database, s.snapshotSettings().DataDirectory)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("config.backup", "", request)
	writeJSON(writer, http.StatusOK, map[string]string{"manifestPath": path})
}

// configBackupVerifyAPI 校验指定完整备份是否可用于恢复，不直接替换运行中的数据库。
func (s *Service) configBackupVerifyAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "备份校验仅支持 POST"})
		return
	}
	s.dataMu.RLock()
	defer s.dataMu.RUnlock()
	var payload struct {
		ManifestPath string `json:"manifestPath"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	manifestPath := strings.TrimSpace(payload.ManifestPath)
	if manifestPath == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "manifestPath 不能为空"})
		return
	}
	manifest, err := storage.VerifyBackupManifest(manifestPath, s.snapshotSettings().DataDirectory)
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("config.backup.verify", "", request)
	writeJSON(writer, http.StatusOK, map[string]any{"valid": true, "createdAt": manifest.CreatedAt, "database": manifest.Database, "contentBlobCount": len(manifest.ContentBlobs)})
}

// configBackupRestoreAPI 校验并切换完整备份；失败时保留当前配置和正文文件。
func (s *Service) configBackupRestoreAPI(writer http.ResponseWriter, request *http.Request) {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "备份恢复仅支持 POST"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(request.Header.Get("X-OneAI-Restore-Confirm")), "true") {
		writeJSON(writer, http.StatusPreconditionRequired, map[string]string{"error": "恢复操作需要显式确认"})
		return
	}
	var payload struct {
		ManifestPath string `json:"manifestPath"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	manifestPath := strings.TrimSpace(payload.ManifestPath)
	if manifestPath == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "manifestPath 不能为空"})
		return
	}
	if err := storage.RestoreCompleteBackup(s.database, manifestPath, s.snapshotSettings().DataDirectory); err != nil {
		writeStorageError(writer, err)
		return
	}
	s.recordAudit("config.backup.restore", "", request)
	writeJSON(writer, http.StatusOK, map[string]any{"restored": true, "restartRequired": true})
}

// configDiagnosticsExportAPI 以 ZIP 附件形式导出不含秘密和正文的诊断包。
func (s *Service) configDiagnosticsExportAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "诊断导出仅支持 GET"})
		return
	}
	var summary bytes.Buffer
	var summaryWriter = &summary
	var channels, requests, attempts int
	_ = s.database.QueryRow("SELECT COUNT(*) FROM channels").Scan(&channels)
	_ = s.database.QueryRow("SELECT COUNT(*) FROM requests").Scan(&requests)
	_ = s.database.QueryRow("SELECT COUNT(*) FROM attempts").Scan(&attempts)
	settings := s.snapshotSettings()
	_ = json.NewEncoder(summaryWriter).Encode(map[string]any{"appVersion": "0.1.0", "schemaVersion": storage.CurrentSchemaVersion(), "proxyListener": s.boundProxyAddress(), "adminListener": s.boundAdminAddress(), "dataDirectory": settings.DataDirectory, "timezone": settings.Timezone, "counts": map[string]int{"channels": channels, "requests": requests, "attempts": attempts}})
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", `attachment; filename="oneai-proxy-diagnostics.zip"`)
	archive := zip.NewWriter(writer)
	entry, err := archive.Create("diagnostics.json")
	if err != nil {
		return
	}
	_, _ = entry.Write(summary.Bytes())
	_ = archive.Close()
}

func (s *Service) buildExportDocument(mode string) (exportDocument, error) {
	if mode != "safe" && mode != "complete_encrypted" {
		return exportDocument{}, fmt.Errorf("导出模式无效: %s", mode)
	}
	channels, err := storage.ListChannels(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	global, err := storage.LoadGlobalModelMappingSet(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	channelMappings, err := storage.ListChannelModelMappings(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	channelModels, err := storage.ListAllChannelModels(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	policies, err := storage.ListProbePolicies(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	modelCatalog, err := storage.ListAllModelCatalog(s.database)
	if err != nil {
		return exportDocument{}, err
	}
	exportSettingsValue, err := s.exportSettings()
	if err != nil {
		return exportDocument{}, err
	}
	document := exportDocument{Format: exportFormat, SchemaVersion: exportSchemaVersion, ExportMode: mode, ExportedAt: time.Now().UTC().Format(time.RFC3339), AppVersion: exportAppVersion, Channels: make([]exportChannel, 0, len(channels)), ChannelModels: channelModels, GlobalModelMappings: global, ModelCatalog: modelCatalog, ChannelModelMappings: channelMappings, ProbePolicies: policies, Settings: exportSettingsValue}
	if mode == "complete_encrypted" {
		tokens, err := storage.LoadExportAuthTokens(s.database, s.secrets)
		if err != nil {
			return exportDocument{}, err
		}
		document.Tokens = &exportAuthTokens{AdminToken: tokens.AdminToken, ProxyToken: tokens.ProxyToken}
	}
	for _, channel := range channels {
		parsedBaseURL, err := url.Parse(channel.BaseURL)
		if err != nil || parsedBaseURL.User != nil {
			return exportDocument{}, fmt.Errorf("渠道 Base URL 含有不允许的用户信息: %s", channel.ID)
		}
		exported := exportChannel{ID: channel.ID, Name: channel.Name, Note: channel.Note, Group: channel.Group, Protocol: channel.Protocol, BaseURL: channel.BaseURL, Capabilities: channel.Capabilities, AdminState: channel.AdminState, FailureAction: channel.FailureAction, FailureThreshold: channel.FailureThreshold, CustomHeaders: channel.CustomHeaders, ConcurrencyLimit: channel.ConcurrencyLimit, RequestTimeoutMs: channel.RequestTimeoutMs, StreamIdleTimeoutMs: channel.StreamIdleTimeoutMs, CooldownSeconds: channel.CooldownSeconds, Priority: channel.Priority, FallbackModel: channel.FallbackModel, ReasoningEffort: channel.ReasoningEffort, ServiceTierPassthrough: channel.ServiceTierPassthrough, CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt}
		if mode == "complete_encrypted" && channel.SecretRef != "" {
			value, err := s.secrets.Get(channel.SecretRef)
			if err != nil {
				return exportDocument{}, fmt.Errorf("读取渠道凭证失败: %s: %w", channel.ID, err)
			}
			var credential storedCredential
			if err := decodeStrictBytes(value, &credential); err != nil {
				return exportDocument{}, fmt.Errorf("解析渠道凭证失败: %s", channel.ID)
			}
			exported.Credential = &exportCredential{Type: credential.Type, Secret: credential.Secret, HeaderName: credential.HeaderName, Prefix: credential.Prefix}
		}
		document.Channels = append(document.Channels, exported)
	}
	return document, nil
}

func (s *Service) exportSettings() (exportSettings, error) {
	persisted, err := storage.LoadRuntimeSettings(s.database, s.snapshotSettings())
	if err != nil {
		return exportSettings{}, err
	}
	proxyHost, proxyPort, _ := config.ListenerParts(persisted.ProxyListen)
	adminHost, adminPort, _ := config.ListenerParts(persisted.AdminListen)
	sourceURL, err := storage.LoadModelCatalogSourceURL(s.database)
	if err != nil {
		return exportSettings{}, err
	}
	return exportSettings{
		ProxyListener: listenerPayload{Host: proxyHost, Port: proxyPort},
		AdminListener: listenerPayload{Host: adminHost, Port: adminPort},
		RequestPolicy: exportRequestPolicy{
			ConnectTimeoutMs:    persisted.RequestPolicy.ConnectTimeoutMs,
			FirstByteTimeoutMs:  persisted.RequestPolicy.FirstByteTimeoutMs,
			StreamIdleTimeoutMs: persisted.RequestPolicy.StreamIdleTimeoutMs,
			TotalTimeoutMs:      persisted.RequestPolicy.TotalTimeoutMs,
			MaxChannelAttempts:  persisted.RequestPolicy.MaxChannelAttempts,
		},
		ChannelSettings:       persisted.ChannelSettings,
		Logging:               exportLogging{ContentPolicy: persisted.Logging.ContentPolicy, RetentionDays: persisted.Logging.RetentionDays, RequestRetentionDays: persisted.Logging.RequestRetentionDays, AuditRetentionDays: persisted.Logging.AuditRetentionDays, RuntimeRetentionDays: persisted.Logging.RuntimeRetentionDays, MaxContentBytes: persisted.Logging.MaxContentBytes, MaxRequestContentBytes: persisted.Logging.MaxRequestContentBytes, MaxResponseContentBytes: persisted.Logging.MaxResponseContentBytes, DiskQuotaBytes: persisted.Logging.DiskQuotaBytes, RuntimeLogMaxBytes: persisted.Logging.RuntimeLogMaxBytes},
		Timezone:              persisted.Timezone,
		ModelCatalogSourceURL: sourceURL,
	}, nil
}

func (s *Service) prepareImportedChannels(channels []exportChannel, mode string) ([]storage.Channel, []string, error) {
	return s.prepareImportedChannelsWithHook(channels, mode, nil)
}

// prepareImportedChannelsWithHook 导入渠道并在写入凭证前通知调用方持久化引用。
func (s *Service) prepareImportedChannelsWithHook(channels []exportChannel, mode string, beforePut func(string) error) ([]storage.Channel, []string, error) {
	result := make([]storage.Channel, 0, len(channels))
	refs := make([]string, 0)
	for _, channel := range channels {
		secretRef := ""
		if channel.Credential != nil {
			if channel.Credential.headerNameNull || channel.Credential.prefixNull {
				return nil, refs, fmt.Errorf("渠道凭证字段必须是字符串: %s", channel.ID)
			}
			if mode != "complete_encrypted" {
				return nil, refs, errors.New("safe 导入不得包含渠道凭证")
			}
			var headerName, prefix *string
			if channel.Credential.headerNamePresent {
				headerName = stringPointer(channel.Credential.HeaderName)
			}
			if channel.Credential.prefixPresent {
				prefix = stringPointer(channel.Credential.Prefix)
			}
			credential, err := normalizeCredential(credentialPayload{Type: channel.Credential.Type, Secret: channel.Credential.Secret, HeaderName: headerName, Prefix: prefix})
			if err != nil {
				return nil, refs, err
			}
			secretRef, err = s.putStoredCredentialWithHook(credential, beforePut)
			if err != nil {
				return nil, refs, err
			}
			refs = append(refs, secretRef)
		}
		adminState := channel.AdminState
		if secretRef == "" {
			adminState = "disabled"
		}
		result = append(result, storage.Channel{ID: channel.ID, Name: channel.Name, Note: channel.Note, Group: channel.Group, Protocol: channel.Protocol, BaseURL: channel.BaseURL, Capabilities: channel.Capabilities, AdminState: adminState, CreatedAt: channel.CreatedAt, UpdatedAt: channel.UpdatedAt, SecretRef: secretRef, FailureAction: channel.FailureAction, FailureThreshold: channel.FailureThreshold, CustomHeaders: channel.CustomHeaders, ConcurrencyLimit: channel.ConcurrencyLimit, RequestTimeoutMs: channel.RequestTimeoutMs, StreamIdleTimeoutMs: channel.StreamIdleTimeoutMs, CooldownSeconds: channel.CooldownSeconds, Priority: channel.Priority, FallbackModel: channel.FallbackModel, ReasoningEffort: channel.ReasoningEffort, ServiceTierPassthrough: channel.ServiceTierPassthrough})
	}
	return result, refs, nil
}

func (s *Service) putStoredCredential(credential storedCredential) (string, error) {
	return s.putStoredCredentialWithHook(credential, nil)
}

// putStoredCredentialWithHook 生成凭证引用，并在写入密钥环前执行持久化钩子。
func (s *Service) putStoredCredentialWithHook(credential storedCredential, beforePut func(string) error) (string, error) {
	value, err := json.Marshal(credential)
	if err != nil {
		return "", fmt.Errorf("序列化渠道凭证失败: %w", err)
	}
	ref, err := secret.NewRef()
	if err != nil {
		return "", err
	}
	if beforePut != nil {
		if err := beforePut(ref); err != nil {
			return "", err
		}
	}
	if err := s.secrets.Put(ref, value); err != nil {
		return "", err
	}
	return ref, nil
}

func importSettings(settings exportSettings, dataDirectory string) (config.Settings, error) {
	proxy := config.ListenerAddress(settings.ProxyListener.Host, settings.ProxyListener.Port)
	admin := config.ListenerAddress(settings.AdminListener.Host, settings.AdminListener.Port)
	validated, err := config.New(proxy, admin, dataDirectory)
	if err != nil {
		return config.Settings{}, err
	}
	if settings.RequestPolicy.ConnectTimeoutMs <= 0 || settings.RequestPolicy.FirstByteTimeoutMs <= 0 || settings.RequestPolicy.StreamIdleTimeoutMs <= 0 || settings.RequestPolicy.TotalTimeoutMs <= 0 || settings.RequestPolicy.MaxChannelAttempts < 0 {
		return config.Settings{}, errors.New("请求策略参数无效")
	}
	if settings.Logging.ContentPolicy != "" && settings.Logging.ContentPolicy != config.RequestLogContentPolicy {
		return config.Settings{}, fmt.Errorf("V1 请求日志固定保存四类快照")
	}
	if settings.Logging.RetentionDays <= 0 && settings.Logging.RequestRetentionDays <= 0 || strings.TrimSpace(settings.Timezone) == "" {
		return config.Settings{}, errors.New("日志保留期和时区不能为空")
	}
	if _, err := time.LoadLocation(strings.TrimSpace(settings.Timezone)); err != nil {
		return config.Settings{}, fmt.Errorf("时区必须是有效的 IANA 时区: %s", settings.Timezone)
	}
	validated.RequestPolicy = config.RequestPolicy{
		ConnectTimeoutMs:    settings.RequestPolicy.ConnectTimeoutMs,
		FirstByteTimeoutMs:  settings.RequestPolicy.FirstByteTimeoutMs,
		StreamIdleTimeoutMs: settings.RequestPolicy.StreamIdleTimeoutMs,
		TotalTimeoutMs:      settings.RequestPolicy.TotalTimeoutMs,
		MaxChannelAttempts:  settings.RequestPolicy.MaxChannelAttempts,
	}
	validated.ChannelSettings = config.NormalizeChannelSettings(settings.ChannelSettings)
	if !config.IsValidReasoningEffort(validated.ChannelSettings.ReasoningEffort) {
		return config.Settings{}, errors.New("全局思考等级无效")
	}
	requestRetentionDays := settings.Logging.RequestRetentionDays
	if requestRetentionDays <= 0 {
		requestRetentionDays = settings.Logging.RetentionDays
	}
	if requestRetentionDays <= 0 {
		requestRetentionDays = 30
	}
	auditRetentionDays := settings.Logging.AuditRetentionDays
	if auditRetentionDays <= 0 {
		auditRetentionDays = 30
	}
	runtimeRetentionDays := settings.Logging.RuntimeRetentionDays
	if runtimeRetentionDays <= 0 {
		runtimeRetentionDays = 7
	}
	maxContentBytes := settings.Logging.MaxContentBytes
	if maxContentBytes <= 0 {
		maxContentBytes = settings.Logging.MaxRequestContentBytes
	}
	if maxContentBytes <= 0 {
		maxContentBytes = 1 << 20
	}
	maxRequestContentBytes := settings.Logging.MaxRequestContentBytes
	if maxRequestContentBytes <= 0 {
		maxRequestContentBytes = maxContentBytes
	}
	maxResponseContentBytes := settings.Logging.MaxResponseContentBytes
	if maxResponseContentBytes <= 0 {
		maxResponseContentBytes = 1 << 20
	}
	diskQuotaBytes := settings.Logging.DiskQuotaBytes
	if diskQuotaBytes <= 0 {
		diskQuotaBytes = 100 << 20
	}
	runtimeLogMaxBytes := settings.Logging.RuntimeLogMaxBytes
	if runtimeLogMaxBytes <= 0 {
		runtimeLogMaxBytes = 100 << 20
	}
	validated.Logging = config.LoggingSettings{ContentPolicy: config.RequestLogContentPolicy, RetentionDays: requestRetentionDays, RequestRetentionDays: requestRetentionDays, AuditRetentionDays: auditRetentionDays, RuntimeRetentionDays: runtimeRetentionDays, MaxContentBytes: maxContentBytes, MaxRequestContentBytes: maxRequestContentBytes, MaxResponseContentBytes: maxResponseContentBytes, DiskQuotaBytes: diskQuotaBytes, RuntimeLogMaxBytes: runtimeLogMaxBytes}
	validated.Timezone = settings.Timezone
	return validated, nil
}

func validateExportDocument(document exportDocument, mode string) error {
	if document.Format != exportFormat || !supportedExportSchemaVersion(document.SchemaVersion) || document.ExportMode != mode {
		return errors.New("导出文件格式或版本不匹配")
	}
	if mode != "safe" && mode != "complete_encrypted" {
		return fmt.Errorf("导入模式无效: %s", mode)
	}
	if mode == "complete_encrypted" {
		if document.Tokens == nil || strings.TrimSpace(document.Tokens.AdminToken) == "" || strings.TrimSpace(document.Tokens.ProxyToken) == "" {
			return errors.New("完整导出必须包含管理令牌和代理令牌")
		}
	}
	if mode == "safe" && document.Tokens != nil {
		return errors.New("safe 导入不得包含鉴权令牌")
	}
	if strings.TrimSpace(document.ExportedAt) == "" || strings.TrimSpace(document.AppVersion) == "" {
		return errors.New("导出文件元数据不完整")
	}
	if strings.TrimSpace(document.Settings.ModelCatalogSourceURL) != "" {
		if _, err := validateModelSourceURL(document.Settings.ModelCatalogSourceURL); err != nil {
			return fmt.Errorf("模型目录来源地址无效: %w", err)
		}
	}
	channelSettings := config.NormalizeChannelSettings(document.Settings.ChannelSettings)
	if !config.IsValidReasoningEffort(channelSettings.ReasoningEffort) {
		return errors.New("全局思考等级无效")
	}
	channelProtocols := make(map[string]storage.Protocol, len(document.Channels))
	for _, channel := range document.Channels {
		if strings.TrimSpace(channel.ID) == "" || strings.TrimSpace(channel.Name) == "" || channel.AdminState == "" {
			return errors.New("渠道字段不能为空")
		}
		if _, exists := channelProtocols[channel.ID]; exists {
			return fmt.Errorf("渠道 ID 重复: %s", channel.ID)
		}
		if !isValidProtocol(channel.Protocol) {
			return fmt.Errorf("渠道协议无效: %s", channel.Protocol)
		}
		if !isHTTPURL(channel.BaseURL) {
			return errors.New("渠道 Base URL 必须是绝对且不含用户信息的 HTTP(S) 地址")
		}
		if channel.AdminState != "enabled" && channel.AdminState != "disabled" {
			return fmt.Errorf("人工状态无效: %s", channel.AdminState)
		}
		if channel.FailureAction != "cooldown" && channel.FailureAction != "auto_disable" {
			return fmt.Errorf("失败动作无效: %s", channel.FailureAction)
		}
		if channel.FailureThreshold <= 0 {
			return fmt.Errorf("连续失败阈值必须大于 0: %s", channel.ID)
		}
		if !config.IsValidReasoningEffort(channel.ReasoningEffort) {
			return fmt.Errorf("渠道思考等级无效: %s", channel.ID)
		}
		if channel.ConcurrencyLimit < 0 || channel.RequestTimeoutMs < 0 || channel.StreamIdleTimeoutMs < 0 || channel.CooldownSeconds < 0 || channel.Priority < 0 {
			return fmt.Errorf("渠道运行参数不能为负数: %s", channel.ID)
		}
		if channel.credentialNull || channel.credentialPresent && channel.Credential == nil {
			return fmt.Errorf("渠道 credential 必须是对象: %s", channel.ID)
		}
		if mode == "safe" && channel.Credential != nil {
			return errors.New("safe 导入不得包含渠道凭证")
		}
		if channel.Credential != nil {
			if strings.TrimSpace(channel.Credential.Secret) == "" {
				return fmt.Errorf("渠道凭证不能为空: %s", channel.ID)
			}
			if channel.Credential.Type != "bearer" && channel.Credential.Type != "api_key" && channel.Credential.Type != "custom" {
				return fmt.Errorf("凭证类型无效: %s", channel.Credential.Type)
			}
			if channel.Credential.Type == "custom" && strings.TrimSpace(channel.Credential.HeaderName) == "" {
				return fmt.Errorf("custom 凭证必须提供 Header 名称: %s", channel.ID)
			}
			if channel.Credential.HeaderName != "" && !isValidHeaderName(channel.Credential.HeaderName) {
				return fmt.Errorf("凭证 Header 名称无效: %s", channel.ID)
			}
		}
		channelProtocols[channel.ID] = channel.Protocol
	}
	channelModelKeys := make(map[string]struct{}, len(document.ChannelModels))
	for _, model := range document.ChannelModels {
		if _, exists := channelProtocols[model.ChannelID]; !exists {
			return fmt.Errorf("渠道模型目录的渠道不存在: %s", model.ChannelID)
		}
		model.Model = strings.TrimSpace(model.Model)
		if model.Model == "" || strings.ContainsAny(model.Model, "\r\n") {
			return fmt.Errorf("渠道模型名称无效: %s", model.ChannelID)
		}
		key := model.ChannelID + "\x00" + model.Model
		if _, exists := channelModelKeys[key]; exists {
			return fmt.Errorf("渠道模型重复: %s/%s", model.ChannelID, model.Model)
		}
		channelModelKeys[key] = struct{}{}
	}
	if err := validateGlobalMappingSet(document.GlobalModelMappings); err != nil {
		return err
	}
	for _, entry := range document.ModelCatalog {
		if err := storage.ValidateModelCatalogEntry(entry); err != nil {
			return fmt.Errorf("模型目录无效: %w", err)
		}
	}
	channelMappingKeys := make(map[string]struct{}, len(document.ChannelModelMappings))
	for _, mapping := range document.ChannelModelMappings {
		protocol, exists := channelProtocols[mapping.ChannelID]
		if !exists || protocol != mapping.Protocol || strings.TrimSpace(mapping.LogicalModel) == "" || strings.TrimSpace(mapping.UpstreamModel) == "" {
			return fmt.Errorf("渠道模型映射字段无效: %s", mapping.ChannelID)
		}
		key := mapping.ChannelID + "\x00" + string(mapping.Protocol) + "\x00" + mapping.LogicalModel
		if _, exists := channelMappingKeys[key]; exists {
			return fmt.Errorf("渠道模型映射重复: %s", mapping.ChannelID)
		}
		channelMappingKeys[key] = struct{}{}
	}
	policyKeys := make(map[string]struct{}, len(document.ProbePolicies))
	for _, policy := range document.ProbePolicies {
		if _, exists := channelProtocols[policy.ChannelID]; !exists {
			return fmt.Errorf("探针策略渠道不存在: %s", policy.ChannelID)
		}
		if _, exists := policyKeys[policy.ChannelID]; exists {
			return fmt.Errorf("探针策略重复: %s", policy.ChannelID)
		}
		if policy.Mode != "connectivity" && policy.Mode != "minimal_inference" || policy.RecoverySuccessThreshold <= 0 || policy.IntervalSeconds != 0 && policy.IntervalSeconds < 10 || policy.FailureThreshold != 0 && policy.FailureThreshold < 1 || policy.RequestTimeoutMs != 0 && policy.RequestTimeoutMs < 1000 || policy.AutoRecover && !policy.Enabled {
			return fmt.Errorf("探针策略无效: %s", policy.ChannelID)
		}
		policyKeys[policy.ChannelID] = struct{}{}
	}
	_, err := importSettings(document.Settings, "./data")
	return err
}

func validateGlobalMappingSetJSON(value json.RawMessage) error {
	var mapping storage.GlobalModelMappingSet
	if _, err := decodeStrictObject(value, &mapping); err != nil {
		return errors.New("globalModelMappings 必须是包含 openai 和 anthropic 的对象")
	}
	if mapping.OpenAI == nil || mapping.Anthropic == nil {
		return errors.New("globalModelMappings 必须同时包含 openai 和 anthropic 对象")
	}
	return validateGlobalMappingSet(mapping)
}

func validateGlobalMappingSet(mapping storage.GlobalModelMappingSet) error {
	for family, values := range map[string]map[string]string{"openai": mapping.OpenAI, "anthropic": mapping.Anthropic} {
		for clientModel, logicalModel := range values {
			if strings.TrimSpace(clientModel) == "" || strings.TrimSpace(logicalModel) == "" || clientModel == logicalModel || strings.ContainsAny(clientModel+logicalModel, "\r\n") {
				return fmt.Errorf("全局模型映射无效: %s.%s", family, clientModel)
			}
		}
	}
	return nil
}

func parseImportDocument(body []byte, password string) (exportDocument, string, error) {
	var header struct {
		ExportMode string `json:"exportMode"`
	}
	if err := json.Unmarshal(body, &header); err != nil {
		return exportDocument{}, "", errors.New("导入文件 JSON 无效或包含未知字段")
	}
	switch header.ExportMode {
	case "safe":
		var document exportDocument
		if err := decodeStrictBytes(body, &document); err != nil {
			return exportDocument{}, "", errors.New("安全导出文件结构无效")
		}
		return document, "safe", nil
	case "complete_encrypted":
		var envelope exportEnvelope
		if err := decodeStrictBytes(body, &envelope); err != nil {
			return exportDocument{}, "", errors.New("完整导出信封结构无效")
		}
		if strings.TrimSpace(password) == "" {
			return exportDocument{}, "", errors.New("完整导入必须提供 X-OneAI-Export-Password")
		}
		plaintext, err := decryptExport(envelope, password)
		if err != nil {
			return exportDocument{}, "", errors.New("完整导入口令错误或文件已损坏")
		}
		var document exportDocument
		if err := decodeStrictBytes(plaintext, &document); err != nil {
			return exportDocument{}, "", errors.New("解密后的导出载荷结构无效")
		}
		return document, "complete_encrypted", nil
	default:
		return exportDocument{}, "", errors.New("导出文件模式无效")
	}
}

func encryptExport(document exportDocument, password string) (exportEnvelope, error) {
	plaintext, err := json.Marshal(document)
	if err != nil {
		return exportEnvelope{}, fmt.Errorf("序列化导出载荷失败: %w", err)
	}
	salt := make([]byte, 16)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(salt); err != nil {
		return exportEnvelope{}, fmt.Errorf("生成导出盐失败: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return exportEnvelope{}, fmt.Errorf("生成导出随机数失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, kdfIterations, kdfMemoryKiB, kdfParallelism, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return exportEnvelope{}, fmt.Errorf("初始化导出加密失败: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(exportFormat))
	return exportEnvelope{Format: exportFormat, SchemaVersion: exportSchemaVersion, ExportMode: "complete_encrypted", KDF: exportKDF{Algorithm: "argon2id", Salt: base64.StdEncoding.EncodeToString(salt), MemoryKiB: kdfMemoryKiB, Iterations: kdfIterations, Parallelism: kdfParallelism}, Cipher: exportCipher{Algorithm: "xchacha20-poly1305", Nonce: base64.StdEncoding.EncodeToString(nonce)}, Ciphertext: base64.StdEncoding.EncodeToString(ciphertext)}, nil
}

func decryptExport(envelope exportEnvelope, password string) ([]byte, error) {
	if envelope.Format != exportFormat || !supportedExportSchemaVersion(envelope.SchemaVersion) || envelope.ExportMode != "complete_encrypted" || envelope.KDF.Algorithm != "argon2id" || envelope.KDF.MemoryKiB != kdfMemoryKiB || envelope.KDF.Iterations != kdfIterations || envelope.KDF.Parallelism != kdfParallelism || envelope.Cipher.Algorithm != "xchacha20-poly1305" {
		return nil, errors.New("导出信封参数无效")
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.KDF.Salt)
	if err != nil || len(salt) < 16 {
		return nil, errors.New("导出盐无效")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Cipher.Nonce)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("导出随机数无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("导出密文无效")
	}
	key := argon2.IDKey([]byte(password), salt, kdfIterations, kdfMemoryKiB, kdfParallelism, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, []byte(exportFormat))
}

func decodeStrictBytes(value []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON 包含多个值")
	}
	return nil
}

func decodeStrictObject(value []byte, target any) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("JSON 必须是对象")
	}
	if err := decodeStrictBytes(trimmed, target); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func requireFields(fields map[string]json.RawMessage, object string, names ...string) error {
	for _, name := range names {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s 缺少必填字段: %s", object, name)
		}
	}
	return nil
}

func requireJSONArray(value json.RawMessage, name string) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return fmt.Errorf("%s 必须是数组且不能为 null", name)
	}
	return nil
}

func validateArrayObjects(value json.RawMessage, name string, required []string) error {
	return validateArrayObjectsOptional(value, name, required, nil)
}

func validateArrayObjectsOptional(value json.RawMessage, name string, required, optional []string) error {
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return fmt.Errorf("%s 必须是数组", name)
	}
	for index, item := range items {
		trimmed := bytes.TrimSpace(item)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return fmt.Errorf("%s[%d] 必须是对象", name, index)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return fmt.Errorf("%s[%d] 结构无效", name, index)
		}
		allowed := make(map[string]struct{}, len(required))
		for _, field := range required {
			allowed[field] = struct{}{}
		}
		for _, field := range optional {
			allowed[field] = struct{}{}
		}
		for field := range fields {
			if _, exists := allowed[field]; !exists {
				return fmt.Errorf("%s[%d] 包含未知字段: %s", name, index, field)
			}
		}
		if err := requireFields(fields, fmt.Sprintf("%s[%d]", name, index), required...); err != nil {
			return err
		}
	}
	return nil
}

func decodeStrictRequiredObject(value json.RawMessage, target any, name string, required []string) error {
	fields, err := decodeStrictObject(value, target)
	if err != nil {
		return fmt.Errorf("%s 结构无效", name)
	}
	return requireFields(fields, name, required...)
}

func stringPointer(value string) *string { return &value }

func isValidProtocol(protocol storage.Protocol) bool {
	return protocol == storage.ProtocolOpenAIChat || protocol == storage.ProtocolOpenAIResponses || protocol == storage.ProtocolAnthropicMessages
}

func isHTTPURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
