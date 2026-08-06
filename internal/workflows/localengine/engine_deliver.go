package localengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Deliver implements agenttools.Engine.
func (e *Engine) Deliver(ctx context.Context, runID string, allowPublish bool) (agenttools.DeliverResult, error) {
	if e == nil || e.Repo == nil {
		return agenttools.DeliverResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	if !allowPublish {
		return agenttools.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
	}
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return agenttools.DeliverResult{}, fmt.Errorf("workflow run %q not found", runID)
		}
		return agenttools.DeliverResult{}, err
	}
	if run.Status == workflowledger.RunStatusSucceeded {
		return e.replayDelivery(ctx, run)
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		return agenttools.DeliverResult{}, fmt.Errorf("run is not waiting for delivery (status %q)", run.Status)
	}
	return e.deliverPending(ctx, run)
}

func (e *Engine) replayDelivery(ctx context.Context, run workflowledger.RunSnapshot) (agenttools.DeliverResult, error) {
	rec, err := e.Repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		// A succeeded run without a readable delivery record must surface the
		// loss, not silently report success with empty URL/Mode: the CLI
		// replay path propagates this error, and the engine must not diverge.
		return agenttools.DeliverResult{}, fmt.Errorf("replay delivery for %q: %w", run.RunID, err)
	}
	return agenttools.DeliverResult{RunID: run.RunID, Status: string(run.Status), URL: rec.URL, Mode: rec.Mode}, nil
}

func (e *Engine) deliverPending(ctx context.Context, run workflowledger.RunSnapshot) (agenttools.DeliverResult, error) {
	runID := run.RunID
	raw, err := e.Repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	compiled, err := compiler.CompileForResume(&wf)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	policy, ok := delivery.FromCompiled(compiled)
	if !ok {
		return agenttools.DeliverResult{}, fmt.Errorf("workflow delivery policy is not active for run %q", runID)
	}
	// Serialize in-process deliveries per run: two concurrent tool calls must
	// not both publish to the shared workspace branch. The claim probe below
	// still guards cross-host contention, but a sibling call in THIS engine
	// would otherwise clear our live claim mid-publish.
	e.mu.Lock()
	if e.delivering == nil {
		e.delivering = make(map[string]string)
	}
	if holder, busy := e.delivering[runID]; busy {
		e.mu.Unlock()
		return agenttools.DeliverResult{}, fmt.Errorf("workflow run %q delivery already in progress (holder %s)", runID, holder)
	}
	e.delivering[runID] = "in-flight"
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.delivering, runID)
		e.mu.Unlock()
	}()
	release, err := e.claimDelivery(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	defer release()
	return e.publishDelivery(ctx, run, snapshot, policy)
}

func (e *Engine) claimDelivery(ctx context.Context, runID string) (func(), error) {
	holder := "wfdel-" + randomToken(5)
	// Never clear a live claim blindly: another host may be mid-delivery on
	// the same run. Clear only after probing, and only once; a second
	// ErrClaimHeld means that host is still publishing, so refuse.
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			return nil, err
		}
		if err := e.Repo.ClearRunClaim(ctx, runID); err != nil {
			return nil, err
		}
		if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
			if errors.Is(err, workflowledger.ErrClaimHeld) {
				return nil, fmt.Errorf("workflow run %q is being delivered by another host", runID)
			}
			return nil, err
		}
	}
	return func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }, nil
}

func (e *Engine) publishDelivery(ctx context.Context, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot, policy delivery.Policy) (agenttools.DeliverResult, error) {
	runID := run.RunID
	git, pr := e.Git, e.PR
	if git == nil {
		git = delivery.RealGit{}
	}
	if pr == nil {
		pr = delivery.GitHubCLI{}
	}
	timeout := e.DeliveryTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dreq := delivery.Request{
		RunID: runID, WorkflowDigest: run.WorkflowDigest, Policy: policy,
		Inputs: snapshot.Inputs, BaseCommit: run.BaseCommit,
		Branch: "wf/" + run.WorktreeName, GitCtx: delivery.GitContext{Dir: e.WorkspaceRoot},
		OriginURL: run.RemoteURL,
	}
	result, err := delivery.Deliver(deliveryCtx, e.Repo, git, pr, dreq)
	if err != nil {
		if delivery.IsRefusal(err) {
			if fresh, getErr := e.Repo.GetRun(ctx, runID); getErr == nil {
				_ = e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil)
			}
			return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, nil
		}
		return agenttools.DeliverResult{}, err
	}
	fresh, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := time.Now()
		if err := e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return agenttools.DeliverResult{}, err
		}
	}
	return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusSucceeded), URL: result.URL, Mode: result.Mode}, nil
}

// settleRunFailure best-effort settles a run whose execution stopped with a
// non-cancel error. If another holder owns the run (claim contention), it is
// left alone: that holder is the live executor and will settle or continue it.
// An abandoned run is also left non-terminal: Interrupt owns that outcome and
// the run must stay resumable.
func (e *Engine) settleRunFailure(runID string, runErr error) {
	log.Printf("workflow engine: run %s stopped with error: %v", runID, runErr)
	e.mu.Lock()
	abandoned := e.fence != nil && e.fence.isAbandoned(runID)
	e.mu.Unlock()
	if abandoned {
		return // Interrupt owns this run's outcome; keep it non-terminal for resume.
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder := "wfsettle-" + randomToken(5)
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		return // another holder owns the run
	}
	defer func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }()
	fresh, err := e.Repo.GetRun(ctx, runID)
	if err != nil || workflowledger.IsTerminalRunStatus(fresh.Status) {
		return
	}
	_ = e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusFailed, nil)
}
