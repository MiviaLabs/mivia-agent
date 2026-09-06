package clichat

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// quoteTOML avoids embedding TOML quoting inside Go string literals.
func quoteTOML(s string) string { return strconv.Quote(s) }

// isolateChatHome points HOME at a fresh temp dir and clears $MIVIA_CONFIG so
// repositoryConfigPath's candidate search only sees what the test writes.
func isolateChatHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MIVIA_CONFIG", "")
	return home
}

func writeRepoConfig(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mivia.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRepositorySessionStorePathProviderlessRepoConfig pins the chat-startup
// fix: a repository .mivia/mivia.toml with no [providers] section must not
// fail store-path resolution ("resolve repository session store:
// [providers.openrouter]: models must be non-empty") when the user config
// carries the provider. Store-path resolution only reads [subagents] and must
// not inherit provider requirements.
func TestRepositorySessionStorePathProviderlessRepoConfig(t *testing.T) {
	home := isolateChatHome(t)
	root := t.TempDir()
	writeRepoConfig(t, root, "[workflows]\n")
	// User config declares the provider the repo config deliberately omits.
	userDir := filepath.Join(home, ".mivia")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	userCfg := strings.Join([]string{
		"[provider]",
		"name = " + quoteTOML("deepseek"),
		"",
		"[providers.deepseek]",
		"models = [{ name = " + quoteTOML("deepseek-chat") + ", context_window_tokens = 131072 }]",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(userDir, "mivia.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := repositorySessionStorePath(root, chatInvocation{}, nil)
	if err != nil {
		t.Fatalf("repositorySessionStorePath: %v", err)
	}
	if want := workspace.GlobalContextStorePath(root); got != want {
		t.Fatalf("store path = %q, want global %q", got, want)
	}
}

// TestRepositorySessionStorePathReadsExplicitStorePath confirms the raw
// reader still resolves an explicitly-set [subagents] store_path exactly as
// the previous full-Load implementation did.
func TestRepositorySessionStorePathReadsExplicitStorePath(t *testing.T) {
	isolateChatHome(t)
	root := t.TempDir()
	writeRepoConfig(t, root, strings.Join([]string{
		"[subagents]",
		"store_path = " + quoteTOML("stores/sessions"),
		"",
	}, "\n"))

	got, err := repositorySessionStorePath(root, chatInvocation{}, nil)
	if err != nil {
		t.Fatalf("repositorySessionStorePath: %v", err)
	}
	want := filepath.Join(root, "stores", "sessions")
	if got != want {
		t.Fatalf("store path = %q, want %q", got, want)
	}
}
