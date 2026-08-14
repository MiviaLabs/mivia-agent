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
	"sync"

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
	if err := reportReconcileActions(stackID, actions, stdout); err != nil {
		return err
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

	maxConcurrent := 1
	if prepared.compiled.Stacking.MaxConcurrentChunks > 0 {
		maxConcurrent = prepared.compiled.Stacking.MaxConcurrentChunks
	}

	for {
		wave := nextAdmissionWave(byID, merged, order)
		if len(wave) == 0 {
			break
		}
		results := driveWave(ctx, prepared, ledger, stackID, wave, order, prBase, policy, allowPublish, planInputs, maxConcurrent, stdout, stderr)
		halt, err := resolveWaveResults(results)
		if err != nil {
			return err
		}
		if halt {
			return nil
		}
		// Refresh durable state after the whole wave settled so the next
		// wave sees freshly merged dependencies.
		byID, err = stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		merged = stackMergedSet(byID)
	}

	if !allChunksMerged(chunks, merged) {
		fmt.Fprintf(stdout, "stack %s: chunks remain unmerged; re-run `mivia stack drive` after merges land\n", stackID)
		return nil
	}
	fmt.Fprintf(stdout, "all chunks merged; admitting the final integration run\n")
	return driveIntegrationRun(ctx, prepared, ledger, stackID, prBase, policy, planInputs, stdout, stderr)
}

// reportReconcileActions prints one line per reconcile action and returns an
// error if any chunk failed terminally (stack-wide halt).
func reportReconcileActions(stackID string, actions []ReconcileAction, stdout io.Writer) error {
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
	return nil
}

// resolveWaveResults decides driveStack's next move from one wave's outcomes.
// Every chunk in the wave is independent by construction (nextAdmissionWave
// only admits a chunk once its deps are merged, and merged chunks are never
// re-admitted into the same wave), so dispatch order carries no dependency
// meaning. Resolution order does: scanning in wave (topological) order first
// for any error, then for any halt, keeps a single-chunk wave
// (max_concurrent_chunks=1, or any wave of size 1) byte-identical to the old
// sequential loop's outcome, while a multi-chunk wave now runs every member
// concurrently instead of processing only the first one per pass.
func resolveWaveResults(results []driveWaveResult) (halt bool, err error) {
	for _, r := range results {
		if r.err != nil {
			return false, fmt.Errorf("stack drive: chunk %s: %w", r.chunkID, r.err)
		}
	}
	for _, r := range results {
		if r.halt {
			return true, nil
		}
	}
	return false, nil
}

// driveWaveResult is one chunk's outcome from a concurrently-dispatched wave.
type driveWaveResult struct {
	chunkID string
	halt    bool
	err     error
}

// driveWave dispatches every chunk in wave concurrently, bounded by
// maxConcurrent chunk runs in flight at once. Each chunk already gets its own
// isolated worktree via admitStackChunkRun's per-runID ensureRunWorktree
// (through workflowRunBuild), so concurrent dispatch is safe by construction;
// this function adds no new isolation, only bounded fan-out. Results are
// returned in wave order (index-addressed, not completion order) so the
// caller's resolution stays deterministic. stdout/stderr are wrapped in a
// mutex so concurrent Fprintf calls from different chunks never interleave
// mid-line.
func driveWave(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, wave []string, order []string, prBase, policy string, allowPublish bool, planInputs map[string]string, maxConcurrent int, stdout, stderr io.Writer) []driveWaveResult {
	syncStdout := newSyncWriter(stdout)
	syncStderr := newSyncWriter(stderr)
	work := func(chunkID string) (bool, error) {
		index, err := chunkPartIndex(chunkID, order)
		if err != nil {
			return false, fmt.Errorf("stack drive: %w", err)
		}
		part := fmt.Sprintf("%d/%d", index+1, len(order))
		return driveChunk(ctx, prepared, ledger, stackID, chunkID, prBase, part, policy, allowPublish, planInputs, syncStdout, syncStderr)
	}
	return dispatchWave(wave, maxConcurrent, work)
}

