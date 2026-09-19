package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type attemptExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// RequestRecord 表示一次客户端逻辑请求的账本记录；token 为 nil 时表示上游未提供该字段。
type RequestRecord struct {
	ID                    string
	Protocol              Protocol
	ClientModel           string
	LogicalModel          string
	FinalStatus           string
	GroupName             string
	InitialChannelID      string
	InitialChannelName    string
	FinalChannelID        string
	FinalChannelName      string
	FallbackTriggered     bool
	AttemptCount          int
	StartedAt             time.Time
	CompletedAt           *time.Time
	InputTokens           *int64
	OutputTokens          *int64
	CacheReadInputTokens  *int64
	CacheWriteInputTokens *int64
	ReasoningTokens       *int64
	TotalTokens           *int64
	CacheRate             *float64
	TTFT                  *time.Duration
	TPS                   *float64
	StreamEnded           *bool
	FinalHTTPStatus       *int
	ErrorClass            string
	ErrorMessage          string
}

// AttemptRecord 表示一次真实上游调用，不保存请求或响应正文。
type AttemptRecord struct {
	ID                    string
	RequestID             string
	ChannelID             string
	ChannelName           string
	GroupName             string
	Protocol              Protocol
	ClientModel           string
	UpstreamModel         string
	LogicalModel          string
	Sequence              int
	Status                string
	ErrorClass            string
	ErrorMessage          string
	RetryReason           string
	FallbackTriggered     bool
	StartedAt             time.Time
	FirstByteAt           *time.Time
	FirstEventAt          *time.Time
	CompletedAt           *time.Time
	HTTPStatus            *int
	Latency               time.Duration
	InputTokens           *int64
	OutputTokens          *int64
	CacheReadInputTokens  *int64
	CacheWriteInputTokens *int64
	ReasoningTokens       *int64
	TotalTokens           *int64
	RequestContentBlobID  string
	ResponseContentBlobID string
	StreamEnded           *bool
}

// RequestDetail 是管理 API 使用的请求和尝试明细视图。
type RequestDetail struct {
	ID                    string          `json:"id"`
	Protocol              Protocol        `json:"protocol"`
	ClientModel           string          `json:"clientModel"`
	LogicalModel          string          `json:"logicalModel"`
	FinalStatus           string          `json:"finalStatus"`
	GroupName             string          `json:"groupName,omitempty"`
	InitialChannelID      string          `json:"initialChannelId,omitempty"`
	InitialChannelName    string          `json:"initialChannelName,omitempty"`
	FinalChannelID        string          `json:"finalChannelId,omitempty"`
	FinalChannelName      string          `json:"finalChannelName,omitempty"`
	ChannelChain          []string        `json:"channelChain,omitempty"`
	ChannelSwitchCount    int             `json:"channelSwitchCount"`
	FallbackTriggered     bool            `json:"fallbackTriggered"`
	AttemptCount          int             `json:"attemptCount"`
	StartedAt             string          `json:"startedAt"`
	CompletedAt           string          `json:"completedAt,omitempty"`
	InputTokens           *int64          `json:"inputTokens"`
	OutputTokens          *int64          `json:"outputTokens"`
	CacheReadInputTokens  *int64          `json:"cacheReadInputTokens"`
	CacheWriteInputTokens *int64          `json:"cacheWriteInputTokens"`
	ReasoningTokens       *int64          `json:"reasoningTokens"`
	TotalTokens           *int64          `json:"totalTokens"`
	CacheRate             *float64        `json:"cacheRate"`
	TTFTMs                *int64          `json:"ttftMs"`
	TPS                   *float64        `json:"tps"`
	StreamEnded           *bool           `json:"streamEnded"`
	FinalHTTPStatus       *int            `json:"finalHttpStatus"`
	ErrorClass            string          `json:"errorClass,omitempty"`
	ErrorMessage          string          `json:"errorMessage,omitempty"`
	LatencyMs             int64           `json:"latencyMs"`
	Attempts              []AttemptDetail `json:"attempts"`
	RouteEvents           []RouteEvent    `json:"routeEvents"`
	ContentBlobs          []ContentBlob   `json:"contentBlobs,omitempty"`
}

