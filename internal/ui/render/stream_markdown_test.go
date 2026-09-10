package render

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// The whole point of StreamRenderer is that it is indistinguishable from
// Markdown. Every test here is a variation on that one assertion: the
// streamed result must equal the one-shot result byte for byte, because
// the transcript commits the finished span through Markdown and any
// difference is a visible pop at the moment the span settles.

func streamTheme(t *testing.T) theme.Theme {
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

// feed streams content through r in chunks of the given size and returns
// the final frame.
func feed(t *testing.T, r *StreamRenderer, th theme.Theme, width, chunk int, content string) string {
	t.Helper()
	var out string
	for i := 0; i < len(content); {
		j := i + chunk
		if j > len(content) {
			j = len(content)
		}
		// Never cut a multi-byte rune: the transcript's deltas are whole
		// UTF-8 chunks and a torn rune is not a case this must handle.
		for j < len(content) && !isRuneStart(content[j]) {
			j++
		}
		out = r.Render(th, theme.TierTrueColor, width, content[:j])
		i = j
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

const streamDoc = "# Retry policy\n\nThe client retries a failed request with backoff and a cap, then\nreturns the **final** error with the attempt count.\n\n## Configuration\n\n- `max_attempts` bounds the attempts\n- `base_delay` is the first interval\n\n### Example\n\n```go\nfunc backoff(attempt int) time.Duration {\n\treturn base << attempt\n}\n```\n\n| Attempt | Delay |\n|--------:|------:|\n| 1       | 100ms |\n| 2       | 200ms |\n\n> Keep the cap low and the jitter wide.\n\nFinal paragraph with émoji ✨ and ünicode.\n"

func TestStreamRendererMatchesOneShot(t *testing.T) {
	th := streamTheme(t)
	want := Markdown(th, theme.TierTrueColor, 80, streamDoc)
	for _, chunk := range []int{1, 3, 17, 64, 4096} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, chunk, streamDoc)
		if got != want {
			t.Errorf("chunk=%d: streamed output differs from one-shot", chunk)
			reportFirstDiff(t, got, want)
		}
	}
}

// A cache that never advanced would still pass the equality tests above,
// because a full re-render is always correct. This asserts the cache
// actually does its job: after streaming a multi-block document, the
// stable prefix must cover everything up to the last safe boundary.
func TestStreamRendererAdvancesTheStablePrefix(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	feed(t, &r, th, 80, 32, streamDoc)
	if r.stableContent == "" {
		t.Fatal("stable prefix never advanced; the cache is doing no work")
	}
	if !strings.HasPrefix(streamDoc, r.stableContent) {
		t.Fatal("stable prefix is not a prefix of the content")
	}
	// The last block is the trailing paragraph, so the cache should have
	// absorbed the blockquote that precedes it.
	if !strings.Contains(r.stableContent, "Keep the cap low") {
		t.Errorf("stable prefix stopped early: %q", tailOf(r.stableContent, 60))
	}
}

func TestStreamRendererResetsOnWidthChange(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	if r.stableContent == "" {
		t.Fatal("precondition: expected a stable prefix at width 80")
	}
	got := r.Render(th, theme.TierTrueColor, 40, streamDoc)
	want := Markdown(th, theme.TierTrueColor, 40, streamDoc)
	if got != want {
		t.Error("width change did not re-render at the new width")
		reportFirstDiff(t, got, want)
	}
}

func TestStreamRendererResetsOnThemeChange(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	got := r.Render(th, theme.TierASCII, 80, streamDoc)
	want := Markdown(th, theme.TierASCII, 80, streamDoc)
	if got != want {
		t.Error("tier change did not re-render at the new tier")
		reportFirstDiff(t, got, want)
	}
}

// A retry, a rewind, or an assistant_reset replaces the buffer instead of
// extending it. The cache must notice it is no longer a prefix.
func TestStreamRendererResetsOnNonPrefixContent(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	replacement := "# Something else\n\nA different answer entirely.\n"
	got := r.Render(th, theme.TierTrueColor, 80, replacement)
	want := Markdown(th, theme.TierTrueColor, 80, replacement)
	if got != want {
		t.Error("non-prefix content did not reset the cache")
		reportFirstDiff(t, got, want)
	}
}

func TestStreamRendererResetClears(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	r.Reset()
	if r.stableContent != "" || r.stableRender != "" {
		t.Fatal("Reset left cached state behind")
	}
	got := r.Render(th, theme.TierTrueColor, 80, streamDoc)
	if want := Markdown(th, theme.TierTrueColor, 80, streamDoc); got != want {
		t.Error("render after Reset differs from one-shot")
	}
}

// An open fence is the case the boundary predicate exists for: a blank
// line inside a code block is not a block boundary, and splicing there
// would render the fence twice.
func TestStreamRendererNoBoundaryInsideOpenFence(t *testing.T) {
	th := streamTheme(t)
	doc := "intro paragraph\n\n```go\nfunc a() {}\n\nfunc b() {}\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 5, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming an unterminated fence differs from one-shot")
		reportFirstDiff(t, got, want)
	}
	if strings.Contains(r.stableContent, "```") {
		t.Errorf("cache advanced into an open fence: %q", r.stableContent)
	}
}

