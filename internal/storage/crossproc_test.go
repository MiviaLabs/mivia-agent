package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// This file proves two invariants hold across a real OS process boundary, not
// just within one Go test binary. modernc.org/sqlite (this repo's driver)
// locks through real fcntl(2)/flock(2) syscalls on the file descriptor, not a
// Go-level mutex - confirmed by reading its transpiled os_unix.c syscall
// table before writing this test. POSIX advisory locks carry a well-known
// gotcha: closing ANY file descriptor a process holds on a given inode can
// release ALL fcntl locks that process holds on it, keyed on (process,
// inode) rather than (fd, inode). Two *storage.SQLite handles opened in the
// SAME go test process (as sqlite_write_tx_test.go and
// worktree_fence_interleave_test.go both do) are therefore not proven
// equivalent to two separate OS processes sharing a file the way
// internal/hub's sibling mivia processes really do - only a genuine second
// process closes that gap.
//
// Synchronization uses marker files under a shared directory rather than
// channels, which cannot cross a process boundary. Each scenario is driven
// by a TestMain branch, following the crash-simulation pattern already
// established in validation_test.go (MIVIA_STORAGE_UNCOMMITTED_CHILD /
// MIVIA_STORAGE_COMMITTED_CHILD) rather than introducing a second technique.

const (
	crossprocMarkerTimeout = 10 * time.Second
	crossprocMarkerPoll    = 10 * time.Millisecond
)

// writeMarker atomically creates path with content b. A rename-into-place
// (write to a temp file, then rename) avoids a reader observing a
// partially-written marker.
func writeMarker(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// waitForMarker polls for path to exist and returns its contents, or an
// error once timeout elapses. Used instead of a fixed sleep: the marker
// itself is the correctness signal, and busy_timeout (5s, see
// pragmaDSNParams) is the real synchronization once both sides are racing
// the same SQLite lock - the timeout here only bounds how long a genuinely
// stuck test waits before failing loudly.
func waitForMarker(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		if b, err := os.ReadFile(path); err == nil {
			return b, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("marker %s did not appear within %s", path, timeout)
		}
		time.Sleep(crossprocMarkerPoll)
	}
}

// runCrossProcHelper re-execs the test binary with the given scenario env var
// set, pointing MIVIA_STORAGE_CHILD_DB and MIVIA_STORAGE_MARKER_DIR at dbPath
// and markerDir. Returns the started (not yet waited) command.
func runCrossProcHelper(t *testing.T, scenarioEnv, dbPath, markerDir string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), scenarioEnv+"=1",
		"MIVIA_STORAGE_CHILD_DB="+dbPath,
		"MIVIA_STORAGE_MARKER_DIR="+markerDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return cmd
}

