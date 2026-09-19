package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
)

// Protocol 表示当前支持的协议入口。
type Protocol string

const (
	// ProtocolOpenAIChat 表示 OpenAI Chat Completions 协议。
	ProtocolOpenAIChat Protocol = "openai_chat"
	// ProtocolOpenAIResponses 表示 OpenAI Responses 协议。
	ProtocolOpenAIResponses Protocol = "openai_responses"
	// ProtocolAnthropicMessages 表示 Anthropic Messages 协议。
	ProtocolAnthropicMessages Protocol = "anthropic_messages"
)

// Channel 表示一个不包含明文凭证的上游渠道。
type Channel struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Note                   string            `json:"note,omitempty"`
	Group                  string            `json:"group,omitempty"`
	Protocol               Protocol          `json:"protocol"`
	BaseURL                string            `json:"baseUrl"`
	Capabilities           []string          `json:"capabilities"`
	AdminState             string            `json:"adminState"`
	HealthState            string            `json:"healthState"`
	HealthVersion          int64             `json:"healthVersion"`
	CreatedAt              string            `json:"createdAt"`
	UpdatedAt              string            `json:"updatedAt"`
	SecretRef              string            `json:"-"`
	CredentialConfigured   bool              `json:"credentialConfigured"`
	FailureAction          string            `json:"failureAction"`
	FailureThreshold       int               `json:"failureThreshold"`
	CustomHeaders          map[string]string `json:"customHeaders,omitempty"`
	ConcurrencyLimit       int               `json:"concurrencyLimit"`
	RequestTimeoutMs       int               `json:"requestTimeoutMs"`
	StreamIdleTimeoutMs    int               `json:"streamIdleTimeoutMs"`
	CooldownSeconds        int               `json:"cooldownSeconds"`
	Priority               int               `json:"priority"`
	FallbackModel          string            `json:"fallbackModel,omitempty"`
	ReasoningEffort        string            `json:"reasoningEffort"`
	ServiceTierPassthrough bool              `json:"serviceTierPassthrough"`
}

// GlobalModelMapping 表示协议内客户端模型到逻辑模型的映射。
type GlobalModelMapping struct {
	Protocol     Protocol `json:"protocol"`
	ClientModel  string   `json:"clientModel"`
	LogicalModel string   `json:"logicalModel"`
}

// GlobalModelMappingSet 保存两个协议族的单层模型映射。
type GlobalModelMappingSet struct {
	OpenAI    map[string]string `json:"openai"`
	Anthropic map[string]string `json:"anthropic"`
}

// ChannelModelMapping 表示渠道内逻辑模型到实际模型的映射。
type ChannelModelMapping struct {
	ChannelID     string   `json:"channelId"`
	Protocol      Protocol `json:"protocol"`
	LogicalModel  string   `json:"logicalModel"`
	UpstreamModel string   `json:"upstreamModel"`
}

// ChannelModel 表示渠道声明可直接使用的一个上游模型。
type ChannelModel struct {
	ChannelID string `json:"channelId"`
	Model     string `json:"model"`
}

// ProbePolicy 表示渠道的探针开关和恢复阈值。
type ProbePolicy struct {
	ChannelID                string `json:"channelId"`
	Enabled                  bool   `json:"enabled"`
	Mode                     string `json:"mode"`
	IntervalSeconds          int    `json:"intervalSeconds"`
	Model                    string `json:"model,omitempty"`
	Path                     string `json:"path,omitempty"`
	FailureThreshold         int    `json:"failureThreshold"`
	RequestTimeoutMs         int    `json:"requestTimeoutMs"`
	AutoRecover              bool   `json:"autoRecover"`
	RecoverySuccessThreshold int    `json:"recoverySuccessThreshold"`
}

// RouteSimulationInput 是真实选路使用的协议、客户端模型和能力。
type RouteSimulationInput struct {
	Protocol     Protocol `json:"protocol"`
	ClientModel  string   `json:"clientModel"`
	Capabilities []string `json:"capabilities"`
}

// RouteTarget 是一次真实转发可使用的渠道目标，不包含秘密明文。
type RouteTarget struct {
	ChannelID              string
	ChannelName            string
	GroupName              string
	Protocol               Protocol
	Priority               int
	BaseURL                string
	SecretRef              string
	UpstreamModel          string
	LogicalModel           string
	MappingSource          string
	FallbackModel          string
	ReasoningEffort        string
	ServiceTierPassthrough bool
	CustomHeaders          map[string]string
	InvalidCustomHeaders   bool
	ConcurrencyLimit       int
	RequestTimeoutMs       int
	StreamIdleTimeoutMs    int
	CooldownSeconds        int
}

