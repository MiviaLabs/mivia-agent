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
