//go:build unix

package vcs

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func LockWorktreeMarkerFile(file *os.File) (func(), error) {
	for range 100 {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return nil, fmt.Errorf("lock Git exclude: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("lock Git exclude: lock is busy")
}
