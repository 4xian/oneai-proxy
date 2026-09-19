package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/secret"
	"github.com/4xian/oneai-proxy/internal/storage"
)

const frontendDevPort = "5188"

//go:embed all:web
var embeddedFiles embed.FS

// Service 持有代理和管理两个 HTTP 服务。
type Service struct {
	settings           config.Settings
	boundProxyListen   string
	boundAdminListen   string
	database           *sql.DB
	logger             *slog.Logger
	auth               storage.AuthTokens
	secrets            secret.Store
	authMu             sync.RWMutex
	probeMu            sync.Mutex
	settingsMu         sync.RWMutex
	dataMu             sync.RWMutex
	migrationBarrierMu sync.RWMutex
	channelMu          sync.Mutex
	probeRun           map[string]bool
	probeLast          map[string]time.Time
	probeSlots         chan struct{}
	channelInFlight    map[string]int
	channelNext        uint64
	migrationSlots     chan struct{}
	runtimeMu          sync.Mutex
	runtimeSubs        map[int]chan storage.RuntimeLog
	runtimeNext        int
	runtimeFile        *os.File
	runtimeDay         string
	startedAt          time.Time
}

// New 创建本地服务实例。
func New(settings config.Settings, database *sql.DB, logger *slog.Logger, auth storage.AuthTokens, stores ...secret.Store) *Service {
	store := secret.NewResilientStore(settings.DataDirectory, logger)
	if len(stores) > 0 && stores[0] != nil {
		store = stores[0]
	}
	return &Service{settings: settings, boundProxyListen: settings.ProxyListen, boundAdminListen: settings.AdminListen, database: database, logger: logger, auth: auth, secrets: store, probeRun: make(map[string]bool), probeLast: make(map[string]time.Time), probeSlots: make(chan struct{}, 4), channelInFlight: make(map[string]int), migrationSlots: make(chan struct{}, 1), runtimeSubs: make(map[int]chan storage.RuntimeLog), startedAt: time.Now().UTC()}
}

// snapshotSettings 复制当前热设置，只使用独立的 settingsMu，避免与数据库写锁重入。
func (s *Service) snapshotSettings() config.Settings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

// publishSettings 一次性发布完整设置快照，只使用独立的 settingsMu。
func (s *Service) publishSettings(next config.Settings) {
	s.settingsMu.Lock()
	s.settings = next
	s.settingsMu.Unlock()
}

// boundAdminAddress 返回当前进程实际绑定的管理地址，供 Host/Origin 边界使用。
func (s *Service) boundAdminAddress() string {
	if strings.TrimSpace(s.boundAdminListen) != "" {
		return s.boundAdminListen
	}
	return s.snapshotSettings().AdminListen
}

// boundProxyAddress 返回当前进程实际绑定的代理地址。
func (s *Service) boundProxyAddress() string {
	if strings.TrimSpace(s.boundProxyListen) != "" {
		return s.boundProxyListen
	}
	return s.snapshotSettings().ProxyListen
}

// listenerRestartRequired 判断已持久化监听地址是否与当前实际绑定不同。
func (s *Service) listenerRestartRequired(settings config.Settings) bool {
	return settings.ProxyListen != s.boundProxyAddress() || settings.AdminListen != s.boundAdminAddress()
}

