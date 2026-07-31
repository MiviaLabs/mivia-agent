# P2.2 — Unify diff-line coloring (`diff_style.go`)

**Status:** DESIGN-READY — implementation must pass ADLC Step 0 before code is
written. REFACTOR; TDD-preserving (no behavior change to existing tests).
**Date:** 2026-07-31
**Source finding:** P2.2 in `.mivia/reports/cli-internal-refactoring-review.md`
(render slice).
**Depends on:** `p1-1-color-style-theme.md` (the theme module **must exist first**).
This plan consumes theme tokens produced by P1.1; it cannot be implemented until the
semantic color/style tokens (diff add/del/header/context backgrounds + foregrounds)
exist in the theme module. Do not start P2.2 before P1.1 lands and its tokens are
importable from `internal/cli`.
**Blocks:** nothing downstream.
**Blast radius:** MEDIUM — diff output is model-visible in several surfaces
(markdown streamer, syntax highlighter, tool result preview, collapsed edit block).
The risk is visual/behavioral drift across surfaces, not data or security.

---

## 1. Problem

The classify-by-prefix (`+++/---/@@/+/-`) → color logic is implemented **four times**
in `internal/cli`, and the implementations **disagree** on hunk-header coloring:

| # | Location | Function | `@@` color | `+++/---` | `+` | `-` |
|---|---|---|---|---|---|---|
| 1 | `highlight.go:329` | `highlightDiffLine` | **magenta** | bold cyan | green/green-bg | red/red-bg |
| 2 | `markdown.go:311` | `MarkdownWriter.formatCodeLine` | **magenta** | bold cyan | green | red |
| 3 | `toolui.go:356` | `colorDiffLine` | **dim** | cyan header | green/green-bg | red/red-bg |
| 4 | `diff_render.go:9` | `renderDiffBody` (delegates → `colorDiffLine`) | (→ #3 **dim**) | (→ #3 cyan) | (→ #3) | (→ #3) |

The `@@` hunk header is **magenta** in the markdown streamer and syntax highlighter
(#1, #2) but **dim** in the tool-result preview path (#3, #4). `@@` is the only
genuine color *conflict*; the other classes are stylistically inconsistent in
background vs foreground-only rendering but agree on hue.

Two parallel ANSI-constant vocabularies (`ansi*` in `markdown.go:16-31` vs `hl*` in
`highlight.go:11-26`) carry the same values, and `colorDiffLine` (#3) uses lipgloss
`lipgloss.Color(...)` styles (`toolDiffAddBg`/`toolDiffDelBg`/`toolDiffHeader`/…,
`toolui.go:210-231`) instead of ANSI strings. So the duplication spans three
different color representations.

## 2. Goals and non-goals

### Goals
- **One** classify-by-prefix → color renderer: `renderDiffLine(line string) string`
  in a new `diff_style.go`, expressed entirely in **theme tokens** from the P1.1
  module (no raw `lipgloss.Color("8")` / `"\033[36m"` literals in diff code).
- All four call sites route through `renderDiffLine`.
- The `@@` disagreement is resolved to a **single conscious, documented** choice.
- Existing diff tests stay green (smoke + gutter + cap + truncation tests); add a
  focused table-driven test pinning the unified contract.

### Non-goals
- Do **not** re-derive diff windows, redaction (`redactPreview`), clipping
  (`clipPreviewLine`), or the change-centric line windowing (`changeCentricWindow`).
  Those stay where they are; only the per-line coloring is unified.
- Do **not** invent a generic "renderer plugin" abstraction — exactly one function,
  no interface (the review explicitly warns against speculative generality here).
- Do **not** change non-diff code-line rendering (plain `text`/unknown-lang lines,
  numbered lists, inline markdown). Only the diff prefix classifier is in scope.
- Do **not** touch the two ANSI vocabularies themselves — that is P1.1's job. This
  plan *consumes* the theme tokens P1.1 produces; it does not delete `ansi*`/`hl*`.

## 3. Decision required before implementation — the `@@` disagreement

The review calls out that "`@@` is magenta in markdown/highlight but dim in
`colorDiffLine`." This **must** be a conscious choice in the plan, not a silent merge.

**Decision: unify on MAGENTA for `@@` hunk headers.**

Rationale (record in Step 0 challenge record):
- Magenta is used in **three of the four** sites (#1, #2, and conceptually #4 routes
  through the lone dim outlier). Magenta is the majority behavior.
- `@@ ... @@` is a structural/range header (not context, not content). Treating it as
  *dim* (the same class as unchanged context lines, #3) loses the visual distinction
  between "where in the file" and "unchanged code". Magenta preserves that
  distinction, matching how `+++/---` (also structural) get a distinct bold-cyan.
- The markdown streamer (#2) and the syntax highlighter (#1) are the **primary**
  user-facing diff surfaces (assistant output and rendered code blocks). The tool
  preview (#3/#4) is secondary. Aligning the secondary surface to the primary is the
  lower-risk direction.
- It is a strictly additive visual change to one surface (tool preview hunks become
  magenta instead of dim); no information is removed, and it matches existing
  markdown/highlight output the user already sees.

**Accepted cost:** tool-result diff previews will render `@@` lines in magenta
instead of dim. This is a visible change to the preview surface and must be called out
in the Step 0 challenge and the commit message. It is the *intended* resolution of the
conflict, not a regression.

The implementation must add/extend a test asserting `renderDiffLine("@@ ...")`
carries the theme's diff-hunk (magenta) token and **not** the context/dim token, so
the resolution is pinned and cannot silently drift back.

## 4. API

New file `internal/cli/diff_style.go`:

```go
// renderDiffLine renders a single unified-diff line using theme tokens.
// Classification by leading marker (+++/---/@@/+/-/context) is centralized here;
// every diff surface in the package must route through this function.
//
// The + and - prefixes are preserved verbatim in the output (existing gutter tests
// assert their presence). Leading indentation ("  " for headers/context, " " for
// add/del) matches the current behavior of the merged implementations.
func renderDiffLine(line string) string { ... }
```

- Input: one raw diff line (already split, pre-redaction/clipping — redaction and
  clipping happen at the call sites, not here).
- Output: the colored line, as an ANSI string (string, matching #1/#2/#4's
  `fmt.Sprintf` output shape and the existing test's `string` assertions). lipgloss
  styles (#3) are reduced to their ANSI string equivalent via the theme tokens.
- Classification order (same precedence as today, which the tests assume): `+++` and
  `---` checked before bare `+`/`-`; `@@` before context.

All diff tokens are sourced from the P1.1 theme module (e.g. `theme.DiffHeader`,
`theme.DiffHunk`, `theme.DiffAdd`, `theme.DiffDel`, `theme.DiffContext`, and their
background variants). The exact token names are whatever P1.1 ships; if P1.1's token
set is missing a diff-specific entry, **add it to the theme module as part of P1.1's
remaining work**, not by reintroducing a literal here.

## 5. Call-site migration

After `diff_style.go` exists and its RED test passes:

| Site | Current | After |
|---|---|---|
| `highlight.go:329` `highlightDiffLine` | inline `hlBgDark`/`hlMagenta`/… switch | body becomes `return renderDiffLine(line)` |
| `markdown.go:311` `formatCodeLine` (diff branch) | inline `ansiBgDark`/`ansiMagenta`/… switch | diff branches become `return renderDiffLine(line)` |
| `toolui.go:356` `colorDiffLine` | lipgloss `toolDiffAddBg`/`toolDiffHeader`/… switch | body becomes `return renderDiffLine(l)` |
| `diff_render.go:9` `renderDiffBody` | routes headers/add/del through `colorDiffLine` | unchanged routing (still calls `colorDiffLine`, now a thin wrapper) **or** call `renderDiffLine` directly — keep `colorDiffLine` as a one-line alias to avoid touching its test until the alias is removed in cleanup |

`diff_render.go` context-line and `… omitted` handling stays as-is (it uses
`toolDiffCtx`/`toolDimStyle` for those non-classified lines); only the
`+++/---/@@/+/-` branches consolidate.

Migration is mechanical: each site's switch is replaced by a single call. The thin
wrapper/alias approach for `colorDiffLine` keeps `toolui_test.go`'s
`TestColorDiffLine` compiling and green without rewriting it in the same change; that
test is then either updated to call `renderDiffLine` directly or left as a backward-
compat smoke (record the choice in the task).

## 6. Implementation waves (ADLC, TDD)

Every production task follows a compiling RED test that fails an assertion first.
Context scope per task ≤ 5 files. Invariant check (`internal/cli`) before Step 0/4.

| Wave | Scope | Files | Required proof |
|---|---|---|---|
| 0 | Challenge: confirm `@@`=magenta decision, confirm P1.1 theme tokens exist and cover diff, confirm no call site does non-color work in its switch (e.g. gutter prefixing) that `renderDiffLine` would lose | `highlight.go`, `markdown.go`, `toolui.go`, `diff_render.go`, theme module | Architecture + correctness reviews dispositioned; P1.1 dependency verified present; `@@` decision recorded |
| 1a (RED) | Table-driven `TestRenderDiffLine` covering `+++/---/@@/+/-/context/empty` : asserts correct theme token present, `+`/`-` prefix preserved, `@@` carries magenta/hunk token **not** context token | `diff_style_test.go` (new) | Compiles; assertion failure (not compile error) |
| 1b (GREEN) | `renderDiffLine` in `diff_style.go`, all tokens from theme module | `diff_style.go` (new) | `TestRenderDiffLine` passes |
| 2 | Migrate `highlightDiffLine` → `renderDiffLine`; migrate `formatCodeLine` diff branches → `renderDiffLine` | `highlight.go`, `markdown.go` | `go test ./internal/cli -run TestHighlight`; existing markdown diff tests green |
| 3 | Migrate `colorDiffLine` → `renderDiffLine` (thin alias or direct); `renderDiffBody` routing verified | `toolui.go`, `diff_render.go` | `TestColorDiffLine`, `TestDiffBodyGuttersAndTruncationIsExplicit` green; `@@` now magenta in preview path |
| 4 (review) | Hostile review reads all four sites + `diff_style.go`; confirm no leftover literal colors in diff code, no behavior divergence | — | REJECT if any surface still diverges or carries a raw color literal |

Wave 2 and 3 are independent of each other (different files) and may be parallelized
within the wave; both depend on Wave 1. Wave 4 depends on 2+3.

## 7. Tests and verification

Preserved existing tests (must stay green, unchanged where possible):
- `toolui_test.go:190` `TestColorDiffLine` — smoke that header/add/del/context are
  non-empty.
- `presentation_test.go:90` `TestDiffBodyGuttersAndTruncationIsExplicit` — cap (≤22
  lines), explicit "omitted" truncation, and `+added` gutter marker preserved.

New RED→GREEN test (`diff_style_test.go`):
- Table: `{ "+++ b/x.go", "--- a/x.go", "@@ -1,3 +1,4 @@", "+added", "-removed",
  " context", "" }`.
- Assert each output is non-empty, UTF-8 valid, and contains the expected theme token.
- **Pin the decision:** assert `renderDiffLine("@@ ...")` contains the theme diff-hunk
  (magenta) token and does **not** equal the context/dim token's output.
- Assert `+`/`-` prefixes survive into the rendered string (strip ANSI, check leading
  `+`/`-`), matching the gutter test's expectation.
- Assert classification precedence: a line beginning `+++` is treated as a header,
  not as an added line.

Verification order:

```text
go test ./internal/cli -run TestRenderDiffLine -count=1
go test ./internal/cli -run 'TestColorDiffLine|TestDiffBody' -count=1
go test ./internal/cli -count=1
go test -race ./internal/cli -count=1
go vet ./internal/cli
go build ./...
make structure-check
make verify
```

## 8. Rollback

- `renderDiffLine` is additive and each call-site migration is a localized edit, so
  rollback is per-site: revert any call site to its prior inline switch.
- Full rollback = delete `diff_style.go` + `diff_style_test.go` and restore the four
  inline switches. Existing tests pass again unconditionally (they pre-date this
  change).
- The only **behavioral** change (not pure refactor) is the `@@`=magenta decision in
  the tool-preview path (#3/#4). If that change is rejected in review, the rest of the
  unification can land with `@@` temporarily rendered via a *parameter* or a second
  token — but the plan's stated decision is to unify on magenta, so rejecting it
  requires returning to Step 0 to re-litigate §3, not a silent local override.

## 9. Dependency and sequencing note

This plan is sequenced **after** `p1-1-color-style-theme.md`. The unified renderer is
*defined* by consuming theme tokens; building it before the theme module exists would
either reintroduce the very literals P1.1 removes or create a throwaway local token
block. Concretely: P2.2 Wave 0 must verify the P1.1 theme module exposes a complete
diff token set (header/hunk/add/del/context, fg + bg). If P1.1 shipped without a diff
token, add it there first — do **not** add a diff-specific constant block to
`diff_style.go`.
