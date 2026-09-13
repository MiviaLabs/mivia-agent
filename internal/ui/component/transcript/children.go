package transcript

import (
	"slices"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

// This file holds C7's compact child tree under a dispatch row
// (docs/design/chat-tui-crush-comparison.md §3 C7): the child tool calls a
// dispatched subagent made, drawn as a few compact body rows of the parent
// dispatch_tasks block so the reader sees what the batch DID without opening
// the thread dialog.
//
// The data path is one-way and screen-owned: the root transcript's subagent
// progress events carry counters and log text only, so the conversation
// screen reads ports.SubagentThreads.Thread(callID).History()[].ToolCalls -
// which it already owns for openThread - and pushes the summary in here
// through SetChildren. The transcript never imports the adapter (INV-TUI-29);
// ChildCall is the narrow vocabulary the two sides share.

// ChildCall is one compact child row under a dispatch block: the tool the
// dispatched subagent ran, a short detail, and the call's own outcome.
// Detail is presentation, built by the screen from the call's arguments;
// the transcript only draws it.
type ChildCall struct {
	Name   string
	Detail string
	OK     bool
}

// maxChildRows caps the tree before it stops being compact. Past six rows
// the count says more than the names would, the same trade the work-run row
// and the dispatch result formatter already make.
const maxChildRows = 6

// SetChildren attaches the child tree to the LAST block tracking callID,
// the same live-block rule updateLive applies. A call the transcript never
// saw (a foreign tool, an evicted block) is a quiet no-op: progress for it
// has no row to land on. An empty list is also a no-op rather than a clear -
// the tree is refreshed from history on every progress event, and a momentary
// empty read must not blink the rows off the screen.
func (m *Model) SetChildren(callID string, children []ChildCall) {
	if callID == "" {
		return
	}
	i := -1
	for j := len(m.blocks) - 1; j >= 0; j-- {
		if m.blocks[j].CallID == callID && m.blocks[j].isToolBlock() {
			i = j
			break
		}
	}
	if i < 0 {
		return
	}
	if len(children) == 0 {
		return
	}
	m.blocks = slices.Clone(m.blocks)
	blk := m.blocks[i]
	blk.Children = slices.Clone(children)
	m.blocks[i] = blk
	m.clampOffset()
}

// childRowCount is how many terminal rows the child tree contributes: at
// most maxChildRows child rows, plus the "+N more" count when the tree was
// capped. Height and Render both derive from this method, so they cannot
// disagree about the block's own row count.
func (b Block) childRowCount() int {
	if len(b.Children) == 0 {
		return 0
	}
	n := len(b.Children)
	if n > maxChildRows {
		n = maxChildRows
	}
	if more := len(b.Children) - maxChildRows; more > 0 {
		n++
	}
	return n
}

// childRows draws the tree: one compact row per child call - the outcome
// glyph, the tool name, the detail - then the "+N more" count when the tree
// was capped. The glyph carries the state ("+" ok, "x" failed) so the tree
// survives NO_COLOR with only its shape (ux-rules 9.4); rows clip at the
// body width rather than wrapping, because one row per child IS the compact
// contract and a wrapped row would double the tree's height unbidden.
func (b Block) childRows(t theme.Theme, tier theme.Tier, width int) []string {
	if len(b.Children) == 0 {
		return nil
	}
	inner := width - uikitconfig.BodyIndent
	if inner <= 0 {
		inner = width
	}
	shown := b.Children
	more := 0
	if len(shown) > maxChildRows {
		more = len(shown) - maxChildRows
		shown = shown[:maxChildRows]
	}
	out := make([]string, 0, len(shown)+1)
	for _, c := range shown {
		glyph, role := "+", theme.RoleSuccess
		if !c.OK {
			glyph, role = "x", theme.RoleDanger
		}
		line := render.Role(t, tier, role).Render(glyph) + " " +
			render.Role(t, tier, theme.RoleFGSubtle).Render(c.Name)
		if c.Detail != "" {
			line += " " + render.Role(t, tier, theme.RoleFG).Render(c.Detail)
		}
		out = append(out, ansi.Truncate(line, inner, ""))
	}
	if more > 0 {
		out = append(out, render.Role(t, tier, theme.RoleFGSubtle).Render("+"+itoa(more)+" more"))
	}
	return out
}
