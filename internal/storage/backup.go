package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// BackupManifest 描述 SQLite 快照和正文密文清单，不包含任何秘密值。
type BackupManifest struct {
	Format         string       `json:"format"`
	SchemaVersion  int          `json:"schemaVersion"`
	CreatedAt      string       `json:"createdAt"`
	Database       string       `json:"database"`
	DatabaseSize   int64        `json:"databaseSize"`
	DatabaseSHA256 string       `json:"databaseSha256"`
	ContentBlobs   []BackupBlob `json:"contentBlobs"`
}

// BackupBlob 描述备份中的正文密文文件及其哈希。
type BackupBlob struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CreateCompleteBackup 创建数据库快照和正文密文清单，返回清单路径。
func CreateCompleteBackup(database *sql.DB, dataDirectory string) (string, error) {
	databasePath, err := BackupDatabase(database, dataDirectory)
	if err != nil {
		return "", err
	}
	backupDir := filepath.Dir(databasePath)
	contentBackupDir := filepath.Join(backupDir, strings.TrimSuffix(filepath.Base(databasePath), ".db")+"-content")
	if err := os.MkdirAll(contentBackupDir, 0o700); err != nil {
		return "", fmt.Errorf("创建正文备份目录失败: %w", err)
	}
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		return "", fmt.Errorf("读取 SQLite 备份信息失败: %w", err)
	}
	databaseDigest, err := fileSHA256(databasePath)
	if err != nil {
		return "", fmt.Errorf("计算 SQLite 备份哈希失败: %w", err)
	}
	manifest := BackupManifest{Format: "oneai-proxy-backup", SchemaVersion: schemaVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Database: databasePath, DatabaseSize: databaseInfo.Size(), DatabaseSHA256: hex.EncodeToString(databaseDigest[:]), ContentBlobs: make([]BackupBlob, 0)}
	// 将正文密文复制到备份目录，避免恢复依赖当前运行目录中的文件。
	rows, err := database.Query(`SELECT id, path FROM content_blobs ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return "", fmt.Errorf("读取正文备份清单失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var blob BackupBlob
		if err := rows.Scan(&blob.ID, &blob.Path); err != nil {
			return "", err
		}
		info, err := os.Stat(blob.Path)
		if err != nil {
			return "", fmt.Errorf("正文备份文件不存在: %s: %w", blob.ID, err)
		}
		backupPath := filepath.Join(contentBackupDir, backupBlobName(blob.ID))
		if err := copyFile(blob.Path, backupPath); err != nil {
			return "", fmt.Errorf("复制正文备份失败: %w", err)
		}
		blob.Path = backupPath
		blob.Size = info.Size()
		digest, err := fileSHA256(backupPath)
		if err != nil {
			return "", err
		}
		blob.SHA256 = hex.EncodeToString(digest[:])
		manifest.ContentBlobs = append(manifest.ContentBlobs, blob)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	manifestPath := filepath.Join(backupDir, fmt.Sprintf("manifest-%d.json", time.Now().UTC().UnixNano()))
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		return "", fmt.Errorf("写入备份清单失败: %w", err)
	}
	return manifestPath, nil
}

// VerifyBackupManifest 校验备份清单、SQLite 快照和正文密文哈希，不修改当前数据。
func VerifyBackupManifest(manifestPath, dataDirectory string) (BackupManifest, error) {
	backupDir := filepath.Join(dataDirectory, "backups")
	rel, err := filepath.Rel(backupDir, manifestPath)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !pathWithinResolved(backupDir, manifestPath) {
		return BackupManifest{}, fmt.Errorf("备份清单路径不在数据目录内")
	}
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("读取备份清单失败: %w", err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil || manifest.Format != "oneai-proxy-backup" || manifest.Database == "" {
		return BackupManifest{}, fmt.Errorf("备份清单格式无效")
	}
	if !pathWithin(backupDir, manifest.Database) || !pathWithinResolved(backupDir, manifest.Database) {
		return BackupManifest{}, fmt.Errorf("SQLite 备份路径不在备份目录内")
	}
	databaseInfo, err := os.Stat(manifest.Database)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("SQLite 备份不存在: %w", err)
	}
	if manifest.DatabaseSize <= 0 || databaseInfo.Size() != manifest.DatabaseSize {
		return BackupManifest{}, fmt.Errorf("SQLite 备份大小不匹配")
	}
	databaseDigest, err := fileSHA256(manifest.Database)
	if err != nil || hex.EncodeToString(databaseDigest[:]) != manifest.DatabaseSHA256 {
		return BackupManifest{}, fmt.Errorf("SQLite 备份校验失败")
	}
	if manifest.SchemaVersion != 0 && manifest.SchemaVersion != schemaVersion {
		return BackupManifest{}, fmt.Errorf("备份 schema 版本不兼容: %d", manifest.SchemaVersion)
	}
	backupDatabase, err := sql.Open("sqlite", manifest.Database)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("打开备份数据库失败: %w", err)
	}
	defer backupDatabase.Close()
	var integrity string
	if err := backupDatabase.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return BackupManifest{}, fmt.Errorf("SQLite 备份完整性校验失败")
	}
	manifestBlobs := make(map[string]BackupBlob, len(manifest.ContentBlobs))
	for _, blob := range manifest.ContentBlobs {
		if _, exists := manifestBlobs[blob.ID]; exists || blob.ID == "" || blob.Size < 0 || !pathWithinResolved(backupDir, blob.Path) && !pathWithinResolved(filepath.Join(dataDirectory, "content"), blob.Path) {
			return BackupManifest{}, fmt.Errorf("正文备份路径不在内容目录内: %s", blob.ID)
		}
		info, err := os.Stat(blob.Path)
		if err != nil {
			return BackupManifest{}, fmt.Errorf("正文备份文件不存在: %s", blob.ID)
		}
		if info.Size() != blob.Size {
			return BackupManifest{}, fmt.Errorf("正文备份大小不匹配: %s", blob.ID)
		}
		digest, err := fileSHA256(blob.Path)
		if err != nil || hex.EncodeToString(digest[:]) != blob.SHA256 {
			return BackupManifest{}, fmt.Errorf("正文备份校验失败: %s", blob.ID)
		}
		manifestBlobs[blob.ID] = blob
	}
	rows, err := backupDatabase.Query("SELECT id FROM content_blobs")
	if err != nil {
		return BackupManifest{}, fmt.Errorf("读取备份正文记录失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return BackupManifest{}, err
		}
		if _, exists := manifestBlobs[id]; !exists {
			return BackupManifest{}, fmt.Errorf("备份缺少正文文件: %s", id)
		}
		delete(manifestBlobs, id)
	}
	if err := rows.Err(); err != nil {
		return BackupManifest{}, err
	}
	if len(manifestBlobs) != 0 {
		return BackupManifest{}, fmt.Errorf("备份清单包含数据库之外的正文文件")
	}
	return manifest, nil
}

// RestoreCompleteBackup 校验并恢复完整备份；失败时保留当前数据库和正文文件不变。
func RestoreCompleteBackup(database *sql.DB, manifestPath, dataDirectory string) error {
	manifest, err := VerifyBackupManifest(manifestPath, dataDirectory)
	if err != nil {
		return err
	}
	if manifest.SchemaVersion != 0 && manifest.SchemaVersion != schemaVersion {
		return fmt.Errorf("备份 schema 版本不兼容: %d", manifest.SchemaVersion)
	}
	backupDatabase, err := sql.Open("sqlite", manifest.Database)
	if err != nil {
		return fmt.Errorf("打开备份数据库失败: %w", err)
	}
	defer backupDatabase.Close()
	var backupVersion int
	if err := backupDatabase.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&backupVersion); err != nil || backupVersion != schemaVersion {
		return fmt.Errorf("备份数据库 schema 版本无效: %d", backupVersion)
	}
	stageDir := filepath.Join(dataDirectory, fmt.Sprintf(".restore-content-%d", time.Now().UTC().UnixNano()))
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return fmt.Errorf("创建恢复临时目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir)
	for _, blob := range manifest.ContentBlobs {
		if err := copyFile(blob.Path, filepath.Join(stageDir, backupBlobName(blob.ID))); err != nil {
			return fmt.Errorf("准备正文恢复失败: %w", err)
		}
	}
	contentDir := filepath.Join(dataDirectory, "content")
	oldContentDir := filepath.Join(dataDirectory, "backups", fmt.Sprintf("restore-old-content-%d", time.Now().UTC().UnixNano()))
	if _, statErr := os.Stat(contentDir); statErr == nil {
		if err := os.Rename(contentDir, oldContentDir); err != nil {
			return fmt.Errorf("保留当前正文目录失败: %w", err)
		}
	}
	if err := os.Rename(stageDir, contentDir); err != nil {
		_ = os.Rename(oldContentDir, contentDir)
		return fmt.Errorf("切换正文目录失败: %w", err)
	}
	if err := replaceFromBackup(database, manifest.Database, contentDir, manifest.ContentBlobs); err != nil {
		_ = os.RemoveAll(contentDir)
		_ = os.Rename(oldContentDir, contentDir)
		return err
	}
	return nil
}

func replaceFromBackup(database *sql.DB, backupPath, contentDir string, blobs []BackupBlob) error {
	deleteTables := []string{"content_blobs", "hourly_metric_dirty", "hourly_metrics", "audit_logs", "probe_runs", "health_events", "health_runtime", "attempts", "request_route_events", "requests", "response_affinity", "probe_policies", "channel_models", "channel_model_mappings", "global_model_mappings", "model_catalog", "channels", "app_settings"}
	insertTables := []string{"app_settings", "channels", "global_model_mappings", "channel_model_mappings", "channel_models", "probe_policies", "response_affinity", "model_catalog", "requests", "request_route_events", "attempts", "health_runtime", "health_events", "probe_runs", "audit_logs", "hourly_metrics", "hourly_metric_dirty", "content_blobs"}
	conn, err := database.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("获取恢复数据库连接失败: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "ATTACH DATABASE ? AS restore_source", backupPath); err != nil {
		return fmt.Errorf("挂载备份数据库失败: %w", err)
	}
	detached := false
	defer func() {
		if !detached {
			_, _ = conn.ExecContext(context.Background(), "DETACH DATABASE restore_source")
		}
	}()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("开始恢复事务失败: %w", err)
	}
	defer tx.Rollback()
	for _, table := range deleteTables {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("清理恢复表 %s 失败: %w", table, err)
		}
	}
	for _, table := range insertTables {
		if _, err := tx.Exec("INSERT INTO " + table + " SELECT * FROM restore_source." + table); err != nil {
			return fmt.Errorf("恢复表 %s 失败: %w", table, err)
		}
	}
	for _, blob := range blobs {
		if _, err := tx.Exec("UPDATE content_blobs SET path = ? WHERE id = ?", filepath.Join(contentDir, backupBlobName(blob.ID)), blob.ID); err != nil {
			return fmt.Errorf("更新正文恢复路径失败: %s: %w", blob.ID, err)
		}
	}
	if _, err := tx.Exec("DELETE FROM response_affinity WHERE expires_at <= ?", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("清理过期 Responses 亲和失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交恢复事务失败: %w", err)
	}
	if _, err := conn.ExecContext(context.Background(), "DETACH DATABASE restore_source"); err != nil {
		// 事务已经提交，连接关闭后 SQLite 会自动释放附加数据库；恢复结果仍然有效。
		return nil
	}
	detached = true
	return nil
}

// backupBlobName 根据正文 ID 生成不含路径分隔符的唯一目标名，避免不同正文的 basename 互相覆盖。
func backupBlobName(id string) string {
	return hex.EncodeToString([]byte(id)) + ".blob"
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func pathWithin(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathWithinResolved(root, value string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedValue, err := filepath.EvalSymlinks(value)
	if err != nil {
		return false
	}
	return pathWithin(resolvedRoot, resolvedValue)
}

func fileSHA256(path string) ([32]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [32]byte{}, err
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
