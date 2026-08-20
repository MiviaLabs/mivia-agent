package approval

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestViewEmptyWhenInactive(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	if got := m.View(); got != "" {
		t.Errorf("got %q, want empty view with no pending request", got)
	}
	if m.Height() != 0 {
		t.Errorf("Height() = %d with no pending request, want 0", m.Height())
	}
}

func TestViewShowsPendingRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command", Args: map[string]any{"cmd": "ls"}})
	if !m.Active() {
		t.Fatal("expected Active() after SetRequest")
	}
	got := m.View()
	for _, want := range []string{"run_command", "cmd=ls", "o once", "D deny always"} {
		if !strings.Contains(got, want) {
			t.Errorf("approval view missing %q:\n%s", want, got)
		}
	}
}

func TestClearDismissesPendingRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	if !m.Active() {
		t.Fatal("expected Active() after SetRequest")
	}
	m.Clear()
	if m.Active() {
		t.Error("expected inactive after Clear()")
	}
	if got := m.View(); got != "" {
		t.Errorf("got %q, want empty view after Clear()", got)
	}
}

func keyMsg(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: s, Code: rune(s[0])}
}

func TestUpdateIgnoresKeysWithNoRequest(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	_, cmd := m.Update(keyMsg("o"))
	if cmd != nil {
		t.Error("expected no Cmd when nothing is pending")
	}
}

// TestDenyOnceIsDistinctFromDenyAlways pins the highest-severity defect
// this component has had: "d" once meant DecisionDenyAlways, so a user
// pressing d - the documented deny-ONCE key - silently granted a
// session-wide standing denial. d and D must never collapse together.
func TestDenyOnceIsDistinctFromDenyAlways(t *testing.T) {
	resolve := func(t *testing.T, key tea.KeyPressMsg) ports.Decision {
		t.Helper()
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(key)
		if cmd == nil {
			t.Fatalf("key %q produced no decision", key.String())
		}
		return cmd().(DecisionMsg).Decision
	}

	if got := resolve(t, keyMsg("d")); got != ports.DecisionDeny {
		t.Errorf("d resolved %v, want DecisionDeny (deny once)", got)
	}
	// Both spellings a terminal may produce for shift+d.
	for _, k := range []tea.KeyPressMsg{
		{Text: "D", Code: 'D'},
		{Code: 'd', Mod: tea.ModShift},
	} {
		if got := resolve(t, k); got != ports.DecisionDenyAlways {
			t.Errorf("%q resolved %v, want DecisionDenyAlways", k.String(), got)
		}
	}
}

func TestUpdateDecisions(t *testing.T) {
	cases := []struct {
		key  string
		want ports.Decision
	}{
		{"o", ports.DecisionOnce},
		{"a", ports.DecisionAlways},
		{"d", ports.DecisionDeny},
		{"D", ports.DecisionDenyAlways},
		// Enter and Esc were unbound by any test. Enter silently meaning
		// deny-always is the same class of defect as "d" once was, and it
		// would have shipped unnoticed.
		{"enter", ports.DecisionOnce},
		{"esc", ports.DecisionDeny},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			m := New(loadTheme(t), theme.TierASCII)
			m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
			next, cmd := m.Update(keyMsg(c.key))
			if cmd == nil {
				t.Fatal("expected a DecisionMsg Cmd")
			}
			msg := cmd()
			dm, ok := msg.(DecisionMsg)
			if !ok {
				t.Fatalf("got %T, want DecisionMsg", msg)
			}
			if dm.ToolCallID != "c1" || dm.Decision != c.want {
				t.Errorf("got %+v, want ToolCallID=c1 Decision=%v", dm, c.want)
			}
			if next.Active() {
				t.Error("expected the request to be cleared after a decision")
			}
		})
	}
}

// TestEnterAndEscAreNotStandingDenials states the dangerous mistake
// directly. A standing session-wide denial the user never asked for is
// the failure this file exists to prevent.
func TestEnterAndEscAreNotStandingDenials(t *testing.T) {
	for _, k := range []string{"enter", "esc"} {
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%q produced no decision", k)
		}
		if got := cmd().(DecisionMsg).Decision; got == ports.DecisionDenyAlways {
			t.Errorf("%q resolved to a standing denial", k)
		}
	}
}

