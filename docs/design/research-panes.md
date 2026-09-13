# Theme colour and contrast reference

This file is the colour and contrast reference for the mivia terminal UI themes. It
records the contrast method, the colour-vision method, the degradation ladder, the
status-palette search, and the measured results behind the shipped themes. The
implementation lives in `internal/ui/theme`; `docs/development/ui-theme-system.md` is
the developer guide.

---

## 1. Scope

Three rules bound every theme decision in this file:

- The set that must stay mutually separable is `{success, warning, danger, info}`.
- **`accent` is chrome and never a status.** It is the prompt marker, the focus ring
  and the selected row. It must pass contrast, but it is exempt from the separation
  check that binds the status set (`internal/ui/theme/role.go`, `StatusRoles`).
- **Every state carries a word as well as a colour.** `ok`, `failed`, `pending` and
  `running` are text in the block header in every frame, so colour never carries a
  state alone.

The first-party accents are achromatic (section 8.3), so they cannot collide with any
status hue under any dichromacy. The accent rule still binds third-party-derived
themes, whose accents carry hue.

The contrast gate is `ValidateContrast` (`internal/ui/theme/contrast.go`). It checks
every pair in `AllContrastChecks()` against the WCAG 2.1 ratio the pair must meet:
4.5:1 for body text, 3:1 for large text and UI components.
`TestFirstPartyContrastPasses` hard-fails the build when an embedded first-party
theme regresses.

---

## 2. Terminal colour detection and the degradation ladder

The degradation ladder is a library concern, not a hand-rolled one. The tiers come
from the colour-profile package (section 2.1), and `Theme.Resolve(role, tier)`
(`internal/ui/theme/degrade.go`) returns a tier-appropriate style. Truecolour and
256-colour tiers use the theme's hex values; the 16-colour tier uses the theme's own
`ansi16` map; the ASCII and no-TTY tiers use no colour, but the `Emphasis` bold and
dim still apply. `docs/development/ui-theme-system.md` carries the full tier table.

Two consequences shape the design:

1. **`NO_COLOR` "disables colors but preserves text decoration".** Bold, dim, reverse
   and underline survive. A design built on typographic hierarchy - weight, dim,
   bright, indentation - therefore keeps almost all of its structure under
   `NO_COLOR`.
2. **The 16-colour tier needs an authored map, not a computed one.** A generic
   downsample turns an achromatic accent into ANSI silver, so every theme carries an
   explicit `ansi16` index per role.

### 2.1 The colour-profile package

Source: <https://pkg.go.dev/github.com/charmbracelet/colorprofile>.

`Profile` constants are `Unknown`, `NoTTY`, `ASCII`, `ANSI` (16), `ANSI256`,
`TrueColor`. `func Detect(output io.Writer, env []string) Profile` respects `NO_COLOR`,
`CLICOLOR` and `CLICOLOR_FORCE`; `TERM=dumb` gives `NoTTY` unless `CLICOLOR_FORCE=1`;
`COLORTERM=truecolor` upgrades to `TrueColor`. `func (p Profile) Convert(c color.Color)
color.Color` downsamples a colour to the profile.

---

## 3. Colour-vision method

**Method.** Simulate each colour with the Vienot, Brettel and Mollon (1999) LMS
dichromat model: convert sRGB to linear, linear RGB to LMS with the
Hunt-Pointer-Estevez matrix, apply the protanopia, deuteranopia or tritanopia
projection in LMS, convert back. Then measure separation as CIE76 dE in CIELAB
between every pair of status colours after simulation, under normal vision and all
three dichromacies. Threshold: **dE 20**, below which two colours read as the same
hue at terminal text sizes. The threshold is a repo choice, not a standard; a
stricter or looser number moves the counts.

**Scope.** The set that must stay mutually separable is `{success, warning, danger,
info}`. `accent` is chrome - the prompt marker, the focus ring, the selected row -
and never encodes a status, so it must pass contrast but is exempt from the
separation check.

The first-party accents are achromatic (section 8.3), so they cannot collide with
any status hue at all. The scope rule still holds for third-party-derived themes,
whose accents carry hue.

### 3.1 Per-theme contrast and colour-vision results

The comparison set is 20 palettes - the three Mivia themes plus 17 common terminal
palettes - checked against 24 contrast pairs and 3 dichromacies each.

| Theme | Contrast pairs passing | CVD collisions |
|---|---|---|
| **mivia-dark** | **24 / 24** | worst-case dE 18.5, by the vividness trade (section 8.4) |
| **mivia-light** | **24 / 24** | **0** |
| **mivia-high-contrast** | **24 / 24** | **0** |
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

