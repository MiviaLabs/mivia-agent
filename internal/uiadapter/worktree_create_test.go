package uiadapter_test

// StartInNewWorktree tests, split out of worktree_sessions_test.go to keep
// it under the go-structure soft cap.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

func TestCommandRunner_StartInNewWorktree_UsesLaunchingCheckoutCommit(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	mainCommit := gitOutput(t, mainDir, "rev-parse", "HEAD")
	launchDir := filepath.Join(filepath.Dir(mainDir), "launch")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "feature/launch", launchDir)
	runGit(t, launchDir, "commit", "--allow-empty", "-qm", "launch-only")
	launchCommit := gitOutput(t, launchDir, "rev-parse", "HEAD")
	if launchCommit == mainCommit {
		t.Fatalf("launch and main commits are equal: %s", launchCommit)
	}
	otherDir := t.TempDir()
	gitInitTempRepo(t, otherDir)

	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(launchDir)
	state := &cliagents.AgentSessionState{WorkspaceRoot: launchDir}
	runner := uiadapter.NewCommandRunner(sess, res, state)
	t.Cleanup(runner.Pool().CloseAll)
	// Changing CWD after construction to another repository must not change
	// the launch checkout used for creation or subsequent session binding.
	t.Chdir(otherDir)

	out := runner.StartInNewWorktree(context.Background(), "launch-base")
	if out.Err != "" {
		t.Fatalf("StartInNewWorktree: %s", out.Err)
	}
	createdDir := filepath.Join(workspace.WorktreesDir(mainDir), "launch-base")
	createdCommit := gitOutput(t, createdDir, "rev-parse", "HEAD")
	if createdCommit != launchCommit {
		t.Fatalf("created worktree commit = %s, want launch commit %s", createdCommit, launchCommit)
	}
	if !strings.HasPrefix(filepath.Clean(createdDir), filepath.Clean(workspace.WorktreesDir(mainDir))+string(filepath.Separator)) {
		t.Fatalf("created worktree path %q is outside main-root worktrees", createdDir)
	}
	principal, err := worktreeroute.Principal(mainDir)
	if err != nil {
		t.Fatalf("main-root principal: %v", err)
	}
	live, err := store.LiveWorktreeInstance(context.Background(), principal, "launch-base")
	if err != nil {
		t.Fatalf("shared-store worktree lookup: %v", err)
	}
	if filepath.Clean(live.CanonicalPath) != filepath.Clean(createdDir) {
		t.Fatalf("shared-store path = %q, want %q", live.CanonicalPath, createdDir)
	}
	if out.Conversation == nil {
		t.Fatal("successful creation did not install a conversation")
	}
	if runner.Pool().Session(out.Conversation.ID()) == nil {
		t.Fatal("successful creation left the new session unbound in the pool")
	}
}

func TestCommandRunner_StartInNewWorktree_InvalidNameWinsOnUnbornRepo(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	runGit(t, mainDir, "init", "-q")
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	t.Chdir(mainDir)

	out := runner.StartInNewWorktree(context.Background(), strings.Repeat("x", 300))
	if out.Err == "" || !strings.Contains(out.Err, "too long") {
		t.Fatalf("invalid-name error = %q, want name validation failure", out.Err)
	}
	if strings.Contains(out.Err, "resolve launch checkout commit") {
		t.Fatalf("invalid-name error was masked by commit resolution: %q", out.Err)
	}
}

func TestCommandRunner_StartInNewWorktree_DetachedLaunchUsesHEAD(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	launchDir := filepath.Join(filepath.Dir(mainDir), "detached-launch")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "feature/detached", launchDir)
	runGit(t, launchDir, "checkout", "--detach", "-q")
	runGit(t, launchDir, "commit", "--allow-empty", "-qm", "detached-launch")
	launchCommit := gitOutput(t, launchDir, "rev-parse", "HEAD")

	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	t.Chdir(launchDir)
	if out := runner.StartInNewWorktree(context.Background(), "detached-base"); out.Err != "" {
		t.Fatalf("StartInNewWorktree from detached HEAD: %s", out.Err)
	}
	createdDir := filepath.Join(workspace.WorktreesDir(mainDir), "detached-base")
	if got := gitOutput(t, createdDir, "rev-parse", "HEAD"); got != launchCommit {
		t.Fatalf("created worktree commit = %s, want detached HEAD %s", got, launchCommit)
	}
}

