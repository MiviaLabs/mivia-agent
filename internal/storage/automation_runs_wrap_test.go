package storage

// Error wraps on the automation run writers. These fire when the table a
// write targets is gone underneath an open handle - the shape a corrupted
// or partially-migrated store takes - and the wrap must name the run,
// because the operator's next move is to look that run up.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecAutomationRunUpdateWrapsAWriteFailure covers the shared update
// error wrap that every conditional automation-run write funnels through.
// A bare driver error here would tell the operator a write failed without
// saying which run it was.
func TestExecAutomationRunUpdateWrapsAWriteFailure(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`DROP TABLE automation_runs`); err != nil {
		t.Fatalf("drop automation_runs: %v", err)
	}

	changed, err := s.InterruptRunningAutomationRun(ctx, "run-gone", "2026-01-01T00:00:00Z", "interrupted")
	if err == nil {
		t.Fatal("InterruptRunningAutomationRun against a dropped table succeeded, want an error")
	}
	if changed {
		t.Fatal("a failed update reported changed=true")
	}
	if !strings.Contains(err.Error(), "run-gone") {
		t.Fatalf("error = %v, want it to name the run id", err)
	}
	if !strings.Contains(err.Error(), "update automation run") {
		t.Fatalf("error = %v, want the shared update wrap", err)
	}
}

// TestFencedRunUpdateWrapsAWriteFailure drives the same wrap through the
// claim-fenced writer, which is the one the executor calls on every
// checkpoint.
func TestFencedRunUpdateWrapsAWriteFailure(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`DROP TABLE automation_runs`); err != nil {
		t.Fatalf("drop automation_runs: %v", err)
	}

	endedAt := "2026-01-01T00:00:00Z"
	changed, err := s.UpdateAutomationRunStateFencedIfRunning(ctx, "fenced-run", "token", "failed", 2, &endedAt, "step", "boom")
	if err == nil {
		t.Fatal("UpdateAutomationRunStateFencedIfRunning against a dropped table succeeded, want an error")
	}
	if changed {
		t.Fatal("a failed fenced update reported changed=true")
	}
	if !strings.Contains(err.Error(), "fenced-run") {
		t.Fatalf("error = %v, want it to name the run id", err)
	}
}
