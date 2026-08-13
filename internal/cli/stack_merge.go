package cli

// Stack merge policy (plan D2/D3/D8, §5a policy B): wait loops that land the
// stack's PRs. Under merge_policy=auto the driver merges published chunk PRs
// itself (mark ready -> CI green -> squash-merge), and the integration PR is
// waited out to an actual git merge before the stack reports complete.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
)

// waitForChunkMerges polls the reconcile loop until every chunk is merged,
// surfacing a terminal chunk failure as a stack halt. Reconcile is idempotent
// and marks a chunk merged as soon as git reports its PR branch merged, so a
// later drive pass naturally skips it and admits the next wave.
func waitForChunkMerges(repo workflowledger.Repository, ledger *tasks.Store, checker MergeChecker, stackID string, chunks []ChunkPlan, policy string, stdout, stderr io.Writer) error {
	const pollInterval = 20 * time.Second
	ticks := 0
	for {
		// merge_policy=auto: publish the outstanding PRs' merges ourselves.
		if policy == "auto" {
			if err := autoMergePublishedChunks(repo, ledger, stackID, stdout, stderr); err != nil {
				return err
			}
		}
		actions, err := reconcileStack(ledger, repo, checker, stackID, stackMaxChunkAttempts)
		if err != nil {
			return fmt.Errorf("stack drive: reconcile: %w", err)
		}
		for _, a := range actions {
			if a.Action == stackActionMarkFailed {
				return fmt.Errorf("stack %s halted: chunk %s failed terminally (%s)", stackID, a.TaskID, a.Note)
			}
		}
		byID, err := stackTaskMap(ledger, stackID)
		if err != nil {
			return err
		}
		if allChunksMerged(chunks, stackMergedSet(byID)) {
			return nil
		}
		ticks++
		if ticks%3 == 0 {
			fmt.Fprintf(stdout, "stack %s: waiting for chunk merges to land...\n", stackID)
		}
		time.Sleep(pollInterval)
	}
}

// autoMergePublishedChunks merges every published chunk PR for the stack
// (merge_policy=auto). A PR that is not mergeable yet (checks pending/red,
// review requirements) reports why and is retried on the next poll; reconcile
// marks it merged the moment git reports the merge landed.
func autoMergePublishedChunks(repo workflowledger.Repository, ledger *tasks.Store, stackID string, stdout, stderr io.Writer) error {
	byID, err := stackTaskMap(ledger, stackID)
	if err != nil {
		return err
	}
	for id, t := range byID {
		if t.Status != stackStatusPublished {
			continue
		}
		if err := autoMergeOne(repo, stackID, id, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// autoMergeOne resolves one chunk's PR (by its run's head branch) and merges
// it. No PR yet, or a merge refusal, is not an error: the wait loop retries.
func autoMergeOne(repo workflowledger.Repository, stackID, chunkID string, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(repo, stackID, chunkID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	head := stackHeadBranch(run)
	if head == "" {
		return nil
	}
	slug, err := delivery.ParseOwnerRepo(run.RemoteURL)
	if err != nil {
		return fmt.Errorf("chunk %s: resolve repo: %w", chunkID, err)
	}
	ref, err := workflowDeliverNewPR().FindByHead(context.Background(), slug, head)
	if err != nil {
		return fmt.Errorf("chunk %s: find PR: %w", chunkID, err)
	}
	if ref == nil {
		return nil // PR not visible yet; poll later
	}
	if err := delivery.MergePullRequest(context.Background(), slug, ref.RemoteID, ref.Draft); err != nil {
		fmt.Fprintf(stdout, "chunk=%s PR %s not mergeable yet: %v\n", chunkID, ref.RemoteID, err)
		return nil // keep polling; reconcile marks merged once it lands
	}
	fmt.Fprintf(stdout, "chunk=%s PR %s merged (or enqueued on the merge queue)\n", chunkID, ref.RemoteID)
	return nil
}

// waitIntegrationRunSettled finishes the last act of a completed stack: the
// integration run was admitted and settled by the drive pass; publish it when
// allowed and report the stack's terminal state. With merge_policy=auto the
// integration PR is merged and the merge waited out before the stack reports
// complete.
func waitIntegrationRunSettled(prepared *preparedWorkflowRun, ledger *tasks.Store, checker MergeChecker, stackID string, policy string, allowPublish bool, stdout, stderr io.Writer) error {
	run, found, err := stackRunRef(prepared.repo, stackID, stackIntegrationChunkID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("stack %s: integration run was not admitted", stackID)
	}
	if run.Status == workflowledger.RunStatusDeliveryPending && allowPublish {
		if err := deliverRunWithStore(context.Background(), prepared.root, prepared.res, prepared.store, prepared.repo, run.RunID, true, false, stdout, stderr); err != nil {
			return fmt.Errorf("integration run delivery failed: %w", err)
		}
	}
	if policy == "auto" && run.Status == workflowledger.RunStatusDeliveryPending {
		if err := autoMergeOne(prepared.repo, stackID, stackIntegrationChunkID, stdout, stderr); err != nil {
			return err
		}
		return waitForIntegrationMerge(prepared.repo, checker, stackID, stdout, stderr)
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		fmt.Fprintf(stdout, "stack %s complete; integration run awaits the publish grant: mivia workflow deliver %s --allow-publish\n", stackID, run.RunID)
		return nil
	}
	fmt.Fprintf(stdout, "stack %s complete: integration run=%s status=%s\n", stackID, run.RunID, run.Status)
	return nil
}

// waitForIntegrationMerge polls git until the integration PR's branch is
// merged into the base, so the stack reports complete only after the final
// PR actually lands.
func waitForIntegrationMerge(repo workflowledger.Repository, checker MergeChecker, stackID string, stdout, stderr io.Writer) error {
	const pollInterval = 20 * time.Second
	ticks := 0
	for {
		run, found, err := stackRunRef(repo, stackID, stackIntegrationChunkID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("stack %s: integration run disappeared", stackID)
		}
		if head := stackHeadBranch(run); head != "" {
			merged, err := checker.Merged(context.Background(), head, stackRunPushed(repo, run))
			if err != nil {
				return err
			}
			if merged {
				fmt.Fprintf(stdout, "stack %s complete: integration PR merged (run=%s)\n", stackID, run.RunID)
				return nil
			}
		}
		ticks++
		if ticks%3 == 0 {
			fmt.Fprintf(stdout, "stack %s: waiting for the integration PR merge to land...\n", stackID)
		}
		time.Sleep(pollInterval)
	}
}
