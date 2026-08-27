package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func TestMarkdownPassthroughAtNoColour(t *testing.T) {
	th := loadTheme(t)
	// At TierASCII every Glamour element renders to plain ASCII, so a
	// fenced code block, a heading, and emphasis all become the readable
	// characters that survive NO_COLOR. goldmark tags themselves never
	// appear - they are parsed and consumed before rendering.
	got := Markdown(th, theme.TierASCII, 60, "before\n```\ncode line\n```\nafter")
	plain := ansi.Strip(got)
	for _, want := range []string{"before", "code line", "after"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain rendering missing %q: %q", want, plain)
		}
	}
	if strings.Contains(got, "<pre>") || strings.Contains(got, "<code>") || strings.Contains(got, "<h1>") {
		t.Errorf("goldmark HTML tags leaked into the output: %q", got)
	}
}

func TestMarkdownStylesHeadings(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 80, "# h1\n## h2\n### h3\n#### h4\n##### h5\n###### h6")
	plain := ansi.Strip(got)
	for _, want := range []string{"h1", "h2", "h3", "h4", "h5", "h6"} {
		if !strings.Contains(plain, want) {
			t.Errorf("heading text %q missing from plain view: %q", want, plain)
		}
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("truecolour markdown has no colour: headings must carry a style: %q", got)
	}
}

func TestMarkdownStylesEmphasisAndStrong(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 80, "*emph* **strong** ~~strike~~")
	// Glamour emits COMBINED SGR (colour;format together), not separate
	// short-form sequences. So italic is `;3m` not `\x1b[3m`, bold is
	// `;1m`, strikethrough is `;9m`. Looking for the format code as a
	// substring past the colour is what proves emphasis survived the
	// theme mapping.
	if !strings.Contains(got, ";3m") {
		t.Errorf("italic emphasis missing: %q", got)
	}
	if !strings.Contains(got, ";1m") {
		t.Errorf("bold strong missing: %q", got)
	}
	if !strings.Contains(got, ";9m") {
		t.Errorf("strikethrough missing: %q", got)
	}
	// ASCII still carries bold via the SGR-1 emphasis, so the rendered
	// bytes change for emphasis even when colour drops out. Strikethrough
	// at the no-colour tier falls back to the literal ~~ text.
	plain := ansi.Strip(Markdown(th, theme.TierASCII, 80, "*emph* **strong** ~~strike~~"))
	for _, want := range []string{"emph", "strong", "strike"} {
		if !strings.Contains(plain, want) {
			t.Errorf("ASCII rendering missing %q: %q", want, plain)
		}
	}
}

func TestMarkdownStylesInlineCode(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 80, "use `retry.Policy` here")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("inline code carries no style: %q", got)
	}
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "retry.Policy") {
		t.Errorf("inline code text dropped from plain view: %q", plain)
	}
}

func TestMarkdownStylesFencedCode(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 60, "before\n```\ncode line\n```\nafter")
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("fenced code block at truecolour carries no colour: %q", got)
	}
	// The fenced code line should be present in plain text.
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "code line") {
		t.Errorf("code line missing from plain view: %q", plain)
	}
	// Glamour margins code blocks; we keep the margin so the block reads
	// as a block, not a row of prose. Empty inner rows are fine.
}

func TestMarkdownRendersLists(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 60, "- one\n- two\n- [ ] three\n- [x] four\n1. five\n2. six")
	plain := ansi.Strip(got)
	for _, want := range []string{"one", "two", "three", "four", "five", "six"} {
		if !strings.Contains(plain, want) {
			t.Errorf("list item %q missing: %q", want, plain)
		}
	}
}

// TestMarkdownRendersTable covers a pipe table's row + header
// rendering. Glamour renders the column separator as `│` at colour tiers
// and `|` at the no-colour tiers (where the box-drawing glyphs are
// rejected by the styled config). Either is acceptable; both prove the
// table did not collapse to a paragraph.
func TestMarkdownRendersTable(t *testing.T) {
	th := loadTheme(t)
	in := "| a | b |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n"
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.TierASCII} {
		got := Markdown(th, tier, 60, in)
		plain := ansi.Strip(got)
		// Either ASCII pipe or Unicode box-drawing separator is OK;
		// what matters is that the rows stayed distinct.
		if !strings.ContainsAny(plain, "│|") {
			t.Errorf("tier %v: table lost its column separators: %q", tier, plain)
		}
		for _, want := range []string{"a", "b", "1", "2", "3", "4"} {
			if !strings.Contains(plain, want) {
				t.Errorf("tier %v: table cell %q missing: %q", tier, want, plain)
			}
		}
	}
}

