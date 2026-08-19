# Mivia terminal UI - research, variant D "Panes"

Status: Phase 0, iteration 2. Additive to `research.md`, which stays valid.
Date of the fetches: 2026-08-19.
This file records what iteration 1 left unverified, plus the evidence for variant D.

Same rule as v1: every claim carries a URL, or says plainly that it is judgement or
measurement rather than a fetched fact.

---

## 1. What changed from iteration 1

Review chose a middle ground between variant A and variant B: keep collapsible blocks
and real dialogs, drop the vertical gutter line beside message text. That is variant D,
specified in `wireframes-panes.md`.

The choice is consistent with the v1 evidence rather than a reversal of it. v1 scored B
down on two mechanical grounds - the gutter costs 2 of 80 columns at every level, and a
vertical rule beside already-scrolled text cannot be repainted inline. Both objections
are about the **rule**, not about collapsibility. Removing the rule and keeping the
collapse resolves them. v1 also scored A worst on "survives very long tool output" (2 of
5); collapsible blocks are the direct fix for that, which is why D is a better answer
than A alone.

D adds one thing neither A nor B had: **dialogs with a frame**. This does not contradict
the "no heavy box-drawing" constraint. That constraint targets box-drawing as a layout
device for streaming content. A dialog is drawn once, never streams, lives on the
alternate screen, and is not part of scrollback. See `wireframes-panes.md` section 1.1.

---

## 2. Bubble Tea v2 API - verified, not remembered

v1 stated the alt-screen change from the upgrade guide only. Iteration 2 checked the
package documentation directly.

Source: <https://pkg.go.dev/charm.land/bubbletea/v2> (v2.0.8 at the time of the fetch).

Confirmed:

- `Model` is now `Init() tea.Cmd`, `Update(msg tea.Msg) (tea.Model, tea.Cmd)`, and
  **`View() tea.View`** - `View` no longer returns `string`. This is the change that
  makes alt-screen a per-frame declarative property.
- `View` is a struct with `func NewView(s string) View` and
  `func (v *View) SetContent(s string)`. The docs describe it as declaring UI content
  and "optionally enables terminal features like alt screen mode, mouse tracking,
  cursor position".
- **There is no `WithAltScreen` program option.** The listed options are
  `WithColorProfile`, `WithContext`, `WithEnvironment`, `WithFPS`, `WithFilter`,
  `WithInput`, `WithOutput`, `WithWindowSize`, `WithoutCatchPanics`, `WithoutRenderer`,
  `WithoutSignalHandler`, `WithoutSignals`. This confirms the v1 reading of the upgrade
  guide from a second source.
- Background detection is real and typed: `BackgroundColorMsg`, `ForegroundColorMsg` and
  `CursorColorMsg` each wrap a `color.Color` and expose **`IsDark() bool`** and
  `String()`. The matching requests are `RequestBackgroundColor()`,
  `RequestForegroundColor()`, `RequestWindowSize()`, `RequestCursorPosition()`.
- Cursor is a value, not an escape sequence: `Cursor{Position, Color, Shape, Blink}`
  with `CursorBlock`, `CursorUnderline`, `CursorBar`.

**Design consequences.** `IsDark()` gives the light-or-dark default for free, so the
theme system should select the variant from `BackgroundColorMsg` and let flag, config
and env override it. `WithFPS` is the batching lever for streaming from v1 principle 3,
and it is a program option rather than something to hand-roll on a ticker.

### 2.1 The colour-profile package

Source: <https://pkg.go.dev/github.com/charmbracelet/colorprofile>.

`Profile` constants are `Unknown`, `NoTTY`, `ASCII`, `ANSI` (16), `ANSI256`,
`TrueColor`. `func Detect(output io.Writer, env []string) Profile` respects `NO_COLOR`,
`CLICOLOR` and `CLICOLOR_FORCE`; `TERM=dumb` gives `NoTTY` unless `CLICOLOR_FORCE=1`;
`COLORTERM=truecolor` upgrades to `TrueColor`. `func (p Profile) Convert(c color.Color)
color.Color` downsamples a colour to the profile.

