package cli

// Stack chunk admission (plan D2/D3/D8, §5a): the drive pass that reconciles
// the stack, admits chunk runs in topological order with stable invocation
// keys, applies the merge policy per chunk, and finally admits the one
// full-suite integration run once every chunk is merged.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// driveStack reconciles the stack and admits chunk runs in topological order
// until the stack is fully merged or the driver must stop for a human grant
// (policy A) or a failure (halt-on-failure). It is resumable: re-running
// drive after a stop picks up from durable state.
func driveStack(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error {
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
			halt, err := driveChunk(ctx, prepared, ledger, stackID, chunkID, prBase, part, policy, allowPublish, planInputs, stdout, stderr)
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
	return driveIntegrationRun(ctx, prepared, ledger, stackID, prBase, policy, planInputs, stdout, stderr)
}

// driveChunk admits and runs one chunk, then applies the merge policy.
// halt=true means the driver must stop: policy A waits for the human publish
// grant, and any terminal failure halts the stack (halt-on-failure).
func driveChunk(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, chunkID, prBase, part, policy string, allowPublish bool, planInputs map[string]string, stdout, stderr io.Writer) (bool, error) {
	// A live run already exists for this chunk's key: leave it alone (F15 -
	// never admit a duplicate).
	if run, found, err := stackRunRef(prepared.repo, stackID, chunkID); err == nil && found {
		if isResumableRunStatus(run.Status) {
			fmt.Fprintf(stdout, "chunk=%s run=%s already in flight (%s); re-run drive after it settles\n", chunkID, run.RunID, run.Status)
			return true, nil
		}
	}
	_ = ledger.TransitionTask(stackID, chunkID, stackStatusRunning)
	inputs, snapshot := chunkRunInputs(planInputs, chunkID, prBase, part)
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
		if policy == "auto" || allowPublish {
			if err := deliverRunWithStore(ctx, prepared.root, prepared.res, prepared.store, prepared.repo, snap.RunID, true, false, stdout, stderr); err != nil {
				return true, fmt.Errorf("chunk %s auto-delivery failed: %w", chunkID, err)
			}
			_ = ledger.TransitionTask(stackID, chunkID, stackStatusPublished)
			fmt.Fprintf(stdout, "chunk=%s published; merge queue will merge; waiting for the merge\n", chunkID)
			return true, nil // sequential create-merge (v1): one chunk per drive pass
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
func driveIntegrationRun(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, prBase, policy string, planInputs map[string]string, stdout, stderr io.Writer) error {
	chunkID := stackIntegrationChunkID
	if run, found, err := stackRunRef(prepared.repo, stackID, chunkID); err == nil && found {
		fmt.Fprintf(stdout, "integration run already exists: run=%s status=%s\n", run.RunID, run.Status)
		return nil
	}
	inputs, snapshot := chunkRunInputs(planInputs, chunkID, prBase, "")
	snap, err := admitStackChunkRun(prepared, stackID, chunkID, inputs, snapshot, stdout, stderr)
	if err != nil {
		return fmt.Errorf("integration run failed: %w", err)
	}
	fmt.Fprintf(stdout, "integration run=%s status=%s\n", snap.RunID, snap.Status)
	if snap.Status == workflowledger.RunStatusDeliveryPending && policy == "auto" {
		return deliverRunWithStore(ctx, prepared.root, prepared.res, prepared.store, prepared.repo, snap.RunID, true, false, stdout, stderr)
	}
	if snap.Status == workflowledger.RunStatusDeliveryPending {
		fmt.Fprintf(stdout, "integration run awaits the publish grant: mivia workflow deliver %s --allow-publish\n", snap.RunID)
	}
	return nil
}
