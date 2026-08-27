package composition

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// toolNames returns the sorted set of names a registry lists, so two
// registries can be compared regardless of registration order.
func toolNames(t *testing.T, r *tools.Registry) []string {
	t.Helper()
	list := r.List()
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Name())
	}
	slices.Sort(names)
	return names
}

// openTestWorkspace opens a workspace.Root rooted at a fresh temp dir, the
// same construction internal/cli's buildWorkflowToolOpts and
// workflowDefaultRegistry perform before calling the registry builder.
func openTestWorkspace(t *testing.T) *workspace.Root {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("workspace.Open: %v", err)
	}
	return ws
}

// TestBuildRegistry_MatchesLegacyShape pins BuildRegistry's output against
// the pre-move construction it replaces: internal/cli built a
// tools.DefaultOptions by hand and called tools.NewDefaultRegistry directly
// (see internal/cli/workflow_authority.go's former workflowDefaultRegistry
// body and chat_workspace.go's former configureChatWorkspace body, both
// moved in slice 1.2). Same inputs must produce the same tool set.
func TestBuildRegistry_MatchesLegacyShape(t *testing.T) {
	ws := openTestWorkspace(t)

	// Inputs mirror internal/cli/characterization_test.go's
	// baseResolvedConfig: RunAllowlist = ["echo"], no Tavily key.
	legacyOpts := tools.DefaultOptions{
		Workspace:    ws,
		RunAllowlist: []string{"echo"},
	}
	legacy := tools.NewDefaultRegistry(legacyOpts)

	in := RegistryInput{
		Workspace:    ws,
		RunAllowlist: []string{"echo"},
	}
	built, err := BuildRegistry(in)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	legacyNames := toolNames(t, legacy)
	builtNames := toolNames(t, built)
	if !slices.Equal(legacyNames, builtNames) {
		t.Fatalf("BuildRegistry tool set diverged from legacy tools.NewDefaultRegistry:\nlegacy = %v\nbuilt  = %v", legacyNames, builtNames)
	}
	if !slices.Contains(builtNames, tools.RunCommandToolName) {
		t.Fatalf("expected %q present with a non-empty allowlist, got %v", tools.RunCommandToolName, builtNames)
	}
}

// TestBuildRegistry_RunCommandPresentByDefault mirrors
// internal/tools/default_registry.go's registerDefaultTools: run_command is
// advertised by default via tools.DefaultRunAllowlist, even with no
// [tools] run_allowlist configured.
func TestBuildRegistry_RunCommandPresentByDefault(t *testing.T) {
	ws := openTestWorkspace(t)
	built, err := BuildRegistry(RegistryInput{Workspace: ws})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	names := toolNames(t, built)
	if !slices.Contains(names, tools.RunCommandToolName) {
		t.Fatalf("expected %q present via the built-in allowlist, got %v", tools.RunCommandToolName, names)
	}
}

// TestBuildRegistry_ExtractAbsentWithoutTavilyKey mirrors
// internal/tools/default_registry.go's registerWebTools: the extract tool
// (Tavily-backed) registers only when TavilyAPIKey is set. web_search and
// fetch_url register unconditionally (web_search falls back to a
// free-engine search), so only extract is checked here.
func TestBuildRegistry_ExtractAbsentWithoutTavilyKey(t *testing.T) {
	ws := openTestWorkspace(t)
	built, err := BuildRegistry(RegistryInput{Workspace: ws})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	names := toolNames(t, built)
	if slices.Contains(names, "extract") {
		t.Fatalf("expected \"extract\" absent with no Tavily key, got %v", names)
	}

	withKey, err := BuildRegistry(RegistryInput{Workspace: ws, TavilyAPIKey: "test-key"})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if !slices.Contains(toolNames(t, withKey), "extract") {
		t.Fatalf("expected \"extract\" present with a Tavily key set")
	}
}
