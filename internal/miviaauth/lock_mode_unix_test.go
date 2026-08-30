//go:build unix

package miviaauth

import (
	"os"
	"testing"
)

// TestLockFileModeIs0600 keeps the lock file as private as the credential it
// sits beside in ~/.mivia/. Unix-only: Windows has no comparable mode bits,
// and a t.Skip there would need a test-skips policy entry for a check that
// simply does not apply.
func TestLockFileModeIs0600(t *testing.T) {
	svc, path := newTestService(t, &fakeSessionClient{})
	mustSave(t, path, farFutureToken())

	if _, err := svc.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(svc.lockPath())
	if err != nil {
		t.Fatalf("stat the lock file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("lock file mode = %o, want 600", got)
	}
}
