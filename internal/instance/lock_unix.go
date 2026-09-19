//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package instance

import (
	"errors"
	"os"
	"syscall"
)

func tryLock(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrAlreadyRunning
		}
		return err
	}
	return nil
}

func unlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
