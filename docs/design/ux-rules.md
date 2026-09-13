# Mivia terminal UI - UX rules

Scope: these rules govern `internal/ui`, `internal/uikit` and the terminal
entry path in `internal/newtui`. They bind implementation choices. A rule
that only says "be consistent" is not a rule and is not listed here.

`wireframes-panes.md` stays the visual specification. This file wins on
interaction and mechanics. Section 10 cross-references it.

---

## 1. Reserved keys

Do not bind these. The terminal, the tty line discipline, or readline owns
them. The consequence column states what breaks.

| Key | Owner | Consequence of binding it |
|---|---|---|
| `Ctrl-C` | `VINTR`, termios | Breaks the universal abort reflex |
| `Ctrl-D` | EOF | Breaks the standard exit gesture |
| `Ctrl-Z` | `VSUSP` | Loses job control; corrupts the terminal on resume |
| `Ctrl-\` | `VQUIT` | Removes the last-resort kill |
| `Ctrl-S` | XOFF, `IXON` | Output freezes; the session looks hung |
| `Ctrl-Q` | XON | The user cannot recover from `Ctrl-S` |
| `Ctrl-V` | `VLNEXT` | Breaks literal control-character entry |
| `Ctrl-U` | readline `unix-line-discard` | Destroys typed text unpredictably |
| `Ctrl-W` | readline `unix-word-rubout` | Also "close tab" in many emulators |
| `Ctrl-A` | readline `beginning-of-line` | Also the default `tmux` prefix |
| `Ctrl-E` | readline `end-of-line` | Per-keystroke friction in the composer |
| `Ctrl-K` | readline `kill-line` | Same |
| `Ctrl-R` | readline `reverse-search-history` | Same |
| `Ctrl-B` | `screen` prefix | Invisible to `screen` users |

Reservation applies to the context that the owner claims.

`Ctrl-U`, `Ctrl-E`, `Ctrl-K` and `Ctrl-R` belong to readline, and readline
owns the line editor. Bind them outside the composer, or bind them to the
same action readline gives them. The keymap binds `Ctrl-U` in the composer
to clear-line, which is the readline action.

`Ctrl-D` is EOF on an empty line, so it stays reserved inside the composer.
Outside the line editor it is free. The pager binds it as half a page down,
which is what `less` does; a pager has no EOF gesture to break.

`Ctrl-B` is the GNU screen prefix. Screen intercepts it before the
application sees it, so the pager's binding is inert for screen users, not
harmful. The pager keeps the modifier-free alternates `b` and `space`.

`Ctrl-S`, `Ctrl-Q`, `Ctrl-C`, `Ctrl-Z`, `Ctrl-\`, `Ctrl-V` and `Ctrl-M`
belong to the tty or the terminal in every context. No context escapes
them. `internal/uikit/keymap` enforces this list mechanically.

**Rule 1.1.** Bind actions to `Ctrl-G`, `Ctrl-O`, `Ctrl-T`, function keys,
or a prefix. These are free in practice.

**Rule 1.2.** `Ctrl-S` is radioactive even in raw mode. Raw mode clears
`IXON`, so the application does receive the byte. But `ssh`, `screen`, and
any spawned pager reinstate flow control. Do not bind it.

**Rule 1.3.** `Ctrl-C` cancels the running turn. A second `Ctrl-C`, at an
empty composer and within a timeout, exits. Print the second step on screen
when the first press lands.

**Rule 1.4.** The footer hint must state the complete truth for the current
state. Do not advertise one cancel key and silently accept another.

---

## 2. Rendering and repaint

**Rule 2.1.** A committed turn is immutable. Only the live tail and the
composer repaint. Re-rendering history breaks screen readers and adds
seconds of input lag.

**Rule 2.2.** Split the transcript into an append-only committed region and
a small live region. This is Ink's `<Static>` and Ratatui's
`Viewport::Inline`.

**Rule 2.3.** Never full-erase and repaint the whole transcript. That is
the documented cause of the flicker reports against Gemini CLI's default
mode.

**Rule 2.4.** An inline mode must be genuinely append-only. Full-screen
redraw in the primary buffer corrupts scrollback and gives up layout
guarantees.

**Rule 2.5.** Cap the repaint rate at 10-20 Hz. Coalesce token deltas.
`indicatif` refreshes at most 20 times a second. The transcript's flush
tick is 66 ms (15 Hz), inside this ceiling.

**Rule 2.6.** Do not hand-roll synchronized output. Bubble Tea v2 enables
mode 2026, which prevents tearing, and mode 2027 for wide Unicode,
automatically. The underlying sequences are `CSI ?2026 h` and
`CSI ?2026 l`; read them to understand the mechanism, not to emit them.

**Rule 2.7.** Reserve fixed height for transient chrome. A warning that
appears and disappears must occupy its row when hidden. A one-line change
reflows every wrapped line above it and destroys both reading position and
any selection. The cockpit applies this rule directly: the top bar, the
status row, the framed composer, and the one-column gutter each side claim
their rows whether or not they have content. Scrolling a surface, such as
the approval diff, never changes the rows it claims.

**Rule 2.8.** Never move the composer while output streams.

---

## 3. The renderer

The cockpit owns the whole terminal surface through the alternate screen.
The alternate screen costs four capabilities the user already had:

1. The session transcript never enters scrollback.
2. `tmux` copy-mode has nothing to show.
3. The terminal's own find cannot search it.
4. Selection across the whole session is impossible.

These are requirements, not objections: the cockpit must replace each one.
[Cockpit rules](cockpit-research.md) maps every lost capability to its
replacement. One warning survives intact: "the alternate screen preserves
scrollback" conflates two guarantees. It preserves what existed before the
application started. Everything the application prints is destroyed on
exit.

**Rule 3.1.** The cockpit is the only interactive renderer. There is no
inline mode and no flag that selects one. The non-interactive paths are
unaffected: `--output json` gives NDJSON, and a non-TTY stdout gives the
plain stream. Two interactive renderers double the surface that every
later feature must satisfy, so the second one does not exist.

**Rule 3.3.** A cockpit owes the user a one-key path that writes the whole
conversation into native scrollback. A file export does not restore
terminal find, `tmux` copy-mode, or selection. The pager binds this to
`[`; see rule 6.3 of [cockpit-research.md](cockpit-research.md).

**Rule 3.4.** Detect multiplexers at startup and change the default.
Alternate screen buffers have no scrollback, and Zellij enforces that.

**Rule 3.5.** Do not enable the cockpit and mouse capture without shipping
the copy path in the same change. In-app drag-select with copy, the
override key for native selection (rule 7.5), and the capture off switch
(rule 7.2) ship together or capture stays off.

---

## 4. Composer input

**Rule 4.1.** `Enter` submits, always.

**Rule 4.2.** `Ctrl-J` is the primary newline, not a fallback. `Ctrl-J` is
line feed, `0x0A`. It works in every terminal with no setup and no
negotiation.

**Rule 4.3.** Backslash then `Enter` is the typeable newline escape hatch.
It needs no key detection and survives `screen` and mosh.

**Rule 4.4.** `Shift-Enter` is a bonus. Enable it only after querying the
Kitty keyboard protocol with `CSI ? u`. Push flag `0b1`. Pop it on exit,
including on panic. Never push flag `0b1000`: it stops `Ctrl-C` generating
SIGINT.

**Rule 4.5.** A failed `Shift-Enter` must never print into the composer.
The common symptom is a literal `OM`. Filter unknown SS3 and CSI sequences.

**Rule 4.6.** Do not document `Alt-Enter` as primary. macOS does not send
Option as a modifier until the user enables "Use Option as Meta Key".

---

## 5. Slash commands and mentions

**Rule 5.1.** Open the command menu only when the buffer's first
non-whitespace character is `/`, the cursor sits in that leading token, and
the keystroke came from typing. This makes `src/foo` structurally incapable
of triggering it.

**Rule 5.2.** A `/` typed mid-sentence must not open the menu. This failure
mode has a long regression history across agent CLIs.

**Rule 5.3.** Check menu state before submit, from one flag. `Enter`
accepts the highlighted row when the menu is open, and does not also
submit.

**Rule 5.4.** `Esc` closes the menu and nothing else on the first press.
The buffer keeps its text. A second `Esc` falls through to the
application.

**Rule 5.5.** Arrow keys move the highlight only while the menu is open.
Otherwise they belong to history. Gate the menu on a typed-keystroke flag,
not on buffer contents, or history recall traps the arrows.

**Rule 5.6.** Do not auto-descend into subcommands on an exact match. Do
not append a trailing space after one. `/stats` plus `Enter` must run
`/stats`, not `/stats session`.

**Rule 5.7.** Score every candidate. Cap only the rendered rows. Render at
least 6 rows with a scrolling window. Capping candidates before scoring
makes later matches unreachable. The row cap is `config.MaxCompletionRows`
(6).

**Rule 5.8.** Use fzf-style scored fuzzy matching with word-boundary,
path-separator and camelCase bonuses. Sort exact name matches first.

**Rule 5.9.** Trigger `@` at any token start, anywhere in the line. With no
match, leave the `@` as literal text and never block submission.

**Rule 5.10.** Ship one sigil. `@` covers every workspace entity.
Disambiguate by a type badge on the row, not by a second sigil. `#` and
`>` carry no cross-product meaning.

**Rule 5.11.** Exclude git-ignored paths from mention candidates. This is
a relevance rule and a secrets rule; it keeps `.env` out.

**Rule 5.12.** Build the candidate index once and update it incrementally.
Never rebuild per keystroke. Never walk outside the workspace. A startup
scan of `$HOME` pins a CPU core for minutes.

**Rule 5.13.** Repaint the menu within 100 ms of the keystroke. That is
the limit for the user to feel their action caused it.

---

## 6. Focus

**Rule 6.1.** Do not use `Tab` to move focus while a completion menu can
be open. Resolve it as: `Tab` completes when the menu is open, and moves
focus only when it is closed.

**Rule 6.2.** Single-key actions are unavailable while the composer holds
focus. Route them through an explicit focus change or a command palette. A
text-first application cannot bind bare letters globally.

**Rule 6.3.** Never signal focus by colour alone. WCAG 1.4.1 requires the
information without colour perception.

**Rule 6.4.** Indicate focus with a gutter marker plus reverse video.
Reverse video inherits the theme's own contrast, so it survives any
palette. Bold alone is unreliable: many terminals render bold as a
brighter colour, which degrades to a colour-only cue.

**Rule 6.5.** Focusing an older item turns auto-follow off. Show that it
is off. Anchor focus to a stable item identity, and capture the anchor at
the moment of the move, not at the moment of the restore. Live mutations
append rows and then rebind, so an index captured after the move can point
into a changed list; the move is the only moment where index and model
agree. Restoring from a stale index silently selects the wrong row.

**Rule 6.6.** Keep a paused viewport paused when the turn finishes. The
finish event is when a naive implementation yanks the user away.

**Rule 6.7.** Show a jump-to-latest affordance with a count while paused.
Approval prompts override the pause and scroll into view, or the agent
appears to hang.

**Rule 6.8.** A list whose sections grow without bound during a run must
let the user fold a section. The files and subagents lists both grow for
the length of a long run and push each other off the pane; without a fold
the reader cannot keep either in view. The section header is the
selectable row that owns the fold: left closes, right opens, Enter
toggles. A section with nothing in it keeps a plain caption and no marker;
offering a fold over nothing costs a stop on the way past and does nothing
when taken. A folded section still states its count, and a folded gauge
still states its share: folding must not cost the reader the number they
were watching.

---

## 7. Mouse

**Rule 7.1.** Mouse capture is ON by default in the cockpit, because the
cockpit provides its own drag-select with OSC 52 copy while capturing, so
"text exists to be copied" is served in-app. Native terminal selection
stays reachable through the per-terminal override key (rule 7.5) and the
live Settings toggle that hands the mouse back entirely. Capture resolves
at startup as `MIVIA_MOUSE` env > `[tui] mouse` config > default true.
The warning stands for any surface without a working in-app selection:
ship capture off there, because agent output is prose, code, diffs and
paths, and that text exists to be copied.

**Rule 7.2.** The capture off switch ships with the first mouse feature,
not after it. The switches are the `[tui] mouse` config key, the
`MIVIA_MOUSE` environment variable, and the Settings → General toggle. A
mouse feature without a working off switch is a regression; three
independent tools logged exactly that.

**Rule 7.3.** Use SGR mode 1006 only. Legacy encodings cap coordinates at
column 223. Terminals routinely exceed that.

**Rule 7.4.** Enable the narrowest mode that satisfies the feature. Prefer
alternate-scroll, `CSI ?1007h`, for wheel-only. Do not reach for mode
1003.

**Rule 7.5.** Never tell the user to hold Shift. The modifier is `Option`
in iTerm2, `Fn` in Terminal.app, and absent in xterm.js, code-server and
VS Code web. Show the correct key for the detected terminal, or show none.
The help overlay shows the probed terminal's own key.

**Rule 7.6.** Mouse capture also steals middle-click PRIMARY paste on
Linux. Treat that as a first-class regression, not an edge case.

**Rule 7.7.** Disable mouse tracking on exit and on panic. Otherwise the
user's terminal stays broken.

**Rule 7.8.** A row that draws a fold marker must toggle on a click on
that row, in both directions. A marker that only ever opens is a control
the user cannot use to put the screen back: with an expand-only handler, a
mis-click on a 400-line tool result was undoable from the keyboard alone.
The converse holds: a click on a body row falls through, so expanded
content is never folded away by a stray click. Bound by this rule:
transcript block headers and coalesced run rows (`ToggleBlockAtScreenRow`),
and sidebar section headers (`handleNavClick`).

**Rule 7.9.** A click row must be derived from the same geometry the
renderer drew, and tests for it must read the row back out of the rendered
output. A test that recomputes the renderer's arithmetic agrees with the
code by construction: when the span geometry named the blank separator row
instead of the header, clicking the header did nothing, clicking the blank
row expanded the block, and every test passed, because each derived its
click row from the same wrong arithmetic.

---

## 8. Clipboard

**Rule 8.1.** OSC 52 is a supplement, never the primary clipboard path.
Prefer the platform tool. Use the `tmux` buffer inside `tmux`. Fall back
to OSC 52 over SSH only.

**Rule 8.1a.** Bubble Tea v2 provides OSC 52 directly: `tea.SetClipboard`,
`SetPrimaryClipboard`, `ReadClipboard` and `ClipboardMsg`. Use those for
the fallback path rather than writing escape sequences. Pair them with a
local clipboard path: terminals that refuse OSC 52 outright still get the
copy.

**Rule 8.2.** OSC 52 is absent on VTE, which is the GNOME Terminal family,
and on macOS Terminal.app. Those are the two most common defaults on Linux
desktop and macOS.

**Rule 8.3.** OSC 52 payload limits fail silently. xterm caps decoded
content at 100,000 bytes. VTE drops oversize sequences with no error.
Base64 inflates the payload by a third. "OSC 52 solves clipboard over SSH"
holds only where every layer supports it and the payload is small.

**Rule 8.4.** Treat clipboard read as unavailable. xterm disallows
`GetSelection` by default; kitty asks; WezTerm defaults reading off. Do
not design a feature that needs it.

**Rule 8.5.** Suppress OSC 52 inside a multiplexer that already handles
it. Two writers race and corrupt the clipboard.

**Rule 8.6.** Print which clipboard path was used after every copy. Silent
clipboard failure is a recurring bug class; the copy confirmation states
the path so a silent drop is visible.

---

## 9. Accessibility, colour and degradation

**Rule 9.1.** Screen-reader mode always renders plain scrolling text. It
never enters the cockpit. Print an explanation instead of switching. An
app-owned viewport emits nothing a screen reader can follow.

**Rule 9.2.** Screen-reader mode is a separate render path, not a theme.
Remove box drawing, colour-only cues, and redraws of unchanged content.
Prefix every turn with a searchable label.

**Rule 9.3.** Reduced motion is a separate setting from screen-reader
mode. Magnifier and colourblind users are not screen-reader users.

**Rule 9.4.** Suppress colour when `NO_COLOR` is present and not empty,
whatever its value. `NO_COLOR=yes` and `NO_COLOR=0` both disable colour.
`NO_COLOR=` empty does not.

**Rule 9.5.** `NO_COLOR` disables colour, not text decoration. Bold, faint
and underline survive.

**Rule 9.6.** Treat `TERM=dumb` as a hard signal to take the non-TTY path.
A dumb terminal has no cursor addressing, so repaint-in-place is invalid.

**Rule 9.7.** Gate every terminal feature on stdout and stdin both being
TTYs. Show no animation when stdout is not a TTY.

**Rule 9.8.** Never prompt when stdin is not a TTY. A permission prompt
needs a non-interactive failure mode, never a silent approval.

**Rule 9.9.** Restore terminal state on SIGINT, SIGTERM, SIGHUP and panic.
Alternate screen, mouse tracking, raw mode and keyboard flags all leave
the terminal unusable if the process dies without emitting the reset.

**Rule 9.10.** Show the active tool or step, not only a spinner. A spinner
cannot distinguish thinking from hung.

---

## 10. Cross-reference: `wireframes-panes.md`

That file stays the visual specification. This table maps its rows to the
current interaction state. The keymap package (`internal/uikit/keymap`) is
the authority for current bindings; read it, never this table.

| `wireframes-panes.md` section | Current state |
|---|---|
| 1 | `Ctrl-D` is readline-owned inside the composer and free outside; the pager binds it as half page down (section 1) |
| 3 | The cockpit is the only interactive renderer (rule 3.1); mouse capture defaults on, with in-app selection and the rule 7.2 off switches |
| 5 | Blocks open under the collapse threshold, close at or above it; a collapsed header states its magnitude (section 11) |
| 10 | The slash menu is start-anchored (rule 5.1); `Tab` accepts the common prefix and `Enter` accepts the highlighted row while the menu is open (rule 6.1) |
| 12 | `@` triggers at any token start, anywhere in the line (rule 5.9) |
| 15 | Reasoning toggles on `Ctrl-R` (global), collapse-all on `Ctrl-G`, expand-all on `Ctrl-E` (transcript context), settings on `F2`, palette on `Ctrl-P`/`Ctrl-X`; `Ctrl-M` is never bound (byte-identical to `Enter`); bare decision keys live in the transcript focus context (`y` copy, `s` diff split, `x` cancel tool call) |
| 2, 4, 5 | Transcript presentation grammar (markers, magnitude hints, duration ladder, usage footer, body rail, run coalescing) lives in section 11 of this file |

---

## 11. Transcript layout

These rules govern `internal/ui/component/transcript`. They are
presentation rules: coalescing and grouping change only what renders.
Blocks stay individual records, so focus, click-to-toggle, copy and
`Dump()` keep per-block identity and full content.

**Rule 11.1.** Spacing follows turns, not events. A blank row separates
turn sections - prose, or the start of a new activity group - and no blank
row sits between members of one activity run. The one exception: a visible
tool card (a tinted multi-line body) does not touch the block after it; a
gap row separates them.

**Rule 11.2.** Activity blocks hang under the turn that produced them at a
2-column group indent; the marker sits at column 3. Prose - the user turn,
assistant text, the usage footer - is the conversation voice and stays at
the top level.

**Rule 11.3.** Two or more consecutive collapsed read-only lookups of one
class (`read_file`, `search`, `list`) coalesce into one leader row that
names its targets (`Read 3 files: a.go, b.go, c.go`). A live, failed or
expanded block never folds into one.

**Rule 11.4.** Three or more consecutive finished activity blocks of any
kind coalesce into one work row that states what ran, how many calls, and
the elapsed time. Rules: running work keeps its own row (it is what the
reader waits on); a failed block never folds (a summary row that could
swallow a failure hides the one block worth the rows); three is the floor;
the read-only row wins a tie because it names its targets; the run's
duration is the sum of the member durations, because calls issued in
parallel add to more than the wall clock. The fold is driven by each
member's own collapsed state, so collapsing the members again re-forms the
run with no extra state.

**Rule 11.5.** Collapse behaviour: a marker prints only where a body
exists; header-only blocks keep a blank marker column. A collapsed body
states its magnitude in the meta (`… +N lines`). A toggle works in both
directions from the keyboard (`space`/`enter` on the focused block) and
from a click on the marker row (rule 7.8). Hints name the true keys; a
global one-key expand needs a reserved-key analysis against section 1
first. Reasoning blocks have three states: collapsed is one summary row,
windowed shows the last `CollapseThresholdLines` rows, expanded shows all.

**Rule 11.6.** The `│` body rail prints on the focused block and the
failed block only. Every other expanded body indents with plain spaces at
the same 4-column position. A rail on every block contradicts the resting
layout and costs a column for no information.

**Rule 11.7.** One duration ladder everywhere: `render.FormatElapsed`
renders under a second as milliseconds (`250ms`), under a minute as
seconds (`4.1s`), and past that as minutes and seconds (`1m 05s`). Tool
meta, subagent ends, and the statusline all use it. Raw `%dms` values do
not appear on screen.

**Rule 11.8.** Usage is one dim, header-less footer line per turn
(`1,284 in  2,940 out  340 cached  $0.04`), not a header block. The footer
stays a `Block` in the model: the alternate screen has no native
scrollback, so removing the fact from the model removes it from the `[`
dump, grep and `tmux` copy. The topbar gauge and statusline cost pill keep
their live roles; the transcript keeps the historical record.

**Rule 11.9.** For a tool the formatter does not know, the header keeps
the tool name and the body carries each output line once. The first output
line must not print twice, once as header detail and once as body row one.

**Rule 11.10.** The streaming tail renders through the same markdown path
as the committed block, so the flush changes style, not layout. The tail
keeps a fixed indent, and an open code fence, blockquote or heading stays
in the tail buffer and commits atomically once its boundary is safe to
cut. The flush tick is 66 ms (15 Hz), inside rule 2.5.

**Rule 11.11.** The `Ctrl-O` pager and the `[` scrollback dump render the
conversation expanded, with the same section separators and group indents
as the live view. Leader runs never appear in a dump: with every member
expanded there is no run to coalesce.

---

## 12. Tool output framing

These rules govern the recorded-result tool formatters
(`internal/ui/render`): `ledger_read`, `read_output`,
`inspect_repository`, the `memory_*` family, and every formatter that
prints a tool result into the transcript.

**Rule 12.1.** Every recorded-result formatter decodes through one ladder:
trim, unwrap JSON-string layers (bounded, so a hostile payload cannot
drive a loop), parse the envelope, salvage with expressions that accept
escaped quotes, and only then fall back. The ladder styles recorded bytes;
it never executes them.

**Rule 12.2.** Bytes the ladder cannot parse still render - the model saw
them, and the reader may need them - but a dim first line labels them
(`unparsed tool result · N B`), so a blob is never mistaken for formatted
content.

**Rule 12.3.** A raw model reasoning dump never reaches the transcript.
Every closed `<think>…</think>` block, and one leading unclosed block from
a truncated payload, is replaced by one dim badge (`· thinking N words
hidden`) that leads the body, so the fact survives a collapsed block's
head window. The model keeps receiving the raw bytes; this shapes only the
display.

**Rule 12.4.** Each tool family recognizes its own error envelope and
renders the error as a one-line summary (`✖ message`) instead of dumping
the JSON object.

**Rule 12.5.** The tool-end header carries the formatter's summary - ref,
size, kind, truncation and paging state - not a raw echo of the call
arguments. The header must state the facts a reader needs without
expanding.

**Rule 12.6.** Truncation is a header badge, not only a tail trailer:
`· truncated` for a cut payload, `· more · offset=N` for a paged read. The
trailer lines stay in the body and travel with `Dump()`, but a reader who
never expands still sees the fact.

**Rule 12.7.** `inspect_repository` output caps per-file matches and
prints `… +N more in this file`; long paths middle-truncate so both ends
stay visible; the summary counts what the envelope claims
(`N matches in M files · truncated, showing X`), not only what survived.

**Rule 12.8.** The memory formatters route through the same ladder: a
`{"results": [...]}` wrapper parses, plain save/delete sentences pass
through unchanged, and a payload that looks like JSON but does not parse
gets the unparsed label, never a naked dump.
