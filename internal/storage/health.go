package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

const (
	healthFailureThreshold = 3
	healthCooldownBase     = 30 * time.Second
	healthCooldownMax      = 15 * time.Minute
)

// AdmissionPurpose 标识一次渠道准入的用途。
type AdmissionPurpose string

const (
	// AdmissionBusiness 表示真实代理业务请求。
	AdmissionBusiness AdmissionPurpose = "business"
	// AdmissionAutomaticProbe 表示定时恢复或健康探针。
	AdmissionAutomaticProbe AdmissionPurpose = "automatic_probe"
	// AdmissionManualProbe 表示操作员主动发起的探针。
	AdmissionManualProbe AdmissionPurpose = "manual_probe"
)

// HealthOutcome 表示一次 lease 完成后的集中健康分类。
type HealthOutcome string

const (
	// HealthSuccess 表示渠道实际调用成功。
	HealthSuccess HealthOutcome = "success"
	// HealthFailure 表示需要累计渠道健康失败的上游错误。
	HealthFailure HealthOutcome = "health_failure"
	// HealthNeutralFailure 表示不应影响渠道健康的已发送错误。
	HealthNeutralFailure HealthOutcome = "neutral_failure"
	// HealthNotSent 表示请求未实际发往上游。
	HealthNotSent HealthOutcome = "not_sent"
)

// ChannelAdmissionState 是 TryAcquire 所需的最新持久化渠道状态。
type ChannelAdmissionState struct {
	ChannelID                string
	AdminState               string
	HealthState              string
	HealthVersion            int64
	ConcurrencyLimit         int
	CooldownUntil            string
	HalfOpenClaimed          bool
	ProbeEnabled             bool
	ProbeAutoRecover         bool
	RecoverySuccessThreshold int
}

// HealthSettleInput 描述一次 lease 的健康结算输入。
type HealthSettleInput struct {
	ChannelID     string
	HealthVersion int64
	Purpose       AdmissionPurpose
	Outcome       HealthOutcome
	ErrorClass    string
	RetryAfter    time.Duration
	OccurredAt    time.Time
}

// HealthResult 描述一次有效健康结算前后的状态。
type HealthResult struct {
	Applied                 bool   `json:"applied"`
	FromState               string `json:"fromState"`
	ToState                 string `json:"toState"`
	HealthVersion           int64  `json:"healthVersion"`
	FailureCountBefore      int    `json:"failureCountBefore"`
	FailureCountAfter       int    `json:"failureCountAfter"`
	ProbeFailureCountBefore int    `json:"probeFailureCountBefore"`
	ProbeFailureCountAfter  int    `json:"probeFailureCountAfter"`
	ProbeSuccessCountBefore int    `json:"probeSuccessCountBefore"`
	ProbeSuccessCountAfter  int    `json:"probeSuccessCountAfter"`
	CooldownUntil           string `json:"cooldownUntil,omitempty"`
}

// ResetHalfOpenClaims 清理进程重启前遗留的半开放探针占用。
func ResetHalfOpenClaims(database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("健康运行态数据库无效")
	}
	if _, err := database.Exec(`UPDATE health_runtime SET half_open_claimed = 0`); err != nil {
		return fmt.Errorf("清理半开放探针占用失败: %w", err)
	}
	return nil
}