// RouteEvent 表示一次没有伪造 Attempt 的路由决策或健康变化。
type RouteEvent struct {
	RequestID        string          `json:"requestId"`
	Sequence         int             `json:"sequence"`
	EventType        string          `json:"eventType"`
	ChannelID        string          `json:"channelId,omitempty"`
	RelatedChannelID string          `json:"relatedChannelId,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Details          json.RawMessage `json:"details"`
	OccurredAt       string          `json:"occurredAt"`
}

// AttemptDetail 是不含正文的上游尝试视图。
type AttemptDetail struct {
	ID                    string   `json:"id"`
	RequestID             string   `json:"requestId"`
	ChannelID             string   `json:"channelId"`
	ChannelName           string   `json:"channelName,omitempty"`
	GroupName             string   `json:"groupName,omitempty"`
	Protocol              Protocol `json:"protocol"`
	ClientModel           string   `json:"clientModel,omitempty"`
	UpstreamModel         string   `json:"upstreamModel,omitempty"`
	LogicalModel          string   `json:"logicalModel"`
	Sequence              int      `json:"sequence"`
	Status                string   `json:"status"`
	ErrorClass            string   `json:"errorClass,omitempty"`
	ErrorMessage          string   `json:"errorMessage,omitempty"`
	RetryReason           string   `json:"retryReason,omitempty"`
	FallbackTriggered     bool     `json:"fallbackTriggered"`
	StartedAt             string   `json:"startedAt"`
	FirstByteAt           string   `json:"firstByteAt,omitempty"`
	FirstEventAt          string   `json:"firstEventAt,omitempty"`
	CompletedAt           string   `json:"completedAt,omitempty"`
	HTTPStatus            *int     `json:"httpStatus"`
	LatencyMs             int64    `json:"latencyMs"`
	InputTokens           *int64   `json:"inputTokens"`
	OutputTokens          *int64   `json:"outputTokens"`
	CacheReadInputTokens  *int64   `json:"cacheReadInputTokens"`
	CacheWriteInputTokens *int64   `json:"cacheWriteInputTokens"`
	ReasoningTokens       *int64   `json:"reasoningTokens"`
	TotalTokens           *int64   `json:"totalTokens"`
	RequestContentBlobID  string   `json:"requestContentBlobId,omitempty"`
	ResponseContentBlobID string   `json:"responseContentBlobId,omitempty"`
	StreamEnded           *bool    `json:"streamEnded"`
}

// RequestListFilter 是请求日志列表的筛选条件。
type RequestListFilter struct {
	From       string
	To         string
	Model      string
	Protocol   string
	Status     string
	Channel    string
	Group      string
	ErrorClass string
	Fallback   string
	Keyword    string
}

// RequestPage 是请求日志分页响应。
type RequestPage struct {
	Items    []RequestDetail `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Total    int64           `json:"total"`
	HasMore  bool            `json:"hasMore"`
}

// NewRequestID 创建不可预测的逻辑请求 ID。
func NewRequestID() (string, error) { return newID("req_") }

// NewAttemptID 创建不可预测的 Attempt ID。
func NewAttemptID() (string, error) { return newID("att_") }

func newID(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成记录 ID 失败: %w", err)
	}
	return prefix + hex.EncodeToString(buffer), nil
}

// CreateRequest 写入逻辑请求的开始记录。
func CreateRequest(database *sql.DB, record RequestRecord) error {
	if database == nil || strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.ClientModel) == "" {
		return fmt.Errorf("请求账本字段无效")
	}
	if record.FinalStatus != "processing" {
		return fmt.Errorf("请求开始状态无效: %s", record.FinalStatus)
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	_, err := database.Exec(`INSERT INTO requests(id, protocol, client_model, logical_model, final_status, group_name, initial_channel_id, initial_channel_name, final_channel_id, final_channel_name, fallback_triggered, attempt_count, started_at, completed_at, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, cache_rate, ttft_ms, tps, stream_ended, final_http_status, error_class, error_message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ID, record.Protocol, record.ClientModel, record.LogicalModel, record.FinalStatus, record.GroupName, record.InitialChannelID, record.InitialChannelName, record.FinalChannelID, record.FinalChannelName, boolInt(record.FallbackTriggered), record.AttemptCount, FormatSQLiteTime(record.StartedAt), optionalTimeString(record.CompletedAt), record.InputTokens, record.OutputTokens, record.CacheReadInputTokens, record.CacheWriteInputTokens, record.ReasoningTokens, record.TotalTokens, record.CacheRate, durationMillis(record.TTFT), record.TPS, nullableBool(record.StreamEnded), record.FinalHTTPStatus, record.ErrorClass, record.ErrorMessage)
	if err != nil {
		return fmt.Errorf("写入请求账本失败: %w", err)
	}
	return nil
}

// RecordAttempt 写入一个上游 Attempt 结果。
func RecordAttempt(database *sql.DB, record AttemptRecord) error {
	if database == nil || strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.RequestID) == "" || strings.TrimSpace(record.ChannelID) == "" || record.Sequence <= 0 {
		return fmt.Errorf("Attempt 账本字段无效")
	}
	if record.Status != "processing" {
		return fmt.Errorf("Attempt 开始状态无效: %s", record.Status)
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC().Add(-record.Latency)
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始 Attempt 账本事务失败: %w", err)
	}
	defer transaction.Rollback()
	_, err = transaction.Exec(`INSERT INTO attempts(id, request_id, channel_id, channel_name, group_name, protocol, client_model, upstream_model, logical_model, sequence, status, error_class, error_message, retry_reason, fallback_triggered, started_at, first_byte_at, first_event_at, completed_at, http_status, latency_ms, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, request_content_blob_id, response_content_blob_id, stream_ended) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ID, record.RequestID, record.ChannelID, record.ChannelName, record.GroupName, record.Protocol, record.ClientModel, record.UpstreamModel, record.LogicalModel, record.Sequence, record.Status, record.ErrorClass, record.ErrorMessage, record.RetryReason, boolInt(record.FallbackTriggered), FormatSQLiteTime(record.StartedAt), optionalTimeString(record.FirstByteAt), optionalTimeString(record.FirstEventAt), optionalTimeString(record.CompletedAt), record.HTTPStatus, record.Latency.Milliseconds(), record.InputTokens, record.OutputTokens, record.CacheReadInputTokens, record.CacheWriteInputTokens, record.ReasoningTokens, record.TotalTokens, record.RequestContentBlobID, record.ResponseContentBlobID, nullableBool(record.StreamEnded))
	if err != nil {
		return fmt.Errorf("写入 Attempt 账本失败: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交 Attempt 账本事务失败: %w", err)
	}
	return nil
}

