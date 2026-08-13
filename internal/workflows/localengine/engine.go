// Package localengine provides an in-process workflow Engine for agent tools.
// It reuses controller, ledger, definition, compiler, and delivery packages.
// Integration tests inject a scripted AgentStepRunner; production hosts may
// inject a coordinator-backed runner.
package localengine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/processservices"
	workflowspace "github.com/MiviaLabs/mivia-agent/internal/workflows/workspace"
)

// Engine runs workflows in-process against a shared ledger repository.
type Engine struct {
	// WorkspaceRoot is the source workspace for discovery and compilation.
	WorkspaceRoot string
	// Repo is the shared workflow ledger. Required.
	Repo workflowledger.Repository
	// NewRunner builds the agent-step runner for one admitted run.
	// Required for agent steps; a nil NewRunner fails closed (no fake success).
	NewRunner func() controller.AgentStepRunner
	// AgentRegistry supplies immutable agent definitions for panel admission.
	// Panel work fails closed when the registry cannot resolve a member.
	AgentRegistry *agents.AgentRegistry
	// NewRunID mints run IDs. Nil uses a secure random wfr- id.
	NewRunID func() string
	// PanelLimiter is the process-wide local actor limiter supplied by the host.
	// A nil value uses the shared workflow process service.
	PanelLimiter *controller.PanelActorLimiter
	// Git and PR are optional delivery adapters.
	Git delivery.GitRunner
	PR  delivery.PRClient
	// DeliveryTimeout bounds one deliver call. Zero uses 2 minutes.
	DeliveryTimeout time.Duration

	mu           sync.Mutex
	active       map[string]*activeRun
	interrupting map[string]uint
	resuming     map[string]chan struct{}
	admitting    map[string]chan struct{}
	fence        *abandonFence
	// worktrees records the resolved git worktree identity per run (Root +
	// MainRoot + admission pins), so delivery can pin GitCtx to the run's real
	// git directory instead of the caller checkout. Recorded at start and
	// resume; delivery falls back to resolving from the durable run record.
	worktrees map[string]workflowspace.Identity
	// delivering tracks in-process deliveries per run so two concurrent
	// workflow_deliver tool calls on the same run cannot both publish to the
	// shared git workspace (a live claim must never be force-cleared by a
	// sibling delivery in the same process).
	delivering map[string]string
}

const (
	runClaimLease = workflowledger.DefaultClaimLease
)

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	ctrl   *controller.LinearController
}

// ctrlRepo returns the fenced repository used by controllers so Interrupt can
// abandon a run without the dying goroutine settling it to canceled/failed.
func (e *Engine) ctrlRepo() workflowledger.Repository {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fence == nil {
		e.fence = newAbandonFence(e.Repo)
	}
	return e.fence
}

func (e *Engine) panelLimiter() *controller.PanelActorLimiter {
	if e.PanelLimiter != nil {
		return e.PanelLimiter
	}
	return processservices.PanelLimiter()
}

