//go:build linux

package clichat

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// openTestPTY opens a pty pair and returns both ends as *os.File, opened
// O_RDWR so the slave works as both a readable stdin and a writable stdout
// for term.IsTerminal checks. Callers: autosetup_test.go passes the pair as
// explicit stdin/stdout parameters to ensureChatAPIKey; diffcov2_test.go's
// withPtyStdin swaps os.Stdin for the slave. Skips (not fails) when the host
// has no usable /dev/ptmx. The TIOCSPTLCK/TIOCGPTN ioctls exist only in the
// Linux build of golang.org/x/sys/unix, so this helper lives behind a linux
// build tag; every other GOOS takes the pty_other_test.go skip stub.
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(m, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(m)
		t.Skipf("unlockpt failed: %v", err)
	}
	ptsN, err := unix.IoctlGetInt(m, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(m)
		t.Skipf("pts number failed: %v", err)
	}
	s, err := unix.Open("/dev/pts/"+itoa(ptsN), unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(m)
		t.Skipf("cannot open pty slave: %v", err)
	}
	master = os.NewFile(uintptr(m), "pty-master")
	slave = os.NewFile(uintptr(s), "pty-slave")
	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})
	return master, slave
}
