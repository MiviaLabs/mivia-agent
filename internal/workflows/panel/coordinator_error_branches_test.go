package panel

import (
	"context"
	"testing"

	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// These tests exercise the coordinator's fail-closed branches flagged by the
// coverage gate: nil repository, missing attempts, wrong phases, missing
// claims, and nil handles.

func TestPanelCoordinatorNilRepoRefusesEverything(t *testing.T) {
	panel := NewPanelCoordinator("wfr-nil", coordinatorStub{}, nil)
	ctx := context.Background()
	if _, err := panel.member(ctx, "a", "m"); err == nil {
		t.Fatal("member(nil repo) must fail")
	}
	if _, _, _, err := panel.synthesis(ctx, "a"); err == nil {
		t.Fatal("synthesis(nil repo) must fail")
	}
	if err := panel.requireRunnablePhase(ctx, "a", ledger.PanelPhaseMembersAdmitted); err == nil {
		t.Fatal("requireRunnablePhase(nil repo) must fail")
	}
	if err := panel.requireTerminalPhase(ctx, "a"); err == nil {
		t.Fatal("requireTerminalPhase(nil repo) must fail")
	}
}

func TestPanelCoordinatorMissingAttemptRefuses(t *testing.T) {
	repo := &fakePanelRepo{}
	panel := NewPanelCoordinator("wfr-x", coordinatorStub{}, repo)
	ctx := context.Background()
	if _, err := panel.member(ctx, "a", "m"); err == nil {
		t.Fatal("member(missing attempt) must fail")
	}
	if _, _, _, err := panel.synthesis(ctx, "a"); err == nil {
		t.Fatal("synthesis(missing attempt) must fail")
	}
	claimed := ledger.ContextWithClaimHolder(ctx, "holder")
	if err := panel.requireRunnablePhase(claimed, "a", ledger.PanelPhaseMembersAdmitted); err == nil {
		t.Fatal("requireRunnablePhase(missing attempt) must fail")
	}
	if err := panel.requireTerminalPhase(claimed, "a"); err == nil {
		t.Fatal("requireTerminalPhase(missing attempt) must fail")
	}
	if _, err := panel.ResumeMember(ctx, "a", "m"); err == nil {
		t.Fatal("ResumeMember(missing attempt) must fail")
	}
	if _, err := panel.ResumeSynthesis(ctx, "a"); err == nil {
		t.Fatal("ResumeSynthesis(missing attempt) must fail")
	}
}

func TestPanelCoordinatorWrongPhaseRefuses(t *testing.T) {
	runID, attemptID := "wfr-phase", "attempt"
	repo := &fakePanelRepo{attempt: ledger.StepAttempt{
		AttemptID: attemptID, RunID: runID, StepID: "panel", AttemptNo: 1,
		PanelExecution: &ledger.PanelExecution{Phase: ledger.PanelPhaseMembersAdmitted},
	}}
	panel := NewPanelCoordinator(runID, coordinatorStub{}, repo)
	if err := panel.requireTerminalPhase(ledger.ContextWithClaimHolder(context.Background(), "h"), attemptID); err == nil {
		t.Fatal("requireTerminalPhase(members-admitted phase) must refuse")
	}
}

func TestPanelCoordinatorMissingClaimRefuses(t *testing.T) {
	runID, attemptID := "wfr-claim", "attempt"
	repo := &fakePanelRepo{attempt: ledger.StepAttempt{
		AttemptID: attemptID, RunID: runID, StepID: "panel", AttemptNo: 1,
		PanelExecution: &ledger.PanelExecution{Phase: ledger.PanelPhaseMembersAdmitted},
	}}
	panel := NewPanelCoordinator(runID, coordinatorStub{}, repo)
	if err := panel.requireRunnablePhase(context.Background(), attemptID, ledger.PanelPhaseMembersAdmitted); err == nil {
		t.Fatal("requireRunnablePhase(no claim) must refuse")
	}
}

func TestPanelCoordinatorNilHandleJoinCancelRefuse(t *testing.T) {
	repo := newMemoryRepo(t)
	work := panelTaskWithID(t, "m0", "task")
	storePanelTask(t, repo, work)
	panel := NewPanelCoordinator("wfr-handles", coordinatorStub{}, repo)
	ctx := ledger.ContextWithClaimHolder(context.Background(), "holder")
	if _, err := panel.join(ctx, "run", "task", work, nil); err == nil {
		t.Fatal("join(nil handle) must refuse")
	} else if err != coordledger.ErrConflict {
		t.Fatalf("join(nil handle) = %v, want ErrConflict", err)
	}
	if err := panel.cancel(ctx, "run", "task", work, nil); err != coordledger.ErrConflict {
		t.Fatalf("cancel(nil handle) = %v, want ErrConflict", err)
	}
}
