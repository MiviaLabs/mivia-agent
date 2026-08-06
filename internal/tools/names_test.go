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
	// Phase 7 workflow tools register only when .mivia/workflows/ exists and a
	// builder is installed (CLI init in production; test builder here).
	if err := os.MkdirAll(filepath.Join(dir, ".mivia", "workflows"), 0o700); err != nil {
		t.Fatal(err)
	}
	installTestWorkflowBuilder(t)
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
		RunAllowlist: []string{"echo"}, // run_command is conditional on non-empty allowlist
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

// TestDeclaredToolNamesExcludesActivationOnly pins plan 43: the static
// declared-tool catalogue used to validate skill frontmatter and agent TOML
// tool requirements must exclude the activation-only read_skill_resource
// capability. Neither a skill nor an agent may statically require or declare
// it; it is injected per invocation only.
func TestDeclaredToolNamesExcludesActivationOnly(t *testing.T) {
	declared := tools.DeclaredToolNames()
	if !slices.IsSorted(declared) {
		t.Fatalf("DeclaredToolNames must be sorted: %v", declared)
	}
	for _, name := range declared {
		if name == tools.SkillResourceToolName {
			t.Fatalf("read_skill_resource leaked into the static declared catalogue")
		}
	}
	// The static catalogue is exactly the full catalogue minus the
	// activation-only capability - nothing else may drift.
	all := tools.AllToolNames()
	expected := make([]string, 0, len(all))
	for _, name := range all {
		if name == tools.SkillResourceToolName {
			continue
		}
		expected = append(expected, name)
	}
	if !slices.Equal(declared, expected) {
		t.Fatalf("DeclaredToolNames = %v, want %v", declared, expected)
	}
}

func TestIsDeclaredToolName(t *testing.T) {
	if !tools.IsDeclaredToolName("read_file") {
		t.Fatal("read_file must be a declared tool")
	}
	if tools.IsDeclaredToolName(tools.SkillResourceToolName) {
		t.Fatal("read_skill_resource must not be a statically declared tool")
	}
	if tools.IsDeclaredToolName("not_a_tool") {
		t.Fatal("unknown name must not be declared")
	}
}
