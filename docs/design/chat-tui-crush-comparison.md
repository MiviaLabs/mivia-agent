# Main chat TUI - Crush comparison and improvement plan

Status: proposal, revised after a hostile architecture review
(2026-09-10, result PARTIAL, 13 findings, all applied below).
Research date: 2026-09-10.
Reference: charmbracelet/crush at commit `bb33cee` (bubbletea v2,
lipgloss v2, glamour v2, ultraviolet) and the README demo GIF.
Scope: the conversation screen (`internal/ui/screen/conversation`),
the transcript (`internal/ui/component/transcript`, `internal/ui/render`),
the composer, the statusline, and the approval box. Owners:
ui-design-phase0.

This plan builds on [transcript-polish.md](transcript-polish.md). Items
R1-R8 of that proposal have landed (turn grouping, work-run folding, one
duration ladder, rail only on focus/failure). R9, R10, R11 are still open
and are folded into this plan where they overlap.

## 1. What the Crush demo shows

Frames from the GIF, in order:

1. **Idle session.** A user line in a left-bar box at the top. A row of
   animated dots below it while the model thinks. The sidebar shows the
   logo, session title, cwd, model with `Thinking On` and a live
   `23% (46.1K) $4.15` context/cost line, then `Modified Files`, `LSPs`,
   `MCPs` sections, each with a rule and a `None` placeholder. The
   editor is a plain `> ` prompt with `::` attachment marks under it.
   The help row reads `esc cancel · tab focus chat · ctrl+p commands ·
   shift+enter newline · ctrl+c quit · ctrl+g more`.
2. **Permission dialog.** A modal over the transcript: `Permission
   Required`, then `Tool multiedit`, `Path ~/src`, `File .../sidebar.go`,
   a syntax-highlighted split diff with line numbers and hunk headers,
   and three buttons `Allow`, `Allow for Session`, `Deny`. The help row
   reads `t toggle diff mode · shift+↑↓ scroll`.
3. **Finished turn.** Assistant prose, then a dim `Thought for 8s` line,
   then `✓ Bash task lint-fix` with a tinted output card (`0 issues.`
   plus the command), prose again, `Thought for 5s`, a numbered summary
   with inline code chips, and a rule line `◇ Claude Opus 4 1m42s`. The
   sidebar's `Modified Files` now lists `~/.../s/sidebar.go +6 -6`.
4. **Model picker.** A filterable list grouped by provider.

The things that make it read as clean: one status glyph per tool row,
almost no chrome on assistant prose, tool output in a tinted card, the
thinking block collapsed to a duration, a rule line that closes each
turn with the model and the wall time, and help hints that change with
focus.

## 2. Side-by-side

| Area | Crush | mivia today | Gap |
|------|-------|-------------|-----|
| Assistant prose | No marker, 2-column pad, width capped at 120 (`messages.go:24`) | Bare at column 1, measure 92 (`config/defaults.go:44`) | Small. Keep ours. |
| User line | `│` left bar in primary, `▌` when focused | `> ` bold accent marker, no bar | Small. Keep ours. |
| Thinking | Box on `bgLeastVisible`, footer `Thought for 8s`, collapsed to last 10 lines, 3-state expand (`assistant.go:544,748`) | Header `v reasoning  84 words  hidden`, last 3 lines, dim italic (`block.go:163`) | **Large.** We show a word count. The reader wants the time. |
| Tool header | `✓ Bash task lint-fix`: glyph, name, one-line params (`tools.go:610`) | `v run_command $ go test ...  4.1s  failed`: marker, label, detail, meta, state word | Medium. Our marker column carries collapse state, not outcome. |
| Tool body | Blank row, then output on a tinted per-line background, 10-line window, hint `… (N lines hidden) [click or space to expand]` (`tools.go:645`) | Plain 4-space indent, 12-line window, meta hint `… +N lines` (`block.go:266`) | Medium. Ours reads as prose, not as a card. |
| Running tool | Animated dots inline under the header; `Requesting permission...` / `Waiting for tool response...` in the body (`tools.go:527`) | Statusline mark only; the row shows `pending` or `running` | Medium. |
| Nested subagent calls | `├──`/`╰──` tree of compact child headers under the dispatch row (`agent.go:107`, `tools.go:1046`) | Sidebar counter plus a full embedded screen in a dialog (`thread.go`) | Medium. The transcript does not say what the child did. |
| Turn footer | `◇ Claude Opus 4 1m42s` on a rule line (`messages.go:383`) | `1,284 in  2,940 out  340 cached  $0.04` dim prose block (`values.go:346`) | Medium. Two facts missing: model, wall time. |
| Streaming | Stable-prefix glamour cache, delta-only render (`streaming_markdown.go`) | Tail is plain text, commits as markdown at text.end (R9 open); 40 ms flush = 25 Hz | **Large.** Visible pop. |
| Prose render hygiene | - | Committed prose carries dozens of empty SGR pairs per line (`transcript-dump.txt:9`, 55 on one line) | Small. Glamour output is not trimmed. |
| Render cost | Per-item memo keyed (item, width, version); finished items frozen (`list.go:63,292`) | `layout()` calls `Block.Height` per block per frame; no cache in `internal/ui` | Unknown until measured. No benchmark covers markdown or layout. |
| Diff | Unified, split only above 120, `t` toggles in the dialog | Split from 60 columns (`diff_split.go:17`); `s` from wireframes §11 still unbound | Medium (R10 open). |
| Help row | `bubbles/help`; hints rewritten per focus, ` · ` joins (`ui.go:3201`) | Hints from the keymap with a width ladder, `key:label` two-space joins (`status.go:132`); ` · ` divider exists in `statusline.go:251` | Small. |
| Composer | `  > ` focused, `::: ` blurred, attachment chips | `› ` in a filled bar; `Focus/Blur` setters, no `Focused()` getter (`composer.go:281`) | Small. Blurred state is not visible in ours. |
| Approval | Modal, key/value header, scrollable diff, three buttons, `a`/`s`/`d` | Inline bordered box with queue depth, diff window, `o`/`a`/`d`/`D` (`approval.go:226-318`) | Keep inline (ux-rules). Adopt the key/value header. |
| Sidebar files | `Modified Files` with `+6 -6` | Files panel already shows `+N -N` in `RoleDiffAddFG`/`RoleDiffDelFG` (`filespanel_layout.go:343-355`) | None. |

