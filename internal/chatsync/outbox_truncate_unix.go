//go:build !windows

package chatsync

import "os"

// truncateOutboxFile cuts the events file to size through the caller's own
// handle, which POSIX allows regardless of the handle's append mode.
func truncateOutboxFile(f *os.File, size int64) error {
	return f.Truncate(size)
}
