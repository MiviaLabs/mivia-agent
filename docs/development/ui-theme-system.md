# New terminal UI theme system

`internal/ui/theme` is the single source of style for the new terminal UI
(`internal/uikit`, `internal/ui`). A view layer never
holds a literal colour. It holds a `theme.Role` and looks it up through a
`theme.Theme`.

Design source of truth: `docs/design/wireframes-panes.md` section 18 (the
locked first-party hex values) and `docs/design/research-panes.md` sections
2-3 (contrast method, colour-vision method, the degradation ladder).

## Role reference

32 roles, in four groups. `AllRoles()` in `internal/ui/theme/role.go` is the
canonical list; this table is a summary, not a second source of truth.

| Group | Roles |
|---|---|
| Base | `bg`, `bg-subtle`, `bg-inset`, `fg`, `fg-muted`, `fg-subtle`, `border`, `border-focus`, `accent`, `accent-fg`, `success`, `warning`, `danger`, `info` |
| Syntax | `keyword`, `string`, `number`, `comment`, `function`, `type`, `variable` |
| Diff | `diff-add-fg`, `diff-add-bg`, `diff-del-fg`, `diff-del-bg`, `diff-hunk` |
| Added in Phase 1 (`wireframes.md` section 7 gap) | `bg-selection`, `diff-add-emph-bg`, `diff-del-emph-bg`, `gutter`, `link`, `fg-inverse` |

Two rules that are easy to get wrong:

- **`accent` is chrome, never a status.** It is the prompt marker, the focus
  ring, the selected row. It must pass contrast, but it is exempt from the
  colour-vision separation check that binds `{success, warning, danger,
  info}` to each other.
- **`border` is decorative, `gutter` is not.** No state is carried by
  `border` alone, so it is exempt from the contrast gate. `gutter` (a line
  rail) is functional and is gated like any other role.

## Contrast gate

`ValidateContrast` (`contrast.go`) checks every pair in `AllContrastChecks()`
against WCAG 2.1: 4.5:1 for body text, 3:1 for large text and UI components.
It is theme-agnostic; the first-party-only enforcement is a test-suite
convention, not something the function itself does.
`TestFirstPartyContrastPasses` hard-fails the build if any embedded
first-party theme regresses, by filtering to `Theme.FirstParty` before
asserting. A third-party theme's `ValidateContrast` result is available but
not asserted on.

## Colour-vision gate

`WorstCaseSeparation` (`cvd.go`) simulates protanopia, deuteranopia and
tritanopia with the Vienot/Brettel LMS model, and measures CIE76 `dE`
between every pair of `{success, warning, danger, info}` under each
dichromacy and under normal vision. `HardFailSeparation` compares the worst
case against the theme's own `CVDBudget` field; it is theme-agnostic too,
the same way `ValidateContrast` is above.

The budget is per-theme, not a single global constant, because
`mivia-dark`'s shipped status colours trade some separation for vividness
(`research-panes.md` section 8.4) - that trade is deliberate and
documented, not a defect. Every state also carries a word (`ok`, `failed`,
`pending`, `running`) in addition to colour, so a reader who cannot
separate two status hues loses no information.

No third-party theme in the survey (`research-panes.md` section 3.1) is
CVD-clean, so a hard gate there would reject nearly every upstream palette.
`HardFailSeparation`'s gate is intended for first-party themes; treat a
third-party theme's number as informational.

## Degradation ladder

`Theme.Resolve(role, tier)` (`degrade.go`) returns a tier-appropriate
`Style`:

| Tier | Colour source |
|---|---|
| `TierTrueColor`, `Tier256` | the theme's hex value, downsampled by the terminal/library at 256 |
| `Tier16` | the theme's own `ansi16` map - an explicit index per role, **not** a computed nearest match (a generic downsample turns an achromatic accent into ANSI silver) |
| `TierASCII`, `TierNoTTY` | no colour; `Bold`/`Dim` from `Emphasis(role)` still apply |

`Detect(w, env)` wraps `colorprofile.Detect` and honours `NO_COLOR`,
`CLICOLOR`, `CLICOLOR_FORCE`, `TERM=dumb`. Structural emphasis (bold for
`accent`/`border-focus`, dim for `fg-muted`/`fg-subtle`/`comment`/`gutter`)
is independent of the colour tier, because `NO_COLOR` disables colour but
preserves text decoration.

## How to add a theme

Adding a theme is a data change, never a code change.

1. Write a JSON file matching `internal/ui/theme/themes/mivia-dark.json`'s
   shape: `name`, `label`, `dark`, `first_party`, `cvd_budget`, a `colors`
   object with all 32 roles, and an `ansi16` object mapping every role to
   an explicit ANSI SGR index (0-15).
2. First-party themes: drop the file in `internal/ui/theme/themes/` (picked
   up by `go:embed`, see `Embedded()` in `embed.go`). User themes: drop the
   file in the user's theme config directory (`LoadUserDir`).
3. For the status quad (`success`/`warning`/`danger`/`info`), prefer
   `SearchStatusPalette` (`search.go`) over hand-picking. Hand-picking a
   status palette measurably loses to a constrained search
   (`research-panes.md` section 3.2): search maximises worst-case
   colour-vision separation within conventional hue windows and a minimum
   contrast floor, instead of picking by eye and hoping.
4. Run `go test ./internal/ui/theme/...`. `TestEmbeddedThemesLoad` checks
   every role and every `ansi16` index is present; the contrast and CVD
   tests gate first-party themes automatically once the theme is
   `first_party: true`.

## Looking at it

The in-app theme picker previews every embedded theme: press `ctrl+t`
on the conversation screen, or run `/theme`. It renders role swatches,
a diff pair, and the status set live. The degradation tiers are gated
offline by `go test ./internal/ui/theme/...`; run the app under
`NO_COLOR=1` or `TERM=dumb` to see the no-colour tier by hand.
