package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestWorkflowResumeRejectsMissingRecordedWriteWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	commit, err := vcs.CurrentCommit(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	recorded := workflowledger.RunSnapshot{
		RunID: "wfr-missing", BaseRef: "main", BaseCommit: commit,
		WorktreeName: "workflow-wfr-missing",
	}
	_, _, err = selectWorkflowWorkspace(t.Context(), root, recorded.RunID, true, &recorded)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("workspace resolution error = %v, want missing-worktree rejection", err)
	}
}
