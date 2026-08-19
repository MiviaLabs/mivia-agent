# Mivia terminal UI - UX rules

Status: evidence-based rules for the new terminal UI. Derived from external
research on 2026-08-19, not from the existing `internal/cli` TUI.

Scope: these rules govern `internal/ui`, `internal/uikit` and `cmd/mivia-ui`.
They bind implementation choices. A rule that only says "be consistent" is not
a rule and is not listed here.

Every rule carries a source. Where sources conflict, the rule says so. Where a
claim could not be verified, the rule marks it UNVERIFIED.

Section 10 lists what this document overturns in `wireframes-panes.md`. That
file stays the visual specification. This file wins on interaction and
mechanics.

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

Reservation applies to the context that the owner claims. `Ctrl-U`, `Ctrl-E`,
`Ctrl-K` and `Ctrl-R` belong to readline, which owns the line editor. Bind them
outside the composer, or bind them to the same action readline gives them. Do
not bind `Ctrl-S`, `Ctrl-Q`, `Ctrl-C`, `Ctrl-Z`, `Ctrl-\`, `Ctrl-V`
or `Ctrl-M` in any context. The tty or the terminal owns those, and no context
escapes them. `internal/uikit/keymap` enforces the second list mechanically.

**Amended 2026-08-19, with transcript mode.** `Ctrl-D` moved from the
never-bind list to the readline-owned class above: readline uses it as EOF on
an empty line, so it is reserved inside the composer and free outside the line
editor. The pager binds it as half a page down, which is what `less` itself
does, and a pager has no EOF gesture to break. `Ctrl-B` (the GNU screen
prefix) is bound in the pager the same way: screen intercepts it before the
app sees it, which makes the binding inert for screen users, not harmful, and
the pager keeps modifier-free alternates (`b`, `space`). `Ctrl-S` stays
unbound everywhere (rule 1.2).

Sources: [stty(1)](https://man7.org/linux/man-pages/man1/stty.1.html),
[GNU Readline](https://tiswww.case.edu/php/chet/readline/readline.html),
[GNU screen flow control](https://www.gnu.org/software/screen/manual/html_node/Flow-Control-Summary.html),
[copilot-cli #2677](https://github.com/github/copilot-cli/issues/2677).

**Rule 1.1.** Bind actions to `Ctrl-G`, `Ctrl-O`, `Ctrl-T`, function keys, or a
prefix. These are free in practice.

**Rule 1.2.** `Ctrl-S` is radioactive even in raw mode. Raw mode clears `IXON`,
so the application does receive the byte. But `ssh`, `screen`, and any spawned
pager reinstate flow control. Do not bind it.
Source: [GNU screen flow control](https://www.gnu.org/software/screen/manual/html_node/Flow-Control-Summary.html).

**Rule 1.3.** `Ctrl-C` cancels the running turn. A second `Ctrl-C`, at an empty
composer and within a timeout, exits. Print the second step on screen when the
first press lands.
Sources: [opencode #9041](https://github.com/anomalyco/opencode/issues/9041),
[codex #14708](https://github.com/openai/codex/issues/14708),
[claude-code #15161](https://github.com/anthropics/claude-code/issues/15161).

**Rule 1.4.** The footer hint must state the complete truth for the current
state. Do not advertise one cancel key and silently accept another.
Source: [copilot-cli #1422](https://github.com/github/copilot-cli/issues/1422).

---

## 2. Rendering and repaint

**Rule 2.1.** A committed turn is immutable. Only the live tail and the
composer repaint. Re-rendering history crashes NVDA and produces input lag of
up to 10 seconds.
Source: [The text-mode lie](https://xogium.me/the-text-mode-lie-why-modern-tuis-are-a-nightmare-for-accessibility).

**Rule 2.2.** Split the transcript into an append-only committed region and a
small live region. This is Ink's `<Static>` and Ratatui's `Viewport::Inline`.
Sources: [Ink](https://github.com/vadimdemedes/ink),
[Ratatui Viewport](https://docs.rs/ratatui/latest/ratatui/enum.Viewport.html).

**Rule 2.3.** Never full-erase and repaint the whole transcript. That is the
documented cause of the flicker reports against Gemini CLI's default mode.
Source: [gemini-cli #21924](https://github.com/google-gemini/gemini-cli/issues/21924).

**Rule 2.4.** An inline mode must be genuinely append-only. Full-screen redraw
in the primary buffer corrupts scrollback and gives up layout guarantees.
Codex CLI's `--no-alt-screen` does this and carries four open issues.
Source: [codex #20063](https://github.com/openai/codex/issues/20063).

**Rule 2.5.** Cap the repaint rate at 10-20 Hz. Coalesce token deltas.
`indicatif` refreshes at most 20 times a second.
Source: [indicatif](https://docs.rs/indicatif/latest/indicatif/struct.ProgressBar.html).

**Rule 2.6.** Do not hand-roll synchronized output. Bubble Tea v2 enables mode
2026, which prevents tearing, and mode 2027 for wide Unicode, automatically.
The underlying sequences are `CSI ?2026 h` and `CSI ?2026 l`; read them to
understand the mechanism, not to emit them.
Sources: [Bubble Tea v2 release](https://charm.land/blog/v2/),
[synchronized output spec](https://github.com/contour-terminal/vt-extensions/blob/master/synchronized-output.md).

**Rule 2.7.** Reserve fixed height for transient chrome. A warning that appears
and disappears must occupy its row when hidden. A one-line change reflows every
wrapped line above it and destroys both reading position and any selection.
Source: [gemini-cli PR #22584](https://github.com/google-gemini/gemini-cli/pull/22584).

**Rule 2.8.** Never move the composer while output streams.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

---

## 3. Inline against cockpit

The field disagrees. State the disagreement rather than hiding it.

Frontier agent CLIs moved to the alternate screen. Claude Code made fullscreen
the default for new users on 2026-05-06. Codex ships alt-screen by default.
opencode and crush started there.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen),
[codex PR #8555](https://github.com/openai/codex/pull/8555).

Terminal fundamentals point the other way. The alternate screen removes four
capabilities the user already had.

1. The session transcript never enters scrollback.
2. `tmux` copy-mode has nothing to show.
3. The terminal's own find cannot search it.
4. Selection across the whole session is impossible.

Sources: [xterm ctlseqs](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html),
[tmux scrollback](https://www.freecodecamp.org/news/tmux-in-practice-scrollback-buffer-47d5ffa71c93/),
[claude-code #67289](https://github.com/anthropics/claude-code/issues/67289).

**Rule 3.1. SUPERSEDED on 2026-08-19 by rule 6.1 of
[cockpit-research.md](cockpit-research.md).** The cockpit is the default and
inline is the opt-out. Read that file, not this rule.

The original rule said the opposite: "Inline is the default. The cockpit is
opt-in." Two pieces of its evidence below are wrong, and section 6 of the
cockpit research states why. The lost capabilities it names are real, and
they became the mitigation list rather than a reason to refuse.

Two further pieces of evidence support this ordering.

**This paragraph was wrong.** It said Claude Code "walked its own default
back". Version 2.1.132 added an opt-out environment variable, which is not a
reversal. The default then moved forward: fullscreen renders by default for
every user who started on or after 2026-05-06.
Source: [Claude Code fullscreen rendering](https://code.claude.com/docs/en/fullscreen).

Charm states that Bubble Tea "supports inline mode as a first-class use case".
Source: [Bubble Tea v2 release](https://charm.land/blog/v2/).

**Rule 3.1a.** A composer pinned to the bottom does not require the alternate
screen. Bubble Tea's inline mode already holds the managed frame at the bottom
while `tea.Println` flushes committed content above it. OpenTUI ships the same
shape as a named mode, `split-footer`, with `externalOutputMode:
"capture-stdout"`. Build the cockpit feel this way before reaching for
alt-screen.
Source: [OpenTUI renderer](https://opentui.com/docs/core-concepts/renderer/).

**Rule 3.2. REMOVED on 2026-08-19.** It required shipping both renderers
behind one command. There is only one interactive renderer now, so there is
nothing to switch between. See rule 6.1 of
[cockpit-research.md](cockpit-research.md).
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

**Rule 3.3.** A cockpit owes the user a one-key path that writes the whole
conversation into native scrollback. A file export does not restore terminal
find, `tmux` copy-mode, or selection.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

**Rule 3.4.** Detect multiplexers at startup and change the default. Alternate
screen buffers have no scrollback, and Zellij enforces that.
Source: [codex PR #8555](https://github.com/openai/codex/pull/8555).

**Rule 3.5.** Do not enable the cockpit and mouse capture without shipping the
copy path in the same change. Copilot CLI did, broke macOS `Cmd-C`, and shipped
`Ctrl-Insert` as the fix. Mac keyboards have no `Insert` key.
Source: [copilot-cli #1585](https://github.com/github/copilot-cli/issues/1585).

---

## 4. Composer input

**Rule 4.1.** `Enter` submits, always.
Source: [Claude Code terminal config](https://code.claude.com/docs/en/terminal-config).

**Rule 4.2.** `Ctrl-J` is the primary newline, not a fallback. `Ctrl-J` is line
feed, `0x0A`. It works in every terminal with no setup and no negotiation.
Source: [terminal keyboard protocol survey](https://blog.fsck.com/agent-blog/2026/02/26/terminal-keyboard-protocol/).

**Rule 4.3.** Backslash then `Enter` is the typeable newline escape hatch. It
needs no key detection and survives `screen` and mosh.
Source: [Claude Code terminal config](https://code.claude.com/docs/en/terminal-config).

**Rule 4.4.** `Shift-Enter` is a bonus. Enable it only after querying the Kitty
keyboard protocol with `CSI ? u`. Push flag `0b1`. Pop it on exit, including on
panic. Never push flag `0b1000`: it stops `Ctrl-C` generating SIGINT.
Source: [kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/).

**Rule 4.5.** A failed `Shift-Enter` must never print into the composer. The
common symptom is a literal `OM`. Filter unknown SS3 and CSI sequences.
Sources: [claude-code #9321](https://github.com/anthropics/claude-code/issues/9321),
[#32090](https://github.com/anthropics/claude-code/issues/32090).

**Rule 4.6.** Do not document `Alt-Enter` as primary. macOS does not send
Option as a modifier until the user enables "Use Option as Meta Key".
Source: [Claude Code terminal config](https://code.claude.com/docs/en/terminal-config).

---

## 5. Slash commands and mentions

**Rule 5.1.** Open the command menu only when the buffer's first non-whitespace
character is `/`, the cursor sits in that leading token, and the keystroke came
from typing. This makes `src/foo` structurally incapable of triggering it.
Source: [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode).

**Rule 5.2.** A `/` typed mid-sentence must not open the menu. Claude Code
crashed on this and the report links five prior regressions of the same family.
Source: [claude-code #25477](https://github.com/anthropics/claude-code/issues/25477).

**Rule 5.3.** Check menu state before submit, from one flag. `Enter` accepts
the highlighted row when the menu is open, and does not also submit. The
"accepts and also submits" bug has three issue numbers across two products.
Source: [claude-code #25353](https://github.com/anthropics/claude-code/issues/25353).

**Rule 5.4.** `Esc` closes the menu and nothing else on the first press. The
buffer keeps its text. A second `Esc` falls through to the application.
Source: [ARIA combobox pattern](https://www.w3.org/WAI/ARIA/apg/patterns/combobox/).

**Rule 5.5.** Arrow keys move the highlight only while the menu is open.
Otherwise they belong to history. Gate the menu on a typed-keystroke flag, not
on buffer contents, or history recall traps the arrows.
Source: [claude-code #56923](https://github.com/anthropics/claude-code/issues/56923).

**Rule 5.6.** Do not auto-descend into subcommands on an exact match. Do not
append a trailing space after one. `/stats` plus `Enter` ran `/stats session`.
Source: [gemini-cli PR #20136](https://github.com/google-gemini/gemini-cli/pull/20136).

**Rule 5.7.** Score every candidate. Cap only the rendered rows. Render at
least 6 rows with a scrolling window. opencode capped candidates before
rendering and made later matches unreachable. The row cap is
`config.MaxCompletionRows`, which `wireframes-panes.md` section 10 sets to 6.
Source: [opencode #17027](https://github.com/anomalyco/opencode/issues/17027).

**Rule 5.8.** Use fzf-style scored fuzzy matching with word-boundary,
path-separator and camelCase bonuses. Sort exact name matches first.
Source: [fzf matching](https://deepwiki.com/junegunn/fzf/2.2-fuzzy-matching-algorithm).

**Rule 5.9.** Trigger `@` at any token start, anywhere in the line. With no
match, leave the `@` as literal text and never block submission.
Source: [Gemini CLI commands](https://google-gemini.github.io/gemini-cli/docs/cli/commands.html).

**Rule 5.10.** Ship one sigil. `@` covers every workspace entity. Disambiguate
by a type badge on the row, not by a second sigil. `#` and `>` carry no
cross-product meaning.
Source: [Cursor @-symbols](https://cursor.com/docs/context/@-symbols).

**Rule 5.11.** Exclude git-ignored paths from mention candidates. This is a
relevance rule and a secrets rule; it keeps `.env` out.
Source: [Gemini CLI commands](https://google-gemini.github.io/gemini-cli/docs/cli/commands.html).

**Rule 5.12.** Build the candidate index once and update it incrementally.
Never rebuild per keystroke. Never walk outside the workspace. opencode scanned
`$HOME` at startup and hit 1000% CPU for ten minutes.
Sources: [gemini-cli #7928](https://github.com/google-gemini/gemini-cli/issues/7928),
[opencode #6741](https://github.com/anomalyco/opencode/issues/6741).

**Rule 5.13.** Repaint the menu within 100 ms of the keystroke. That is the
limit for the user to feel their action caused it.
Source: [NN/g response limits](https://www.nngroup.com/articles/response-times-3-important-limits/).

---

## 6. Focus

**Rule 6.1.** Do not use `Tab` to move focus while a completion menu can be
open. Resolve it as: `Tab` completes when the menu is open, and moves focus
only when it is closed.
Source: [mui #20904](https://github.com/mui/material-ui/issues/20904).

**Rule 6.2.** Single-key actions are unavailable while the composer holds
focus. Route them through an explicit focus change or a command palette. A
text-first application cannot bind bare letters globally.

**Rule 6.3.** Never signal focus by colour alone. WCAG 1.4.1 requires the
information without colour perception.
Source: [W3C SC 1.4.1](https://www.w3.org/WAI/WCAG21/Understanding/use-of-color.html).

**Rule 6.4.** Indicate focus with a gutter marker plus reverse video. Reverse
video inherits the theme's own contrast, so it survives any palette. Bold alone
is unreliable: many terminals render bold as a brighter colour, which degrades
to a colour-only cue.
Source: [W3C SC 2.4.13](https://www.w3.org/WAI/WCAG22/Understanding/focus-appearance.html).

**Rule 6.5.** Focusing an older item turns auto-follow off. Show that it is
off. Anchor focus to a stable item identity, not a line offset, so content
arriving above does not move the selection. k9s has five open issues from
getting this wrong.
Sources: [k9s #444](https://github.com/derailed/k9s/issues/444),
[k9s #155](https://github.com/derailed/k9s/issues/155).

**Rule 6.6.** Keep a paused viewport paused when the turn finishes. The finish
event is when a naive implementation yanks the user away.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

**Rule 6.7.** Show a jump-to-latest affordance with a count while paused.
Approval prompts override the pause and scroll into view, or the agent appears
to hang.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

---

## 7. Mouse

**Rule 7.1.** Mouse is off by default. An agent CLI's output is prose, code,
diffs and paths. That text exists to be copied. Mouse capture removes exactly
that. k9s defaults `enableMouse` to false for the same reason.
Sources: [k9s config](https://k9scli.io/topics/config/),
[Bubble Tea #162](https://github.com/charmbracelet/bubbletea/issues/162).

**Rule 7.2.** Ship `mouse: off` before shipping any mouse feature. Its absence
is logged as a regression across three independent tools.
Source: [lazygit #602](https://github.com/jesseduffield/lazygit/issues/602).

**Rule 7.3.** Use SGR mode 1006 only. Legacy encodings cap coordinates at
column 223. Terminals routinely exceed that.
Source: [xterm ctlseqs](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html).

**Rule 7.4.** Enable the narrowest mode that satisfies the feature. Prefer
alternate-scroll, `CSI ?1007h`, for wheel-only. Do not reach for mode 1003.
Source: [xterm ctlseqs](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html).

**Rule 7.5.** Never tell the user to hold Shift. The modifier is `Option` in
iTerm2, `Fn` in Terminal.app, and absent in xterm.js, code-server and VS Code
web. Show the correct key for the detected terminal, or show none.
Sources: [iTerm2 docs](https://iterm2.com/3.2/documentation-general-usage.html),
[claude-code #74320](https://github.com/anthropics/claude-code/issues/74320).

**Rule 7.6.** Mouse capture also steals middle-click PRIMARY paste on Linux.
Treat that as a first-class regression, not an edge case.
Source: [claude-code #66957](https://github.com/anthropics/claude-code/issues/66957).

**Rule 7.7.** Disable mouse tracking on exit and on panic. Otherwise the user's
terminal stays broken.
Source: [lazygit #1764](https://github.com/jesseduffield/lazygit/issues/1764).

---

## 8. Clipboard

**Rule 8.1.** OSC 52 is a supplement, never the primary clipboard path. Prefer
the platform tool. Use the `tmux` buffer inside `tmux`. Fall back to OSC 52
over SSH only.
Source: [Claude Code fullscreen](https://code.claude.com/docs/en/fullscreen).

**Rule 8.1a.** Bubble Tea v2 provides OSC 52 directly: `tea.SetClipboard`,
`SetPrimaryClipboard`, `ReadClipboard` and `ClipboardMsg`. Use those for the
fallback path rather than writing escape sequences. Note the counter-example:
Charm's own Crush uses the local OS clipboard instead, and carries WSL,
Wayland and remote copy complaints.
Sources: [Bubble Tea v2 release](https://charm.land/blog/v2/),
[crush #661](https://github.com/charmbracelet/crush/issues/661).

**Rule 8.2.** OSC 52 is absent on VTE, which is the GNOME Terminal family, and
on macOS Terminal.app. Those are the two most common defaults on Linux desktop
and macOS.
Sources: [VTE issue 2495](https://gitlab.gnome.org/GNOME/vte/-/issues/2495),
[can-i-use-terminal](https://can-i-use-terminal.github.io/features/osc52copy.html).

**Rule 8.3.** OSC 52 payload limits fail silently. xterm caps decoded content
at 100,000 bytes. VTE drops oversize sequences with no error. Base64 inflates
the payload by a third.
Source: [OSC 52 write](https://vtdn.dev/docs/osc/osc52-write/).

**Rule 8.4.** Treat clipboard read as unavailable. xterm disallows
`GetSelection` by default; kitty asks; WezTerm defaults reading off. Do not
design a feature that needs it.
Sources: [xterm(1)](https://linux.die.net/man/1/xterm),
[kitty conf](https://sw.kovidgoyal.net/kitty/conf/).

**Rule 8.5.** Suppress OSC 52 inside a multiplexer that already handles it.
Two writers race and corrupt the clipboard.
Source: [opencode #12455](https://github.com/anomalyco/opencode/issues/12455).

**Rule 8.6.** Print which clipboard path was used after every copy. Silent
clipboard failure is a recurring bug class; opencode showed success while the
clipboard stayed empty.
Source: [opencode #17796](https://github.com/anomalyco/opencode/issues/17796).

---

## 9. Accessibility, colour and degradation

**Rule 9.1.** Screen-reader mode always renders plain scrolling text. It never
enters the cockpit. Print an explanation instead of switching. An app-owned
viewport emits nothing a screen reader can follow.
Source: [Claude Code accessibility](https://code.claude.com/docs/en/accessibility).

**Rule 9.2.** Screen-reader mode is a separate render path, not a theme. Remove
box drawing, colour-only cues, and redraws of unchanged content. Prefix every
turn with a searchable label.
Source: [Claude Code accessibility](https://code.claude.com/docs/en/accessibility).

**Rule 9.3.** Reduced motion is a separate setting from screen-reader mode.
Magnifier and colourblind users are not screen-reader users.
Source: [Claude Code accessibility](https://code.claude.com/docs/en/accessibility).

**Rule 9.4.** Suppress colour when `NO_COLOR` is present and not empty,
whatever its value. `NO_COLOR=yes` and `NO_COLOR=0` both disable colour.
`NO_COLOR=` empty does not.
Source: [no-color.org](https://no-color.org/).

**Rule 9.5.** `NO_COLOR` disables colour, not text decoration. Bold, faint and
underline survive.
Source: [no-color.org](https://no-color.org/).

**Rule 9.6.** Treat `TERM=dumb` as a hard signal to take the non-TTY path. A
dumb terminal has no cursor addressing, so repaint-in-place is invalid.
Source: [clig.dev](https://clig.dev/).

**Rule 9.7.** Gate every terminal feature on stdout and stdin both being TTYs.
Show no animation when stdout is not a TTY.
Source: [clig.dev](https://clig.dev/).

**Rule 9.8.** Never prompt when stdin is not a TTY. A permission prompt needs a
non-interactive failure mode, never a silent approval.
Source: [clig.dev](https://clig.dev/).

**Rule 9.9.** Restore terminal state on SIGINT, SIGTERM, SIGHUP and panic.
Alternate screen, mouse tracking, raw mode and keyboard flags all leave the
terminal unusable if the process dies without emitting the reset.
Source: [clig.dev](https://clig.dev/).

**Rule 9.10.** Show the active tool or step, not only a spinner. A spinner
cannot distinguish thinking from hung.
Source: [clig.dev](https://clig.dev/).

---

## 10. What this overturns in `wireframes-panes.md`

That file stays the visual specification. These interaction points change.

The keymap rebinds are **applied**: `wireframes-panes.md` section 15 and
`internal/uikit/keymap` now agree, and the table below records only why.
Read the keymap package for the current bindings, never this table.

| Section | Was | Now | Why |
|---|---|---|---|
| 15 | `Ctrl-S` toggles reasoning | `Ctrl-R`, global | `Ctrl-S` is XOFF (rule 1.2) |
| 15 | `Ctrl-W` collapses all | `Ctrl-G` | readline word-rubout; emulator close-tab (section 1) |
| 15 | `Ctrl-E` expands all | Kept, live window only | The composer never holds focus when it acts |
| 15 | `Ctrl-M` opens the model dialog | `Ctrl-P` | `Ctrl-M` is byte-identical to `Enter` |
| 15 | `Tab` focuses blocks | Conditional | Conflicts with completion (rule 6.1) |
| 15 | Bare `y`, `s`, `n`, `N` act | Needs a focus model | Composer holds focus (rule 6.2) |
| 15 | No newline key | `Ctrl-J`, then `\`+`Enter` | Composer is single-line today (section 4) |
| 5 | Blocks freeze in scrollback | Resolved | Blocks freeze on eviction, not on finalize |
| 10 | Slash trigger unstated | Start-anchored | Prevents the `src/foo` trigger (rule 5.1) |
| 10 | `Tab` accepts common prefix | Accepts selection | The menu keeps a highlighted row (rule 6.1) |
| 12 | No mention affordance | `@` at token start | Section 5 |
| 3 | Mouse unstated | Off by default | Rule 7.1 |
| 3 | Inline is the default | Cockpit is the default | Rule 6.1 of [cockpit-research.md](cockpit-research.md) |
| 1 | `Ctrl-D` never bound | Readline-owned, pager binds it | less binds ctrl+d as half a page; a pager is not a line editor (amended 2026-08-19, transcript mode) |

`research-panes.md` stays as the record of the colour and contrast work. Its
sections 7.1 and 7.2, on markdown and diagram rendering, are unverified against
current library availability and are not binding until re-checked.

---

## 11. Confidence

High confidence, from a specification or vendor documentation:

- Every reserved key in section 1.
- xterm mode numbers, SGR 1006, alternate-scroll 1007, alternate screen 1049.
- The `NO_COLOR` wording.
- Kitty keyboard protocol flag semantics, including flag `0b1000` and SIGINT.
- Synchronized output sequences and the query reply.
- Claude Code accessibility behaviours.

Medium confidence:

- The OSC 52 support matrix. Two trackers agree on writing and conflict on
  WezTerm, which resolves as write supported and read gated. Neither publishes
  a per-row verification date.
- Kitty keyboard protocol support on Windows Terminal's stable channel. Sources
  conflict. Treat as UNVERIFIED.

Low confidence, or user reports rather than vendor statements:

- All linked issue trackers. They are strong evidence of what users experience.
  They are not vendor positions.
- The claim that which-key popups improve discoverability. Adoption and
  testimony only; no controlled study found.

Folklore corrected by this research:

1. "Hold Shift to bypass mouse reporting" is false as a portable claim.
2. "OSC 52 solves clipboard over SSH" holds only where every layer supports it
   and the payload is small.
3. "Alternate screen preserves scrollback" conflates two guarantees. It
   preserves what existed before the application started. Everything the
   application prints is destroyed on exit.