// UpdateAttempt 更新已创建 Attempt 的终态和上游响应元数据。
func UpdateAttempt(database *sql.DB, record AttemptRecord) error {
	if database == nil {
		return fmt.Errorf("Attempt 更新字段无效")
	}
	return updateAttempt(database, record)
}

// DeleteAttempt 删除一次未发送的临时 Attempt 账本记录。
func DeleteAttempt(database *sql.DB, requestID, attemptID string) error {
	if database == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(attemptID) == "" {
		return fmt.Errorf("Attempt 删除字段无效")
	}
	if _, err := database.Exec(`DELETE FROM attempts WHERE id = ? AND request_id = ?`, attemptID, requestID); err != nil {
		return fmt.Errorf("删除临时 Attempt 账本失败: %w", err)
	}
	return nil
}

func updateAttempt(executor attemptExecutor, record AttemptRecord) error {
	if strings.TrimSpace(record.ID) == "" || record.Sequence <= 0 || strings.TrimSpace(record.RequestID) == "" {
		return fmt.Errorf("Attempt 更新字段无效")
	}
	if !validAttemptFinalStatus(record.Status) {
		return fmt.Errorf("Attempt 最终状态无效: %s", record.Status)
	}
	result, err := executor.Exec(`UPDATE attempts SET status = ?, error_class = ?, error_message = ?, retry_reason = ?, fallback_triggered = ?, first_byte_at = ?, first_event_at = ?, completed_at = ?, http_status = ?, latency_ms = ?, input_tokens = ?, output_tokens = ?, cache_read_input_tokens = ?, cache_write_input_tokens = ?, reasoning_tokens = ?, total_tokens = ?, request_content_blob_id = ?, response_content_blob_id = ?, stream_ended = ? WHERE id = ? AND request_id = ? AND status = 'processing'`, record.Status, record.ErrorClass, record.ErrorMessage, record.RetryReason, boolInt(record.FallbackTriggered), optionalTimeString(record.FirstByteAt), optionalTimeString(record.FirstEventAt), optionalTimeString(record.CompletedAt), record.HTTPStatus, record.Latency.Milliseconds(), record.InputTokens, record.OutputTokens, record.CacheReadInputTokens, record.CacheWriteInputTokens, record.ReasoningTokens, record.TotalTokens, record.RequestContentBlobID, record.ResponseContentBlobID, nullableBool(record.StreamEnded), record.ID, record.RequestID)
	if err != nil {
		return fmt.Errorf("更新 Attempt 账本失败: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var current string
		if err := executor.QueryRow(`SELECT status FROM attempts WHERE id = ? AND request_id = ?`, record.ID, record.RequestID).Scan(&current); err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("读取 Attempt 终态失败: %w", err)
		} else if current == record.Status {
			return nil
		}
		return fmt.Errorf("Attempt 已处于终态 %s，不能更新为 %s", current, record.Status)
	}
	return nil
}

