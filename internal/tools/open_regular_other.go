//go:build !unix

package tools

import (
	"fmt"
	"os"
)

// openRegularFile opens path for reading and refuses non-regular files.
// Non-unix: best-effort Stat-after-open (no O_NONBLOCK); FIFO blocking on open
// remains a residual platform risk.
func openRegularFile(path string) (*os.File, os.FileInfo, error) {
	f, err := os.Open(path)
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
	return f, st, nil
}

func openRegularFileWrite(path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	if flag&os.O_WRONLY == 0 && flag&os.O_RDWR == 0 {
		flag |= os.O_WRONLY
	}
	f, err := os.OpenFile(path, flag, perm)
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
	return f, st, nil
}
