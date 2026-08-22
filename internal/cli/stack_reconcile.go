package cli

// Stack driver core: chunk-plan parsing, stable admission keys, topological
// admission order, and idempotent task reconciliation (plan v2.1 §5a, D8).
// Every decision here is derived from durable state only - the task ledger,
// the run ledger, and git merge state - never from driver memory.

import (
	"context"
	"fmt"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Stack task statuses, chunk-plan parsing, stable admission keys, and the
// topological admission order live in the shared delivery package; these
// cli-local names are aliases and thin wrappers so the driver keeps its
// existing call sites (see internal/workflows/delivery).
const (
	stackStatusPlanned     = delivery.StatusPlanned
	stackStatusQueued      = delivery.StatusQueued
	stackStatusBlocked     = delivery.StatusBlocked
	stackStatusRunning     = delivery.StatusRunning
	stackStatusImplemented = delivery.StatusImplemented
	stackStatusReviewed    = delivery.StatusReviewed
	stackStatusPublished   = delivery.StatusPublished
	stackStatusMerged      = delivery.StatusMerged
	stackStatusReopened    = delivery.StatusReopened
	stackStatusFailed      = delivery.StatusFailed
	stackStatusSkipped     = delivery.StatusSkipped
	stackStatusCanceled    = delivery.StatusCanceled
)

// stackStatusIsTerminal reports whether a chunk task status is terminal: no
// drive pass may re-admit it, re-open it, or re-mark it (see
// delivery.TerminalStatuses).
func stackStatusIsTerminal(status string) bool {
	return delivery.StatusIsTerminal(status)
}

// stackAdmissiblePreStatuses are the statuses nextAdmissionWave selects a
// chunk from. driveChunk's admission guard (TransitionTaskCAS) uses the same
// list as its compare-and-swap precondition, so the two never drift apart:
// a status nextAdmissionWave would offer for admission is always one
// driveChunk's CAS accepts, and vice versa.
var stackAdmissiblePreStatuses = delivery.AdmissiblePreStatuses

// stackStatusIsAdmissiblePre reports whether status is one nextAdmissionWave
// and driveChunk's admission CAS both treat as "not yet admitted."
func stackStatusIsAdmissiblePre(status string) bool {
	return delivery.StatusIsAdmissiblePre(status)
}

const (
	stackPlanSchema         = delivery.PlanSchema
	stackDecomposeStepID    = delivery.DecomposeStepID
	stackIntegrationChunkID = delivery.IntegrationChunkID
	stackMaxChunkAttempts   = delivery.MaxChunkAttempts
)

// stackScope binds every stack task to the plan run that produced the chunk
// plan, so queries never cross stacks (D8 scope binding).
func stackScope(stackID string) workflowledger.Scope {
	return delivery.Scope(stackID)
}

// ChunkPlan is one entry of a decompose chunk-plan output (shared type).
type ChunkPlan = delivery.ChunkPlan

// parseStackPlanOutput decodes a decompose step output (see
// delivery.ParseStackPlanOutput).
func parseStackPlanOutput(raw []byte) (mode string, chunks []ChunkPlan, hasMore bool, remainingScope string, err error) {
	return delivery.ParseStackPlanOutput(raw)
}

// stackAdmissionKey derives the stable run invocation key for a chunk run
// (plan F15): <stack-id>:<chunk-id> (see delivery.AdmissionKey).
func stackAdmissionKey(stackID, chunkID string) (string, error) {
	return delivery.AdmissionKey(stackID, chunkID)
}

// stackTopologicalOrder returns chunk ids in admission order, dependencies
// first (see delivery.TopologicalOrder).
func stackTopologicalOrder(chunks []ChunkPlan) ([]string, error) {
	return delivery.TopologicalOrder(chunks)
}

// RunInfo is the driver's read of one chunk run's ledger state.
type RunInfo struct {
	Present bool
	Status  string
	// ClaimStale reports whether an active-status run's execution claim is
	// absent or older than its lease (F7 liveness probe: GetRunClaim /
	// DefaultClaimLease). The caller derives it; reconcileTask stays a pure
	// function over already-read state.
	ClaimStale bool
	// NoDiff reports CONFIRMED no_diff delivery evidence for a succeeded run
	// (chunkRunNoDiff): an actual no_diff delivery record, never inferred
	// from the mere absence of pushed evidence. The caller derives it;
	// reconcileTask stays a pure function over already-read state. A
	// read failure on the delivery records must resolve to false here (fail
	// closed), same rule as ClaimStale.
	NoDiff bool
}

// ReconcileAction is one idempotent recovery decision for a chunk task.
type ReconcileAction struct {
	TaskID        string
	Action        string
	NewStatus     string // durable status to transition to ("" = none)
	CurrentStatus string // task status before this action (set by reconcileStack)
	Attempts      int    // attempt count to record on reopen
	Note          string
}

// Reconcile actions (§5a step 2).
const (
	stackActionLeave         = "leave"
	stackActionDeliver       = "deliver"
	stackActionMarkMerged    = "mark_merged"
	stackActionMarkPublished = "mark_published"
	stackActionReopen        = "reopen"
	stackActionMarkFailed    = "mark_failed"
)

// Run statuses mirrored from the workflow ledger for the pure reconciler.
const (
	runStatusPending         = "pending"
	runStatusRunning         = "running"
	runStatusWaitingApproval = "waiting_approval"
	runStatusDeliveryPending = "delivery_pending"
	runStatusSucceeded       = "succeeded"
	runStatusFailed          = "failed"
	runStatusCanceled        = "canceled"
	runStatusTimedOut        = "timed_out"
	runStatusDeliveryFailed  = "delivery_failed"
)

// reconcileTask derives the idempotent recovery action for one non-terminal
// chunk task from its run and git merge state (plan §5a). Terminal task
// statuses (delivery.TerminalStatuses: merged, failed, skipped, canceled)
// always leave, BEFORE any other rule - a canceled dependent keeps a failed
// run row, so a later short-circuit would let the reopen arm re-admit it,
// and its branch can even report merged. Every decision is derived from
// durable state only.
//
// Rules: running -> leave; succeeded+delivery_pending -> deliver (publish
// grant); merged (git) with durable pushed evidence -> mark merged, unblock
// dependents; failed/timed_out/canceled -> reopen with bounded retries (task
// Attempts) or mark failed and halt.
//
// merged alone never marks: marking requires runPushed, durable evidence that
// the branch was ever pushed (a delivery record that reached pushed/succeeded
// with a commit SHA). A delivery_pending run whose branch was never pushed has
// no remote ref and no PR; ref absence must not complete its stack.
func reconcileTask(t workflowledger.Task, run RunInfo, merged bool, runPushed bool, maxAttempts int) ReconcileAction {
	leave := func(note string) ReconcileAction {
		return ReconcileAction{TaskID: t.ID, Action: stackActionLeave, Note: note}
	}
	if stackStatusIsTerminal(t.Status) {
		return leave("")
	}
	if merged && runPushed {
		return ReconcileAction{TaskID: t.ID, Action: stackActionMarkMerged, NewStatus: stackStatusMerged, Note: "git reports the PR branch merged"}
	}
	if !run.Present {
		// No run row: a planned/queued task waits for its wave; a task that
		// claims to be in flight with no run was never admitted - reopen it.
		switch t.Status {
		case stackStatusRunning, stackStatusImplemented, stackStatusReviewed, stackStatusPublished:
			return reopenOrFail(t, maxAttempts)
		}
		return leave("no run; waiting for admission")
	}
	switch run.Status {
	case runStatusPending, runStatusRunning, runStatusWaitingApproval:
		if run.ClaimStale {
			// The admitting process died mid-run (F7): nothing durable will ever
			// settle this without a resume. This note tells an operator reading
			// `stack status`/`stack reconcile` output the correct manual path,
			// instead of the misleading "run is active". Note: `mivia stack
			// drive` does NOT auto-resume this case because nextAdmissionWave
			// admits only planned/queued/blocked/reopened tasks; a task
			// already at running is never re-admitted into the drive wave.
			return leave("run's claim is stale; run `mivia workflow resume <run-id>` to resume it")
		}
		return leave("run is active")
	case runStatusDeliveryPending, runStatusSucceeded:
		// A CONFIRMED no_diff run (an actual no_diff delivery record, not
		// merely the absence of pushed evidence - see RunInfo.NoDiff) has no
		// PR to merge. It is complete; mark it merged so dependents can
		// proceed. Ambiguous evidence (NoDiff=false, e.g. a ListDeliveries
		// read failure) must never take this branch: it would durably drop
		// the chunk's content if a real PR does exist.
		if run.Status == runStatusSucceeded && run.NoDiff && isInFlightStackStatus(t.Status) {
			return ReconcileAction{TaskID: t.ID, Action: stackActionMarkMerged, NewStatus: stackStatusMerged, Note: "no diff to publish; run completed without a PR"}
		}
		// A real publish (pushed evidence, not no_diff) can land OUTSIDE
		// driveChunk: a human `mivia workflow deliver <run> --allow-publish`
		// grant for a reviewed chunk, or a resumed run the recovery sweep
		// delivered. The task must move to published so
		// autoMergePublishedChunks and the grant-pause hints see it -
		// otherwise it stays at its prior in-flight status forever:
		// driveChunk's admission CAS only claims planned/queued/blocked/
		// reopened tasks, so a task stuck at running/reviewed is never
		// re-admitted and never merged.
		if run.Status == runStatusSucceeded && runPushed && t.Status != stackStatusPublished && isInFlightStackStatus(t.Status) {
			return ReconcileAction{TaskID: t.ID, Action: stackActionMarkPublished, NewStatus: stackStatusPublished, Note: "run published outside the drive; PR open"}
		}
		// Still delivery_pending, no positive publish evidence (F9). A
		// published task already has its own PR open and its own
		// grant-only wait; it must not be downgraded to reviewed just
		// because its run row has not caught up (chunkSettleAfterDelivery
		// sets published on ANY non-no-diff outcome, including a repair
		// re-entry that leaves the run at delivery_pending). Every other
		// in-flight status (typically running, orphaned by a mid-delivery
		// crash) moves to reviewed so it registers with
		// stackAwaitsGrantOnly instead of staying wedged at a status its
		// switch has no case for.
		if t.Status == stackStatusPublished {
			return ReconcileAction{TaskID: t.ID, Action: stackActionDeliver, Note: "run reached delivery; publish grant required"}
		}
		if isInFlightStackStatus(t.Status) {
			return ReconcileAction{TaskID: t.ID, Action: stackActionDeliver, NewStatus: stackStatusReviewed, Note: "run reached delivery; publish grant required"}
		}
		return leave("run done; no publish state")
	case runStatusFailed, runStatusCanceled, runStatusTimedOut, runStatusDeliveryFailed:
		return reopenOrFail(t, maxAttempts)
	default:
		return leave("run status " + run.Status)
	}
}

// isInFlightStackStatus reports whether a task status means the chunk's run
// was admitted and did not yet merge.
func isInFlightStackStatus(status string) bool {
	switch status {
	case stackStatusRunning, stackStatusImplemented, stackStatusReviewed, stackStatusPublished, stackStatusReopened:
		return true
	}
	return false
}

// reopenOrFail reopens a task whose run ended without merging, bounded by the
// task's attempt count; past the bound the stack halts on a failed task.
func reopenOrFail(t workflowledger.Task, maxAttempts int) ReconcileAction {
	if maxAttempts <= 0 {
		maxAttempts = stackMaxChunkAttempts
	}
	if t.Attempts+1 > maxAttempts {
		return ReconcileAction{TaskID: t.ID, Action: stackActionMarkFailed, NewStatus: stackStatusFailed, Note: fmt.Sprintf("run failed after %d attempts; stack halts", t.Attempts)}
	}
	return ReconcileAction{TaskID: t.ID, Action: stackActionReopen, NewStatus: stackStatusReopened, Attempts: t.Attempts + 1, Note: fmt.Sprintf("reopen attempt %d/%d", t.Attempts+1, maxAttempts)}
}

// reconcileReopenOrFail applies reopenOrFail's decision atomically: the
// task's attempt count is read and its transition applied inside one
// TransitionTaskCASDecide call, so two concurrent failure handlers for the
// SAME chunk (reachable once wave execution runs concurrently) cannot both
// read the same attempt count and both reopen past maxAttempts. The task
// must be in stackStatusRunning (the only status a chunk's failure path is
// reached from); a task no longer running has already been handled by
// another caller, and this returns a leave action, not an error.
func reconcileReopenOrFail(ledger *workflowledger.Store, stackID, chunkID string) (ReconcileAction, error) {
	var built ReconcileAction
	applied, _, attempts, err := ledger.TransitionTaskCASDecide(stackID, chunkID, []string{stackStatusRunning}, stackStatusReopened,
		func(attempts int) (string, bool) {
			built = reopenOrFail(workflowledger.Task{ID: chunkID, Attempts: attempts}, stackMaxChunkAttempts)
			return built.NewStatus, true
		})
	if err != nil {
		return ReconcileAction{}, err
	}
	if !applied {
		return ReconcileAction{TaskID: chunkID, Action: stackActionLeave, Note: fmt.Sprintf("already handled (attempts=%d)", attempts)}, nil
	}
	return built, nil
}

// stackTaskReady reports whether a task's dependencies are all merged.
func stackTaskReady(t workflowledger.Task, merged map[string]bool) bool {
	for _, dep := range t.Deps {
		if !merged[dep] {
			return false
		}
	}
	return true
}

// nextAdmissionWave returns the chunk ids to admit now: planned/queued/
// blocked/reopened tasks whose dependencies are all merged, in dependency
// order (§5a step 4: schedule the next wave).
func nextAdmissionWave(tasksByID map[string]workflowledger.Task, merged map[string]bool, order []string) []string {
	var wave []string
	for _, id := range order {
		t, ok := tasksByID[id]
		if !ok {
			continue
		}
		if stackStatusIsAdmissiblePre(t.Status) && stackTaskReady(t, merged) {
			wave = append(wave, id)
		}
	}
	return wave
}

// MergeChecker and gitMergeChecker (the merge oracle) live in
// stack_merge_checker.go.

// stackRunRef finds the run whose stable invocation key is
// <stack-id>:<chunk-id> in the run ledger (F15). There is at most one live
// run per key; the latest is returned.
func stackRunRef(repo workflowledger.Repository, stackID, chunkID string) (workflowledger.RunSnapshot, bool, error) {
	key, err := stackAdmissionKey(stackID, chunkID)
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return workflowledger.RunSnapshot{}, false, err
	}
	var best workflowledger.RunSnapshot
	found := false
	for _, r := range runs {
		if r.InvocationKey != key {
			continue
		}
		if !found || r.StartedAt.After(best.StartedAt) {
			best = r
			found = true
		}
	}
	return best, found, nil
}

