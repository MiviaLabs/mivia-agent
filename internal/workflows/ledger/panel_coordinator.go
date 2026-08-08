package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// PanelCoordinator binds every child operation to persisted panel state.
// It does not execute panel fan-out or aggregation.
type PanelCoordinator struct {
	workflowRunID string
	inner         coordinator.Coordinator
	repo          Repository
}

type panelActorPermitProbe interface {
	NeedsActorPermit(context.Context, coordinator.EnsureRunRequest) (bool, error)
}

// MemberNeedsActorPermit checks whether member admission can create a local
// actor. Existing remote and terminal children only need a wait-only join.
func (p PanelCoordinator) MemberNeedsActorPermit(ctx context.Context, attemptID, memberID string) (bool, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return false, err
	}
	if err := p.requireRunnablePhase(ctx, attemptID, PanelPhaseMembersAdmitted); err != nil {
		return false, err
	}
	req, err := p.request(ctx, member.CoordinatorRunID, member.TaskID, member.Work, false)
	if err != nil {
		return false, err
	}
	probe, ok := p.inner.(panelActorPermitProbe)
	if !ok {
		return true, nil
	}
	return probe.NeedsActorPermit(p.childContext(ctx), req)
}

func NewPanelCoordinator(workflowRunID string, inner coordinator.Coordinator, repo Repository) PanelCoordinator {
	return PanelCoordinator{workflowRunID: workflowRunID, inner: inner, repo: repo}
}

func (p PanelCoordinator) childContext(ctx context.Context) context.Context {
	return ContextWithPanelChildPrincipal(ctx, p.workflowRunID)
}

func (p PanelCoordinator) EnsureMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	if err := p.requireRunnablePhase(ctx, attemptID, PanelPhaseMembersAdmitted); err != nil {
		return nil, err
	}
	return p.ensure(ctx, member.CoordinatorRunID, member.TaskID, member.Work, false)
}

// EnsureRemoteMember joins an already remote member without taking it over.
// A caller that loses the remote state receives ErrWaitOnlyJoinLost and must
// acquire a local actor permit before a normal ensure.
func (p PanelCoordinator) EnsureRemoteMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	return p.EnsureMember(coordinator.ContextWithPanelWaitOnlyJoin(ctx), attemptID, memberID)
}

func (p PanelCoordinator) EnsureTerminalMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	if err := p.requireTerminalPhase(ctx, attemptID); err != nil {
		return nil, err
	}
	return p.ensure(ctx, member.CoordinatorRunID, member.TaskID, member.Work, true)
}

func (p PanelCoordinator) JoinMember(ctx context.Context, attemptID, memberID string, handle *coordinator.RunHandle) (*coordinator.RunResult, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	return p.join(ctx, member.CoordinatorRunID, member.TaskID, member.Work, handle)
}

func (p PanelCoordinator) ResumeMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	if err := p.requireRunnablePhase(ctx, attemptID, PanelPhaseMembersAdmitted); err != nil {
		return nil, err
	}
	return p.resume(ctx, member.CoordinatorRunID, member.TaskID, member.Work)
}

func (p PanelCoordinator) CancelMember(ctx context.Context, attemptID, memberID string, handle *coordinator.RunHandle) error {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return err
	}
	return p.cancel(ctx, member.CoordinatorRunID, member.TaskID, member.Work, handle)
}