// Start implements agenttools.Engine.
func (e *Engine) Start(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	if e == nil || e.Repo == nil {
		return agenttools.StartResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	if req.Resume {
		return e.resume(ctx, req)
	}
	return e.startNew(ctx, req)
}

func (e *Engine) startNew(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	compiled, raw, baseDir, inputs, inputSnapshot, err := e.loadAndValidateWorkflow(req)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID := e.newRunID()
	if key := strings.TrimSpace(req.InvocationKey); key != "" {
		runID = agenttools.InvocationRunID(key)
		if existing, getErr := e.Repo.GetRun(ctx, runID); getErr == nil {
			if result, resumed, resumeErr := e.resumeExistingInvocation(ctx, existing, req); resumed || resumeErr != nil {
				return result, resumeErr
			}
			return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
		} else if !errors.Is(getErr, workflowledger.ErrNotFound) {
			return agenttools.StartResult{}, getErr
		}
		owner, release := e.beginInvocationAdmission(runID)
		if !owner {
			select {
			case <-release:
			case <-ctx.Done():
				return agenttools.StartResult{}, ctx.Err()
			}
			existing, getErr := e.Repo.GetRun(ctx, runID)
			if getErr != nil {
				return agenttools.StartResult{}, fmt.Errorf("invocation %q did not admit run %q: %w", key, runID, getErr)
			}
			return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
		}
		defer e.finishInvocationAdmission(runID, release)
	}
	ctrl, admission, err := e.newRunController(compiled, raw, baseDir, inputs, inputSnapshot, runID, req.InvocationKey)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	// Create (or validate) the run's git worktree and record the identity on
	// the engine so workflow_deliver resolves the run's real git directory.
	if identity, ok := e.ensureRunWorktree(ctx, runID, nil); ok {
		admission.BaseRef, admission.BaseCommit, admission.OriginBaseCommit, admission.WorktreeName = identity.BaseRef, identity.BaseCommit, identity.OriginBaseCommit, identity.WorktreeName
		if compiled.Delivery != nil && compiled.DeliveryActive() {
			url, uerr := resolveOriginURL(ctx, identity, compiled.Delivery.Base)
			if uerr != nil {
				return agenttools.StartResult{}, fmt.Errorf("resolve delivery origin: %w", uerr)
			}
			admission.RemoteURL = url
		}
		// Pin the run's git context for the fail-fast diff-size gate. This is
		// the FRESH-start path: stacking chunk runs are fresh engine starts,
		// so without it the gate would never fire for them.
		if serr := ctrl.WireGitContext(identity.MainRoot, identity.WorktreeName, identity.Root); serr != nil {
			return agenttools.StartResult{}, serr
		}
	} else if base, commit, wt, rerr := resolveLocalIdentity(e.WorkspaceRoot, runID); rerr == nil {
		admission.BaseRef, admission.BaseCommit, admission.WorktreeName = base, commit, wt
	}
	if err := ctrl.SetAdmission(admission); err != nil {
		return agenttools.StartResult{}, err
	}
	created, err := ctrl.StartNew(ctx)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	if !created {
		existing, getErr := e.Repo.GetRun(ctx, runID)
		if getErr != nil {
			return agenttools.StartResult{}, getErr
		}
		return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
	}
	_ = req.AllowPublish // publication is a separate deliver step for tools
	// Durable local trace: create .mivia/runs + admission summary; fail-soft.
	e.writeRunTrace(runID)
	e.launch(ctrl)
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: runID, Status: string(run.Status), Workflow: compiled.Name}, nil
}

func (e *Engine) beginInvocationAdmission(runID string) (bool, chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.admitting == nil {
		e.admitting = make(map[string]chan struct{})
	}
	if done, ok := e.admitting[runID]; ok {
		return false, done
	}
	done := make(chan struct{})
	e.admitting[runID] = done
	return true, done
}

func (e *Engine) finishInvocationAdmission(runID string, done chan struct{}) {
	e.mu.Lock()
	if e.admitting[runID] == done {
		delete(e.admitting, runID)
		close(done)
	}
	e.mu.Unlock()
}