// GetChannelAdmissionState 读取渠道准入所需的最新配置和健康运行态。
func GetChannelAdmissionState(database *sql.DB, channelID string) (ChannelAdmissionState, error) {
	if database == nil || strings.TrimSpace(channelID) == "" {
		return ChannelAdmissionState{}, fmt.Errorf("渠道 ID 无效")
	}
	if _, err := database.Exec(`INSERT OR IGNORE INTO health_runtime(channel_id) VALUES (?)`, channelID); err != nil {
		return ChannelAdmissionState{}, fmt.Errorf("初始化渠道健康运行态失败: %w", err)
	}
	var state ChannelAdmissionState
	var halfOpenClaimed, probeEnabled, autoRecover int
	err := database.QueryRow(`
SELECT c.id, c.admin_state, c.health_state, c.health_version, c.concurrency_limit,
       r.cooldown_until, r.half_open_claimed, COALESCE(p.enabled, 0),
       COALESCE(p.auto_recover, 0), COALESCE(p.recovery_success_threshold, 1)
FROM channels c
JOIN health_runtime r ON r.channel_id = c.id
LEFT JOIN probe_policies p ON p.channel_id = c.id
WHERE c.id = ?`, channelID).Scan(
		&state.ChannelID, &state.AdminState, &state.HealthState, &state.HealthVersion,
		&state.ConcurrencyLimit, &state.CooldownUntil, &halfOpenClaimed,
		&probeEnabled, &autoRecover, &state.RecoverySuccessThreshold,
	)
	if err != nil {
		return ChannelAdmissionState{}, fmt.Errorf("读取渠道准入状态失败: %w", err)
	}
	if state.ConcurrencyLimit <= 0 {
		state.ConcurrencyLimit = 8
	}
	if state.RecoverySuccessThreshold <= 0 {
		state.RecoverySuccessThreshold = 1
	}
	state.HalfOpenClaimed = halfOpenClaimed == 1
	state.ProbeEnabled = probeEnabled == 1
	state.ProbeAutoRecover = autoRecover == 1
	return state, nil
}

// ClaimChannelHalfOpen 原子进入或领取当前健康版本的唯一半开放验证名额。
func ClaimChannelHalfOpen(database *sql.DB, channelID string, expectedVersion int64, now time.Time) (int64, bool, error) {
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := database.Begin()
	if err != nil {
		return expectedVersion, false, fmt.Errorf("开始半开放准入事务失败: %w", err)
	}
	defer tx.Rollback()
	var state, cooldownUntil string
	var version int64
	var claimed int
	if err := tx.QueryRow(`SELECT c.health_state, c.health_version, r.cooldown_until, r.half_open_claimed FROM channels c JOIN health_runtime r ON r.channel_id = c.id WHERE c.id = ?`, channelID).Scan(&state, &version, &cooldownUntil, &claimed); err != nil {
		return expectedVersion, false, fmt.Errorf("读取半开放准入状态失败: %w", err)
	}
	if version != expectedVersion || claimed != 0 {
		return version, false, nil
	}
	if state == "cooldown" {
		expires, parseErr := time.Parse(time.RFC3339Nano, cooldownUntil)
		if parseErr != nil || now.Before(expires) {
			return version, false, nil
		}
		result, updateErr := tx.Exec(`UPDATE channels SET health_state = 'half_open', health_version = health_version + 1, updated_at = ? WHERE id = ? AND health_state = 'cooldown' AND health_version = ?`, now.UTC().Format(time.RFC3339Nano), channelID, expectedVersion)
		if updateErr != nil {
			return version, false, fmt.Errorf("进入半开放状态失败: %w", updateErr)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return version, false, nil
		}
		version++
		if err := recordHealthEvent(tx, channelID, "cooldown", "half_open", "cooldown_expired", now); err != nil {
			return version, false, err
		}
	} else if state != "half_open" {
		return version, false, nil
	}
	result, err := tx.Exec(`UPDATE health_runtime SET half_open_claimed = 1 WHERE channel_id = ? AND half_open_claimed = 0`, channelID)
	if err != nil {
		return version, false, fmt.Errorf("领取半开放名额失败: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return version, false, nil
	}
	if err := tx.Commit(); err != nil {
		return version, false, fmt.Errorf("提交半开放准入事务失败: %w", err)
	}
	return version, true, nil
}

// SettleChannelHealth 按 lease 健康版本结算业务或探针结果。
func SettleChannelHealth(database *sql.DB, input HealthSettleInput) (HealthResult, error) {
	if database == nil || strings.TrimSpace(input.ChannelID) == "" {
		return HealthResult{}, fmt.Errorf("渠道 ID 无效")
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}
	tx, err := database.Begin()
	if err != nil {
		return HealthResult{}, fmt.Errorf("开始渠道健康结算事务失败: %w", err)
	}
	defer tx.Rollback()
	result, err := settleChannelHealthTx(tx, input)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("提交渠道健康结算事务失败: %w", err)
	}
	return result, nil
}

