//go:build windows

package vcs

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func LockWorktreeMarkerFile(file *os.File) (func(), error) {
	var overlapped windows.Overlapped
	for range 100 {
		err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped) }, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			return nil, fmt.Errorf("lock Git exclude: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("lock Git exclude: lock is busy")
}
