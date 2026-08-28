# Mivia terminal UI - wireframes, variant D "Panes"

Status: shipped. This is the visual specification for the current terminal UI (`internal/ui/*`).
Supersedes the recommendation in `wireframes.md`. That file stays as the record of
variants A, B and C. This file specifies the direction that shipped.
Companions: `research-panes.md` (new evidence), `mivia-ui-mock-panes.html` (colour, disposable).

All frames are 80 columns. Section 14 states what changes at 120.

---

## 1. What variant D is

D is the middle ground between A (Ledger) and B (Blocks), decided by review:

- **Take from B**: collapsible blocks, labelled block headers, and real dialogs.
- **Reject from B**: the vertical gutter line down the message body.
- **Take from A**: the message body is plain text at a fixed indent, with nothing
  drawn beside it.

The rule that follows: **structure is carried by the header line and by indentation,
never by a vertical rule.** A block is a header row plus an indented body. The eye
finds the left edge from the header, not from a drawn line.

This also removes the main mechanical objection to B in `research.md` section 6: a
gutter costs 2 of 80 columns at every level, and a vertical rule beside text that has
already scrolled into scrollback cannot be repainted anyway.

### 1.1 Nothing draws a border

An earlier revision allowed a frame around modal dialogs. That is withdrawn. Dialogs
are a **background wash** with a title band, and no box glyph is drawn anywhere in the
UI chrome. Section 8 gives the reason, which is mechanical: a hand-aligned frame is a
recurring correctness bug, and four of four framed dialogs in the previous revision
were ragged.

The one place box glyphs remain is **inside rendered content** - a mermaid diagram
draws boxes because the boxes are the information, not decoration. See section 16.

---

## 2. Block anatomy

```
label  detail  meta   state
  body line
  body line
```

- column 1 holds the collapse marker: `v` (open), `>` (closed), or a space.
- `label` is the block kind, at column 3. Dim, never bold. A block that cannot
  collapse still starts at column 3, so every header aligns.
- `detail` is the subject: a path, a command, a subagent name.
- `meta` and `state` sit inline, immediately after `detail`, separated by a fixed
  two-column gap - not right-aligned to the pane width. Chat rows vary too widely
  in `detail` length for a right-aligned column to read as a scannable table; a
  fixed gap keeps the status close to the content it describes, the way other
  desktop chat and agent UIs place inline tool metadata. State is always a word,
  never only a colour.
- the body is indented 4 columns. Nothing is drawn in columns 1 to 4 of a body line.
  Section 11 and every drawn wireframe use 4. An earlier revision of this section
  said 2, which was wrong.

Assistant prose is not a block. It has no header and no indent. It is the only content
at column 1, which is what makes it read as the conversation rather than as tooling.

---

## 3. Glyph table

| Role | Unicode | ASCII | Note |
|---|---|---|---|
| user marker | `>` | `>` | identical |
| collapsed | `>` | `>` | identical |
| expanded | `v` | `v` | identical |
| tool ok | `+` | `+` | identical |
| tool failed | `x` | `x` | identical |
| pending | `?` | `?` | identical |
| spinner | `-\|/-` | `-\|/-` | identical, 4 frames |
| progress | `#` and `.` | `#` and `.` | identical |
| selected row | reverse video | reverse video | not a glyph |
| mark, idle | `U+2B16` | `<` | the logo at rest |
| mark, rotating | `U+2B16`..`U+2B19` | `<` `^` `>` `v` | 4 frames |
| mark, filling | `U+25C7` `U+25C8` `U+25C6` | `.` `o` `0` | streaming |
| mark, pending | `U+25C8` | `?` | static |
| mark, failed | `U+25C6` | `X` | static |

Every glyph in D is already ASCII. The Unicode and ASCII sets are the same set. This is
deliberate and it is why the ASCII degradation tier in the mock differs from truecolor
only in colour.

---

## 4. Transcript, every block type

