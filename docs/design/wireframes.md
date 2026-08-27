# Mivia terminal UI - ASCII wireframes

Status: Phase 0 design proposal. Not implemented.
Companion files: `research.md` (sources), `mivia-ui-mock.html` (colour and density, disposable).

This file is the width-accurate and glyph-accurate source of truth. The HTML mock
shows colour. This file shows layout. If the two disagree, this file wins.

All frames are drawn at **80 columns**. Section 6 states what changes at 120 columns.
Every frame is inline output. Only the theme picker (view 7) uses the alternate screen.

---

## 1. Glyph table and ASCII fallback

The renderer selects one of three glyph sets. It selects the ASCII set when the locale
is not UTF-8, when `TERM=dumb`, or when the user sets `--ascii`. Colour is a separate
axis: `NO_COLOR` removes colour but keeps Unicode glyphs.

| Role | Unicode | ASCII | Note |
|---|---|---|---|
| user prompt marker | `>` | `>` | same in both sets |
| reasoning marker | `.` | `.` | same in both sets |
| tool pending | `?` | `?` | same in both sets |
| tool success | `+` | `+` | same in both sets |
| tool failure | `x` | `x` | same in both sets |
| block gutter | `|` | `|` | variant B only |
| collapsed block | `>` | `>` | variant B and C |
| expanded block | `v` | `v` | variant B and C |
| spinner frames | `-\|/-\|/` | `-\|/-\|/` | 4 frames, 8 Hz |
| progress filled | `#` | `#` | variant B and C |
| progress empty | `.` | `.` | variant B and C |
| truncation | `...` | `...` | same in both sets |
| composer cursor | `_` | `_` | block cursor drawn by the terminal |

**Design decision**: the two sets are almost identical. The design carries meaning in
weight, dim, indentation and one accent, not in glyphs. This is the direct consequence
of the "no heavy box-drawing, no emoji as iconography" constraint. The only glyphs that
differ between the sets in the mock are the spinner and the progress bar, and both are
transient. A terminal that cannot draw them loses no information.

**Colour is never the only signal.** Every state also carries a text token
(`ok`, `failed`, `denied`, `pending`). This is what makes `NO_COLOR` usable and what
makes the design safe under deuteranopia and protanopia.

---

## 2. Fixture conversation

All three variants render the same content:

1. user message
2. streamed assistant text with markdown and a fenced code block
3. a reasoning block
4. a tool call that waits for approval
5. tool output
6. a unified diff
7. a subagent progress block
8. an error
9. a plan/todo block
10. usage and cost
11. a notice
12. turn end

---

## 3. Variant A - "Ledger" (transcript-first)

Near-zero chrome. The transcript reads like a log file. Every piece of state lives in a
transient line that belongs to the active turn and is removed when the turn ends.
Indentation is the only structure. There is no gutter and no block header.

### A.1 Transcript, all block types

```
> Add retry with exponential backoff to the S3 uploader, and cover it
  with a test.

. Reasoning  the uploader wraps a single PutObject call; the retry belongs
  in the transport, not the caller.

I will add a bounded retry to the uploader transport. Three attempts, full
jitter, and a cap of 5s.

    func (u *Uploader) put(ctx context.Context, k string, b []byte) error {
        return retry.Do(ctx, retry.Policy{Max: 3, Cap: 5 * time.Second},
            func() error { return u.raw.Put(ctx, k, b) })
    }

  read_file  internal/storage/s3_uploader.go                       ok  12ms
    1..48 of 210 lines
    package storage
    import ("context"; "time")
    ...

  edit  internal/storage/s3_uploader.go                            ok  31ms
    --- a/internal/storage/s3_uploader.go
    +++ b/internal/storage/s3_uploader.go
    @@ -14,7 +14,11 @@ func (u *Uploader) put(
    -    return u.raw.Put(ctx, k, b)
    +    return retry.Do(ctx, retry.Policy{
    +        Max: 3,
    +        Cap: 5 * time.Second,
    +    }, func() error { return u.raw.Put(ctx, k, b) })

  subagent test-writer  running        -  2 of 3 steps        18s
    + read existing table tests
    + draft TestPutRetriesOnTransient
    - run the package test

  run_command  go test ./internal/storage/...                  failed  4.1s
    x  s3_uploader_test.go:88: want 3 attempts, got 1
       exit status 1

  Plan
    [x] add retry policy to the transport
    [x] wire the uploader through it
    [ ] fix the fake transport to count attempts
    [ ] run the package test again

  Notice  context is at 62% (78k of 125k). /compact frees about 30k.

  1,284 in  2,940 out  340 cached      $0.041      claude-opus-5      21.4s

>
```

