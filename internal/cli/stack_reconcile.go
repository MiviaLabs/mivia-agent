package cli

// Stack driver core: chunk-plan parsing, stable admission keys, topological
// admission order, and idempotent task reconciliation (plan v2.1 §5a, D8).
// Every decision here is derived from durable state only - the task ledger,
// the run ledger, and git merge state - never from driver memory.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// Stack task statuses (stacking vocabulary; D8: statuses are opaque strings
// owned by the consumer; the engine only makes transitions durable).
const (
	stackStatusPlanned     = "planned"
	stackStatusQueued      = "queued"
	stackStatusBlocked     = "blocked"
	stackStatusRunning     = "running"
	stackStatusImplemented = "implemented"
	stackStatusReviewed    = "reviewed"
	stackStatusPublished   = "published"
	stackStatusMerged      = "merged"
	stackStatusReopened    = "reopened"
	stackStatusFailed      = "failed"
	stackStatusSkipped     = "skipped"
)

// stackAdmissiblePreStatuses are the statuses nextAdmissionWave selects a
// chunk from. driveChunk's admission guard (TransitionTaskCAS) uses the same
// list as its compare-and-swap precondition, so the two never drift apart:
// a status nextAdmissionWave would offer for admission is always one
// driveChunk's CAS accepts, and vice versa.
var stackAdmissiblePreStatuses = []string{stackStatusPlanned, stackStatusQueued, stackStatusBlocked, stackStatusReopened}

// stackStatusIsAdmissiblePre reports whether status is one nextAdmissionWave
// and driveChunk's admission CAS both treat as "not yet admitted."
func stackStatusIsAdmissiblePre(status string) bool {
	for _, s := range stackAdmissiblePreStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// stackPlanSchema is the schema name recorded on the stack's plan artifact.
const stackPlanSchema = "chunk-plan-v1"

// stackDecomposeStepID is the engine-synthesized decompose step (compiler
// s2 contract). The driver uses it to read the plan-mode run's chunk plan.
const stackDecomposeStepID = "decompose"

// stackIntegrationChunkID is the fixed chunk id of the final full-suite run.
const stackIntegrationChunkID = "integration"

// stackMaxChunkAttempts bounds reopen retries of a failed chunk run (plan F6
// engine default). Past the bound a chunk is marked failed and the stack
// halts.
const stackMaxChunkAttempts = 3

// stackScope binds every stack task to the plan run that produced the chunk
// plan, so queries never cross stacks (D8 scope binding).
func stackScope(stackID string) tasks.Scope {
	return tasks.Scope{Type: tasks.ScopeRun, ID: stackID}
}

// ChunkPlan is one entry of a decompose chunk-plan output.
type ChunkPlan struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Files        []string `json:"files"`
	EstDiffLines int      `json:"est_diff_lines"`
	Tests        bool     `json:"tests"`
	DependsOn    []string `json:"depends_on"`
}

// stackPlanDocument is the decompose output envelope carrying the chunk list.
type stackPlanDocument struct {
	StackMode string      `json:"stack_mode"`
	ChunkPlan stackChunks `json:"chunk_plan"`
}

type stackChunks struct {
	Chunks         []ChunkPlan `json:"chunks"`
	HasMore        bool        `json:"has_more"`
	RemainingScope string      `json:"remaining_scope"`
}

// parseStackPlanOutput decodes a decompose step output into the stack mode,
// its chunk list, and whether decompose declared more scope than this wave
// planned (§12.1 incremental decompose). stack_mode=single and no_bug are
// valid and mean there is nothing to stack; malformed output is an error
// (fail closed). hasMore/remainingScope are always zero-valued for single/
// no_bug modes, matching decompose.md's contract that incremental planning
// only applies to multi mode.
func parseStackPlanOutput(raw []byte) (mode string, chunks []ChunkPlan, hasMore bool, remainingScope string, err error) {
	var doc stackPlanDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, false, "", fmt.Errorf("stack plan output is not valid JSON: %w", err)
	}
	switch doc.StackMode {
	case "single", "no_bug", "multi":
	default:
		return "", nil, false, "", fmt.Errorf("stack plan stack_mode %q is invalid; want single, multi or no_bug", doc.StackMode)
	}
	if doc.StackMode != "multi" {
		return doc.StackMode, doc.ChunkPlan.Chunks, false, "", nil
	}
	if doc.ChunkPlan.HasMore && strings.TrimSpace(doc.ChunkPlan.RemainingScope) == "" {
		return "", nil, false, "", fmt.Errorf("stack plan declares has_more=true with an empty remaining_scope")
	}
	return doc.StackMode, doc.ChunkPlan.Chunks, doc.ChunkPlan.HasMore, doc.ChunkPlan.RemainingScope, nil
}

