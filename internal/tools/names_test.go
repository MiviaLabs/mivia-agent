package tools_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestAllToolNamesMatchesFullRegistry(t *testing.T) {
	dir := t.TempDir()
	// find_references registers only when a workspace is present.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
	})
	// Also register skill resource which default registry may not include.
	// AllToolNames lists it; if not in reg, catalogue still claims it as known.
	got := map[string]struct{}{}
	for _, tool := range reg.List() {
		got[tool.Name()] = struct{}{}
	}
	// Catalogue must cover every name the registry actually registered.
	catalogue := tools.AllToolNames()
	if !slices.IsSorted(catalogue) {
		t.Fatalf("AllToolNames must be sorted: %v", catalogue)
	}
	for name := range got {
		if !tools.IsKnownToolName(name) {
			t.Errorf("registry tool %q missing from AllToolNames catalogue", name)
		}
	}
	// And every catalogue name must be a real tool the binary can construct
	// (allow skill resource which is CLI-registered).
	for _, name := range catalogue {
		if name == tools.SkillResourceToolName {
			continue
		}
		if _, ok := got[name]; !ok {
			t.Errorf("catalogue name %q not present in full default registry", name)
		}
	}
}
