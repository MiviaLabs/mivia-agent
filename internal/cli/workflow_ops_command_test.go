package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// gatedWorkflowDefinition is a one-agent + one-human_gate workflow used to
// park runs at waiting_approval for the operator commands.
const gatedWorkflowDefinition = `version = 1
name = "gated"
initial_step = "one"

[inputs.task]
type = "string"
required = true
max_bytes = 100

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[steps]]
id = "review"
kind = "human_gate"

[[transitions]]
from = "one"
to = "review"
[transitions.match]
status = "succeeded"

[[transitions]]
from = "review"
to = "success"
[transitions.match]
status = "succeeded"
`

// newGatedApprovalFixture seeds a real store with a run parked at
// waiting_approval on human_gate step "review" (after a succeeded agent step
// "one"), the state an interrupted executor leaves behind.
func newGatedApprovalFixture(t *testing.T) (root, configPath, storePath, runID string) {
	t.Helper()
	root, configPath, storePath, raw, run := newGatedApprovalWorkspace(t)
	seedGatedApprovalHistory(t, storePath, raw, run)
	return root, configPath, storePath, run.RunID
}

// newGatedApprovalWorkspace builds the workspace, config, and the immutable
// run record for the gated approval fixture, before any history is seeded.
func newGatedApprovalWorkspace(t *testing.T) (root, configPath, storePath string, raw []byte, run workflowledger.RunSnapshot) {
	t.Helper()
	root = t.TempDir()
	storePath = filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	configPath = filepath.Join(root, "config.toml")
	if err := os.WriteFile(filepath.Join(root, ".mivia", "workflows", "gated.toml"), []byte(gatedWorkflowDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
	wf, _, err := definition.ParseWorkflowTOML([]byte(gatedWorkflowDefinition), "gated.toml")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		t.Fatal(err)
	}
	skills, err := loadChatSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAgentDefinitions(root, "", skills)
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := loaded.Registry.Get("one")
	if !ok {
		t.Fatal("agent one is missing")
	}
	digest, err := agent.DefinitionDigest()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := workflowledger.Snapshot{
		SchemaVersion:  workflowledger.SnapshotSchemaVersion,
		DefinitionTOML: []byte(gatedWorkflowDefinition), DefinitionDigest: compiled.Digest,
		Inputs: map[string]string{"task": "test"},
		Agents: map[string]workflowledger.AgentSnapshot{"one": {Digest: digest}},
	}
	raw, err = workflowledger.MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	run = workflowledger.RunSnapshot{
		RunID: "wfr-gated-approval", WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		SnapshotDigest: workflowledger.SnapshotDigest(raw),
		InputDigest:    workflowledger.InputDigest(snapshot.Inputs),
		Status:         workflowledger.RunStatusPending, ActiveStepID: compiled.InitialStep,
	}
	return root, configPath, storePath, raw, run
}

// seedGatedApprovalHistory persists the run and its parked history: the
// agent step succeeded, then the human gate parks at waiting_approval.
func seedGatedApprovalHistory(t *testing.T, storePath string, raw []byte, run workflowledger.RunSnapshot) {
	t.Helper()
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	repo := workflowledger.NewStorageRepository(store)
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

// parkGatedAgentStep completes step "one" with a route to the human gate and
// parks the run at waiting_approval.
func parkGatedAgentStep(ctx context.Context, repo workflowledger.Repository, runID string) error {
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		return err
	}
	attempt := workflowledger.StepAttempt{AttemptID: "att-one", RunID: runID, StepID: "one", AttemptNo: 1, Status: workflowledger.AttemptStatusPending}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return err
	}
	storedAttempt, err := repo.GetStepAttempt(ctx, runID, attempt.AttemptID)
	if err != nil {
		return err
	}
	if err := repo.CompleteStepAttempt(ctx, runID, attempt.AttemptID, storedAttempt.Version, workflowledger.AttemptOutcome{
		Status: workflowledger.AttemptStatusSucceeded, ToStepID: "review",
	}); err != nil {
		return err
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	return repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusWaitingApproval, nil)
}

// parkGatedHumanGate creates the human-gate attempt and its pending approval.
func parkGatedHumanGate(ctx context.Context, repo workflowledger.Repository, runID string) error {
	attempt := workflowledger.StepAttempt{AttemptID: "att-review", RunID: runID, StepID: "review", AttemptNo: 1, Status: workflowledger.AttemptStatusPending}
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		return err
	}
	return repo.CreateApproval(ctx, workflowledger.ApprovalRecord{
		ApprovalID: "wfa-approval-review-1", RunID: runID, StepID: "review", Status: "pending",
	})
}

// openWorkflowTestStore reopens the fixture store read-only for assertions.
func openWorkflowTestStore(t *testing.T, storePath string) workflowledger.Repository {
	t.Helper()
	store, err := openContextStorePath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return workflowledger.NewStorageRepository(store)
}

