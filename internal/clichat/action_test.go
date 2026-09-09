package clichat

import (
	"testing"
)

// Typed action model: every transcript action is a tool, an agent, or a
// skill - with a single-width glyph. Emoji are gone: they are double-width
// and font-dependent, misaligning columns in real terminals.

func TestActionKindForTool(t *testing.T) {
	for _, name := range []string{"delegate", "dispatch_tasks", "spawn_agent", "multi_step", "oneshot", "join_run", "inspect_agents", "cancel_run"} {
		if ActionKindForTool(name) != ActionAgent {
			t.Fatalf("%s must classify as agent", name)
		}
	}
	for _, name := range []string{"read_file", "grep", "run_command", "unknown_tool"} {
		if ActionKindForTool(name) != actionTool {
			t.Fatalf("%s must classify as tool", name)
		}
	}
}

func TestActionIconsSingleWidth(t *testing.T) {
	for _, name := range []string{"read_file", "grep", "delegate", "spawn_agent", "whatever"} {
		icon := ToolIconForName(name)
		if len([]rune(icon)) != 1 {
			t.Fatalf("%s icon %q is not a single rune", name, icon)
		}
		for _, r := range icon {
			if r >= 0x1F000 {
				t.Fatalf("%s icon %q is an emoji - banned (double-width, font-dependent)", name, icon)
			}
		}
	}
	if ToolIconForName("delegate") != "◆" {
		t.Fatalf("agent tools must use the brand diamond, got %q", ToolIconForName("delegate"))
	}
}
