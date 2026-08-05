//go:build !unix && !windows

package cli

import (
	"os"
	"sync"
)

var worktreeMarkerFileMutex sync.Mutex

func lockWorktreeMarkerFile(_ *os.File) (func(), error) {
	worktreeMarkerFileMutex.Lock()
	return worktreeMarkerFileMutex.Unlock, nil
}
