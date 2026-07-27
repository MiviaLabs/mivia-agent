package cli

import (
	"strings"
	"testing"
	"time"
)

func TestFocusableBlockIDs_OrderAndExcludesDivider(t *testing.T) {
	blocks := []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "hi"},
		{ID: "d1", Kind: ChatBlockDivider, Text: "───"},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "yo"},
	}
	ranges := map[string][2]int{
		"u1":      {0, 2},
		"d1":      {3, 4},
		"a1":      {5, 7},
		"work:t1": {8, 9},
	}
	ids := focusableBlockIDs(ranges, blocks)
	want := []string{"u1", "a1", "work:t1"}
	if len(ids) != len(want) {
		t.Fatalf("ids=%v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids[%d]=%q want %q full=%v", i, ids[i], want[i], ids)
		}
	}
}

func TestCycleChatFocus_TabFromComposerSelectsFirst(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "hello user"},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "hello asst"},
	}
	m.renderVP()
	m.setFocus(focusComposer)
	m.selectedBlockID = ""

	if !m.cycleChatFocus(false) {
		t.Fatal("cycle should consume tab")
	}
	if m.focus != focusScrollback {
		t.Fatalf("focus=%v want scrollback", m.focus)
	}
	ids := focusableBlockIDs(m.chatBlockRanges, m.blocks)
	if len(ids) == 0 {
		t.Fatal("no focusable ids")
	}
	if m.selectedBlockID != ids[0] {
		t.Fatalf("selected=%q want first %q (all=%v)", m.selectedBlockID, ids[0], ids)
	}
}

func TestCycleChatFocus_TabThroughBlocksThenComposer(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "u"},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "a"},
	}
	m.renderVP()
	ids := focusableBlockIDs(m.chatBlockRanges, m.blocks)
	if len(ids) < 2 {
		t.Fatalf("need ≥2 focusable, got %v ranges=%v", ids, m.chatBlockRanges)
	}

	m.setFocus(focusComposer)
	_ = m.cycleChatFocus(false) // → first
	if m.selectedBlockID != ids[0] {
		t.Fatalf("1st tab selected=%q want %q", m.selectedBlockID, ids[0])
	}
	_ = m.cycleChatFocus(false) // → second
	if m.selectedBlockID != ids[1] {
		t.Fatalf("2nd tab selected=%q want %q", m.selectedBlockID, ids[1])
	}
	_ = m.cycleChatFocus(false) // → composer
	if m.focus != focusComposer || m.selectedBlockID != "" {
		t.Fatalf("after last tab focus=%v sel=%q", m.focus, m.selectedBlockID)
	}
}

func TestCycleChatFocus_WorkGroupChildrenWhenExpanded(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = fourToolWorkBlocks()
	m.renderVP()

	gs := findWorkGroups(m.blocks)
	if len(gs) != 1 {
		t.Fatalf("groups=%d", len(gs))
	}
	key := gs[0].Key

	// Collapsed: work header focusable, tools not all in ranges as separate rows.
	idsCol := focusableBlockIDs(m.chatBlockRanges, m.blocks)
	hasWork := false
	for _, id := range idsCol {
		if id == key {
			hasWork = true
		}
	}
	if !hasWork {
		t.Fatalf("collapsed list missing work key: %v", idsCol)
	}

	// Expand
	m.selectedBlockID = key
	m.focus = focusScrollback
	if !m.toggleSelectedBlock() {
		t.Fatal("expand failed")
	}
	idsExp := focusableBlockIDs(m.chatBlockRanges, m.blocks)
	// Expect work header + tool children
	childCount := 0
	for _, id := range idsExp {
		if id == "t1" || id == "t2" || id == "t3" || id == "t4" {
			childCount++
		}
	}
	if childCount < 4 {
		t.Fatalf("expanded want 4 tool children focusable, got %d ids=%v", childCount, idsExp)
	}
	// Enter on child toggles tool only
	m.selectedBlockID = "t1"
	before := m.blocks[1].Collapsed
	if !m.toggleSelectedBlock() {
		t.Fatal("child toggle failed")
	}
	if m.blocks[1].Collapsed == before {
		t.Fatal("child Collapsed should flip")
	}
	// Group map unchanged (still expanded)
	if m.workGroupCollapsed[key] != false {
		t.Fatalf("group should stay expanded, map=%v", m.workGroupCollapsed)
	}
}

