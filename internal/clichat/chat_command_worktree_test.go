package clichat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestRunConfiguredChatRestartsWithCreatedWorktree(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	original := runConfiguredChatOnceImpl
	t.Cleanup(func() { runConfiguredChatOnceImpl = original })
	originalLoad := loadConfigForRestart
	t.Cleanup(func() { loadConfigForRestart = originalLoad })

	worktree := t.TempDir()
	// runConfiguredChat chdirs into the restart worktree and leaves it there;
	// the cwd restore must therefore run BEFORE the temp dir is removed,
	// because Windows refuses to remove a directory that is a process's cwd.
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	before := &config.Resolved{StorePath: "before"}
	after := &config.Resolved{StorePath: "after"}
	loads := 0
	wantConfig := filepath.Join(originalDir, "config", "mivia.toml")
	loadConfigForRestart = func(opts config.LoadOptions) (*config.Resolved, error) {
		loads++
		if opts.ConfigPath != wantConfig {
			t.Fatalf("restart config path = %q, want %q", opts.ConfigPath, wantConfig)
		}
		return after, nil
	}
	calls := 0
	runConfiguredChatOnceImpl = func(invocation chatInvocation, res *config.Resolved) error {
		calls++
		if calls == 1 {
			if res != before {
				t.Fatal("first session did not use the original config")
			}
			if invocation.workspacePath != "" {
				t.Fatalf("first workspace = %q, want empty", invocation.workspacePath)
			}
			return stubWorkspaceRestart{dir: worktree}
		}
		if invocation.workspacePath != worktree {
			t.Fatalf("restart workspace = %q, want %q", invocation.workspacePath, worktree)
		}
		if res != after {
			t.Fatal("restarted session did not reload configuration")
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if cwd != worktree {
			t.Fatalf("cwd = %q, want restarted workspace %q", cwd, worktree)
		}
		return nil
	}

	if err := runConfiguredChat(chatInvocation{configPath: "config/mivia.toml"}, before); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if loads != 1 {
		t.Fatalf("config reloads = %d, want 1", loads)
	}
}

func TestRunConfiguredChatCarriesResumeSessionAcrossRestart(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	original := runConfiguredChatOnceImpl
	t.Cleanup(func() { runConfiguredChatOnceImpl = original })
	originalLoad := loadConfigForRestart
	t.Cleanup(func() { loadConfigForRestart = originalLoad })

	worktree := t.TempDir()
	// Same cwd-restore ordering as TestRunConfiguredChatRestartsWithCreatedWorktree.
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	loadConfigForRestart = func(config.LoadOptions) (*config.Resolved, error) {
		return &config.Resolved{}, nil
	}
	calls := 0
	runConfiguredChatOnceImpl = func(invocation chatInvocation, _ *config.Resolved) error {
		calls++
		if calls == 1 {
			return stubWorkspaceRestart{dir: worktree, resumeSessionName: "root-session"}
		}
		if invocation.resumeSessionName != "root-session" {
			t.Fatalf("resume session = %q, want root-session", invocation.resumeSessionName)
		}
		return nil
	}

	// workspacePath must NOT be the ambient cwd: chatRepositoryRoot resolves
	// to the real main repository root (vcs.MainRepoRoot), so an empty
	// workspacePath here would read that repo's real, uncommitted
	// .mivia/mivia.toml - environment state this test has no business
	// depending on. A non-repo tempdir makes chatRepositoryRoot fail cleanly
	// and skips repository-session-store binding, which this test isn't
	// exercising anyway.
	if err := runConfiguredChat(chatInvocation{workspacePath: t.TempDir()}, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositorySessionStorePathUsesMainRepositoryConfig(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	configDir := filepath.Join(repoRoot, ".mivia")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configText := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "root.db"
`
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := vcs.Create(context.Background(), repoRoot, "config-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite", StorePath: "worktree.db"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repoRoot, "root.db"); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRepositorySessionStorePathUsesUserConfigFromMainRepository(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "user-config-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".mivia")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configText := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "user-root.db"
`
	if err := os.WriteFile(filepath.Join(configDir, "mivia.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite", StorePath: "worktree.db"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repoRoot, "user-root.db"); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRepositorySessionStorePathUsesEnvironmentConfig(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	configPath := filepath.Join(t.TempDir(), "mivia.toml")
	configText := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "environment.db"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIVIA_CONFIG", configPath)

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repoRoot, "environment.db"); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRepositorySessionStorePathUsesRelativeEnvironmentConfig(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	configPath := filepath.Join(repoRoot, "environment.toml")
	configText := `[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[subagents]
store_backend = "sqlite"
store_path = "environment.db"
`
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	t.Setenv("MIVIA_CONFIG", "environment.toml")

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repoRoot, "environment.db"); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRepositorySessionStorePathDefaultsToMainRepository(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "no-root-config-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree.Path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{Subagents: config.SubagentConfig{StoreBackend: "sqlite", StorePath: "worktree.db"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := workspace.GlobalContextStorePath(repoRoot); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRepositorySessionStorePathIgnoresWorktreeConfig(t *testing.T) {
	repoRoot := newWorktreeCommandRepo(t)
	worktree, err := vcs.Create(context.Background(), repoRoot, "worktree-config-target", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeWorktreeStoreConfig(t, worktree.Path, "worktree.db")
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := repositorySessionStorePath(repoRoot, chatInvocation{}, &config.Resolved{StorePathSet: true, Subagents: config.SubagentConfig{StorePath: "worktree.db"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := workspace.GlobalContextStorePath(repoRoot); path != want {
		t.Fatalf("repository session store = %q, want %q", path, want)
	}
}

func TestRunConfiguredChatKeepsRelativeEnvConfigOnRestart(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	t.Setenv("MIVIA_CONFIG", "config/mivia.toml")
	original := runConfiguredChatOnceImpl
	t.Cleanup(func() { runConfiguredChatOnceImpl = original })
	originalLoad := loadConfigForRestart
	t.Cleanup(func() { loadConfigForRestart = originalLoad })

	worktree := t.TempDir()
	// Restore the cwd before the temp dir is removed (see
	// TestRunConfiguredChatRestartsWithCreatedWorktree for the ordering rule).
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	wantConfig := filepath.Join(originalDir, "config", "mivia.toml")
	loadConfigForRestart = func(opts config.LoadOptions) (*config.Resolved, error) {
		if opts.ConfigPath != wantConfig {
			t.Fatalf("restart config path = %q, want %q", opts.ConfigPath, wantConfig)
		}
		return &config.Resolved{}, nil
	}
	calls := 0
	runConfiguredChatOnceImpl = func(_ chatInvocation, _ *config.Resolved) error {
		calls++
		if calls == 1 {
			return stubWorkspaceRestart{dir: worktree}
		}
		return nil
	}

	if err := runConfiguredChat(chatInvocation{}, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
}
