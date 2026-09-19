package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/storage"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	migrationFormat             = "oneai-proxy-migration"
	migrationPayloadFormat      = "oneai-proxy-migration-payload"
	migrationVersion            = 1
	migrationPayloadEncoding    = "gzip+json"
	migrationMaxEnvelopeBytes   = 8 << 20
	migrationMaxPayloadBytes    = 32 << 20
	migrationMaxExpansionRatio  = 100
	migrationSaltBytes          = 16
	migrationCiphertextOverhead = chacha20poly1305.Overhead
	migrationMaxDecodeDuration  = 10 * time.Second
)

type migrationEnvelope struct {
	Format                string       `json:"format"`
	MigrationVersion      int          `json:"migrationVersion"`
	ExportedAt            string       `json:"exportedAt"`
	AppVersion            string       `json:"appVersion"`
	ConfigSchemaVersion   int          `json:"configSchemaVersion"`
	DatabaseSchemaVersion int          `json:"databaseSchemaVersion"`
	PayloadEncoding       string       `json:"payloadEncoding"`
	KDF                   exportKDF    `json:"kdf"`
	Cipher                exportCipher `json:"cipher"`
	Ciphertext            string       `json:"ciphertext"`
}

type migrationEnvelopeAAD struct {
	Format                string       `json:"format"`
	MigrationVersion      int          `json:"migrationVersion"`
	ExportedAt            string       `json:"exportedAt"`
	AppVersion            string       `json:"appVersion"`
	ConfigSchemaVersion   int          `json:"configSchemaVersion"`
	DatabaseSchemaVersion int          `json:"databaseSchemaVersion"`
	PayloadEncoding       string       `json:"payloadEncoding"`
	KDF                   exportKDF    `json:"kdf"`
	Cipher                exportCipher `json:"cipher"`
}

type migrationPayload struct {
	Format                string                        `json:"format"`
	MigrationVersion      int                           `json:"migrationVersion"`
	ConfigSchemaVersion   int                           `json:"configSchemaVersion"`
	DatabaseSchemaVersion int                           `json:"databaseSchemaVersion"`
	Settings              exportSettings                `json:"settings"`
	Channels              []exportChannel               `json:"channels"`
	ChannelModels         []storage.ChannelModel        `json:"channelModels"`
	GlobalModelMappings   storage.GlobalModelMappingSet `json:"globalModelMappings"`
	ChannelModelMappings  []storage.ChannelModelMapping `json:"channelModelMappings"`
	ProbePolicies         []storage.ProbePolicy         `json:"probePolicies"`
	ModelCatalog          []storage.ModelCatalogEntry   `json:"modelCatalog"`
	Tokens                exportAuthTokens              `json:"tokens"`
}

// UnmarshalJSON 严格解析迁移包外层信封及嵌套加密参数。
func (envelope *migrationEnvelope) UnmarshalJSON(data []byte) error {
	type wireEnvelope struct {
		Format                string          `json:"format"`
		MigrationVersion      int             `json:"migrationVersion"`
		ExportedAt            string          `json:"exportedAt"`
		AppVersion            string          `json:"appVersion"`
		ConfigSchemaVersion   int             `json:"configSchemaVersion"`
		DatabaseSchemaVersion int             `json:"databaseSchemaVersion"`
		PayloadEncoding       string          `json:"payloadEncoding"`
		KDF                   json.RawMessage `json:"kdf"`
		Cipher                json.RawMessage `json:"cipher"`
		Ciphertext            string          `json:"ciphertext"`
	}
	var wire wireEnvelope
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "迁移包信封", "format", "migrationVersion", "exportedAt", "appVersion", "configSchemaVersion", "databaseSchemaVersion", "payloadEncoding", "kdf", "cipher", "ciphertext"); err != nil {
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
	*envelope = migrationEnvelope{
		Format: wire.Format, MigrationVersion: wire.MigrationVersion, ExportedAt: wire.ExportedAt,
		AppVersion: wire.AppVersion, ConfigSchemaVersion: wire.ConfigSchemaVersion,
		DatabaseSchemaVersion: wire.DatabaseSchemaVersion, PayloadEncoding: wire.PayloadEncoding,
		KDF: kdf, Cipher: cipher, Ciphertext: wire.Ciphertext,
	}
	return nil
}