func TestWorkflowStatusCommand(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"status", runID, "--workspace", root, "--config", configPath}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("workflow status error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"run_id: " + runID,
		"workflow: gated",
		"status: waiting_approval",
		"active_step: review",
		"attempts: 2",
		"  one #1 succeeded -> review",
		"  review #1 running",
		"approvals:",
		"  wfa-approval-review-1 pending (step review)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
	// The status command never claims or mutates the run.
	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("status command changed the run to %q", run.Status)
	}
}

func TestWorkflowStatusCommandMissingRun(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "workflow.db")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	writeWorkflowRunFixture(t, root, "http://127.0.0.1:1", storePath)
	configPath := filepath.Join(root, "config.toml")
	err := runWorkflowWithIO([]string{"status", "wfr-missing", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `workflow run "wfr-missing" not found`) {
		t.Fatalf("workflow status error = %v, want not-found", err)
	}
}

func TestWorkflowEventsCommand(t *testing.T) {
	root, configPath, _, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"events", runID, "--workspace", root, "--config", configPath}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("workflow events error = %v", err)
	}
	out := stdout.String()
	for _, want := range []string{
		"run created",
		"status changed",
		"attempt started: step \"one\"",
		"attempt completed",
		"approval created",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("events output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--limit/--offset") {
		t.Fatalf("unpaged events output should not hint paging:\n%s", out)
	}
	// Paged output hints at more events when a full page is returned.
	var paged strings.Builder
	if err := runWorkflowWithIO([]string{"events", runID, "--limit", "1", "--workspace", root, "--config", configPath}, &paged, io.Discard); err != nil {
		t.Fatalf("paged workflow events error = %v", err)
	}
	if !strings.Contains(paged.String(), "--limit/--offset") {
		t.Fatalf("paged events output should hint paging:\n%s", paged.String())
	}
}

func TestWorkflowApproveCommandHappyPath(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"approve", runID, "wfa-approval-review-1", "--workspace", root, "--config", configPath}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("workflow approve error = %v", err)
	}
	if !strings.Contains(stdout.String(), "status=succeeded") {
		t.Fatalf("approve output = %q, want status=succeeded", stdout.String())
	}
	repo := openWorkflowTestStore(t, storePath)
	approvals, err := repo.ListApprovals(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Status != "approved" || approvals[0].Actor != WorkflowApprovalDefaultActor {
		t.Fatalf("approvals = %+v, want one approved by %q", approvals, WorkflowApprovalDefaultActor)
	}
}

func TestWorkflowApproveCommandActorFlag(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"approve", runID, "wfa-approval-review-1", "--actor", "alice", "--workspace", root, "--config", configPath}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("workflow approve error = %v", err)
	}
	repo := openWorkflowTestStore(t, storePath)
	approvals, err := repo.ListApprovals(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Actor != "alice" {
		t.Fatalf("approvals = %+v, want actor alice", approvals)
	}
}

