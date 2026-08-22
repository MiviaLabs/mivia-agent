//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cliworkflow

import (
	"os"
	"syscall"
)

func workflowStoreHasSingleLink(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
