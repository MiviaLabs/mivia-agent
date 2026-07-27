# Plan 004: CRT / 8-Bit Welcome Screen

**Status:** Validated implementation plan
**Target files:** `internal/cli/logo.go`, `internal/cli/pixel.go`, `internal/cli/tui_view.go`, `internal/cli/welcome.go`
**Design principles:** terminal-native (braille), zero extra deps, backward-compatible layout, no fake loading

---

## 1. What we have now

The welcome screen renders as:

```
  mivia gpt-4o ──────────── welcome    ← status bar (1 line)

       ⠀⠛⠿⣿⠿⠛                 ← animated braille diamond
       ⠶⠿⣿⣶⠶                     24 frames, ~1.9s loop
       ...                             48×48px hi-res / 28×28px compact

             MIVIA  agent              ← STATIC styled text wordmark (1 line)

   type a message to start · select…   ← tag line

   Sessions                            ← session picker (variable rows)
   ❯ Last session
     Auto · 2h ago

  ┌──────────────────────────────────┐
  │ ❯ _                            │  ← composer textarea
  └──────────────────────────────────┘
    ↑↓ sessions · enter open · ctrl+c  ← hint
```

**Code entry points:**
- `renderWordmark(width)` — `logo.go` → 1 line of bold "MIVIA" + faint "agent" via lipgloss
- `viewWelcome()` — `tui_view.go` → composes status + logo + word + tag + picker + composer
- `renderWelcomeBody()` — `tui_view.go` → vertical budget for session picker
- `diamondAnimFrames()` — `pixel.go` → 24 pre-rendered braille frames
- `rasterDiamond()` — `pixel.go` → L1 diamond in boolean pixelGrid
- `logoTickMsg` / `logoTickCmd` — 80ms tick drives `m.logoFrame++`

---

## 2. Brainstorm evaluation

### ACCEPTED

| Idea | Rationale | Constraints |
|------|-----------|-------------|
| **Dot-matrix "MIVIA" via braille pixel canvas** | Reuses existing pixelGrid/braille infra. Huge aesthetic impact. Fits 8-bit terminal vision without external deps. | Must compute height impact on layout; fall back to text wordmark when terminal too short |
| **Glow-wave animation across letters** | Continuous KITT-scanner brightness sweep across M-I-V-I-A. Pure ANSI color cycling — no pixel changes. Low cost, high polish. | Uses same frame counter as diamond; both animate in sync |
| **"agent" stays styled text** | Below the braille MIVIA as one line. Keeps the subtitle readable at any terminal size, grounds the pixel art in modern UI. | Must fit in vertical budget |

### REJECTED

| Idea | Why not |
|------|---------|
| **CRT bezel (`╔══╗`)** | Loses 2 usable columns of width. Conflicts with clean/minimal design language. Looks cramped on narrow terminals. The app should feel like the workspace, not a window into a machine. |
| **Boot sequence / fake loading** | UX anti-pattern — delays interaction for pure decoration. The app IS ready instantly. "Loading" text with nothing loading is dishonest. On repeat launches it becomes annoying. |
| **Scanline overlay** | Only makes visual sense inside a CRT bezel (which we rejected). Without the bezel, scanlines across full terminal width look like rendering artifacts. Hurts text readability. |
| **Green/amber color themes** | Let the terminal emulator handle color theming. Green phosphor on dark backgrounds is hard to read for session names and guidance text. White-on-dark is accessible and terminal-agnostic. |
| **Version stamp** | Better suited for `--version` flag. On welcome screen it's noise. |
| **VU meter on status bar** | The status bar communicates state — adding audio-visual decoration would be confusing. |

### DEFERRED

| Idea | When |
|------|------|
| Configurable CRT palette themes | If/when a full theming system is added across all UI surfaces |
| Glitch-in logo reveal | A nice-to-have polish pass after the core wordmark animation lands |

---

## 3. Design: dot-matrix wordmark

### 3.1 Letter bitmaps (5×7 pixel)

Each letter is defined as a 5-wide × 7-tall boolean bitmap. At braille resolution
(2×4 subpixels per cell), each letter occupies 3 braille columns × 2 braille rows.