// SaveResponseAffinity 保存 Responses 响应 ID 与渠道的短期亲和关系。
func SaveResponseAffinity(database *sql.DB, responseID, channelID string, expiresAt time.Time) error {
	responseID = strings.TrimSpace(responseID)
	channelID = strings.TrimSpace(channelID)
	if responseID == "" || channelID == "" {
		return errors.New("Responses 亲和关系缺少响应或渠道 ID")
	}
	if _, err := database.Exec(`
INSERT INTO response_affinity(response_id, channel_id, expires_at) VALUES (?, ?, ?)
ON CONFLICT(response_id) DO UPDATE SET channel_id = excluded.channel_id, expires_at = excluded.expires_at`,
		responseID, channelID, expiresAt.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("保存 Responses 亲和关系失败: %w", err)
	}
	return nil
}

// GetResponseAffinity 读取未过期的 Responses 亲和渠道，过期记录会被清理。
func GetResponseAffinity(database *sql.DB, responseID string, now time.Time) (string, error) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return "", errors.New("Responses 响应 ID 不能为空")
	}
	var channelID, expiresAt string
	err := database.QueryRow(`SELECT channel_id, expires_at FROM response_affinity WHERE response_id = ?`, responseID).Scan(&channelID, &expiresAt)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取 Responses 亲和关系失败: %w", err)
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || !expires.After(now.UTC()) {
		if _, deleteErr := database.Exec(`DELETE FROM response_affinity WHERE response_id = ?`, responseID); deleteErr != nil {
			return "", fmt.Errorf("清理过期 Responses 亲和关系失败: %w", deleteErr)
		}
		return "", ErrNotFound
	}
	return channelID, nil
}