// TestStructuralEnterAndEscape covers the other spelling a terminal may
// send: a key event carrying a Code rather than Text.
func TestStructuralEnterAndEscape(t *testing.T) {
	cases := []struct {
		msg  tea.KeyPressMsg
		want ports.Decision
	}{
		{tea.KeyPressMsg{Code: tea.KeyEnter}, ports.DecisionOnce},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, ports.DecisionDeny},
	}
	for _, c := range cases {
		m := New(loadTheme(t), theme.TierASCII)
		m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
		_, cmd := m.Update(c.msg)
		if cmd == nil {
			t.Fatalf("%v produced no decision", c.msg)
		}
		if got := cmd().(DecisionMsg).Decision; got != c.want {
			t.Errorf("%v resolved %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestUpdateIgnoresNonKeyMsgWithARequestArmed covers the guard's other
// arm: a request IS armed, but the Msg is not a key press.
func TestUpdateIgnoresNonKeyMsgWithARequestArmed(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Error("a resize resolved an approval")
	}
	if !next.Active() {
		t.Error("a resize cleared the pending request")
	}
}

func TestUpdateIgnoresUnknownKey(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1"})
	next, cmd := m.Update(keyMsg("x"))
	if cmd != nil {
		t.Error("expected no Cmd for an unrecognised key")
	}
	if !next.Active() {
		t.Error("expected the request to remain pending after an unrecognised key")
	}
}

// TestViewDrawsABorderAtEveryTier pins the box: the prompt is framed by
// a RoleBorderFocus border at every colour tier, and the frame degrades
// to plain glyphs (no escape sequences) when the tier carries no colour.
func TestViewDrawsABorderAtEveryTier(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})

	colored := m.View()
	for _, glyph := range []string{"╭", "╰", "│"} {
		if !strings.Contains(colored, glyph) {
			t.Errorf("true-color view missing border glyph %q:\n%s", glyph, colored)
		}
	}
	if !strings.Contains(colored, "\x1b[") {
		t.Error("true-color view has no colour escape; the border role must colour it")
	}

	plain := New(th, theme.TierASCII)
	plain.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	// Structural emphasis (the bold title) survives NO_COLOR by design, so
	// the assertion targets colour escapes, not all escapes.
	plainView := plain.View()
	for _, colour := range []string{"38;2", "38;5", "\x1b[3"} {
		if strings.Contains(plainView, colour) {
			t.Errorf("ASCII view carries colour %q:\n%s", colour, plainView)
		}
	}
	if !strings.Contains(plainView, "╭") {
		t.Errorf("ASCII view lost the border:\n%s", plainView)
	}
}

// previewDiff builds a diff of hunkLines lines in one hunk.
func previewDiff(t *testing.T, hunkLines int) *uievent.Diff {
	t.Helper()
	lines := make([]uievent.DiffLine, hunkLines)
	for i := range lines {
		lines[i] = uievent.DiffLine{Kind: uievent.DiffLineAdd, Text: fmt.Sprintf("line %d", i)}
	}
	return &uievent.Diff{
		Path: "internal/ui/component/approval/approval.go", Added: hunkLines,
		Hunks: []uievent.DiffHunk{{Header: "@@ -1,1 +1,2 @@", Lines: lines}},
	}
}

