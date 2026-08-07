package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	workflowResumeOpenStore    = openWorkflowStore
	workflowResumeInstallHooks = installHookSession
	workflowResumeBuild        = buildWorkflowController
	workflowResumeSetAdmission = func(b workflowControllerBuild) error {
		return b.Controller.SetAdmission(b.Admission)
	}
	workflowResumeSetForce = func(b workflowControllerBuild) error {
		return b.Controller.SetForceResume(true)
	}
	workflowResumeRun = func(ctx context.Context, b workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		return b.Controller.Run(ctx)
	}
	// workflowResumeJoinBound bounds the pre-Run in-flight attempt join for runs
	// that carry no deadline of their own, so a coordinator child whose run never
	// settles cannot park resume forever (runs WITH a deadline bound the join to
	// the time remaining before it; see workflowResumeJoinCtx). Injectable for
	// tests.
	workflowResumeJoinBound = 10 * time.Minute
)

func executeWorkflowResume(runID, root, configPath string, force bool, stdout, _ io.Writer) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := workflowResumeOpenStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	releaseExecution, err := acquireWorkflowExecutionLock(contextStorePath(work.Abs, res.Subagents), runID)
	if err != nil {
		return err
	}
	defer releaseExecution()

	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	// A delivery_pending run is settled: the workflow body is complete and the
	// result is waiting for publication. Resume must refuse BEFORE any terminal
	// reconciliation — delivery is a separate host-owned step, and reconciling
	// here would CAS delivery_pending->delivery_pending (invalid) or, under an
	// older classification, delivery_pending->succeeded (skipping delivery).
	if run.Status == workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("workflow run %q is waiting for delivery; deliver with: mivia workflow deliver %s --allow-publish", runID, runID)
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return err
	}
	snapshot, compiled, inputs, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil {
		return err
	}
	terminal, err := reconcileWorkflowTerminal(ctx, repo, runID, compiled.DeliveryActive(), stdout)
	if err != nil || terminal {
		return err
	}
	if !force {
		return fmt.Errorf("workflow run %q is not terminal; pass --force only after the prior executor stopped", runID)
	}
	uninstallHooks, err := workflowResumeInstallHooks(work.Abs, false)
	if err != nil {
		return err
	}
	defer uninstallHooks()
	built, err := workflowResumeBuild(work.Abs, res, store, repo, compiled, "", inputs, snapshot.Inputs, snapshot.DefinitionTOML, runID, &snapshot, &run)
	if err != nil {
		return err
	}
	defer built.Dispatcher.Close()
	if err := workflowResumeSetAdmission(built); err != nil {
		return err
	}
	if err := workflowResumeSetForce(built); err != nil {
		return err
	}
	if err := repo.ClearRunClaim(ctx, runID); err != nil {
		return fmt.Errorf("clear interrupted workflow claim: %w", err)
	}
	if err := joinInFlightAttempts(ctx, built, repo, runID, stdout); err != nil {
		return err
	}
	snap, err := workflowResumeRun(ctx, built)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, snap.Status)
	return err
}

// joinInFlightAttempts consumes PlanResume.AttemptsInFlight: it joins each
// recorded in-flight attempt's coordinator run through the controller BEFORE
// the Run loop starts, so a completed (or failed) child settles the attempt
// with its outcome and route instead of being orphaned by a fresh re-dispatch
// (recovery.go: a recorded attempt is joined, never re-dispatched). Attempts
// whose child never ran are left in-flight for the controller's Advance to
// interrupt and re-dispatch under the run claim. The join is idempotent:
// attempts already terminal (or superseded) are no-ops. On failure the run's
// settled status is reported to stdout before the error is returned.
func joinInFlightAttempts(ctx context.Context, built workflowControllerBuild, repo workflowledger.Repository, runID string, stdout io.Writer) (err error) {
	defer func() {
		if err != nil {
			if settled, getErr := repo.GetRun(ctx, runID); getErr == nil {
				fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, settled.Status)
			}
		}
	}()
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		return err
	}
	if len(plan.AttemptsInFlight) == 0 {
		return nil
	}
	if built.Controller == nil {
		return fmt.Errorf("workflow controller is nil; cannot join %d in-flight attempt(s)", len(plan.AttemptsInFlight))
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	// The join is bounded so a child whose coordinator run never settles cannot
	// park resume forever: bound to the run's own deadline when present (time.Until
	// it), otherwise a fixed workflowResumeJoinBound. On expiry the join is
	// abandoned with a clear error and the attempt stays in-flight for the
	// controller's normal reconciliation (Advance interrupts and re-dispatches it
	// under the run claim on a subsequent resume).
	joinCtx, cancelJoin := workflowResumeJoinCtx(ctx, run)
	defer cancelJoin()
	for _, inFlight := range plan.AttemptsInFlight {
		if err := built.Controller.JoinInFlightAttempt(joinCtx, inFlight); err != nil {
			return fmt.Errorf("join in-flight attempt %s for step %q: %w", inFlight.AttemptID, inFlight.StepID, err)
		}
		if joinCtx.Err() != nil {
			return fmt.Errorf("join in-flight attempt %s for step %q did not settle within the resume join bound; leaving it in-flight for controller reconciliation: %w", inFlight.AttemptID, inFlight.StepID, joinCtx.Err())
		}
	}
	return nil
}

