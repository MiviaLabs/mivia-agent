//go:build windows

package cliworktree

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func OpenMarkerExcludeLockFile(root *os.Root, path string) (*os.File, error) {
	rootFile, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	anchor := windows.Handle(rootFile.Fd())
	if dir := filepath.Dir(path); dir != "." {
		infoHandle, err := openWindowsDirectoryNoFollow(anchor, dir)
		if err != nil {
			return nil, err
		}
		defer windows.CloseHandle(infoHandle)
		anchor = infoHandle
	}
	relativeName, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	var attrs windows.OBJECT_ATTRIBUTES
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	attrs.RootDirectory = anchor
	attrs.ObjectName = relativeName
	attrs.Attributes = windows.OBJ_CASE_INSENSITIVE
	var handle windows.Handle
	var io windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(
		&handle,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		&attrs,
		&io,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), filepath.Base(path))
	// FILE_OPEN_REPARSE_POINT opens the reparse point itself instead of
	// following it, so a final-component symlink must be rejected after the
	// open, matching the Unix O_NOFOLLOW contract: the lock is a place where
	// an attacker would plant a link, and the handle must never name the
	// link's target.
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, fmt.Errorf("Git exclude lock is a symlink")
	}
	return file, nil
}