// TestViewShowsDiffPreview pins the wiring: a pending file-edit with a
// diff renders the diff inside the border, above the hint, and the hint
// line stays complete.
func TestViewShowsDiffPreview(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "edit_file",
		Args: map[string]any{"path": "a.go"}, Diff: previewDiff(t, 3),
	})
	got := m.View()
	for _, want := range []string{"approve edit_file", "@@ -1,1 +1,2 @@", "+ line 0", "+ line 2", "o once    a always    d deny    D deny always"} {
		if !strings.Contains(got, want) {
			t.Errorf("approval view missing %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "line 2"), strings.Index(got, "o once"); i > j || j < 0 {
		t.Errorf("hint must come after the diff preview:\n%s", got)
	}
}

// TestViewWindowsAndScrollsLongDiffs pins the scrollable preview: a
// diff beyond ApprovalDiffPreviewLines shows a fixed window with a
// position row, ScrollBy moves the window and clamps at both ends, the
// height never changes with the offset, and Height() agrees with the
// rows View() actually claims.
func TestViewWindowsAndScrollsLongDiffs(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	d := previewDiff(t, 30) // 31 rendered lines, window 10
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "edit_file", Diff: d})
	got := m.View()
	if !strings.Contains(got, "lines 1-10 of 31") {
		t.Errorf("windowed view missing the position row:\n%s", got)
	}
	if strings.Contains(got, "+ line 10") {
		t.Errorf("window shows a line beyond the window:\n%s", got)
	}
	rows := len(strings.Split(got, "\n"))
	if m.Height() != rows {
		t.Errorf("Height() = %d, want %d (View's row count)", m.Height(), rows)
	}

	m = m.ScrollBy(5)
	if !strings.Contains(m.View(), "lines 6-15 of 31") {
		t.Errorf("scrolled view missing position 6-15:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "+ line 10") {
		t.Errorf("scrolled view does not show line 10:\n%s", m.View())
	}
	if m.Height() != rows {
		t.Errorf("Height changed with offset: %d, want %d", m.Height(), rows)
	}

	m = m.ScrollBy(1000) // clamp at the end
	if !strings.Contains(m.View(), "lines 22-31 of 31") {
		t.Errorf("unclamped scroll:\n%s", m.View())
	}
	m = m.ScrollBy(-1000) // clamp at the start
	if !strings.Contains(m.View(), "lines 1-10 of 31") {
		t.Errorf("unclamped scroll-up:\n%s", m.View())
	}
}

// TestScrollByIsANoOpWithoutADiff: a pending tool call with no diff has
// nothing to window, so scroll keys and the wheel must do nothing
// rather than panic or shift state.
func TestScrollByIsANoOpWithoutADiff(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command"})
	before := m.View()
	m = m.ScrollBy(3)
	if m.View() != before || m.Height() != len(strings.Split(before, "\n")) {
		t.Error("scroll changed the diff-less prompt")
	}
}

// TestSetRequestResetsTheScrollOffset: a second pending request starts
// at the top of its own diff, not wherever the previous one was left.
func TestSetRequestResetsTheScrollOffset(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Diff: previewDiff(t, 30)})
	m = m.ScrollBy(20)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c2", Diff: previewDiff(t, 30)})
	if !strings.Contains(m.View(), "lines 1-10 of 31") {
		t.Errorf("new request kept a stale offset:\n%s", m.View())
	}
}

// TestScrollDoesNotEatDecisionKeys pins the routing: the scroll window
// moves on up/down, and the decision keys still resolve while it is
// scrolled.
func TestScrollDoesNotEatDecisionKeys(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetRequest(uievent.ToolPendingBody{ToolCallID: "c1", Diff: previewDiff(t, 30)})
	m = m.ScrollBy(4)
	next, cmd := m.Update(keyMsg("o"))
	if cmd == nil {
		t.Fatal("o no longer resolves after scrolling")
	}
	if next.Active() {
		t.Error("approve did not clear the request")
	}
}

// TestBorderKeepsAFixedSizeWhileScrolling pins the defect a scrolling
// box invites: lipgloss sizes a border to its widest line, so the box
// breathed with every scroll step. With a width set, every rendered row
// must be the same width at every offset.
func TestBorderKeepsAFixedSizeWhileScrolling(t *testing.T) {
	long := make([]uievent.DiffLine, 30)
	for i := range long {
		long[i] = uievent.DiffLine{Kind: uievent.DiffLineAdd,
			Text: strings.Repeat("x", 20+i) + fmt.Sprintf(" %d", i)}
	}
	m := New(loadTheme(t), theme.TierASCII)
	m.SetWidth(40)
	m.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "edit_file",
		Diff: &uievent.Diff{Path: "a.go", Hunks: []uievent.DiffHunk{{Header: "@@ -1 +1 @@", Lines: long}}},
	})

	rowWidths := func(v string) []int {
		var ws []int
		for _, ln := range strings.Split(v, "\n") {
			ws = append(ws, ansi.StringWidth(ln))
		}
		return ws
	}
	want := rowWidths(m.View())
	for i := 0; i < 35; i++ {
		m = m.ScrollBy(1)
		got := rowWidths(m.View())
		if len(got) != len(want) {
			t.Fatalf("offset %d: %d rows, want %d", m.offset, len(got), len(want))
		}
		for r := range got {
			if got[r] != want[r] {
				t.Fatalf("offset %d row %d: width %d, want %d (border moved)", m.offset, r, got[r], want[r])
			}
		}
	}
}

