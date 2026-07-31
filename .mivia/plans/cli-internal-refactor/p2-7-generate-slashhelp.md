# P2.7 — Generate `slashHelp` from the catalog

**Source finding:** P2.7 in `.mivia/reports/cli-internal-refactoring-review.md`
**Status:** DESIGN-READY — implementation must pass ADLC Step 0 before code is written.
**Date:** 2026-07-31
**Depends on:** nothing beyond the shipped `slashCommands(...)` catalog (`slash_catalog.go`) and the shipped TUI reference generator (`tuiHelpCommandsFor`).
**Blocks:** nothing (independent; rides alongside P1.2 if scheduled together, but needs no part of it).
**Blast radius:** LOW — one internal help-string surface, classic REPL only, no API or config change, no cross-package move.

---

## 1. The drift, re-derived at HEAD

The report flagged `chat.go:~198` `const slashHelp`. Re-reading the code shows the drift
is real **and a little wider than the report's single-line note implied**. Two facts that
shape this plan:

### 1.1 There are TWO hand-maintained command listings in the classic REPL

| Surface | Where | What it is | How rendered |
|---|---|---|---|
| **Primary** | `dialog.go:11` `var replHelpContent` | categorized `[]helpSection` | `ShowHelpDialog` → `renderHelpLines` (bordered dialog) |
| **Fallback** | `chat.go:207` `const slashHelp` | one raw backtick string | `displayInlineHelp` (terminal too small) + `chat_slash.go:62` stderr (no terminal) |

Both are hand-maintained, both duplicate the catalog, and **both have drifted.** The report
names only `slashHelp`; the plan covers both because fixing one and leaving the other
re-creates the exact hazard this plan exists to remove.

### 1.2 The two drifts, verified

**Mojibake (P2.7 as filed).** `chat.go:226-227`:

```
  â†‘ â†“                history
  â† â†’                cursor
```

Those are double-encoded UTF-8 — the bytes that result when `↑ ↓` / `← →` are read as
Latin-1 then re-encoded. The sibling `replHelpContent` in `dialog.go` renders the *same*
rows correctly (`"↑ ↓"` / `"← →"`), so the fallback is visibly broken where the dialog is
not. Verified: `grep -nP "[\x{0080}-\x{00FF}]" internal/cli/chat.go` hits exactly those
two lines (and one unrelated, correct box-drawing line at `chat.go:56`).

**Missing `/resume`.** `/resume` is a real, handled catalog command — declared in
`slash_catalog.go` with `Surface: slashSurfaceBoth`, and dispatched in
`chat_slash.go:64` (`case "/resume": return handleSlashResume(...)`). It appears in
**neither** classic-REPL listing:

- `grep resume internal/cli/dialog.go` → no matches (`replHelpContent` omits it).
- `slashHelp` (the fallback) omits it.

The TUI is immune because it generates its command list from the catalog
(`tuiHelpCommandsFor` → `slashCommands(slashSurfaceTUI, …)`), which is exactly the
asymmetry the report points at.

### 1.3 The invariant this fixes

> The classic REPL advertises exactly the slash commands it handles — no more, no less —
> in every surface a user can reach, and never from a second hand-maintained copy of the
> command table.

Today's guardrail (`TestNewInHelpSurfaces`, `new_session_slash_test.go:202`) only checks
that `/new` is *present*. It cannot catch an omission (it asserts existence, not
completeness) and it cannot catch the mojibake at all. The next command to be added would
drift again the same way `/resume` did.

---

## 2. Goals and non-goals

### Goals
- The classic REPL's command listing is **generated from `slashCommands(slashSurfacePlain, …)`**
  in every reachable surface — the bordered dialog, the too-small inline fallback, and the
  no-terminal stderr print — so it can never drift from the catalog again.
- Remove the mojibake by construction (the key rows come from the same generator as the
  command rows; arrows are written once, correctly).
- Add a **completeness test** that asserts the generated help includes every
  `slashSurfacePlain` catalog command, so a future omission fails CI.

### Non-goals (deliberately out of scope)
- Do **not** change command *behavior*, ordering, wording, or which commands exist. This is
  a help-text refactor; `/resume` is already handled, it is only newly *advertised*.
