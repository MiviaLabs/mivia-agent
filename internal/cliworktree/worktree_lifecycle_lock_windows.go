//go:build windows

package cliworktree

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

// openWindowsDirectoryNoFollow opens one directory component relative to root
// without following a reparse point at that component. A junction or symlink
// at the component yields a handle to the reparse point itself, which is
// rejected by the FILE_ATTRIBUTE_REPARSE_POINT check. The caller owns the
// returned handle.
func openWindowsDirectoryNoFollow(rootDir windows.Handle, name string) (windows.Handle, error) {
	relativeName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	var attrs windows.OBJECT_ATTRIBUTES
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	attrs.RootDirectory = rootDir
	attrs.ObjectName = relativeName
	attrs.Attributes = windows.OBJ_CASE_INSENSITIVE
	var handle windows.Handle
	var io windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		windows.GENERIC_READ,
		&attrs,
		&io,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		0,
		0,
	)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("directory component is a reparse point")
	}
	return handle, nil
}

func openLifecycleLockFileIn(dirHandle windows.Handle, path string) (*os.File, error) {
	anchor := dirHandle
	if dir := filepath.Dir(path); dir != "." {
		lockDir, err := openWindowsDirectoryNoFollow(anchor, dir)
		if err != nil {
			return nil, fmt.Errorf("open worktree lifecycle lock directory: %w", err)
		}
		defer windows.CloseHandle(lockDir)
		anchor = lockDir
	}
	relativeName, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	var attrs windows.OBJECT_ATTRIBUTES
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	attrs.RootDirectory = anchor
	attrs.ObjectName = relativeName
	attrs.Attributes = windows.OBJ_CASE_INSENSITIVE
	// The lock file handle grants FILE_SHARE_READ so other handles (Lstat,
	// Stat) can still inspect the file while the lock is held. Write access
	// is not shared, so a second lock's GENERIC_WRITE open fails with
	// STATUS_SHARING_VIOLATION and the retry loop below reports "lock is
	// busy" - which is exactly the exclusivity the lock needs. Sharing
	// nothing would make even an Lstat fail with a raw sharing violation.
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
			windows.FILE_SHARE_READ,
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
