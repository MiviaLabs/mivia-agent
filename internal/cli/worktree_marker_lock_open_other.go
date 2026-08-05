//go:build !unix && !windows

package cli

import "os"

func openMarkerExcludeLockFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
}