// SettleAttemptHealth 在同一事务内写入 Attempt 终态、健康结算和对应路由事件。
func SettleAttemptHealth(database *sql.DB, attempt AttemptRecord, health HealthSettleInput, channelName string) (HealthResult, error) {
	if database == nil {
		return HealthResult{}, fmt.Errorf("Attempt 健康结算数据库无效")
	}
	if health.OccurredAt.IsZero() {
		health.OccurredAt = time.Now()
	}
	transaction, err := database.Begin()
	if err != nil {
		return HealthResult{}, fmt.Errorf("开始 Attempt 健康结算事务失败: %w", err)
	}
	defer transaction.Rollback()
	if err := updateAttempt(transaction, attempt); err != nil {
		return HealthResult{}, err
	}
	if attempt.Sequence == 1 && health.Outcome != HealthNotSent {
		if _, err := transaction.Exec(`UPDATE requests SET initial_channel_id = ?, initial_channel_name = ?, group_name = COALESCE(NULLIF(?, ''), group_name) WHERE id = ? AND initial_channel_id = ''`, attempt.ChannelID, channelName, attempt.GroupName, attempt.RequestID); err != nil {
			return HealthResult{}, fmt.Errorf("回填请求首次渠道失败: %w", err)
		}
	}
	result, err := settleChannelHealthTx(transaction, health)
	if err != nil {
		return result, err
	}
	if result.Applied {
		details := map[string]any{
			"attemptID":               attempt.ID,
			"channelName":             channelName,
			"fromState":               result.FromState,
			"toState":                 result.ToState,
			"failureCountBefore":      result.FailureCountBefore,
			"failureCountAfter":       result.FailureCountAfter,
			"probeFailureCountBefore": result.ProbeFailureCountBefore,
			"probeFailureCountAfter":  result.ProbeFailureCountAfter,
			"probeSuccessCountBefore": result.ProbeSuccessCountBefore,
			"probeSuccessCountAfter":  result.ProbeSuccessCountAfter,
			"healthVersion":           result.HealthVersion,
			"cooldownUntil":           result.CooldownUntil,
		}
		if _, err := recordRequestRouteEventTx(transaction, RouteEvent{
			RequestID:  attempt.RequestID,
			EventType:  "health_changed",
			ChannelID:  health.ChannelID,
			Reason:     health.ErrorClass,
			OccurredAt: FormatSQLiteTime(health.OccurredAt),
		}, details); err != nil {
			return result, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("提交 Attempt 健康结算事务失败: %w", err)
	}
	return result, nil
}

func validAttemptFinalStatus(status string) bool {
	switch status {
	case "success", "failed", "timeout", "cancelled", "stream_interrupted":
		return true
	default:
		return false
	}
}

// FinishRequest 写入逻辑请求最终状态和完成时间。
func FinishRequest(database *sql.DB, requestID, status string, completedAt time.Time) error {
	return FinishRequestWithMeta(database, requestID, status, completedAt, RequestFinishMeta{})
}

// RequestFinishMeta 保存完成时提取到的 token 和请求延迟。
type RequestFinishMeta struct {
	InputTokens           *int64
	OutputTokens          *int64
	CacheReadInputTokens  *int64
	CacheWriteInputTokens *int64
	ReasoningTokens       *int64
	TotalTokens           *int64
	CacheRate             *float64
	TTFT                  *time.Duration
	TPS                   *float64
	StreamEnded           *bool
	FinalHTTPStatus       *int
	FinalChannelID        string
	FinalChannelName      string
	AttemptCount          int
	FallbackTriggered     *bool
	ErrorClass            string
	ErrorMessage          string
	Latency               time.Duration
}

// FinishRequestWithMeta 更新最终状态、延迟和可用 token 元数据。
func FinishRequestWithMeta(database *sql.DB, requestID, status string, completedAt time.Time, meta RequestFinishMeta) error {
	if database == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(status) == "" {
		return fmt.Errorf("请求完成账本字段无效")
	}
	switch status {
	case "success", "error", "timeout", "cancelled", "partial":
	default:
		return fmt.Errorf("请求最终状态无效: %s", status)
	}
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("开始请求终态事务失败: %w", err)
	}
	defer transaction.Rollback()
	var startedAt string
	err = transaction.QueryRow(`SELECT started_at FROM requests WHERE id = ?`, requestID).Scan(&startedAt)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("读取请求开始时间失败: %w", err)
	}
	result, err := transaction.Exec(`UPDATE requests SET final_status = ?, completed_at = ?, input_tokens = COALESCE(?, input_tokens), output_tokens = COALESCE(?, output_tokens), cache_read_input_tokens = COALESCE(?, cache_read_input_tokens), cache_write_input_tokens = COALESCE(?, cache_write_input_tokens), reasoning_tokens = COALESCE(?, reasoning_tokens), total_tokens = COALESCE(?, total_tokens), cache_rate = COALESCE(?, cache_rate), ttft_ms = COALESCE(?, ttft_ms), tps = COALESCE(?, tps), stream_ended = COALESCE(?, stream_ended), final_http_status = COALESCE(?, final_http_status), final_channel_id = COALESCE(NULLIF(?, ''), final_channel_id), final_channel_name = COALESCE(NULLIF(?, ''), final_channel_name), attempt_count = CASE WHEN ? > 0 THEN ? ELSE attempt_count END, fallback_triggered = COALESCE(?, fallback_triggered), error_class = CASE WHEN ? <> '' THEN ? ELSE error_class END, error_message = CASE WHEN ? <> '' THEN ? ELSE error_message END, latency_ms = ? WHERE id = ? AND final_status = 'processing'`, status, FormatSQLiteTime(completedAt), meta.InputTokens, meta.OutputTokens, meta.CacheReadInputTokens, meta.CacheWriteInputTokens, meta.ReasoningTokens, meta.TotalTokens, meta.CacheRate, durationMillis(meta.TTFT), meta.TPS, nullableBool(meta.StreamEnded), meta.FinalHTTPStatus, meta.FinalChannelID, meta.FinalChannelName, meta.AttemptCount, meta.AttemptCount, nullableBool(meta.FallbackTriggered), meta.ErrorClass, meta.ErrorClass, meta.ErrorMessage, meta.ErrorMessage, meta.Latency.Milliseconds(), requestID)
	if err != nil {
		return fmt.Errorf("更新请求账本失败: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var current string
		if err := transaction.QueryRow(`SELECT final_status FROM requests WHERE id = ?`, requestID).Scan(&current); err == sql.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("读取请求终态失败: %w", err)
		} else if current == status {
			return nil
		}
		return fmt.Errorf("请求已处于终态 %s，不能更新为 %s", current, status)
	}
	if err := markHourlyMetricDirtyTx(transaction, startedAt); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交请求终态失败: %w", err)
	}
	return nil
}

const requestDetailSelect = `id, protocol, client_model, logical_model, final_status, group_name, initial_channel_id, initial_channel_name, final_channel_id, final_channel_name, fallback_triggered, attempt_count, started_at, completed_at, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, cache_rate, ttft_ms, tps, stream_ended, final_http_status, error_class, error_message, latency_ms`

type requestRowScanner interface {
	Scan(dest ...any) error
}

