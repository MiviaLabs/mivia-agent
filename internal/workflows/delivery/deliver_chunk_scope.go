package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// InputChunkPlan is the reserved admission input carrying the chunk's own
// decompose plan entry (JSON: id, title, files, ...). Only chunk-mode runs
// of a stacking workflow have it.
const InputChunkPlan = "chunk_plan"

// guardChunkScope refuses a stacked chunk whose staged diff touches files
// outside the chunk's declared plan slice. Live finding
// (smoke-stack-3chunk-v3): every chunk of a stack implemented the whole
// task; the duplicate implementations used different filenames, so git
// merged sibling PRs cleanly and master ended up with two definitions of
// the same functions in one package. The declared file list is host ground
// truth from decompose; the agent's own writes are measured, never trusted.
// Runs without a chunk_plan input (plan/single/integration) have no slice
// to enforce.
func guardChunkScope(ctx context.Context, git GitRunner, req Request) error {
	raw := strings.TrimSpace(req.Inputs[InputChunkPlan])
	if req.Policy.StackingHardLines <= 0 || raw == "" {
		return nil
	}
	var plan struct {
		ID    string   `json:"id"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return fmt.Errorf("cannot decode the chunk_plan input: %w", err)
	}
	declared := make(map[string]bool, len(plan.Files))
	for _, f := range plan.Files {
		if f = strings.TrimSpace(f); f != "" {
			declared[f] = true
		}
	}
	if len(declared) == 0 {
		return nil
	}
	// Stage the chunk's working-tree state first so the diff sees it,
	// exactly as checkChunkBaseDrift and MeasureChunkDiffSize do.
	if _, err := git.Run(ctx, req.GitCtx, "-c", "core.fsmonitor=false", "add", "-A"); err != nil {
		return fmt.Errorf("cannot stage the chunk's diff to check its scope: %w", err)
	}
	touchedOut, err := git.Run(ctx, req.GitCtx, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", req.BaseCommit)
	if err != nil {
		return fmt.Errorf("cannot diff chunk's touched files: %w", err)
	}
	var outside []string
	for _, f := range strings.Split(strings.TrimSpace(touchedOut), "\n") {
		if f != "" && !declared[f] {
			outside = append(outside, f)
		}
	}
	if len(outside) == 0 {
		return nil
	}
	sort.Strings(outside)
	// A plain error, NOT a RefusalError: unlike a permanent host refusal
	// (branch checked out elsewhere, origin mismatch), an out-of-scope write
	// is exactly what a repair agent can fix by reverting the file - a
	// RefusalError short-circuits straight to delivery_failed before
	// RepairTarget/ReopenForRepair ever run (workflow_deliver.go
	// settleDeliveryError), which would discard the chunk's completed work
	// on the first overreach instead of giving the repair step one chance.
	return fmt.Errorf(
		"delivery: chunk %s touches %d file(s) outside its declared plan slice (%s); declared files: %s. Revert every out-of-scope change - sibling chunks deliver the rest of the task",
		plan.ID, len(outside), strings.Join(outside, ", "), strings.Join(plan.Files, ", "))
}
