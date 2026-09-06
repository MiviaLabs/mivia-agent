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
