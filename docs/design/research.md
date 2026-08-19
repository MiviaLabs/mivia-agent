# Mivia terminal UI - Phase 0 research

Status: Phase 0 research. No code exists for this design.
Date of the fetches: 2026-08-19. Star counts move; treat them as a snapshot.
Companion files: `wireframes.md` (layout source of truth), `mivia-ui-mock.html` (colour, disposable).

Rule for this file: every claim carries a URL. Where a claim is my judgement and not a
fetched fact, the text says so. Where a source did not answer the question, the text
says that too instead of filling the gap from memory.

---

## 1. How current developer and agent CLIs look

Star counts come from the GitHub REST API `/repos/{owner}/{repo}` endpoint, fetched
2026-08-19.

### 1.1 charmbracelet/crush - 27,490 stars, FSL-1.1-MIT

Source: <https://github.com/charmbracelet/crush>,
README <https://raw.githubusercontent.com/charmbracelet/crush/main/README.md>

What it does with density, hierarchy and colour: crush is session-based, and it exposes
density as an explicit user setting - the README documents a `"compact_mode": true` TUI
option. Its session picker shows per-session signals such as `IsBusy` and
`AttachedClients`. The README also documents a notification ladder,
`"auto, native, osc, bell, or disabled"`, which treats "the agent needs you" as a
first-class event rather than a line of text.

What I will not copy, and why: the session-picker-as-home-screen model. Mivia renders
inline, so a persistent picker surface is not available (see section 3.1). I take the
idea that "compact" is a user setting, not a designer's decision, and I take the
notification ladder as the model for how an approval prompt should announce itself.

I could not fetch a description of crush's colour system from the README; the README
links a demo image rather than documenting the palette. I do not assert anything about
its palette.

### 1.2 cli/cli (`gh`) - 45,882 stars, MIT

Sources: <https://cli.github.com/manual/gh_help_formatting>,
<https://raw.githubusercontent.com/cli/cli/trunk/docs/command-line-syntax.md>

What it does: `gh` defaults to "line-based plain text format" and adds structure only
on request through `--json`, `--jq` and `--template`. Its template helper set includes
an `autocolor` function that "works like `color`, but only emits color to terminals",
and it pretty-prints only when "connected to a terminal". Colour and structure are a
function of the output sink, not of the command.

What I will not copy: the template language as a user-facing surface. It is the right
answer for a scriptable porcelain and the wrong answer for a conversation. What I do
copy is the rule that the TTY check chooses the renderer. This is the direct source of
the three-renderer split (TUI, plain stream, JSON) that the build spec already requires.

### 1.3 jesseduffield/lazygit - 81,444 stars, MIT

Source: <https://raw.githubusercontent.com/jesseduffield/lazygit/master/README.md>

What it does: a full-screen multi-panel layout (branches, commits, files, diff) that is
entirely keyboard-driven, with `space` to stage, `/` to filter and `shift+w` to compare
commits. Colour carries git semantics directly: green commits are in master, yellow are
not, red are unpushed.

What I will not copy: the panel layout. It is alt-screen by construction and it is
exactly the dashboard the inline constraint rules out. What I do copy is the discipline
of colour as a semantic code with a small fixed vocabulary. Mivia's version of this is
the `success`/`warning`/`danger`/`info` role set, with a text token beside every colour
so the code still reads under `NO_COLOR`.

### 1.4 dandavison/delta - 31,792 stars, MIT

Source: <https://raw.githubusercontent.com/dandavison/delta/main/README.md>

What it does: the reference for terminal diffs. It provides "language syntax
highlighting with the same syntax-highlighting themes as bat", side-by-side view with
line numbers and wrapping, "stylable box/line decorations to draw attention to commit,
file and hunk header sections", `n`/`N` hunk navigation, and "word-level diff
highlighting using a Levenshtein edit inference algorithm". It supports light and dark
selection manually or by terminal background detection.