// ResolveRouteAffinity 只返回 Responses 亲和渠道，避免把未知响应 ID 静默发送到其他渠道。
func ResolveRouteAffinity(database *sql.DB, input RouteSimulationInput, channelID string) ([]RouteTarget, error) {
	targets, err := ResolveRoute(database, input)
	if err != nil {
		if errors.Is(err, ErrNoAvailableRoute) {
			return nil, fmt.Errorf("%w: %s", ErrAffinityChannelUnavailable, channelID)
		}
		return nil, err
	}
	for _, target := range targets {
		if target.ChannelID == channelID {
			return []RouteTarget{target}, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrAffinityChannelUnavailable, channelID)
}

// ResolveRouteTarget 重新读取指定渠道的当前路由目标，避免使用过期的候选快照。
func ResolveRouteTarget(database *sql.DB, input RouteSimulationInput, channelID string) (RouteTarget, error) {
	targets, err := ResolveRoute(database, input)
	if err != nil {
		if errors.Is(err, ErrNoAvailableRoute) {
			return RouteTarget{}, ErrNotFound
		}
		return RouteTarget{}, err
	}
	for _, target := range targets {
		if target.ChannelID == channelID {
			return target, nil
		}
	}
	return RouteTarget{}, ErrNotFound
}

// ResolveRoute 按稳定顺序返回可用于真实转发的渠道目标。
func ResolveRoute(database *sql.DB, input RouteSimulationInput) ([]RouteTarget, error) {
	if err := validateProtocol(input.Protocol); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.ClientModel) == "" {
		return nil, errors.New("客户端模型不能为空")
	}
	logicalModel := input.ClientModel
	// 全局映射只是后备逻辑模型，渠道自身的同名映射优先于它。
	if err := database.QueryRow(`SELECT logical_model FROM global_model_mappings WHERE protocol_family = ? AND client_model = ?`, protocolFamily(input.Protocol), input.ClientModel).Scan(&logicalModel); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: 读取全局模型映射失败: %w", ErrRouteStorage, err)
	}
	rows, err := database.Query(`
	SELECT c.id, c.name, c.group_name, c.protocol, c.base_url, c.secret_ref, c.admin_state, c.health_state, c.capabilities_json, c.custom_headers_json,
	       c.concurrency_limit, c.request_timeout_ms, c.stream_idle_timeout_ms, c.cooldown_seconds, c.priority, c.fallback_model, c.reasoning_effort, c.service_tier_passthrough,
		       COALESCE((SELECT upstream_model FROM channel_model_mappings WHERE channel_id = c.id AND protocol = c.protocol AND logical_model = ?), ''),
		       COALESCE((SELECT model FROM channel_models WHERE channel_id = c.id AND model = ?), ''),
		       COALESCE((SELECT upstream_model FROM channel_model_mappings WHERE channel_id = c.id AND protocol = c.protocol AND logical_model = ?), ''),
		       COALESCE((SELECT model FROM channel_models WHERE channel_id = c.id AND model = ?), '')
	FROM channels c
	WHERE c.protocol = ?
	ORDER BY c.priority DESC, c.created_at ASC, c.id ASC`, input.ClientModel, input.ClientModel, logicalModel, logicalModel, input.Protocol)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取路由候选失败: %w", ErrRouteStorage, err)
	}
	defer rows.Close()
	targets := make([]RouteTarget, 0)
	for rows.Next() {
		var target RouteTarget
		var adminState, healthState, capabilitiesJSON, headersJSON string
		var directMapping, directModel, logicalMapping, logicalModelUpstream string
		var serviceTierPassthrough int
		if err := rows.Scan(&target.ChannelID, &target.ChannelName, &target.GroupName, &target.Protocol, &target.BaseURL, &target.SecretRef, &adminState, &healthState, &capabilitiesJSON, &headersJSON, &target.ConcurrencyLimit, &target.RequestTimeoutMs, &target.StreamIdleTimeoutMs, &target.CooldownSeconds, &target.Priority, &target.FallbackModel, &target.ReasoningEffort, &serviceTierPassthrough, &directMapping, &directModel, &logicalMapping, &logicalModelUpstream); err != nil {
			return nil, fmt.Errorf("%w: 读取路由候选字段失败: %w", ErrRouteStorage, err)
		}
		target.ServiceTierPassthrough = serviceTierPassthrough == 1
		if target.ReasoningEffort == "" {
			target.ReasoningEffort = config.ReasoningEffortPassthrough
		}
		var capabilities []string
		if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
			return nil, fmt.Errorf("%w: 解析渠道能力失败: %w", ErrRouteStorage, err)
		}
		if strings.TrimSpace(headersJSON) != "" {
			if err := json.Unmarshal([]byte(headersJSON), &target.CustomHeaders); err != nil {
				target.CustomHeaders = nil
				target.InvalidCustomHeaders = true
			}
		}
		if strings.TrimSpace(target.SecretRef) == "" {
			continue
		}
		if !capabilitiesMatch(capabilities, input.Capabilities) {
			continue
		}
		switch {
		case strings.TrimSpace(directMapping) != "":
			target.UpstreamModel, target.MappingSource = strings.TrimSpace(directMapping), "channel"
		case strings.TrimSpace(directModel) != "":
			target.UpstreamModel, target.MappingSource = strings.TrimSpace(directModel), "channel"
		case strings.TrimSpace(logicalMapping) != "":
			target.UpstreamModel, target.MappingSource = strings.TrimSpace(logicalMapping), "channel"
		case strings.TrimSpace(logicalModelUpstream) != "":
			target.UpstreamModel, target.MappingSource = strings.TrimSpace(logicalModelUpstream), "channel"
		default:
			target.UpstreamModel, target.MappingSource = strings.TrimSpace(target.FallbackModel), "fallback"
		}
		if target.UpstreamModel == "" {
			continue
		}
		target.LogicalModel = logicalModel
		target = normalizeRouteTarget(target)
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: 遍历路由候选失败: %w", ErrRouteStorage, err)
	}
	if len(targets) == 0 {
		return nil, ErrNoAvailableRoute
	}
	return targets, nil
}

// ErrNoAvailableRoute 表示当前没有匹配协议、模型和能力的路由目标。
var ErrNoAvailableRoute = errors.New("没有可用路由候选")

// ErrRouteStorage 表示读取路由配置时发生本地存储错误。
var ErrRouteStorage = errors.New("路由配置存储不可用")

// ErrAffinityChannelUnavailable 表示 Responses 亲和渠道已无法承接本次请求。
var ErrAffinityChannelUnavailable = errors.New("Responses 亲和渠道不可用")

// ErrNotFound 表示请求的配置记录不存在。
var ErrNotFound = errors.New("记录不存在")

// ErrChannelHasHistory 表示渠道仍被请求账本引用，不能物理删除。
var ErrChannelHasHistory = errors.New("渠道仍有请求记录，不能删除")

var copyNameSuffix = regexp.MustCompile(`\s+副本(?:\s+\d+)?$`)

// NextChannelCopyName 生成「原名 副本」，重名时递增为「原名 副本 2」起。
func NextChannelCopyName(database *sql.DB, sourceName string) (string, error) {
	channels, err := ListChannels(database)
	if err != nil {
		return "", err
	}
	used := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		used[channel.Name] = struct{}{}
	}
	return nextChannelCopyName(sourceName, used), nil
}

