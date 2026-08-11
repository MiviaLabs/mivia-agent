package localengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
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
	if run.Status != workflowledger.RunStatusDeliveryPending && run.Status != workflowledger.RunStatusDeliveryFailed {
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
	holder, release, err := e.claimDelivery(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	defer release()
	ctx = workflowledger.ContextWithClaimHolder(ctx, holder)
	// Refresh the durable on-disk trace after delivery settles (success,
	// failure, or refusal), so .mivia/runs carries the delivery hint.
	defer e.writeRunTrace(runID)
	return e.publishDelivery(ctx, run, snapshot, policy)
}

func (e *Engine) claimDelivery(ctx context.Context, runID string) (string, func(), error) {
	holder := "wfdel-" + randomToken(5)
	// Never clear a held claim: the holder may be another host mid-publish
	// (clearing would let both hosts publish to the same branch) or a
	// crashed deliverer. In-process deliveries are already serialized by the
	// delivering map, so a held claim here is cross-host: refuse and let the
	// operator settle it (the CLI's workflow deliver takes over an EXPIRED
	// claim via lease; use --force to bypass the lease explicitly).
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, workflowledger.ErrClaimHeld) {
			return "", nil, fmt.Errorf("workflow run %q is being delivered by another host or has a fresh delivery claim; retry after it settles (mivia workflow deliver --force takes over an expired claim)", runID)
		}
		return "", nil, err
	}
	return holder, func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }, nil
}

func (e *Engine) publishDelivery(ctx context.Context, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot, policy delivery.Policy) (agenttools.DeliverResult, error) {
	runID := run.RunID
	ctx = workflowledger.ContextWithRunID(ctx, runID)
	repo := e.ctrlRepo()
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
	// Resolve the run's delivery workspace and verify its real git directory.
	// The CLI path does the same (workflow_deliver.go: Resolve + VerifyGitDir);
	// an empty GitDir would make pinnedEnv emit GIT_DIR= and every git command
	// would fail against an invalid empty path.
	gitCtx, err := e.deliveryGitCtx(ctx, run)
	if err != nil {
		// A deliveryGitCtx refusal is permanent (same contract as delivery.Deliver
		// refusals): settle the run to delivery_failed so it does not wedge in
		// delivery_pending forever.
		if delivery.IsRefusal(err) {
			if fresh, getErr := repo.GetRun(ctx, runID); getErr == nil {
				_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil)
			}
			return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, nil
		}
		return agenttools.DeliverResult{}, err
	}
	// The engine has no event bus today (Engine carries no bus field), so the
	// delivery Stage callback is left nil here and delivery.Deliver treats it
	// as a no-op; the CLI deliver path is the primary stage consumer. When the
	// engine gains a bus, wire Stage to publish events.Event{Kind:
	// events.KindWorkflowDeliveryStage, Name: stage, Detail: detail} per call.
	dreq := delivery.Request{
		RunID: runID, WorkflowDigest: run.WorkflowDigest, Policy: policy,
		Inputs: snapshot.Inputs, BaseCommit: run.BaseCommit,
		Branch: "wf/" + run.WorktreeName, GitCtx: gitCtx,
		OriginURL: run.RemoteURL,
	}
	result, err := delivery.Deliver(deliveryCtx, repo, git, pr, dreq)
	if err != nil {
		if delivery.IsRefusal(err) {
			if fresh, getErr := repo.GetRun(ctx, runID); getErr == nil {
				_ = repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil)
			}
			return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, nil
		}
		res, handled, rerr := routePRMetadataRepair(ctx, repo, runID, policy, err)
		if handled {
			return res, nil
		}
		if rerr != nil {
			return agenttools.DeliverResult{}, rerr
		}
		return agenttools.DeliverResult{}, err
	}
	fresh, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := time.Now()
		if err := repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return agenttools.DeliverResult{}, err
		}
		// Delivery completion settles outside the controller (which parked at
		// delivery_pending and emitted no run_finished), so publish the
		// terminal event here.
		emitDeliveredRunFinished(runID)
	}
	return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusSucceeded), URL: result.URL, Mode: result.Mode}, nil
}

// routePRMetadataRepair routes a PR-metadata delivery failure back into the
// workflow when the policy names a repair step. A PR-metadata failure is a
// condition in the change: the agent's title or summary violates the workspace
// pr-title policy, so the agent can fix the metadata and the run returns to the
// step the workflow names in delivery.on_pr_metadata_failure (which defaults to
// on_failure). It is never a refusal and never a transport fault. The boolean
// reports whether the failure was routed (handled); a non-nil error means the
// routing itself failed and the caller must surface it.
func routePRMetadataRepair(ctx context.Context, repo workflowledger.Repository, runID string, policy delivery.Policy, err error) (agenttools.DeliverResult, bool, error) {
	if !delivery.IsPRMetadataError(err) || policy.OnPRMetadataFailure == "" {
		return agenttools.DeliverResult{}, false, nil
	}
	if rerr := delivery.ReopenForRepair(ctx, repo, runID, policy.OnPRMetadataFailure, policy.MaxRepairs, err, io.Discard); rerr != nil {
		return agenttools.DeliverResult{}, false, rerr
	}
	fresh, gerr := repo.GetRun(ctx, runID)
	if gerr != nil {
		return agenttools.DeliverResult{}, false, gerr
	}
	return agenttools.DeliverResult{RunID: runID, Status: string(fresh.Status)}, true, nil
}