Two consequences, and the first one is important for the whole design:

1. **`NO_COLOR` "disables colors but preserves text decoration".** Bold, dim, reverse
   and underline survive. A design built on typographic hierarchy - weight, dim, bright,
   indentation - therefore keeps almost all of its structure under `NO_COLOR`. That is
   direct support for the build spec's design direction, and it is stronger evidence
   than v1 had.
2. The degradation ladder is a library concern, not something to hand-roll. But see
   finding 8 in `research.md`: `Convert` is a generic downsample, and a generic
   downsample is exactly what breaks the accent at 16 colours. The per-theme 16-colour
   map still has to be authored.

---

## 3. Colour-vision deficiency - measured, not assumed

v1 listed this as unverified (v1 finding 6). It is now measured.

**Method.** Simulate each colour with the Vienot, Brettel and Mollon (1999) LMS
dichromat model: convert sRGB to linear, linear RGB to LMS with the
Hunt-Pointer-Estevez matrix, apply the protanopia, deuteranopia or tritanopia projection
in LMS, convert back. Then measure separation as CIE76 dE in CIELAB between every pair
of status colours after simulation. Threshold: **dE 20**, below which two colours read
as the same hue at terminal text sizes. The threshold is my choice, not a standard, and
a stricter or looser number would move the counts.

**Scope decision.** The set that must stay mutually separable is
`{success, warning, danger, info}`. `accent` is chrome - the prompt marker, the focus
ring, the selected row - and never encodes a status, so it is required to pass contrast
but not to be separable from the status set.

> **Superseded in part by section 8.3.** When this was written the mivia accent was
> amber, and the scope rule was the only reason the mivia themes passed. The accent is
> now achromatic, so it cannot collide with any status hue at all. The rule still holds
> for third-party themes, whose accents do carry hue.

### 3.1 Result: every hand-picked palette collides

Run across 20 palettes, checking 24 contrast pairs and 3 dichromacies each.

| Theme | Contrast pairs passing | CVD collisions |
|---|---|---|
| **mivia-dark** | **24 / 24** | see section 8.4 - now 3, by an explicit vividness trade |
| **mivia-light** | **24 / 24** | **0** after section 8.3 |
| **mivia-high-contrast** | **24 / 24** | **0** after section 8.3 |
| campbell | 24 / 24 | 2 |
| dracula | 24 / 24 | 4 |
| catppuccin-mocha | 24 / 24 | 7 |
| rose-pine | 22 / 24 | 7 |
| gruvbox-dark | 22 / 24 | 9 |
| xterm | 22 / 24 | 2 |
| linux-vga | 21 / 24 | 2 |
| tango-dark | 21 / 24 | 4 |
| nord | 20 / 24 | 7 |
| tokyonight-night | 19 / 24 | 4 |
| tango-light | 18 / 24 | 5 |
| solarized-dark | 17 / 24 | 5 |
| gruvbox-light | 15 / 24 | 9 |
| catppuccin-latte | 12 / 24 | 5 |
| rose-pine-dawn | 7 / 24 | 11 |
| solarized-light | 3 / 24 | 5 |
| tokyonight-day | 2 / 24 | 4 |

Totals across the set: 115 contrast failures, 101 CVD collisions.

Findings:

1. **No third-party palette is CVD-clean on the status set.** The commonest collision is
   `success`/`danger` under deuteranopia - the classic red-green case. Catppuccin Mocha
   loses `success`/`warning` under protanopia at dE 4.5, and `info`/`accent` at dE 1.1,
   which is effectively the same colour.
2. **This is not fixable by palette choice.** It is the argument for v1 principle 4,
   now measured rather than asserted: every state must carry a word as well as a colour.
   Variant D does this - `ok`, `failed`, `pending`, `running`, `fatal` are text in the
   block header, right-aligned, in every frame.
