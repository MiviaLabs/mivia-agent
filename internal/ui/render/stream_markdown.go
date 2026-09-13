package render

// StreamRenderer is Markdown for a growing buffer: it keeps the
// rendered output of the longest PREFIX that is safe to render
// separately (a stable-prefix cache), and renders only the remainder
// each tick, so a repaint of a long streaming reply stays linear
// instead of quadratic in its length.
//
// Render's contract is exact byte identity with Markdown on the same
// arguments, not "visually equivalent" - the transcript commits the
// settled span through Markdown directly, so any difference is a pop
// the reader sees at the moment the span ends.
//
// The full design - why splicing rendered markdown is legal at all,
// every boundary this scanner refuses and why (fences, lists,
// paragraph interruption, backward-reaching constructs, blocks that
// render to nothing, singleton supersession within one tick, fence
// marker matching), the deliberate choice NOT to cut unbroken prose at
// an unsafe point, and how each rule was found via fuzzing - is
// docs/development/stream-markdown-boundary-rules.md. Read it before
// changing the boundary predicate (scanLine and its helpers below).

import (
	"regexp"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// StreamRenderer caches the rendered prefix of a growing markdown
// buffer. The zero value is ready to use. It is not safe for concurrent
// use: the transcript drives it from the single Bubble Tea goroutine.
type StreamRenderer struct {
	// stableContent is the prefix of the last content whose render is
	// cached. Empty when the cache holds nothing, which is also the
	// state a poisoned document stays in.
	stableContent string
	// stableRender is Markdown(stableContent) for the key below.
	stableRender string

	// The cache key. A change in any of these invalidates everything,
	// because all three change the bytes glamour emits.
	width     int
	tier      theme.Tier
	themeName string

	// scanned is how much of stableContent's successor has already been
	// examined for boundaries and for poison, so each tick pays only for
	// the bytes that arrived since the last one.
	scanned int
	// poisoned means this document contains a document-scoped construct
	// and can never be spliced. It is sticky: once seen, the whole
	// stream renders one-shot.
	poisoned bool
	// candidate is the offset of the most recent blank-line position
	// whose "before" side passed, waiting on the next non-blank line to
	// confirm the "after" side. Zero means none pending.
	candidate int
	// bestCut is the latest confirmed safe boundary found so far.
	bestCut int
	// fenceOpen tracks fenced-code parity across the scan.
	fenceOpen bool
	// fenceChar and fenceLen are the opening fence's marker character
	// and run length. CommonMark requires a closing fence to use the
	// SAME character and be AT LEAST as long as the opener: a run of
	// "````" is only closed by four or more backticks, and a shorter
	// "```" inside it is ordinary content, not a closer. Without this,
	// isFenceLineClosing would toggle fenceOpen on the first
	// short-enough-looking line, closing the fence early and treating
	// its remaining "content" as ordinary text outside it. Found by
	// FuzzStreamedMarkdownMatchesOneShot on "````\n```\n\n0\n".
	fenceChar byte
	fenceLen  int
	// blockUnsafe means the block the scan is inside contains a
	// construct that can reach across the blank line that ends it (a
	// list item or an indented code line, either of which MERGES with a
	// same-kind block on the other side of the blank line).
	//
	// It is a property of the WHOLE block, not of any one line. Neither
	// the first line nor the last is enough: "0. item\ncontinued" is a
	// list whose last line is ordinary prose, and "text\n- item" is a
	// paragraph a list interrupts. Both were found by
	// FuzzStreamedMarkdownMatchesOneShot, and both splice wrongly if the
	// block is judged by a single line.
	blockUnsafe bool
	// sawBlock is false until the first block of content opens; there is
	// nothing to separate before it.
	sawBlock bool
	// atBlockStart is true when the next non-blank line opens a block.
	atBlockStart bool
	// blockStart is the byte offset where the current block's first
	// line begins.
	blockStart int
	// blockEnd is the byte offset just past the last NON-BLANK line of
	// the current block - it excludes any blank lines that follow.
	// content[blockStart:blockEnd] is exactly the block's own source,
	// which blockRendersBlank renders to test whether the block that
	// would end a segment actually draws anything.
	blockEnd int
	// blankCheckedEnd and blankCheckedResult memoize
	// tailOpensWithBlankBlock's own render by blockEnd: an idle tick (no
	// new content since the last one) leaves blockStart/blockEnd
	// unchanged, and without this the check would re-render the same
	// open block on every tick's repaint, doubling the very cost this
	// type exists to remove.
	blankCheckedEnd    int
	blankCheckedResult bool

	// renders and renderedBytes count the calls this renderer has made
	// into Markdown and the total source length it passed. They exist so
	// the saving can be ASSERTED rather than assumed: without the cache
	// the byte count is the sum of the whole buffer once per tick, which
	// is the quadratic growth this type removes.
	renders       int
	renderedBytes int
}

// markdown wraps Markdown so every call the cache makes is counted.
func (s *StreamRenderer) markdown(t theme.Theme, tier theme.Tier, width int, in string) string {
	s.renders++
	s.renderedBytes += len(in)
	return Markdown(t, tier, width, in)
}

var (
	// listMarker matches a bullet or ordered list item opener.
	listMarker = regexp.MustCompile(`^ {0,3}(?:[-*+]|\d{1,9}[.)])(?:\s|$)`)
	// linkRefDef matches a link-reference definition.
	linkRefDef = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:`)
	// defListMarker matches goldmark's definition-list marker, which
	// retroactively reclassifies the paragraph before it.
	defListMarker = regexp.MustCompile(`^ {0,3}:(?:\s|$)`)
	// blockquoteStart and atxHeading are the two constructs this
	// scanner recognizes as interrupting a paragraph - see
	// interruptsParagraph.
	blockquoteStart = regexp.MustCompile(`^ {0,3}>`)
	atxHeading      = regexp.MustCompile(`^ {0,3}#{1,6}(?:\s|$)`)
)

// Reset drops the cache. The next Render starts from a full render.
// The lifetime cost counters survive: they measure what this renderer
// has spent, and a reset is part of that cost, not an escape from it.
func (s *StreamRenderer) Reset() {
	renders, bytes := s.renders, s.renderedBytes
	*s = StreamRenderer{}
	s.renders, s.renderedBytes = renders, bytes
}

// Poisoned reports whether this renderer has given up splicing for the
// current document (see the "Refused for the whole document" section of
// docs/development/stream-markdown-boundary-rules.md) and is falling
// back to a full render every call. A caller that owns this renderer's
// lifetime - resetting it between logically unrelated spans - can use
// this to confirm Reset actually cleared the flag, since a poisoned
// renderer with a stale key match would otherwise silently keep paying
// the full-render cost with no visible symptom besides slower ticks.
func (s *StreamRenderer) Poisoned() bool { return s.poisoned }

// CachedPrefixLen returns how many bytes of the last-rendered content
// are covered by the cached prefix. A caller that owns this renderer's
// lifetime can use it to confirm the cache is actually doing work
// (non-zero after a multi-block span) or that Reset genuinely cleared
// it (zero).
func (s *StreamRenderer) CachedPrefixLen() int { return len(s.stableContent) }

// Render returns Markdown(t, tier, width, content), reusing the cached
// render of a prefix of content when one applies.
func (s *StreamRenderer) Render(t theme.Theme, tier theme.Tier, width int, content string) string {
	if content == "" {
		return ""
	}
	if s.width != width || s.tier != tier || s.themeName != t.Name || !strings.HasPrefix(content, s.stableContent) {
		// A width, theme or tier change invalidates every cached byte.
		// So does content that is not an extension of what we cached: a
		// retry or an assistant_reset replaces the buffer rather than
		// growing it.
		s.Reset()
		s.width, s.tier, s.themeName = width, tier, t.Name
	}

	s.advance(t, tier, width, content)

	if s.stableContent == "" {
		return s.markdown(t, tier, width, content)
	}
	tailSource := content[len(s.stableContent):]
	tail := s.markdown(t, tier, width, tailSource)
	if rendersBlank(tailSource, tail) || s.tailOpensWithBlankBlock(t, tier, width, content) {
		// rendersBlank catches a tail that is ENTIRELY degenerate.
		// tailOpensWithBlankBlock catches the harder case: a degenerate
		// block (like the empty heading above) followed, with no blank
		// line between them, by a block with real content - "#\n0" is
		// not blank as a WHOLE (it contains "0"), but compose()'s
		// leading-blank trim cannot tell the heading's own blank row
		// from ordinary margin once it is buried ahead of real content,
		// and drops it. Found by FuzzStreamedMarkdownMatchesOneShot on
		// "00000\n\n#\n0".
		s.poison()
		return s.markdown(t, tier, width, content)
	}
	return compose(s.stableRender, tail)
}

// advance scans the bytes that arrived since the last call, updates the
// boundary search, and promotes the newest confirmed boundary into the
// cache. It renders at most once per tick, and only when the boundary
// actually moved - and then only the SEGMENT between the old boundary
// and the new one, which is what keeps the whole stream linear.
func (s *StreamRenderer) advance(t theme.Theme, tier theme.Tier, width int, content string) {
	if s.poisoned {
		return
	}
	s.scan(t, tier, width, content)
	if s.bestCut <= len(s.stableContent) {
		return
	}
	source := content[len(s.stableContent):s.bestCut]
	segment := s.markdown(t, tier, width, source)
	if rendersBlank(source, segment) {
		// A block that renders to nothing visible breaks the assumption
		// compose rests on: that a fragment's outer blank lines are
		// glamour's block margins and can be re-derived. An empty ATX
		// heading ("#" with no text) still emits a blank ROW, which
		// compose would trim away as margin. Give up on this document
		// rather than splice around it.
		s.poison()
		return
	}
	if s.stableContent == "" {
		s.stableRender = segment
	} else {
		s.stableRender = compose(s.stableRender, segment)
	}
	s.stableContent = content[:s.bestCut]
}

// poison switches the cache off for the rest of this document.
//
// It drops only the cached payload. The cache KEY (width, tier, theme)
// has to survive, or the next Render sees a key mismatch, calls Reset,
// clears this flag, and walks straight back into the same block - once
// per tick, which is worse than having no cache at all.
func (s *StreamRenderer) poison() {
	s.stableContent = ""
	s.stableRender = ""
	s.candidate = 0
	s.bestCut = 0
	s.poisoned = true
}

// tailOpensWithBlankBlock reports whether the block the scanner has
// currently open lies entirely within the tail (nothing of it has been
// promoted into stableContent yet) and renders blank on its own. It
// reuses the scan's own blockStart/blockEnd - the same tracking
// blockRendersBlank uses at a confirmed boundary - to answer the mirror
// question for the tail's LEADING block, which never goes through that
// confirmation because no blank line offered it as a candidate.
func (s *StreamRenderer) tailOpensWithBlankBlock(t theme.Theme, tier theme.Tier, width int, content string) bool {
	if s.fenceOpen {
		// An unclosed fence is UNFINISHED, not degenerate: "```go\n" on
		// its own can render near-blank simply because its content has
		// not arrived yet, and scanLine already refuses it as a
		// candidate boundary for exactly that reason (the early return
		// in the fenceOpen branch). Judging it here would poison every
		// document that happens to be mid-fence at a flush tick.
		return false
	}
	if s.blockStart < len(s.stableContent) || s.blockEnd <= s.blockStart {
		return false
	}
	if s.blockEnd == s.blankCheckedEnd {
		// Nothing new arrived in this block since the last tick: reuse
		// the answer instead of re-rendering the same bytes.
		return s.blankCheckedResult
	}
	src := content[s.blockStart:s.blockEnd]
	result := rendersBlank(src, s.markdown(t, tier, width, src))
	s.blankCheckedEnd = s.blockEnd
	s.blankCheckedResult = result
	return result
}

// rendersBlank reports whether src carries visible text that out does
// not. Whitespace-only source rendering to nothing is ordinary; real
// text rendering to nothing is a degenerate block.
func rendersBlank(src, out string) bool {
	if strings.TrimSpace(src) == "" {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if !isBlankRendered(line) {
			return false
		}
	}
	return true
}

// scan walks the unexamined bytes one line at a time, maintaining fence
// parity, the poison flag, and the pending/confirmed boundary offsets.
// Everything it tracks is derived from lines it has already passed, so
// the work is proportional to the new bytes, not to the document.
func (s *StreamRenderer) scan(t theme.Theme, tier theme.Tier, width int, content string) {
	for s.scanned < len(content) {
		nl := strings.IndexByte(content[s.scanned:], '\n')
		if nl < 0 {
			// A partial final line: leave it for the next tick, when it
			// is either complete or longer. Scanning it now would judge
			// a boundary on half a line.
			return
		}
		lineStart := s.scanned
		line := content[s.scanned : s.scanned+nl]
		s.scanned += nl + 1
		s.scanLine(t, tier, width, content, lineStart, line, s.scanned)
		if s.poisoned {
			return
		}
	}
}

// scanLine folds one complete line into the scan state. content is the
// full buffer (so blockRendersBlank can slice an absolute range), start
// is this line's own offset, line is its text, and end is the offset
// just past its newline - the cut position a blank line offers.
func (s *StreamRenderer) scanLine(t theme.Theme, tier theme.Tier, width int, content string, start int, line string, end int) {
	trimmed := strings.TrimSpace(line)

	if s.scanFenceLine(trimmed, start, end) {
		return
	}
	if s.fenceOpen {
		// Inside a code block nothing is a boundary and nothing poisons:
		// an HTML tag or a link-reference shape in here is just text.
		s.blockEnd = end
		s.candidate = 0
		return
	}
	if trimmed == "" {
		s.scanBlankLine(end)
		return
	}
	s.scanContentLine(t, tier, width, content, start, line, trimmed, end)
}

// scanFenceLine handles a line that opens or closes a fence, and
// reports whether it did - the caller (scanLine) takes no further
// action on a line this claims. See the doc comment's "Refused as a
// boundary" section for why a fence's whole span, opener to closer, is
// treated as one block.
func (s *StreamRenderer) scanFenceLine(trimmed string, start, end int) bool {
	if !s.fenceOpen && isFenceLine(trimmed) {
		// Opening a fence starts a new block: whatever text preceded it
		// (even with no blank line, since a fence can interrupt a
		// paragraph the same way a heading can) is not part of it.
		s.fenceOpen = true
		s.fenceChar, s.fenceLen = fenceMarker(trimmed)
		s.openBlock(start, true)
		s.blockEnd = end
		s.candidate = 0
		return true
	}
	if s.fenceOpen && isFenceCloser(trimmed, s.fenceChar, s.fenceLen) {
		s.fenceOpen = false
		// This line belongs to the fence's own block, which spans from
		// its opener to its closer.
		s.blockEnd = end
		// The fence just closed. Like a heading, it is now a COMPLETE,
		// self-contained block - whatever follows, even without a
		// blank line, starts fresh. Getting this wrong made the
		// closing "```" alone the tracked block on the next tick,
		// which reliably renders blank on its own and poisoned every
		// document that streams a fenced code block. Found by
		// TestStreamRendererAdvancesTheStablePrefix once
		// tailOpensWithBlankBlock started testing it.
		s.atBlockStart = true
		s.candidate = 0
		return true
	}
	return false
}

// scanBlankLine handles a blank line: it may offer the position after
// it, end, as a cut candidate.
func (s *StreamRenderer) scanBlankLine(end int) {
	// A blank line offers the position after it as a cut, provided the
	// block above cannot continue past it. In a RUN of blank lines the
	// first one wins. That is a preference, not a rule: the extra
	// blanks then ride on the delta instead of the prefix, and compose
	// trims both sides, so either choice renders the same bytes. Taking
	// the first just avoids re-testing the same unchanged blockEnd once
	// per blank line.
	if s.candidate == 0 && s.beforeSideAllows() {
		s.candidate = end
	}
	// Whatever comes next opens a new block.
	s.atBlockStart = true
}

// scanContentLine handles a non-blank, non-fence line: the poison
// checks, the paragraph-interruption and singleton-supersession tests
// (see the doc comment's "Blocks that render to nothing" section), and
// the candidate confirmation a preceding blank line may have offered.
func (s *StreamRenderer) scanContentLine(t theme.Theme, tier theme.Tier, width int, content string, start int, line, trimmed string, end int) {
	// Poison checks first, since they end the scan.
	if strings.HasPrefix(trimmed, "<") || linkRefDef.MatchString(line) || defListMarker.MatchString(line) {
		s.poison()
		return
	}
	// interrupts is CommonMark's "paragraph interruption": a blockquote
	// or an ATX heading opens a NEW block even with no blank line before
	// it ("text\n> quote" is two blocks, not a lazy continuation of the
	// paragraph). Missing this let a degenerate block get silently
	// absorbed into the block before it - content[blockStart:blockEnd]
	// spanned both, rendered non-blank because the FIRST block had real
	// text, and blockRendersBlank never saw the second block on its own.
	// Found by FuzzStreamedMarkdownMatchesOneShot on the corpus entry
	// "00000\n>\n\n0\n" (a paragraph, an empty blockquote, then text).
	// This list is not CommonMark-complete (a thematic break and certain
	// list transitions can also interrupt); it covers the constructs
	// this scanner already reasons about elsewhere, and an unrecognized
	// interruption is conservative-safe, not silently wrong: it is
	// merely grouped with the block before it for the blank-render test,
	// the same test a genuinely single block gets.
	interrupts := interruptsParagraph(line)
	if s.supersededBlockRendersBlank(t, tier, width, content, interrupts) {
		s.poison()
		return
	}
	// This line is the "after" side of any pending candidate. Confirming
	// it renders the block that is about to end and refuses a boundary
	// that would end on a block drawing nothing - see the doc comment's
	// "Blocks that render to nothing" section. (This is a DIFFERENT
	// transition from the one above: a blank line in between is what
	// creates a candidate at all, so the two checks fire on disjoint
	// cases in practice, though nothing is wrong if they both happen to
	// cover the same block once.)
	if s.candidate != 0 {
		if s.afterSideAllows(line) && !s.blockRendersBlank(t, tier, width, content) {
			s.bestCut = s.candidate
		}
		s.candidate = 0
	}
	s.openBlock(start, interrupts)
	s.blockEnd = end
	if listMarker.MatchString(line) || isIndented(line) {
		s.blockUnsafe = true
	}
	if atxHeading.MatchString(line) {
		// An ATX heading is a SINGLETON: it is always exactly one line,
		// so whatever comes next - even with no blank line separating
		// them - starts a fresh block, the same reasoning as a fence's
		// closer (see scanFenceLine). Missing this let an empty heading
		// absorb the paragraph after it into one tracked block, whose
		// combined render has real text and so passes the blank-render
		// test even though the heading itself is degenerate. Found by
		// FuzzStreamedMarkdownMatchesOneShot on "0\n\n#\n0\n".
		s.atBlockStart = true
	}
}

// supersededBlockRendersBlank reports whether the block about to be
// LEFT BEHIND - by an interruption, or by the forced-fresh-start after
// a singleton (a closed fence, an ATX heading) - renders blank. That
// block never goes through the candidate/blank-line confirm path above,
// because there was no blank line to offer it as a candidate; if
// nothing tests it here, it is simply gone the moment openBlock
// overwrites blockStart/blockEnd, and Render's own
// tailOpensWithBlankBlock only ever sees whichever block is CURRENT,
// not one an EARLIER line in the same tick already superseded.
// "0\n\n#\n0\n" is exactly this: the empty heading closes (singleton),
// the next "0" opens fresh with no blank line between them, and by the
// time anything looks, the heading's span is already history. Found by
// FuzzStreamedMarkdownMatchesOneShot.
func (s *StreamRenderer) supersededBlockRendersBlank(t theme.Theme, tier theme.Tier, width int, content string, interrupts bool) bool {
	if !(interrupts || s.atBlockStart) || !s.sawBlock || s.blockEnd <= s.blockStart || s.blockStart < len(s.stableContent) {
		return false
	}
	src := content[s.blockStart:s.blockEnd]
	return rendersBlank(src, s.markdown(t, tier, width, src))
}

// openBlock starts a fresh block at content offset start when the scan
// is at a block start (after a blank line, or at an interruption), and
// does nothing when it is mid-block.
func (s *StreamRenderer) openBlock(start int, interrupts bool) {
	if s.sawBlock && !s.atBlockStart && !interrupts {
		return
	}
	s.blockUnsafe = false
	s.sawBlock = true
	s.atBlockStart = false
	s.blockStart = start
}

// interruptsParagraph reports whether line opens a new block even with
// no blank line before it.
func interruptsParagraph(line string) bool {
	return blockquoteStart.MatchString(line) || atxHeading.MatchString(line)
}

// beforeSideAllows reports whether the block that just ended CAN be
// separated from whatever follows on structural grounds - the merge
// hazards a list or an indented code block create. It is silent on
// whether the block itself draws anything; blockRendersBlank answers
// that, at confirmation time, because it needs a render.
func (s *StreamRenderer) beforeSideAllows() bool {
	// fenceOpen needs no test here: scanLine returns before the blank-line
	// branch while a fence is open, so this is only ever reached outside
	// one.
	return s.sawBlock && !s.blockUnsafe
}

// blockRendersBlank renders the block that is about to end - the slice
// from where it opened to the last non-blank line it contained - and
// reports whether that render draws nothing. It is the general
// replacement for pattern-matching every markdown construct that can
// render blank (an empty heading, an empty blockquote, and whatever a
// future glamour version adds): rendering the actual block and checking
// its output is the only test that cannot miss one.
//
// This costs one Markdown call per candidate boundary that reaches
// confirmation, not per byte, so it stays linear in the number of
// blocks in the document.
func (s *StreamRenderer) blockRendersBlank(t theme.Theme, tier theme.Tier, width int, content string) bool {
	if s.blockEnd <= s.blockStart {
		return false
	}
	src := content[s.blockStart:s.blockEnd]
	return rendersBlank(src, s.markdown(t, tier, width, src))
}

// afterSideAllows reports whether the block starting at line can be
// rendered without the block above it.
func (s *StreamRenderer) afterSideAllows(line string) bool {
	// An indented line may attach to the block above as a continuation:
	// two adjacent indented code blocks merge into one.
	return !isIndented(line)
}

// isFenceLine reports whether trimmed OPENS a fenced code block: a run
// of three or more backticks or tildes, where a BACKTICK fence's info
// string (whatever follows the run) contains no further backtick -
// CommonMark reserves that shape for inline code spans, so "```00`" is
// not a fence at all, it is a paragraph. A tilde fence has no such
// restriction. Found by FuzzStreamedMarkdownMatchesOneShot on
// "```00`\n```\n\n0\n": treating it as an opener made the scanner
// believe a fence was open when goldmark saw plain text.
func isFenceLine(trimmed string) bool {
	char, n := fenceMarker(trimmed)
	if n < 3 {
		return false
	}
	if char == '`' && strings.IndexByte(trimmed[n:], '`') >= 0 {
		return false
	}
	return char == '`' || char == '~'
}

// fenceMarker returns the character and run length of trimmed's leading
// fence marker (backticks or tildes), or (0, 0) if it has none.
func fenceMarker(trimmed string) (char byte, n int) {
	if trimmed == "" {
		return 0, 0
	}
	char = trimmed[0]
	if char != '`' && char != '~' {
		return 0, 0
	}
	for n < len(trimmed) && trimmed[n] == char {
		n++
	}
	return char, n
}

// isFenceCloser reports whether trimmed closes a fence that opened with
// openChar repeated openLen times. CommonMark requires the SAME
// character, AT LEAST as many of them, and NOTHING ELSE on the line
// except trailing whitespace: a run of three backticks inside a
// four-backtick fence is content, not a closer, and "```extra" is
// content too, not a closer with a stray info string.
func isFenceCloser(trimmed string, openChar byte, openLen int) bool {
	char, n := fenceMarker(trimmed)
	if n < 3 || n < openLen || char != openChar {
		return false
	}
	return strings.TrimSpace(trimmed[n:]) == ""
}

func isIndented(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

// compose joins two rendered fragments the way glamour separates two
// blocks: each side's own outer blank lines dropped, exactly one blank
// line between them. The head keeps its leading blanks and the tail its
// trailing blanks, because those belong to the document's first and last
// block and survive in the one-shot render.
func compose(head, tail string) string {
	trimmedHead := trimTrailingBlankLines(head)
	trimmedTail := trimLeadingBlankLines(tail)
	if trimmedHead == "" {
		return tail
	}
	if trimmedTail == "" {
		return head
	}
	return trimmedHead + "\n\n" + trimmedTail
}

func trimTrailingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && isBlankRendered(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func trimLeadingBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for len(lines) > 0 && isBlankRendered(lines[0]) {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// isBlankRendered reports whether a RENDERED line draws nothing. The
// styles have to come off first: glamour emits colour for padding
// columns, so a line that is visually empty is not an empty string.
func isBlankRendered(line string) bool {
	return strings.TrimSpace(xansi.Strip(line)) == ""
}
