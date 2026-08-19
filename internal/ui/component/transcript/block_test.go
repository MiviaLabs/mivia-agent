package transcript

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

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
	want := "    line"
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
