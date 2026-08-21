package stacking

// stacking_state.go: the durable-state builders of the stack driver - task
// ledger seeding, the admission-input builders for chunk and integration
// runs, task-state reads for a drive pass, and the delivery/merge evidence
// readers (no_diff, pushed, head commit). Every function here derives from
// durable state only, so either drive surface (CLI or engine) can resume the
// other's stack.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// SeedStackLedger records the plan artifact and the chunk tasks (D8).
// Re-entry is idempotent: existing tasks are left untouched (their durable
// status wins - a re-drive after a partial completion, a continuation-wave
// seed, or the recovery sweep must never overwrite or re-create them), and
// only missing tasks are created. A lost race against a concurrent seed is
// also a no-op (the other writer won).
func SeedStackLedger(ctx context.Context, ledger *workflowledger.Store, stackID string, chunks []ChunkPlan) error {
	if _, err := ledger.StorePlan(workflowledger.Plan{ID: stackID, Scope: Scope(stackID), Schema: PlanSchema}); err != nil {
		return err
	}
	for _, c := range chunks {
		if existing, err := ledger.GetTask(stackID, c.ID); err == nil {
			_ = existing // durable state wins; never overwrite a seeded/advanced task
			continue
		}
		task := workflowledger.Task{
			ID: c.ID, PlanRef: stackID, Scope: Scope(stackID),
			Status: StatusPlanned, Deps: append([]string(nil), c.DependsOn...),
		}
		if err := ledger.CreateTask(task); err != nil {
			// A concurrent seed may have created the task between our read and
			// the create: that is a win for the other writer, not an error.
			if _, gerr := ledger.GetTask(stackID, c.ID); gerr == nil {
				continue
			}
			return err
		}
	}
	return nil
}

// ChunkRunInputs builds the admission inputs and snapshot for one chunk-mode
// run: the plan run's declared inputs replayed (D3) plus the engine's
// reserved stack inputs, which win on any name collision. The integration run
// uses the same shape with an empty stack_part and a nil plan entry. When the
// chunk's decompose plan entry is given, it rides along as chunk_plan JSON:
// without it the implement agent sees only the FULL task text and a bare
// chunk ID, and (live finding, smoke-stack-3chunk-v3) implements the whole
// task instead of its slice.
func ChunkRunInputs(planInputs map[string]string, chunkID, prBase, stackPart string, plan *ChunkPlan, siblingFiles []string) (map[string]any, map[string]string) {
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
	// The sibling union is the stack's ground truth for the engine's
	// chunk finding-scope filter: a demanded path declared by ANOTHER
	// chunk is out of scope for this one, whatever directory tree it
	// lives in. Absent (integration run, single-chunk stack) leaves the
	// engine's directory heuristic in charge.
	if len(siblingFiles) > 0 {
		if raw, err := json.Marshal(siblingFiles); err == nil {
			inputs["sibling_files"] = string(raw)
			snapshot["sibling_files"] = string(raw)
		}
	}
	return inputs, snapshot
}

// IntegrationRunInputs builds the admission inputs for the final full-suite
// integration run: it replays the plan run's declared inputs and admits as
// stack_mode=single (running the workflow's own plan+implement steps
// inline), never stack_mode=chunk. chunk_plan's chunk/pr_base/stack_part are
// deliberately absent: stack_mode=chunk REQUIRES stack_part present
// (validateStackingReservedInputs), and the integration run has none - a bug
// an adversarial audit found: chunkRunInputs forced stack_mode=chunk here
// with an always-empty stack_part, so every stack's integration run failed
// admission the moment every chunk merged.
func IntegrationRunInputs(planInputs map[string]string, prBase string) (map[string]any, map[string]string) {
	inputs := make(map[string]any, len(planInputs)+2)
	snapshot := make(map[string]string, len(planInputs)+2)
	for k, v := range planInputs {
		inputs[k] = v
		snapshot[k] = v
	}
	// stack_mode=single forbids chunk_plan (validateStackingReservedInputs),
	// and a plan run admits with one (the implicit-plan path never checks
	// it), so the replay must strip it instead of carrying it over.
	delete(inputs, "chunk_plan")
	delete(snapshot, "chunk_plan")
	delete(inputs, "sibling_files")
	delete(snapshot, "sibling_files")
	inputs["stack_mode"] = "single"
	snapshot["stack_mode"] = "single"
	if prBase != "" {
		inputs["pr_base"] = prBase
		snapshot["pr_base"] = prBase
	}
	return inputs, snapshot
}

