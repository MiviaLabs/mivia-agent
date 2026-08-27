package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FuzzMarkdown proves Markdown never panics on arbitrary streamed
// assistant text and preserves the two invariants it owns: no row
// exceeds the wrap width, and no panic ever surfaces from the renderer
// (goldmark plugins have historically been panic-prone).
func FuzzMarkdown(f *testing.F) {
	f.Add("hello\nworld")
	f.Add("# heading\n\nbody")
	f.Add("```\ncode\n```")
	f.Add("```unterminated\nfence")
	f.Add("")
	f.Add("```\n```\n```")
	f.Add("emoji: \U0001F600\n```\n\U0001F600\n```")
	f.Add("*emph* **strong** ~~strike~~ `code`")
	f.Add("| a | b |\n|---|---|\n| 1 | 2 |\n")

	th, err := theme.Embedded()
	if err != nil {
		f.Fatal(err)
	}
	var mivia theme.Theme
	for _, t := range th {
		if t.Name == "mivia-dark" {
			mivia = t
		}
	}

	f.Fuzz(func(t *testing.T, s string) {
		// Skip inputs that exercise only the empty-input branch or that
		// exceed a generous safety bound. Glamour parses the full
		// markdown on every call, and a multi-MB input makes a fuzz
		// iteration take seconds rather than milliseconds.
		if len(s) > 64*1024 {
			t.Skip("fuzz corpus size guard: input exceeds the 64KiB safety bound")
		}
		got := Markdown(mivia, theme.TierTrueColor, 80, s)
		// Width contract: no row may exceed the wrap width.
		for i, row := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(row); w > 80 {
				t.Errorf("row %d is %d columns, exceeds width=80: %q", i, w, row)
			}
		}
	})
}
