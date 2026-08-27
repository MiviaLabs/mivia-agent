package tools_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/memory"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// diagProg returns a portable allowlist program: echo is a Windows shell
// builtin with no executable, and the registration gate resolves argv[0]
// against the allowlist (and PATH at run time), so tests that need a
// resolvable program use "go" there.
func diagProg() string {
	if runtime.GOOS == "windows" {
		return "go"
	}
	return "echo"
}

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
	store, err := memory.Open(memory.Config{Backend: memory.BackendMemory})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{
		Workspace:    ws,
		TavilyAPIKey: "test-key-not-real",
		RunAllowlist: []string{diagProg()}, // run_command is conditional on non-empty allowlist
		Memory:       store,                // memory tools are conditional on a wired store
		// get_diagnostics is conditional on a configured DiagnosticsCommands
		// map whose default entry's argv[0] is on the effective run_command
		// allowlist. diagProg() is on the harness allowlist, so the tool
		// registers and the catalogue ↔ registry contract below holds for it
		// in both directions.
		DiagnosticsCommands: map[string][]string{
			"default": {diagProg(), "diagnostics"},
		},
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

// TestGetDiagnosticsRegistrationConditions pins the get_diagnostics
// registration contract (locked plan v2, task t6): the tool is advertised only
// when it can succeed. It must be absent when DiagnosticsCommands is unset, and
// absent when the default command's argv[0] is not on the effective run_command
// allowlist; only configured AND allowlisted defaults register it. This mirrors
// the advertised-iff-can-succeed contract of run_command and extract.
func TestGetDiagnosticsRegistrationConditions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	newReg := func(commands map[string][]string, allowlist []string) *tools.Registry {
		return tools.NewDefaultRegistry(tools.DefaultOptions{
			Workspace:           ws,
			RunAllowlist:        allowlist,
			DiagnosticsCommands: commands,
		})
	}
	hasDiagnostics := func(reg *tools.Registry) bool {
		_, ok := reg.Get(tools.GetDiagnosticsToolName)
		return ok
	}

	// Unconfigured: no DiagnosticsCommands → the tool is not registered.
	if hasDiagnostics(newReg(nil, []string{diagProg()})) {
		t.Errorf("get_diagnostics must not register when DiagnosticsCommands is unset")
	}

	// Configured but the default argv[0] NOT on the allowlist → the tool is
	// not registered. The allowlist membership check precedes PATH resolution,
	// so this is deterministic regardless of whether the program exists.
	if hasDiagnostics(newReg(map[string][]string{"default": {"not_an_allowlisted_program", "diagnostics"}}, []string{diagProg()})) {
		t.Errorf("get_diagnostics must not register when the default argv[0] is not allowlisted")
	}

	// Configured with a "default" entry whose argv[0] is allowlisted (and
	// resolvable on PATH) → registered.
	if !hasDiagnostics(newReg(map[string][]string{"default": {diagProg(), "diagnostics"}}, []string{diagProg()})) {
		t.Errorf("get_diagnostics must register when DiagnosticsCommands has an allowlisted default entry")
	}

	// A sole non-"default" command is its own default (the v2 defaultName
	// rule) and gates registration the same way.
	if !hasDiagnostics(newReg(map[string][]string{"check": {diagProg(), "diagnostics"}}, []string{diagProg()})) {
		t.Errorf("get_diagnostics must register with a sole non-default command name as the default")
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
