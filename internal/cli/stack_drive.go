package cli

// mivia stack drive (plan D2/D3/D8, §5a): the driver loop. On start it
// reconciles every chunk task against its run and git merge state (idempotent
// recovery), then admits chunk runs in topological order with stable
// admission keys, honors the merge policy (A approve / B auto), and finishes
// with one full-suite integration run.

import (
	"context"
	"encoding/json"
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

// loadStackDrivePlanInputs resolves the stack id and loads its plan run's
// declared inputs, so chunk runs can replay them (D3). The driver never runs
// the plan run itself, but every chunk run replays the plan run's declared
// inputs, so this must happen before prepare (required-input validation).
func loadStackDrivePlanInputs(workspaceRoot, configPath, name, stackFlag string) (map[string]string, error) {
	_, repo, closeEarly, err := openStackLedger(workspaceRoot, configPath)
	if err != nil {
		return nil, fmt.Errorf("stack drive: %w", err)
	}
	defer closeEarly()
	stackID, err := resolveStackID(repo, name, stackFlag)
	if err != nil {
		return nil, err
	}
	planInputs, err := stackPlanInputs(repo, stackID)
	if err != nil {
		return nil, fmt.Errorf("stack drive: %w", err)
	}
	return planInputs, nil
}

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
	planInputs, err := loadStackDrivePlanInputs(workspaceRoot, configPath, name, stackFlag)
	if err != nil {
		return err
	}

	rawInputs := make([]string, 0, len(planInputs))
	for k, v := range planInputs {
		rawInputs = append(rawInputs, k+"="+v)
	}
	prepared, err := prepareWorkflowRun(name, workspaceRoot, configPath, rawInputs)
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
	mode, _, _, _, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if mode == "single" || mode == "no_bug" {
		fmt.Fprintf(stdout, "stack %s: %s - nothing to stack\n", stackID, mode)
		return nil
	}
	// Reconstruct the full chunk list across every already-admitted decompose
	// wave (not just the plan run's own first wave): a prior process may have
	// already admitted continuation waves before this invocation. The drive
	// loader recovers a wedged wave instead of failing on it (see
	// loadAllStackChunksForDrive); loadAllStackChunks stays strict for the
	// reconcile sweep.
	chunks, hasMore, remainingScope, err := loadAllStackChunksForDrive(prepared, stackID, planOutput, planInputs, stdout, stderr)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Errorf("stack drive: stack %s has a multi plan with no chunks", stackID)
	}
	if err := seedStackLedger(ledger, stackID, chunks); err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	if err := driveStack(context.Background(), prepared, ledger, stackID, chunks, planInputs, false, stdout, stderr); err != nil {
		return err
	}
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	if err := admitPendingFollowUps(prepared, ledger, stackID, byID, stdout, stderr); err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	return admitNextWaveIfReady(prepared, ledger, stackID, chunks, hasMore, remainingScope, planInputs, stdout, stderr)
}

