package cliagents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// rosterFixtureRegistry builds a registry from plain resolved agents.
func rosterFixtureRegistry(t *testing.T, defs ...agents.ResolvedAgent) *agents.AgentRegistry {
	t.Helper()
	reg := agents.NewRegistry()
	for _, def := range defs {
		if err := reg.Publish(def); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// TestSubagentRosterSectionExactRender pins the deterministic small-case
// render. Kill mutation: any change to the header wording, entry shape
// ("- name: desc"), or ordering.
func TestSubagentRosterSectionExactRender(t *testing.T) {
	reg := rosterFixtureRegistry(t,
		agents.ResolvedAgent{Name: "reviewer", Description: "Reviews changed code for quality."},
		agents.ResolvedAgent{Name: "general-purpose", Description: "General-purpose agent with the default toolset; use for research."},
	)
	want := "\n\n# Subagents\n" +
		"Loaded subagents, selectable via dispatch_tasks' optional agent field:\n" +
		"- reviewer: Reviews changed code for quality.\n" +
		"- general-purpose: General-purpose agent with the default toolset; use for research.\n"
	if got := SubagentRosterSection(reg); got != want {
		t.Fatalf("section = %q, want %q", got, want)
	}
}

// TestSubagentRosterSectionCapAndTail pins the 8-line cap plus overflow tail
// naming the remainder. Kill mutation: raise SubagentRosterMaxLines or drop
// the tail branch.
func TestSubagentRosterSectionCapAndTail(t *testing.T) {
	defs := make([]agents.ResolvedAgent, 0, 12)
	for i := 0; i < 12; i++ {
		defs = append(defs, agents.ResolvedAgent{Name: "agent" + string(rune('a'+i))})
	}
	got := SubagentRosterSection(rosterFixtureRegistry(t, defs...))
	if gotEntries := strings.Count(got, "\n- "); gotEntries != SubagentRosterMaxLines+1 { // entries + tail line
		t.Fatalf("roster lines = %d, want %d entries + 1 tail", gotEntries-1, SubagentRosterMaxLines)
	}
	if !strings.Contains(got, "...and 4 more") {
		t.Fatalf("overflow tail missing: %q", got)
	}
	if !strings.Contains(got, "- agenta") || !strings.Contains(got, "- agenth") {
		t.Fatalf("first eight entries not shown in registry order: %q", got)
	}
}

// TestSubagentRosterSectionClamps pins the size clamps: long names truncate
// at subagentRosterNameBytes and long descriptions keep only the first
// sentence clamped to subagentRosterDescBytes. Kill mutation: remove a clamp
// in subagentRosterEntry - the byte formula below then fails loudly.
func TestSubagentRosterSectionClamps(t *testing.T) {
	longDesc := "Clamps to the opening sentence. " + strings.Repeat("filler ", 60) + "end."
	reg := rosterFixtureRegistry(t,
		agents.ResolvedAgent{Name: strings.Repeat("n", 100), Description: longDesc},
	)
	section := SubagentRosterSection(reg)
	lines := strings.Split(strings.TrimPrefix(section, "\n\n"), "\n")
	entry := lines[2]
	// Line = "- " + name(clamped<=40) + optional ": "+desc(clamped<=160) + no newline.
	maxEntry := 2 + subagentRosterNameBytes + len(": ") + subagentRosterDescBytes
	if got := len(entry); got > maxEntry {
		t.Fatalf("entry = %d bytes, want <= %d: %q", got, maxEntry, entry)
	}
	if !strings.HasSuffix(entry, ".") {
		t.Fatalf("description should end at its first sentence: %q", entry)
	}
	if strings.Contains(entry, "Tail that must vanish") {
		t.Fatalf("post-sentence text leaked into the roster: %q", entry)
	}
}

// TestRootSystemPromptWithRoster pins the assembly contract: additive for a
// custom operator prompt, passthrough when there is nothing to announce.
// Kill mutation: route the call through buildAgentPrompt or drop the empty
// registry guard (custom prompts would lose content).
func TestRootSystemPromptWithRoster(t *testing.T) {
	custom := "You are my bespoke orchestrator. Obey the house style."
	reg := rosterFixtureRegistry(t, agents.ResolvedAgent{Name: "general-purpose", Description: "General toolwork."})

	got := RootSystemPromptWithRoster(custom, reg)
	if !strings.HasPrefix(got, custom) {
		t.Fatalf("roster must append after the custom prompt, got %q", got)
	}
	if !strings.Contains(got, "# Subagents") || !strings.Contains(got, "general-purpose") {
		t.Fatalf("roster missing from assembled prompt: %q", got)
	}
	if again := RootSystemPromptWithRoster(custom, reg); again != got {
		t.Fatal("assembly is not deterministic for an unchanged registry")
	}

	if changed := RootSystemPromptWithRoster(custom, nil); changed != custom {
		t.Fatalf("nil registry must return the prompt unchanged, got %q", changed)
	}
	if changed := RootSystemPromptWithRoster(custom, agents.NewRegistry()); changed != custom {
		t.Fatalf("empty registry must return the prompt unchanged, got %q", changed)
	}
	if bare := RootSystemPromptWithRoster("", reg); !strings.HasPrefix(bare, "# Subagents") {
		t.Fatalf("blank prompt gets the section without leading newlines, got %q", bare)
	}
}

// TestCleanLoadRosterNamesBuiltIn wires D3 end to end at the load level: the
// registry a session attaches announces exactly the built-in on a clean
// workspace. Kill mutation: drop general-purpose from builtInInputs.
func TestCleanLoadRosterNamesBuiltIn(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	reg, _, warnings, err := agents.LoadAndResolve(ws, nil)
	if err != nil {
		t.Fatalf("LoadAndResolve error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	section := SubagentRosterSection(reg)
	if !strings.Contains(section, "- general-purpose: General-purpose agent with the default toolset; use for research, audits, reviews, and multi-step tasks that need tools.") {
		t.Fatalf("clean-workspace roster must announce the built-in: %q", section)
	}
}
