package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testWorkspaceProviderTOML builds a minimal provider-declaring workspace
// config without embedding TOML quoting in a Go string literal.
func testWorkspaceProviderTOML() string {
	return strings.Join([]string{
		"[provider]",
		"name = " + strconv.Quote("deepseek"),
		"",
		"[providers.deepseek]",
		"models = [{ name = " + strconv.Quote("deepseek-chat") + ", context_window_tokens = 131072 }]",
		"",
	}, "\n")
}

// TestWorkspaceConfigWithoutProviderBootstrapsFirstTime pins the
// first-time-user fix: a workspace .mivia/mivia.toml that carries only
// workspace concerns (no [provider]/[providers]) must NOT shadow the user
// config and hard-fail startup with "[providers.openrouter]: models must be
// non-empty". Instead, with AutoBootstrapUserConfig set (mivia chat), the
// user config is bootstrapped and becomes the base; the provider-less
// workspace file keeps applying through the overlay path.
func TestWorkspaceConfigWithoutProviderBootstrapsFirstTime(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	ws := t.TempDir()
	wsDir := filepath.Join(ws, ".mivia")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Provider-less workspace config: verifiers-style workspace settings only.
	wsCfg := "[workflows]\n"
	if err := os.WriteFile(filepath.Join(wsDir, "mivia.toml"), []byte(wsCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the workspace the cwd so its config is the first candidate.
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	res, err := Load(LoadOptions{WorkspaceRoot: ws, AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "openrouter" {
		t.Fatalf("ProviderName = %q, want openrouter (bootstrapped)", res.ProviderName)
	}
	if res.ConfigPath != UserConfigPath() {
		t.Fatalf("ConfigPath = %q, want bootstrapped user config %q", res.ConfigPath, UserConfigPath())
	}
	if _, err := os.Stat(UserConfigPath()); err != nil {
		t.Fatalf("user config not bootstrapped: %v", err)
	}
}

// TestBootstrapNeverOverwritesProviderlessUserConfig pins the data-loss
// guard: an existing ~/.mivia/mivia.toml that declares no provider (only
// approvals/chat-style settings) must never be replaced by the bootstrapped
// default template. firstProviderCandidate's provider filter made this state
// report "nothing exists", which sent loadFile into autoBootstrapUserConfig -
// a bare os.WriteFile over the user's file, with no backup and no error.
// The contract is bootstrap.go's own documented invariant: the write only
// happens when the user config does not exist; an existing provider-less
// user config keeps pre-filter behavior (found=true, the provider error
// surfaces from resolveProvider).
func TestBootstrapNeverOverwritesProviderlessUserConfig(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	userCfg := "[approvals]\ndefault_mode = \"once\"\n\n[chat]\nstream_idle = 30\n"
	userPath := UserConfigPath()
	if userPath == "" {
		t.Fatal("UserConfigPath empty; HOME must resolve in this test")
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(LoadOptions{AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err == nil {
		t.Fatalf("expected the provider-resolution error for an existing provider-less user config")
	}
	// The guard must not swallow the diagnosis: the caller has to see the
	// actionable provider error, not "already exists".
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("bootstrap guard masked the real error: %v", err)
	}
	if !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("want the resolveProvider error, got: %v", err)
	}
	data, readErr := os.ReadFile(userPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != userCfg {
		t.Fatalf("existing user config was overwritten:\n got %q\nwant %q", data, userCfg)
	}
}

// TestBootstrapNeverOverwritesCorruptUserConfig pins the corrupt-file
// variant: a user config that fails to decode is treated as provider-less
// by the candidate scan, but the scan's "" return must not turn into an
// overwrite - the parse error has to surface instead.
func TestBootstrapNeverOverwritesCorruptUserConfig(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	bad := "[approvals\ndefault_mode = \"once\"\n"
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(LoadOptions{AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err == nil {
		t.Fatalf("expected the user config's parse error to surface")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("bootstrap guard masked the parse error: %v", err)
	}
	if !strings.Contains(err.Error(), userPath) {
		t.Fatalf("want a parse error naming %s, got: %v", userPath, err)
	}
	data, readErr := os.ReadFile(userPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != bad {
		t.Fatalf("corrupt user config was overwritten:\n got %q\nwant %q", data, bad)
	}
}

// TestWorkspaceConfigWithProviderStillWins confirms a workspace config that
// DOES declare a provider remains the base config - the filter only skips
// provider-less files.
func TestWorkspaceConfigWithProviderStillWins(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	ws := t.TempDir()
	wsDir := filepath.Join(ws, ".mivia")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wsCfg := testWorkspaceProviderTOML()
	wsPath := filepath.Join(wsDir, "mivia.toml")
	if err := os.WriteFile(wsPath, []byte(wsCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	res, err := Load(LoadOptions{WorkspaceRoot: ws, AllowMissingConfig: true, AutoBootstrapUserConfig: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.ProviderName != "deepseek" {
		t.Fatalf("ProviderName = %q, want deepseek", res.ProviderName)
	}
	if !strings.HasSuffix(res.ConfigPath, filepath.Join(".mivia", "mivia.toml")) ||
		!filepath.IsAbs(res.ConfigPath) {
		t.Fatalf("ConfigPath = %q, want the workspace config", res.ConfigPath)
	}
	if _, err := os.Stat(UserConfigPath()); err == nil {
		t.Fatalf("user config should not have been bootstrapped")
	}
}

// TestNoBootstrapNonChatCallerKeepsLegacyError pins the non-bootstrapping
// caller contract: without AutoBootstrapUserConfig, a provider-less first
// candidate is still returned as the base (found=true) and the provider
// resolution error surfaces unchanged - internal/read-only Load callers see
// today's behavior.
func TestNoBootstrapNonChatCallerKeepsLegacyError(t *testing.T) {
	isolateHomeAndConfigEnv(t)
	ws := t.TempDir()
	wsDir := filepath.Join(ws, ".mivia")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "mivia.toml"), []byte("[workflows]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	_, err = Load(LoadOptions{WorkspaceRoot: ws})
	if err == nil {
		t.Fatalf("expected the legacy provider error without AutoBootstrapUserConfig")
	}
	if !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFirstProviderCandidateSkipsBlankConfigEnv pins that a blank
// $MIVIA_CONFIG never becomes a candidate: the search must fall through to
// the user config that actually declares the provider, instead of stopping on
// a whitespace-only path.
func TestFirstProviderCandidateSkipsBlankConfigEnv(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	t.Setenv("MIVIA_CONFIG", "   ")
	userPath := writeUserConfig(t, home, testWorkspaceProviderTOML())

	if got := firstProviderCandidate(true); got != userPath {
		t.Fatalf("firstProviderCandidate = %q, want the user config %q", got, userPath)
	}
	if got := firstProviderCandidate(false); got != userPath {
		t.Fatalf("firstProviderCandidate without bootstrap = %q, want %q", got, userPath)
	}
}

// TestFirstProviderCandidateSkipsUnreadableCandidate pins the read-failure
// branch: a candidate that exists but cannot be read is treated as
// provider-less, so it never shadows a later, readable config that does
// declare a provider.
func TestFirstProviderCandidateSkipsUnreadableCandidate(t *testing.T) {
	home := isolateHomeAndConfigEnv(t)
	unreadable := filepath.Join(t.TempDir(), "unreadable.toml")
	if err := os.WriteFile(unreadable, []byte(testWorkspaceProviderTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	if _, err := os.ReadFile(unreadable); err == nil {
		t.Fatalf("read of %s unexpectedly succeeded; the test cannot force a read failure", unreadable)
	}
	t.Setenv("MIVIA_CONFIG", unreadable)

	// With no other candidate, the unreadable file still counts as the first
	// EXISTING candidate, so the non-bootstrap caller keeps its legacy return
	// and the bootstrap caller is free to write a fresh user config.
	if got := firstProviderCandidate(false); got != unreadable {
		t.Fatalf("firstProviderCandidate without bootstrap = %q, want the first existing candidate %q", got, unreadable)
	}
	if got := firstProviderCandidate(true); got != "" {
		t.Fatalf("firstProviderCandidate = %q, want empty so bootstrap can run", got)
	}

	// A readable user config that declares a provider now wins: the
	// unreadable candidate must never shadow it.
	userPath := writeUserConfig(t, home, testWorkspaceProviderTOML())
	if got := firstProviderCandidate(true); got != userPath {
		t.Fatalf("firstProviderCandidate = %q, want the readable user config %q", got, userPath)
	}
	if got := firstProviderCandidate(false); got != userPath {
		t.Fatalf("firstProviderCandidate without bootstrap = %q, want %q", got, userPath)
	}
}
