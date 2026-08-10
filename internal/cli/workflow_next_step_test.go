package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// testWorkflowDefinition is a minimal linear workflow: start -> review ->
// success. The file name and in-file name are both "test-wf", matching the
// runs the event fixture seeds.
const testWorkflowDefinition = `version = 1
name = "test-wf"
description = "Runs the test workflow."
initial_step = "start"

[[steps]]
id = "start"
kind = "agent"
agent = "test-agent"

[[steps]]
id = "review"
kind = "agent"
agent = "test-agent"

[[transitions]]
from = "start"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "review"
to = "success"
match = { status = "succeeded" }
`

// writeWorkflowDefinition writes a workflow definition beneath the workspace
// .mivia/workflows directory.
func writeWorkflowDefinition(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runningFixtureRun(t *testing.T, runID string) (root string, closeFn func()) {
	t.Helper()
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, runID)
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		closeFn()
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		closeFn()
		t.Fatal(err)
	}
	return root, closeFn
}

// TestWorkflowNextStepRunningRunWithSuccessor is the success case: a
// non-terminal run whose active step has a successor in declaration order.
func TestWorkflowNextStepRunningRunWithSuccessor(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0001")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	})
	if got != "review" {
		t.Fatalf("workflowNextStep = %q, want review", got)
	}
}

// TestWorkflowNextStepFinalDeclaredStep pins that a run on the last declared
// step has no next step.
func TestWorkflowNextStepFinalDeclaredStep(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0002")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "review",
	})
	if got != "" {
		t.Fatalf("workflowNextStep(final step) = %q, want empty", got)
	}
}

// TestWorkflowNextStepTerminalStatuses pins that every terminal status yields
// no next step even when the definition has a successor.
func TestWorkflowNextStepTerminalStatuses(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0003")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusSucceeded, workflowledger.RunStatusFailed,
		workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut,
		workflowledger.RunStatusDeliveryFailed,
	} {
		if got := workflowNextStep(root, workflowledger.RunSnapshot{
			WorkflowName: "test-wf", Status: status, ActiveStepID: "start",
		}); got != "" {
			t.Errorf("workflowNextStep(%s) = %q, want empty", status, got)
		}
	}
}

// TestWorkflowNextStepUnknownWorkflowName pins the unknown-name degrade path.
func TestWorkflowNextStepUnknownWorkflowName(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0004")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "ghost", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	})
	if got != "" {
		t.Fatalf("workflowNextStep(unknown name) = %q, want empty", got)
	}
}

// TestWorkflowNextStepMissingDirectory pins the missing .mivia/workflows
// degrade path.
func TestWorkflowNextStepMissingDirectory(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0005")
	defer closeFn()

	got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	})
	if got != "" {
		t.Fatalf("workflowNextStep(missing dir) = %q, want empty", got)
	}
}

// TestWorkflowNextStepEmptyDefinition pins the empty-file degrade path.
func TestWorkflowNextStepEmptyDefinition(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0006")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", "")

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(empty definition) = %q, want empty", got)
	}
}

// TestWorkflowNextStepMalformedDefinition pins the malformed-TOML degrade
// path.
func TestWorkflowNextStepMalformedDefinition(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0007")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", "name = [unterminated")

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(malformed) = %q, want empty", got)
	}
}

// TestWorkflowNextStepOversizedDefinition pins the oversize degrade path:
// discovery refuses a file over the byte bound before any parsing.
func TestWorkflowNextStepOversizedDefinition(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0008")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", strings.Repeat("x", 70000))

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(oversized) = %q, want empty", got)
	}
}

// TestWorkflowNextStepDuplicateStepIDs pins the duplicate-step-id degrade
// path: ParseWorkflowTOML rejects the file, so the helper yields nothing.
func TestWorkflowNextStepDuplicateStepIDs(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0009")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", `version = 1
name = "test-wf"
initial_step = "start"

[[steps]]
id = "start"
kind = "agent"

[[steps]]
id = "start"
kind = "agent"
`)

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(duplicate step ids) = %q, want empty", got)
	}
}

// TestWorkflowNextStepAmbiguousTransitions pins the duplicate-transition-key
// degrade path: two transitions from one step with no distinguishing match
// criteria fail compilation, so the helper yields nothing.
func TestWorkflowNextStepAmbiguousTransitions(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0010")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", `version = 1
name = "test-wf"
initial_step = "start"

[[steps]]
id = "start"
kind = "agent"
agent = "test-agent"

[[steps]]
id = "review"
kind = "agent"
agent = "test-agent"

[[transitions]]
from = "start"
to = "review"
match = { status = "succeeded" }

[[transitions]]
from = "start"
to = "success"
match = { status = "succeeded" }
`)

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(ambiguous transitions) = %q, want empty", got)
	}
}

// TestWorkflowNextStepSymlinkedDirectory pins that the helper degrades,
// never follows a symlinked workflows directory.
func TestWorkflowNextStepSymlinkedDirectory(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0011")
	defer closeFn()
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, ".mivia", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, ".mivia", "workflows", "test-wf.toml"), []byte(testWorkflowDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, ".mivia", "workflows"), filepath.Join(root, ".mivia", "workflows")); err != nil {
		t.Fatal(err)
	}

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(symlinked dir) = %q, want empty", got)
	}
}

