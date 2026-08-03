# 51.03 - Structure-aware search results

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-03). Re-scoped:
the seen-ledger half is **split out to plan `10`**, and the outline scanner
is **deleted in favour of reusing `internal/codeintel`**.
**Date:** 2026-08-02, revised after challenge 2026-08-03
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing. Stage A and Stage B are independently landable.
**Blast radius:** **MEDIUM**, downgraded from HIGH - the `path:line:text`
contract has one real consumer, not "every prompt and skill".

## 0. Step 0 disposition (hostile challenge)

Panel verdict: **DO-NOT-BUILD as written; SHOULD-SPLIT.** Both lenses
independently reached the same conclusion by different routes.

### 0.1 The founding premise was false

| Finding | Severity | Disposition |
|---------|----------|-------------|
| **Rule 60 does not exclude `codeintel`.** Three model-facing tools use it today: `list_symbols` (`internal/tools/default_registry.go:333` wiring `codeintel.FileOutline`), `find_references`, `go_to_definition` (`internal/tools/go_to_definition.go:10`). They satisfy rule 60 via an explicit degrade string (`list_symbols.go:61`). The plan's premise - "no parser is available, therefore hand-roll one" - is wrong | BLOCK | **Accepted.** §4.1's outline scanner is **deleted**. |
| `codeintel.FileOutline` (`outline.go:21`) and `snapshot.declSpan` (`span.go:45`) **already answer §4.1's exact question**: nearest enclosing declaration and its span. Neither was mentioned in the plan | BLOCK | **Accepted.** Reuse before build. |
| The **byte span cannot be consumed by any tool in the repo**: `read_file`'s `offset`/`limit` are 1-based **lines** (`read.go:36,46-53`). The plan's own example emits `1840-1902` bytes. The follow-up read it exists to make exact remains impossible, and §9 forbade the only fix | BLOCK | **Accepted.** Emit a **line** span, matching `codeintel.Symbol.Line/EndLine`, which `read_file` consumes directly. |
| §4.2 ranking needs two inputs `internal/tools` cannot reach: there is no `internal/git` package and zero non-test git invocations repo-wide; tools are built from `workspace.Root` alone and hold no session state | BLOCK | **Accepted.** Git recency and session proximity are **deleted**. Match density is computable from data the walk already holds. |
| §6.1 was called the hardest decision on the basis of a **fictional consumer set**. `grep -rn "path:line"` returns only `search.go:65` (the Description) and one test comment. `internal/cli/prompt.go` names `grep` but never its output shape; TUI code switches on tool *name* only | MEDIUM | **Accepted.** Blast radius downgraded to MEDIUM; the "separate tool" and "opt-in param" options are over-engineering for a one-line contract. |
| INV-CE-03-B ("every emitted span contains the match line") is **trivially satisfiable** by returning `0-filesize` for every match. There was no tightness invariant at all | BLOCK | **Accepted.** Replaced with a precision invariant against an AST oracle, §5. |
| The brace/indent scanner fails on ordinary inputs: CRLF offset arithmetic (`bufio.Scanner` strips `\r`, so `len(line)+1` undercounts 1 B/line and the span can end *before* the match), `msg := "}"`, `// }`, raw strings containing `func f() {`, heredocs, and `Scanner`'s 1 MiB token cap on minified files | BLOCK | **Moot** - scanner deleted. Retained as the evidence that hand-rolling was the wrong call. |
| `generic_surface_test.go` is a **regex denylist over `Description()` text only** (`:24-40`). A per-extension table naming `.go`/`.py` in *code* trips nothing, so INV-CE-03-C rested on a guard that does not guard | MEDIUM | **Accepted.** The real guard is degrade-to-plain behaviour, tested as behaviour. |
| Three unrelated defects bundled behind one number with three dependency profiles, which the plan itself admitted | MEDIUM | **Accepted.** Split: A, B here; the ledger becomes plan `10`. |
| §6.5, the plan's self-declared principal risk, is **worse than stated**: `49` shipped and its elision notice carries no reference (`planner_elision.go:127-131`), so a ledger would claim "shown at step 14" for content that is gone and unrecoverable | BLOCK for the ledger half | **Accepted.** Moved to `10` and blocked there. |
| Per-match reference substitution is **anti-economic** for grep: a `ref:` is 75 B, a match line is ~40-80 B, so substitution can *increase* size | BLOCK for the ledger half | **Accepted.** Moved to `10` §5, which excludes grep. |
| Baseline errors: the `path:line:text` contract is minted at `search.go:209` and stated at `:65`, not `:61`; the byte-cap rationale is `search.go:19-23`; **`truncationReserve` does not cover the pagination trailer or `walkErrors.notice()`, so grep output already exceeds `maxBytes` today** | Confirmed | Corrected in §2. The reserve gap is overview defect §8.7 and a precondition here. |

