package localengine

// engine_stack.go: drive-before-delivery for the agent-tools engine.
//
// A stacking plan run whose controller parks at its delivery_pending success
// terminal must drive its chunk stack automatically before the plan run's own
// delivery can settle or publish. The CLI path drives in executeWorkflowRun
// (maybeDriveSettledStack), the session path in sessionAutoDeliveryRepairLoop,
// and this engine drives here. The drive reuses the same durable state layer
// as the CLI driver (internal/workflows/stacking): the task ledger, the run
// ledger, and the PR merge oracle. It admits chunk runs through the engine's
// own Start with stable invocation keys (re-entry resolves to the same runs),
// delivers them per the workflow's merge policy, waits for merges, admits the
// final integration run, and settles the plan run.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/stacking"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// stackDrivePollInterval is how often a drive pass re-reads durable state
// while waiting for chunk runs to settle or PRs to merge. Both drive pacing
// intervals are vars (not consts) so the drive tests can shrink them to a
// fast, deterministic clock; these defaults are the shipped pacing.
var stackDrivePollInterval = 2 * time.Second

// stackDriveMaxBackoff caps the exponential backoff a drive pass waits
// before polling again when nothing progressed (a transient delivery fault
// that leaves a chunk delivery_pending, or a merge the oracle has not yet
// observed). Without it a persistently faulting chunk burns one delivery
// attempt every poll tick forever (STACK-2, 2026-08-16): 2s -> 4s -> 8s ->
// ... -> 30s, reset to the base interval by any progressing pass.
var stackDriveMaxBackoff = 30 * time.Second

// stackDriveAfterPark drives the chunk stack of a plan run whose controller
// just parked at delivery_pending. It is a re-entrant, durable-state-only
// pass: any state it cannot reach (missing store, missing ledger, incremental
// decompose continuation) leaves the run delivery_pending and the stack
// resumable via `mivia stack drive`.
func (e *Engine) stackDriveAfterPark(ctx context.Context, runID string) {
	run, err := e.Repo.GetRun(ctx, runID)
	if err != nil {
		return
	}
	if run.Status != workflowledger.RunStatusDeliveryPending {
		return
	}
	// A derived run of another stack - a chunk run, the final integration
	// run, or a decompose-continuation wave - is never itself the plan run
	// of a stack to drive, even though its decompose output can parse as
	// mode=multi (the integration run re-plans the merged suite inline). An
	// auto-drive here would seed a brand-new stack under the derived run and
	// cascade: each admitted integration run would park and drive again,
	// forever (live finding, 2026-08-17: the scripted-runner integration run
	// re-decomposed the merged suite as mode=multi and the engine drove
	// wfr-inv-* chains without bound). The plan run's own InvocationKey is
	// always "" (stacking.PlanInputs), and derived keys are always
	// "<stack-id>:<chunk-id>" / "<stack-id>:decompose:N" with the plan run's
	// RunID as the stack-id prefix (stacking.AdmissionKey,
	// decomposeContinueKey), so a key whose colon-prefix is a real run marks
	// a derived run beyond doubt.
	if key := run.InvocationKey; key != "" {
		if i := strings.Index(key, ":"); i > 0 && run.RunID != key[:i] {
			if _, err := e.Repo.GetRun(ctx, key[:i]); err == nil {
				return
			}
		}
	}
	compiled, err := compileStackPlanRun(ctx, e.Repo, run)
	if err != nil || compiled == nil || compiled.Stacking == nil || !compiled.Stacking.Enabled || !compiled.DeliveryActive() {
		return
	}
	planOutput, err := stacking.LoadStackPlanOutput(ctx, e.Repo, runID)
	if err != nil {
		return // not a plan run with a succeeded decompose output
	}
	mode, chunks, hasMore, _, err := stacking.ParseStackPlanOutput(planOutput)
	if err != nil || mode != "multi" || len(chunks) == 0 {
		return
	}
	if e.Store == nil {
		return // no task ledger; the operator drive owns this stack
	}
	ledger := tasks.NewStore(e.Store)
	if err := stacking.SeedStackLedger(ctx, ledger, runID, chunks); err != nil {
		log.Printf("workflow: drive stack for %s: seed: %v", runID, err)
		return
	}
	if hasMore {
		// Incremental-decompose continuation waves remain the operator
		// drive's concern (`mivia stack drive`); the run stays
		// delivery_pending and the stack resumable from durable state.
		return
	}
	order, err := stacking.TopologicalOrder(chunks)
	if err != nil {
		log.Printf("workflow: drive stack for %s: %v", runID, err)
		return
	}
	planInputs, err := stacking.PlanInputs(ctx, e.Repo, runID)
	if err != nil {
		log.Printf("workflow: drive stack for %s: plan inputs: %v", runID, err)
		return
	}
	prBase, err := stacking.PRBase(compiled)
	if err != nil {
		log.Printf("workflow: drive stack for %s: pr base: %v", runID, err)
		return
	}
	e.driveStackLoop(ctx, run, compiled, ledger, runID, chunks, order, planInputs, prBase)
}

