// Package stacking provides the durable state layer of the workflow stack
// driver: chunk-plan parsing, stable chunk admission keys, topological chunk
// ordering, task-ledger seeding, and the admission-input builders for chunk
// and integration runs.
//
// It is the shared implementation behind two drive surfaces:
//   - internal/cli's `mivia workflow run`/`mivia stack drive` driver (the
//     operator path), which keeps its own drive loop and merge machinery;
//   - internal/workflows/localengine's in-process engine (the agent-tools
//     path), which drives a parked multi-chunk plan run automatically after
//     its controller settles (drive-before-delivery).
//
// Every decision in this package is derived from durable state only - the
// task ledger, the run ledger, and git merge state - never from driver
// memory, so either surface can resume the other's stack (D8, plan v2.1 §5a).
package stacking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// Stack task statuses (stacking vocabulary; D8: statuses are opaque strings
// owned by the consumer; the engine only makes transitions durable).
const (
	StatusPlanned     = "planned"
	StatusQueued      = "queued"
	StatusBlocked     = "blocked"
	StatusRunning     = "running"
	StatusImplemented = "implemented"
	StatusReviewed    = "reviewed"
	StatusPublished   = "published"
	StatusMerged      = "merged"
	StatusReopened    = "reopened"
	StatusFailed      = "failed"
	StatusSkipped     = "skipped"
	// StatusCanceled marks a chunk that a drive gave up on because a chunk
	// it depends on failed terminally. It is TERMINAL: no drive pass may
	// re-admit it, re-open it, or mark it merged. A canceled chunk was
	// never implemented, so its content is absent from the stack.
	StatusCanceled = "canceled"
)

// TerminalStatuses are the statuses no drive pass may move a chunk out of.
// Every enumeration of "leave this task alone" must derive from this list,
// or a terminal task is resurrected by the next pass: a canceled dependent
// whose run row is failed fell to the reopen path and was re-admitted,
// because the reconciler's short-circuit named only merged/failed/skipped
// (audit finding R3, 2026-08-17).
var TerminalStatuses = []string{StatusMerged, StatusFailed, StatusSkipped, StatusCanceled}