// TaskMap loads every stack task by id for a drive pass.
func TaskMap(ctx context.Context, ledger *workflowledger.Store, stackID string) (map[string]workflowledger.Task, error) {
	list, err := ledger.ListTasksByScope(Scope(stackID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]workflowledger.Task, len(list))
	for _, t := range list {
		out[t.ID] = t
	}
	return out, nil
}

// MergedSet returns the set of chunk ids whose tasks are merged.
func MergedSet(byID map[string]workflowledger.Task) map[string]bool {
	out := make(map[string]bool, len(byID))
	for id, t := range byID {
		if t.Status == StatusMerged {
			out[id] = true
		}
	}
	return out
}

// AllChunksMerged reports whether every chunk in the plan is merged.
func AllChunksMerged(chunks []ChunkPlan, merged map[string]bool) bool {
	for _, c := range chunks {
		if !merged[c.ID] {
			return false
		}
	}
	return true
}

// TaskReady reports whether a task's dependencies are all merged.
func TaskReady(t workflowledger.Task, merged map[string]bool) bool {
	for _, dep := range t.Deps {
		if !merged[dep] {
			return false
		}
	}
	return true
}

// SiblingFiles returns the union of the declared files of every chunk except
// the named one, sorted for a deterministic input digest. The union covers
// the chunks known at admission; later decompose waves are not visible to
// already-admitted chunk runs, which keep the directory heuristic inside the
// engine.
func SiblingFiles(chunks map[string]*ChunkPlan, chunkID string) []string {
	seen := make(map[string]bool)
	var files []string
	for id, chunk := range chunks {
		if chunk == nil || id == chunkID {
			continue
		}
		for _, f := range chunk.Files {
			f = strings.TrimSpace(f)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// ChunkRunNoDiff reports whether a run settled succeeded with a confirmed
// no_diff delivery outcome: the intended diff was empty, no PR was created,
// and the chunk is therefore complete. This requires POSITIVE evidence - an
// actual "no_diff" delivery record - not merely the absence of pushed
// evidence: a ListDeliveries read failure or a not-yet-recorded delivery
// also produce zero pushed records, and misreading either as "confirmed
// no_diff" durably marks a chunk merged with no PR ever created, silently
// dropping its content (an adversarial audit found this exact regression).
// A record that reached pushed/succeeded with a commit SHA always wins over
// a stale no_diff record from an earlier attempt on the same run.
func ChunkRunNoDiff(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	if run.Status != workflowledger.RunStatusSucceeded {
		return false
	}
	records, err := repo.ListDeliveries(ctx, run.RunID)
	if err != nil {
		return false
	}
	sawNoDiff := false
	for _, rec := range records {
		switch rec.Status {
		case "pushed", "succeeded":
			if rec.CommitSHA != "" {
				return false
			}
		case "no_diff":
			sawNoDiff = true
		}
	}
	return sawNoDiff
}

// RunPushed reports durable pushed evidence for a chunk run: any of its
// delivery records reached pushed/succeeded with a commit SHA. A record in
// that state is only written after the branch was actually pushed to origin
// (the deliverer writes pushed after the push, succeeded after the PR is
// created). Without this evidence a missing remote ref means "never pushed",
// not "merged" - a delivery_pending run's PR may never have been created.
func RunPushed(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot) bool {
	records, err := repo.ListDeliveries(ctx, run.RunID)
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

// RunHeadCommit returns the pushed commit SHA for a chunk run, if any. The
// commit is the durable evidence the merge oracle uses to verify that the
// base branch contains the change.
func RunHeadCommit(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot) string {
	records, err := repo.ListDeliveries(ctx, run.RunID)
	if err != nil {
		return ""
	}
	for _, rec := range records {
		if rec.CommitSHA == "" {
			continue
		}
		switch rec.Status {
		case "pushed", "succeeded":
			return rec.CommitSHA
		}
	}
	return ""
}
