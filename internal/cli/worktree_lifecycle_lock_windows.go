//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openWorktreeLifecycleLockFile(root *os.Root, path string) (*os.File, func(), error) {
	if filepath.Dir(path) != worktreeLifecycleLockDir {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: invalid path")
	}
	rootFile, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	defer rootFile.Close()
	var rootIdentity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(rootFile.Fd()), &rootIdentity); err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	dirName, err := windows.UTF16PtrFromString(root.Name())
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	dirHandle, err := windows.CreateFile(
		dirName,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	defer windows.CloseHandle(dirHandle)
	var dirIdentity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(dirHandle, &dirIdentity); err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	if dirIdentity.VolumeSerialNumber != rootIdentity.VolumeSerialNumber ||
		dirIdentity.FileIndexHigh != rootIdentity.FileIndexHigh ||
		dirIdentity.FileIndexLow != rootIdentity.FileIndexLow {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: Git common directory identity changed")
	}
	file, err := openLifecycleLockFileIn(dirHandle, path)
	if err != nil {
		return nil, nil, err
	}
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

func openLifecycleLockFileIn(dirHandle windows.Handle, path string) (*os.File, error) {
	relativeName, err := windows.NewNTUnicodeString(path)
	if err != nil {
		return nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	var attrs windows.OBJECT_ATTRIBUTES
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	attrs.RootDirectory = dirHandle
	attrs.ObjectName = relativeName
	attrs.Attributes = windows.OBJ_CASE_INSENSITIVE
	for range 100 {
		var handle windows.Handle
		var io windows.IO_STATUS_BLOCK
		err := windows.NtCreateFile(
			&handle,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			&attrs,
			&io,
			nil,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
			windows.FILE_OPEN_IF,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
			0,
			0,
		)
		if err == nil {
			return os.NewFile(uintptr(handle), filepath.Base(path)), nil
		}
		if err != windows.STATUS_SHARING_VIOLATION && err != windows.STATUS_LOCK_NOT_GRANTED {
			return nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("open worktree lifecycle lock: lock is busy")
}
