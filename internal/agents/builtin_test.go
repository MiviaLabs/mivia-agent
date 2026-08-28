package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestCleanWorkspaceShipsGeneralPurpose pins the first-run contract: a clean
// binary in a clean workspace (no user agents, no workspace agents) still
// resolves a spawnable, tool-bearing general-purpose agent.
func TestCleanWorkspaceShipsGeneralPurpose(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, warnings, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings on a clean load: %v", warnings)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
	if got := reg.Names(); !slices.Equal(got, []string{BuiltInGeneralPurposeName}) {
		t.Fatalf("clean-workspace registry = %v, want [%s]", got, BuiltInGeneralPurposeName)
	}
	builtin, ok := reg.Get(BuiltInGeneralPurposeName)
	if !ok {
		t.Fatalf("built-in %q missing", BuiltInGeneralPurposeName)
	}
	if builtin.Provenance.Source != config.AgentSourceBuiltIn {
		t.Fatalf("provenance source = %q, want %q", builtin.Provenance.Source, config.AgentSourceBuiltIn)
	}
	if builtin.Provider != "" || builtin.Model != "" {
		t.Fatalf("built-in must follow the session binding, got provider=%q model=%q", builtin.Provider, builtin.Model)
	}
	if strings.TrimSpace(builtin.SystemPrompt) == "" {
		t.Fatal("built-in system prompt is empty")
	}
	// The effective toolset is the full declared catalogue, as a full-set
	// equality (not membership). This holds while the compiled mandatory
	// denylist and the declared catalogue stay disjoint: applyToolPolicy
	// strips denylist names at resolve time, so a future overlap invalidates
	// this pin loudly, which is the intent.
	want := slices.Clone(tools.DeclaredToolNames())
	slices.Sort(want)
	got := slices.Clone(builtin.EffectiveTools)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("EffectiveTools = %d tools, want the full declared catalogue (%d): missing=%v extra=%v",
			len(got), len(want), missing(want, got), missing(got, want))
	}
	// The built-in is published after every file-backed agent.
	for _, other := range reg.List() {
		if other.Name == BuiltInGeneralPurposeName {
			continue
		}
		t.Fatalf("unexpected registry member %q on a clean load", other.Name)
	}
}

// TestBuiltInDigestStable pins that repeated loads of the same built-in
// produce the same definition digest (routing persists digests).
func TestBuiltInDigestStable(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	digests := map[string]bool{}
	for i := 0; i < 2; i++ {
		reg, _, _, err := LoadAndResolve(ws, nil)
		if err != nil {
			t.Fatalf("load %d: error = %v", i, err)
		}
		builtin, ok := reg.Get(BuiltInGeneralPurposeName)
		if !ok {
			t.Fatalf("load %d: built-in missing", i)
		}
		digest, err := builtin.DefinitionDigest()
		if err != nil {
			t.Fatalf("load %d: digest error = %v", i, err)
		}
		digests[digest] = true
	}
	if len(digests) != 1 {
		t.Fatalf("built-in digest is not stable across loads: %v", digests)
	}
}

// TestBuiltInShadowingPrecedence pins user > workspace > built-in: a
// same-name file-backed definition replaces the built-in with a warning.
func TestBuiltInShadowingPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		write   func(t *testing.T, ws string)
		wantSrc config.AgentSource
	}{
		{
			name: "workspace file shadows built-in",
			write: func(t *testing.T, ws string) {
				writeAgent(t, config.WorkspaceAgentsDir(ws), "general-purpose.toml",
					"name = \"general-purpose\"\ndescription = \"custom\"\ntools = [\"read_file\"]\n")
			},
			wantSrc: config.AgentSourceWorkspace,
		},
		{
			name: "user file shadows built-in",
			write: func(t *testing.T, ws string) {
				writeAgent(t, config.UserAgentsDir(), "general-purpose.toml",
					"name = \"general-purpose\"\ndescription = \"custom\"\ntools = [\"read_file\"]\n")
			},
			wantSrc: config.AgentSourceUser,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, ws := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			tc.write(t, ws)

			reg, _, warnings, err := LoadAndResolve(ws, nil)
			if err != nil {
				t.Fatalf("LoadAndResolve error = %v", err)
			}
			got, ok := reg.Get("general-purpose")
			if !ok {
				t.Fatalf("general-purpose missing from registry %v", reg.Names())
			}
			if got.Provenance.Source != tc.wantSrc {
				t.Fatalf("general-purpose source = %q, want %q", got.Provenance.Source, tc.wantSrc)
			}
			joined := strings.Join(warnings, " ")
			if !strings.Contains(joined, "built-in agent") || !strings.Contains(joined, "general-purpose") {
				t.Fatalf("warnings = %q, want a built-in override notice naming general-purpose", joined)
			}
		})
	}
}