3. **The old terminal palettes do better than the modern ones.** Campbell, xterm and
   linux-vga each collide only twice; Gruvbox and Rose Pine collide nine and eleven
   times. Saturated primaries separate under simulation; muted pastels do not. That is
   a real cost of the current aesthetic and it is worth stating plainly.

### 3.2 Search beat taste, and the margin was large

I picked the v1 Mivia status colours by eye. Measured, they collided: dark scored a
worst-case dE of 6.9 and light 3.0 - visually identical pairs under deuteranopia.

I then ran a search over hue, saturation and lightness for `{success, warning, danger,
info}`, constrained to conventional hue windows (green, amber, red, blue) and to WCAG AA
on the theme background, maximising the worst-case dE across normal vision and all three
dichromacies.

| Palette | Chosen by | Worst-case dE |
|---|---|---|
| mivia-dark, v1, by eye | taste | 6.9 |
| mivia-dark, hand-tempered second attempt | taste | 19.5 |
| **mivia-dark, v2** | **search** | **33.2** |
| mivia-light, v1, by eye | taste | 3.0 |
| mivia-light, hand-tempered second attempt | taste | 7.8 |
| **mivia-light, v2** | **search** | **34.5** |
| **mivia-high-contrast, v2** | **search, AAA 7:1** | **30.8** |

My second attempt, made *after* seeing the first set of numbers, was still four times
worse than the search. The conclusion generalises beyond this project: **accessible
palette selection is a constrained optimisation, and doing it by eye does not work even
when you know what you are optimising.** The real theme package should ship the search,
not only the validator, so that a new first-party theme is generated against the
constraint rather than checked after the fact.

Unconstrained, the search scored higher still - dE 42.9 for dark - but chose a cyan
`success` and a violet `info`. Convention has a value the metric cannot see, so the
shipped set is the constrained result.

### 3.3 Final Mivia status colours

| Role | mivia-dark | mivia-light | mivia-high-contrast |
|---|---|---|---|
| success | `#4edc4e` | `#1d6b53` | `#19e6a8` |
| warning | `#f3f34e` | `#7d5a08` | `#e6b319` |
| danger | `#dc4e4e` | `#5e0808` | `#ef6c6c` |
| info | `#5b8cff` | `#142671` | `#8595d6` |
| accent (chrome) | `#fafafa` | `#18181b` | `#ffffff` |

Superseded values from the earlier revision of this file are kept in the history of
sections 3.2 and 8.4 rather than here, because the search results that produced them
are the useful record, not the hexes themselves.

`mivia-high-contrast` is new in v2, is original, and meets **WCAG AAA (7:1)** on every
status role against black, with a worst-case CVD separation of dE 30.8.

---

## 4. Themes added in v2

The picker is now a grid of palette previews rather than a list, which is why the set
is larger: the grid makes a large set browsable.

| Scheme | Source | Licence |
|---|---|---|
| Dracula | <https://github.com/dracula/dracula-theme> | MIT, Dracula Theme 2023 |
| Campbell (Windows Terminal) | <https://learn.microsoft.com/en-us/windows/terminal/customize-settings/color-schemes> | Microsoft docs, see the note below |
| Tango (dark and light) | Tango Desktop Project palette | see the note below |
| Linux console (VGA) | <https://en.wikipedia.org/wiki/ANSI_escape_code> | palette values, factual |
| xterm | <https://en.wikipedia.org/wiki/ANSI_escape_code> | palette values, factual |
| Solarized (dark and light) | <https://ethanschoonover.com/solarized/> | MIT, Ethan Schoonover 2011 |

Carried from v1 unchanged: Catppuccin Mocha and Latte, Tokyo Night Night and Day, Rose
Pine and Dawn, Gruvbox dark and light, Nord. Licences are in `research.md` section 2.

**Derived mapping - state this in the shipped credits.** Campbell, Tango, Linux, xterm
and Solarized are 16-colour terminal palettes, not role palettes. Mapping them onto
`success`, `warning`, `keyword`, `diff-add-bg` and the rest is **my derivation, not the
upstream's design**. The diff background tints in particular are invented, because no
16-colour palette defines one. Do not present these as the upstream theme without that
qualification.

