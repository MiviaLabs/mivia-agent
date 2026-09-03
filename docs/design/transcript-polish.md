# Transcript polish — findings and recommendations

Status: proposal, revised after reviewer pass (HEAD 4cba0699).
Research date: 2026-08-28.
Scope: the main chat history in the cockpit screen
(`internal/ui/component/transcript`, `internal/ui/render`,
`internal/ui/screen/conversation`). Owners: ui-design-phase0.

## 1. Problem

The transcript reads cluttered and unpolished. It shows every block as a
top-level entry with its own header, its own `v` marker, its own body
rail, and its own blank row. The fixture turn in the golden dump renders
9 marker headers plus user and assistant prose across 48 terminal rows.
Some facts repeat. The eye finds no hierarchy, and nothing tells it where
one activity ends and the next starts.

## 2. Evidence from the current rendering

The golden dump (`internal/ui/component/transcript/testdata/golden/
transcript-dump.txt`, ANSI stripped, excerpt — a few body rows elided):

```
> Add retry with exponential backoff to the S3 uploader, and cover it
  with a test.

v reasoning  84 words  hidden
│   Considering retry policy shape, jitter strategy, and where the cap belongs b
│   efore touching the transport.

I will add a bounded retry to the uploader transport. Three attempts, full
jitter, and a cap of 5s.
  func (u *Uploader) put(ctx context.Context, k string, b []byte) error {
      return retry.Do(ctx, retry.Policy{Max: 3, Cap: 5 * time.Second},
          func() error { return u.raw.Put(ctx, k, b) })
  }

v read_file internal/storage/s3_uploader.go  12ms  ok
│   package storage
│   ...
v edit internal/storage/s3_uploader.go  +4 -1  31ms  ok
│   @@ -14,7 +14,11 @@ func (u *Uploader) put(
│     14 -return u.raw.Put(ctx, k, b)    │  14 +return retry.Do(ctx, ...
v subagent agent=test-writer  23500ms  ok
│   3 of 3 steps complete

v run_command $ go test ./internal/storage/...  4100ms  failed
│   --- FAIL: TestPutRetriesOnTransient
│   s3_uploader_test.go:88: want 3 attempts, got 1

v plan  2 of 4
│   [x] add retry policy to the transport
│   ...
v error transport refused after 3 attempts  fatal
│   dial tcp 10.0.0.4:443: i/o timeout

v notice context 62% (78k of 125k). /compact frees about 30k.

v usage 1284 in  2940 out  340 cached  $0.041
```

Confirmed defects and clutter sources:

