package server

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/storage"
)

// logsAPI 提供请求明细、审计、统计、聚合和清理管理接口。
func (s *Service) logsAPI(writer http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/admin/v1/logs"), "/")
	parts := strings.Split(path, "/")
	if path == "" || path == "requests" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		page := queryInt(request, "page", 1)
		pageSize := queryInt(request, "pageSize", 50)
		status := strings.TrimSpace(request.URL.Query().Get("status"))
		if !validRequestLogStatus(status) {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求状态仅支持 processing、success、error、timeout、cancelled、partial"})
			return
		}
		filter := storage.RequestListFilter{
			From: request.URL.Query().Get("from"), To: request.URL.Query().Get("to"), Model: request.URL.Query().Get("model"),
			Protocol: request.URL.Query().Get("protocol"), Status: status, Channel: request.URL.Query().Get("channel"),
			Group: request.URL.Query().Get("group"), ErrorClass: request.URL.Query().Get("errorClass"), Fallback: request.URL.Query().Get("fallback"), Keyword: request.URL.Query().Get("keyword"),
		}
		days, daysErr := parseLogDays(request)
		if daysErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": daysErr.Error()})
			return
		}
		if err := s.applyDefaultLogWindow(&filter.From, &filter.To, days); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		items, err := storage.ListRequestsPage(s.database, filter, page, pageSize)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, items)
		return
	}
	if parts[0] == "requests" && len(parts) == 4 && parts[2] == "content" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		body, blob, err := storage.ReadContentBlob(s.database, s.secrets, parts[3])
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		if blob.RequestID != parts[1] {
			writeJSON(writer, http.StatusNotFound, map[string]string{"error": "正文不属于该请求"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"blob": blob, "content": string(storage.RedactContentForAPI(body, blob.RedactHeaders))})
		return
	}
	if parts[0] == "requests" && len(parts) == 2 {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		item, err := storage.GetRequest(s.database, parts[1])
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"request": item, "contentBlobs": item.ContentBlobs})
		return
	}
	if parts[0] == "audit" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		page := queryInt(request, "page", 1)
		pageSize := queryInt(request, "pageSize", 50)
		filter := storage.AuditListFilter{From: request.URL.Query().Get("from"), To: request.URL.Query().Get("to"), Category: request.URL.Query().Get("category"), Result: request.URL.Query().Get("result"), Target: request.URL.Query().Get("target"), OperatorIP: request.URL.Query().Get("operatorIp")}
		days, daysErr := parseLogDays(request)
		if daysErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": daysErr.Error()})
			return
		}
		if err := s.applyDefaultLogWindow(&filter.From, &filter.To, days); err != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		items, err := storage.ListAuditLogsPage(s.database, filter, page, pageSize)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, items)
		return
	}
	if parts[0] == "runtime" && len(parts) == 2 && parts[1] == "stream" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		s.runtimeStream(writer, request)
		return
	}
	if parts[0] == "runtime" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		items, usedLimit, err := storage.ListRuntimeLogs(s.database, queryInt(request, "limit", 100), request.URL.Query().Get("after"))
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "hasMore": len(items) == usedLimit})
		return
	}
	if parts[0] == "stats" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		days, daysErr := parseLogDays(request)
		if daysErr != nil {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": daysErr.Error()})
			return
		}
		stats, err := storage.QueryStats(s.database, time.Now(), s.snapshotSettings().Timezone, days)
		if err != nil {
			writeStorageError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, stats)
		return
	}
	if parts[0] == "settings" {
		s.logsSettingsAPI(writer, request)
		return
	}
	if parts[0] == "aggregate" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		if err := storage.AggregateAllMetrics(s.database); err != nil {
			writeStorageError(writer, err)
			return
		}
		s.recordAuditRequest(request, "logs.aggregate", "", "success")
		writeJSON(writer, http.StatusOK, map[string]string{"status": "aggregated"})
		return
	}
	if parts[0] == "cleanup" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		settings := s.snapshotSettings()
		retention := settings.Logging.RequestRetentionDays
		if retention <= 0 {
			retention = settings.Logging.RetentionDays
		}
		var payload struct {
			RetentionDays int `json:"retentionDays"`
		}
		if request.Body != nil && request.ContentLength != 0 {
			if !decodeJSON(writer, request, &payload) {
				return
			}
			if payload.RetentionDays > 0 {
				retention = payload.RetentionDays
			}
		}
		cleanupFailure := func(err error) {
			s.logger.Error("手动清理日志失败", "error", err)
			s.recordAuditRequest(request, "logs.cleanup", "", "failed")
			s.emitRuntime("error", "logs.cleanup.failed", "手动清理日志失败", map[string]any{"error": err.Error()})
			writeStorageError(writer, err)
		}
		contentCutoff := time.Now().UTC().AddDate(0, 0, -retention).Truncate(time.Hour)
		contentCount, contentErr := storage.CleanupContentBlobs(s.database, s.secrets, contentCutoff)
		if contentErr != nil {
			cleanupFailure(contentErr)
			return
		}
		auditRetention := settings.Logging.AuditRetentionDays
		if auditRetention <= 0 {
			auditRetention = retention
		}
		result, err := storage.CleanupLogsWithRetention(s.database, retention, auditRetention, time.Now())
		if err != nil {
			cleanupFailure(err)
			return
		}
		response := make(map[string]any, len(result)+5)
		for key, count := range result {
			response[key] = count
		}
		response["contentBlobs"] = contentCount
		runtimeRetention := settings.Logging.RuntimeRetentionDays
		if runtimeRetention <= 0 {
			runtimeRetention = 7
		}
		runtimeCutoff := time.Now().UTC().AddDate(0, 0, -runtimeRetention)
		runtimeCount, runtimeErr := storage.CleanupRuntimeLogs(s.database, runtimeCutoff)
		response["runtimeFiles"] = s.cleanupRuntimeFiles(runtimeCutoff)
		// 请求/正文已删除后 runtime 失败视为部分成功，避免调用方按整单失败重试。
		if runtimeErr != nil {
			response["runtimeLogs"] = int64(0)
			response["runtimeError"] = runtimeErr.Error()
			response["message"] = "请求日志已清理，运行日志清理失败"
			s.logger.Error("手动清理运行日志失败", "error", runtimeErr)
			s.recordAuditRequest(request, "logs.cleanup", "", "success")
			s.emitRuntime("warn", "logs.cleanup.partial", "请求日志已清理，运行日志清理失败", map[string]any{"result": response, "error": runtimeErr.Error()})
			writeJSON(writer, http.StatusMultiStatus, response)
			return
		}
		response["runtimeLogs"] = runtimeCount
		s.recordAuditRequest(request, "logs.cleanup", "", "success")
		s.emitRuntime("info", "logs.cleanup.completed", "手动清理日志完成", map[string]any{"result": response})
		writeJSON(writer, http.StatusOK, response)
		return
	}
	writeJSON(writer, http.StatusNotFound, map[string]string{"error": "未知日志接口"})
}

