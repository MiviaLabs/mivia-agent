// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (executor.go) is chunk 6: RunOnce is the one entry point
// that turns an admitted fire into a real, durably-recorded execution -
// optional worktree creation (D3), one spawned session, unattended
// approval posture installed on it (D8), and its steps dispatched in
// order (D2) with per-step checkpointing so a later resume (chunk 8)
// restarts at step_index+1 rather than from scratch.
package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// defaultTurnTimeout bounds a headless turn when Config.TurnTimeout was
// left at its zero value: sendTurnHeadless refuses a non-positive
// timeout outright (D5), so the executor must supply SOME positive
// bound rather than propagate zero and fail every run.
const defaultTurnTimeout = 10 * time.Minute

// ErrAutomationNotFound is returned by RunOnce when automationID names
// no automation in the store (the plan's "404" case).
var ErrAutomationNotFound = fmt.Errorf("automation: not found")

// ErrAutomationDisabled is returned by RunOnce when the automation
// exists but is disabled: a disabled automation must never execute,
// whether the fire was scheduled or a manual trigger.
var ErrAutomationDisabled = fmt.Errorf("automation: disabled")

// automationSessionName composes D11's reserved save-name scheme for an
// automation run's background session: "__auto__<automationID>__<runID>".
// It never collides with chat.AutoSaveName ("__last__") - see D11's own
// text for the collision analysis - but this package does not rely on
// that safety net alone: automationID and runID are both already
// ValidateID-checked before this is ever composed (admitFire/createRun).
func automationSessionName(automationID, runID string) string {
	return "__auto__" + automationID + "__" + runID
}

// originForTrigger maps ports.TriggerKind to the Run.Origin string this
// package's runstore persists ("manual"/"scheduled") - the forward
// direction of runOriginToTrigger (service.go).
func originForTrigger(trigger ports.TriggerKind) string {
	if trigger == ports.TriggerScheduled {
		return "scheduled"
	}
	return "manual"
}

// turnTimeout resolves the executor's per-turn deadline: the configured
// value, or defaultTurnTimeout when unset.
func (s *Service) turnTimeout() time.Duration {
	if s.cfg.TurnTimeout > 0 {
		return s.cfg.TurnTimeout
	}
	return defaultTurnTimeout
}

// findSpec locates automationID among every defined automation at
// scopeForLoad, returning the plan's documented "404" (ErrAutomationNotFound)
// when it is missing.
func (s *Service) findSpec(automationID string) (Spec, error) {
	specs, err := LoadSpecs(scopeForLoad, s.root)
	if err != nil {
		return Spec{}, fmt.Errorf("automation: load specs: %w", err)
	}
	for _, spec := range specs {
		if spec.ID == automationID {
			return spec, nil
		}
	}
	return Spec{}, fmt.Errorf("%w: %q", ErrAutomationNotFound, automationID)
}

// recordSkippedRun persists a RunSkipped row for a fire that lost D7's
// fenced single-fire claim - a documented no-op, not an error, so the
// caller (RunOnce) returns this run with a nil error rather than
// propagating anything.
func (s *Service) recordSkippedRun(ctx context.Context, automationID string, trigger ports.TriggerKind, stepCount int) (ports.Run, error) {
	now := time.Now().UTC()
	run := Run{
		ID:           newHolderToken("run-"),
		AutomationID: automationID,
		Origin:       originForTrigger(trigger),
		State:        RunSkipped,
		StepCount:    stepCount,
		StartedAt:    now,
		EndedAt:      &now,
	}
	if err := s.createRun(ctx, run); err != nil {
		return ports.Run{}, fmt.Errorf("automation: record skipped run: %w", err)
	}
	return runToPorts(run), nil
}

// unattendedGateFor resolves the D8 approval override for spec.Unattended:
// UnattendedDeny (default/empty) -> DenyGate; UnattendedAuto ->
// AutoApproveGate. The returned func's signature matches
// SessionSpawner.SetApprovalOverride's gate parameter exactly.
func unattendedGateFor(automationID string, policy UnattendedPolicy) (func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, string) {
	if policy == UnattendedAuto {
		return AutoApproveGate(), "auto"
	}
	return DenyGate(automationID), "deny"
}