What I will not copy: the box decorations around commit, file and hunk headers. Section
3.2 of the build spec rules out heavy box-drawing. Mivia marks the hunk header with the
`diff-hunk` role and vertical space instead. What I do copy: word-level highlighting,
`n`/`N` navigation, and side-by-side as a width-gated option. Word-level highlighting
is the reason `wireframes.md` reports `diff-add-emph-bg` and `diff-del-emph-bg` as
missing theme roles.

### 1.5 atuinsh/atuin - 31,315 stars, MIT

Source: <https://raw.githubusercontent.com/atuinsh/atuin/main/README.md>

What it does: a history search UI bound over `ctrl-r` and `up`, with filter modes cycled
by `ctrl-r` (session, directory, global), and metadata columns showing "exit code,
duration, time and command".

What I will not copy: nothing structural; atuin's contribution here is the metadata row.
Its per-entry columns are the model for mivia's tool result row (name, path, status,
duration).

Honest gap: I intended to cite atuin's `inline_height` and `style` settings as prior art
for bounded inline rendering. The README does not document them and
<https://docs.atuin.sh/configuration/config/> returned HTTP 404 on 2026-08-19. **I make
no claim about atuin's inline rendering settings.** The inline-height argument in
section 3.1 rests on the Bubble Tea and xterm sources instead.

### 1.6 derailed/k9s - 34,386 stars, Apache-2.0

Source: <https://raw.githubusercontent.com/derailed/k9s/master/README.md>

What it does: a frame-based layout with borders and menus; skins are YAML files that map
names or hex values to roles, and a skin may use a `default` colour that "preserve[s]
your terminal background color settings". Density is a runtime toggle (`Ctrl-W` for wide
columns, `Ctrl-E` for headers). Navigation is a colon command surface (`:pod`, `:ctx`,
`:xray`).

What I will not copy: the frames and borders. What I do copy, and it is the strongest
single idea in this section: **themes are data files, not code**, and a theme may name a
role `default` to defer to the terminal. That is the model for mivia's embedded-plus-user
theme loading. The colon command surface is the direct ancestor of variant C's palette.

### 1.7 zellij-org/zellij - 34,987 stars, MIT

Source: <https://raw.githubusercontent.com/zellij-org/zellij/main/README.md>

What it does: the README states the design principle that "one must not sacrifice
simplicity for power", and describes floating and stacked panes plus WebAssembly
plugins.

Honest gap: the README does not describe the status bar, the mode system or keybinding
discoverability in enough detail to cite, and it points to
<https://zellij.dev/documentation/configuration.html> for that. **I make no claim about
zellij's status bar or its keybinding hints.** I take only the stated principle.

### 1.8 helix-editor/helix - 45,870 stars, MPL-2.0

Source: <https://docs.helix-editor.com/keymap.html>

What it does: modal editing with Normal mode as the default, `Escape` as the universal
return, and minor modes reached by a prefix key (`z` view, `g` goto, `m` match,
`Ctrl-w` window). `Space` opens a menu that groups file pickers, buffer pickers, LSP
actions and workspace search.

What I will not copy: modality in the transcript. A conversation UI where the composer
is sometimes not a composer is a trap. What I do copy: the prefix-key menu as the
discovery mechanism. It is why every variant in `wireframes.md` binds `?` on an empty
composer to print the keymap, and why variant C's palette is a single prefix
(`Ctrl-K`) rather than a mode.

### 1.9 Mainstream coding agents

I did not fetch a citable, specific description of the screen layout of the mainstream
coding-agent CLIs. Their published screenshots are images, and I will not describe an
image I did not read as text and then present it as a sourced finding. **This part of
the brief is not satisfied by evidence and I flag it rather than inventing it.** The
design decisions below do not depend on it.

---

## 2. Theme upstreams

Licences come from the GitHub REST API `/repos/{owner}/{repo}/license` endpoint or from
the fetched licence file, on 2026-08-19. Stars from the same API on the same date.

