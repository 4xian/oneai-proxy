package secret

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	localSecretDirectory = "secret-store"
	localMasterKeyFile   = "master.key"
	localSecretFileMagic = "oneai-local-secret-v1\n"
)

// FallbackStore 优先使用系统密钥环，并在其不可用时使用受限本地加密存储。
type FallbackStore struct {
	primary  Store
	local    *encryptedFileStore
	logger   *slog.Logger
	warnOnce sync.Once
	mu       sync.Mutex
}

// NewResilientStore 创建带受限本地加密降级能力的系统秘密存储。
func NewResilientStore(dataDirectory string, logger *slog.Logger) Store {
	return NewFallbackStore(NewKeyringStore(), dataDirectory, logger)
}

// NewFallbackStore 使用可注入的首选存储创建降级适配器。
func NewFallbackStore(primary Store, dataDirectory string, logger *slog.Logger) Store {
	if primary == nil {
		primary = NewKeyringStore()
	}
	return &FallbackStore{primary: primary, local: &encryptedFileStore{directory: filepath.Join(dataDirectory, localSecretDirectory), enabled: strings.TrimSpace(dataDirectory) != ""}, logger: logger}
}

// Put 优先写入系统密钥环，失败时才写入受限本地加密文件。
func (store *FallbackStore) Put(ref string, value []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.primary.Put(ref, value); err == nil {
		if _, cleanupErr := store.local.delete(ref); cleanupErr != nil {
			rollbackErr := store.primary.Delete(ref)
			return errors.Join(fmt.Errorf("清理本地秘密副本失败: %w", cleanupErr), rollbackErr)
		}
		return nil
	} else if localErr := store.local.Put(ref, value); localErr != nil {
		return errors.Join(err, localErr)
	} else {
		store.warnFallback(err)
		return nil
	}
}

// Get 先读取系统密钥环，失败后尝试受限本地加密文件。
func (store *FallbackStore) Get(ref string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, primaryErr := store.primary.Get(ref)
	if primaryErr == nil {
		return value, nil
	}
	value, localErr := store.local.Get(ref)
	if localErr != nil {
		return nil, errors.Join(primaryErr, localErr)
	}
	store.warnFallback(primaryErr)
	return value, nil
}

// Delete 同时清理系统密钥环和本地副本，确保降级写入的秘密可以正常删除。
func (store *FallbackStore) Delete(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	primaryErr := store.primary.Delete(ref)
	_, localErr := store.local.delete(ref)
	if localErr != nil {
		return errors.Join(primaryErr, localErr)
	}
	return primaryErr
}

func (store *FallbackStore) warnFallback(cause error) {
	store.warnOnce.Do(func() {
		if store.logger == nil {
			return
		}
		store.logger.Warn("系统密钥环不可用，已启用受限本地加密秘密存储；主密钥和密文位于同一数据目录，只能降低数据库单文件泄露风险，无法防止同一账户读取整个数据目录", "directory", store.local.directory, "error", cause)
	})
}

type encryptedFileStore struct {
	directory string
	enabled   bool
	mu        sync.Mutex
}

func (store *encryptedFileStore) Put(ref string, value []byte) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("本地秘密引用不能为空")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key, err := store.loadMasterKey()
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("初始化本地秘密加密失败: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("生成本地秘密随机数失败: %w", err)
	}
	payload := append([]byte(localSecretFileMagic), nonce...)
	payload = aead.Seal(payload, nonce, value, []byte(ref))
	if err := writePrivateFile(store.path(ref), payload); err != nil {
		return fmt.Errorf("写入本地秘密密文失败: %w", err)
	}
	return nil
}

func (store *encryptedFileStore) Get(ref string) ([]byte, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, errors.New("本地秘密引用不能为空")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key, err := store.loadMasterKey()
	if err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(store.path(ref))
	if err != nil {
		return nil, fmt.Errorf("读取本地秘密密文失败: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("初始化本地秘密解密失败: %w", err)
	}
	prefixLength := len(localSecretFileMagic)
	if len(payload) < prefixLength+aead.NonceSize()+aead.Overhead() || string(payload[:prefixLength]) != localSecretFileMagic {
		return nil, errors.New("本地秘密密文格式无效")
	}
	nonce := payload[prefixLength : prefixLength+aead.NonceSize()]
	value, err := aead.Open(nil, nonce, payload[prefixLength+aead.NonceSize():], []byte(ref))
	if err != nil {
		return nil, fmt.Errorf("校验本地秘密密文失败: %w", err)
	}
	return value, nil
}

func (store *encryptedFileStore) Delete(ref string) error {
	_, err := store.delete(ref)
	return err
}

func (store *encryptedFileStore) delete(ref string) (bool, error) {
	if strings.TrimSpace(ref) == "" {
		return false, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.Remove(store.path(ref)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("删除本地秘密密文失败: %w", err)
	}
	return true, nil
}

func (store *encryptedFileStore) loadMasterKey() ([]byte, error) {
	if !store.enabled {
		return nil, errors.New("本地秘密存储缺少数据目录")
	}
	if err := os.MkdirAll(store.directory, 0o700); err != nil {
		return nil, fmt.Errorf("创建本地秘密目录失败: %w", err)
	}
	if err := os.Chmod(store.directory, 0o700); err != nil {
		return nil, fmt.Errorf("限制本地秘密目录权限失败: %w", err)
	}
	path := filepath.Join(store.directory, localMasterKeyFile)
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != chacha20poly1305.KeySize {
			return nil, errors.New("本地秘密主密钥格式无效")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("限制本地秘密主密钥权限失败: %w", err)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取本地秘密主密钥失败: %w", err)
	}
	key = make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成本地秘密主密钥失败: %w", err)
	}
	if err := writePrivateFile(path, key); err != nil {
		return nil, fmt.Errorf("写入本地秘密主密钥失败: %w", err)
	}
	return key, nil
}

func (store *encryptedFileStore) path(ref string) string {
	digest := sha256.Sum256([]byte(ref))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".enc")
}

func writePrivateFile(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
