# Cockpit research

Status: evidence for the full-screen cockpit renderer. Researched on
2026-08-19.

Scope: this file decides HOW the cockpit works and WHAT it must replace. It
does not restate the block layout, which stays in `wireframes-panes.md`.

This research overturns section 3 of `ux-rules.md`. Section 10 of that file
records the change. Read section 6 below first: it is the correction.

---

## 1. Why the cockpit, in one paragraph

The cockpit is not a style choice. It buys three things the inline renderer
cannot give at any price: no flicker, constant memory in a long session, and
mouse input. An agent session runs for hours and produces tens of thousands
of lines. The inline renderer holds every line in the terminal and repaints
against a moving frame, so cost grows with the session. The cockpit draws
only the rows that are visible, so cost is a function of the WINDOW, not of
the session.

Source: [Claude Code fullscreen rendering](https://code.claude.com/docs/en/fullscreen).

---

## 2. What the field actually did

Claude Code renders fullscreen BY DEFAULT for every user who started on or
after 2026-05-06. The classic renderer is now the opt-out, through
`/tui default` or `CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1`.

The stated reasons are rendering throughput, memory, and mouse support. The
gain is largest where throughput is the bottleneck: the VS Code integrated
terminal, tmux, and iTerm2.

Codex and opencode also render to the alternate screen. opencode added a
selection mode because native selection stops working there.

Sources: [Claude Code fullscreen rendering](https://code.claude.com/docs/en/fullscreen),
[opencode copy mode](https://github.com/anomalyco/opencode/issues/2755),
[codex alt-screen issue](https://github.com/openai/codex/issues/2836).

---

## 3. The four capabilities the alternate screen removes

Each one has a known mitigation. A cockpit that ships without the
mitigation ships a regression.

| Lost | Mitigation | Rule |
|---|---|---|
| Terminal find (`Cmd-F`, tmux search) | In-app transcript mode with `less` keys and `/` search | 6.2 |
| The session in native scrollback | A key that WRITES the transcript into scrollback on demand | 6.3 |
| Native click-and-drag selection | In-app selection that copies on release, plus a modifier-key escape hatch | 6.5 |
| Native wheel scrolling | In-app scrolling with a speed multiplier | 6.6 |

**Rule 6.1. AMENDED on 2026-08-19.** The cockpit is the ONLY interactive
renderer. The inline renderer is removed, not kept behind a flag.

The original rule kept inline as `--tui inline`. The product owner decided
against a second interactive renderer: two renderers double the surface that
every later feature must satisfy, and the non-interactive paths already cover
the cases inline was for. `--output json` gives NDJSON, and a non-TTY stdout
gives the plain stream. Both are unaffected by this change.

This removes rule 3.2 of `ux-rules.md`, which required shipping both
renderers behind one command.

**Rule 6.2. IMPLEMENTED 2026-08-19.** Ship transcript mode before the
cockpit becomes the default. It is the replacement for terminal find, so
shipping the cockpit first removes a capability with nothing in its
place. Bind it to `Ctrl-O`, which is already reserved for the pager in
`keymap.go`. Keys follow `less`: `/` search, `n` and `N` for matches, `g`
and `G` for the ends, `j` and `k` for one row, `Ctrl-U` and `Ctrl-D` for
a half page, `q` or `Esc` to leave.
Implementation: the pager at `internal/ui/screen/transcript`, pushed onto
the router stack by Ctrl-O; the `ContextPager` table in
`internal/uikit/keymap`; the integration test at
`cmd/mivia-ui/teatest_transcript_test.go`. The pager is live under streaming:
blocks arriving while it is open appear without reopening. Search is case-insensitive
substring, reports the match count, highlights every visible match, and
Esc from the bar restores the scroll position the search started from.
The pager also binds `{` and `}` to jump between user prompts, and the
wheel scrolls it.

**Rule 6.3. IMPLEMENTED 2026-08-19.** Transcript mode must offer a key
that writes the whole conversation into the terminal's native scrollback,
tool output expanded. This is the single most important mitigation. It
hands the session back to `grep`, `tmux` copy-mode, and the terminal's
own find, so the cockpit borrows the surface rather than taking it.
Claude Code binds `[` for this and `v` to open the transcript in
`$EDITOR`. Take both.
Implementation: `[` returns `tea.Exec` with an in-process `ExecCommand`
writer. Bubble Tea handles `execMsg` synchronously: it releases the
terminal - flushing the renderer, which writes the alternate-screen exit
- before the command runs, so the dump always lands in the primary
screen's scrollback. `tea.Println` cannot give this ordering: its
`insertAbove` bypasses the render queue, and it is a documented no-op
while the alternate screen is active. The integration test proves the
byte ordering: the dump appears inside the inline window between the
alternate-screen exit and the re-entry. The handover lasts until any key
returns the user to the cockpit. `v` writes the dump to a file created
by `os.CreateTemp` (mode 0600) and opens `$VISUAL` or `$EDITOR` through
`tea.ExecProcess`.

**Rule 6.4. IMPLEMENTED 2026-08-19.** Never enter the cockpit in
screen-reader mode. An alternate screen with a virtualized viewport is
unreadable to a screen reader, and rule 9.1 of `ux-rules.md` already
forbids it.
Implementation: `termprobe.ScreenReader` in `internal/uikit/termprobe`
detects `MIVIA_SCREEN_READER`; `cmd/mivia-ui` prints one line that says
why and renders the plain stream from `internal/ui/stream` instead.
`TERM=dumb` takes the same path (ux-rules rule 9.6).

**Rule 6.5. AMENDED 2026-08-24.** Mouse capture is OFF by default
(ux-rules.md rule 7.1) and `--mouse` is the opt-in. The previous
implementation had capture on with `--no-mouse` as the escape hatch;
that contradicted rule 7.1 and made terminal-native selection
(Cmd-A, click-and-drag, middle-click PRIMARY paste on Linux) silently
broken the moment the cockpit rendered. The opt-in (`--mouse`) keeps
the cockpit and turns on `tea.MouseModeCellMotion` so clicks, drags
and the wheel reach the transcript. The help overlay states the
terminal's own override key (Fn on Terminal.app, Option on iTerm2,
Shift almost everywhere else) so the user can still native-select
under capture; `termprobe.MouseOverrideHint` provides it. Capture is
also released while a `[` handover holds no alternate screen, so the
terminal's own selection can reach the transcript in scrollback.

**Rule 6.6. IMPLEMENTED 2026-08-19.** Wheel speed is not portable. Some
terminals send one event per notch and some amplify. Ship a multiplier in
`config/defaults.go` and a way to change it. A value of 3 matches `vim`.
Implementation: `CockpitScrollLines` is a settable var (default 3,
documented in `internal/uikit/config/defaults.go`); `--scroll-lines`
writes it once at startup.

**Rule 6.7. IMPLEMENTED 2026-08-19.** Scrolling up pauses auto-follow.
Show a jump-to-bottom affordance with a count of what arrived while the
user was reading. Returning to the bottom resumes following. Without this
the view fights the user every time a token streams in.
Implementation: `transcript.Model.NewWhilePaused` counts finished blocks
that arrive while follow is paused and clears when following resumes.
The status row states the count: "N new blocks while you read - ctrl+end
to follow again".

---

## 4. Terminal hazards to design around

These are known failures, not speculation. Each needs a probe or a
documented refusal.

| Hazard | Effect | Response |
|---|---|---|
| `tmux -CC` (iTerm2 integration) | The alternate screen and mouse tracking are broken. Double-click can corrupt the terminal | Detect and refuse the cockpit. IMPLEMENTED: `termprobe.InTmuxControlMode`; `cmd/mivia-ui` prints one line and renders the plain stream |
| tmux at 3.6 or older | No synchronized output, so redraws flicker | Probe for the capability and warn. IMPLEMENTED: `termprobe.OldTmuxWarning` parses `tmux -V`; the warning lands once as a transcript notice |
| Windows Terminal, ConPTY | Positioned writes coalesce wrongly and leave stale cells | A full-repaint mode. IMPLEMENTED: `termprobe.IsConPTY` auto-enables it; `--full-repaint` forces it; the app issues `tea.ClearScreen` on every resize while it is on. The effect on a real terminal is not testable here - the decision and the Cmd are tested |
| iTerm2 default profile | Mouse reporting is off, so the wheel and clicks do nothing | Detect and print a one-time hint. IMPLEMENTED: `termprobe.Probe` warns once as a transcript notice |
| tmux without `mouse on` | Wheel events go to tmux | Detect and print a one-time hint. IMPLEMENTED: inside tmux the probe states the `set -g mouse on` fix once. tmux's own option cannot be read without running tmux, so the hint is worded as a condition, not a claim |

Source: [Claude Code fullscreen rendering](https://code.claude.com/docs/en/fullscreen).

---

## 5. What Bubble Tea v2 already gives us

Every mechanism the cockpit needs is declarative in v2, set on the `tea.View`
this repository already returns. No new dependency is required.

| Need | v2 mechanism |
|---|---|
| The alternate screen | `View.AltScreen` - already used for modals in `app.go` |
| Mouse | `View.MouseMode`, with `tea.MouseModeCellMotion` or `tea.MouseModeAllMotion` |
| Mouse events | `tea.MouseClickMsg`, `tea.MouseReleaseMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg` |
| Cursor placement in the composer | `View.Cursor`, a `tea.Cursor` with `tea.Position` |
| The window title | `View.WindowTitle` |
| Redraw cost | The Cursed Renderer, which diffs cells with the ncurses algorithm |

The Cursed Renderer is why the cockpit is worth building. It sends only the
cells that changed, so a full-surface repaint costs about what a small
update costs.

Sources: [Bubble Tea v2 discussion](https://github.com/charmbracelet/bubbletea/discussions/1374),
[Bubble Tea v2 upgrade guide](https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md).

---

## 6. The correction to `ux-rules.md` section 3

Rule 3.1 said inline is the default and the cockpit is opt-in. Two pieces of
its evidence were wrong.

**Wrong: "Claude Code walked its own default back."** It did not. Version
2.1.132 added an opt-out environment variable. The default then moved
FORWARD: fullscreen is the default for every user who started on or after
2026-05-06. Adding an escape hatch is not a reversal, and reading it as one
inverted the conclusion.

**Incomplete: the reason for the cockpit.** Rule 3.1 weighed portability
against appearance. The real drivers are flicker, memory, and mouse. Memory
is the decisive one for this product, because an agent session is long by
design. No amount of portability argument answers "the session gets slower
the longer you work".

**What stands.** The four lost capabilities in section 3 above are real, and
`ux-rules.md` was right to name them. The error was treating them as
unanswerable rather than as a list of features to build. Rules 6.2 and 6.3
turn them into work.

**What changes.** Rule 3.1 is replaced by rule 6.1. Rule 3.1a, that a
bottom-anchored composer does not need the alternate screen, remains TRUE as
a statement about terminals. It is no longer load-bearing here, because the
inline renderer is gone.

---

## 7. What the cockpit changes in the code

The live-window architecture is INLINE-specific. It exists because the
inline renderer must keep `View()` shorter than the terminal, so it commits
blocks to scrollback when they are evicted.

The cockpit inverts this. It owns the whole surface, so nothing is ever
handed to the terminal, and the transcript must hold every block itself.

| Concern | Inline, removed | Cockpit, shipped |
|---|---|---|
| Where finished blocks live | Terminal scrollback | The transcript model |
| What bounds memory | The terminal | A viewport that styles only the visible rows |
| `MaxTranscriptLines` | A secondary ring for the pager | The primary store, and it states what it dropped |
| Eviction | Commits to scrollback | Does not exist |
| Scrolling | The terminal | The application |
| The status row | No permanent bar | Permanent, and it never reflows the transcript |

Two consequences follow.

First, `Block` values are already the right shape. They re-render at a given
width, so a viewport can draw any slice of them. The Wave 1 decision to
keep values rather than strings is what makes the cockpit cheap to add.

Second, the two renderers must sit behind one interface, and switching must
keep the live session. A switch that drops the conversation is worse than no
switch.

---

## 8. Open questions

These need a decision before implementation, not during it.

1. **Answered.** The store stays bounded by `MaxTranscriptLines`, and the
   transcript COUNTS what it dropped. The status row states the count and
   `Dump` writes it at the head, so truncation is never silent.
2. **Deferred, not refused.** In-app selection is not in the first slice.
   Rule 6.3's write-to-scrollback key gives the terminal's own selection
   back for far less code, and it is the honest first answer.
3. **Answered.** The status row is permanent and always reserved.
4. **Answered 2026-08-19.** Transcript mode is built: `internal/ui/screen/transcript`
   gives `less`-style search and paging under `ContextPager`, and rule 6.3's
   `[` and `v` keys write the conversation to native scrollback or `$EDITOR`.
   The cockpit has in-app search now, so this question is closed.