1. **No third-party palette in the set is CVD-clean on the status set.** The
   commonest collision is `success`/`danger` under deuteranopia - the classic
   red-green case. Catppuccin Mocha loses `success`/`warning` under protanopia at
   dE 4.5, and `info`/`accent` at dE 1.1, which is effectively the same colour.
2. **This is not fixable by palette choice.** It is the argument for the word rule in
   section 1, now measured rather than asserted: every state must carry a word as
   well as a colour. The layout does this - `ok`, `failed`, `pending`, `running`,
   `fatal` are text in the block header, right-aligned, in every frame.
3. **The old terminal palettes do better than the modern ones.** Campbell, xterm and
   linux-vga each collide only twice; Gruvbox and Rose Pine collide nine and eleven
   times. Saturated primaries separate under simulation; muted pastels do not.

### 3.2 The status-palette search

Hand-picking a status palette loses to a constrained search. The first hand-picked
dark set scored a worst-case dE of 6.9 and light 3.0 - visually identical pairs under
deuteranopia. A tempered second attempt, made with the first set of numbers already
known, still scored 19.5 against the search's 33.2 for dark.

The search runs over hue, saturation and lightness for `{success, warning, danger,
info}`, constrained to conventional hue windows (green, amber, red, blue) and to a
WCAG contrast floor on the theme background, maximising the worst-case dE across
normal vision and all three dichromacies.

| Palette | Chosen by | Worst-case dE |
|---|---|---|
| mivia-dark, hand-picked, first attempt | taste | 6.9 |
| mivia-dark, hand-picked, tempered | taste | 19.5 |
| **mivia-dark, searched, conventional hue windows** | **search** | **33.2** |
| mivia-light, hand-picked, first attempt | taste | 3.0 |
| mivia-light, hand-picked, tempered | taste | 7.8 |
| **mivia-light, searched, conventional hue windows** | **search** | **34.5** |
| **mivia-high-contrast, searched at the AAA 7:1 floor** | **search** | **30.8** |

**Accessible palette selection is a constrained optimisation, and picking by eye does
not work even when the objective is known.** `SearchStatusPalette`
(`internal/ui/theme/search.go`) ships the search, so a new first-party theme is
generated against the constraint rather than checked after the fact.

Unconstrained, the search scores higher still - dE 42.9 for dark - but picks a cyan
`success` and a violet `info`. Convention has a value the metric cannot see, so the
constrained result is the basis for the shipped palettes. The dark theme then trades
part of the searched margin for vividness (section 8.4).

### 3.3 Shipped Mivia status colours

| Role | mivia-dark | mivia-light | mivia-high-contrast |
|---|---|---|---|
| success | `#4edc4e` | `#1d6b53` | `#19e6a8` |
| warning | `#ffb900` | `#7d5a08` | `#e6b319` |
| danger | `#dc4e4e` | `#5e0808` | `#ef6c6c` |
| info | `#5b8cff` | `#142671` | `#8595d6` |
| accent (chrome) | `#fafafa` | `#18181b` | `#ffffff` |

`mivia-high-contrast` meets **WCAG AAA (7:1)** on every status role against black,
with a worst-case CVD separation of dE 30.8. `mivia-light` keeps the searched status
set. `mivia-dark` trades part of its searched separation for vividness (section
8.4).

---

## 4. Third-party-derived themes

The embedded theme set (`internal/ui/theme/themes/`) ships two themes derived from
public palettes beside the three Mivia themes: `dracula-classic` and `nord-aurora`.
Both are marked `first_party`, so the contrast and colour-vision gates test them
exactly like the Mivia themes.

A public palette mapped onto the role set is a **derivation, not the upstream's
design**. The role mapping and the diff background tints in particular are invented:
no upstream palette defines them. Present these themes with that qualification.

---

## 5. Implementation rules

1. **Generate a status palette with the search, not by hand.** Section 3.2;
   `SearchStatusPalette` in `internal/ui/theme/search.go`.
2. **`accent` is chrome and never a status.** Section 3. This rule is what lets an
   accent share a hue family with `warning` without weakening the separation check.
3. **Collapse state is decided at print time, not at toggle time.** Inline rendering
   freezes a block once it scrolls out of the repaint window, so the default
   open-or-closed decision is made from a size threshold when the block is first
   written. `wireframes-panes.md` section 5.