| Scheme | Palette source | Licence | Stars | Shipped in the mock |
|---|---|---|---|---|
| Catppuccin (Latte, Frappe, Macchiato, Mocha) | <https://raw.githubusercontent.com/catppuccin/palette/main/palette.json> | MIT <https://github.com/catppuccin/catppuccin/blob/main/LICENSE> | 19,658 | yes, Mocha and Latte |
| Tokyo Night (+ Day) | <https://raw.githubusercontent.com/folke/tokyonight.nvim/main/extras/lua/tokyonight_night.lua>, `..._day.lua` | Apache-2.0 <https://github.com/folke/tokyonight.nvim/blob/main/LICENSE> | 8,171 | yes, Night and Day |
| Rose Pine (+ Moon, Dawn) | <https://raw.githubusercontent.com/rose-pine/neovim/main/lua/rose-pine/palette.lua> | MIT <https://github.com/rose-pine/rose-pine-theme/blob/main/LICENSE> | 3,072 (neovim port) | yes, main and Dawn |
| Kanagawa (+ Lotus) | <https://raw.githubusercontent.com/rebelot/kanagawa.nvim/master/lua/kanagawa/colors.lua> | MIT, Tommaso Laurenzi 2021 | 6,343 | no |
| Nord | <https://raw.githubusercontent.com/nordtheme/nord/develop/src/nord.css> | MIT, Sven Greb <https://raw.githubusercontent.com/nordtheme/nord/develop/license> | 6,865 | yes, dark only |
| Gruvbox | <https://raw.githubusercontent.com/morhetz/gruvbox/master/colors/gruvbox.vim> | see the note below | 15,690 | yes, dark and light |
| Everforest | <https://raw.githubusercontent.com/sainnhe/everforest/master/autoload/everforest.vim> | MIT, sainnhe 2019 | 4,173 | no |
| Ayu | <https://github.com/ayu-theme/ayu-colors> | MIT <https://github.com/ayu-theme/ayu-colors/blob/master/license> | 850 | no |
| Solarized | <https://ethanschoonover.com/solarized/> | MIT, Ethan Schoonover 2011 <https://raw.githubusercontent.com/altercation/solarized/master/LICENSE> | 16,010 | no |
| Night Owl | <https://github.com/sdras/night-owl-vscode-theme> | MIT <https://github.com/sdras/night-owl-vscode-theme/blob/main/LICENSE.md> | 2,957 | no |
| Dracula | <https://github.com/dracula/dracula-theme> | MIT, Dracula Theme 2023 <https://raw.githubusercontent.com/dracula/dracula-theme/main/LICENSE> | 23,561 | no |
| Nightfox | <https://github.com/EdenEast/nightfox.nvim> | MIT, James Simpson 2021 <https://raw.githubusercontent.com/EdenEast/nightfox.nvim/main/LICENSE> | 4,063 | no |

