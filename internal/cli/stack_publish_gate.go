package cli

// stackRunAutoPublishAllowed and its invocation-key helper answer one
// question for any code path that is about to auto-deliver a parked run
// without a human --allow-publish grant: is this run a stack chunk or
// integration run, and if so, does the stack's merge policy actually
// authorize automatic publication? Both the recovery sweep
// (reconcileParkedDelivery) and the session repair loop
// (sessionAutoDeliveryRepairLoop) must derive publish authority from this
// same policy - see stackingDriveAllowPublish's doc comment - or
// merge_policy=approve never pauses for a chunk/integration run and the
// human publish-grant checkpoint is dead (reachable-bug audit finding 1).

import (
	"context"
	"log"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackRunAutoPublishAllowed reports whether runID is a stack chunk or
// integration run (isStackRun) and, if so, whether its stack's merge policy
// authorizes automatic publication (allowed = merge_policy=="auto").
// isStackRun=false lets the caller fall through to its existing behavior for
// non-stacking runs: an ordinary workflow run, or the plan run itself, whose
// own InvocationKey is always "" (see stackPlanInputs) - that run's
// publication is authorized separately by the workflow's
// delivery.deliver_plan_run setting, not this predicate. Any resolution
// failure (missing run, corrupt snapshot) resolves to allowed=false: fail
// closed, exactly like stackingDriveAllowPublish's own default.
//
// isStackRun is derived from the run's OWN admission evidence: its admitted
// snapshot inputs must carry one of the reserved stack shapes (stack_mode
// with chunk/stack_part for chunk runs, stack_mode=single for integration
// runs, or stack_mode=decompose_continue for continuation runs). A
// caller-supplied invocation key that happens to embed a foreign plan-run id
// (e.g. "wfr-real:c1" from an ordinary workflow_run retry) is not
// withheld: the key prefix alone is not evidence of stack membership.
func stackRunAutoPublishAllowed(ctx context.Context, repo workflowledger.Repository, runID string) (isStackRun, allowed bool) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false, false
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return false, false
	}
	snapshot, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil || compiled == nil || compiled.Stacking == nil {
		return false, false
	}
	if !isStackRunInputs(snapshot.Inputs) {
		return false, false
	}
	stackID, ok := stackIDFromChunkInvocationKey(run.InvocationKey)
	if !ok {
		return false, false
	}
	planCompiled, ok := stackPlanCompiledWorkflow(ctx, repo, stackID)
	if !ok || planCompiled.Stacking == nil {
		// The run itself is stack-shaped, but its plan run cannot be resolved
		// as a stacking workflow (deleted, corrupt, or a non-stacking run id
		// collision). Fail closed: treat it as a stack run whose policy does
		// NOT authorize auto-publish, so it stays parked instead of falling
		// through to allowPublish=true (F2).
		return true, false
	}
	return true, planCompiled.Stacking.MergePolicy == "auto"
}

// isStackRunInputs reports whether a run's admitted snapshot inputs carry one
// of the reserved stack shapes that only the driver sets:
//   - stack_mode=chunk with chunk and stack_part (chunk run)
//   - stack_mode=single (integration run)
//   - stack_mode=decompose_continue (continuation wave)
//
// An ordinary workflow_run invocation carries none of these, so a
// caller-supplied invocation key that collides with a real plan-run id
// never matches.
func isStackRunInputs(inputs map[string]string) bool {
	switch inputs["stack_mode"] {
	case "chunk":
		return inputs["chunk"] != "" && inputs["stack_part"] != ""
	case "single", "decompose_continue":
		return true
	}
	return false
}

// stackPlanCompiledWorkflow resolves a run id's admitted compiled workflow,
// mirroring stackPlanMergePolicy's resolution but reporting ok=false on any
// failure instead of collapsing straight to a policy string - the caller
// needs to distinguish "not a stacking plan run at all" from "is one, but
// merge_policy isn't auto".
func stackPlanCompiledWorkflow(ctx context.Context, repo workflowledger.Repository, runID string) (*definition.CompiledWorkflow, bool) {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return nil, false
	}
	raw, err := repo.GetRunSnapshot(ctx, runID)
	if err != nil {
		return nil, false
	}
	_, compiled, _, err := validateWorkflowResumeSnapshot(run, raw)
	if err != nil || compiled == nil {
		return nil, false
	}
	return compiled, true
}

// stackRunPublishWithheld is the shared call-site guard for both
// reconcileParkedDelivery and sessionAutoDeliveryRepairLoop: it reports
// whether runID's publication must be withheld (a stack chunk/integration
// run under a non-auto merge policy) and, when so, logs the reason unless
// quiet. A caller that gets withheld=true must return without attempting
// deliverRunWithStore - the run stays parked at delivery_pending for the
// human publish grant.
func stackRunPublishWithheld(ctx context.Context, repo workflowledger.Repository, runID string, quiet bool) bool {
	isStackRun, allowed := stackRunAutoPublishAllowed(ctx, repo, runID)
	if !isStackRun || allowed {
		return false
	}
	if !quiet {
		if planMissingOrCorrupt(ctx, repo, runID) {
			log.Printf("workflow: session recovery: %s is a stack chunk/integration run, but its stack plan run is missing or unresolvable; leaving parked", runID)
		} else {
			log.Printf("workflow: session recovery: %s is a stack chunk/integration run awaiting a human publish grant (merge_policy != auto); leaving parked", runID)
		}
	}
	return true
}

// planMissingOrCorrupt reports whether a stack-shaped run's plan run cannot be
// resolved. It is used only for diagnostics in stackRunPublishWithheld; a true
// result means the gate withheld publication because the plan run was deleted
// or corrupt, not because merge_policy is approve/grant.
func planMissingOrCorrupt(ctx context.Context, repo workflowledger.Repository, runID string) bool {
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	stackID, ok := stackIDFromChunkInvocationKey(run.InvocationKey)
	if !ok {
		return false
	}
	_, ok = stackPlanCompiledWorkflow(ctx, repo, stackID)
	return !ok
}

// stackIDFromChunkInvocationKey extracts the stack (plan run) id from a
// chunk/integration run's stable invocation key ("<stack-id>:<chunk-id>",
// stackAdmissionKey) or a decompose-continuation key
// ("<stack-id>:decompose:N", stackDecomposeContinueKey). Both share the
// stack id as the prefix before the first colon; chunkIDRE forbids colons in
// a chunk id, so the first colon is always the stack/chunk boundary, never
// ambiguous with one inside the id itself. The plan run's own InvocationKey
// is always "" (stackPlanInputs), so this never matches it.
func stackIDFromChunkInvocationKey(key string) (string, bool) {
	i := strings.Index(key, ":")
	if i <= 0 {
		return "", false
	}
	return key[:i], true
}
