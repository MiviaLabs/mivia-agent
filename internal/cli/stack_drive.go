package cli

// mivia stack drive (plan D2/D3/D8, §5a): the driver loop. On start it
// reconciles every chunk task against its run and git merge state (idempotent
// recovery), then admits chunk runs in topological order with stable
// admission keys, honors the merge policy (A approve / B auto), and finishes
// with one full-suite integration run.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// runStackDrive parses `stack drive <workflow> [--stack <id>]` and runs the
// driver loop.
func runStackDrive(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	name, stackFlag, rest, err := parseStackWorkflowArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("stack drive: unexpected argument %q", rest[0])
	}
	prepared, err := prepareWorkflowRun(name, workspaceRoot, configPath, nil)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	defer prepared.closeFn()
	logMCPWarnings(stderr, prepared.res)

	wf := prepared.compiled
	if wf.Stacking == nil || !wf.Stacking.Enabled {
		return fmt.Errorf("stack drive: workflow %q is not stacking-enabled", name)
	}
	if !wf.DeliveryActive() {
		return fmt.Errorf("stack drive: workflow %q has no active delivery policy; stacking requires chunk PR delivery", name)
	}
	stackID, err := resolveStackID(prepared.repo, name, stackFlag)
	if err != nil {
		return err
	}
	ledger := tasks.NewStore(prepared.store)

	planOutput, err := loadStackPlanOutput(prepared.repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	mode, chunks, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if mode == "single" || mode == "no_bug" {
		fmt.Fprintf(stdout, "stack %s: %s - nothing to stack\n", stackID, mode)
		return nil
	}
	if len(chunks) == 0 {
		return fmt.Errorf("stack drive: stack %s has a multi plan with no chunks", stackID)
	}
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	return driveStack(prepared, ledger, stackID, chunks, stdout, stderr)
}

// driveStack reconciles the stack and admits chunk runs in topological order
// until the stack is fully merged or the driver must stop for a human grant
// (policy A) or a failure (halt-on-failure). It is resumable: re-running
// drive after a stop picks up from durable state.
func driveStack(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, stdout, stderr io.Writer) error {
	repo := prepared.repo
	checker := gitMergeChecker{
		git: workflowDeliverGit,
		gc:  delivery.GitContext{Dir: prepared.root, GitDir: filepath.Join(prepared.root, ".git")},
	}
	actions, err := reconcileStack(ledger, repo, checker, stackID, stackMaxChunkAttempts)
	if err != nil {
		return fmt.Errorf("stack drive: reconcile: %w", err)
	}
	for _, a := range actions {
		switch a.Action {
		case stackActionMarkFailed:
			return fmt.Errorf("stack drive: stack %s halted: chunk %s failed terminally (%s)", stackID, a.TaskID, a.Note)
		case stackActionReopen:
			fmt.Fprintf(stdout, "chunk=%s reopened (%s)\n", a.TaskID, a.Note)
		case stackActionMarkMerged:
			fmt.Fprintf(stdout, "chunk=%s marked merged (git)\n", a.TaskID)
		case stackActionDeliver:
			fmt.Fprintf(stdout, "chunk=%s run reached delivery; publish grant required\n", a.TaskID)
		}
	}

	order, err := stackTopologicalOrder(chunks)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	merged := stackMergedSet(byID)

	prBase, err := stackPRBase(prepared.compiled)
	if err != nil {
		return err
	}
	policy := prepared.compiled.Stacking.MergePolicy

	for {
		wave := nextAdmissionWave(byID, merged, order)
		if len(wave) == 0 {
			break
		}
		for _, chunkID := range wave {
			index := chunkPartIndex(chunkID, order)
			part := fmt.Sprintf("%d/%d", index+1, len(order))
			halt, err := driveChunk(prepared, ledger, stackID, chunkID, prBase, part, policy, stdout, stderr)
			if err != nil {
				return fmt.Errorf("stack drive: chunk %s: %w", chunkID, err)
			}
			if halt {
				return nil
			}
			// Refresh durable state after the chunk settled so the next
			// wave sees freshly merged dependencies.
			byID, err = stackTaskMap(ledger, stackID)
			if err != nil {
				return err
			}
			merged = stackMergedSet(byID)
		}
	}

	if !allChunksMerged(chunks, merged) {
		fmt.Fprintf(stdout, "stack %s: chunks remain unmerged; re-run `mivia stack drive` after merges land\n", stackID)
		return nil
	}
	fmt.Fprintf(stdout, "all chunks merged; admitting the final integration run\n")
	return driveIntegrationRun(prepared, ledger, stackID, prBase, policy, stdout, stderr)
}

