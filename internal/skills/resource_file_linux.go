//go:build linux

package skills

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// openDeclaredResourceFile walks a root descriptor using openat(2). Every
// parent is lstat'ed without following links, and the final O_NONBLOCK open
// prevents a replacement FIFO from pinning an agent worker.
func openDeclaredResourceFile(root *os.File, _ *os.Root, resourcePath string) (*os.File, error) {
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	// Close the live fd at return: the traversal loop below closes and
	// reassigns fd, so a bare `defer unix.Close(fd)` would capture the dup'd
	// root fd value at registration, double-close it after the loop reused it
	// (an fd-reuse hazard), and leak the final parent directory fd once per
	// call. The closure reads the current fd at return and closes each
	// directory fd exactly once on the success path and every error branch.
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(resourcePath, "/")
	for _, part := range parts[:len(parts)-1] {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, part, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, &os.PathError{Op: "open", Path: resourcePath, Err: err}
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, fmt.Errorf("resource parent is invalid")
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(fd)
		fd = next
	}
	name := parts[len(parts)-1]
	var expected unix.Stat_t
	if err := unix.Fstatat(fd, name, &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, &os.PathError{Op: "open", Path: resourcePath, Err: err}
	}
	if expected.Mode&unix.S_IFMT != unix.S_IFREG || expected.Nlink != 1 {
		return nil, fmt.Errorf("resource is not a safe regular file")
	}
	fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Nlink != 1 || opened.Dev != expected.Dev || opened.Ino != expected.Ino {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("resource changed while opening")
	}
	flags, err := unix.FcntlInt(uintptr(fileFD), unix.F_GETFL, 0)
	if err != nil {
		_ = unix.Close(fileFD)
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(fileFD), unix.F_SETFL, flags&^unix.O_NONBLOCK); err != nil {
		_ = unix.Close(fileFD)
		return nil, err
	}
	return os.NewFile(uintptr(fileFD), name), nil
}
