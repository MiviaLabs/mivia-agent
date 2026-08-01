package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Typed action model: every transcript action is a tool, an agent, or a
// skill - with a single-width glyph. Emoji are gone: they are double-width
// and font-dependent, misaligning columns in real terminals.

func TestActionKindForTool(t *testing.T) {
	for _, name := range []string{"delegate", "dispatch_tasks", "spawn_agent", "multi_step", "oneshot", "join_run", "inspect_agents", "cancel_run"} {
		if actionKindForTool(name) != actionAgent {
			t.Fatalf("%s must classify as agent", name)
		}
	}
	for _, name := range []string{"read_file", "grep", "run_command", "unknown_tool"} {
		if actionKindForTool(name) != actionTool {
			t.Fatalf("%s must classify as tool", name)
		}
	}
}

func TestActionIconsSingleWidth(t *testing.T) {
	for _, name := range []string{"read_file", "grep", "delegate", "spawn_agent", "whatever"} {
		icon := toolIconForName(name)
		if len([]rune(icon)) != 1 {
			t.Fatalf("%s icon %q is not a single rune", name, icon)
		}
		for _, r := range icon {
			if r >= 0x1F000 {
				t.Fatalf("%s icon %q is an emoji - banned (double-width, font-dependent)", name, icon)
			}
		}
	}
	if toolIconForName("delegate") != "◆" {
		t.Fatalf("agent tools must use the brand diamond, got %q", toolIconForName("delegate"))
	}
}

func TestWorkGroupHeaderTypedCounts(t *testing.T) {
	blocks := []ChatBlock{
		{ID: "b1", Kind: ChatBlockTool, ToolName: "read_file", Text: "x", Collapsed: true},
		{ID: "b2", Kind: ChatBlockTool, ToolName: "grep", Text: "x", Collapsed: true},
		{ID: "b3", Kind: ChatBlockTool, ToolName: "delegate", Text: "x", Collapsed: true},
		{ID: "b4", Kind: ChatBlockTool, ToolName: "run_command", Text: "x", Collapsed: true, Failed: true},
	}
	out := RenderChatBlocksWithWorkGroups(blocks, "m", 100, false, map[string]bool{})
	plain := stripANSI(strings.Join(out.Lines, "\n"))
	// Compatibility segment stays; typed segments extend it.
	if !strings.Contains(plain, "Work · 4 tools") {
		t.Fatalf("header lost base segment: %q", plain)
	}
	if !strings.Contains(plain, "1 ◆") {
		t.Fatalf("header missing agent count: %q", plain)
	}
	if !strings.Contains(plain, "1 ✗") {
		t.Fatalf("header missing failure count: %q", plain)
	}
}

func TestWorkGroupExpandedRowsCapped(t *testing.T) {
	// Expanding a huge group must not dump hundreds of rows: members are
	// capped with an explicit "… N more" line, never silently truncated.
	var blocks []ChatBlock
	for i := 0; i < 50; i++ {
		blocks = append(blocks, ChatBlock{
			ID: fmt.Sprintf("b%d", i), Kind: ChatBlockTool,
			ToolName: "read_file", Text: "x", Collapsed: true,
		})
	}
	collapsed := map[string]bool{}
	groups := findWorkGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("groups: %d", len(groups))
	}
	collapsed[groups[0].Key] = false // explicitly expanded
	out := RenderChatBlocksWithWorkGroups(blocks, "m", 100, false, collapsed)
	plain := stripANSI(strings.Join(out.Lines, "\n"))
	rows := strings.Count(plain, "read_file")
	if rows > maxWorkGroupRows {
		t.Fatalf("expanded group shows %d rows, cap is %d", rows, maxWorkGroupRows)
	}
	if !strings.Contains(plain, "more") {
		t.Fatalf("cap must be explicit, not silent: %q", plain)
	}
}

func TestToolLedgerRowShowsDurationAndStatus(t *testing.T) {
	ok := ChatBlock{Kind: ChatBlockTool, ToolName: "grep", Text: "3 hits", Collapsed: true, Elapsed: 1200 * time.Millisecond}
	lines := renderOneChatBlock(ok, "m", 100, false)
	plain := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "1.2s") {
		t.Fatalf("row missing duration: %q", plain)
	}
	if !strings.Contains(plain, "✓") {
		t.Fatalf("row missing ok status: %q", plain)
	}

	bad := ChatBlock{Kind: ChatBlockTool, ToolName: "run_command", Text: "exit 1", Collapsed: true, Failed: true, Elapsed: time.Second}
	lines = renderOneChatBlock(bad, "m", 100, false)
	plain = stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "✗") {
		t.Fatalf("failed row missing ✗: %q", plain)
	}
}