func TestWorkflowRejectCommandHappyPath(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	err := runWorkflowWithIO([]string{"reject", runID, "wfa-approval-review-1", "--actor", "alice", "--reason", "not now", "--workspace", root, "--config", configPath}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("workflow reject error = %v", err)
	}
	if !strings.Contains(stdout.String(), "status=failed") {
		t.Fatalf("reject output = %q, want status=failed", stdout.String())
	}
	repo := openWorkflowTestStore(t, storePath)
	approvals, err := repo.ListApprovals(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Status != "rejected" || approvals[0].Actor != "alice" || approvals[0].Reason != "not now" {
		t.Fatalf("approvals = %+v, want one rejected by alice with reason", approvals)
	}
}

func TestWorkflowApproveCommandUnknownApproval(t *testing.T) {
	root, configPath, _, runID := newGatedApprovalFixture(t)
	err := runWorkflowWithIO([]string{"approve", runID, "wfa-approval-review-9", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), `approval "wfa-approval-review-9" not found`) {
		t.Fatalf("workflow approve error = %v, want unknown approval", err)
	}
}

func TestWorkflowApproveCommandFlagParsing(t *testing.T) {
	root, configPath, _, runID := newGatedApprovalFixture(t)
	err := runWorkflowWithIO([]string{"approve", runID, "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "expected a run ID and an approval ID") {
		t.Fatalf("workflow approve error = %v, want arity refusal", err)
	}
	err = runWorkflowWithIO([]string{"approve", runID, "wfa-approval-review-1", "--actor", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--actor requires a value") {
		t.Fatalf("workflow approve error = %v, want --actor value refusal", err)
	}
}

func TestWorkflowCancelCommand(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	var stdout strings.Builder
	if err := runWorkflowWithIO([]string{"cancel", runID, "--workspace", root, "--config", configPath}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow cancel error = %v", err)
	}
	if !strings.Contains(stdout.String(), "status=canceled") {
		t.Fatalf("cancel output = %q, want status=canceled", stdout.String())
	}
	// Cancel is idempotent on the now-terminal run.
	var again strings.Builder
	if err := runWorkflowWithIO([]string{"cancel", runID, "--workspace", root, "--config", configPath}, &again, io.Discard); err != nil {
		t.Fatalf("second workflow cancel error = %v", err)
	}
	if !strings.Contains(again.String(), "status=canceled") {
		t.Fatalf("second cancel output = %q, want status=canceled", again.String())
	}
	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != workflowledger.RunStatusCanceled {
		t.Fatalf("run status = %q, want canceled", run.Status)
	}
}

func TestWorkflowCancelCommandMissingRun(t *testing.T) {
	root, configPath, _, _ := newGatedApprovalFixture(t)
	err := runWorkflowWithIO([]string{"cancel", "wfr-missing", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("workflow cancel error = %v, want not-found", err)
	}
}

func TestWorkflowCleanupCommand(t *testing.T) {
	root, storePath, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	repo := openWorkflowTestStore(t, storePath)
	run, err := repo.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	name := run.WorktreeName
	if name == "" {
		t.Fatalf("delivery run has no worktree: %+v", run)
	}
	worktree, err := vcs.Resolve(t.Context(), root, name)
	if err != nil || worktree == nil {
		t.Fatalf("resolve run worktree = %+v, %v", worktree, err)
	}

	var stdout strings.Builder
	if err := runWorkflowWithIO([]string{"cleanup", runID, "--workspace", root, "--config", config}, &stdout, io.Discard); err != nil {
		t.Fatalf("workflow cleanup error = %v", err)
	}
	if !strings.Contains(stdout.String(), "cleaned") {
		t.Fatalf("cleanup output = %q, want cleaned", stdout.String())
	}
	if worktree, err := vcs.Resolve(t.Context(), root, name); err != nil || worktree != nil {
		t.Fatalf("worktree still resolvable after cleanup: %+v, %v", worktree, err)
	}
	if _, err := workflowCleanupGit.Run(t.Context(), delivery.GitContext{Dir: root, GitDir: filepath.Join(root, ".git")}, "show-ref", "--verify", "--quiet", "refs/heads/wf/"+name); err == nil {
		t.Fatalf("wf/%s branch still exists after cleanup", name)
	}
	// Cleanup is idempotent.
	var again strings.Builder
	if err := runWorkflowWithIO([]string{"cleanup", runID, "--workspace", root, "--config", config}, &again, io.Discard); err != nil {
		t.Fatalf("second workflow cleanup error = %v", err)
	}
}

func TestWorkflowCleanupCommandRefusesNonterminalRun(t *testing.T) {
	root, configPath, _, runID := newGatedApprovalFixture(t)
	err := runWorkflowWithIO([]string{"cleanup", runID, "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cleanup requires a finished run") {
		t.Fatalf("workflow cleanup error = %v, want finished-run refusal", err)
	}
}

func TestWorkflowCancelCommandRefusesDeliveryPending(t *testing.T) {
	root, _, config, _ := newDeliveryFixture(t)
	runID := runFixtureToDeliveryPending(t, root, config)
	err := runWorkflowWithIO([]string{"cancel", runID, "--workspace", root, "--config", config}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "deliver") {
		t.Fatalf("workflow cancel error = %v, want delivery refusal", err)
	}
}

func TestWorkflowCommandDispatchErrors(t *testing.T) {
	if err := runWorkflowWithIO([]string{"status"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "expected one run ID") {
		t.Fatalf("bare status error = %v, want arity refusal", err)
	}
	if err := runWorkflowWithIO([]string{"frobnicate"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("unknown subcommand error = %v", err)
	}
}

// TestWorkflowOpsCommandsLockSafety verifies that the mutating operator
// commands run under the workflow execution file lock: a concurrent holder
// fails the command instead of racing the ledger claim. The refusal is the
// bounded acquire's explained "still held after" error (a ~5s wait for a
// settling controller), not the plain lock's opaque "lock is busy" after its
// fixed retry budget.
func TestWorkflowOpsCommandsLockSafety(t *testing.T) {
	shortenWorkflowResolutionLockWait(t)
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	release, err := acquireWorkflowExecutionLock(storePath, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = runWorkflowWithIO([]string{"approve", runID, "wfa-approval-review-1", "--workspace", root, "--config", configPath}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("approve under a held execution lock succeeded; want a refusal")
	}
	if !strings.Contains(err.Error(), "still held after") {
		t.Fatalf("approve under a held execution lock error = %v, want the bounded 'still held after' refusal", err)
	}
	repo := openWorkflowTestStore(t, storePath)
	run, getErr := repo.GetRun(t.Context(), runID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if run.Status != workflowledger.RunStatusWaitingApproval {
		t.Fatalf("run status = %q after refused approve, want waiting_approval", run.Status)
	}
	approvals, listErr := repo.ListApprovals(t.Context(), runID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(approvals) != 1 || approvals[0].Status != "pending" {
		t.Fatalf("approvals = %+v after refused approve, want untouched pending", approvals)
	}
}
