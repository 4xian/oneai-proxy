package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/4xian/oneai-proxy/internal/config"
	"github.com/4xian/oneai-proxy/internal/secret"
	_ "modernc.org/sqlite"
)

const (
	adminTokenSecretRef = "auth-admin-token"
	proxyTokenSecretRef = "auth-proxy-token"
)

const schemaVersion = 27

// CurrentSchemaVersion 返回当前 SQLite 数据模型版本，供迁移和诊断契约校验使用。
func CurrentSchemaVersion() int {
	return schemaVersion
}

// BackupDatabase 创建导入前的 SQLite 一致性快照，并返回快照路径。
func BackupDatabase(database *sql.DB, dataDirectory string) (string, error) {
	backupDirectory := filepath.Join(dataDirectory, "backups")
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	path := filepath.Join(backupDirectory, fmt.Sprintf("config-%d.db", time.Now().UTC().UnixNano()))
	if _, err := database.Exec(`VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("创建 SQLite 配置备份失败: %w", err)
	}
	return path, nil
}

// RestoreDatabaseSnapshot 将 SQLite 快照恢复到当前数据库，不修改正文文件。
func RestoreDatabaseSnapshot(database *sql.DB, snapshotPath string) error {
	if strings.TrimSpace(snapshotPath) == "" {
		return fmt.Errorf("SQLite 快照路径为空")
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		return fmt.Errorf("SQLite 快照不存在: %w", err)
	}
	conn, err := database.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("获取恢复数据库连接失败: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "ATTACH DATABASE ? AS migration_restore", snapshotPath); err != nil {
		return fmt.Errorf("挂载 SQLite 快照失败: %w", err)
	}
	detached := false
	defer func() {
		if !detached {
			_, _ = conn.ExecContext(context.Background(), "DETACH DATABASE migration_restore")
		}
	}()
	transaction, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("开始 SQLite 快照恢复事务失败: %w", err)
	}
	defer transaction.Rollback()
	deleteTables := []string{"content_blobs", "hourly_metric_dirty", "hourly_metrics", "audit_logs", "probe_runs", "health_events", "health_runtime", "attempts", "request_route_events", "requests", "response_affinity", "probe_policies", "channel_models", "channel_model_mappings", "global_model_mappings", "model_catalog", "channels", "app_settings"}
	insertTables := []string{"app_settings", "channels", "global_model_mappings", "channel_model_mappings", "channel_models", "probe_policies", "response_affinity", "model_catalog", "requests", "request_route_events", "attempts", "health_runtime", "health_events", "probe_runs", "audit_logs", "hourly_metrics", "hourly_metric_dirty", "content_blobs"}
	for _, table := range deleteTables {
		if _, err := transaction.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("清理 SQLite 恢复表 %s 失败: %w", table, err)
		}
	}
	for _, table := range insertTables {
		if _, err := transaction.Exec("INSERT INTO " + table + " SELECT * FROM migration_restore." + table); err != nil {
			return fmt.Errorf("恢复 SQLite 表 %s 失败: %w", table, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("提交 SQLite 快照恢复事务失败: %w", err)
	}
	if _, err := conn.ExecContext(context.Background(), "DETACH DATABASE migration_restore"); err != nil {
		return nil
	}
	detached = true
	return nil
}

// AuthTokens 保存管理令牌和代理令牌的不可逆摘要。
type AuthTokens struct {
	AdminHash   string `json:"adminHash"`
	ProxyHash   string `json:"proxyHash"`
	AdminCustom bool   `json:"adminCustom,omitempty"`
	ProxyCustom bool   `json:"proxyCustom,omitempty"`
	AdminToken  string `json:"adminToken,omitempty"`
	ProxyToken  string `json:"proxyToken,omitempty"`
}

type persistedAuthTokens struct {
	AdminHash   string `json:"adminHash"`
	ProxyHash   string `json:"proxyHash"`
	AdminCustom bool   `json:"adminCustom,omitempty"`
	ProxyCustom bool   `json:"proxyCustom,omitempty"`
}

// NewAuthTokens 根据明文令牌构造可持久化的鉴权配置。
func NewAuthTokens(adminToken, proxyToken string) (AuthTokens, error) {
	adminToken = strings.TrimSpace(adminToken)
	proxyToken = strings.TrimSpace(proxyToken)
	if adminToken == "" || proxyToken == "" {
		return AuthTokens{}, fmt.Errorf("管理令牌和代理令牌不能为空")
	}
	return AuthTokens{
		AdminHash: hashAuthToken(adminToken), ProxyHash: hashAuthToken(proxyToken),
		AdminToken: adminToken, ProxyToken: proxyToken,
		AdminCustom: adminToken != LocalDefaultAdminToken, ProxyCustom: proxyToken != LocalDefaultProxyToken,
	}, nil
}

// Open 创建数据目录、打开 SQLite 并执行版本化迁移。
func Open(dataDirectory string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDirectory, "oneai-proxy.db"))
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		database.Close()
		return nil, fmt.Errorf("设置 SQLite busy_timeout 失败: %w", err)
	}
	if err := migrate(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func migrate(database *sql.DB) error {
	const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
	if _, err := database.Exec(schema); err != nil {
		return fmt.Errorf("初始化 SQLite 失败: %w", err)
	}
	var current int
	if err := database.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("读取迁移版本失败: %w", err)
	}
	if current < schemaVersion {
		foreignKeysDisabled := current < 22
		if foreignKeysDisabled {
			if _, err := database.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				return fmt.Errorf("准备重建渠道表失败: %w", err)
			}
			defer func() { _, _ = database.Exec("PRAGMA foreign_keys = ON") }()
		}
		transaction, err := database.Begin()
		if err != nil {
			return fmt.Errorf("开始迁移事务失败: %w", err)
		}
		if current < 2 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS channels (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  base_url TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '[]',
  admin_state TEXT NOT NULL DEFAULT 'enabled' CHECK (admin_state IN ('enabled', 'disabled')),
  health_state TEXT NOT NULL DEFAULT 'healthy' CHECK (health_state IN ('healthy', 'degraded', 'cooldown', 'half_open', 'auto_disabled')),
  health_version INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(id, protocol)
);
CREATE TABLE IF NOT EXISTS route_groups (
  id TEXT PRIMARY KEY,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  logical_model TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(id, protocol),
  UNIQUE(protocol, logical_model)
);
CREATE TABLE IF NOT EXISTS route_members (
  route_group_id TEXT NOT NULL,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  channel_id TEXT NOT NULL,
  priority INTEGER NOT NULL CHECK (priority >= 0),
  position INTEGER NOT NULL CHECK (position >= 0),
  PRIMARY KEY(route_group_id, channel_id),
  UNIQUE(route_group_id, position),
  FOREIGN KEY(route_group_id, protocol) REFERENCES route_groups(id, protocol) ON DELETE CASCADE,
  FOREIGN KEY(channel_id, protocol) REFERENCES channels(id, protocol) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS global_model_mappings (
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  client_model TEXT NOT NULL,
  logical_model TEXT NOT NULL,
  PRIMARY KEY(protocol, client_model)
);
CREATE TABLE IF NOT EXISTS channel_model_mappings (
  channel_id TEXT NOT NULL,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  logical_model TEXT NOT NULL,
  upstream_model TEXT NOT NULL,
  PRIMARY KEY(channel_id, protocol, logical_model),
  FOREIGN KEY(channel_id, protocol) REFERENCES channels(id, protocol) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS probe_policies (
  channel_id TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  mode TEXT NOT NULL DEFAULT 'connectivity' CHECK (mode IN ('connectivity', 'minimal_inference')),
  auto_recover INTEGER NOT NULL DEFAULT 0 CHECK (auto_recover IN (0, 1)),
  recovery_success_threshold INTEGER NOT NULL DEFAULT 1 CHECK (recovery_success_threshold > 0),
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE,
  CHECK (auto_recover = 0 OR enabled = 1)
);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("执行阶段 2 迁移失败: %w", err)
			}
		}
		if current < 3 {
			if _, err := transaction.Exec(`
ALTER TABLE channels ADD COLUMN secret_ref TEXT NOT NULL DEFAULT '';
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("执行渠道凭证迁移失败: %w", err)
			}
			if _, err := transaction.Exec(`UPDATE channels SET admin_state = 'disabled' WHERE secret_ref = ''`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("禁用无凭证渠道失败: %w", err)
			}
		}
		if current < 4 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS response_affinity (
  response_id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建 Responses 亲和表失败: %w", err)
			}
		}
		if current < 5 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  client_model TEXT NOT NULL,
  final_status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(id, protocol)
);
CREATE TABLE IF NOT EXISTS attempts (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence > 0),
  status TEXT NOT NULL,
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE,
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE,
  UNIQUE(request_id, sequence)
);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建请求账本表失败: %w", err)
			}
		}
		if current < 6 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS health_runtime (
  channel_id TEXT PRIMARY KEY,
  failure_count INTEGER NOT NULL DEFAULT 0,
  cooldown_level INTEGER NOT NULL DEFAULT 0,
  cooldown_until TEXT NOT NULL DEFAULT '',
  half_open_claimed INTEGER NOT NULL DEFAULT 0 CHECK (half_open_claimed IN (0, 1)),
  last_error_class TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
INSERT OR IGNORE INTO health_runtime(channel_id) SELECT id FROM channels;
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建健康运行态表失败: %w", err)
			}
		}
		if current < 7 {
			if _, err := transaction.Exec(`
ALTER TABLE channels ADD COLUMN failure_action TEXT NOT NULL DEFAULT 'cooldown' CHECK (failure_action IN ('cooldown', 'auto_disable'));
ALTER TABLE channels ADD COLUMN failure_threshold INTEGER NOT NULL DEFAULT 3 CHECK (failure_threshold > 0);
CREATE TABLE IF NOT EXISTS health_events (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL,
  from_state TEXT NOT NULL,
  to_state TEXT NOT NULL,
  reason TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展健康策略和事件表失败: %w", err)
			}
		}
		if current < 8 {
			if _, err := transaction.Exec(`
ALTER TABLE health_runtime ADD COLUMN probe_success_count INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS probe_runs (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  occurred_at TEXT NOT NULL,
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建探针运行记录失败: %w", err)
			}
		}
		if current < 9 {
			if _, err := transaction.Exec(`
ALTER TABLE requests ADD COLUMN logical_model TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN input_tokens INTEGER;
ALTER TABLE requests ADD COLUMN output_tokens INTEGER;
ALTER TABLE requests ADD COLUMN cache_read_input_tokens INTEGER;
ALTER TABLE requests ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE attempts ADD COLUMN logical_model TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN input_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN output_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN cache_read_input_tokens INTEGER;
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展请求账本字段失败: %w", err)
			}
		}
		if current < 10 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS audit_logs (
  id TEXT PRIMARY KEY,
  actor_type TEXT NOT NULL,
  action TEXT NOT NULL,
  target_id TEXT NOT NULL DEFAULT '',
  details_json TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_logs_occurred_at ON audit_logs(occurred_at);
CREATE TABLE IF NOT EXISTS hourly_metrics (
  bucket_start_utc TEXT NOT NULL,
  dimension_type TEXT NOT NULL,
  dimension_id TEXT NOT NULL DEFAULT '',
  request_count INTEGER NOT NULL DEFAULT 0,
	  request_success_count INTEGER NOT NULL DEFAULT 0,
	  request_failure_count INTEGER NOT NULL DEFAULT 0,
	  fallback_request_count INTEGER NOT NULL DEFAULT 0,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  attempt_success_count INTEGER NOT NULL DEFAULT 0,
  probe_count INTEGER NOT NULL DEFAULT 0,
  probe_success_count INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cache_read_input_tokens INTEGER,
  request_latency_total_ms INTEGER NOT NULL DEFAULT 0,
  request_latency_count INTEGER NOT NULL DEFAULT 0,
  attempt_latency_total_ms INTEGER NOT NULL DEFAULT 0,
  attempt_latency_count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(bucket_start_utc, dimension_type, dimension_id)
);
CREATE INDEX IF NOT EXISTS idx_hourly_metrics_bucket ON hourly_metrics(bucket_start_utc);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建审计和小时聚合表失败: %w", err)
			}
		}
		if current < 11 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS content_blobs (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL,
  key_ref TEXT NOT NULL,
  nonce BLOB NOT NULL,
  sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
  created_at TEXT NOT NULL,
  FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_content_blobs_request ON content_blobs(request_id);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建加密正文元数据表失败: %w", err)
			}
		}
		if current < 12 {
			if _, err := transaction.Exec(`ALTER TABLE content_blobs ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE content_blobs ADD COLUMN content_type TEXT NOT NULL DEFAULT 'request';`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展正文文件元数据失败: %w", err)
			}
		}
		if current < 13 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN custom_headers_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE channels ADD COLUMN concurrency_limit INTEGER NOT NULL DEFAULT 8;
ALTER TABLE channels ADD COLUMN request_timeout_ms INTEGER NOT NULL DEFAULT 120000;
ALTER TABLE channels ADD COLUMN stream_idle_timeout_ms INTEGER NOT NULL DEFAULT 300000;
ALTER TABLE channels ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 2;
ALTER TABLE channels ADD COLUMN cooldown_seconds INTEGER NOT NULL DEFAULT 30;`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道运行参数失败: %w", err)
			}
		}
		if current < 14 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE probe_policies ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 60;
ALTER TABLE probe_policies ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE probe_policies ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE probe_policies ADD COLUMN failure_threshold INTEGER NOT NULL DEFAULT 3;
ALTER TABLE probe_policies ADD COLUMN request_timeout_ms INTEGER NOT NULL DEFAULT 15000;`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道备注和探针参数失败: %w", err)
			}
		}
		if current < 15 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN priority INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 0);
ALTER TABLE channels ADD COLUMN fallback_model TEXT NOT NULL DEFAULT '';`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道优先级和兜底模型失败: %w", err)
			}
		}
		if current < 16 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN group_name TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS channel_models (
  channel_id TEXT NOT NULL,
  model TEXT NOT NULL,
  PRIMARY KEY(channel_id, model),
  FOREIGN KEY(channel_id) REFERENCES channels(id) ON DELETE CASCADE
);
INSERT OR IGNORE INTO channel_models(channel_id, model)
SELECT channel_id, upstream_model FROM channel_model_mappings WHERE TRIM(upstream_model) <> '';`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道分组和模型目录失败: %w", err)
			}
		}
		if current < 17 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN service_tier_passthrough INTEGER NOT NULL DEFAULT 1 CHECK (service_tier_passthrough IN (0, 1));`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道 service_tier 透传配置失败: %w", err)
			}
		}
		if current < 18 {
			if err := validateGlobalMappingFamilyMigration(transaction); err != nil {
				_ = transaction.Rollback()
				return err
			}
			if _, err := transaction.Exec(`
ALTER TABLE global_model_mappings RENAME TO global_model_mappings_v17;
CREATE TABLE global_model_mappings (
  protocol_family TEXT NOT NULL CHECK (protocol_family IN ('openai', 'anthropic')),
  client_model TEXT NOT NULL,
  logical_model TEXT NOT NULL,
  PRIMARY KEY(protocol_family, client_model)
);
INSERT OR IGNORE INTO global_model_mappings(protocol_family, client_model, logical_model)
SELECT CASE WHEN protocol = 'anthropic_messages' THEN 'anthropic' ELSE 'openai' END, client_model, logical_model
FROM global_model_mappings_v17
ORDER BY CASE protocol WHEN 'openai_chat' THEN 0 WHEN 'openai_responses' THEN 1 ELSE 2 END;
DROP TABLE global_model_mappings_v17;
CREATE TABLE model_catalog (
  stable_key TEXT PRIMARY KEY,
  base_model_id TEXT NOT NULL DEFAULT '',
  model_id TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  vendor TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages', 'protocol_unconfirmed')),
  model_type TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  modalities_json TEXT NOT NULL DEFAULT '{"input":[],"output":[]}',
  capabilities_json TEXT NOT NULL DEFAULT '[]',
  context_window INTEGER NOT NULL DEFAULT 0 CHECK (context_window >= 0),
  max_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_input_tokens >= 0),
  max_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_output_tokens >= 0),
  pricing_json TEXT NOT NULL DEFAULT '{}',
  currency TEXT NOT NULL DEFAULT '',
  billing_unit TEXT NOT NULL DEFAULT '',
  source_type TEXT NOT NULL CHECK (source_type IN ('online', 'manual')),
  source_url TEXT NOT NULL DEFAULT '',
  source_adapter TEXT NOT NULL DEFAULT '',
  source_version TEXT NOT NULL DEFAULT '',
  source_status TEXT NOT NULL CHECK (source_status IN ('valid', 'retired', 'protocol_unconfirmed')),
  synced_at TEXT NOT NULL DEFAULT '',
  published_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_model_catalog_protocol ON model_catalog(protocol);
CREATE INDEX idx_model_catalog_source_status ON model_catalog(source_status);
CREATE INDEX idx_model_catalog_source ON model_catalog(source_url, source_adapter);
CREATE INDEX idx_model_catalog_base_model ON model_catalog(base_model_id);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("创建模型目录和协议族映射失败: %w", err)
			}
		}
		if current < 19 {
			if _, err := transaction.Exec(`
ALTER TABLE requests ADD COLUMN group_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN initial_channel_id TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN initial_channel_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN final_channel_id TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN final_channel_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN fallback_triggered INTEGER NOT NULL DEFAULT 0 CHECK (fallback_triggered IN (0, 1));
ALTER TABLE requests ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN cache_write_input_tokens INTEGER;
ALTER TABLE requests ADD COLUMN reasoning_tokens INTEGER;
ALTER TABLE requests ADD COLUMN total_tokens INTEGER;
ALTER TABLE requests ADD COLUMN cache_rate REAL;
ALTER TABLE requests ADD COLUMN ttft_ms INTEGER;
ALTER TABLE requests ADD COLUMN tps REAL;
ALTER TABLE requests ADD COLUMN stream_ended INTEGER CHECK (stream_ended IS NULL OR stream_ended IN (0, 1));
ALTER TABLE requests ADD COLUMN final_http_status INTEGER;
ALTER TABLE requests ADD COLUMN error_class TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN error_message TEXT NOT NULL DEFAULT '';

ALTER TABLE attempts ADD COLUMN channel_name TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN group_name TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN protocol TEXT NOT NULL DEFAULT 'openai_chat';
ALTER TABLE attempts ADD COLUMN client_model TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN upstream_model TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN started_at TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN first_byte_at TEXT;
ALTER TABLE attempts ADD COLUMN first_event_at TEXT;
ALTER TABLE attempts ADD COLUMN completed_at TEXT;
ALTER TABLE attempts ADD COLUMN http_status INTEGER;
ALTER TABLE attempts ADD COLUMN retry_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN fallback_triggered INTEGER NOT NULL DEFAULT 0 CHECK (fallback_triggered IN (0, 1));
ALTER TABLE attempts ADD COLUMN cache_write_input_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN reasoning_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN total_tokens INTEGER;
ALTER TABLE attempts ADD COLUMN request_content_blob_id TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN response_content_blob_id TEXT NOT NULL DEFAULT '';
ALTER TABLE attempts ADD COLUMN stream_ended INTEGER CHECK (stream_ended IS NULL OR stream_ended IN (0, 1));

ALTER TABLE content_blobs ADD COLUMN attempt_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_requests_started_at ON requests(started_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(final_status, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_requests_channels ON requests(initial_channel_id, final_channel_id);
CREATE INDEX IF NOT EXISTS idx_attempts_request_sequence ON attempts(request_id, sequence);
CREATE INDEX IF NOT EXISTS idx_attempts_channel ON attempts(channel_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_content_blobs_attempt ON content_blobs(attempt_id, created_at);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展阶段 6R 请求日志字段失败: %w", err)
			}
			if _, err := transaction.Exec(`
UPDATE attempts
SET protocol = (SELECT r.protocol FROM requests r WHERE r.id = attempts.request_id),
    client_model = (SELECT r.client_model FROM requests r WHERE r.id = attempts.request_id),
    logical_model = COALESCE(NULLIF(logical_model, ''), (SELECT r.logical_model FROM requests r WHERE r.id = attempts.request_id)),
    group_name = COALESCE(NULLIF(group_name, ''), (SELECT r.group_name FROM requests r WHERE r.id = attempts.request_id)),
    started_at = COALESCE(NULLIF(started_at, ''), (SELECT r.started_at FROM requests r WHERE r.id = attempts.request_id))
WHERE EXISTS (SELECT 1 FROM requests r WHERE r.id = attempts.request_id);
UPDATE attempts
SET channel_name = COALESCE(NULLIF(channel_name, ''), (SELECT c.name FROM channels c WHERE c.id = attempts.channel_id))
WHERE TRIM(channel_name) = '';
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("回填阶段 6R 历史 Attempt 失败: %w", err)
			}
		}
		if current < 20 {
			if _, err := transaction.Exec(`
ALTER TABLE content_blobs ADD COLUMN original_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE content_blobs ADD COLUMN saved_size INTEGER NOT NULL DEFAULT 0;
UPDATE content_blobs SET original_size = size, saved_size = size WHERE original_size = 0 AND saved_size = 0;
ALTER TABLE hourly_metrics ADD COLUMN cache_write_input_tokens INTEGER;
ALTER TABLE hourly_metrics ADD COLUMN reasoning_tokens INTEGER;
ALTER TABLE hourly_metrics ADD COLUMN total_tokens INTEGER;
ALTER TABLE hourly_metrics ADD COLUMN ttft_total_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hourly_metrics ADD COLUMN ttft_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hourly_metrics ADD COLUMN tps_total REAL NOT NULL DEFAULT 0;
ALTER TABLE hourly_metrics ADD COLUMN tps_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hourly_metrics ADD COLUMN request_latency_histogram_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE hourly_metrics ADD COLUMN attempt_latency_histogram_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE hourly_metrics ADD COLUMN ttft_histogram_json TEXT NOT NULL DEFAULT '[]';`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展请求正文和小时指标字段失败: %w", err)
			}
		}
		if current < 21 {
			if _, err := transaction.Exec(`
ALTER TABLE hourly_metrics ADD COLUMN fallback_eligible_request_count INTEGER NOT NULL DEFAULT 0;
UPDATE hourly_metrics SET fallback_eligible_request_count = request_success_count + request_failure_count WHERE fallback_eligible_request_count = 0;`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展 fallback 统计分母字段失败: %w", err)
			}
		}
		if current < 22 {
			if _, err := transaction.Exec(`
CREATE TABLE channels_v22 (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  protocol TEXT NOT NULL CHECK (protocol IN ('openai_chat', 'openai_responses', 'anthropic_messages')),
  base_url TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '[]',
  admin_state TEXT NOT NULL DEFAULT 'enabled' CHECK (admin_state IN ('enabled', 'disabled')),
  health_state TEXT NOT NULL DEFAULT 'healthy' CHECK (health_state IN ('healthy', 'degraded', 'cooldown', 'half_open', 'auto_disabled')),
  health_version INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  secret_ref TEXT NOT NULL DEFAULT '',
  failure_action TEXT NOT NULL DEFAULT 'cooldown' CHECK (failure_action IN ('cooldown', 'auto_disable')),
  failure_threshold INTEGER NOT NULL DEFAULT 3 CHECK (failure_threshold > 0),
  custom_headers_json TEXT NOT NULL DEFAULT '{}',
  concurrency_limit INTEGER NOT NULL DEFAULT 8,
  request_timeout_ms INTEGER NOT NULL DEFAULT 120000,
  stream_idle_timeout_ms INTEGER NOT NULL DEFAULT 300000,
  cooldown_seconds INTEGER NOT NULL DEFAULT 30,
  note TEXT NOT NULL DEFAULT '',
  priority INTEGER NOT NULL DEFAULT 100 CHECK (priority >= 0),
  fallback_model TEXT NOT NULL DEFAULT '',
  group_name TEXT NOT NULL DEFAULT '',
  service_tier_passthrough INTEGER NOT NULL DEFAULT 1 CHECK (service_tier_passthrough IN (0, 1)),
  UNIQUE(id, protocol)
);
INSERT INTO channels_v22(
  id, name, protocol, base_url, capabilities_json, admin_state, health_state, health_version,
  created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json,
  concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, note, priority,
  fallback_model, group_name, service_tier_passthrough
)
SELECT
  id, name, protocol, base_url, capabilities_json, admin_state, health_state, health_version,
  created_at, updated_at, secret_ref, failure_action, failure_threshold, custom_headers_json,
  concurrency_limit, request_timeout_ms, stream_idle_timeout_ms, cooldown_seconds, note, priority,
  fallback_model, group_name, service_tier_passthrough
FROM channels;
DROP TABLE channels;
ALTER TABLE channels_v22 RENAME TO channels;
ALTER TABLE health_runtime ADD COLUMN probe_failure_count INTEGER NOT NULL DEFAULT 0;
CREATE TABLE request_route_events (
  request_id TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence > 0),
  event_type TEXT NOT NULL CHECK (event_type IN ('candidate_skipped', 'channel_switched', 'health_changed', 'routing_exhausted')),
  channel_id TEXT NOT NULL DEFAULT '',
  related_channel_id TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  details_json TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(request_id, sequence),
  FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE
);
CREATE INDEX idx_request_route_events_request ON request_route_events(request_id, sequence);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("替换阶段 12 路由配置和事件表失败: %w", err)
			}
			rows, err := transaction.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("检查阶段 12 外键完整性失败: %w", err)
			}
			if rows.Next() {
				_ = rows.Close()
				_ = transaction.Rollback()
				return fmt.Errorf("阶段 12 渠道表重建后存在外键损坏")
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				_ = transaction.Rollback()
				return fmt.Errorf("读取阶段 12 外键检查失败: %w", err)
			}
			if err := rows.Close(); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("完成阶段 12 外键检查失败: %w", err)
			}
		}
		if current < 23 {
			if _, err := transaction.Exec(`ALTER TABLE channels ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT 'passthrough';`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展渠道思考等级配置失败: %w", err)
			}
		}
		if current < 24 {
			if _, err := transaction.Exec(`
CREATE INDEX IF NOT EXISTS idx_probe_runs_channel_occurred ON probe_runs(channel_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_probe_runs_occurred ON probe_runs(occurred_at DESC, id DESC);
ALTER TABLE health_runtime ADD COLUMN last_probe_id TEXT NOT NULL DEFAULT '';
ALTER TABLE health_runtime ADD COLUMN last_probe_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE health_runtime ADD COLUMN last_probe_status TEXT NOT NULL DEFAULT '';
ALTER TABLE health_runtime ADD COLUMN last_probe_latency_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_runtime ADD COLUMN last_probe_error_class TEXT NOT NULL DEFAULT '';
ALTER TABLE health_runtime ADD COLUMN last_probe_error_message TEXT NOT NULL DEFAULT '';
ALTER TABLE health_runtime ADD COLUMN last_probe_occurred_at TEXT NOT NULL DEFAULT '';
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展最近探针快照和探针索引失败: %w", err)
			}
			if err := backfillLatestProbeSnapshots(transaction); err != nil {
				_ = transaction.Rollback()
				return err
			}
		}
		if current < 25 {
			if _, err := transaction.Exec(`
CREATE TABLE IF NOT EXISTS hourly_metric_dirty (
  bucket_start_utc TEXT PRIMARY KEY,
  revision INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE content_blobs ADD COLUMN redact_headers TEXT NOT NULL DEFAULT '';
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展小时聚合脏标记和正文遮罩字段失败: %w", err)
			}
		}
		if current < 26 {
			if _, err := transaction.Exec(`DROP TABLE IF EXISTS route_members; DROP TABLE IF EXISTS route_groups;`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("删除未使用的路由组表失败: %w", err)
			}
		}
		if current < 27 {
			if _, err := transaction.Exec(`
CREATE TABLE request_route_events_v27 (
  request_id TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence > 0),
  event_type TEXT NOT NULL CHECK (event_type IN ('candidate_skipped', 'channel_switched', 'health_changed', 'routing_exhausted', 'custom_headers_ignored')),
  channel_id TEXT NOT NULL DEFAULT '',
  related_channel_id TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  details_json TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(request_id, sequence),
  FOREIGN KEY(request_id) REFERENCES requests(id) ON DELETE CASCADE
);
INSERT INTO request_route_events_v27(request_id, sequence, event_type, channel_id, related_channel_id, reason, details_json, occurred_at)
SELECT request_id, sequence, event_type, channel_id, related_channel_id, reason, details_json, occurred_at
FROM request_route_events;
DROP TABLE request_route_events;
ALTER TABLE request_route_events_v27 RENAME TO request_route_events;
CREATE INDEX idx_request_route_events_request ON request_route_events(request_id, sequence);
`); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("扩展自定义请求头路由事件类型失败: %w", err)
			}
		}
		if _, err := transaction.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (?, datetime('now'))", schemaVersion); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("记录迁移版本失败: %w", err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("提交迁移事务失败: %w", err)
		}
	}
	// 运行日志表需要兼容已处于 v19 的旧数据目录。
	if _, err := database.Exec(`
CREATE TABLE IF NOT EXISTS runtime_logs (
  id TEXT PRIMARY KEY,
  occurred_at TEXT NOT NULL,
  level TEXT NOT NULL,
  event TEXT NOT NULL,
  message TEXT NOT NULL,
  request_id TEXT NOT NULL DEFAULT '',
  attempt_id TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '',
  context_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_occurred_at ON runtime_logs(occurred_at DESC, id DESC);
`); err != nil {
		return fmt.Errorf("初始化运行日志表失败: %w", err)
	}
	for _, statement := range []string{
		`ALTER TABLE audit_logs ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE audit_logs ADD COLUMN result TEXT NOT NULL DEFAULT 'success'`,
		`ALTER TABLE audit_logs ADD COLUMN operator_ip TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := database.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("扩展审计字段失败: %w", err)
		}
	}
	if _, err := database.Exec(`UPDATE audit_logs SET category = CASE
		WHEN action LIKE 'channels.%' THEN 'channels'
		WHEN action LIKE 'models.%' THEN 'models'
		WHEN action LIKE 'probes.%' THEN 'probes'
		WHEN action LIKE 'auth.%' THEN 'auth'
		WHEN action LIKE 'config.%' THEN 'config'
		WHEN action LIKE 'logs.%' THEN 'logs'
		ELSE category END WHERE TRIM(category) = ''`); err != nil {
		return fmt.Errorf("回填审计分类失败: %w", err)
	}
	return nil
}

// validateGlobalMappingFamilyMigration 确保旧版 OpenAI 映射合并到协议族时不会静默丢失冲突值。
func validateGlobalMappingFamilyMigration(transaction *sql.Tx) error {
	var clientModel string
	err := transaction.QueryRow(`
SELECT client_model
FROM global_model_mappings
WHERE protocol IN ('openai_chat', 'openai_responses')
GROUP BY client_model
HAVING COUNT(DISTINCT logical_model) > 1
LIMIT 1`).Scan(&clientModel)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("校验 OpenAI 全局映射迁移失败: %w", err)
	}
	return fmt.Errorf("OpenAI 全局映射存在冲突，无法安全升级: %s", clientModel)
}

// LoadRuntimeSettings 读取持久化监听配置，没有记录时写入默认值。
func LoadRuntimeSettings(database *sql.DB, fallback config.Settings) (config.Settings, error) {
	var value string
	err := database.QueryRow("SELECT value_json FROM app_settings WHERE key = 'runtime.listeners'").Scan(&value)
	if err == sql.ErrNoRows {
		if err := SaveRuntimeSettings(database, fallback); err != nil {
			return config.Settings{}, err
		}
		return fallback, nil
	}
	if err != nil {
		return config.Settings{}, fmt.Errorf("读取监听配置失败: %w", err)
	}
	var persisted config.Settings
	if err := json.Unmarshal([]byte(value), &persisted); err != nil {
		return config.Settings{}, fmt.Errorf("解析监听配置失败: %w", err)
	}
	validated, err := config.New(persisted.ProxyListen, persisted.AdminListen, fallback.DataDirectory)
	if err != nil {
		return config.Settings{}, fmt.Errorf("监听配置无效: %w", err)
	}
	// 兼容阶段 1 只保存监听地址的旧记录，同时保留已持久化的完整运行策略。
	if persisted.RequestPolicy != (config.RequestPolicy{}) {
		validated.RequestPolicy = persisted.RequestPolicy
	}
	if persisted.Logging != (config.LoggingSettings{}) {
		validated.Logging = persisted.Logging
		validated.Logging.ContentPolicy = config.RequestLogContentPolicy
		if validated.Logging.RequestRetentionDays <= 0 {
			validated.Logging.RequestRetentionDays = validated.Logging.RetentionDays
		}
		if validated.Logging.RequestRetentionDays <= 0 {
			validated.Logging.RequestRetentionDays = 30
		}
		if validated.Logging.AuditRetentionDays <= 0 {
			validated.Logging.AuditRetentionDays = 30
		}
		if validated.Logging.RuntimeRetentionDays <= 0 {
			validated.Logging.RuntimeRetentionDays = 7
		}
		if validated.Logging.RetentionDays <= 0 {
			validated.Logging.RetentionDays = validated.Logging.RequestRetentionDays
		}
		if validated.Logging.MaxRequestContentBytes <= 0 {
			validated.Logging.MaxRequestContentBytes = validated.Logging.MaxContentBytes
		}
		if validated.Logging.MaxRequestContentBytes <= 0 {
			validated.Logging.MaxRequestContentBytes = 1 << 20
		}
		if validated.Logging.MaxResponseContentBytes <= 0 {
			validated.Logging.MaxResponseContentBytes = 1 << 20
		}
		if validated.Logging.MaxContentBytes <= 0 {
			validated.Logging.MaxContentBytes = validated.Logging.MaxRequestContentBytes
		}
		if validated.Logging.DiskQuotaBytes <= 0 {
			validated.Logging.DiskQuotaBytes = 100 << 20
		}
		if validated.Logging.RuntimeLogMaxBytes <= 0 {
			validated.Logging.RuntimeLogMaxBytes = 100 << 20
		}
	}
	if persisted.Timezone != "" {
		validated.Timezone = persisted.Timezone
	}
	validated.ChannelSettings = config.NormalizeChannelSettings(persisted.ChannelSettings)
	if persisted.ChannelSettings.ReasoningEffort == "" {
		validated.ChannelSettings = config.NormalizeChannelSettings(fallback.ChannelSettings)
		if !validated.ChannelSettings.ServiceTierPassthrough {
			validated.ChannelSettings.ServiceTierPassthrough = true
		}
	}
	if !config.IsValidReasoningEffort(validated.ChannelSettings.ReasoningEffort) {
		return config.Settings{}, fmt.Errorf("全局思考等级无效: %s", validated.ChannelSettings.ReasoningEffort)
	}
	if err := SaveRuntimeSettings(database, validated); err != nil {
		return config.Settings{}, err
	}
	return validated, nil
}

// SaveRuntimeSettings 保存下次启动时使用的监听配置。
func SaveRuntimeSettings(database *sql.DB, settings config.Settings) error {
	value, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("序列化监听配置失败: %w", err)
	}
	_, err = database.Exec(`
INSERT INTO app_settings(key, value_json, updated_at)
VALUES ('runtime.listeners', ?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		string(value), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("保存监听配置失败: %w", err)
	}
	return nil
}