```
> Add retry with exponential backoff to the S3 uploader, and cover it
  with a test.

I will add a bounded retry to the uploader transport. Three attempts, full
jitter, and a cap of 5s.

    func (u *Uploader) put(ctx context.Context, k string, b []byte) error {
        return retry.Do(ctx, retry.Policy{Max: 3, Cap: 5 * time.Second},
            func() error { return u.raw.Put(ctx, k, b) })
    }

> reasoning                                     84 words  … +2 lines  hidden

v read_file   internal/storage/s3_uploader.go        48 lines   12ms   ok
    package storage
    import ("context"; "time")
    ...

v edit        internal/storage/s3_uploader.go           +4 -1   31ms   ok
    @@ -14,7 +14,11 @@ func (u *Uploader) put(
    -   return u.raw.Put(ctx, k, b)
    +   return retry.Do(ctx, retry.Policy{
    +       Max: 3,
    +       Cap: 5 * time.Second,
    +   }, func() error { return u.raw.Put(ctx, k, b) })

v subagent    test-writer                             2 of 3  18.0s    running
    [######################........]  67%
    + read existing table tests
    + draft TestPutRetriesOnTransient
    - run the package test

v run_command go test ./internal/storage/...        1 failure   4.1s   failed
│   x s3_uploader_test.go:88: want 3 attempts, got 1

v plan                                                 2 of 4          open
    [x] add retry policy to the transport
    [x] wire the uploader through it
    [ ] fix the fake transport to count attempts
    [ ] run the package test again

v error       transport refused after 3 attempts                      fatal
│   dial tcp 10.0.0.4:443: i/o timeout

  notice      context 62% (78k of 125k). /compact frees about 30k.

1,284 in  2,940 out  340 cached  $0.04

>
```

A header-only block - a one-line `notice`, a pending tool call - carries a blank marker
column: there is no body to open. A collapsed block whose body is hidden states the
magnitude in the meta column: `… +N lines`. A body sits at plain 4-column indent by
default; the `│` rail marks only the two moments that must stand out, the focused block
and the failed block. `usage` is not a block: it is one dim footer line per turn, in
the meta grammar above. Durations follow one ladder at every surface
(`render.FormatElapsed`): under 1s prints `250ms`, under 60s prints `4.1s`, 60s and up
print `1m 05s`.

The transcript groups by turn (transcript-polish.md R1): the user line and the
assistant prose sit at column 1, and the turn's tool activity hangs under them as one
group at a 2-column indent. A blank row separates sections - before prose, before a
group, before the usage footer - and never falls inside a group, so a burst of tool
calls reads as one dense run. Two or more consecutive collapsed read-only lookups
(reads, searches) coalesce into one leader row, `> Read 2 files: a.go, b.go  2 files`;
a click on the row, or `Space` with the focus inside the run, dissolves it back into
the per-block headers. Coalescing is display-only: the pager, the `y` copy, and the
scrollback dump keep every per-block header and body.

Keys: `Tab` / `Shift-Tab` focus the next or previous block. `Space` or `Enter` toggles
the focused block. `Ctrl-E` expands all, `Ctrl-W` collapses all. `y` copies the focused
block. `Ctrl-O` opens the focused block in the pager. `Ctrl-C` cancels the turn, twice
quits. `?` on an empty composer prints the keymap.

---

## 5. Collapsed against expanded

The same two blocks, closed and open. Collapsing must not move any other row, so the
header row changes only in its marker cell. A closed block with a body also states its
magnitude in the meta column - `… +N lines` - so the reader sees what expanding reveals.
The state word never moves to the end of the row, and the detail clips before it.

```
> read_file   internal/storage/s3_uploader.go   48 lines  12ms  … +3 lines   ok
> edit        internal/storage/s3_uploader.go      +4 -1  31ms  … +3 lines   ok
```

```
v read_file   internal/storage/s3_uploader.go        48 lines   12ms   ok
    package storage
    import ("context"; "time")
    ...
v edit        internal/storage/s3_uploader.go           +4 -1   31ms   ok
    @@ -14,7 +14,11 @@
    -   return u.raw.Put(ctx, k, b)
    +   return retry.Do(ctx, retry.Policy{Max: 3})
```

**Live-window constraint.** A block stays interactive while it is in the live window.
The window holds the blocks that fit in the terminal height minus the reserved chrome.
The renderer sets the default state when the block finalizes, from a size threshold.
Eviction prints the block to scrollback once and freezes it. Finalization does not
freeze a block, because a finalized block is usually still on screen.
Default: open under 12 body lines, closed at or above.

---

## 6. Streaming, mid-token

```
I will add a bounded retry to the uploader transport. Three attempts, ful_

  - thinking   4.2s   62% ctx   esc to cancel
```

Only the last partial line and the status line repaint. No header is redrawn. Keys:
`Esc` cancels and keeps the partial text, `Ctrl-C` cancels and discards, `Ctrl-S` hides
or shows the reasoning block while the turn runs.

---

## 7. Tool approval, inline

