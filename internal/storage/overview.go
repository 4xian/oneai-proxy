package storage

import (
	"database/sql"
	"log/slog"
	"math"
	"sort"
	"time"
)

// OverviewMetrics 是总览页首屏使用的 24 小时业务指标。
type OverviewMetrics struct {
	RequestCount         int64    `json:"requestCount"`
	RequestSuccessCount  int64    `json:"requestSuccessCount"`
	RequestFailureCount  int64    `json:"requestFailureCount"`
	FallbackRequestCount int64    `json:"fallbackRequestCount"`
	SuccessRate          float64  `json:"successRate"`
	FallbackRate         float64  `json:"fallbackRate"`
	P95LatencyMs         float64  `json:"p95LatencyMs"`
	AverageLatencyMs     float64  `json:"averageLatencyMs"`
	RequestChange        *float64 `json:"requestChange"`
	SuccessRateChange    *float64 `json:"successRateChange"`
	P95LatencyChange     *float64 `json:"p95LatencyChange"`
	FallbackChange       *float64 `json:"fallbackChange"`
}

// OverviewTrend 是一个小时桶的真实请求趋势。
type OverviewTrend struct {
	Bucket               string  `json:"bucket"`
	RequestCount         int64   `json:"requestCount"`
	RequestSuccessCount  int64   `json:"requestSuccessCount"`
	FallbackRequestCount int64   `json:"fallbackRequestCount"`
	SuccessRate          float64 `json:"successRate"`
	P95LatencyMs         float64 `json:"p95LatencyMs"`
}

// OverviewChannel 是总览页渠道健康表的非敏感摘要。
type OverviewChannel struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Protocol             Protocol `json:"protocol"`
	BaseURL              string   `json:"baseUrl"`
	AdminState           string   `json:"adminState"`
	HealthState          string   `json:"healthState"`
	CredentialConfigured bool     `json:"credentialConfigured"`
	Requests24h          int64    `json:"requests24h"`
	SuccessRate          float64  `json:"successRate"`
	P95LatencyMs         float64  `json:"p95LatencyMs"`
	ModelCount           int      `json:"modelCount"`
	HasRoutableModel     bool     `json:"hasRoutableModel"`
}

// OverviewRequest 是最近请求表的非敏感摘要。
type OverviewRequest struct {
	ID               string   `json:"id"`
	Protocol         Protocol `json:"protocol"`
	ClientModel      string   `json:"clientModel"`
	FinalStatus      string   `json:"finalStatus"`
	StartedAt        string   `json:"startedAt"`
	LatencyMs        int64    `json:"latencyMs"`
	AttemptCount     int      `json:"attemptCount"`
	FinalChannelID   string   `json:"finalChannelId,omitempty"`
	FinalChannelName string   `json:"finalChannelName,omitempty"`
	Tokens           *int64   `json:"tokens"`
}

// OverviewSnapshot 汇总总览页所需的全部数据库数据。
type OverviewSnapshot struct {
	Metrics        OverviewMetrics   `json:"metrics"`
	Trends         []OverviewTrend   `json:"trends"`
	Channels       []OverviewChannel `json:"channels"`
	RecentRequests []OverviewRequest `json:"recentRequests"`
}

type overviewRequestRow struct {
	id                string
	protocol          Protocol
	clientModel       string
	finalStatus       string
	startedAt         time.Time
	startedRaw        string
	latencyMs         int64
	tokens            *int64
	finalChannelID    string
	fallbackTriggered bool
	errorClass        string
}

type overviewAttemptRow struct {
	requestID string
	channelID string
	status    string
	latencyMs int64
	sequence  int
}

type channelAggregate struct {
	requestIDs      map[string]struct{}
	completedIDs    map[string]struct{}
	finalSuccessIDs map[string]struct{}
	latencies       []int64
}

