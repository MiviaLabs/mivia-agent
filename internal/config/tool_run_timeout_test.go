package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeToolRunTimeoutConfig writes a minimal config with the given [tools]
// tool_run_timeout_seconds line (empty string omits the key entirely).
func writeToolRunTimeoutConfig(t *testing.T, line string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mivia.toml")
	body := "[provider]\nname = \"deepseek\"\n\n[providers.deepseek]\nmodels = [{name=\"deepseek-v4-flash\", context_window_tokens=128000}]\n\n[chat]\nmax_tokens = 8192\n"
	if line != "" {
		body += "\n[tools]\n" + line + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestToolRunTimeoutSecDefaultsToUncapped pins the shipped default: an
// absent key resolves to 0, which the agent layer maps to the SDK's
// TimeoutNone (no registry-wide run cap). Uncapped is correct by
// default because the CLI dispatcher already arms every tool call's
// Capability.Timeout as a real deadline; the SDK backstop must never
// be tighter than those declared budgets unless the operator asks.
func TestToolRunTimeoutSecDefaultsToUncapped(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolRunTimeoutConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.ToolRunTimeoutSec != 0 {
		t.Fatalf("unset tool_run_timeout_seconds resolved to %d, want 0 (uncapped)", res.Tools.ToolRunTimeoutSec)
	}
}

// TestToolRunTimeoutSecFromTOML pins the parse path for a positive value.
func TestToolRunTimeoutSecFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolRunTimeoutConfig(t, "tool_run_timeout_seconds = 90")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.ToolRunTimeoutSec != 90 {
		t.Fatalf("tool_run_timeout_seconds = 90 resolved to %d", res.Tools.ToolRunTimeoutSec)
	}
}

// TestToolRunTimeoutSecNegativeNormalizesToUncapped pins the explicit
// negative-means-uncapped symmetry: a negative value normalizes to 0 so
// every consumer treats <= 0 uniformly as "no registry-wide cap",
// matching the max_tool_result_bytes precedent.
func TestToolRunTimeoutSecNegativeNormalizesToUncapped(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeToolRunTimeoutConfig(t, "tool_run_timeout_seconds = -5")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tools.ToolRunTimeoutSec != 0 {
		t.Fatalf("tool_run_timeout_seconds = -5 resolved to %d, want 0 (uncapped)", res.Tools.ToolRunTimeoutSec)
	}
}