### 0.2 Locked thesis

The valuable, cheap, independently landable piece is: **attach the
enclosing symbol and its line span to each search hit, by calling code that
already ships.** Everything else in the original plan was either
unreachable (git/session ranking), unconsumable (byte spans), duplicative
(the outline scanner) or blocked (the ledger).

## 1. Goal

Make a search hit carry the enclosing symbol and a line span, so the
follow-up read is exact rather than guessed - reusing `internal/codeintel`,
and degrading to today's output where it is unavailable.

## 2. Verified baseline (re-read at Step 0)

- `grep` mints `path:line:text` at `internal/tools/search.go:209`; the
  contract is stated to the model at `:65`.
- Dual caps: `maxMatches` and `maxBytes`, with the rationale at
  `search.go:19-23`. Truncation is by **walk order**; `walkGrep` aborts at
  `errMaxBytes`/`errMaxMatches` (`search.go:214-221`), so losing matches are
  never materialised.
- `truncationReserve` (`search.go:33-38`) reserves for the **two truncation
  notices only**. The pagination trailer (`search.go:133-136`) and
  `walkErrors.notice()` (`glob_match.go:174-182`, which embeds an arbitrary
  path and error string) are appended after the budget check at `:144-146`.
  **Grep output already exceeds `maxBytes` today.**
- `offset`/`limit` paginate the already-collected slice, which is already
  byte-truncated: `totalFound` (`search.go:118`) counts survivors only, the
  "N more matches" trailer under-reports, and no `offset` can reach a match
  dropped by `errMaxBytes`.
- `codeintel.FileOutline` (`outline.go:21`), `snapshot.declSpan`
  (`span.go:45`), `codeintel.Symbol` with `Line`/`EndLine`
  (`symbols.go`), and a cache (`cache.go`) all ship.
- `codeintel` is Go-only and returns `ErrUnavailable` without a `go.mod`
  (`analyzer.go:54`). Model-facing tools already handle that by degrading
  with an explicit string (`list_symbols.go:61`).
- `read_file` takes 1-based **line** offset and line limit
  (`read.go:36,46-53`).
- No `internal/git` package; no session state reachable from
  `internal/tools`.

## 3. The defects (refined)

**3.1 No structure.** `path:line:text` says where a token appears, not what
it is part of. The model's recovery is a speculative `read_file` with a
guessed line window - typically twice. The harness can know the enclosing
declaration and its line range at match time.

**3.2 Quality-blind truncation.** When the byte budget binds, survivors are
whatever the directory walk reached first, effectively alphabetical.

Not defects this plan addresses: re-delivery of already-seen content
(plan `10`), and the notice-reserve gap (a precondition, §4.3).

## 4. Locked design

### 4.1 Stage A - enclosing symbol and line span, by reuse

For each match, ask `codeintel` for the nearest enclosing declaration and
its line range, and emit:

```
internal/tools/search.go:209:grepTool.executeGrep:196-224: <match text>
```

- Structure comes from `codeintel.FileOutline`/`declSpan`. **No new
  parser, no regex table, no nesting model.**
- `ErrUnavailable`, an unknown language, or any per-file failure yields
  today's `path:line:text` for that match. Degradation is per file, never
  per repository.
- The span is **lines**, so `read_file` consumes it directly.
- Results are cached through the existing `codeintel` cache; a match-heavy
  grep must not re-analyse one file per hit.

Non-Go languages are added to `codeintel` behind its existing
`ErrUnavailable` seam **only when a driver appears** - not speculatively,
and not in this plan.

### 4.2 Stage B - match-density ranking

Rank by **match density per file** (matches ÷ file size) before the byte
budget cuts, and truncate the low-density tail.

Deleted from the original design: git recency and session proximity. Both
require boundaries `internal/tools` does not have, and both would make
output non-deterministic across sessions, breaking any digest-keyed
consumer.

Ranking applies **only when the budget binds**; complete results keep walk
order. The panel's objection that ranking cannot see matches the walk never
materialised (`search.go:214-221`) is real and bounds the ambition: Stage B
ranks *within* the collected set. It reorders which survivors are shown
first; it does not recover better matches from an aborted walk. That is a
smaller claim than the original plan made, and it is the true one.

### 4.3 Precondition

