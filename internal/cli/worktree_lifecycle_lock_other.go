//go:build !unix && !windows

package cli

import (
	"fmt"
	"os"
)

func openWorktreeLifecycleLockFile(root *os.Root, path string) (*os.File, func(), error) {
	file, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open worktree lifecycle lock: %w", err)
	}
	unlock, err := lockWorktreeMarkerFile(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, unlock, nil
}
