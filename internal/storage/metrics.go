package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

var metricHistogramBounds = []int64{25, 50, 100, 250, 500, 1000, 2000, 5000, 10000, 30000, 60000}

// StatsSummary 是时间窗口内的业务与探针统计；未知 token/cache 使用 nil 表示。
type StatsSummary struct {
	WindowDays                   int      `json:"windowDays"`
	From                         string   `json:"from"`
	To                           string   `json:"to"`
	RequestCount                 int64    `json:"requestCount"`
	RequestSuccessCount          int64    `json:"requestSuccessCount"`
	RequestFailureCount          int64    `json:"requestFailureCount"`
	FallbackRequestCount         int64    `json:"fallbackRequestCount"`
	FallbackEligibleRequestCount int64    `json:"fallbackEligibleRequestCount"`
	AttemptCount                 int64    `json:"attemptCount"`
	AttemptSuccessCount          int64    `json:"attemptSuccessCount"`
	ProbeCount                   int64    `json:"probeCount"`
	ProbeSuccessCount            int64    `json:"probeSuccessCount"`
	InputTokens                  *int64   `json:"inputTokens"`
	OutputTokens                 *int64   `json:"outputTokens"`
	CacheReadInputTokens         *int64   `json:"cacheReadInputTokens"`
	CacheWriteInputTokens        *int64   `json:"cacheWriteInputTokens"`
	ReasoningTokens              *int64   `json:"reasoningTokens"`
	AverageLatencyMs             float64  `json:"averageLatencyMs"`
	SuccessRate                  float64  `json:"successRate"`
	FallbackRate                 float64  `json:"fallbackRate"`
	CacheRate                    *float64 `json:"cacheRate"`
	AverageTTFTMs                *float64 `json:"averageTTFTMs"`
	P95TTFTMs                    *float64 `json:"p95TTFTMs"`
	P95TTFTOverflow              bool     `json:"p95TTFTOverflow"`
	AverageTPS                   *float64 `json:"averageTPS"`
	TotalTokens                  *int64   `json:"totalTokens"`
}

type metricAccumulator struct {
	RequestCount, RequestSuccessCount, RequestFailureCount, FallbackRequestCount     int64
	FallbackEligibleRequestCount                                                     int64
	AttemptCount, AttemptSuccessCount, ProbeCount, ProbeSuccessCount                 int64
	InputTokens, OutputTokens, CacheReadInputTokens                                  int64
	CacheWriteInputTokens, ReasoningTokens, TotalTokens                              int64
	InputKnown, OutputKnown, CacheKnown, CacheWriteKnown, ReasoningKnown, TotalKnown bool
	RequestLatencyTotal, RequestLatencyCount                                         int64
	AttemptLatencyTotal, AttemptLatencyCount                                         int64
	TTFTTotal, TTFTCount, TPSCount                                                   int64
	TPSTotal                                                                         float64
	RequestLatencyHistogram, AttemptLatencyHistogram, TTFTHistogram                  []int64
}

// requestMetric 保存一次逻辑请求参与聚合所需的字段。
type requestMetric struct {
	ID, Protocol, LogicalModel, FinalChannelID, Status, ErrorClass string
	Latency                                                        int64
	Input, Output, Cache, CacheWrite, Reasoning, TotalTokens, TTFT sql.NullInt64
	TPS                                                            sql.NullFloat64
}

// metricDimensions 按维度类型和维度 ID 保存独立累加器。
type metricDimensions map[string]map[string]*metricAccumulator

// AggregateHourlyMetrics 聚合当前 UTC 小时的请求、尝试和探针数据。
func AggregateHourlyMetrics(database *sql.DB, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return aggregateBucket(database, now.UTC().Truncate(time.Hour))
}

// AggregateAllMetrics 重新聚合明细中的所有完整小时，供手动全量重算使用。
func AggregateAllMetrics(database *sql.DB) error {
	return aggregateMetricBuckets(database, false, time.Time{})
}

// AggregateMissingMetrics 只补齐尚未写入聚合表的完整小时，避免热路径重算全部历史。
func AggregateMissingMetrics(database *sql.DB, now time.Time) error {
	return aggregateMetricBuckets(database, true, now)
}

