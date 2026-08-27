//go:build unix

package cliworktree

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func OpenMarkerExcludeLockFile(root *os.Root, path string) (*os.File, error) {
	rootFile, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer rootFile.Close()
	dirFD := int(rootFile.Fd())
	if dir := filepath.Dir(path); dir != "." {
		fd, err := unix.Openat(dirFD, dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		defer unix.Close(fd)
		dirFD = fd
	}
	fileFD, err := unix.Openat(dirFD, filepath.Base(path), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), filepath.Base(path)), nil
}
