# Cockpit rules

Scope: this file states how cockpit mode works and what it must replace. It
does not restate the block layout, which stays in `wireframes-panes.md`.
Interaction rules that bind the cockpit too - reserved keys, composer,
mouse, clipboard - live in `ux-rules.md`.

---

## 1. What cockpit mode is

The cockpit is the only interactive renderer (rule 6.1). It owns the whole
terminal surface through the alternate screen and draws only the rows that
are visible.

Cost is a function of the window, not of the session. An agent session runs
for hours and produces tens of thousands of lines. A renderer that repaints
against a moving frame slows as the session grows; the cockpit does not,
because it never repaints rows the reader cannot see. Owning the surface
also buys synchronized, flicker-free output and mouse input.

---

## 2. What the alternate screen costs

The alternate screen is a private surface. The terminal swaps it in when
the application starts and out when it exits. Everything the application
prints is destroyed on exit; only what existed before the start survives.
The claim "the alternate screen preserves scrollback" conflates those two
guarantees.

While the cockpit holds the surface:

1. The session transcript never enters scrollback.
2. `tmux` copy-mode has nothing to show.
3. The terminal's own find cannot search the session.
4. Native click-and-drag selection across the session is impossible.

Section 3 maps each lost capability to its replacement. A cockpit that
ships without the replacement ships a regression.

---

## 3. The rules

| Lost capability | Replacement | Rule |
|---|---|---|
| Terminal find (`Cmd-F`, tmux search) | Transcript mode with `less` keys and `/` search | 6.2 |
| The session in native scrollback | A key that writes the transcript into scrollback on demand | 6.3 |
| Native click-and-drag selection | In-app drag-select that copies on release, plus an override key for native selection | 6.5 |
| Native wheel scrolling | In-app scrolling with a speed multiplier | 6.6 |

**Rule 6.1.** The cockpit is the ONLY interactive renderer. There is no
inline renderer and no flag that selects one. Two interactive renderers
double the surface that every later feature must satisfy. The
non-interactive paths are unaffected: `--output json` gives NDJSON, and a
non-TTY stdout gives the plain stream.

**Rule 6.2.** Transcript mode ships with the cockpit. It is the
replacement for terminal find, so a cockpit without it removes a
capability with nothing in its place. `Ctrl-O` opens it; the pager is
`internal/ui/screen/transcript`, pushed onto the router stack, and its
keys live in the `ContextPager` table of `internal/uikit/keymap`. Keys
follow `less`: `/` search, `n` and `N` for matches, `g` and `G` for the
ends, `j` and `k` for one row, `Ctrl-U` and `Ctrl-D` for a half page,
`Ctrl-B`/`b` and `Ctrl-F`/`space` for a full page, `{` and `}` to jump
between user prompts, the wheel to scroll, and `q`, `Esc` or `Ctrl-O` to
leave. The pager is live under streaming: blocks arriving while it is open
appear without reopening. Search is case-insensitive substring search; it
reports the match count, highlights every visible match, and `Esc` from
the bar restores the scroll position the search started from.

**Rule 6.3.** Transcript mode offers a key that writes the whole
conversation into the terminal's native scrollback, every block expanded.
This is the single most important mitigation: it hands the session back to
`grep`, `tmux` copy-mode, and the terminal's own find, so the cockpit
borrows the surface rather than taking it. `[` runs the handover: it
returns a `tea.Exec` command with an in-process writer. Bubble Tea
releases the terminal before the command runs - flushing the renderer,
which writes the alternate-screen exit - so the dump always lands in the
primary screen's scrollback. `tea.Println` cannot give this ordering: its
`insertAbove` bypasses the render queue, and it is a documented no-op
while the alternate screen is active. The handover lasts until any key
returns the user to the cockpit. `v` writes the dump to a temp file (mode
0600) and opens `$VISUAL` or `$EDITOR` through `tea.ExecProcess`. Mouse
capture is released while the handover holds, so the terminal's own
selection can reach the transcript in scrollback.

**Rule 6.4.** Never enter the cockpit in screen-reader mode. An alternate
screen with a virtualized viewport is unreadable to a screen reader, and
rule 9.1 of `ux-rules.md` forbids it. `termprobe.ScreenReader` in
`internal/uikit/termprobe` detects `MIVIA_SCREEN_READER`; on a hit the
plain stream from `internal/ui/stream` renders instead, with one line that
says why. `TERM=dumb` (`termprobe.DumbTerminal`) takes the same path
(rule 9.6 of `ux-rules.md`).

**Rule 6.5.** Mouse capture is ON by default: the cockpit's own
drag-select and wheel scrolling work from the first frame, and drag-select
copies through OSC 52 with a status-line toast. Capture resolves at
startup as `MIVIA_MOUSE` env > `[tui] mouse` config > default true
(`internal/newtui` `mouseEnabled`). Settings → General "mouse capture"
takes effect live: it sends `app.MouseCaptureMsg`, which flips
`View().MouseMode`, and the renderer writes `?1002`/`?1006` on or off.
The help overlay names the detected terminal's own override key for native
selection under capture (`termprobe.MouseOverrideHint`). Capture is also
released while a `[` handover holds the surface (rule 6.3).

