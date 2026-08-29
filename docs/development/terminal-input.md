# Terminal input verification

What automated tests cannot prove about the TUI input layer, and how to check it
by hand.

The bindings themselves are **not** listed here. `internal/cli/keymap.go` is the
single declaration, and `/help` renders from it - a second list in a document is
the exact drift this layer was rebuilt to remove.

## What is already covered automatically

Do not re-test these by hand; they fail the build if they break.

| Layer | Covers |
|---|---|
| Unit (`internal/cli`) | Which pane a key reaches per focus, editing keys, paste refocus, copy acknowledgement truthfulness, registry validation, generated help |
| PTY (`*_pty_test.go`, Linux) | Real CSI bytes for End/PgUp, SGR mouse wheel, bracketed paste (multi-line, >256-byte reassembly, no send from embedded newlines) |

## What needs a human, and why

Terminals disagree about what bytes they send, whether they honour OSC 52, and
what they intercept before the app sees it. None of that is observable from a
Linux PTY in CI.

Run `mivia chat` and work down the list. Record pass/fail per terminal.

### Keys

1. **Word motion** - type `hello world`, press `ctrl+←` then `ctrl+→`. Cursor
   moves by words. (Bound alongside `alt+←/→`; some terminals send only one.)
2. **Line motion** - `home` and `end` move within the line while composing.
3. **Transcript extremes while composing** - with text in the composer,
   `shift+home` / `shift+end` scroll the transcript and leave the draft intact.
   **Known to fail in GNOME Terminal/VTE and Konsole**, which bind both to their
   own scrollback and never forward them. Fall back to `tab`, then `home`/`end`.
4. **Empty-draft `end`** - with an empty composer, `end` jumps to the latest
   message.
5. **F2 select mode** - toggles, and the hint line says so. Some multiplexers
   remap function keys; `/select` must work as the fallback.
6. **`ctrl+c`** - mid-turn cancels; at rest with a message selected it copies;
   with a draft it clears the draft and prompts; twice in a row quits.

### Paste

7. **Terminal paste** (`ctrl+shift+v` or middle-click) of a 200-line block:
   arrives as one message, fires no sends, and does not visibly stall.
   *VS Code's integrated terminal is known upstream to drop characters on very
   large pastes - reproduce there before blaming mivia.*
8. **`ctrl+v`** - pastes, or says why it could not (no clipboard tool).
9. **Paste after clicking a message** - lands in the composer, does not vanish.

### Mouse and selection

10. **Shift+drag** selects text without leaving mouse capture. **iTerm2 uses
    Option, not Shift.**
11. **Wheel** scrolls the transcript; **left-drag** selects text in the
    transcript, composer, and pager, and releases to an OSC 52 copy.
12. **`MIVIA_MOUSE=0`** starts with capture off and native selection working.
    `[tui] mouse = false` is the config form; Settings → General toggles it live.

### Modal dialogs

13. Open `/help`, `/status`, `/tools`, `/sessions`, `/model`, block detail, and fleet
    detail in a normal terminal. The chat frame remains legible behind a
    centered panel, including after resizing from wide to narrow and back.
14. In `/model`, providers are grouped, the full provider/model selection marker
    is correct, missing-credential rows are visibly disabled, and clicking a row
    moves the cursor while Enter commits. Esc/q cancels; a failed switch leaves
    the dialog and existing binding unchanged. While any modal is open, wheel scrolls only its pager or session cursor;
    left/middle/right clicks, motion, and release do not select transcript text,
    copy a block, focus the composer, or scroll the transcript. A paste that was
    already in flight is swallowed while the modal owns the screen.
15. Close with `esc`, render once, and right-click the old transcript area. It
    must not activate a stale hit zone. Reopen status/fleet to refresh their
    captured-at-open content.

### Clipboard delivery

16. **Local**: copy a message, paste into another application. Every release
    batches three things: the OSC 52 write, a best-effort local clipboard-tool
    fallback (`internal/uikit/clipboardwrite`: `wl-copy`/`xclip`/`xsel` on
    Linux, `pbcopy` on macOS, `clip.exe` on Windows/WSL - whichever is on PATH
    for the detected display), and the status-line toast. No configuration is
    required for either path.
17. **Over SSH** (no local display, so the clipboard-tool fallback is a
    no-op): copy relies on OSC 52 alone. Expect it to work in kitty,
    alacritty, foot, WezTerm, iTerm2, Windows Terminal, and Ghostty;
    **GNOME Terminal and other VTE terminals refuse OSC 52 outright** (an
    upstream VTE decision, not a mivia gap) - over SSH into a VTE-hosted
    session there is no working copy path at all.
18. **Inside tmux**: `set-clipboard` defaults to `external` in tmux 2.6+,
    which already forwards a bare OSC 52 write to the outer terminal with no
    configuration - do not wrap the sequence in tmux's DCS passthrough
    envelope (`allow-passthrough` defaults *off*, so a wrapped sequence is
    silently dropped in the common case; this bit Neovim and other tools
    that "helpfully" wrap for tmux). If a user's `tmux.conf` sets
    `set-clipboard off` explicitly, tmux drops the sequence outright and no
    escape sequence can override that from inside the pane - the clipboard-tool
    fallback in item 16 still runs locally regardless, since it never goes
    through tmux's OSC 52 interception at all.

## Terminal matrix

Cover at least one from each family; the failure modes cluster by engine.

| Terminal | Notes |
|---|---|
| kitty, foot, Ghostty | Kitty keyboard protocol; OSC 52 supported |
| Alacritty, WezTerm | xterm-conformant; OSC 52 supported |
| GNOME Terminal (VTE) | **No OSC 52** (refused on principle, unresolved since 2018); falls back to xclip/wl-copy locally; intercepts shift+home/end |
| Konsole | OSC 52 write support landed in 2024 - update if copy still fails; also intercepts shift+home/end |
| iTerm2 | **Option**, not Shift, bypasses mouse capture |
| Windows Terminal, VS Code terminal | Large-paste character loss reported upstream in VS Code |
| xterm | Baseline encodings |
| tmux over any of the above | `set-clipboard` defaults to `external` (forwards with no config); do not rely on DCS passthrough wrapping - `allow-passthrough` defaults off |

## Known limitations

- **SS3 `home`/`end`** (`ESC OH` / `ESC OF`, sent by terminals in application
  cursor mode) are not decoded by bubbletea v1.3.10 - it accepts `CSI H/F`,
  `CSI 1~/4~` and `CSI 7~/8~` only. Upgrading to bubbletea v2 (which also brings
  the kitty keyboard protocol and native OSC 52) is the fix; there is no
  app-side workaround.
- **Shift+Enter** cannot be distinguished from Enter under bubbletea v1. Use
  `alt+enter` for a newline.
- An OSC 52 write can in principle interleave with a rendered frame; the write is
  a single call to keep the window minimal. See `tea.SetClipboard` usage in
  `internal/ui/app/mouse_router.go`.