func (p PanelCoordinator) EnsureSynthesis(ctx context.Context, attemptID string) (*coordinator.RunHandle, error) {
	runID, taskID, work, err := p.synthesis(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := p.requireRunnablePhase(ctx, attemptID, PanelPhaseSynthesisAdmitted); err != nil {
		return nil, err
	}
	return p.ensure(ctx, runID, taskID, work, false)
}

func (p PanelCoordinator) EnsureTerminalSynthesis(ctx context.Context, attemptID string) (*coordinator.RunHandle, error) {
	runID, taskID, work, err := p.synthesis(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := p.requireTerminalPhase(ctx, attemptID); err != nil {
		return nil, err
	}
	return p.ensure(ctx, runID, taskID, work, true)
}

func (p PanelCoordinator) JoinSynthesis(ctx context.Context, attemptID string, handle *coordinator.RunHandle) (*coordinator.RunResult, error) {
	runID, taskID, work, err := p.synthesis(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	return p.join(ctx, runID, taskID, work, handle)
}

func (p PanelCoordinator) ResumeSynthesis(ctx context.Context, attemptID string) (*coordinator.RunHandle, error) {
	runID, taskID, work, err := p.synthesis(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if err := p.requireRunnablePhase(ctx, attemptID, PanelPhaseSynthesisAdmitted); err != nil {
		return nil, err
	}
	return p.resume(ctx, runID, taskID, work)
}

func (p PanelCoordinator) CancelSynthesis(ctx context.Context, attemptID string, handle *coordinator.RunHandle) error {
	runID, taskID, work, err := p.synthesis(ctx, attemptID)
	if err != nil {
		return err
	}
	return p.cancel(ctx, runID, taskID, work, handle)
}

func (p PanelCoordinator) member(ctx context.Context, attemptID, memberID string) (PanelMemberExecution, error) {
	if p.repo == nil {
		return PanelMemberExecution{}, ErrNotFound
	}
	attempt, err := p.repo.GetStepAttempt(ctx, p.workflowRunID, attemptID)
	if err != nil || attempt.PanelExecution == nil {
		return PanelMemberExecution{}, ErrNotFound
	}
	for _, member := range attempt.PanelExecution.Members {
		if member.MemberID == memberID {
			return member.clone(), nil
		}
	}
	return PanelMemberExecution{}, ErrNotFound
}

func (p PanelCoordinator) synthesis(ctx context.Context, attemptID string) (string, string, PanelTaskSpec, error) {
	if p.repo == nil {
		return "", "", PanelTaskSpec{}, ErrNotFound
	}
	attempt, err := p.repo.GetStepAttempt(ctx, p.workflowRunID, attemptID)
	if err != nil || attempt.PanelExecution == nil || attempt.PanelExecution.Synthesis == nil {
		return "", "", PanelTaskSpec{}, ErrNotFound
	}
	return attempt.PanelExecution.SynthesisRunID, attempt.PanelExecution.SynthesisTaskID, attempt.PanelExecution.Synthesis.Work.clone(), nil
}

func (p PanelCoordinator) requireRunnablePhase(ctx context.Context, attemptID string, want PanelPhase) error {
	if p.repo == nil {
		return ErrNotFound
	}
	if err := p.requireWorkflowClaim(ctx); err != nil {
		return err
	}
	attempt, err := p.repo.GetStepAttempt(ctx, p.workflowRunID, attemptID)
	if err != nil || attempt.PanelExecution == nil {
		return ErrNotFound
	}
	if IsTerminalAttemptStatus(attempt.Status) || attempt.PanelExecution.Phase != want {
		return coordledger.ErrConflict
	}
	return nil
}

func (p PanelCoordinator) requireTerminalPhase(ctx context.Context, attemptID string) error {
	if p.repo == nil {
		return ErrNotFound
	}
	if err := p.requireWorkflowClaim(ctx); err != nil {
		return err
	}
	attempt, err := p.repo.GetStepAttempt(ctx, p.workflowRunID, attemptID)
	if err != nil || attempt.PanelExecution == nil {
		return ErrNotFound
	}
	if IsTerminalAttemptStatus(attempt.Status) || attempt.PanelExecution.Phase != PanelPhaseCancelPending {
		return coordledger.ErrConflict
	}
	return nil
}

// requireWorkflowClaim refreshes the caller's workflow claim. The caller keeps
// this claim while it dispatches panel children, so a different controller
// cannot change the panel phase between its phase check and child admission.
func (p PanelCoordinator) requireWorkflowClaim(ctx context.Context) error {
	holder, ok := claimHolderFromContext(ctx)
	if !ok {
		return ErrClaimNotHeld
	}
	return p.repo.ClaimRun(ctx, p.workflowRunID, holder)
}

func (p PanelCoordinator) ensure(ctx context.Context, runID, taskID string, work PanelTaskSpec, terminal bool) (*coordinator.RunHandle, error) {
	req, err := p.request(ctx, runID, taskID, work, terminal)
	if err != nil {
		return nil, err
	}
	if terminal {
		return p.inner.EnsureTerminalSingleTaskRun(p.childContext(ctx), req, coordledger.TaskStatusCanceled)
	}
	h, err := p.inner.EnsureSingleTaskRun(p.childContext(ctx), req)
	return h, err
}

func (p PanelCoordinator) join(ctx context.Context, runID, taskID string, work PanelTaskSpec, handle *coordinator.RunHandle) (*coordinator.RunResult, error) {
	if err := p.requireWorkflowClaim(ctx); err != nil {
		return nil, err
	}
	if _, err := p.request(ctx, runID, taskID, work, true); err != nil {
		return nil, err
	}
	if handle == nil || handle.RunID() != runID {
		return nil, coordledger.ErrConflict
	}
	return p.inner.Join(p.childContext(ctx), handle)
}

func (p PanelCoordinator) resume(ctx context.Context, runID, taskID string, work PanelTaskSpec) (*coordinator.RunHandle, error) {
	req, err := p.request(ctx, runID, taskID, work, false)
	if err != nil {
		return nil, err
	}
	req.ForceResume = true
	h, err := p.inner.EnsureSingleTaskRun(p.childContext(ctx), req)
	return h, err
}

func (p PanelCoordinator) cancel(ctx context.Context, runID, taskID string, work PanelTaskSpec, handle *coordinator.RunHandle) error {
	if err := p.requireWorkflowClaim(ctx); err != nil {
		return err
	}
	if _, err := p.request(ctx, runID, taskID, work, true); err != nil {
		return err
	}
	if handle == nil || handle.RunID() != runID {
		return coordledger.ErrConflict
	}
	return p.inner.Cancel(p.childContext(ctx), handle)
}

func (p PanelCoordinator) request(ctx context.Context, runID, taskID string, work PanelTaskSpec, allowExpired bool) (coordinator.EnsureRunRequest, error) {
	if p.repo == nil {
		return coordinator.EnsureRunRequest{}, fmt.Errorf("panel repository is nil")
	}
	if err := work.validateLegacy(); err != nil {
		return coordinator.EnsureRunRequest{}, err
	}
	if !allowExpired && !time.Now().Before(work.DeadlineAt) {
		return coordinator.EnsureRunRequest{}, coordledger.ErrConflict
	}
	input, inputSchema, outputSchema, err := panelWorkContent(ctx, p.repo, work)
	if err != nil {
		return coordinator.EnsureRunRequest{}, err
	}
	task := subagents.Task{ID: taskID, Name: work.TaskName, Input: input, InputSchema: inputSchema, OutputSchema: outputSchema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil || fingerprint != work.CoordinatorRequestFingerprint {
		return coordinator.EnsureRunRequest{}, coordledger.ErrConflict
	}
	return coordinator.EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "panel-child:" + p.workflowRunID + ":" + taskID, Policy: work.Policy}, nil
}

func panelWorkContent(ctx context.Context, loader interface {
	LoadContent(context.Context, string) ([]byte, error)
}, work PanelTaskSpec) (json.RawMessage, map[string]any, map[string]any, error) {
	refs := []struct{ ref, digest string }{{work.InputRef, work.InputDigest}, {work.InputSchemaRef, work.InputSchemaDigest}, {work.OutputSchemaRef, work.OutputSchemaDigest}}
	data := make([][]byte, len(refs))
	for i, item := range refs {
		value, err := loader.LoadContent(ctx, item.ref)
		if err != nil {
			return nil, nil, nil, err
		}
		sum := sha256.Sum256(value)
		if hex.EncodeToString(sum[:]) != item.digest {
			return nil, nil, nil, coordledger.ErrConflict
		}
		data[i] = value
	}
	var inputSchema, outputSchema map[string]any
	if err := json.Unmarshal(data[1], &inputSchema); err != nil {
		return nil, nil, nil, coordledger.ErrConflict
	}
	if err := json.Unmarshal(data[2], &outputSchema); err != nil {
		return nil, nil, nil, coordledger.ErrConflict
	}
	if !json.Valid(data[0]) {
		return nil, nil, nil, coordledger.ErrConflict
	}
	compiled, err := jschema.Compile(inputSchema)
	if err != nil {
		return nil, nil, nil, coordledger.ErrConflict
	}
	if _, err := compiled.ValidateJSONBytes(data[0]); err != nil {
		return nil, nil, nil, coordledger.ErrConflict
	}
	return json.RawMessage(data[0]), inputSchema, outputSchema, nil
}
