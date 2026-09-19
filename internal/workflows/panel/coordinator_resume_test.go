package panel

import (
	"context"
	"fmt"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Coverage repair for the coordinator's member-list, resume, and
// phase-mismatch branches flagged by the diff-coverage gate.

// panelAttemptRepo is a fakePanelRepo seeded with a panel attempt and the
// content its members reference, so member/phase/resume paths run past the
// repository guards.
type panelAttemptRepo struct {
	fakePanelRepo
}

func newPanelAttemptRepo(t *testing.T, runID, attemptID string, phase ledger.PanelPhase, memberIDs []string, withContent bool) *panelAttemptRepo {
	t.Helper()
	members := make([]ledger.PanelMemberExecution, 0, len(memberIDs))
	for i, memberID := range memberIDs {
		childRun, childTask := ledger.PanelChildIDs(runID, attemptID, memberID)
		work := panelTaskWithID(t, memberID, childTask)
		members = append(members, ledger.PanelMemberExecution{MemberID: memberID, CoordinatorRunID: childRun, TaskID: childTask, Order: i, Work: work})
	}
	repo := &panelAttemptRepo{fakePanelRepo{attempt: ledger.StepAttempt{
		AttemptID: attemptID, RunID: runID, StepID: "panel", AttemptNo: 1,
		PanelExecution: &ledger.PanelExecution{Phase: phase, Members: members},
	}}}
	if withContent {
		repo.content = map[string][]byte{}
		for _, member := range members {
			input := fmt.Sprintf(`{"input":%q}`, member.Work.TaskName)
			for ref, data := range map[string]string{
				member.Work.InputRef:        input,
				member.Work.InputSchemaRef:  `{}`,
				member.Work.OutputSchemaRef: `{}`,
			} {
				repo.content[ref] = []byte(data)
			}
		}
	}
	return repo
}

func TestCoordinatorMemberNotInListFails(t *testing.T) {
	repo := newPanelAttemptRepo(t, "wfr-x", "attempt", ledger.PanelPhaseMembersAdmitted, []string{"member-0"}, false)
	panel := NewPanelCoordinator("wfr-x", coordinatorStub{}, repo)
	if _, err := panel.member(context.Background(), "attempt", "member-9"); err == nil {
		t.Fatal("member(unknown id) must fail")
	}
}

func TestCoordinatorResumeMemberWrongPhaseFails(t *testing.T) {
	repo := newPanelAttemptRepo(t, "wfr-x", "attempt", ledger.PanelPhaseSynthesisAdmitted, []string{"member-0"}, false)
	panel := NewPanelCoordinator("wfr-x", coordinatorStub{}, repo)
	ctx := ledger.ContextWithClaimHolder(context.Background(), "holder")
	if _, err := panel.ResumeMember(ctx, "attempt", "member-0"); err == nil {
		t.Fatal("ResumeMember(synthesis phase) must fail")
	}
}

func TestCoordinatorResumeSynthesisWrongPhaseFails(t *testing.T) {
	repo := newPanelAttemptRepo(t, "wfr-x", "attempt", ledger.PanelPhaseMembersAdmitted, []string{"member-0"}, true)
	synthesisRun, synthesisTask := ledger.PanelChildIDs("wfr-x", "attempt", "synthesis")
	repo.attempt.PanelExecution.SynthesisRunID = synthesisRun
	repo.attempt.PanelExecution.SynthesisTaskID = synthesisTask
	repo.attempt.PanelExecution.Synthesis = &ledger.PanelSynthesisExecution{Work: validSynthesisTask(t, "wfr-x", "attempt")}
	panel := NewPanelCoordinator("wfr-x", coordinatorStub{}, repo)
	ctx := ledger.ContextWithClaimHolder(context.Background(), "holder")
	if _, err := panel.ResumeSynthesis(ctx, "attempt"); err == nil {
		t.Fatal("ResumeSynthesis(members phase) must fail")
	}
}

func TestCoordinatorResumeReachesCoordinator(t *testing.T) {
	repo := newPanelAttemptRepo(t, "wfr-x", "attempt", ledger.PanelPhaseMembersAdmitted, []string{"member-0"}, true)
	panel := NewPanelCoordinator("wfr-x", coordinatorStub{}, repo)
	ctx := ledger.ContextWithClaimHolder(context.Background(), "holder")
	if _, err := panel.ResumeMember(ctx, "attempt", "member-0"); err == nil {
		t.Fatal("ResumeMember against the stub coordinator must surface its error")
	}
}

func TestCoordinatorNilRepoCancelMemberFails(t *testing.T) {
	panel := NewPanelCoordinator("wfr-nil", coordinatorStub{}, nil)
	if err := panel.CancelMember(context.Background(), "a", "m", nil); err == nil {
		t.Fatal("CancelMember(nil repo) must fail")
	}
	if err := panel.CancelSynthesis(context.Background(), "a", nil); err == nil {
		t.Fatal("CancelSynthesis(nil repo) must fail")
	}
}

var _ = fmt.Errorf("x")
