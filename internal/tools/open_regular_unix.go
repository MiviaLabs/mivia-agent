//go:build unix

package tools

import (
	"fmt"
	"os"
	"syscall"
)

// openFileNonblock opens path with O_NONBLOCK so FIFO/special open cannot
// block the agent tool worker (TOCTOU after a prior Stat).
func openFileNonblock(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NONBLOCK, perm)
}

// clearNonblock restores blocking I/O for sequential reads/writes on a
// confirmed regular file.
func clearNonblock(f *os.File) error {
	fd := int(f.Fd())
	flags, err := fcntlGetFl(fd)
	if err != nil {
		return err
	}
	return fcntlSetFl(fd, flags&^syscall.O_NONBLOCK)
}

func fcntlGetFl(fd int) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFL), 0)
	if errno != 0 {
		return 0, errno
	}
	return int(flags), nil
}

func fcntlSetFl(fd int, flags int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_SETFL), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}

// openRegularFile opens path for reading without blocking on special files.
// On success the file is a regular file and blocking mode is restored.
func openRegularFile(path string) (*os.File, os.FileInfo, error) {
	f, err := openFileNonblock(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if st.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is a directory; use list_dir")
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
	}
	if err := clearNonblock(f); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, st, nil
}

// openRegularFileWrite opens/creates path for writing without blocking on FIFOs.
// flag should include O_WRONLY and typically O_CREATE|O_TRUNC as needed.
func openRegularFileWrite(path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	if flag&os.O_WRONLY == 0 && flag&os.O_RDWR == 0 {
		flag |= os.O_WRONLY
	}
	f, err := openFileNonblock(path, flag, perm)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if st.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is a directory")
	}
	if !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is not a regular file (mode %s); refusing special files that can block", st.Mode().Type())
	}
	if err := clearNonblock(f); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, st, nil
}