**M** (5×7):
```
██ █
██ ██
█ █ █
█ █ █
█  ██
█   █
█   █
```

**I** (5×7):
```
█████
  █
  █
  █
  █
  █
█████
```

**V** (5×7):
```
█   █
█   █
█   █
█   █
 █ █
 █ █
  █
```

**A** (5×7):
```
  █
 █ █
█   █
█████
█   █
█   █
█   █
```

### 3.2 Layout

```
  ┌──┐┌──┐┌──┐┌──┐┌──┐     ← each letter = 3 braille cols × 2 braille rows
  │M ││I ││V ││I ││A │     1 braille col gap between letters
  └──┘└──┘└──┘└──┘└──┘
              agent          ← styled text, 1 line below
```

Total: 5 × (3+1) − 1 = 19 braille cols wide, 2 braille rows tall, + 1 line for "agent".

### 3.3 Pixel → braille pipeline

```go
// letterBraille returns the pre-rendered braille rune matrix for a letter.
// Using pixelGrid under the hood but caching the result.
func letterBraille(letter rune) [][]rune  // 2 rows × 3 cols of braille runes

// renderWordmarkBraille renders the animated wordmark.
// frame: animation frame index (for glow wave)
// width: horizontal centering target
func renderWordmarkBraille(frame, width int) string
```

The braille rendering for each letter is pre-computed (constant). Per-frame animation
only changes ANSI colors (no pixel re-rasterization).

---

## 4. Design: glow-wave animation

### 4.1 Brightness model

Each of the 5 letters gets a brightness factor (0.3–1.0) per frame:

```go
func letterBrightness(frame, letterIndex int) float64 {
    phase := float64(letterIndex) * 1.256  // ~72° offset per letter
    t := float64(frame) * 0.3              // speed
    return 0.65 + 0.35 * math.Sin(t + phase)
}
```

This creates a continuous wave: at frame 0, M is brightest, I is dimming, V is at minimum, I is rising, A is mid-brightness. The wave sweeps left-to-right continuously.

### 4.2 Brightness → ANSI color

| Brightness | ANSI 256 | Effect |
|-----------|----------|--------|
| 0.85–1.0  | 15       | Bright white (peak glow) |
| 0.65–0.85 | 250      | Light gray |
| 0.45–0.65 | 244      | Mid gray |
| < 0.45    | 236      | Dim (phosphor decay) |

Each letter's braille cells are colored individually using `lipgloss.NewStyle().Foreground(lipgloss.Color(ansiN))`.

### 4.3 Integration with existing frame loop

The existing `logoTickMsg` at 80ms advances `m.logoFrame`. Both `diamondAnimFrames()` and the new wordmark animation read from the same counter:

```go
// In viewWelcome():
logo := renderLogoFrameColor(m.logoFrame, w, brandColorWelcome)
word := renderWordmarkBraille(m.logoFrame, w)
```

---

## 5. Layout integration

### 5.1 Thresholds

| Terminal height | Wordmark variant |
|----------------|-----------------|
| h ≥ 28 AND w ≥ 60 | Dot-matrix braille MIVIA + glow wave animation + styled "agent" |
| 24 ≤ h < 28 | Dot-matrix braille MIVIA, static (no glow), + styled "agent" |
| h < 24 | Styled text wordmark (current `renderWordmark()`) |

The threshold values are validated against the actual diamond logo height (12 braille rows for hi-res, 7 for compact).

### 5.2 Vertical budget update

`renderWelcomeBody()` currently computes:

```go
fixedNoPicker := logoLines + inputLines + 9
```

For the dot-matrix wordmark, the wordmark has 3 lines of content (2 braille + 1 agent + blank separators), replacing the current 1-line wordmark. Updated:

```go
wordLines := strings.Count(word, "\n") + 1
fixedNoPicker := logoLines + wordLines + inputLines + 9
```

The `yBase` for the session picker also needs updating to account for the new wordmark height.

### 5.3 Compact mode

On compact terminals (h < 22), the existing compact diamond (28×28px, 7 braille rows) + text wordmark (`renderWordmark()`) is used. No changes needed.

---

## 6. Files changed