// scanRequestDetail 读取 requests 表一行的列表/详情公共字段。
func scanRequestDetail(scanner requestRowScanner) (RequestDetail, error) {
	var detail RequestDetail
	var completed, firstChannelID, firstChannelName, finalChannelID, finalChannelName, groupName, errorClass, errorMessage sql.NullString
	var input, output, cache, cacheWrite, reasoning, total sql.NullInt64
	var ttft sql.NullInt64
	var fallback, attemptCount sql.NullInt64
	var cacheRate, tps sql.NullFloat64
	var streamEnded sql.NullInt64
	var finalHTTPStatus sql.NullInt64
	if err := scanner.Scan(&detail.ID, &detail.Protocol, &detail.ClientModel, &detail.LogicalModel, &detail.FinalStatus, &groupName, &firstChannelID, &firstChannelName, &finalChannelID, &finalChannelName, &fallback, &attemptCount, &detail.StartedAt, &completed, &input, &output, &cache, &cacheWrite, &reasoning, &total, &cacheRate, &ttft, &tps, &streamEnded, &finalHTTPStatus, &errorClass, &errorMessage, &detail.LatencyMs); err != nil {
		return RequestDetail{}, err
	}
	detail.CompletedAt = completed.String
	detail.GroupName = groupName.String
	detail.InitialChannelID, detail.InitialChannelName = firstChannelID.String, firstChannelName.String
	detail.FinalChannelID, detail.FinalChannelName = finalChannelID.String, finalChannelName.String
	detail.FallbackTriggered, detail.AttemptCount = fallback.Int64 != 0, int(attemptCount.Int64)
	detail.InputTokens = nullableInt64(input)
	detail.OutputTokens = nullableInt64(output)
	detail.CacheReadInputTokens = nullableInt64(cache)
	detail.CacheWriteInputTokens = nullableInt64(cacheWrite)
	detail.ReasoningTokens = nullableInt64(reasoning)
	detail.TotalTokens = nullableInt64(total)
	detail.CacheRate = nullableFloat64(cacheRate)
	detail.TTFTMs = nullableInt64(ttft)
	detail.TPS = nullableFloat64(tps)
	detail.StreamEnded = nullableBoolPtr(streamEnded)
	detail.FinalHTTPStatus = nullableIntValue(finalHTTPStatus)
	detail.ErrorClass, detail.ErrorMessage = errorClass.String, errorMessage.String
	return detail, nil
}

// GetRequest 返回请求及其所有渠道尝试，按 sequence 升序排列。
func GetRequest(database *sql.DB, requestID string) (RequestDetail, error) {
	detail, err := scanRequestDetail(database.QueryRow(`SELECT `+requestDetailSelect+` FROM requests WHERE id = ?`, requestID))
	if err == sql.ErrNoRows {
		return RequestDetail{}, ErrNotFound
	}
	if err != nil {
		return RequestDetail{}, fmt.Errorf("读取请求账本失败: %w", err)
	}
	detail.Attempts, err = listAttemptDetails(database, requestID)
	if err != nil {
		return RequestDetail{}, err
	}
	detail.RouteEvents, err = ListRequestRouteEvents(database, requestID)
	if err != nil {
		return RequestDetail{}, err
	}
	decorateRequestRouting(&detail)
	detail.ContentBlobs, err = ListContentBlobs(database, requestID)
	if err != nil {
		return RequestDetail{}, err
	}
	return detail, nil
}

// RecordRequestRouteEvent 按请求内顺序追加一条结构化路由事件。
func RecordRequestRouteEvent(database *sql.DB, event RouteEvent, details any) (RouteEvent, error) {
	if database == nil {
		return RouteEvent{}, fmt.Errorf("路由事件请求 ID 无效")
	}
	transaction, err := database.Begin()
	if err != nil {
		return RouteEvent{}, fmt.Errorf("开始路由事件事务失败: %w", err)
	}
	defer transaction.Rollback()
	event, err = recordRequestRouteEventTx(transaction, event, details)
	if err != nil {
		return RouteEvent{}, err
	}
	if err := transaction.Commit(); err != nil {
		return RouteEvent{}, fmt.Errorf("提交路由事件失败: %w", err)
	}
	return event, nil
}

func recordRequestRouteEventTx(transaction *sql.Tx, event RouteEvent, details any) (RouteEvent, error) {
	if strings.TrimSpace(event.RequestID) == "" {
		return RouteEvent{}, fmt.Errorf("路由事件请求 ID 无效")
	}
	switch event.EventType {
	case "candidate_skipped", "channel_switched", "health_changed", "routing_exhausted", "custom_headers_ignored":
	default:
		return RouteEvent{}, fmt.Errorf("路由事件类型无效: %s", event.EventType)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return RouteEvent{}, fmt.Errorf("序列化路由事件详情失败: %w", err)
	}
	if details == nil {
		encoded = []byte(`{}`)
	}
	if event.OccurredAt == "" {
		event.OccurredAt = FormatSQLiteTime(time.Now())
	}
	if event.Sequence <= 0 {
		if err := transaction.QueryRow(`SELECT COALESCE(MAX(sequence), 0) + 1 FROM request_route_events WHERE request_id = ?`, event.RequestID).Scan(&event.Sequence); err != nil {
			return RouteEvent{}, fmt.Errorf("读取路由事件顺序失败: %w", err)
		}
	}
	if _, err := transaction.Exec(`INSERT INTO request_route_events(request_id, sequence, event_type, channel_id, related_channel_id, reason, details_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, event.RequestID, event.Sequence, event.EventType, event.ChannelID, event.RelatedChannelID, event.Reason, string(encoded), event.OccurredAt); err != nil {
		return RouteEvent{}, fmt.Errorf("写入路由事件失败: %w", err)
	}
	event.Details = json.RawMessage(encoded)
	return event, nil
}

// ListRequestRouteEvents 返回请求内按顺序排列的路由事件。
func ListRequestRouteEvents(database *sql.DB, requestID string) ([]RouteEvent, error) {
	rows, err := database.Query(`SELECT request_id, sequence, event_type, channel_id, related_channel_id, reason, details_json, occurred_at FROM request_route_events WHERE request_id = ? ORDER BY sequence ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("读取路由事件失败: %w", err)
	}
	defer rows.Close()
	events := make([]RouteEvent, 0)
	for rows.Next() {
		var event RouteEvent
		var details string
		if err := rows.Scan(&event.RequestID, &event.Sequence, &event.EventType, &event.ChannelID, &event.RelatedChannelID, &event.Reason, &details, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("读取路由事件字段失败: %w", err)
		}
		if !json.Valid([]byte(details)) {
			details = `{}`
		}
		event.Details = json.RawMessage(details)
		events = append(events, event)
	}
	return events, rows.Err()
}

