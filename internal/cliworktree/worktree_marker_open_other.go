//go:build !unix

package cliworktree

import "os"

func openWorktreeMarkerForRead(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY, 0)
}
