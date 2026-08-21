//go:build !unix && !windows

package cliworktree

import (
	"fmt"
	"os"
	"runtime"
)

func LockWorktreeMarkerFile(_ *os.File) (func(), error) {
	return nil, fmt.Errorf("workflow execution locks are not supported on %s/%s; build for a unix or windows target", runtime.GOOS, runtime.GOARCH)
}
