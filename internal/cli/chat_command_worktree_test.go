package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestRunConfiguredChatRestartsWithCreatedWorktree(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })
	original := runConfiguredChatOnceImpl
	t.Cleanup(func() { runConfiguredChatOnceImpl = original })
	originalLoad := loadConfigForRestart
	t.Cleanup(func() { loadConfigForRestart = originalLoad })

	worktree := t.TempDir()
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
			return &workspaceRestart{dir: worktree}
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
			return &workspaceRestart{dir: worktree}
		}
		return nil
	}

	if err := runConfiguredChat(chatInvocation{}, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
}
