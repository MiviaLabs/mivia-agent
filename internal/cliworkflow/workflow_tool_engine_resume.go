package cliworkflow

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type resumePrepared struct {
	runID         string
	workflow      string
	root          string
	built         WorkflowControllerBuild
	closeFn       func()
	finishExec    func()
	repo          workflowledger.Repository
	store         *storage.SQLite
	res           *config.Resolved
	compiled      *definition.CompiledWorkflow
	inputs        map[string]any
	inputSnapshot map[string]string
	raw           []byte
}

// resumeCLI resumes through the same durable preflight as the command path.
func (e *sessionWorkflowEngine) resumeCLI(ctx context.Context, req workflowledger.StartRequest) (workflowledger.StartResult, error) {
	prepared, err := e.prepareResume(ctx, req)
	if err != nil {
		return workflowledger.StartResult{}, err
	}
	return e.launchResume(ctx, prepared)
}

func (e *sessionWorkflowEngine) prepareResume(ctx context.Context, req workflowledger.StartRequest) (resumePrepared, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return resumePrepared{}, fmt.Errorf("resume requires run_id")
	}
	res, store, repo, run, snapshot, priorRaw, compiled, inputs, closeFn, err := e.openResumeTarget(ctx, req)
	if err != nil {
		return resumePrepared{}, err
	}
	finishExecution, err := BeginWorkflowExecution(workForResume(e, req), ContextStorePath(workForResume(e, req), res.Subagents), req.RunID)
	if err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	remaining, err := WorkflowRemainingSteps(ctx, repo, req.RunID, compiled)
	if err != nil {
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	built, err := workflowResumeBuild(workForResume(e, req), res, store, repo, compiled, "", inputs, snapshot.Inputs, snapshot.DefinitionTOML, req.RunID, &snapshot, priorRaw, &run, remaining, nil)
	if err != nil {
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	if err := WorkflowResumeSetAdmission(built); err != nil {
		built.Dispatcher.Close()
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	if err := WorkflowResumeSetForce(built); err != nil {
		built.Dispatcher.Close()
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	e.attachWorkflowProgressBus(built.Controller)
	if err := prepareWorkflowResumeExecution(ctx, built, repo, req.RunID, req.Force, io.Discard); err != nil {
		built.Dispatcher.Close()
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	return resumePrepared{runID: req.RunID, workflow: run.WorkflowName, root: workForResume(e, req), built: built, closeFn: closeFn, finishExec: finishExecution, repo: repo, store: store, res: res, compiled: compiled, inputs: inputs, inputSnapshot: snapshot.Inputs, raw: snapshot.DefinitionTOML}, nil
}

// workForResume resolves the engine's workspace root for a resume.
func workForResume(e *sessionWorkflowEngine, req workflowledger.StartRequest) string {
	root := e.root
	if strings.TrimSpace(root) == "" {
		return "."
	}
	return root
}

// openResumeTarget opens the workspace and store for a resume and validates the
// admitted snapshot, returning the resolved config, store, repo, run row,
// snapshot, its raw admission bytes, the compiled workflow and inputs, and the
// store close function.
func (e *sessionWorkflowEngine) openResumeTarget(ctx context.Context, req workflowledger.StartRequest) (*config.Resolved, *storage.SQLite, workflowledger.Repository, workflowledger.RunSnapshot, workflowledger.Snapshot, []byte, *definition.CompiledWorkflow, map[string]any, func(), error) {
	root := workForResume(e, req)
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, workflowledger.RunSnapshot{}, workflowledger.Snapshot{}, nil, nil, nil, nil, err
	}
	configPath := WorkflowConfigPath(work.Abs, e.configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, workflowledger.RunSnapshot{}, workflowledger.Snapshot{}, nil, nil, nil, nil, err
	}
	ApplyPrivacyPolicyFunc(res)
	ApplyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, workflowledger.RunSnapshot{}, workflowledger.Snapshot{}, nil, nil, nil, nil, err
	}
	fail := func(err error) (*config.Resolved, *storage.SQLite, workflowledger.Repository, workflowledger.RunSnapshot, workflowledger.Snapshot, []byte, *definition.CompiledWorkflow, map[string]any, func(), error) {
		closeFn()
		return nil, nil, nil, workflowledger.RunSnapshot{}, workflowledger.Snapshot{}, nil, nil, nil, nil, err
	}
	run, err := repo.GetRun(ctx, req.RunID)
	if err != nil {
		return fail(err)
	}
	if err := refuseNonResumable(run); err != nil {
		return fail(err)
	}
	raw, err := repo.GetRunSnapshot(ctx, req.RunID)
	if err != nil {
		return fail(err)
	}
	snapshot, compiled, inputs, err := ValidateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return fail(err)
	}
	if err := validateWorkflowMCPConfigDigest(req.RunID, snapshot, res.MCP); err != nil {
		return fail(err)
	}
	return res, store, repo, run, snapshot, raw, compiled, inputs, closeFn, nil
}

func refuseNonResumable(run workflowledger.RunSnapshot) error {
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("workflow run %q is waiting for delivery; call workflow_deliver", run.RunID)
	}
	if workflowledger.IsTerminalRunStatus(run.Status) {
		return fmt.Errorf("workflow run %q is terminal (status %s); resume requires a non-terminal run", run.RunID, run.Status)
	}
	return nil
}