// RunOnce turns an admitted fire into a real execution: load the spec,
// win D7's fenced claim (a loss is a documented no-op - RunSkipped, nil
// error), create the run row, optionally create a managed worktree
// (D3), spawn ONE background session, install its unattended approval
// posture (D8) BEFORE any step runs, and dispatch spec.Steps in order
// (D2) with per-step checkpointing.
func (s *Service) RunOnce(ctx context.Context, automationID string, trigger ports.TriggerKind) (ports.Run, error) {
	spec, err := s.findSpec(automationID)
	if err != nil {
		return ports.Run{}, err
	}
	if !spec.Enabled {
		return ports.Run{}, fmt.Errorf("%w: %q", ErrAutomationDisabled, automationID)
	}

	holder, ok, err := s.admitFire(ctx, automationID)
	if err != nil {
		return ports.Run{}, err
	}
	if !ok {
		return s.recordSkippedRun(ctx, automationID, trigger, len(spec.Steps))
	}
	// Release the claim on every exit path from here on, per the plan's
	// "defer pattern from right after admitFire wins is fine".
	defer func() {
		_ = s.db.ReleaseClaim(context.Background(), claimKey(automationID), holder)
	}()

	run, err := s.startRun(ctx, spec, automationID, trigger, holder)
	if err != nil {
		return ports.Run{}, err
	}

	workDir := s.root
	if spec.Worktree == WorktreeNew {
		wtDir, wtBranch, failErr := s.createRunWorktree(spec, run.ID)
		if failErr != nil {
			return s.failRun(ctx, run, 0, failErr), nil
		}
		workDir = wtDir
		run.WorktreePath = wtDir
		run.WorktreeBranch = wtBranch
	}

	conv, boundSess, err := s.spawnRunSession(spec, automationID, run.ID, workDir)
	if err != nil {
		return s.failRun(ctx, run, 0, err), nil
	}
	run.SessionName = automationSessionName(automationID, run.ID)

	if err := s.runSteps(ctx, spec, automationID, workDir, conv, boundSess, &run); err != nil {
		return s.failRun(ctx, run, run.StepIndex, err), nil
	}

	endedAt := time.Now().UTC()
	if err := s.updateRunState(ctx, run.ID, RunSucceeded, len(spec.Steps), &endedAt, RunFailNone, ""); err != nil {
		return ports.Run{}, fmt.Errorf("automation: mark run succeeded: %w", err)
	}
	run.State = RunSucceeded
	run.EndedAt = &endedAt
	return runToPorts(run), nil
}

// startRun creates the run row (RunPending) and immediately marks it
// RunRunning, so a crash between the two still leaves a durable
// RunPending row for a later sweep rather than nothing at all.
func (s *Service) startRun(ctx context.Context, spec Spec, automationID string, trigger ports.TriggerKind, holder string) (Run, error) {
	run := Run{
		ID:           newHolderToken("run-"),
		AutomationID: automationID,
		Origin:       originForTrigger(trigger),
		State:        RunPending,
		StepCount:    len(spec.Steps),
		ClaimToken:   holder,
		StartedAt:    time.Now().UTC(),
	}
	if err := s.createRun(ctx, run); err != nil {
		return Run{}, fmt.Errorf("automation: create run: %w", err)
	}
	if err := s.updateRunState(ctx, run.ID, RunRunning, 0, nil, RunFailNone, ""); err != nil {
		return Run{}, fmt.Errorf("automation: mark run running: %w", err)
	}
	run.State = RunRunning
	return run, nil
}

// spawnRunSession spawns the ONE session RunOnce drives for the whole
// run (D2) and installs its D8 unattended approval override
// immediately after - never inside the bind closure, which
// CreateFreshInDir invokes BEFORE wireEntryLocked's inheritApprovalLocked
// call and would therefore be clobbered (see approval.go's doc
// comments). Returns the spawned conversation and the underlying
// *chat.Session the bind closure captured, so StepAgent can select an
// agent on it directly.
func (s *Service) spawnRunSession(spec Spec, automationID, runID, workDir string) (ports.Conversation, *chat.Session, error) {
	var boundSess *chat.Session
	bindFn := func(sess *chat.Session) (string, error) {
		boundSess = sess
		if sess != nil && sess.ContextEnabled() {
			// Best-effort: a spawner with no context store configured
			// (many test fixtures) leaves this a no-op rather than a
			// failed bind - the run's SessionName field is the durable
			// record of the intended name regardless.
			_ = sess.Save(automationSessionName(automationID, runID))
		}
		return "", nil
	}
	conv, err := s.spawn.CreateFreshInDir(bindFn, workDir)
	if err != nil {
		return nil, nil, fmt.Errorf("spawn session: %w", err)
	}
	gate, policy := unattendedGateFor(automationID, spec.Unattended)
	if err := s.spawn.SetApprovalOverride(conv.ID(), gate, policy); err != nil {
		return nil, nil, fmt.Errorf("install approval override: %w", err)
	}
	return conv, boundSess, nil
}

