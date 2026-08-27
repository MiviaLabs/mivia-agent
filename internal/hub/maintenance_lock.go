package hub

// TryAcquireMaintenanceLock attempts the same non-blocking hub.lock a
// membershipLoop owner takes, so a destructive maintenance operation (store
// compaction, a full reset) can refuse to run while any interactive process -
// TUI, REPL, or a desktop sidecar - is joined to this workspace's hub.
//
// It reuses tryAcquireLock rather than a second flock path, so maintenance
// and ordinary hub ownership can never both believe they hold storeDir
// exclusively.
//
// This is a narrower guarantee than "no other process has the store file
// open": a one-shot CLI invocation (another `mivia sessions gc`, a bare
// `mivia chat` single turn) never calls Join and so never holds hub.lock.
// Deliberately left open rather than closed with a second, general-purpose
// reader-writer lock: SQLite's own busy-fails-clean behavior (see
// internal/storage/compact_contention_test.go) already bounds the residual
// risk to a transient, retryable error, never data loss or a torn file - a
// locking protocol on every store-opening command would cost more than that
// risk warrants. Callers must not treat a successful acquisition as proof of
// total exclusivity, only of no live interactive session.
//
// On success, call the returned release before the caller's own process
// exits; the lock is also released automatically if the process dies without
// calling it, so a crash mid-maintenance never wedges the workspace for
// future hub owners.
func TryAcquireMaintenanceLock(storeDir string) (release func(), ok bool) {
	lock, acquired := tryAcquireLock(lockFilePath(storeDir))
	if !acquired {
		return nil, false
	}
	return func() { _ = lock.Unlock() }, true
}
