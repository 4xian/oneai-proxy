package storage

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/4xian/oneai-proxy/internal/secret"
	"github.com/zalando/go-keyring"
)

const contentChunkSize = 64 << 10

const contentMasterKeyRef = "content-master"

var contentMasterKeyMu sync.Mutex

// ContentBlob 描述一份可选的加密正文文件，不包含正文内容本身。
type ContentBlob struct {
	ID            string `json:"id"`
	RequestID     string `json:"requestId"`
	AttemptID     string `json:"attemptId,omitempty"`
	ContentType   string `json:"contentType"`
	KeyRef        string `json:"keyRef"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"` // 兼容旧客户端，等同于保存大小
	OriginalSize  int64  `json:"originalSize"`
	SavedSize     int64  `json:"savedSize"`
	Truncated     bool   `json:"truncated"`
	CreatedAt     string `json:"createdAt"`
	RedactHeaders string `json:"-"`
}

// WriteContentBlob 压缩并以分块 AEAD 写入原始正文；临时文件首字节即为密文。
func WriteContentBlob(database *sql.DB, store secret.Store, dataDirectory, requestID, contentType string, data []byte, originalSize int64, maxBytes int, quotaBytes int64) (ContentBlob, error) {
	return writeContentBlob(database, store, dataDirectory, requestID, "", contentType, data, originalSize, maxBytes, quotaBytes, "")
}

// WriteAttemptContentBlob 保存与单次上游 Attempt 关联的加密正文。
func WriteAttemptContentBlob(database *sql.DB, store secret.Store, dataDirectory, requestID, attemptID, contentType string, data []byte, originalSize int64, maxBytes int, quotaBytes int64) (ContentBlob, error) {
	return writeContentBlob(database, store, dataDirectory, requestID, attemptID, contentType, data, originalSize, maxBytes, quotaBytes, "")
}

// WriteAttemptContentBlobWithRedact 保存 Attempt 正文，并记录当次需要遮罩的凭证 Header 名称。
func WriteAttemptContentBlobWithRedact(database *sql.DB, store secret.Store, dataDirectory, requestID, attemptID, contentType string, data []byte, originalSize int64, maxBytes int, quotaBytes int64, redactHeaders string) (ContentBlob, error) {
	return writeContentBlob(database, store, dataDirectory, requestID, attemptID, contentType, data, originalSize, maxBytes, quotaBytes, redactHeaders)
}