func (e *Engine) resume(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	resumeDone, err := e.reserveResume(req.RunID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	defer e.finishResume(req.RunID, resumeDone)
	run, err := e.Repo.GetRun(ctx, req.RunID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return agenttools.StartResult{}, fmt.Errorf("workflow run %q not found", req.RunID)
		}
		return agenttools.StartResult{}, err
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return agenttools.StartResult{}, fmt.Errorf("workflow run %q is waiting for delivery; call workflow_deliver", req.RunID)
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		// Terminal runs are not resumed. Callers must start a new run or deliver.
		return agenttools.StartResult{}, fmt.Errorf("workflow run %q is terminal (status %s); resume requires a non-terminal run", req.RunID, run.Status)
	}
	if !workflowledger.IsResumableRunStatus(run.Status) {
		return agenttools.StartResult{}, fmt.Errorf("workflow run %q status %s is not resumable", req.RunID, run.Status)
	}
	if err := e.prepareResumeWorktree(ctx, run); err != nil {
		return agenttools.StartResult{}, err
	}
	raw, err := e.Repo.GetRunSnapshot(ctx, req.RunID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	ctrl, err := e.buildResumeController(ctx, req, run, raw)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	// Claim-liveness probe: the run may be executing on ANOTHER host even
	// though this engine is not its executor. Per-step claims mean the claim
	// is only held while a step runs, so this probe is the resume-time
	// exclusion check (see probeResumeClaim).
	if err := e.probeResumeClaim(ctx, req.RunID, ctrl.Holder, req.Force); err != nil {
		return agenttools.StartResult{}, err
	}
	// The probe claim uses the controller's own holder and is deliberately
	// kept: the first Advance refreshes it (same-holder refresh), so the run
	// stays exclusively ours across the Start handoff. If Start fails, the
	// claim is released so the run is not left claimed by a dead attempt.
	if err := ctrl.Start(ctx); err != nil {
		_ = e.Repo.ReleaseRun(context.Background(), req.RunID, ctrl.Holder)
		return agenttools.StartResult{}, err
	}
	e.launch(ctrl)
	// Durable local trace for the resumed run (see startNew).
	e.writeRunTrace(req.RunID)
	fresh, err := e.Repo.GetRun(ctx, req.RunID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: req.RunID, Status: string(fresh.Status), Workflow: run.WorkflowName, Resumed: true}, nil
}

// probeResumeClaim acquires the run claim for resume with the controller's own
// holder. Non-force resumes refuse a claimed run; force-resume clears only a
// claim that is actually held, and only once - a second ErrClaimHeld means
// another executor is mid-step right now, so it refuses too. A blind clear
// would let two hosts execute the same run and duplicate every agent step.
func (e *Engine) probeResumeClaim(ctx context.Context, runID, holder string, force bool) error {
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if errors.Is(err, workflowledger.ErrClaimHeld) && force {
			if err := e.Repo.TakeoverExpiredRunClaim(ctx, runID, holder, runClaimLease); errors.Is(err, workflowledger.ErrClaimNotHeld) {
				return e.Repo.ClaimRun(ctx, runID, holder)
			} else if err != nil {
				return err
			}
		} else if errors.Is(err, workflowledger.ErrClaimHeld) {
			if takeoverErr := e.Repo.TakeoverExpiredRunClaim(ctx, runID, holder, runClaimLease); takeoverErr == nil {
				return nil
			} else if errors.Is(takeoverErr, workflowledger.ErrClaimNotHeld) {
				return e.Repo.ClaimRun(ctx, runID, holder)
			}
			return fmt.Errorf("workflow run %q is executing on another host; wait for its lease to expire or force-resume", runID)
		} else {
			return err
		}
	}
	return nil
}

