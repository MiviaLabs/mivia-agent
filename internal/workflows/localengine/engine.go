// Package localengine provides an in-process workflow Engine for agent tools.
// It reuses controller, ledger, definition, compiler, and delivery packages.
// Integration tests inject a scripted AgentStepRunner; production hosts may
// inject a coordinator-backed runner.
package localengine

import (
	"context"
	"encoding/json"
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
)

// Engine runs workflows in-process against a shared ledger repository.
type Engine struct {
	// WorkspaceRoot is the source workspace for discovery and compilation.
	WorkspaceRoot string
	// Repo is the shared workflow ledger. Required.
	Repo workflowledger.Repository
	// NewRunner builds the agent-step runner for one admitted run.
	NewRunner func() controller.AgentStepRunner
	// NewRunID mints run IDs. Nil uses a secure random wfr- id.
	NewRunID func() string
	// Git and PR are optional delivery adapters.
	Git delivery.GitRunner
	PR  delivery.PRClient
	// DeliveryTimeout bounds one deliver call. Zero uses 2 minutes.
	DeliveryTimeout time.Duration

	mu     sync.Mutex
	active map[string]*activeRun
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	ctrl   *controller.LinearController
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
	compiled, raw, _, err := e.loadWorkflow(req.Workflow)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	inputs, inputSnapshot, err := validateInputs(req.Inputs, compiled.Inputs)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	runID := e.newRunID()
	steps := buildStepRuntimes(compiled)
	snap := workflowledger.Snapshot{
		SchemaVersion:    workflowledger.SnapshotSchemaVersion,
		DefinitionTOML:   append([]byte(nil), raw...),
		DefinitionDigest: compiled.Digest,
		Inputs:           inputSnapshot,
	}
	if compiled.Delivery != nil && compiled.DeliveryActive() {
		snap.Delivery = &workflowledger.DeliverySnapshot{
			Mode: compiled.Delivery.Mode, Provider: compiled.Delivery.Provider, Base: compiled.Delivery.Base,
		}
	}
	snapshot, err := workflowledger.MarshalSnapshot(snap)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	ctrl, err := controller.NewLinearController(e.Repo, e.runner(), compiled, steps, inputs, runID, snapshot)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	admission := controller.Admission{
		BaseRef:      "main",
		BaseCommit:   "test-base",
		WorktreeName: "workflow-" + runID,
		InputDigest:  workflowledger.InputDigest(inputSnapshot),
	}
	if base, commit, wt, err := resolveLocalIdentity(e.WorkspaceRoot, runID); err == nil {
		admission.BaseRef, admission.BaseCommit, admission.WorktreeName = base, commit, wt
	}
	if err := ctrl.SetAdmission(admission); err != nil {
		return agenttools.StartResult{}, err
	}
	if err := ctrl.Start(ctx); err != nil {
		return agenttools.StartResult{}, err
	}
	_ = req.AllowPublish // publication is a separate deliver step for tools
	e.launch(ctrl)
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: runID, Status: string(run.Status), Workflow: compiled.Name}, nil
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
		return agenttools.StartResult{RunID: run.RunID, Status: string(run.Status), Workflow: run.WorkflowName, Resumed: true}, nil
	}
	raw, err := e.Repo.GetRunSnapshot(ctx, req.RunID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return agenttools.StartResult{}, err
	}
	compiled, err := compiler.CompileForResume(&wf)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	inputs := make(map[string]any, len(snapshot.Inputs))
	for k, v := range snapshot.Inputs {
		inputs[k] = v
	}
	ctrl, err := controller.NewLinearController(e.Repo, e.runner(), compiled, buildStepRuntimes(compiled), inputs, req.RunID, raw)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	admission := controller.Admission{
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit, WorktreeName: run.WorktreeName,
		InputDigest: run.InputDigest, DeadlineAt: run.DeadlineAt, RemoteURL: run.RemoteURL,
	}
	if err := ctrl.SetAdmission(admission); err != nil {
		return agenttools.StartResult{}, err
	}
	if req.Force {
		if err := ctrl.SetForceResume(true); err != nil {
			return agenttools.StartResult{}, err
		}
		if err := e.Repo.ClearRunClaim(ctx, req.RunID); err != nil {
			return agenttools.StartResult{}, err
		}
	}
	if err := ctrl.Start(ctx); err != nil {
		return agenttools.StartResult{}, err
	}
	e.launch(ctrl)
	fresh, err := e.Repo.GetRun(ctx, req.RunID)
	if err != nil {
		return agenttools.StartResult{}, err
	}
	return agenttools.StartResult{RunID: req.RunID, Status: string(fresh.Status), Workflow: run.WorkflowName, Resumed: true}, nil
}

func (e *Engine) launch(ctrl *controller.LinearController) {
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.mu.Lock()
	if e.active == nil {
		e.active = make(map[string]*activeRun)
	}
	if prev, ok := e.active[ctrl.RunID]; ok {
		prev.cancel()
		<-prev.done
	}
	e.active[ctrl.RunID] = &activeRun{cancel: cancel, done: done, ctrl: ctrl}
	e.mu.Unlock()
	go func() {
		defer close(done)
		_, _ = ctrl.Run(runCtx)
		e.mu.Lock()
		if cur, ok := e.active[ctrl.RunID]; ok && cur.done == done {
			delete(e.active, ctrl.RunID)
		}
		e.mu.Unlock()
	}()
}

