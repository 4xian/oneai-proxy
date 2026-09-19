package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// RequestPolicy 保存代理请求的时间和跨渠道尝试预算。
type RequestPolicy struct {
	ConnectTimeoutMs    int
	FirstByteTimeoutMs  int
	StreamIdleTimeoutMs int
	TotalTimeoutMs      int
	MaxChannelAttempts  int
}

// ChannelSettings 保存应用到所有代理请求的渠道级请求体策略。
type ChannelSettings struct {
	ReasoningEffort        string `json:"reasoningEffort"`
	ServiceTierPassthrough bool   `json:"serviceTierPassthrough"`
}

// ReasoningEffortPassthrough 表示保留客户端请求中的原始思考等级。
const ReasoningEffortPassthrough = "passthrough"

// IsValidReasoningEffort 判断思考等级是否为支持的值。
func IsValidReasoningEffort(value string) bool {
	switch value {
	case ReasoningEffortPassthrough, "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// NormalizeChannelSettings 补齐旧配置缺少的渠道策略默认值。
func NormalizeChannelSettings(settings ChannelSettings) ChannelSettings {
	if settings.ReasoningEffort == "" {
		settings.ReasoningEffort = ReasoningEffortPassthrough
	}
	return settings
}

// LoggingSettings 保存日志正文策略、各类日志保留期和磁盘限制。
type LoggingSettings struct {
	ContentPolicy           string `json:"contentPolicy"`
	RetentionDays           int    `json:"retentionDays,omitempty"` // 兼容旧配置，作为请求日志保留期
	RequestRetentionDays    int    `json:"requestRetentionDays"`
	AuditRetentionDays      int    `json:"auditRetentionDays"`
	RuntimeRetentionDays    int    `json:"runtimeRetentionDays"`
	MaxContentBytes         int    `json:"maxContentBytes,omitempty"` // 兼容旧配置，作为请求大小上限
	MaxRequestContentBytes  int    `json:"maxRequestContentBytes"`
	MaxResponseContentBytes int    `json:"maxResponseContentBytes"`
	DiskQuotaBytes          int64  `json:"diskQuotaBytes"`
	RuntimeLogMaxBytes      int64  `json:"runtimeLogMaxBytes"`
}

// RequestLogContentPolicy 是 V1 固定保存四类请求/响应快照的正文策略。
const RequestLogContentPolicy = "request_and_response_content"

// Settings 保存本地服务启动配置及运行策略。
type Settings struct {
	ProxyListen             string
	AdminListen             string
	DataDirectory           string
	AllowLocalDefaultTokens bool `json:"-"`
	RequestPolicy           RequestPolicy
	ChannelSettings         ChannelSettings `json:"channelSettings"`
	Logging                 LoggingSettings
	Timezone                string
}

// New 根据命令行覆盖值创建并校验默认运行配置。
func New(proxyListen, adminListen, dataDirectory string) (Settings, error) {
	if proxyListen == "" {
		proxyListen = "127.0.0.1:9988"
	}
	if adminListen == "" {
		adminListen = "127.0.0.1:9989"
	}
	if err := validateListen("代理", proxyListen); err != nil {
		return Settings{}, err
	}
	if err := validateListen("管理", adminListen); err != nil {
		return Settings{}, err
	}
	proxyHost, proxyPort, _ := net.SplitHostPort(proxyListen)
	adminHost, adminPort, _ := net.SplitHostPort(adminListen)
	if strings.EqualFold(proxyHost, adminHost) && proxyPort == adminPort {
		return Settings{}, fmt.Errorf("代理和管理监听地址不能相同: %s", proxyListen)
	}
	if dataDirectory == "" {
		dataDirectory = defaultDataDirectory()
	}
	return Settings{
		ProxyListen:   proxyListen,
		AdminListen:   adminListen,
		DataDirectory: dataDirectory,
		RequestPolicy: RequestPolicy{
			ConnectTimeoutMs:    5000,
			FirstByteTimeoutMs:  30000,
			StreamIdleTimeoutMs: 60000,
			TotalTimeoutMs:      120000,
			MaxChannelAttempts:  0,
		},
		ChannelSettings: ChannelSettings{ReasoningEffort: ReasoningEffortPassthrough, ServiceTierPassthrough: true},
		Logging:         LoggingSettings{ContentPolicy: RequestLogContentPolicy, RetentionDays: 30, RequestRetentionDays: 30, AuditRetentionDays: 30, RuntimeRetentionDays: 7, MaxContentBytes: 1 << 20, MaxRequestContentBytes: 1 << 20, MaxResponseContentBytes: 1 << 20, DiskQuotaBytes: 100 << 20, RuntimeLogMaxBytes: 100 << 20},
		Timezone:        "Asia/Shanghai",
	}, nil
}

func validateListen(name, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s监听地址无效: %s", name, address)
	}
	if portNumber := parsePort(port); portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s监听端口无效: %s", name, port)
	}
	return nil
}

func parsePort(value string) int {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return port
}

// ListenerParts 将监听地址拆成管理页使用的 IP 和端口字段。
func ListenerParts(address string) (string, int, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	return host, parsePort(port), nil
}

// ListenerAddress 将管理页的 IP 和端口组合为标准监听地址。
func ListenerAddress(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}

func defaultDataDirectory() string {
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "OneAI Proxy")
		}
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "OneAI Proxy")
		}
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, "OneAI Proxy")
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "oneai-proxy")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "oneai-proxy")
	}
	return "./data"
}