## 3. Recommendations

House rules that hold for every item: state as a word survives color
removal, the glyph set is the one in `wireframes-panes.md` §3 (Unicode
and ASCII sets are identical, by decision), blocks keep raw payloads so
a theme change rebuilds them, one spinner clock armed only through
`Screen.armTick`, hints state the true binding (ux-rules 1.4), and every
item that touches a file over 500 LOC names its split.

No item adds an event kind. C1, C2, C5, and C7 add transcript-side
clocks, setters, or port fields instead, following the `Model.Now`
precedent, so `.mivia/policy/viewer-surfaces.json` is unaffected.

### P0 - the reading experience

**C1. Thinking as a duration.** Replace the `reasoning  84 words` header
with a dim `Thought for 8s` line. Reasoning streams into `Model.pending`
with no start time and commits through `reasoningBlock`
(`transcript.go:330-345`) with no `StartedAt`. Add `pendingStartedAt
time.Time` to `transcript.Model`, stamped in `appendPending` when the
kind switches to reasoning, and copy it to `Block.StartedAt` in
`flushPending` and `reasoningBlock`. The whole-text path
(`transcript.go:321-327`, `WordCount != 0`) stamps `Now()` directly.
Default collapsed: the line alone. Expanded: the last 10 rendered lines
on `RoleBGInset`, then the full text on a second toggle. The third state
is a new `Block` field (`Expanded`), because `Collapsed` stays the field
`ToggleReasoning` (`focus.go:238`) drives for the global `ctrl+r` hide.
While reasoning streams, show `Thinking` plus the elapsed time,
refreshed on the existing tick. Spec: amend `wireframes-panes.md` §4.

**C2. Turn footer with model and wall time.** Merge the usage line with a
rule line: `+ claude-opus-5  1m 42s  1.3k in  2.9k out  $0.04` padded
with `-` to the width (Crush `common.Section`; the `-` rule is the §3
set, no new glyph). `UsageBody.ElapsedSeconds` is never set by
`uiadapter.translateTokenUsage` (`uiadapter/event.go:161-171`), so wall
time is measured transcript-side from the `TurnStartBody` push
(`transcript.go:174`) to the usage push via `Model.Now`, the same
pattern as tool `StartedAt`. The transcript has no session access: the
screen injects the model name through a new `transcript.Model.SetModel`
at `SetSession` time (`commands.go:102`). The rule row is not prose. It
gets its own branch in `Height` and `bodyRows`, and the pad is computed
at `Render` time from `width`, or it word-wraps at `ProseMeasure`
(`block.go:157-170`). Keep it a `Block` so `Dump()` and `[` keep the
fact (R6 rationale).

**C3. Outcome glyph in the marker column.** Today column 1 of a tool row
says collapse state (`v`/`>`), and the outcome sits at the far right as
a word. Move the outcome to the front with the §3 glyphs: `+` ok, `x`
failed, `?` pending, and the §3 spinner frames `-|/-` while running.
Keep the state word for `failed`, `pending`, and `running`; drop the
trailing `ok`, because `+` is in every tier. Collapse state moves to the
body hint (C4). Four consumers read column 1 today and change together:
`hitsCollapseMarker` (`viewport.go:293,309`) moves its hit target to the
body hint row; `FocusedText` (`focus.go:226`) stops stripping the marker,
because outcome is record, not view state; `headerPlain` (`block.go:322`)
copies the glyph; `header.go:88` keeps its one-cell lead width. Add a
`ToggleBlockAtScreenRow` test for the new hit target. Spec: amend
`wireframes-panes.md` §4 wording; §3 is unchanged.

**C4. Tool output as a card.** One blank row between header and body,
body lines on `RoleBGInset` at the body indent (Crush `ContentLine`),
window of 10 lines, and a trailing hint that names the affordance:
`… 38 more lines  space to expand` on the focused block, `… 38 more
lines` otherwise. Remove `headerMeta`'s `… +N lines` hint
(`block.go:266-276`) in the same commit or two hints show. Commands keep
the `$ cmd` detail in the header. Rails stay for failure and focus (R4).
Precondition: contrast rows `{RoleFGSubtle, RoleBGInset}` and
`{RoleFG, RoleBGInset}` in `theme/contrast.go`, which has no
`RoleBGInset` row today.