// compileStackPlanRun compiles a run's recorded definition TOML for resume.
func compileStackPlanRun(ctx context.Context, repo workflowledger.Repository, run workflowledger.RunSnapshot) (*definition.CompiledWorkflow, error) {
	raw, err := repo.GetRunSnapshot(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil, err
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, run.WorkflowName+".toml")
	if err != nil {
		return nil, err
	}
	return definition.CompileForResume(&wf)
}

// driveStackLoop advances the stack until it is complete or nothing can
// progress, polling durable state between passes.
func (e *Engine) driveStackLoop(ctx context.Context, planRun workflowledger.RunSnapshot, compiled *definition.CompiledWorkflow, ledger *tasks.Store, stackID string, chunks []stacking.ChunkPlan, order []string, planInputs map[string]string, prBase string) {
	autoPublish := compiled.Stacking.MergePolicy == "auto"
	chunkPlans := make(map[string]*stacking.ChunkPlan, len(chunks))
	for i := range chunks {
		chunkPlans[chunks[i].ID] = &chunks[i]
	}
	// The wait between passes doubles when nothing progressed (a transient
	// delivery fault leaving a chunk delivery_pending, a merge the oracle has
	// not observed yet), so a persistently faulting chunk cannot burn a
	// delivery attempt every poll tick: 2s -> 4s -> 8s -> ... ->
	// stackDriveMaxBackoff, reset to the base interval by any progressing
	// pass (STACK-2, 2026-08-16).
	backoff := stackDrivePollInterval
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		byID, err := stacking.TaskMap(ctx, ledger, stackID)
		if err != nil {
			log.Printf("workflow: drive stack %s: task map: %v", stackID, err)
			return
		}
		progressed := false
		// 1. Advance settled chunk runs (deliver per policy, mark
		//    published/merged, reopen failures).
		if e.processSettledChunks(ctx, ledger, stackID, byID, autoPublish) {
			progressed = true
			byID, err = stacking.TaskMap(ctx, ledger, stackID)
			if err != nil {
				return
			}
		}
		// 2. Mark chunks merged once their PRs actually merged.
		if e.markMergedChunks(ctx, ledger, stackID, byID) {
			progressed = true
			byID, err = stacking.TaskMap(ctx, ledger, stackID)
			if err != nil {
				return
			}
		}
		merged := stacking.MergedSet(byID)
		// 3. Every chunk merged: admit the integration run and settle the
		//    plan run.
		if stacking.AllChunksMerged(chunks, merged) {
			e.finishStack(ctx, planRun, compiled, ledger, stackID, planInputs, prBase)
			return
		}
		// 4. Admit the next ready wave; the next pass re-reads durable
		//    state for the admitted runs.
		wave := nextAdmissionWave(byID, merged, order)
		if len(wave) > 0 {
			e.admitWave(ctx, planRun, ledger, stackID, chunkPlans, order, wave, planInputs, prBase, autoPublish)
			backoff = stackDrivePollInterval // a fresh wave: resume the base poll pace
			continue
		}
		// 5. Stop when nothing can progress without external action: no ready
		//    chunks, nothing in flight that a pass can advance, no published
		//    PR awaiting a merge the oracle can see.
		if !stackHasProgress(byID, e.PR != nil) {
			return
		}
		// Nothing advanced this pass: wait out the backoff before polling
		// again, preserving the ctx stop so a cancelled pass still returns
		// and releases the run execution flock.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if progressed {
			backoff = stackDrivePollInterval
		} else {
			backoff *= 2
			if backoff > stackDriveMaxBackoff {
				backoff = stackDriveMaxBackoff
			}
		}
	}
}

