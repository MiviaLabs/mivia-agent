# 51.03 - Addon 2: structure-aware and seen-aware search results

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** the seen-ledger half depends on `08` (shaping stage) and
`07` (references). The structure half depends on nothing and can ship
alone.
**Blast radius:** HIGH - `grep` output shape is consumed by the model on
almost every task, and by every prompt and skill that assumes
`path:line:text`.

## 1. Goal

Make a search result carry enough structure that the follow-up read is
*exact*, rank what survives truncation by usefulness rather than walk order,
and stop re-delivering content the session has already delivered.

## 2. Verified baseline

- `grep` returns `path:line:text`, one match per line
  (`internal/tools/search.go:61`, and its `Description()` states that
  contract to the model).
- It caps by match count (`maxMatches`) **and** bytes (`maxBytes`), with a
  documented reason: a count cap bounds no number of bytes because paths can
  approach `PATH_MAX` (`search.go:20-24`).
- Truncation is by **walk order**. `walkGrep` stops on `errMaxMatches` or
  `errMaxBytes`; `offset`/`limit` paginate the already-collected slice.
- `truncationReserve` pre-reserves bytes for whichever notice may be
  appended, so notices are paid from the budget - an existing pattern this
  plan must reuse rather than reinvent.
- `codeintel.Analyzer` type-checks with `golang.org/x/tools/go/packages`
  and is **Go-only** (`internal/codeintel/analyzer.go`). It requires a
  `go.mod` at the workspace root and returns `ErrUnavailable` otherwise.
- `.mivia/rules/60-tools-project-language-generic.md` forbids baking a
  language into model-facing tool behaviour, and is enforced by
  `internal/tools/generic_surface_test.go`.
- The module is pure Go, no cgo. Tree-sitter's canonical bindings are cgo.
- `contentref.Reference` is the single canonical digest minter
  (`internal/contentref/contentref.go`).

## 3. The three defects

**3.1 No structure.** `path:line:text` tells the model *where* a token
appears but not *what it is part of*. The model's standard recovery is a
speculative `read_file` with a guessed window around the line - often twice,
once too narrow and once too wide. The harness knew the enclosing span at
match time and threw it away.

**3.2 Truncation is quality-blind.** When the byte budget binds, the matches
that survive are the ones the directory walk reached first - effectively
alphabetical. There is no reason to believe those are the useful ones. Aider
solved the equivalent problem with a def/ref graph ranked by PageRank and
rendered to a token budget; the ranking, not the parsing, is the part that
matters here.

**3.3 Re-delivery.** Nothing tracks that a given file span has already been
put in front of the model. A repeated `grep`, an overlapping `read_file`, or
a re-run `run_command` pays full price for content already in context.

## 4. Design

### 4.1 Structure: a generic outline scanner, not a parser

Constraints exclude both available parsers: `go/packages` violates rule 60,
tree-sitter violates the no-cgo constraint. The portable substitute is a
**line-oriented outline scanner**:

- A per-extension table of declaration-line patterns plus a nesting model
  (braces, or indentation for indentation-structured files).
- It answers exactly one question: for line N, what is the nearest enclosing
  declaration line, and what byte span does that declaration cover?
- **Unknown extension means no structure**, not a guess. The scanner
  degrades to today's output. This is what keeps rule 60 satisfiable: the
  tool's contract is "structure when we can, plain when we cannot", which
  is language-generic even though the table is not.

Output gains an enclosing-symbol field and a byte span:

```
internal/tools/search.go:61:12:grepTool.Name:1840-1902: func (t *grepTool) Name() string { return "grep" }
```

The exact field order and separator is an open decision (§6.1) - it is a
breaking change to a contract the model, the prompts, and the skills all
depend on.

**The span is the real saving.** With it, the follow-up read is exact
instead of guessed, which removes an entire speculative round trip per
investigation.

### 4.2 Ranking before truncation

A cheap, deterministic, host-computed rank applied before the byte budget
cuts:

- match density per file (matches / file size),
- path proximity to files already touched in this session,
- git recency, where a repository is available.

No PageRank, no symbol graph, no parse - those need the tag extraction this
plan has already ruled out. The claim is only that *any* defensible ranking
beats alphabetical walk order.

Ranking must not change results when the budget does **not** bind. A tool
whose output reorders unpredictably is harder to reason about, so ordering
stays walk-order for complete results and rank-order only for truncated
ones. Whether that inconsistency is acceptable is §6.2.

