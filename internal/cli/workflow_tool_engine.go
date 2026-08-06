package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// sessionCancelWait is how long Cancel waits for an in-process controller to
// drop its claim after context cancel before settling the ledger.
const sessionCancelWait = 3 * time.Second

// sessionWorkflowEngine is the production Engine for chat-session workflow tools.
// New runs use the full CLI admission path (providers, worktrees, coordinator)
// and return the run ID without waiting for terminal state.
// Cancel and Deliver use the same CLI ledger/worktree paths as the operator
// commands — not localengine delivery against the caller workspace root.
type sessionWorkflowEngine struct {
	root       string
	configPath string

	mu     sync.Mutex
	active map[string]*sessionActiveRun
}

type sessionActiveRun struct {
	cancel  context.CancelFunc
	done    chan struct{}
	closeFn func()
}

// newSessionWorkflowEngine builds the chat-session workflow engine.
func newSessionWorkflowEngine(root, configPath string) *sessionWorkflowEngine {
	return &sessionWorkflowEngine{
		root:       root,
		configPath: configPath,
		active:     make(map[string]*sessionActiveRun),
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

// stopActive cancels an in-process controller for runID and waits until it
// exits (or the wait bound fires). It must run before claim clear / CancelRun
// so the dying controller is not racing ledger settlement.
func (e *sessionWorkflowEngine) stopActive(ctx context.Context, runID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	active, ok := e.active[runID]
	e.mu.Unlock()
	if !ok || active == nil {
		return
	}
	active.cancel()
	select {
	case <-active.done:
	case <-ctx.Done():
	case <-time.After(sessionCancelWait):
	}
}

// Cancel implements agenttools.Engine.
// It stops any in-process controller first, then settles via the same
// execution-lock + CancelRun path as `mivia workflow cancel`.
func (e *sessionWorkflowEngine) Cancel(ctx context.Context, runID string) (agenttools.CancelResult, error) {
	if e == nil {
		return agenttools.CancelResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return agenttools.CancelResult{}, fmt.Errorf("run_id is required")
	}
	e.stopActive(ctx, runID)
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContext(e.root, e.configPath, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	defer closeFn()
	defer releaseExecution()
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		return agenttools.CancelResult{}, fmt.Errorf("clear workflow claim: %w", err)
	}
	if err := controller.CancelRun(ctx, repo, runID); err != nil {
		// Context cancel or a prior settle may already leave the run terminal.
		run, getErr := repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return agenttools.CancelResult{}, err
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
}

// Deliver implements agenttools.Engine.
// Publication uses the CLI deliver path (run-owned worktree + execution lock).
// It never delivers from the caller workspace root via localengine.
func (e *sessionWorkflowEngine) Deliver(ctx context.Context, runID string, allowPublish bool) (agenttools.DeliverResult, error) {
	if e == nil {
		return agenttools.DeliverResult{}, fmt.Errorf("workflow engine is nil")
	}
	if strings.TrimSpace(runID) == "" {
		return agenttools.DeliverResult{}, fmt.Errorf("run_id is required")
	}
	if !allowPublish {
		return agenttools.DeliverResult{RunID: runID, Refused: true, Reason: "delivery requires allow_publish=true"}, nil
	}
	var stdout, stderr strings.Builder
	if err := executeWorkflowDeliver(runID, e.root, e.configPath, allowPublish, &stdout, &stderr); err != nil {
		// Prefer structured status when the ledger still opens after a refusal.
		if result, ok := sessionDeliverResultFromLedger(ctx, e.root, e.configPath, runID, err); ok {
			return result, nil
		}
		return agenttools.DeliverResult{}, err
	}
	repo, closeFn, err := openWorkflowReportContext(e.root, e.configPath)
	if err != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	defer closeFn()
	run, getErr := repo.GetRun(ctx, runID)
	if getErr != nil {
		return agenttools.DeliverResult{RunID: runID, Status: "unknown"}, nil
	}
	result := agenttools.DeliverResult{RunID: runID, Status: string(run.Status)}
	if rec, recErr := repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest)); recErr == nil {
		result.URL = rec.URL
		result.Mode = rec.Mode
	}
	return result, nil
}

func sessionDeliverResultFromLedger(ctx context.Context, root, configPath, runID string, deliverErr error) (agenttools.DeliverResult, bool) {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return agenttools.DeliverResult{}, false
	}
	defer closeFn()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, false
	}
	// delivery_failed after a host refusal is a settled outcome, not a tool error.
	if run.Status == workflowledger.RunStatusDeliveryFailed {
		return agenttools.DeliverResult{
			RunID: runID, Status: string(run.Status),
			Refused: true, Reason: deliverErr.Error(),
		}, true
	}
	return agenttools.DeliverResult{}, false
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

// workflowToolSubagentConfig resolves the store config used by workflow tools.
// It matches CLI workflow commands: prefer the session Resolved config, else
// load the workspace config, then apply the workspace store-root default.
func workflowToolSubagentConfig(root string, res *config.Resolved) config.SubagentConfig {
	if res != nil {
		applyWorkflowStoreRoot(res, root)
		return res.Subagents
	}
	configPath := workflowConfigPath(root, "")
	loaded, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil || loaded == nil {
		return config.DefaultSubagentConfig
	}
	applyWorkflowStoreRoot(loaded, root)
	return loaded.Subagents
}

// wireWorkflowToolOptions attaches Phase 7 workflow tools to DefaultOptions
// when the workspace has .mivia/workflows/. Reads open the same store path as
// CLI workflow commands. Run/cancel/deliver use the session engine (CLI paths).
func wireWorkflowToolOptions(opts *tools.DefaultOptions, root string, res *config.Resolved) {
	if opts == nil || !agenttools.HasWorkflows(root) {
		return
	}
	cfg := workflowToolSubagentConfig(root, res)
	configPath := workflowConfigPath(root, "")
	repoFactory := func(context.Context) (workflowledger.Repository, func(), error) {
		_, repo, closeFn, err := openWorkflowStore(root, cfg)
		return repo, closeFn, err
	}
	engine := newSessionWorkflowEngine(root, configPath)
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: engine,
		Repo:   repoFactory,
	})
	if err != nil {
		return
	}
	opts.WorkflowTools = wrapWorkflowTools(svc)
}
