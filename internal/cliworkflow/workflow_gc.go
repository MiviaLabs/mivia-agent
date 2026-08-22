package cliworkflow

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// RunWorkflowCommandGC prunes workflow-ledger content rows ("sha256:"
// prefix) that no live run's events reference any longer - the content a
// deleted run left behind (mivia workflow delete strips a run's events to
// a tombstone but never touches the content table). Scoped to the
// workflow ledger's own ref prefix only: a coordinator/subagent/chat
// content row (a different prefix in the same table) is never a candidate.
func RunWorkflowCommandGC(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("workflow gc: unexpected argument %q", args[0])
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		workspaceRoot = "."
	}
	work, err := workspace.Open(workspaceRoot)
	if err != nil {
		return err
	}
	configPath = WorkflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return err
	}
	ApplyPrivacyPolicyFunc(res)
	ApplyWorkflowStoreRoot(res, work.Abs)
	store, _, closeFn, err := OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return err
	}
	defer closeFn()
	removed, err := store.PruneOrphanedContent(context.Background())
	if err != nil {
		return fmt.Errorf("workflow gc: %w", err)
	}
	fmt.Fprintf(stdout, "removed %d orphaned content row(s)\n", removed)
	return nil
}
