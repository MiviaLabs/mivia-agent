//go:build unix

package chatsync

import (
	"syscall"
	"testing"
)

// TestOutboxClose_EventsFileCloseErrorSurfaces pins Close's first-error
// capture: closing the fd out from under the *os.File (bypassing its own
// Close) makes the file's own Close report EBADF, the one reliable way to
// make a healthy, already-synced regular file's Close fail without an OS-
// specific fault. syscall.Close takes an int fd on unix but a
// syscall.Handle on Windows, so this is unix-only; Close's error-capture
// logic itself is platform-agnostic and gets its Windows-reachable coverage
// from every other test that opens and closes a real outbox.
func TestOutboxClose_EventsFileCloseErrorSurfaces(t *testing.T) {
	ob, _ := openTestOutbox(t)
	fd := ob.eventsFile.Fd()
	if err := syscall.Close(int(fd)); err != nil {
		t.Skipf("platform would not let the test close the fd directly: %v", err)
	}
	if err := ob.Close(); err == nil {
		t.Fatal("Close accepted an events file whose fd was already closed")
	}
	// The lock must still be released despite the events-file error: Close
	// captures the first error but must not return early and skip it.
	ob.lockFile = nil // avoid a double-release in t.Cleanup's ob.Close()
}
