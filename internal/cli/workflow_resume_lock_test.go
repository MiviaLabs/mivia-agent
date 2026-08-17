package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestSessionLaunchResumeReleasesExecutionLockBeforeDoneCloses is a regression
// test: launchResume used to close done and only then run finishExec (via
// closeGuarded -> closeFn), releasing the execution flock AFTER done closed.
// A waiter woken by done closing (e.g. Deliver, workflow_tool_engine_ops.go)
// could then race the still-held flock. finishExec must complete before done
// closes, matching launchStartedWorkflow's established ordering.
func TestSessionLaunchResumeReleasesExecutionLockBeforeDoneCloses(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	t.Cleanup(func() { _ = repo.Close() })
	const runID = "wfr-resume-lock-order"
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending}, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	linear := &controller.LinearController{Holder: "session-resumer"}
	if err := repo.ClaimRun(ctx, runID, linear.Holder); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	finishRun := make(chan struct{})
	originalRun := workflowResumeRun
	workflowResumeRun = func(context.Context, workflowControllerBuild) (workflowledger.RunSnapshot, error) {
		close(started)
		<-finishRun
		return workflowledger.RunSnapshot{}, errors.New("injected run failure")
	}
	t.Cleanup(func() { workflowResumeRun = originalRun })

	finishExecCalled := make(chan struct{})
	allowFinish := make(chan struct{})
	closed := make(chan struct{})

	engine := newSessionWorkflowEngine(".", "")
	prepared := resumePrepared{
		runID:    runID,
		workflow: "test",
		built: workflowControllerBuild{
			Controller: linear,
			Dispatcher: workflowTestDispatcher{},
		},
		repo: repo,
		finishExec: func() {
			close(finishExecCalled)
			<-allowFinish
		},
		closeFn: func() { close(closed) },
	}
	if _, err := engine.launchResume(ctx, prepared); err != nil {
		t.Fatalf("launchResume() error = %v", err)
	}
	engine.mu.Lock()
	active := engine.active[runID]
	engine.mu.Unlock()
	if active == nil {
		t.Fatal("launchResume did not register an active run")
	}

	<-started
	close(finishRun)

	<-finishExecCalled
	select {
	case <-active.done:
		t.Fatal("done closed before finishExec (the execution flock release) completed")
	default:
	}

	close(allowFinish)
	<-active.done
	<-closed
}

func TestWorkflowForceResumeStopsBeforeClaimWorkWhenLockIsHeld(t *testing.T) {
	shortenWorkflowResolutionLockWait(t)
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

// TestWorkflowResumeWithoutForceRefusesFreshClaim: without --force, a fresh
// claim within its lease belongs to a live executor and the resume handoff is
// refused (the lease-bounded takeover path).
func TestWorkflowResumeWithoutForceRefusesFreshClaim(t *testing.T) {
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
	if err := claimWorkflowResumeHandoff(ctx, repo, runID, "resumer", false); err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("resume without force of a fresh claim = %v, want active-claim refusal", err)
	}
}

// TestWorkflowForceResumeTakesOverFreshClaim: --force is the operator override
// for crash recovery - a fresh claim left by a killed executor is taken over
// unconditionally so resume can recover the run immediately. Regression: the
// branch dropped the override and bricked recovery until lease expiry while the
// refusal message still advertised --force as the remedy.
func TestWorkflowForceResumeTakesOverFreshClaim(t *testing.T) {
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
	if err := claimWorkflowResumeHandoff(ctx, repo, runID, "resumer", true); err != nil {
		t.Fatalf("forced resume of a fresh claim = %v, want takeover", err)
	}
	// The claim now belongs to the resumer; a third party is still fenced.
	if err := repo.ClaimRun(ctx, runID, "third"); !errors.Is(err, workflowledger.ErrClaimHeld) {
		t.Fatalf("claim after forced takeover = %v, want ErrClaimHeld", err)
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

// TestLockBusyErrorNamesWorkflowExecution pins that a held workflow execution
// lock surfaces a workflow-specific message, not the low-level Git exclude lock
// wording borrowed from the shared file-lock primitive.
func TestLockBusyErrorNamesWorkflowExecution(t *testing.T) {
	shortenWorkflowResolutionLockWait(t)
	rootPath := t.TempDir()
	storePath := filepath.Join(rootPath, "context.db")
	const runID = "wfr-busy-name"

	// Remove unrelated hook-session behavior so the only failure path under test
	// is the execution-lock acquire.
	prevHooks := workflowExecutionHooks
	workflowExecutionHooks = func(string, bool, bool) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { workflowExecutionHooks = prevHooks })

	acquired := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		release, err := acquireWorkflowExecutionLock(storePath, runID)
		if err != nil {
			t.Errorf("holder acquire: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		<-done
		release()
	}()
	<-acquired

	assertLockErrorNamesWorkflowExecution := func(surface string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s succeeded while lock held; want workflow execution lock error", surface)
		}
		if !strings.Contains(err.Error(), "workflow execution") {
			t.Fatalf("%s error = %v; want message containing \"workflow execution\"", surface, err)
		}
		if strings.Contains(err.Error(), "Git exclude") {
			t.Fatalf("%s error = %v; message must not contain \"Git exclude\"", surface, err)
		}
	}

	_, err := beginWorkflowExecution(rootPath, storePath, runID)
	assertLockErrorNamesWorkflowExecution("beginWorkflowExecution", err)

	_, err = beginWorkflowExecutionBounded(context.Background(), rootPath, storePath, runID, testWorkflowResolutionLockWait)
	assertLockErrorNamesWorkflowExecution("beginWorkflowExecutionBounded", err)

	close(done)
	<-finished
}
