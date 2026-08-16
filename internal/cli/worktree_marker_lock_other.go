//go:build !unix && !windows

package cli

import (
	"fmt"
	"os"
	"runtime"
)

func lockWorktreeMarkerFile(_ *os.File) (func(), error) {
	return nil, fmt.Errorf("workflow execution locks are not supported on %s/%s; build for a unix or windows target", runtime.GOOS, runtime.GOARCH)
}
