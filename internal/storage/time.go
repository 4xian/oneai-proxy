package storage

import "time"

// sqliteTimeLayout 是写入 SQLite TEXT 时间列的固定格式。
// Go 的 RFC3339Nano 会裁掉 0 纳秒小数，字典序会把同秒内的 .123Z 排到 Z 之前。
const sqliteTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatSQLiteTime 将时间格式化为 UTC 固定九位小数，供 TEXT 比较使用。
func FormatSQLiteTime(value time.Time) string {
	return value.UTC().Format(sqliteTimeLayout)
}

// ParseSQLiteTime 解析 RFC3339 或 RFC3339Nano 文本，并规范为 UTC。
func ParseSQLiteTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}