// processSettledChunks advances the durable task state of every chunk whose
// run has settled, mirroring the CLI driver's chunkSettle* transitions:
//   - succeeded + confirmed no_diff -> merged
//   - succeeded + pushed evidence   -> published
//   - delivery_pending under auto   -> deliver now, then merged/published
//   - delivery_pending under approve -> reviewed (the publish grant is the
//     host's auto-delivery or an explicit deliver)
//   - failed/canceled/timed_out/delivery_failed -> reopen (bounded attempts)
//     or mark failed
//
// Returns whether any task transitioned.
func (e *Engine) processSettledChunks(ctx context.Context, ledger *tasks.Store, stackID string, byID map[string]tasks.Task, autoPublish bool) bool {
	progressed := false
	for id, t := range byID {
		if !isStackInFlightStatus(t.Status) {
			continue
		}
		run, found, err := e.stackRunByKey(ctx, stackID, id)
		if err != nil {
			log.Printf("workflow: drive stack %s: lookup chunk %s: %v", stackID, id, err)
			continue
		}
		if !found {
			continue // run not admitted yet; wait for it
		}
		switch run.Status {
		case workflowledger.RunStatusPending, workflowledger.RunStatusRunning, workflowledger.RunStatusWaitingApproval:
			continue // still in flight
		case workflowledger.RunStatusSucceeded:
			if stacking.ChunkRunNoDiff(ctx, e.Repo, run) {
				progressed = transitionStackTask(ledger, stackID, id, stacking.StatusMerged) || progressed
				continue
			}
			if stacking.RunPushed(ctx, e.Repo, run) {
				if t.Status != stacking.StatusPublished {
					progressed = transitionStackTask(ledger, stackID, id, stacking.StatusPublished) || progressed
				}
				continue
			}
			if t.Status != stacking.StatusImplemented {
				progressed = transitionStackTask(ledger, stackID, id, stacking.StatusImplemented) || progressed
			}
		case workflowledger.RunStatusDeliveryPending:
			if t.Status == stacking.StatusPublished {
				continue // PR open; the merge wait owns it
			}
			if t.Status == stacking.StatusReviewed {
				continue // awaiting the publish grant
			}
			if autoPublish {
				if _, derr := e.Deliver(ctx, run.RunID, true); derr == nil {
					if e.settleChunkDelivery(ctx, ledger, stackID, id, run) {
						progressed = true
					}
				} else {
					log.Printf("workflow: drive stack %s: deliver chunk %s: %v", stackID, id, derr)
				}
			} else {
				progressed = transitionStackTask(ledger, stackID, id, stacking.StatusReviewed) || progressed
			}
		case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
			progressed = e.reopenOrFailStackTask(ctx, ledger, stackID, id) || progressed
		}
	}
	return progressed
}

// settleChunkDelivery applies the outcome of a chunk delivery that returned
// a nil error. Deliver reports success, a permanent refusal, and a repair
// re-entry identically (nil error): only a succeeded run may mark the chunk
// published or merged (trusting derr==nil alone marks a refused or repairable
// delivery published and wedges the stack in published-never-merged, STACK-1
// 2026-08-16), a delivery_failed run applies the bounded reopen-or-fail
// decision (mirroring the CLI's reconcileReopenOrFail, capped at
// stacking.MaxChunkAttempts), and anything else stays in flight. It reports
// whether the chunk's task map entry changed.
func (e *Engine) settleChunkDelivery(ctx context.Context, ledger *tasks.Store, stackID, chunkID string, run workflowledger.RunSnapshot) bool {
	fresh, err := e.Repo.GetRun(ctx, run.RunID)
	if err != nil {
		log.Printf("workflow: drive stack %s: reread chunk %s: %v", stackID, chunkID, err)
		return false
	}
	switch fresh.Status {
	case workflowledger.RunStatusSucceeded:
		if stacking.ChunkRunNoDiff(ctx, e.Repo, fresh) {
			return transitionStackTask(ledger, stackID, chunkID, stacking.StatusMerged)
		}
		return transitionStackTask(ledger, stackID, chunkID, stacking.StatusPublished)
	case workflowledger.RunStatusDeliveryFailed:
		return e.reopenOrFailStackTask(ctx, ledger, stackID, chunkID)
	default:
		// running (repair re-entry) and delivery_pending (transient/
		// transport fault) leave the task in flight: a later pass re-drives
		// the run once it settles, or re-delivers after the bounded backoff.
		return false
	}
}

// markMergedChunks marks a published/implemented chunk merged once its PR
// actually merged. The engine has no merge authority (no PRClient.Merge); the
// host or an operator lands the merge, and the PR oracle (e.PR) reports it.
// Without an oracle nothing can be marked merged and the drive stops (the
// operator `mivia stack drive` path carries the git+gh oracle).
func (e *Engine) markMergedChunks(ctx context.Context, ledger *tasks.Store, stackID string, byID map[string]tasks.Task) bool {
	if e.PR == nil {
		return false
	}
	progressed := false
	for id, t := range byID {
		if t.Status != stacking.StatusPublished && t.Status != stacking.StatusImplemented {
			continue
		}
		run, found, err := e.stackRunByKey(ctx, stackID, id)
		if err != nil || !found {
			continue
		}
		merged, err := e.prMerged(ctx, run)
		if err != nil {
			log.Printf("workflow: drive stack %s: merge check chunk %s: %v", stackID, id, err)
			continue
		}
		if merged {
			progressed = transitionStackTask(ledger, stackID, id, stacking.StatusMerged) || progressed
		}
	}
	return progressed
}

