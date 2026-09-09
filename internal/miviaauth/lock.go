package miviaauth

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// The refresh lock serializes the read-decide-write span around
// ~/.mivia/auth.json ACROSS PROCESSES.
//
// Why it has to be cross-process: the refresh token is one-time use. Two
// mivia processes sharing one session file both Load the same token, both
// POST /v1/auth/refresh inside the few hundred milliseconds of each other's
// round trip, and the server reads the loser's request as a reused token --
// which is its theft signal, so it revokes the whole session and logs BOTH
// processes out. That is not exotic: two `mivia chat` sessions read the same
// ExpiresAt and therefore enter the proactive refresh window together. An
// in-process mutex cannot see the other process, so it cannot help.
//
// Holding the lock across the whole span is what makes it work: the loser
// blocks, re-reads inside the lock, finds the winner's fresh token, and takes
// the fast path without refreshing at all.
//
// This is a concurrency guard, not an authorization gate. It must never be
// the reason authentication fails -- see the caller in service.go for which
// failures proceed unlocked and which refuse.

// lockBudget bounds how long to wait for another process to finish.
//
// Derived from the longest span anything holds the lock across a network
// call: ensureToken's single Refresh, bounded by requestTimeout (10s, applied
// by http.Client to the whole round trip), plus its Save. Logout deliberately
// does its revoke and retry OUTSIDE the lock, so its several round trips never
// enter this derivation. Doubling the one bounded request leaves room for a
// slow disk without waiting so long that a wedged holder looks like a hang.
const lockBudget = 2 * requestTimeout

// lockRetryDelay is how often to retry while another process holds the lock.
const lockRetryDelay = 20 * time.Millisecond

// processLocks serializes callers WITHIN this process, keyed by lock path.
//
// flock is per open-file-description, so two goroutines that each open the
// file would already exclude each other -- but they would do it by burning
// the whole lockBudget in a retry loop, and one of them would then take the
// busy path and report contention to a user who is only racing themselves.
// Taking the cheap in-process lock first turns that into a wait.
var processLocks sync.Map // map[string]*sync.Mutex

// lockResult reports how an attempt to take the refresh lock ended.
//
// fn RUNS for lockHeld and lockUnavailable, and does NOT run for lockBusy.
// That asymmetry is the whole contract: a caller may not skip its work just
// because no lock was available -- refusing to authenticate because a lock
// file could not be created is worse than the rare race the lock prevents --
// but it must not barge past a lock another process is actively holding.
type lockResult int

const (
	// lockHeld means the lock was acquired and fn ran under it.
	lockHeld lockResult = iota
	// lockUnavailable means no advisory lock was possible -- the file could
	// not be created, or the filesystem does not support locking. fn ran
	// anyway, unprotected.
	lockUnavailable
	// lockBusy means another process held the lock for the whole budget, and
	// fn DID NOT RUN. Proceeding as if unlocked would convert a rare race
	// into a certain one at the exact moment of contention.
	lockBusy
)

// acquireFunc is the shape Service depends on, so tests can drive every
// branch without build tags or a real wall-clock wait.
type acquireFunc func(lockPath string, fn func() error) (lockResult, error)

// withRefreshLock runs fn while holding lockPath exclusively.
//
// It opens its own handle per call and closes it before returning. Callers
// must not hand it a cached one: flock is scoped to the open file
// description, so two goroutines sharing a descriptor would both "acquire"
// the lock and silently void the guarantee.
//
// It is NOT reentrant. Ensure, Login, and Logout each take it and must never
// call one another. Whoami takes it twice in sequence, never nested.
func withRefreshLock(lockPath string, fn func() error) (lockResult, error) {
	unlockProcess := lockProcessLocal(lockPath)
	defer unlockProcess()

	// The session directory may not exist yet on a first run. Creating it
	// here rather than leaving the open to fail is what keeps the very first
	// login from silently running unlocked.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return lockUnavailable, fn()
	}

	lock := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), lockBudget)
	defer cancel()

	locked, err := lock.TryLockContext(ctx, lockRetryDelay)
	if err != nil {
		// TryLockContext reports the deadline as an error rather than a false
		// return, so a timeout and a genuinely broken lock file arrive the
		// same way. They are told apart by whether the budget is spent: the
		// context is done only in the timeout case.
		if ctx.Err() != nil {
			return lockBusy, nil
		}
		return lockUnavailable, fn()
	}
	if !locked {
		return lockBusy, nil
	}
	defer func() { _ = lock.Unlock() }()

	return lockHeld, fn()
}

// lockProcessLocal takes the in-process mutex for lockPath, resolved to an
// absolute path so two spellings of the same file are one key.
func lockProcessLocal(lockPath string) func() {
	key := lockPath
	if abs, err := filepath.Abs(lockPath); err == nil {
		key = abs
	}
	v, _ := processLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// lockPathFor names the lock file for an auth file.
//
// It is a SEPARATE file on purpose. Save installs the auth file by renaming a
// new inode over it, so a lock held on the auth file itself would be a lock
// on an inode that is no longer at that path -- protecting nothing. The lock
// file holds no data and is never deleted; deleting it would race with
// whoever is about to open it.
func lockPathFor(authPath string) string {
	return authPath + ".lock"
}