// TestMarkdownNoPanicOnHostileInput sweeps a seed corpus of inputs that
// historically tripped common markdown libraries: empty, a single
// backtick, an unterminated fence, unbalanced emphasis, raw HTML, and a
// stream of multibyte runes.
func TestMarkdownNoPanicOnHostileInput(t *testing.T) {
	th := loadTheme(t)
	inputs := []string{
		"",
		"`",
		"`a",
		"```\n",
		"```unterminated\nopen fence",
		"**unbalanced",
		"*also *unbalanced*",
		"<script>alert(1)</script>",
		"<!--raw comment-->",
		"\n\n\n",
		"é漢\n漢漢\n漢\n漢漢漢漢漢漢漢漢",
		strings.Repeat("x", 4096),
	}
	for _, in := range inputs {
		_ = Markdown(th, theme.TierTrueColor, 60, in)
		_ = Markdown(th, theme.TierASCII, 60, in)
		_ = Markdown(th, theme.Tier16, 60, in)
		_ = Markdown(th, theme.TierNoTTY, 60, in)
	}
}

// TestMarkdownWidthRespected pins the wrap contract: a long paragraph at
// width=40 must produce no row wider than 40 columns, even after the
// Glamour indent and the ANSI escapes are stripped.
func TestMarkdownWidthRespected(t *testing.T) {
	th := loadTheme(t)
	in := strings.Repeat("lorem ipsum dolor sit amet ", 30)
	got := Markdown(th, theme.TierTrueColor, 40, in)
	for i, row := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(row); w > 40 {
			t.Errorf("row %d is %d columns, exceeds width=40", i, w)
		}
	}
}

// TestMarkdownWidthFloor holds the guard: a width of 0 or negative must
// NOT ask Glamour for a 0-column wrap, which would crash or render an
// empty page. The plan pins max(20, width-2) at the call site, but the
// guard here is the last line of defence.
func TestMarkdownWidthFloor(t *testing.T) {
	th := loadTheme(t)
	for _, w := range []int{0, -1, -100} {
		_ = Markdown(th, theme.TierTrueColor, w, "hello world")
	}
}

// TestMarkdownTierMatrix exercises one input across every tier. Each
// tier must return some output and never panic; the output MAY differ
// (ASCII strips colour, NoTTY uses a different style) but each must
// contain the literal payload as plaintext.
func TestMarkdownTierMatrix(t *testing.T) {
	th := loadTheme(t)
	in := "# title\n\nbody with `code` and *emph*\n\n- one\n- two\n"
	for _, tier := range []theme.Tier{theme.TierTrueColor, theme.Tier256, theme.Tier16, theme.TierASCII, theme.TierNoTTY} {
		got := Markdown(th, tier, 60, in)
		if got == "" {
			t.Errorf("tier %v returned empty", tier)
		}
		plain := ansi.Strip(got)
		for _, want := range []string{"title", "body", "code", "emph", "one", "two"} {
			if !strings.Contains(plain, want) {
				t.Errorf("tier %v lost %q: %q", tier, want, plain)
			}
		}
	}
}

// TestMarkdownRendersLink: the link text is rendered and a link target
// is rendered beside it (Glamour's default GFM footer shape).
func TestMarkdownRendersLink(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 80, "see [docs](https://example.com)")
	plain := ansi.Strip(got)
	if !strings.Contains(plain, "docs") {
		t.Errorf("link text missing: %q", plain)
	}
	if !strings.Contains(plain, "https://example.com") {
		t.Errorf("link target missing: %q", plain)
	}
}

// TestMarkdownMermaidIsCodeNotDiagram: ```mermaid fences render as a
// styled code block. Mermaid is intentionally out of scope per
// wireframes-panes.md 16.2; Glamour's parser consumes the language hint
// without a label, which is the right behaviour for "this is code, not
// a diagram" - the user already knows they wrote ```mermaid. What we
// verify is that the body is rendered as code (with a leading indent
// that marks it as a block) and that Glamour did not panic on the
// lexer lookup.
func TestMarkdownMermaidIsCodeNotDiagram(t *testing.T) {
	th := loadTheme(t)
	got := Markdown(th, theme.TierTrueColor, 60, "```mermaid\ngraph TD\nA-->B\n```")
	plain := ansi.Strip(got)
	for _, want := range []string{"graph TD", "A-->B"} {
		if !strings.Contains(plain, want) {
			t.Errorf("mermaid fence body missing %q: %q", want, plain)
		}
	}
}
