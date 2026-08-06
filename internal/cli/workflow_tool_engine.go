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
)

// sessionWorkflowEngine is the production Engine for chat-session workflow tools.
// New runs use the full CLI admission path (providers, worktrees, coordinator)
// and return the run ID without waiting for terminal state.
type sessionWorkflowEngine struct {
	root       string
	configPath string
	// Local backs resume/cancel/deliver and scripted fallbacks.
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
func (e *sessionWorkflowEngine) Start(ctx context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	if e == nil {
		return agenttools.StartResult{}, fmt.Errorf("workflow engine is nil")
	}
	if req.Resume {
		if e.Local != nil {
			return e.Local.Start(ctx, req)
		}
		return agenttools.StartResult{}, fmt.Errorf("resume requires a workflow ledger")
	}
	result, err := e.startCLI(ctx, req)
	if err != nil && e.Local != nil && e.Local.Repo != nil && shouldFallbackLocal(err) {
		return e.Local.Start(ctx, req)
	}
	return result, err
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

func shouldFallbackLocal(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "provider") ||
		strings.Contains(msg, "api_key") ||
		strings.Contains(msg, "completer") ||
		strings.Contains(msg, "no such file")
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