The default. It matches the block shape exactly, so the eye does not move.

```
? edit        internal/storage/s3_uploader.go           +4 -1          pending
    scope workspace, path is inside the project root

    o once    a always    d deny    D deny always    v view diff
```

Keys: `o` `a` `d` `D` `v`. `Enter` takes `once`. `Esc` is `deny`.

---

## 8. Tool approval, dialog

Used when the call is outside the workspace, or when the user sets
`approval.style = dialog`. This is the alternate screen.

**Dialogs draw no border.** There is no `+---+` frame anywhere in this design. A
dialog is a **background wash**: a title band on `accent`, body rows on `bg-subtle`,
the selected row on `border-focus`, a footer on `bg-inset`. Plain text cannot show a
wash. Plain text cannot show a background, and trailing spaces carry no information, so
the panel is drawn below without padding: the wash extends to a fixed **62 columns**,
inset 8, on every row. `>` marks the selected row. The HTML mock shows the real thing.

```
          Approve tool call                              run_command

          the model wants to run
            rm -rf /var/tmp/mivia-cache

          scope   outside the workspace
          risk    deletes files, cannot be undone

            o  once           run this call only
            a  always         auto-approve run_command here
          > d  deny           refuse and tell the model why
            D  deny always    refuse for this whole session

          enter = deny    esc = deny    the safe one is default
```

Two reasons, and the first is mechanical rather than aesthetic:

1. **A drawn frame is a correctness problem.** Every row must end at the same column,
   so every content edit is a chance to break the right edge. An earlier revision of
   this document drew four framed dialogs by hand and **all four were ragged** - found
   by a script, not by eye. A wash has no edge to misalign: the renderer pads each row
   to the panel width and the background does the rest.
2. A frame spends four columns and two rows on chrome that carries no information, in
   a design whose premise is that structure comes from type and space.

The renderer must **clip** a row wider than the panel rather than let it push past the
edge. The mock implements exactly that, with a `~` marking the clip.

The default for a dialog approval is **deny**, not `once`. An inline approval defaults
to `once` because it was judged safe enough to stay inline. Anything promoted to a
dialog was not.

---

## 9. Status line and top bar

The status line is permanent: always the row above the composer, idle or busy.
A fixed top bar carries the brand mark, the `mivia` wordmark, the bound model,
and the context share; it changes at turn boundaries only, never per token.
Every rendered row is framed by a one-column gutter, so no text touches the
screen edge.

```
  - running  go test ./internal/storage/...   12s   62% ctx   esc to cancel
```

Truncation order from the right when the terminal narrows: context use, elapsed,
activity detail.

---

## 10. Composer and slash completion

```
> /comp_
    /compact     summarise the transcript and free context
    /completion  print the shell completion script
```

Keys: `Tab` accepts the common prefix, `Up` / `Down` select, `Enter` accepts, `Esc`
dismisses, `Ctrl-U` clears the line. At most 6 rows, scrolling.

---

## 11. Unified diff

```
v edit        internal/storage/s3_uploader.go           +4 -1   31ms   ok
    @@ -14,7 +14,11 @@ func (u *Uploader) put(ctx, k, b) error {
      14      ctx, cancel := context.WithTimeout(ctx, u.timeout)
      15      defer cancel()
      16
      17  -   return u.raw.Put(ctx, k, b)
          +   return retry.Do(ctx, retry.Policy{
          +       Max: 3,
          +       Cap: 5 * time.Second,
          +   }, func() error { return u.raw.Put(ctx, k, b) })
      18  }
```

No gutter. The diff body sits at the standard 4-column body indent, and the `+` and `-`
signs carry the meaning with no colour. Keys: `Ctrl-O` full screen, `n` and `N` between
hunks, `s` side-by-side at 120 columns or wider.

---

## 12. Dialogs

Same wash, no borders. Width 62 columns, inset 8, clipped rather than wrapped.

### 12.1 Theme picker

Every row previews its own palette, so the choice is made from colour, not from names.
Selection previews live: the whole UI re-skins as the cursor moves.

```
          Theme                                         20 available

        > Mivia Dark        the quick brown fox  ok warn fail info
          Mivia Light       the quick brown fox  ok warn fail info
          Catppuccin Mocha  the quick brown fox  ok warn fail info
          Tokyo Night       the quick brown fox  ok warn fail info
          Gruvbox Dark      the quick brown fox  ok warn fail info

          up/down preview   enter apply   l light/dark   esc
```