// TestBuiltInGateOffStillPresent pins D5: the compiled built-ins are product
// content, so a user config with load_workspace_config = false still ships
// general-purpose.
func TestBuiltInGateOffStillPresent(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, home+"/.mivia", "mivia.toml", "[agents]\nload_workspace_config = false\n")

	_, global, _, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if global.LoadWorkspaceConfig {
		t.Fatal("gate fixture did not apply; test is vacuous")
	}
	reg, _, _, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if _, ok := reg.Get(BuiltInGeneralPurposeName); !ok {
		t.Fatalf("built-in %q must load with the workspace gate off, registry = %v",
			BuiltInGeneralPurposeName, reg.Names())
	}
}

// TestBuiltInSkillCollisionTolerated pins D11: a skill named general-purpose
// makes the built-in fail its collision check, which must skip the built-in
// with a warning instead of aborting the load.
func TestBuiltInSkillCollisionTolerated(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, warnings, err := LoadAndResolve(ws, map[string]struct{}{"general-purpose": {}})
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v, want tolerant skip", err)
	}
	if reg == nil {
		t.Fatal("registry is nil")
	}
	if _, ok := reg.Get(BuiltInGeneralPurposeName); ok {
		t.Fatalf("built-in must be skipped on a skill collision: %v", reg.Names())
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "skipped built-in agent") || !strings.Contains(joined, BuiltInGeneralPurposeName) {
		t.Fatalf("warnings = %q, want a built-in skip notice naming %q", joined, BuiltInGeneralPurposeName)
	}
}

// TestRootNameReservedAgainstFileAgents pins D14: the root identity name is
// not loadable from files. User-sourced files fail closed; workspace-sourced
// files are tolerated with a skip warning; the built-in general-purpose
// survives both cases.
func TestRootNameReservedAgainstFileAgents(t *testing.T) {
	t.Run("user file is a hard error", func(t *testing.T) {
		home, ws := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		writeAgent(t, config.UserAgentsDir(), "general-orchestrator.toml",
			"name = \"general-orchestrator\"\ndescription = \"imposter\"\ntools = [\"read_file\"]\n")

		_, _, _, err := LoadAndResolve(ws, nil)
		if err == nil {
			t.Fatal("user file with the reserved root name must fail the load")
		}
	})
	t.Run("workspace file is a tolerant skip", func(t *testing.T) {
		home, ws := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		writeAgent(t, config.WorkspaceAgentsDir(ws), "general-orchestrator.toml",
			"name = \"general-orchestrator\"\ndescription = \"imposter\"\ntools = [\"read_file\"]\n")

		reg, _, warnings, err := LoadAndResolve(ws, nil)
		if err != nil {
			t.Fatalf("LoadAndResolve error = %v, want tolerant skip", err)
		}
		if _, ok := reg.Get(config.RootAgentName); ok {
			t.Fatalf("reserved root name must not publish: %v", reg.Names())
		}
		if _, ok := reg.Get(BuiltInGeneralPurposeName); !ok {
			t.Fatalf("built-in must survive the skip: %v", reg.Names())
		}
		joined := strings.Join(warnings, " ")
		if !strings.Contains(joined, "skipped workspace agent") || !strings.Contains(joined, config.RootAgentName) {
			t.Fatalf("warnings = %q, want a skip notice naming %q", joined, config.RootAgentName)
		}
	})
}