// driveChunk admits and runs one chunk, then applies the merge policy.
// halt=true means the driver must stop: policy A waits for the human publish
// grant, and any terminal failure halts the stack (halt-on-failure).
func driveChunk(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, chunkID, prBase, part, policy string, stdout, stderr io.Writer) (bool, error) {
	// A live run already exists for this chunk's key: leave it alone (F15 -
	// never admit a duplicate).
	if run, found, err := stackRunRef(prepared.repo, stackID, chunkID); err == nil && found {
		if isResumableRunStatus(run.Status) {
			fmt.Fprintf(stdout, "chunk=%s run=%s already in flight (%s); re-run drive after it settles\n", chunkID, run.RunID, run.Status)
			return true, nil
		}
	}
	_ = ledger.TransitionTask(stackID, chunkID, stackStatusRunning)
	inputs, snapshot := chunkRunInputs(chunkID, prBase, part)
	snap, err := admitStackChunkRun(prepared, stackID, chunkID, inputs, snapshot, stdout, stderr)
	if err != nil {
		attempts := stackAttemptCount(ledger, stackID, chunkID)
		act := reopenOrFail(tasks.Task{ID: chunkID, Attempts: attempts}, stackMaxChunkAttempts)
		_ = applyReconcileAction(ledger, stackID, act)
		if act.Action == stackActionMarkFailed {
			return true, fmt.Errorf("chunk %s failed terminally after %d attempts; stack halts", chunkID, attempts)
		}
		return true, fmt.Errorf("chunk %s run failed; reopened for retry (%s)", chunkID, act.Note)
	}
	fmt.Fprintf(stdout, "chunk=%s run=%s status=%s\n", chunkID, snap.RunID, snap.Status)
	switch snap.Status {
	case workflowledger.RunStatusDeliveryPending:
		if policy == "auto" {
			if err := deliverRunWithStore(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, snap.RunID, true, false, stdout, stderr); err != nil {
				return true, fmt.Errorf("chunk %s auto-delivery failed: %w", chunkID, err)
			}
			_ = ledger.TransitionTask(stackID, chunkID, stackStatusPublished)
			fmt.Fprintf(stdout, "chunk=%s published; merge queue will merge; re-run drive after the merge lands\n", chunkID)
			return true, nil // sequential create-merge (v1): one chunk per drive
		}
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusReviewed)
		fmt.Fprintf(stdout, "chunk=%s awaits the publish grant: mivia workflow deliver %s --allow-publish\n", chunkID, snap.RunID)
		return true, nil // policy A: the human publish grant is the single checkpoint (D1)
	case workflowledger.RunStatusSucceeded:
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusImplemented)
		return false, nil
	case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
		attempts := stackAttemptCount(ledger, stackID, chunkID)
		act := reopenOrFail(tasks.Task{ID: chunkID, Attempts: attempts}, stackMaxChunkAttempts)
		_ = applyReconcileAction(ledger, stackID, act)
		if act.Action == stackActionMarkFailed {
			return true, fmt.Errorf("chunk %s failed terminally after %d attempts; stack halts", chunkID, attempts)
		}
		fmt.Fprintf(stdout, "chunk=%s run failed; reopened for retry (%s)\n", chunkID, act.Note)
		return true, nil
	default:
		fmt.Fprintf(stdout, "chunk=%s run settled at %s; leaving for reconciliation\n", chunkID, snap.Status)
		return true, nil
	}
}