// chunkIDRE constrains chunk ids so an admission key stays unambiguous: a
// colon inside a chunk id would make "<stack>:<chunk>" unparseable.
var chunkIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// stackAdmissionKey derives the stable run invocation key for a chunk run
// (plan F15): re-admission after a restart resolves to the SAME run, never a
// duplicate. The key is <stack-id>:<chunk-id> (plan D3, §5a step 3).
func stackAdmissionKey(stackID, chunkID string) (string, error) {
	if strings.TrimSpace(stackID) == "" {
		return "", fmt.Errorf("stack id must not be empty")
	}
	if !chunkIDRE.MatchString(chunkID) {
		return "", fmt.Errorf("chunk id %q must match %s for a stable admission key", chunkID, chunkIDRE)
	}
	return stackID + ":" + chunkID, nil
}

// stackTopologicalOrder returns chunk ids in admission order (dependencies
// first) using Kahn's algorithm. Unknown or cyclic dependencies are errors:
// a stack must never admit a chunk before the chunks it depends on.
func stackTopologicalOrder(chunks []ChunkPlan) ([]string, error) {
	byID := make(map[string]ChunkPlan, len(chunks))
	for _, c := range chunks {
		byID[c.ID] = c
	}
	indegree := make(map[string]int, len(chunks))
	dependents := make(map[string][]string, len(chunks))
	for _, c := range chunks {
		for _, dep := range c.DependsOn {
			if _, ok := byID[dep]; !ok {
				return nil, fmt.Errorf("chunk %q depends on unknown chunk %q", c.ID, dep)
			}
			indegree[c.ID]++
			dependents[dep] = append(dependents[dep], c.ID)
		}
	}
	ready := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if indegree[c.ID] == 0 {
			ready = append(ready, c.ID)
		}
	}
	sort.Strings(ready)
	var order []string
	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]
		order = append(order, next)
		for _, d := range dependents[next] {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(chunks) {
		return nil, fmt.Errorf("chunk depends_on graph contains a cycle")
	}
	return order, nil
}

// RunInfo is the driver's read of one chunk run's ledger state.
type RunInfo struct {
	Present bool
	Status  string
}

// ReconcileAction is one idempotent recovery decision for a chunk task.
type ReconcileAction struct {
	TaskID    string
	Action    string
	NewStatus string // durable status to transition to ("" = none)
	Attempts  int    // attempt count to record on reopen
	Note      string
}

