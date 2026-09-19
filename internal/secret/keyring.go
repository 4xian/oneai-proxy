package secret

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const serviceName = "oneai-proxy"

// Store 定义平台秘密存储的最小读写边界。
type Store interface {
	Put(ref string, value []byte) error
	Get(ref string) ([]byte, error)
	Delete(ref string) error
}

// KeyringStore 将秘密值存入当前系统用户的原生密钥环。
type KeyringStore struct{}

// NewKeyringStore 创建跨平台系统密钥环适配器。
func NewKeyringStore() Store {
	return KeyringStore{}
}

// NewRef 创建不可预测的秘密引用标识。
func NewRef() (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成秘密引用失败: %w", err)
	}
	return "secret_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

// Put 写入或更新一个秘密引用对应的密文值。
func (KeyringStore) Put(ref string, value []byte) error {
	if err := keyring.Set(serviceName, ref, base64.RawStdEncoding.EncodeToString(value)); err != nil {
		return fmt.Errorf("写入系统密钥环失败: %w", err)
	}
	return nil
}

// Get 读取一个秘密引用对应的值。
func (KeyringStore) Get(ref string) ([]byte, error) {
	encoded, err := keyring.Get(serviceName, ref)
	if err != nil {
		return nil, fmt.Errorf("读取系统密钥环失败: %w", err)
	}
	value, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("解析系统密钥环值失败: %w", err)
	}
	return value, nil
}

// Delete 删除一个秘密引用对应的值。
func (KeyringStore) Delete(ref string) error {
	if ref == "" {
		return nil
	}
	if err := keyring.Delete(serviceName, ref); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("删除系统密钥环值失败: %w", err)
	}
	return nil
}