// Run 并行启动两个监听器，并在收到退出信号后优雅停止。
func (s *Service) Run(ctx context.Context) error {
	if err := storage.ResetHalfOpenClaims(s.database); err != nil {
		return err
	}
	adminHandler, err := s.adminHandler()
	if err != nil {
		return err
	}
	proxyServer := &http.Server{Addr: s.boundProxyAddress(), Handler: s.proxyHandler()}
	adminServer := &http.Server{Addr: s.boundAdminAddress(), Handler: adminHandler}
	proxyListener, err := listen(s.boundProxyAddress(), "代理")
	if err != nil {
		return err
	}
	adminListener, err := listen(s.boundAdminAddress(), "管理")
	if err != nil {
		_ = proxyListener.Close()
		return err
	}
	errs := make(chan error, 2)
	go serveHTTP(adminServer, adminListener, errs)
	go serveHTTP(proxyServer, proxyListener, errs)
	go s.runProbeScheduler(ctx)
	go s.runLogCleanupScheduler(ctx)
	go s.runStorageMaintenanceScheduler(ctx)
	s.emitRuntime("info", "database.migration.completed", "数据库迁移检查完成", nil)
	s.cleanupPendingMigrationSecrets()
	s.emitRuntime("info", "service.started", "服务监听已启动", map[string]any{"proxy": s.boundProxyAddress(), "admin": s.boundAdminAddress()})

	select {
	case <-ctx.Done():
		s.emitRuntime("info", "service.stopping", "服务正在停止", nil)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		adminErr := adminServer.Shutdown(shutdownCtx)
		proxyErr := proxyServer.Shutdown(shutdownCtx)
		if adminErr != nil {
			return adminErr
		}
		return proxyErr
	case err := <-errs:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = adminServer.Shutdown(shutdownCtx)
		_ = proxyServer.Shutdown(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// runLogCleanupScheduler 每 24 小时触发一次日志保留期清理。
func (s *Service) runLogCleanupScheduler(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.cleanupExpiredLogs(now)
		}
	}
}

// runStorageMaintenanceScheduler 每小时补齐缺失聚合并压缩探针历史，不占用管理写锁。
func (s *Service) runStorageMaintenanceScheduler(ctx context.Context) {
	s.maintainHotStorage(time.Now())
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.maintainHotStorage(now)
		}
	}
}

// maintainHotStorage 在请求路径外补齐完整小时聚合，并限制探针明细增长。
func (s *Service) maintainHotStorage(now time.Time) {
	if err := storage.AggregateMissingMetrics(s.database, now); err != nil {
		s.emitRuntime("error", "metrics.aggregate.failed", "补齐小时聚合失败", map[string]any{"error": err.Error()})
	}
	settings := s.snapshotSettings()
	requestRetention := settings.Logging.RequestRetentionDays
	if requestRetention <= 0 {
		requestRetention = settings.Logging.RetentionDays
	}
	count, err := storage.TrimProbeRuns(s.database, requestRetention, now)
	if err != nil {
		s.emitRuntime("error", "logs.probe_trim.failed", "压缩探针历史失败", map[string]any{"error": err.Error()})
		return
	}
	if count > 0 {
		s.emitRuntime("info", "logs.probe_trim.completed", "压缩探针历史完成", map[string]any{"probeRuns": count})
	}
}

// cleanupExpiredLogs 执行一次保留期清理；正文失败时保留请求行，避免文件和密钥引用孤立。
func (s *Service) cleanupExpiredLogs(now time.Time) {
	result := make(map[string]int64)
	cleanupFailed := false
	settings := s.snapshotSettings()
	requestRetention := settings.Logging.RequestRetentionDays
	if requestRetention <= 0 {
		requestRetention = settings.Logging.RetentionDays
	}
	if requestRetention <= 0 {
		requestRetention = 30
	}
	contentCutoff := now.UTC().AddDate(0, 0, -requestRetention).Truncate(time.Hour)
	if count, err := storage.CleanupContentBlobs(s.database, s.secrets, contentCutoff); err != nil {
		cleanupFailed = true
		s.emitRuntime("error", "logs.content_cleanup.failed", "自动清理正文失败", map[string]any{"error": err.Error()})
	} else {
		result["contentBlobs"] = count
		auditRetention := settings.Logging.AuditRetentionDays
		if auditRetention <= 0 {
			auditRetention = requestRetention
		}
		if counts, err := storage.CleanupLogsWithRetention(s.database, requestRetention, auditRetention, now); err != nil {
			cleanupFailed = true
			s.emitRuntime("error", "logs.cleanup.failed", "自动清理日志失败", map[string]any{"error": err.Error()})
		} else {
			for key, count := range counts {
				result[key] = count
			}
		}
	}
	runtimeRetention := settings.Logging.RuntimeRetentionDays
	if runtimeRetention <= 0 {
		runtimeRetention = 7
	}
	if count, err := storage.CleanupRuntimeLogs(s.database, now.UTC().AddDate(0, 0, -runtimeRetention)); err != nil {
		cleanupFailed = true
		s.logger.Error("自动清理运行日志失败", "error", err)
		s.emitRuntime("error", "logs.runtime_cleanup.failed", "自动清理运行日志失败", map[string]any{"error": err.Error()})
	} else {
		result["runtimeLogs"] = count
	}
	result["runtimeFiles"] = s.cleanupRuntimeFiles(now.UTC().AddDate(0, 0, -runtimeRetention))
	if cleanupFailed {
		s.emitRuntime("warn", "logs.cleanup.partial", "自动清理日志部分失败", map[string]any{"result": result})
		return
	}
	s.emitRuntime("info", "logs.cleanup.completed", "自动清理日志完成", map[string]any{"result": result})
}