**Gruvbox licence - open question, do not ship until it is resolved.** The README states
the licence is "MIT/X11"
(<https://raw.githubusercontent.com/morhetz/gruvbox/master/README.md>, rendered at
<https://github.com/morhetz/gruvbox>), but the repository has **no LICENSE file**: the
GitHub licence API returns `Not Found` for `morhetz/gruvbox`, and the top-level file list
is `.github/`, `autoload/`, `colors/`, `CHANGELOG.md`, `README.md`,
`gruvbox_256palette.sh`, `gruvbox_256palette_osx.sh`, `package.json`. A README statement
is weaker evidence than a licence file. The mock includes Gruvbox to judge density;
before the real binary embeds it, either confirm the licence with the author or embed a
fork that carries a real licence file. This is a finding, not a blocker for Phase 0.

**Tokyo Night is Apache-2.0, not MIT**, unlike every other candidate. Apache-2.0 carries
a NOTICE and patent-grant obligation that MIT does not. That is a packaging decision for
the real binary, not a design decision.

### 2.1 Popularity

Two independent signals agree, and both are weak in different ways.

- Star counts, above. Highest: Dracula 23,561; Catppuccin 19,658; Solarized 16,010;
  Gruvbox 15,690. Stars measure lifetime accumulation, so they favour old projects.
  Solarized (2011) and Dracula are old; Catppuccin's count is recent accumulation.
- Editorial: <https://moltamp.com/blog/best-terminal-color-schemes-2026/> names
  Gruvbox, Catppuccin, Tokyo Night, Nord and Solarized as the 2026 set, and describes
  Catppuccin as "the breakout star of the last few years" whose "biggest strength is
  ecosystem reach". This is a blog, not data. I cite it as opinion.

Judgement, not a fetched fact: I ship Catppuccin, Tokyo Night, Rose Pine, Gruvbox and
Nord because they appear in both signals or have very wide port coverage, and I hold
Dracula, Solarized, Kanagawa, Everforest, Ayu, Night Owl and Nightfox for later. Adding
a theme must not require a code change, so this list is cheap to extend and nothing is
lost by starting small.

### 2.2 Contrast measurement - executed

WCAG 2.1 requires 4.5:1 for normal text and 3:1 for large text
(<https://www.w3.org/WAI/WCAG21/Understanding/contrast-minimum.html>, which defines
large as "at least 18 point or 14 point bold"), and 3:1 for user interface components
and graphical objects
(<https://www.w3.org/WAI/WCAG21/Understanding/non-text-contrast.html>, which also warns
that "computed values should not be rounded").

Terminal text is normal text. It is never large text. So **every foreground role over
every background it is drawn on must reach 4.5:1**, and only the pure UI roles
(`border`, `border-focus`, `fg-subtle`, `comment`) may use 3:1.

I ran a WCAG 2.1 relative-luminance check over 25 role pairs per theme, for all 11
palettes shipped in the mock. Result:

| Theme | Pairs passing | Notable failures |
|---|---|---|
| mivia-dark | 25 of 25 | none |
| mivia-light | 25 of 25 | none |
| catppuccin-mocha | 24 of 25 | `border` on `bg` 1.80 |
| catppuccin-latte | 12 of 25 | `string` on `bg-inset` 2.75, `type` 2.15, `warning` 2.31, `success` 2.96 |
| tokyonight-night | 19 of 25 | `fg-muted` on `bg` 2.76, `fg-subtle` on `bg` 1.97 |
| tokyonight-day | 2 of 25 | `fg` on `bg-inset` 3.52, `accent` on `bg` 3.33, and 21 more |
| rose-pine | 22 of 25 | `keyword` on `bg-inset` 2.91 |
| rose-pine-dawn | 7 of 25 | `warning` on `bg` 2.05, `string` on `bg-inset` 1.87 |
| gruvbox-dark | 22 of 25 | `danger` on `bg` 4.29 |
| gruvbox-light | 15 of 25 | `type` on `bg-inset` 2.20, `string` 2.83 |
| nord | 20 of 25 | `danger` on `bg` 3.05, `fg-subtle` on `bg` 1.69 |

Four conclusions follow, and they are the most useful output of this research.

1. **The Mivia default passes everywhere. No upstream theme does.** The two themes I
   designed are the only ones that reach 25 of 25. This is not an accident of taste; I
   iterated the palette against the checker.
2. **`border` on `bg` fails 3:1 in every upstream theme**, from 1.27 (rose-pine-dawn) to
   1.80 (catppuccin-mocha). Upstream borders are deliberately faint. WCAG 1.4.11 applies
   to "visual information required to identify user interface components and states", so
   a purely decorative rule is exempt. **The design must therefore never carry state in
   `border` alone.** It does not: the design uses almost no borders, and `border-focus`
   carries focus and does meet 3:1 in the Mivia themes.
3. **Light variants are much worse than dark variants**, and by a large margin:
   tokyonight-day scores 2 of 25 and rose-pine-dawn 7 of 25, against 19 and 22 for their
   dark siblings. Upstream light themes are tuned for a syntax-highlighted editor with a
   large font, not for dense terminal text. This is direct evidence for the build spec's
   requirement that light and dark be separately tuned palettes rather than one palette
   with an inverted background.
4. **A contrast test that hard-fails the build cannot be applied to third-party
   palettes.** It would reject nine of the eleven. The workable rule: hard-fail on
   first-party themes (`mivia-dark`, `mivia-light`), and emit a warning plus a
   machine-readable report for embedded upstream themes. Shipping Catppuccin means
   shipping Catppuccin, not a re-tinted variant that no longer matches the user's editor.

The checker is 40 lines of arithmetic over the sRGB relative-luminance formula. Porting
it to Go for the real test is trivial and is the right gate.

---

## 3. Prior art on the hard parts

### 3.1 Inline versus alternate screen

The alternate screen is xterm private mode 1049, which "Save[s] cursor as in DECSC ...
After saving the cursor, switch[es] to the Alternate Screen Buffer, clearing it first",
and the older mode 47, which switches buffers without saving the cursor
(<https://invisible-island.net/xterm/ctlseqs/ctlseqs.html>). The buffer is cleared and
is not part of scrollback, which is the mechanical reason an alt-screen app loses native
scrollback and native selection.

Bubble Tea v2 supports both, and the choice moved from a program option to a declarative
field: v1's `tea.WithAltScreen()` option and the `tea.EnterAltScreen` and
`tea.ExitAltScreen` commands are replaced by setting `view.AltScreen = true` inside
`View()` (<https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md>).
That guide also documents `tea.WithColorProfile(p)` to "force a specific color profile
(great for testing)" and a `view.BackgroundColor` field.

This matters for the design: because alt-screen is a per-frame declarative field, mixing
inline and modal is a state transition in `Update`, not a program restart. The wireframe
structure - inline transcript, alt-screen only for the theme picker and the diff pager -
is directly supported and cheap.

Bubble Tea itself is 44,444 stars, MIT (<https://github.com/charmbracelet/bubbletea>).

### 3.2 Streaming-token repaint cost

Bubble Tea v2 replaces the v1 renderer with a cell-based diffing renderer built on
Ultraviolet. Ultraviolet's README states it "only redraws what changed. Optimizes cursor
movement, uses ECH/REP/ICH/DCH when available, and supports scroll optimizations"
(<https://raw.githubusercontent.com/charmbracelet/ultraviolet/main/README.md>, MIT).
Charm's v2 post calls it the "Cursed Renderer", says it is "modeled on the ncurses
rendering algorithm", and claims rendering is "faster and more efficient by orders of
magnitude", with the note that over SSH "the changes are monetarily quantifiable"
(<https://charm.land/blog/v2/>). The v2 upgrade guide removes `tea.WithANSICompressor()`
because "the new renderer handles optimization automatically".

Judgement, not a fetched fact: a cell-diffing renderer lowers the cost of a repaint, but
it does not lower the cost of *deciding* to repaint. One `tea.Msg` per token is still one
`Update` and one `View` per token, and `View` must re-render the whole visible frame
before the diff can find that four cells changed. Batching deltas on a fixed tick is
therefore still required, and it is cheap. This is a design constraint on the variants:
a variant whose frame is expensive to build pays that cost at the flush rate, so the
number of lines `View` must produce per flush is the real streaming metric. That is the
basis for the "streaming repaint cost" column in the comparison table.

The inline case has a second, sharper constraint that alt-screen does not: text that has
already scrolled into the terminal's scrollback **cannot be repainted at all**. So the
inline design must treat committed transcript lines as immutable and confine all repaint
to the tail. Every variant in `wireframes.md` obeys this: only the last partial line and
the transient status line are ever redrawn.

### 3.3 Approval prompts

I did not find a citable standard for terminal approval prompts, and I will not invent
one. The two sourced inputs are crush's notification ladder
("auto, native, osc, bell, or disabled",
<https://raw.githubusercontent.com/charmbracelet/crush/main/README.md>) and lazygit's
single-key action model
(<https://raw.githubusercontent.com/jesseduffield/lazygit/master/README.md>).

Judgement, stated as such: the approval prompt is the only irreversible decision in the
loop, so it is the one place where the design spends space rather than saving it, and it
must never depend on colour to distinguish "deny" from "always". Every variant labels
the four decisions with both a key and a word.

### 3.4 Diff rendering

delta is the sourced reference; see section 1.4. The transferable findings are:
word-level highlighting via edit inference, `n`/`N` hunk navigation, side-by-side as an
option rather than a default, and theme selection tied to terminal background detection.

---

## 4. Design principles derived from the above

Each principle names the evidence it comes from.

1. **The sink chooses the renderer.** TTY gets the inline UI, a pipe gets the plain
   stream, `--output json` gets JSON. From `gh`'s `autocolor` and its
   connected-to-a-terminal pretty-printing (1.2).
2. **Committed transcript lines are immutable.** Only the tail and the transient status
   line repaint. From the scrollback semantics of xterm mode 1049 (3.1).
3. **Batch text deltas on a tick.** The cell renderer makes the repaint cheap, not the
   decision to repaint. Judgement built on 3.2.
4. **Colour is never the only signal.** Every state carries a word as well as a colour.
   From lazygit's semantic colour code (1.3) plus the `NO_COLOR` requirement, which says
   a non-empty `NO_COLOR` "prevents the addition of ANSI color" and that command-line
   arguments should override it (<https://no-color.org/>).
5. **Themes are data, and a role may defer to the terminal.** From k9s skins and their
   `default` colour (1.6).
6. **Light and dark are separately tuned.** From the measured 2-of-25 and 7-of-25 scores
   of upstream light variants (2.2).
7. **Never carry state in `border` alone.** From `border` failing 3:1 in all eleven
   upstream palettes (2.2) against WCAG 1.4.11.
8. **Hard-fail contrast on first-party themes; warn on third-party.** From 2.2,
   conclusion 4.
9. **Discovery is a prefix key, not a mode.** From helix's `Space` menu (1.8), with
   modality rejected because the composer must always be a composer.
10. **Density is a user setting.** From crush's `compact_mode` (1.1) and k9s's `Ctrl-W`
    (1.6).
11. **Spend space on the irreversible decision.** Judgement, section 3.3.
12. **Word-level diff, unified by default, side-by-side gated on width.** From delta
    (1.4) and the 120-column table in `wireframes.md`.

---

## 5. Open findings to carry into implementation

1. Gruvbox has no LICENSE file. Resolve before embedding. Section 2.
2. Tokyo Night is Apache-2.0, not MIT. It needs NOTICE handling. Section 2.
3. Five theme roles are missing from the supplied list: `bg-selection`,
   `diff-add-emph-bg`, `diff-del-emph-bg`, `gutter`, `link`, `fg-inverse`. See the table
   in `wireframes.md` section 7.
4. The contrast gate cannot hard-fail third-party themes. Section 2.2, conclusion 4.
5. `docs/OWNERS.yaml` is an existing file and this task must not modify it. These three
   files therefore have no owner entry yet. Add one in a follow-up change.
6. Deuteranopia and protanopia simulation is required by the build spec and **was not
   run** in Phase 0. Principle 4 (colour is never the only signal) reduces but does not
   remove the risk. Run the simulation when the theme package lands.
7. The mainstream coding agents were not analysed from a citable source. Section 1.9.
8. **Nearest-RGB downsampling breaks the accent at 16 colours.** The mock quantises the
   palette in the browser to preview the degradation ladder. At 24-bit the Mivia accent
   is `#f0a860`. At 256 it becomes `#ffaf5f`, which is correct. At 16 the nearest ANSI
   colour by squared RGB distance is `#c0c0c0`, silver - the accent stops being an
   accent. The real theme package must carry an **explicit per-theme 16-colour map**
   (here: ANSI yellow), not a computed nearest match. Measured in the mock, not fetched.
9. Nord ships no light variant. The mock falls back to its dark palette and the light
   button has no effect. The real picker must either hide the light toggle for such a
   family or borrow a neutral light base. Decide in the theme package.

---

## 6. Variant comparison and recommendation

The three variants are drawn in `wireframes.md` sections 3, 4 and 5 and are rendered
side by side in `mivia-ui-mock.html`. Scores are 1 (poor) to 5 (good). They are my
judgement, and the "basis" column names the evidence each rests on.

| Criterion | A Ledger | B Blocks | C Console | Basis |
|---|---|---|---|---|
| Scannability at high output volume | 3 | 5 | 4 | B's gutter and labelled headers give the eye a fixed left edge; A relies on indentation alone and blurs when 20 tool calls run |
| Behaviour at 80 columns | 5 | 3 | 5 | B spends 2 of 80 columns on the gutter at every nesting level; that is the difference between a wrapped diff line and a clean one (wireframes.md B.6) |
| `NO_COLOR` / ASCII-only | 5 | 4 | 4 | all three carry a text token beside every colour (principle 4); A degrades best because it never used a glyph for structure; B's gutter and C's progress bar are the only glyph-dependent parts |
| Streaming repaint cost | 5 | 3 | 5 | the metric is lines `View` must build per flush (section 3.2). A and C repaint one partial line plus the status line. B must also rebuild the enclosing block frame, and a collapsed-block model means the frame is stateful |
| Implementation cost | 5 | 2 | 3 | B needs per-block focus, collapse state, `Tab` traversal and a hit-test - that is a component tree. C needs a palette, a pager and an alt-screen router, but the transcript renderer stays trivial |
| Survives very long tool output | 2 | 4 | 5 | A prints 4,000 lines of `go test` output into scrollback and the conversation is gone. B collapses it to one row. C never prints it inline at all |

### Confidence

```
[##########################......]  81%
```

81%. The evidence is strong on the mechanical criteria (column budget, repaint cost,
contrast) because those are measured or sourced. It is weaker on scannability, which is
a judgement I could not source, and the mainstream-agent comparison is missing entirely
(section 1.9). A user test of the mock would move this number more than more research.

### Recommendation: variant A - Ledger, with two grafts from B and C

Take **A** as the base, and graft in exactly two things:

1. **From C: the reference row for long output.** Any tool result over a threshold
   prints a one-line reference with a handle, and the full output opens in the
   alt-screen pager. This closes A's single worst score, "survives very long tool
   output" (2 of 5), and it is cheap because the pager already exists for the diff.
2. **From B: the collapsible diff and the labelled tool row.** Keep B's tool result row
   shape (name, path, status, duration, delta) because it is the best-scanning line in
   the three designs, but **drop the gutter**. The gutter is where B's cost lives: two
   columns, a stateful frame and a component tree.

Why A and not B, tied to the constraints rather than to taste:

- **The inline constraint punishes B most.** Committed lines cannot be repainted
  (principle 2, from the xterm mode 1049 semantics in 3.1). A collapsible block whose
  content has already scrolled into scrollback cannot actually collapse. B's central
  idea works in alt-screen and only partly works inline. That is a structural mismatch
  with a fixed constraint, not a preference.
- **80 columns is the design target and B spends 2.5% of it on chrome** at every level,
  against a brief that says to use typographic hierarchy rather than borders
  (build spec 3.2). B's gutter is a border drawn one character wide.
- **A has the lowest streaming cost**, and streaming is the most frequent frame in the
  product. C ties on this but pays elsewhere.

Why A and not C: C is the strongest design for a heavy-output day and the weakest for
reading a conversation. Its transcript is a table of references, so the model's actual
work is one keystroke away at all times. That is the right trade for an operations tool
such as k9s and the wrong one for a conversation. Taking C's reference row only for
*long* output gets most of C's benefit at none of its cost.

**What the recommendation trades away.** A is the least scannable design at high volume
even after the grafts, because it has no fixed left edge. If a user test shows people
losing their place in a busy transcript, the fix is to add B's block header without B's
gutter, and that is an additive change to A. A is also the least impressive design in a
screenshot. That is an acceptable cost for a tool people read for hours.

**What is deliberately not decided here.** The `--compact` density setting (principle
10), the exact long-output threshold, and the 16-colour map from finding 8 are
implementation choices, not design choices.
