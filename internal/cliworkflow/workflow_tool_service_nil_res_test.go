package cliworkflow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestWorkflowToolSubagentConfigNilResLoadsWorkspaceConfig covers the res-nil,
// load-success path of workflowToolSubagentConfig: with no session Resolved
// available, it must load the workspace's own .mivia/mivia.toml (rather than
// falling back to config.DefaultSubagentConfig on a load error) and still
// apply the workspace store-root default to the freshly loaded value.
//
// config.Load requires a resolvable default provider even under
// AllowMissingConfig (resolveProvider always needs non-empty
// [providers.<name>].models), so the res-nil branch only reaches the
// ApplyWorkflowStoreRoot success line when a real, valid project config
// exists on disk - an empty or absent file takes the err path instead. This
// writes a minimal valid config, matching the pattern other cliworkflow
// tests use (see env_test_helpers_test.go's writeWorkflowRunFixture).
func TestWorkflowToolSubagentConfigNilResLoadsWorkspaceConfig(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	mivDir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(mivDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const cfg = `[provider]
name = "openrouter"

[providers.openrouter]
base_url = "http://127.0.0.1:0"
api_key_env = "WORKFLOW_TOOL_SERVICE_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
`
	if err := os.WriteFile(filepath.Join(mivDir, "mivia.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got := workflowToolSubagentConfig(root, nil)

	if want := workspace.ContextStorePath(root); got.StorePath != want {
		t.Fatalf("returned StorePath = %q, want workspace default %q (indicates the err/DefaultSubagentConfig fallback ran instead of the load-success path)", got.StorePath, want)
	}
}
