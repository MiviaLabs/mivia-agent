package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

type resumePrepared struct {
	runID      string
	workflow   string
	root       string
	built      workflowControllerBuild
	closeFn    func()
	finishExec func()
	repo       workflowledger.Repository
	store      *storage.SQLite
	res        *config.Resolved
}

// resumeCLI resumes through the same durable preflight as the command path.
func (e *sessionWorkflowEngine) resumeCLI(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	prepared, err := e.prepareResume(ctx, req)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return e.launchResume(ctx, prepared)
}

func (e *sessionWorkflowEngine) prepareResume(ctx context.Context, req agenttools.StartRequest) (resumePrepared, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return resumePrepared{}, fmt.Errorf("resume requires run_id")
	}
	root := e.root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return resumePrepared{}, err
	}
	configPath := workflowConfigPath(work.Abs, e.configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return resumePrepared{}, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return resumePrepared{}, err
	}
	run, err := repo.GetRun(ctx, req.RunID)
	if err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	if err := refuseNonResumable(run); err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	raw, err := repo.GetRunSnapshot(ctx, req.RunID)
	if err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	snapshot, compiled, inputs, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	if err := validateWorkflowMCPConfigDigest(snapshot, res.MCP); err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	finishExecution, err := beginWorkflowExecution(work.Abs, contextStorePath(work.Abs, res.Subagents), req.RunID)
	if err != nil {
		closeFn()
		return resumePrepared{}, err
	}
	built, err := workflowResumeBuild(work.Abs, res, store, repo, compiled, "", inputs, snapshot.Inputs, snapshot.DefinitionTOML, req.RunID, &snapshot, &run)
	if err != nil {
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	if err := workflowResumeSetAdmission(built); err != nil {
		built.Dispatcher.Close()
		finishExecution()
		closeFn()
		return resumePrepared{}, err
	}
	if err := workflowResumeSetForce(built); err != nil {
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
	return resumePrepared{runID: req.RunID, workflow: run.WorkflowName, root: work.Abs, built: built, closeFn: closeFn, finishExec: finishExecution, repo: repo, store: store, res: res}, nil
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

func (e *sessionWorkflowEngine) launchResume(ctx context.Context, p resumePrepared) (agenttools.StartResult, error) {
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
		p.finishExec()
		if p.built.Dispatcher != nil {
			p.built.Dispatcher.Close()
		}
		p.closeFn()
	}}
	e.mu.Unlock()
	go func() {
		defer close(done)
		// Safety net: releases the preflight handoff claim if the advance
		// wrapper's release below was not reached. Releasing with a stale
		// holder is harmless: ReleaseRun is a no-op when the caller is not the
		// current holder.
		defer releaseWorkflowResumeHandoff(p.repo, p.runID, p.built.Controller)
		sessionAutoDeliveryRepairLoop(runCtx, p.repo, p.root, p.res, p.store, p.runID, func(ctx context.Context) (workflowledger.RunSnapshot, error) {
			snap, err := workflowResumeRun(ctx, p.built)
			// Release the preflight handoff claim BEFORE settling: settle
			// claims the run with its own holder, so a still-held handoff (the
			// controller stopped before its first Advance claimed and released
			// the run) makes the settle a no-op and the run stays running with
			// no cause.
			releaseWorkflowResumeHandoff(p.repo, p.runID, p.built.Controller)
			return snap, err
		})
		// Delivery completion settles outside the controller (which parked at
		// delivery_pending and emitted no run_finished), so publish the terminal
		// event here once delivery actually succeeded.
		e.publishDeliveredRunFinished(context.Background(), p.repo, p.runID)
		e.mu.Lock()
		active := e.active[p.runID]
		delete(e.active, p.runID)
		e.mu.Unlock()
		if active != nil {
			active.closeFn()
		}
	}()
	fresh, err := p.repo.GetRun(ctx, p.runID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: p.runID, Status: string(fresh.Status), Workflow: p.workflow, Resumed: true}, nil
}