// settleChannelHealthTx 在同一事务内按健康版本结算渠道状态；版本冲突或人工禁用时只释放半开放占用。
func settleChannelHealthTx(tx *sql.Tx, input HealthSettleInput) (HealthResult, error) {
	if strings.TrimSpace(input.ChannelID) == "" {
		return HealthResult{}, fmt.Errorf("渠道 ID 无效")
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO health_runtime(channel_id) VALUES (?)`, input.ChannelID); err != nil {
		return HealthResult{}, err
	}
	var adminState, healthState, failureAction, cooldownUntil string
	var version int64
	var failureThreshold, cooldownSeconds, failureCount, probeFailureCount, cooldownLevel, probeSuccessCount int
	var probeEnabled, autoRecover, probeFailureThreshold, recoveryThreshold int
	err := tx.QueryRow(`
SELECT c.admin_state, c.health_state, c.health_version, c.failure_action, c.failure_threshold, c.cooldown_seconds,
       r.failure_count, r.probe_failure_count, r.cooldown_level, r.cooldown_until, r.probe_success_count,
       COALESCE(p.enabled, 0), COALESCE(p.auto_recover, 0), COALESCE(p.failure_threshold, 3), COALESCE(p.recovery_success_threshold, 1)
FROM channels c
JOIN health_runtime r ON r.channel_id = c.id
LEFT JOIN probe_policies p ON p.channel_id = c.id
WHERE c.id = ?`, input.ChannelID).Scan(
		&adminState, &healthState, &version, &failureAction, &failureThreshold, &cooldownSeconds,
		&failureCount, &probeFailureCount, &cooldownLevel, &cooldownUntil, &probeSuccessCount,
		&probeEnabled, &autoRecover, &probeFailureThreshold, &recoveryThreshold,
	)
	if err != nil {
		return HealthResult{}, fmt.Errorf("读取渠道健康结算状态失败: %w", err)
	}
	result := HealthResult{FromState: healthState, ToState: healthState, HealthVersion: version, FailureCountBefore: failureCount, FailureCountAfter: failureCount, ProbeFailureCountBefore: probeFailureCount, ProbeFailureCountAfter: probeFailureCount, ProbeSuccessCountBefore: probeSuccessCount, ProbeSuccessCountAfter: probeSuccessCount, CooldownUntil: cooldownUntil}
	// 版本不匹配或已人工禁用时不改健康状态，但仍释放本 lease 持有的半开放占用。
	if version != input.HealthVersion || adminState == "disabled" {
		if _, err := tx.Exec(`UPDATE health_runtime SET half_open_claimed = 0 WHERE channel_id = ?`, input.ChannelID); err != nil {
			return result, err
		}
		return result, nil
	}
	if input.Outcome == HealthNotSent || input.Outcome == HealthNeutralFailure {
		query := `UPDATE health_runtime SET half_open_claimed = 0 WHERE channel_id = ?`
		if input.Outcome == HealthNeutralFailure && input.Purpose == AdmissionAutomaticProbe && healthState == "auto_disabled" {
			query = `UPDATE health_runtime SET probe_success_count = 0, half_open_claimed = 0 WHERE channel_id = ?`
			result.ProbeSuccessCountAfter = 0
		}
		if _, err := tx.Exec(query, input.ChannelID); err != nil {
			return result, err
		}
		return result, nil
	}
	if input.Purpose == AdmissionBusiness {
		result, err = settleBusinessHealth(tx, input, result, healthState, failureAction, failureThreshold, cooldownSeconds, failureCount, cooldownLevel)
	} else {
		result, err = settleProbeHealth(tx, input, result, healthState, failureAction, cooldownSeconds, failureCount, probeFailureCount, cooldownLevel, probeSuccessCount, probeEnabled == 1, autoRecover == 1, probeFailureThreshold, recoveryThreshold)
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func settleBusinessHealth(tx *sql.Tx, input HealthSettleInput, result HealthResult, state, action string, threshold, cooldownSeconds, failureCount, cooldownLevel int) (HealthResult, error) {
	if input.Outcome == HealthSuccess {
		result.Applied = state == "half_open" || state == "degraded" || failureCount != 0
		result.FailureCountAfter = 0
		result.CooldownUntil = ""
		if state == "half_open" {
			result.ToState = "healthy"
			result.HealthVersion++
			if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = 'healthy', health_version = health_version + 1, updated_at = ? WHERE id = ? AND health_version = ?`, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
				return result, err
			}
			if err := recordHealthEvent(tx, input.ChannelID, state, "healthy", "request_success", input.OccurredAt); err != nil {
				return result, err
			}
		} else if state == "healthy" || state == "degraded" {
			result.ToState = "healthy"
			if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = 'healthy', updated_at = ? WHERE id = ? AND health_version = ?`, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
				return result, err
			}
			if state != "healthy" {
				if err := recordHealthEvent(tx, input.ChannelID, state, "healthy", "request_success", input.OccurredAt); err != nil {
					return result, err
				}
			}
		}
		_, err := tx.Exec(`UPDATE health_runtime SET failure_count = 0, cooldown_level = 0, cooldown_until = '', half_open_claimed = 0 WHERE channel_id = ?`, input.ChannelID)
		return result, err
	}
	if input.Outcome != HealthFailure || (state != "healthy" && state != "degraded" && state != "half_open") {
		return result, nil
	}
	if threshold <= 0 {
		threshold = healthFailureThreshold
	}
	failureCount++
	result.Applied = true
	result.FailureCountAfter = failureCount
	if state != "half_open" && failureCount < threshold {
		result.ToState = "degraded"
		if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = 'degraded', updated_at = ? WHERE id = ? AND health_version = ?`, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
			return result, err
		}
		if state != "degraded" {
			if err := recordHealthEvent(tx, input.ChannelID, state, "degraded", input.ErrorClass, input.OccurredAt); err != nil {
				return result, err
			}
		}
		_, err := tx.Exec(`UPDATE health_runtime SET failure_count = ?, last_error_class = ? WHERE channel_id = ?`, failureCount, input.ErrorClass, input.ChannelID)
		return result, err
	}
	if state == "half_open" {
		action = "cooldown"
	}
	return transitionFailedHealth(tx, input, result, state, action, cooldownSeconds, failureCount, cooldownLevel)
}