Keys on this view:
`Up`/`Down` scroll native scrollback (the terminal owns it, mivia does not repaint).
`Ctrl-C` cancels the active turn. `Ctrl-C` twice quits. `Ctrl-R` reruns the last turn.
`Ctrl-O` opens the last tool output in the pager. `Ctrl-T` opens the theme picker.
`?` on an empty composer prints the keymap inline.

### A.2 Streaming mid-token

The last line ends without a newline. The status line is the last row and is redrawn in
place. Nothing above the status line is ever repainted.

```
I will add a bounded retry to the uploader transport. Three attempts, ful_

  - thinking   4.2s   esc to cancel
```

Keys: `Esc` cancels the turn and keeps the partial text. `Ctrl-C` cancels and discards.
`Ctrl-S` toggles the reasoning block on or off while the turn runs.

### A.3 Tool approval

```
  ? edit  internal/storage/s3_uploader.go
    +4 -1 lines, 1 hunk

    [o] once   [a] always for edit   [d] deny   [D] deny always   [v] view diff
```

Keys: `o` `a` `d` `D` `v`, `Enter` takes the default (`once`), `Esc` is the same as
`deny`. The decision line is removed after the choice and is replaced by the result
line. No dialog. No border.

### A.4 Transient status line

One line. It is the last row of the frame. It is deleted at turn end.

```
  - running  go test ./internal/storage/...   12s   62% ctx   esc to cancel
```

Fields, left to right: spinner, current activity, elapsed, context use, the escape
hint. The line is truncated from the right at 80 columns and drops fields in this
order: context use, elapsed, activity.

### A.5 Composer and slash completion

```
> /comp_
  /compact     summarise the transcript and free context
  /completion  print the shell completion script
```

Keys: `Tab` accepts the common prefix, `Up`/`Down` move the selection, `Enter` accepts,
`Esc` dismisses the list, `Ctrl-U` clears the line. The list is at most 6 rows and
scrolls. It is drawn below the composer and is erased on dismissal.

### A.6 Unified diff