// nextChannelCopyName 按已占用名称生成下一个可用副本名。
func nextChannelCopyName(sourceName string, used map[string]struct{}) string {
	base := copyNameBase(sourceName)
	candidate := base + " 副本"
	if _, exists := used[candidate]; !exists {
		return candidate
	}
	for index := 2; ; index++ {
		candidate = fmt.Sprintf("%s 副本 %d", base, index)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

// copyNameBase 去掉名称末尾已有的「副本」序号，避免再叠一层。
func copyNameBase(name string) string {
	name = strings.TrimSpace(name)
	for {
		trimmed := strings.TrimSpace(copyNameSuffix.ReplaceAllString(name, ""))
		if trimmed == name || trimmed == "" {
			return name
		}
		name = trimmed
	}
}

// ListChannels 返回所有渠道，按创建时间和 ID 稳定排序。
func ListChannels(database *sql.DB) ([]Channel, error) {
	rows, err := database.Query(`SELECT id, name, note, group_name, protocol, base_url, capabilities_json, admin_state, health_state, health_version, created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json, concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, priority, fallback_model, reasoning_effort, service_tier_passthrough FROM channels ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("读取渠道失败: %w", err)
	}
	defer rows.Close()
	channels := make([]Channel, 0)
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历渠道失败: %w", err)
	}
	return channels, nil
}

// GetChannel 按 ID 读取单个渠道及其内部秘密引用。
func GetChannel(database *sql.DB, id string) (Channel, error) {
	row := database.QueryRow(`SELECT id, name, note, group_name, protocol, base_url, capabilities_json, admin_state, health_state, health_version, created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json, concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, priority, fallback_model, reasoning_effort, service_tier_passthrough FROM channels WHERE id = ?`, id)
	channel, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	return channel, err
}

// SaveChannelBundle 在一个事务内保存渠道、渠道模型映射和探针策略。
func SaveChannelBundle(database *sql.DB, channel Channel, models []string, mappings []ChannelModelMapping, policy ProbePolicy) (Channel, error) {
	existing, lookupErr := GetChannel(database, channel.ID)
	isCreate := errors.Is(lookupErr, ErrNotFound)
	if lookupErr != nil && !isCreate {
		return Channel{}, lookupErr
	}
	if strings.TrimSpace(channel.ID) == "" {
		id, err := nextChannelID(database)
		if err != nil {
			return Channel{}, err
		}
		channel.ID = id
		isCreate = true
	}
	existingPolicy := ProbePolicy{}
	if !isCreate {
		existingPolicy, lookupErr = GetProbePolicy(database, channel.ID)
		if lookupErr != nil {
			return Channel{}, lookupErr
		}
	}
	if !isCreate {
		if channel.CreatedAt == "" {
			channel.CreatedAt = existing.CreatedAt
		}
		if channel.HealthState == "" {
			channel.HealthState = existing.HealthState
		}
		if channel.SecretRef == "" {
			channel.SecretRef = existing.SecretRef
		}
	}
	channel.AdminState = defaultString(channel.AdminState, "enabled")
	channel.HealthState = defaultString(channel.HealthState, "healthy")
	channel.FailureAction = defaultString(channel.FailureAction, "cooldown")
	if channel.FailureThreshold <= 0 {
		channel.FailureThreshold = 3
	}
	channel = normalizeChannelRuntime(channel)
	if !isCreate {
		channel.HealthVersion = existing.HealthVersion
		if channelHealthSemanticsChanged(existing, channel) {
			channel.HealthVersion++
		}
	}
	if err := validateChannel(channel); err != nil {
		return Channel{}, err
	}
	channel.Capabilities = normalizeCapabilities(channel.Capabilities)
	models, err := normalizeChannelModels(models)
	if err != nil {
		return Channel{}, err
	}
	channel.CredentialConfigured = channel.SecretRef != ""
	if channel.CreatedAt == "" {
		channel.CreatedAt = nowUTC()
	}
	channel.UpdatedAt = nowUTC()
	policy.ChannelID = channel.ID
	policy = normalizeProbePolicy(policy)
	policyChanged := isCreate || probePolicySemanticsChanged(existingPolicy, policy)
	if !isCreate && policyChanged && channel.HealthVersion == existing.HealthVersion {
		channel.HealthVersion++
	}
	if err := validateProbePolicy(policy); err != nil {
		return Channel{}, err
	}
	for _, mapping := range mappings {
		if mapping.ChannelID == "" {
			mapping.ChannelID = channel.ID
		}
		if mapping.ChannelID != channel.ID || mapping.Protocol != channel.Protocol {
			return Channel{}, fmt.Errorf("渠道模型映射协议或渠道不一致: %s", mapping.LogicalModel)
		}
		if err := validateChannelMapping(mapping); err != nil {
			return Channel{}, err
		}
	}
	transaction, err := database.Begin()
	if err != nil {
		return Channel{}, fmt.Errorf("开始保存渠道事务失败: %w", err)
	}
	rollback := func(saveErr error) (Channel, error) {
		_ = transaction.Rollback()
		return Channel{}, saveErr
	}
	capabilities, _ := json.Marshal(channel.Capabilities)
	headers, _ := json.Marshal(channel.CustomHeaders)
	if isCreate {
		if _, err := transaction.Exec(`INSERT INTO channels(id, name, note, group_name, protocol, base_url, capabilities_json, admin_state, health_state, health_version, created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json, concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, priority, fallback_model, reasoning_effort, service_tier_passthrough) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, channel.ID, channel.Name, channel.Note, channel.Group, channel.Protocol, channel.BaseURL, string(capabilities), channel.AdminState, channel.HealthState, channel.HealthVersion, channel.CreatedAt, channel.UpdatedAt, channel.SecretRef, channel.FailureAction, channel.FailureThreshold, string(headers), channel.ConcurrencyLimit, channel.RequestTimeoutMs, channel.StreamIdleTimeoutMs, channel.CooldownSeconds, channel.Priority, channel.FallbackModel, channel.ReasoningEffort, boolInt(channel.ServiceTierPassthrough)); err != nil {
			return rollback(fmt.Errorf("创建渠道失败: %w", err))
		}
	} else if result, err := transaction.Exec(`UPDATE channels SET name = ?, note = ?, group_name = ?, protocol = ?, base_url = ?, capabilities_json = ?, admin_state = ?, secret_ref = ?, failure_action = ?, failure_threshold = ?, custom_headers_json = ?, concurrency_limit = ?, request_timeout_ms = ?, stream_idle_timeout_ms = ?, cooldown_seconds = ?, priority = ?, fallback_model = ?, reasoning_effort = ?, service_tier_passthrough = ?, health_version = ?, updated_at = ? WHERE id = ? AND health_version = ?`, channel.Name, channel.Note, channel.Group, channel.Protocol, channel.BaseURL, string(capabilities), channel.AdminState, channel.SecretRef, channel.FailureAction, channel.FailureThreshold, string(headers), channel.ConcurrencyLimit, channel.RequestTimeoutMs, channel.StreamIdleTimeoutMs, channel.CooldownSeconds, channel.Priority, channel.FallbackModel, channel.ReasoningEffort, boolInt(channel.ServiceTierPassthrough), channel.HealthVersion, channel.UpdatedAt, channel.ID, existing.HealthVersion); err != nil {
		return rollback(fmt.Errorf("更新渠道失败: %w", err))
	} else if count, _ := result.RowsAffected(); count == 0 {
		return rollback(fmt.Errorf("渠道配置已被并发更新，请重试"))
	}
	if !isCreate && (channel.HealthVersion != existing.HealthVersion || policyChanged) {
		if _, err := transaction.Exec(`UPDATE health_runtime SET probe_failure_count = 0, probe_success_count = 0, half_open_claimed = 0 WHERE channel_id = ?`, channel.ID); err != nil {
			return rollback(fmt.Errorf("重置失效探针恢复状态失败: %w", err))
		}
	}
	if _, err := transaction.Exec(`DELETE FROM channel_models WHERE channel_id = ?`, channel.ID); err != nil {
		return rollback(fmt.Errorf("清理渠道模型目录失败: %w", err))
	}
	for _, model := range models {
		if _, err := transaction.Exec(`INSERT INTO channel_models(channel_id, model) VALUES (?, ?)`, channel.ID, model); err != nil {
			return rollback(fmt.Errorf("保存渠道模型目录失败: %w", err))
		}
	}
	if _, err := transaction.Exec(`DELETE FROM channel_model_mappings WHERE channel_id = ?`, channel.ID); err != nil {
		return rollback(fmt.Errorf("清理渠道模型映射失败: %w", err))
	}
	for _, mapping := range mappings {
		if _, err := transaction.Exec(`INSERT INTO channel_model_mappings(channel_id, protocol, logical_model, upstream_model) VALUES (?, ?, ?, ?)`, channel.ID, channel.Protocol, mapping.LogicalModel, mapping.UpstreamModel); err != nil {
			return rollback(fmt.Errorf("保存渠道模型映射失败: %w", err))
		}
	}
	if _, err := transaction.Exec(`DELETE FROM probe_policies WHERE channel_id = ?`, channel.ID); err != nil {
		return rollback(fmt.Errorf("清理渠道探针策略失败: %w", err))
	}
	enabled, autoRecover := boolInt(policy.Enabled), boolInt(policy.AutoRecover)
	if _, err := transaction.Exec(`INSERT INTO probe_policies(channel_id, enabled, mode, interval_seconds, model, path, failure_threshold, request_timeout_ms, auto_recover, recovery_success_threshold) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, channel.ID, enabled, policy.Mode, policy.IntervalSeconds, policy.Model, policy.Path, policy.FailureThreshold, policy.RequestTimeoutMs, autoRecover, policy.RecoverySuccessThreshold); err != nil {
		return rollback(fmt.Errorf("保存渠道探针策略失败: %w", err))
	}
	if isCreate {
		if _, err := transaction.Exec(`INSERT OR IGNORE INTO health_runtime(channel_id) VALUES (?)`, channel.ID); err != nil {
			return rollback(fmt.Errorf("初始化渠道健康运行态失败: %w", err))
		}
	}
	if err := transaction.Commit(); err != nil {
		return Channel{}, fmt.Errorf("提交渠道事务失败: %w", err)
	}
	return channel, nil
}

func channelHealthSemanticsChanged(previous, next Channel) bool {
	return previous.Protocol != next.Protocol || previous.BaseURL != next.BaseURL || previous.SecretRef != next.SecretRef ||
		previous.AdminState != next.AdminState || previous.FailureAction != next.FailureAction ||
		previous.FailureThreshold != next.FailureThreshold || previous.CooldownSeconds != next.CooldownSeconds ||
		previous.RequestTimeoutMs != next.RequestTimeoutMs || previous.StreamIdleTimeoutMs != next.StreamIdleTimeoutMs ||
		!customHeadersEqual(previous.CustomHeaders, next.CustomHeaders)
}

func customHeadersEqual(left, right map[string]string) bool {
	normalizedLeft, leftOK := normalizeCustomHeaders(left)
	normalizedRight, rightOK := normalizeCustomHeaders(right)
	if !leftOK || !rightOK {
		return false
	}
	if len(normalizedLeft) != len(normalizedRight) {
		return false
	}
	for name, value := range normalizedLeft {
		rightValue, exists := normalizedRight[name]
		if !exists || rightValue != value {
			return false
		}
	}
	return true
}

func normalizeCustomHeaders(headers map[string]string) (map[string]string, bool) {
	if len(headers) == 0 {
		return map[string]string{}, true
	}
	normalized := make(map[string]string, len(headers))
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" {
			return nil, false
		}
		if _, exists := normalized[canonical]; exists {
			return nil, false
		}
		normalized[canonical] = value
	}
	return normalized, true
}

func probePolicySemanticsChanged(previous, next ProbePolicy) bool {
	return previous.Enabled != next.Enabled || previous.Mode != next.Mode ||
		previous.IntervalSeconds != next.IntervalSeconds || previous.Model != next.Model ||
		previous.Path != next.Path || previous.FailureThreshold != next.FailureThreshold ||
		previous.RequestTimeoutMs != next.RequestTimeoutMs || previous.AutoRecover != next.AutoRecover ||
		previous.RecoverySuccessThreshold != next.RecoverySuccessThreshold
}

// DeleteChannel 删除没有请求账本引用的渠道及其级联配置。
func DeleteChannel(database *sql.DB, id string) error {
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始删除渠道事务失败: %w", err)
	}
	defer transaction.Rollback()
	referenced, err := channelHasHistoricalReferenceTx(transaction, id)
	if err != nil {
		return err
	}
	if referenced {
		return ErrChannelHasHistory
	}
	result, err := transaction.Exec(`DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除渠道失败: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交删除渠道事务失败: %w", err)
	}
	return nil
}

func scanChannel(scanner interface{ Scan(...any) error }) (Channel, error) {
	var channel Channel
	var capabilities, headers string
	var serviceTierPassthrough int
	if err := scanner.Scan(&channel.ID, &channel.Name, &channel.Note, &channel.Group, &channel.Protocol, &channel.BaseURL, &capabilities, &channel.AdminState, &channel.HealthState, &channel.HealthVersion, &channel.CreatedAt, &channel.UpdatedAt, &channel.SecretRef, &channel.FailureAction, &channel.FailureThreshold, &headers, &channel.ConcurrencyLimit, &channel.RequestTimeoutMs, &channel.StreamIdleTimeoutMs, &channel.CooldownSeconds, &channel.Priority, &channel.FallbackModel, &channel.ReasoningEffort, &serviceTierPassthrough); err != nil {
		return Channel{}, fmt.Errorf("读取渠道字段失败: %w", err)
	}
	channel.ServiceTierPassthrough = serviceTierPassthrough == 1
	if channel.ReasoningEffort == "" {
		channel.ReasoningEffort = config.ReasoningEffortPassthrough
	}
	if err := json.Unmarshal([]byte(capabilities), &channel.Capabilities); err != nil {
		return Channel{}, fmt.Errorf("解析渠道能力失败: %w", err)
	}
	if strings.TrimSpace(headers) != "" {
		if err := json.Unmarshal([]byte(headers), &channel.CustomHeaders); err != nil {
			return Channel{}, fmt.Errorf("解析渠道自定义请求头失败: %w", err)
		}
	}
	channel = normalizeChannelRuntime(channel)
	channel.CredentialConfigured = channel.SecretRef != ""
	return channel, nil
}

// normalizeChannelRuntime 补齐渠道运行参数的安全默认值。
func normalizeChannelRuntime(channel Channel) Channel {
	if channel.CustomHeaders == nil {
		channel.CustomHeaders = map[string]string{}
	}
	if channel.ConcurrencyLimit <= 0 {
		channel.ConcurrencyLimit = 8
	}
	if channel.RequestTimeoutMs <= 0 {
		channel.RequestTimeoutMs = 120000
	}
	if channel.StreamIdleTimeoutMs <= 0 {
		channel.StreamIdleTimeoutMs = 300000
	}
	if channel.CooldownSeconds <= 0 {
		channel.CooldownSeconds = 30
	}
	if channel.Priority < 0 {
		channel.Priority = 0
	}
	if channel.ReasoningEffort == "" {
		channel.ReasoningEffort = config.ReasoningEffortPassthrough
	}
	channel.Group = strings.TrimSpace(channel.Group)
	return channel
}

func normalizeRouteTarget(target RouteTarget) RouteTarget {
	if target.ConcurrencyLimit <= 0 {
		target.ConcurrencyLimit = 8
	}
	if target.RequestTimeoutMs <= 0 {
		target.RequestTimeoutMs = 120000
	}
	if target.StreamIdleTimeoutMs <= 0 {
		target.StreamIdleTimeoutMs = 300000
	}
	if target.CooldownSeconds <= 0 {
		target.CooldownSeconds = 30
	}
	return target
}

// nextChannelID 生成人类可读且稳定递增的渠道 ID；调用方未提供 ID 时使用。
func nextChannelID(database *sql.DB) (string, error) {
	rows, err := database.Query(`SELECT id FROM channels`)
	if err != nil {
		return "", fmt.Errorf("读取渠道 ID 失败: %w", err)
	}
	defer rows.Close()
	used := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("读取渠道 ID 字段失败: %w", err)
		}
		used[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("遍历渠道 ID 失败: %w", err)
	}
	for index := 1; ; index++ {
		candidate := fmt.Sprintf("channel-%03d", index)
		if _, exists := used[candidate]; !exists {
			return candidate, nil
		}
	}
}



