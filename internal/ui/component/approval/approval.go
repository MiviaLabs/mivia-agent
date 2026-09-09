// Package approval renders one pending tool-approval request inline and
// turns keypresses into a ports.Decision.
package approval

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// Model holds every pending request and shows the OLDEST.
//
// It used to hold exactly one, replaced on arrival. The agent runs tool calls
// in parallel and each gated call blocks its own goroutine until answered, so
// a second prompt discarded the first and the gate behind it waited for a
// decision that could no longer be made. A session then looked idle while
// being blocked for ever.
//
// Showing the oldest rather than the newest is deliberate: the operator
// answers the prompt they have been reading, and a call that arrives while
// they decide must not move the target under their fingers.
//
// This is still the inline prompt the build spec calls for
// (uikit/config.ApprovalDefaultInline), not a queue-visualising dialog. The
// queue is depth, not chrome: only the head is rendered.
type Model struct {
	Theme theme.Theme
	Tier  theme.Tier

	// pending is the queue, oldest first. The head is what View renders and
	// what a key press answers.
	pending []uievent.ToolPendingBody

	// offset is the first rendered diff line the preview window shows.
	// It is only meaningful while active carries a diff, and it never
	// changes the prompt's height (see View).
	offset int

	// width is the terminal width. The bordered box is padded to it and
	// every diff line is clipped to it, so the border keeps one fixed
	// size while the window scrolls instead of breathing with whichever
	// line is widest (ux-rules.md rule 2.7: moving chrome reflows the
	// reading). 0 means unsized, for tests and non-terminal renders.
	width int
}

// SetWidth records the terminal width so the box renders at a fixed size.
// Call it on WindowSizeMsg, like the composer.
//
// It re-clamps the scroll offset, because the width DECIDES the line count: a
// diff renders unified below render.MinSplitDiffWidth and split above it,
// roughly halving the number of lines. Widening a scrolled prompt therefore
// left the offset past the end of the new content, and View sliced backwards
// and panicked - killing the process, and with it every queued prompt and
// every tool-call goroutine waiting on one.
//
// Reached from Screen.reflow, so it fires on every terminal resize AND every
// files-panel toggle, not only at startup.
func (m *Model) SetWidth(w int) {
	m.width = w
	m.clampOffset()
}

// clampOffset holds the scroll offset inside the content the CURRENT width
// renders.
func (m *Model) clampOffset() {
	max := m.diffTotal() - m.windowHeight()
	if max < 0 {
		max = 0
	}
	m.offset = clamp(m.offset, 0, max)
}

// DecisionMsg is emitted when the user resolves the active request.
type DecisionMsg struct {
	ToolCallID string
	Decision   ports.Decision
}

// New returns a Model with no request pending.
func New(t theme.Theme, tier theme.Tier) Model {
	return Model{Theme: t, Tier: tier}
}

// SetRequest queues a pending tool call. The scroll offset restarts at the
// top when this becomes the head: a new request is a new diff, and a stale
// offset could land the first view halfway through it.
//
// A call already queued is ignored rather than queued twice - one gate cannot
// be answered two times, and a duplicate would leave a prompt the operator
// answers into nothing.
func (m *Model) SetRequest(b uievent.ToolPendingBody) {
	for _, existing := range m.pending {
		if existing.ToolCallID == b.ToolCallID {
			return
		}
	}
	// A fresh slice, never an append into the shared array. Model is passed
	// BY VALUE between the foreground screen and its per-session states
	// (internal/ui/screen/conversation/session.go), so two live headers can
	// share one backing array: appending through one then overwrites a queued
	// prompt held by the other. A lost prompt is a tool-call gate that never
	// returns.
	next := make([]uievent.ToolPendingBody, 0, len(m.pending)+1)
	next = append(next, m.pending...)
	m.pending = append(next, b)
	if len(m.pending) == 1 {
		m.offset = 0
	}
}

// Resolve drops the request for one tool call, wherever it sits in the queue,
// without emitting a decision. It is what a tool starting or ending reports:
// that call is no longer waiting on the operator.
//
// It is per-call ON PURPOSE. This used to be a blanket Clear, so a parallel
// call reaching tool.start dismissed the prompt for a DIFFERENT call that was
// still waiting - and that gate then blocked with nothing on screen to answer.
func (m *Model) Resolve(toolCallID string) {
	for i, req := range m.pending {
		if req.ToolCallID != toolCallID {
			continue
		}
		// Copy rather than reslice in place: an in-place removal writes through
		// the shared array and corrupts a copy of this Model, which can
		// resurrect a resolved prompt or drop a queued one.
		next := make([]uievent.ToolPendingBody, 0, len(m.pending)-1)
		next = append(next, m.pending[:i]...)
		next = append(next, m.pending[i+1:]...)
		m.pending = next
		if i == 0 {
			// The head changed, so the diff behind it did too.
			m.offset = 0
		}
		return
	}
}

