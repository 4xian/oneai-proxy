package server

import (
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/storage"
)

type overviewResponse struct {
	Metrics        storage.OverviewMetrics   `json:"metrics"`
	Trends         []storage.OverviewTrend   `json:"trends"`
	ProxyAccess    overviewProxyAccess       `json:"proxyAccess"`
	Channels       []storage.OverviewChannel `json:"channels"`
	RecentRequests []storage.OverviewRequest `json:"recentRequests"`
	SystemInfo     overviewSystemInfo        `json:"systemInfo"`
}

type overviewProxyAccess struct {
	BaseURL string `json:"baseUrl"`
	WebURL  string `json:"webUrl"`
}

type overviewSystemInfo struct {
	Version       string  `json:"version"`
	OS            string  `json:"os"`
	GoVersion     string  `json:"goVersion"`
	MemoryBytes   uint64  `json:"memoryBytes"`
	MemoryAlloc   uint64  `json:"memoryAlloc"`
	Goroutines    int     `json:"goroutines"`
	StartedAt     string  `json:"startedAt"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
	ProxyListen   string  `json:"proxyListen"`
	AdminListen   string  `json:"adminListen"`
}

// overviewAPI 返回总览页需要的真实指标、渠道、请求和运行时信息。
func (s *Service) overviewAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	snapshot, err := storage.QueryOverview(s.database, time.Now())
	if err != nil {
		writeStorageError(writer, err)
		return
	}
	startedAt := s.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	proxyListen := s.boundProxyAddress()
	adminListen := s.boundAdminAddress()
	proxyHost, proxyPort, _ := config.ListenerParts(proxyListen)
	adminHost, adminPort, _ := config.ListenerParts(adminListen)
	proxyBase := "http://" + config.ListenerAddress(overviewAccessHost(proxyHost), proxyPort) + "/v1"
	webURL := "http://" + config.ListenerAddress(overviewAccessHost(adminHost), adminPort)
	writeJSON(writer, http.StatusOK, overviewResponse{
		Metrics: snapshot.Metrics, Trends: snapshot.Trends, Channels: snapshot.Channels, RecentRequests: snapshot.RecentRequests,
		ProxyAccess: overviewProxyAccess{BaseURL: proxyBase, WebURL: webURL},
		SystemInfo:  overviewSystemInfo{Version: exportAppVersion, OS: runtime.GOOS, GoVersion: runtime.Version(), MemoryBytes: memory.Sys, MemoryAlloc: memory.Alloc, Goroutines: runtime.NumGoroutine(), StartedAt: storage.FormatSQLiteTime(startedAt), UptimeSeconds: time.Since(startedAt).Seconds(), ProxyListen: proxyListen, AdminListen: adminListen},
	})
}

// overviewAccessHost 将通配监听地址转换为本机客户端可连接的地址。
func overviewAccessHost(host string) string {
	address := net.ParseIP(host)
	if address != nil && address.IsUnspecified() {
		return "127.0.0.1"
	}
	return host
}