func TestCommandRunner_StartInNewWorktree_RetainsExistingManagedBranch(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	mainCommit := gitOutput(t, mainDir, "rev-parse", "HEAD")
	launchDir := filepath.Join(filepath.Dir(mainDir), "retained-launch")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "feature/retained-launch", launchDir)
	runGit(t, launchDir, "commit", "--allow-empty", "-qm", "retained-launch")
	launchCommit := gitOutput(t, launchDir, "rev-parse", "HEAD")
	runGit(t, mainDir, "branch", "mivia/retained-base", mainCommit)

	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	t.Chdir(launchDir)
	if out := runner.StartInNewWorktree(context.Background(), "retained-base"); out.Err != "" {
		t.Fatalf("StartInNewWorktree with retained branch: %s", out.Err)
	}
	createdDir := filepath.Join(workspace.WorktreesDir(mainDir), "retained-base")
	if got := gitOutput(t, createdDir, "rev-parse", "HEAD"); got != mainCommit {
		t.Fatalf("retained branch commit = %s, want original branch tip %s (launch commit %s)", got, mainCommit, launchCommit)
	}
}

func TestCommandRunner_StartInNewWorktree_CommitResolutionFailureDoesNotCreate(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	runGit(t, mainDir, "init", "-q")
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	runner := uiadapter.NewCommandRunner(sess, res, nil)
	t.Chdir(mainDir)

	out := runner.StartInNewWorktree(context.Background(), "no-commit")
	if out.Err == "" || !strings.Contains(out.Err, "resolve launch checkout commit") {
		t.Fatalf("commit-resolution failure = %q", out.Err)
	}
	if _, err := os.Stat(filepath.Join(workspace.WorktreesDir(mainDir), "no-commit")); !os.IsNotExist(err) {
		t.Fatalf("worktree exists after commit-resolution failure: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git -C %s %v: %v", dir, args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestCommandRunner_StartInNewWorktree_CreateFailureSurfacesError(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(mainDir)

	// Create once, then begin removal: a second create for the same name
	// hits the recovery-required error inside CreateManagedWorktreeForPool.
	if err := cliagents.CreateManagedWorktreeForPool(store, mainDir, "wtz"); err != nil {
		t.Fatalf("pre-create wtz: %v", err)
	}
	principal, _ := worktreeroute.Principal(mainDir)
	live, lerr := store.LiveWorktreeInstance(context.Background(), principal, "wtz")
	if lerr != nil {
		t.Fatalf("probe wtz: %v", lerr)
	}
	if err := store.BeginWorktreeDeletion(context.Background(), principal, live.Instance); err != nil {
		t.Fatalf("begin deletion: %v", err)
	}

	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	runner := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)
	out := runner.StartInNewWorktree(context.Background(), "wtz")
	if out.Err == "" || !strings.Contains(out.Err, "failed to create worktree") {
		t.Fatalf("create-failure arm = %q", out.Err)
	}
	if out.Conversation != nil {
		t.Error("failed creation must not install a conversation")
	}
}

func TestCommandRunner_StartInNewWorktree_GuardArms(t *testing.T) {
	res := &config.Resolved{ProviderName: "fake", Model: "m1"}

	// No active session.
	r := uiadapter.NewCommandRunner(nil, res, nil)
	if out := r.StartInNewWorktree(context.Background(), ""); !strings.Contains(out.Err, "no active session") {
		t.Errorf("nil-session guard = %q", out.Err)
	}

	// Nil pool.
	sess := chat.NewSession(res, &nullCompleter{})
	sess.SessionID = "s0"
	r2 := uiadapter.NewCommandRunnerWithPool(sess, nil, res, nil)
	if out := r2.StartInNewWorktree(context.Background(), ""); !strings.Contains(out.Err, "no session pool") {
		t.Errorf("nil-pool guard = %q", out.Err)
	}

	// Non-SQLite context store (plain session has none at all).
	pool := uiadapter.NewSessionPool(sess, res, nil, false)
	r3 := uiadapter.NewCommandRunnerWithPool(sess, pool, res, nil)
	if out := r3.StartInNewWorktree(context.Background(), ""); !strings.Contains(out.Err, "repository context store") {
		t.Errorf("store guard = %q", out.Err)
	}

	// Outside any git repository: Root("") fails.
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	sess4, _ := catalogSession(t, store, mainDir)
	sess4.UseTools = true
	sess4.Tools = toolRegistryAt(t, mainDir)
	r4 := uiadapter.NewCommandRunner(sess4, res, nil)
	t.Chdir(t.TempDir()) // no repo above cwd
	if out := r4.StartInNewWorktree(context.Background(), ""); !strings.Contains(out.Err, "resolve repository root") {
		t.Errorf("root guard = %q", out.Err)
	}
}

func TestCommandRunner_StartInNewWorktree_GeneratesNameAndStarts(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	out := runner.StartInNewWorktree(context.Background(), "")
	if out.Err != "" {
		t.Fatalf("StartInNewWorktree(\"\") errored: %s", out.Err)
	}
	if out.Conversation == nil || out.Conversation.ID() == "" {
		t.Fatal("no conversation installed")
	}
	if !strings.Contains(out.Notice, "Started new session in worktree wt-") {
		t.Errorf("notice = %q, want auto-generated wt- prefix", out.Notice)
	}
	if got := runner.Pool().Session(out.Conversation.ID()); got == nil {
		t.Fatal("new session not pooled")
	}
}

func TestCommandRunner_StartInNewWorktree_ExplicitNameSanitizes(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	out := runner.StartInNewWorktree(context.Background(), "My Feature!")
	if out.Err != "" {
		t.Fatalf("errored: %s", out.Err)
	}
	if !strings.Contains(out.Notice, "my-feature") {
		t.Errorf("notice = %q, want sanitized name", out.Notice)
	}
}

func TestCommandRunner_StartInNewWorktree_DuplicateSurfacesError(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	// Create once.
	out := runner.StartInNewWorktree(context.Background(), "dup-test")
	if out.Err != "" {
		t.Fatalf("first create errored: %s", out.Err)
	}
	// Create again with the same name: duplicate error.
	out = runner.StartInNewWorktree(context.Background(), "dup-test")
	if out.Err == "" || !strings.Contains(out.Err, "dup-test") {
		t.Fatalf("duplicate error = %q, want dup-test mention", out.Err)
	}
}

// TestCommandRunner_StartInNewWorktree_SanitizeErrorNamesTheInput pins
// error fidelity: an invalid typed name must surface ITS OWN failure
// (reason and input), not a downstream `failed to create worktree ""`
// after the error was discarded.
func TestCommandRunner_StartInNewWorktree_SanitizeErrorNamesTheInput(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	long := strings.Repeat("x", 300)
	out := runner.StartInNewWorktree(context.Background(), long)
	if out.Err == "" || !strings.Contains(out.Err, "too long") {
		t.Fatalf("sanitize failure lost its reason: %q", out.Err)
	}
	if strings.Contains(out.Err, `worktree ""`) {
		t.Fatalf("error reports the discarded empty name instead of the input: %q", out.Err)
	}
}

// TestCommandRunner_StartInNewWorktree_SameSecondNamesDoNotCollide pins
// the auto-name generator: two presses inside one wall-clock second must
// produce distinct worktrees, not a duplicate-name failure whose
// documented remedy (press again) regenerates the same colliding name.
func TestCommandRunner_StartInNewWorktree_SameSecondNamesDoNotCollide(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)
	sess.UseTools = true
	sess.Tools = toolRegistryAt(t, mainDir)
	t.Chdir(mainDir)

	runner := uiadapter.NewCommandRunner(sess, res, nil)
	first := runner.StartInNewWorktree(context.Background(), "")
	if first.Err != "" {
		t.Fatalf("first StartInNewWorktree: %s", first.Err)
	}
	second := runner.StartInNewWorktree(context.Background(), "")
	if second.Err != "" {
		t.Fatalf("second StartInNewWorktree in the same second: %s", second.Err)
	}
	if first.Notice == second.Notice {
		t.Fatalf("both presses produced the same worktree: %q", first.Notice)
	}
}

// TestCommandRunner_StartInNewWorktree_LaunchCheckoutDirErrorSurfaces
// pins StartInNewWorktree's own error wrap around r.launchCheckoutDir():
// a relative WorkspaceRoot whose filepath.Abs cannot resolve (the
// process's working directory removed out from under it) must surface
// as "resolve launch checkout: ...", not silently proceed with an
// unresolved directory.
func TestCommandRunner_StartInNewWorktree_LaunchCheckoutDirErrorSurfaces(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureReopenable(t)
	store, mainDir := fx.Store, fx.MainDir
	gitInitTempRepo(t, mainDir)

	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess, _ := catalogSession(t, store, mainDir)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gone := t.TempDir()
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.RemoveAll(gone); err != nil {
		t.Skipf("platform will not remove its own working directory: %v", err)
	}
	if _, probeErr := filepath.Abs("relative-root"); probeErr == nil {
		t.Skip("platform still resolves an absolute path from a removed working directory")
	}

	state := &cliagents.AgentSessionState{WorkspaceRoot: "relative-root"}
	runner := uiadapter.NewCommandRunner(sess, res, state)
	t.Cleanup(runner.Pool().CloseAll)

	out := runner.StartInNewWorktree(context.Background(), "some-name")
	if !strings.Contains(out.Err, "resolve launch checkout") {
		t.Fatalf("StartInNewWorktree error = %q, want the resolve-launch-checkout wrap", out.Err)
	}
}