// cleanupPendingMigrationSecrets 重试上次迁移提交后未清理的旧凭证。
func (s *Service) cleanupPendingMigrationSecrets() {
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	refs, err := storage.LoadMigrationPendingSecretRefs(s.database)
	if err != nil {
		s.logger.Warn("读取迁移待清理凭证失败", "error", err)
		return
	}
	if len(refs) == 0 {
		return
	}
	remaining := make([]string, 0, len(refs))
	for _, ref := range refs {
		referenced, referenceErr := storage.IsSecretRefReferenced(s.database, ref)
		if referenceErr != nil {
			s.logger.Warn("检查迁移待清理凭证引用失败", "secretRef", ref, "error", referenceErr)
			remaining = append(remaining, ref)
			continue
		}
		if referenced {
			continue
		}
		if err := s.secrets.Delete(ref); err != nil {
			remaining = append(remaining, ref)
			s.logger.Warn("重试清理迁移旧凭证失败", "error", err)
		}
	}
	if len(remaining) == 0 {
		if err := storage.ClearMigrationPendingSecretRefs(s.database); err != nil {
			s.logger.Warn("清除迁移待清理凭证记录失败", "error", err)
		}
		return
	}
	if err := storage.SaveMigrationPendingSecretRefs(s.database, remaining); err != nil {
		s.logger.Warn("更新迁移待清理凭证记录失败", "error", err)
	}
}

func serveHTTP(server *http.Server, listener net.Listener, errs chan<- error) {
	if err := server.Serve(listener); err != nil {
		errs <- err
	}
}

func listen(address, name string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%s监听地址 %s 不可用，请检查端口占用: %w", name, address, err)
	}
	return listener, nil
}

func (s *Service) adminHandler() (http.Handler, error) {
	files, err := fs.Sub(embeddedFiles, "web")
	if err != nil {
		return nil, err
	}
	static := http.FileServer(http.FS(files))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	api := http.NewServeMux()
	api.HandleFunc("/api/admin/v1/auth", s.authAPI)
	api.HandleFunc("/api/admin/v1/settings", s.settingsAPI)
	api.HandleFunc("/api/admin/v1/overview", s.overviewAPI)
	api.HandleFunc("/api/admin/v1/channels", s.channelsAPI)
	api.HandleFunc("/api/admin/v1/channels/", s.channelsAPI)
	api.HandleFunc("/api/admin/v1/models", s.modelsAPI)
	api.HandleFunc("/api/admin/v1/models/", s.modelsAPI)
	api.HandleFunc("/api/admin/v1/config/export", s.configExportAPI)
	api.HandleFunc("/api/admin/v1/config/import", s.configImportAPI)
	api.HandleFunc("/api/admin/v1/config/preview", s.configPreviewAPI)
	api.HandleFunc("/api/admin/v1/migration/export", s.migrationExportAPI)
	api.HandleFunc("/api/admin/v1/migration/preview", s.migrationPreviewAPI)
	api.HandleFunc("/api/admin/v1/migration/import", s.migrationImportAPI)
	api.HandleFunc("/api/admin/v1/config/diagnostics", s.configDiagnosticsAPI)
	api.HandleFunc("/api/admin/v1/config/backup", s.configBackupAPI)
	api.HandleFunc("/api/admin/v1/config/backup/verify", s.configBackupVerifyAPI)
	api.HandleFunc("/api/admin/v1/config/backup/restore", s.configBackupRestoreAPI)
	api.HandleFunc("/api/admin/v1/config/diagnostics/export", s.configDiagnosticsExportAPI)
	api.HandleFunc("/api/admin/v1/logs", s.logsAPI)
	api.HandleFunc("/api/admin/v1/logs/", s.logsAPI)
	mux.Handle("/api/admin/v1/", s.runtimeRequestLogger(s.adminBoundary(s.adminAuth(api))))
	mux.Handle("/", s.adminHeaders(static))
	return mux, nil
}