// TestBuiltInPublishedAfterFileAgents pins the ordering contract: built-ins
// publish after every file-backed agent (registry List order).
func TestBuiltInPublishedAfterFileAgents(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, config.WorkspaceAgentsDir(ws), "aardvark.toml",
		"name = \"aardvark\"\ndescription = \"a\"\ntools = [\"read_file\"]\n")

	reg, _, _, err := LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	list := reg.List()
	if len(list) != 2 {
		t.Fatalf("registry = %v, want [aardvark general-purpose]", reg.Names())
	}
	if list[0].Name != "aardvark" || list[1].Name != BuiltInGeneralPurposeName {
		t.Fatalf("publication order = [%s %s], want file agents before built-ins", list[0].Name, list[1].Name)
	}
}

// TestBuiltInPromptLanguageGeneric pins rule 60 for the compiled prompt and
// description constants: they must not bake in a language, framework, or this
// repo's own layout.
func TestBuiltInPromptLanguageGeneric(t *testing.T) {
	banned := []string{"golang", "go.mod", "make ", "cmd/mivia", "internal/", "rust", "python", "node.js", "typescript"}
	for _, s := range []string{BuiltInGeneralPurposePrompt, BuiltInGeneralPurposeDescription, BuiltInOrchestratorPrompt} {
		lower := strings.ToLower(s)
		for _, b := range banned {
			if strings.Contains(lower, b) {
				t.Fatalf("compiled prompt content contains project/language-specific term %q", b)
			}
		}
	}
}

// TestBuiltInPromptDisciplineLines pins the briefing/steering discipline added
// for dispatched-task bounds (86f7f561) and subagent checkpointing
// (abce735c): future trims of the compiled prompts must not silently drop
// these lines, and the steering rule must stay grounded in what the parent
// can actually observe (timeout-based, never "identical running checks").
func TestBuiltInPromptDisciplineLines(t *testing.T) {
	for _, want := range []string{
		"a real timeout_seconds",
		"interrupt:true",
		"cancel_run",
	} {
		if !strings.Contains(BuiltInOrchestratorPrompt, want) {
			t.Errorf("orchestrator prompt lost the discipline line fragment %q", want)
		}
	}
	for _, want := range []string{
		"Time-box each line of inquiry",
		"Checkpoint via post_message",
		"never end silent",
	} {
		if !strings.Contains(BuiltInGeneralPurposePrompt, want) {
			t.Errorf("general-purpose prompt lost the discipline line fragment %q", want)
		}
	}
	if strings.Contains(BuiltInOrchestratorPrompt, "No progress across two checks") {
		t.Error("orchestrator prompt still claims observable progress the parent cannot see")
	}
}

func missing(want, got []string) []string {
	var out []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			out = append(out, w)
		}
	}
	return out
}

// TestInspectBuiltInCollisionIsWarningNotMalformedFile pins that the
// inspection path reports a skipped built-in as a warning, never as a
// malformed-file diagnostic (compiled content has no file to be malformed).
func TestInspectBuiltInCollisionIsWarningNotMalformedFile(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	report, err := Inspect(ws, LoadResolveOptions{SkillNames: map[string]struct{}{"general-purpose": {}}})
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if _, ok := report.Registry.Get(BuiltInGeneralPurposeName); ok {
		t.Fatalf("built-in must be skipped on a skill collision: %v", report.Registry.Names())
	}
	joined := strings.Join(report.Warnings, " ")
	if !strings.Contains(joined, "skipped built-in agent") || !strings.Contains(joined, BuiltInGeneralPurposeName) {
		t.Fatalf("warnings = %q, want a built-in skip notice", joined)
	}
	if strings.Contains(report.DiagnosticSummary(), "malformed") {
		t.Fatalf("compiled content must not become a malformed-file diagnostic: %q", report.DiagnosticSummary())
	}
}