// StatusIsTerminal reports whether status is one a drive pass must leave
// alone.
func StatusIsTerminal(status string) bool {
	for _, s := range TerminalStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// AdmissiblePreStatuses are the statuses a drive pass selects a chunk from.
// Drive admission guards (TransitionTaskCAS) use the same list as their
// compare-and-swap precondition, so the two never drift apart: a status an
// admission pass would offer is always one the admission CAS accepts.
var AdmissiblePreStatuses = []string{StatusPlanned, StatusQueued, StatusBlocked, StatusReopened}

// StatusIsAdmissiblePre reports whether status is one a drive pass treats as
// "not yet admitted."
func StatusIsAdmissiblePre(status string) bool {
	for _, s := range AdmissiblePreStatuses {
		if status == s {
			return true
		}
	}
	return false
}

// PlanSchema is the schema name recorded on the stack's plan artifact.
const PlanSchema = "chunk-plan-v1"

// DecomposeStepID is the engine-synthesized decompose step (compiler s2
// contract). The driver uses it to read the plan-mode run's chunk plan.
const DecomposeStepID = "decompose"

// IntegrationChunkID is the fixed chunk id of the final full-suite run.
const IntegrationChunkID = "integration"

// MaxChunkAttempts bounds reopen retries of a failed chunk run (plan F6
// engine default). Past the bound a chunk is marked failed and the stack
// halts.
const MaxChunkAttempts = 3

// Scope binds every stack task to the plan run that produced the chunk plan,
// so queries never cross stacks (D8 scope binding).
func Scope(stackID string) tasks.Scope {
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

// planDocument is the decompose output envelope carrying the chunk list.
type planDocument struct {
	StackMode string    `json:"stack_mode"`
	ChunkPlan chunkList `json:"chunk_plan"`
}

type chunkList struct {
	Chunks         []ChunkPlan `json:"chunks"`
	HasMore        bool        `json:"has_more"`
	RemainingScope string      `json:"remaining_scope"`
}

// ParseStackPlanOutput decodes a decompose step output into the stack mode,
// its chunk list, and whether decompose declared more scope than this wave
// planned (§12.1 incremental decompose). stack_mode=single and no_bug are
// valid and mean there is nothing to stack; malformed output is an error
// (fail closed). hasMore/remainingScope are always zero-valued for single/
// no_bug modes, matching decompose.md's contract that incremental planning
// only applies to multi mode.
func ParseStackPlanOutput(raw []byte) (mode string, chunks []ChunkPlan, hasMore bool, remainingScope string, err error) {
	var doc planDocument
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

// AdmissionKey derives the stable run invocation key for a chunk run (plan
// F15): re-admission after a restart resolves to the SAME run, never a
// duplicate. The key is <stack-id>:<chunk-id> (plan D3, §5a step 3).
func AdmissionKey(stackID, chunkID string) (string, error) {
	if strings.TrimSpace(stackID) == "" {
		return "", fmt.Errorf("stack id must not be empty")
	}
	if !chunkIDRE.MatchString(chunkID) {
		return "", fmt.Errorf("chunk id %q must match %s for a stable admission key", chunkID, chunkIDRE)
	}
	return stackID + ":" + chunkID, nil
}

// TopologicalOrder returns chunk ids in admission order (dependencies first)
// using Kahn's algorithm. Unknown, duplicated, or cyclic dependencies are
// errors: a stack must never admit a chunk before the chunks it depends on,
// and a duplicated id must never emit a duplicated admission order. Without
// the duplicate check, two chunks sharing a zero-indegree id both land in the
// ready queue, the loop emits the id twice, len(order) == len(chunks) still
// holds, and the driver silently gets a wrong order: duplicated wave entries,
// inflated "k/N" stack_part labels (ChunkPartIndex returns the first
// occurrence), and admission of the same chunk twice. The deterministic
// chunk-plan gate rejects duplicate ids per wave, but a continuation wave can
// reuse an id from an earlier wave (loadAllStackChunks concatenates the
// waves), so the shared ordering must fail closed on its own.
func TopologicalOrder(chunks []ChunkPlan) ([]string, error) {
	byID := make(map[string]ChunkPlan, len(chunks))
	for _, c := range chunks {
		if _, dup := byID[c.ID]; dup {
			return nil, fmt.Errorf("chunk id %q appears more than once", c.ID)
		}
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

// ChunkPartIndex returns the 0-based position of a chunk in dependency order,
// for the canonical "k/N" stack_part. An id absent from order is an error,
// not position 0: silently treating an unknown chunk as "first" would mislabel
// its stack_part and, once cross-wave chunk ids exist, mask a real bug (an id
// order was built without).
func ChunkPartIndex(chunkID string, order []string) (int, error) {
	for i, id := range order {
		if id == chunkID {
			return i, nil
		}
	}
	return 0, fmt.Errorf("chunk %q not found in dependency order", chunkID)
}

// LoadStackPlanOutput reads the succeeded decompose step output of a
// plan-mode run from the run ledger (F1/F8: the plan is a run output). When
// the plan run's decompose step ran more than once, the LATEST succeeded
// attempt that produced an output is authoritative.
func LoadStackPlanOutput(ctx context.Context, repo workflowledger.Repository, stackID string) ([]byte, error) {
	attempts, err := repo.ListStepAttempts(ctx, stackID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return nil, fmt.Errorf("plan run %q not found", stackID)
		}
		return nil, err
	}
	// The decompose step can run several times in ONE plan run: the
	// chunk_plan_validate gate rejects the first plan and the decompose_repair
	// loop re-runs it (compiler synthesis, MaxIterations 3), leaving MULTIPLE
	// succeeded decompose attempts. The operative plan is the LAST one - the
	// plan that passed the gate - so select the latest succeeded decompose
	// attempt that produced an output, exactly like DecomposedChunks and the
	// controller's latestOutputAttempt. Returning the first would hand the
	// driver the REJECTED plan: the stack would be seeded, driven, and
	// verified against chunks the deterministic gate refused, while the
	// delivery gate (DecomposedChunks) counts the accepted plan's chunks.
	var latest workflowledger.StepAttempt
	found := false
	for _, a := range attempts {
		if a.StepID == DecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded && a.OutputRef != "" {
			if !found || a.AttemptNo > latest.AttemptNo {
				latest, found = a, true
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("plan run %q has no succeeded decompose output", stackID)
	}
	data, err := repo.LoadContent(ctx, latest.OutputRef)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// DecomposedChunks reports whether runID is the plan run of a multi-chunk
// stack: the LATEST succeeded decompose step attempt that produced an output
// parses as mode=multi with at least one chunk (same selection rule as
// LoadStackPlanOutput and the controller's latestOutputAttempt; a later
// succeeded attempt with no output must not shadow the recorded plan).
// ok=false covers every other case (not a stacking plan run, single/no_bug, a
// malformed decompose output, or a lookup failure) - callers must treat a
// lookup failure as "not applicable", never as a refusal or a false
// "undriven" diagnostic.
func DecomposedChunks(ctx context.Context, repo workflowledger.Repository, runID string) (chunks int, ok bool) {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return 0, false
	}
	// Select the latest succeeded decompose attempt by AttemptNo, exactly like
	// LoadStackPlanOutput and the controller's latestOutputAttempt. Iteration
	// order is not part of the Repository contract (ListStepAttempts orders by
	// event sequence, which is a storage implementation detail), so taking the
	// last match in slice order can hand the delivery gate a DIFFERENT plan
	// than the driver seeds and drives when a repository returns attempts out
	// of AttemptNo order. A later succeeded attempt without an OutputRef still
	// must not shadow the recorded plan (the filter below).
	var decomposeOutputRef string
	latestAttemptNo := 0
	found := false
	for _, a := range attempts {
		if a.StepID == DecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded && a.OutputRef != "" {
			if !found || a.AttemptNo > latestAttemptNo {
				decomposeOutputRef = a.OutputRef
				latestAttemptNo = a.AttemptNo
				found = true
			}
		}
	}
	if decomposeOutputRef == "" {
		return 0, false // not a plan run with a succeeded decompose step
	}
	raw, err := repo.LoadContent(ctx, decomposeOutputRef)
	if err != nil {
		return 0, false
	}
	mode, parsedChunks, _, _, err := ParseStackPlanOutput(raw)
	if err != nil || mode != "multi" || len(parsedChunks) == 0 {
		return 0, false // single/no_bug, or a malformed plan another gate already rejects
	}
	return len(parsedChunks), true
}

// PlanInputs reads the plan run's admitted snapshot and returns the
// workflow-declared inputs the chunks were decomposed from, so chunk runs can
// replay them (D3: chunk runs replay the plan run's inputs). The plan run's
// own RunID IS the stack id; it was never admitted with a "<stack>:<chunk>"
// key, so it is read directly by RunID.
func PlanInputs(ctx context.Context, repo workflowledger.Repository, stackID string) (map[string]string, error) {
	raw, err := repo.GetRunSnapshot(ctx, stackID)
	if err != nil {
		return nil, fmt.Errorf("plan run snapshot: %w", err)
	}
	snap, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, fmt.Errorf("plan run snapshot decode: %w", err)
	}
	return snap.Inputs, nil
}

// PRBase returns the delivery base branch the chunk PRs branch from: the
// workflow's delivery policy base (delivery honors pr_base, S4).
func PRBase(wf *compiler.CompiledWorkflow) (string, error) {
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