// buildResumeController rebuilds a fresh controller for an interrupted run
// from its durable snapshot: parse + compile the stored definition, restore
// the admitted inputs, clear the abandon fence so the new controller may
// write again after Interrupt, and re-apply the run's admission pins.
func (e *Engine) buildResumeController(ctx context.Context, req agenttools.StartRequest, run workflowledger.RunSnapshot, raw []byte) (*controller.LinearController, error) {
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if run.SnapshotDigest == "" || run.SnapshotDigest != workflowledger.SnapshotDigest(raw) {
		return nil, fmt.Errorf("workflow snapshot digest does not match the admitted snapshot")
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return nil, err
	}
	compiled, err := compiler.CompileForResume(&wf)
	if err != nil {
		return nil, err
	}
	// A stacking run EXECUTES the synthesized graph; rebuild it here so the
	// resumed runtimes carry the engine-synthesized steps (decompose,
	// chunk_plan_validate) and admission re-validates them with their pinned
	// routing snapshots instead of refusing a digest-less step.
	compiled, err = compiler.SynthesizeStacking(compiled)
	if err != nil {
		return nil, err
	}
	if snapshot.DefinitionDigest != run.WorkflowDigest {
		return nil, fmt.Errorf("workflow definition digest does not match the admitted definition")
	}
	inputs := make(map[string]any, len(snapshot.Inputs))
	for k, v := range snapshot.Inputs {
		inputs[k] = v
	}
	// Clear abandon fence so a fresh controller may write again after Interrupt.
	// Must use clearAbandon (holds fence.mu); bare delete races with isAbandoned.
	_ = e.ctrlRepo()
	e.mu.Lock()
	if e.fence != nil {
		e.fence.clearAbandon(req.RunID)
	}
	e.mu.Unlock()
	// Step schemas are pinned in the admitted snapshot (SchemaVersion 1 carries
	// them as snapshot.Schemas); rebuild the runtimes from those bytes so a
	// resumed run enforces the output_schema admitted at start, never a schema
	// changed, deleted, or renamed on disk after admission (CLI parity with
	// loadStepReferences' prior-snapshot path). No filesystem reads on resume.
	steps, err := buildStepRuntimesFromSnapshot(compiled, snapshot.Schemas)
	if err != nil {
		return nil, err
	}
	ctrl, err := controller.NewLinearController(e.ctrlRepo(), e.runner(), compiled, steps, inputs, req.RunID, raw)
	if err != nil {
		return nil, err
	}
	if err := ctrl.SetPanelLimiter(e.panelLimiter()); err != nil {
		return nil, err
	}
	// Pin the run's git context for the fail-fast diff-size gate (best-effort).
	if ident, ok := e.worktreeIdentity(req.RunID); ok {
		if serr := ctrl.WireGitContext(ident.MainRoot, ident.WorktreeName, ident.Root); serr != nil {
			return nil, serr
		}
	}
	// Every field sameAdmission compares comes from the record. The invocation
	// key and the workflow digest were missing, so a run started with a key,
	// or admitted before the definition types gained a field, could not resume.
	admission := controller.Admission{
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit, OriginBaseCommit: run.OriginBaseCommit,
		WorktreeName: run.WorktreeName, InputDigest: run.InputDigest, DeadlineAt: run.DeadlineAt, RemoteURL: run.RemoteURL,
		InvocationKey: run.InvocationKey, WorkflowDigest: run.WorkflowDigest,
	}
	if err := ctrl.SetAdmission(admission); err != nil {
		return nil, err
	}
	if req.Force {
		if err := ctrl.SetForceResume(true); err != nil {
			return nil, err
		}
	}
	return ctrl, nil
}

func (e *Engine) launch(ctrl *controller.LinearController) {
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*activeRun)
	}
	prev, hadPrev := e.active[ctrl.RunID]
	// Register the new handle before waiting so concurrent Interrupt sees it.
	e.active[ctrl.RunID] = &activeRun{cancel: cancel, done: done, ctrl: ctrl}
	e.mu.Unlock()
	if hadPrev {
		// Never wait for prev.done while holding e.mu (deadlock with Interrupt).
		prev.cancel()
		<-prev.done
	}
	go func() {
		defer close(done)
		_, runErr := controller.RunWithCancelReconciliationRetry(runCtx, ctrl.Run)
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, controller.ErrPanelMembersComplete) && !errors.Is(runErr, controller.ErrCancelReconciliationPending) {
			// Surface the failure instead of silently dropping it: a
			// claim-contention or step error that stops the run must not
			// look like a healthy no-op resume. Best-effort settle to
			// failed so the run reaches a terminal state the operator can
			// act on.
			e.settleRunFailure(ctrl.RunID, runErr)
		}
		// Release the resume probe claim if it is still held (e.g. Run
		// exited before the first Advance could claim/refresh it). No-op
		// when the last Advance already released it or another host took
		// the claim.
		_ = e.Repo.ReleaseRun(context.Background(), ctrl.RunID, ctrl.Holder)
		// Final durable trace with the terminal status and delivery hints.
		e.writeRunTrace(ctrl.RunID)
		e.mu.Lock()
		if cur, ok := e.active[ctrl.RunID]; ok && cur.done == done {
			delete(e.active, ctrl.RunID)
		}
		e.mu.Unlock()
	}()
}

// Wait blocks until the background run for runID exits or ctx is done.
func (e *Engine) Wait(ctx context.Context, runID string) error {
	e.mu.Lock()
	active, ok := e.active[runID]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-active.done:
		return nil
	}
}

// Ensure Engine implements agenttools.Engine.
var _ agenttools.Engine = (*Engine)(nil)
