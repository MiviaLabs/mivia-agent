package cli

import (
	"strings"
	"testing"
)

func TestFindWorkGroups_SplitsOnInterim(t *testing.T) {
	t.Parallel()
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "go"},
		{ID: "s1", Kind: ChatBlockSystem, Text: "→ Listing…"},
		{ID: "t1", Kind: ChatBlockTool, ToolName: "list_dir"},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "glob"},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "Next I'll read the entrypoint."},
		{ID: "t3", Kind: ChatBlockTool, ToolName: "read_file"},
		{ID: "t4", Kind: ChatBlockTool, ToolName: "grep"},
		{ID: "a2", Kind: ChatBlockAssistant, Text: "Here is the answer."},
	}
	gs := findWorkGroups(blocks)
	if len(gs) != 2 {
		t.Fatalf("want 2 groups, got %d: %+v", len(gs), gs)
	}
	if gs[0].ToolCount != 2 || gs[1].ToolCount != 2 {
		t.Fatalf("tool counts: %+v", gs)
	}
	// Final assistant not inside any group.
	for _, g := range gs {
		for i := g.Start; i < g.End; i++ {
			if blocks[i].Kind == ChatBlockAssistant {
				t.Fatalf("assistant inside group %v", g)
			}
		}
	}
}

func TestWorkGroupAutoCollapseAt4(t *testing.T) {
	t.Parallel()
	blocks := []ChatBlock{
		{ID: "u", Kind: ChatBlockUser, Text: "x"},
		{ID: "t1", Kind: ChatBlockTool, ToolName: "a", Text: "1"},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "b", Text: "2"},
		{ID: "t3", Kind: ChatBlockTool, ToolName: "c", Text: "3"},
		{ID: "t4", Kind: ChatBlockTool, ToolName: "d", Text: "4"},
		{ID: "a", Kind: ChatBlockAssistant, Text: "final answer outside"},
	}
	collapsed := map[string]bool{}
	r := RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, collapsed)
	plain := strings.Join(r.Lines, "\n")
	if !strings.Contains(stripANSI(plain), "Work · 4 tools") {
		t.Fatalf("expected work header, got %q", plain)
	}
	// Collapsed by default: tool names should not all appear as full rows.
	// Header only when collapsed.
	if strings.Count(stripANSI(plain), "final answer outside") != 1 {
		t.Fatal("final assistant must remain visible")
	}
	// Expanded when map forces false.
	gs := findWorkGroups(blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d", len(gs))
	}
	collapsed[gs[0].Key] = false
	r2 := RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, collapsed)
	plain2 := stripANSI(strings.Join(r2.Lines, "\n"))
	if !strings.Contains(plain2, "Work · 4 tools") {
		t.Fatal("header when expanded")
	}
}

func TestWorkGroupFinalAssistantOutside(t *testing.T) {
	t.Parallel()
	blocks := []ChatBlock{
		{Kind: ChatBlockUser, Text: "u"},
		{ID: "t1", Kind: ChatBlockTool, ToolName: "a"},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "b"},
		{ID: "t3", Kind: ChatBlockTool, ToolName: "c"},
		{ID: "t4", Kind: ChatBlockTool, ToolName: "d"},
		{Kind: ChatBlockAssistant, Text: "THE_FINAL"},
	}
	r := RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, nil)
	if !strings.Contains(strings.Join(r.Lines, "\n"), "THE_FINAL") {
		t.Fatal("final must be outside collapsed group")
	}
}

func TestWorkGroupToggle(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.workGroupCollapsed = map[string]bool{}
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "u"})
	for i := 0; i < 4; i++ {
		m.appendBlock(ChatBlock{ID: "t" + itoa(i), Kind: ChatBlockTool, ToolName: "tool", Text: "x"})
	}
	m.appendBlock(ChatBlock{Kind: ChatBlockAssistant, Text: "done"})
	gs := findWorkGroups(m.blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d kinds=%v", len(gs), blockKinds(m.blocks))
	}
	// Auto-collapsed: few lines.
	m.renderVP()
	before := len(m.messages)
	m.selectedBlockID = gs[0].Key
	if !m.toggleSelectedBlock() {
		t.Fatal("toggle work group failed")
	}
	after := len(m.messages)
	if after <= before {
		t.Fatalf("expand should add lines: %d → %d", before, after)
	}
	// SoT blocks unchanged count.
	if len(m.blocks) != 6 {
		t.Fatalf("blocks SoT mutated: %d", len(m.blocks))
	}
}

func TestWorkGroup_TrailingEmptyLane(t *testing.T) {
	t.Parallel()
	blocks := []ChatBlock{
		{ID: "t1", Kind: ChatBlockTool, ToolName: "a", Text: "1", Collapsed: true},
		{ID: "t2", Kind: ChatBlockTool, ToolName: "b", Text: "2", Collapsed: true},
		{ID: "a", Kind: ChatBlockAssistant, Text: "answer after work"},
	}
	// Force group collapsed so header is visible then empty lane then assistant.
	gs := findWorkGroups(blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d", len(gs))
	}
	collapsed := map[string]bool{gs[0].Key: true}
	r := RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, collapsed)
	plain := make([]string, len(r.Lines))
	for i, ln := range r.Lines {
		plain[i] = stripANSI(ln)
	}
	workIdx, asstIdx := -1, -1
	for i, p := range plain {
		if strings.Contains(p, "Work ·") {
			workIdx = i
		}
		if strings.Contains(p, "answer after work") {
			asstIdx = i
		}
	}
	if workIdx < 0 || asstIdx < 0 {
		t.Fatalf("missing work/assistant: %v", plain)
	}
	// At least one blank line after Work section before next content.
	blank := false
	for i := workIdx + 1; i < asstIdx; i++ {
		if strings.TrimSpace(plain[i]) == "" {
			blank = true
			break
		}
	}
	if !blank {
		t.Fatalf("want empty lane after Work group, got %v", plain[workIdx:asstIdx+1])
	}
}

func TestWorkGroupNoNewKind(t *testing.T) {
	t.Parallel()
	blocks := []ChatBlock{
		{Kind: ChatBlockTool, ToolName: "a", ID: "1"},
		{Kind: ChatBlockTool, ToolName: "b", ID: "2"},
	}
	_ = RenderChatBlocksWithWorkGroups(blocks, "m", 80, true, nil)
	for _, b := range blocks {
		switch b.Kind {
		case ChatBlockUser, ChatBlockAssistant, ChatBlockTool, ChatBlockThinking, ChatBlockSystem, ChatBlockDivider:
		default:
			t.Fatalf("unknown kind %q", b.Kind)
		}
	}
}
