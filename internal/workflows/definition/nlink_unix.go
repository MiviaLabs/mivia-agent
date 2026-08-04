//go:build unix

package definition

import (
	"io/fs"
	"syscall"
)

// fileNlink returns the hard-link count of info, or 0 if unavailable.
func fileNlink(info fs.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}
