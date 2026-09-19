package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
)

const (
	probeRunRetentionDays  = 7
	maxProbeRunsPerChannel = 200
)

// ProbeRun 表示一次独立于业务请求的探针执行结果。
type ProbeRun struct {
	ID           string        `json:"id"`
	ChannelID    string        `json:"channelId"`
	Mode         string        `json:"mode"`
	Status       string        `json:"status"`
	Latency      time.Duration `json:"-"`
	LatencyMs    int64         `json:"latencyMs"`
	ErrorClass   string        `json:"errorClass,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	OccurredAt   string        `json:"occurredAt"`
}

// GetProbePolicy 返回渠道显式探针策略；未配置时返回安全默认值。
func GetProbePolicy(database *sql.DB, channelID string) (ProbePolicy, error) {
	var policy ProbePolicy
	var enabled, autoRecover int
	err := database.QueryRow(`SELECT channel_id, enabled, mode, interval_seconds, model, path, failure_threshold, request_timeout_ms, auto_recover, recovery_success_threshold FROM probe_policies WHERE channel_id = ?`, channelID).Scan(&policy.ChannelID, &enabled, &policy.Mode, &policy.IntervalSeconds, &policy.Model, &policy.Path, &policy.FailureThreshold, &policy.RequestTimeoutMs, &autoRecover, &policy.RecoverySuccessThreshold)
	if err == sql.ErrNoRows {
		return normalizeProbePolicy(ProbePolicy{ChannelID: channelID, Mode: "connectivity"}), nil
	}
	if err != nil {
		return ProbePolicy{}, fmt.Errorf("读取探针策略失败: %w", err)
	}
	policy.Enabled = enabled == 1
	policy.AutoRecover = autoRecover == 1
	return normalizeProbePolicy(policy), nil
}

// normalizeProbePolicy 补齐探针的安全默认值。
func normalizeProbePolicy(policy ProbePolicy) ProbePolicy {
	if policy.Mode != "connectivity" && policy.Mode != "minimal_inference" {
		policy.Mode = "connectivity"
	}
	if policy.IntervalSeconds <= 0 {
		policy.IntervalSeconds = 60
	}
	if policy.FailureThreshold <= 0 {
		policy.FailureThreshold = 3
	}
	if policy.RequestTimeoutMs <= 0 {
		policy.RequestTimeoutMs = 15000
	}
	if policy.RecoverySuccessThreshold <= 0 {
		policy.RecoverySuccessThreshold = 1
	}
	if strings.TrimSpace(policy.Path) == "" {
		policy.Path = "/v1/models"
	}
	return policy
}

// GetProbeTarget 返回渠道探针所需的协议、地址、凭证引用和实际模型。
func GetProbeTarget(database *sql.DB, channelID string) (RouteTarget, error) {
	var target RouteTarget
	var upstream, headersJSON string
	var serviceTierPassthrough int
	err := database.QueryRow(`SELECT c.id, c.protocol, c.base_url, c.secret_ref, c.custom_headers_json, c.concurrency_limit, c.request_timeout_ms, c.stream_idle_timeout_ms, c.cooldown_seconds, c.priority, c.fallback_model, c.reasoning_effort, c.service_tier_passthrough, COALESCE((SELECT model FROM channel_models WHERE channel_id = c.id ORDER BY model LIMIT 1), (SELECT upstream_model FROM channel_model_mappings WHERE channel_id = c.id AND protocol = c.protocol ORDER BY logical_model LIMIT 1), c.fallback_model, '') FROM channels c WHERE c.id = ?`, channelID).Scan(&target.ChannelID, &target.Protocol, &target.BaseURL, &target.SecretRef, &headersJSON, &target.ConcurrencyLimit, &target.RequestTimeoutMs, &target.StreamIdleTimeoutMs, &target.CooldownSeconds, &target.Priority, &target.FallbackModel, &target.ReasoningEffort, &serviceTierPassthrough, &upstream)
	if err == sql.ErrNoRows {
		return RouteTarget{}, ErrNotFound
	}
	if err != nil {
		return RouteTarget{}, fmt.Errorf("读取探针渠道失败: %w", err)
	}
	target.ServiceTierPassthrough = serviceTierPassthrough == 1
	if target.ReasoningEffort == "" {
		target.ReasoningEffort = config.ReasoningEffortPassthrough
	}
	target.UpstreamModel = strings.TrimSpace(upstream)
	target = normalizeRouteTarget(target)
	if strings.TrimSpace(headersJSON) != "" {
		if err := json.Unmarshal([]byte(headersJSON), &target.CustomHeaders); err != nil {
			target.CustomHeaders = nil
			target.InvalidCustomHeaders = true
		}
	}
	return target, nil
}

// NewProbeRunID 创建不可预测的探针运行记录 ID。
func NewProbeRunID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成探针运行 ID 失败: %w", err)
	}
	return "probe_" + hex.EncodeToString(buffer), nil
}

// RecordProbeRun 持久化探针结果，并更新该渠道的最近探针快照。
func RecordProbeRun(database *sql.DB, run ProbeRun) error {
	if database == nil || strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.ChannelID) == "" || strings.TrimSpace(run.Mode) == "" || strings.TrimSpace(run.Status) == "" {
		return fmt.Errorf("探针运行记录字段无效")
	}
	if run.OccurredAt == "" {
		run.OccurredAt = FormatSQLiteTime(time.Now())
	}
	latency := run.LatencyMs
	if run.Latency > 0 {
		latency = run.Latency.Milliseconds()
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始写入探针运行记录失败: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.Exec(`INSERT INTO probe_runs(id, channel_id, mode, status, latency_ms, error_class, error_message, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, run.ChannelID, run.Mode, run.Status, latency, run.ErrorClass, run.ErrorMessage, run.OccurredAt); err != nil {
		return fmt.Errorf("写入探针运行记录失败: %w", err)
	}
	if err := upsertLatestProbeSnapshot(transaction, run, latency); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交探针运行记录失败: %w", err)
	}
	return nil
}

