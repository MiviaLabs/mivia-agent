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
	// The driver never runs the plan run itself, but every chunk run must
	// replay the plan run's declared inputs (D3). Load the stack's plan run
	// first so its inputs can be threaded through prepare (required-input
	// validation) and into every chunk admission.
	_, repo, closeEarly, err := openStackLedger(workspaceRoot, configPath)
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
	}
	stackID, err := resolveStackID(repo, name, stackFlag)
	if err != nil {
		closeEarly()
		return err
	}
	planInputs, err := stackPlanInputs(repo, stackID)
	closeEarly()
	if err != nil {
		return fmt.Errorf("stack drive: %w", err)
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
	stackID, err = resolveStackID(prepared.repo, name, stackFlag)
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
	return driveStack(context.Background(), prepared, ledger, stackID, chunks, planInputs, false, stdout, stderr)
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
func driveStackToCompletion(ctx context.Context, prepared *preparedWorkflowRun, ledger *tasks.Store, stackID string, chunks []ChunkPlan, planInputs map[string]string, allowPublish bool, stdout, stderr io.Writer) error {
	checker := gitMergeChecker{
		git: workflowDeliverGit,
		gc:  delivery.GitContext{Dir: prepared.root, GitDir: filepath.Join(prepared.root, ".git")},
	}
	policy := prepared.compiled.Stacking.MergePolicy
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
		if allChunksMerged(chunks, stackMergedSet(byID)) {
			return waitIntegrationRunSettled(ctx, prepared, ledger, checker, stackID, policy, allowPublish, stdout, stderr)
		}
		// The pass halted before completion: wait for the outstanding chunk's
		// delivery + merge (or a terminal failure), then drive again. With
		// merge_policy=auto the wait also merges published PRs itself.
		if err := waitForChunkMerges(ctx, prepared.repo, ledger, checker, stackID, chunks, policy, stdout, stderr); err != nil {
			return err
		}
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
// uses the same shape with an empty stack_part.
func chunkRunInputs(planInputs map[string]string, chunkID, prBase, stackPart string) (map[string]any, map[string]string) {
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
