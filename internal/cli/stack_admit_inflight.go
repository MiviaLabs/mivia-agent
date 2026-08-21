package cli

// F7 self-heal for an orphaned in-flight chunk run: driveChunk defers to
// driveChunkInFlight whenever a chunk's stable-key run already exists at
// pending/running/waiting_approval, so this stays a peer of driveChunk in
// the admission call graph. Split out of stack_admit.go to keep that file
// under the repo's per-file line ceiling (.mivia/policy/go-structure.json).

import (
	"context"
	"fmt"
	"io"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// stackChunkResumeFn resumes an orphaned in-flight chunk run (F7 self-heal
// in driveChunkInFlight). It is set in init(), not by a direct var
// initializer, to avoid a package-initialization cycle: driveChunkInFlight
// is reached from driveStackToCompletion's call graph, but executeWorkflowResume's
// own call graph reaches back into workflowStackDriveToCompletion via
// finishWorkflowResumeSettled -> maybeDriveSettledStack (see that var's
// comment in workflow_run.go for the mirror-image problem it already
// solves). A direct `var stackChunkResumeFn = executeWorkflowResume` here
// would close that loop and fail with "initialization cycle"; Go's
// dependency analysis only traces variable INITIALIZER expressions, so a
// nil-initialized var assigned inside init() carries no such dependency.
// Production always resolves to executeWorkflowResume; tests may override it.
var stackChunkResumeFn func(runID, root, configPath string, force, allowPublish, acceptVerifierChange, acceptSkillChange bool, stdout, stderr io.Writer) error

func init() {
	stackChunkResumeFn = executeWorkflowResume
}

// driveChunkInFlight handles a chunk whose run is already pending/running/
// waiting_approval (F15's "never admit a duplicate"). GetRunClaim is a
// trustworthy liveness probe here: LinearController.Advance claims and
// heartbeats the run's execution claim for as long as its process is
// genuinely alive, for every admission path (CLI and resume alike), not just
// the resume path (F7's "no liveness probe anywhere in the driver"). A live
// claim means the run is actively being driven elsewhere, so the driver
// parks with an honest status instead of the old unconditional "re-run drive
// after it settles" (which was never true once the admitting process died).
// A stale or absent claim means no process currently holds it - the
// admitting process died, or the run legitimately parked at
// waiting_approval and its controller already returned - and the driver
// self-heals by resuming it through the exact non-force expired-claim
// takeover `mivia workflow resume` already uses (claimWorkflowResumeHandoff),
// instead of waiting for a manual resume or the >=2min session sweep.
func driveChunkInFlight(ctx context.Context, prepared *preparedWorkflowRun, ledger *workflowledger.Store, stackID, chunkID string, run workflowledger.RunSnapshot, allowPublish bool, stdout, stderr io.Writer) (bool, error) {
	holder, acquiredAt, ok, claimErr := prepared.repo.GetRunClaim(ctx, run.RunID)
	if claimErr == nil && ok && time.Since(acquiredAt) <= workflowledger.DefaultClaimLease {
		fmt.Fprintf(stdout, "chunk=%s run=%s already in flight (%s), held by %s, refreshed %s ago; re-run drive after it settles\n", chunkID, run.RunID, run.Status, holder, time.Since(acquiredAt).Round(time.Second))
		return true, nil
	}
	if err := stackChunkResumeFn(run.RunID, prepared.root, prepared.res.ConfigPath, false, allowPublish, false, false, stdout, stderr); err != nil {
		// Most commonly a genuine race: another executor claimed the run
		// between the probe above and this call. Fall back to the honest
		// park message rather than halting the stack over it.
		fmt.Fprintf(stdout, "chunk=%s run=%s already in flight (%s); auto-resume failed (%v); re-run drive after it settles\n", chunkID, run.RunID, run.Status, err)
		return true, nil
	}
	return driveChunkResumedOutcome(ctx, prepared, ledger, stackID, chunkID, run.RunID, stdout)
}

// driveChunkResumedOutcome maps a resumed chunk run's final status to the
// stack task ledger. Unlike driveChunk's fresh-admission switch, this never
// triggers deliverRunWithStore itself: executeWorkflowResume already drove
// delivery (via finishWorkflowResumeSettled) when the run reached
// delivery_pending during the resume, so RunStatusSucceeded here covers both
// an already-delivered run and a confirmed no-diff run - chunkSettleAfterDelivery
// (not chunkSettleSucceeded, which is for a run that reached succeeded
// directly at fresh admission, before any delivery attempt this call) is the
// correct settle for either, since it classifies purely from delivery
// records. A still-delivery_pending run means the resume did not deliver
// (no --allow-publish, policy not auto): that mirrors driveChunk's own
// "awaits the publish grant" branch exactly.
func driveChunkResumedOutcome(ctx context.Context, prepared *preparedWorkflowRun, ledger *workflowledger.Store, stackID, chunkID, runID string, stdout io.Writer) (bool, error) {
	fresh, err := prepared.repo.GetRun(ctx, runID)
	if err != nil {
		return true, fmt.Errorf("chunk %s: read run status after resume: %w", chunkID, err)
	}
	switch fresh.Status {
	case workflowledger.RunStatusSucceeded:
		fmt.Fprintln(stdout, chunkSettleAfterDelivery(prepared.repo, ledger, stackID, chunkID, fresh))
		return true, nil
	case workflowledger.RunStatusDeliveryPending:
		_ = ledger.TransitionTask(stackID, chunkID, stackStatusReviewed)
		fmt.Fprintf(stdout, "chunk=%s awaits the publish grant: mivia workflow deliver %s --allow-publish\n", chunkID, fresh.RunID)
		return true, nil
	case workflowledger.RunStatusFailed, workflowledger.RunStatusCanceled, workflowledger.RunStatusTimedOut, workflowledger.RunStatusDeliveryFailed:
		act, err := reconcileReopenOrFail(ledger, stackID, chunkID)
		if err != nil {
			return true, fmt.Errorf("chunk %s: reopen/fail transition failed: %w", chunkID, err)
		}
		if act.Action == stackActionMarkFailed {
			return true, fmt.Errorf("chunk %s failed terminally after %d attempts; stack halts", chunkID, act.Attempts)
		}
		fmt.Fprintf(stdout, "chunk=%s run failed; reopened for retry (%s)\n", chunkID, act.Note)
		return true, nil
	default:
		fmt.Fprintf(stdout, "chunk=%s resumed run settled at %s; leaving for reconciliation\n", chunkID, fresh.Status)
		return true, nil
	}
}

// driveIntegrationInFlight handles an orphaned in-flight integration run with
// the same liveness-probe and auto-resume self-heal as driveChunkInFlight.
// Integration runs have no task ledger entry, so this function does not call
// driveChunkResumedOutcome; it only probes the claim and resumes.
func driveIntegrationInFlight(ctx context.Context, prepared *preparedWorkflowRun, run workflowledger.RunSnapshot, allowPublish bool, stdout, stderr io.Writer) error {
	holder, acquiredAt, ok, claimErr := prepared.repo.GetRunClaim(ctx, run.RunID)
	if claimErr == nil && ok && time.Since(acquiredAt) <= workflowledger.DefaultClaimLease {
		fmt.Fprintf(stdout, "integration run=%s already in flight (%s), held by %s, refreshed %s ago; re-run drive after it settles\n", run.RunID, run.Status, holder, time.Since(acquiredAt).Round(time.Second))
		return nil
	}
	if err := stackChunkResumeFn(run.RunID, prepared.root, prepared.res.ConfigPath, false, allowPublish, false, false, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "integration run=%s already in flight (%s); auto-resume failed (%v); re-run drive after it settles\n", run.RunID, run.Status, err)
		return nil
	}
	fresh, err := prepared.repo.GetRun(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("integration run: read status after resume: %w", err)
	}
	fmt.Fprintf(stdout, "integration run=%s resumed; status=%s\n", fresh.RunID, fresh.Status)
	return nil
}