// ClearAll drops every pending request. The turn that produced them has
// ended, so no decision can reach a gate any more and showing a prompt would
// invite an answer that goes nowhere.
func (m *Model) ClearAll() {
	m.pending = nil
	m.offset = 0
}

// Active reports whether a request is currently awaiting a decision.
func (m Model) Active() bool { return len(m.pending) > 0 }

// Waiting is how many calls are queued behind and including the head.
//
// borderLabel uses it to say that answering this prompt will not unblock the
// turn on its own. It previously said the statusline did, and nothing did:
// the function had no caller at all, so a queue of five write calls looked
// exactly like one and the operator learned about the rest only by answering
// and watching the prompt come back.
func (m Model) Waiting() int { return len(m.pending) }

// head returns the request being rendered, or nil when the queue is empty.
//
// It returns a COPY. A pointer into m.pending would be a pointer into an array
// shared with every value copy of this Model, so a write through it would
// reach queues this Model does not own. Nothing writes through it today; the
// copy is what keeps that true without depending on nobody ever trying.
func (m Model) head() *uievent.ToolPendingBody {
	if len(m.pending) == 0 {
		return nil
	}
	req := m.pending[0]
	return &req
}

// Update ignores every Msg except a key press while a request is active.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok || len(m.pending) == 0 {
		return m, nil
	}
	// Key set is wireframes-panes.md section 7, exactly: o once, a always,
	// d deny, D deny always. Enter takes once, Esc is deny.
	//
	// "d" is deny-ONCE. An earlier version of this file mapped "d" to
	// DecisionDenyAlways, so a user pressing d granted a standing
	// session-wide denial they did not ask for. Keep d and D distinct.
	var decision ports.Decision
	switch key.String() {
	case "o", "enter":
		decision = ports.DecisionOnce
	case "a":
		decision = ports.DecisionAlways
	case "d", "esc":
		decision = ports.DecisionDeny
	case "D", "shift+d":
		// Both spellings: a terminal sending Text="D" reports "D", but a
		// key event carrying only Code='d' plus ModShift reports
		// "shift+d". Verified against charm.land/bubbletea/v2 Key.String.
		decision = ports.DecisionDenyAlways
	default:
		return m, nil
	}
	id := m.pending[0].ToolCallID
	// Copy: the receiver is a value, but the slice header is shared with the
	// caller's Model, so reslicing in place would edit theirs too.
	m.pending = append([]uievent.ToolPendingBody(nil), m.pending[1:]...)
	m.offset = 0
	return m, func() tea.Msg { return DecisionMsg{ToolCallID: id, Decision: decision} }
}

// View renders the prompt, or "" when nothing is pending.
//
// The diff preview is a FIXED-height window (ApprovalDiffPreviewLines)
// into the full diff at the current scroll offset, with a position row
// ("lines X-Y of Z") when the diff is longer than the window. Fixed
// height matters: scrolling must never change the rows the prompt
// claims, or every wrapped line above it reflows on every keystroke
// (ux-rules.md rule 2.7).
func (m Model) View() string {
	if m.head() == nil {
		return ""
	}
	// Every chrome row is clipped to the wrap width before it enters the
	// box: lipgloss wraps a row that does not fit, and a wrapped row is a
	// row Height() does not claim - the box would push into the composer.
	// The title is the realistic breaker (a long command's arguments).
	clip := func(s string) string {
		if m.width > 4 {
			return ansi.Truncate(s, m.width-4, "")
		}
		return s
	}
	label, folded := m.borderLabel()
	// The hint states the complete truth for this state: every key listed
	// works, and no key that works is omitted. The scroll keys live in the
	// keymap's approval context; they are absent here because this line
	// names decision keys only - scrolling has its own position row.
	hint := clip(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
		"o once    a always    d deny    D deny always"))
	var body string
	if !folded {
		// The action did not fit the border row, so it stays where it has
		// always been. Dropping it would hide WHAT is being approved.
		body = clip(render.Role(m.Theme, m.Tier, theme.RoleWarning).Bold(true).
			Render("approve "+m.action())) + "\n"
	}
	if diff := m.diffWindow(); len(diff) > 0 {
		body += strings.Join(diff, "\n") + "\n"
		if m.scrollable() {
			body += clip(render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render(
				fmt.Sprintf("lines %d-%d of %d  up/down:scroll",
					m.offset+1, m.offset+len(diff), m.diffTotal()))) + "\n"
		}
	}
	body += hint
	// RoleBorder, the decorative role, not RoleBorderFocus: the state this
	// prompt carries is now said by the border LABEL, which is text in the
	// warning role and carries its own contrast, so the frame is back to
	// being frame. Same rule as the composer's: the box spans the full
	// terminal width, one fixed size.
	inner := 0
	if m.width > 0 {
		inner = m.width
	}
	return render.BorderedWithHint(m.Theme, m.Tier, theme.RoleBorder, theme.RoleWarning, inner, body, label)
}