| # | Finding | Evidence |
|---|---------|----------|
| D1 | Flat block stream. Every tool call, plan, notice, and status fact is a top-level block. Nothing groups blocks under the turn that produced them. (Tool lifecycle is already merged: pending→start→end updates one block by `CallID`, `transcript.go:317-336,370-395`.) | `transcript.go:113-175`. |
| D2 | A `v` collapse marker prints on blocks with no body at all (`usage`, the one-line `notice`), and `push()` forces `Collapsible` on every non-prose block — including the `error` header, whose body is always visible and which `wireframes-panes.md` §4 draws with a blank marker column. | `viewport.go:103-111`; golden lines 43-48. |
| D3 | One blank separator row inside every block's own `Height`/`Render`, so a burst of small blocks burns rows on air and separators are not turn-aware. | `block.go:92-104,155-182`. |
| D4 | The `│ ` body rail prints on every expanded block at every tier — and the golden shows it side-by-side on 80 columns. This contradicts the governing wireframe: "the body is indented 4 columns. Nothing is drawn in columns 1 to 4 of a body line" (`docs/design/wireframes-panes.md` §2). The code is the drift, not the spec. | `block.go:174`; `wireframes-panes.md` §2, §4, §11. |
| D5 | Duration grammar is inconsistent inside one screen: raw `12ms`, `31ms`, `4100ms`, `23500ms` from one `%dms` format, while `wireframes-panes.md` §4 specifies a `4.1s`/`18s` ladder. No shared formatter exists. | `values.go:180,191`; golden lines 17, 23, 30, 33. |
| D6 | Usage facts overlap surfaces. The transcript prints a `usage` block with in/out/cached/cost (`transcript.go:153-161`). The topbar shows a context-fill gauge derived from input tokens only (`screen/conversation/events.go:164-177`). The statusline shows a cost pill (`component/statusline/statusline.go:189-190`). Per-turn output/cached tokens live only in the transcript block. | golden line 48. |
| D7 | A collapsed block states only a word count, never a line count and never the real expand affordance. Today the affordance is `space`/`enter` on the focused block (`uikit/keymap/keymap.go:221`) or `ctrl+e` expand-all (:222). | `transcript.go:237-241`; `block.go:165-167`. |
| D8 | For a tool the formatter does not know, the first output line can print twice: once as header detail, once as body row one. Reachable on the direct-push end path when no live start block exists. | `values.go:165-169`; `render/output_formatter.go:76-78`; `transcript.go:395`. |
| D9 | The streaming tail renders as plain text; the committed block re-renders as styled markdown, so long replies visibly pop and reflow at the flush tick. | `transcript.go:213-252`, `tailRows` at `transcript.go:431`. |
| D10 | Raw token counts (`1284 in 2940 out`) with no compact formatter. | `transcript.go:157-159`. |
| D11 | Line-number gutters use two widths in one formatter (`%4d │`, `%3d │`), and the side-by-side diff auto-splits from 60 columns (`render/diff_split.go:14-17`), while `wireframes-panes.md` §11/§14 specify unified at 80 and side-by-side only at 120+ via the `s` key. | `output_formatter.go:439-454`. |
| D12 | Docs and comments drift from code: the transcript package doc still describes the retired live-window/ring/scrollback design (`transcript.go:3-24` vs `viewport.go:12-19`); `focus.go:9-16` still scopes itself to the "LIVE WINDOW"; `userLines` claims a full-width selection background that no code applies (`values.go:29-42`). |
| D13 | Dead weight: `shortResultCols` unused (`transcript.go:200`); `FormatToolOutput` has no callers (`output_formatter.go:20-22`); `FormatGrepOutput`/`FormatFileReadOutput` survive only via their own tests; `MaxToolOutputBytes` is asserted only by a tautological config test (`uikit/config/defaults_test.go:21-22`). `output_formatter.go` is 788 LOC. |

Note: an earlier draft flagged a duplicate subagent duration
(`18s 23500ms`). Commit `e02a8060` already fixed that by moving subagent
progress to the sidebar panel.

## 3. What the reference products do

Researched from shipped source: Claude Code (npm bundle), Codex CLI
(`codex-rs/tui`, checked-in render snapshots), Crush
(`internal/ui/chat`, `internal/ui/styles`), OpenCode classic TUI, aider.
(The external constants below, e.g. Codex `TOOL_CALL_MAX_LINES = 5`, come
from that source pass and were not re-verified offline since.)

The convergent rules:

1. **One leader line per logical activity; everything else hangs under
   it.** Codex prints `• ` for an agent message and indents continuations
   two columns. Tool output sits under `⎿ ` (Claude Code) or `└ ` (Codex).
   No product repeats a full header per event.
2. **Repeated activity coalesces into a count.** Codex folds sequential
   reads into `Read a.rs, b.rs` and a run into `• Ran 3 commands ·
   ctrl + t to view transcript`. Claude Code folds repeats into
   `Called slack 3 times`. The reader sees magnitude, not 20 headers.
3. **Collapse to a count plus one expand affordance.** Caps are 5–10
   lines. The hint carries the number and the true key. Nobody paints a
   `v` on rows with nothing under them.
4. **Dim is the default voice; saturated color marks state only.** Codex
   keeps a style guide (`styles.md`): ANSI-16 colors, headers bold,
   secondary text dim. Crush recolors one bullet glyph for state instead
   of adding lines.
5. **Borders cost columns and weight; whitespace and hanging indent do
   the grouping.** Crush reserves a `│`/`▌` rail for message identity and
   focus; the resting transcript is plain padding. OpenCode uses a left
   border only.
6. **Metadata appears once, in one grammar.** One duration ladder
   (`250ms → 3.45s → 1m 05s`, Codex `utils/elapsed`), one token formatter
   (`999 / 1.2k / 12k`, aider). Codex joins compact metadata with ` · `.