// Ordered lists renumber when two of them merge, and indented code
// blocks merge outright. Both are why a boundary after a list is refused.
func TestStreamRendererNoBoundaryAfterList(t *testing.T) {
	th := streamTheme(t)
	doc := "1. first\n2. second\n\n1. third\n2. fourth\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 4, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming adjacent ordered lists differs from one-shot")
		reportFirstDiff(t, got, want)
	}
}

// A link-reference definition anywhere in the document changes how a
// link ANYWHERE ELSE renders, including text already in the stable
// prefix. The cache has to switch itself off for the whole document.
func TestStreamRendererDisabledByLinkReference(t *testing.T) {
	th := streamTheme(t)
	doc := "see [a] below\n\nmore prose here\n\n[a]: https://example.invalid\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 7, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming a late link reference differs from one-shot")
		reportFirstDiff(t, got, want)
	}
	if r.stableContent != "" {
		t.Errorf("cache stayed on for a document with a link reference: %q", r.stableContent)
	}
}

func TestStreamRendererDisabledByHTMLBlock(t *testing.T) {
	th := streamTheme(t)
	doc := "prose first\n\n<div>\nraw html\n</div>\n\ntrailing prose\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 6, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming an HTML block differs from one-shot")
		reportFirstDiff(t, got, want)
	}
	if r.stableContent != "" {
		t.Errorf("cache stayed on for a document with an HTML block: %q", r.stableContent)
	}
}

// A setext underline binds to the line above it with no blank line
// between, so a boundary can never fall between the two. This pins that
// reasoning: setext documents stream exactly, with no rule of their own
// in the predicate.
func TestStreamRendererNoBoundaryBeforeSetextUnderline(t *testing.T) {
	th := streamTheme(t)
	doc := "opening paragraph\n\nTitle\n=====\n\nbody text\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 3, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming a setext heading differs from one-shot")
		reportFirstDiff(t, got, want)
	}
}

// Unbroken prose has no safe boundary at all. The contract is that the
// renderer stays CORRECT and falls back to a full render, rather than
// cutting somewhere unsafe to stay fast. This pins that choice: a 4 KiB
// paragraph must still match the one-shot render exactly.
func TestStreamRendererUnbrokenProseStaysExact(t *testing.T) {
	th := streamTheme(t)
	doc := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps going ", 70) + "\n"
	if len(doc) < 4096 {
		t.Fatalf("fixture is %d bytes, want at least 4096", len(doc))
	}
	var r StreamRenderer
	got := feed(t, &r, th, 80, 512, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("unbroken prose streamed differently from one-shot")
		reportFirstDiff(t, got, want)
	}
	if r.stableContent != "" {
		t.Errorf("cache cut unbroken prose at an unsafe point: %q", tailOf(r.stableContent, 40))
	}
}

func TestStreamRendererEmptyContent(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	if got := r.Render(th, theme.TierTrueColor, 80, ""); got != "" {
		t.Errorf("empty content rendered %q, want empty", got)
	}
}

