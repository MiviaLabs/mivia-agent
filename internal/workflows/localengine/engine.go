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
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
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
	// Store is the shared SQLite store backing the stack task ledger
	// (tasks.NewStore). Required to drive (or verify the drive of) a
	// multi-chunk stacking plan run; a nil Store degrades the engine to the
	// operator drive (`mivia stack drive`) and refuses delivery of an
	// undriven plan run instead of publishing it.
	Store storage.Store
	// NewRunner builds the agent-step runner for one admitted run.
	// Required for agent steps; a nil NewRunner fails closed (no fake success).
	NewRunner func() controller.AgentStepRunner
	// AgentRegistry supplies immutable agent definitions for panel admission
	// and for agent-step routing pins. Panel work fails closed when the
	// registry cannot resolve a member. Agent steps pin and re-verify their
	// definition digests when the registry is set, and keep the legacy
	// synthetic-digest mode when it is nil.
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

// admitInvocation resolves the run ID for req, handling the InvocationKey
// idempotency path: reuse or resume an existing run for the key, or admit
// this call as the run's sole starter. done=true means startNew must return
// (result, err) immediately; otherwise runID is ready and finish must be
// deferred by the caller to release invocation admission.
func (e *Engine) admitInvocation(ctx context.Context, req agenttools.StartRequest) (runID string, result agenttools.StartResult, done bool, err error, finish func()) {
	noop := func() {}
	key := strings.TrimSpace(req.InvocationKey)
	if key == "" {
		return e.newRunID(), agenttools.StartResult{}, false, nil, noop
	}
	runID = agenttools.InvocationRunID(key)
	existing, getErr := e.Repo.GetRun(ctx, runID)
	if getErr == nil {
		if result, resumed, resumeErr := e.resumeExistingInvocation(ctx, existing, req); resumed || resumeErr != nil {
			return runID, result, true, resumeErr, noop
		}
		return runID, agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, true, nil, noop
	} else if !errors.Is(getErr, workflowledger.ErrNotFound) {
		return runID, agenttools.StartResult{}, false, getErr, noop
	}
	owner, release := e.beginInvocationAdmission(runID)
	if !owner {
		select {
		case <-release:
		case <-ctx.Done():
			return runID, agenttools.StartResult{}, false, ctx.Err(), noop
		}
		existing, getErr := e.Repo.GetRun(ctx, runID)
		if getErr != nil {
			return runID, agenttools.StartResult{}, false, fmt.Errorf("invocation %q did not admit run %q: %w", key, runID, getErr), noop
		}
		return runID, agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, true, nil, noop
	}
	return runID, agenttools.StartResult{}, false, nil, func() { e.finishInvocationAdmission(runID, release) }
}

func (e *Engine) startNew(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	compiled, raw, baseDir, inputs, inputSnapshot, err := e.loadAndValidateWorkflow(req)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID, admitResult, done, admitErr, finish := e.admitInvocation(ctx, req)
	if done {
		return admitResult, admitErr
	}
	if admitErr != nil {
		return agenttools.StartResult{}, admitErr
	}
	defer finish()
	ctrl, admission, err := e.newRunController(compiled, raw, baseDir, inputs, inputSnapshot, runID, req.InvocationKey)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	cleanup, err := e.pinNewRunIdentity(ctx, ctrl, compiled, &admission, runID, inputSnapshot)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	// A fresh admission created the run worktree before control returns to us.
	// If a later admission step fails - SetAdmission or StartNew - the worktree
	// must still be removed so the pre-created worktree does not leak (mirrors
	// the CLI's admitted flag + built.Cleanup() pair in workflow_run.go). Once
	// StartNew succeeds the worktree belongs to the run, so the cleanup is
	// disarmed.
	disarmCleanup := cleanup == nil
	defer func() {
		if !disarmCleanup {
			cleanup()
		}
	}()
	if err := ctrl.SetAdmission(admission); err != nil {
		return agenttools.StartResult{}, err
	}
	created, err := ctrl.StartNew(ctx)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	disarmCleanup = true
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

// beginInvocationAdmission acquires the invocation-key admission slot for
// runID, returning owner=true for the sole starter.
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
		snap, runErr := controller.RunWithCancelReconciliationRetry(runCtx, ctrl.Run)
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, controller.ErrPanelMembersComplete) && !errors.Is(runErr, controller.ErrCancelReconciliationPending) {
			// Surface the failure instead of silently dropping it: a
			// claim-contention or step error that stops the run must not
			// look like a healthy no-op resume. Best-effort settle to
			// failed so the run reaches a terminal state the operator can
			// act on.
			e.settleRunFailure(ctrl.RunID, runErr)
		}
		// Drive-before-delivery: a stacking plan run that parks at its
		// delivery_pending success terminal must drive its chunk stack
		// automatically (the CLI path drives in executeWorkflowRun, the
		// session path in sessionAutoDeliveryRepairLoop; the agent-tools
		// engine drives here - see engine_stack.go). Runs in its own
		// goroutine so the run's launch handle (and Wait) return when the
		// controller parks; the drive is bounded by runCtx (engine
		// shutdown/Interrupt cancels it).
		if runErr == nil && snap.Status == workflowledger.RunStatusDeliveryPending {
			go e.stackDriveAfterPark(runCtx, ctrl.RunID)
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
