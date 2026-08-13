package hub

import "github.com/gofrs/flock"

// tryAcquireLock attempts (non-blocking) to become storeDir's hub owner.
// Returns the held lock (release via lock.Unlock()) and true on success; on
// failure - another process already owns it - returns a nil lock and false,
// never an error: "someone else has it" is the expected, common outcome,
// not a failure. The OS releases the lock automatically if this process
// dies without calling Unlock (including a crash), so a stale owner never
// wedges the workspace.
func tryAcquireLock(path string) (*flock.Flock, bool) {
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil || !ok {
		return nil, false
	}
	return lock, true
}