// runtimeRequestLogger 记录管理 API 的完成请求，不保存查询参数、请求头或正文。
func (s *Service) runtimeRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !shouldLogRuntimeRequest(request.URL.Path) {
			next.ServeHTTP(writer, request)
			return
		}
		startedAt := time.Now()
		capture := &runtimeResponseWriter{ResponseWriter: writer}
		next.ServeHTTP(capture, request)
		status := capture.status
		if status == 0 {
			status = http.StatusOK
		}
		level := "info"
		if status >= http.StatusInternalServerError {
			level = "error"
		} else if status >= http.StatusBadRequest {
			level = "warn"
		}
		attrs := map[string]any{
			"method":     request.Method,
			"path":       request.URL.Path,
			"status":     status,
			"durationMs": time.Since(startedAt).Milliseconds(),
		}
		if operatorIP := requestOperatorIP(request); operatorIP != "" {
			attrs["remoteIP"] = operatorIP
		}
		s.emitRuntime(level, "http.request.completed", "管理接口请求完成", attrs)
	})
}

// shouldLogRuntimeRequest 排除运行日志读取接口，避免日志页面自身制造无意义的访问日志。
func shouldLogRuntimeRequest(path string) bool {
	if path == "/api/admin/v1/logs/runtime" || strings.HasPrefix(path, "/api/admin/v1/logs/runtime/") {
		return false
	}
	return strings.HasPrefix(path, "/api/admin/v1/")
}

type runtimeResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *runtimeResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *runtimeResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *runtimeResponseWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Service) proxyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", s.proxyAuth(http.HandlerFunc(s.forwardAPI)))
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "代理入口只接受 /v1/*"})
	})
	return mux
}

// adminAuth 保护管理 API，只接受管理令牌 Bearer 认证。
func (s *Service) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		valid := hasBearerToken(request, func(token string) bool {
			settings := s.snapshotSettings()
			s.authMu.RLock()
			defer s.authMu.RUnlock()
			return storage.AuthenticateAuthToken(s.auth, "admin", token) ||
				(settings.AllowLocalDefaultTokens && !s.auth.AdminCustom && token == storage.LocalDefaultAdminToken)
		})
		if !valid {
			s.recordAuditRequest(request, "auth.admin.access", request.URL.Path, "rejected")
			writer.Header().Set("WWW-Authenticate", `Bearer realm="oneai-admin"`)
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "需要有效的管理令牌"})
			return
		}
		capture := &auditResponseWriter{ResponseWriter: writer}
		next.ServeHTTP(capture, request)
		if capture.status >= http.StatusBadRequest {
			result := "failed"
			if capture.status == http.StatusUnauthorized || capture.status == http.StatusForbidden {
				result = "rejected"
			}
			s.recordAuditRequest(request, auditActionForPath(request.URL.Path), request.URL.Path, result)
		} else if request.Method == http.MethodGet && isHighRiskAuditPath(request.URL.Path) {
			s.recordAuditRequest(request, auditActionForPath(request.URL.Path), request.URL.Path, "success")
		}
	})
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *auditResponseWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *auditResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *auditResponseWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func auditActionForPath(path string) string {
	if strings.HasSuffix(path, "/config/diagnostics/export") {
		return "config.diagnostics.export"
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/admin/v1/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "admin.operation"
	}
	switch parts[0] {
	case "channels":
		return "channels.operation"
	case "models":
		return "models.operation"
	case "config":
		return "config.operation"
	case "auth":
		return "auth.operation"
	case "settings", "logs":
		return "logs.operation"
	default:
		return "admin.operation"
	}
}

