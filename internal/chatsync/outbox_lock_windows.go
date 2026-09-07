//go:build windows

package chatsync

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockOutboxFile takes a non-blocking exclusive lock on the first byte of the
// outbox lock file. LOCKFILE_FAIL_IMMEDIATELY gives the same refuse-do-not-wait
// contract as the Unix LOCK_NB flock; the lock is released with the file
// handle, so unlockOutboxFile only has to undo the explicit range.
func lockOutboxFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
}

func unlockOutboxFile(file *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