func aggregateMetricBuckets(database *sql.DB, missingOnly bool, now time.Time) error {
	query := `SELECT DISTINCT substr(started_at, 1, 13) || ':00:00Z' AS bucket FROM requests UNION SELECT DISTINCT substr(occurred_at, 1, 13) || ':00:00Z' AS bucket FROM probe_runs`
	if missingOnly {
		if now.IsZero() {
			now = time.Now().UTC()
		}
		query = `
SELECT bucket FROM (
  SELECT substr(started_at, 1, 13) || ':00:00Z' AS bucket FROM requests
  UNION
  SELECT substr(occurred_at, 1, 13) || ':00:00Z' AS bucket FROM probe_runs
  UNION
  SELECT bucket_start_utc AS bucket FROM hourly_metric_dirty
)
WHERE bucket < ? AND (
  NOT EXISTS (SELECT 1 FROM hourly_metrics WHERE bucket_start_utc = bucket)
  OR EXISTS (SELECT 1 FROM hourly_metric_dirty WHERE bucket_start_utc = bucket)
)`
	}
	var rows *sql.Rows
	var err error
	if missingOnly {
		rows, err = database.Query(query, now.UTC().Truncate(time.Hour).Format(time.RFC3339))
	} else {
		rows, err = database.Query(query)
	}
	if err != nil {
		return fmt.Errorf("读取统计小时失败: %w", err)
	}
	buckets := make([]time.Time, 0)
	for rows.Next() {
		var bucket string
		if err := rows.Scan(&bucket); err != nil {
			rows.Close()
			return err
		}
		at, err := time.Parse(time.RFC3339, bucket)
		if err != nil {
			continue
		}
		buckets = append(buckets, at)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, bucket := range buckets {
		if err := aggregateBucket(database, bucket); err != nil {
			return err
		}
	}
	return nil
}

// aggregateBucket 将一个完整 UTC 小时的明细写入聚合表。
func aggregateBucket(database *sql.DB, start time.Time) error {
	revision, err := hourlyMetricDirtyRevision(database, start)
	if err != nil {
		return err
	}
	end := start.UTC().Add(time.Hour)
	_, err = calculateMetrics(database, start, end, true, revision)
	return err
}

// hourlyBucketKey 返回小时聚合主键，与历史 hourly_metrics 行格式保持一致。
func hourlyBucketKey(start time.Time) string {
	return start.UTC().Truncate(time.Hour).Format(time.RFC3339)
}

// hourlyMetricDirtyRevision 读取小时桶当前脏版本；不存在时为 0。
func hourlyMetricDirtyRevision(database *sql.DB, start time.Time) (int64, error) {
	var revision int64
	err := database.QueryRow(`SELECT revision FROM hourly_metric_dirty WHERE bucket_start_utc = ?`, hourlyBucketKey(start)).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取小时聚合脏标记失败: %w", err)
	}
	return revision, nil
}

// markHourlyMetricDirtyTx 在同一事务中把请求所属 UTC 小时桶标脏。
func markHourlyMetricDirtyTx(transaction *sql.Tx, startedAt string) error {
	parsed, err := ParseSQLiteTime(startedAt)
	if err != nil {
		return fmt.Errorf("解析请求开始时间失败: %w", err)
	}
	bucket := hourlyBucketKey(parsed)
	if _, err := transaction.Exec(`INSERT INTO hourly_metric_dirty(bucket_start_utc, revision) VALUES (?, 1) ON CONFLICT(bucket_start_utc) DO UPDATE SET revision = hourly_metric_dirty.revision + 1`, bucket); err != nil {
		return fmt.Errorf("标记小时聚合脏桶失败: %w", err)
	}
	return nil
}

