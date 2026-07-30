package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ── Tables ────────────────────────────────────────────────────────────
// Tables were unreadable: the header row rendered identically to data
// (the GFM separator was dropped and nothing replaced it), there were no
// box borders, and pipe-less GFM tables were not detected at all.

func TestTableHasHeaderRuleAndBorders(t *testing.T) {
	out := stripANSI(RenderMarkdown("| Col A | Col B |\n|---|---|\n| one | two |", 60))
	lines := strings.Split(out, "\n")
	if len(lines) < 5 {
		t.Fatalf("table needs top border, header, rule, body, bottom border:\n%s", out)
	}
	if !strings.Contains(lines[0], "┌") || !strings.Contains(lines[0], "┐") {
		t.Fatalf("missing top border: %q", lines[0])
	}
	if !strings.Contains(lines[2], "├") || !strings.Contains(lines[2], "┤") {
		t.Fatalf("missing header rule under the header row: %q", lines[2])
	}
	if !strings.Contains(lines[len(lines)-1], "└") {
		t.Fatalf("missing bottom border: %q", lines[len(lines)-1])
	}
	if !strings.Contains(out, "Col A") || !strings.Contains(out, "two") {
		t.Fatalf("table lost content:\n%s", out)
	}
}

func TestTableWithoutOuterPipesRenders(t *testing.T) {
	// GFM allows omitting leading/trailing pipes; models emit this constantly
	// and it used to fall through as raw text.
	src := "Col A | Col B\n--- | ---\none | two"
	out := stripANSI(RenderMarkdown(src, 60))
	if strings.Contains(out, "---") {
		t.Fatalf("separator row leaked as text — table not detected:\n%s", out)
	}
	if !strings.Contains(out, "┌") {
		t.Fatalf("pipe-less table not rendered as a table:\n%s", out)
	}
	if !strings.Contains(out, "Col A") || !strings.Contains(out, "two") {
		t.Fatalf("table lost content:\n%s", out)
	}
}

func TestTableSingleLineNotTreatedAsTable(t *testing.T) {
	// A lone pipe line is prose (e.g. "a | b"), not a table: without a GFM
	// separator there is no table to build.
	out := stripANSI(RenderMarkdown("value a | value b", 60))
	if strings.Contains(out, "┌") {
		t.Fatalf("prose with a pipe became a table:\n%s", out)
	}
}

func TestTableNeverExceedsWidth(t *testing.T) {
	src := "| Component | Verdict | Notes |\n|---|---|---|\n| markdown renderer | broken | " +
		strings.Repeat("very long note ", 12) + "|"
	for _, w := range []int{40, 60, 100} {
		out := stripANSI(RenderMarkdown(src, w))
		for _, line := range strings.Split(out, "\n") {
			if visibleWidth(line) > w {
				t.Fatalf("width=%d: line %d wide: %q", w, visibleWidth(line), line)
			}
		}
	}
}

// ── Diffs ─────────────────────────────────────────────────────────────
// File edits are the agent's most consequential output, so the transcript
// shows the diff itself rather than a bare "updated x.go" line.

func TestEditToolBlockShowsDiffStat(t *testing.T) {
	block := ChatBlock{
		Kind: ChatBlockTool, ToolName: "search_replace", Collapsed: true,
		Text:    "updated internal/cli/x.go (1 replacement, +3 −1)\n--- a/internal/cli/x.go\n+++ b/internal/cli/x.go\n-old\n+new\n+more\n+lines",
		Elapsed: 200 * time.Millisecond,
	}
	out := stripANSI(strings.Join(renderOneChatBlock(block, "m", 80, false), "\n"))
	if !strings.Contains(out, "+") || !strings.Contains(out, "−") && !strings.Contains(out, "-") {
		t.Fatalf("collapsed edit row must carry a +/− stat: %q", out)
	}
	if !strings.Contains(out, "x.go") {
		t.Fatalf("collapsed edit row must name the file: %q", out)
	}
}

func TestDiffBodyGuttersAndTruncationIsExplicit(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a/x.go\n+++ b/x.go\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "+added line %d\n", i)
	}
	lines := renderDiffBody(b.String(), 80, 20)
	if len(lines) > 22 {
		t.Fatalf("diff body ignored its cap: %d lines", len(lines))
	}
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "omitted") {
		t.Fatalf("truncation must be explicit: %q", joined)
	}
	// Added/removed lines carry a visible gutter marker.
	if !strings.Contains(joined, "+added line 0") {
		t.Fatalf("diff lines lost their +/- gutter: %q", joined)
	}
}

// ── Work group scrolling ──────────────────────────────────────────────
// An expanded group used to dump every member; the cap made it stop at 30
// with no way to see the rest. It is now a fixed-height scrollable window.

func manyToolBlocks(n int) []ChatBlock {
	var blocks []ChatBlock
	for i := 0; i < n; i++ {
		blocks = append(blocks, ChatBlock{
			ID: fmt.Sprintf("b%d", i), Kind: ChatBlockTool,
			ToolName: "read_file", Text: fmt.Sprintf("file-%03d.go", i), Collapsed: true,
		})
	}
	return blocks
}

