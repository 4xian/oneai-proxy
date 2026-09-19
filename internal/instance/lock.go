// Package instance 提供数据目录级单实例锁。
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrAlreadyRunning 表示同一数据目录已有进程持有锁。
var ErrAlreadyRunning = errors.New("oneai-proxy 已在运行")

// Lock 表示当前进程持有的数据目录锁。
type Lock struct {
	file *os.File
	path string
}

type metadata struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
}

// Acquire 获取数据目录级单实例锁，并记录当前进程信息。
func Acquire(dataDirectory string) (*Lock, error) {
	if err := os.MkdirAll(dataDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	path := filepath.Join(dataDirectory, "oneai-proxy.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开单实例锁失败: %w", err)
	}
	if err := tryLock(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrAlreadyRunning) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, lockOwner(path))
		}
		return nil, fmt.Errorf("获取单实例锁失败: %w", err)
	}
	info := metadata{PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	encoded, err := json.Marshal(info)
	if err != nil {
		_ = unlock(file)
		_ = file.Close()
		return nil, fmt.Errorf("写入单实例锁信息失败: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = unlock(file)
		_ = file.Close()
		return nil, fmt.Errorf("清理单实例锁信息失败: %w", err)
	}
	if _, err := file.WriteAt(encoded, 0); err != nil {
		_ = unlock(file)
		_ = file.Close()
		return nil, fmt.Errorf("写入单实例锁信息失败: %w", err)
	}
	return &Lock{file: file, path: path}, nil
}

// Close 释放单实例锁；锁文件本身保留为空文件，避免并发启动时路径消失。
func (lock *Lock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unlock(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func lockOwner(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "无法读取已有实例信息"
	}
	defer file.Close()
	var info metadata
	if err := json.NewDecoder(file).Decode(&info); err != nil || info.PID <= 0 {
		return "已有实例持有数据目录锁"
	}
	return fmt.Sprintf("进程 PID %d，启动于 %s", info.PID, info.StartedAt)
}