// runSteps dispatches every step of spec.Steps in order (D2) on the one
// already-spawned conversation, checkpointing run.StepIndex after each
// completed step so a later Resume (chunk 8) restarts at index+1. On a
// step failure, run.StepIndex is left at the failing index (not
// advanced) so the caller's failRun call records exactly where
// execution stopped.
func (s *Service) runSteps(ctx context.Context, spec Spec, automationID, workDir string, conv ports.Conversation, boundSess *chat.Session, run *Run) error {
	timeout := s.turnTimeout()
	for i, step := range spec.Steps {
		if stepErr := s.runStep(ctx, automationID, run.ID, i, workDir, conv, boundSess, step, timeout); stepErr != nil {
			run.StepIndex = i
			return stepErr
		}
		if err := s.updateRunState(ctx, run.ID, RunRunning, i+1, nil, RunFailNone, ""); err != nil {
			return fmt.Errorf("automation: checkpoint run %q step %d: %w", run.ID, i, err)
		}
		run.StepIndex = i + 1
	}
	return nil
}

// failRun transitions run to RunFailed/RunFailJobError at stepIndex and
// returns its ports.Run view. The claim release itself is the caller's
// deferred responsibility (RunOnce); this only updates the durable
// record. The update error itself is deliberately swallowed here: a
// failed run whose OWN failure cannot be recorded must still report the
// original failure to the caller, not a secondary bookkeeping error.
func (s *Service) failRun(ctx context.Context, run Run, stepIndex int, cause error) ports.Run {
	endedAt := time.Now().UTC()
	_ = s.updateRunState(ctx, run.ID, RunFailed, stepIndex, &endedAt, RunFailJobError, cause.Error())
	run.State = RunFailed
	run.StepIndex = stepIndex
	run.EndedAt = &endedAt
	run.FailKind = RunFailJobError
	run.Message = cause.Error()
	return runToPorts(run)
}

// createRunWorktree implements D3: create a managed worktree named
// "auto-"+automationID+"-"+runID off spec.BaseRef, BEFORE the session
// exists, using the project's configured branch prefix. Correction vs
// the original plan text: config.LoadWorktreeConfig returns
// (WorktreeConfig, error) - two values, not a bare struct - so the
// error is checked explicitly here rather than chained inline.
func (s *Service) createRunWorktree(spec Spec, runID string) (dir, branch string, err error) {
	wtCfg, err := config.LoadWorktreeConfig(s.root)
	if err != nil {
		return "", "", fmt.Errorf("load worktree config: %w", err)
	}
	name := "auto-" + spec.ID + "-" + runID
	wt, err := cliworktree.CreateManagedWorktree(s.root, name, spec.BaseRef, wtCfg.BranchPrefix)
	if err != nil {
		return "", "", fmt.Errorf("create managed worktree: %w", err)
	}
	return wt.Path, wt.Branch, nil
}

