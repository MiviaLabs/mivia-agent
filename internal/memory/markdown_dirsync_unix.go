//go:build !windows

package memory

import (
	"fmt"
	"os"
)

// syncMemoryDir fsyncs the directory so the preceding rename is durable
// across a crash. POSIX requires the directory fsync: without it the rename
// can be lost even though the file's own data reached the disk.
func syncMemoryDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open memory directory for sync: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync memory directory: %w", err)
	}
	return nil
}
