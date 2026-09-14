//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package workflow

import "os"

func workflowStoreHasSingleLink(string, os.FileInfo) bool {
	return false
}