// admitStackChunkRun admits and runs one chunk-mode workflow run with the
// chunk's stable invocation key (F15). It reuses the exact controller build
// path the workflow CLI uses; the invocation key is the only addition.
func admitStackChunkRun(prepared *preparedWorkflowRun, stackID, chunkID string, inputs map[string]any, inputSnapshot map[string]string, stdout, stderr io.Writer) (workflowledger.RunSnapshot, error) {
	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	defer finishExecution()
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, inputs, inputSnapshot, prepared.raw, runID, nil, nil)
	if err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	defer built.Dispatcher.Close()
	admitted := false
	defer func() {
		if !admitted {
			built.Cleanup()
		}
	}()
	key, err := stackAdmissionKey(stackID, chunkID)
	if err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	built.Admission.InvocationKey = key
	if err := built.Controller.SetAdmission(built.Admission); err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	wireCLIWorkflowProgress(&built, stderr)
	if err := built.Controller.Start(context.Background()); err != nil {
		return workflowledger.RunSnapshot{}, err
	}
	admitted = true
	snap, err := built.Controller.Run(context.Background())
	if err != nil {
		settleCLIRunFailure(prepared.repo, built.Controller.RunID, err)
		return snap, err
	}
	return snap, nil
}

// driveIntegrationRun admits the final full-suite run (stack_mode=single runs
// the workflow's own plan+implement steps inline) after every chunk merged.
func driveIntegrationRun(prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, prBase, policy string, stdout, stderr io.Writer) error {
	chunkID := stackIntegrationChunkID
	if run, found, err := stackRunRef(prepared.repo, stackID, chunkID); err == nil && found {
		fmt.Fprintf(stdout, "integration run already exists: run=%s status=%s\n", run.RunID, run.Status)
		return nil
	}
	inputs, snapshot := chunkRunInputs(chunkID, prBase, "")
	snap, err := admitStackChunkRun(prepared, stackID, chunkID, inputs, snapshot, stdout, stderr)
	if err != nil {
		return fmt.Errorf("integration run failed: %w", err)
	}
	fmt.Fprintf(stdout, "integration run=%s status=%s\n", snap.RunID, snap.Status)
	if snap.Status == workflowledger.RunStatusDeliveryPending && policy == "auto" {
		return deliverRunWithStore(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, snap.RunID, true, false, stdout, stderr)
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		fmt.Fprintf(stdout, "integration run awaits the publish grant: mivia workflow deliver %s --allow-publish\n", snap.RunID)
	}
	return nil
}

