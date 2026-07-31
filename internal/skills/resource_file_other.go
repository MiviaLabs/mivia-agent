//go:build !linux

package skills

import (
	"fmt"
	"os"
	"strings"
)

// Non-Linux platforms retain descriptor-relative path validation and opened
// file identity checks. Linux additionally uses openat(O_NONBLOCK) to close
// the replacement-FIFO race; platforms without that primitive keep the
// documented residual risk rather than pretending to provide that guarantee.
func openDeclaredResourceFile(root *os.File, resourcePath string) (*os.File, error) {
	dir, err := os.OpenRoot(root.Name())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	parts := strings.Split(resourcePath, "/")
	for _, part := range parts[:len(parts)-1] {
		info, err := dir.Lstat(part)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("resource parent is invalid")
		}
		next, err := dir.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		dir.Close()
		dir = next
	}
	name := parts[len(parts)-1]
	info, err := dir.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || !hasSingleLink(info) {
		return nil, fmt.Errorf("resource is not a safe regular file")
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !hasSingleLink(opened) || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("resource changed while opening")
	}
	return file, nil
}
