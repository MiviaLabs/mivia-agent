package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
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
	return p.ensure(ctx, member.CoordinatorRunID, member.TaskID, member.Work, false)
}

func (p PanelCoordinator) EnsureTerminalMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	return p.ensure(ctx, member.CoordinatorRunID, member.TaskID, member.Work, true)
}

func (p PanelCoordinator) JoinMember(ctx context.Context, attemptID, memberID string, handle *coordinator.RunHandle) (*coordinator.RunResult, error) {
	if _, err := p.member(ctx, attemptID, memberID); err != nil {
		return nil, err
	}
	return p.inner.Join(p.childContext(ctx), handle)
}

func (p PanelCoordinator) ResumeMember(ctx context.Context, attemptID, memberID string) (*coordinator.RunHandle, error) {
	member, err := p.member(ctx, attemptID, memberID)
	if err != nil {
		return nil, err
	}
	return p.inner.ResumeInterruptedRun(p.childContext(ctx), member.CoordinatorRunID)
}

func (p PanelCoordinator) CancelMember(ctx context.Context, attemptID, memberID string, handle *coordinator.RunHandle) error {
	if _, err := p.member(ctx, attemptID, memberID); err != nil {
		return err
	}
	return p.inner.Cancel(p.childContext(ctx), handle)
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

func (p PanelCoordinator) ensure(ctx context.Context, runID, taskID string, work PanelTaskSpec, terminal bool) (*coordinator.RunHandle, error) {
	req, err := p.request(ctx, runID, taskID, work)
	if err != nil {
		return nil, err
	}
	if terminal {
		return p.inner.EnsureTerminalSingleTaskRun(p.childContext(ctx), req, coordledger.TaskStatusCanceled)
	}
	return p.inner.EnsureSingleTaskRun(p.childContext(ctx), req)
}

func (p PanelCoordinator) request(ctx context.Context, runID, taskID string, work PanelTaskSpec) (coordinator.EnsureRunRequest, error) {
	if p.repo == nil {
		return coordinator.EnsureRunRequest{}, fmt.Errorf("panel repository is nil")
	}
	if err := work.Validate(); err != nil {
		return coordinator.EnsureRunRequest{}, err
	}
	input, inputSchema, outputSchema, err := panelWorkContent(ctx, p.repo, work)
	if err != nil {
		return coordinator.EnsureRunRequest{}, err
	}
	task := subagents.Task{ID: taskID, Name: work.TaskName, Input: input, InputSchema: inputSchema, OutputSchema: outputSchema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model}
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
	return json.RawMessage(data[0]), inputSchema, outputSchema, nil
}