**Rule 6.6.** Wheel speed is not portable. Some terminals send one event
per notch and some amplify. The wheel scrolls `CockpitScrollLines` rows
per event - a settable var in `internal/uikit/config/defaults.go`, default
3 to match `vim` - and `[tui] scroll_lines` is the config key for it.
While an approval prompt is active the wheel scrolls the diff preview,
because the prompt is modal; a panel dialog scrolls its own content.

**Rule 6.7.** Scrolling up pauses auto-follow, and returning to the bottom
resumes it. While paused, the status row states the count and the
affordance: "N new blocks while you read - ctrl+end to follow again"
(`NewWhilePaused` counts finished blocks that arrive while follow is
paused and clears when following resumes). Without this the view fights
the user every time a token streams in. Approval prompts override the
pause and scroll into view.

---

## 4. Terminal hazards

These are known failures, not speculation. Each has a probe or a
documented refusal in `internal/uikit/termprobe`.

| Hazard | Effect | Response |
|---|---|---|
| `tmux -CC` (iTerm2 integration) | The alternate screen and mouse tracking are broken. Double-click can corrupt the terminal | `termprobe.InTmuxControlMode` detects it; `Probe` sets `Report.RefuseReason`, and the caller must not enter the cockpit - it says why on one line and renders the plain stream |
| tmux at 3.6 or older | No synchronized output, so redraws flicker | `termprobe.OldTmuxWarning` parses the `tmux -V` output and adds a warning to `Report.Warnings` when the version is 3.6 or older |
| Windows Terminal, ConPTY | Positioned writes coalesce wrongly and leave stale cells | Full-repaint mode: `termprobe.IsConPTY` sets `Report.FullRepaint`, the router enables it, and a resize under full-repaint issues `tea.ClearScreen` |
| iTerm2 default profile | Mouse reporting is off, so the wheel and clicks do nothing | `Probe` adds a warning to `Report.Warnings` |
| tmux without `mouse on` | Wheel events go to tmux | `Probe` adds a warning to `Report.Warnings`, worded as a condition: tmux's own option cannot be read without running tmux, so the hint says "if the wheel does not scroll" rather than claiming the state |

Every row of this table has a probe in `internal/uikit/termprobe`, and the
package doc cites this section. `Report.Warnings` and `Report.RefuseReason`
are the surfaces that carry the responses to the caller.

---

## 5. Renderer mechanisms

Every mechanism the cockpit needs is declarative on the `tea.View` the
router already returns (`internal/ui/app` `Model.View`). No new dependency
is required.

| Need | Mechanism |
|---|---|
| The alternate screen | `View.AltScreen`, set per frame from the top screen's `ViewFlags`, so the mode cannot drift from what was drawn |
| Mouse | `View.MouseMode`; the app requests `tea.MouseModeCellMotion`, which reports clicks, drags and the wheel |
| Mouse events | `tea.MouseClickMsg`, `tea.MouseReleaseMsg`, `tea.MouseWheelMsg`, `tea.MouseMotionMsg` |
| Redraw cost | The Cursed Renderer, which diffs cells with the ncurses algorithm and sends only the cells that changed |

`MouseModeCellMotion`, not `AllMotion`: motion over the surface adds an
event for every cursor movement, and that traffic buys nothing here. The
Cursed Renderer is why the cockpit is cheap to repaint: a full-surface
update costs about what a small update costs.

---

## 6. Ownership boundaries

The cockpit owns the session. Nothing is handed to the terminal until the
user asks for it with `[` or `v`:

- Finished blocks live in the transcript model, not in terminal
  scrollback.
- Memory is bounded by a viewport that styles only the visible rows, not
  by the terminal.
- Scrolling is the application's job; the terminal has no scrollback of
  its own to offer.
- The status row is permanent, always reserved, and never reflows the
  transcript (rule 2.7 of `ux-rules.md`).
- `Block` values re-render at a given width, which is what lets a viewport
  draw any slice of the conversation cheaply.

---

## 7. Storage

The transcript stays bounded by `MaxTranscriptLines` (2000 blocks).
`Model.trim` evicts blocks over the bound and adds every evicted block to
a running dropped count. The count is not decoration: a transcript that
silently forgets its own start is a transcript that lies, and the user has
no way to tell a short session from a truncated one.

---

## 8. Truncation is never silent

The dropped count of section 7 reaches the reader on two surfaces:

- The status row states it while the session runs.
- The `[` scrollback dump and the `v` editor file open with it at the head
  (`[N earlier blocks dropped from this transcript]`), so an exported copy
  is honest too.

When eviction moves content, the viewport re-anchors to the same CONTENT,
not to a row arithmetic guess: it notes which block sits at the top of the
viewport and how far into it, then puts the viewport back on that block
after the survivors have been laid out again.