// TestWideDiffLinesAreClippedToTheBox: a diff line wider than the
// terminal must be clipped, not allowed to widen or wrap the box.
func TestWideDiffLinesAreClippedToTheBox(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetWidth(30)
	m.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "edit_file",
		Diff: &uievent.Diff{Path: "a.go", Hunks: []uievent.DiffHunk{{
			Header: "@@ -1 +1 @@",
			Lines:  []uievent.DiffLine{{Kind: uievent.DiffLineAdd, Text: strings.Repeat("y", 200)}},
		}}},
	})
	for _, ln := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(ln); w > 30 {
			t.Errorf("row width %d exceeds the 30-cell terminal: %q", w, ln)
		}
	}
}

// TestLongTitleDoesNotWidenOrWrapTheBox is the regression for the
// review finding: the title, hint, and position rows were never
// clipped, so a long tool-call title word-wrapped inside the border and
// the box claimed fewer rows than it rendered, pushing it into the
// composer. Every row must fit the width and Height must equal the
// rendered rows.
func TestLongTitleDoesNotWidenOrWrapTheBox(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetWidth(40)
	m.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "run_command",
		Args: map[string]any{"cmd": strings.Repeat("rm -rf ", 20)},
		Diff: previewDiff(t, 3),
	})
	view := m.View()
	rows := strings.Split(view, "\n")
	for i, ln := range rows {
		if w := ansi.StringWidth(ln); w > 40 {
			t.Errorf("row %d width %d exceeds the 40-cell terminal", i, w)
		}
	}
	if m.Height() != len(rows) {
		t.Errorf("Height() = %d, want %d rendered rows", m.Height(), len(rows))
	}

	// Narrow terminal: the fixed hint (44 cells) must clip, not wrap.
	m.SetWidth(24)
	rows = strings.Split(m.View(), "\n")
	for i, ln := range rows {
		if w := ansi.StringWidth(ln); w > 24 {
			t.Errorf("narrow row %d width %d exceeds 24", i, w)
		}
	}
	if m.Height() != len(rows) {
		t.Errorf("narrow Height() = %d, want %d rendered rows", m.Height(), len(rows))
	}
}

// TestHunklessDiffClaimsNoPreviewRows: a Diff with no hunks renders no
// preview and no position row, and Height must agree - the zero-window
// branch.
func TestHunklessDiffClaimsNoPreviewRows(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetWidth(80)
	m.SetRequest(uievent.ToolPendingBody{
		ToolCallID: "c1", Name: "edit_file",
		Diff: &uievent.Diff{Path: "a.go"},
	})
	view := m.View()
	if strings.Contains(view, "lines ") {
		t.Errorf("hunkless diff drew a position row:\n%s", view)
	}
	if rows := len(strings.Split(view, "\n")); m.Height() != rows {
		t.Errorf("Height() = %d, want %d rows", m.Height(), rows)
	}
	m = m.ScrollBy(5) // must stay a no-op
	if !strings.Contains(m.View(), "o once") {
		t.Error("scroll on a hunkless diff broke the prompt")
	}
}

// pending arms m with one request and returns its rendered rows.
func pendingRows(t *testing.T, m Model, width int, b uievent.ToolPendingBody) (Model, []string) {
	t.Helper()
	m.SetWidth(width)
	m.SetRequest(b)
	return m, strings.Split(m.View(), "\n")
}

// TestBorderIsTheCalmDecorativeRole: the prompt's frame must read like
// the composer's, not shout. The state it carries is said by the border
// LABEL now, which is text and keeps its own contrast; the frame itself
// is decoration and uses the decorative role.
func TestBorderIsTheCalmDecorativeRole(t *testing.T) {
	th := loadTheme(t)
	m, rows := pendingRows(t, New(th, theme.TierTrueColor), 80,
		uievent.ToolPendingBody{ToolCallID: "c1", Name: "edit_file"})
	_ = m

	// The whole border run is drawn in one style, so the assertion is on
	// the colour the run opens with, not on a styled glyph substring.
	calm := colourOf(th, theme.RoleBorder)
	loud := colourOf(th, theme.RoleBorderFocus)
	bottom := rows[len(rows)-1]
	if !strings.Contains(bottom, calm) {
		t.Errorf("the frame is not drawn in the decorative border role %s: %q", calm, bottom)
	}
	if calm != loud && strings.Contains(bottom, loud) {
		t.Errorf("the frame is still drawn in the focus border role: %q", bottom)
	}
}

