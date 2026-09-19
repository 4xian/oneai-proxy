package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ChannelDirectorySummary 汇总渠道主页需要的运行与配置状态。
type ChannelDirectorySummary struct {
	Total              int `json:"total"`
	Available          int `json:"available"`
	Attention          int `json:"attention"`
	ModelMappings      int `json:"modelMappings"`
	Protocols          int `json:"protocols"`
	MappedChannels     int `json:"mappedChannels"`
	Cooldown           int `json:"cooldown"`
	Degraded           int `json:"degraded"`
	HalfOpen           int `json:"halfOpen"`
	Disabled           int `json:"disabled"`
	AdminDisabled      int `json:"adminDisabled"`
	AutoDisabled       int `json:"autoDisabled"`
	MissingCredentials int `json:"missingCredentials"`
	MissingModels      int `json:"missingModels"`
}

// ChannelDirectoryEntry 表示渠道列表的一条非敏感聚合记录。
type ChannelDirectoryEntry struct {
	ID                     string      `json:"id"`
	Name                   string      `json:"name"`
	Note                   string      `json:"note,omitempty"`
	Group                  string      `json:"group,omitempty"`
	Protocol               Protocol    `json:"protocol"`
	BaseURL                string      `json:"baseUrl"`
	Capabilities           []string    `json:"capabilities"`
	AdminState             string      `json:"adminState"`
	HealthState            string      `json:"healthState"`
	CredentialConfigured   bool        `json:"credentialConfigured"`
	FailureAction          string      `json:"failureAction"`
	FailureThreshold       int         `json:"failureThreshold"`
	InFlight               int         `json:"inFlight"`
	ConcurrencyLimit       int         `json:"concurrencyLimit"`
	RequestTimeoutMs       int         `json:"requestTimeoutMs"`
	StreamIdleTimeoutMs    int         `json:"streamIdleTimeoutMs"`
	CooldownSeconds        int         `json:"cooldownSeconds"`
	Priority               int         `json:"priority"`
	FallbackModel          string      `json:"fallbackModel,omitempty"`
	ReasoningEffort        string      `json:"reasoningEffort"`
	ServiceTierPassthrough bool        `json:"serviceTierPassthrough"`
	ModelCount             int         `json:"modelCount"`
	HasRoutableModel       bool        `json:"hasRoutableModel"`
	LogicalModels          []string    `json:"logicalModels"`
	TestModel              string      `json:"testModel,omitempty"`
	ProbePolicy            ProbePolicy `json:"probePolicy"`
	LatestProbe            *ProbeRun   `json:"latestProbe,omitempty"`
	FailureCount           int         `json:"failureCount"`
	CooldownUntil          string      `json:"cooldownUntil,omitempty"`
}

// ChannelDirectorySnapshot 是渠道主页一次读取所需的完整目录快照。
type ChannelDirectorySnapshot struct {
	Summary  ChannelDirectorySummary `json:"summary"`
	Channels []ChannelDirectoryEntry `json:"channels"`
}

type channelHealthRuntime struct {
	FailureCount  int
	CooldownUntil string
	LatestProbe   *ProbeRun
}

