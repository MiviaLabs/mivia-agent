package cli

import (
	"context"
	"fmt"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// integrationRunInputs builds the admission inputs for the final full-suite
// integration run: it replays the plan run's declared inputs and admits as
// stack_mode=single (running the workflow's own plan+implement steps
// inline), never stack_mode=chunk. chunk_plan's chunk/pr_base/stack_part are
// deliberately absent: stack_mode=chunk REQUIRES stack_part present
// (validateStackingReservedInputs), and the integration run has none - a bug
// an adversarial audit found: chunkRunInputs forced stack_mode=chunk here
// with an always-empty stack_part, so every stack's integration run failed
// admission the moment every chunk merged.
func integrationRunInputs(planInputs map[string]string, prBase string) (map[string]any, map[string]string) {
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

// refuseUndrivenStackPlanRun refuses `mivia workflow deliver <runID>` on the
// PLAN run of a multi-chunk stack: `workflow run`/`resume` drive such a
// stack's chunks BEFORE delivering the plan run itself (drive-before-
// delivery ordering, see maybeDriveSettledStack's call sites). Delivering
// the plan run directly skips that ordering - it settles the plan run's
// own delivery (no diff for a stacking workflow, so it settles succeeded)
// without ever driving the chunks or the final integration run, silently
// abandoning the stack while reporting the plan run "succeeded" (live
// finding, 2026-08-15). A non-stacking run, a single/no_bug plan, or a
// chunk/integration run (no decompose attempt of its own) is unaffected.
func refuseUndrivenStackPlanRun(ctx context.Context, repo workflowledger.Repository, runID string) error {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return nil // best-effort: a lookup failure here must not block a legitimate deliver
	}
	var decomposeOutputRef string
	for _, a := range attempts {
		if a.StepID == stackDecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded {
			decomposeOutputRef = a.OutputRef
		}
	}
	if decomposeOutputRef == "" {
		return nil // not a plan run with a succeeded decompose step
	}
	raw, err := repo.LoadContent(ctx, decomposeOutputRef)
	if err != nil {
		return nil
	}
	mode, chunks, _, _, err := parseStackPlanOutput(raw)
	if err != nil || mode != "multi" || len(chunks) == 0 {
		return nil // single/no_bug, or a malformed plan another gate already rejects
	}
	return fmt.Errorf("workflow run %q is the plan run of a %d-chunk stack: deliver it via `mivia workflow run` or `mivia workflow resume %s`, which drive the stack's chunks and integration run before delivering the plan run itself - delivering it directly here would abandon the undriven stack while reporting the plan run succeeded", runID, len(chunks), runID)
}
