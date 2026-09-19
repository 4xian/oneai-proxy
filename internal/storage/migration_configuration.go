package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/4xian/oneai-proxy/internal/config"
)

// MigrationConfiguration 是跨平台迁移包允许替换的配置集合，不包含日志和运行态。
type MigrationConfiguration struct {
	Channels              []Channel
	ChannelModels         map[string][]string
	GlobalModelMappings   GlobalModelMappingSet
	ChannelModelMappings  []ChannelModelMapping
	ProbePolicies         []ProbePolicy
	Settings              config.Settings
	ModelCatalog          []ModelCatalogEntry
	ModelCatalogSourceURL string
	Auth                  AuthTokens
	PendingSecretRefs     []string
}

// MigrationReplaceResult 描述迁移后保留的历史渠道占位。
type MigrationReplaceResult struct {
	PlaceholderChannelIDs []string
}

const migrationPendingSecretCleanupKey = "migration.pending-secret-cleanup"

// IsSecretRefReferenced 判断当前配置是否仍引用指定的密钥环引用。
func IsSecretRefReferenced(database *sql.DB, ref string) (bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false, nil
	}
	var referenced int
	if err := database.QueryRow(`SELECT EXISTS (SELECT 1 FROM channels WHERE secret_ref = ?)`, ref).Scan(&referenced); err != nil {
		return false, fmt.Errorf("检查凭证引用失败: %w", err)
	}
	return referenced == 1, nil
}

// SaveMigrationPendingSecretRefs 保存暂存或提交后尚未清理的凭证引用。
func SaveMigrationPendingSecretRefs(database *sql.DB, refs []string) error {
	normalized := uniqueSortedStrings(refs)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化待清理凭证引用失败: %w", err)
	}
	_, err = database.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, migrationPendingSecretCleanupKey, string(encoded), nowUTC())
	if err != nil {
		return fmt.Errorf("保存待清理凭证引用失败: %w", err)
	}
	return nil
}

// LoadMigrationPendingSecretRefs 读取上次迁移遗留的凭证引用。
func LoadMigrationPendingSecretRefs(database *sql.DB) ([]string, error) {
	var encoded string
	err := database.QueryRow(`SELECT value_json FROM app_settings WHERE key = ?`, migrationPendingSecretCleanupKey).Scan(&encoded)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取待清理凭证引用失败: %w", err)
	}
	var refs []string
	if err := json.Unmarshal([]byte(encoded), &refs); err != nil {
		return nil, fmt.Errorf("解析待清理凭证引用失败: %w", err)
	}
	return uniqueSortedStrings(refs), nil
}

// ClearMigrationPendingSecretRefs 清除已完成的旧凭证清理记录。
func ClearMigrationPendingSecretRefs(database *sql.DB) error {
	if _, err := database.Exec(`DELETE FROM app_settings WHERE key = ?`, migrationPendingSecretCleanupKey); err != nil {
		return fmt.Errorf("清除待清理凭证引用失败: %w", err)
	}
	return nil
}