4. **Dialog approvals default to deny; inline approvals default to once.** The
   promotion to a dialog is itself the signal that the call was not judged safe.
   (`ApprovalDefaultInline`, `ApprovalDefaultDialog` in
   `internal/uikit/config/defaults.go`.)
5. **Saturated primaries survive CVD simulation better than muted pastels.** Section
   3.1. Let the search arbitrate any new status palette.

---

## 7. Markdown and diagram rendering libraries

### 7.1 glamour for markdown

`charm.land/glamour/v2` renders the markdown (`go.mod`: glamour v2.0.1). Project:
<https://github.com/charmbracelet/glamour> - MIT.

glamour is stylesheet-based markdown rendering. Styles are **JSON** with one entry
per element: `document`, `paragraph`, `heading` plus `h1`..`h6`, `list`, `item`,
`enumeration`, `task`, `table`, `link`, `image`, `code`, `code_block` with a nested
`chroma` object for syntax highlighting, `block_quote`, `emph`, `strong`,
`strikethrough`, `hr`. Colours are given either as ANSI numbers (`"color": "252"`)
or hex (`"#C4C4C4"`), and elements also carry `bold`, `italic`, `underline`, margin
and indent. Word wrap is a render option, `glamour.WithWordWrap(40)`, default 80.

**The integration rule:** generate the glamour stylesheet from the mivia theme at
render time (`styleConfigFor` in `internal/ui/render/markdown.go`). Do not ship a
static style asset. If the stylesheet were static, markdown would stop matching the
UI the moment the user switched theme, and the theme would no longer be the single
source of style. The mapping is mechanical - `h1`..`h3` to `accent`,
`code_block.chroma` to the syntax roles, `block_quote` to `fg-muted`, `link` to the
`link` role (`docs/development/ui-theme-system.md`, role reference).

### 7.2 mermaid-ascii for diagrams

Project: <https://github.com/AlexanderGrooff/mermaid-ascii> - MIT.