// loadStackPlanOutput reads the succeeded decompose step output of a
// plan-mode run from the run ledger (F1/F8: the plan is a run output).
func loadStackPlanOutput(repo workflowledger.Repository, stackID string) ([]byte, error) {
	attempts, err := repo.ListStepAttempts(context.Background(), stackID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return nil, fmt.Errorf("plan run %q not found", stackID)
		}
		return nil, err
	}
	for _, a := range attempts {
		if a.StepID == stackDecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded && a.OutputRef != "" {
			data, err := repo.LoadContent(context.Background(), a.OutputRef)
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("plan run %q has no succeeded decompose output", stackID)
}

// seedStackLedger records the plan artifact and the chunk tasks (D8).
// Re-entry is idempotent: identical records are no-ops, conflicting ones are
// errors.
func seedStackLedger(ledger *tasks.Store, stackID string, chunks []ChunkPlan) error {
	if _, err := ledger.StorePlan(tasks.Plan{ID: stackID, Scope: stackScope(stackID), Schema: stackPlanSchema}); err != nil {
		return err
	}
	for _, c := range chunks {
		task := tasks.Task{
			ID: c.ID, PlanRef: stackID, Scope: stackScope(stackID),
			Status: stackStatusPlanned, Deps: append([]string(nil), c.DependsOn...),
		}
		if err := ledger.CreateTask(task); err != nil {
			return err
		}
	}
	return nil
}

// chunkRunInputs builds the admission inputs and snapshot for one chunk-mode
// run (D3 reserved inputs). The integration run uses the same shape with an
// empty stack_part.
func chunkRunInputs(chunkID, prBase, stackPart string) (map[string]any, map[string]string) {
	inputs := map[string]any{
		"stack_mode": "chunk",
		"chunk":      chunkID,
		"pr_base":    prBase,
	}
	snapshot := map[string]string{
		"stack_mode": "chunk",
		"chunk":      chunkID,
		"pr_base":    prBase,
	}
	if stackPart != "" {
		inputs["stack_part"] = stackPart
		snapshot["stack_part"] = stackPart
	}
	return inputs, snapshot
}

// stackPRBase returns the delivery base branch the chunk PRs branch from:
// the workflow's delivery policy base (delivery honors pr_base, S4).
func stackPRBase(wf *compiler.CompiledWorkflow) (string, error) {
	if wf == nil || wf.Delivery == nil {
		return "", fmt.Errorf("workflow has no delivery policy")
	}
	policy, ok := delivery.FromCompiled(wf)
	if !ok {
		return "", fmt.Errorf("workflow delivery policy is not active")
	}
	if strings.TrimSpace(policy.Base) == "" {
		return "", fmt.Errorf("workflow delivery policy has no base branch")
	}
	return policy.Base, nil
}

// reconcileStack applies the §5a recovery actions for every chunk task of
// the stack: task ledger x run ledger x git merge state, idempotently.
func reconcileStack(ledger *tasks.Store, repo workflowledger.Repository, checker MergeChecker, stackID string, maxAttempts int) ([]ReconcileAction, error) {
	list, err := ledger.ListTasksByScope(stackScope(stackID))
	if err != nil {
		return nil, err
	}
	var actions []ReconcileAction
	for _, t := range list {
		run, found, err := stackRunRef(repo, stackID, t.ID)
		if err != nil {
			return nil, err
		}
		info := RunInfo{Present: found}
		if found {
			info.Status = string(run.Status)
		}
		merged := false
		// Only a delivered run can be merged; a never-delivered run with a
		// missing remote ref must not be mistaken for a merge.
		if found && (run.Status == workflowledger.RunStatusDeliveryPending || run.Status == workflowledger.RunStatusSucceeded) {
			if head := stackHeadBranch(run); head != "" {
				merged, err = checker.Merged(context.Background(), head)
				if err != nil {
					return nil, err
				}
			}
		}
		t.Attempts = stackAttemptCount(ledger, stackID, t.ID)
		act := reconcileTask(t, info, merged, maxAttempts)
		actions = append(actions, act)
		if err := applyReconcileAction(ledger, stackID, act); err != nil {
			return nil, err
		}
	}
	return actions, nil
}

// stackTaskMap loads every stack task by id for the drive loop.
func stackTaskMap(ledger *tasks.Store, stackID string) (map[string]tasks.Task, error) {
	list, err := ledger.ListTasksByScope(stackScope(stackID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]tasks.Task, len(list))
	for _, t := range list {
		out[t.ID] = t
	}
	return out, nil
}

// stackMergedSet returns the set of chunk ids whose tasks are merged.
func stackMergedSet(byID map[string]tasks.Task) map[string]bool {
	out := make(map[string]bool, len(byID))
	for id, t := range byID {
		if t.Status == stackStatusMerged {
			out[id] = true
		}
	}
	return out
}

// allChunksMerged reports whether every chunk in the plan is merged.
func allChunksMerged(chunks []ChunkPlan, merged map[string]bool) bool {
	for _, c := range chunks {
		if !merged[c.ID] {
			return false
		}
	}
	return true
}

// chunkPartIndex returns the 0-based position of a chunk in dependency order,
// for the canonical "k/N" stack_part.
func chunkPartIndex(chunkID string, order []string) int {
	for i, id := range order {
		if id == chunkID {
			return i
		}
	}
	return 0
}

// allMerged reports whether every known task is merged (used by the wave
// loop to decide whether to keep driving).
func allMerged(byID map[string]tasks.Task) bool {
	if len(byID) == 0 {
		return false
	}
	for _, t := range byID {
		if t.Status != stackStatusMerged {
			return false
		}
	}
	return true
}

// isResumableRunStatus mirrors the ledger's resumable set for the driver.
func isResumableRunStatus(status workflowledger.RunStatus) bool {
	switch status {
	case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
		return true
	}
	return false
}
