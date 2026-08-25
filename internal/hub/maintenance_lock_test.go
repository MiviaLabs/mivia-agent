package hub

import (
	"path/filepath"
	"testing"
)

// TestTryAcquireMaintenanceLockSharesTheHubLockPath proves maintenance and
// ordinary hub ownership contend for the identical file: acquiring the hub
// owner lock directly must block a maintenance acquisition, and vice versa.
func TestTryAcquireMaintenanceLockSharesTheHubLockPath(t *testing.T) {
	dir := t.TempDir()
	ownerLock, ok := tryAcquireLock(lockFilePath(dir))
	if !ok {
		t.Fatal("tryAcquireLock: expected to acquire the free hub lock")
	}
	defer func() { _ = ownerLock.Unlock() }()

	if _, ok := TryAcquireMaintenanceLock(dir); ok {
		t.Fatal("TryAcquireMaintenanceLock succeeded while the hub owner lock was held")
	}
}

// TestTryAcquireMaintenanceLockSucceedsWhenFree covers the common path and
// proves release actually frees the lock for a later acquisition.
func TestTryAcquireMaintenanceLockSucceedsWhenFree(t *testing.T) {
	dir := t.TempDir()
	release, ok := TryAcquireMaintenanceLock(dir)
	if !ok {
		t.Fatal("TryAcquireMaintenanceLock: expected to acquire a free lock")
	}
	if release == nil {
		t.Fatal("TryAcquireMaintenanceLock: release func is nil despite ok=true")
	}
	if _, ok := tryAcquireLock(lockFilePath(dir)); ok {
		t.Fatal("hub owner lock acquired while maintenance holds it")
	}
	release()
	if lock, ok := tryAcquireLock(lockFilePath(dir)); !ok {
		t.Fatal("hub owner lock still unavailable after maintenance release")
	} else {
		_ = lock.Unlock()
	}
}

// TestTryAcquireMaintenanceLockUsesStoreDir proves the lock file actually
// lands beside the given directory, not some other well-known path.
func TestTryAcquireMaintenanceLockUsesStoreDir(t *testing.T) {
	dir := t.TempDir()
	release, ok := TryAcquireMaintenanceLock(dir)
	if !ok {
		t.Fatal("TryAcquireMaintenanceLock: expected success")
	}
	defer release()
	want := filepath.Join(dir, "hub.lock")
	if _, err := filepath.Abs(want); err != nil {
		t.Fatal(err)
	}
	if got := lockFilePath(dir); got != want {
		t.Fatalf("lockFilePath(%q) = %q, want %q", dir, got, want)
	}
}