func settleProbeHealth(tx *sql.Tx, input HealthSettleInput, result HealthResult, state, action string, cooldownSeconds, failureCount, probeFailureCount, cooldownLevel, probeSuccessCount int, probeEnabled, autoRecover bool, failureThreshold, recoveryThreshold int) (HealthResult, error) {
	if input.Outcome == HealthSuccess {
		result.ProbeFailureCountAfter = 0
		result.ProbeSuccessCountAfter = 0
		result.Applied = probeFailureCount != 0 || probeSuccessCount != 0
		if state == "half_open" {
			result.Applied = true
			result.ToState = "healthy"
			result.HealthVersion++
			result.FailureCountAfter = 0
			result.CooldownUntil = ""
			if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = 'healthy', health_version = health_version + 1, updated_at = ? WHERE id = ? AND health_version = ?`, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
				return result, err
			}
			if err := recordHealthEvent(tx, input.ChannelID, state, "healthy", "probe_success", input.OccurredAt); err != nil {
				return result, err
			}
			_, err := tx.Exec(`UPDATE health_runtime SET failure_count = 0, probe_failure_count = 0, cooldown_level = 0, cooldown_until = '', half_open_claimed = 0, probe_success_count = 0 WHERE channel_id = ?`, input.ChannelID)
			return result, err
		}
		if state == "auto_disabled" && probeEnabled && autoRecover && input.Purpose == AdmissionAutomaticProbe {
			probeSuccessCount++
			result.Applied = true
			result.ProbeSuccessCountAfter = probeSuccessCount
			if recoveryThreshold <= 0 {
				recoveryThreshold = 1
			}
			if probeSuccessCount >= recoveryThreshold {
				result.Applied = true
				result.ToState = "healthy"
				result.HealthVersion++
				result.FailureCountAfter = 0
				result.ProbeSuccessCountAfter = 0
				result.CooldownUntil = ""
				if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = 'healthy', health_version = health_version + 1, updated_at = ? WHERE id = ? AND health_version = ?`, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
					return result, err
				}
				if err := recordHealthEvent(tx, input.ChannelID, state, "healthy", "probe_success_threshold", input.OccurredAt); err != nil {
					return result, err
				}
				_, err := tx.Exec(`UPDATE health_runtime SET failure_count = 0, probe_failure_count = 0, cooldown_level = 0, cooldown_until = '', half_open_claimed = 0, probe_success_count = 0 WHERE channel_id = ?`, input.ChannelID)
				return result, err
			}
			_, err := tx.Exec(`UPDATE health_runtime SET probe_failure_count = 0, probe_success_count = ?, half_open_claimed = 0 WHERE channel_id = ?`, probeSuccessCount, input.ChannelID)
			return result, err
		}
		_, err := tx.Exec(`UPDATE health_runtime SET probe_failure_count = 0, half_open_claimed = 0 WHERE channel_id = ?`, input.ChannelID)
		return result, err
	}
	if input.Outcome != HealthFailure {
		return result, nil
	}
	if state == "auto_disabled" {
		result.Applied = true
		result.ProbeFailureCountAfter = 0
		result.ProbeSuccessCountAfter = 0
		_, err := tx.Exec(`UPDATE health_runtime SET probe_failure_count = 0, probe_success_count = 0, half_open_claimed = 0 WHERE channel_id = ?`, input.ChannelID)
		return result, err
	}
	if state == "half_open" {
		result.Applied = true
		return transitionFailedHealth(tx, input, result, state, "cooldown", cooldownSeconds, failureCount, cooldownLevel)
	}
	if state != "healthy" && state != "degraded" {
		return result, nil
	}
	if failureThreshold <= 0 {
		failureThreshold = healthFailureThreshold
	}
	probeFailureCount++
	result.Applied = true
	result.ProbeFailureCountAfter = probeFailureCount
	result.ProbeSuccessCountAfter = 0
	if probeFailureCount < failureThreshold {
		_, err := tx.Exec(`UPDATE health_runtime SET probe_failure_count = ?, probe_success_count = 0, last_error_class = ? WHERE channel_id = ?`, probeFailureCount, input.ErrorClass, input.ChannelID)
		return result, err
	}
	result.Applied = true
	result.ProbeFailureCountAfter = 0
	return transitionFailedHealth(tx, input, result, state, action, cooldownSeconds, failureCount, cooldownLevel)
}