// UnmarshalJSON 严格解析迁移载荷白名单，拒绝运行日志、请求正文和其他未知字段。
func (payload *migrationPayload) UnmarshalJSON(data []byte) error {
	type wirePayload struct {
		Format                string                        `json:"format"`
		MigrationVersion      int                           `json:"migrationVersion"`
		ConfigSchemaVersion   int                           `json:"configSchemaVersion"`
		DatabaseSchemaVersion int                           `json:"databaseSchemaVersion"`
		Settings              exportSettings                `json:"settings"`
		Channels              []exportChannel               `json:"channels"`
		ChannelModels         []storage.ChannelModel        `json:"channelModels"`
		RouteGroups           json.RawMessage               `json:"routeGroups"`
		GlobalModelMappings   storage.GlobalModelMappingSet `json:"globalModelMappings"`
		ChannelModelMappings  []storage.ChannelModelMapping `json:"channelModelMappings"`
		ProbePolicies         []storage.ProbePolicy         `json:"probePolicies"`
		ModelCatalog          []storage.ModelCatalogEntry   `json:"modelCatalog"`
		Tokens                json.RawMessage               `json:"tokens"`
	}
	var wire wirePayload
	fields, err := decodeStrictObject(data, &wire)
	if err != nil {
		return err
	}
	if err := requireFields(fields, "迁移载荷", "format", "migrationVersion", "configSchemaVersion", "databaseSchemaVersion", "settings", "channels", "channelModels", "globalModelMappings", "channelModelMappings", "probePolicies", "modelCatalog", "tokens"); err != nil {
		return err
	}
	for _, field := range []string{"channels", "channelModels", "channelModelMappings", "probePolicies", "modelCatalog"} {
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
	var tokens exportAuthTokens
	if err := decodeStrictRequiredObject(wire.Tokens, &tokens, "tokens", []string{"adminToken", "proxyToken"}); err != nil {
		return err
	}
	*payload = migrationPayload{
		Format: wire.Format, MigrationVersion: wire.MigrationVersion,
		ConfigSchemaVersion: wire.ConfigSchemaVersion, DatabaseSchemaVersion: wire.DatabaseSchemaVersion,
		Settings: wire.Settings, Channels: wire.Channels, ChannelModels: wire.ChannelModels,
		GlobalModelMappings: wire.GlobalModelMappings,
		ChannelModelMappings: wire.ChannelModelMappings, ProbePolicies: wire.ProbePolicies,
		ModelCatalog: wire.ModelCatalog, Tokens: tokens,
	}
	return nil
}

// encodeMigrationPackage 校验迁移载荷后生成单文件加密迁移包。
func encodeMigrationPackage(payload migrationPayload, password string) ([]byte, error) {
	if err := validateMigrationPayload(payload); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化迁移载荷失败: %w", err)
	}
	return encodeMigrationEnvelope(encoded, password)
}

// parseMigrationPackage 解密、严格解析并校验迁移载荷，不执行任何持久化写入。
func parseMigrationPackage(ctx context.Context, encoded []byte, password string) (migrationPayload, error) {
	startedAt := time.Now()
	decodeCtx, cancel := context.WithTimeout(ctx, migrationMaxDecodeDuration)
	defer cancel()
	decoded, err := decodeMigrationEnvelope(decodeCtx, encoded, password)
	if err != nil {
		return migrationPayload{}, err
	}
	if err := migrationDecodeContextError(decodeCtx, startedAt); err != nil {
		return migrationPayload{}, err
	}
	var payload migrationPayload
	if err := decodeStrictBytes(decoded, &payload); err != nil {
		return migrationPayload{}, errors.New("迁移载荷 JSON 无效或包含未知字段")
	}
	if err := migrationDecodeContextError(decodeCtx, startedAt); err != nil {
		return migrationPayload{}, err
	}
	if err := validateMigrationPayload(payload); err != nil {
		return migrationPayload{}, err
	}
	if err := migrationDecodeContextError(decodeCtx, startedAt); err != nil {
		return migrationPayload{}, err
	}
	return payload, nil
}