### 4.3 The seen-ledger

A per-session map from `contentref` digest to the step at which that content
was delivered. Keys are minted over `(path, span, content)` - one canonical
minter, per `contentref`'s own invariant, never a private hash.

On a repeat delivery, stage 1 of `08`'s pipeline substitutes:

```
ref:output:<64 hex> (identical content shown at step 14)
```

Scope and rules:

- **Insert time only.** History is never rewritten (INV-CE-B). A result
  already sent stays as it was sent.
- **Digest-identical only.** Overlapping-but-different spans are not
  deduplicated; that would require the harness to assert equivalence it
  cannot prove. Possibly-stale state is `09`'s subject, not this plan's.
- Generalises past `grep` to `read_file` re-reads of unchanged files and to
  identical `run_command` output, because the key is content, not tool.

## 5. Invariants

- **INV-CE-03-A.** Structure extraction never fails a search. An
  unparseable or unknown file yields today's `path:line:text` for that
  match.
- **INV-CE-03-B.** Every emitted byte span, when read, contains the match
  line. Tested directly against the file, not against the scanner's own
  model.
- **INV-CE-03-C.** The tool's model-facing description stays project- and
  language-generic (rule 60, `generic_surface_test.go`).
- **INV-CE-03-D.** Notices and structure fields are paid from the existing
  byte budget via `truncationReserve`-style accounting. Adding structure
  must not push a result past its declared budget.
- **INV-CE-03-E.** Dedup substitution happens at insert time only, never
  retroactively (INV-CE-B).
- **INV-CE-03-F.** A substituted reference resolves (INV-CE-07-A). If `07`
  is not built, dedup does not ship.
- **INV-CE-03-G.** Ranking is deterministic for identical inputs, so
  repeated identical searches are byte-identical.

## 6. Open decisions for Step 0

1. **Output format is a breaking contract change.** Every prompt, skill,
   and test that reads `path:line:text` is affected. Options: extra fields
   inline (breaks parsers), an opt-in `structure: true` parameter (adds
   schema mass, and the model must know to ask), or a separate tool
   (duplicates the walk). None is free.
2. Is rank-order-when-truncated / walk-order-otherwise too surprising?
   The alternative is always rank-order, which changes every result.
3. Is a per-extension regex table maintainable, or does it become the
   "spaghetti growth" rule 30 warns about? What is the ceiling on supported
   extensions, and where does the table live so it stays under 500 LOC?
4. Are there pure-Go tree-sitter ports mature enough to reconsider §4.1? If
   one exists and is acceptable, the outline scanner is strictly inferior
   and should be dropped.
5. Does the seen-ledger leak across a compaction boundary? After
   compaction the model may **no longer hold** content the ledger believes
   was delivered - substituting a reference for it would then be a silent
   loss. The ledger must be invalidated or re-scoped at compaction. This is
   the plan's principal correctness risk.
6. Session scope only, or workspace scope across resumed sessions? §6.5
   argues strongly for session scope.

## 7. Delivery slices

1. Outline scanner as a standalone package with no tool wiring: given a
   file and a line, return enclosing symbol and span. Fully testable alone.
2. Ranking, applied only when the budget binds.
3. Output format change, after §6.1 is decided, with prompt/skill/test
   updates in the same change.
4. Seen-ledger and dedup - only after `07`, `08`, and §6.5.

## 8. Required tests

- Span containment (INV-CE-03-B) over a corpus of files in several
  languages, including files with no recognised structure.
- Unknown extension, binary file, and unterminated-nesting inputs all yield
  plain output and no error.
- Budget accounting: structured output with the maximum-length structure
  fields still fits the declared budget.
- Ranking determinism across repeated runs and across walk orders.
- Non-truncated results are unaffected by ranking.
- Dedup: second identical delivery substitutes; a one-byte-different
  delivery does not.
- Dedup after compaction does not substitute content the model no longer
  holds (§6.5).
- `generic_surface_test.go` still passes - no language names in the
  description.

## 9. Out of scope

- A repo-wide symbol map or PageRank ranking. That needs real tag
  extraction; revisit only if §6.4 changes.
- Semantic or embedding-based search ranking (see `01`, `02`).
- Changing `read_file`'s window semantics.
- Cross-session dedup.
