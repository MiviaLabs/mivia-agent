package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestWorkflowForceResumeStopsBeforeClaimWorkWhenLockIsHeld(t *testing.T) {
	rootPath := t.TempDir()
	storePath := filepath.Join(rootPath, "context.db")
	configPath := filepath.Join(rootPath, "config.toml")
	config := "[provider]\nname = \"openrouter\"\n\n[providers.openrouter]\nbase_url = \"http://127.0.0.1\"\napi_key_env = \"WORKFLOW_LOCK_KEY\"\nmodels = [{ name = \"test/model\", context_window_tokens = 128000 }]\n\n[subagents]\nstore_backend = \"sqlite\"\nstore_path = \"" + tomlPathLiteral(storePath) + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKFLOW_LOCK_KEY", "test-key")
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-contended")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	err = runWorkflowWithIO([]string{"resume", "wfr-contended", "--force", "--workspace", rootPath, "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "workflow execution lock") {
		t.Fatalf("contended force resume error = %v, want execution lock failure", err)
	}
}

func TestWorkflowForceResumeRefusesFreshClaim(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-force-fresh"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClaimRun(ctx, runID, "live-owner"); err != nil {
		t.Fatal(err)
	}
	if err := claimWorkflowResumeHandoff(ctx, repo, runID, "resumer", true); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("forced resume of a fresh claim = %v, want active-claim refusal", err)
	}
}

func TestWorkflowExecutionLockUsesMainRepositoryRoot(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "fixture"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, rootPath)
	_, err := vcs.CreateWithPrefix(context.Background(), rootPath, "lock-linked", "HEAD", "test/")
	if err != nil {
		t.Fatal(err)
	}

	storePath := filepath.Join(rootPath, ".mivia", "sessions", "context.db")
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-shared")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-shared"); err == nil || !strings.Contains(err.Error(), "workflow execution lock") {
		t.Fatalf("linked worktree lock error = %v, want contention", err)
	}
}

func TestWorkflowExecutionLockFencesSameRun(t *testing.T) {
	rootPath := t.TempDir()
	storePath := filepath.Join(rootPath, "context.db")
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-active")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-active"); err == nil || !strings.Contains(err.Error(), "workflow execution lock") {
		t.Fatalf("second lock error = %v, want contention", err)
	}
	releaseOther, err := acquireWorkflowExecutionLock(storePath, "wfr-other")
	if err != nil {
		t.Fatalf("other run lock: %v", err)
	}
	releaseOther()
}

func TestWorkflowExecutionLockUsesSharedStoreAcrossWorkspaces(t *testing.T) {
	storeRoot := t.TempDir()
	storePath := filepath.Join(storeRoot, "context.db")
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-shared-store")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireWorkflowExecutionLock(storePath, "wfr-shared-store"); err == nil || !strings.Contains(err.Error(), "workflow execution lock") {
		t.Fatalf("shared store lock error = %v, want contention", err)
	}
}

func TestWorkflowExecutionLockRejectsHardLinkedStore(t *testing.T) {
	rootPath := t.TempDir()
	storePath := filepath.Join(rootPath, "context.db")
	aliasPath := filepath.Join(rootPath, "context-alias.db")
	if err := os.WriteFile(storePath, []byte("store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(storePath, aliasPath); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	if _, err := acquireWorkflowExecutionLock(aliasPath, "wfr-hard-link"); err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("hard-linked store error = %v", err)
	}
}

func TestWorkflowExecutionLockDoesNotDirtyCheckout(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "fixture"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignore := ".mivia/context.db\n.mivia/context.db-*\n.mivia/.mivia-workflow-locks/\n"
	if err := os.WriteFile(filepath.Join(rootPath, ".gitignore"), []byte(ignore), 0o600); err != nil {
		t.Fatal(err)
	}
	initWorkflowGitRepo(t, rootPath)
	storePath := workspace.ContextStorePath(rootPath)
	storeDir := filepath.Dir(storePath)
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireWorkflowExecutionLock(storePath, "wfr-clean")
	if err != nil {
		t.Fatal(err)
	}
	release()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = rootPath
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("git status = %q, error = %v", output, err)
	}
}
