package localengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

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
	if run.Status == workflowledger.RunStatusDeliveryFailed {
		return agenttools.StartResult{}, fmt.Errorf("workflow run %q failed delivery; call workflow_deliver", req.RunID)
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
			// force-resume must actually force: unconditionally replace the
			// held claim rather than only clear it if the lease already
			// expired. TakeoverExpiredRunClaim here would make force a no-op
			// against a live (non-expired) claim.
			return e.Repo.TakeoverRunClaim(ctx, runID, holder)
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
	compiled, inputs, err := e.resumeCompileAndValidate(snapshot, run, raw)
	if err != nil {
		return nil, err
	}
	return e.newResumeController(req, run, raw, snapshot, compiled, inputs)
}

// resumeCompileAndValidate proves the durable snapshot is the admitted one
// (blob digest, internal Validate, definition digest, input digest, delivery
// policy) and recompiles it, mirroring the CLI's validateWorkflowResumeSnapshot.
func (e *Engine) resumeCompileAndValidate(snapshot workflowledger.Snapshot, run workflowledger.RunSnapshot, raw []byte) (*definition.CompiledWorkflow, map[string]any, error) {
	if run.SnapshotDigest == "" || run.SnapshotDigest != workflowledger.SnapshotDigest(raw) {
		return nil, nil, fmt.Errorf("workflow snapshot digest does not match the admitted snapshot")
	}
	// CLI parity (validateWorkflowResumeSnapshot): the blob digest proves the
	// bytes are the admitted bytes; Validate proves their internal consistency
	// (schema version, per-ref digest/bytes agreement).
	if err := snapshot.Validate(); err != nil {
		return nil, nil, err
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return nil, nil, err
	}
	compiled, err := definition.CompileForResume(&wf)
	if err != nil {
		return nil, nil, err
	}
	// The engine-reserved stacking inputs (D3) were merged into the admitted
	// contract, so resume accepts them too (a no-op for non-stacking runs).
	definition.MergeStackingInputs(compiled)
	// A stacking run EXECUTES the synthesized graph; rebuild it here so the
	// resumed runtimes carry the engine-synthesized steps (decompose,
	// chunk_plan_validate) and admission re-validates them with their pinned
	// routing snapshots instead of refusing a digest-less step.
	compiled, err = definition.SynthesizeStacking(compiled)
	if err != nil {
		return nil, nil, err
	}
	if snapshot.DefinitionDigest != run.WorkflowDigest {
		return nil, nil, fmt.Errorf("workflow definition digest does not match the admitted definition")
	}
	// CLI parity: the recorded input digest must match the snapshot's inputs,
	// every snapshot input must exist in the admitted contract, and typed
	// inputs resume with their declared Go type, never as raw strings.
	if run.InputDigest == "" || run.InputDigest != workflowledger.InputDigest(snapshot.Inputs) {
		return nil, nil, fmt.Errorf("workflow input digest does not match the admitted inputs")
	}
	inputs := make(map[string]any, len(snapshot.Inputs))
	for k, v := range snapshot.Inputs {
		def, ok := compiled.Inputs[k]
		if !ok {
			return nil, nil, fmt.Errorf("snapshot contains unknown workflow input %q", k)
		}
		parsed, parseErr := definition.ParseInputValue(v, def.Type)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("workflow input %q: %w", k, parseErr)
		}
		inputs[k] = parsed
	}
	// CLI parity: a pinned delivery policy must match the compiled definition.
	if snapshot.Delivery != nil {
		if compiled.Delivery == nil ||
			compiled.Delivery.Mode != snapshot.Delivery.Mode ||
			compiled.Delivery.Provider != snapshot.Delivery.Provider ||
			compiled.Delivery.Base != snapshot.Delivery.Base {
			return nil, nil, fmt.Errorf("snapshot delivery policy does not match the admitted definition")
		}
	}
	return compiled, inputs, nil
}

// newResumeController clears the abandon fence, rebuilds the pinned step
// runtimes, and wires a fresh controller against them with the run's
// admission pins re-applied.
func (e *Engine) newResumeController(req agenttools.StartRequest, run workflowledger.RunSnapshot, raw []byte, snapshot workflowledger.Snapshot, compiled *definition.CompiledWorkflow, inputs map[string]any) (*controller.LinearController, error) {
	// Clear abandon fence so a fresh controller may write again after Interrupt.
	// Must use clearAbandon (holds fence.mu); bare delete races with isAbandoned.
	_ = e.ctrlRepo()
	e.mu.Lock()
	if e.fence != nil {
		e.fence.clearAbandon(req.RunID)
	}
	e.mu.Unlock()
	// Step schemas and templates are pinned in the admitted snapshot
	// (SchemaVersion 1 carries them as snapshot.Schemas / snapshot.Templates);
	// rebuild the runtimes from those bytes so a resumed run enforces the
	// references admitted at start. No filesystem reads on resume.
	steps, err := e.buildResumeStepRuntimes(compiled, snapshot, req.RunID)
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

// buildResumeStepRuntimes verifies pinned agents against the live registry and
// rebuilds step runtimes from the admitted snapshot. A run admitted before
// agent pinning carries no pins and resumes in the legacy synthetic-digest
// mode it was admitted with (CLI parity with workflowAgent's prior check).
func (e *Engine) buildResumeStepRuntimes(compiled *definition.CompiledWorkflow, snapshot workflowledger.Snapshot, runID string) (map[string]controller.StepRuntime, error) {
	var agentPins map[string]workflowledger.AgentSnapshot
	if len(snapshot.Agents) > 0 {
		if e.AgentRegistry == nil {
			return nil, fmt.Errorf("workflow run %q was admitted with pinned agent definitions but no agent registry is configured", runID)
		}
		if err := verifyStepAgents(compiled, e.AgentRegistry, snapshot.Agents); err != nil {
			return nil, err
		}
		agentPins = snapshot.Agents
	}
	return buildStepRuntimesFromSnapshot(compiled, snapshot.Schemas, snapshot.Templates, agentPins)
}
