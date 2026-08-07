package cli

import (
	"context"
	"encoding/json"
	"errors"
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
)

// sessionCancelWait is how long Cancel waits for an in-process controller to
// drop its claim after context cancel before settling the ledger.
const sessionCancelWait = 3 * time.Second

// workflowResolutionLockWait bounds the execution-lock wait for cancel after
// stopActive: a settling controller can hold the flock past the cancel wait
// bound, and a non-blocking acquire would surface as an opaque lock error
// while the run keeps running.
const workflowResolutionLockWait = 5 * time.Second

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
	rawInputs, err := inputsToRawFlags(req.Inputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	prepared, err := prepareWorkflowRun(req.Workflow, e.root, e.configPath, rawInputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID := newCLIWorkflowRunID()
	if key := strings.TrimSpace(req.InvocationKey); key != "" {
		runID = agenttools.InvocationRunID(key)
		if existing, getErr := prepared.repo.GetRun(ctx, runID); getErr == nil {
			prepared.closeFn()
			if !workflowledger.IsTerminalRunStatus(existing.Status) && existing.Status != workflowledger.RunStatusDeliveryPending {
				e.mu.Lock()
				_, active := e.active[runID]
				e.mu.Unlock()
				if !active {
					return e.resumeCLI(ctx, agenttools.StartRequest{Resume: true, RunID: runID, Force: req.Force, AllowPublish: req.AllowPublish})
				}
			}
			return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
		} else if !errors.Is(getErr, workflowledger.ErrNotFound) {
			prepared.closeFn()
			return agenttools.StartResult{}, getErr
		}
	}
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
	built.Admission.InvocationKey = strings.TrimSpace(req.InvocationKey)
	if err := workflowRunSetAdmission(built); err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	created, err := built.Controller.StartNew(ctx)
	if err != nil {
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		return agenttools.StartResult{}, err
	}
	if !created {
		existing, getErr := prepared.repo.GetRun(ctx, runID)
		built.Cleanup()
		built.Dispatcher.Close()
		finishExecution()
		prepared.closeFn()
		if getErr != nil {
			return agenttools.StartResult{}, getErr
		}
		return agenttools.StartResult{RunID: runID, Status: string(existing.Status), Workflow: existing.WorkflowName}, nil
	}
	return e.launchStartedWorkflow(ctx, prepared, built, runID, req.Workflow, req.AllowPublish, finishExecution)
}

func (e *sessionWorkflowEngine) launchStartedWorkflow(ctx context.Context, prepared *preparedWorkflowRun, built workflowControllerBuild, runID, workflow string, allowPublish bool, finishExecution func()) (agenttools.StartResult, error) {
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*sessionActiveRun)
	}
	e.active[runID] = &sessionActiveRun{cancel: cancel, done: done, closeFn: func() { finishExecution(); built.Dispatcher.Close(); prepared.closeFn() }}
	e.mu.Unlock()
	go func() {
		defer close(done)
		snap, runErr := built.Controller.Run(runCtx)
		if runErr == nil && snap.Status == workflowledger.RunStatusDeliveryPending && allowPublish {
			if err := deliverRunWithStore(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, runID, true, io.Discard, io.Discard); err != nil {
				recordAutoDeliveryFailure(context.Background(), prepared.repo, runID, err)
			}
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
	return agenttools.StartResult{RunID: runID, Status: string(run.Status), Workflow: workflow}, nil
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
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContextBounded(e.root, e.configPath, runID, workflowResolutionLockWait)
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
func inputsToRawFlags(inputs map[string]any) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(inputs))
	for k, v := range inputs {
		switch x := v.(type) {
		case string:
			out = append(out, k+"="+x)
		default:
			raw, err := json.Marshal(x)
			if err != nil {
				return nil, fmt.Errorf("workflow input %q: encode JSON: %w", k, err)
			}
			out = append(out, k+"="+string(raw))
		}
	}
	return out, nil
}

// workflowToolSubagentConfig resolves the store config used by workflow tools.
// It matches CLI workflow commands: prefer the session Resolved config, else
// load the workspace config, then apply the workspace store-root default.
func workflowToolSubagentConfig(root string, res *config.Resolved) config.SubagentConfig {
	if res != nil {
		applyWorkflowStoreRoot(res, root)
		return res.Subagents
	}
	configPath := sessionEngineConfigPath(root, nil)
	loaded, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil || loaded == nil {
		return config.DefaultSubagentConfig
	}
	applyWorkflowStoreRoot(loaded, root)
	return loaded.Subagents
}

// sessionEngineConfigPath is the config file identity for session workflow
// tools. Prefer the session Resolved.ConfigPath (covers --config / MIVIA_CONFIG)
// so read and mutate paths open the same store. Fall back to the workspace
// project file when no session config is available.
func sessionEngineConfigPath(root string, res *config.Resolved) string {
	if res != nil && strings.TrimSpace(res.ConfigPath) != "" {
		return res.ConfigPath
	}
	return workflowConfigPath(root, "")
}

// workflowToolService builds the in-process workflow tool service for a
// workspace. res carries the session config identity when available; nil
// falls back to the workspace project config. Returns nil when the workspace
// has no .mivia/workflows/ or the service cannot be built.
func workflowToolService(root string, res *config.Resolved) *agenttools.Service {
	if !agenttools.HasWorkflows(root) {
		return nil
	}
	cfg := workflowToolSubagentConfig(root, res)
	configPath := sessionEngineConfigPath(root, res)
	repoFactory := func(context.Context) (workflowledger.Repository, func(), error) {
		_, repo, closeFn, err := openWorkflowStore(root, cfg)
		return repo, closeFn, err
	}
	svc, err := agenttools.NewService(agenttools.ServiceOptions{
		Engine: newSessionWorkflowEngine(root, configPath),
		Repo:   repoFactory,
	})
	if err != nil {
		return nil
	}
	return svc
}

// wireWorkflowToolOptions attaches Phase 7 workflow tools to DefaultOptions
// when the workspace has .mivia/workflows/. Reads and mutates share one config
// identity (session ConfigPath or workspace project file).
func wireWorkflowToolOptions(opts *tools.DefaultOptions, root string, res *config.Resolved) {
	if opts == nil {
		return
	}
	svc := workflowToolService(root, res)
	if svc == nil {
		return
	}
	opts.WorkflowTools = wrapWorkflowTools(svc)
}
