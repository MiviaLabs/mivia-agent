package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
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

// TestSelectWorkflowWorkspaceInvocationRunIDFitsWorktreeNameLimit pins that a
// write-capable admission with an invocation run ID succeeds on a git repo and
// records a worktree name that round-trips vcs.SanitizeName. A full-length
// digest made "workflow-"+runID exceed vcs.MaxWorktreeNameLen, and
// SanitizeName rejected it.
func TestSelectWorkflowWorkspaceInvocationRunIDFitsWorktreeNameLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, root)
	identity, cleanup, err := selectWorkflowWorkspace(t.Context(), root, agenttools.InvocationRunID("request-1"), true, nil)
	if err != nil {
		t.Fatalf("selectWorkflowWorkspace: %v", err)
	}
	defer cleanup()
	if identity.WorktreeName == "" {
		t.Fatal("write-capable admission recorded no worktree name")
	}
	sanitized, err := vcs.SanitizeName(identity.WorktreeName)
	if err != nil || sanitized != identity.WorktreeName {
		t.Fatalf("recorded worktree name %q does not round-trip SanitizeName (%q, %v)", identity.WorktreeName, sanitized, err)
	}
}