// ListChannelDirectory 聚合渠道、模型、探针和健康运行态，供渠道主页一次读取。
func ListChannelDirectory(database *sql.DB) (ChannelDirectorySnapshot, error) {
	channels, err := ListChannels(database)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}
	mappings, err := ListChannelModelMappings(database)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}
	models, err := ListAllChannelModels(database)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}
	policies, err := ListProbePolicies(database)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}
	runtimes, err := listChannelHealthRuntimes(database)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}

	mappingsByChannel := make(map[string][]ChannelModelMapping, len(channels))
	for _, mapping := range mappings {
		mappingsByChannel[mapping.ChannelID] = append(mappingsByChannel[mapping.ChannelID], mapping)
	}
	modelsByChannel := make(map[string][]string, len(channels))
	for _, model := range models {
		modelsByChannel[model.ChannelID] = append(modelsByChannel[model.ChannelID], model.Model)
	}
	policiesByChannel := make(map[string]ProbePolicy, len(policies))
	for _, policy := range policies {
		policiesByChannel[policy.ChannelID] = policy
	}
	protocols := make(map[Protocol]struct{})
	mappedChannels := make(map[string]struct{})
	entries := make([]ChannelDirectoryEntry, 0, len(channels))
	summary := ChannelDirectorySummary{Total: len(channels), ModelMappings: len(mappings)}

	for _, channel := range channels {
		protocols[channel.Protocol] = struct{}{}
		channelMappings := mappingsByChannel[channel.ID]
		logicalModels := make([]string, 0, len(modelsByChannel[channel.ID]))
		logicalModels = append(logicalModels, modelsByChannel[channel.ID]...)
		sort.Strings(logicalModels)
		capabilities := make([]string, 0, len(channel.Capabilities))
		capabilities = append(capabilities, channel.Capabilities...)
		if len(channelMappings) > 0 {
			mappedChannels[channel.ID] = struct{}{}
		}
		policy, exists := policiesByChannel[channel.ID]
		if !exists {
			policy = normalizeProbePolicy(ProbePolicy{ChannelID: channel.ID, Mode: "connectivity"})
		}
		runtime := runtimes[channel.ID]
		routableIDs := uniqueChannelModelIDs(logicalModels, channelMappings, channel.FallbackModel)
		hasModel := len(routableIDs) > 0
		entry := ChannelDirectoryEntry{
			ID:                     channel.ID,
			Name:                   channel.Name,
			Note:                   channel.Note,
			Group:                  channel.Group,
			Protocol:               channel.Protocol,
			BaseURL:                channel.BaseURL,
			Capabilities:           capabilities,
			AdminState:             channel.AdminState,
			HealthState:            channel.HealthState,
			CredentialConfigured:   channel.CredentialConfigured,
			FailureAction:          channel.FailureAction,
			FailureThreshold:       channel.FailureThreshold,
			ConcurrencyLimit:       channel.ConcurrencyLimit,
			RequestTimeoutMs:       channel.RequestTimeoutMs,
			StreamIdleTimeoutMs:    channel.StreamIdleTimeoutMs,
			CooldownSeconds:        channel.CooldownSeconds,
			Priority:               channel.Priority,
			FallbackModel:          channel.FallbackModel,
			ReasoningEffort:        channel.ReasoningEffort,
			ServiceTierPassthrough: channel.ServiceTierPassthrough,
			ModelCount:             len(routableIDs),
			HasRoutableModel:       hasModel,
			LogicalModels:          logicalModels,
			ProbePolicy:            policy,
			FailureCount:           runtime.FailureCount,
			CooldownUntil:          runtime.CooldownUntil,
		}
		if len(logicalModels) > 0 {
			entry.TestModel = logicalModels[0]
		} else if len(channelMappings) > 0 {
			entry.TestModel = channelMappings[0].UpstreamModel
		} else if strings.TrimSpace(channel.FallbackModel) != "" {
			entry.TestModel = strings.TrimSpace(channel.FallbackModel)
		}
		entry.LatestProbe = runtime.LatestProbe
		entries = append(entries, entry)

		available := channel.AdminState == "enabled" && channel.HealthState == "healthy" && channel.CredentialConfigured && hasModel
		if available {
			summary.Available++
		}
		if channel.HealthState == "cooldown" {
			summary.Cooldown++
		}
		if channel.HealthState == "degraded" {
			summary.Degraded++
		}
		if channel.HealthState == "half_open" {
			summary.HalfOpen++
		}
		if channel.AdminState == "disabled" {
			summary.AdminDisabled++
			summary.Disabled++
		}
		if channel.HealthState == "auto_disabled" {
			summary.AutoDisabled++
			if channel.AdminState != "disabled" {
				summary.Disabled++
			}
		}
		if !channel.CredentialConfigured {
			summary.MissingCredentials++
		}
		if !hasModel {
			summary.MissingModels++
		}
	}

	summary.Attention = summary.Total - summary.Available
	summary.Protocols = len(protocols)
	summary.MappedChannels = len(mappedChannels)
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].Priority > entries[right].Priority
	})
	return ChannelDirectorySnapshot{Summary: summary, Channels: entries}, nil
}

