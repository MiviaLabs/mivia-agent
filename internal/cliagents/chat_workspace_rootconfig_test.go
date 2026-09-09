package cliagents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// A session's tool registry can be built for a root that is NOT the launch
// checkout: a git worktree, on its own branch, with its own committed
// .mivia/mivia.toml. Its declared policy is the one its tools must run under
// - the pool reused the launch config for every root, so a worktree that
// disabled a tool (or tightened any other limit) on its branch still ran with
// the launch checkout's rules.
func TestBuildToolsForRootHonorsThatRootsOwnToolsPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, bool,
		ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"),
		[]byte("[tools]\ndisable_tools = [\"read_file\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	launch := &config.Resolved{} // launch policy disables nothing

	for _, tc := range []struct {
		name         string
		gate         bool
		wantReadFile bool
	}{
		{"gate on: this root's own policy applies", true, false},
		{"gate off: workspace policy is refused", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, closeFn, err := cliagents.BuildToolsForRoot(root, t.TempDir(), false, launch,
				cliagents.SessionRootWiring{LoadWorkspaceConfig: tc.gate})
			if err != nil {
				t.Fatalf("BuildToolsForRoot: %v", err)
			}
			defer closeFn()
			_, got := reg.Get("read_file")
			if got != tc.wantReadFile {
				t.Fatalf("read_file present = %v, want %v - the root's own [tools] policy was not honored as the gate dictates", got, tc.wantReadFile)
			}
		})
	}
}

// TestBuildToolsForRootWithNoWorkspaceConfigUsesLaunchPolicy covers
// resolvedForRoot's !found branch: the gate is on, but this root carries no
// .mivia/mivia.toml at all, so the launch policy must be used unchanged
// rather than treated as an error.
func TestBuildToolsForRootWithNoWorkspaceConfigUsesLaunchPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, bool,
		ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	launch := &config.Resolved{}
	reg, closeFn, err := cliagents.BuildToolsForRoot(t.TempDir(), t.TempDir(), false, launch,
		cliagents.SessionRootWiring{LoadWorkspaceConfig: true})
	if err != nil {
		t.Fatalf("BuildToolsForRoot with no workspace config file: %v", err)
	}
	defer closeFn()
	if _, got := reg.Get("read_file"); !got {
		t.Fatal("read_file missing: the launch policy was not used for a root with no workspace config")
	}
}

// TestBuildToolsForRootPropagatesMalformedWorkspaceConfig covers
// BuildToolsForRoot's own resolvedForRoot error propagation, via
// WorkspaceToolsConfig's real TOML-parse failure.
func TestBuildToolsForRootPropagatesMalformedWorkspaceConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte("not valid toml [[["), 0o600); err != nil {
		t.Fatal(err)
	}
	launch := &config.Resolved{}
	if _, _, err := cliagents.BuildToolsForRoot(root, t.TempDir(), false, launch,
		cliagents.SessionRootWiring{LoadWorkspaceConfig: true}); err == nil {
		t.Fatal("BuildToolsForRoot accepted a root with malformed workspace config")
	}
}
