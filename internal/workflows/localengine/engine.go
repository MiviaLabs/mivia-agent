// Package localengine provides an in-process workflow Engine for agent tools.
// It reuses controller, ledger, definition, compiler, and delivery packages.
// Integration tests inject a scripted AgentStepRunner; production hosts may
// inject a coordinator-backed runner.
package localengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
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
	// NewRunID mints run IDs. Nil uses a secure random wfr- id.
	NewRunID func() string
	// Git and PR are optional delivery adapters.
	Git delivery.GitRunner
	PR  delivery.PRClient
	// DeliveryTimeout bounds one deliver call. Zero uses 2 minutes.
	DeliveryTimeout time.Duration

	mu        sync.Mutex
	active    map[string]*activeRun
	admitting map[string]chan struct{}
	fence     *abandonFence
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
	compiled, raw, baseDir, err := e.loadWorkflow(req.Workflow)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	inputs, inputSnapshot, err := validateInputs(req.Inputs, compiled.Inputs)
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
	// Create (or validate) the run's git worktree like the CLI does and record
	// the resulting identity on the engine, so a later workflow_deliver can
	// resolve the run's real git directory. A non-git workspace falls back to
	// the previous no-worktree admission; delivery then refuses with a clear
	// error instead of pinning an empty GIT_DIR.
	if identity, ok := e.ensureRunWorktree(ctx, runID, nil); ok {
		admission.BaseRef, admission.BaseCommit, admission.OriginBaseCommit, admission.WorktreeName = identity.BaseRef, identity.BaseCommit, identity.OriginBaseCommit, identity.WorktreeName
		if compiled.Delivery != nil && compiled.DeliveryActive() {
			url, uerr := resolveOriginURL(ctx, identity, compiled.Delivery.Base)
			if uerr != nil {
				return agenttools.StartResult{}, fmt.Errorf("resolve delivery origin: %w", uerr)
			}
			admission.RemoteURL = url
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
	// Never resume a run this engine is already executing: tearing the live
	// run down to "resume" it would cancel mid-step work and corrupt the
	// interrupted state it was supposed to recover. The caller must wait for
	// the active run to settle first.
	e.mu.Lock()
	_, activeHere := e.active[req.RunID]
	e.mu.Unlock()
	if activeHere {
		return agenttools.StartResult{}, fmt.Errorf("workflow run %q is already executing in this engine; cancel it first", req.RunID)
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
			if err := e.Repo.TakeoverRunClaim(ctx, runID, holder); err != nil {
				return err
			}
		} else if errors.Is(err, workflowledger.ErrClaimHeld) {
			if takeoverErr := e.Repo.TakeoverExpiredRunClaim(ctx, runID, holder, runClaimLease); takeoverErr == nil {
				return nil
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
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return nil, err
	}
	compiled, err := compiler.CompileForResume(&wf)
	if err != nil {
		return nil, err
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
		_, runErr := ctrl.Run(runCtx)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
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

// ensureRunWorktree creates or validates the git worktree for a run, mirroring
// the CLI's workspace selection: a fresh run gets a new worktree via
// workflowspace.Ensure; a resumed run re-validates the recorded worktree and
// recreates it when missing. ok=false means the workspace is not a usable git
// repository or the worktree could not be ensured; callers fall back to the
// previous no-worktree behavior.
func (e *Engine) ensureRunWorktree(ctx context.Context, runID string, recorded *workflowledger.RunSnapshot) (workflowspace.Identity, bool) {
	if e.WorkspaceRoot == "" {
		return workflowspace.Identity{}, false
	}
	if recorded != nil && recorded.WorktreeName != "" {
		recordedIdentity := workflowspace.Identity{
			BaseRef: recorded.BaseRef, BaseCommit: recorded.BaseCommit,
			WorktreeName: recorded.WorktreeName, Branch: "wf/" + recorded.WorktreeName,
		}
		if identity, err := workflowspace.Resolve(ctx, e.WorkspaceRoot, recordedIdentity); err == nil {
			return identity, true
		}
		identity, err := workflowspace.EnsureRecorded(ctx, e.WorkspaceRoot, recordedIdentity)
		if err != nil {
			return workflowspace.Identity{}, false
		}
		return identity, true
	}
	identity, err := workflowspace.Ensure(ctx, e.WorkspaceRoot, runID, workflowspace.IsolationWorktree)
	if err != nil {
		return workflowspace.Identity{}, false
	}
	return identity, identity.WorktreeName != ""
}

// recordWorktree stores the resolved worktree identity for a run so delivery
// can pin the run's git context without re-running vcs discovery.
func (e *Engine) recordWorktree(runID string, identity workflowspace.Identity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.worktrees == nil {
		e.worktrees = make(map[string]workflowspace.Identity)
	}
	e.worktrees[runID] = identity
}

// worktreeIdentity returns the recorded worktree identity for a run.
func (e *Engine) worktreeIdentity(runID string) (workflowspace.Identity, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	identity, ok := e.worktrees[runID]
	return identity, ok
}

// resolveOriginURL records the delivery origin for the immutable admission
// record, mirroring the CLI's workflowDeliveryAdmission: the main repository
// must have an origin remote and the delivery base must sit at the admitted
// base commit. A delivery workflow without a matching origin cannot publish.
func resolveOriginURL(ctx context.Context, identity workflowspace.Identity, base string) (string, error) {
	if identity.MainRoot == "" {
		return "", fmt.Errorf("workflow identity has no main root")
	}
	git := delivery.GitContext{Dir: identity.MainRoot, GitDir: filepath.Join(identity.MainRoot, ".git")}
	origin, err := delivery.RealGit{}.Run(ctx, git, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("workflow requires delivery but the repository has no origin remote: %w", err)
	}
	baseCommit, err := delivery.RealGit{}.Run(ctx, git, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+base+"^{commit}")
	if err != nil || strings.TrimSpace(baseCommit) != identity.BaseCommit {
		return "", fmt.Errorf("delivery base %q is not at the admitted base commit", base)
	}
	return strings.TrimSpace(origin), nil
}

// Ensure Engine implements agenttools.Engine.
var _ agenttools.Engine = (*Engine)(nil)