7. **Transient state lives in the footer chrome; history in the
   transcript.** Context %, model, elapsed, and key hints never print
   into the record. aider prints one quiet usage footer per turn.
8. **80-column diffs are a rail plus a number gutter, not a side-by-side
   box.** Codex snapshot `diff_gallery_80x24`: `• Edited 6 files (+9 -9)`,
   then `└ path (+3 -0)` per file, then numbered single-column lines.
9. **Streaming must not reflow committed text.** Codex prints finished
   lines once and keeps only a tail cell live; unstable constructs
   (tables, open fences) stay buffered until they close.

## 4. Recommendations

Keep the house style that works: the four-column header
(marker, label, detail, meta/state), state as a word so meaning survives
color removal, tier degradation with an ASCII glyph set
(`wireframes-panes.md` §3), and theme-rebuildable block values. Change
the presentation grammar.

### P0 — grouping and density

R1. **Group by turn.** A turn renders as: user line, assistant prose,
then one activity group holding the tool calls that turn produced. Group
members render at a 2-column indent, without per-block blank rows. A
blank row separates turns, not blocks. Implementation is a restructure,
not a tweak: the blank row lives inside `Block.Height`/`Render`
(`block.go:92-104`), so turn-aware separators must move layout into
`viewport.Rows` and re-derive the eviction budget, `trim`'s dropped count
(`viewport.go:129-148`), and `missed` accounting (`viewport.go:103-116`).
Keep per-block IDs so the focus walk enters the group.

R2. **Coalesce repeated read-only tool calls, display-only.** Consecutive
`read_file`/`search`/`list` blocks within one turn render as one leader
line with a count and the file list (`Read a.rs, b.rs`; drop to
`Read 4 files` when the list does not fit). Hard constraint: coalescing
must change only rendering. Children stay real blocks in `Model.blocks`,
so focus, click-to-expand (`ExpandBlockAtScreenRow`,
`viewport.go:199-227`), the `FocusedText` copy contract
(`focus.go:184-201`), and `Dump()` keep per-child identity and full
content. State-changing tools (edits, commands) never coalesce.

R3. **Show the marker only where a body exists, and say what expanding
costs.** Stop forcing `Collapsible` in `push()` (`viewport.go:103-111`).
Header-only blocks keep a blank marker column — `wireframes-panes.md` §4
already draws `error`, `notice`, `usage` that way. A collapsed body gets
its magnitude in the meta: `… +38 lines`. The hint must name the true
affordance: focus with `tab`, toggle with `space`/`enter`
(`keymap.go:219-222`). Do not invent a key: `ctrl+r` is the global
reasoning toggle (`keymap.go:181`) and a reserved readline key
(`ux-rules.md` §1); ux-rules 1.4 requires hints to state the complete
truth. If a global one-key expand is wanted, first run a reserved-key
analysis against ux-rules §1. (Affordance bindings:
`keymap.go:217-222`.)

R4. **Restore the spec's resting state: no rail.** Indent bodies with
plain spaces at the same 4-column position (`wireframes-panes.md` §2).
Reserve the `│` rail for two moments only: the focused block, and the
failed/error block, where rail plus `RoleDanger` earns its weight. This
is the D4 drift correction; it supersedes the §3-tier justification in
`block.go:168-173`'s comment, which must be rewritten.

R2a. **Coalesce a wall of finished work into one row.** *(Implemented,
amends R2.)* R2 folds consecutive same-class read-only lookups. That
leaves the common shape untouched: a long turn whose activity is a
dozen mixed calls — reads, edits, commands, subagents — each drawing its
own header. Three or more CONSECUTIVE FINISHED calls now draw as one
row instead:

```
  > work read_file, edit, run_command +1 more  5 calls  4.2s
```

Rules, all of them load-bearing:

- **Finished only.** A call that has not ended keeps its own row. Work
  still running is the one thing the reader is waiting on.
- **Failures never fold.** A `RoleDanger` block keeps its header, its
  body and its place. A summary row that could swallow a failure would
  hide the one block worth the rows.
