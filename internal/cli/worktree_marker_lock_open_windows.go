//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openMarkerExcludeLockFile(root *os.Root, path string) (*os.File, error) {
	rootFile, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	relativeName, err := windows.NewNTUnicodeString(path)
	if err != nil {
		return nil, err
	}
	var attrs windows.OBJECT_ATTRIBUTES
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	attrs.RootDirectory = windows.Handle(rootFile.Fd())
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
	return os.NewFile(uintptr(handle), filepath.Base(path)), nil
}