### 12.2 Session picker

```
          Session                                        / to filter

        > s3-retry-backoff        14 turns   2h ago    41k ctx
          workflow-repair         31 turns   1d ago    88k ctx
          theme-contrast-audit     6 turns   3d ago    12k ctx

          enter load   n new   d delete   esc cancel
```

### 12.3 Model and effort picker

```
          Model and effort

        > claude-opus-5        low  med  HIGH  max
          claude-sonnet-5      low  MED   high  max
          claude-haiku-4-5     LOW  med

          up/down model   left/right effort   enter apply   esc
```

---

## 13. Error and cancellation

An error is a block, not a dialog. A dialog would demand a decision the user does not
have to make.

```
  error       transport refused after 3 attempts                      fatal
    dial tcp 10.0.0.4:443: i/o timeout
    retry with /retry, or /model to switch provider
```

Cancellation, after `Ctrl-C` mid-stream. The partial text stays, and the reason is
stated so the transcript does not lie about why it stopped.

```
I will add a bounded retry to the uploader transport. Three attempts, ful

  cancelled   by user at 4.2s   1,284 in  0 out   $0.004
```

---

## 14. What changes at 120 columns

| Element | 80 columns | 120 columns |
|---|---|---|
| prose wrap | 76 columns | 92 columns, not 116 |
| block header | label, detail, meta, state | adds the tool scope and the byte count |
| theme grid | 2 columns | 3 columns |
| diff | unified | side-by-side available on `s` |
| status line | drops fields right to left | every field plus the model name |
| dialogs | 62 columns wide | 72 columns wide, never full width |

Below 80: the status line sheds fields, then the header clips the detail. The header
keeps the meta and the state, because the state carries meaning. A clipped detail ends
with `~`. Below 40 columns the renderer prints the plain stream format.

---

## 15. Keymap, complete

| Key | Acts on | Does |
|---|---|---|
| `Tab` / `Shift-Tab` | live window | focus next / previous block |
| `Space` / `Enter` | focused block | toggle collapsed |
| `Ctrl-E` / `Ctrl-G` | live window | expand all / collapse all |
| `y` | focused block | copy to clipboard (OSC 52) |
| `Ctrl-O` | focused block | open in the pager |
| `Esc` | turn, dialog | cancel turn keeping text; close dialog |
| `Ctrl-C` | turn | cancel discarding; twice quits |
| `Ctrl-R` | global | hide or show reasoning |
| `o` `a` `d` `D` | approval | once, always, deny, deny always |
| `Ctrl-T` | global | theme dialog |
| `Ctrl-P` | global | model and effort dialog |
| `Ctrl-L` | global | session dialog |
| `n` / `N` | pager | next / previous hunk |
| `s` | pager | side-by-side, 120 columns or wider |
| `/` | dialog | filter |
| `?` | empty composer | print the keymap inline |
| `Tab` | composer | accept completion prefix |
| `Ctrl-U` | composer | clear the line |

---

## 16. Markdown and mermaid

These are the two content types the agent emits most often after plain text, and
neither is a mivia invention: both have an existing Go renderer.

### 16.1 Markdown, through glamour

`charmbracelet/glamour` renders markdown to ANSI with a **stylesheet**, and the
stylesheet is JSON with one entry per element (`heading` h1..h6, `code_block` with a
nested `chroma` section, `table`, `list`, `link`, `block_quote`, `emph`, `strong`),
where colours are ANSI numbers or hex. Word wrap is a render option.

The integration rule follows directly: **the glamour stylesheet is generated from the
mivia theme, not authored.** One function maps theme roles onto glamour keys, and it
runs again on every theme switch. If the stylesheet were a static asset, markdown would
stop matching the rest of the UI the moment the user changed theme, and the theme
system would no longer be the single source of style.

Rendered at 80 columns, the element set looks like this:

```
Retry policy
============

The uploader now retries transient failures only. Permanent failures
still fail fast, because retrying a 403 cannot succeed.

How the backoff is computed
---------------------------

  1. base delay is 100ms
  2. each attempt multiplies by 2
  3. full jitter is applied, then the result is capped

| Full jitter beats equal jitter under contention. See the AWS
| architecture blog for the measurements.

  [x] retry policy added
  [x] uploader wired through it
  [ ] fake transport counts attempts

  attempt   delay    cumulative
  -------   -----    ----------
  1         100ms    100ms
  2         200ms    300ms
  3         400ms    700ms

Inline code reads as  retry.Policy{Max: 3}  and a link is underlined
with the target dimmed after it.

    if isTransient(err) {
        continue
    }

------------------------------------------------------------------
```