// prMerged reports whether the run's PR branch actually merged, via the
// engine's PR adapter (delivery.PRClient.IsMerged). Mirrors the CLI's
// MergeChecker.Merged for the in-process drive.
func (e *Engine) prMerged(ctx context.Context, run workflowledger.RunSnapshot) (bool, error) {
	if e.PR == nil || run.WorktreeName == "" {
		return false, nil
	}
	slug, _ := delivery.ParseOwnerRepo(run.RemoteURL)
	if slug == "" {
		return false, nil
	}
	head := "wf/" + run.WorktreeName
	ref, err := e.PR.FindByHead(ctx, slug, head)
	if err != nil {
		return false, err
	}
	if ref == nil {
		return false, nil
	}
	return e.PR.IsMerged(ctx, slug, head)
}

// reopenOrFailStackTask reopens a failed chunk when its attempt budget
// remains, else marks it failed. The decision and the attempt-count read are
// atomic (TransitionTaskCASDecide), mirroring the CLI's
// reconcileReopenOrFail: two concurrent failure handlers for the same chunk
// cannot both reopen past MaxChunkAttempts. The task must be running (the
// only status a chunk's failure path is reached from); otherwise this is a
// clean loss and returns false.
func (e *Engine) reopenOrFailStackTask(ctx context.Context, ledger *tasks.Store, stackID, id string) bool {
	applied, _, _, err := ledger.TransitionTaskCASDecide(stackID, id, []string{stacking.StatusRunning}, stacking.StatusReopened,
		func(attempts int) (string, bool) {
			if attempts+1 > stacking.MaxChunkAttempts {
				return stacking.StatusFailed, true
			}
			return stacking.StatusReopened, true
		})
	if err != nil {
		log.Printf("workflow: drive stack %s: reopen-or-fail %s: %v", stackID, id, err)
		return false
	}
	return applied
}

// nextAdmissionWave returns the next wave of ready chunks in topological
// order: admissible-pre and with all dependencies merged.
func nextAdmissionWave(byID map[string]tasks.Task, merged map[string]bool, order []string) []string {
	var wave []string
	for _, id := range order {
		t, ok := byID[id]
		if !ok {
			continue
		}
		if stacking.StatusIsAdmissiblePre(t.Status) && stacking.TaskReady(t, merged) {
			wave = append(wave, id)
		}
	}
	return wave
}

// admitWave admits one wave of ready chunk runs through the engine's own
// Start, with the stable chunk admission key (re-entry resolves to the same
// run) and the chunk admission inputs (D3 replay + stack_mode=chunk +
// chunk_plan + sibling_files). A chunk whose run already exists is left to
// the settle processing: in-flight runs are not double-admitted, and a
// terminal run is handled by processSettledChunks (reopen) or the operator
// drive (which mints a fresh run).
func (e *Engine) admitWave(ctx context.Context, planRun workflowledger.RunSnapshot, ledger *tasks.Store, stackID string, chunkPlans map[string]*stacking.ChunkPlan, order, wave []string, planInputs map[string]string, prBase string, autoPublish bool) {
	for _, id := range wave {
		_, found, err := e.stackRunByKey(ctx, stackID, id)
		if err != nil {
			log.Printf("workflow: drive stack %s: lookup chunk %s: %v", stackID, id, err)
			continue
		}
		if found {
			continue // in-flight or terminal; settle processing handles it
		}
		part, perr := stacking.ChunkPartIndex(id, order)
		if perr != nil {
			log.Printf("workflow: drive stack %s: chunk %s: %v", stackID, id, perr)
			continue
		}
		stackPart := fmt.Sprintf("%d/%d", part+1, len(order))
		siblings := stacking.SiblingFiles(chunkPlans, id)
		inputs, _ := stacking.ChunkRunInputs(planInputs, id, prBase, stackPart, chunkPlans[id], siblings)
		key, kerr := stacking.AdmissionKey(stackID, id)
		if kerr != nil {
			log.Printf("workflow: drive stack %s: chunk %s: %v", stackID, id, kerr)
			continue
		}
		// Claim the task before admitting so a concurrent driver cleanly
		// loses the race (CAS against admissible-pre statuses).
		ok, terr := ledger.TransitionTaskCAS(stackID, id, stacking.AdmissiblePreStatuses, stacking.StatusRunning)
		if terr != nil || !ok {
			continue
		}
		if _, serr := e.Start(ctx, agenttools.StartRequest{
			Workflow:      planRun.WorkflowName,
			Inputs:        inputs,
			InvocationKey: key,
			AllowPublish:  autoPublish,
		}); serr != nil {
			log.Printf("workflow: drive stack %s: admit chunk %s: %v", stackID, id, serr)
			_ = ledger.TransitionTask(stackID, id, stacking.StatusReopened)
		}
	}
}