// workflowResumeJoinCtx derives the bounded context for the pre-Run in-flight
// attempt join: the run's own deadline when present (time.Until it), otherwise
// the fixed workflowResumeJoinBound. An already-expired deadline yields an
// immediately-expired context so the join fails fast instead of hanging.
func workflowResumeJoinCtx(parent context.Context, run workflowledger.RunSnapshot) (context.Context, context.CancelFunc) {
	if run.DeadlineAt != nil {
		return context.WithDeadline(parent, *run.DeadlineAt)
	}
	return context.WithTimeout(parent, workflowResumeJoinBound)
}

func validateWorkflowResumeSnapshot(run workflowledger.RunSnapshot, raw []byte) (workflowledger.Snapshot, *compiler.CompiledWorkflow, map[string]any, error) {
	if run.SnapshotDigest == "" || run.SnapshotDigest != workflowledger.SnapshotDigest(raw) {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow snapshot digest does not match the admitted snapshot")
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if err := snapshot.Validate(); err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if run.InputDigest == "" || run.InputDigest != workflowledger.InputDigest(snapshot.Inputs) {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow input digest does not match the admitted inputs")
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	// Resume is recovery, not admission: the definition was already admitted,
	// so the unbounded-cycle admission check must not strand an in-flight run.
	compiled, err := compiler.CompileForResume(&wf)
	if err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if snapshot.DefinitionDigest != compiled.Digest || run.WorkflowDigest != compiled.Digest {
		return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("workflow definition digest does not match the admitted definition")
	}
	if err := validateWorkflowSnapshotReferences(compiled, snapshot); err != nil {
		return workflowledger.Snapshot{}, nil, nil, err
	}
	if snapshot.Delivery != nil {
		if compiled.Delivery == nil ||
			compiled.Delivery.Mode != snapshot.Delivery.Mode ||
			compiled.Delivery.Provider != snapshot.Delivery.Provider ||
			compiled.Delivery.Base != snapshot.Delivery.Base {
			return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("snapshot delivery policy does not match the admitted definition")
		}
	}
	inputs := make(map[string]any, len(snapshot.Inputs))
	for key, value := range snapshot.Inputs {
		def, ok := compiled.Inputs[key]
		if !ok {
			return workflowledger.Snapshot{}, nil, nil, fmt.Errorf("snapshot contains unknown workflow input %q", key)
		}
		parsed, parseErr := parseWorkflowInputValue(value, def.Type)
		if parseErr != nil {
			return workflowledger.Snapshot{}, nil, nil, parseErr
		}
		inputs[key] = parsed
	}
	return snapshot, compiled, inputs, nil
}

func validateWorkflowSnapshotReferences(wf *compiler.CompiledWorkflow, snapshot workflowledger.Snapshot) error {
	schemas := make(map[string][]byte, len(snapshot.Schemas))
	for name, ref := range snapshot.Schemas {
		if ref.Digest == "" || digestBytes(ref.Bytes) != ref.Digest {
			return fmt.Errorf("snapshot schema %q digest is invalid", name)
		}
		schemas[name] = ref.Bytes
	}
	for _, step := range wf.Steps {
		// Agent-less steps (human_gate, evidence_gate) have no agent
		// admission to pin; only agent-bearing steps are checked.
		if step.Agent == "" {
			continue
		}
		agent, ok := snapshot.Agents[step.Agent]
		if !ok || agent.Digest == "" {
			return fmt.Errorf("snapshot agent %q admission is incomplete", step.Agent)
		}
		if step.Template != "" {
			ref, ok := snapshot.Templates[step.Template]
			if !ok || ref.Digest == "" || digestBytes(ref.Bytes) != ref.Digest {
				return fmt.Errorf("snapshot template %q digest is invalid", step.Template)
			}
		}
	}
	return compiler.ValidateSchemaReferenceBytes(&definition.WorkflowFile{Steps: wf.Steps}, schemas)
}

func reconcileWorkflowTerminal(ctx context.Context, repo workflowledger.Repository, runID string, deliveryActive bool, stdout io.Writer) (bool, error) {
	plan, err := workflowledger.PlanResume(ctx, repo, runID)
	if err != nil {
		return false, err
	}
	if !plan.Terminal {
		return false, nil
	}
	// A run whose derived route reached the success terminal under an active
	// delivery policy must settle at delivery_pending, never succeeded: the
	// delivery phase still has to publish. This mirrors the controller's
	// terminal-route routing for the crash window where the route is durable
	// but the delivery_pending CAS was never recorded.
	if deliveryActive && plan.TerminalStatus == workflowledger.RunStatusSucceeded &&
		plan.Run.Status != workflowledger.RunStatusDeliveryPending {
		plan.TerminalStatus = workflowledger.RunStatusDeliveryPending
	}
	if !workflowledger.IsTerminalRunStatus(plan.Run.Status) && plan.TerminalStatus != plan.Run.Status {
		if err := repo.ClearRunClaim(ctx, runID); err != nil {
			return false, err
		}
		from := plan.Run
		// waiting_approval has no direct edge to a terminal status (the edge
		// table only allows running/failed/canceled/timed_out); step through
		// running first, exactly as the controller's reconcileTerminalRoute
		// does for the approve crash window.
		if from.Status == workflowledger.RunStatusWaitingApproval {
			if err := repo.CompareAndSetRunStatus(ctx, runID, from.Version, workflowledger.RunStatusRunning, nil); err != nil {
				return false, err
			}
			fresh, err := repo.GetRun(ctx, runID)
			if err != nil {
				return false, err
			}
			from = fresh
		}
		if err := repo.CompareAndSetRunStatus(ctx, runID, from.Version, plan.TerminalStatus, nil); err != nil {
			return false, err
		}
		plan.Run, err = repo.GetRun(ctx, runID)
		if err != nil {
			return false, err
		}
	}
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, plan.Run.Status)
	return true, nil
}