// runStep dispatches ONE step of spec.Steps against the already-spawned
// conversation, per D2: StepPrompt sent verbatim, StepSkill rendered as
// "/"+Ref, StepSlash re-validated at execution time (defense in depth)
// then rendered+sent, StepAgent applies the agent selection then sends
// Prompt, StepWorkflow dispatches to the workflow engine and never sets
// AllowPublish.
func (s *Service) runStep(ctx context.Context, automationID, runID string, stepIndex int, workDir string, conv ports.Conversation, boundSess *chat.Session, step Step, timeout time.Duration) error {
	switch step.Kind {
	case StepPrompt:
		_, err := sendTurnHeadless(ctx, conv, step.Prompt, timeout)
		return wrapStepErr(automationID, stepIndex, err)
	case StepSkill:
		_, err := sendTurnHeadless(ctx, conv, "/"+step.Ref, timeout)
		return wrapStepErr(automationID, stepIndex, err)
	case StepSlash:
		// Defense in depth: the spec was already validated at Apply time
		// (ValidateSpec), but re-validate here too so a spec written by
		// an older, less-strict version of this package (or edited by
		// hand on disk) cannot reach a rejected slash command at
		// execution time. registry is nil: the executor carries no live
		// *skills.Registry today (Config has no such field yet), matching
		// upsert's own nil-registry validation call in service.go.
		if err := validateStepSlash(automationID, stepIndex, step.Ref, nil); err != nil {
			return err
		}
		_, err := sendTurnHeadless(ctx, conv, step.Ref, timeout)
		return wrapStepErr(automationID, stepIndex, err)
	case StepAgent:
		// KNOWN GAP (documented deviation, not silently dropped): ports.
		// Conversation exposes no *chat.Session accessor, and
		// automation.Service carries no *config.Resolved or
		// *cliagents.AgentSessionState to select a NAMED agent by
		// registry lookup - that wiring does not exist in this chunk.
		// boundSess (captured from the bind closure CreateFreshInDir
		// invokes) is the real underlying session, so ApplySessionAgent
		// is reachable, but with an empty per-run AgentSessionState it
		// can only ever resolve config.RootAgentName; any other agent
		// name fails with "no agents loaded". This is a real limitation
		// of chunk 6 as specified, not a stand-in.
		if err := cliagents.ApplySessionAgent(boundSess, nil, &cliagents.AgentSessionState{}, step.Ref, false); err != nil {
			return fmt.Errorf("automation %q: step %d: agent %q: %w", automationID, stepIndex, step.Ref, err)
		}
		_, err := sendTurnHeadless(ctx, conv, step.Prompt, timeout)
		return wrapStepErr(automationID, stepIndex, err)
	case StepWorkflow:
		return s.runWorkflowStep(ctx, automationID, runID, stepIndex, workDir, step)
	default:
		return fmt.Errorf("automation %q: step %d: unknown step kind %d", automationID, stepIndex, int(step.Kind))
	}
}

// runWorkflowStep implements D2's StepWorkflow dispatch: the existing
// named path (cliworkflow.NewSessionWorkflowEngine + Start), never an
// in-memory CompiledWorkflow, and AllowPublish is never set (left at its
// zero value false) - delivery stays pending for a human, per D8.
func (s *Service) runWorkflowStep(ctx context.Context, automationID, runID string, stepIndex int, workDir string, step Step) error {
	configPath := cliworkflow.SessionEngineConfigPath(workDir, nil)
	eng := cliworkflow.NewSessionWorkflowEngine(workDir, configPath)
	req := newWorkflowStartRequest(runID, stepIndex, step)
	if _, err := eng.Start(ctx, req); err != nil {
		return fmt.Errorf("automation %q: step %d: workflow %q: %w", automationID, stepIndex, step.Ref, err)
	}
	return nil
}

// newWorkflowStartRequest builds the workflowledger.StartRequest for one
// StepWorkflow step. Factored out of runWorkflowStep so a test can assert
// AllowPublish stays at its zero value (false) without needing a live
// workflow engine to observe it - D8's "never set for StepWorkflow"
// requirement is a property of the REQUEST this package builds, not of
// the engine it hands the request to.
func newWorkflowStartRequest(runID string, stepIndex int, step Step) workflowledger.StartRequest {
	inputs := make(map[string]any, len(step.Inputs))
	for k, v := range step.Inputs {
		inputs[k] = v
	}
	return workflowledger.StartRequest{
		Workflow:      step.Ref,
		InvocationKey: runID + ":" + strconv.Itoa(stepIndex),
		Inputs:        inputs,
		// AllowPublish is deliberately left at its zero value (false):
		// D8 requires this for every automation-dispatched StepWorkflow,
		// unattended or not - delivery stays pending for a human.
	}
}

// wrapStepErr names the automation/step index in a step-dispatch error,
// or returns nil unchanged.
func wrapStepErr(automationID string, stepIndex int, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("automation %q: step %d: %w", automationID, stepIndex, err)
}