// TestCrossProcessBeginWriteSurvivesConcurrentCommit is the cross-process
// port of TestBeginWriteSurvivesConcurrentCommit (sqlite_write_tx_test.go).
// A real child process opens its own store on the same file and appends an
// event while this process holds an open beginWrite transaction that has
// already read but not yet written - under BEGIN IMMEDIATE the child must
// block on the write lock until this process commits, matching the
// same-process test, but now proven across the boundary that fix actually
// targets (internal/hub's sibling processes).
func TestCrossProcessBeginWriteSurvivesConcurrentCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real OS process; skipped under -short")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "context.db")
	markerDir := t.TempDir()

	first, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx := context.Background()
	tx, err := first.beginWrite(ctx)
	if err != nil {
		t.Fatalf("beginWrite: %v", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read inside write transaction: %v", err)
	}

	cmd := runCrossProcHelper(t, "MIVIA_STORAGE_CROSSPROC_WRITER", path, markerDir)
	startedMarker := filepath.Join(markerDir, "child-started")
	if _, err := waitForMarker(startedMarker, crossprocMarkerTimeout); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}

	// The child has launched its Append call. Under BEGIN IMMEDIATE it must
	// still be blocked on the write lock this process holds - if it finished
	// already, the fix regressed.
	doneMarker := filepath.Join(markerDir, "child-done")
	if _, err := os.Stat(doneMarker); err == nil {
		_ = tx.Rollback()
		t.Fatal("child process completed its write while a write transaction was open in this process - the write lock was not taken cross-process")
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO events(id,run_id,sequence,kind,payload) VALUES(?,?,?,?,?)`,
		"ev-parent", "run-parent", 1, "k", []byte(`{"a":1}`)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("write after read lost the race: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after spawning the child: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("child process failed: %v", err)
	}
	if _, err := waitForMarker(doneMarker, crossprocMarkerTimeout); err != nil {
		t.Fatal(err)
	}

	second, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	total, err := second.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("events = %d, want 2 (both processes' commits durable)", total)
	}
}

// runCrossProcWriterChild is the child side of
// TestCrossProcessBeginWriteSurvivesConcurrentCommit, run from TestMain.
func runCrossProcWriterChild() {
	path := os.Getenv("MIVIA_STORAGE_CHILD_DB")
	markerDir := os.Getenv("MIVIA_STORAGE_MARKER_DIR")
	store, err := OpenSQLite(path)
	if err != nil {
		os.Exit(20)
	}
	defer store.Close()
	if err := writeMarker(filepath.Join(markerDir, "child-started"), []byte("1")); err != nil {
		os.Exit(21)
	}
	// If the parent's write transaction has already committed, this returns
	// almost immediately; if not, it blocks on busy_timeout until it does -
	// exactly the behaviour under test.
	if err := store.Append(context.Background(), Event{ID: "ev-child", RunID: "run-child", Sequence: 1, Kind: "k", Payload: []byte(`{"a":1}`)}); err != nil {
		os.Exit(22)
	}
	if err := writeMarker(filepath.Join(markerDir, "child-done"), []byte("1")); err != nil {
		os.Exit(23)
	}
	os.Exit(0)
}

// TestCrossProcessWorktreeFenceInterleavingSaveSession is the cross-process
// port of the SaveSession case in worktree_fence_interleave_test.go - the
// highest-risk scenario per the review that scoped this test, because the
// fence deliberately relies on the DEFERRED pool's SQLITE_BUSY_SNAPSHOT
// upgrade failure (see beginWrite's doc comment on why that path was left
// deferred) rather than BEGIN IMMEDIATE. A child process holds the stale
// mutation open across the pause hook; this process performs the concurrent
// deletion. The stale write must fail atomically with nothing landed, and a
// fresh retry must return ErrWorktreeDeleted - identical to the same-process
// assertion, now proven across a real process boundary.
func TestCrossProcessWorktreeFenceInterleavingSaveSession(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real OS process; skipped under -short")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "context.db")
	markerDir := t.TempDir()

	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}

	seed, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := seed.BeginWorktreeCreation(ctx, principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := seed.RegisterWorktreeInstance(ctx, principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := seed.SaveSession(ctx, principal, "snap", []byte(`[{"role":"user","content":"original"}]`), "model", "provider", 1, 1, 1,
		contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance}); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	cmd := runCrossProcHelper(t, "MIVIA_STORAGE_CROSSPROC_FENCE_STALE", path, markerDir)
	pausedMarker := filepath.Join(markerDir, "child-paused")
	if _, err := waitForMarker(pausedMarker, crossprocMarkerTimeout); err != nil {
		t.Fatal(err)
	}

	deleter, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer deleter.Close()
	if err := deleter.BeginWorktreeDeletion(ctx, principal, instance); err != nil {
		t.Fatalf("BeginWorktreeDeletion: %v", err)
	}
	if err := writeMarker(filepath.Join(markerDir, "release"), []byte("1")); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("child process failed: %v", err)
	}
	firstResult, err := waitForMarker(filepath.Join(markerDir, "first-result"), crossprocMarkerTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstResult) == "NIL" {
		t.Fatal("stale in-flight SaveSession succeeded across the process boundary")
	}
	retryResult, err := waitForMarker(filepath.Join(markerDir, "retry-result"), crossprocMarkerTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if string(retryResult) != "ERR_WORKTREE_DELETED" {
		t.Fatalf("retry result = %q, want ERR_WORKTREE_DELETED", retryResult)
	}

	var messages []byte
	if err := deleter.db.QueryRow(`SELECT messages FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`,
		principal.WorkspaceID, principal.SubjectID, instance.ID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if string(messages) != `[{"role":"user","content":"original"}]` {
		t.Fatalf("snapshot overwritten by the cross-process stale write: %s", messages)
	}
}

// runCrossProcFenceStaleChild is the child side of
// TestCrossProcessWorktreeFenceInterleavingSaveSession, run from TestMain.
func runCrossProcFenceStaleChild() {
	path := os.Getenv("MIVIA_STORAGE_CHILD_DB")
	markerDir := os.Getenv("MIVIA_STORAGE_MARKER_DIR")
	store, err := OpenSQLite(path)
	if err != nil {
		os.Exit(30)
	}
	defer store.Close()

	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		os.Exit(31)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}

	pauseAfterWorktreeFenceCheck = func() {
		if err := writeMarker(filepath.Join(markerDir, "child-paused"), []byte("1")); err != nil {
			os.Exit(32)
		}
		if _, err := waitForMarker(filepath.Join(markerDir, "release"), crossprocMarkerTimeout); err != nil {
			os.Exit(33)
		}
	}

	attempt := func() string {
		err := store.SaveSession(context.Background(), principal, "snap", []byte(`[{"role":"user","content":"stale-overwrite"}]`), "model", "provider", 1, 1, 1,
			contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance})
		switch {
		case err == nil:
			return "NIL"
		case errors.Is(err, contextstate.ErrWorktreeDeleted):
			return "ERR_WORKTREE_DELETED"
		default:
			return "OTHER:" + err.Error()
		}
	}

	first := attempt()
	if err := writeMarker(filepath.Join(markerDir, "first-result"), []byte(first)); err != nil {
		os.Exit(34)
	}
	// pauseAfterWorktreeFenceCheck already fired once (sync.Once semantics
	// are not needed here - production code has none; the hook simply never
	// fires again because the retry hits ErrWorktreeDeleted before reaching
	// the same in-transaction check point on a durably deleted instance).
	retry := attempt()
	if err := writeMarker(filepath.Join(markerDir, "retry-result"), []byte(retry)); err != nil {
		os.Exit(35)
	}
	os.Exit(0)
}