Decisions this forces:

- **Wrap at the prose measure, not the terminal width.** 76 columns at 80, 92 at 120,
  as in section 14. Markdown that fills a 200-column terminal is unreadable.
- **Tables are the failure case.** A markdown table wider than the terminal cannot
  wrap without becoming unreadable. Rule: shrink the widest column to its content, then
  drop trailing columns with a `+2 more` marker, then fall back to a definition list
  under 60 columns. Do not horizontally scroll a table inline.
- **Render once, at `text.end`.** Streaming raw text and re-rendering markdown per
  delta would re-flow the whole block on every token. Stream plain, render on
  completion, cache by message id.

### 16.2 Mermaid, through mermaid-ascii

`AlexanderGrooff/mermaid-ascii` lays out mermaid source as terminal art. It covers
**flowchart, sequence diagram and entity relationship diagram**. It does not cover
subgraphs, non-rectangular node shapes, diagonal arrows, sequence activation boxes,
class diagrams or state diagrams.

Flowchart:

```
    +-----------+     +------------+     +-----------+
    | upload    |---->| retry.Do   |---->| raw.Put   |
    +-----------+     +-----+------+     +-----+-----+
                            |                  |
                            +--->+--------+<---+
                                 | backoff|
                                 +--------+
```

Sequence:

```
    +-------+        +----------+      +--------+
    | agent |        | uploader |      |   s3   |
    +---+---+        +----+-----+      +---+----+
        |                 |                |
        | put(key, blob)  |                |
        +---------------->|                |
        |                 | PutObject      |
        |                 +--------------->|
        |                 | 503 slow down  |
        |                 |<- - - - - - - -+
        |                 |                |
        |   loop 3 attempts, full jitter   |
        |                 +--------------->|
        |                 | 200 ok         |
        |                 |<- - - - - - - -+
        |<----------------+                |
```

Entity relationship:

```
    +---------------+              +------------------+
    | UPLOAD        |              | ATTEMPT          |
    +---------------+              +------------------+
    | string  key PK|---o{         | int     n        |
    | int     bytes |   makes      | int     status   |
    | time    at    |              | dur     delay    |
    +---------------+              +------------------+
```

Decisions this forces:

- **A diagram is content, so box glyphs are correct here.** This is the one exception
  to section 1.1, and the test is whether removing the glyph destroys information. In a
  dialog frame it does not. In a diagram it does.
- **Never drop a diagram.** An unsupported type, a parse error or an over-wide layout
  all fall back to the fenced source, syntax highlighted, with the reason in the block
  header (`not supported`, `source`). Silently swallowing a diagram is the worst
  outcome, because the user cannot tell that anything was lost.
- **Colour maps to roles.** Mermaid `classDef fill:#f9f` names a literal colour, which
  would break theming. Map declared classes onto theme roles in declaration order and
  ignore the literal hex.
- **Diagrams are wide.** A diagram wider than the terminal scrolls horizontally inside
  its block rather than wrapping, because wrapping a diagram destroys it. Over the
  block width it collapses by default and opens in the pager.

---

## 17. The mark

The Mivia logo is a diamond whose left half is black. That is a Unicode character:
**U+2B16 DIAMOND WITH LEFT HALF BLACK**. Its three neighbours move the black half:
U+2B17 right, U+2B18 top, U+2B19 bottom. Cycling U+2B16, U+2B18, U+2B17, U+2B19 rotates
the diamond, so **the logo and the activity indicator are the same object.** The brand
mark never turns into a generic spinner.

It sits in the first cell of the status line above the composer - the cell the
spinner used to occupy - and a static idle instance sits in the top bar next to
the wordmark. The animated instance is the status line's; the top bar's stays
idle, because the bar is session identity, not turn state.

| State | Unicode | ASCII | Motion | Meaning |
|---|---|---|---|---|
| idle | `U+2B16` | `<` | static, dim | ready for input |
| waiting | `U+2B16` / `U+25C7` | `<` / space | blink, slow | request sent, no bytes yet |
| thinking | `U+2B16`..`U+2B19` | `<` `^` `>` `v` | rotate | model is reasoning |
| writing | `U+25C7` `U+25C8` `U+25C6` | `.` `o` `0` | fill | tokens arriving |
| running | `U+2B16`..`U+2B19` | `<` `^` `>` `v` | rotate, `info` | a tool is executing |
| pending | `U+25C8` | `?` | static, `warning` | approval needed |
| failed | `U+25C6` | `X` | static, `danger` | the turn failed |
| done | `U+2B16` | `<` | static, `success` | turn complete |

