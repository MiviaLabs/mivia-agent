package uiadapter_test

// StartInNewWorktree tests, split out of worktree_sessions_test.go to keep
// it under the go-structure soft cap.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/worktreeroute"
)

func TestCommandRunner_StartInNewWorktree_CreateFailureSurfacesError(t *testing.T) {
	stubWorkflowWiring(t)
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
	fx := worktreeCatalogFixtureNoClose(t)
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
