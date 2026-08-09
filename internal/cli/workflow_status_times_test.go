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
