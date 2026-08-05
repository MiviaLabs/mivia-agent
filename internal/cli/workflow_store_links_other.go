//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package cli

import "os"

func workflowStoreHasSingleLink(string, os.FileInfo) bool {
	return false
}