func validateMigrationPayload(payload migrationPayload) error {
	if payload.Format != migrationPayloadFormat || payload.MigrationVersion != migrationVersion {
		return errors.New("迁移载荷格式或迁移版本不受支持")
	}
	if !supportedExportSchemaVersion(payload.ConfigSchemaVersion) {
		return fmt.Errorf("迁移载荷配置版本不受支持: %d", payload.ConfigSchemaVersion)
	}
	if payload.DatabaseSchemaVersion != storage.CurrentSchemaVersion() {
		return fmt.Errorf("迁移载荷数据库版本不受支持: %d", payload.DatabaseSchemaVersion)
	}
	document := exportDocument{
		Format: exportFormat, SchemaVersion: exportSchemaVersion, ExportMode: "complete_encrypted",
		ExportedAt: "migration", AppVersion: exportAppVersion,
		Channels: payload.Channels, ChannelModels: payload.ChannelModels,
		GlobalModelMappings: payload.GlobalModelMappings, ModelCatalog: payload.ModelCatalog,
		ChannelModelMappings: payload.ChannelModelMappings, ProbePolicies: payload.ProbePolicies,
		Settings: payload.Settings, Tokens: &payload.Tokens,
	}
	if err := validateExportDocument(document, "complete_encrypted"); err != nil {
		return err
	}
	seenCatalog := make(map[string]struct{}, len(payload.ModelCatalog))
	for _, entry := range payload.ModelCatalog {
		if _, exists := seenCatalog[entry.StableKey]; exists {
			return fmt.Errorf("模型目录稳定键重复: %s", entry.StableKey)
		}
		seenCatalog[entry.StableKey] = struct{}{}
	}
	return nil
}

// encodeMigrationEnvelope 压缩并加密迁移载荷，返回独立 JSON 信封。
func encodeMigrationEnvelope(payload []byte, password string) ([]byte, error) {
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("迁移包口令不能为空")
	}
	if len(payload) == 0 || len(payload) > migrationMaxPayloadBytes {
		return nil, errors.New("迁移载荷大小无效")
	}
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(payload); err != nil {
		return nil, fmt.Errorf("压缩迁移载荷失败: %w", err)
	}
	if err := compressor.Close(); err != nil {
		return nil, fmt.Errorf("完成迁移载荷压缩失败: %w", err)
	}
	salt := make([]byte, migrationSaltBytes)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("生成迁移包盐失败: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成迁移包随机数失败: %w", err)
	}
	envelope := migrationEnvelope{
		Format: migrationFormat, MigrationVersion: migrationVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339), AppVersion: exportAppVersion,
		ConfigSchemaVersion: exportSchemaVersion, DatabaseSchemaVersion: storage.CurrentSchemaVersion(),
		PayloadEncoding: migrationPayloadEncoding,
		KDF:             exportKDF{Algorithm: "argon2id", Salt: base64.StdEncoding.EncodeToString(salt), MemoryKiB: kdfMemoryKiB, Iterations: kdfIterations, Parallelism: kdfParallelism},
		Cipher:          exportCipher{Algorithm: "xchacha20-poly1305", Nonce: base64.StdEncoding.EncodeToString(nonce)},
	}
	aad, err := migrationEnvelopeAdditionalData(envelope)
	if err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, kdfIterations, kdfMemoryKiB, kdfParallelism, chacha20poly1305.KeySize)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("初始化迁移包加密失败: %w", err)
	}
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, compressed.Bytes(), aad))
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("序列化迁移包失败: %w", err)
	}
	if len(encoded) > migrationMaxEnvelopeBytes {
		return nil, errors.New("迁移包超过大小限制")
	}
	return encoded, nil
}

// decodeMigrationEnvelope 校验、解密并限量解压迁移包载荷。
func decodeMigrationEnvelope(ctx context.Context, encoded []byte, password string) ([]byte, error) {
	startedAt := time.Now()
	if len(encoded) == 0 || len(encoded) > migrationMaxEnvelopeBytes {
		return nil, errors.New("迁移包大小无效")
	}
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("迁移包口令不能为空")
	}
	var envelope migrationEnvelope
	if err := decodeStrictBytes(encoded, &envelope); err != nil {
		return nil, errors.New("迁移包 JSON 无效或包含未知字段")
	}
	if err := validateMigrationEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := migrationDecodeContextError(ctx, startedAt); err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(envelope.KDF.Salt)
	if err != nil || len(salt) != migrationSaltBytes {
		return nil, errors.New("迁移包盐无效")
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Cipher.Nonce)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("迁移包随机数无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < migrationCiphertextOverhead || len(ciphertext) > migrationMaxEnvelopeBytes {
		return nil, errors.New("迁移包密文无效")
	}
	aad, err := migrationEnvelopeAdditionalData(envelope)
	if err != nil {
		return nil, err
	}
	key, err := deriveMigrationKey(ctx, password, salt)
	if err != nil {
		return nil, err
	}
	if err := migrationDecodeContextError(ctx, startedAt); err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("初始化迁移包解密失败: %w", err)
	}
	compressed, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("迁移包口令错误或文件已被篡改")
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, errors.New("迁移包压缩载荷无效")
	}
	payload, readErr := readMigrationPayload(ctx, reader, migrationMaxPayloadBytes, startedAt)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.New("解压迁移载荷失败")
	}
	if len(payload) == 0 || len(payload) > migrationMaxPayloadBytes {
		return nil, errors.New("迁移载荷超过大小限制")
	}
	if len(compressed) == 0 || len(payload) > len(compressed)*migrationMaxExpansionRatio {
		return nil, errors.New("迁移载荷解压比例超过限制")
	}
	return payload, nil
}