// driveStackResumeStaleClaims rescues in-flight chunk runs whose task is at
// running (not an admissible pre-status) but whose execution claim is stale
// (F7): the admitting process died mid-run. nextAdmissionWave never picks
// running tasks, so without this pass the drive skips them entirely and the
// stack stalls. Each stale-claim chunk is routed to driveChunkInFlight which
// resumes it through the same non-force expired-claim takeover path that
// `mivia workflow resume` uses.
func driveStackResumeStaleClaims(ctx context.Context, prepared *preparedWorkflowRun, ledger *workflowledger.Store, stackID string, order []string, stdout, stderr io.Writer) error {
	for _, chunkID := range order {
		t, err := ledger.GetTask(stackID, chunkID)
		if err != nil {
			continue
		}
		if t.Status != stackStatusRunning {
			continue
		}
		run, found, err := stackRunRef(prepared.repo, stackID, chunkID)
		if err != nil || !found {
			continue
		}
		if !isResumableRunStatus(run.Status) {
			continue
		}
		if !stackRunClaimStale(prepared.repo, run.RunID) {
			continue
		}
		fmt.Fprintf(stdout, "chunk=%s task is running with stale claim; auto-resuming\n", chunkID)
		if _, err := driveChunkInFlight(ctx, prepared, ledger, stackID, chunkID, run, true, stdout, stderr); err != nil {
			return fmt.Errorf("stack drive: stale-claim resume chunk %s: %w", chunkID, err)
		}
	}
	return nil
}