**C5. Live rows say what they wait for.** A tool row whose
`Header.State == "pending"` (`transcript.go:361-370`) renders `waiting
to run`; after `ToolStart` (`State == "running"`) it renders `waiting
for result`, both in `RoleFGSubtle` with the §3 spinner driven by the
one existing tick. This is rendering only. "Requesting approval" is not
used: a policy-auto-approved pending call never enters the approval
queue (`events.go:207`), so the phrase would be false for it.

**C6. Streaming through the markdown path (R9).** Render the pending
tail with glamour using a stable-prefix cache: keep the rendered output
of the longest prefix that ends at a safe boundary (blank line, balanced
fences, no open list or table), render only the delta, and glue. Port the
boundary predicate and the relaxed-boundary bound from Crush
(`streaming_markdown.go:275,306`), with O(delta) fence and list counters.
`render.Markdown` builds a fresh glamour renderer per call
(`markdown.go:62`), so the cache has no shared-state hazard. Lower
`TextDeltaFlushInterval` to 66 ms (15 Hz) to sit inside ux-rules 2.5.
Gates: a new `BenchmarkMarkdown` (`render/bench_test.go` covers only
Header and Wrap) and a new fuzz target
`FuzzStreamedMarkdownMatchesOneShot` proving byte identity with the
one-shot render. The pending/flush/tail code moves to
`transcript_stream.go` first, because `transcript.go` is at 607 LOC.

**C6a. Trim empty SGR pairs.** The committed prose line in the golden
carries 55 empty `ESC[38;2;…m ESC[m` pairs (`transcript-dump.txt:9`).
Post-process glamour output in `render.Markdown` to drop empty styled
spans. This is hygiene, but it shrinks every prose frame and makes
goldens readable.

### P1 - structure and chrome

**C7. Compact child tree under a dispatch row.** When a `dispatch_tasks`
block has child tool calls, render up to 6 children as compact rows
(`  + read_file a.go`, `  x edit b.go`) with `+N more` and the existing
`open` hint to the thread dialog. Data path: root-transcript progress
events carry counts and log text only (`uiadapter/event_kind.go:156-165`),
so the source is `ports.SubagentThreads.Thread(callID).History()[]
.ToolCalls`, which the screen owns (`conversation.go:61,727`). The screen
injects a per-CallID children summary through a new
`transcript.Model.SetChildren(callID, []ChildCall)`. `ports.ToolCall`
gains an `OK bool` (producer: `uiadapter/subagent.go` history) so a child
can show `x`. Child rows are body rows of the parent block, so `Height`
counts them. A block with children is exempt from work-run folding
(`workRunLen`, `layout.go:188`) or the tree vanishes inside the fold.

**C8. Diff policy (R10).** Raise `MinSplitDiffWidth` to 120, bind `s` to
toggle split on the focused diff block, and bind `t` to the same toggle
inside the approval box. `s` is unbound; `t` is bound only in
`ContextSettings` (`keymap.go:333`); neither is ux-rules §1 reserved.
Diff blocks and the approval preview (`approval.go:329-392`) are exempt
from the prose measure so the diff gets the full width.

**C9. Help row polish.** Join hints with the existing ` · ` divider
(`statusline.go:251`, ASCII ` - `), dim the key and mute the label, and
rewrite the `tab` and `esc` hints per focus and per state (`esc cancel`
while a turn runs, `esc clear queue` when the queue is non-empty,
nothing when idle). The keymap stays the single source (`status.go`).
R11's rule applies: regenerate goldens in the same commit.

**C10. Composer focus state.** Add a `Focused()` getter. Blurred: marker
`:` in `RoleFGSubtle`, no fill. Focused: `› ` in accent with the fill
bar. Accepted `@` mentions (`AcceptMention`, `composer.go:235`) record a
chip list rendered under the prompt, so the reader sees what the next
turn carries. The chip list is new composer state, cleared on submit.

**C11. Approval box header.** Render `Tool`, `Path`/`File`/`Command` as
aligned key/value rows above the preview instead of folding the action
into the border label (`approval.go:292-318`). `View` and `Height` must
agree (`approval.go:305`). Keep the inline box, the queue depth, and the
`o`/`a`/`d`/`D` keys.

### P2 - conditional on measurement

**C13. Per-block render memo.** Only if `BenchmarkLayout` at 2k blocks
exceeds the frame budget after C6. A `*renderCache` pointer on `Model`
(value copies share it) keyed by `(ID, width, tier, version)`;
invalidated by ID in `updateLive` (`transcript.go:374,400,427`),
`restyle`, `ToggleReasoning`, collapse and focus toggles; replaced in
`Clear()`. Test: a copied `Model` shares no stale entry after `Clear()`.

**C14. Per-line style prefix.** Pairs with C13: render bodies as lines
and prepend the style prefix per line instead of `Style.Render` over a
block, so the memo stores plain lines.

**C15. Staged resize.** Recorded trigger only: if `BenchmarkLayout`
shows `reflow()` (`conversation.go:514-519`) above 16 ms at 2k blocks,
add a 120 ms settle and batch warm-up (Crush `chat.go:53-72`).

## 4. Not adopted, and why