// Reconcile actions (§5a step 2).
const (
	stackActionLeave      = "leave"
	stackActionDeliver    = "deliver"
	stackActionMarkMerged = "mark_merged"
	stackActionReopen     = "reopen"
	stackActionMarkFailed = "mark_failed"
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
// statuses (merged, failed, skipped) always leave. Every decision is derived
// from durable state only.
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
func reconcileTask(t tasks.Task, run RunInfo, merged bool, runPushed bool, maxAttempts int) ReconcileAction {
	leave := func(note string) ReconcileAction {
		return ReconcileAction{TaskID: t.ID, Action: stackActionLeave, Note: note}
	}
	switch t.Status {
	case stackStatusMerged, stackStatusFailed, stackStatusSkipped:
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
		return leave("run is active")
	case runStatusDeliveryPending, runStatusSucceeded:
		if isInFlightStackStatus(t.Status) {
			return ReconcileAction{TaskID: t.ID, Action: stackActionDeliver, Note: "run reached delivery; publish grant required"}
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
func reopenOrFail(t tasks.Task, maxAttempts int) ReconcileAction {
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
func reconcileReopenOrFail(ledger *tasks.Store, stackID, chunkID string) (ReconcileAction, error) {
	var built ReconcileAction
	applied, _, attempts, err := ledger.TransitionTaskCASDecide(stackID, chunkID, []string{stackStatusRunning}, stackStatusReopened,
		func(attempts int) (string, bool) {
			built = reopenOrFail(tasks.Task{ID: chunkID, Attempts: attempts}, stackMaxChunkAttempts)
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
func stackTaskReady(t tasks.Task, merged map[string]bool) bool {
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
func nextAdmissionWave(tasksByID map[string]tasks.Task, merged map[string]bool, order []string) []string {
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

// MergeChecker reports whether a chunk's PR is merged, from durable git
// state. The CLI implementation treats the disappearance of the pushed head
// branch on origin as merged (GitHub's default merge behavior deletes the
// branch). Tests inject a fake.
//
// wasPushed is the driver's durable pushed evidence for the run (a delivery
// record that reached pushed/succeeded with a commit SHA). Ref absence only
// means merged when the branch was ever pushed: a never-pushed branch has no
// remote ref and no PR, and must not read as merged.
type MergeChecker interface {
	Merged(ctx context.Context, headBranch string, wasPushed bool) (bool, error)
}

// gitMergeChecker is the MergeChecker over the repository's origin remote.
// It never contacts the network: it inspects the local remote-tracking refs,
// so a missing fetch degrades to "not merged" (the driver leaves the chunk
// alone) instead of failing the stack.
type gitMergeChecker struct {
	git delivery.GitRunner
	gc  delivery.GitContext
}

// Merged reports whether the head branch is gone from origin (GitHub's
// default "delete branch on merge" makes ref absence a merge proxy), with two
// corrections over the old tracking-ref-only check:
//
//   - Never pushed: a branch that was never pushed has no tracking ref AND no
//     remote ref, so ref absence alone read as merged and a delivery_pending
//     run completed its stack with the PR never created. Ref absence only
//     means merged when wasPushed carries durable pushed evidence (a delivery
//     record that reached pushed/succeeded with a commit SHA).
//   - Stale tracking ref: nothing prunes refs/remotes/origin/wf/*, so after a
//     real remote merge the local tracking ref persists and rev-parse alone
//     reads "not merged" forever. The remote is authoritative: a branch that
//     exists only as a stale local tracking ref (gone from origin per
//     ls-remote) is merged.
func (g gitMergeChecker) Merged(ctx context.Context, headBranch string, wasPushed bool) (bool, error) {
	if strings.TrimSpace(headBranch) == "" {
		return false, nil
	}
	trackingRef := "refs/remotes/origin/" + headBranch
	local, _ := g.git.Run(ctx, g.gc, "rev-parse", "--verify", "-q", trackingRef)
	remote, remoteErr := g.git.Run(ctx, g.gc, "ls-remote", "--heads", "origin", headBranch)
	if remoteErr != nil {
		// The remote cannot confirm the branch state (network outage, missing
		// origin). Degrade to "not merged": the stack keeps waiting instead of
		// completing on a guess, matching the checker's original network-free
		// posture. A stale local tracking ref alone must not complete a stack
		// while the remote is unreachable.
		return false, nil
	}
	hasLocal := strings.TrimSpace(local) != ""
	hasRemote := strings.TrimSpace(remote) != ""
	switch {
	case hasLocal && hasRemote:
		// Branch still exists both places: the PR (or at least the push) is
		// open. ls-remote defeats the false-positive local tracking ref a
		// merge UI can leave behind.
		return false, nil
	case hasLocal && !hasRemote:
		// Stale local tracking ref: the remote merge deleted the branch.
		// The remote is authoritative - merged.
		return true, nil
	case !hasLocal && !hasRemote && wasPushed:
		// Branch was pushed (durable evidence) and no longer exists on
		// origin: merged.
		return true, nil
	default:
		// Never pushed: ref absence is not a merge. The stack keeps waiting
		// for its publish grant.
		return false, nil
	}
}

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
	return workflowBranchPrefix + run.WorktreeName
}

// stackAttemptCount counts a task's reopen transitions in the durable
// journal (D8): the ledger has no task-field update API, so the transition
// journal IS the attempt counter, rebuilt from the event log on restart.
func stackAttemptCount(ledger *tasks.Store, stackID, taskID string) int {
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
// leave and deliver carry no transition (deliver is a driver-side note).
func applyReconcileAction(ledger *tasks.Store, stackID string, act ReconcileAction) error {
	switch act.Action {
	case stackActionMarkMerged, stackActionReopen, stackActionMarkFailed:
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
