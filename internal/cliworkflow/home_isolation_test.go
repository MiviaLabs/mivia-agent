package cliworkflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/testenv"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// writeMinimalWorkflowConfig writes a config that names a provider and model
// and nothing else - in particular no [mcp] table, so anything MCP-shaped in
// the resolved result came from outside this file.
func writeMinimalWorkflowConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_HERMETIC_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// digestFreeSnapshot is the snapshot shape every fixture in this package
// writes: a ledger row that pins no MCP configuration.
func digestFreeSnapshot() workflowledger.Snapshot {
	return workflowledger.Snapshot{}
}

// TestPackageRunsUnderAnIsolatedHome pins this package's hermeticity.
//
// config.Load merges the USER-level MCP table (LoadTrustedMCPConfig reads
// config.UserConfigPath) into every resolved configuration, even when the
// caller passes an explicit ConfigPath. So without an isolated home, a
// developer whose own ~/.mivia/mivia.toml enables MCP servers gives every
// fixture here res.MCP.Enabled = true, while the ledger rows those fixtures
// write carry no MCPConfigDigest. validateWorkflowMCPConfigDigest then
// correctly refuses the resume - "snapshot does not pin the enabled MCP
// configuration" - and 19 tests fail for a reason that has nothing to do with
// the code under test.
//
// The check was right and the fixtures were right. Only the environment was
// shared. Asserting isolation directly is what makes that unrepeatable: it
// fails on the machine that leaks, not merely on the one test that noticed.
func TestPackageRunsUnderAnIsolatedHome(t *testing.T) {
	if !testenv.HomeIsolated() {
		t.Fatalf("package home is not isolated: TestMain must call testenv.IsolateHome before m.Run; user config path = %q", config.UserConfigPath())
	}
}

// TestAmbientMCPConfigDoesNotReachResolvedConfig proves the isolation reaches
// the exact seam that failed: the user-level [mcp] table config.Load merges
// behind an explicit ConfigPath. A fixture-built config must resolve with MCP
// off, otherwise every digest-free snapshot in this package is refused.
func TestAmbientMCPConfigDoesNotReachResolvedConfig(t *testing.T) {
	res, err := config.Load(config.LoadOptions{
		ConfigPath:         writeMinimalWorkflowConfig(t),
		AllowMissingConfig: true,
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if res.MCP.Enabled || len(res.MCP.Servers) > 0 {
		t.Fatalf("resolved MCP leaked from the ambient user config: enabled=%v servers=%d (user config %q)",
			res.MCP.Enabled, len(res.MCP.Servers), config.UserConfigPath())
	}
	if err := validateWorkflowMCPConfigDigest("wfr-hermetic", digestFreeSnapshot(), res.MCP); err != nil {
		t.Fatalf("digest-free snapshot refused under a hermetic config: %v", err)
	}
}