// QueryOverview 从请求账本和渠道配置生成最近 24 小时总览快照。
func QueryOverview(database *sql.DB, now time.Time) (OverviewSnapshot, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	from := now.Add(-24 * time.Hour)
	requestRows, err := database.Query(`SELECT id, protocol, client_model, final_status, started_at, latency_ms, input_tokens, output_tokens, final_channel_id, fallback_triggered, error_class FROM requests WHERE started_at >= ? AND started_at < ? ORDER BY started_at DESC, id DESC`, FormatSQLiteTime(from), FormatSQLiteTime(now))
	if err != nil {
		return OverviewSnapshot{}, err
	}
	requests := make([]overviewRequestRow, 0)
	for requestRows.Next() {
		var item overviewRequestRow
		var started string
		var input, output sql.NullInt64
		var fallbackTriggered int64
		if err := requestRows.Scan(&item.id, &item.protocol, &item.clientModel, &item.finalStatus, &started, &item.latencyMs, &input, &output, &item.finalChannelID, &fallbackTriggered, &item.errorClass); err != nil {
			requestRows.Close()
			return OverviewSnapshot{}, err
		}
		item.startedRaw = started
		item.startedAt, err = ParseSQLiteTime(started)
		if err != nil {
			slog.Error("概览请求时间解析失败", "requestID", item.id, "startedAt", started, "error", err)
		}
		item.tokens = overviewTokens(input, output)
		item.fallbackTriggered = fallbackTriggered != 0
		requests = append(requests, item)
	}
	if err := requestRows.Err(); err != nil {
		requestRows.Close()
		return OverviewSnapshot{}, err
	}
	requestRows.Close()

	attemptRows, err := database.Query(`SELECT a.request_id, a.channel_id, a.status, a.latency_ms, a.sequence FROM attempts a JOIN requests r ON r.id = a.request_id WHERE r.started_at >= ? AND r.started_at < ? ORDER BY a.request_id, a.sequence`, FormatSQLiteTime(from), FormatSQLiteTime(now))
	if err != nil {
		return OverviewSnapshot{}, err
	}
	attemptsByRequest := make(map[string][]overviewAttemptRow)
	channelStats := make(map[string]*channelAggregate)
	for attemptRows.Next() {
		var item overviewAttemptRow
		if err := attemptRows.Scan(&item.requestID, &item.channelID, &item.status, &item.latencyMs, &item.sequence); err != nil {
			attemptRows.Close()
			return OverviewSnapshot{}, err
		}
		attemptsByRequest[item.requestID] = append(attemptsByRequest[item.requestID], item)
		aggregate := channelStats[item.channelID]
		if aggregate == nil {
			aggregate = &channelAggregate{requestIDs: make(map[string]struct{}), completedIDs: make(map[string]struct{}), finalSuccessIDs: make(map[string]struct{})}
			channelStats[item.channelID] = aggregate
		}
		aggregate.requestIDs[item.requestID] = struct{}{}
		if item.latencyMs > 0 {
			aggregate.latencies = append(aggregate.latencies, item.latencyMs)
		}
	}
	if err := attemptRows.Err(); err != nil {
		attemptRows.Close()
		return OverviewSnapshot{}, err
	}
	attemptRows.Close()

	requestByID := make(map[string]overviewRequestRow, len(requests))
	for _, request := range requests {
		requestByID[request.id] = request
	}
	for requestID, attempts := range attemptsByRequest {
		request, ok := requestByID[requestID]
		if !ok {
			continue
		}
		finalChannelID := requestFinalChannelID(request.finalChannelID, attempts)
		completed := overviewCompleted(request.finalStatus)
		seen := make(map[string]struct{})
		for _, attempt := range attempts {
			if _, exists := seen[attempt.channelID]; exists {
				continue
			}
			seen[attempt.channelID] = struct{}{}
			aggregate := channelStats[attempt.channelID]
			if aggregate == nil {
				continue
			}
			if completed {
				aggregate.completedIDs[requestID] = struct{}{}
			}
			if request.finalStatus == "success" && attempt.channelID == finalChannelID {
				aggregate.finalSuccessIDs[requestID] = struct{}{}
			}
		}
	}

	channels, err := ListChannels(database)
	if err != nil {
		return OverviewSnapshot{}, err
	}
	directoryModels, err := ListAllChannelModels(database)
	if err != nil {
		return OverviewSnapshot{}, err
	}
	mappings, err := ListChannelModelMappings(database)
	if err != nil {
		return OverviewSnapshot{}, err
	}
	modelsByChannel := make(map[string][]string)
	for _, model := range directoryModels {
		modelsByChannel[model.ChannelID] = append(modelsByChannel[model.ChannelID], model.Model)
	}
	mappingsByChannel := make(map[string][]ChannelModelMapping)
	for _, mapping := range mappings {
		mappingsByChannel[mapping.ChannelID] = append(mappingsByChannel[mapping.ChannelID], mapping)
	}

	channelNames := make(map[string]string, len(channels))
	channelSummaries := make([]OverviewChannel, 0, len(channels))
	for _, channel := range channels {
		channelNames[channel.ID] = channel.Name
		aggregate := channelStats[channel.ID]
		var requestCount, successCount, completedCount int64
		var p95 float64
		if aggregate != nil {
			requestCount = int64(len(aggregate.requestIDs))
			successCount = int64(len(aggregate.finalSuccessIDs))
			completedCount = int64(len(aggregate.completedIDs))
			p95 = percentile95(aggregate.latencies)
		}
		routableIDs := uniqueChannelModelIDs(modelsByChannel[channel.ID], mappingsByChannel[channel.ID], channel.FallbackModel)
		channelSummaries = append(channelSummaries, OverviewChannel{ID: channel.ID, Name: channel.Name, Protocol: channel.Protocol, BaseURL: channel.BaseURL, AdminState: channel.AdminState, HealthState: channel.HealthState, CredentialConfigured: channel.CredentialConfigured, Requests24h: requestCount, SuccessRate: overviewSuccessRate(successCount, completedCount-successCount), P95LatencyMs: p95, ModelCount: len(routableIDs), HasRoutableModel: len(routableIDs) > 0})
	}

	metrics := OverviewMetrics{}
	var fallbackEligible int64
	latencies := make([]int64, 0, len(requests))
	trends := make([]OverviewTrend, 24)
	bucketFailures := make([]int64, 24)
	bucketLatencies := make([][]int64, 24)
	bucketStart := from
	for index := range trends {
		trends[index].Bucket = bucketStart.Add(time.Duration(index) * time.Hour).Format(time.RFC3339)
	}
	for _, request := range requests {
		attempts := attemptsByRequest[request.id]
		addOverviewRequestMetric(&metrics, &fallbackEligible, request.finalStatus, request.errorClass, len(attempts), request.fallbackTriggered)
		if request.latencyMs > 0 {
			metrics.AverageLatencyMs += float64(request.latencyMs)
			latencies = append(latencies, request.latencyMs)
		}
		if request.startedAt.IsZero() {
			continue
		}
		bucket := int(request.startedAt.Sub(bucketStart) / time.Hour)
		if bucket < 0 || bucket >= len(trends) {
			continue
		}
		trends[bucket].RequestCount++
		if request.finalStatus == "success" {
			trends[bucket].RequestSuccessCount++
		} else if request.finalStatus == "error" || request.finalStatus == "partial" || request.finalStatus == "timeout" || request.finalStatus == "cancelled" {
			bucketFailures[bucket]++
		}
		if request.latencyMs > 0 {
			bucketLatencies[bucket] = append(bucketLatencies[bucket], request.latencyMs)
		}
		if request.fallbackTriggered && request.finalStatus != "processing" && !overviewCancelled(request.finalStatus, request.errorClass) {
			trends[bucket].FallbackRequestCount++
		}
	}
	metrics.SuccessRate = overviewSuccessRate(metrics.RequestSuccessCount, metrics.RequestFailureCount)
	if fallbackEligible > 0 {
		metrics.FallbackRate = float64(metrics.FallbackRequestCount) / float64(fallbackEligible)
	}
	if len(latencies) > 0 {
		metrics.AverageLatencyMs /= float64(len(latencies))
	}
	metrics.P95LatencyMs = percentile95(latencies)
	for index := range trends {
		trends[index].SuccessRate = overviewSuccessRate(trends[index].RequestSuccessCount, bucketFailures[index])
		trends[index].P95LatencyMs = percentile95(bucketLatencies[index])
	}
	previous, err := queryOverviewMetricsWindow(database, from.Add(-24*time.Hour), from)
	if err != nil {
		return OverviewSnapshot{}, err
	}
	metrics.RequestChange = relativeChange(float64(metrics.RequestCount), float64(previous.RequestCount))
	if metrics.RequestCount > 0 && previous.RequestCount > 0 {
		metrics.SuccessRateChange = relativeChange(metrics.SuccessRate, previous.SuccessRate)
	}
	if metrics.P95LatencyMs > 0 && previous.P95LatencyMs > 0 {
		metrics.P95LatencyChange = relativeChange(metrics.P95LatencyMs, previous.P95LatencyMs)
	}
	metrics.FallbackChange = relativeChange(float64(metrics.FallbackRequestCount), float64(previous.FallbackRequestCount))

	recent := make([]OverviewRequest, 0, 6)
	for _, request := range requests {
		attempts := attemptsByRequest[request.id]
		startedAt := request.startedRaw
		if !request.startedAt.IsZero() {
			startedAt = FormatSQLiteTime(request.startedAt)
		}
		item := OverviewRequest{ID: request.id, Protocol: request.protocol, ClientModel: request.clientModel, FinalStatus: request.finalStatus, StartedAt: startedAt, LatencyMs: request.latencyMs, AttemptCount: len(attempts), Tokens: request.tokens}
		item.FinalChannelID = requestFinalChannelID(request.finalChannelID, attempts)
		item.FinalChannelName = channelNames[item.FinalChannelID]
		recent = append(recent, item)
		if len(recent) == 6 {
			break
		}
	}
	return OverviewSnapshot{Metrics: metrics, Trends: trends, Channels: channelSummaries, RecentRequests: recent}, nil
}