func TestExpandedWorkGroupIsBoundedWindow(t *testing.T) {
	blocks := manyToolBlocks(120)
	groups := findWorkGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("groups=%d", len(groups))
	}
	key := groups[0].Key
	out := RenderChatBlocksWithWorkGroupsWindow(blocks, "m", 90, false,
		map[string]bool{key: false}, map[string]int{}, railView{})
	plain := stripANSI(strings.Join(out.Lines, "\n"))
	if got := strings.Count(plain, "read_file"); got > workGroupWindowRows {
		t.Fatalf("window shows %d rows, cap is %d", got, workGroupWindowRows)
	}
	if !strings.Contains(plain, "↓") {
		t.Fatalf("must indicate more rows below:\n%s", plain)
	}
	if !strings.Contains(plain, "file-000.go") {
		t.Fatalf("window must start at the top:\n%s", plain)
	}
}

func TestWorkGroupWindowScrolls(t *testing.T) {
	blocks := manyToolBlocks(120)
	key := findWorkGroups(blocks)[0].Key
	scroll := map[string]int{key: 40}
	out := RenderChatBlocksWithWorkGroupsWindow(blocks, "m", 90, false,
		map[string]bool{key: false}, scroll, railView{})
	plain := stripANSI(strings.Join(out.Lines, "\n"))
	if strings.Contains(plain, "file-000.go") {
		t.Fatalf("scrolled window still shows the first row:\n%s", plain)
	}
	if !strings.Contains(plain, "file-040.go") {
		t.Fatalf("scrolled window should start at offset 40:\n%s", plain)
	}
	if !strings.Contains(plain, "↑") {
		t.Fatalf("must indicate rows above:\n%s", plain)
	}
}

func TestWorkGroupScrollKeysWhenSelected(t *testing.T) {
	m := newReadyChatModel(40, 90)
	m.blocks = manyToolBlocks(60)
	key := findWorkGroups(m.blocks)[0].Key
	m.workGroupCollapsed = map[string]bool{key: false}
	m.renderVP()
	m.focus = focusScrollback
	m.selectedBlockID = key

	m.handleChatKey("j", false)
	if m.workGroupScroll[key] == 0 {
		t.Fatal("j must scroll the selected work group window")
	}
	down := m.workGroupScroll[key]
	m.handleChatKey("k", false)
	if m.workGroupScroll[key] >= down {
		t.Fatal("k must scroll back up")
	}
	// Clamped at the top.
	for i := 0; i < 10; i++ {
		m.handleChatKey("k", false)
	}
	if m.workGroupScroll[key] != 0 {
		t.Fatalf("scroll must clamp at 0, got %d", m.workGroupScroll[key])
	}
}

// ── Table row separators + wrapping ───────────────────────────────────

func TestTableHasRowSeparators(t *testing.T) {
	out := stripANSI(RenderMarkdown("| A | B |\n|---|---|\n| one | two |\n| three | four |", 60))
	lines := strings.Split(out, "\n")
	rules := 0
	for _, l := range lines {
		if strings.Contains(l, "├") {
			rules++
		}
	}
	// One rule under the header + one between the two data rows.
	if rules != 2 {
		t.Fatalf("want a rule under the header and between rows, got %d:\n%s", rules, out)
	}
}

func TestTableCellsWrapInsteadOfTruncating(t *testing.T) {
	long := "this cell holds a lot of text that cannot possibly fit on a single line"
	src := "| Key | Notes |\n|---|---|\n| k | " + long + " |"
	out := stripANSI(RenderMarkdown(src, 50))
	if strings.Contains(out, "…") {
		t.Fatalf("cell was truncated instead of wrapped:\n%s", out)
	}
	// Every word survives somewhere in the block.
	for _, word := range []string{"this", "possibly", "single", "line"} {
		if !strings.Contains(out, word) {
			t.Fatalf("wrapping lost %q:\n%s", word, out)
		}
	}
	// Row height grew: more lines than a 1-row table would need.
	if n := strings.Count(out, "\n") + 1; n < 6 {
		t.Fatalf("row height did not grow (%d lines):\n%s", n, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if visibleWidth(line) > 50 {
			t.Fatalf("wrapped table exceeded width: %q", line)
		}
	}
}

func TestTableWrapKeepsStyledCellsAligned(t *testing.T) {
	src := "| Key | Notes |\n|---|---|\n| `k` | **bold text that will certainly need to wrap across lines** |"
	out := RenderMarkdown(src, 46)
	var w int
	for _, line := range strings.Split(stripANSI(out), "\n") {
		if w == 0 {
			w = visibleWidth(line)
		}
		if visibleWidth(line) != w {
			t.Fatalf("ragged table edges (%d vs %d):\n%s", visibleWidth(line), w, stripANSI(out))
		}
	}
}

// ── Turn divider ──────────────────────────────────────────────────────

func TestEmptyTurnDividerRendersNothing(t *testing.T) {
	// The bare "─── · ───" rule carried no information and just ate a row.
	lines := renderOneChatBlock(ChatBlock{Kind: ChatBlockDivider}, "m", 60, false)
	for _, l := range lines {
		if strings.Contains(stripANSI(l), "·") {
			t.Fatalf("empty divider still renders a rule: %q", stripANSI(l))
		}
	}
	// A divider WITH text (the turn footer) still renders.
	lines = renderOneChatBlock(ChatBlock{Kind: ChatBlockDivider, Text: "  ─ turn 3 · 4.0s ─"}, "m", 60, false)
	if len(lines) == 0 || !strings.Contains(stripANSI(strings.Join(lines, "")), "turn 3") {
		t.Fatalf("turn footer divider must still render: %q", lines)
	}
}
