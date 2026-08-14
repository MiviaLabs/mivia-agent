package delivery

// Deferred-files lookup (spec-auto-split-oversized-prs.md §5.2-5.3): reading
// a repair step's declared split decision back out of the run ledger before
// building the delivery Request. Split out of stacking.go since it needs
// ledger.Repository, a dependency the rest of that file's pure git/regex
// logic does not.

import (
	"context"
	"encoding/json"

	ledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// LatestDeferredFilesInput scans runID's step attempts for the most recent
// succeeded output declaring deferred_files (change-summary-v1.json's
// optional field, populated by a diff-size repair step per the repair.md/
// bugfix-repair.md template contract) and returns it JSON re-encoded, ready
// to assign to delivery.Request.Inputs[InputDeferredFiles]. Returns "" when
// no attempt declared one. Generic across workflows: no specific repair step
// id is named here, since only a repair step's template ever sets the field.
// Errors are swallowed (best-effort): a ledger read failure here must never
// block delivery on its own - it only means the split decision is lost for
// this attempt, which is no worse than today's un-split behavior.
func LatestDeferredFilesInput(ctx context.Context, repo ledger.Repository, runID string) string {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return ""
	}
	var latest string
	for _, a := range attempts {
		if a.Status != ledger.AttemptStatusSucceeded || a.OutputRef == "" {
			continue
		}
		data, err := repo.LoadContent(ctx, a.OutputRef)
		if err != nil {
			continue
		}
		var out struct {
			DeferredFiles []string `json:"deferred_files"`
		}
		if err := json.Unmarshal(data, &out); err != nil || len(out.DeferredFiles) == 0 {
			continue
		}
		reencoded, err := json.Marshal(out.DeferredFiles)
		if err != nil {
			continue
		}
		latest = string(reencoded)
	}
	return latest
}

// InputsWithDeferredFiles returns a copy of inputs with InputDeferredFiles
// set from LatestDeferredFilesInput, when the workflow is stacking-active
// (StackingHardLines > 0) and a repair step declared one. Every
// delivery.Request construction site (the CLI and the local engine) calls
// this so the split decision reaches Deliver() without either caller
// duplicating the ledger lookup. Returns inputs UNCHANGED (same map, no
// copy) when stacking is inactive or nothing was found, so a non-stacking
// delivery pays no extra ledger read.
func InputsWithDeferredFiles(ctx context.Context, repo ledger.Repository, runID string, inputs map[string]string, policy Policy) map[string]string {
	if policy.StackingHardLines <= 0 {
		return inputs
	}
	deferred := LatestDeferredFilesInput(ctx, repo, runID)
	if deferred == "" {
		return inputs
	}
	out := make(map[string]string, len(inputs)+1)
	for k, v := range inputs {
		out[k] = v
	}
	out[InputDeferredFiles] = deferred
	return out
}