Stage A adds per-match bytes. It must not ship on top of an accounting
error. **Fix the notice reserve first** so it covers the pagination trailer
and `walkErrors.notice()`, with a test asserting `len(out) <= maxBytes`
across truncation × pagination × walk-error combinations. Overview §8.7.

### 4.4 Output format

One field inserted between line and text. The contract lives in exactly one
place the model reads (`search.go:65`) and one place it is minted
(`search.go:209`). No opt-in parameter (schema mass, and the model must know
to ask), no separate tool (duplicate walk).

## 5. Invariants

- **INV-CE-03-A.** Structure never fails a search. Any `codeintel` error,
  unavailability, or unknown language yields today's output for the
  affected file.
- **INV-CE-03-B** (restated for precision). For a file `codeintel` can
  analyse, the emitted span is the **smallest enclosing declaration range**
  containing the match line, verified against `codeintel`'s own AST as the
  oracle. The original containment-only wording was satisfiable by
  `0-filesize` and is retracted.
- **INV-CE-03-C.** Rule 60 compliance is proved by **behaviour** - degrade
  to plain output on unavailability - not by the absence of language names
  in a description string. `generic_surface_test.go` is a denylist over
  `Description()` and does not establish this on its own.
- **INV-CE-03-D.** Total output never exceeds `maxBytes`, structure fields
  and every trailer included. Requires §4.3 first.
- **INV-CE-03-G** (restated). Ranking is a pure function of the tool
  arguments and the scanned file bytes. No session, git, or clock signal
  participates.

INV-CE-03-E and -F moved to plan `10` with the ledger.

## 6. Closed decisions (were open)

| # | Decision | Lock |
|---|----------|------|
| 1 | Output format / breaking change | **Inline field.** One consumer, not many. MEDIUM not HIGH |
| 2 | Rank-when-truncated vs always | **Only when the budget binds** |
| 3 | Per-extension regex table maintainability | **Moot** - deleted, reuse `codeintel` |
| 4 | Pure-Go tree-sitter ports | **Moot** - `codeintel` already ships and is already model-facing |
| 5 | Seen-ledger stale across compaction | **Split to plan `10`**, blocked there |
| 6 | Ledger scope | **Split to plan `10`** |

## 7. Delivery slices

1. **Precondition:** repair `truncationReserve` to cover every trailer.
2. Stage A: enclosing symbol + line span via `codeintel`, degrading per
   file. Format change to `Description()` and the mint site together.
3. Stage B: match-density ranking when the budget binds.

Stages A and B are independently landable and independently valuable.

## 8. Required tests

| Test | Asserts |
|------|---------|
| Emitted span is the smallest enclosing declaration, against `codeintel` as oracle | INV-CE-03-B |
| Non-Go file, binary file, missing `go.mod`, `codeintel` error → today's plain output for that file only | INV-CE-03-A |
| `len(out) <= maxBytes` across truncation × pagination × walk-error | INV-CE-03-D, §4.3 |
| Structure-bearing output at maximum field length still fits the budget | INV-CE-03-D |
| Ranking deterministic across repeated runs and walk orders; identical bytes | INV-CE-03-G |
| Non-truncated results unaffected by ranking | §4.2 |
| Match-heavy grep over one file analyses it once | cache use |
| `generic_surface_test.go` still passes | rule 60 string guard |
| Degrade behaviour asserted directly, not via description text | INV-CE-03-C |

## 9. Plan scorecard

| Criterion | Result |
|-----------|--------|
| Reuses an existing element rather than building | PASS (was FAIL) |
| Span is consumable by an existing tool | PASS (was FAIL) |
| All design inputs reachable from the package | PASS (was FAIL) |
| Invariants non-trivial | PASS (was FAIL) |
| Independently landable stages | PASS |
| Blast radius evidence-based | PASS |

## 10. Rollback criterion

Revert if a structure-bearing result exceeds `maxBytes`, if any file's
analysis failure degrades more than that file, or if the emitted span does
not contain the match line.

## 11. Residual risk

- Structure is Go-only until a driver justifies extending `codeintel`.
  Every other language gets today's output. That is the honest cost of
  refusing to hand-roll a parser.
- Stage B cannot rank matches an aborted walk never produced. The gain is
  ordering within the surviving set only.

## 12. Out of scope

- The seen-ledger and dedup - **plan `10`**.
- A repo-wide symbol map or PageRank ranking.
- Semantic or embedding-based ranking (`01`, `02`).
- Changing `read_file` window semantics.
- Extending `codeintel` to new languages.