// action names what this request would do: the tool and its arguments.
func (m Model) action() string {
	if m.head() == nil {
		return ""
	}
	h := m.head()
	return strings.TrimSpace(h.Name + " " + render.FormatToolDetail(h.Name, h.Args))
}

// borderLabel is the text of the top border row, and whether the action
// went into it.
//
// The state badge always rides there. The action joins it when the row
// can carry the pair whole - a truncated action would name the wrong
// path or the wrong command, which is worse than putting it back in the
// body. The second return is what View and Height agree on, so the row
// count never depends on two separate readings of the same rule.
func (m Model) borderLabel() (string, bool) {
	badge := "⚠ Approval Required"
	if m.Tier == theme.TierASCII || m.Tier == theme.TierNoTTY {
		badge = "! Approval Required"
	}
	// The queue depth belongs here rather than on the hint row. The hint is
	// clipped to the width and promises to name every key that works;
	// appending to it would let a narrow terminal truncate a live key. The
	// badge is already the part that survives the fold below.
	if behind := m.Waiting() - 1; behind > 0 {
		badge += fmt.Sprintf(" (%d more)", behind)
	}
	// Bracketed like the composer's own top-border hint: the two frames
	// sit one above the other, and matching chrome is what makes the
	// prompt read as part of the same surface rather than an alarm.
	action := m.action()
	if action == "" {
		return "[ " + badge + " ]", true
	}
	if full := "[ " + badge + " - " + action + " ]"; render.HintFits(m.width, full) {
		return full, true
	}
	return "[ " + badge + " ]", false
}

// ScrollBy moves the diff preview window by n lines and returns the
// model. The offset clamps to [0, total-window], so scrolling past
// either end is a no-op, never an empty window.
func (m Model) ScrollBy(n int) Model {
	if !m.scrollable() {
		return m
	}
	max := m.diffTotal() - m.windowHeight()
	m.offset = clamp(m.offset+n, 0, max)
	return m
}

func (m Model) diffLines() []string {
	if m.head() == nil || m.head().Diff == nil {
		return nil
	}
	return render.FormatDiffLines(m.Theme, m.Tier, m.width-4, *m.head().Diff)
}

// diffTotal is the full rendered line count of the pending diff.
func (m Model) diffTotal() int {
	return len(m.diffLines())
}

// windowHeight is how many diff lines the preview shows at once.
func (m Model) windowHeight() int {
	return min(m.diffTotal(), uikitconfig.ApprovalDiffPreviewLines)
}

// scrollable reports whether the diff is longer than its window.
func (m Model) scrollable() bool {
	return m.diffTotal() > uikitconfig.ApprovalDiffPreviewLines
}

// diffWindow is the styled diff lines currently visible, each clipped to
// the box's inner width: an over-long line would widen (or wrap inside)
// the border and move it while the user scrolls.
func (m Model) diffWindow() []string {
	lines := m.diffLines()
	if len(lines) == 0 {
		return nil
	}
	// Clamp the START too, not only the end. Clamping one side alone is what
	// let a stale offset slice backwards; a render must never be able to panic
	// on state a caller left behind, whatever that caller forgot.
	start := m.offset
	if start > len(lines) {
		start = len(lines)
	}
	if start < 0 {
		start = 0
	}
	end := start + m.windowHeight()
	if end > len(lines) {
		end = len(lines)
	}
	window := lines[start:end]
	// Clip to the box's effective wrap width, not the inner width:
	// lipgloss counts the border cells inside Width(), and a line it
	// wraps would add a row Height() does not claim - the box would push
	// into the composer.
	if m.width > 4 {
		wrap := m.width - 4
		for i, ln := range window {
			if ansi.StringWidth(ln) > wrap {
				window[i] = ansi.Truncate(ln, wrap, "")
			}
		}
	}
	return window
}

// Height is the number of terminal rows View() claims, border included,
// so the enclosing screen can reserve them without re-rendering. It is
// 0 when nothing is pending, and CONSTANT while scrolling: the window
// never grows or shrinks with the offset.
func (m Model) Height() int {
	if m.head() == nil {
		return 0
	}
	rows := 1 // the decision-key hint
	if _, folded := m.borderLabel(); !folded {
		rows++ // the action, back in the body
	}
	if n := m.windowHeight(); n > 0 {
		rows += n
		if m.scrollable() {
			rows++ // the position row
		}
	}
	return rows + 2 // the border's top and bottom rows
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