func validateChannel(channel Channel) error {
	if strings.TrimSpace(channel.ID) == "" || strings.TrimSpace(channel.Name) == "" {
		return errors.New("渠道 ID 和名称不能为空")
	}
	if err := validateProtocol(channel.Protocol); err != nil {
		return err
	}
	parsed, err := url.ParseRequestURI(channel.BaseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("渠道 Base URL 必须是绝对且不含用户信息的 HTTP(S) 地址")
	}
	if channel.AdminState != "" && channel.AdminState != "enabled" && channel.AdminState != "disabled" {
		return fmt.Errorf("人工状态无效: %s", channel.AdminState)
	}
	if defaultString(channel.AdminState, "enabled") == "enabled" && channel.SecretRef == "" {
		return errors.New("启用渠道前必须配置渠道凭证")
	}
	if channel.ConcurrencyLimit < 0 || channel.RequestTimeoutMs < 0 || channel.StreamIdleTimeoutMs < 0 || channel.CooldownSeconds < 0 || channel.Priority < 0 {
		return errors.New("渠道运行参数不能为负数")
	}
	if channel.ReasoningEffort != "" && !config.IsValidReasoningEffort(channel.ReasoningEffort) {
		return fmt.Errorf("渠道思考等级无效: %s", channel.ReasoningEffort)
	}
	if strings.ContainsAny(channel.Group, "\r\n") {
		return errors.New("渠道分组不能包含换行符")
	}
	if _, ok := normalizeCustomHeaders(channel.CustomHeaders); !ok {
		return errors.New("自定义请求头存在重复或无效名称")
	}
	for name, value := range channel.CustomHeaders {
		if !isHeaderToken(name) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("自定义请求头无效: %s", name)
		}
	}
	if channel.FailureAction != "" || channel.FailureThreshold != 0 {
		if err := validateFailurePolicy(defaultString(channel.FailureAction, "cooldown"), channel.FailureThreshold); err != nil {
			return err
		}
	}
	return nil
}

func isHeaderToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		default:
			return false
		}
	}
	return true
}

func validateFailurePolicy(action string, threshold int) error {
	if action != "cooldown" && action != "auto_disable" {
		return fmt.Errorf("失败动作无效: %s", action)
	}
	if threshold <= 0 {
		return errors.New("连续失败阈值必须大于 0")
	}
	return nil
}

func validateMapping(mapping GlobalModelMapping) error {
	if err := validateProtocol(mapping.Protocol); err != nil {
		return err
	}
	if strings.TrimSpace(mapping.ClientModel) == "" || strings.TrimSpace(mapping.LogicalModel) == "" {
		return errors.New("模型映射字段不能为空")
	}
	return nil
}

func validateChannelMapping(mapping ChannelModelMapping) error {
	if err := validateProtocol(mapping.Protocol); err != nil {
		return err
	}
	if strings.TrimSpace(mapping.ChannelID) == "" || strings.TrimSpace(mapping.LogicalModel) == "" || strings.TrimSpace(mapping.UpstreamModel) == "" {
		return errors.New("渠道模型映射字段不能为空")
	}
	return nil
}

func validateProbePolicy(policy ProbePolicy) error {
	if strings.TrimSpace(policy.ChannelID) == "" {
		return errors.New("探针策略必须指定渠道")
	}
	if policy.Mode != "connectivity" && policy.Mode != "minimal_inference" {
		return fmt.Errorf("探针模式无效: %s", policy.Mode)
	}
	if policy.IntervalSeconds != 0 && policy.IntervalSeconds < 10 {
		return errors.New("探针间隔不能小于 10 秒")
	}
	if policy.FailureThreshold != 0 && policy.FailureThreshold < 1 {
		return errors.New("探针失败阈值必须大于 0")
	}
	if policy.RequestTimeoutMs != 0 && policy.RequestTimeoutMs < 1000 {
		return errors.New("探针超时必须至少为 1000 毫秒")
	}
	if strings.ContainsAny(policy.Model+policy.Path, "\r\n") {
		return errors.New("探针模型和路径不能包含换行符")
	}
	if policy.AutoRecover && !policy.Enabled {
		return errors.New("开启自动恢复时必须同时开启探针")
	}
	if policy.RecoverySuccessThreshold < 1 {
		return errors.New("恢复成功阈值必须大于 0")
	}
	return nil
}

func validateProtocol(protocol Protocol) error {
	switch protocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropicMessages:
		return nil
	default:
		return fmt.Errorf("不支持的协议: %s", protocol)
	}
}

func protocolFamily(protocol Protocol) string {
	if protocol == ProtocolAnthropicMessages {
		return "anthropic"
	}
	return "openai"
}

func normalizeCapabilities(capabilities []string) []string {
	seen := make(map[string]struct{}, len(capabilities))
	result := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, exists := seen[capability]; !exists {
			seen[capability] = struct{}{}
			result = append(result, capability)
		}
	}
	sort.Strings(result)
	return result
}

func normalizeChannelModels(models []string) ([]string, error) {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || strings.ContainsAny(model, "\r\n") {
			return nil, errors.New("模型名称不能为空或包含换行符")
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	sort.Strings(result)
	return result, nil
}

func capabilitiesMatch(available, requested []string) bool {
	// 渠道未声明能力时表示不限制请求能力；声明后才按集合严格匹配。
	if len(available) == 0 || len(requested) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(available))
	for _, capability := range available {
		set[capability] = struct{}{}
	}
	for _, capability := range requested {
		if _, exists := set[capability]; !exists {
			return false
		}
	}
	return true
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