func isHighRiskAuditPath(path string) bool {
	return strings.Contains(path, "/credential") || strings.Contains(path, "/diagnostics") || strings.Contains(path, "/logs/requests/") && strings.Contains(path, "/content")
}

// proxyAuth 保护代理入口，只接受独立的代理令牌 Bearer 认证。
func (s *Service) proxyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !hasBearerToken(request, func(token string) bool {
			settings := s.snapshotSettings()
			s.authMu.RLock()
			defer s.authMu.RUnlock()
			return storage.AuthenticateAuthToken(s.auth, "proxy", token) ||
				(settings.AllowLocalDefaultTokens && !s.auth.ProxyCustom && token == storage.LocalDefaultProxyToken)
		}) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="oneai-proxy"`)
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "需要有效的代理令牌"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// adminBoundary 限制管理 API 的 Host 和跨站写请求来源。
func (s *Service) adminBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		boundAdmin := s.boundAdminAddress()
		if !allowedAdminHost(request.Host, boundAdmin) {
			s.recordAuditRequest(request, "auth.admin.boundary", request.URL.Path, "rejected")
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "管理 API Host 不被允许"})
			return
		}
		origin := strings.TrimSpace(request.Header.Get("Origin"))
		if origin != "" && request.Method != http.MethodGet && !allowedAdminOrigin(origin, boundAdmin) {
			s.recordAuditRequest(request, "auth.admin.boundary", request.URL.Path, "rejected")
			writeJSON(writer, http.StatusForbidden, map[string]string{"error": "管理 API 来源不被允许"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func allowedAdminHost(value, listen string) bool {
	host, port, err := splitHostPort(value)
	listenHost, listenPort, err := splitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if port != listenPort {
		return false
	}
	if strings.EqualFold(host, listenHost) || strings.EqualFold(host, "localhost") && isLoopbackHost(listenHost) {
		return true
	}
	return false
}

func allowedAdminOrigin(origin, listen string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	originHost, originPort, err := net.SplitHostPort(parsed.Host)
	listenHost, listenPort, listenErr := net.SplitHostPort(listen)
	if err != nil || listenErr != nil || originHost == "" || originPort == "" {
		return false
	}
	if originPort == listenPort {
		return strings.EqualFold(originHost, listenHost) || strings.EqualFold(originHost, "localhost") && isLoopbackHost(listenHost)
	}
	// 开发服务器固定使用回环端口 5188；仅在管理监听器也绑定回环地址时放行。
	return originPort == frontendDevPort && isLoopbackHost(originHost) && isLoopbackHost(listenHost)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func splitHostPort(value string) (string, string, error) {
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		return host, port, nil
	}
	return value, "", err
}

// requestBearerToken 读取 Authorization 中的 Bearer 令牌原文，缺失或格式不正确时返回空字符串。
func requestBearerToken(request *http.Request) string {
	value := strings.TrimSpace(request.Header.Get("Authorization"))
	if len(value) < len("Bearer ") || !strings.EqualFold(value[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(value[len("Bearer "):])
}

func hasBearerToken(request *http.Request, validate func(string) bool) bool {
	token := requestBearerToken(request)
	return token != "" && validate(token)
}

func (s *Service) adminHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func (s *Service) health(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{
		"status":        "ok",
		"proxyListener": s.boundProxyAddress(),
		"adminListener": s.boundAdminAddress(),
		"database":      "ready",
		"schemaVersion": fmt.Sprintf("%d", storage.CurrentSchemaVersion()),
	})
}

type listenerPayload struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type settingsPayload struct {
	ProxyListener      listenerPayload        `json:"proxyListener"`
	AdminListener      listenerPayload        `json:"adminListener"`
	CurrentListener    listenerPayload        `json:"currentListener,omitempty"`
	ConfiguredListener listenerPayload        `json:"configuredListener,omitempty"`
	RequestPolicy      requestPolicyPayload   `json:"requestPolicy"`
	ChannelSettings    *config.ChannelSettings `json:"channelSettings"`
	RestartRequired    bool                   `json:"restartRequired,omitempty"`
}

type requestPolicyPayload struct {
	ConnectTimeoutMs    int `json:"connectTimeoutMs"`
	FirstByteTimeoutMs  int `json:"firstByteTimeoutMs"`
	StreamIdleTimeoutMs int `json:"streamIdleTimeoutMs"`
	TotalTimeoutMs      int `json:"totalTimeoutMs"`
	MaxChannelAttempts  int `json:"maxChannelAttempts"`
}

func listenerPayloadFrom(address string) listenerPayload {
	host, port, _ := config.ListenerParts(address)
	return listenerPayload{Host: host, Port: port}
}

func (s *Service) settingsAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		settings := s.snapshotSettings()
		writeJSON(writer, http.StatusOK, s.settingsResponse(settings))
		return
	}
	if request.Method != http.MethodPut {
		writer.Header().Set("Allow", "GET, PUT")
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET 和 PUT"})
		return
	}
	var payload settingsPayload
	if !decodeJSON(writer, request, &payload) {
		return
	}
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	current := s.snapshotSettings()
	proxyListen := config.ListenerAddress(payload.ProxyListener.Host, payload.ProxyListener.Port)
	adminListen := config.ListenerAddress(payload.AdminListener.Host, payload.AdminListener.Port)
	updated, err := config.New(proxyListen, adminListen, current.DataDirectory)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if payload.RequestPolicy.ConnectTimeoutMs <= 0 || payload.RequestPolicy.FirstByteTimeoutMs <= 0 || payload.RequestPolicy.StreamIdleTimeoutMs <= 0 || payload.RequestPolicy.TotalTimeoutMs <= 0 || payload.RequestPolicy.MaxChannelAttempts < 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "请求策略超时必须大于 0，最大渠道尝试数不能为负数"})
		return
	}
	if payload.ChannelSettings == nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "必须提交 channelSettings"})
		return
	}
	channelSettings := config.NormalizeChannelSettings(*payload.ChannelSettings)
	if !config.IsValidReasoningEffort(channelSettings.ReasoningEffort) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "全局思考等级无效"})
		return
	}
	updated.RequestPolicy = config.RequestPolicy{
		ConnectTimeoutMs:    payload.RequestPolicy.ConnectTimeoutMs,
		FirstByteTimeoutMs:  payload.RequestPolicy.FirstByteTimeoutMs,
		StreamIdleTimeoutMs: payload.RequestPolicy.StreamIdleTimeoutMs,
		TotalTimeoutMs:      payload.RequestPolicy.TotalTimeoutMs,
		MaxChannelAttempts:  payload.RequestPolicy.MaxChannelAttempts,
	}
	updated.ChannelSettings = channelSettings
	updated.Logging = current.Logging
	updated.Timezone = current.Timezone
	updated.AllowLocalDefaultTokens = current.AllowLocalDefaultTokens
	if err := storage.SaveRuntimeSettings(s.database, updated); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.publishSettings(updated)
	s.recordAudit("settings.update", "", request)
	writeJSON(writer, http.StatusOK, s.settingsResponse(updated))
}

func (s *Service) settingsResponse(settings config.Settings) settingsPayload {
	return settingsPayload{
		ProxyListener:      listenerPayloadFrom(settings.ProxyListen),
		AdminListener:      listenerPayloadFrom(settings.AdminListen),
		CurrentListener:    listenerPayloadFrom(s.boundAdminAddress()),
		ConfiguredListener: listenerPayloadFrom(settings.AdminListen),
		RequestPolicy: requestPolicyPayload{
			ConnectTimeoutMs:    settings.RequestPolicy.ConnectTimeoutMs,
			FirstByteTimeoutMs:  settings.RequestPolicy.FirstByteTimeoutMs,
			StreamIdleTimeoutMs: settings.RequestPolicy.StreamIdleTimeoutMs,
			TotalTimeoutMs:      settings.RequestPolicy.TotalTimeoutMs,
			MaxChannelAttempts:  settings.RequestPolicy.MaxChannelAttempts,
		},
		ChannelSettings: ptrChannelSettings(config.NormalizeChannelSettings(settings.ChannelSettings)),
		RestartRequired: s.listenerRestartRequired(settings),
	}
}

func ptrChannelSettings(value config.ChannelSettings) *config.ChannelSettings {
	return &value
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
