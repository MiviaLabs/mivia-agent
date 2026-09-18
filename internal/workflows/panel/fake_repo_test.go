package panel

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// fakePanelRepo is the minimal persistence surface NewPanelCoordinator
// accepts: the panel depends on this consumer-side subset interface, not on
// the concrete storage repository.
type fakePanelRepo struct {
	attempt ledger.StepAttempt
	claims  []string
}

func (f *fakePanelRepo) GetStepAttempt(_ context.Context, runID, attemptID string) (ledger.StepAttempt, error) {
	if f.attempt.RunID != runID || f.attempt.AttemptID != attemptID {
		return ledger.StepAttempt{}, ledger.ErrNotFound
	}
	return f.attempt, nil
}

func (f *fakePanelRepo) ClaimRun(_ context.Context, _, holder string) error {
	f.claims = append(f.claims, holder)
	return nil
}

func (f *fakePanelRepo) ReleaseRun(context.Context, string, string) error { return nil }

func (f *fakePanelRepo) LoadContent(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// TestPanelCoordinatorWorksWithFakeRepo pins that the coordinator reads
// attempts and refreshes claims through the panelRepo interface, so a
// persistence implementation other than StorageRepository works unchanged.
func TestPanelCoordinatorWorksWithFakeRepo(t *testing.T) {
	runID := "wfr-fake-repo"
	attemptID := "attempt"
	members := make([]ledger.PanelMemberExecution, 2)
	for i, memberID := range []string{"member-0", "member-1"} {
		childRun, childTask := ledger.PanelChildIDs(runID, attemptID, memberID)
		members[i] = ledger.PanelMemberExecution{MemberID: memberID, CoordinatorRunID: childRun, TaskID: childTask, Order: i}
	}
	repo := &fakePanelRepo{attempt: ledger.StepAttempt{
		AttemptID: attemptID, RunID: runID, StepID: "panel", AttemptNo: 1,
		PanelExecution: &ledger.PanelExecution{Phase: ledger.PanelPhaseMembersAdmitted, Members: members},
	}}
	panel := NewPanelCoordinator(runID, coordinatorStub{}, repo)

	member, err := panel.member(context.Background(), attemptID, "member-1")
	if err != nil {
		t.Fatalf("member() error = %v", err)
	}
	if member.MemberID != "member-1" {
		t.Fatalf("member = %+v", member)
	}
	if err := panel.requireWorkflowClaim(ledger.ContextWithClaimHolder(context.Background(), "holder")); err != nil {
		t.Fatalf("requireWorkflowClaim error = %v", err)
	}
	if len(repo.claims) != 1 || repo.claims[0] != "holder" {
		t.Fatalf("claims = %v", repo.claims)
	}
}

// coordinatorStub is the minimal PanelChildCoordinator; the member read and
// claim paths must never reach it.
type coordinatorStub struct{}

func (coordinatorStub) EnsureSingleTaskRun(context.Context, coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	return nil, errors.New("unexpected")
}
func (coordinatorStub) EnsureTerminalSingleTaskRun(context.Context, coordinator.EnsureRunRequest, coordledger.TaskStatus) (*coordinator.RunHandle, error) {
	return nil, errors.New("unexpected")
}
func (coordinatorStub) JoinAsRecovered(context.Context, coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	return nil, errors.New("unexpected")
}
func (coordinatorStub) Join(context.Context, *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return nil, errors.New("unexpected")
}
func (coordinatorStub) Cancel(context.Context, *coordinator.RunHandle) error {
	return errors.New("unexpected")
}
