package render

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FuzzText proves Text never panics on arbitrary streamed assistant text
// and preserves the one real invariant it owns: no line of input is ever
// dropped from the output.
func FuzzText(f *testing.F) {
	f.Add("hello\nworld")
	f.Add("```\ncode\n```")
	f.Add("```unterminated\nfence")
	f.Add("")
	f.Add("```\n```\n```")
	f.Add("emoji: \U0001F600\n```\n\U0001F600\n```")

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
		got := Text(mivia, theme.TierTrueColor, s)
		wantLines := len(strings.Split(s, "\n"))
		gotLines := len(strings.Split(got, "\n"))
		if gotLines != wantLines {
			t.Errorf("Text changed line count: input had %d lines, output has %d\ninput: %q\noutput: %q", wantLines, gotLines, s, got)
		}
	})
}