func transitionFailedHealth(tx *sql.Tx, input HealthSettleInput, result HealthResult, state, action string, cooldownSeconds, failureCount, cooldownLevel int) (HealthResult, error) {
	toState := "cooldown"
	if action == "auto_disable" {
		toState = "auto_disabled"
	}
	result.ToState = toState
	result.HealthVersion++
	result.CooldownUntil = ""
	if toState == "cooldown" {
		cooldownLevel++
		cooldown := channelCooldown(input.ChannelID, result.HealthVersion, cooldownSeconds, cooldownLevel)
		if input.RetryAfter > cooldown {
			cooldown = input.RetryAfter
		}
		result.CooldownUntil = input.OccurredAt.Add(cooldown).UTC().Format(time.RFC3339Nano)
	}
	if err := execVersionedChannelHealthUpdate(tx, `UPDATE channels SET health_state = ?, health_version = health_version + 1, updated_at = ? WHERE id = ? AND health_version = ?`, toState, input.OccurredAt.UTC().Format(time.RFC3339Nano), input.ChannelID, input.HealthVersion); err != nil {
		return result, err
	}
	if err := recordHealthEvent(tx, input.ChannelID, state, toState, input.ErrorClass, input.OccurredAt); err != nil {
		return result, err
	}
	_, err := tx.Exec(`UPDATE health_runtime SET failure_count = ?, probe_failure_count = 0, cooldown_level = ?, cooldown_until = ?, half_open_claimed = 0, probe_success_count = 0, last_error_class = ? WHERE channel_id = ?`, failureCount, cooldownLevel, result.CooldownUntil, input.ErrorClass, input.ChannelID)
	return result, err
}

