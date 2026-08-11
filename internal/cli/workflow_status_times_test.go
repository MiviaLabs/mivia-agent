package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedAttemptTimingHistory persists the gated run under a deterministic clock:
// every stamp advances two seconds, so the completed attempt's started and
// finished timestamps and its two-second elapsed are exact in CLI output.
func seedAttemptTimingHistory(t *testing.T, storePath string, raw []byte, run workflowledger.RunSnapshot) {
	t.Helper()
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewStorageRepository(store)
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time {
		clock = clock.Add(2 * time.Second)
		return clock
	})
	ctx := context.Background()
	if err := repo.CreateRun(ctx, run, raw); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := parkGatedAgentStep(ctx, repo, run.RunID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := parkGatedHumanGate(ctx, repo, run.RunID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowStatusCommandAttemptTiming pins G6 for the operator view: the
// completed attempt line carries started, finished, and elapsed values that
// mirror the ledger, and the running attempt line carries started.
func TestWorkflowStatusCommandAttemptTiming(t *testing.T) {
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	seedAttemptTimingHistory(t, storePath, raw, run)
	var stdout strings.Builder
	if err := runWorkflowWithIO([]string{"status", run.RunID, "--workspace", root, "--config", configPath}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow status error = %v", err)
	}
	out := stdout.String()

	repo := openWorkflowTestStore(t, storePath)
	attempts, err := repo.ListStepAttempts(t.Context(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var completed *workflowledger.StepAttempt
	for i := range attempts {
		if attempts[i].StepID == "one" {
			completed = &attempts[i]
		}
	}
	if completed == nil || completed.FinishedAt == nil {
		t.Fatal("seeded completed attempt is missing")
	}
	want := fmt.Sprintf("  one #1 succeeded -> review started=%s finished=%s elapsed=%ds",
		completed.StartedAt.UTC().Format(time.RFC3339),
		completed.FinishedAt.UTC().Format(time.RFC3339),
		int64(completed.FinishedAt.Sub(completed.StartedAt).Seconds()))
	if !strings.Contains(out, want) {
		t.Fatalf("status output missing %q:\n%s", want, out)
	}
	if !strings.Contains(out, "  review #1 running started=") {
		t.Fatalf("status output missing started for the running attempt:\n%s", out)
	}
}

// TestWorkflowStatusCommandAttemptHeartbeat pins the liveness surface of the
// status report: a running attempt with a recorded heartbeat carries
// last_heartbeat=<RFC3339> plus a staleness note, and an attempt without one
// renders last_heartbeat=-.
func TestWorkflowStatusCommandAttemptHeartbeat(t *testing.T) {
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	seedAttemptTimingHistory(t, storePath, raw, run)

	// Record a heartbeat on the running review attempt, then close the store
	// so the status command reopens the ledger from disk.
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewStorageRepository(store)
	attempts, err := repo.ListStepAttempts(t.Context(), run.RunID)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	reviewID := ""
	for _, a := range attempts {
		if a.StepID == "review" {
			reviewID = a.AttemptID
		}
	}
	if reviewID == "" {
		_ = store.Close()
		t.Fatal("running review attempt is missing")
	}
	hb := time.Now().Add(-60 * time.Second)
	if err := repo.SetStepAttemptHeartbeat(t.Context(), run.RunID, reviewID, hb); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	if err := runWorkflowWithIO([]string{"status", run.RunID, "--workspace", root, "--config", configPath}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow status error = %v", err)
	}
	out := stdout.String()
	want := fmt.Sprintf("last_heartbeat=%s (60s ago)", hb.UTC().Format(time.RFC3339))
	if !strings.Contains(out, want) {
		t.Fatalf("status output missing %q:\n%s", want, out)
	}
	// The completed attempt recorded no heartbeat and must render the dash
	// placeholder on the same line.
	if !strings.Contains(out, "  one #1 succeeded -> review started=") {
		t.Fatalf("status output missing the completed attempt:\n%s", out)
	}
	if !strings.Contains(out, "last_heartbeat=-") {
		t.Fatalf("status output missing last_heartbeat=- for the no-heartbeat attempt:\n%s", out)
	}
}
