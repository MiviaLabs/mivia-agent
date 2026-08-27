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
//
// Known cost: the darwin and windows CI runners exercise the pty-dependent
// tests (ensureChatAPIKey interactive prompt, chat-TUI dispatch) as skips,
// so their TTY paths have zero automated coverage off Linux. A native darwin
// implementation would close this debt.
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	t.Skipf("pty helper requires Linux; skipping on %s", runtime.GOOS)
	// Compile-required: Skipf is not a terminating statement, so removing
	// this return breaks the build even though it is unreachable at runtime.
	return nil, nil
}
