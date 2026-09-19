package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
)

// ListGlobalModelMappings 返回全局模型映射。
func ListGlobalModelMappings(database *sql.DB) ([]GlobalModelMapping, error) {
	rows, err := database.Query(`SELECT protocol_family, client_model, logical_model FROM global_model_mappings ORDER BY protocol_family, client_model`)
	if err != nil {
		return nil, fmt.Errorf("读取全局模型映射失败: %w", err)
	}
	defer rows.Close()
	mappings := make([]GlobalModelMapping, 0)
	for rows.Next() {
		var mapping GlobalModelMapping
		var family string
		if err := rows.Scan(&family, &mapping.ClientModel, &mapping.LogicalModel); err != nil {
			return nil, fmt.Errorf("读取全局模型映射字段失败: %w", err)
		}
		if family == "anthropic" {
			mapping.Protocol = ProtocolAnthropicMessages
		} else {
			mapping.Protocol = ProtocolOpenAIChat
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

// SaveGlobalModelMappings 用事务替换全局模型映射。
func SaveGlobalModelMappings(database *sql.DB, mappings []GlobalModelMapping) error {
	for _, mapping := range mappings {
		if err := validateMapping(mapping); err != nil {
			return err
		}
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始保存全局映射事务失败: %w", err)
	}
	for _, mapping := range mappings {
		if err := validateGlobalMappingReference(transaction, mapping); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	if _, err := transaction.Exec(`DELETE FROM global_model_mappings`); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("清理全局模型映射失败: %w", err)
	}
	for _, mapping := range mappings {
		if _, err := transaction.Exec(`INSERT INTO global_model_mappings(protocol_family, client_model, logical_model) VALUES (?, ?, ?)`, protocolFamily(mapping.Protocol), mapping.ClientModel, mapping.LogicalModel); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("保存全局模型映射失败: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交全局模型映射事务失败: %w", err)
	}
	return nil
}

// LoadGlobalModelMappingSet 返回 OpenAI 和 Anthropic 两个协议族的单层映射。
func LoadGlobalModelMappingSet(database *sql.DB) (GlobalModelMappingSet, error) {
	set := GlobalModelMappingSet{OpenAI: make(map[string]string), Anthropic: make(map[string]string)}
	rows, err := database.Query(`SELECT protocol_family, client_model, logical_model FROM global_model_mappings ORDER BY protocol_family, client_model`)
	if err != nil {
		return GlobalModelMappingSet{}, fmt.Errorf("读取协议族模型映射失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var family, clientModel, logicalModel string
		if err := rows.Scan(&family, &clientModel, &logicalModel); err != nil {
			return GlobalModelMappingSet{}, fmt.Errorf("读取协议族模型映射字段失败: %w", err)
		}
		if family == "anthropic" {
			set.Anthropic[clientModel] = logicalModel
		} else {
			set.OpenAI[clientModel] = logicalModel
		}
	}
	if err := rows.Err(); err != nil {
		return GlobalModelMappingSet{}, fmt.Errorf("遍历协议族模型映射失败: %w", err)
	}
	return set, nil
}

// SaveGlobalModelMappingFamily 只替换一个协议族的单层映射。
func SaveGlobalModelMappingFamily(database *sql.DB, family string, mappings map[string]string) error {
	if family != "openai" && family != "anthropic" {
		return fmt.Errorf("协议族无效: %s", family)
	}
	normalized := make(map[string]string, len(mappings))
	for clientModel, logicalModel := range mappings {
		clientModel, logicalModel = strings.TrimSpace(clientModel), strings.TrimSpace(logicalModel)
		if clientModel == "" || logicalModel == "" || clientModel == logicalModel || strings.ContainsAny(clientModel+logicalModel, "\r\n") {
			return errors.New("全局模型映射键和值必须是非空且不能自映射")
		}
		normalized[clientModel] = logicalModel
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始保存协议族模型映射事务失败: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`DELETE FROM global_model_mappings WHERE protocol_family = ?`, family); err != nil {
		return fmt.Errorf("清理协议族模型映射失败: %w", err)
	}
	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, clientModel := range keys {
		if _, err := transaction.Exec(`INSERT INTO global_model_mappings(protocol_family, client_model, logical_model) VALUES (?, ?, ?)`, family, clientModel, normalized[clientModel]); err != nil {
			return fmt.Errorf("保存协议族模型映射失败: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交协议族模型映射事务失败: %w", err)
	}
	return nil
}

// ListChannelModelMappings 返回渠道级模型映射。
func ListChannelModelMappings(database *sql.DB) ([]ChannelModelMapping, error) {
	rows, err := database.Query(`SELECT channel_id, protocol, logical_model, upstream_model FROM channel_model_mappings ORDER BY channel_id, protocol, logical_model`)
	if err != nil {
		return nil, fmt.Errorf("读取渠道模型映射失败: %w", err)
	}
	defer rows.Close()
	mappings := make([]ChannelModelMapping, 0)
	for rows.Next() {
		var mapping ChannelModelMapping
		if err := rows.Scan(&mapping.ChannelID, &mapping.Protocol, &mapping.LogicalModel, &mapping.UpstreamModel); err != nil {
			return nil, fmt.Errorf("读取渠道模型映射字段失败: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

// ListAllChannelModels 返回全部渠道模型目录，按渠道和模型稳定排序。
func ListAllChannelModels(database *sql.DB) ([]ChannelModel, error) {
	rows, err := database.Query(`SELECT channel_id, model FROM channel_models ORDER BY channel_id, model`)
	if err != nil {
		return nil, fmt.Errorf("读取渠道模型目录失败: %w", err)
	}
	defer rows.Close()
	models := make([]ChannelModel, 0)
	for rows.Next() {
		var model ChannelModel
		if err := rows.Scan(&model.ChannelID, &model.Model); err != nil {
			return nil, fmt.Errorf("读取渠道模型目录字段失败: %w", err)
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

// ListChannelModels 返回一个渠道可直接使用的模型名称。
func ListChannelModels(database *sql.DB, channelID string) ([]string, error) {
	rows, err := database.Query(`SELECT model FROM channel_models WHERE channel_id = ? ORDER BY model`, channelID)
	if err != nil {
		return nil, fmt.Errorf("读取渠道模型目录失败: %w", err)
	}
	defer rows.Close()
	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, fmt.Errorf("读取渠道模型名称失败: %w", err)
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

// SaveChannelModelMappings 用事务替换渠道级模型映射。
func SaveChannelModelMappings(database *sql.DB, mappings []ChannelModelMapping) error {
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始保存渠道映射事务失败: %w", err)
	}
	for _, mapping := range mappings {
		if err := validateChannelMapping(mapping); err != nil {
			_ = transaction.Rollback()
			return err
		}
		if err := validateChannelMappingReference(transaction, mapping); err != nil {
			_ = transaction.Rollback()
			return err
		}
	}
	if _, err := transaction.Exec(`DELETE FROM channel_model_mappings`); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("清理渠道模型映射失败: %w", err)
	}
	for _, mapping := range mappings {
		if _, err := transaction.Exec(`INSERT INTO channel_model_mappings(channel_id, protocol, logical_model, upstream_model) VALUES (?, ?, ?, ?)`, mapping.ChannelID, mapping.Protocol, mapping.LogicalModel, mapping.UpstreamModel); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("保存渠道模型映射失败: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交渠道模型映射事务失败: %w", err)
	}
	return nil
}

// ListProbePolicies 返回所有探针策略。
func ListProbePolicies(database *sql.DB) ([]ProbePolicy, error) {
	rows, err := database.Query(`SELECT channel_id, enabled, mode, interval_seconds, model, path, failure_threshold, request_timeout_ms, auto_recover, recovery_success_threshold FROM probe_policies ORDER BY channel_id`)
	if err != nil {
		return nil, fmt.Errorf("读取探针策略失败: %w", err)
	}
	defer rows.Close()
	policies := make([]ProbePolicy, 0)
	for rows.Next() {
		var policy ProbePolicy
		var enabled, autoRecover int
		if err := rows.Scan(&policy.ChannelID, &enabled, &policy.Mode, &policy.IntervalSeconds, &policy.Model, &policy.Path, &policy.FailureThreshold, &policy.RequestTimeoutMs, &autoRecover, &policy.RecoverySuccessThreshold); err != nil {
			return nil, fmt.Errorf("读取探针策略字段失败: %w", err)
		}
		policy.Enabled = enabled == 1
		policy.AutoRecover = autoRecover == 1
		policy = normalizeProbePolicy(policy)
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

// ReplaceConfiguration 在一个事务内替换所有可导出的配置记录。
// 调用方应在进入本方法前完成秘密暂存；本方法只写入秘密引用，不接触秘密明文。
func ReplaceConfiguration(database *sql.DB, channels []Channel, channelModels map[string][]string, globalMappings []GlobalModelMapping, channelMappings []ChannelModelMapping, policies []ProbePolicy, settings config.Settings) error {
	return ReplaceConfigurationWithAuth(database, channels, channelModels, globalMappings, channelMappings, policies, settings, nil)
}

// ReplaceConfigurationWithAuth 在同一事务内替换配置和可选的鉴权令牌。
func ReplaceConfigurationWithAuth(database *sql.DB, channels []Channel, channelModels map[string][]string, globalMappings []GlobalModelMapping, channelMappings []ChannelModelMapping, policies []ProbePolicy, settings config.Settings, auth *AuthTokens) error {
	return ReplaceConfigurationWithAuthAndCatalog(database, channels, channelModels, globalModelMappingSetFromSlice(globalMappings), channelMappings, policies, settings, nil, "", auth)
}

// ReplaceConfigurationWithAuthAndCatalog 在同一事务内替换配置、模型目录和可选鉴权令牌。
func ReplaceConfigurationWithAuthAndCatalog(database *sql.DB, channels []Channel, channelModels map[string][]string, globalMappings GlobalModelMappingSet, channelMappings []ChannelModelMapping, policies []ProbePolicy, settings config.Settings, modelCatalog []ModelCatalogEntry, modelCatalogSourceURL string, auth *AuthTokens) error {
	globalMappingSlice := globalModelMappingSlice(globalMappings)
	if err := validateConfiguration(channels, channelModels, globalMappingSlice, channelMappings, policies); err != nil {
		return err
	}
	seenCatalog := make(map[string]struct{}, len(modelCatalog))
	for _, entry := range modelCatalog {
		if err := ValidateModelCatalogEntry(entry); err != nil {
			return err
		}
		if _, exists := seenCatalog[entry.StableKey]; exists {
			return fmt.Errorf("模型目录稳定键重复: %s", entry.StableKey)
		}
		seenCatalog[entry.StableKey] = struct{}{}
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始替换配置事务失败: %w", err)
	}
	rollback := func(saveErr error) error {
		_ = transaction.Rollback()
		return saveErr
	}
	currentIDs, err := listChannelIDsTx(transaction)
	if err != nil {
		return rollback(fmt.Errorf("读取当前渠道失败: %w", err))
	}
	incomingIDs := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		incomingIDs[channel.ID] = struct{}{}
	}
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
			continue
		}
		if _, err := transaction.Exec(`DELETE FROM channels WHERE id = ?`, channelID); err != nil {
			return rollback(fmt.Errorf("删除无引用旧渠道失败: %w", err))
		}
	}
	// 只替换可导出的配置子表，历史和运行记录保持不变。
	for _, statement := range []string{
		`DELETE FROM channel_models`,
		`DELETE FROM channel_model_mappings`,
		`DELETE FROM probe_policies`,
		`DELETE FROM model_catalog`,
		`DELETE FROM global_model_mappings`,
	} {
		if _, err := transaction.Exec(statement); err != nil {
			return rollback(fmt.Errorf("清理旧配置失败: %w", err))
		}
	}
	for _, channel := range channels {
		if err := upsertMigrationChannelTx(transaction, channel); err != nil {
			return rollback(err)
		}
	}
	for channelID, models := range channelModels {
		normalizedModels, err := normalizeChannelModels(models)
		if err != nil {
			return rollback(fmt.Errorf("规范化渠道模型目录失败: %s: %w", channelID, err))
		}
		for _, model := range normalizedModels {
			if _, err := transaction.Exec(`INSERT INTO channel_models(channel_id, model) VALUES (?, ?)`, channelID, model); err != nil {
				return rollback(fmt.Errorf("写入渠道模型目录失败: %w", err))
			}
		}
	}
	for _, mapping := range globalMappingSlice {
		if _, err := transaction.Exec(`INSERT INTO global_model_mappings(protocol_family, client_model, logical_model) VALUES (?, ?, ?)`, protocolFamily(mapping.Protocol), mapping.ClientModel, mapping.LogicalModel); err != nil {
			return rollback(fmt.Errorf("写入全局模型映射失败: %w", err))
		}
	}
	for _, entry := range modelCatalog {
		if entry.CreatedAt == "" {
			entry.CreatedAt = nowUTC()
		}
		if entry.UpdatedAt == "" {
			entry.UpdatedAt = entry.CreatedAt
		}
		if _, err := upsertModelCatalogEntryTx(transaction, entry, true); err != nil {
			return rollback(fmt.Errorf("写入模型目录失败: %w", err))
		}
	}
	for _, mapping := range channelMappings {
		if _, err := transaction.Exec(`INSERT INTO channel_model_mappings(channel_id, protocol, logical_model, upstream_model) VALUES (?, ?, ?, ?)`, mapping.ChannelID, mapping.Protocol, mapping.LogicalModel, mapping.UpstreamModel); err != nil {
			return rollback(fmt.Errorf("写入渠道模型映射失败: %w", err))
		}
	}
	for _, policy := range policies {
		policy = normalizeProbePolicy(policy)
		enabled, autoRecover := boolInt(policy.Enabled), boolInt(policy.AutoRecover)
		if _, err := transaction.Exec(`INSERT INTO probe_policies(channel_id, enabled, mode, interval_seconds, model, path, failure_threshold, request_timeout_ms, auto_recover, recovery_success_threshold) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, policy.ChannelID, enabled, policy.Mode, policy.IntervalSeconds, policy.Model, policy.Path, policy.FailureThreshold, policy.RequestTimeoutMs, autoRecover, policy.RecoverySuccessThreshold); err != nil {
			return rollback(fmt.Errorf("写入探针策略失败: %w", err))
		}
	}
	value, err := json.Marshal(settings)
	if err != nil {
		return rollback(fmt.Errorf("序列化监听配置失败: %w", err))
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('runtime.listeners', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(value), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return rollback(fmt.Errorf("写入监听配置失败: %w", err))
	}
	sourceJSON, marshalErr := json.Marshal(strings.TrimSpace(modelCatalogSourceURL))
	if marshalErr != nil {
		return rollback(fmt.Errorf("序列化模型目录来源地址失败: %w", marshalErr))
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('model-catalog.source-url', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(sourceJSON), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return rollback(fmt.Errorf("写入模型目录来源地址失败: %w", err))
	}
	if auth != nil {
		if err := saveAuthTokensTx(transaction, *auth); err != nil {
			return rollback(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交配置替换事务失败: %w", err)
	}
	return nil
}

func globalModelMappingSetFromSlice(mappings []GlobalModelMapping) GlobalModelMappingSet {
	set := GlobalModelMappingSet{OpenAI: make(map[string]string), Anthropic: make(map[string]string)}
	for _, mapping := range mappings {
		if mapping.Protocol == ProtocolAnthropicMessages {
			set.Anthropic[mapping.ClientModel] = mapping.LogicalModel
		} else {
			set.OpenAI[mapping.ClientModel] = mapping.LogicalModel
		}
	}
	return set
}

func globalModelMappingSlice(set GlobalModelMappingSet) []GlobalModelMapping {
	result := make([]GlobalModelMapping, 0, len(set.OpenAI)+len(set.Anthropic))
	for clientModel, logicalModel := range set.OpenAI {
		result = append(result, GlobalModelMapping{Protocol: ProtocolOpenAIChat, ClientModel: clientModel, LogicalModel: logicalModel})
	}
	for clientModel, logicalModel := range set.Anthropic {
		result = append(result, GlobalModelMapping{Protocol: ProtocolAnthropicMessages, ClientModel: clientModel, LogicalModel: logicalModel})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Protocol != result[right].Protocol {
			return result[left].Protocol < result[right].Protocol
		}
		return result[left].ClientModel < result[right].ClientModel
	})
	return result
}

func saveAuthTokensTx(transaction *sql.Tx, tokens AuthTokens) error {
	record := persistedAuthTokens{AdminHash: tokens.AdminHash, ProxyHash: tokens.ProxyHash, AdminCustom: tokens.AdminCustom, ProxyCustom: tokens.ProxyCustom}
	if record.AdminHash == "" || record.ProxyHash == "" {
		return fmt.Errorf("鉴权令牌配置不完整")
	}
	value, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化鉴权令牌失败: %w", err)
	}
	if _, err := transaction.Exec(`INSERT INTO app_settings(key, value_json, updated_at) VALUES ('auth.tokens', ?, ?) ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(value), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("保存鉴权令牌失败: %w", err)
	}
	return nil
}

func validateConfiguration(channels []Channel, channelModels map[string][]string, globalMappings []GlobalModelMapping, channelMappings []ChannelModelMapping, policies []ProbePolicy) error {
	channelProtocols := make(map[string]Protocol, len(channels))
	for _, channel := range channels {
		if strings.TrimSpace(channel.ID) == "" || strings.TrimSpace(channel.Name) == "" {
			return errors.New("渠道 ID 和名称不能为空")
		}
		if _, exists := channelProtocols[channel.ID]; exists {
			return fmt.Errorf("渠道 ID 重复: %s", channel.ID)
		}
		if err := validateProtocol(channel.Protocol); err != nil {
			return err
		}
		parsed, err := url.ParseRequestURI(channel.BaseURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("渠道 Base URL 必须是绝对且不含用户信息的 HTTP(S) 地址")
		}
		if channel.AdminState != "enabled" && channel.AdminState != "disabled" {
			return fmt.Errorf("人工状态无效: %s", channel.AdminState)
		}
		if channel.ReasoningEffort != "" && !config.IsValidReasoningEffort(channel.ReasoningEffort) {
			return fmt.Errorf("渠道思考等级无效: %s", channel.ID)
		}
		channelProtocols[channel.ID] = channel.Protocol
	}
	for channelID, models := range channelModels {
		if _, exists := channelProtocols[channelID]; !exists {
			return fmt.Errorf("渠道模型目录的渠道不存在: %s", channelID)
		}
		if _, err := normalizeChannelModels(models); err != nil {
			return fmt.Errorf("渠道模型目录无效: %s: %w", channelID, err)
		}
	}
	globalKeys := make(map[string]struct{}, len(globalMappings))
	for _, mapping := range globalMappings {
		if err := validateMapping(mapping); err != nil {
			return err
		}
		key := string(mapping.Protocol) + "\x00" + mapping.ClientModel
		if _, exists := globalKeys[key]; exists {
			return fmt.Errorf("全局模型映射重复: %s/%s", mapping.Protocol, mapping.ClientModel)
		}
		globalKeys[key] = struct{}{}
	}
	channelMappingKeys := make(map[string]struct{}, len(channelMappings))
	for _, mapping := range channelMappings {
		if err := validateChannelMappingShape(mapping); err != nil {
			return err
		}
		protocol, exists := channelProtocols[mapping.ChannelID]
		if !exists {
			return fmt.Errorf("渠道模型映射渠道不存在: %s", mapping.ChannelID)
		}
		if protocol != mapping.Protocol {
			return fmt.Errorf("渠道模型映射协议与渠道不一致: %s", mapping.ChannelID)
		}
		key := mapping.ChannelID + "\x00" + string(mapping.Protocol) + "\x00" + mapping.LogicalModel
		if _, exists := channelMappingKeys[key]; exists {
			return fmt.Errorf("渠道模型映射重复: %s/%s", mapping.ChannelID, mapping.LogicalModel)
		}
		channelMappingKeys[key] = struct{}{}
	}
	policyChannels := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if _, exists := channelProtocols[policy.ChannelID]; !exists {
			return fmt.Errorf("探针策略渠道不存在: %s", policy.ChannelID)
		}
		if _, exists := policyChannels[policy.ChannelID]; exists {
			return fmt.Errorf("探针策略重复: %s", policy.ChannelID)
		}
		if err := validateProbePolicy(policy); err != nil {
			return err
		}
		policyChannels[policy.ChannelID] = struct{}{}
	}
	return nil
}

func validateChannelMappingShape(mapping ChannelModelMapping) error {
	if err := validateProtocol(mapping.Protocol); err != nil {
		return err
	}
	if strings.TrimSpace(mapping.ChannelID) == "" || strings.TrimSpace(mapping.LogicalModel) == "" || strings.TrimSpace(mapping.UpstreamModel) == "" {
		return errors.New("渠道模型映射字段不能为空")
	}
	return nil
}

func validateGlobalMappingReference(transaction *sql.Tx, mapping GlobalModelMapping) error {
	return validateMapping(mapping)
}

func validateChannelMappingReference(transaction *sql.Tx, mapping ChannelModelMapping) error {
	var protocol Protocol
	err := transaction.QueryRow(`SELECT protocol FROM channels WHERE id = ?`, mapping.ChannelID).Scan(&protocol)
	if err == sql.ErrNoRows {
		return fmt.Errorf("渠道模型映射渠道不存在: %s", mapping.ChannelID)
	}
	if err != nil {
		return fmt.Errorf("校验渠道模型映射渠道失败: %w", err)
	}
	if protocol != mapping.Protocol {
		return fmt.Errorf("渠道模型映射协议与渠道不一致: %s", mapping.ChannelID)
	}
	return nil
}

func validateProbePolicyReference(transaction *sql.Tx, policy ProbePolicy) error {
	var exists int
	err := transaction.QueryRow(`SELECT 1 FROM channels WHERE id = ?`, policy.ChannelID).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("探针策略渠道不存在: %s", policy.ChannelID)
	}
	if err != nil {
		return fmt.Errorf("校验探针策略渠道失败: %w", err)
	}
	return nil
}

