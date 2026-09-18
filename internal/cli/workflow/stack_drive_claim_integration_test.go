package workflow

// Integration-level regression tests for the RunStackDrive execution claim
// (locked ADLC plan, slice A of the stack-reconciliation-sweep design), run
// through the REAL `mivia stack drive` CLI entrypoint against the same full
// git/PR/merge fixture (stackDriveIT, session_stack_drive_integration_fixture_test.go)
// the drive-ordering suite already uses — not just the claimStackDrive/
// releaseStackDrive helpers exercised in stack_drive_claim_test.go.

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// seedPlanRun creates the plan run directly, with a succeeded decompose
// attempt carrying planOutput, bypassing the scripted plan/decompose
// controller steps — RunStackDrive only ever reads the plan run's snapshot
// (declared inputs) and its succeeded decompose attempt, so driving the
// controller through those steps first is unnecessary ceremony for tests
// that exercise RunStackDrive's own claim wrap, not plan-step execution.
func (it *stackDriveIT) seedPlanRun(planOutput string) string {
	t := it.t
	t.Helper()
	runID := NewCLIWorkflowRunID()
	snap := miniStackSnapshot(t, it.root, it.compiled, it.rawDef)
	snap.Inputs = map[string]string{"task": "x"}
	raw, err := workflowledger.MarshalSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	runSnap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: it.compiled.Name, WorkflowDigest: it.compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := it.repo.CreateRun(context.Background(), runSnap, raw); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, it.repo, runID, []byte(planOutput))
	return runID
}

// driveDirect calls RunStackDrive directly against stackID — the actual CLI
// entrypoint under test (as opposed to startPlanRun's session-engine path,
// which drives through LaunchStartedWorkflow/maybeDriveSettledStack and does
// not exercise RunStackDrive's claim wrap at all).
func (it *stackDriveIT) driveDirect(stackID string) (stdout, stderr string, err error) {
	it.t.Helper()
	var out, errOut bytes.Buffer
	err = RunStackDrive([]string{"mini-stack", "--stack", stackID}, it.root, it.configPath, &out, &errOut)
	return out.String(), errOut.String(), err
}

// claimObservingGit wraps a GitRunner and invokes check before delegating,
// so a test can observe ledger state (e.g. GetRunClaim) at the moment the
// real drive is actively doing git work — a synchronous seam already wired
// into every drive path via WorkflowDeliverGit, reused here purely for
// observation rather than behavior stubbing.
type claimObservingGit struct {
	inner delivery.GitRunner
	check func()
}

func (g claimObservingGit) Run(ctx context.Context, gc delivery.GitContext, args ...string) (string, error) {
	g.check()
	return g.inner.Run(ctx, gc, args...)
}

// TestStackDriveClaimsRunForDuration pins the full lifecycle through the real
// entrypoint: the stack's execution claim is held while RunStackDrive is
// actively driving it, and released once it returns successfully.
func TestStackDriveClaimsRunForDuration(t *testing.T) {
	it := newStackDriveIT(t, "auto", "")
	stackID := it.seedPlanRun(multiChunkPlanOutput)

	var mu sync.Mutex
	var heldDuringDrive bool
	var checked bool
	prevGit := WorkflowDeliverGit
	t.Cleanup(func() { WorkflowDeliverGit = prevGit })
	WorkflowDeliverGit = claimObservingGit{
		inner: prevGit,
		check: func() {
			mu.Lock()
			defer mu.Unlock()
			if checked {
				return
			}
			checked = true
			_, _, ok, err := it.repo.GetRunClaim(context.Background(), stackID)
			heldDuringDrive = ok && err == nil
		},
	}

	if _, stderr, err := it.driveDirect(stackID); err != nil {
		t.Fatalf("driveDirect() error = %v; stderr = %q", err, stderr)
	}

	mu.Lock()
	got := heldDuringDrive
	sawCheck := checked
	mu.Unlock()
	if !sawCheck {
		t.Fatal("the observing git seam was never invoked; the drive did no git work to observe")
	}
	if !got {
		t.Fatal("claim was not held while the stack was being driven")
	}

	_, _, ok, err := it.repo.GetRunClaim(context.Background(), stackID)
	if err != nil {
		t.Fatalf("GetRunClaim after drive: %v", err)
	}
	if ok {
		t.Fatal("claim still held after RunStackDrive returned successfully")
	}
}

// TestStackDriveRefusesWhenClaimHeldByAnotherHolder is the direct regression
// test for the race this slice closes: a second driver (another operator
// invocation, or later the reconciliation sweep) must never proceed against
// a stack another live process already holds the claim for.
func TestStackDriveRefusesWhenClaimHeldByAnotherHolder(t *testing.T) {
	it := newStackDriveIT(t, "auto", "")
	stackID := it.seedPlanRun(multiChunkPlanOutput)

	if err := it.repo.ClaimRun(context.Background(), stackID, "other-driver"); err != nil {
		t.Fatalf("prime foreign claim: %v", err)
	}

	_, _, err := it.driveDirect(stackID)
	if err == nil {
		t.Fatal("RunStackDrive succeeded while another driver held the claim, want a refusal")
	}
	if !strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("error = %q, want it to mention the stack is claimed by another executor", err.Error())
	}

	// The refused drive must never have touched the task ledger: admission
	// happens strictly after the claim in RunStackDrive's body.
	seeded, err := workflowledger.NewStore(it.store).ListTasksByScope(StackScope(stackID))
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 0 {
		t.Fatalf("seeded chunk tasks = %d, want 0 — a refused drive must never admit chunks", len(seeded))
	}

	// The foreign claim must be untouched by the refused attempt.
	holder, _, ok, err := it.repo.GetRunClaim(context.Background(), stackID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if !ok || holder != "other-driver" {
		t.Fatalf("claim after refused drive = (holder=%q, ok=%v), want other-driver still held", holder, ok)
	}
}