- Do **not** touch the TUI (`tuiHelpCommandsFor` is already catalog-driven and correct —
  it is the *reference*, not a target).
- Do **not** unify the classic REPL and TUI dispatch (that is P1.2, a larger change).
  This plan reuses the catalog as a *read-only source* and does not depend on P1.2 landing.
- Do **not** generate the **editing-keys** section from anything. Those keys
  (`Ctrl+U`, `Ctrl+W`, `Tab`, `Esc`, arrows) are REPL line-editor bindings, not slash
  commands, and the catalog does not describe them. They stay hand-written — but written
  *once*, correctly, in the shared content structure, not twice with one copy mojibake'd.
- Do **not** add skill commands to the classic-REPL listing. Skill commands are
  `slashSurfaceTUI` only (`slashCommands` returns them only for the TUI surface), so a
  `slashSurfacePlain` generator naturally excludes them. No special-casing needed.

---

## 3. The reference implementation already exists

The TUI solved this identically and ships today. The plan mirrors it for the plain surface:

```go
// tui_help_content.go — the pattern to copy (already shipped, correct)
func tuiHelpCommandsFor(registry *skills.Registry) []helpSection {
	commands := slashCommands(slashSurfaceTUI, registry)
	items := make([]helpItem, 0, len(commands))
	for _, command := range commands {
		key := command.Name
		// … append Aliases, ArgsHint …
		items = append(items, helpItem{key: key, desc: command.Description})
	}
	return []helpSection{{title: "Commands", items: items}}
}
```

The classic REPL needs the same thing with `slashSurfacePlain`. `slashCommands` already
returns only the commands whose `Surface & slashSurfacePlain != 0`, so the plain surface
gets `/exit /quit /q`, `/provider`, `/workspace` (plain-only) plus the both-surface
commands, and excludes `/sessions`, `/plain`, `/select` (TUI-only). That is exactly the
correct partition.

---

## 4. Design decision: one shared generator, two renderers

The classic REPL renders help two ways (bordered dialog vs. inline/stderr string) but the
**content** is one thing: a set of sections, each a title plus key/desc items, of which
exactly one section (Commands) is catalog-derived and the rest (Navigation, Configuration,
Editing Keys, …) are hand-written REPL-key documentation.

**Decision: generate the Commands section from the catalog; keep the key sections
hand-written in one `replHelpContent`-style structure; delete `const slashHelp` entirely
and render the inline/stderr path from the same structure the dialog uses.**

This kills the duplication at its root: there is no longer a second string to drift.

| | Option | Assessment |
|---|---|---|
| **A** *(chosen)* | Generate the Commands section from `slashCommands(slashSurfacePlain, …)`; keep key sections hand-written; delete `slashHelp`, render inline/stderr from the section structure | One source of truth for commands; mojibake gone by construction; one place to edit keys. Matches the shipped TUI pattern exactly. Low risk — `replHelpContent` already exists and already feeds the dialog |
| **B** | Generate `slashHelp` as a `string` via `init()` from the catalog, keep both `slashHelp` and `replHelpContent` | Removes command drift but **preserves** the two-listing duplication and keeps a generated string competing with a hand-written slice. Solves half the problem and the other half re-drifts |
| **C** | Hand-fix `slashHelp` (add `/resume`, fix the bytes) and add `/resume` to `replHelpContent` | Cheapest now, zero structural improvement. Re-creates the drift condition; the next command added is missed again. Explicitly rejected by the report ("generate … from the catalog") |

**Why A over B:** the report's framing — "generate the command table portion from
`slashCommands(...)` like the TUI's `newHelpDialogFor` already does" — points at the
generator, but the *reason* is "has drifted". Drift is a symptom of duplication; B leaves
the duplication. A removes it. The TUI has no `slashHelp`-equivalent fallback string, and
that is precisely why the TUI has not drifted.

---

## 5. Changes

All changes are in `internal/cli` (package `cli`). No new files are strictly required —
the generator is small and belongs beside the existing help content — but a new
`slash_help_content.go` is the clean home if the reviewer prefers isolation. Files:

| # | File | Type | Change |
|---|---|---|---|
| 1 | `internal/cli/dialog.go` | modify | Replace the hand-written **Commands** portion of `replHelpContent` with a catalog-generated section. Concretely: add `replHelpCommands()` mirroring `tuiHelpCommandsFor` but over `slashSurfacePlain`, and compose `replHelpContent` (or a new `replHelpContentFor`) as `replHelpCommands()` + the hand-written non-command sections. Non-command sections (Navigation, Editing Keys, …) stay hand-written with correct arrow glyphs (`↑ ↓`, `← →`) — unchanged from today's `dialog.go` |
| 2 | `internal/cli/chat.go` | modify | **Delete `const slashHelp`.** The two consumers (`displayInlineHelp` in `dialog.go`, the stderr branch in `chat_slash.go:62`) now render from the same section structure the dialog uses — add a small `renderReplHelpInline()` (or reuse `renderHelpLines` joined) that flattens `replHelpContent` to a plain string, and point both consumers at it. The mojibake disappears with the string it lived in |
| 3 | `internal/cli/chat_slash.go` | modify | `showSlashHelp`'s no-`term` branch (`fmt.Fprint(os.Stderr, slashHelp)`) switches to the new inline renderer. The `term != nil` branch (`ShowHelpDialog`) is unchanged — it already reads `replHelpContent` via `renderHelpLines` |
| 4 | *(test)* new test, see §6 | add | Completeness test asserting every `slashSurfacePlain` catalog command appears in the generated classic-REPL help, in *both* the dialog-rendered and inline-rendered forms |

**No new types.** `helpSection`/`helpItem` already exist (`dialog.go`). The generator
returns `[]helpItem`, same as `tuiHelpCommandsFor`.

**Ordering note:** items 1–3 are one cohesive change (delete the string, introduce the
generator, repoint two callers). They cannot be split across waves usefully — the package
won't compile if `slashHelp` is deleted but a caller still references it. Implement them as
one RED→GREEN cycle (§7).

---

## 6. Test strategy (TDD — RED before GREEN)

All tests in `internal/cli`. The existing `TestNewInHelpSurfaces` (`new_session_slash_test.go:202`)
is retained (it still passes — `/new` is still present) and is *not* relied upon for
completeness; the new test is strictly stronger.

### New test: `TestReplHelpAdvertisesEveryPlainCommand`

The load-bearing, drift-preventing test. For every command in
`slashCommands(slashSurfacePlain, nil)`:

- Its `Name` appears in the **dialog-rendered** help (`renderHelpLines(...)` joined), AND
- Its `Name` appears in the **inline-rendered** help (the new `renderReplHelpInline()`).

This is the test that would have caught `/resume`'s omission, and will catch any future
omission. It asserts on *both* render paths because the bug was that the two paths
disagreed.

```go
func TestReplHelpAdvertisesEveryPlainCommand(t *testing.T) {
	commands := slashCommands(slashSurfacePlain, nil)
	if len(commands) == 0 {
		t.Fatal("precondition: slashSurfacePlain catalog is empty")
	}
	dialog := strings.Join(renderHelpLines(120), "\n")
	inline := renderReplHelpInline() // new helper
	for _, c := range commands {
		for _, which := range []string{"dialog", "inline"} {
			hay := inline
			if which == "dialog" {
				hay = dialog
			}
			if !strings.Contains(hay, c.Name) {
				t.Errorf("%s help does not advertise catalog command %s", which, c.Name)
			}
		}
	}
}
```

### New test: `TestReplHelpHasNoMojibake`

Guards the specific regression this plan fixes, so it cannot silently return. Asserts the
inline render contains the correct arrow glyphs and contains **none** of the mojibake
byte sequences (`â†‘`, `â†“`, `â†`, `â†’`) that were in `chat.go:226-227`.

### Existing test adjustments
- `TestNewInHelpSurfaces` (`new_session_slash_test.go:226`) asserts `strings.Contains(slashHelp, "/new")`.
  With `slashHelp` deleted, that line must be updated to assert against the new inline
  renderer instead. The assertion's *intent* ("/new is advertised") is unchanged and still
  holds. This is a test-only mechanical edit, not a weakening.