// validRequestLogStatus 校验请求列表支持的请求级状态，空值表示不筛选。
func validRequestLogStatus(status string) bool {
	switch status {
	case "", "processing", "success", "error", "timeout", "cancelled", "partial":
		return true
	default:
		return false
	}
}

// logsSettingsAPI 读取并保存日志正文策略；配置立即影响后续请求。
func (s *Service) logsSettingsAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		settings := s.snapshotSettings()
		logging := settings.Logging
		if logging.RequestRetentionDays <= 0 {
			logging.RequestRetentionDays = logging.RetentionDays
		}
		if logging.MaxRequestContentBytes <= 0 {
			logging.MaxRequestContentBytes = logging.MaxContentBytes
		}
		writeJSON(writer, http.StatusOK, map[string]any{"logging": logging, "timezone": settings.Timezone})
		return
	}
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, http.MethodGet, http.MethodPut)
		return
	}
	var payload struct {
		Logging  config.LoggingSettings `json:"logging"`
		Timezone string                 `json:"timezone"`
	}
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if payload.Logging.ContentPolicy != config.RequestLogContentPolicy {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "V1 请求日志固定保存四类快照"})
		return
	}
	if payload.Logging.RequestRetentionDays <= 0 {
		payload.Logging.RequestRetentionDays = payload.Logging.RetentionDays
	}
	if payload.Logging.RequestRetentionDays <= 0 {
		payload.Logging.RequestRetentionDays = 30
	}
	if payload.Logging.AuditRetentionDays <= 0 {
		payload.Logging.AuditRetentionDays = 30
	}
	if payload.Logging.RuntimeRetentionDays <= 0 {
		payload.Logging.RuntimeRetentionDays = 7
	}
	if payload.Logging.MaxRequestContentBytes <= 0 {
		payload.Logging.MaxRequestContentBytes = payload.Logging.MaxContentBytes
	}
	if payload.Logging.MaxRequestContentBytes <= 0 {
		payload.Logging.MaxRequestContentBytes = 1 << 20
	}
	if payload.Logging.MaxResponseContentBytes <= 0 {
		payload.Logging.MaxResponseContentBytes = 1 << 20
	}
	if payload.Logging.DiskQuotaBytes <= 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "正文大小和磁盘配额必须大于 0"})
		return
	}
	payload.Logging.RetentionDays = payload.Logging.RequestRetentionDays
	payload.Logging.MaxContentBytes = payload.Logging.MaxRequestContentBytes
	if payload.Logging.RuntimeLogMaxBytes <= 0 {
		payload.Logging.RuntimeLogMaxBytes = 100 << 20
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	current := s.snapshotSettings()
	if strings.TrimSpace(payload.Timezone) == "" {
		payload.Timezone = current.Timezone
	}
	if _, err := time.LoadLocation(payload.Timezone); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "时区必须是有效的 IANA 时区"})
		return
	}
	updated := current
	updated.Logging = payload.Logging
	updated.Timezone = payload.Timezone
	if err := storage.SaveRuntimeSettings(s.database, updated); err != nil {
		writeStorageError(writer, err)
		return
	}
	s.publishSettings(updated)
	s.recordAuditRequest(request, "logs.settings.update", "", "success")
	writeJSON(writer, http.StatusOK, map[string]any{"logging": updated.Logging, "timezone": updated.Timezone})
}

