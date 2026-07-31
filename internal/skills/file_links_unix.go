//go:build unix

package skills

import (
	"io/fs"
	"syscall"
)

func hasSingleLink(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