### Mutation proofs (what each test MUST catch)

| # | Mutation | Test that fails |
|---|---|---|
| M1 | Revert `slashSurfacePlain` generator to hand-written list and drop `/resume` | `TestReplHelpAdvertisesEveryPlainCommand` |
| M2 | Reintroduce the `â†‘ â†“` mojibake in the editing-keys section | `TestReplHelpHasNoMojibake` |
| M3 | Make the inline renderer and the dialog renderer read different content sources (the original bug) | `TestReplHelpAdvertisesEveryPlainCommand` (one of the two `which` arms) |

---

## 7. ADLC wave mapping

This is a small, single-package change. It does not warrant multiple waves; it maps to one
TDD cycle under ADLC Step 4, with the challenge/audit steps scaled to size.

| ADLC step | For this plan |
|---|---|
| **Step 0 — Challenge** | One architecture lens: "is generating the plain-surface command list from the catalog correct, given skill commands are TUI-only and `slashCommands` already partitions by surface?" One correctness lens: "do the two render paths (dialog, inline) now read the same content source, and is the inline form a faithful flatten of the dialog form?" |
| **Step 1 — Tasks** | One RED task (the two new tests + the `TestNewInHelpSurfaces` edit, compiling and failing) and one GREEN task (generator + delete `slashHelp` + repoint callers, one cohesive diff). |
| **Step 2 — Validate** | Validator reads `dialog.go`, `chat.go`, `chat_slash.go`, `slash_catalog.go`, `tui_help_content.go` (≤5 files) and confirms the generator signature and caller repointing are implementable as described. |
| **Step 4 — Implement** | RED: tests compile, `TestReplHelpAdvertisesEveryPlainCommand` fails on `/resume` (proving the drift is real and the test sees it). GREEN: generator + delete + repoint; both new tests pass, existing tests pass. |
| **Step 5 — Audit** | Hostile audit on the single diff: did the inline renderer preserve formatting/wrapping well enough for the too-small-terminal path? Did deleting `slashHelp` leave any dangling reference (`grep slashHelp` must return only the updated test)? Is the editing-keys section the *only* hand-written part, and are its glyphs correct? |
| **Step 6 — Verify** | `go build ./... && go vet ./... && go test -race ./internal/cli/...` |

**Fast-path eligibility:** borderline. It is ~3 files but cohesive (delete + introduce +
repoint) with no new types. Treat as one normal TDD cycle rather than the ≤5-line fast path,
because the drift test is the point and must be written first.

---

## 8. Verification

Minimum gates after implementation:

```text
go build ./...
go vet ./...
go test ./internal/cli/... -count=1
go test -race ./internal/cli/... -count=1
make verify
make invariants   # internal/cli is invariant-governed (ADLC: read invariants.md before Step 4)
```

Acceptance checks specific to this plan:

- `grep -nP "[\x{0080}-\x{00FF}]" internal/cli/chat.go` returns at most the one pre-existing,
  correct box-drawing line (`chat.go:56`); the two mojibake lines are gone.
- `grep -rn "slashHelp" internal/cli/` returns **zero** references (the const is deleted; the
  one test line that named it is updated to the new renderer).
- `TestReplHelpAdvertisesEveryPlainCommand` passes, and temporarily removing the generator's
  catalog source (M1) makes it fail.
- `/resume` appears in both the dialog render and the inline render.

---

## 9. Rollback criterion

If generating the command list from the catalog turns out to mis-render for some terminal
size (e.g. the inline flatten wraps badly for the too-small-terminal path, or a
long-description catalog command overflows the dialog column), the fix is the **renderer**,
not a return to hand-maintenance: fix the flatten/wrapping in `renderReplHelpInline` /
`renderHelpLines`. Do **not** restore `const slashHelp` — that restores the duplication this
plan exists to remove and the drift returns with it.

The only condition that justifies reverting the generator itself is a discovery that the
classic REPL genuinely needs to advertise a command it does **not** handle (i.e. the
catalog is wrong, not the help). That is a `slash_catalog.go` bug and is fixed there, not
by re-diverging the help text.
