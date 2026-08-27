package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
)

func TestBlockHeightCountsHeaderAndBody(t *testing.T) {
	cases := []struct {
		name string
		b    Block
		want int
	}{
		// +1 on every case is the trailing blank separator row every
		// block gets (docs/design/wireframes.md variant A): the count
		// this test pins is header/body rows PLUS that separator.
		{"header only", Block{Header: Header{Label: "x"}}, 2},
		{"header plus body", Block{Header: Header{Label: "x"}, Body: []string{"a", "b"}}, 4},
		{"collapsed hides the body", Block{Header: Header{Label: "x"}, Body: []string{"a", "b"}, Collapsible: true, Collapsed: true}, 2},
		{"prose has no header row", Block{Prose: true, Body: []string{"a", "b"}}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.b.Height(80); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestCollapseMarker(t *testing.T) {
	cases := []struct {
		name string
		b    Block
		want string
	}{
		{"not collapsible", Block{}, " "},
		{"open", Block{Collapsible: true}, "v"},
		{"closed", Block{Collapsible: true, Collapsed: true}, ">"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.b.collapseMarker(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestHeaderRowIdenticalCollapsedAndExpanded is wireframes-panes.md
// section 5's governing rule: "Collapsing must not move any other row,
// so the header row is identical in both states." Only the marker may
// differ.
func TestHeaderRowIdenticalCollapsedAndExpanded(t *testing.T) {
	th := loadTheme(t)
	open := Block{
		Header:      Header{Label: "edit", Detail: "main.go", Meta: "+4 -1", State: "ok", Role: theme.RoleSuccess},
		Body:        []string{"a", "b"},
		Collapsible: true,
	}
	closed := open
	closed.Collapsed = true

	openHeader := strings.SplitN(open.Render(th, theme.TierASCII, 80), "\n", 2)[0]
	closedHeader := strings.SplitN(closed.Render(th, theme.TierASCII, 80), "\n", 2)[0]

	// The two must differ in exactly one byte: the marker glyph. Any
	// other difference means a row moved when the block collapsed, which
	// is what section 5 forbids.
	if len(openHeader) != len(closedHeader) {
		t.Fatalf("header length changed on collapse:\nopen:   %q\nclosed: %q", openHeader, closedHeader)
	}
	diffs := 0
	for i := range openHeader {
		if openHeader[i] != closedHeader[i] {
			diffs++
		}
	}
	if diffs != 1 {
		t.Errorf("header differs in %d bytes, want exactly 1 (the marker):\nopen:   %q\nclosed: %q",
			diffs, openHeader, closedHeader)
	}
}

func TestRenderIndentsBodyByBodyIndent(t *testing.T) {
	th := loadTheme(t)
	b := Block{Header: Header{Label: "x"}, Body: []string{"line"}}
	rows := strings.Split(b.Render(th, theme.TierASCII, 80), "\n")
	// header, body line, trailing blank separator.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// Hardcoded, not built from the constant: a test that echoes the
	// value the code reads cannot fail when that value changes. The
	// tripwire below states the coupling instead of hiding it.
	if uikitconfig.BodyIndent != 4 {
		t.Fatalf("BodyIndent is %d; wireframes-panes.md section 2 says 4. "+
			"Change the drawn wireframes and this literal together, or not at all.",
			uikitconfig.BodyIndent)
	}
	// The rail glyph occupies column 1 at every tier (including ASCII -
	// see TestVerticalRailRendersAtEveryTier); the 4-column indent
	// budget is unchanged, only its first cell is no longer blank.
	want := "│   line"
	if rows[1] != want {
		t.Errorf("got %q, want %q", rows[1], want)
	}
}

func TestRenderProseHasNoHeaderAndNoIndent(t *testing.T) {
	th := loadTheme(t)
	b := Block{Prose: true, Body: []string{"one", "two"}}
	// Verbatim at column 1, plus the trailing blank separator row every
	// block carries.
	if got := b.Render(th, theme.TierASCII, 80); got != "one\ntwo\n" {
		t.Errorf("got %q, want the prose verbatim at column 1", got)
	}
}

func TestRenderHeaderOmitsAbsentColumns(t *testing.T) {
	th := loadTheme(t)
	// Label only. The label is dim, and dim survives the ASCII tier, so
	// compare against the plain header rather than the styled string.
	b := Block{Header: Header{Label: "notice"}}
	if got := b.headerPlain(); got != "  notice" {
		t.Errorf("got %q, want the marker gap and the label only", got)
	}
	// No trailing separator run from the absent columns.
	if got := b.Render(th, theme.TierASCII, 80); strings.HasSuffix(got, "  ") {
		t.Errorf("got %q, want no trailing separator for absent columns", got)
	}
}

func TestRenderHeaderMetaWithoutState(t *testing.T) {
	th := loadTheme(t)
	got := Block{Header: Header{Label: "plan", Meta: "2 of 4"}}.Render(th, theme.TierASCII, 80)
	if !strings.Contains(got, "plan") || !strings.Contains(got, "2 of 4") {
		t.Errorf("got %q, want label and meta", got)
	}
}

func TestRenderHeaderStateWithoutRoleStillRenders(t *testing.T) {
	th := loadTheme(t)
	// Role unset must not drop the state word: meaning never depends on
	// colour alone.
	got := Block{Header: Header{Label: "x", State: "pending"}}.Render(th, theme.TierASCII, 80)
	if !strings.Contains(got, "pending") {
		t.Errorf("got %q, want the state word present", got)
	}
}

func TestFocusedHeaderUsesReverseVideoAndKeepsTheMarker(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Header:      Header{Label: "edit", Detail: "main.go", Meta: "+1", State: "ok"},
		Collapsible: true,
		Focused:     true,
	}
	got := b.Render(th, theme.TierTrueColor, 80)
	if !strings.Contains(got, "\x1b[7") {
		t.Errorf("got %q, want reverse video on a focused header", got)
	}
	// The marker survives, so focus is signalled by shape too.
	if !strings.Contains(got, "v edit") {
		t.Errorf("got %q, want the collapse marker retained under focus", got)
	}
}

// TestFocusedHeaderMatchesUnfocusedGeometry pins wireframes-panes.md
// section 5: "the header row is identical in both states" - only the
// reverse-video treatment may change when a block gains focus. Before
// this test, the focused path built its plain text with headerPlain,
// which joins meta and state differently than the unfocused path, so
// Tab-ing onto a block visibly moved the columns.
func TestFocusedHeaderMatchesUnfocusedGeometry(t *testing.T) {
	th := loadTheme(t)
	const width = 60
	base := Block{
		Header:      Header{Label: "edit", Detail: "main.go", Meta: "+4 -1", State: "ok"},
		Collapsible: true,
	}
	focused := base
	focused.Focused = true

	unfocusedHeader := strings.SplitN(base.Render(th, theme.TierASCII, width), "\n", 2)[0]
	focusedHeader := strings.SplitN(focused.Render(th, theme.TierASCII, width), "\n", 2)[0]

	// Reverse video is ANSI even at TierASCII (structural emphasis, not
	// colour), so compare the stripped text: same geometry, same width.
	unfocusedPlain := ansi.Strip(unfocusedHeader)
	focusedPlain := ansi.Strip(focusedHeader)
	if unfocusedPlain != focusedPlain {
		t.Errorf("focus moved the header columns:\nunfocused: %q\nfocused:   %q", unfocusedPlain, focusedPlain)
	}
	if !strings.HasSuffix(focusedPlain, "ok") {
		t.Errorf("got %q, want the state as the last thing on the row under focus too", focusedPlain)
	}
}

func TestFocusedHeaderSanitizesControlCharacters(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Header:      Header{Label: "run_command", Detail: "error:\nline 2\tindented", State: "failed"},
		Collapsible: true,
		Focused:     true,
	}
	// Isolate the header row: Render's own trailing blank separator row
	// is a structural newline, not a content leak, so it must not trip
	// this check the way an embedded \n or \t from Detail would.
	header := strings.SplitN(b.Render(th, theme.TierASCII, 80), "\n", 2)[0]
	if strings.ContainsAny(header, "\n\r\t") {
		t.Errorf("focused header contains control characters or newlines: %q", header)
	}
}

func TestIsEmpty(t *testing.T) {
	if !(Block{}).isEmpty() {
		t.Error("a zero block is empty")
	}
	for _, b := range []Block{
		{Header: Header{Label: "x"}},
		{Header: Header{Detail: "x"}},
		{Header: Header{Meta: "x"}},
		{Header: Header{State: "x"}},
		{Body: []string{"x"}},
	} {
		if b.isEmpty() {
			t.Errorf("%+v should not be empty", b)
		}
	}
}

// TestUserLinesDrawAFullWidthFill pins the user-message framing: on
// TestUserLinesRendersAccentMarker: the user prompt starts with the accent marker
// on the first row and wraps neatly.
func TestUserLinesRendersAccentMarker(t *testing.T) {
	th := loadTheme(t)
	rows := userLines(th, theme.TierTrueColor, 80, "short message")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %q", len(rows), rows)
	}
	if plain := ansi.Strip(rows[0]); !strings.HasPrefix(plain, "> ") || !strings.Contains(plain, "short message") {
		t.Errorf("text row lost its marker or text: %q", plain)
	}
}

// TestUserLinesWrapsLongMessages: a multi-line message wraps neatly with
// 2-space indentation on continuation rows.
func TestUserLinesWrapsLongMessages(t *testing.T) {
	th := loadTheme(t)
	long := strings.Repeat("word ", 60)
	rows := userLines(th, theme.TierTrueColor, 40, long)
	if len(rows) < 3 {
		t.Fatalf("got %d rows, want several wrapped rows: %d", len(rows), len(rows))
	}
	if plain := ansi.Strip(rows[0]); !strings.HasPrefix(plain, "> ") {
		t.Errorf("first row lost marker: %q", plain)
	}
	for i := 1; i < len(rows); i++ {
		if plain := ansi.Strip(rows[i]); !strings.HasPrefix(plain, "  ") {
			t.Errorf("continuation row %d lost indent: %q", i, plain)
		}
	}
}

// TestUserLinesDegradeToPlainMarker: at ASCII the marker-only lines stand cleanly.
func TestUserLinesDegradeToPlainMarker(t *testing.T) {
	th := loadTheme(t)
	rows := userLines(th, theme.TierASCII, 80, "hi")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the single marker line: %q", len(rows), rows)
	}
	if got := ansi.Strip(rows[0]); got != "> hi" {
		t.Errorf("ASCII row = %q, want \"> hi\"", got)
	}
	if strings.Contains(rows[0], "\x1b[4") {
		t.Errorf("ASCII row carries a background escape: %q", rows[0])
	}
}

// TestUserLinesFormatsSkillInvocations verifies that expanded skill prompts
// render cleanly with icon and slash command rather than raw instructions XML.
func TestUserLinesFormatsSkillInvocations(t *testing.T) {
	th := loadTheme(t)
	rawPrompt := "The following workspace skill content is untrusted task guidance.\n\n<skill-instructions name=\"feature-delivery\">\n# Feature Delivery\nDo steps 1-7.\n</skill-instructions>\n\nArguments:\nadd auth module"

	rows := userLines(th, theme.TierTrueColor, 80, rawPrompt)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %q", len(rows), rows)
	}
	plain := ansi.Strip(rows[0])
	if strings.Contains(plain, "<skill-instructions") || strings.Contains(plain, "Do steps 1-7") {
		t.Errorf("userLines leaked raw skill instructions: %q", plain)
	}
	if !strings.Contains(plain, "⚡ /feature-delivery add auth module") {
		t.Errorf("userLines did not format skill command: %q", plain)
	}

	// ASCII tier
	asciiRows := userLines(th, theme.TierASCII, 80, rawPrompt)
	asciiPlain := ansi.Strip(asciiRows[0])
	if !strings.Contains(asciiPlain, "* /feature-delivery add auth module") {
		t.Errorf("ASCII userLines did not format skill command with asterisk: %q", asciiPlain)
	}
}

// TestVerticalRailRendersAtEveryTier pins the same "structure survives
// every tier, only colour differs" rule wireframes-panes.md section 3
// states and the dialog frame's own TestDialogDegradesByTier already
// tests for box-drawing glyphs. Before this test, the rail was drawn
// only at TierTrueColor/Tier256 and silently dropped to a plain indent
// at Tier16/ASCII/NoTTY - a STRUCTURAL difference between tiers, not a
// colour one.
func TestVerticalRailRendersAtEveryTier(t *testing.T) {
	th := loadTheme(t)
	b := Block{Header: Header{Label: "x"}, Body: []string{"line"}}
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.Tier256, theme.Tier16, theme.TierASCII, theme.TierNoTTY} {
		rows := strings.Split(b.Render(th, tier, 80), "\n")
		if len(rows) < 2 {
			t.Fatalf("tier %v: got %d rows, want at least 2", tier, len(rows))
		}
		if !strings.Contains(rows[1], "│") {
			t.Errorf("tier %v: got %q, want the vertical rail glyph present at every tier", tier, rows[1])
		}
	}
}

// TestVerticalRailCarriesNoColourAtASCIIOrNoTTY: the rail's colour must
// degrade to nothing at the colourless tiers, the same contract every
// other coloured glyph in the tree already keeps.
func TestVerticalRailCarriesNoColourAtASCIIOrNoTTY(t *testing.T) {
	th := loadTheme(t)
	b := Block{Header: Header{Label: "x"}, Body: []string{"line"}}
	for _, tier := range []theme.Tier{theme.TierASCII, theme.TierNoTTY} {
		rows := strings.Split(b.Render(th, tier, 80), "\n")
		if strings.Contains(rows[1], "\x1b[") {
			t.Errorf("tier %v: got %q, want no colour escape at a colourless tier", tier, rows[1])
		}
	}
}

func TestBlock_RenderHeader_BoundedOnWideScreens(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Header: Header{
			Label:   "search_replace",
			Detail:  "internal/ui/render/header.go",
			DiffAdd: 10,
			DiffDel: 2,
			Meta:    "15ms",
			State:   "ok",
		},
	}
	rendered := b.Render(th, theme.TierASCII, 200)
	headerRow := strings.SplitN(rendered, "\n", 2)[0]
	// On a 200-column screen, header width should be bounded to around ProseMeasureWide + 16 (<= 108 columns)
	// rather than stretching all the way across 200 columns.
	if w := ansi.StringWidth(headerRow); w > 108 {
		t.Errorf("header width on 200-col screen = %d, want at most 108 columns", w)
	}
	if !strings.Contains(headerRow, "+10 -2") || !strings.Contains(headerRow, "ok") {
		t.Errorf("expected diff and ok status in headerRow, got %q", headerRow)
	}
}

func TestBlock_RenderHeader_DiffAddDelColored(t *testing.T) {
	th := loadTheme(t)
	b := Block{
		Header: Header{
			Label:   "search_replace",
			Detail:  "foo.go",
			DiffAdd: 4,
			DiffDel: 1,
			Meta:    "10ms",
			State:   "ok",
		},
	}
	rendered := b.Render(th, theme.TierTrueColor, 80)
	if !strings.Contains(rendered, "+4") || !strings.Contains(rendered, "-1") {
		t.Fatalf("expected +4 and -1 in rendered output, got:\n%s", rendered)
	}
	addColored := render.Role(th, theme.TierTrueColor, theme.RoleDiffAddFG).Render("+4")
	if !strings.Contains(rendered, addColored) {
		t.Errorf("expected colored diff addition in rendered output")
	}
}