Rules:

- **Motion means work.** Four states move, four do not. A still mark means the agent is
  not working, so the absence of motion is information too.
- **Speed is meaning.** `waiting` blinks at a quarter of the rotation rate. A slow mark
  reads as blocked on someone else; a fast one reads as busy here.
- **The word always accompanies the mark.** `thinking`, `running`, `pending` are text
  on the same line. The mark never carries state alone - the same rule that makes the
  UI work under `NO_COLOR` and under colour blindness.
- **`--screen-reader` removes the glyph and the animation** and prints the state word
  only. This is not optional politeness: screen readers announce an animated cell as a
  stream of unrelated characters. `gcloud`, `gh` and Gemini CLI all ship such a mode.
- **The mark is output, not a control.** No key acts on it.
- Animation stops when the program is not on a TTY, and never appears in `--output
  json` or the plain stream renderer.

---

## 18. First-party theme reference

The three original themes, as verified. Every foreground/background pair used by the UI
meets WCAG AA; `mivia-high-contrast` meets AAA (7:1). Measurement method and the
colour-blindness results are in `research-panes.md` sections 3 and 8.

| Role | mivia-dark | mivia-light | mivia-high-contrast |
|---|---|---|---|
| `bg` | `#0a0a0b` | `#fcfcfc` | `#000000` |
| `bg-subtle` | `#17171a` | `#f4f4f5` | `#141414` |
| `bg-inset` | `#050506` | `#e9e9eb` | `#000000` |
| `fg` | `#fafafa` | `#18181b` | `#ffffff` |
| `fg-muted` | `#a1a1aa` | `#52525b` | `#d0d0d0` |
| `fg-subtle` | `#71717a` | `#71717a` | `#a0a0a0` |
| `border` | `#52525b` | `#a1a1aa` | `#a0a0a0` |
| `border-focus` | `#fafafa` | `#18181b` | `#ffffff` |
| `accent` | `#fafafa` | `#18181b` | `#ffffff` |
| `accent-fg` | `#0a0a0b` | `#fcfcfc` | `#000000` |
| `success` | `#4edc4e` | `#1d6b53` | `#19e6a8` |
| `warning` | `#f3f34e` | `#7d5a08` | `#e6b319` |
| `danger` | `#dc4e4e` | `#5e0808` | `#ef6c6c` |
| `info` | `#5b8cff` | `#142671` | `#8595d6` |
| `keyword` | `#5b8cff` | `#3f3f46` | `#8595d6` |
| `string` | `#4edc4e` | `#1d6b53` | `#19e6a8` |
| `number` | `#d18fd1` | `#7d5a08` | `#ffffff` |
| `comment` | `#71717a` | `#71717a` | `#a0a0a0` |
| `function` | `#d3ce85` | `#18181b` | `#e6b319` |
| `type` | `#74c9cc` | `#0f5f66` | `#19e6a8` |
| `variable` | `#e4e4e7` | `#27272a` | `#ffffff` |
| `diff-add-fg` | `#4edc4e` | `#1d6b53` | `#19e6a8` |
| `diff-add-bg` | `#0d2410` | `#eaf5ee` | `#00220f` |
| `diff-del-fg` | `#dc4e4e` | `#5e0808` | `#ef6c6c` |
| `diff-del-bg` | `#240c0d` | `#faeaea` | `#2a0000` |
| `diff-hunk` | `#5b8cff` | `#52525b` | `#8595d6` |

Notes:

- `accent` is achromatic in all three. It is chrome - prompt marker, focus, selection -
  and never encodes a status.
- The dark status colours are the Linux VGA bright family, with the blue lifted from
  VGA's `#4e4edc` (which fails contrast at 3.25) to `#5b8cff`.
- The dark syntax roles are toned down from the VGA primaries: `type` `#74c9cc`,
  `function` `#d3ce85`, `number` `#d18fd1`, each about half the saturation of the
  status colours. Syntax is a background texture; status is a signal.
- `warning` is deliberately **not** toned. Measured, reducing its saturation drops the
  worst-case colour-blind separation from 18.5 to 12.2, because its high lightness is
  what separates it from `success` under deuteranopia.
