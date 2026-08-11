package localengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestInvocationRunIDIsStableAndKeyScoped(t *testing.T) {
	first := agenttools.InvocationRunID("request-1")
	if first != agenttools.InvocationRunID("request-1") {
		t.Fatal("same invocation key produced different run IDs")
	}
	if first == agenttools.InvocationRunID("request-2") {
		t.Fatal("different invocation keys produced the same run ID")
	}
	if len(first) < len("wfr-inv-")+32 || first[:len("wfr-inv-")] != "wfr-inv-" {
		t.Fatalf("invocation run ID = %q, want wfr-inv- plus a digest", first)
	}
}

// TestInvocationKeyAdmissionWorktreeNameFitsLimit starts a run with an
// InvocationKey on a git workspace and pins that the recorded worktree name
// round-trips vcs.SanitizeName. A full-length digest made the derived name
// exceed vcs.MaxWorktreeNameLen, so SanitizeName rejected it and admission
// silently fell back to an unsanitised name.
func TestInvocationKeyAdmissionWorktreeNameFitsLimit(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	runGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")

	wfRoot := filepath.Join(root, ".mivia", "workflows")
	if err := os.MkdirAll(wfRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `version = 1
name = "inv-admit"
initial_step = "one"

[[steps]]
id = "one"
kind = "agent"
agent = "one"
on_failure = "failure"

[[transitions]]
from = "one"
to = "success"
[transitions.match]
status = "succeeded"
`
	if err := os.WriteFile(filepath.Join(wfRoot, "inv-admit.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := workflowledger.NewMemoryRepository()
	engine := &Engine{
		WorkspaceRoot: root,
		Repo:          repo,
		NewRunner: func() controller.AgentStepRunner {
			return &StaticStepRunner{Output: json.RawMessage(`{"ok":true}`)}
		},
	}
	result, err := engine.Start(context.Background(), agenttools.StartRequest{
		Workflow: "inv-admit", InvocationKey: "request-1",
	})
	if err != nil {
		t.Fatalf("Start with InvocationKey: %v", err)
	}
	run, err := repo.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun(%q): %v", result.RunID, err)
	}
	if run.WorktreeName == "" {
		t.Fatalf("recorded run %q has no worktree name", result.RunID)
	}
	got, err := vcs.SanitizeName(run.WorktreeName)
	if err != nil {
		t.Fatalf("SanitizeName(%q): %v", run.WorktreeName, err)
	}
	if got != run.WorktreeName {
		t.Fatalf("SanitizeName(%q) = %q, want the name unchanged", run.WorktreeName, got)
	}
	// Wait for the background controller (and the terminal on-disk trace) to
	// finish before the temp workspace is removed, so teardown does not race
	// the engine's final summary write.
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Wait(waitCtx, result.RunID); err != nil {
		t.Fatalf("Wait(%q): %v", result.RunID, err)
	}
}