**Licence notes.**

- Campbell's values come from Microsoft's documentation page, which shows the scheme as
  a JSON sample. That documents the values; it is not a licence grant for the scheme as
  an artifact. A 16-colour list is close to unprotectable fact, but confirm before
  shipping if legal review is available.
- **A sourcing error worth recording.** The Wikipedia ANSI-escape-code colour table has
  a column that reads as "Tango" in some renderings but whose values are Campbell's -
  its blue is `0,55,218`, which is exactly Campbell's `#0037DA` from the Microsoft page.
  The genuine Tango Desktop Project values are different (`#3465a4` blue, `#4e9a06`
  green, `#c4a000` yellow). I used the Tango Desktop values for the Tango themes and
  the Microsoft page for Campbell. If a future contributor copies that Wikipedia column
  as "Tango", they will ship Campbell under the wrong name.
- Gruvbox's missing LICENSE file (v1 finding 1) is unchanged and still unresolved.

---

## 5. Findings carried into implementation

Still open from `research.md`: the Gruvbox licence, the Tokyo Night Apache-2.0 NOTICE,
the missing theme roles, the third-party contrast gate, `docs/OWNERS.yaml`, and the
16-colour accent map.

New in v2:

1. **Ship the palette search, not only the validator.** Section 3.2. A first-party theme
   should be generated against the contrast and CVD constraints.
2. **`accent` is chrome and never a status.** Section 3. This must be a documented rule
   in the theme package, because it is the assumption that lets an amber brand accent
   coexist with an amber `warning`.
3. **Collapse state must be decided at print time, not at toggle time.** Inline
   rendering freezes a block once it scrolls out of the repaint window, so the default
   open-or-closed decision is made from a size threshold when the block is first
   written. `wireframes-panes.md` section 5.
4. **Dialog approvals default to deny; inline approvals default to once.** The promotion
   to a dialog is itself the signal that the call was not judged safe.
   `wireframes-panes.md` section 8.
5. **Saturated primaries survive CVD simulation better than muted pastels.** Section
   3.1. If a future first-party theme is designed to look fashionable, it will score
   worse, and the search should be the arbiter.
6. Not verified in v2, still open: no user has been observed using any of this. Every
   scannability claim in both research files remains judgement.

---

## 7. Markdown and mermaid rendering (added after review)

### 7.1 glamour for markdown

Source: <https://github.com/charmbracelet/glamour> - MIT, 3,650 stars.
Style file: <https://raw.githubusercontent.com/charmbracelet/glamour/master/styles/dark.json>

glamour is "stylesheet-based markdown rendering for your CLI apps". Styles are **JSON**
with one entry per element: `document`, `paragraph`, `heading` plus `h1`..`h6`, `list`,
`item`, `enumeration`, `task`, `table`, `link`, `image`, `code`, `code_block` with a
nested `chroma` object for syntax highlighting, `block_quote`, `emph`, `strong`,
`strikethrough`, `hr`. Colours are given either as ANSI numbers (`"color": "252"`) or
hex (`"#C4C4C4"`), and elements also carry `bold`, `italic`, `underline`, margin and
indent. Word wrap is a render option, `glamour.WithWordWrap(40)`, default 80.

**The integration decision this forces:** generate the glamour stylesheet from the
mivia theme at theme-switch time. Do not ship a static style asset. If the stylesheet
were static, markdown would stop matching the UI the moment the user switched theme,
and the theme would no longer be the single source of style. The mapping is
mechanical - `h1`..`h3` to `accent`, `code_block.chroma` to the syntax roles,
`block_quote` to `fg-muted`, `link` to a `link` role (see the missing-roles finding in
`wireframes.md` section 7).

Open question I did not resolve: glamour's `chroma` section names chroma token types,
and chroma's own licence reports as `NOASSERTION` on the GitHub API. It arrives as a
glamour dependency rather than a direct one, but confirm before shipping.

