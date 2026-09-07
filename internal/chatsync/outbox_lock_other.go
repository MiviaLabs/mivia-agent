//go:build !unix && !windows

package chatsync

import (
	"fmt"
	"os"
	"runtime"
)

// lockOutboxFile fails closed on a platform with no advisory file lock: the
// outbox must never admit a second writer just because the lock cannot be
// taken.
func lockOutboxFile(_ *os.File) error {
	return fmt.Errorf("chatsync: outbox locking is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

func unlockOutboxFile(_ *os.File) {}