// ReplaceMigrationConfiguration 在单个事务中替换可迁移配置并保留历史引用。
func ReplaceMigrationConfiguration(database *sql.DB, configuration MigrationConfiguration) (MigrationReplaceResult, error) {
	globalMappings := globalModelMappingSlice(configuration.GlobalModelMappings)
	if err := validateConfiguration(configuration.Channels, configuration.ChannelModels, globalMappings, configuration.ChannelModelMappings, configuration.ProbePolicies); err != nil {
		return MigrationReplaceResult{}, err
	}
	seenCatalog := make(map[string]struct{}, len(configuration.ModelCatalog))
	for _, entry := range configuration.ModelCatalog {
		if err := ValidateModelCatalogEntry(entry); err != nil {
			return MigrationReplaceResult{}, err
		}
		if _, exists := seenCatalog[entry.StableKey]; exists {
			return MigrationReplaceResult{}, fmt.Errorf("模型目录稳定键重复: %s", entry.StableKey)
		}
		seenCatalog[entry.StableKey] = struct{}{}
	}
	if configuration.Auth.AdminToken == "" || configuration.Auth.ProxyToken == "" {
		return MigrationReplaceResult{}, fmt.Errorf("鉴权令牌配置不完整")
	}

	transaction, err := database.Begin()
	if err != nil {
		return MigrationReplaceResult{}, fmt.Errorf("开始迁移配置事务失败: %w", err)
	}
	rollback := func(saveErr error) (MigrationReplaceResult, error) {
		_ = transaction.Rollback()
		return MigrationReplaceResult{}, saveErr
	}

	currentIDs, err := listChannelIDsTx(transaction)
	if err != nil {
		return rollback(fmt.Errorf("读取当前渠道失败: %w", err))
	}
	incomingIDs := make(map[string]struct{}, len(configuration.Channels))
	for _, channel := range configuration.Channels {
		incomingIDs[channel.ID] = struct{}{}
	}
	placeholders := make([]string, 0)
	for _, channelID := range currentIDs {
		if _, exists := incomingIDs[channelID]; exists {
			continue
		}
		referenced, referenceErr := channelHasHistoricalReferenceTx(transaction, channelID)
		if referenceErr != nil {
			return rollback(referenceErr)
		}
		if referenced {
			if _, err := transaction.Exec(`UPDATE channels SET admin_state = 'disabled', secret_ref = '', health_version = health_version + 1, updated_at = ? WHERE id = ?`, nowUTC(), channelID); err != nil {
				return rollback(fmt.Errorf("保留历史渠道占位失败: %w", err))
			}
			placeholders = append(placeholders, channelID)
			continue
		}
		if _, err := transaction.Exec(`DELETE FROM channels WHERE id = ?`, channelID); err != nil {
			return rollback(fmt.Errorf("删除无引用旧渠道失败: %w", err))
		}
	}
	sort.Strings(placeholders)

	for _, statement := range []string{
		`DELETE FROM channel_models`,
		`DELETE FROM channel_model_mappings`,
		`DELETE FROM probe_policies`,
		`DELETE FROM model_catalog`,
		`DELETE FROM global_model_mappings`,
	} {
		if _, err := transaction.Exec(statement); err != nil {
			return rollback(fmt.Errorf("清理迁移配置子表失败: %w", err))
		}
	}

	for _, channel := range configuration.Channels {
		if err := upsertMigrationChannelTx(transaction, channel); err != nil {
			return rollback(err)
		}
	}
	for channelID, models := range configuration.ChannelModels {
		normalizedModels, err := normalizeChannelModels(models)
		if err != nil {
			return rollback(fmt.Errorf("规范化迁移渠道模型目录失败: %s: %w", channelID, err))
		}
		for _, model := range normalizedModels {
			if _, err := transaction.Exec(`INSERT INTO channel_models(channel_id, model) VALUES (?, ?)`, channelID, model); err != nil {
				return rollback(fmt.Errorf("写入迁移渠道模型目录失败: %w", err))
			}
		}
	}
	for _, mapping := range globalMappings {
		if _, err := transaction.Exec(`INSERT INTO global_model_mappings(protocol_family, client_model, logical_model) VALUES (?, ?, ?)`, protocolFamily(mapping.Protocol), mapping.ClientModel, mapping.LogicalModel); err != nil {
			return rollback(fmt.Errorf("写入迁移全局模型映射失败: %w", err))
		}
	}
	for _, entry := range configuration.ModelCatalog {
		if entry.CreatedAt == "" {
			entry.CreatedAt = nowUTC()
		}
		if entry.UpdatedAt == "" {
			entry.UpdatedAt = entry.CreatedAt
		}
		if _, err := upsertModelCatalogEntryTx(transaction, entry, true); err != nil {
			return rollback(fmt.Errorf("写入迁移模型目录失败: %w", err))
		}
	}
	for _, mapping := range configuration.ChannelModelMappings {
		if _, err := transaction.Exec(`INSERT INTO channel_model_mappings(channel_id, protocol, logical_model, upstream_model) VALUES (?, ?, ?, ?)`, mapping.ChannelID, mapping.Protocol, mapping.LogicalModel, mapping.UpstreamModel); err != nil {
			return rollback(fmt.Errorf("写入迁移渠道模型映射失败: %w", err))
		}
	}
	for _, policy := range configuration.ProbePolicies {
		policy = normalizeProbePolicy(policy)
		if _, err := transaction.Exec(`INSERT INTO probe_policies(channel_id, enabled, mode, interval_seconds, model, path, failure_threshold, request_timeout_ms, auto_recover, recovery_success_threshold) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, policy.ChannelID, boolInt(policy.Enabled), policy.Mode, policy.IntervalSeconds, policy.Model, policy.Path, policy.FailureThreshold, policy.RequestTimeoutMs, boolInt(policy.AutoRecover), policy.RecoverySuccessThreshold); err != nil {
			return rollback(fmt.Errorf("写入迁移探针策略失败: %w", err))
		}
	}
	settingsJSON, err := json.Marshal(configuration.Settings)
	if err != nil {
		return rollback(fmt.Errorf("序列化迁移监听配置失败: %w", err))
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('runtime.listeners', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(settingsJSON), nowUTC()); err != nil {
		return rollback(fmt.Errorf("写入迁移监听配置失败: %w", err))
	}
	sourceJSON, err := json.Marshal(strings.TrimSpace(configuration.ModelCatalogSourceURL))
	if err != nil {
		return rollback(fmt.Errorf("序列化迁移模型目录来源失败: %w", err))
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('model-catalog.source-url', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(sourceJSON), nowUTC()); err != nil {
		return rollback(fmt.Errorf("写入迁移模型目录来源失败: %w", err))
	}
	if err := saveAuthTokensTx(transaction, configuration.Auth); err != nil {
		return rollback(err)
	}
	pendingRefs := uniqueSortedStrings(configuration.PendingSecretRefs)
	if len(pendingRefs) == 0 {
		if _, err := transaction.Exec(`DELETE FROM app_settings WHERE key = ?`, migrationPendingSecretCleanupKey); err != nil {
			return rollback(fmt.Errorf("清除迁移待清理凭证记录失败: %w", err))
		}
	} else {
		encoded, err := json.Marshal(pendingRefs)
		if err != nil {
			return rollback(fmt.Errorf("序列化迁移待清理凭证引用失败: %w", err))
		}
		if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, migrationPendingSecretCleanupKey, string(encoded), nowUTC()); err != nil {
			return rollback(fmt.Errorf("写入迁移待清理凭证记录失败: %w", err))
		}
	}
	if err := transaction.Commit(); err != nil {
		return MigrationReplaceResult{}, fmt.Errorf("提交迁移配置事务失败: %w", err)
	}
	return MigrationReplaceResult{PlaceholderChannelIDs: placeholders}, nil
}

func listChannelIDsTx(transaction *sql.Tx) ([]string, error) {
	rows, err := transaction.Query(`SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func channelHasHistoricalReferenceTx(transaction *sql.Tx, channelID string) (bool, error) {
	var referenced int
	err := transaction.QueryRow(`SELECT CASE WHEN
		EXISTS (SELECT 1 FROM requests WHERE initial_channel_id = ? OR final_channel_id = ?) OR
		EXISTS (SELECT 1 FROM attempts WHERE channel_id = ?)
		THEN 1 ELSE 0 END`, channelID, channelID, channelID).Scan(&referenced)
	if err != nil {
		return false, fmt.Errorf("检查渠道请求记录失败: %w", err)
	}
	return referenced == 1, nil
}

func upsertMigrationChannelTx(transaction *sql.Tx, channel Channel) error {
	capabilities, _ := json.Marshal(normalizeCapabilities(channel.Capabilities))
	headers, _ := json.Marshal(channel.CustomHeaders)
	createdAt := defaultString(channel.CreatedAt, nowUTC())
	updatedAt := defaultString(channel.UpdatedAt, createdAt)
	failureAction := defaultString(channel.FailureAction, "cooldown")
	failureThreshold := channel.FailureThreshold
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	channel = normalizeChannelRuntime(channel)
	_, err := transaction.Exec(`INSERT INTO channels(id, name, note, group_name, protocol, base_url, capabilities_json, admin_state, health_state, health_version, created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json, concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, priority, fallback_model, reasoning_effort, service_tier_passthrough) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'healthy', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name = excluded.name, note = excluded.note, group_name = excluded.group_name, protocol = excluded.protocol, base_url = excluded.base_url, capabilities_json = excluded.capabilities_json, admin_state = excluded.admin_state, health_version = channels.health_version + 1, created_at = excluded.created_at, updated_at = excluded.updated_at, secret_ref = excluded.secret_ref, failure_action = excluded.failure_action, failure_threshold = excluded.failure_threshold, custom_headers_json = excluded.custom_headers_json, concurrency_limit = excluded.concurrency_limit, request_timeout_ms = excluded.request_timeout_ms, stream_idle_timeout_ms = excluded.stream_idle_timeout_ms, cooldown_seconds = excluded.cooldown_seconds, priority = excluded.priority, fallback_model = excluded.fallback_model, reasoning_effort = excluded.reasoning_effort, service_tier_passthrough = excluded.service_tier_passthrough`, channel.ID, channel.Name, channel.Note, channel.Group, channel.Protocol, channel.BaseURL, string(capabilities), defaultString(channel.AdminState, "enabled"), createdAt, updatedAt, channel.SecretRef, failureAction, failureThreshold, string(headers), channel.ConcurrencyLimit, channel.RequestTimeoutMs, channel.StreamIdleTimeoutMs, channel.CooldownSeconds, channel.Priority, channel.FallbackModel, channel.ReasoningEffort, boolInt(channel.ServiceTierPassthrough))
	if err != nil {
		return fmt.Errorf("写入迁移渠道失败: %w", err)
	}
	if _, err := transaction.Exec(`INSERT INTO health_runtime(channel_id) VALUES (?) ON CONFLICT(channel_id) DO NOTHING`, channel.ID); err != nil {
		return fmt.Errorf("初始化迁移渠道健康运行态失败: %w", err)
	}
	return nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