// Cancel implements agenttools.Engine.
func (e *Engine) Cancel(ctx context.Context, runID string) (agenttools.CancelResult, error) {
	if e == nil || e.Repo == nil {
		return agenttools.CancelResult{}, fmt.Errorf("workflow engine is incomplete")
	}
	e.mu.Lock()
	active, ok := e.active[runID]
	e.mu.Unlock()
	if ok {
		active.cancel()
		// Wait for the controller to drop its claim so CancelRun can settle.
		select {
		case <-active.done:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	_ = e.Repo.ClearRunClaim(ctx, runID)
	if err := controller.CancelRun(ctx, e.Repo, runID); err != nil {
		// Context cancel may already have settled the run; treat terminal as success.
		run, getErr := e.Repo.GetRun(ctx, runID)
		if getErr == nil && workflowledger.IsTerminalRunStatus(run.Status) {
			return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
		}
		return agenttools.CancelResult{}, err
	}
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.CancelResult{}, err
	}
	return agenttools.CancelResult{RunID: runID, Status: string(run.Status)}, nil
}

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
	if run.Status != workflowledger.RunStatusDeliveryPending {
		return agenttools.DeliverResult{}, fmt.Errorf("run is not waiting for delivery (status %q)", run.Status)
	}
	return e.deliverPending(ctx, run)
}

func (e *Engine) replayDelivery(ctx context.Context, run workflowledger.RunSnapshot) (agenttools.DeliverResult, error) {
	rec, err := e.Repo.GetDeliveryByIdempotencyKey(ctx, delivery.DeliveryKey(run.RunID, run.WorkflowDigest))
	if err != nil {
		return agenttools.DeliverResult{RunID: run.RunID, Status: string(run.Status)}, nil
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
	release, err := e.claimDelivery(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	defer release()
	return e.publishDelivery(ctx, run, snapshot, policy)
}

func (e *Engine) claimDelivery(ctx context.Context, runID string) (func(), error) {
	holder := "wfdel-" + randomToken(5)
	if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
		if !errors.Is(err, workflowledger.ErrClaimHeld) {
			return nil, err
		}
		_ = e.Repo.ClearRunClaim(ctx, runID)
		if err := e.Repo.ClaimRun(ctx, runID, holder); err != nil {
			return nil, err
		}
	}
	return func() { _ = e.Repo.ReleaseRun(context.Background(), runID, holder) }, nil
}

func (e *Engine) publishDelivery(ctx context.Context, run workflowledger.RunSnapshot, snapshot workflowledger.Snapshot, policy delivery.Policy) (agenttools.DeliverResult, error) {
	runID := run.RunID
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
	dreq := delivery.Request{
		RunID: runID, WorkflowDigest: run.WorkflowDigest, Policy: policy,
		Inputs: snapshot.Inputs, BaseCommit: run.BaseCommit,
		Branch: "wf/" + run.WorktreeName, GitCtx: delivery.GitContext{Dir: e.WorkspaceRoot},
		OriginURL: run.RemoteURL,
	}
	result, err := delivery.Deliver(deliveryCtx, e.Repo, git, pr, dreq)
	if err != nil {
		if delivery.IsRefusal(err) {
			if fresh, getErr := e.Repo.GetRun(ctx, runID); getErr == nil {
				_ = e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusDeliveryFailed, nil)
			}
			return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusDeliveryFailed), Refused: true, Reason: err.Error()}, nil
		}
		return agenttools.DeliverResult{}, err
	}
	fresh, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return agenttools.DeliverResult{}, err
	}
	if fresh.Status == workflowledger.RunStatusDeliveryPending {
		now := time.Now()
		if err := e.Repo.CompareAndSetRunStatus(ctx, runID, fresh.Version, workflowledger.RunStatusSucceeded, &now); err != nil {
			return agenttools.DeliverResult{}, err
		}
	}
	return agenttools.DeliverResult{RunID: runID, Status: string(workflowledger.RunStatusSucceeded), URL: result.URL, Mode: result.Mode}, nil
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

// Interrupt cancels the in-process controller without settling the ledger.
func (e *Engine) Interrupt(runID string) {
	e.mu.Lock()
	active, ok := e.active[runID]
	e.mu.Unlock()
	if !ok {
		return
	}
	active.cancel()
	<-active.done
}

func (e *Engine) runner() controller.AgentStepRunner {
	if e.NewRunner != nil {
		return e.NewRunner()
	}
	return &StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
}

func (e *Engine) newRunID() string {
	if e.NewRunID != nil {
		return e.NewRunID()
	}
	return "wfr-" + randomToken(10)
}

func (e *Engine) loadWorkflow(name string) (*compiler.CompiledWorkflow, []byte, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil, "", fmt.Errorf("workflow name is required")
	}
	root := e.WorkspaceRoot
	if root == "" {
		root = "."
	}
	workflows, err := definition.DiscoverWorkflows(root)
	if err != nil {
		return nil, nil, "", err
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == name {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return nil, nil, "", fmt.Errorf("workflow %q was not found", name)
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return nil, nil, "", err
	}
	compiled, err := compiler.Compile(&wf)
	if err != nil {
		return nil, nil, "", err
	}
	return compiled, found.Raw, filepath.Dir(found.Path), nil
}

// Ensure Engine implements agenttools.Engine.
var _ agenttools.Engine = (*Engine)(nil)