// writeContentBlob 按大小策略截取原始内容，并以分块 AEAD 写入正文文件和元数据。
func writeContentBlob(database *sql.DB, store secret.Store, dataDirectory, requestID, attemptID, contentType string, data []byte, originalSize int64, maxBytes int, quotaBytes int64, redactHeaders string) (ContentBlob, error) {
	if database == nil || store == nil || strings.TrimSpace(requestID) == "" {
		return ContentBlob{}, fmt.Errorf("正文日志参数无效")
	}
	if originalSize < int64(len(data)) {
		originalSize = int64(len(data))
	}
	savedLen := len(data)
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	if maxBytes > 0 && savedLen > maxBytes {
		savedLen = maxBytes
	}
	truncated := originalSize > int64(savedLen)
	saved := data[:savedLen]
	compressed, err := gzipBytes(saved)
	if err != nil {
		return ContentBlob{}, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return ContentBlob{}, fmt.Errorf("生成正文密钥失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return ContentBlob{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ContentBlob{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ContentBlob{}, fmt.Errorf("生成正文随机数失败: %w", err)
	}
	keyRef, err := secret.NewRef()
	if err != nil {
		return ContentBlob{}, err
	}
	masterKey, err := getContentMasterKey(store)
	if err != nil {
		return ContentBlob{}, err
	}
	wrappedKey, err := wrapContentKey(masterKey, key)
	if err != nil {
		return ContentBlob{}, err
	}
	if err := store.Put(keyRef, wrappedKey); err != nil {
		return ContentBlob{}, err
	}
	contentDir := filepath.Join(dataDirectory, "content")
	if err := os.MkdirAll(contentDir, 0o700); err != nil {
		_ = store.Delete(keyRef)
		return ContentBlob{}, fmt.Errorf("创建正文目录失败: %w", err)
	}
	if quotaBytes > 0 {
		if err := trimContentQuota(database, store, contentDir, quotaBytes, int64(len(compressed))+int64((len(compressed)+contentChunkSize-1)/contentChunkSize)*int64(aead.Overhead())); err != nil {
			_ = store.Delete(keyRef)
			return ContentBlob{}, err
		}
	}
	id, err := NewContentBlobID()
	if err != nil {
		_ = store.Delete(keyRef)
		return ContentBlob{}, err
	}
	finalPath := filepath.Join(contentDir, id+".enc")
	tempFile, err := os.CreateTemp(contentDir, ".content-*.tmp")
	if err != nil {
		_ = store.Delete(keyRef)
		return ContentBlob{}, fmt.Errorf("创建正文临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	_ = tempFile.Chmod(0o600)
	for index, offset := 0, 0; offset < len(compressed); index, offset = index+1, offset+contentChunkSize {
		end := offset + contentChunkSize
		if end > len(compressed) {
			end = len(compressed)
		}
		chunkNonce := chunkNonce(nonce, index)
		ciphertext := aead.Seal(nil, chunkNonce, compressed[offset:end], chunkAAD(index))
		if _, err := tempFile.Write(ciphertext); err != nil {
			_ = tempFile.Close()
			_ = store.Delete(keyRef)
			return ContentBlob{}, fmt.Errorf("写入正文密文失败: %w", err)
		}
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		_ = store.Delete(keyRef)
		return ContentBlob{}, fmt.Errorf("同步正文密文失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = store.Delete(keyRef)
		return ContentBlob{}, err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = store.Delete(keyRef)
		return ContentBlob{}, fmt.Errorf("原子保存正文密文失败: %w", err)
	}
	blob := ContentBlob{ID: id, RequestID: requestID, AttemptID: attemptID, ContentType: contentType, KeyRef: keyRef, SHA256: hex.EncodeToString(sumSHA256(saved)), Size: int64(len(saved)), OriginalSize: originalSize, SavedSize: int64(len(saved)), Truncated: truncated, CreatedAt: FormatSQLiteTime(time.Now()), RedactHeaders: strings.TrimSpace(redactHeaders)}
	_, err = database.Exec(`INSERT INTO content_blobs(id, request_id, attempt_id, key_ref, nonce, sha256, size, original_size, saved_size, truncated, created_at, path, content_type, redact_headers) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, blob.ID, blob.RequestID, blob.AttemptID, blob.KeyRef, nonce, blob.SHA256, blob.Size, blob.OriginalSize, blob.SavedSize, boolIntContent(blob.Truncated), blob.CreatedAt, finalPath, blob.ContentType, blob.RedactHeaders)
	if err != nil {
		_ = os.Remove(finalPath)
		_ = store.Delete(keyRef)
		return ContentBlob{}, fmt.Errorf("写入正文元数据失败: %w", err)
	}
	return blob, nil
}

// getContentMasterKey 读取正文主密钥；仅在密钥环明确返回不存在时初始化新密钥。
func getContentMasterKey(store secret.Store) ([]byte, error) {
	// 主密钥首次创建需要把读取和写入放在同一临界区，避免并发请求各自生成并覆盖主密钥。
	contentMasterKeyMu.Lock()
	defer contentMasterKeyMu.Unlock()

	value, err := store.Get(contentMasterKeyRef)
	if err == nil {
		if len(value) != 32 {
			return nil, fmt.Errorf("正文主密钥长度无效: %d", len(value))
		}
		return value, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("读取正文主密钥失败: %w", err)
	}
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		return nil, fmt.Errorf("生成正文主密钥失败: %w", err)
	}
	if err := store.Put(contentMasterKeyRef, master); err != nil {
		return nil, err
	}
	return master, nil
}

// wrapContentKey 使用正文主密钥封装单份正文的数据密钥。
func wrapContentKey(master, dataKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, dataKey, []byte("oneai-proxy-content-key"))...), nil
}

// unwrapContentKey 使用正文主密钥解封单份正文的数据密钥。
func unwrapContentKey(master, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(master)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < aead.NonceSize() {
		return nil, fmt.Errorf("正文密钥长度无效")
	}
	nonce, ciphertext := wrapped[:aead.NonceSize()], wrapped[aead.NonceSize():]
	return aead.Open(nil, nonce, ciphertext, []byte("oneai-proxy-content-key"))
}

// NewContentBlobID 创建正文记录 ID。
func NewContentBlobID() (string, error) { return newID("blob_") }

func gzipBytes(data []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func gunzipBytes(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func chunkNonce(base []byte, index int) []byte {
	value := append([]byte(nil), base...)
	binary.BigEndian.PutUint32(value[len(value)-4:], uint32(index))
	return value
}

func chunkAAD(index int) []byte {
	value := make([]byte, 4)
	binary.BigEndian.PutUint32(value, uint32(index))
	return value
}

var credentialJSONValue = regexp.MustCompile(`(?i)("(?:authorization|x[-_ ]?api[-_ ]?key|api[-_ ]?key)"\s*:\s*")(.*?)(")`)

// RedactContentForAPI 仅在管理 API 返回正文时遮罩指定的上游凭证字段。
func RedactContentForAPI(data []byte, extraNames ...string) []byte {
	extra := extraCredentialNames(extraNames)
	var value any
	if json.Unmarshal(data, &value) != nil {
		return credentialJSONValue.ReplaceAll(data, []byte(`${1}***${3}`))
	}
	if !redactCredentialValue(value, extra) {
		return data
	}
	result, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return result
}

// extraCredentialNames 把调用方传入的额外凭证头名规范化成集合，忽略空值和 Cookie。
func extraCredentialNames(values []string) map[string]struct{} {
	extra := make(map[string]struct{})
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			normalized := normalizeHeaderName(name)
			if normalized != "" && normalized != "cookie" {
				extra[normalized] = struct{}{}
			}
		}
	}
	return extra
}

// redactCredentialValue 递归遮罩 JSON 对象中的凭证字段，命中时原地改写并返回 true。
func redactCredentialValue(value any, extra map[string]struct{}) bool {
	redacted := false
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			name := normalizeHeaderName(key)
			if isCredentialField(name) {
				current[key] = "***"
				redacted = true
				continue
			}
			if extra != nil {
				if _, match := extra[name]; match && name != "cookie" {
					current[key] = "***"
					redacted = true
					continue
				}
			}
			if redactCredentialValue(child, extra) {
				redacted = true
			}
		}
	case []any:
		for _, child := range current {
			if redactCredentialValue(child, extra) {
				redacted = true
			}
		}
	}
	return redacted
}

// normalizeHeaderName 把 HTTP 头名转成小写连字符形式，便于凭证字段比对。
func normalizeHeaderName(name string) string {
	return strings.Trim(strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", "-"), " ", "-")), "-")
}

// isCredentialField 判断规范化后的字段名是否属于固定凭证名或凭证后缀。
func isCredentialField(name string) bool {
	name = strings.Trim(name, "-")
	if name == "authorization" || name == "x-api-key" || name == "api-key" || name == "apikey" || name == "xapikey" || name == "token" || name == "access-token" || name == "refresh-token" || name == "id-token" || name == "client-secret" || name == "secret" || name == "password" || name == "credential" || name == "credentials" {
		return true
	}
	return strings.HasSuffix(name, "-token") || strings.HasSuffix(name, "-secret") || strings.HasSuffix(name, "-password")
}

func sumSHA256(data []byte) []byte {
	// 使用标准库哈希，避免把正文写入日志或数据库。
	imported := sha256Sum(data)
	return imported[:]
}

func boolIntContent(value bool) int {
	if value {
		return 1
	}
	return 0
}

func trimContentQuota(database *sql.DB, store secret.Store, contentDir string, quotaBytes, incoming int64) error {
	rows, err := database.Query(`SELECT id, key_ref, path, size FROM content_blobs ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return fmt.Errorf("读取正文配额失败: %w", err)
	}
	defer rows.Close()
	type item struct {
		id, keyRef, path string
		size             int64
	}
	items := make([]item, 0)
	var total int64
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.keyRef, &value.path, &value.size); err != nil {
			return err
		}
		if info, statErr := os.Stat(value.path); statErr == nil {
			value.size = info.Size()
		}
		items = append(items, value)
		total += value.size
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for total+incoming > quotaBytes && len(items) > 0 {
		value := items[0]
		items = items[1:]
		if err := deleteContentResources(store, value.keyRef, value.path); err != nil {
			return err
		}
		if _, err := database.Exec(`DELETE FROM content_blobs WHERE id = ?`, value.id); err != nil {
			return err
		}
		total -= value.size
	}
	if total+incoming > quotaBytes {
		return fmt.Errorf("正文磁盘配额不足")
	}
	return nil
}

// ListContentBlobs 返回请求关联的正文元数据。
func ListContentBlobs(database *sql.DB, requestID string) ([]ContentBlob, error) {
	rows, err := database.Query(`SELECT id, request_id, attempt_id, content_type, key_ref, sha256, size, original_size, saved_size, truncated, created_at, redact_headers FROM content_blobs WHERE request_id = ? ORDER BY created_at ASC, id ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("读取正文元数据失败: %w", err)
	}
	defer rows.Close()
	result := make([]ContentBlob, 0)
	for rows.Next() {
		var item ContentBlob
		var truncated int
		if err := rows.Scan(&item.ID, &item.RequestID, &item.AttemptID, &item.ContentType, &item.KeyRef, &item.SHA256, &item.Size, &item.OriginalSize, &item.SavedSize, &truncated, &item.CreatedAt, &item.RedactHeaders); err != nil {
			return nil, err
		}
		if item.SavedSize == 0 {
			item.SavedSize = item.Size
		}
		if item.Size == 0 {
			item.Size = item.SavedSize
		}
		item.Truncated = truncated != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

// ReadContentBlob 解密并解压一份正文快照；调用方应在管理鉴权后再返回内容。
func ReadContentBlob(database *sql.DB, store secret.Store, blobID string) ([]byte, ContentBlob, error) {
	var blob ContentBlob
	var nonce []byte
	var truncated int
	var path string
	err := database.QueryRow(`SELECT id, request_id, attempt_id, content_type, key_ref, nonce, sha256, size, original_size, saved_size, truncated, created_at, path, redact_headers FROM content_blobs WHERE id = ?`, blobID).Scan(&blob.ID, &blob.RequestID, &blob.AttemptID, &blob.ContentType, &blob.KeyRef, &nonce, &blob.SHA256, &blob.Size, &blob.OriginalSize, &blob.SavedSize, &truncated, &blob.CreatedAt, &path, &blob.RedactHeaders)
	if err == sql.ErrNoRows {
		return nil, ContentBlob{}, ErrNotFound
	}
	if err != nil {
		return nil, ContentBlob{}, fmt.Errorf("读取正文元数据失败: %w", err)
	}
	blob.Truncated = truncated != 0
	if blob.SavedSize == 0 {
		blob.SavedSize = blob.Size
	}
	if blob.Size == 0 {
		blob.Size = blob.SavedSize
	}
	wrapped, err := store.Get(blob.KeyRef)
	if err != nil {
		return nil, ContentBlob{}, fmt.Errorf("读取正文密钥失败: %w", err)
	}
	master, err := getContentMasterKey(store)
	if err != nil {
		return nil, ContentBlob{}, err
	}
	key, err := unwrapContentKey(master, wrapped)
	if err != nil {
		return nil, ContentBlob{}, fmt.Errorf("解包正文密钥失败: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ContentBlob{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ContentBlob{}, err
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, ContentBlob{}, fmt.Errorf("读取正文密文失败: %w", err)
	}
	plain := make([]byte, 0, len(ciphertext))
	for offset, index := 0, 0; offset < len(ciphertext); index++ {
		end := offset + contentChunkSize + aead.Overhead()
		if end > len(ciphertext) {
			end = len(ciphertext)
		}
		part, err := aead.Open(nil, chunkNonce(nonce, index), ciphertext[offset:end], chunkAAD(index))
		if err != nil {
			return nil, ContentBlob{}, fmt.Errorf("校验正文密文失败: %w", err)
		}
		plain = append(plain, part...)
		offset = end
	}
	result, err := gunzipBytes(plain)
	if err != nil {
		return nil, ContentBlob{}, fmt.Errorf("解压正文失败: %w", err)
	}
	return result, blob, nil
}

// CleanupContentBlobs 删除已过期请求关联的正文文件和密钥引用。
func CleanupContentBlobs(database *sql.DB, store secret.Store, cutoff time.Time) (int64, error) {
	rows, err := database.Query(`SELECT id, key_ref, path FROM content_blobs WHERE request_id IN (SELECT id FROM requests WHERE COALESCE(completed_at, started_at) < ?)`, FormatSQLiteTime(cutoff))
	if err != nil {
		return 0, err
	}
	type item struct{ id, keyRef, path string }
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.keyRef, &value.path); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range items {
		if err := deleteContentResources(store, value.keyRef, value.path); err != nil {
			return 0, err
		}
		if _, err := database.Exec(`DELETE FROM content_blobs WHERE id = ?`, value.id); err != nil {
			return 0, err
		}
	}
	return int64(len(items)), nil
}

// DeleteFinalResponseContent 删除尚未提交给客户端的最终响应快照，供账本失败响应替换使用。
func DeleteFinalResponseContent(database *sql.DB, store secret.Store, requestID string) error {
	rows, err := database.Query(`SELECT id, key_ref, path FROM content_blobs WHERE request_id = ? AND attempt_id = '' AND content_type IN ('response_headers', 'response')`, requestID)
	if err != nil {
		return fmt.Errorf("读取最终响应快照失败: %w", err)
	}
	type item struct{ id, keyRef, path string }
	items := make([]item, 0, 2)
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.keyRef, &value.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range items {
		if err := deleteContentResources(store, value.keyRef, value.path); err != nil {
			return err
		}
		if _, err := database.Exec(`DELETE FROM content_blobs WHERE id = ?`, value.id); err != nil {
			return fmt.Errorf("删除最终响应快照失败: %w", err)
		}
	}
	return nil
}

// DeleteAttemptContent 删除一次未发送 Attempt 产生的正文文件和密钥引用。
func DeleteAttemptContent(database *sql.DB, store secret.Store, requestID, attemptID string) error {
	rows, err := database.Query(`SELECT id, key_ref, path FROM content_blobs WHERE request_id = ? AND attempt_id = ?`, requestID, attemptID)
	if err != nil {
		return fmt.Errorf("读取 Attempt 正文快照失败: %w", err)
	}
	type item struct{ id, keyRef, path string }
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.id, &value.keyRef, &value.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range items {
		if err := deleteContentResources(store, value.keyRef, value.path); err != nil {
			return err
		}
		if _, err := database.Exec(`DELETE FROM content_blobs WHERE id = ?`, value.id); err != nil {
			return fmt.Errorf("删除 Attempt 正文快照失败: %w", err)
		}
	}
	return nil
}

func deleteContentResources(store secret.Store, keyRef, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除正文密文失败: %w", err)
	}
	if err := store.Delete(keyRef); err != nil {
		return fmt.Errorf("删除正文密钥失败: %w", err)
	}
	return nil
}

// sha256Sum 独立封装以便正文模块保持单一职责。
func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