// colourOf is a role's truecolor SGR parameters, as they appear in
// rendered output.
func colourOf(th theme.Theme, r theme.Role) string {
	styled := render.Role(th, theme.TierTrueColor, r).Render("x")
	i := strings.Index(styled, "38;2;")
	if i < 0 {
		return ""
	}
	return styled[i : i+strings.IndexByte(styled[i:], 'm')]
}

// TestActionRidesInTheBorderLabel: what is being approved belongs beside
// "Approval Required" in the top border row, not on a full-width line of
// its own inside the box.
func TestActionRidesInTheBorderLabel(t *testing.T) {
	m, rows := pendingRows(t, New(loadTheme(t), theme.TierTrueColor), 80,
		uievent.ToolPendingBody{ToolCallID: "c1", Name: "edit_file",
			Args: map[string]any{"path": "/asdasd"}})

	top := ansi.Strip(rows[0])
	for _, want := range []string{"Approval Required", "edit_file", "path=/asdasd"} {
		if !strings.Contains(top, want) {
			t.Errorf("the top border is missing %q: %q", want, top)
		}
	}
	// The body must not repeat it.
	body := ansi.Strip(strings.Join(rows[1:], "\n"))
	if strings.Contains(body, "edit_file") {
		t.Errorf("the action is still drawn inside the box as well:\n%s", body)
	}
	if got, want := len(rows), m.Height(); got != want {
		t.Errorf("View drew %d rows, Height claims %d", got, want)
	}
}

// TestActionStaysInTheBodyWhenItCannotFitTheBorder: the border row is a
// fixed budget, so a long action cannot always ride there. It must then
// stay in the body - dropping it would hide what is being approved, and
// truncating it into the border would name the wrong command.
func TestActionStaysInTheBodyWhenItCannotFitTheBorder(t *testing.T) {
	long := uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command",
		Args: map[string]any{"cmd": strings.Repeat("very-long-argument ", 12)}}

	for _, width := range []int{40, 60, 100} {
		m, rows := pendingRows(t, New(loadTheme(t), theme.TierTrueColor), width, long)
		plain := ansi.Strip(strings.Join(rows, "\n"))
		if !strings.Contains(plain, "run_command") {
			t.Errorf("width %d: the action is nowhere on screen:\n%s", width, plain)
		}
		if strings.Contains(ansi.Strip(rows[0]), "run_command") {
			t.Errorf("width %d: a long action was forced into the border row: %q", width, rows[0])
		}
		if got, want := len(rows), m.Height(); got != want {
			t.Errorf("width %d: View drew %d rows, Height claims %d", width, got, want)
		}
	}
}

// TestNarrowTerminalKeepsTheBoxIntact: at widths too small for either
// placement the prompt must still be a well-formed box whose row count
// Height predicts, and must not overflow the terminal.
func TestNarrowTerminalKeepsTheBoxIntact(t *testing.T) {
	req := uievent.ToolPendingBody{ToolCallID: "c1", Name: "run_command",
		Args: map[string]any{"cmd": "ls -la"}}
	for _, width := range []int{8, 12, 20, 39} {
		m, rows := pendingRows(t, New(loadTheme(t), theme.TierTrueColor), width, req)
		if got, want := len(rows), m.Height(); got != want {
			t.Errorf("width %d: View drew %d rows, Height claims %d", width, got, want)
		}
		for i, r := range rows {
			if w := ansi.StringWidth(r); w > width {
				t.Errorf("width %d: row %d is %d columns", width, i, w)
			}
		}
	}
}

// TestBorderLabelDegradesToASCII: the warning glyph is not ASCII, so the
// no-colour tiers must still name the state.
func TestBorderLabelDegradesToASCII(t *testing.T) {
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		_, rows := pendingRows(t, New(loadTheme(t), tier), 80,
			uievent.ToolPendingBody{ToolCallID: "c1", Name: "edit_file"})
		top := ansi.Strip(rows[0])
		if !strings.Contains(top, "Approval Required") || !strings.Contains(top, "edit_file") {
			t.Errorf("tier %v top border: %q", tier, top)
		}
		if strings.Contains(top, "⚠") {
			t.Errorf("tier %v drew a non-ASCII glyph: %q", tier, top)
		}
	}
}
