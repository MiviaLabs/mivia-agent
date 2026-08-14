package storage

import (
	"context"
	"time"
)

// sqliteBusyRetryDelays is the backoff for retrySQLiteBusy. It mirrors the
// busy-retry cadence of internal/chat/session_title.go: a write lock another
// process holds is normally gone within milliseconds, so a short backoff
// clears a transient SQLITE_BUSY collision.
var sqliteBusyRetryDelays = []time.Duration{150 * time.Millisecond, 400 * time.Millisecond}

// retrySQLiteBusy repeats op while SQLite reports a transient busy or locked
// condition. Pass an operation that runs one full transaction: a failed
// attempt rolled back and left no state, so a retry repeats a complete, safe
// body. A retry also re-reads current state, which keeps fence checks intact:
// a busy failure caused by a concurrent invalidation of what the transaction
// read (the stale write-upgrade failure the interleave tests rely on) returns
// the durable sentinel on the retry, not the busy error.
//
// This exists because several processes can share one context.db file - the
// mivia chat sidecars of sibling desktop threads do. A deferred transaction
// that another process commits between its reads and its first write fails
// the write-lock upgrade with SQLITE_BUSY_SNAPSHOT at once; busy_timeout
// cannot clear that failure. Without a retry, the commit of a finished turn
// was lost to an unrelated sibling commit, and the turn with it.
func retrySQLiteBusy(ctx context.Context, op func() error) error {
	err := op()
	for _, delay := range sqliteBusyRetryDelays {
		if !isSQLiteBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		err = op()
	}
	return err
}