// calculateMetrics 计算指定 UTC 时间范围内的统计，并可将完整小时的多维结果写入聚合表。
func calculateMetrics(database *sql.DB, start, end time.Time, persist bool, dirtyRevision int64) (metricAccumulator, error) {
	total := newMetricAccumulator()
	dimensions := metricDimensions{
		"channel":  make(map[string]*metricAccumulator),
		"protocol": make(map[string]*metricAccumulator),
		"model":    make(map[string]*metricAccumulator),
	}
	rows, err := database.Query(`SELECT id, protocol, logical_model, final_channel_id, final_status, error_class, latency_ms, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, ttft_ms, tps FROM requests WHERE started_at >= ? AND started_at < ?`, FormatSQLiteTime(start), FormatSQLiteTime(end))
	if err != nil {
		return metricAccumulator{}, fmt.Errorf("读取请求统计明细失败: %w", err)
	}
	requests := make([]requestMetric, 0)
	for rows.Next() {
		var request requestMetric
		if err := rows.Scan(&request.ID, &request.Protocol, &request.LogicalModel, &request.FinalChannelID, &request.Status, &request.ErrorClass, &request.Latency, &request.Input, &request.Output, &request.Cache, &request.CacheWrite, &request.Reasoning, &request.TotalTokens, &request.TTFT, &request.TPS); err != nil {
			rows.Close()
			return metricAccumulator{}, err
		}
		requests = append(requests, request)
		addRequestMetric(&total, request)
		addRequestMetric(dimensionMetric(dimensions, "protocol", request.Protocol), request)
		addRequestMetric(dimensionMetric(dimensions, "model", request.LogicalModel), request)
		if request.FinalChannelID != "" {
			addRequestMetric(dimensionMetric(dimensions, "channel", request.FinalChannelID), request)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return metricAccumulator{}, err
	}
	rows.Close()
	for _, request := range requests {
		attemptRows, err := database.Query(`SELECT channel_id, status, fallback_triggered, latency_ms FROM attempts WHERE request_id = ?`, request.ID)
		if err != nil {
			return metricAccumulator{}, err
		}
		attemptCount := int64(0)
		fallback := false
		for attemptRows.Next() {
			var channelID, status string
			var fallbackTriggered, latency int64
			if err := attemptRows.Scan(&channelID, &status, &fallbackTriggered, &latency); err != nil {
				attemptRows.Close()
				return metricAccumulator{}, err
			}
			attemptCount++
			fallback = fallback || fallbackTriggered != 0
			addAttemptMetric(&total, status, latency)
			addAttemptMetric(dimensionMetric(dimensions, "protocol", request.Protocol), status, latency)
			addAttemptMetric(dimensionMetric(dimensions, "model", request.LogicalModel), status, latency)
			addAttemptMetric(dimensionMetric(dimensions, "channel", channelID), status, latency)
		}
		if err := attemptRows.Err(); err != nil {
			attemptRows.Close()
			return metricAccumulator{}, err
		}
		attemptRows.Close()
		cancelled := request.Status == "cancelled" || request.ErrorClass == "cancelled"
		eligible := attemptCount > 0 && request.Status != "processing" && !cancelled
		fallback = fallback && request.Status != "processing" && !cancelled
		addFallbackMetric(&total, eligible, fallback)
		addFallbackMetric(dimensionMetric(dimensions, "protocol", request.Protocol), eligible, fallback)
		addFallbackMetric(dimensionMetric(dimensions, "model", request.LogicalModel), eligible, fallback)
		if request.FinalChannelID != "" {
			addFallbackMetric(dimensionMetric(dimensions, "channel", request.FinalChannelID), eligible, fallback)
		}
	}
	probeRows, err := database.Query(`SELECT channel_id, status FROM probe_runs WHERE occurred_at >= ? AND occurred_at < ?`, FormatSQLiteTime(start), FormatSQLiteTime(end))
	if err != nil {
		return metricAccumulator{}, err
	}
	for probeRows.Next() {
		var channelID, status string
		if err := probeRows.Scan(&channelID, &status); err != nil {
			probeRows.Close()
			return metricAccumulator{}, err
		}
		addProbeMetric(&total, status)
		addProbeMetric(dimensionMetric(dimensions, "channel", channelID), status)
	}
	if err := probeRows.Err(); err != nil {
		probeRows.Close()
		return metricAccumulator{}, err
	}
	probeRows.Close()
	if !persist {
		return total, nil
	}
	if err := persistMetricDimensions(database, start, total, dimensions, dirtyRevision); err != nil {
		return metricAccumulator{}, fmt.Errorf("写入小时聚合失败: %w", err)
	}
	return total, nil
}

// dimensionMetric 返回指定维度累加器，不存在时按固定直方图桶创建。
func dimensionMetric(dimensions metricDimensions, dimensionType, dimensionID string) *metricAccumulator {
	if accumulator := dimensions[dimensionType][dimensionID]; accumulator != nil {
		return accumulator
	}
	accumulator := newMetricAccumulator()
	dimensions[dimensionType][dimensionID] = &accumulator
	return &accumulator
}

// addRequestMetric 将一条请求的终态指标累加到目标维度。
func addRequestMetric(target *metricAccumulator, request requestMetric) {
	target.RequestCount++
	if request.Status == "success" {
		target.RequestSuccessCount++
	} else if request.Status == "error" || request.Status == "partial" || request.Status == "timeout" || request.Status == "cancelled" {
		target.RequestFailureCount++
	}
	if request.Status == "processing" {
		return
	}
	if request.Latency > 0 {
		target.RequestLatencyTotal += request.Latency
		target.RequestLatencyCount++
	}
	addHistogram(target.RequestLatencyHistogram, request.Latency)
	if request.Input.Valid {
		target.InputTokens += request.Input.Int64
		target.InputKnown = true
	}
	if request.Output.Valid {
		target.OutputTokens += request.Output.Int64
		target.OutputKnown = true
	}
	if request.Cache.Valid {
		target.CacheReadInputTokens += request.Cache.Int64
		target.CacheKnown = true
	}
	if request.CacheWrite.Valid {
		target.CacheWriteInputTokens += request.CacheWrite.Int64
		target.CacheWriteKnown = true
	}
	if request.Reasoning.Valid {
		target.ReasoningTokens += request.Reasoning.Int64
		target.ReasoningKnown = true
	}
	if request.TotalTokens.Valid {
		target.TotalTokens += request.TotalTokens.Int64
		target.TotalKnown = true
	}
	if request.TTFT.Valid {
		target.TTFTTotal += request.TTFT.Int64
		target.TTFTCount++
		addHistogram(target.TTFTHistogram, request.TTFT.Int64)
	}
	if request.TPS.Valid {
		target.TPSTotal += request.TPS.Float64
		target.TPSCount++
	}
}

// addAttemptMetric 将一条上游尝试的计数和延迟累加到目标维度。
func addAttemptMetric(target *metricAccumulator, status string, latency int64) {
	target.AttemptCount++
	if status == "success" {
		target.AttemptSuccessCount++
	}
	if status == "processing" {
		return
	}
	target.AttemptLatencyTotal += latency
	target.AttemptLatencyCount++
	addHistogram(target.AttemptLatencyHistogram, latency)
}

// addFallbackMetric 将请求级 fallback 分子和分母累加到目标维度。
func addFallbackMetric(target *metricAccumulator, eligible, fallback bool) {
	if eligible {
		target.FallbackEligibleRequestCount++
	}
	if fallback {
		target.FallbackRequestCount++
	}
}

// addProbeMetric 将一条渠道探针结果累加到目标维度。
func addProbeMetric(target *metricAccumulator, status string) {
	target.ProbeCount++
	if status == "success" {
		target.ProbeSuccessCount++
	}
}

// persistMetricDimensions 在一个事务内重写指定小时的全部聚合维度，并清除未变化的脏标记。
func persistMetricDimensions(database *sql.DB, start time.Time, total metricAccumulator, dimensions metricDimensions, dirtyRevision int64) error {
	transaction, err := database.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	bucket := hourlyBucketKey(start)
	var currentRevision sql.NullInt64
	if err := transaction.QueryRow(`SELECT revision FROM hourly_metric_dirty WHERE bucket_start_utc = ?`, bucket).Scan(&currentRevision); err != nil && err != sql.ErrNoRows {
		return err
	}
	if currentRevision.Valid && currentRevision.Int64 != dirtyRevision {
		return nil
	}
	if _, err := transaction.Exec(`DELETE FROM hourly_metrics WHERE bucket_start_utc = ?`, bucket); err != nil {
		return err
	}
	if total.RequestCount != 0 || total.AttemptCount != 0 || total.ProbeCount != 0 {
		if err := insertHourlyMetric(transaction, bucket, "all", "", total); err != nil {
			return err
		}
	}
	for dimensionType, values := range dimensions {
		for dimensionID, accumulator := range values {
			if accumulator.RequestCount == 0 && accumulator.AttemptCount == 0 && accumulator.ProbeCount == 0 {
				continue
			}
			if err := insertHourlyMetric(transaction, bucket, dimensionType, dimensionID, *accumulator); err != nil {
				return err
			}
		}
	}
	if currentRevision.Valid {
		if _, err := transaction.Exec(`DELETE FROM hourly_metric_dirty WHERE bucket_start_utc = ? AND revision = ?`, bucket, dirtyRevision); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

// insertHourlyMetric 写入一条小时维度指标。
func insertHourlyMetric(transaction *sql.Tx, bucket, dimensionType, dimensionID string, total metricAccumulator) error {
	var input, output, cache, cacheWrite, reasoning, totalTokens any
	if total.InputKnown {
		input = total.InputTokens
	}
	if total.OutputKnown {
		output = total.OutputTokens
	}
	if total.CacheKnown {
		cache = total.CacheReadInputTokens
	}
	if total.CacheWriteKnown {
		cacheWrite = total.CacheWriteInputTokens
	}
	if total.ReasoningKnown {
		reasoning = total.ReasoningTokens
	}
	if total.TotalKnown {
		totalTokens = total.TotalTokens
	}
	requestHistogram, _ := json.Marshal(total.RequestLatencyHistogram)
	attemptHistogram, _ := json.Marshal(total.AttemptLatencyHistogram)
	ttftHistogram, _ := json.Marshal(total.TTFTHistogram)
	_, err := transaction.Exec(`INSERT INTO hourly_metrics(bucket_start_utc, dimension_type, dimension_id, request_count, request_success_count, request_failure_count, fallback_request_count, fallback_eligible_request_count, attempt_count, attempt_success_count, probe_count, probe_success_count, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, request_latency_total_ms, request_latency_count, attempt_latency_total_ms, attempt_latency_count, ttft_total_ms, ttft_count, tps_total, tps_count, request_latency_histogram_json, attempt_latency_histogram_json, ttft_histogram_json, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, bucket, dimensionType, dimensionID, total.RequestCount, total.RequestSuccessCount, total.RequestFailureCount, total.FallbackRequestCount, total.FallbackEligibleRequestCount, total.AttemptCount, total.AttemptSuccessCount, total.ProbeCount, total.ProbeSuccessCount, input, output, cache, cacheWrite, reasoning, totalTokens, total.RequestLatencyTotal, total.RequestLatencyCount, total.AttemptLatencyTotal, total.AttemptLatencyCount, total.TTFTTotal, total.TTFTCount, total.TPSTotal, total.TPSCount, string(requestHistogram), string(attemptHistogram), string(ttftHistogram), FormatSQLiteTime(time.Now()))
	return err
}

// newMetricAccumulator 创建带固定直方图桶的统计累加器。
func newMetricAccumulator() metricAccumulator {
	return metricAccumulator{
		RequestLatencyHistogram: make([]int64, len(metricHistogramBounds)+1),
		AttemptLatencyHistogram: make([]int64, len(metricHistogramBounds)+1),
		TTFTHistogram:           make([]int64, len(metricHistogramBounds)+1),
	}
}

// mergeMetricAccumulator 将一个时间分段的统计合并到总计中。
func mergeMetricAccumulator(target *metricAccumulator, source metricAccumulator) {
	target.RequestCount += source.RequestCount
	target.RequestSuccessCount += source.RequestSuccessCount
	target.RequestFailureCount += source.RequestFailureCount
	target.FallbackRequestCount += source.FallbackRequestCount
	target.FallbackEligibleRequestCount += source.FallbackEligibleRequestCount
	target.AttemptCount += source.AttemptCount
	target.AttemptSuccessCount += source.AttemptSuccessCount
	target.ProbeCount += source.ProbeCount
	target.ProbeSuccessCount += source.ProbeSuccessCount
	target.InputTokens += source.InputTokens
	target.OutputTokens += source.OutputTokens
	target.CacheReadInputTokens += source.CacheReadInputTokens
	target.CacheWriteInputTokens += source.CacheWriteInputTokens
	target.ReasoningTokens += source.ReasoningTokens
	target.TotalTokens += source.TotalTokens
	target.InputKnown = target.InputKnown || source.InputKnown
	target.OutputKnown = target.OutputKnown || source.OutputKnown
	target.CacheKnown = target.CacheKnown || source.CacheKnown
	target.CacheWriteKnown = target.CacheWriteKnown || source.CacheWriteKnown
	target.ReasoningKnown = target.ReasoningKnown || source.ReasoningKnown
	target.TotalKnown = target.TotalKnown || source.TotalKnown
	target.RequestLatencyTotal += source.RequestLatencyTotal
	target.RequestLatencyCount += source.RequestLatencyCount
	target.AttemptLatencyTotal += source.AttemptLatencyTotal
	target.AttemptLatencyCount += source.AttemptLatencyCount
	target.TTFTTotal += source.TTFTTotal
	target.TTFTCount += source.TTFTCount
	target.TPSTotal += source.TPSTotal
	target.TPSCount += source.TPSCount
	for index := range target.RequestLatencyHistogram {
		target.RequestLatencyHistogram[index] += source.RequestLatencyHistogram[index]
		target.AttemptLatencyHistogram[index] += source.AttemptLatencyHistogram[index]
		target.TTFTHistogram[index] += source.TTFTHistogram[index]
	}
}

// addHistogram 将一个耗时值计入固定边界的直方图桶。
func addHistogram(histogram []int64, value int64) {
	if value <= 0 {
		return
	}
	for index, bound := range metricHistogramBounds {
		if value <= bound {
			histogram[index]++
			return
		}
	}
	histogram[len(histogram)-1]++
}

// mergeHistogram 将数据库中的 JSON 直方图合并到查询窗口。
func mergeHistogram(target []int64, encoded string) {
	var values []int64
	if json.Unmarshal([]byte(encoded), &values) != nil {
		return
	}
	for index := range target {
		if index < len(values) {
			target[index] += values[index]
		}
	}
}

// histogramPercentile 根据合并后的固定桶估算给定分位值。
func histogramPercentile(histogram []int64, percentile float64) (*float64, bool) {
	var total int64
	for _, value := range histogram {
		total += value
	}
	if total == 0 {
		return nil, false
	}
	target := int64(math.Ceil(float64(total) * percentile))
	var seen int64
	for index, value := range histogram {
		seen += value
		if seen >= target {
			if index >= len(metricHistogramBounds) {
				return nil, true
			}
			result := float64(metricHistogramBounds[index])
			if index > 0 {
				result = float64(metricHistogramBounds[index-1]+metricHistogramBounds[index]) / 2
			}
			return &result, false
		}
	}
	return nil, false
}

// StatsWindow 返回用户时区下的日历统计窗口，days 仅支持 1、7、30。
func StatsWindow(now time.Time, timezone string, days int) (time.Time, time.Time, error) {
	if days != 1 && days != 7 && days != 30 {
		return time.Time{}, time.Time{}, fmt.Errorf("统计窗口必须是 1、7 或 30 天")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("加载统计时区失败: %w", err)
	}
	if now.IsZero() {
		now = time.Now()
	}
	localNow := now.In(loc)
	startLocal := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	return startLocal.UTC(), localNow.UTC(), nil
}

// QueryStats 按用户时区的日历窗口读取统计，days 仅支持 1、7、30。
func QueryStats(database *sql.DB, now time.Time, timezone string, days int) (StatsSummary, error) {
	startUTC, endUTC, err := StatsWindow(now, timezone, days)
	if err != nil {
		return StatsSummary{}, err
	}
	if err := AggregateMissingMetrics(database, now); err != nil {
		return StatsSummary{}, err
	}
	var summary StatsSummary
	total := newMetricAccumulator()
	fullStart := startUTC.Truncate(time.Hour)
	if fullStart.Before(startUTC) {
		fullStart = fullStart.Add(time.Hour)
	}
	fullEnd := endUTC.Truncate(time.Hour)
	rows, err := database.Query(`SELECT request_count, request_success_count, request_failure_count, fallback_request_count, fallback_eligible_request_count, attempt_count, attempt_success_count, probe_count, probe_success_count, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, reasoning_tokens, total_tokens, request_latency_total_ms, request_latency_count, ttft_total_ms, ttft_count, tps_total, tps_count, request_latency_histogram_json, ttft_histogram_json FROM hourly_metrics WHERE bucket_start_utc >= ? AND bucket_start_utc < ? AND dimension_type = 'all'`, fullStart.Format(time.RFC3339), fullEnd.Format(time.RFC3339))
	if err != nil {
		return StatsSummary{}, fmt.Errorf("读取统计聚合失败: %w", err)
	}
	for rows.Next() {
		var requestCount, requestSuccess, requestFailure, fallbackCount, fallbackEligible, attemptCount, attemptSuccess, probeCount, probeSuccess int64
		var requestHistogramJSON, ttftHistogramJSON string
		var rowTPS float64
		var rowTPSCount int64
		var rowInput, rowOutput, rowCache, rowCacheWrite, rowReasoning, rowTotal sql.NullInt64
		var rowLatencyTotal, rowLatencyCount, rowTTFTTotal, rowTTFTCount int64
		if err := rows.Scan(&requestCount, &requestSuccess, &requestFailure, &fallbackCount, &fallbackEligible, &attemptCount, &attemptSuccess, &probeCount, &probeSuccess, &rowInput, &rowOutput, &rowCache, &rowCacheWrite, &rowReasoning, &rowTotal, &rowLatencyTotal, &rowLatencyCount, &rowTTFTTotal, &rowTTFTCount, &rowTPS, &rowTPSCount, &requestHistogramJSON, &ttftHistogramJSON); err != nil {
			rows.Close()
			return StatsSummary{}, err
		}
		if rowInput.Valid {
			total.InputTokens += rowInput.Int64
			total.InputKnown = true
		}
		if rowOutput.Valid {
			total.OutputTokens += rowOutput.Int64
			total.OutputKnown = true
		}
		if rowCache.Valid {
			total.CacheReadInputTokens += rowCache.Int64
			total.CacheKnown = true
		}
		if rowCacheWrite.Valid {
			total.CacheWriteInputTokens += rowCacheWrite.Int64
			total.CacheWriteKnown = true
		}
		if rowReasoning.Valid {
			total.ReasoningTokens += rowReasoning.Int64
			total.ReasoningKnown = true
		}
		if rowTotal.Valid {
			total.TotalTokens += rowTotal.Int64
			total.TotalKnown = true
		}
		total.RequestLatencyTotal += rowLatencyTotal
		total.RequestLatencyCount += rowLatencyCount
		total.TTFTTotal += rowTTFTTotal
		total.TTFTCount += rowTTFTCount
		total.RequestCount += requestCount
		total.RequestSuccessCount += requestSuccess
		total.RequestFailureCount += requestFailure
		total.FallbackRequestCount += fallbackCount
		total.FallbackEligibleRequestCount += fallbackEligible
		total.AttemptCount += attemptCount
		total.AttemptSuccessCount += attemptSuccess
		total.ProbeCount += probeCount
		total.ProbeSuccessCount += probeSuccess
		total.TPSTotal += rowTPS
		total.TPSCount += rowTPSCount
		mergeHistogram(total.RequestLatencyHistogram, requestHistogramJSON)
		mergeHistogram(total.TTFTHistogram, ttftHistogramJSON)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return StatsSummary{}, err
	}
	rows.Close()
	if fullStart.After(fullEnd) {
		boundary, err := calculateMetrics(database, startUTC, endUTC, false, 0)
		if err != nil {
			return StatsSummary{}, err
		}
		mergeMetricAccumulator(&total, boundary)
	} else {
		if startUTC.Before(fullStart) {
			boundary, err := calculateMetrics(database, startUTC, fullStart, false, 0)
			if err != nil {
				return StatsSummary{}, err
			}
			mergeMetricAccumulator(&total, boundary)
		}
		if fullEnd.Before(endUTC) {
			boundary, err := calculateMetrics(database, fullEnd, endUTC, false, 0)
			if err != nil {
				return StatsSummary{}, err
			}
			mergeMetricAccumulator(&total, boundary)
		}
	}
	summary.RequestCount = total.RequestCount
	summary.RequestSuccessCount = total.RequestSuccessCount
	summary.RequestFailureCount = total.RequestFailureCount
	summary.FallbackRequestCount = total.FallbackRequestCount
	summary.FallbackEligibleRequestCount = total.FallbackEligibleRequestCount
	summary.AttemptCount = total.AttemptCount
	summary.AttemptSuccessCount = total.AttemptSuccessCount
	summary.ProbeCount = total.ProbeCount
	summary.ProbeSuccessCount = total.ProbeSuccessCount
	summary.WindowDays, summary.From, summary.To = days, FormatSQLiteTime(startUTC), FormatSQLiteTime(endUTC)
	if total.InputKnown {
		value := total.InputTokens
		summary.InputTokens = &value
	}
	if total.OutputKnown {
		value := total.OutputTokens
		summary.OutputTokens = &value
	}
	if total.CacheKnown {
		value := total.CacheReadInputTokens
		summary.CacheReadInputTokens = &value
	}
	if total.CacheWriteKnown {
		value := total.CacheWriteInputTokens
		summary.CacheWriteInputTokens = &value
	}
	if total.ReasoningKnown {
		value := total.ReasoningTokens
		summary.ReasoningTokens = &value
	}
	if total.TotalKnown {
		value := total.TotalTokens
		summary.TotalTokens = &value
	}
	if total.RequestLatencyCount > 0 {
		summary.AverageLatencyMs = float64(total.RequestLatencyTotal) / float64(total.RequestLatencyCount)
	}
	completedCount := summary.RequestSuccessCount + summary.RequestFailureCount
	if completedCount > 0 {
		summary.SuccessRate = float64(summary.RequestSuccessCount) / float64(completedCount)
	}
	if total.FallbackEligibleRequestCount > 0 {
		summary.FallbackRate = float64(summary.FallbackRequestCount) / float64(total.FallbackEligibleRequestCount)
	}
	if summary.InputTokens != nil && *summary.InputTokens > 0 && summary.CacheReadInputTokens != nil {
		value := float64(*summary.CacheReadInputTokens) / float64(*summary.InputTokens)
		summary.CacheRate = &value
	}
	if total.TTFTCount > 0 {
		value := float64(total.TTFTTotal) / float64(total.TTFTCount)
		summary.AverageTTFTMs = &value
	}
	if total.TPSCount > 0 {
		value := total.TPSTotal / float64(total.TPSCount)
		summary.AverageTPS = &value
	}
	summary.P95TTFTMs, summary.P95TTFTOverflow = histogramPercentile(total.TTFTHistogram, 0.95)
	return summary, nil
}

// CleanupLogs 聚合后清理指定保留期之前的请求、探针、健康和审计明细。
func CleanupLogs(database *sql.DB, retentionDays int, now time.Time) (map[string]int64, error) {
	return CleanupLogsWithRetention(database, retentionDays, retentionDays, now)
}

// CleanupLogsWithRetention 分别清理请求/健康明细和操作审计明细。
func CleanupLogsWithRetention(database *sql.DB, requestRetentionDays, auditRetentionDays int, now time.Time) (map[string]int64, error) {
	if requestRetentionDays <= 0 || auditRetentionDays <= 0 {
		return nil, fmt.Errorf("日志保留天数必须大于 0")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := AggregateMissingMetrics(database, now); err != nil {
		return nil, err
	}
	// 只删除完整 UTC 小时，避免后续重聚合用残余半小时覆盖已保存的完整桶。
	cutoff := now.UTC().AddDate(0, 0, -requestRetentionDays).Truncate(time.Hour)
	auditCutoff := now.UTC().AddDate(0, 0, -auditRetentionDays)
	result := make(map[string]int64)
	for name, query := range map[string]string{"requests": `DELETE FROM requests WHERE COALESCE(completed_at, started_at) < ?`, "healthEvents": `DELETE FROM health_events WHERE occurred_at < ?`} {
		res, err := database.Exec(query, FormatSQLiteTime(cutoff))
		if err != nil {
			return nil, fmt.Errorf("清理%s失败: %w", name, err)
		}
		result[name], _ = res.RowsAffected()
	}
	probeRuns, err := TrimProbeRuns(database, requestRetentionDays, now)
	if err != nil {
		return nil, err
	}
	result["probeRuns"] = probeRuns
	res, err := database.Exec(`DELETE FROM audit_logs WHERE occurred_at < ?`, FormatSQLiteTime(auditCutoff))
	if err != nil {
		return nil, fmt.Errorf("清理auditLogs失败: %w", err)
	}
	result["auditLogs"], _ = res.RowsAffected()
	if _, err := database.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return nil, fmt.Errorf("SQLite checkpoint 失败: %w", err)
	}
	return result, nil
}
