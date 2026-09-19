package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RuntimeLog 表示一条不含秘密正文的后端运行事件。
type RuntimeLog struct {
	ID         string          `json:"id"`
	OccurredAt string          `json:"occurredAt"`
	Level      string          `json:"level"`
	Event      string          `json:"event"`
	Message    string          `json:"message"`
	RequestID  string          `json:"requestId,omitempty"`
	AttemptID  string          `json:"attemptId,omitempty"`
	ChannelID  string          `json:"channelId,omitempty"`
	Context    json.RawMessage `json:"context,omitempty"`
}

// NewRuntimeLogID 创建运行事件 ID。
func NewRuntimeLogID() (string, error) { return newID("run_") }

// RecordRuntimeLog 持久化一条运行事件，调用方负责向订阅者发布。
func RecordRuntimeLog(database *sql.DB, log RuntimeLog) error {
	if database == nil || strings.TrimSpace(log.ID) == "" || strings.TrimSpace(log.Level) == "" || strings.TrimSpace(log.Event) == "" {
		return fmt.Errorf("运行日志字段无效")
	}
	if log.OccurredAt == "" {
		log.OccurredAt = FormatSQLiteTime(time.Now())
	} else {
		occurredAt, err := time.Parse(time.RFC3339Nano, log.OccurredAt)
		if err != nil {
			return fmt.Errorf("运行日志时间无效: %w", err)
		}
		// 统一为 UTC RFC3339Nano，保证 SQLite 文本排序保留纳秒精度。
		log.OccurredAt = FormatSQLiteTime(occurredAt)
	}
	contextJSON := strings.TrimSpace(string(log.Context))
	if contextJSON == "" {
		contextJSON = "{}"
	}
	if !json.Valid([]byte(contextJSON)) {
		return fmt.Errorf("运行日志上下文必须是 JSON")
	}
	_, err := database.Exec(`INSERT INTO runtime_logs(id, occurred_at, level, event, message, request_id, attempt_id, channel_id, context_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, log.ID, log.OccurredAt, log.Level, log.Event, log.Message, log.RequestID, log.AttemptID, log.ChannelID, contextJSON)
	if err != nil {
		return fmt.Errorf("写入运行日志失败: %w", err)
	}
	return nil
}

// ListRuntimeLogs 返回最近的运行事件，可按 ID 补发后续事件。
// usedLimit 是夹取后的实际查询条数；非法或超过 2000 的 limit 会被夹成 100。
func ListRuntimeLogs(database *sql.DB, limit int, afterID string) ([]RuntimeLog, int, error) {
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	items, err := listRuntimeLogs(database, limit, afterID)
	return items, limit, err
}

// ListRuntimeLogsForStream 返回 SSE 游标后的事件，并标记是否超过前端可见上限。
func ListRuntimeLogsForStream(database *sql.DB, afterID string) ([]RuntimeLog, bool, error) {
	if strings.TrimSpace(afterID) == "" {
		return nil, false, nil
	}
	result, err := listRuntimeLogs(database, 2001, afterID)
	if err != nil {
		return nil, false, err
	}
	if len(result) > 2000 {
		return result[:2000], true, nil
	}
	return result, false, nil
}

func listRuntimeLogs(database *sql.DB, limit int, afterID string) ([]RuntimeLog, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, occurred_at, level, event, message, request_id, attempt_id, channel_id, context_json FROM runtime_logs`
	args := make([]any, 0, 2)
	if strings.TrimSpace(afterID) != "" {
		query += ` WHERE (occurred_at > (SELECT occurred_at FROM runtime_logs WHERE id = ?) OR (occurred_at = (SELECT occurred_at FROM runtime_logs WHERE id = ?) AND id > ?))`
		args = append(args, afterID, afterID, afterID)
	}
	query += ` ORDER BY occurred_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取运行日志失败: %w", err)
	}
	defer rows.Close()
	result := make([]RuntimeLog, 0, limit)
	for rows.Next() {
		var item RuntimeLog
		var contextJSON string
		if err := rows.Scan(&item.ID, &item.OccurredAt, &item.Level, &item.Event, &item.Message, &item.RequestID, &item.AttemptID, &item.ChannelID, &contextJSON); err != nil {
			return nil, err
		}
		item.Context = json.RawMessage(contextJSON)
		result = append(result, item)
	}
	return result, rows.Err()
}

// RuntimeLogExists 判断 SSE 补发游标是否仍在保留期内。
func RuntimeLogExists(database *sql.DB, id string) (bool, error) {
	if database == nil || strings.TrimSpace(id) == "" {
		return false, nil
	}
	var exists int
	err := database.QueryRow(`SELECT EXISTS(SELECT 1 FROM runtime_logs WHERE id = ?)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查运行日志游标失败: %w", err)
	}
	return exists == 1, nil
}

// CleanupRuntimeLogs 删除截止时间之前的运行事件。
func CleanupRuntimeLogs(database *sql.DB, cutoff time.Time) (int64, error) {
	result, err := database.Exec(`DELETE FROM runtime_logs WHERE occurred_at < ?`, FormatSQLiteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("清理运行日志失败: %w", err)
	}
	count, _ := result.RowsAffected()
	return count, nil
}