- The logo banner and the sidebar-with-logo. Our topbar already carries
  brand, session, model, and context on one row; a 32-column sidebar
  costs width that the transcript uses.
- A modal permission dialog. ux-rules chose inline; the queue and the
  per-call `Resolve` contract depend on it.
- Crush's Unicode glyph set (`✓ × ● ◇ ├── ╰──`). `wireframes-panes.md`
  §3 decided that the Unicode and ASCII sets are one set so the ASCII
  tier differs only in color. Overturning that is a design decision
  with its own review, not a table edit. If it is ever taken, a
  `render.Glyph(tier, role)` table with a `degrade_test.go` golden lands
  first.
- Files-panel counts (was C12). Already shipped at
  `filespanel_layout.go:343-355`.
- Crush's ANSI-16 remap of raw tool output. Useful, but it needs a
  theme-role table for 16 colors; defer until a real report shows
  unreadable command output.

## 5. Target shape (mock)

One turn at 80 columns after C1-C6, ANSI stripped:

```
> Add retry with backoff to the S3 uploader, and cover it with a test.

  Thought for 4s

I will add a bounded retry to the uploader transport. Three attempts,
full jitter, and a cap of 5s.

  + read_file internal/storage/s3_uploader.go  12ms
  + edit internal/storage/s3_uploader.go  +4 -1  31ms
  / run_command $ go test ./internal/storage/...  waiting for result
  x run_command $ go test ./internal/storage/...  4.1s  failed
  │
  │   --- FAIL: TestPutRetriesOnTransient
  │   s3_uploader_test.go:88: want 3 attempts, got 1
  │   … 38 more lines  space to expand
  + dispatch_tasks test-writer  23.5s
      + read_file internal/storage/s3_uploader_test.go
      + edit internal/storage/s3_uploader_test.go  +12 -0

+ claude-opus-5  1m 42s  1.3k in  2.9k out  $0.04 ----------------------
```

The tinted card background on the failed body and the spinner on the
running row do not show in a stripped mock.

## 6. Sequencing and verification

Order, with the reason for each position:

1. **Foundations** (contrast rows, `transcript.go` split, empty-span
   trim, `BenchmarkMarkdown`/`BenchmarkLayout`). Nothing user-visible;
   unblocks every later gate.
2. **C3 + C4** together. Both rewrite the tool row; one golden change.
3. **C1**, then **C2**. Each adds one transcript field and one block
   shape.
4. **C5**. Small; depends on C3's glyph column.
5. **C6 + C6a**. The largest change; benchmarked, fuzzed.
6. **C8**, **C9**, **C10**, **C11**. Independent chrome items.
7. **C7**. Needs the port change and the fold exemption.
8. **C13/C14** only if the benchmark from step 1 says so.

Every step: regenerate both goldens (`cockpit-80x20.txt`,
`transcript-dump.txt`) and keep the property tests in
`transcript_test.go:63` green. UI-ship steps (2, 3, 4, 5, 7) add or
extend the offline smoke test per
`.agents/rules/05-adlc-agentic-development-lifecycle.md`. Anything
that refreshes on time uses `Screen.armTick`; add a test that counts
armed clocks.

Invariants that every step must keep: state meaning without color, the
§3 glyph set, theme rebuild from raw payloads, the `View` height bound,
one-row headers with `~` clipping, truthful key hints, one spinner clock,
and files ≤500 LOC (`transcript.go` 607, `viewport.go` 503, `events.go`
601, `conversation.go` 774, `commands.go` 507 are already over; each
item touching them names its split).

Residual risks:

- C4's tinted background on every tool body may fight the focus rail on
  dark themes. The contrast rows are the gate.
- C6 changes what the reader sees every 66 ms. If the boundary predicate
  is wrong, a fence opens and closes visibly. The fuzz target is the
  gate.
- C7 depends on `ports.ToolCall.OK` having a producer. If the subagent
  history does not record failure, children render `+` only, and the
  item is worth less.

## 7. Phased implementation plan with prompts

Each phase is one delivery on its own branch and commit. Every prompt
below is self-contained: paste it into a fresh `mivia` or Claude Code
session at the repo root. Each prompt assumes the prior phases landed;
where a phase depends on an earlier one, the prompt says so.

Common contract for every prompt (repeated inside each so it stands
alone): read `AGENTS.md` and `.agents/memories/*.md` first; TDD (RED
before GREEN); scope `go test` to touched packages, never `./...`; run
`python3 scripts/check_go_structure.py` on touched files; never bypass
hooks; commit as `type(cli): subject`; report Outcome, Changed files,
Verification (commands actually run), Residual risk.

### Phase 0 - foundations

