# 51 - Harness context economics: program overview

**Status:** DESIGN - **ADLC Step 0 complete for members `04` and `05`
(2026-08-02).** Other members remain unchallenged and are not
implementation-ready. Each remaining member still states its own open
decisions; none of those may be built before a hostile challenge closes them.
**Date:** 2026-08-02
**Depends on:** `48` (uncapped-default reliability) for the truncation
semantics that `07` and `08` build on. Coordinates with `49` (compaction
elision tier) - see §4.
**Blocks:** nothing.
**Blast radius:** program-level HIGH, member-level MEDIUM to HIGH. Members
touch the planner, the dispatcher, tool result shape, and durable storage.

## 1. Thesis

Context cost is currently controlled in two places: the structural planner
(`contextmgr.Plan`) at compaction time, and per-tool byte ceilings in the
dispatcher. Everything between those two points - what a tool result *says*,
whether the agent has already seen it, whether the model still needs it - is
uncontrolled, and the model is left to pay for it.

This program moves those decisions into the harness, where they are
deterministic and testable, rather than into the prompt, where they are
advisory. It adds one new capability (passive memory recall) and eight
economy measures over surfaces that already exist.

The organising constraint: **cost truth stays in one function.** Anything
this program injects into the request must be visible to
`provider.EstimatePromptCost` and to the calibration ratio, or the planner's
budget math silently becomes a lie.

## 2. Members

| File | Subject | Blast radius |
|------|---------|--------------|
| `01-passive-memory-v1-lexical.md` | Recall tier, lexical retrieval, no model weights | HIGH |
| `02-passive-memory-v2-static-embeddings.md` | Same seam, static-embedding retriever | MEDIUM |
| `03-structure-and-seen-aware-search.md` | Enclosing symbols, byte spans, ranking, seen-ledger | HIGH |
| `04-split-calibration-ratios.md` | Class-aware token estimation (prose vs structured divisors; single residual EWMA). Multi-ratio deferred (Stage B purity default 80% configurable). **Step 0 locked.** Implement **after `05`**. | LOW product / MEDIUM eng |
| `05-tool-schema-gating.md` | Prove/harden auth-scoped tool schemas (ScopedRegistry); relevance gating deferred. **Step 0 locked 2026-08-02.** Implement **before `04`**. | MEDIUM |
| `06-token-capped-recent-tail.md` | Retire the message-count tail cap | LOW |
| `07-pageable-truncated-results.md` | Truncated remainders become referenceable | MEDIUM |
| `08-dispatcher-result-shaping.md` | A shaping stage before the ceiling check | HIGH |
| `09-supersede-stale-tool-results.md` | Drop superseded results for the same resource | MEDIUM |

`01` and `02` are the same feature at two retrieval qualities. `02` does not
replace `01`'s seam; it replaces only `01`'s `Embedder` implementation.

## 3. Verified baseline

Facts below were read at HEAD `0363ca1` (2026-08-02). Re-verify before
building; do not trust this section as a substitute for reading the code.

- `contextmgr.Plan` is a pure function over `PlanInput` with no provider,
  storage, or filesystem effects, and derives an idempotency key from its
  inputs (`internal/contextmgr/planner.go:63`).
- Token cost is a `len/4` heuristic (`internal/provider/context.go:11`)
  corrected by an EWMA ratio from observed provider usage
  (`internal/contextmgr/calibration.go`), applied as a single global scalar.
- `EstimateRequestCost` charges every registered tool schema on every request
  (`internal/provider/context.go`). Nothing subsets the registry per turn.
- The recent-tail admission cap is a message count,
  `defaultRecentTailMessages = 8` (`internal/contextmgr/planner.go:17`),
  applied alongside the separate token cap `target`.
- The dispatcher derives a per-tool output ceiling from
  `tools.ResultBudgetTool` and **hard-fails** results above it
  (`internal/runtime/output_ceiling.go`). Plan `48` §3.1 replaces the
  destroy with truncation; this program assumes that outcome.
- `contentref.Reference` is the single canonical minter for
  `ref:<kind>:<sha256>` (`internal/contentref/contentref.go`), and
  `internal/ledger` re-exports it.
- Hook events shipped in v1 are `PreToolUse`, `PostToolUse`, `Stop`.
  `UserPromptSubmit` is explicitly deferred
  (`internal/hooks/config.go:115`).