// TestWorkflowNextStepSymlinkedFile pins that the helper degrades, never
// follows a symlinked workflow file.
func TestWorkflowNextStepSymlinkedFile(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXT0012")
	defer closeFn()
	dir := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(t.TempDir(), "real-wf.toml")
	if err := os.WriteFile(real, []byte(testWorkflowDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "test-wf.toml")); err != nil {
		t.Fatal(err)
	}

	if got := workflowNextStep(root, workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(symlinked file) = %q, want empty", got)
	}
}

// TestWorkflowNextStepEmptyRootNormalizes the empty-root normalization: a
// blank root resolves like ".".
func TestWorkflowNextStepEmptyRootNormalizes(t *testing.T) {
	if got := workflowNextStep("", workflowledger.RunSnapshot{
		WorkflowName: "test-wf", Status: workflowledger.RunStatusRunning, ActiveStepID: "start",
	}); got != "" {
		t.Fatalf("workflowNextStep(empty root) = %q, want empty for a missing cwd definition", got)
	}
}

// TestExecuteWorkflowRunsPrintsNextStep is the wiring success case: the runs
// listing appends next=<step> after step=<active>.
func TestExecuteWorkflowRunsPrintsNextStep(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXTWIRE01")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "step=start") || !strings.Contains(out, "next=review") {
		t.Fatalf("output does not carry the next step:\n%s", out)
	}
}

// TestExecuteWorkflowRunsOmitsNextForFinalStep pins that a run on the final
// declared step prints no next= suffix. The active step is derived from the
// ledger projection: completing the "start" attempt routes to "review", the
// last declared step.
func TestExecuteWorkflowRunsOmitsNextForFinalStep(t *testing.T) {
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, "wfr-NEXTWIRE02")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
		AttemptID: "att-final", RunID: run, StepID: "start", AttemptNo: 1,
	}); err != nil {
		t.Fatal(err)
	}
	attempt, err := repo.GetStepAttempt(ctx, run, "att-final")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteStepAttempt(ctx, run, "att-final", attempt.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "review",
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	if strings.Contains(stdout.String(), "next=") {
		t.Fatalf("final-step run printed a next= suffix:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "step=review") {
		t.Fatalf("final-step run lost its step= column:\n%s", stdout.String())
	}
}

// TestExecuteWorkflowRunsOmitsNextForTerminalRun pins that a terminal run
// prints no next= suffix even when the definition has a successor.
func TestExecuteWorkflowRunsOmitsNextForTerminalRun(t *testing.T) {
	root, _, repo, closeFn, ctx, run := openEventsFixtureWithRun(t, "wfr-NEXTWIRE03")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	stored, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	// pending -> running -> succeeded: the ledger rejects a direct pending
	// -> succeeded edge, so the run must pass through running first.
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, run, stored.Version+1, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns: %v", err)
	}
	if strings.Contains(stdout.String(), "next=") {
		t.Fatalf("terminal run printed a next= suffix:\n%s", stdout.String())
	}
}

// TestExecuteWorkflowRunsOmitsNextForUnknownName pins that an unknown
// workflow name prints no next= suffix and no error.
func TestExecuteWorkflowRunsOmitsNextForUnknownName(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-NEXTWIRE04")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	other := "wfr-NEXTWIRE05"
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateRun admits only pending runs; advance to running after admission.
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: other, WorkflowName: "ghost", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, other, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := executeWorkflowRuns(root, filepath.Join(root, "config.toml"), "", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRuns must not error on an unknown name: %v", err)
	}
	if !strings.Contains(stdout.String(), other) {
		t.Fatalf("unknown-name run missing from listing:\n%s", stdout.String())
	}
	// The ghost run itself must print no next= suffix. The fixture's own
	// test-wf run legitimately resolves one, so the check is scoped to the
	// ghost run's line rather than the whole listing.
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, other+"  ") && strings.Contains(line, "next=") {
			t.Fatalf("unknown-name run printed a next= suffix:\n%s", stdout.String())
		}
	}
}

// TestExecuteWorkflowRunsWatchHasNoNextStep pins that the --watch path is
// untouched: it never prints a next= suffix.
func TestExecuteWorkflowRunsWatchHasNoNextStep(t *testing.T) {
	root, closeFn := runningFixtureRun(t, "wfr-NEXTWIRE06")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)

	original := workflowWatchSleep
	t.Cleanup(func() { workflowWatchSleep = original })
	workflowWatchSleep = func(time.Duration) {}

	var stdout bytes.Buffer
	if err := executeWorkflowRunsWatch(root, filepath.Join(root, "config.toml"), "succeeded", 20, &stdout, io.Discard); err != nil {
		t.Fatalf("executeWorkflowRunsWatch: %v", err)
	}
	if strings.Contains(stdout.String(), "next=") {
		t.Fatalf("--watch output carries a next= suffix:\n%s", stdout.String())
	}
}