// driveStackToCompletion drives the stack until every chunk is merged and the
// final integration run is published, waiting out publish grants (policy A)
// and merge-queue times instead of halting for a re-invocation. It is the
// in-command stacking engine: one `workflow run` invocation owns the whole
// stack (plan -> chunks -> per-chunk PRs -> integration) and only returns when
// the stack is complete, a chunk failed terminally, or the process is
// interrupted (the stack stays resumable from durable state). The ctx is the
// drive's stop signal: a cancelled/expired ctx (the session attempt bound)
// returns the cancellation error so the caller can release the run's
// execution flock instead of polling forever. CLI foreground paths pass
// context.Background() and stay unbounded by design.
func driveStackToCompletion(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, hasMore bool, remainingScope string, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error {
	checker := gitMergeChecker{
		git: workflowDeliverGit,
		gc:  delivery.GitContext{Dir: prepared.root, GitDir: filepath.Join(prepared.root, ".git")},
	}
	policy := prepared.compiled.Stacking.MergePolicy
	wave, err := latestDecomposeContinueWave(prepared.repo, stackID)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	for {
		// The drive must stop when its context is done: a stuck merge-queue
		// poll would otherwise hold the plan run's execution flock forever.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stack drive: %w", err)
		}
		// One drive pass admits the next ready wave and applies the merge
		// policy; it halts when a chunk needs a grant or a merge to land.
		if err := driveStack(ctx, prepared, ledger, stackID, chunks, planInputs, allowPublish, stdout, stderr); err != nil {
			return err
		}
		byID, err := stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		// A chunk that just delivered may have left a deferred commit
		// (§5.2-5.3): admit its follow-up PR before deciding whether the
		// stack is complete, so allTasksMerged below waits for it too.
		if err := admitPendingFollowUps(prepared, ledger, stackID, byID, stdout, stderr); err != nil {
			return fmt.Errorf("stack drive: %w", err)
		}
		byID, err = stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		if !allChunksMerged(chunks, stackMergedSet(byID)) || !allTasksMerged(byID) {
			// The pass halted before completion: wait for the outstanding
			// chunk's delivery + merge (or a terminal failure), then drive
			// again. With merge_policy=auto the wait also merges published
			// PRs itself.
			if err := waitForChunkMerges(ctx, prepared, ledger, checker, stackID, chunks, policy, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if !hasMore {
			return waitIntegrationRunSettled(ctx, prepared, ledger, checker, stackID, policy, allowPublish, stdout, stderr)
		}
		// Every currently-known chunk merged, but an earlier decompose call
		// declared more scope than it planned (§12.1). Request the next wave
		// before considering the stack complete.
		wave++
		nextChunks, nextHasMore, nextRemaining, err := admitDecomposeContinuationRun(prepared, stackID, wave, remainingScope, planInputs, stdout, stderr)
		if err != nil {
			return fmt.Errorf("stack drive: %w", err)
		}
		if maxTotal := prepared.compiled.Stacking.MaxTotalChunks; maxTotal > 0 && len(chunks)+len(nextChunks) > maxTotal {
			return fmt.Errorf("stack drive: stack %s would admit %d total chunks, exceeding max_total_chunks=%d (already have %d, wave %d adds %d)",
				stackID, len(chunks)+len(nextChunks), maxTotal, len(chunks), wave, len(nextChunks))
		}
		if err := seedStackLedger(ledger, stackID, nextChunks); err != nil {
			return fmt.Errorf("stack drive: wave %d seed: %w", wave, err)
		}
		chunks = append(chunks, nextChunks...)
		hasMore, remainingScope = nextHasMore, nextRemaining
	}
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

// loadAllStackChunks reconstructs the FULL chunk list a stack has planned
// across every already-admitted decompose wave: the plan run's own output
// (wave 0), plus any decompose-continuation runs (§12.1, waves 1..N) found
// in the run ledger. This is what lets a crashed-and-resumed `stack drive`
// see wave-2+ chunks a prior process already admitted - driveStack's
// dependency ordering and admission are derived entirely from the chunks
// slice it is called with, so a resumed process that only reconstructed
// wave 0 would never admit or drive the later waves' chunks again, even
// though they are already seeded in the task ledger. Returns the final
// hasMore/remainingScope from the LATEST wave found, so the caller knows
// whether to request yet another continuation.
func loadAllStackChunks(repo workflowledger.Repository, stackID string) (chunks []ChunkPlan, hasMore bool, remainingScope string, err error) {
	planOutput, err := loadStackPlanOutput(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	mode, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(planOutput)
	if err != nil {
		return nil, false, "", err
	}
	if mode != "multi" {
		return waveChunks, false, "", nil
	}
	chunks = append(chunks, waveChunks...)
	hasMore, remainingScope = waveHasMore, waveRemaining
	lastWave, err := latestDecomposeContinueWave(repo, stackID)
	if err != nil {
		return nil, false, "", err
	}
	for wave := 1; wave <= lastWave; wave++ {
		run, found, err := stackDecomposeContinueRunRef(repo, stackID, wave)
		if err != nil {
			return nil, false, "", err
		}
		if !found {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d has an invocation key but no run", stackID, wave)
		}
		raw, err := loadStackPlanOutput(repo, run.RunID)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		_, waveChunks, waveHasMore, waveRemaining, err := parseStackPlanOutput(raw)
		if err != nil {
			return nil, false, "", fmt.Errorf("stack %s: decompose continuation wave %d: %w", stackID, wave, err)
		}
		chunks = append(chunks, waveChunks...)
		hasMore, remainingScope = waveHasMore, waveRemaining
	}
	return chunks, hasMore, remainingScope, nil
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
// run: the plan run's declared inputs replayed (D3) plus the engine's
// reserved stack inputs, which win on any name collision. The integration run
// uses the same shape with an empty stack_part and a nil plan entry. When the
// chunk's decompose plan entry is given, it rides along as chunk_plan JSON:
// without it the implement agent sees only the FULL task text and a bare
// chunk ID, and (live finding, smoke-stack-3chunk-v3) implements the whole
// task instead of its slice.
func chunkRunInputs(planInputs map[string]string, chunkID, prBase, stackPart string, plan *ChunkPlan) (map[string]any, map[string]string) {
	inputs := make(map[string]any, len(planInputs)+4)
	snapshot := make(map[string]string, len(planInputs)+4)
	for k, v := range planInputs {
		inputs[k] = v
		snapshot[k] = v
	}
	inputs["stack_mode"] = "chunk"
	inputs["chunk"] = chunkID
	inputs["pr_base"] = prBase
	snapshot["stack_mode"] = "chunk"
	snapshot["chunk"] = chunkID
	snapshot["pr_base"] = prBase
	if stackPart != "" {
		inputs["stack_part"] = stackPart
		snapshot["stack_part"] = stackPart
	}
	if plan != nil {
		if raw, err := json.Marshal(plan); err == nil {
			inputs["chunk_plan"] = string(raw)
			snapshot["chunk_plan"] = string(raw)
		}
	}
	return inputs, snapshot
}

// stackPlanInputs reads the plan run's admitted snapshot and returns the
// workflow-declared inputs the chunks were decomposed from, so chunk runs can
// replay them (D3: chunk runs replay the plan run's inputs).
func stackPlanInputs(repo workflowledger.Repository, stackID string) (map[string]string, error) {
	run, found, err := stackRunRef(repo, stackID, "")
	if err != nil {
		return nil, fmt.Errorf("plan run lookup: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("stack %s has no plan run", stackID)
	}
	raw, err := repo.GetRunSnapshot(context.Background(), run.RunID)
	if err != nil {
		return nil, fmt.Errorf("plan run snapshot: %w", err)
	}
	snap, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, fmt.Errorf("plan run snapshot decode: %w", err)
	}
	return snap.Inputs, nil
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
				merged, err = checker.Merged(context.Background(), head, stackRunPushed(repo, run))
				if err != nil {
					return nil, err
				}
			}
		}
		t.Attempts = stackAttemptCount(ledger, stackID, t.ID)
		act := reconcileTask(t, info, merged, stackRunPushed(repo, run), maxAttempts)
		actions = append(actions, act)
		if err := applyReconcileAction(ledger, stackID, act); err != nil {
			return nil, err
		}
	}
	return actions, nil
}

// stackRunPushed reports durable pushed evidence for a chunk run: any of its
// delivery records reached pushed/succeeded with a commit SHA. A record in
// that state is only written after the branch was actually pushed to origin
// (the deliverer writes pushed after the push, succeeded after the PR is
// created). Without this evidence a missing remote ref means "never pushed",
// not "merged" - a delivery_pending run's PR may never have been created.
func stackRunPushed(repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	records, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err != nil {
		return false
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return true
		}
	}
	return false
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
// for the canonical "k/N" stack_part. An id absent from order is an error,
// not position 0: silently treating an unknown chunk as "first" would mislabel
// its stack_part and, once cross-wave chunk ids exist, mask a real bug (an id
// order was built without).
func chunkPartIndex(chunkID string, order []string) (int, error) {
	for i, id := range order {
		if id == chunkID {
			return i, nil
		}
	}
	return 0, fmt.Errorf("chunk %q not found in dependency order", chunkID)
}

// isResumableRunStatus mirrors the ledger's resumable set for the driver.
func isResumableRunStatus(status workflowledger.RunStatus) bool {
	switch status {
	case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
		return true
	}
	return false
}