- **Three is the floor.** Two headers are not a wall; folding them costs
  two tool names to save one row.
- **The read row wins a tie.** It names its targets, so it says strictly
  more about the same blocks than the generic row does.
- **Display-only, exactly as R2 requires.** Children stay real blocks:
  focus, click-to-toggle, `FocusedText` copy and `Dump()` all keep
  per-child identity and full content.
- **The duration is the SUM of the members' own durations**, which is
  what the blocks carry (`Block.ElapsedMS`). Calls the loop issued in
  parallel therefore add to more than the wall clock; no block records
  when the run started, so sum is the only honest number available.

The fold is driven by each member's own collapsed state, not by a
separate per-run flag, so collapsing the members again re-forms the run
with no extra state to keep, migrate, or leak. That is also why the
default changed: a call that ends successfully now collapses whatever
its body size, the way R2's read-only lookups already did.

### P0 — deduplication and grammar

R5. **One duration formatter everywhere.** Add `render.FormatElapsed(ms)`:
`<1s → 250ms`, `<60s → 4.1s`, `≥60s → 1m 05s` — and amend
`wireframes-panes.md` §4's own mixed ladder to it. Use it in tool meta,
subagent end, and statusline.

R6. **Usage: one per-turn footer, not a header block.** Replace the
`usage` block with one dim, header-less footer line per turn, in the
established meta grammar: `1,284 in  2,940 out  340 cached  $0.04`
(§4's existing comma style; a `1.3k` compact option needs a shared
formatter — pick one and update §4). The footer must stay a `Block` in
the model: alt-screen has no native scrollback, and `[` writes
`Model.Dump()` to the primary screen, so removing the fact from the
model removes it from grep/tmux copy. The topbar gauge and statusline
pill keep their live roles; the transcript keeps the historical record.

R7. **Fix the unknown-tool duplicate.** When the formatter yields no
detail, keep the tool name in the header and leave `lines[0]` only in the
body (`values.go:165-169`).

R8. **Quiet by default, loud for state.** Body text and metadata use
`RoleFGSubtle`; saturated `RoleSuccess/Warning/Danger/Info` stay on the
state word and the marker. Failed blocks prefix a danger-colored `×`
glyph (ASCII tier: `x`) so a scan finds them without color. Notice lines
render as dim prose — but stay a focusable block, because focus and `y`
copy need the identity.

### P1 — streaming and diffs

R9. **Kill the streaming pop, within the repaint budget.** Render the
pending tail through the same markdown path as the committed block,
throttled to the 10-20 Hz ceiling of `ux-rules.md` rule 2.5 — the
current 40 ms `FlushMsg` tick (`uikit/config/defaults.go:9-10`, 25 Hz)
already sits above that line; fix both together. Commit rule: an open
code fence or incomplete table stays in the tail buffer and commits
atomically on close or on an idle tick (Codex tail-cell pattern). The
tail keeps its fixed indent so commit changes styles, not layout.

R10. **Follow the spec for narrow diffs.** Unified form below 120
columns: per-file header `path (+a −d)`, number gutter, `+`/`−`
sigils, foreground-only color. Side-by-side only at ≥120 via the `s`
key specified in `wireframes-panes.md` §11/§14/§15 — it is not yet
bound in `keymap.go`. Code change: raise `MinSplitDiffWidth`
(`render/diff_split.go:14-17`, now 60) and gate it behind a new `s`
binding. Standardize the gutter to one width rule (D11).

### P1 — header grammar (explicit, because it touches copy)

R11. **Compact metadata joins use ` · `, if we adopt it.** Current
headers join columns with the fixed 2-space gap (`render/header.go:38`
`minHeaderGap`), and `headerPlain` — the clipboard shape behind
`FocusedText` — uses the same joins (`block.go:247-279`). Adopting a
` · ` separator (ASCII tier fallback: ` | ` or two spaces) is a real
change: update both renderers together, the header one-row/width and
`~`-clip contracts, and every golden. Adopt it only with the goldens
regenerated in the same commit; otherwise keep the 2-space grammar and
drop the separator from all mocks.

### P2 — hygiene

