package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// SetGitContext wires the run's pinned git context (worktree directory and
// its real git directory) into the controller before Start. It is what lets
// the controller measure the worktree diff itself, so the post-implement
// diff-size gate can reroute an oversized chunk to the workflow's diff-size
// repair step BEFORE the panel and preflight pipeline run on it. Without it
// the gate is off and delivery-time enforcement (delivery.on_diff_size_failure
// via delivery.RepairTarget) is the only guard.
func (c *LinearController) SetGitContext(gc delivery.GitContext) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.gitRunner = delivery.RealGit{}
	c.gitCtx = gc
	return nil
}

// WireGitContext pins the run's git context (main root, worktree name, and
// worktree directory) for the post-implement diff-size gate. It is the engine
// entry point used by both fresh starts and resumes; SetGitContext is the
// controller-internal setter. Best-effort: an empty or unverifiable worktree
// keeps the gate off and delivery-time enforcement as the only guard.
func (c *LinearController) WireGitContext(mainRoot, worktree, root string) error {
	if root == "" || mainRoot == "" || worktree == "" {
		return nil // not pinned; the gate stays off
	}
	gitDir, err := delivery.VerifyGitDir(context.Background(), mainRoot, worktree, root)
	if err != nil {
		return nil // best-effort: keep the gate off
	}
	return c.SetGitContext(delivery.GitContext{Dir: root, GitDir: gitDir})
}

// chunkDiffSizeGate is the deterministic post-implement diff-size gate. When
// a stacking run's implement step succeeds, the controller measures the
// ACTUAL worktree diff vs the admitted base (delivery.MeasureChunkDiffSize,
// the exact measurement delivery enforces at publish time). An over-limit
// diff reroutes the succeeded implement step to the workflow's diff-size
// repair step (delivery.on_diff_size_failure), so the chunk is shrunk before
// the panel and preflight pipeline run on it - instead of after a full
// pipeline pass and a delivery rejection.
//
// The gate is opt-in and conservative: it fires only when the workflow
// declares a stacking hard limit, the implement step succeeded, a git context
// is wired, the run has a base commit to measure against, and
// delivery.on_diff_size_failure names a declared non-terminal step. Every
// other shape (no stacking config, no hard limit, no declared repair target,
// a measurement failure) leaves the route untouched and the delivery-time
// gate remains the single authority.
func (c *LinearController) chunkDiffSizeGate(ctx context.Context, step definition.Step, route RouteDecision) (RouteDecision, bool) {
	if c.Workflow == nil || c.Workflow.Stacking == nil || c.Workflow.Stacking.HardLines <= 0 {
		return route, false
	}
	if step.ID != c.Workflow.Stacking.ImplementStep || route.ToStepID == "" {
		return route, false
	}
	var target string
	if c.Workflow.Delivery != nil {
		target = c.Workflow.Delivery.OnDiffSizeFailure
	}
	if target == "" || workflowledger.IsTerminalStepID(target) {
		// No declared repair target (or a terminal one): leave the route
		// alone. Delivery-time enforcement, which falls back to
		// delivery.on_failure, remains the guard.
		return route, false
	}
	if _, ok := c.WorkflowStep(target); !ok {
		return route, false
	}
	if route.ToStepID == target {
		return route, false
	}
	// The repair step runs BEFORE the panel and preflight pipeline, so its
	// MANDATORY steps.X.output bindings must resolve at implement-success
	// time. A binding to a step that has not produced an output in this run,
	// and that the chunk-mode grace does not cover (a post-implement step in
	// a plan or single run), would hard-fail the rerouted step with "missing
	// prior output". Leave the route untouched in that case: the
	// delivery-time gate re-enters the SAME step AFTER the pipeline ran -
	// when the binding resolves - so enforcement is deferred, never lost.
	if ok, err := c.repairStepContextResolvable(ctx, target); err != nil || !ok {
		return route, false
	}
	if c.gitRunner == nil || c.gitCtx.Dir == "" || c.gitCtx.GitDir == "" || c.admission.BaseCommit == "" {
		return route, false
	}
	// Measurement is best-effort: a failure to stage or measure (git missing,
	// a timeout, a transient error) leaves the route unchanged. The
	// delivery-time gate still measures with the delivery attempt's own
	// context, so a slow worktree is never silently unguarded.
	gitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	size, err := delivery.MeasureChunkDiffSize(gitCtx, c.gitRunner, c.gitCtx, c.admission.BaseCommit, c.Workflow.Stacking.HardLines, nil)
	if err != nil {
		return route, false
	}
	if size <= c.Workflow.Stacking.HardLines {
		return route, false
	}
	// The implement step's output succeeded but the actual diff exceeds the
	// hard limit. Reroute to the workflow's diff-size repair step: the chunk
	// is split or shrunk in the worktree before the panel and preflight
	// pipeline run on it. The attempt stays Succeeded, exactly like the
	// chunk-plan repair loop's reroute.
	repair := RouteDecision{
		ToStepID:        target,
		TransitionIndex: route.TransitionIndex,
		MatchDigest:     route.MatchDigest,
		DecisionJSON:    append([]byte(nil), route.DecisionJSON...),
	}
	return repair, true
}

// repairStepContextResolvable reports whether the repair step's MANDATORY
// steps.X.output context bindings resolve at implement-success time, mirroring
// contextForStep's resolution exactly. A binding is resolvable when the
// referenced step already produced an output in this run (the implement step's
// own output is recorded right after this settle, before the repair step's
// context builds), when the chunk-mode grace covers it (a step that never runs
// in a chunk run - the plan phase ran in the parent run), or when it is
// optional (an optional-absent binding resolves ""). Note that envelope_only
// does NOT make a mandatory binding resolvable: resolveBindingOutput hard-fails
// a mandatory binding with "missing prior output" when the referenced step has
// no output yet, envelope_only or not. A mandatory binding that would fail the
// rerouted step keeps the diff-size gate off: the delivery-time gate re-enters
// the SAME step after the pipeline ran, when the binding resolves, so
// enforcement is deferred, never lost.
func (c *LinearController) repairStepContextResolvable(ctx context.Context, target string) (bool, error) {
	targetStep, ok := c.WorkflowStep(target)
	if !ok {
		return false, nil
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return false, err
	}
	implementStep := ""
	if c.Workflow != nil && c.Workflow.Stacking != nil {
		implementStep = c.Workflow.Stacking.ImplementStep
	}
	for _, binding := range targetStep.Context {
		if binding.Optional {
			continue
		}
		parts := strings.Split(binding.From, ".")
		if len(parts) != 3 || parts[0] != "steps" || parts[2] != "output" {
			continue
		}
		fromStep := parts[1]
		if fromStep == implementStep {
			continue // its output is recorded before the repair step runs
		}
		if _, has := latestOutputAttempt(attempts, fromStep); has {
			continue
		}
		if c.preImplementStep(fromStep) {
			continue // chunk-mode grace resolves it absent
		}
		return false, nil
	}
	return true, nil
}