// queryOverviewMetricsWindow 按与当前窗口相同的成功/失败和 fallback 口径汇总上一窗口指标。
func queryOverviewMetricsWindow(database *sql.DB, from, to time.Time) (OverviewMetrics, error) {
	rows, err := database.Query(`SELECT r.final_status, r.latency_ms, r.fallback_triggered, r.error_class, (SELECT COUNT(*) FROM attempts a WHERE a.request_id = r.id) FROM requests r WHERE r.started_at >= ? AND r.started_at < ?`, FormatSQLiteTime(from), FormatSQLiteTime(to))
	if err != nil {
		return OverviewMetrics{}, err
	}
	metrics := OverviewMetrics{}
	var fallbackEligible int64
	latencies := make([]int64, 0)
	for rows.Next() {
		var status, errorClass string
		var latency, fallbackTriggered, attemptCount int64
		if err := rows.Scan(&status, &latency, &fallbackTriggered, &errorClass, &attemptCount); err != nil {
			rows.Close()
			return OverviewMetrics{}, err
		}
		addOverviewRequestMetric(&metrics, &fallbackEligible, status, errorClass, int(attemptCount), fallbackTriggered != 0)
		if latency > 0 {
			latencies = append(latencies, latency)
			metrics.AverageLatencyMs += float64(latency)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return OverviewMetrics{}, err
	}
	rows.Close()
	metrics.SuccessRate = overviewSuccessRate(metrics.RequestSuccessCount, metrics.RequestFailureCount)
	if fallbackEligible > 0 {
		metrics.FallbackRate = float64(metrics.FallbackRequestCount) / float64(fallbackEligible)
	}
	if len(latencies) > 0 {
		metrics.AverageLatencyMs /= float64(len(latencies))
	}
	metrics.P95LatencyMs = percentile95(latencies)
	return metrics, nil
}

// addOverviewRequestMetric 将一条请求计入窗口请求数、成功/失败和 fallback 分子分母。
func addOverviewRequestMetric(metrics *OverviewMetrics, fallbackEligible *int64, status, errorClass string, attemptCount int, fallbackTriggered bool) {
	metrics.RequestCount++
	if status == "success" {
		metrics.RequestSuccessCount++
	} else if status == "error" || status == "partial" || status == "timeout" || status == "cancelled" {
		metrics.RequestFailureCount++
	}
	cancelled := overviewCancelled(status, errorClass)
	eligible := attemptCount > 0 && status != "processing" && !cancelled
	if eligible {
		*fallbackEligible++
	}
	if fallbackTriggered && status != "processing" && !cancelled {
		metrics.FallbackRequestCount++
	}
}

// overviewCompleted 判断请求是否已进入成功或失败口径。
func overviewCompleted(status string) bool {
	return status == "success" || status == "error" || status == "partial" || status == "timeout" || status == "cancelled"
}

// overviewCancelled 判断请求是否按账本口径视为取消。
func overviewCancelled(status, errorClass string) bool {
	return status == "cancelled" || errorClass == "cancelled"
}

// overviewSuccessRate 按已完成请求计算成功率，分母为 0 时返回 0。
func overviewSuccessRate(successCount, failureCount int64) float64 {
	completed := successCount + failureCount
	if completed == 0 {
		return 0
	}
	return float64(successCount) / float64(completed)
}

// overviewTokens 将输入/输出 token 合成可空合计；两端都未知时返回 nil。
func overviewTokens(input, output sql.NullInt64) *int64 {
	if !input.Valid && !output.Valid {
		return nil
	}
	value := int64(0)
	if input.Valid {
		value += input.Int64
	}
	if output.Valid {
		value += output.Int64
	}
	return &value
}

// requestFinalChannelID 优先使用账本最终渠道，缺失时回退到 sequence 最后一条 Attempt。
func requestFinalChannelID(finalChannelID string, attempts []overviewAttemptRow) string {
	if finalChannelID != "" {
		return finalChannelID
	}
	if len(attempts) == 0 {
		return ""
	}
	return attempts[len(attempts)-1].channelID
}

func relativeChange(current, previous float64) *float64 {
	if previous == 0 {
		if current != 0 {
			return nil
		}
		value := float64(0)
		return &value
	}
	value := (current - previous) / previous
	return &value
}

func percentile95(values []int64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	index := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index])
}