// LoadModelCatalogSourceURL 读取最近一次使用的模型目录在线地址。
func LoadModelCatalogSourceURL(database *sql.DB) (string, error) {
	var value string
	err := database.QueryRow("SELECT value_json FROM app_settings WHERE key = 'model-catalog.source-url'").Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取模型目录来源地址失败: %w", err)
	}
	var sourceURL string
	if err := json.Unmarshal([]byte(value), &sourceURL); err != nil {
		return "", fmt.Errorf("解析模型目录来源地址失败: %w", err)
	}
	return sourceURL, nil
}

// SaveModelCatalogSourceURL 保存最近一次使用的模型目录在线地址。
func SaveModelCatalogSourceURL(database *sql.DB, sourceURL string) error {
	value, err := json.Marshal(strings.TrimSpace(sourceURL))
	if err != nil {
		return fmt.Errorf("序列化模型目录来源地址失败: %w", err)
	}
	_, err = database.Exec(`
INSERT INTO app_settings(key, value_json, updated_at)
VALUES ('model-catalog.source-url', ?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		string(value), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("保存模型目录来源地址失败: %w", err)
	}
	return nil
}

// LoadOrCreateAuthTokens 读取令牌哈希；首次运行时写入默认令牌，并把明文放到秘密存储。
func LoadOrCreateAuthTokens(database *sql.DB, store secret.Store) (AuthTokens, error) {
	var value string
	err := database.QueryRow("SELECT value_json FROM app_settings WHERE key = 'auth.tokens'").Scan(&value)
	if err == nil {
		var tokens AuthTokens
		if err := json.Unmarshal([]byte(value), &tokens); err != nil {
			return AuthTokens{}, fmt.Errorf("解析鉴权令牌失败: %w", err)
		}
		if tokens.AdminHash == "" || tokens.ProxyHash == "" {
			return AuthTokens{}, fmt.Errorf("鉴权令牌配置不完整，请删除旧数据目录后重新启动")
		}
		if tokens.AdminToken != "" || tokens.ProxyToken != "" {
			filled, fillErr := fillAuthTokenSecrets(store, tokens)
			if fillErr != nil || strings.TrimSpace(filled.AdminToken) == "" || strings.TrimSpace(filled.ProxyToken) == "" {
				return AuthTokens{}, fmt.Errorf("鉴权令牌配置不完整，请删除旧数据目录后重新启动")
			}
			tokens = filled
			if err := PersistAuthTokenSecrets(store, tokens); err != nil {
				return AuthTokens{}, err
			}
			if err := persistAuthRecord(database, tokens); err != nil {
				return AuthTokens{}, err
			}
			return tokens, nil
		}
		filled, _ := fillAuthTokenSecrets(store, tokens)
		return filled, nil
	}
	if err != sql.ErrNoRows {
		return AuthTokens{}, fmt.Errorf("读取鉴权令牌失败: %w", err)
	}
	tokens := AuthTokens{AdminHash: hashAuthToken(LocalDefaultAdminToken), ProxyHash: hashAuthToken(LocalDefaultProxyToken), AdminToken: LocalDefaultAdminToken, ProxyToken: LocalDefaultProxyToken}
	if err := PersistAuthTokenSecrets(store, tokens); err != nil {
		return AuthTokens{}, err
	}
	if err := persistAuthRecord(database, tokens); err != nil {
		return AuthTokens{}, err
	}
	return tokens, nil
}

// LoadExportAuthTokens 读取完整导出所需的令牌明文；秘密存储失败或任一令牌为空时返回错误。
func LoadExportAuthTokens(database *sql.DB, store secret.Store) (AuthTokens, error) {
	tokens, err := LoadOrCreateAuthTokens(database, store)
	if err != nil {
		return AuthTokens{}, err
	}
	tokens, err = fillAuthTokenSecrets(store, tokens)
	if err != nil {
		return AuthTokens{}, fmt.Errorf("读取导出令牌失败: %w", err)
	}
	if strings.TrimSpace(tokens.AdminToken) == "" || strings.TrimSpace(tokens.ProxyToken) == "" {
		return AuthTokens{}, fmt.Errorf("完整导出缺少管理令牌或代理令牌明文")
	}
	return tokens, nil
}

// SetAuthToken 保存用户指定的管理或代理令牌，并返回新的令牌摘要。
func SetAuthToken(database *sql.DB, store secret.Store, kind, token string) (AuthTokens, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AuthTokens{}, fmt.Errorf("令牌不能为空")
	}
	tokens, err := LoadOrCreateAuthTokens(database, store)
	if err != nil {
		return AuthTokens{}, err
	}
	switch kind {
	case "admin":
		tokens.AdminHash = hashAuthToken(token)
		tokens.AdminCustom = true
		tokens.AdminToken = token
	case "proxy":
		tokens.ProxyHash = hashAuthToken(token)
		tokens.ProxyCustom = true
		tokens.ProxyToken = token
	default:
		return AuthTokens{}, fmt.Errorf("令牌类型无效: %s", kind)
	}
	if err := PersistAuthTokenSecrets(store, tokens); err != nil {
		return AuthTokens{}, err
	}
	if err := persistAuthRecord(database, tokens); err != nil {
		return AuthTokens{}, err
	}
	return tokens, nil
}

// RotateAuthToken 轮换指定类型的令牌，并返回新的明文令牌。
func RotateAuthToken(database *sql.DB, store secret.Store, kind string) (string, AuthTokens, error) {
	tokens, err := LoadOrCreateAuthTokens(database, store)
	if err != nil {
		return "", AuthTokens{}, err
	}
	token, err := newAuthToken()
	if err != nil {
		return "", AuthTokens{}, err
	}
	switch kind {
	case "admin":
		tokens.AdminHash = hashAuthToken(token)
		tokens.AdminCustom = true
		tokens.AdminToken = token
	case "proxy":
		tokens.ProxyHash = hashAuthToken(token)
		tokens.ProxyCustom = true
		tokens.ProxyToken = token
	default:
		return "", AuthTokens{}, fmt.Errorf("令牌类型无效: %s", kind)
	}
	if err := PersistAuthTokenSecrets(store, tokens); err != nil {
		return "", AuthTokens{}, err
	}
	if err := persistAuthRecord(database, tokens); err != nil {
		return "", AuthTokens{}, err
	}
	return token, tokens, nil
}

// AuthenticateAuthToken 校验令牌明文是否匹配指定令牌类型。
func AuthenticateAuthToken(tokens AuthTokens, kind, token string) bool {
	hash := hashAuthToken(token)
	switch kind {
	case "admin":
		return equalAuthHash(tokens.AdminHash, hash)
	case "proxy":
		return equalAuthHash(tokens.ProxyHash, hash)
	default:
		return false
	}
}

func persistAuthRecord(database *sql.DB, tokens AuthTokens) error {
	record := persistedAuthTokens{AdminHash: tokens.AdminHash, ProxyHash: tokens.ProxyHash, AdminCustom: tokens.AdminCustom, ProxyCustom: tokens.ProxyCustom}
	if record.AdminHash == "" || record.ProxyHash == "" {
		return fmt.Errorf("鉴权令牌配置不完整")
	}
	value, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化鉴权令牌失败: %w", err)
	}
	_, err = database.Exec(`
INSERT INTO app_settings(key, value_json, updated_at)
VALUES ('auth.tokens', ?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
		string(value), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("保存鉴权令牌失败: %w", err)
	}
	return nil
}

// PersistAuthTokenSecrets 把管理令牌和代理令牌明文写入与渠道凭证相同的秘密存储。
func PersistAuthTokenSecrets(store secret.Store, tokens AuthTokens) error {
	if store == nil {
		return fmt.Errorf("秘密存储不可用")
	}
	if strings.TrimSpace(tokens.AdminToken) == "" || strings.TrimSpace(tokens.ProxyToken) == "" {
		return fmt.Errorf("鉴权令牌配置不完整")
	}
	if err := store.Put(adminTokenSecretRef, []byte(tokens.AdminToken)); err != nil {
		return fmt.Errorf("保存管理令牌失败: %w", err)
	}
	if err := store.Put(proxyTokenSecretRef, []byte(tokens.ProxyToken)); err != nil {
		return fmt.Errorf("保存代理令牌失败: %w", err)
	}
	return nil
}

func fillAuthTokenSecrets(store secret.Store, tokens AuthTokens) (AuthTokens, error) {
	if store == nil {
		return tokens, fmt.Errorf("秘密存储不可用")
	}
	if tokens.AdminToken == "" {
		value, err := store.Get(adminTokenSecretRef)
		if err != nil {
			return tokens, fmt.Errorf("读取管理令牌失败: %w", err)
		}
		tokens.AdminToken = string(value)
	}
	if tokens.ProxyToken == "" {
		value, err := store.Get(proxyTokenSecretRef)
		if err != nil {
			return tokens, fmt.Errorf("读取代理令牌失败: %w", err)
		}
		tokens.ProxyToken = string(value)
	}
	if strings.TrimSpace(tokens.AdminToken) == "" || strings.TrimSpace(tokens.ProxyToken) == "" {
		return tokens, fmt.Errorf("鉴权令牌配置不完整")
	}
	return tokens, nil
}