// dispatchWave runs work for every id in wave concurrently, bounded by
// maxConcurrent in-flight calls at once, and returns results in wave order
// (index-addressed, not completion order) so callers get deterministic
// resolution regardless of goroutine scheduling. Factored out of driveWave so
// the concurrency/aggregation contract (bounded fan-out, stable result order,
// no dropped or duplicated results) is unit-testable without standing up the
// full workflow-run fixture machinery driveChunk itself requires.
func dispatchWave(wave []string, maxConcurrent int, work func(chunkID string) (bool, error)) []driveWaveResult {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	results := make([]driveWaveResult, len(wave))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for i, chunkID := range wave {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, chunkID string) {
			defer wg.Done()
			defer func() { <-sem }()
			halt, err := work(chunkID)
			results[i] = driveWaveResult{chunkID: chunkID, halt: halt, err: err}
		}(i, chunkID)
	}
	wg.Wait()
	return results
}

// syncWriter serializes concurrent writes to an underlying io.Writer so
// output from concurrently-dispatched chunks never interleaves mid-line.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newSyncWriter(w io.Writer) *syncWriter {
	return &syncWriter{w: w}
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// driveChunk admits and runs one chunk, then applies the merge policy.
// halt=true means the driver must stop: policy A waits for the human publish
// grant, and any terminal failure halts the stack (halt-on-failure).
func driveChunk(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID, chunkID, prBase, part, policy string, allowPublish bool, planInputs map[string]string, stdout, stderr io.Writer) (bool, error) {
	// A live run already exists for this chunk's key: leave it alone (F15 -
	// never admit a duplicate). This covers crash recovery (a prior process
	// admitted the run but died before its task-status transition landed);
	// the CAS below covers the concurrent-goroutine case within one process.
	if run, found, err := stackRunRef(prepared.repo, stackID, chunkID); err == nil && found {
		if isResumableRunStatus(run.Status) {
			fmt.Fprintf(stdout, "chunk=%s run=%s already in flight (%s); re-run drive after it settles\n", chunkID, run.RunID, run.Status)
			return true, nil
		}
	}
	// Atomic check-and-claim: only a caller that observes the task still in
	// an admissible pre-status (planned/queued/blocked/reopened) wins the
	// transition to running. A concurrent caller racing for the same chunk
	// sees claimed=false and backs off cleanly instead of double-admitting.
	claimed, err := ledger.TransitionTaskCAS(stackID, chunkID, stackAdmissiblePreStatuses, stackStatusRunning)
	if err != nil {
		return true, fmt.Errorf("chunk %s: admission transition failed: %w", chunkID, err)
	}
	if !claimed {
		fmt.Fprintf(stdout, "chunk=%s already claimed by a concurrent admission; skipping\n", chunkID)
		return true, nil
	}
	inputs, snapshot := chunkRunInputs(planInputs, chunkID, prBase, part)
	snap, err := admitStackChunkRun(prepared, stackID, chunkID, inputs, snapshot, stdout, stderr)
	if err != nil {
		act, actErr := reconcileReopenOrFail(ledger, stackID, chunkID)
		if actErr != nil {
			return true, fmt.Errorf("chunk %s: reopen/fail transition failed: %w", chunkID, actErr)
		}
		if act.Action == stackActionMarkFailed {
			return true, fmt.Errorf("chunk %s failed terminally after %d attempts; stack halts", chunkID, act.Attempts)
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
		act, err := reconcileReopenOrFail(ledger, stackID, chunkID)
		if err != nil {
			return true, fmt.Errorf("chunk %s: reopen/fail transition failed: %w", chunkID, err)
		}
		if act.Action == stackActionMarkFailed {
			return true, fmt.Errorf("chunk %s failed terminally after %d attempts; stack halts", chunkID, act.Attempts)
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

// admitDecomposeContinuationRun admits and runs a decompose-continuation run
// for wave N (§12.1 incremental decompose): a fresh run of the same compiled
// workflow, started directly at the decompose step (stack_mode=
// decompose_continue), seeded with remaining_scope instead of a plan-step
// output. It reuses admitStackChunkRun's exact admission path (same
// per-run worktree isolation, same stable-key resumability), differing only
// in which reserved inputs it carries. Returns the next wave's chunks and
// whether decompose declared yet more scope beyond THIS wave.
func admitDecomposeContinuationRun(prepared *preparedWorkflowRun, stackID string, wave int, remainingScope string, planInputs map[string]string, stdout, stderr io.Writer) (chunks []ChunkPlan, hasMore bool, nextRemainingScope string, err error) {
	key, err := stackDecomposeContinueKey(stackID, wave)
	if err != nil {
		return nil, false, "", err
	}
	inputs := make(map[string]any, len(planInputs)+2)
	snapshot := make(map[string]string, len(planInputs)+2)
	for k, v := range planInputs {
		inputs[k] = v
		snapshot[k] = v
	}
	inputs["stack_mode"] = "decompose_continue"
	inputs["remaining_scope"] = remainingScope
	snapshot["stack_mode"] = "decompose_continue"
	snapshot["remaining_scope"] = remainingScope

	runID := newCLIWorkflowRunID()
	finishExecution, err := beginWorkflowExecution(prepared.root, contextStorePath(prepared.root, prepared.res.Subagents), runID)
	if err != nil {
		return nil, false, "", err
	}
	defer finishExecution()
	built, err := workflowRunBuild(prepared.root, prepared.res, prepared.store, prepared.repo, prepared.compiled, prepared.refBase, inputs, snapshot, prepared.raw, runID, nil, nil)
	if err != nil {
		return nil, false, "", err
	}
	defer built.Dispatcher.Close()
	admitted := false
	defer func() {
		if !admitted {
			built.Cleanup()
		}
	}()
	built.Admission.InvocationKey = key
	if err := built.Controller.SetAdmission(built.Admission); err != nil {
		return nil, false, "", err
	}
	wireCLIWorkflowProgress(&built, stderr)
	if err := built.Controller.Start(context.Background()); err != nil {
		return nil, false, "", err
	}
	admitted = true
	snap, err := built.Controller.Run(context.Background())
	if err != nil {
		settleCLIRunFailure(prepared.repo, built.Controller.RunID, err)
		return nil, false, "", fmt.Errorf("decompose continuation wave %d failed: %w", wave, err)
	}
	fmt.Fprintf(stdout, "stack %s: decompose continuation wave %d run=%s status=%s\n", stackID, wave, snap.RunID, snap.Status)
	if snap.Status != workflowledger.RunStatusSucceeded && snap.Status != workflowledger.RunStatusDeliveryPending {
		return nil, false, "", fmt.Errorf("decompose continuation wave %d settled at %s, not succeeded", wave, snap.Status)
	}
	raw, err := loadStackPlanOutput(prepared.repo, snap.RunID)
	if err != nil {
		return nil, false, "", fmt.Errorf("decompose continuation wave %d: %w", wave, err)
	}
	mode, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(raw)
	if err != nil {
		return nil, false, "", fmt.Errorf("decompose continuation wave %d: %w", wave, err)
	}
	if mode != "multi" {
		return nil, false, "", fmt.Errorf("decompose continuation wave %d: stack_mode %q; want multi (a continuation wave cannot change the stack's mode)", wave, mode)
	}
	return waveChunks, waveHasMore, waveRemaining, nil
}

// stackDecomposeContinueAdmit is the decompose-continuation admission entry
// point the drive's wave recovery calls (loadAllStackChunksForDrive) to
// re-admit a failed wave with a fresh run under the same stable key. It is a
// package variable so tests can stub the admission without running a full
// controller; production always points at admitDecomposeContinuationRun.
var stackDecomposeContinueAdmit = admitDecomposeContinuationRun

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