// deriveMigrationKey 在请求取消时等待当前 KDF 完成，避免释放迁移槽位时仍有后台计算。
func deriveMigrationKey(ctx context.Context, password string, salt []byte) ([]byte, error) {
	result := make(chan []byte, 1)
	go func() {
		result <- argon2.IDKey([]byte(password), salt, kdfIterations, kdfMemoryKiB, kdfParallelism, chacha20poly1305.KeySize)
	}()
	select {
	case key := <-result:
		return key, nil
	case <-ctx.Done():
		<-result
		return nil, migrationDecodeContextError(ctx, time.Now())
	}
}

func readMigrationPayload(ctx context.Context, reader io.Reader, limit int, startedAt time.Time) ([]byte, error) {
	var payload bytes.Buffer
	buffer := make([]byte, 32*1024)
	for {
		if err := migrationDecodeContextError(ctx, startedAt); err != nil {
			return nil, err
		}
		readCount, err := reader.Read(buffer)
		if readCount > 0 {
			if payload.Len()+readCount > limit {
				return nil, errors.New("迁移载荷超过大小限制")
			}
			_, _ = payload.Write(buffer[:readCount])
		}
		if err == io.EOF {
			return payload.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func migrationDecodeContextError(ctx context.Context, startedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("迁移包解析超时")
		}
		return errors.New("迁移包解析已取消")
	}
	if !startedAt.IsZero() && time.Since(startedAt) > migrationMaxDecodeDuration {
		return errors.New("迁移包解析超时")
	}
	return nil
}

func validateMigrationEnvelope(envelope migrationEnvelope) error {
	if envelope.Format != migrationFormat || envelope.MigrationVersion != migrationVersion {
		return errors.New("迁移包格式或迁移版本不受支持")
	}
	if !supportedExportSchemaVersion(envelope.ConfigSchemaVersion) {
		return fmt.Errorf("迁移包配置版本不受支持: %d", envelope.ConfigSchemaVersion)
	}
	if envelope.DatabaseSchemaVersion != storage.CurrentSchemaVersion() {
		return fmt.Errorf("迁移包数据库版本不受支持: %d", envelope.DatabaseSchemaVersion)
	}
	if strings.TrimSpace(envelope.ExportedAt) == "" || strings.TrimSpace(envelope.AppVersion) == "" || envelope.PayloadEncoding != migrationPayloadEncoding {
		return errors.New("迁移包元数据无效")
	}
	if envelope.KDF.Algorithm != "argon2id" || envelope.KDF.MemoryKiB != kdfMemoryKiB || envelope.KDF.Iterations != kdfIterations || envelope.KDF.Parallelism != kdfParallelism {
		return errors.New("迁移包 KDF 参数不受支持")
	}
	if envelope.Cipher.Algorithm != "xchacha20-poly1305" {
		return errors.New("迁移包加密算法不受支持")
	}
	return nil
}

func migrationEnvelopeAdditionalData(envelope migrationEnvelope) ([]byte, error) {
	value := migrationEnvelopeAAD{
		Format: envelope.Format, MigrationVersion: envelope.MigrationVersion, ExportedAt: envelope.ExportedAt,
		AppVersion: envelope.AppVersion, ConfigSchemaVersion: envelope.ConfigSchemaVersion,
		DatabaseSchemaVersion: envelope.DatabaseSchemaVersion, PayloadEncoding: envelope.PayloadEncoding,
		KDF: envelope.KDF, Cipher: envelope.Cipher,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("序列化迁移包认证元数据失败: %w", err)
	}
	return encoded, nil
}