// upsertLatestProbeSnapshot 仅在记录更新时覆盖渠道最近探针快照。
func upsertLatestProbeSnapshot(transaction *sql.Tx, run ProbeRun, latency int64) error {
	if _, err := transaction.Exec(`INSERT OR IGNORE INTO health_runtime(channel_id) VALUES (?)`, run.ChannelID); err != nil {
		return fmt.Errorf("初始化渠道健康运行态失败: %w", err)
	}
	_, err := transaction.Exec(`
UPDATE health_runtime
SET last_probe_id = ?, last_probe_mode = ?, last_probe_status = ?, last_probe_latency_ms = ?, last_probe_error_class = ?, last_probe_error_message = ?, last_probe_occurred_at = ?
WHERE channel_id = ?
  AND (last_probe_occurred_at = '' OR last_probe_occurred_at < ? OR (last_probe_occurred_at = ? AND last_probe_id < ?))`,
		run.ID, run.Mode, run.Status, latency, run.ErrorClass, run.ErrorMessage, run.OccurredAt,
		run.ChannelID, run.OccurredAt, run.OccurredAt, run.ID)
	if err != nil {
		return fmt.Errorf("更新最近探针快照失败: %w", err)
	}
	return nil
}

// backfillLatestProbeSnapshots 用当前探针历史回填每个渠道的最近探针快照。
func backfillLatestProbeSnapshots(transaction *sql.Tx) error {
	rows, err := transaction.Query(`
SELECT id, channel_id, mode, status, latency_ms, error_class, error_message, occurred_at
FROM (
  SELECT id, channel_id, mode, status, latency_ms, error_class, error_message, occurred_at,
         ROW_NUMBER() OVER (PARTITION BY channel_id ORDER BY occurred_at DESC, id DESC) AS rn
  FROM probe_runs
)
WHERE rn = 1`)
	if err != nil {
		return fmt.Errorf("读取最近探针快照失败: %w", err)
	}
	runs := make([]ProbeRun, 0)
	for rows.Next() {
		var run ProbeRun
		if err := rows.Scan(&run.ID, &run.ChannelID, &run.Mode, &run.Status, &run.LatencyMs, &run.ErrorClass, &run.ErrorMessage, &run.OccurredAt); err != nil {
			rows.Close()
			return fmt.Errorf("读取最近探针快照字段失败: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("遍历最近探针快照失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭最近探针快照游标失败: %w", err)
	}
	for _, run := range runs {
		if err := upsertLatestProbeSnapshot(transaction, run, run.LatencyMs); err != nil {
			return err
		}
	}
	return nil
}

// TrimProbeRuns 按较短保留期和每渠道条数上限压缩探针历史，不修改最近探针快照。
func TrimProbeRuns(database *sql.DB, requestRetentionDays int, now time.Time) (int64, error) {
	if database == nil {
		return 0, fmt.Errorf("数据库未打开")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	retentionDays := probeRunRetentionDays
	if requestRetentionDays > 0 && requestRetentionDays < retentionDays {
		retentionDays = requestRetentionDays
	}
	cutoff := now.UTC().AddDate(0, 0, -retentionDays).Truncate(time.Hour)
	result, err := database.Exec(`DELETE FROM probe_runs WHERE occurred_at < ?`, FormatSQLiteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("按保留期清理探针记录失败: %w", err)
	}
	removed, _ := result.RowsAffected()
	result, err = database.Exec(`
DELETE FROM probe_runs WHERE id IN (
  SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY channel_id ORDER BY occurred_at DESC, id DESC) AS rn
    FROM probe_runs
  ) ranked
  WHERE ranked.rn > ?
)`, maxProbeRunsPerChannel)
	if err != nil {
		return 0, fmt.Errorf("按渠道上限清理探针记录失败: %w", err)
	}
	extra, _ := result.RowsAffected()
	return removed + extra, nil
}
