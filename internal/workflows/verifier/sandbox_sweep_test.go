package verifier

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkAgedDir creates dir under root and backdates it by age.
func mkAgedDir(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(path, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A stranded root holds real bytes; prove the sweep removes contents too.
	if err := os.WriteFile(filepath.Join(path, "work", "payload"), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSweepStaleSandboxRootsRemovesAbandoned is the regression guard for the
// leak: runSandboxedCommand defers removal of its own root, but a deferred
// call never runs when the process is killed, so a SIGKILL or `timeout` kill
// during a workflow run stranded the root forever. On a tmpfs TMPDIR each
// stranded root held ~0.5 GB of RAM until removed by hand.
func TestSweepStaleSandboxRootsRemovesAbandoned(t *testing.T) {
	root := t.TempDir()
	stale := mkAgedDir(t, root, sandboxRootPrefix+"111", 24*time.Hour)
	fresh := mkAgedDir(t, root, sandboxRootPrefix+"222", time.Minute)
	foreign := mkAgedDir(t, root, "someone-elses-tmp", 24*time.Hour)

	sweepStaleSandboxRoots(root, time.Now(), staleSandboxAge)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale sandbox root survived the sweep (stat err = %v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("sweep removed a live sandbox root: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("sweep removed an unrelated directory: %v", err)
	}
}

// TestSweepStaleSandboxRootsIgnoresFiles pins that a regular file sharing the
// prefix is left alone: the sweep only ever removes directories it made.
func TestSweepStaleSandboxRootsIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, sandboxRootPrefix+"file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	sweepStaleSandboxRoots(root, time.Now(), staleSandboxAge)
	if _, err := os.Stat(file); err != nil {
		t.Errorf("sweep removed a regular file: %v", err)
	}
}

// TestSweepStaleSandboxRootsUnreadableDir pins the opportunistic contract: an
// unreadable sweep directory must return quietly, never panic or block the
// verification the caller actually asked for.
func TestSweepStaleSandboxRootsUnreadableDir(t *testing.T) {
	sweepStaleSandboxRoots(filepath.Join(t.TempDir(), "missing"), time.Now(), staleSandboxAge)
}
