package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

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
	snap, err := workflowResumeRun(ctx, built)
	fmt.Fprintf(stdout, "run_id=%s status=%s\n", runID, snap.Status)
	return err
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
		if err := repo.CompareAndSetRunStatus(ctx, runID, plan.Run.Version, plan.TerminalStatus, nil); err != nil {
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