// uniqueChannelModelIDs 返回独立模型目录、映射上游模型和非空兜底模型去重后的标识。
func uniqueChannelModelIDs(directoryModels []string, mappings []ChannelModelMapping, fallback string) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	for _, model := range directoryModels {
		add(model)
	}
	for _, mapping := range mappings {
		add(mapping.UpstreamModel)
	}
	add(fallback)
	return ids
}

// UpdateChannelAdminState 只修改渠道的人工启停状态，不覆盖其他配置。
func UpdateChannelAdminState(database *sql.DB, channelID, adminState string) (Channel, error) {
	channelID = strings.TrimSpace(channelID)
	adminState = strings.TrimSpace(adminState)
	if channelID == "" {
		return Channel{}, fmt.Errorf("渠道 ID 不能为空")
	}
	if adminState != "enabled" && adminState != "disabled" {
		return Channel{}, fmt.Errorf("人工状态必须是 enabled 或 disabled")
	}
	transaction, err := database.Begin()
	if err != nil {
		return Channel{}, fmt.Errorf("开始更新渠道人工状态事务失败: %w", err)
	}
	defer transaction.Rollback()
	var previousState, secretRef string
	if err := transaction.QueryRow(`SELECT admin_state, secret_ref FROM channels WHERE id = ?`, channelID).Scan(&previousState, &secretRef); errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	} else if err != nil {
		return Channel{}, fmt.Errorf("读取渠道人工状态和凭证失败: %w", err)
	}
	if adminState == "enabled" && strings.TrimSpace(secretRef) == "" {
		return Channel{}, fmt.Errorf("渠道未配置凭证，不能启用")
	}
	result, err := transaction.Exec(`UPDATE channels SET admin_state = ?, health_version = health_version + CASE WHEN admin_state <> ? THEN 1 ELSE 0 END, updated_at = ? WHERE id = ?`, adminState, adminState, nowUTC(), channelID)
	if err != nil {
		return Channel{}, fmt.Errorf("更新渠道人工状态失败: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return Channel{}, ErrNotFound
	}
	if previousState != adminState {
		if _, err := transaction.Exec(`UPDATE health_runtime SET half_open_claimed = 0 WHERE channel_id = ?`, channelID); err != nil {
			return Channel{}, fmt.Errorf("释放失效半开名额失败: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return Channel{}, fmt.Errorf("提交渠道人工状态事务失败: %w", err)
	}
	return GetChannel(database, channelID)
}

func listChannelHealthRuntimes(database *sql.DB) (map[string]channelHealthRuntime, error) {
	rows, err := database.Query(`SELECT channel_id, failure_count, cooldown_until, last_probe_id, last_probe_mode, last_probe_status, last_probe_latency_ms, last_probe_error_class, last_probe_error_message, last_probe_occurred_at FROM health_runtime`)
	if err != nil {
		return nil, fmt.Errorf("读取渠道健康运行态失败: %w", err)
	}
	defer rows.Close()
	items := make(map[string]channelHealthRuntime)
	for rows.Next() {
		var channelID, probeID, probeMode, probeStatus, probeErrorClass, probeErrorMessage, probeOccurredAt string
		var probeLatency int64
		var runtime channelHealthRuntime
		if err := rows.Scan(&channelID, &runtime.FailureCount, &runtime.CooldownUntil, &probeID, &probeMode, &probeStatus, &probeLatency, &probeErrorClass, &probeErrorMessage, &probeOccurredAt); err != nil {
			return nil, fmt.Errorf("读取渠道健康运行态字段失败: %w", err)
		}
		if strings.TrimSpace(probeID) != "" {
			runtime.LatestProbe = &ProbeRun{
				ID:           probeID,
				ChannelID:    channelID,
				Mode:         probeMode,
				Status:       probeStatus,
				LatencyMs:    probeLatency,
				ErrorClass:   probeErrorClass,
				ErrorMessage: probeErrorMessage,
				OccurredAt:   probeOccurredAt,
			}
		}
		items[channelID] = runtime
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历渠道健康运行态失败: %w", err)
	}
	return items, nil
}
