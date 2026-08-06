package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

var (
	workflowCleanupResolve                    = vcs.Resolve
	workflowCleanupRemove                     = vcs.RemoveWithPrefix
	workflowCleanupGit     delivery.GitRunner = delivery.RealGit{}
)

// executeWorkflowCleanup removes the run worktree and its wf/ branch after a
// terminal run. It is idempotent: a second run reports success when the
// worktree is already gone. Delivery-pending runs are allowed (cleanup is the
// explicit operator choice not to deliver). The caller checkout case (no
// recorded worktree) is a no-op that prints the reason.
func executeWorkflowCleanup(runID, root, configPath string, stdout, stderr io.Writer) error {
	releaseExecution, repo, closeFn, err := openWorkflowResolutionContext(root, configPath, runID)
	if err != nil {
		return err
	}
	defer closeFn()
	defer releaseExecution()
	ctx := context.Background()

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return fmt.Errorf("workflow run %q not found", runID)
		}
		return err
	}
	if !workflowledger.IsTerminalRunStatus(run.Status) && run.Status != workflowledger.RunStatusDeliveryPending {
		return fmt.Errorf("workflow run %q is %q; cleanup requires a finished run (succeeded, failed, canceled, timed_out, delivery_failed, or delivery_pending)", runID, run.Status)
	}
	name := run.WorktreeName
	if name == "" {
		fmt.Fprintln(stdout, "run used the caller checkout; nothing to clean")
		return nil
	}
	// Defense in depth: only ever touch a sanitized wf/ worktree whose name
	// matches the admitted record exactly.
	sanitized, err := vcs.SanitizeName(name)
	if err != nil || sanitized != name {
		return fmt.Errorf("workflow run %q records an unsafe worktree name %q; refusing cleanup", runID, name)
	}
	mainRoot, err := vcs.MainRepoRoot(rootOrDefault(root))
	if err != nil {
		return err
	}
	existing, err := workflowCleanupResolve(ctx, mainRoot, name)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := workflowCleanupRemove(ctx, mainRoot, name, workflowBranchPrefix); err != nil {
			var notFound vcs.WorktreeNotFoundError
			if !errors.As(err, &notFound) {
				return fmt.Errorf("remove workflow worktree: %w", err)
			}
		}
	}
	// Delete the wf/<name> branch with a pinned git context. Deleting a
	// missing ref is a no-op, so a partially cleaned run stays idempotent.
	branch := workflowBranchPrefix + name
	if _, err := workflowCleanupGit.Run(ctx, delivery.GitContext{Dir: mainRoot, GitDir: filepath.Join(mainRoot, ".git")}, "update-ref", "-d", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("delete workflow branch %q: %w", branch, err)
	}
	fmt.Fprintf(stdout, "run_id=%s cleaned worktree=%s branch=%s\n", runID, name, branch)
	return nil
}

func rootOrDefault(root string) string {
	if strings.TrimSpace(root) == "" {
		return "."
	}
	return root
}