// recordAudit 记录不含令牌和正文的管理操作；可选 request 用于保存真实操作 IP。
func (s *Service) recordAudit(action, targetID string, requests ...*http.Request) {
	if len(requests) > 0 {
		s.recordAuditRequest(requests[0], action, targetID, "success")
		return
	}
	s.recordAuditEntry(action, targetID, "success", "")
}

func (s *Service) recordAuditRequest(request *http.Request, action, targetID, result string) {
	operatorIP := ""
	if request != nil {
		operatorIP = requestOperatorIP(request)
	}
	s.recordAuditEntry(action, targetID, result, operatorIP)
}

func requestOperatorIP(request *http.Request) string {
	if request == nil {
		return ""
	}
	operatorIP, _, _ := net.SplitHostPort(request.RemoteAddr)
	if operatorIP == "" {
		return request.RemoteAddr
	}
	return operatorIP
}

func (s *Service) recordAuditEntry(action, targetID, result, operatorIP string) {
	id, err := storage.NewAuditLogID()
	if err != nil {
		return
	}
	if err := storage.RecordAuditLog(s.database, storage.AuditLog{ID: id, ActorType: "admin", Action: action, TargetID: targetID, Result: result, OperatorIP: operatorIP}); err != nil {
		s.logger.Warn("写入管理审计失败", "action", action, "error", err)
	}
}

const invalidLogDaysError = "days 必须是 1、7 或 30"

// parseLogDays 解析请求、审计和统计共用的 days 参数；未出现时默认 1。
func parseLogDays(request *http.Request) (int, error) {
	values, present := request.URL.Query()["days"]
	if !present {
		return 1, nil
	}
	if len(values) != 1 {
		return 0, errors.New(invalidLogDaysError)
	}
	parsedDays, err := strconv.Atoi(strings.TrimSpace(values[0]))
	if err != nil || (parsedDays != 1 && parsedDays != 7 && parsedDays != 30) {
		return 0, errors.New(invalidLogDaysError)
	}
	return parsedDays, nil
}

// applyDefaultLogWindow 仅在未提供 from/to 时，按 days 填入默认统计窗口。
func (s *Service) applyDefaultLogWindow(from, to *string, days int) error {
	if strings.TrimSpace(*from) != "" || strings.TrimSpace(*to) != "" {
		return nil
	}
	windowFrom, windowTo, err := storage.StatsWindow(time.Now(), s.snapshotSettings().Timezone, days)
	if err != nil {
		return err
	}
	*from, *to = storage.FormatSQLiteTime(windowFrom), storage.FormatSQLiteTime(windowTo)
	return nil
}

func queryInt(request *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(request.URL.Query().Get(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func methodNotAllowed(writer http.ResponseWriter, methods ...string) {
	allowed := strings.Join(methods, ", ")
	writer.Header().Set("Allow", allowed)
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "请求方法不被允许"})
}