// decorateRequestRouting 从 Attempt 链路派生列表所需的渠道路径和跨渠道切换次数。
func decorateRequestRouting(detail *RequestDetail) {
	if detail == nil {
		return
	}
	detail.ChannelChain = make([]string, 0, len(detail.Attempts))
	for index, attempt := range detail.Attempts {
		name := strings.TrimSpace(attempt.ChannelName)
		if name == "" {
			name = attempt.ChannelID
		}
		if name != "" {
			detail.ChannelChain = append(detail.ChannelChain, name)
		}
		if index > 0 && attempt.ChannelID != detail.Attempts[index-1].ChannelID {
			detail.ChannelSwitchCount++
		}
	}
}

// ListRequests 返回最近的请求明细摘要。
func ListRequests(database *sql.DB, limit int) ([]RequestDetail, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := database.Query(`SELECT id FROM requests ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取请求列表失败: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]RequestDetail, 0, len(ids))
	for _, id := range ids {
		item, err := GetRequest(database, id)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

// ListRequestsPage 返回按开始时间倒序排列的请求日志分页结果。
func ListRequestsPage(database *sql.DB, filter RequestListFilter, page, pageSize int) (RequestPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize != 20 && pageSize != 50 && pageSize != 100 {
		pageSize = 50
	}
	where, args := requestFilterSQL(filter)
	var total int64
	if err := database.QueryRow("SELECT COUNT(*) FROM requests r "+where, args...).Scan(&total); err != nil {
		return RequestPage{}, fmt.Errorf("统计请求日志失败: %w", err)
	}
	rows, err := database.Query("SELECT r."+strings.ReplaceAll(requestDetailSelect, ", ", ", r.")+" FROM requests r "+where+" ORDER BY r.started_at DESC, r.id DESC LIMIT ? OFFSET ?", append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return RequestPage{}, fmt.Errorf("读取请求日志分页失败: %w", err)
	}
	defer rows.Close()
	items := make([]RequestDetail, 0, pageSize)
	ids := make([]string, 0, pageSize)
	for rows.Next() {
		item, err := scanRequestDetail(rows)
		if err != nil {
			return RequestPage{}, fmt.Errorf("读取请求日志字段失败: %w", err)
		}
		items = append(items, item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		return RequestPage{}, err
	}
	attemptsByRequest, err := listAttemptRoutingByRequestIDs(database, ids)
	if err != nil {
		return RequestPage{}, err
	}
	for index := range items {
		items[index].Attempts = attemptsByRequest[items[index].ID]
		decorateRequestRouting(&items[index])
		items[index].Attempts = nil
		items[index].RouteEvents = nil
		items[index].ContentBlobs = nil
	}
	return RequestPage{Items: items, Page: page, PageSize: pageSize, Total: total, HasMore: int64(page*pageSize) < total}, nil
}

// listAttemptRoutingByRequestIDs 一次读取本页 Attempt 的渠道顺序，供列表拼链路。
func listAttemptRoutingByRequestIDs(database *sql.DB, requestIDs []string) (map[string][]AttemptDetail, error) {
	result := make(map[string][]AttemptDetail, len(requestIDs))
	if len(requestIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(requestIDs)), ",")
	args := make([]any, 0, len(requestIDs))
	for _, id := range requestIDs {
		args = append(args, id)
	}
	rows, err := database.Query(`SELECT request_id, channel_id, channel_name, sequence FROM attempts WHERE request_id IN (`+placeholders+`) ORDER BY request_id, sequence ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("批量读取请求尝试失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item AttemptDetail
		if err := rows.Scan(&item.RequestID, &item.ChannelID, &item.ChannelName, &item.Sequence); err != nil {
			return nil, fmt.Errorf("读取请求尝试渠道字段失败: %w", err)
		}
		result[item.RequestID] = append(result[item.RequestID], item)
	}
	return result, rows.Err()
}

func requestFilterSQL(filter RequestListFilter) (string, []any) {
	clauses := make([]string, 0, 10)
	args := make([]any, 0, 10)
	if from := strings.TrimSpace(filter.From); from != "" {
		clauses = append(clauses, "r.started_at >= ?")
		args = append(args, requestFilterTimeParam(from))
	}
	if to := strings.TrimSpace(filter.To); to != "" {
		clauses = append(clauses, "r.started_at < ?")
		args = append(args, requestFilterTimeParam(to))
	}
	if value := strings.TrimSpace(filter.Model); value != "" {
		like := likeContainsPattern(value)
		clauses = append(clauses, "(r.client_model LIKE ? ESCAPE '\\' OR r.logical_model LIKE ? ESCAPE '\\')")
		args = append(args, like, like)
	}
	if value := strings.TrimSpace(filter.Protocol); value != "" {
		clauses = append(clauses, "r.protocol = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Status); value != "" {
		clauses = append(clauses, "r.final_status = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Channel); value != "" {
		clauses = append(clauses, "(r.initial_channel_id = ? OR r.final_channel_id = ? OR EXISTS (SELECT 1 FROM attempts af WHERE af.request_id = r.id AND (af.channel_id = ? OR af.channel_name LIKE ? ESCAPE '\\')))")
		args = append(args, value, value, value, likeContainsPattern(value))
	}
	if value := strings.TrimSpace(filter.Group); value != "" {
		clauses = append(clauses, "r.group_name LIKE ? ESCAPE '\\'")
		args = append(args, likeContainsPattern(value))
	}
	if value := strings.TrimSpace(filter.ErrorClass); value != "" {
		clauses = append(clauses, "(r.error_class = ? OR EXISTS (SELECT 1 FROM attempts ae WHERE ae.request_id = r.id AND ae.error_class = ?))")
		args = append(args, value, value)
	}
	if filter.Fallback == "yes" {
		clauses = append(clauses, "r.fallback_triggered = 1")
	} else if filter.Fallback == "no" {
		clauses = append(clauses, "r.fallback_triggered = 0")
	}
	if value := strings.TrimSpace(filter.Keyword); value != "" {
		like := likeContainsPattern(value)
		clauses = append(clauses, "(r.id LIKE ? ESCAPE '\\' OR r.client_model LIKE ? ESCAPE '\\' OR r.logical_model LIKE ? ESCAPE '\\' OR r.error_message LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// requestFilterTimeParam 将列表窗口 From/To 规范为 UTC 固定九位小数文本。
// 解析兼容 RFC3339 与 RFC3339Nano；失败时保留原字符串，避免静默丢掉过滤。
func requestFilterTimeParam(value string) string {
	parsed, err := ParseSQLiteTime(value)
	if err != nil {
		return value
	}
	return FormatSQLiteTime(parsed)
}

// likeContainsPattern 转义 LIKE 通配符后包成包含匹配。
func likeContainsPattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}

func listAttemptDetails(database *sql.DB, requestID string) ([]AttemptDetail, error) {
	rows, err := database.Query(`SELECT id, request_id, channel_id, channel_name, group_name, protocol, client_model, upstream_model, logical_model, sequence, status, error_class, error_message, retry_reason, fallback_triggered, started_at, first_byte_at, first_event_at, completed_at, http_status, latency_ms, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, request_content_blob_id, response_content_blob_id, stream_ended FROM attempts WHERE request_id = ? ORDER BY sequence ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("读取请求尝试失败: %w", err)
	}
	defer rows.Close()
	result := make([]AttemptDetail, 0)
	for rows.Next() {
		var item AttemptDetail
		var input, output, cache, cacheWrite, reasoning, total sql.NullInt64
		var fallback, streamEnded sql.NullInt64
		var started, firstByte, firstEvent, completed sql.NullString
		if err := rows.Scan(&item.ID, &item.RequestID, &item.ChannelID, &item.ChannelName, &item.GroupName, &item.Protocol, &item.ClientModel, &item.UpstreamModel, &item.LogicalModel, &item.Sequence, &item.Status, &item.ErrorClass, &item.ErrorMessage, &item.RetryReason, &fallback, &started, &firstByte, &firstEvent, &completed, &item.HTTPStatus, &item.LatencyMs, &input, &output, &cache, &cacheWrite, &reasoning, &total, &item.RequestContentBlobID, &item.ResponseContentBlobID, &streamEnded); err != nil {
			return nil, fmt.Errorf("读取请求尝试字段失败: %w", err)
		}
		item.InputTokens = nullableInt64(input)
		item.OutputTokens = nullableInt64(output)
		item.CacheReadInputTokens = nullableInt64(cache)
		item.CacheWriteInputTokens = nullableInt64(cacheWrite)
		item.ReasoningTokens = nullableInt64(reasoning)
		item.TotalTokens = nullableInt64(total)
		item.FallbackTriggered = fallback.Int64 != 0
		item.StartedAt, item.FirstByteAt, item.FirstEventAt, item.CompletedAt = started.String, firstByte.String, firstEvent.String, completed.String
		item.StreamEnded = nullableBoolPtr(streamEnded)
		result = append(result, item)
	}
	return result, rows.Err()
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableFloat64(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func nullableIntValue(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	if *value {
		return 1
	}
	return 0
}

func nullableBoolPtr(value sql.NullInt64) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Int64 != 0
	return &result
}

func optionalTimeString(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return FormatSQLiteTime(*value)
}

func durationMillis(value *time.Duration) any {
	if value == nil {
		return nil
	}
	return value.Milliseconds()
}

// NewAuditLogID 创建审计记录 ID。
func NewAuditLogID() (string, error) { return newID("audit_") }

// AuditLog 表示不含秘密和正文的管理操作审计记录。
type AuditLog struct {
	ID         string          `json:"id"`
	ActorType  string          `json:"actorType"`
	Action     string          `json:"action"`
	TargetID   string          `json:"targetId"`
	Category   string          `json:"category,omitempty"`
	Result     string          `json:"result"`
	OperatorIP string          `json:"operatorIp,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	OccurredAt string          `json:"occurredAt"`
}

// RecordAuditLog 写入管理操作审计记录。
func RecordAuditLog(database *sql.DB, log AuditLog) error {
	if database == nil || strings.TrimSpace(log.ID) == "" || strings.TrimSpace(log.ActorType) == "" || strings.TrimSpace(log.Action) == "" {
		return fmt.Errorf("审计记录字段无效")
	}
	if log.OccurredAt == "" {
		log.OccurredAt = FormatSQLiteTime(time.Now())
	}
	details, err := sanitizeAuditDetails(log.Details)
	if err != nil {
		return fmt.Errorf("审计详情必须是 JSON")
	}
	if log.Result == "" {
		log.Result = "success"
	}
	if log.Category == "" {
		log.Category = auditCategory(log.Action)
	}
	_, err = database.Exec(`INSERT INTO audit_logs(id, actor_type, action, target_id, category, result, operator_ip, details_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, log.ID, log.ActorType, log.Action, log.TargetID, log.Category, log.Result, log.OperatorIP, details, log.OccurredAt)
	if err != nil {
		return fmt.Errorf("写入审计记录失败: %w", err)
	}
	return nil
}

// auditCategory 将内部 action 前缀归一为稳定的管理日志分类。
func auditCategory(action string) string {
	prefix := strings.ToLower(strings.TrimSpace(strings.SplitN(action, ".", 2)[0]))
	switch prefix {
	case "channels", "channel":
		return "channels"
	case "models", "model":
		return "models"
	case "probes", "probe":
		return "probes"
	case "auth", "security", "secure":
		return "auth"
	case "config", "settings":
		return "config"
	case "logs", "log":
		return "logs"
	default:
		return prefix
	}
}

// sanitizeAuditDetails 在审计存储边界递归遮罩秘密、请求头和正文字段。
func sanitizeAuditDetails(raw json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "{}", nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	redactAuditValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func redactAuditValue(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", "-"), " ", "-"))
			if strings.Contains(normalized, "authorization") || strings.Contains(normalized, "api-key") || strings.Contains(normalized, "apikey") || strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "password") || strings.Contains(normalized, "cookie") || strings.Contains(normalized, "header") || strings.Contains(normalized, "body") || strings.Contains(normalized, "credential") {
				current[key] = "***"
				continue
			}
			redactAuditValue(child)
		}
	case []any:
		for _, child := range current {
			redactAuditValue(child)
		}
	}
}

// ListAuditLogs 返回最近的审计记录。
func ListAuditLogs(database *sql.DB, limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := database.Query(`SELECT id, actor_type, action, target_id, category, result, operator_ip, details_json, occurred_at FROM audit_logs ORDER BY occurred_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读取审计记录失败: %w", err)
	}
	defer rows.Close()
	result := make([]AuditLog, 0)
	for rows.Next() {
		var item AuditLog
		var details string
		if err := rows.Scan(&item.ID, &item.ActorType, &item.Action, &item.TargetID, &item.Category, &item.Result, &item.OperatorIP, &details, &item.OccurredAt); err != nil {
			return nil, err
		}
		if sanitized, err := sanitizeAuditDetails(json.RawMessage(details)); err == nil {
			item.Details = json.RawMessage(sanitized)
		} else {
			item.Details = json.RawMessage(`{}`)
		}
		if item.Category == "" {
			item.Category = auditCategory(item.Action)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// AuditListFilter 是操作日志列表筛选条件。
type AuditListFilter struct{ From, To, Category, Result, Target, OperatorIP string }

// AuditPage 是操作日志分页响应。
type AuditPage struct {
	Items    []AuditLog `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
	Total    int64      `json:"total"`
	HasMore  bool       `json:"hasMore"`
}

// ListAuditLogsPage 返回带筛选的操作日志分页结果。
func ListAuditLogsPage(database *sql.DB, filter AuditListFilter, page, pageSize int) (AuditPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize != 20 && pageSize != 50 && pageSize != 100 {
		pageSize = 50
	}
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if filter.From != "" {
		clauses = append(clauses, "occurred_at >= ?")
		args = append(args, filter.From)
	}
	if filter.To != "" {
		clauses = append(clauses, "occurred_at < ?")
		args = append(args, filter.To)
	}
	if filter.Category != "" {
		clauses = append(clauses, "category = ?")
		args = append(args, filter.Category)
	}
	if filter.Result != "" {
		clauses = append(clauses, "result = ?")
		args = append(args, filter.Result)
	}
	if filter.Target != "" {
		clauses = append(clauses, "target_id LIKE ?")
		args = append(args, "%"+filter.Target+"%")
	}
	if filter.OperatorIP != "" {
		clauses = append(clauses, "operator_ip = ?")
		args = append(args, filter.OperatorIP)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int64
	if err := database.QueryRow("SELECT COUNT(*) FROM audit_logs"+where, args...).Scan(&total); err != nil {
		return AuditPage{}, err
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := database.Query("SELECT id, actor_type, action, target_id, category, result, operator_ip, details_json, occurred_at FROM audit_logs"+where+" ORDER BY occurred_at DESC, id DESC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	items := make([]AuditLog, 0, pageSize)
	for rows.Next() {
		var item AuditLog
		var details string
		if err := rows.Scan(&item.ID, &item.ActorType, &item.Action, &item.TargetID, &item.Category, &item.Result, &item.OperatorIP, &details, &item.OccurredAt); err != nil {
			return AuditPage{}, err
		}
		if sanitized, err := sanitizeAuditDetails(json.RawMessage(details)); err == nil {
			item.Details = json.RawMessage(sanitized)
		} else {
			item.Details = json.RawMessage(`{}`)
		}
		if item.Category == "" {
			item.Category = auditCategory(item.Action)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, err
	}
	return AuditPage{Items: items, Page: page, PageSize: pageSize, Total: total, HasMore: int64(page*pageSize) < total}, nil
}