// randomDoc builds a document out of block fragments, which is where the
// interesting boundaries are: every adjacent pair of block kinds is a
// different margin case in glamour.
func randomDoc(rng *rand.Rand) string {
	blocks := []string{
		"# Heading\n", "## Sub heading\n",
		"a paragraph with **bold**, *em*, `code` and enough words to wrap at eighty columns comfortably\n",
		"- item one\n- item two\n", "1. first\n2. second\n",
		"```go\nfunc f() int { return 1 }\n```\n",
		"| a | b |\n|---|---|\n| 1 | 2 |\n",
		"> a quoted line\n", "---\n", "short line\n",
		"- a\n  - b\n", "[link](https://x.invalid) inline\n",
		"~~struck~~ text\n", "Setext\n======\n", "    indented code\n",
		"émoji ✨ and ünicode ß\n", "- [ ] task open\n- [x] task done\n",
	}
	n := 1 + rng.Intn(7)
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, blocks[rng.Intn(len(blocks))])
	}
	return strings.Join(parts, "\n")
}

// TestStreamRendererRandomDocuments is the seeded companion to the fuzz
// target: it runs in the normal gate, where the fuzz target only replays
// its corpus.
func TestStreamRendererRandomDocuments(t *testing.T) {
	th := streamTheme(t)
	rng := rand.New(rand.NewSource(20260911))
	for i := 0; i < 300; i++ {
		doc := randomDoc(rng)
		width := 40 + rng.Intn(60)
		chunk := 1 + rng.Intn(50)
		var r StreamRenderer
		got := feed(t, &r, th, width, chunk, doc)
		want := Markdown(th, theme.TierTrueColor, width, doc)
		if got != want {
			t.Errorf("doc=%q width=%d chunk=%d", doc, width, chunk)
			reportFirstDiff(t, got, want)
			return
		}
	}
}

func reportFirstDiff(t *testing.T, got, want string) {
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
			t.Errorf("  first difference at line %d\n   got  %q\n   want %q", i, g, w)
			return
		}
	}
	t.Errorf("  lengths differ: got %d lines, want %d", len(gl), len(wl))
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// A theme swap at the SAME tier changes every colour the cache holds.
// The tier test above cannot see this: it changes tiers, which is a
// different key.
func TestStreamRendererResetsOnThemeNameChange(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) < 2 {
		t.Skip("need two embedded themes")
	}
	first, second := themes[0], themes[1]
	for _, th := range themes {
		if th.Name != first.Name {
			second = th
			break
		}
	}
	if first.Name == second.Name {
		t.Skip("only one distinct theme")
	}
	var r StreamRenderer
	r.Render(first, theme.TierTrueColor, 80, streamDoc)
	got := r.Render(second, theme.TierTrueColor, 80, streamDoc)
	want := Markdown(second, theme.TierTrueColor, 80, streamDoc)
	if got != want {
		t.Errorf("theme change from %q to %q reused the old palette", first.Name, second.Name)
		reportFirstDiff(t, got, want)
	}
}

// Indented code merges with an adjacent indented block, so the cache
// must refuse to end a prefix immediately before one. This asserts the
// refusal, not just the output: the output is correct either way, which
// is exactly why the behaviour needs its own assertion.
func TestStreamRendererNoBoundaryBeforeIndentedCode(t *testing.T) {
	th := streamTheme(t)
	doc := "opening paragraph\n\n    indented code\n\nclosing paragraph\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 4, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming indented code differs from one-shot")
		reportFirstDiff(t, got, want)
	}
	if strings.Contains(r.stableContent, "opening paragraph") {
		t.Errorf("cache ended a prefix immediately before indented code: %q", r.stableContent)
	}
}

// Leading blank lines offer a cut position with no block above it.
func TestStreamRendererNoBoundaryBeforeFirstBlock(t *testing.T) {
	th := streamTheme(t)
	doc := "\n\n\nfirst real paragraph\n\nsecond paragraph\n"
	var r StreamRenderer
	got := feed(t, &r, th, 80, 5, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Error("streaming a document that opens with blank lines differs from one-shot")
		reportFirstDiff(t, got, want)
	}
	// The end state is a real boundary either way, so the interesting
	// moment is mid-stream: while only the leading blanks and the first
	// words have arrived there is no block above to separate, and the
	// cache must still be holding nothing.
	var mid StreamRenderer
	mid.Render(th, theme.TierTrueColor, 80, "\n\n\nfirst real paragraph\n")
	if mid.stableContent != "" {
		t.Errorf("cache stabilised before the first block: %q", mid.stableContent)
	}
}