- `grep` returns `path:line:text`, caps by match count and byte budget, and
  truncates in walk order (`internal/tools/search.go:61`).
- `codeintel.Analyzer` is Go-only (`golang.org/x/tools/go/packages`) and is
  therefore unavailable to any model-facing tool under
  `.mivia/rules/60-tools-project-language-generic.md`.
- The module is pure Go with a lean dependency set and uses
  `modernc.org/sqlite`. **No cgo.** Any design requiring a C SQLite
  extension (`sqlite-vec`) or C tree-sitter bindings is out of bounds.

## 4. Program-wide invariants

These bind every member. A member that cannot hold them is not ready.

- **INV-CE-A (single cost truth).** Any content this program adds to a
  request is priced by `provider.EstimatePromptCost` before the planner
  makes an admission decision. No path may inject tokens the planner cannot
  see.
- **INV-CE-B (prefix stability).** History already sent to the provider is
  not rewritten for economy reasons outside a compaction boundary. Mutating
  an old message invalidates the prompt cache from that point forward, so
  per-turn shaping applies **at insert time only**. This is why `49` batches
  elision at compaction, and why `03`'s seen-ledger substitutes bodies as
  they are appended rather than retroactively.
- **INV-CE-C (no silent loss).** Content the harness removes or shortens is
  either recoverable through a `contentref` handle or is content the model
  demonstrably already holds. "Truncated and gone" is a defect, not a
  saving.
- **INV-CE-D (language-generic surfaces).** Model-facing tool descriptions
  and behaviour stay project- and language-agnostic
  (`.mivia/rules/60-tools-project-language-generic.md`). Structure
  extraction degrades to nothing on unknown file types rather than assuming
  Go.
- **INV-CE-E (offline verification).** `make verify` stays offline. No
  member may make a network call part of a normal turn.

## 5. Suggested sequencing

Smallest blast radius first, and nothing that depends on `48` before `48`.
Operator update 2026-08-02: **`05` before `04`.**

1. `05` - prove/harden authorization-scoped schemas (existing ScopedRegistry
   seam); measurable for restricted agents; no new data model.
2. `04`, `06` - accounting corrections once schema mass is auth-truthful;
   class-aware estimates then residual calibration.
3. `07`, then `08` - both sit on `48` §3.1's truncation semantics; `07`
   supplies the handle that makes `08`'s shaping non-destructive.
4. `09` - reuses `08`'s shaping stage and the existing
   `Capability.ResourceKey`.
5. `03` - the seen-ledger depends on `08`; the structure work does not, and
   may be split ahead of it.
6. `01`, then `02` - last, because the recall sub-budget must ride inside
   `Plan`, and `Plan` should be stable when it lands.

## 6. Out of scope for the whole program

- Any learned or model-directed policy. Adaptive compaction is plan `43`'s
  subject and stays deferred there.
- A model-facing memory tool. `01` and `02` are explicitly passive; if the
  model must call a tool, the feature has failed its own premise.
- Replacing the `len/4` estimator with a real tokenizer.
- Cross-workspace or cross-user memory sharing.
- Any network-backed embedding or retrieval service.

## 7. External prior art consulted

Recorded so the next reader can challenge the reasoning, not just the code.

- Aider's repo map: tree-sitter def/ref tags, PageRank over the symbol
  graph, token-budgeted rendering with enclosing scopes -
  <https://aider.chat/2023/10/22/repomap.html>
- "Less Context, Better Agents" (2606.10209): pruning to recent tool pairs
  beats full retention, because superseded tool state actively misleads.
  Motivates `09`.
- SWE-Pruner (2601.16746): self-adaptive context pruning for coding agents.
- TokenPilot (2606.17016): cache-efficient context management; motivates
  INV-CE-B.
- GAAMA / graph-augmented associative memory: hybrid graph+embedding
  retrieval beats embedding-only on long-horizon reasoning, because purely
  semantic recall misses causal anchors with low textual similarity.
  Motivates the graph in `01`.
- Model2Vec static embeddings: table-lookup embeddings, ~8-30 MB, large CPU
  speedup over transformer encoders - <https://github.com/MinishLab/model2vec>.
  Motivates `02`.
- `sqlite-vec` (<https://github.com/asg017/sqlite-vec>) was evaluated and
  **rejected**: it is a C extension and this module is pure Go.