func execVersionedChannelHealthUpdate(tx *sql.Tx, query string, arguments ...any) error {
	result, err := tx.Exec(query, arguments...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取渠道健康更新结果失败: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("渠道健康版本条件更新未命中")
	}
	return nil
}

// channelCooldown 返回带确定性抖动且不超过固定上限的渠道冷却时间。
func channelCooldown(channelID string, version int64, baseSeconds, level int) time.Duration {
	if baseSeconds <= 0 {
		baseSeconds = int(healthCooldownBase / time.Second)
	}
	if level <= 0 {
		level = 1
	}
	if baseSeconds >= int(healthCooldownMax/time.Second) {
		return healthCooldownMax
	}
	cooldown := time.Duration(baseSeconds) * time.Second
	if cooldown >= healthCooldownMax {
		return healthCooldownMax
	}
	for current := 1; current < level && cooldown < healthCooldownMax; current++ {
		cooldown *= 2
		if cooldown >= healthCooldownMax {
			return healthCooldownMax
		}
	}
	hash := fnv.New64a()
	_, _ = fmt.Fprintf(hash, "%s:%d", channelID, version)
	jitter := time.Duration(hash.Sum64()%1001) * (cooldown / 10) / 1000
	if cooldown+jitter > healthCooldownMax {
		return healthCooldownMax
	}
	return cooldown + jitter
}

// NewHealthEventID 创建健康状态事件 ID。
func NewHealthEventID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成健康事件 ID 失败: %w", err)
	}
	return "health_" + hex.EncodeToString(buffer), nil
}

// recordHealthEvent 写入一次渠道健康状态变化事件。
func recordHealthEvent(tx *sql.Tx, channelID, fromState, toState, reason string, at time.Time) error {
	id, err := NewHealthEventID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO health_events(id, channel_id, from_state, to_state, reason, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`, id, channelID, fromState, toState, reason, at.UTC().Format(time.RFC3339Nano))
	return err
}

// ResetChannelHealth 人工恢复系统健康状态；人工禁用渠道仍保持不可路由。
func ResetChannelHealth(database *sql.DB, channelID string, at time.Time) error {
	if database == nil || strings.TrimSpace(channelID) == "" {
		return fmt.Errorf("渠道 ID 无效")
	}
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT health_state FROM channels WHERE id = ?`, channelID).Scan(&state); err != nil {
		return err
	}
	if state == "healthy" {
		if _, err := tx.Exec(`UPDATE channels SET updated_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), channelID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE channels SET health_state = 'healthy', health_version = health_version + 1, updated_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), channelID); err != nil {
			return err
		}
		if err := recordHealthEvent(tx, channelID, state, "healthy", "manual_recover", at); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE health_runtime SET failure_count = 0, probe_failure_count = 0, cooldown_level = 0, cooldown_until = '', half_open_claimed = 0, probe_success_count = 0, last_error_class = '' WHERE channel_id = ?`, channelID); err != nil {
		return err
	}
	return tx.Commit()
}
