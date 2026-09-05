//go:build unix

package vcs

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var statLifecycleLockFile = func(file *os.File) (os.FileInfo, error) { return file.Stat() }

func openWorktreeLifecycleLockFile(root *os.Root, path string) (*os.File, func(), error) {
	if filepath.Dir(path) != worktreeLifecycleLockDir {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: invalid path")
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	rootFD, err := unix.Open(root.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	reopened := os.NewFile(uintptr(rootFD), root.Name())
	defer reopened.Close()
	reopenedInfo, statErr := reopened.Stat()
	if statErr != nil || !os.SameFile(rootInfo, reopenedInfo) {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: cannot verify Git common directory identity")
	}
	dirFD, err := unix.Openat(int(reopened.Fd()), worktreeLifecycleLockDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock directory: %w", err)
	}
	defer unix.Close(dirFD)
	fileFD, err := unix.Openat(dirFD, filepath.Base(path), unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), filepath.Base(path))
	info, err := statLifecycleLockFile(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspect worktree lifecycle lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("worktree lifecycle lock is not a regular file")
	}
	unlock, err := LockWorktreeMarkerFile(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, unlock, nil
}
