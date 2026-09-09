//go:build unix

package chatsync

import (
	"os"
	"syscall"
)

// lockOutboxFile takes a non-blocking exclusive advisory lock on the outbox
// lock file. A busy lock is reported as a failure, never as a wait: a second
// process must be refused admission, not queued behind the first.
func lockOutboxFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockOutboxFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
