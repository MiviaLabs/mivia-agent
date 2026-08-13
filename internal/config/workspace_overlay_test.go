package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// baseConfigTOML is a minimal but complete config - a provider/model catalog
// plus an [subagents] store_path a workspace overlay test can assert either
// survives (no overlay override) or gets replaced (overlay override).
func baseConfigTOML(envPath, storePath string) string {
	return "env_file = \"" + filepath.ToSlash(envPath) + "\"\n\n" +
		"[provider]\nname = \"deepseek\"\n\n" +
		"[providers.deepseek]\nmodels = [{ name = \"deepseek-v4-pro\", context_window_tokens = 128000 }]\n\n" +
		"[subagents]\nstore_backend = \"sqlite\"\nstore_path = \"" + filepath.ToSlash(storePath) + "\"\n"
}

// TestLoadLayersWorkspaceConfigOverExplicitConfigPath pins the fix for the
// bug where mivia-agent-desktop pins an explicit ConfigPath (its own
// resolve_user_config_path, a user-level provider catalog) for every spawned
// thread, which used to make loadFile ignore the picked project's own
// WorkspaceRoot/.mivia/mivia.toml entirely - including a [subagents]
// store_path override redirecting durable storage (chat sessions, workflow
// run history) elsewhere. The interactive TUI (no explicit ConfigPath, cwd
// search finds the workspace file directly) had always honored it; a caller
// pinning ConfigPath read and wrote a completely different SQLite file and
// saw "no sessions"/"no workflow runs" for a project with plenty of both.
func TestLoadLayersWorkspaceConfigOverExplicitConfigPath(t *testing.T) {
	userDir := t.TempDir()
	userEnv := filepath.Join(userDir, ".env")
	userStore := filepath.Join(userDir, "user-store.db")
	userConfig := filepath.Join(userDir, "mivia.toml")
	if err := os.WriteFile(userConfig, []byte(baseConfigTOML(userEnv, userStore)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userEnv, []byte("DEEPSEEK_API_KEY=user-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaceRoot := t.TempDir()
	workspaceStore := filepath.Join(workspaceRoot, "workspace-store.db")
	workspaceConfigPath := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(workspaceConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceTOML := "[subagents]\nstore_backend = \"sqlite\"\nstore_path = \"" + filepath.ToSlash(workspaceStore) + "\"\n"
	if err := os.WriteFile(workspaceConfigPath, []byte(workspaceTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Load(LoadOptions{ConfigPath: userConfig, WorkspaceRoot: workspaceRoot})
	if err != nil {
		t.Fatal(err)
	}

	// The workspace's own store_path wins - the whole point of the fix.
	if res.Subagents.StorePath != workspaceStore {
		t.Fatalf("Subagents.StorePath = %q, want workspace override %q", res.Subagents.StorePath, workspaceStore)
	}
	// The base (user) config's provider/API key wiring survives - the
	// workspace overlay only defines [subagents], so everything else must
	// still come from the base file, not be wiped by the overlay decode.
	if res.ProviderName != "deepseek" || !res.APIKeySet || res.APIKey != "user-key" {
		t.Fatalf("base provider config not preserved: provider=%s apiKeySet=%v apiKey=%s", res.ProviderName, res.APIKeySet, res.APIKey)
	}
}

// TestLoadWithNoWorkspaceOverlayUnaffected pins that a WorkspaceRoot with no
// .mivia/mivia.toml of its own (or none given at all) behaves exactly as
// before this fix - no error, no unexpected override.
func TestLoadWithNoWorkspaceOverlayUnaffected(t *testing.T) {
	userDir := t.TempDir()
	userEnv := filepath.Join(userDir, ".env")
	userStore := filepath.Join(userDir, "user-store.db")
	userConfig := filepath.Join(userDir, "mivia.toml")
	if err := os.WriteFile(userConfig, []byte(baseConfigTOML(userEnv, userStore)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userEnv, []byte("DEEPSEEK_API_KEY=user-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	workspaceRoot := t.TempDir() // no .mivia/mivia.toml under here

	res, err := Load(LoadOptions{ConfigPath: userConfig, WorkspaceRoot: workspaceRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.StorePath != userStore {
		t.Fatalf("Subagents.StorePath = %q, want unchanged base %q", res.Subagents.StorePath, userStore)
	}
}

// TestLoadSkipsOverlayWhenWorkspaceFileIsAlreadyTheBase covers the ordinary
// interactive-CLI case: no explicit --config/MIVIA_CONFIG, so loadFile's own
// DefaultConfigCandidates search already resolves the workspace's own
// .mivia/mivia.toml as the base file - it must load exactly once, not be
// re-read as its own overlay.
func TestLoadSkipsOverlayWhenWorkspaceFileIsAlreadyTheBase(t *testing.T) {
	workspaceRoot := t.TempDir()
	env := filepath.Join(workspaceRoot, ".env")
	store := filepath.Join(workspaceRoot, "store.db")
	workspaceConfigPath := workspace.NamespacePath(workspaceRoot, "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(workspaceConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceConfigPath, []byte(baseConfigTOML(env, store)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("DEEPSEEK_API_KEY=workspace-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Load(LoadOptions{ConfigPath: workspaceConfigPath, WorkspaceRoot: workspaceRoot})
	if err != nil {
		t.Fatal(err)
	}
	if res.Subagents.StorePath != store || res.ProviderName != "deepseek" {
		t.Fatalf("unexpected result: %+v", res)
	}
}
