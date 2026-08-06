package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// sessionWorkflowEngine is the production Engine for chat-session workflow tools.
// New runs use the full CLI admission path (providers, worktrees, coordinator)
// and return the run ID without waiting for terminal state.
type sessionWorkflowEngine struct {
	root       string
	configPath string
	// Local backs cancel/deliver ledger paths when the session store is open.
	// It must not run agent steps without an explicit NewRunner (fail-closed).
	Local *localengine.Engine

	mu     sync.Mutex
	active map[string]*sessionActiveRun
}

type sessionActiveRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	closeFn func()
}

// newSessionWorkflowEngine builds the chat-session workflow engine over repo.
func newSessionWorkflowEngine(root, configPath string, repo workflowledger.Repository) *sessionWorkflowEngine {
	return &sessionWorkflowEngine{
		root:       root,
		configPath: configPath,
		Local: &localengine.Engine{
			WorkspaceRoot: root,
			Repo:          repo,
		},
		active: make(map[string]*sessionActiveRun),
	}
}

// Start implements agenttools.Engine.
// New runs and resumes use the full CLI admission path only. There is no
// silent fallback to a scripted local runner: missing provider config fails.
func (e *sessionWorkflowEngine) Start(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	if e == nil {
		return agenttools.StartResult{}, fmt.Errorf("workflow engine is nil")
	}
	if req.Resume {
		return e.resumeCLI(ctx, req)
	}
	return e.startCLI(ctx, req)
}

func (e *sessionWorkflowEngine) startCLI(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	rawInputs := inputsToRawFlags(req.Inputs)
	prepared, err := prepareWorkflowRun(req.Workflow, e.root, e.configPath, rawInputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, prepared.inputs, prepared.inputSnapshot, prepared.raw, runID, nil, nil)
	if err != nil {
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	if err := workflowRunSetAdmission(built); err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	if err := built.Controller.Start(ctx); err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*sessionActiveRun)
	}
	e.active[runID] = &sessionActiveRun{
		cancel: cancel, done: done,
		closeFn: func() {
			finishExecution()
			built.Dispatcher.Close()
			prepared.closeFn()
		},
	}
	e.mu.Unlock()
	allowPublish := req.AllowPublish
	go func() {
		defer close(done)
		snap, err := built.Controller.Run(runCtx)
		if err == nil && snap.Status == workflowledger.RunStatusDeliveryPending && allowPublish {
			_ = deliverRunWithStore(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, runID, true, io.Discard, io.Discard)
		}
		e.mu.Lock()
		active := e.active[runID]
		delete(e.active, runID)
		e.mu.Unlock()
		if active != nil {
			active.closeFn()
		}
	}()
	run, err := prepared.repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: runID, Status: string(run.Status), Workflow: req.Workflow}, nil
}

// resumeCLI resumes a non-terminal run through the real CLI controller path
// (same admission wiring as `mivia workflow resume --force`). It never falls
// back to a scripted step runner.
func (e *sessionWorkflowEngine) resumeCLI(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	prepared, err := e.prepareResume(ctx, req)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return e.launchResume(ctx, prepared)
}

type resumePrepared struct {
	runID      string
	workflow   string
	built      workflowControllerBuild
	closeFn    func()
	finishExec func()
	repo       workflowledger.Repository
}

func (e *sessionWorkflowEngine) prepareResume(ctx context.Context, req agenttools.StartRequest) (resumePrepared, error) {
	if strings.TrimSpace(req.RunID) == "" {
		return resumePrepared{}, fmt.Errorf("resume requires run_id")
	}
	if !req.Force {
		return resumePrepared{}, fmt.Errorf("workflow run %q is not terminal; set force=true only after the prior executor stopped", req.RunID)
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
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
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
	if err := repo.ClearRunClaim(ctx, req.RunID); err != nil {
		built.Dispatcher.Close()
		finishExecution()
		closeFn()
		return resumePrepared{}, fmt.Errorf("clear interrupted workflow claim: %w", err)
	}
	return resumePrepared{runID: req.RunID, workflow: run.WorkflowName, built: built, closeFn: closeFn, finishExec: finishExecution, repo: repo}, nil
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
	e.active[p.runID] = &sessionActiveRun{
		cancel: cancel, done: done,
		closeFn: func() {
			p.finishExec()
			p.built.Dispatcher.Close()
			p.closeFn()
		},
	}
	e.mu.Unlock()
	go func() {
		defer close(done)
		_, _ = workflowResumeRun(runCtx, p.built)
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

// Cancel implements agenttools.Engine.
func (e *sessionWorkflowEngine) Cancel(ctx context.Context, runID string) (agenttools.CancelResult, error) {
	e.mu.Lock()
	if active, ok := e.active[runID]; ok {
		active.cancel()
	}
	e.mu.Unlock()
	if e.Local != nil {
		return e.Local.Cancel(ctx, runID)
	}
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	defer closeFn()
	_ = repo.ClearRunClaim(ctx, runID)
	if err := controller.CancelRun(ctx, repo, runID); err != nil {
		return agenttools.CancelResult{}, err
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
}

// Deliver implements agenttools.Engine.
func (e *sessionWorkflowEngine) Deliver(ctx context.Context, runID string, allowPublish bool) (agenttools.DeliverResult, error) {
	if !allowPublish {
		return agenttools.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
	}
	if e.Local != nil {
		return e.Local.Deliver(ctx, runID, allowPublish)
	}
	var stdout, stderr strings.Builder
	if err := executeWorkflowDeliver(runID, e.root, e.configPath, allowPublish, &stdout, &stderr); err != nil {
		return agenttools.DeliverResult{}, err
	}
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	defer closeFn()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	return agenttools.DeliverResult{RunID: runID, Status: string(run.Status)}, nil
}

func inputsToRawFlags(inputs map[string]any) []string {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]string, 0, len(inputs))
	for k, v := range inputs {
		switch x := v.(type) {
		case string:
			out = append(out, k+"="+x)
		default:
			out = append(out, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return out
}

// wireWorkflowToolOptions attaches Phase 7 workflow tools to DefaultOptions
// when the workspace has .mivia/workflows/. The service opens the shared
// workflow store for reads; the engine drives run/cancel/deliver in-process.
func wireWorkflowToolOptions(opts *tools.DefaultOptions, root string, res *config.Resolved) {
	if opts == nil || !agenttools.HasWorkflows(root) {
		return
	}
	cfg := config.DefaultSubagentConfig
	if res != nil {
		applyWorkflowStoreRoot(res, root)
		cfg = res.Subagents
	}
	configPath := workflowConfigPath(root, "")
	repoFactory := func(context.Context) (workflowledger.Repository, func(), error) {
		_, repo, closeFn, err := openWorkflowStore(root, cfg)
		return repo, closeFn, err
	}
	// Open one store for the engine. Background controllers keep it for the
	// process lifetime (same model as the session ledger). Do not close it here.
	_, repo, _, err := openWorkflowStore(root, cfg)
	if err != nil {
		svc, svcErr := agenttools.NewService(agenttools.ServiceOptions{Repo: repoFactory})
		if svcErr == nil {
			opts.WorkflowTools = wrapWorkflowTools(svc)
		}
		return
	}
	engine := newSessionWorkflowEngine(root, configPath, repo)
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo:   repoFactory,
	})
	if err != nil {
		return
	}
	opts.WorkflowTools = wrapWorkflowTools(svc)
}