// deliveryGitCtx resolves the run's delivery workspace and verifies its real
// git directory, mirroring the CLI's workflow deliver path (workflowspace.
// Resolve + delivery.VerifyGitDir). The engine records the worktree identity
// at start/resume; runs admitted by another engine (or before the identity was
// recorded) are resolved from the durable run record. A run without a recorded
// worktree cannot publish and is refused permanently.
func (e *Engine) deliveryGitCtx(ctx context.Context, run workflowledger.RunSnapshot) (delivery.GitContext, error) {
	if run.WorktreeName == "" {
		return delivery.GitContext{}, &delivery.RefusalError{Reason: fmt.Sprintf("workflow run %q has no recorded worktree; delivery requires a run worktree", run.RunID)}
	}
	identity, ok := e.worktreeIdentity(run.RunID)
	if !ok || identity.Root == "" || identity.MainRoot == "" {
		var err error
		identity, err = workflowspace.Resolve(ctx, e.WorkspaceRoot, workflowspace.Identity{
			BaseRef: run.BaseRef, BaseCommit: run.BaseCommit,
			WorktreeName: run.WorktreeName, Branch: "wf/" + run.WorktreeName,
		})
		if err != nil {
			return delivery.GitContext{}, &delivery.RefusalError{Reason: "resolve delivery workspace: " + err.Error()}
		}
	}
	gitDir, err := delivery.VerifyGitDir(ctx, identity.MainRoot, run.WorktreeName, identity.Root)
	if err != nil {
		return delivery.GitContext{}, err
	}
	return delivery.GitContext{Dir: identity.Root, GitDir: gitDir}, nil
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

// progressSink is the package progress sink for localengine terminal
// operations. Cancel and delivery-completion settle outside a controller (the
// only other progress source), so those paths publish through this hook.
// Hosts wire it once at startup with SetProgressSink, typically with a bus
// adapter such as NewBusProgressSink; nil disables publishing.
var progressSink controller.ProgressSink

// SetProgressSink wires the package progress sink. Call it once at startup,
// before any run, from a single goroutine. A nil sink disables publishing.
func SetProgressSink(s controller.ProgressSink) {
	progressSink = s
}

// NewBusProgressSink adapts a controller progress sink to an events.Bus: each
// terminal progress event is published as one events.Event with the workflow
// kind mapping and run/step attribution, mirroring the session engine's
// workflowBusProgressSink adapter.
func NewBusProgressSink(bus *events.Bus) controller.ProgressSink {
	return busProgressSink{bus: bus}
}

// busProgressSink publishes controller progress events onto an events.Bus.
type busProgressSink struct {
	bus *events.Bus
}

// Emit publishes one controller progress event onto the bus.
func (s busProgressSink) Emit(e controller.ProgressEvent) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Kind:      localProgressKind(e.Kind),
		Timestamp: e.Timestamp,
		Name:      "workflow",
		Detail:    e.Detail,
		AgentTask: e.TaskID,
		AgentName: "workflow:" + e.StepID,
		Metadata: map[string]string{
			"run_id":             e.RunID,
			"step":               e.StepID,
			"attempt":            strconv.Itoa(e.AttemptNo),
			"coordinator_run_id": e.CoordinatorRunID,
			"task_id":            e.TaskID,
		},
	})
}

// localProgressKind maps one localengine terminal progress kind onto the
// session event kind. Unrecognised kinds fall back to a heartbeat tick.
func localProgressKind(k controller.ProgressKind) events.Kind {
	switch k {
	case controller.ProgressStepCompleted:
		return events.KindWorkflowStepCompleted
	case controller.ProgressRunFinished, controller.ProgressRunFailed:
		return events.KindWorkflowRunFinished
	default:
		return events.KindWorkflowStepHeartbeat
	}
}

// emitProgress delivers one terminal progress event to the package progress
// sink. A nil sink makes the call a no-op.
func emitProgress(e controller.ProgressEvent) {
	if progressSink == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	progressSink.Emit(e)
}

// emitDeliveredRunFinished publishes run_finished(succeeded) after delivery
// CASes the run to succeeded.
func emitDeliveredRunFinished(runID string) {
	emitProgress(controller.ProgressEvent{
		Kind: controller.ProgressRunFinished, RunID: runID, Detail: "succeeded",
	})
}

// emitCanceledAttempts publishes one step_completed(canceled) per attempt an
// operator cancel settled.
func emitCanceledAttempts(runID string, attempts []workflowledger.StepAttempt) {
	for _, attempt := range attempts {
		emitProgress(controller.ProgressEvent{
			Kind:             controller.ProgressStepCompleted,
			RunID:            runID,
			StepID:           attempt.StepID,
			AttemptNo:        attempt.AttemptNo,
			TaskID:           attempt.TaskID,
			CoordinatorRunID: attempt.CoordinatorRunID,
			Detail:           "canceled",
		})
	}
}