| File | Change |
|------|--------|
| `internal/cli/logo.go` | Add `renderWordmarkBraille(frame, width)`; letter bitmap constants; `letterBraille()` pre-compute; `letterBrightness()` glow function |
| `internal/cli/pixel.go` | Minor: add `rasterLetter(g, letter, x, y, brightness)` helper (optional — could be a separate function) |
| `internal/cli/tui_view.go` | Update `viewWelcome()` to select wordmark variant based on terminal dimensions; update `renderWelcomeBody()` to track word height |
| `internal/cli/welcome_test.go` | Add `TestWordmarkBrailleBraille` (validates braille output, multi-line, MIVIA presence); update existing tests if `renderWordmark` signature changes |
| `internal/cli/logo_dump_test.go` | Extend DUMP_LOGO test to also dump wordmark frames |

---

## 7. What is NOT changing

- **Diamond logo animation** — unchanged. It continues to use 24 pre-rendered frames with the wipe/pulse animation sequence.
- **Compact terminal path** — unchanged. h < 24 still uses the compact diamond + text wordmark.
- **Status bar / brand chrome** — unchanged. The work chrome and idle status are separate systems.
- **Chat view** — unchanged. Wordmark animation is exclusive to the welcome screen.
- **No new dependencies** — all changes use existing `pixelGrid`, lipgloss, and math packages.
- **No new configuration** — no env vars, CLI flags, or config keys for the wordmark aesthetic.
- **No race conditions** — `logoOnce` sync mechanism stays. Wordmark pre-computation can use the same or a separate `sync.Once`.

---

## 8. Test plan

| Test | What it validates |
|------|-------------------|
| `TestWordmarkBrailleBraille` | `renderWordmarkBraille()` output has braille runes, multi-line, contains M,I,V,I,A shapes |
| `TestWordmarkBrailleAnimation` | Consecutive frames differ (animation is alive); glow wave progresses |
| `TestWordmarkFallbackText` | `renderWordmark()` unchanged and still returns "MIVIA" + "agent" |
| `TestWelcomeLayoutHeightBudget` | The picker is allocated the correct number of rows with wordmark in dot-matrix vs text mode |
| `TestGlowBrightnessRange` | `letterBrightness()` stays in [0.3, 1.0]; phase offsets distribute evenly |

---

## 9. Visual reference: what the welcome screen will look like

```
  mivia gpt-4o ──────────── welcome

       ⠀⠛⠿⣿⠿⠛                 ← animated diamond (unchanged)
       ⠶⠿⣿⣶⠶

  ┌──┐┌──┐┌──┐┌──┐┌──┐             ← dot-matrix MIVIA with glow wave
  │M ││I ││V ││I ││A │                each letter cycles brightness
  └──┘└──┘└──┘└──┘└──┘
              agent                    ← styled text (unchanged)

   type a message to start · select…

   Sessions
   ❯ Last session
     Auto · 2h ago

  ┌──────────────────────────────────┐
  │ ❯ _                            │
  └──────────────────────────────────┘
    ↑↓ sessions · enter open · ctrl+c
```

The glow-wave makes the M-I-V-I-A letters appear to "breathe" with a scanning left-to-right
brightness pulse, imitating:
- **KITT scanner** (Knight Rider) — sequential sweep
- **CRT phosphor** — random per-letter brightness variance within a smooth wave
- **Neon tube flicker** — subtle, continuous, never static

---

## 10. Edge cases

| Case | Behavior |
|------|----------|
| Terminal = 80×24 | Hi-res diamond + dot-matrix wordmark + glow (h ≥ threshold) |
| Terminal = 80×20 | Compact diamond + text wordmark (no dot-matrix) |
| Terminal = 40×30 | Dot-matrix wordmark but narrow width collapses layout — text wordmark fallback at w < 60 |
| Terminal with no braille font | Braille renders as blank squares; user sees diamond outline + text wordmark. Acceptable degradation. |
| Terminal resize after launch | `WindowSizeMsg` re-runs `viewWelcome()`; threshold re-evaluated |
| `MIVIA_TERM` in screen/tmux | No change — braille works over SSH/tmux with any monospaced font covering U+2800 |
