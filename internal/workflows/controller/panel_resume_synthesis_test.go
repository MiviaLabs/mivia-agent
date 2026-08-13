package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// countingChildDispatchCoordinator wraps a coordinator.Coordinator and counts
// EnsureSingleTaskRun calls per child run ID, so a test can prove a resume
// joins the persisted synthesis child without ever re-dispatching a member.
// Access is single-threaded: a resume drives every child dispatch on the
// Advance goroutine, and this type is only installed after setup's member
// dispatch has already completed.
type countingChildDispatchCoordinator struct {
	coordinator.Coordinator
	memberRunIDs        map[string]struct{}
	synthesisRunID      string
	memberDispatches    int
	synthesisDispatches int
}

func (c *countingChildDispatchCoordinator) EnsureSingleTaskRun(ctx context.Context, req coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	if _, member := c.memberRunIDs[req.RunID]; member {
		c.memberDispatches++
	}
	if req.RunID == c.synthesisRunID {
		c.synthesisDispatches++
	}
	return c.Coordinator.EnsureSingleTaskRun(ctx, req)
}

// missingSynthesisInputRepo wraps a Repository and returns ErrContentNotFound
// for one named content ref, simulating a lost persisted synthesis envelope
// (the crash window's content store entry missing on resume).
type missingSynthesisInputRepo struct {
	workflowledger.Repository
	missingRef string
}

func (r *missingSynthesisInputRepo) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	if ref == r.missingRef {
		return nil, workflowledger.ErrContentNotFound
	}
	return r.Repository.LoadContent(ctx, ref)
}

// TestAdvancePanelStep_ResumeAfterSynthesisAdmittedJoinsSynthesisNotMembers
// is the F1-panel-resume-redispatch regression: an executor that crashed after
// CompareAndSetPanelPhase committed members_admitted -> synthesis_admitted but
// before EnsureSynthesis dispatched leaves the attempt in exactly the
// synthesis_admitted crash window. One fresh Advance must join the persisted
// synthesis child and settle the run succeeded, never re-running members.
//
// Pre-fix, advancePanelStep re-ran RunPanelMembers for every non-cancel_pending
// phase; each member's admission then failed
// requireRunnablePhase(members_admitted) with ErrConflict
// (panel_coordinator.go), so RunPanelMembers returned an error and
// settleAgentAttempt settled the run failed - an unnecessary failure plus
// redundant provider spend and a fresh synthesis child.
func TestAdvancePanelStep_ResumeAfterSynthesisAdmittedJoinsSynthesisNotMembers(t *testing.T) {
	memberReport := `{"verdict":"changes_requested","findings":[{"id":"f1","title":"t","severity":"low","description":"d"}]}`
	synthesisOutput := `{"dispositions":[` +
		`{"member_id":"security","finding_id":"f1","disposition":"included","final_finding_id":"F1"},` +
		`{"member_id":"correctness","finding_id":"f1","disposition":"duplicate","final_finding_id":"F1"}` +
		`],"summary":"One finding reported by both members."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-resume-synth", memberReport, synthesisOutput)
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	_, attempt, _, _ := driveToSynthesisAdmitted(t, ctx, ctrl, repo, step)

	// Wrap the inner coordinator so the resume's child dispatches are
	// observable: member children must NOT be re-dispatched, the persisted
	// synthesis child must be (that is the join).
	runner := ctrl.Runner.(*CoordinatorRunner)
	counting := &countingChildDispatchCoordinator{
		Coordinator:    runner.Coordinator,
		memberRunIDs:   map[string]struct{}{},
		synthesisRunID: attempt.PanelExecution.SynthesisRunID,
	}
	for _, m := range attempt.PanelExecution.Members {
		counting.memberRunIDs[m.CoordinatorRunID] = struct{}{}
	}
	runner.Coordinator = counting

	// One fresh Advance from the crash window.
	got, done, err := ctrl.Advance(ctx)
	if err != nil {
		t.Fatalf("Advance() error = %v", err)
	}
	if !done || got.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("Advance() run = %+v, done = %v, want a succeeded terminal run", got, done)
	}
	if counting.memberDispatches != 0 {
		t.Fatalf("resume re-dispatched %d member child(ren); a synthesis_admitted resume must join the persisted synthesis child, not re-run members", counting.memberDispatches)
	}
	if counting.synthesisDispatches != 1 {
		t.Fatalf("resume dispatched synthesis %d times, want exactly 1 (join the persisted child)", counting.synthesisDispatches)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (no new attempt minted on resume)", len(attempts))
	}
	if attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempt status = %q, want succeeded", attempts[0].Status)
	}
	if attempts[0].ErrorRef != "" {
		t.Fatalf("attempt ErrorRef = %q, want empty on a clean resume success", attempts[0].ErrorRef)
	}
}

// TestAdvancePanelStep_ResumeSynthesisAdmittedFailsClosedOnMissingPersistedEnvelope
// is the negative path: a synthesis_admitted resume whose persisted envelope
// is gone (crash lost the content, or a store failure) must fail closed with a
// cause naming the persisted envelope - no member re-dispatch, no synthesis
// dispatch, no panic, and no partially settled attempt. This also exercises
// the fail-closed claim shape: the same-holder claim is refreshed by Advance
// and every failure branch returns through failAttempt/settleAgentAttempt
// with the claim released by Advance's deferred ReleaseRun.
func TestAdvancePanelStep_ResumeSynthesisAdmittedFailsClosedOnMissingPersistedEnvelope(t *testing.T) {
	memberReport := `{"verdict":"approved","findings":[]}`
	synthesisOutput := `{"dispositions":[],"summary":"Nothing to report."}`
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-panel-resume-synth-missing", memberReport, synthesisOutput)
	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	_, attempt, _, _ := driveToSynthesisAdmitted(t, ctx, ctrl, repo, step)

	missing := &missingSynthesisInputRepo{Repository: repo, missingRef: attempt.PanelExecution.Synthesis.Work.InputRef}
	ctrl.Repo = missing

	runner := ctrl.Runner.(*CoordinatorRunner)
	counting := &countingChildDispatchCoordinator{
		Coordinator:    runner.Coordinator,
		memberRunIDs:   map[string]struct{}{},
		synthesisRunID: attempt.PanelExecution.SynthesisRunID,
	}
	for _, m := range attempt.PanelExecution.Members {
		counting.memberRunIDs[m.CoordinatorRunID] = struct{}{}
	}
	runner.Coordinator = counting

	got, done, err := ctrl.Advance(ctx)
	if err == nil {
		t.Fatal("Advance() error = nil, want the persisted-envelope failure")
	}
	if !done || got.Status != workflowledger.RunStatusFailed {
		t.Fatalf("Advance() run = %+v, done = %v, want a failed terminal run", got, done)
	}
	if counting.memberDispatches != 0 || counting.synthesisDispatches != 0 {
		t.Fatalf("fail-closed resume dispatched children: members = %d, synthesis = %d, want 0/0", counting.memberDispatches, counting.synthesisDispatches)
	}
	attempts, err := missing.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempts = %+v, want exactly one failed attempt", attempts)
	}
	if attempts[0].ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty, want the persisted-envelope failure cause")
	}
	body, err := missing.LoadContent(context.Background(), attempts[0].ErrorRef)
	if err != nil {
		t.Fatalf("load ErrorRef content: %v", err)
	}
	if !strings.Contains(string(body), "persisted synthesis envelope") {
		t.Fatalf("ErrorRef content = %q, want it to name the persisted synthesis envelope", body)
	}
}