```text
Task: lay the foundations for the chat TUI polish plan in
docs/design/chat-tui-crush-comparison.md (§7 Phase 0). No user-visible
change. Read AGENTS.md and every file under .agents/memories/ first.

Do these four slices, each with a RED test before code:

1. Contrast rows. In internal/ui/theme/contrast.go add matrix rows
   {RoleFGSubtle on RoleBGInset} and {RoleFG on RoleBGInset}. Run
   go test ./internal/ui/theme/ and fix any embedded theme in
   internal/ui/theme/themes/ that fails the new rows by adjusting its
   bg-inset value, not by weakening the threshold.

2. Split transcript.go. internal/ui/component/transcript/transcript.go
   is 607 LOC (soft cap 500). Move the streaming code (pending buffer,
   appendPending, flushPending, handleReasoningDelta, reasoningBlock,
   tailRows and their helpers) into transcript_stream.go with no
   behavior change. Both goldens (testdata/golden/cockpit-80x20.txt,
   transcript-dump.txt) must stay byte-identical. Run
   python3 scripts/check_go_structure.py internal/ui/component/transcript/*.go.

3. Trim empty SGR pairs. testdata/golden/transcript-dump.txt line 9
   carries 55 empty ESC[38;2;…m ESC[m pairs on one prose line. In
   internal/ui/render/markdown.go post-process glamour output to drop
   styled spans whose content is empty. Write the test first: render a
   two-sentence paragraph and assert no "m\x1b[m" adjacency. Regenerate
   the goldens (the golden tests in transcript_test.go show how) and
   confirm the stripped text is unchanged with
   sed 's/\x1b\[[0-9;]*m//g' before/after.

4. Benchmarks. Add BenchmarkMarkdown (a 40-line mixed markdown document
   at width 80) to internal/ui/render/bench_test.go, and BenchmarkLayout
   (a transcript Model with 2,000 mixed blocks, call Rows() at 80x24) in
   internal/ui/component/transcript/. Record the numbers in the commit
   body; they are the baseline the later phases compare against.

Gates: go test ./internal/ui/render/ ./internal/ui/component/transcript/
./internal/ui/theme/ ; go vet on the same; the structure script above.
Commit: refactor(cli): lay chat TUI polish foundations
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk. Do not claim a check passed unless it ran.
```

### Phase 1 - outcome glyph and output card (C3 + C4)

```text
Task: implement items C3 and C4 of
docs/design/chat-tui-crush-comparison.md (read §3 and §5). Phase 0 has
landed (contrast rows for RoleBGInset exist; transcript.go is split).
Read AGENTS.md and every file under .agents/memories/ first.

Behavior to build, in internal/ui/component/transcript and
internal/ui/render:

C3. Column 1 of a tool block shows the outcome using the glyph set in
docs/design/wireframes-panes.md §3: "+" ok, "x" failed, "?" pending, and
the §3 spinner frames "-|/-" while running (frame chosen from the
existing statusline tick; do not arm a new clock, see
.agents/memories/tui-spinner-clock-*.md). The trailing state word stays
for failed, pending, running; the "ok" word is dropped. Collapse state
no longer lives in column 1. Update together: hitsCollapseMarker in
viewport.go (hit target moves to the body hint row from C4), FocusedText
in focus.go (stop stripping the marker; outcome is record, not view
state), headerPlain in block.go (copies the glyph), and confirm
render/header.go keeps a one-cell lead. Add a ToggleBlockAtScreenRow
test for the new hit target and a header_test.go case that a failed row
is one row and clips detail, never the glyph or state.

C4. Tool bodies render as a card: one blank row after the header, body
lines on RoleBGInset at BodyIndent, a 10-line window
(uikit/config/defaults.go CollapseThresholdLines becomes 10), and a
trailing hint row "… N more lines  space to expand" when the block is
focused, "… N more lines" otherwise. Remove the "… +N lines" hint from
headerMeta in block.go in the same change. The focus and failure rail
(R4) stays. Keep Height and Render in agreement: add a test that
Height(width) equals len(strings.Split(Render(...), "\n")) for a
collapsed, an expanded, and a focused card.

Spec: amend docs/design/wireframes-panes.md §4 wording for the tool row
and the hint; §3 is unchanged. Regenerate both goldens and keep the
property tests in transcript_test.go green. Add or extend the offline
smoke test (see .agents/rules/05-adlc-agentic-development-lifecycle.md,
"Smoke tests for UI-ship phases"): drive a scripted stream with one
pending→start→end success and one failure through uiadapter and assert
the rendered rows show "+" once and "x" once.

Files over the 500 LOC soft cap that you touch (viewport.go 503,
block.go will grow) must be split in the same commit; name the split.
Gates: go test ./internal/ui/component/transcript/ ./internal/ui/render/
./internal/uiadapter/ ; go vet; python3 scripts/check_go_structure.py
on touched files; python3 scripts/check_docs_ownership.py.
Commit: feat(cli): show tool outcome in column 1 and render output as a card
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 2 - thinking as a duration (C1)

```text
Task: implement item C1 of docs/design/chat-tui-crush-comparison.md
(read §3 C1 and §5). Phases 0 and 1 have landed. Read AGENTS.md and
every file under .agents/memories/ first.

In internal/ui/component/transcript:

1. Add pendingStartedAt time.Time to Model (transcript.go). Stamp it in
   appendPending (transcript_stream.go) when pendingKind switches to
   reasoning, using Model.Now. Copy it to Block.StartedAt in
   flushPending and reasoningBlock. The whole-text path (reasoning delta
   with WordCount != 0 and no prior pending) stamps Now() directly.
   RED test first: a reasoning delta at T, a text delta at T+8s, then
   assert the committed block has ElapsedMS == 8000.

