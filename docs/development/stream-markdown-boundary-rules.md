# StreamRenderer boundary rules

Owner: quality. Implementation:
[internal/ui/render/stream_markdown.go](../../internal/ui/render/stream_markdown.go).
Tests: `stream_markdown_test.go`, fuzz target
`FuzzStreamedMarkdownMatchesOneShot` in `stream_markdown_fuzz_test.go`.

## Why this exists

The transcript re-renders the streaming tail on every flush tick while
an assistant span is live. `render.Markdown` costs about 22
microseconds per source line (`BenchmarkMarkdown`), so re-rendering a
growing document at 15 Hz is quadratic in the length of the reply: a
600-line answer reaches ~15 ms per tick, and a very long one exceeds the
frame budget. `StreamRenderer` keeps the rendered output of the longest
prefix that is safe to render separately, and renders only the
remainder each tick.

## The contract

`Render` returns exactly what `Markdown` would return for the same
arguments — not "visually equivalent", the same bytes. That is the
whole safety property: the transcript commits the settled span through
`Markdown` directly, so any difference between the streamed and
one-shot renders is a pop the reader sees at the moment the span ends,
which is the defect this type exists to remove.

## Why splicing is legal at all

Glamour renders a document block by block. Two properties hold,
established by measurement rather than by reading glamour's source
(see `stream_markdown_test.go`):

1. The lines a block renders to do not depend on the blocks around it,
   provided the construct cannot continue across the blank line.
2. The separator glamour puts between two blocks is one blank line,
   once each side's own leading and trailing blank lines are removed.

So `Markdown(a+b) == compose(Markdown(a), Markdown(b))` whenever the
split point is a boundary where no construct continues. The boundary
predicate is deliberately conservative: it refuses far more positions
than it must, because a wrong "yes" corrupts the transcript and a wrong
"no" only costs a re-render.

## What is refused, and why

Refused as a boundary (the construct can swallow what follows):

- inside an open fenced code block;
- after a list — two adjacent ordered lists merge and the second one's
  numbers continue from the first. What counts is the line that
  *opened* the block, not the last line in it: a list item's
  continuation lines look like ordinary prose;
- after an indented code block — two adjacent ones merge outright;
- before an indented line, which could attach to the block above.

A setext underline needs no rule of its own: it binds to the line
immediately above it with no blank line between them, and a blank line
is exactly what creates a candidate boundary, so an underline can never
be the line that follows one. A thematic break of the same shape
(`---`) can follow a boundary safely — it is a whole block.

Refused for the whole document (the construct is backward-reaching — it
can change how content already rendered would have rendered, which
nothing about a growing prefix can see coming):

- a link-reference definition, because a definition at the end changes
  how a link at the start renders;
- an HTML block, which glamour treats differently depending on what
  precedes it;
- a definition-list marker (`": "` at the start of a line), because
  goldmark's definition-list extension reclassifies the *preceding*
  paragraph as a term and re-renders it entirely differently — even
  across a blank line, so the interruption logic (which only reasons
  forward) cannot catch it. Found by `FuzzStreamedMarkdownMatchesOneShot`
  on `"0000000\n\n: \n"`: the already-cached `"0000000"` line changes
  shape the moment `": "` arrives.

## Blocks that render to nothing

An empty ATX heading (`"#"` alone) and an empty blockquote (`">"`
alone) both still emit a blank row — that is not the same as producing
no output. If a boundary ended on one of these, `compose` would trim
that row away as ordinary block-separator margin, and the spliced
result would be one blank line short of the one-shot render.

Rather than enumerate every markdown construct that can render blank
(there is no closed list — a future glamour version could add one), a
boundary is confirmed only after rendering the block it would end and
checking that the block actually drew something
(`blockRendersBlank`/`supersededBlockRendersBlank`, `tailOpensWithBlankBlock`).

Three related pitfalls the fuzz target found and the code now guards
against:

- **Paragraph interruption.** A blockquote or an ATX heading opens a
  new block even with no blank line before it (`"text\n> quote"` is two
  blocks). Missing this let a degenerate block get silently absorbed
  into the block before it, whose combined render has real text and so
  passes the blank-render test.
- **Singleton supersession within one tick.** An ATX heading and a
  closed fence are always exactly one construct; whatever follows opens
  a fresh block even with no blank line. If a later block supersedes an
  earlier degenerate one before anything examines it (both closing
  within the same scan pass), the earlier block's blank render is never
  tested unless the scanner tests it the moment it closes.
- **Fence marker matching.** A closing fence must use the same
  character as its opener and be at least as long (`` ```` `` is only
  closed by four or more backticks); a backtick fence's info string may
  not itself contain a backtick, or CommonMark treats the line as a
  paragraph, not a fence opener.

## The deliberate omission

Unbroken prose has no blank line and therefore no safe boundary, so the
cache never advances and the cost falls back to a full render per tick
— exactly the non-streamed cost, never worse. The reference implementation this
design is modelled on (charmbracelet/crush) cuts at a plain newline
once the unrendered tail passes 2 KiB. That is not adopted here: a
single newline inside a paragraph is a soft break, the paragraph
re-wraps as a whole, and cutting there breaks the byte identity above.
Correct and occasionally slow beats fast and occasionally wrong;
`TestStreamRendererUnbrokenProseStaysExact` pins the choice.

## Verification

The boundary rules above are pinned by
`FuzzStreamedMarkdownMatchesOneShot`.
The corpus under `internal/ui/render/testdata/fuzz/
FuzzStreamedMarkdownMatchesOneShot/` records each case; the
corresponding `TestStreamRenderer*` case in `stream_markdown_test.go`
pins the fix without needing the fuzz corpus. Re-run the fuzz target
after any change to the boundary predicate:

```bash
go test ./internal/ui/render/ -run XXX -fuzz FuzzStreamedMarkdownMatchesOneShot -parallel 2 -fuzztime 60s
```

Never the default parallelism — see AGENTS.md's fuzzing guard.
