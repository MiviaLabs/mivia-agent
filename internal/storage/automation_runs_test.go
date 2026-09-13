package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestAutomationRunsTableMigration covers Chunk 1's storage scope: the
// automation_runs table is created by OpenSQLite (D1's migration idiom -
// CREATE TABLE IF NOT EXISTS, same as run_claims/fenced_tokens above it in
// sqlite.go), and a row round-trips through insert/select with all
// columns named in docs/design/automations.md's D1: (id, automation_id,
// origin, state, step_index, step_count, session_name, worktree_path,
// worktree_branch, claim_token, started_at, ended_at, fail_kind, message).
func TestAutomationRunsTableMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='automation_runs'`).Scan(&name); err != nil {
		t.Fatalf("automation_runs table missing: %v", err)
	}

	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	ended := time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC).Format(time.RFC3339)

	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_runs
		(id, automation_id, origin, state, step_index, step_count, session_name, worktree_path, worktree_branch, claim_token, started_at, ended_at, fail_kind, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-1", "auto-1", "manual", "succeeded", 2, 3, "__auto__auto-1__run-1", "/tmp/wt", "auto-branch", "claim-token", started, ended, "", "all steps ok")
	if err != nil {
		t.Fatalf("insert automation_runs row: %v", err)
	}

	var (
		id, automationID, origin, state, sessionName, worktreePath string
		worktreeBranch, claimToken, gotStarted, failKind, message  string
		gotEnded                                                   *string
		stepIndex, stepCount                                       int
	)
	row := s.db.QueryRowContext(ctx, `SELECT id, automation_id, origin, state, step_index, step_count, session_name, worktree_path, worktree_branch, claim_token, started_at, ended_at, fail_kind, message FROM automation_runs WHERE id = ?`, "run-1")
	if err := row.Scan(&id, &automationID, &origin, &state, &stepIndex, &stepCount, &sessionName, &worktreePath, &worktreeBranch, &claimToken, &gotStarted, &gotEnded, &failKind, &message); err != nil {
		t.Fatalf("select automation_runs row: %v", err)
	}

	if id != "run-1" || automationID != "auto-1" || origin != "manual" || state != "succeeded" {
		t.Fatalf("row identity mismatch: id=%s automation_id=%s origin=%s state=%s", id, automationID, origin, state)
	}
	if stepIndex != 2 || stepCount != 3 {
		t.Fatalf("step_index/step_count = %d/%d, want 2/3", stepIndex, stepCount)
	}
	if sessionName != "__auto__auto-1__run-1" {
		t.Fatalf("session_name = %q, want __auto__auto-1__run-1", sessionName)
	}
	if worktreePath != "/tmp/wt" || worktreeBranch != "auto-branch" {
		t.Fatalf("worktree fields mismatch: path=%q branch=%q", worktreePath, worktreeBranch)
	}
	if claimToken != "claim-token" {
		t.Fatalf("claim_token = %q, want claim-token", claimToken)
	}
	if gotStarted != started {
		t.Fatalf("started_at = %q, want %q", gotStarted, started)
	}
	if gotEnded == nil || *gotEnded != ended {
		t.Fatalf("ended_at = %v, want %q", gotEnded, ended)
	}
	if failKind != "" {
		t.Fatalf("fail_kind = %q, want empty", failKind)
	}
	if message != "all steps ok" {
		t.Fatalf("message = %q, want %q", message, "all steps ok")
	}
}

// insertBareAutomationRun inserts a minimal automation_runs row for the
// UpdateAutomationRunSession/UpdateAutomationRunClaimToken tests below,
// factored out since those tests only care about one column changing.
func insertBareAutomationRun(t *testing.T, s *SQLite, id string) {
	t.Helper()
	ctx := context.Background()
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	err := s.InsertAutomationRun(ctx, AutomationRun{
		ID:           id,
		AutomationID: "auto-1",
		Origin:       "manual",
		State:        "pending",
		StartedAt:    started,
	})
	if err != nil {
		t.Fatalf("insert automation run: %v", err)
	}
}

// TestUpdateAutomationRunSessionPersists proves
// UpdateAutomationRunSession writes session_name for an existing row and
// leaves every other column untouched.
func TestUpdateAutomationRunSessionPersists(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-session")

	if err := s.UpdateAutomationRunSession(ctx, "run-session", "__auto__auto-1__run-session"); err != nil {
		t.Fatalf("UpdateAutomationRunSession: %v", err)
	}

	got, ok, err := s.GetAutomationRun(ctx, "run-session")
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if got.SessionName != "__auto__auto-1__run-session" {
		t.Fatalf("SessionName = %q, want __auto__auto-1__run-session", got.SessionName)
	}
}

// TestUpdateAutomationRunSessionUnknownRunReturnsNotFound proves
// UpdateAutomationRunSession returns ErrAutomationRunNotFound when id has
// no row, mirroring UpdateAutomationRunState's own not-found contract.
func TestUpdateAutomationRunSessionUnknownRunReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.UpdateAutomationRunSession(ctx, "no-such-run", "some-session")
	if !errors.Is(err, ErrAutomationRunNotFound) {
		t.Fatalf("UpdateAutomationRunSession on missing run = %v, want ErrAutomationRunNotFound", err)
	}
}

// TestUpdateAutomationRunClaimTokenPersists proves
// UpdateAutomationRunClaimToken writes claim_token for an existing row.
func TestUpdateAutomationRunClaimTokenPersists(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-claim")

	if err := s.UpdateAutomationRunClaimToken(ctx, "run-claim", "claim-token-xyz"); err != nil {
		t.Fatalf("UpdateAutomationRunClaimToken: %v", err)
	}

	got, ok, err := s.GetAutomationRun(ctx, "run-claim")
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if got.ClaimToken != "claim-token-xyz" {
		t.Fatalf("ClaimToken = %q, want claim-token-xyz", got.ClaimToken)
	}
}

// TestUpdateAutomationRunClaimTokenUnknownRunReturnsNotFound proves
// UpdateAutomationRunClaimToken returns ErrAutomationRunNotFound when id
// has no row.
func TestUpdateAutomationRunClaimTokenUnknownRunReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err = s.UpdateAutomationRunClaimToken(ctx, "no-such-run", "some-token")
	if !errors.Is(err, ErrAutomationRunNotFound) {
		t.Fatalf("UpdateAutomationRunClaimToken on missing run = %v, want ErrAutomationRunNotFound", err)
	}
}

// TestUpdateAutomationRunStateFencedStaleTokenLeavesRowUnchanged proves
// a fenced write with a token that no longer matches claim_token
// reports false and changes no column.
func TestUpdateAutomationRunStateFencedStaleTokenLeavesRowUnchanged(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-fenced")
	if err := s.UpdateAutomationRunClaimToken(ctx, "run-fenced", "token-new"); err != nil {
		t.Fatalf("UpdateAutomationRunClaimToken: %v", err)
	}
	ended := time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC).Format(time.RFC3339)
	ok, err := s.UpdateAutomationRunStateFenced(ctx, "run-fenced", "token-old", "failed", 3, &ended, "job_error", "stale writer")
	if err != nil {
		t.Fatalf("UpdateAutomationRunStateFenced: %v", err)
	}
	if ok {
		t.Fatal("stale token write reported ok=true, want false")
	}
	got, found, err := s.GetAutomationRun(ctx, "run-fenced")
	if err != nil || !found {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", found, err)
	}
	if got.State != "pending" || got.StepIndex != 0 || got.EndedAt != nil || got.FailKind != "" || got.Message != "" {
		t.Fatalf("row changed by a stale-token write: %+v", got)
	}
}

// TestUpdateAutomationRunStateFencedMatchingTokenWrites proves the
// fenced write moves every lifecycle column when the token matches.
func TestUpdateAutomationRunStateFencedMatchingTokenWrites(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-fenced-ok")
	if err := s.UpdateAutomationRunClaimToken(ctx, "run-fenced-ok", "token-live"); err != nil {
		t.Fatalf("UpdateAutomationRunClaimToken: %v", err)
	}
	ended := time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC).Format(time.RFC3339)
	ok, err := s.UpdateAutomationRunStateFenced(ctx, "run-fenced-ok", "token-live", "succeeded", 2, &ended, "", "done")
	if err != nil || !ok {
		t.Fatalf("UpdateAutomationRunStateFenced = (%v, %v), want (true, nil)", ok, err)
	}
	got, _, err := s.GetAutomationRun(ctx, "run-fenced-ok")
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if got.State != "succeeded" || got.StepIndex != 2 || got.EndedAt == nil || *got.EndedAt != ended || got.Message != "done" {
		t.Fatalf("row after fenced write = %+v", got)
	}
}

// TestUpdateAutomationRunStateFencedIfRunningSkipsNonRunningRow proves
// the checkpoint-only write leaves a row untouched once its state has
// already moved off running, even when the claim token still matches -
// the guard the mid-run checkpoint needs so it cannot resurrect a row
// InterruptRunningAutomationRun (or any other terminal write) already
// closed out.
func TestUpdateAutomationRunStateFencedIfRunningSkipsNonRunningRow(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-checkpoint-settled")
	if err := s.UpdateAutomationRunClaimToken(ctx, "run-checkpoint-settled", "token-live"); err != nil {
		t.Fatalf("UpdateAutomationRunClaimToken: %v", err)
	}
	if err := s.UpdateAutomationRunState(ctx, "run-checkpoint-settled", "running", 0, nil, "", ""); err != nil {
		t.Fatalf("UpdateAutomationRunState: %v", err)
	}
	interruptedAt := time.Date(2026, 9, 10, 12, 4, 0, 0, time.UTC).Format(time.RFC3339)
	ok, err := s.InterruptRunningAutomationRun(ctx, "run-checkpoint-settled", interruptedAt, "interrupted: shutdown")
	if err != nil || !ok {
		t.Fatalf("InterruptRunningAutomationRun = (%v, %v), want (true, nil)", ok, err)
	}

	checkpointOK, err := s.UpdateAutomationRunStateFencedIfRunning(ctx, "run-checkpoint-settled", "token-live", "running", 1, nil, "", "")
	if err != nil {
		t.Fatalf("UpdateAutomationRunStateFencedIfRunning: %v", err)
	}
	if checkpointOK {
		t.Fatal("checkpoint on an already-interrupted row reported ok=true, want false")
	}
	got, _, err := s.GetAutomationRun(ctx, "run-checkpoint-settled")
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if got.State != "interrupted" || got.StepIndex != 0 || got.EndedAt == nil || *got.EndedAt != interruptedAt {
		t.Fatalf("row resurrected by checkpoint write: %+v", got)
	}
}

// TestUpdateAutomationRunStateFencedIfRunningWritesWhileRunning proves
// the checkpoint-only write still lands normally while the row is
// running and the claim token matches - the common case, unaffected by
// the added state guard.
func TestUpdateAutomationRunStateFencedIfRunningWritesWhileRunning(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-checkpoint-live")
	if err := s.UpdateAutomationRunClaimToken(ctx, "run-checkpoint-live", "token-live"); err != nil {
		t.Fatalf("UpdateAutomationRunClaimToken: %v", err)
	}
	if err := s.UpdateAutomationRunState(ctx, "run-checkpoint-live", "running", 0, nil, "", ""); err != nil {
		t.Fatalf("UpdateAutomationRunState: %v", err)
	}

	ok, err := s.UpdateAutomationRunStateFencedIfRunning(ctx, "run-checkpoint-live", "token-live", "running", 1, nil, "", "")
	if err != nil || !ok {
		t.Fatalf("UpdateAutomationRunStateFencedIfRunning = (%v, %v), want (true, nil)", ok, err)
	}
	got, _, err := s.GetAutomationRun(ctx, "run-checkpoint-live")
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if got.State != "running" || got.StepIndex != 1 {
		t.Fatalf("row after checkpoint = %+v, want running at step 1", got)
	}
}

// TestInterruptRunningAutomationRunSkipsNonRunningRow proves the
// conditional interrupt touches only a row still in state running.
func TestInterruptRunningAutomationRunSkipsNonRunningRow(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-done")
	ended := time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC).Format(time.RFC3339)
	if err := s.UpdateAutomationRunState(ctx, "run-done", "succeeded", 1, &ended, "", ""); err != nil {
		t.Fatalf("UpdateAutomationRunState: %v", err)
	}
	later := time.Date(2026, 9, 10, 12, 9, 0, 0, time.UTC).Format(time.RFC3339)
	ok, err := s.InterruptRunningAutomationRun(ctx, "run-done", later, "interrupted: too late")
	if err != nil {
		t.Fatalf("InterruptRunningAutomationRun: %v", err)
	}
	if ok {
		t.Fatal("interrupt of a succeeded row reported ok=true, want false")
	}
	got, _, err := s.GetAutomationRun(ctx, "run-done")
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if got.State != "succeeded" || *got.EndedAt != ended || got.Message != "" {
		t.Fatalf("succeeded row changed by interrupt: %+v", got)
	}
}

// TestInterruptRunningAutomationRunMarksRunningRow proves the
// conditional interrupt marks a running row and keeps its step_index.
func TestInterruptRunningAutomationRunMarksRunningRow(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	insertBareAutomationRun(t, s, "run-live")
	if err := s.UpdateAutomationRunState(ctx, "run-live", "running", 2, nil, "", ""); err != nil {
		t.Fatalf("UpdateAutomationRunState: %v", err)
	}
	ended := time.Date(2026, 9, 10, 12, 9, 0, 0, time.UTC).Format(time.RFC3339)
	ok, err := s.InterruptRunningAutomationRun(ctx, "run-live", ended, "interrupted: service closed")
	if err != nil || !ok {
		t.Fatalf("InterruptRunningAutomationRun = (%v, %v), want (true, nil)", ok, err)
	}
	got, _, err := s.GetAutomationRun(ctx, "run-live")
	if err != nil {
		t.Fatalf("GetAutomationRun: %v", err)
	}
	if got.State != "interrupted" || got.StepIndex != 2 || got.EndedAt == nil || *got.EndedAt != ended || got.Message != "interrupted: service closed" {
		t.Fatalf("row after interrupt = %+v", got)
	}
}