// stackHeadBranch is the origin head branch a chunk run pushed: the run
// worktree branch (F1). Empty when the run has no worktree (not delivered).
func stackHeadBranch(run workflowledger.RunSnapshot) string {
	if run.WorktreeName == "" {
		return ""
	}
	return cliworkflow.WorkflowBranchPrefix + run.WorktreeName
}

// stackAttemptCount counts a task's reopen transitions in the durable
// journal (D8): the ledger has no task-field update API, so the transition
// journal IS the attempt counter, rebuilt from the event log on restart.
func stackAttemptCount(ledger *workflowledger.Store, stackID, taskID string) int {
	trs, err := ledger.ListTransitions(stackID)
	if err != nil {
		return 0
	}
	n := 0
	for _, tr := range trs {
		if tr.TaskID == taskID && tr.ToStatus == stackStatusReopened {
			n++
		}
	}
	return n
}

// applyReconcileAction persists the durable part of one recovery action.
// leave carries no transition; deliver (F9) now does for most in-flight
// statuses, but not an already-published task (see reconcileTask's deliver
// arm) - guard on NewStatus rather than the action, or that case hits
// TransitionTask with an empty status.
func applyReconcileAction(ledger *workflowledger.Store, stackID string, act ReconcileAction) error {
	if act.NewStatus == "" {
		return nil
	}
	if act.CurrentStatus == act.NewStatus {
		return nil
	}
	switch act.Action {
	case stackActionMarkMerged, stackActionMarkPublished, stackActionDeliver, stackActionReopen, stackActionMarkFailed:
		return ledger.TransitionTask(stackID, act.TaskID, act.NewStatus)
	default:
		return nil
	}
}

// stackPRNumber extracts the pull-request number from a delivery URL, or ""
// when the URL does not carry one. Status output degrades gracefully.
func stackPRNumber(url string) string {
	i := strings.LastIndex(url, "/pull/")
	if i < 0 {
		return ""
	}
	num := url[i+len("/pull/"):]
	if num == "" || !strings.ContainsAny(num, "0123456789") {
		return ""
	}
	trimmed := strings.TrimRight(num, "/")
	if trimmed == "" {
		return ""
	}
	return trimmed
}