R12. Fix the doc drifts in D12 (package doc, `focus.go:9-16`, the
`userLines` background claim — decide whether to implement the fill or
correct the comment).

R13. Delete the dead code in D13, including the tautological tests that
keep the unused wrappers alive. Split `output_formatter.go` (788 LOC) by
tool family at the next touch.

## 5. Target shape (mock)

One turn, 80 columns, after R1-R10 (R11 not assumed; header keeps the
current 2-space grammar). ANSI stripped. Markers keep column 1 at the
top level and sit at the group indent inside a group: `>` = collapsed
with content, `v` = expanded, blank = nothing to open. Glyph note: `×`
is the color tier's fail mark, `x` in the ASCII tier.

```
> Add retry with backoff to the S3 uploader, and cover it with a test.

I will add a bounded retry to the uploader transport. Three attempts,
full jitter, and a cap of 5s.

  > reasoning  84 words  2 lines
  v read_file internal/storage/s3_uploader.go  12ms  ok
      package storage
      import ("context"; "time")
      ...
  v edit internal/storage/s3_uploader.go  +4 -1  31ms  ok
      @@ -14,7 +14,11 @@ func (u *Uploader) put(
        14 -return u.raw.Put(ctx, k, b)
        14 +return retry.Do(ctx, retry.Policy{Max: 3, ...
× run_command $ go test ./internal/storage/...  4.1s  failed
  │ --- FAIL: TestPutRetriesOnTransient
  │ s3_uploader_test.go:88: want 3 attempts, got 1
  │ … +38 lines
  > plan  2 of 4

  1,284 in  2,940 out  340 cached  $0.04
```

Compared with the golden dump: one blank row per turn instead of per
block, no marker on empty bodies, one duration ladder (R5), unified diff
lines at plain indent with a gutter instead of the 80-column
side-by-side split (R10), the usage footer as dim prose in R6's grammar
instead of a header block, and the failed block as the only railed line
(R4/R8). R2 coalescing does not show here: this fixture has one read,
not a run of reads. The subagent block would sit in the same group; its
duration follows R5 (`23.5s`).

## 6. Sequencing and risk

1. **R3, R5, R7** are small and fix outright defects (phantom markers,
   raw milliseconds, duplicated detail). Each touches both goldens
   (`cockpit-80x20.txt`, `transcript-dump.txt`; regenerated via the
   golden tests in `transcript_test.go`) and R5 additionally touches the
   header one-row/`~`-clip contracts in `render/header.go`.
2. **R6, R8, R12, R13** are presentation-policy changes. Affected spec
   rows must be enumerated and updated in the same change:
   `wireframes-panes.md` §2 (rail/indent), §3 (glyph table, for the `×`
   addition), §4 (usage footer, duration ladder, notice), §5 (default
   collapse), §11/§14 (diff split threshold), and the `ux-rules.md` §10
   overturns table. R13's deletions include their tests
   (`output_formatter_test.go`, `defaults_test.go`).
3. **R1, R2, R9, R10** are structural. Affected surfaces: eviction and
   `trim`/`missed` accounting, focus walk and `FocusedText`/`y` copy,
   `ExpandBlockAtScreenRow` and mouse routing, the ctrl+o pager screen
   (`internal/ui/screen/transcript`), the `[` scrollback dump, and the
   repaint budget (rule 2.5). R2 must be display-only (see its clause)
   or copy and audit regress silently.
4. **Invariants that must survive every step:** state meaning without
   color, ASCII-tier degradation for every glyph, theme rebuild from raw
   payloads, the View-height bound, header one-row geometry and `~`
   clipping, and truthful key hints (ux-rules 1.4).

Residual risks:

- Turn grouping may make single calls harder to reach. Mitigation: per-
  block IDs remain, and the focus walk enters groups (R1/R2).
- R6's footer keeps per-turn cost history but lengthens every turn by one
  row. If density still loses, move the footer behind the ctrl+o verbose
  view — do not delete the fact from the model, or `[`/Dump loses it.
- R4 removes the only column-1 body cue; if boundaries read ambiguously
  after the change, restore the rail for multi-line bodies only, as an
  amendment to `wireframes-panes.md` §2 rather than code drift.
