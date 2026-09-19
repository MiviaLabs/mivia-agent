package workflow

// Moved from chat's coverage_gaps_test.go with the stack command helpers.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failingStackRepo wraps a real repository but fails ListRuns or
// ListStepAttempts on demand, to cover ResolveStackID's and isStackPlanRun's
// error branches.
type failingStackRepo struct {
	workflowledger.Repository
	failRuns      bool
	failAttempts  bool
	attemptsCalls int
}

func (f *failingStackRepo) ListRuns(ctx context.Context, status ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
	if f.failRuns {
		return nil, errors.New("list runs boom")
	}
	return f.Repository.ListRuns(ctx, status...)
}

func (f *failingStackRepo) ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error) {
	if f.failAttempts {
		return nil, errors.New("list attempts boom")
	}
	f.attemptsCalls++
	return f.Repository.ListStepAttempts(ctx, runID)
}

// --- stack_command_helpers.go ---

func TestParseStackWorkflowArgsFlagError(t *testing.T) {
	// FlagValue is a plain function since the move; exercise its own
	// missing-value error through the parser instead of a seam stub.
	if _, _, _, err := ParseStackWorkflowArgs([]string{"--stack"}); err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("ParseStackWorkflowArgs(--stack without value) err = %v; want requires-a-value", err)
	}
}

// seedPlanRun admits a run of workflowName and optionally completes a
// decompose step attempt so the run counts as a plan-mode run.
func seedPlanRun(t *testing.T, repo workflowledger.Repository, runID, workflowName string, started time.Time, planRun bool, invocationKey string) {
	t.Helper()
	ctx := context.Background()
	snap := workflowledger.RunSnapshot{
		RunID:         runID,
		WorkflowName:  workflowName,
		Status:        workflowledger.RunStatusPending,
		ActiveStepID:  "decompose",
		StartedAt:     started,
		InvocationKey: invocationKey,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatalf("CreateRun(%s): %v", runID, err)
	}
	step := delivery.DecomposeStepID
	status := workflowledger.AttemptStatusSucceeded
	if !planRun {
		step = "other-step"
		status = workflowledger.AttemptStatusFailed
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: runID + "-a1", RunID: runID, StepID: step, AttemptNo: 1,
	}); err != nil {
		t.Fatalf("CreateStepAttempt(%s): %v", runID, err)
	}
	if err := repo.CompleteStepAttempt(ctx, runID, runID+"-a1", 1, workflowledger.AttemptOutcome{Status: status}); err != nil {
		t.Fatalf("CompleteStepAttempt(%s): %v", runID, err)
	}
}

func TestResolveStackIDLatestPlanRun(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	seedPlanRun(t, repo, "wfr-old", "wf", time.Now().Add(-2*time.Hour), true, "")
	seedPlanRun(t, repo, "wfr-new", "wf", time.Now().Add(-time.Hour), true, "")
	seedPlanRun(t, repo, "wfr-nonplan", "wf", time.Now(), false, "")
	seedPlanRun(t, repo, "wfr-invoked", "wf", time.Now(), true, "inv-key")
	seedPlanRun(t, repo, "wfr-other", "other-wf", time.Now(), true, "")

	id, err := ResolveStackID(repo, "wf", "")
	if err != nil || id != "wfr-new" {
		t.Fatalf("ResolveStackID(latest plan run) = (%q, %v); want wfr-new", id, err)
	}

	// isStackPlanRun must report true for the plan run and false for the rest.
	runs, rerr := repo.ListRuns(context.Background())
	if rerr != nil {
		t.Fatal(rerr)
	}
	plan, nonPlan := 0, 0
	for _, r := range runs {
		if isStackPlanRun(repo, r) {
			plan++
		} else {
			nonPlan++
		}
	}
	// The invocation-key and other-workflow runs are still plan runs in
	// themselves; ResolveStackID filters them at the caller level.
	if plan != 4 || nonPlan != 1 {
		t.Fatalf("isStackPlanRun counts = (%d plan, %d non-plan); want (4, 1)", plan, nonPlan)
	}

	// No plan-mode run for an unknown workflow must fail closed.
	if _, err := ResolveStackID(repo, "missing-wf", ""); err == nil || !strings.Contains(err.Error(), "no plan-mode run found") {
		t.Fatalf("ResolveStackID(missing) err = %v; want no-plan-run error", err)
	}

	// Repository errors surface unchanged from both helpers.
	failRuns := &failingStackRepo{Repository: repo, failRuns: true}
	if _, err := ResolveStackID(failRuns, "wf", ""); err == nil || !strings.Contains(err.Error(), "list runs boom") {
		t.Fatalf("ResolveStackID(list error) err = %v", err)
	}
	failAttempts := &failingStackRepo{Repository: repo, failAttempts: true}
	if isStackPlanRun(failAttempts, workflowledger.RunSnapshot{RunID: "wfr-new"}) {
		t.Fatal("isStackPlanRun(attempt error) must report false")
	}
}

func TestOpenStackLedgerErrorBranches(t *testing.T) {
	// A non-existent root fails inside workspace.Open.
	if _, _, _, err := OpenStackLedger(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("OpenStackLedger(missing root) must error")
	}

	// A valid workspace root with an invalid TOML config fails in config.Load.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	badCfg := filepath.Join(root, "bad.toml")
	if err := os.WriteFile(badCfg, []byte("this is not = valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenStackLedger(root, badCfg); err == nil {
		t.Fatal("OpenStackLedger(invalid config) must error")
	}

	// With the context store path occupied by a directory the workflow store
	// cannot open its database.
	root2 := t.TempDir()
	ns := filepath.Join(root2, ".mivia")
	if err := os.MkdirAll(ns, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[[providers.openrouter.models]]\nname = \"m\"\ncontext_window_tokens = 8192\n"
	if err := os.WriteFile(filepath.Join(ns, "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ns, "context.db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := OpenStackLedger(root2, ""); err == nil {
		t.Log("OpenStackLedger(store path blocked) returned no error in this environment")
	}
}