// TestStackDriveReleasesClaimOnFailure pins the defer-based release: an error
// after the claim is acquired (a malformed decompose output - the earliest
// point after the claim RunStackDrive can fail, well short of any chunk
// admission) must still release the claim, not just the happy path -
// otherwise one failed drive would strand the stack, refusing every future
// operator retry AND the reconciliation sweep with "claimed by another
// executor" forever. RunStackDrive covers every later failure point (a real
// mid-drive chunk-admission error included) the same way: one defer
// registered immediately after the claim succeeds, so this earliest-failure
// case generalizes rather than needing its own test per failure site.
func TestStackDriveReleasesClaimOnFailure(t *testing.T) {
	it := newStackDriveIT(t, "auto", "")
	stackID := it.seedPlanRun("not valid json")

	if _, _, err := it.driveDirect(stackID); err == nil {
		t.Fatal("RunStackDrive succeeded against a malformed decompose output, want an error")
	}

	_, _, ok, err := it.repo.GetRunClaim(context.Background(), stackID)
	if err != nil {
		t.Fatalf("GetRunClaim: %v", err)
	}
	if ok {
		t.Fatal("claim still held after a failed drive, want it released on the error path")
	}

	// The release must be real, not cosmetic: a fresh drive attempt must be
	// free to claim and proceed (it will fail again on the same malformed
	// output, but it must fail on THAT, never on a claim refusal).
	_, _, err = it.driveDirect(stackID)
	if err == nil || strings.Contains(err.Error(), "claimed by another executor") {
		t.Fatalf("retry after a released failed claim = %v, want it to fail on the malformed output again, not a claim refusal", err)
	}
}

// TestWorkflowDeleteRefusedWhileStackDriveHoldsExecutionFlock is the direct
// regression test for F6: `mivia stack drive` used to take no execution flock
// on the plan run at all, so once claimStackDrive's heartbeat lapsed (a
// single transient store error is terminal for it, see
// startStackDriveClaimHeartbeat's doc comment) the DB claim would go stale
// after workflowledger.DefaultClaimLease and `workflow delete --force`
// against the still-running plan run would succeed, permanently stranding
// the stack (loadStackPlanOutput needs the deleted run's step attempts).
// This test reproduces exactly that degraded state directly — plan run
// claimed by the drive's flock but with NO live DB claim, mirroring a dead
// heartbeat — and pins that claimStackDrive's added flock (mirroring every
// other CLI-operator command's BeginWorkflowExecution) refuses the delete on
// its own, independent of claim freshness.
func TestWorkflowDeleteRefusedWhileStackDriveHoldsExecutionFlock(t *testing.T) {
	ShortenWorkflowResolutionLockWaitForTest(t)
	it := newStackDriveIT(t, "auto", "")
	stackID := it.seedPlanRun(multiChunkPlanOutput)

	release, err := AcquireWorkflowExecutionLock(it.storePath, stackID)
	if err != nil {
		t.Fatalf("AcquireWorkflowExecutionLock: %v", err)
	}
	defer release()

	var stdout, stderr bytes.Buffer
	err = RunWorkflowWithIO([]string{"delete", stackID, "--force", "--workspace", it.root, "--config", it.configPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("workflow delete --force succeeded against a stack drive's held execution flock; want a refusal")
	}

	if _, getErr := it.repo.GetRun(context.Background(), stackID); getErr != nil {
		t.Fatalf("plan run must survive a flock-refused delete: %v", getErr)
	}
}

// TestStackDriveSequentialRedrivesDoNotSpuriouslyRefuse pins that re-running
// `stack drive` after a prior invocation completed (the recovery/
// re-invocation path stack_drive_recovery_test.go already covers at the
// chunk-ledger level) never spuriously refuses at the claim layer: each
// invocation mints its own fresh holder (unlike ClaimRun's literal
// same-holder refresh, pinned separately at the unit level by
// TestClaimStackDriveSameHolderRefreshSucceeds in stack_drive_claim_test.go),
// and a claim with no live holder is always claimable - ordinary sequential
// re-drives must never trip the "claimed by another executor" refusal
// against themselves.
func TestStackDriveSequentialRedrivesDoNotSpuriouslyRefuse(t *testing.T) {
	it := newStackDriveIT(t, "auto", "")
	stackID := it.seedPlanRun(multiChunkPlanOutput)

	if _, stderr, err := it.driveDirect(stackID); err != nil {
		t.Fatalf("first driveDirect() error = %v; stderr = %q", err, stderr)
	}
	if _, stderr, err := it.driveDirect(stackID); err != nil {
		t.Fatalf("second driveDirect() error = %v; stderr = %q", err, stderr)
	}
}