// Runs of blank lines between blocks are a separator, not a block.
func TestStreamRendererMultipleBlankLinesBetweenBlocks(t *testing.T) {
	th := streamTheme(t)
	for _, sep := range []string{"\n\n", "\n\n\n", "\n\n\n\n"} {
		doc := "first paragraph" + sep + "second paragraph" + sep + "third paragraph\n"
		var r StreamRenderer
		got := feed(t, &r, th, 80, 6, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("separator %q streamed differently from one-shot", sep)
			reportFirstDiff(t, got, want)
		}
	}
}

// The reason this type exists. Without a cache, streaming a document in
// N chunks feeds Markdown the whole buffer N times, so the source bytes
// rendered grow with the SQUARE of the reply length. With it, each byte
// of the document is rendered a bounded number of times.
func TestStreamRendererRendersFarFewerBytesThanNaive(t *testing.T) {
	th := streamTheme(t)
	doc := strings.Repeat(streamDoc, 6)
	const chunk = 32
	var r StreamRenderer
	got := feed(t, &r, th, 80, chunk, doc)
	if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
		t.Fatal("precondition: streamed output must match one-shot")
	}
	ticks := (len(doc) + chunk - 1) / chunk
	naive := ticks * len(doc)
	if r.renderedBytes >= naive/4 {
		t.Errorf("cache saved too little: rendered %d source bytes over %d ticks; a cacheless renderer would spend %d",
			r.renderedBytes, ticks, naive)
	}
	t.Logf("doc=%d bytes ticks=%d rendered=%d bytes (naive %d, saving %.1fx)",
		len(doc), ticks, r.renderedBytes, naive, float64(naive)/float64(r.renderedBytes))
}

// A tick that adds nothing must not re-render anything.
func TestStreamRendererIdleTickCostsNothingExtra(t *testing.T) {
	th := streamTheme(t)
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	before := r.renderedBytes
	rendersBefore := r.renders
	r.Render(th, theme.TierTrueColor, 80, streamDoc)
	if r.renders != rendersBefore+1 {
		t.Errorf("a repeat tick made %d Markdown calls, want exactly 1", r.renders-rendersBefore)
	}
	if grew := r.renderedBytes - before; grew > len(streamDoc)-len(r.stableContent) {
		t.Errorf("a repeat tick rendered %d bytes, more than the %d-byte tail", grew, len(streamDoc)-len(r.stableContent))
	}
}

