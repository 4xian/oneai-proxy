//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || windows)

package instance

import (
	"errors"
	"os"
)

func tryLock(_ *os.File) error {
	return errors.New("当前平台不支持单实例文件锁")
}

func unlock(_ *os.File) error { return nil }