### 7.2 mermaid-ascii for diagrams

Source: <https://github.com/AlexanderGrooff/mermaid-ascii> - MIT, 1,537 stars.
Also on pkg.go.dev, and a Go-to-TypeScript port exists as `beautiful-mermaid`.

Supported: **flowcharts** (labelled edges, LR and TD, `classDef` node colour),
**sequence diagrams** (solid and dotted arrows, self-messages, `loop`, `opt`,
`alt`/`else`, `par`, `critical`, `break`, `rect`, autonumbering), and **entity
relationship diagrams** (crow's-foot cardinalities, attributes).

Not supported: subgraphs, non-rectangular node shapes, diagonal arrows, sequence
activation boxes, class diagrams, state diagrams.

The README documents the CLI, not a public Go API. **Verify the exported package
surface before depending on it in-process**; the fallback is to shell out, which this
design would rather avoid because it puts a second binary in the render path.

Design decisions in `wireframes-panes.md` section 16: never drop a diagram (fall back to
the fenced source with the reason in the block header), map `classDef` colours onto
theme roles rather than honouring literal hex, and scroll wide diagrams horizontally
rather than wrapping them.

---

## 8. The mark, and the graphite palette (added after review)

### 8.1 The logo is a Unicode character

The Mivia logo is a diamond with a black left half and a white right half. That is
exactly **U+2B16 DIAMOND WITH LEFT HALF BLACK**, verified against the Unicode Character
Database (Python `unicodedata`, UCD 16.0.0). Its neighbours complete the set:

| Codepoint | Name |
|---|---|
| U+2B16 | DIAMOND WITH LEFT HALF BLACK |
| U+2B17 | DIAMOND WITH RIGHT HALF BLACK |
| U+2B18 | DIAMOND WITH TOP HALF BLACK |
| U+2B19 | DIAMOND WITH BOTTOM HALF BLACK |
| U+25C6 / U+25C7 / U+25C8 | BLACK DIAMOND / WHITE DIAMOND / WHITE DIAMOND CONTAINING BLACK SMALL DIAMOND |

Cycling U+2B16, U+2B18, U+2B17, U+2B19 moves the black half left, top, right, bottom -
a rotation. **The logo and the activity indicator are therefore the same object**, and
the brand mark is never replaced by a generic spinner. The fill sequence
U+25C7, U+25C8, U+25C6 gives a second, different motion for streaming.

This is a lucky result rather than a clever one: the logo happened to already exist in
Unicode. If it had not, the honest fallback would have been a two-cell mark, which
would have cost alignment in the status line.

### 8.2 Animation is an accessibility hazard

Spinners are actively hostile to screen readers: a blind developer hears the animation
as an unintelligible stream of individual characters, not as progress. `gcloud` ships
an `accessibility/screen_reader` setting that swaps spinners for plain "working" text,
GitHub rewrote the `gh` spinner for accessibility, and Gemini CLI added a
`--screen-reader` flag. Sources gathered via search:
<https://evilmartians.com/chronicles/cli-ux-best-practices-3-patterns-for-improving-progress-displays>
and <https://github.com/zaydea805/term-a11y>. I did not fetch the gcloud, gh or Gemini
documentation directly, so treat the specific flag names as reported rather than
verified.

Consequences, in `wireframes-panes.md` section 17: a `--screen-reader` mode drops the
glyph and the animation and prints the state word; the state word is present beside the
mark in every state anyway; animation never appears on a non-TTY, in `--output json`,
or in the plain stream renderer.

### 8.3 Graphite, and an accent that is not a colour

The palette moved to a neutral graphite base in the shadcn and Vercel direction.
shadcn's dark tokens are OKLCH with **zero chroma** - `--background oklch(0.145 0 0)`,
`--foreground oklch(0.985 0 0)`, `--muted oklch(0.269 0 0)`, `--primary oklch(0.922 0
0)` - which convert to `#0a0a0a`, `#fafafa`, `#262626` and `#e5e5e5` (converted here;
source <https://ui.shadcn.com/docs/theming>). Only `--destructive` carries chroma.

Following the logo, **the mivia accent is now achromatic**: `#fafafa` in dark,
`#18181b` in light, `#ffffff` in high contrast. This was an aesthetic decision that
paid an accessibility dividend, which is worth recording because it is the opposite of
the usual direction:

| Palette | Contrast pairs | CVD collisions |
|---|---|---|
| mivia-dark, v2 amber accent | 24 / 24 | 3, all `warning`/`accent` |
| **mivia-dark, graphite achromatic accent** | **24 / 24** | **0** |
| **mivia-light, graphite** | **24 / 24** | **0** |
| **mivia-high-contrast, achromatic accent** | **24 / 24** | **0** |

An achromatic accent cannot collide with a status hue under any dichromacy, because it
has no hue. The `accent`-is-chrome scope rule from section 3 is still correct, but it
is no longer load-bearing for the first-party themes.

### 8.4 Vivid status colours, and what they cost

Review asked for more vivid status colours, in the Linux VGA and Campbell direction.
Measured, that request is in direct tension with CVD separation:

| Status set | Worst-case dE |
|---|---|
| searched, unconstrained hue | 42.9 |
| searched, conventional hue windows | 33.2 |
| searched, high saturation floor | 38.2 |
| **Linux VGA bright (shipped)** | **18.5** |
| Campbell green + Campbell red | 14.8 |
| Campbell green + VGA red | 10.0 |

The shipped dark set is Linux VGA bright green `#4edc4e`, red `#dc4e4e` and yellow
`#f3f34e`, with the blue lifted from VGA's `#4e4edc` (which fails contrast at 3.25) to
`#5b8cff`.

**18.5 is below the 20 threshold, and I am not hiding that.** Two facts put it in
proportion. First, the binding constraint is `success`/`danger` under deuteranopia -
green against red - and once those two are fixed by choice, **no selection of the other
two colours can raise the worst case above 18.5**; that ceiling was measured by
searching `warning` and `info` exhaustively against the fixed pair. Second, 18.5 still
beats every third-party palette in the set: Catppuccin Mocha collides at 1.1, Gruvbox
at nine pairs, Rose Pine Dawn at eleven.

The mitigation is the one the design already had: every state carries a word. `ok`,
`failed`, `pending`, `running` are text in the block header in every frame, so a reader
who cannot separate the green from the red loses no information. The vividness is
decoration on top of a signal that does not depend on it - which is exactly the
condition under which trading measured separation for appearance is defensible.

`mivia-light` and `mivia-high-contrast` remain at 0 collisions; the trade applies to
the dark theme only.

### 8.5 Syntax roles are toned, status roles are not

Review asked for the vivid cyan, yellow and magenta to be calmed. Measured, the two
groups behave differently and were treated differently:

| Role | Was | Now | Saturation |
|---|---|---|---|
| `type` (syntax) | `#4ef3f3` | `#74c9cc` | 0.87 -> 0.46 |
| `function` (syntax) | `#f3f34e` | `#d3ce85` | 0.87 -> 0.47 |
| `number` (syntax) | `#f34ef3` | `#d18fd1` | 0.87 -> 0.42 |
| `warning` (status) | `#f3f34e` | `#f3f34e` | unchanged |

`warning` was left alone because toning it is measurably harmful, which was not
obvious in advance:

| `warning` | Saturation | Worst-case dE |
|---|---|---|
| `#f3f34e` (shipped) | 0.87 | **18.5** |
| `#d3ce85` | 0.47 | 15.6 |
| `#dcd77e` | 0.57 | 12.2 |
| `#e2e06a` | 0.67 | 7.9 |

The vivid yellow's lightness is what separates it from `success` under deuteranopia.
Desaturating it moves it toward the green it must be distinguished from. The general
rule this suggests: **tone syntax freely, tone status only against the measurement.**
Syntax is texture and a reader who confuses two token colours loses little; status is a
signal and a reader who confuses two status colours loses the meaning of the frame.
