package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The interleaving tests implement plan 57 test #9 with deterministic
// check-then-act hooks. A stale mutation pauses inside its transaction after
// the in-transaction active check, a concurrent deletion commits, and the
// stale write must fail atomically with no rows changed; a fresh retry then
// returns ErrWorktreeDeleted. The tests depend on WAL snapshot isolation (the
// shipped DSN sets journal_mode=WAL): the stale write-upgrade fails with
// SQLITE_BUSY_SNAPSHOT instead of blocking or succeeding. Moving the store off
// WAL changes that failure mode and these tests need revisiting. The tests
// assert outcomes only (the mutation fails, nothing lands, the retry yields
// the durable sentinel), never the driver-specific error text.

type fenceInterleaveCase struct {
	name   string
	seed   func(*testing.T, *SQLite, contextstate.Principal, contextstate.WorktreeInstance, contextstate.BindingRevision)
	stale  func(*SQLite, contextstate.Principal, contextstate.WorktreeInstance, contextstate.BindingRevision) error
	verify func(*testing.T, *SQLite, contextstate.Principal, contextstate.WorktreeInstance)
}

func TestWorktreeFenceInterleavingEnsureSession(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "EnsureSession",
		seed: func(*testing.T, *SQLite, contextstate.Principal, contextstate.WorktreeInstance, contextstate.BindingRevision) {
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, binding contextstate.BindingRevision) error {
			return store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: principal, Binding: binding, WorktreeInstance: instance})
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, _ contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM context_sessions WHERE workspace_id=? AND session_id=?`, 0, p.WorkspaceID, p.SessionID)
		},
	})
}

func TestWorktreeFenceInterleavingSaveSession(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "SaveSession",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, _ contextstate.BindingRevision) {
			t.Helper()
			if err := store.SaveSession(context.Background(), p, "snap", []byte(`[{"role":"user","content":"original"}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: i.Worktree, WorktreeInstance: i}); err != nil {
				t.Fatal(err)
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, _ contextstate.BindingRevision) error {
			return store.SaveSession(context.Background(), principal, "snap", []byte(`[{"role":"user","content":"stale-overwrite"}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: instance.Worktree, WorktreeInstance: instance})
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance) {
			t.Helper()
			var messages []byte
			if err := store.db.QueryRow(`SELECT messages FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, p.WorkspaceID, p.SubjectID, i.ID).Scan(&messages); err != nil {
				t.Fatal(err)
			}
			if string(messages) != `[{"role":"user","content":"original"}]` {
				t.Fatalf("snapshot overwritten by stale write: %s", messages)
			}
		},
	})
}

