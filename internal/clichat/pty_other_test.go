//go:build !linux

package clichat

import (
	"os"
	"runtime"
	"testing"
)

// openTestPTY skips on non-Linux hosts: the underlying TIOCSPTLCK/TIOCGPTN
// ioctls exist only in the Linux build of golang.org/x/sys/unix, so callers
// see the same t.Skip posture they would get on a Linux host without
// /dev/ptmx.
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	t.Skipf("pty helper requires Linux; skipping on %s", runtime.GOOS)
	return nil, nil
}
