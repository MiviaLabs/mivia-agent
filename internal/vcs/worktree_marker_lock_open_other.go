//go:build !unix && !windows

package vcs

import "os"

func OpenMarkerExcludeLockFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
}