Supported: **flowcharts** (labelled edges, LR and TD, `classDef` node colour),
**sequence diagrams** (solid and dotted arrows, self-messages, `loop`, `opt`,
`alt`/`else`, `par`, `critical`, `break`, `rect`, autonumbering), and **entity
relationship diagrams** (crow's-foot cardinalities, attributes).

Not supported: subgraphs, non-rectangular node shapes, diagonal arrows, sequence
activation boxes, class diagrams, state diagrams.

The project documents a CLI, not a stable Go API. An in-process dependency needs its
exported surface pinned; shelling out keeps the API at arm's length but puts a
second binary in the render path.

Design decisions in `wireframes-panes.md` section 16: never drop a diagram (fall
back to the fenced source with the reason in the block header), map `classDef`
colours onto theme roles rather than honouring literal hex, and scroll wide diagrams
horizontally rather than wrapping them.

---

## 8. The mark and the graphite base

### 8.1 The logo is a Unicode character

The Mivia logo is a diamond with a black left half and a white right half. That is
exactly **U+2B16 DIAMOND WITH LEFT HALF BLACK**, verified against the Unicode
Character Database (UCD 16.0.0). Its neighbours complete the set:

| Codepoint | Name |
|---|---|
| U+2B16 | DIAMOND WITH LEFT HALF BLACK |
| U+2B17 | DIAMOND WITH RIGHT HALF BLACK |
| U+2B18 | DIAMOND WITH TOP HALF BLACK |
| U+2B19 | DIAMOND WITH BOTTOM HALF BLACK |
| U+25C6 / U+25C7 / U+25C8 | BLACK DIAMOND / WHITE DIAMOND / WHITE DIAMOND CONTAINING BLACK SMALL DIAMOND |

Cycling U+2B16, U+2B18, U+2B17, U+2B19 moves the black half left, top, right,
bottom - a rotation. **The logo and the activity indicator are therefore the same
object**, and the brand mark is never replaced by a generic spinner. The fill
sequence U+25C7, U+25C8, U+25C6 gives a second, different motion for streaming.

### 8.2 Animation is an accessibility hazard

Spinners are hostile to screen readers: a blind developer hears the animation as an
unintelligible stream of individual characters, not as progress. `gcloud`, GitHub's
`gh` and Gemini CLI all ship screen-reader modes that swap the animation for plain
state text. See
<https://evilmartians.com/chronicles/cli-ux-best-practices-3-patterns-for-improving-progress-displays>.

Consequences, specified in `wireframes-panes.md` section 17: a `--screen-reader`
mode drops the glyph and the animation and prints the state word; the state word is
present beside the mark in every state anyway; animation never appears on a
non-TTY, in `--output json`, or in the plain stream renderer.

### 8.3 Graphite, and an accent that is not a colour

The dark theme's base is a neutral graphite, in the shadcn and Vercel direction.
shadcn's dark tokens are OKLCH with **zero chroma** - `--background oklch(0.145 0 0)`,
`--foreground oklch(0.985 0 0)`, `--muted oklch(0.269 0 0)`, `--primary oklch(0.922 0
0)` - which convert to `#0a0a0a`, `#fafafa`, `#262626` and `#e5e5e5` (converted here;
source <https://ui.shadcn.com/docs/theming>). Only `--destructive` carries chroma.

Following the logo, **the mivia accent is achromatic**: `#fafafa` in dark,
`#18181b` in light, `#ffffff` in high contrast. An achromatic accent cannot collide
with a status hue under any dichromacy, because it has no hue. The achromatic accent
is an aesthetic decision that paid an accessibility dividend:

| Palette | Contrast pairs | CVD collisions |
|---|---|---|
| mivia-dark, amber accent | 24 / 24 | 3, all `warning`/`accent` |
| **mivia-dark, graphite achromatic accent** | **24 / 24** | **0** |
| **mivia-light, graphite** | **24 / 24** | **0** |
| **mivia-high-contrast, achromatic accent** | **24 / 24** | **0** |

The `accent`-is-chrome scope rule from section 3 still holds, but it is no longer
load-bearing for the first-party themes.

### 8.4 Vivid status colours, and what they cost

Vivid status colours, in the Linux VGA and Campbell direction, are in direct tension
with CVD separation:

| Status set | Worst-case dE |
|---|---|
| searched, unconstrained hue | 42.9 |
| searched, conventional hue windows | 33.2 |
| searched, high saturation floor | 38.2 |
| **Shipped dark set** | **18.5** |
| Campbell green + Campbell red | 14.8 |
| Campbell green + VGA red | 10.0 |

The shipped dark set is Linux VGA bright green `#4edc4e` and red `#dc4e4e`, amber
`#ffb900` as `warning`, with the blue lifted from VGA's `#4e4edc` (which fails
contrast at 3.25) to `#5b8cff`.

18.5 is below the 20 threshold. Two facts put it in proportion. First, the binding
constraint is `success`/`danger` under deuteranopia - green against red - and once
those two are fixed by choice, **no selection of the other two colours can raise the
worst case above 18.5**; the ceiling was measured by searching `warning` and `info`
exhaustively against the fixed pair. Second, 18.5 still beats every third-party
palette in the set: Catppuccin Mocha collides at 1.1, Gruvbox at nine pairs, Rose
Pine Dawn at eleven.

The mitigation is the standing design rule from section 1: every state carries a
word. `ok`, `failed`, `pending`, `running` are text in the block header in every
frame, so a reader who cannot separate the green from the red loses no information.
The vividness is decoration on top of a signal that does not depend on it - which is
exactly the condition under which trading measured separation for appearance is
defensible. The trade is encoded as the theme's own `cvd_budget` (18.5 for
`mivia-dark`), not as a single global constant.

`mivia-light` and `mivia-high-contrast` stay at 0 collisions; the trade applies to
the dark theme only.

### 8.5 Syntax roles are toned, status roles are not

The syntax and status colour groups behave differently under toning, and the themes
treat them differently:

| Role | Vivid form | Shipped form | Saturation |
|---|---|---|---|
| `type` (syntax) | `#4ef3f3` | `#74c9cc` | 0.87 -> 0.46 |
| `function` (syntax) | `#f3f34e` | `#d3ce85` | 0.87 -> 0.47 |
| `number` (syntax) | `#f34ef3` | `#d18fd1` | 0.87 -> 0.42 |
| `warning` (status) | `#ffb900` | `#ffb900` | not toned |

`warning` keeps full saturation because toning it is measurably harmful:

| `warning` | Saturation | Worst-case dE |
|---|---|---|
| `#f3f34e` (candidate) | 0.87 | **18.5** |
| `#d3ce85` | 0.47 | 15.6 |
| `#dcd77e` | 0.57 | 12.2 |
| `#e2e06a` | 0.67 | 7.9 |

The yellow's lightness is what separates `warning` from `success` under deuteranopia.
Desaturating it moves it toward the green it must be distinguished from. The general
rule: **tone syntax freely, tone status only against the measurement.** Syntax is
texture and a reader who confuses two token colours loses little; status is a signal
and a reader who confuses two status colours loses the meaning of the frame.
