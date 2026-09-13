package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// dispatchTreeModel builds a transcript holding one settled dispatch_tasks
// call, the way the screen sees one: a live start block merged into a
// terminal tool.end by CallID.
func dispatchTreeModel(t *testing.T) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "dispatch-1", Name: "dispatch_tasks",
			Args: map[string]any{"tasks": []any{map[string]any{"id": "task-a"}}}},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: "dispatch-1", Name: "dispatch_tasks", OK: true,
			Result: `[{"task_id":"task-a","status":"completed"}]`,
		},
	})
	return m
}

// eightChildren is one child call per letter, the 6th failed, so the cap and
// both glyphs are exercised by one table - the failed call stays INSIDE the
// shown window, where a reader can see it.
func eightChildren() []ChildCall {
	children := make([]ChildCall, 0, 8)
	for _, c := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		children = append(children, ChildCall{Name: "edit", Detail: c + ".go", OK: c != "f"})
	}
	return children
}

// TestSetChildrenRendersChildTree pins C7's row contract: at most
// maxChildRows compact child rows under the parent block, then the "+N more"
// count, with the outcome glyph carrying the state (so the tree survives
// NO_COLOR, ux-rules 9.4). The child rows are BODY rows: Height and Render
// must agree on them, or the viewport budget lies.
func TestSetChildrenRendersChildTree(t *testing.T) {
	m := dispatchTreeModel(t)
	m.SetChildren("dispatch-1", eightChildren())

	dump := ansi.Strip(m.Dump())
	if c := strings.Count(dump, "edit a.go"); c != 1 {
		t.Errorf("child row a.go occurrence=%d, want 1:\n%s", c, dump)
	}
	if !strings.Contains(dump, "x edit f.go") {
		t.Errorf("the failed child must render the x glyph:\n%s", dump)
	}
	if strings.Count(dump, "x edit") != 1 {
		t.Errorf("exactly one child failed; dump shows otherwise:\n%s", dump)
	}
	for _, gone := range []string{"g.go", "h.go"} {
		if strings.Contains(dump, "edit "+gone) {
			t.Errorf("child %s is past the %d-row cap and must be counted, not drawn:\n%s", gone, maxChildRows, dump)
		}
	}
	for _, kept := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"} {
		if !strings.Contains(dump, "edit "+kept) {
			t.Errorf("child row %s missing from the tree:\n%s", kept, dump)
		}
	}
	if !strings.Contains(dump, "+2 more") {
		t.Errorf("the capped tail must state its count:\n%s", dump)
	}

	// Height and Render derive from the same count; a disagreement here
	// makes the eviction budget nominal and the View taller than claimed.
	inner := 80 - groupIndent
	blk := m.Blocks()[0]
	if got, want := len(strings.Split(blk.Render(loadTheme(t), theme.TierASCII, inner), "\n")), blk.Height(inner); got != want {
		t.Errorf("Render drew %d rows, Height claims %d", got, want)
	}
}

// TestToolBlockWithChildrenAndNoBodyKeepsItsRows pins the live-batch state:
// a dispatch_tasks call whose child tree arrived on a progress event while
// the call's own body is still empty. Height and Render must agree on that
// block too - a disagreement makes the viewport budget lie, shifting every
// later row (and every click/selection target derived from it) by one.
func TestToolBlockWithChildrenAndNoBodyKeepsItsRows(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "dispatch-1", Name: "dispatch_tasks"}})
	m.SetChildren("dispatch-1", []ChildCall{
		{Name: "read_file", Detail: "a.go", OK: true},
		{Name: "edit", Detail: "b.go", OK: false},
	})

	inner := 80 - groupIndent
	blk := m.Blocks()[0]
	if len(blk.Body) != 0 {
		t.Fatalf("fixture must be a body-less live dispatch block, got %d body rows", len(blk.Body))
	}
	if len(blk.Children) == 0 {
		t.Fatal("fixture must carry a child tree")
	}
	lines := strings.Split(blk.Render(loadTheme(t), theme.TierASCII, inner), "\n")
	if got, want := len(lines), blk.Height(inner); got != want {
		t.Errorf("Render drew %d rows, Height claims %d:\n%s", got, want, strings.Join(lines, "\n"))
	}
}

// TestBlockWithChildrenKeepsItsRowsWhileSiblingsFold pins the work-run
// exemption (layout.go workRunLen): a settled dispatch block with a child
// tree is exactly the block whose content the fold would hide, so it keeps
// its own rows while the finished siblings around it coalesce into the
// "> work" summary row.
func TestBlockWithChildrenKeepsItsRowsWhileSiblingsFold(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "run the batch"}})
	// Three settled calls with distinct names: a work run, not a read run.
	for i, tc := range []struct{ name, result string }{
		{"read_file", "48 lines"},
		{"edit", "patched"},
		{"run_command", "exit=0"},
	} {
		id := string(rune('a' + i))
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: id, Name: tc.name}})
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: id, Name: tc.name, OK: true, Result: tc.result}})
	}
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "dispatch-1", Name: "dispatch_tasks"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: "dispatch-1", Name: "dispatch_tasks", OK: true,
			Result: `[{"task_id":"task-a","status":"completed"}]`,
		}})
	m.SetChildren("dispatch-1", []ChildCall{
		{Name: "read_file", Detail: "a.go", OK: true},
		{Name: "edit", Detail: "b.go", OK: false},
	})

	rows := m.Rows()
	workFolded := false
	for _, row := range rows {
		if strings.Contains(ansi.Strip(row), "> work") {
			workFolded = true
		}
	}
	if !workFolded {
		t.Errorf("the three settled siblings must fold into the work row:\n%s", strings.Join(rows, "\n"))
	}
	dump := ansi.Strip(m.Dump())
	if !strings.Contains(dump, "dispatch_tasks") {
		t.Errorf("the dispatch block lost its own header row:\n%s", dump)
	}
	if !strings.Contains(dump, "+ read_file a.go") || !strings.Contains(dump, "x edit b.go") {
		t.Errorf("the child tree must survive beside the folded siblings:\n%s", dump)
	}
	// The fold must not swallow the dispatch block: the work row carries
	// only the three siblings, so the tree's parent keeps its own header.
	for _, row := range rows {
		if strings.Contains(ansi.Strip(row), "> work") && strings.Contains(ansi.Strip(row), "dispatch_tasks") {
			t.Errorf("the dispatch block joined the work fold:\n%s", strings.Join(rows, "\n"))
		}
	}
}

// TestSetChildrenIsNoOpWithoutAMatch pins the quiet-miss contract: a progress
// event for a call the transcript never saw (a foreign tool, an already
// evicted block) changes nothing, and an empty list clears a tree without
// touching anything else.
func TestSetChildrenIsNoOpWithoutAMatch(t *testing.T) {
	m := dispatchTreeModel(t)
	before := m.Dump()

	m.SetChildren("unknown-call", eightChildren())
	if m.Dump() != before {
		t.Error("SetChildren for an unknown call ID must not change the transcript")
	}

	m.SetChildren("dispatch-1", eightChildren())
	withTree := m.Dump()
	m.SetChildren("dispatch-1", nil)
	if m.Dump() != withTree {
		t.Error("SetChildren with an empty list must be a no-op, not a clear")
	}
}