func TestActivateSelected_WorkGroupAndTool(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.blocks = fourToolWorkBlocks()
	m.renderVP()
	gs := findWorkGroups(m.blocks)
	m.selectedBlockID = gs[0].Key
	m.focus = focusScrollback
	if !m.toggleSelectedBlock() {
		t.Fatal("work toggle")
	}
	m.selectedBlockID = "t2"
	if !m.toggleSelectedBlock() {
		t.Fatal("tool toggle")
	}
}

func TestApplySelectionChrome_LineCountStable(t *testing.T) {
	lines := []string{"  one", "  two", "", "  three"}
	ranges := map[string][2]int{"b1": {0, 2}}
	out := applySelectionChrome(lines, ranges, "b1", true)
	if len(out) != len(lines) {
		t.Fatalf("line count %d → %d", len(lines), len(out))
	}
	// blank lane unchanged
	if out[2] != lines[2] {
		t.Fatalf("blank lane mutated: %q", out[2])
	}
	// content lines get bg (ANSI) when focused
	if out[0] == lines[0] && !strings.Contains(out[0], "\033") {
		// may equal if no color env - at least length same
		t.Logf("selection chrome plain mode: %q", out[0])
	}
	noop := applySelectionChrome(lines, ranges, "b1", false)
	if len(noop) != len(lines) {
		t.Fatal("unfocused chrome must not change length")
	}
}

func TestUserBubbleTime_NoSecondsBracketedDimTrailing(t *testing.T) {
	sent := time.Date(2026, 7, 27, 22, 30, 45, 0, time.Local)
	got := formatUserBubbleTime(sent)
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("want brackets: %q", got)
	}
	if strings.Contains(got, ":45") || strings.Count(got, ":") != 1 {
		t.Fatalf("no seconds, one colon: %q", got)
	}
	if !strings.Contains(got, "PM") && !strings.Contains(got, "AM") {
		t.Fatalf("want AM/PM: %q", got)
	}

	lines := UserBubble.Render("hello body", 40, sent)
	var content []string
	for _, ln := range lines {
		if strings.TrimSpace(stripANSI(ln)) != "" {
			content = append(content, stripANSI(ln))
		}
	}
	if len(content) < 2 {
		t.Fatalf("body + meta: %v", content)
	}
	// Body first — first content line is message, not time
	if strings.Contains(content[0], "PM") || strings.Contains(content[0], "AM") {
		t.Fatalf("time must not lead body: %v", content)
	}
	if !strings.Contains(content[0], "hello body") {
		t.Fatalf("first content should be body: %v", content)
	}
	last := content[len(content)-1]
	if !strings.Contains(last, "PM") && !strings.Contains(last, "AM") {
		t.Fatalf("last content should be time meta: %v", content)
	}
	if strings.Contains(last, ":45") {
		t.Fatalf("seconds leaked into meta: %q", last)
	}
}

func TestHandleChatKey_TabCyclesFocus(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "hi"},
		{ID: "a1", Kind: ChatBlockAssistant, Text: "yo"},
	}
	m.renderVP()
	m.setFocus(focusComposer)

	skip, _, _ := m.handleChatKey("tab", false)
	if !skip {
		t.Fatal("tab should skip textarea")
	}
	if m.focus != focusScrollback || m.selectedBlockID == "" {
		t.Fatalf("tab from composer → scrollback+selection, focus=%v sel=%q", m.focus, m.selectedBlockID)
	}
}

func TestCycleChatFocus_WrapToComposerRepaints(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.width = 80
	m.height = 40
	m.ready = true
	m.blocks = []ChatBlock{
		{ID: "u1", Kind: ChatBlockUser, Text: "only"},
	}
	m.renderVP()
	ids := focusableBlockIDs(m.chatBlockRanges, m.blocks)
	if len(ids) == 0 {
		t.Fatal("no ids")
	}
	m.setFocus(focusScrollback)
	m.selectedBlockID = ids[0]
	m.renderVP()
	// Tab past last → composer + clear selection
	_ = m.cycleChatFocus(false)
	if m.focus != focusComposer || m.selectedBlockID != "" {
		t.Fatalf("wrap focus=%v sel=%q", m.focus, m.selectedBlockID)
	}
}