2. Render the settled reasoning block as one dim line "Thought for 8s"
   using render.FormatElapsed. Default collapsed shows only that line.
   Expanded shows the last 10 rendered lines on RoleBGInset. A second
   toggle shows the full text. The third state is a new Block field
   (Expanded bool); Collapsed stays the field ToggleReasoning in
   focus.go drives for the global ctrl+r hide. Tests: the three-state
   cycle via the toggle path used by space/enter, and that ctrl+r still
   collapses every reasoning block regardless of Expanded.

3. While reasoning streams (pendingKind == reasoning), the tail row
   reads "Thinking  4s" refreshed by the existing statusline tick. Do
   not arm a new clock; if the transcript needs a tick, route it
   through Screen.armTick in internal/ui/screen/conversation and add a
   test that counts armed clocks (see
   .agents/memories/tui-spinner-clock-*.md).

Spec: amend docs/design/wireframes-panes.md §4 reasoning row. Regenerate
both goldens; the fixture in transcript_test.go must set Now so the
duration is deterministic. Extend the offline smoke test with a
reasoning delta sequence and assert "Thought for" appears once.
Gates: go test ./internal/ui/component/transcript/
./internal/ui/screen/conversation/ ./internal/uiadapter/ ; go vet;
python3 scripts/check_go_structure.py on touched files.
Commit: feat(cli): show reasoning as a duration with a three-state expand
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 3 - turn footer with model and wall time (C2)

```text
Task: implement item C2 of docs/design/chat-tui-crush-comparison.md
(read §3 C2 and §5). Phases 0 to 2 have landed. Read AGENTS.md and every
file under .agents/memories/ first.

Facts you must not assume away: uievent.UsageBody.ElapsedSeconds is
never set by uiadapter.translateTokenUsage (internal/uiadapter/event.go);
the transcript has no session access; the usage block today is
Prose: true with no header (values.go usage block).

Build:

1. Wall time. In internal/ui/component/transcript, stamp
   turnStartedAt on the TurnStartBody push using Model.Now and compute
   elapsed at the usage push. RED test: turn.start at T, usage at
   T+102s, assert the footer text contains "1m 42s".

2. Model name. Add transcript.Model.SetModel(name string). The screen
   calls it where it calls topbar.SetSession
   (internal/ui/screen/conversation/commands.go). Test in the screen
   package that SetSession propagates to the transcript.

3. Footer shape. Replace the usage prose block with a non-prose,
   non-collapsible footer block rendered as
   "+ <model>  <elapsed>  <in> in  <out> out  <cost>" followed by "-"
   padding to the full width, computed in Render from width (never
   stored). Give it its own branch in Height and bodyRows so it never
   word-wraps at ProseMeasure. Add a compact token formatter
   (1,284 → "1.3k", below 1000 unchanged) in render and use it here;
   update wireframes-panes.md §4 to the compact grammar. Keep the raw
   UsageBody on the block so a theme change rebuilds it, and keep it a
   Block so Dump() and the "[" scrollback dump still carry the fact.
   Test: Height equals rendered line count at widths 40, 80, 120, and
   the footer is one row at every width.

Regenerate both goldens. Extend the offline smoke test: one turn ends
with exactly one footer row containing the model name.
Gates: go test ./internal/ui/component/transcript/ ./internal/ui/render/
./internal/ui/screen/conversation/ ./internal/uiadapter/ ; go vet;
python3 scripts/check_go_structure.py on touched files;
python3 scripts/check_docs_ownership.py.
Commit: feat(cli): close each turn with a model, wall time, and usage rule
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 4 - live rows say what they wait for (C5)

```text
Task: implement item C5 of docs/design/chat-tui-crush-comparison.md
(read §3 C5). Phases 0 to 3 have landed. Read AGENTS.md and every file
under .agents/memories/ first.

In internal/ui/component/transcript, a tool block with
Header.State == "pending" renders its detail column suffix "waiting to
run", and with State == "running" renders "waiting for result", both in
RoleFGSubtle, with the §3 spinner frame in column 1 (Phase 1 wired the
frame source). The wording "requesting approval" must not be used: a
policy-auto-approved pending call never enters the approval queue. On
tool.end the suffix disappears and the elapsed meta appears.

RED tests first: pending → row contains "waiting to run"; start → row
contains "waiting for result" and not "waiting to run"; end → neither.
Add a header_test.go case that the suffix is clipped before the label
when the row is too narrow. Regenerate the cockpit golden if the fixture
has a live block; otherwise add a live block to the fixture so the
golden covers it. Extend the offline smoke test to assert the two
phrases each appear exactly once across a pending→start→end sequence.
Gates: go test ./internal/ui/component/transcript/ ./internal/ui/render/
./internal/uiadapter/ ; go vet; python3 scripts/check_go_structure.py.
Commit: feat(cli): say what a live tool row is waiting for
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 5 - streamed markdown with a stable-prefix cache (C6)

