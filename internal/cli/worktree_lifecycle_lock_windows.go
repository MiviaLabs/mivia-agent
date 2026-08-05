//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func openWorktreeLifecycleLockFile(root *os.Root, path string) (*os.File, func(), error) {
	fullPath, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), path))
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	for range 100 {
		handle, openErr := windows.CreateFile(
			fullPath,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_ALWAYS,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if openErr == nil {
			file := os.NewFile(uintptr(handle), filepath.Base(path))
			info, statErr := file.Stat()
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				_ = file.Close()
				if statErr != nil {
					return nil, nil, fmt.Errorf("inspect worktree lifecycle lock: %w", statErr)
				}
				return nil, nil, fmt.Errorf("worktree lifecycle lock is not a regular file")
			}
			return file, func() {}, nil
		}
		if openErr != windows.ERROR_SHARING_VIOLATION && openErr != windows.ERROR_LOCK_VIOLATION {
			return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", openErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, nil, fmt.Errorf("open worktree lifecycle lock: lock is busy")
}
