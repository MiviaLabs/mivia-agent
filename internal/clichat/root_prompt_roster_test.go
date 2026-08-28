package clichat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// rosterTestRegistry is a one-entry registry mirroring a clean-workspace load.
func rosterTestRegistry(t *testing.T) *agents.AgentRegistry {
	t.Helper()
	reg := agents.NewRegistry()
	if err := reg.Publish(agents.ResolvedAgent{
		Name:        "general-purpose",
		Description: "General-purpose agent with the default toolset.",
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestRootPromptForSessionAppendsRosterOnToolSessions pins D3 wiring: the
// additive roster lands on tool-bearing sessions and on CUSTOM operator
// prompts alike (the roster is environment fact). Kill mutation: move the
// RootSystemPromptWithRoster call inside buildAgentPrompt or drop it here.
func TestRootPromptForSessionAppendsRosterOnToolSessions(t *testing.T) {
	reg := rosterTestRegistry(t)
	custom := &config.Resolved{SystemPrompt: "You are my bespoke orchestrator."}

	got := rootPromptForSession(true, custom, reg)
	if !strings.HasPrefix(got, "You are my bespoke orchestrator.") {
		t.Fatalf("custom prompt must survive untouched before the roster: %q", got)
	}
	if !strings.Contains(got, "# Subagents") || !strings.Contains(got, "- general-purpose:") {
		t.Fatalf("tool session missing roster announcement: %q", got)
	}
}

// TestRootPromptForSessionNoToolsSkipsRoster pins that a no-tools session
// never announces dispatch_tasks-selectable subagents - the tool does not
// exist there. Kill mutation: drop the useTools guard.
func TestRootPromptForSessionNoToolsSkipsRoster(t *testing.T) {
	reg := rosterTestRegistry(t)
	res := &config.Resolved{}

	got := rootPromptForSession(false, res, reg)
	if strings.Contains(got, "# Subagents") || got != defaultSystemPrompt {
		t.Fatalf("no-tools session must get the bare default prompt, got %q", got)
	}
}

// TestRootPromptForSessionFallbackPlusRoster pins the clean-workspace
// composition: compiled orchestrator prompt first, roster appended after.
// Kill mutation: replace the fallback branch ordering.
func TestRootPromptForSessionFallbackPlusRoster(t *testing.T) {
	res := &config.Resolved{}
	got := rootPromptForSession(true, res, rosterTestRegistry(t))

	if !strings.HasPrefix(got, buildAgentPrompt(config.SubagentConfig{})) {
		t.Fatal("roster must come after the compiled fallback prompt")
	}
	staticLen := len(buildAgentPrompt(config.SubagentConfig{}))
	// The static prompt MENTIONS "# Subagents"; anchor on the rendered
	// section header instead.
	if idx := strings.Index(got, "\n# Subagents\n"); idx < 0 || idx < staticLen {
		t.Fatalf("rendered section must follow the static prompt (static %d bytes), index %d", staticLen, idx)
	}
	if !strings.Contains(got, "- general-purpose: General-purpose agent with the default toolset.") {
		t.Fatalf("roster line missing: %q", got)
	}
}

// TestRootPromptForSessionEmptyRegistryByteStable pins the no-op arm: an
// empty or nil registry returns the composed prompt unchanged byte-for-byte,
// so sessions without any agent definition see zero drift.
func TestRootPromptForSessionEmptyRegistryByteStable(t *testing.T) {
	res := &config.Resolved{SystemPrompt: "custom"}
	for _, reg := range []*agents.AgentRegistry{nil, agents.NewRegistry()} {
		if got := rootPromptForSession(true, res, reg); got != "custom" {
			t.Fatalf("registry %v must be a byte-stable no-op, got %q", reg != nil, got)
		}
	}
}