func TestWorktreeFenceInterleavingCommit(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "Commit",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, binding contextstate.BindingRevision) {
			t.Helper()
			if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: p, Binding: binding, WorktreeInstance: i}); err != nil {
				t.Fatal(err)
			}
			commit, err := interleaveCommitRequest(p, i, contextstate.Revision{}, binding, "commit-1", "first")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Commit(context.Background(), commit); err != nil {
				t.Fatal(err)
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, binding contextstate.BindingRevision) error {
			commit, err := interleaveCommitRequest(principal, instance, contextstate.Revision{Session: 1, Durable: 1, Source: 1}, binding, "commit-2", "second")
			if err != nil {
				return err
			}
			return store.Commit(context.Background(), commit)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, _ contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM context_checkpoints WHERE session_id=?`, 1, p.SessionID)
		},
	})
}

func TestWorktreeFenceInterleavingAdvance(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "Advance",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, binding contextstate.BindingRevision) {
			t.Helper()
			if err := store.EnsureSession(context.Background(), contextstate.EnsureSessionRequest{Principal: p, Binding: binding, WorktreeInstance: i}); err != nil {
				t.Fatal(err)
			}
			commit, err := interleaveCommitRequest(p, i, contextstate.Revision{}, binding, "commit-1", "first")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Commit(context.Background(), commit); err != nil {
				t.Fatal(err)
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, binding contextstate.BindingRevision) error {
			advance := contextstate.AdvanceRequest{
				OperationID: "advance-stale", Principal: principal, SessionID: principal.SessionID,
				Expected: contextstate.Revision{Session: 1, Durable: 1, Source: 1}, ExpectedBinding: binding,
				NewSession: 2, NewDurable: 2, NewSourceSequence: 1, NewBinding: binding,
				ClearActive: true, Reason: "stale advance", WorktreeInstance: instance,
			}
			return store.Advance(context.Background(), advance)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, _ contextstate.WorktreeInstance) {
			t.Helper()
			var revision int
			if err := store.db.QueryRow(`SELECT session_revision FROM context_sessions WHERE workspace_id=? AND session_id=?`, p.WorkspaceID, p.SessionID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			if revision != 1 {
				t.Fatalf("stale advance moved revision to %d", revision)
			}
		},
	})
}

func TestWorktreeFenceInterleavingSaveWorktreeSessionAdmission(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "SaveWorktreeSessionAdmission",
		seed: func(*testing.T, *SQLite, contextstate.Principal, contextstate.WorktreeInstance, contextstate.BindingRevision) {
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, _ contextstate.BindingRevision) error {
			return store.SaveWorktreeSessionAdmission(context.Background(), principal, "admission", contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}, instance)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, 0, p.WorkspaceID, p.SubjectID, i.ID)
		},
	})
}

func TestWorktreeFenceInterleavingDeleteWorktreeSessionAdmission(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "DeleteWorktreeSessionAdmission",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, _ contextstate.BindingRevision) {
			t.Helper()
			if err := store.SaveWorktreeSessionAdmission(context.Background(), p, "admission", contextstate.SessionAdmission{Agent: "agent", Digest: "digest", Names: []string{"tool"}}, i); err != nil {
				t.Fatal(err)
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, _ contextstate.BindingRevision) error {
			return store.SaveWorktreeSessionAdmission(context.Background(), principal, "admission", contextstate.SessionAdmission{}, instance)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM chat_session_admissions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, 1, p.WorkspaceID, p.SubjectID, i.ID)
		},
	})
}

func TestWorktreeFenceInterleavingDeleteWorktreeSessionSnapshot(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "DeleteWorktreeSessionSnapshot",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, _ contextstate.BindingRevision) {
			t.Helper()
			if err := store.SaveSession(context.Background(), p, "snap", []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: i.Worktree, WorktreeInstance: i}); err != nil {
				t.Fatal(err)
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, _ contextstate.BindingRevision) error {
			return store.DeleteWorktreeSessionSnapshot(context.Background(), principal, "snap", instance)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, 1, p.WorkspaceID, p.SubjectID, i.ID)
		},
	})
}

func TestWorktreeFenceInterleavingPruneWorktreeSessionSnapshots(t *testing.T) {
	runWorktreeFenceInterleaving(t, fenceInterleaveCase{
		name: "PruneWorktreeSessionSnapshots",
		seed: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance, _ contextstate.BindingRevision) {
			t.Helper()
			for _, name := range []string{"snap-a", "snap-b"} {
				if err := store.SaveSession(context.Background(), p, name, []byte(`[{}]`), "model", "provider", 1, 1, 1, contextstate.SessionSaveOptions{Worktree: i.Worktree, WorktreeInstance: i}); err != nil {
					t.Fatal(err)
				}
			}
		},
		stale: func(store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance, _ contextstate.BindingRevision) error {
			return store.PruneWorktreeSessionSnapshots(context.Background(), principal, []string{"snap-a", "snap-b"}, instance)
		},
		verify: func(t *testing.T, store *SQLite, p contextstate.Principal, i contextstate.WorktreeInstance) {
			t.Helper()
			assertTableRowCount(t, store, `SELECT count(*) FROM chat_sessions WHERE workspace_id=? AND subject_id=? AND instance_id=?`, 2, p.WorkspaceID, p.SubjectID, i.ID)
		},
	})
}

// runWorktreeFenceInterleaving drives one fence interleaving case: seed an
// active instance plus the case rows, arm the one-shot pause hook, run the
// stale mutation in a goroutine so it pauses inside its transaction after the
// active check, commit a concurrent deletion, release the hook, and assert the
// stale write failed, nothing landed, and a fresh retry returns
// ErrWorktreeDeleted.
func runWorktreeFenceInterleaving(t *testing.T, tc fenceInterleaveCase) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	instance := contextstate.WorktreeInstance{Worktree: "wt-a", ID: "wt_1234567890abcdef"}
	binding := interleaveBinding()
	dir := t.TempDir()
	stale, err := OpenSQLite(filepath.Join(dir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	deleter, err := OpenSQLite(filepath.Join(dir, "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer deleter.Close()
	seedActiveWorktreeInstance(t, stale, principal, instance)
	tc.seed(t, stale, principal, instance, binding)
	// Arm only after seeding: the hook must fire on the stale mutation's
	// in-transaction check, never on a seeding call.
	fired, release := armFencePause(t)
	// The paused goroutine holds stale.writeMu and an open read
	// transaction. While it is paused, do not call any writeMu-taking
	// method on stale.
	result := make(chan error, 1)
	go func() {
		result <- tc.stale(stale, principal, instance, binding)
	}()
	select {
	case <-fired:
	case <-time.After(30 * time.Second):
		t.Fatal("stale mutation did not reach the fence check")
	}
	if err := deleter.BeginWorktreeDeletion(context.Background(), principal, instance); err != nil {
		t.Fatalf("BeginWorktreeDeletion: %v", err)
	}
	close(release)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("stale in-flight mutation succeeded")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stale mutation did not complete after release")
	}
	tc.verify(t, stale, principal, instance)
	// The durable fence: a fresh attempt returns ErrWorktreeDeleted.
	if err := tc.stale(stale, principal, instance, binding); !errors.Is(err, contextstate.ErrWorktreeDeleted) {
		t.Fatalf("retry after deletion = %v, want ErrWorktreeDeleted", err)
	}
}

func seedActiveWorktreeInstance(t *testing.T, store *SQLite, principal contextstate.Principal, instance contextstate.WorktreeInstance) {
	t.Helper()
	worktreeDir := filepath.Join(t.TempDir(), "worktrees", instance.Worktree)
	if err := store.BeginWorktreeCreation(context.Background(), principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorktreeInstance(context.Background(), principal, instance, worktreeDir); err != nil {
		t.Fatal(err)
	}
}

// armFencePause arms the deterministic one-shot pause hook. The hook fires on
// the next requireActiveWorktreeTx active check, blocks until release, and
// never fires again. The cleanup closes release, so a failed assertion cannot
// strand the paused goroutine holding its store's writeMu.
func armFencePause(t *testing.T) (fired, release chan struct{}) {
	t.Helper()
	fired = make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	pauseAfterWorktreeFenceCheck = func() {
		once.Do(func() {
			close(fired)
			<-release
		})
	}
	t.Cleanup(func() {
		pauseAfterWorktreeFenceCheck = func() {}
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return fired, release
}

func assertTableRowCount(t *testing.T, store *SQLite, query string, want int, args ...any) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s = %d, want %d", query, count, want)
	}
}

// interleaveBinding returns a binding without a testing.T so a stale mutation
// goroutine can build requests without calling test methods off the test
// goroutine.
func interleaveBinding() contextstate.BindingRevision {
	binding, _ := contextstate.NewBindingRevision("provider", "model", 1)
	return binding
}

// interleaveCommitRequest mirrors contextCommitRequest without a testing.T and
// binds the request to the worktree instance before the fingerprint is set.
func interleaveCommitRequest(p contextstate.Principal, i contextstate.WorktreeInstance, expected contextstate.Revision, binding contextstate.BindingRevision, key, state string) (contextstate.CommitRequest, error) {
	sequence := expected.Source + 1
	sourceID, err := contextstate.NewSourceID(p.SessionID, sequence)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	rng, err := contextstate.NewSourceRange(sourceID, sourceID)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	checkpointID, err := contextstate.NewCheckpointID(p.SessionID, rng, "context-compact-v1", 1, binding.Model, key)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	active := []byte(`{"state":"` + state + `"}`)
	checkpoint := contextstate.CheckpointRecord{
		ID: checkpointID, Revision: contextstate.Revision{Session: expected.Session + 1, Durable: expected.Durable + 1, Source: sequence},
		Binding: binding, SourceRange: rng, ActiveContext: active, SummaryMetadata: []byte(`{"version":1}`), TurnID: expected.Session + 1,
	}
	event := contextstate.SourceEvent{ID: sourceID, Kind: "message", Role: "user", Provenance: "test", RedactionStatus: "metadata", Size: len(state)}
	req, err := contextstate.NewCommitRequest(p, p.SessionID, expected, binding, []contextstate.SourceEvent{event}, checkpoint, active, binding, expected.Session+1)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	req.WorktreeInstance = i
	// The fingerprint covers the whole request, so bind the instance before
	// recomputing it.
	req.Fingerprint, err = contextstate.FingerprintCommitRequest(req)
	if err != nil {
		return contextstate.CommitRequest{}, err
	}
	return req, req.Validate()
}
