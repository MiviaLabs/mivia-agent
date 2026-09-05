//go:build !unix && !windows

package vcs

import (
	"fmt"
	"os"
)

func openWorktreeLifecycleLockFile(_ *os.Root, _ string) (*os.File, func(), error) {
	return nil, nil, fmt.Errorf("open worktree lifecycle lock: atomic no-follow open is not available")
}