// A list item's continuation lines look like ordinary prose, so judging
// a boundary by the last line of the block above lets a list be spliced
// and renumbered. Found by FuzzStreamedMarkdownMatchesOneShot on the
// input below; kept here so the case is covered without the corpus.
func TestStreamRendererNoBoundaryAfterLazyListContinuation(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"0. 0\n0\n\n0. \n0",
		"1. first item\nlazy continuation\n\n1. second item\nalso lazy\n",
		"- bullet\ncontinued prose\n\n- another\ncontinued too\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 9, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// An empty ATX heading renders to a blank ROW, which compose would trim
// away as if it were a block margin. Found by
// FuzzStreamedMarkdownMatchesOneShot on "#\n\n0\n".
func TestStreamRendererBlockThatRendersToNothing(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"#\n\n0\n",
		"##\n\ntext after an empty heading\n",
		"text first\n\n#\n\nmore text\n",
		"para\n\n#\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 2, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// Poisoning must not cost a re-scan and a re-poison on every later tick:
// the cache key has to survive so the poisoned flag does.
func TestStreamRendererPoisonIsSticky(t *testing.T) {
	th := streamTheme(t)
	doc := "prose\n\n<div>\nhtml\n</div>\n\ntail prose\n"
	var r StreamRenderer
	r.Render(th, theme.TierTrueColor, 80, doc)
	if !r.poisoned {
		t.Fatal("precondition: document with an HTML block should be poisoned")
	}
	before := r.renders
	for i := 0; i < 5; i++ {
		r.Render(th, theme.TierTrueColor, 80, doc)
	}
	if !r.poisoned {
		t.Error("poison did not survive later ticks")
	}
	if got := r.renders - before; got != 5 {
		t.Errorf("5 ticks on a poisoned document made %d Markdown calls, want 5", got)
	}
}

// A blockquote or an ATX heading interrupts a paragraph with no blank
// line between them, so grouping "text\n>\n" as one block let the
// degenerate blockquote hide inside the paragraph's real content and
// pass the blank-render test wrongly. Found by
// FuzzStreamedMarkdownMatchesOneShot on "00000\n>\n\n0\n".
func TestStreamRendererBlockquoteInterruptsParagraph(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"00000\n>\n\n0\n",
		"some text\n> \n\nmore text\n",
		"paragraph\n# \n\nafter\n",
		"paragraph\n>quoted right after\n\nafter\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 3, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// A definition-list marker ("`: `") retroactively reclassifies the
// PRECEDING paragraph as a term and re-renders it, even across a blank
// line - a backward-reaching hazard nothing forward-only can predict.
// The whole document must poison. Found by
// FuzzStreamedMarkdownMatchesOneShot on "0000000\n\n: \n".
func TestStreamRendererDisabledByDefinitionList(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"0000000\n\n: \n",
		"term\n\n: a real definition\n",
		"term\n: tight definition\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 3, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// An empty ATX heading followed, with no blank line, by a paragraph is
// not entirely blank as a tail (it contains real text), so the
// whole-tail blank check alone misses it - the heading's own blank row
// gets silently eaten by compose's leading-blank trim once it is ahead
// of real content. Found by FuzzStreamedMarkdownMatchesOneShot on
// "00000\n\n#\n0".
func TestStreamRendererDegenerateBlockFollowedByRealContent(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"00000\n\n#\n0",
		"00000\n\n#\n\nmore text\n",
		"para\n\n>\nquoted text\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 3, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// A block that closes via a singleton transition (an ATX heading, which
// is always exactly one line) with no blank line separating it from
// what follows is tested for blank-rendering the MOMENT it closes, not
// only when Render happens to look at whatever is currently open.
// Without that, an earlier singleton in the same tick can be superseded
// by a later block before anything ever examines it. Found by
// FuzzStreamedMarkdownMatchesOneShot on "0\n\n#\n0\n".
func TestStreamRendererBlockSupersededWithinOneTick(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"0\n\n#\n0\n",
		"para\n\n#\ntext right after\n",
		"para\n\n##\n\n### \nmore\n",
	} {
		var r StreamRenderer
		// A single large chunk delivers the whole document in one
		// tick, which is exactly the shape that let an intermediate
		// block escape testing.
		got := r.Render(th, theme.TierTrueColor, 80, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q one-tick delivery differs from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// A closing fence must use the SAME character and be AT LEAST as long
// as its opener - a three-backtick line inside a four-backtick fence is
// ordinary content, not a closer. Found by
// FuzzStreamedMarkdownMatchesOneShot on "````\n```\n\n0\n".
func TestStreamRendererFenceCloserRequiresMatchingLength(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"````\n```\n\n0\n",
		"````go\ncode with ``` inside it\n````\n\nafter\n",
		"~~~~\n~~~\nstill inside\n~~~~\n\nafter\n",
		"```\ncode\n```\n\nafter\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 5, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}

// A backtick fence's info string (whatever follows the opening run) may
// not itself contain a backtick - CommonMark reserves that shape for an
// inline code span, so "```00`" is a paragraph, not a fence. A tilde
// fence has no such restriction. Found by
// FuzzStreamedMarkdownMatchesOneShot on "```00`\n```\n\n0\n".
func TestStreamRendererBacktickInfoStringRejectsBacktick(t *testing.T) {
	th := streamTheme(t)
	for _, doc := range []string{
		"```00`\n```\n\n0\n",
		"para ```with backtick` text\n\nmore\n",
		"~~~lang`with backtick\ncode\n~~~\n\nafter\n",
	} {
		var r StreamRenderer
		got := feed(t, &r, th, 80, 5, doc)
		if want := Markdown(th, theme.TierTrueColor, 80, doc); got != want {
			t.Errorf("doc=%q streamed differently from one-shot", doc)
			reportFirstDiff(t, got, want)
		}
	}
}