```
  edit  internal/storage/s3_uploader.go                           +4 -1

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

Added lines carry `diff-add-bg` across the full row. Deleted lines carry `diff-del-bg`.
The `+` and `-` signs stay in the ASCII set, so the diff is readable with no colour.
Keys: `Ctrl-O` opens the diff full-screen, `n` and `N` move between hunks in the pager.

### A.7 Alternate screen - theme picker

This is the only view that leaves inline mode. It saves the cursor, switches to the
alternate buffer, and restores the inline transcript on exit.

```
  Theme                                            preview

  mivia dark            <                          > Add retry with backoff
  mivia light                                        to the uploader.
  catppuccin mocha
  catppuccin latte                                   I will add a bounded
  tokyonight night                                   retry to the transport.
  tokyonight day
  rose-pine                                            func put() error {
  rose-pine dawn                                           return retry.Do(
  gruvbox dark                                         }
  gruvbox light
  nord                                               + added line
                                                     - removed line

  up/down select   enter apply   l toggle light/dark   esc cancel
```

Keys: `Up`/`Down` select and preview live, `Enter` applies and writes the choice to
config, `l` toggles the light or dark variant of the highlighted family, `Esc` cancels
and restores the previous theme, `/` filters the list.

---

## 4. Variant B - "Blocks" (structured blocks)

Every turn is a labelled block. A one-column gutter runs down the left of the block
body. Tool calls and diffs collapse to a single summary row. The gutter is the only
persistent chrome and it is a single character, not a box.

### B.1 Transcript, all block types

```
you                                                            14:22:07
| Add retry with exponential backoff to the S3 uploader, and cover it
| with a test.

opus-5                                                         14:22:08
| v reasoning
|   the uploader wraps a single PutObject call; the retry belongs in the
|   transport, not the caller.
|
| I will add a bounded retry to the uploader transport. Three attempts,
| full jitter, and a cap of 5s.
|
|     func (u *Uploader) put(ctx context.Context, k string, b []byte) error {
|         return retry.Do(ctx, retry.Policy{Max: 3, Cap: 5 * time.Second},
|             func() error { return u.raw.Put(ctx, k, b) })
|     }
|
| > read_file  internal/storage/s3_uploader.go        ok    12ms   48 lines
| v edit       internal/storage/s3_uploader.go        ok    31ms   +4 -1
|   @@ -14,7 +14,11 @@
|   -   return u.raw.Put(ctx, k, b)
|   +   return retry.Do(ctx, retry.Policy{
|   +       Max: 3, Cap: 5 * time.Second,
|   +   }, func() error { return u.raw.Put(ctx, k, b) })
|
| v subagent test-writer                          running   18s   2 of 3
|   [######################........]  67%
|   + read existing table tests
|   + draft TestPutRetriesOnTransient
|   - run the package test
|
| > run_command  go test ./internal/storage/...  failed   4.1s   1 failure
|   x s3_uploader_test.go:88: want 3 attempts, got 1
|
| v plan                                                          2 of 4
|   [x] add retry policy to the transport
|   [x] wire the uploader through it
|   [ ] fix the fake transport to count attempts
|   [ ] run the package test again
|
| notice  context 62% (78k of 125k). /compact frees about 30k.

usage    1,284 in   2,940 out   340 cached   $0.041   21.4s

>
```

Keys: `Tab` and `Shift-Tab` move between collapsible blocks. `Space` or `Enter` toggles
the focused block. `Ctrl-E` expands every block in the turn, `Ctrl-W` collapses every
block. `y` copies the focused block. Everything from A.1 also applies.

### B.2 Streaming mid-token

The block header is drawn once. Only the body tail and the status row repaint.

```
opus-5                                                         14:22:08
| I will add a bounded retry to the uploader transport. Three attempts,
| full jitter, and a ca_

  - streaming   4.2s   1,284 in   esc to cancel
```

### B.3 Tool approval

The approval is a block in the same shape as the tool block, so the eye does not move.

```
| ? edit       internal/storage/s3_uploader.go                    +4 -1
|   scope: workspace   path is inside the project root
|
|   [o] once   [a] always   [d] deny   [D] deny always   [v] view diff
```

Keys: as A.3, plus `Tab` to move to the diff preview inside the block.

### B.4 Transient status line

Same single line as A.4. The block gutter does not extend into it, because the status
line is not part of the transcript and is erased at turn end.

```
  - running  go test ./internal/storage/...   12s   62% ctx   esc to cancel
```

### B.5 Composer and slash completion

```
>  /comp_
   /compact     summarise the transcript and free context
   /completion  print the shell completion script
```

Keys: as A.5.

### B.6 Unified diff

```
| v edit  internal/storage/s3_uploader.go                ok  31ms  +4 -1
|   @@ -14,7 +14,11 @@ func (u *Uploader) put(ctx, k, b) error {
|    14      ctx, cancel := context.WithTimeout(ctx, u.timeout)
|    15      defer cancel()
|    17  -   return u.raw.Put(ctx, k, b)
|        +   return retry.Do(ctx, retry.Policy{
|        +       Max: 3,
|        +       Cap: 5 * time.Second,
|        +   }, func() error { return u.raw.Put(ctx, k, b) })
|    18  }
```

The gutter costs two columns of the code area. At 80 columns that is the difference
between a readable line and a wrapped one. This is the main cost of variant B.

### B.7 Alternate screen - theme picker

Same as A.7. The picker is shared by all three variants because it is a modal, and the
modal surface is not what distinguishes the variants.

---

## 5. Variant C - "Console" (modal workspace)

The inline transcript is deliberately thin. Long content does not print inline: it
prints a one-line reference and opens on demand in an alternate-screen pager. A command
palette on `Ctrl-K` replaces most slash commands.

### C.1 Transcript, all block types

```
> Add retry with exponential backoff to the S3 uploader, and cover it
  with a test.

I will add a bounded retry to the uploader transport. Three attempts, full
jitter, and a cap of 5s.

    func (u *Uploader) put(ctx context.Context, k string, b []byte) error {
        return retry.Do(ctx, retry.Policy{Max: 3, Cap: 5 * time.Second},
            func() error { return u.raw.Put(ctx, k, b) })
    }

  reasoning   84 words                                        ctrl-k r
  read_file   internal/storage/s3_uploader.go       ok   12ms  48 lines
  edit        internal/storage/s3_uploader.go       ok   31ms  +4 -1  d1
  subagent    test-writer                      running   18s   2 of 3
  run_command go test ./internal/storage/...    failed   4.1s  1 failure
  plan        4 items                                    2 done  ctrl-k p
  notice      context 62% (78k of 125k)

  1,284 in  2,940 out  340 cached      $0.041      claude-opus-5      21.4s

>
```

Every row after the prose is a reference, not content. `d1` is a diff handle. Keys:
`d1` typed into the composer opens diff 1 full-screen. `Ctrl-K` opens the palette.
`Ctrl-O` opens the last reference. Everything else matches A.1.

### C.2 Streaming mid-token

```
I will add a bounded retry to the uploader transport. Three attempts, ful_

  - opus-5  4.2s  62% ctx  esc cancel  ctrl-k palette
```

### C.3 Tool approval

The approval is the one moment variant C makes larger, not smaller, because it is the
only irreversible decision in the loop.

```
  ? edit  internal/storage/s3_uploader.go   +4 -1   scope workspace

    o  once            run this call only
    a  always          auto-approve edit inside this workspace
    d  deny            refuse and tell the model why
    D  deny always     refuse every edit for this session
    v  view diff       open the diff full-screen first

    [o]
```

Keys: `o` `a` `d` `D` `v`, `Enter` accepts the shown default, `Esc` denies.

### C.4 Transient status line

Variant C's status line carries the palette hint, because the palette is the primary
command surface.

```
  - running  go test   12s   62% ctx   esc cancel   ctrl-k palette
```

### C.5 Composer and command palette

The composer still accepts slash commands. The palette is the richer surface and it is
an alternate-screen modal.

```
> /comp_
  /compact     summarise the transcript and free context
  /completion  print the shell completion script
```

Palette, on `Ctrl-K` (alternate screen):

```
  > comp_

    compact          summarise the transcript and free context      /compact
    completion       print the shell completion script
    model            switch model and effort                        ctrl-m
    theme            switch theme                                   ctrl-t

  up/down select   enter run   esc close
```

### C.6 Unified diff

Inline, the diff is one row: `edit  ...  +4 -1  d1`. Full-screen, on `d1` or `Ctrl-O`:

```
  internal/storage/s3_uploader.go                         +4 -1   1 hunk

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

  n next hunk   N prev hunk   s side-by-side   q close
```

### C.7 Alternate screen - theme picker

Same as A.7, reached with `Ctrl-T` or through the palette.

---

## 6. What changes at 120 columns

The design does not add a panel at 120 columns. The inline constraint removes that
option. It spends the extra 40 columns like this:

| Element | 80 columns | 120 columns |
|---|---|---|
| prose wrap | 76 columns | 92 columns, not 116; long measure hurts reading |
| tool result row | name, path, status, time | adds the byte or line count and the tool scope |
| diff | unified only | side-by-side becomes available (`s` in the pager) |
| status line | drops fields from the right | shows every field, plus the model name |
| slash completion | name and short text | name, short text, and the source (builtin or skill) |
| subagent progress | 30-cell bar | 40-cell bar plus the current step text |
| variant B gutter | 2 columns | unchanged; the gain goes to the code area |

Below 80 columns the design degrades in this order: the status line drops fields, the
tool result rows drop the timing column, then the path is elided from the left
(`.../storage/s3_uploader.go`). Prose never falls below a 40-column measure. Under 40
columns the renderer prints the plain stream format instead of the inline UI.

---

## 7. Theme role reference

The mock is the first consumer of this list. The real theme package ships the same
names.

Base: `bg`, `bg-subtle`, `bg-inset`, `fg`, `fg-muted`, `fg-subtle`, `border`,
`border-focus`, `accent`, `accent-fg`, `success`, `warning`, `danger`, `info`.

Syntax: `keyword`, `string`, `number`, `comment`, `function`, `type`, `variable`.

Diff: `diff-add-fg`, `diff-add-bg`, `diff-del-fg`, `diff-del-bg`, `diff-hunk`.

Roles the mock needed and the supplied list does not contain. Report them as findings:

| Proposed role | Why the mock needs it |
|---|---|
| `bg-selection` | the picker and the completion list need a selected row that is not `accent` |
| `diff-add-emph-bg`, `diff-del-emph-bg` | word-level diff, which delta shows is the main readability win |
| `gutter` | variant B's gutter must be dimmer than `border` and is not decorative |
| `link` | file paths and URLs are actionable and must differ from `info` |
| `fg-inverse` | text on `success`, `warning` and `danger` fills; `accent-fg` covers only `accent` |

`border` is a special case. In the shipped design no state is carried by `border`
alone, so `border` is decorative and is exempt from WCAG 1.4.11. `border-focus` does
carry state and must meet 3:1. See `research.md` for the measured numbers.