```text
Task: implement item C6 of docs/design/chat-tui-crush-comparison.md
(read §3 C6 and §6). Phases 0 to 4 have landed; BenchmarkMarkdown exists
in internal/ui/render/bench_test.go with a recorded baseline in the
Phase 0 commit body. Read AGENTS.md and every file under
.agents/memories/ first. This is the largest phase; do it in the order
below and commit once.

Reference design (do not copy code; the license differs): Crush's
internal/ui/chat/streaming_markdown.go keeps the rendered output of the
longest content prefix that ends at a "safe boundary" and renders only
the delta. A boundary is a position after a blank line where: the
triple-backtick fence count is even, no HTML block or link-reference
definition is open, the last non-blank line does not open a list item
or table row, and the next non-blank line is not a setext underline.
Fence and list counters are maintained incrementally so validation is
O(delta). A relaxed rule cuts at a plain newline once the unrendered
tail exceeds 2 KiB, so unbroken prose cannot go quadratic.

1. Add internal/ui/render/stream_markdown.go with type StreamRenderer
   {Render(content string, width int) string; Reset()}. It holds
   stablePrefix, stablePrefixRender, width, and the incremental
   counters. render.Markdown builds a fresh glamour renderer per call,
   so there is no shared-state hazard. Unit tests first: width change
   resets; non-prefix content resets; a boundary inside an open fence is
   rejected; the relaxed rule triggers past 2 KiB.

2. Add fuzz target FuzzStreamedMarkdownMatchesOneShot in
   internal/ui/render/: feed random UTF-8 markdown in random-size
   chunks through StreamRenderer and assert the final output is
   byte-identical to render.Markdown(whole). Run it seeded only
   (go test, no -fuzz flag) in the gate; if you run -fuzz at all use
   -parallel 2 -fuzztime 60s, never the default (see AGENTS.md).

3. Wire it: in internal/ui/component/transcript/transcript_stream.go,
   tailRows renders the pending text buffer through a StreamRenderer
   held by pointer on Model (value copies share it; Clear() replaces
   it). An open fence or table stays in the tail and commits atomically
   at text.end. The tail keeps its indent so the commit changes style,
   not layout.

4. Lower uikit/config/defaults.go TextDeltaFlushInterval to 66 ms and
   update its comment and docs/design/ux-rules.md 2.5 if it cites the
   number.

5. Measure: run BenchmarkMarkdown and the new BenchmarkStream (streamed
   40-line document in 20-byte chunks) and put before/after numbers in
   the commit body. Run BenchmarkLayout from Phase 0 and record whether
   Rows() at 2k blocks exceeds 16 ms; that number decides Phase 8.

Regenerate both goldens. Extend the offline smoke test: stream a
document with a fenced code block in three deltas and assert the fence
renders once, highlighted, with no intermediate unclosed-fence row in
the final transcript.
Gates: go test ./internal/ui/render/ ./internal/ui/component/transcript/
./internal/uiadapter/ ./internal/uikit/config/ ; go test -race
./internal/ui/component/transcript/ ; go vet; python3
scripts/check_go_structure.py on touched files.
Commit: feat(cli): stream assistant markdown through a stable-prefix cache
Report Outcome, Changed files, Verification with the commands and the
benchmark numbers, Residual risk.
```

### Phase 6 - diff policy and split toggles (C8)

```text
Task: implement item C8 of docs/design/chat-tui-crush-comparison.md
(read §3 C8; it closes transcript-polish.md R10). Phases 0 to 5 have
landed. Read AGENTS.md and every file under .agents/memories/ first.

1. internal/ui/render/diff_split.go: raise MinSplitDiffWidth to 120 and
   make split opt-in: FormatDiffLines takes a split bool; unified is the
   default at every width. Update the comment and the tests.
2. Keymap: bind "s" in the transcript focus context to toggle split on
   the focused diff block (internal/uikit/keymap/keymap.go; "s" is
   unbound today), and "t" in the approval context to toggle split in
   the approval preview (internal/ui/component/approval/approval.go,
   diffLines/diffWindow). Both toggles are refused, with no hint shown,
   below 120 columns (ux-rules 1.4: hints state the true binding). The
   help screen and the status hints derive from the keymap; confirm the
   new bindings appear there without hand edits.
3. Diff bodies and the approval preview are exempt from ProseMeasure so
   they use the full width; add a test at width 160 that a split diff
   uses more than 92 columns.

Spec: wireframes-panes.md §11 and §14 already specify "s"; update §15
if it lists keys. Regenerate goldens. Tests: keymap conflict test (no
duplicate binding in a context), toggle round-trip on a focused diff
block, approval Height/View agreement after toggling.
Gates: go test ./internal/ui/render/ ./internal/uikit/keymap/
./internal/ui/component/approval/ ./internal/ui/component/transcript/
./internal/ui/screen/conversation/ ; go vet; python3
scripts/check_go_structure.py; python3 scripts/check_docs_ownership.py.
Commit: feat(cli): default to unified diffs and add split toggles
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 7 - chrome: hints, composer focus, approval header (C9-C11)

```text
Task: implement items C9, C10, C11 of
docs/design/chat-tui-crush-comparison.md (read §3). Phases 0 to 6 have
landed. Read AGENTS.md and every file under .agents/memories/ first.
Three slices, one commit each, in this order.

Slice A (C9, status hints). internal/ui/screen/conversation/status.go
and internal/ui/component/statusline/statusline.go: join hints with the
existing " · " divider (ASCII tier " - ", statusline.go already has it);
render the key in RoleFGSubtle and the label in RoleFGMuted; rewrite
"tab" and "esc" hints per state: "esc cancel" only while a turn runs,
"esc clear queue" only when the queue is non-empty, no esc hint when
idle. The keymap stays the single source; the width ladder in status.go
must still degrade gracefully. Tests: hint string per state; no hint
names a key that is not bound in that context. Regenerate the cockpit
golden. Commit: style(cli): join status hints and make esc hints truthful

