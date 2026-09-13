package render

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// FuzzStreamedMarkdownMatchesOneShot is the safety net under
// StreamRenderer's one claim: a document streamed in arbitrary chunks
// renders to the same bytes as the same document rendered once.
//
// The seeds are the constructs the boundary predicate reasons about -
// fences, lists that merge, indented code, setext underlines, link
// references, HTML - because a corpus of plain prose would never reach
// the interesting branches.
//
// Run seeded (plain `go test`) in the gate. To actually fuzz, cap the
// workers: `go test -fuzz FuzzStreamedMarkdownMatchesOneShot -parallel 2
// -fuzztime 60s`. Never the default parallelism - AGENTS.md records that
// one worker per core exhausts this machine's memory.
func FuzzStreamedMarkdownMatchesOneShot(f *testing.F) {
	seeds := []string{
		"hello world",
		"# heading\n\nbody text\n",
		"para one\n\npara two\n\npara three\n",
		"```go\nfunc a() {}\n```\n\nafter the fence\n",
		"```unterminated\nfence body\n\nstill inside\n",
		"1. first\n2. second\n\n1. third\n2. fourth\n",
		"- bullet\n- points\n\n- more\n- points\n",
		"text\n\n    indented code\n\n    more indented\n",
		"Setext\n======\n\nbody\n",
		"see [a]\n\n[a]: https://example.invalid\n",
		"prose\n\n<div>\nhtml\n</div>\n\nafter\n",
		"| a | b |\n|---|---|\n| 1 | 2 |\n\nafter table\n",
		"> quote\n\n> another quote\n",
		"---\n\nafter a rule\n",
		"émoji ✨ ünicode ß\n\nsecond block\n",
		"a\n\n\n\nb\n",
		"- [ ] task\n- [x] done\n\nafter\n",
		"",
	}
	for _, s := range seeds {
		f.Add(s, uint8(7))
	}

	themes, err := theme.Embedded()
	if err != nil {
		f.Fatal(err)
	}
	var mivia theme.Theme
	for _, t := range themes {
		if t.Name == "mivia-dark" {
			mivia = t
		}
	}

	f.Fuzz(func(t *testing.T, doc string, chunk uint8) {
		// Glamour parses the whole document on every call, so a large
		// input turns one iteration into seconds. The transcript's own
		// cap is 64 KiB.
		if len(doc) > 16*1024 {
			t.Skip("fuzz corpus size guard: input exceeds 16KiB")
		}
		// The transcript's deltas are whole strings off a decoded event,
		// never torn UTF-8, so invalid input is not a case this must
		// answer for.
		if !utf8.ValidString(doc) {
			t.Skip("fuzz corpus guard: input is not valid UTF-8")
		}
		step := int(chunk)
		if step == 0 {
			step = 1
		}

		const width = 80
		got := streamInChunks(mivia, width, doc, step)
		want := Markdown(mivia, theme.TierTrueColor, width, doc)
		if got != want {
			reportStreamMismatch(t, doc, step, got, want)
		}
	})
}

// streamInChunks feeds doc through a fresh StreamRenderer step bytes at
// a time, advancing each chunk boundary to the next full rune, and
// returns the final frame.
func streamInChunks(mivia theme.Theme, width int, doc string, step int) string {
	var r StreamRenderer
	if doc == "" {
		return r.Render(mivia, theme.TierTrueColor, width, doc)
	}
	var got string
	for i := 0; i < len(doc); {
		j := i + step
		if j > len(doc) {
			j = len(doc)
		}
		// Advance to a rune boundary: a chunk that ends mid-rune is not
		// a shape the transcript can produce.
		for j < len(doc) && doc[j]&0xC0 == 0x80 {
			j++
		}
		got = r.Render(mivia, theme.TierTrueColor, width, doc[:j])
		i = j
	}
	return got
}

// reportStreamMismatch fails t with the first differing line between
// the streamed and one-shot renders, or the line-count difference if
// every line up to the shorter render's length matched.
func reportStreamMismatch(t *testing.T, doc string, step int, got, want string) {
	t.Helper()
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("streamed output differs from one-shot at line %d\n doc   %q\n chunk %d\n got   %q\n want  %q",
				i, doc, step, g, w)
		}
	}
	t.Fatalf("streamed output differs in length: got %d lines, want %d (doc %q, chunk %d)",
		len(gl), len(wl), doc, step)
}