func (e *sessionWorkflowEngine) launchResume(ctx context.Context, p resumePrepared) (workflowledger.StartResult, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*sessionActiveRun)
	}
	var runner *controller.CoordinatorRunner
	if p.built.Controller != nil {
		runner, _ = p.built.Controller.Runner.(*controller.CoordinatorRunner)
	}
	e.active[p.runID] = &sessionActiveRun{cancel: cancel, done: done, runner: runner, closeFn: func() {
		if p.built.Dispatcher != nil {
			p.built.Dispatcher.Close()
		}
		p.closeFn()
	}}
	e.mu.Unlock()
	go func() {
		// Safety net: releases the preflight handoff claim if the advance
		// wrapper's release below was not reached. Releasing with a stale
		// holder is harmless: ReleaseRun is a no-op when the caller is not the
		// current holder.
		defer releaseWorkflowResumeHandoff(p.repo, p.runID, p.built.Controller)
		SessionAutoDeliveryRepairLoopFunc(runCtx, p.repo, p.root, p.res, p.store, p.runID, func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			return controller.RunWithCancelReconciliationRetry(ctx, func(ctx context.Context) (workflowledger.RunSnapshot, error) {
				snap, err := WorkflowResumeRun(ctx, p.built)
				// Release the preflight handoff claim BEFORE settling: settle
				// claims the run with its own holder, so a still-held handoff
				// (the controller stopped before its first Advance claimed and
				// released the run) makes the settle a no-op and the run stays
				// running with no cause.
				releaseWorkflowResumeHandoff(p.repo, p.runID, p.built.Controller)
				return snap, err
			})
		}, func(ctx context.Context) (bool, error) {
			// resume mirror of the CLI run entry point's
			// drive-before-delivery ordering: a stacking plan run that settles
			// delivery_pending with a multi-chunk plan drives its stack before
			// the plan run is delivered. The hook ctx is the bounded session
			// attempt ctx (workflowAutoDeliveryAttemptTimeout), so a stuck
			// drive can be stopped instead of holding the execution flock.
			// Publish authority derives from the merge policy like the session
			// hook: an approve stack pauses for the per-chunk deliver grant.
			if p.compiled == nil {
				return false, nil // no compiled workflow data: nothing to stack
			}
			return maybeDriveSettledStack(ctx, &PreparedWorkflowRun{
				Root: p.root, Res: p.res, Store: p.store, Repo: p.repo,
				Compiled: p.compiled, Inputs: p.inputs, InputSnapshot: p.inputSnapshot,
				RefBase: "", Raw: p.raw,
			}, p.runID, StackingDriveAllowPublishFunc(p.compiled), io.Discard, io.Discard)
		}, compiledDeliverPlanRun(p.compiled))
		// Delivery completion settles outside the controller (which parked at
		// delivery_pending and emitted no run_finished), so publish the terminal
		// event here once delivery actually succeeded.
		e.publishDeliveredRunFinished(context.Background(), p.repo, p.runID)
		// A plan run whose own publication is disabled settles succeeded with no
		// delivery record; publish its terminal event from the stack-ledger
		// marker.
		e.publishSkippedPlanRunFinished(context.Background(), p.store, p.repo, p.runID)
		// Release the flock before close(done): a waiter woken by done (Deliver,
		// Cancel) must be able to contend for it immediately - see
		// LaunchStartedWorkflow's matching comment.
		p.finishExec()
		close(done)
		e.mu.Lock()
		active := e.active[p.runID]
		delete(e.active, p.runID)
		e.mu.Unlock()
		if active != nil {
			active.closeGuarded()
		}
	}()
	fresh, err := p.repo.GetRun(ctx, p.runID)
	if err != nil {
		return workflowledger.StartResult{}, err
	}
	return workflowledger.StartResult{RunID: p.runID, Status: string(fresh.Status), Workflow: p.workflow, Resumed: true}, nil
}