Slice B (C10, composer focus). internal/ui/component/composer: add
Focused() bool. Blurred renders the marker ":" in RoleFGSubtle with no
fill bar; focused renders "› " in RoleAccent with the fill bar (ASCII
tier ">" as today). AcceptMention appends to a chips []string rendered
as one row under the prompt ("@ a.go  @ b.go"), cleared on submit;
Height accounts for the chip row. Tests: Height/View agreement with and
without chips; blurred view contains no fill. Commit:
feat(cli): show composer focus state and mention chips

Slice C (C11, approval header). internal/ui/component/approval: render
"Tool", then "Path"/"File"/"Command" (whichever the tool detail
provides) as aligned key/value rows inside the box above the preview,
and drop the action from the border label; the label keeps the badge
and the queue depth. View and Height must agree (there is an existing
assertion near approval.go:305). Tests: rows present for a command tool
and a file tool; one-row clip at width 40. Regenerate goldens if the
approval box appears in one. Commit:
style(cli): show approval tool and target as key/value rows

Gates per slice: go test on the touched package plus
./internal/ui/screen/conversation/ ; go vet; python3
scripts/check_go_structure.py on touched files. Report per slice.
```

### Phase 8 - compact child tree under a dispatch row (C7)

```text
Task: implement item C7 of docs/design/chat-tui-crush-comparison.md
(read §3 C7 and §5). Phases 0 to 7 have landed. Read AGENTS.md and every
file under .agents/memories/ first, especially viewer-surfaces-must-agree
and sibling-implementations-drift.

Facts: root-transcript subagent progress events carry counts and log
text, not child tool names (internal/uiadapter/event_kind.go). Child
calls live in ports.SubagentThreads.Thread(callID).History()[].ToolCalls,
which the conversation screen owns. ports.ToolCall has no success field.

1. Port change. Add OK bool to internal/uikit/ports.ToolCall and set it
   in the producer (internal/uiadapter/subagent*.go history). RED test
   first in uiadapter: a failed child tool.end yields OK == false.
   Confirm the plain renderer (internal/ui/stream) and jsonout are not
   consumers of ToolCall; if either is, update it in the same commit.

2. Transcript API. Add transcript.Model.SetChildren(callID string,
   children []ChildCall) where ChildCall{Name, Detail string; OK bool}.
   Child rows are body rows of the parent block: "  + read_file a.go",
   "  x edit b.go", at most 6, then "+N more", followed by the existing
   "open" hint. Height counts them. A block with children is exempt from
   work-run folding (workRunLen in layout.go); add a layout test that a
   settled dispatch block with children keeps its own rows while its
   siblings fold.

3. Screen wiring. In internal/ui/screen/conversation, on each subagent
   progress event and on thread end, read the thread history and call
   SetChildren for that callID. Do not arm a clock. Test: a scripted
   thread with two child calls, one failed, renders "+" and "x" child
   rows under the parent.

Regenerate both goldens (add a dispatch block with children to the
fixture). Extend the offline smoke test to assert child rows appear once
and the parent row is not folded. UI isolation: internal/ui must not
import uiadapter; the screen talks only through ports.
Gates: go test ./internal/uikit/ports/ ./internal/uiadapter/
./internal/ui/component/transcript/ ./internal/ui/screen/conversation/
./internal/ui/stream/ ; go vet; python3 scripts/check_import_layers.py;
python3 scripts/check_go_structure.py on touched files.
Commit: feat(cli): show subagent child calls under the dispatch row
Report Outcome, Changed files, Verification with the commands you ran,
Residual risk.
```

### Phase 9 - conditional render memo (C13 + C14)

```text
Task: decide and, only if justified, implement items C13 and C14 of
docs/design/chat-tui-crush-comparison.md (read §3 P2). Phases 0 to 8
have landed. Read AGENTS.md and every file under .agents/memories/ first.

Step 1, decide. Run go test -run XXX -bench BenchmarkLayout -benchmem
./internal/ui/component/transcript/ three times and compare with the
Phase 0 and Phase 5 numbers in those commit bodies. If Rows() at 2,000
blocks completes under 16 ms per call, stop: report the numbers, do not
implement, and add the trigger to docs/design/chat-tui-crush-comparison.md
§3 C13 ("re-measure when …"). Commit only the doc line.

Step 2, only above 16 ms. Implement a *renderCache held by pointer on
transcript.Model (value copies share it; Clear() replaces it), keyed by
(block ID, width, tier, version). Add a version counter to Block and
bump it at every mutation site: updateLive sites in transcript.go,
restyle, ToggleReasoning, collapse and focus toggles, SetChildren.
layout() reads heights from the memo. Bodies render as lines with a
per-line style prefix instead of Style.Render over a block, so the memo
stores plain lines (C14). Tests: a copied Model shares no stale entry
after Clear(); a theme change invalidates every entry; a mutated block
re-renders; a golden byte-identical before and after. Re-run the
benchmark and put before/after in the commit body.
Gates: go test ./internal/ui/component/transcript/ ; go test -race the
same; go vet; python3 scripts/check_go_structure.py on touched files.
Commit: perf(cli): memoize transcript block rendering
Report Outcome, Changed files, Verification with the numbers, Residual
risk.
```
