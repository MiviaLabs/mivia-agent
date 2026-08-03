# 51 - Harness context economics: program overview

**Status:** DESIGN - **ADLC Step 0 complete for all members (2026-08-03).**
`04` and `05` locked 2026-08-02. `01`, `02`, `03`, `06`, `07`, `08`, `09`
challenged 2026-08-03; **four members were closed or stopped**, see §2.
**Date:** 2026-08-02, re-baselined 2026-08-03
**Depends on:** `48` (archived; items F/G still TODO and stay with `48`) and
`49` (archived, shipped as `internal/contextmgr/planner_elision.go`).
**Blocks:** nothing.
**Blast radius:** program-level HIGH, member-level LOW to HIGH.

## 0. Step 0 disposition, program level

The program was drafted against `0363ca1`. By the time the panel ran, HEAD
was ~35 commits ahead and **three members had been overtaken by shipped
code**. The single most important program-level finding:

> A plan's "Verified baseline" section is a claim with a shelf life. Every
> member here that survived contact did so by being re-derived against HEAD,
> not against its own baseline section. Two members proposed building code
> that already exists.

Panel verdicts:

| Member | Verdict | Disposition |
|--------|---------|-------------|
| `01` | BLOCK / DO-NOT-BUILD (both lenses) | **Deferred-blocked.** Four missing foundations, §2 there. |
| `02` | PARTIAL, "demote" | **Closed-deferred** into `01` §11. |
| `03` | DO-NOT-BUILD as written, SHOULD-SPLIT | **Re-scoped** to Stage A/B; ledger split out to `10`. |
| `04` | locked 2026-08-02 | Ready for Step 1. |
| `05` | locked 2026-08-02 | Ready for Step 1. Implement first. |
| `06` | PARTIAL | **Locked** with two invariants restated. |
| `07` | DO-NOT-BUILD, already shipped | **Closed - merged** into archived `tools/01`. |
| `08` | DO-NOT-BUILD, already shipped | **Closed - merged** into archived `tools/06`. |
| `09` | DO-NOT-BUILD | **Stopped.** Its key is unusable and its benefit is already collected by `49`. |

Net: of nine drafted members, **three ship** (`04`, `05`, `06`), **two are
re-scoped and live** (`03`, `10`), **three are closed**, and **one is
blocked on foundations outside this program** (`01`, with `02` folded in).

### 0.1 The program's real yield

The challenge round found more value in **defects in shipped code** than in
the plans it was challenging. Those are recorded in §8 and are not this
program's work - they are bug fixes that need owners.

## 1. Thesis (unchanged)

Context cost is controlled at the structural planner and at per-tool byte
ceilings. This program moves the decisions between those points into the
harness, where they are deterministic and testable, rather than into the
prompt, where they are advisory.

The organising constraint stands: **cost truth stays in one function.**

## 2. Members

| File | Subject | State |
|------|---------|-------|
| `01-passive-memory-v1-lexical.md` | Passive recall tier | **DEFERRED-BLOCKED** - four foundations absent |
| `02-passive-memory-v2-static-embeddings.md` | Static-embedding retriever | **CLOSED-DEFERRED** into `01` §11 |
| `03-structure-and-seen-aware-search.md` | Enclosing symbol + line span (A), match-density ranking (B) | **LOCKED**, re-scoped |
| `04-split-calibration-ratios.md` | Class-aware token estimation | **LOCKED** 2026-08-02, after `05` |
| `05-tool-schema-gating.md` | Auth-truthful advertised schemas | **LOCKED** 2026-08-02, first |
| `06-token-capped-recent-tail.md` | Retire the message-count tail cap | **LOCKED** 2026-08-03 |
| `07-pageable-truncated-results.md` | - | **CLOSED - MERGED** into archived `tools/01` |
| `08-dispatcher-result-shaping.md` | - | **CLOSED - MERGED** into archived `tools/06` |
| `09-supersede-stale-tool-results.md` | - | **STOPPED - DO NOT BUILD** |
| `10-seen-content-substitution.md` | Insert-time seen-content substitution | **DESIGN** - blocked, see its §3 |

`10` is new: both `03`'s ledger half and `08`'s residual stage described the
same feature from different ends, so it was re-parented into one document
with one owner.

## 3. Verified baseline (re-read 2026-08-03)

The `0363ca1` baseline is superseded. What actually holds at HEAD:

- `contextmgr.Plan` is at `internal/contextmgr/planner.go:68`, still pure.
  **`retainMessages` (`planner.go:136`) is reached only from `planCompact`
  (`planner_elision.go:23`)**; below the trigger `Plan` returns its input
  untouched (`planner.go:100`). There is no per-turn admission stage.
- Compaction elision **shipped** (`planner_elision.go`). It replaces
  non-mandatory prior tool bodies over 2048 B with a notice that carries
  **no recoverable reference** (`planner_elision.go:127-131`).
- Remainder spooling and paging **shipped**: `internal/remainder/spool.go`,
  `internal/cli/read_output.go`, wired at `internal/agent/loop_scheduler.go:88`.
- Batch result shaping **shipped**: `internal/agent/shape_batch.go`, called
  from `internal/agent/loop_tools.go:44`. The dispatcher-vs-loop question is
  answered: shaping lives in the loop.
- The dispatcher still **hard-fails** over-ceiling
  (`internal/runtime/dispatcher.go:471`). Plan `48` items F and G remain
  TODO and stay with `48`.
- **Rule 60 does not exclude `codeintel`.** `list_symbols`,
  `go_to_definition`, and `find_references` are model-facing and use it
  today (`internal/tools/go_to_definition.go:10`), passing rule 60 via an
  explicit degrade string. `codeintel.FileOutline` (`outline.go:21`) and
  `declSpan` (`span.go:45`) already answer "nearest enclosing declaration
  and its span". The overview's earlier claim to the contrary was wrong.
- `Capability.ResourceKey` is derived from `path` **only**
  (`internal/tools/tools.go:483`). Not from `pattern`, `offset`, or
  `limit`; a path-less call returns the constant `"workspace:read"`.
- The `content` table is **never reclaimed** by design today
  (`internal/ledger/content_retention_test.go:55`); `Spool.MarkExpired` has
  no production caller.
- No evaluation harness exists. `Makefile` offers test/race/vet/invariants/
  coverage only.

## 4. Program-wide invariants

Unchanged in intent; two amended by evidence.

- **INV-CE-A (single cost truth).** Content added to a request is priced by
  `provider.EstimatePromptCost` before admission.
  *Amendment:* holds in the planner, but is **violated end-to-end on the
  `/compact` path**, which passes no tool schemas
  (`internal/cli/context_setup.go:102`). Recorded as a defect in §8, not
  waived.
- **INV-CE-B (prefix stability).** History already sent is not rewritten
  for economy outside a compaction boundary.
- **INV-CE-C (no silent loss).** Removed or shortened content is
  recoverable through a `contentref` handle, or is content the model
  demonstrably already holds.
  *Amendment:* **currently violated by shipped code** in two places -
  `49`'s elision notice carries no handle, and tool-internal truncation in
  `search.go`/`read.go` discards bytes the spool never sees. §8.
- **INV-CE-D (language-generic surfaces).** Model-facing tools stay
  project- and language-agnostic, degrading rather than assuming a
  language. `codeintel`-backed tools satisfy this by degrading with
  `ErrUnavailable`, which is the pattern to copy - not to avoid.
- **INV-CE-E (offline verification).** `make verify` stays offline.

## 5. Sequencing (revised 2026-08-03)

1. **`05`** - auth-truthful schemas. No new data model.
2. **`04`** - class-aware estimation, once schema mass is auth-truthful.
3. **`06`** - retire the count cap. Must follow `05`: its gain is masked
   while schema mass consumes `target` before the tail is reached.
4. **`03` Stage A** - enclosing symbol + line span on search results,
   reusing `codeintel`. Independent of everything above.
5. **`03` Stage B** - match-density ranking. Independent.
6. **`10`** - seen-content substitution. Blocked on the `49` elision
   reference defect (§8.2) being fixed first; a ledger cannot be correct
   while elision destroys content unrecoverably.
7. **`01`** - only if its four foundations are built, which is not this
   program's work.

## 6. Out of scope for the whole program

- Any learned or model-directed policy (plan `43`).
- A model-facing memory tool.
- Replacing the `len/4` estimator with a real tokenizer.
- Cross-workspace or cross-user memory.
- Network-backed embedding or retrieval.
- Plan `48` items F/G. They stay with `48`.

## 7. External prior art consulted

- Aider repo map, tree-sitter tags + PageRank, token-budgeted rendering -
  <https://aider.chat/2023/10/22/repomap.html>
- "Less Context, Better Agents" (arXiv 2606.10209) - pruning superseded
  tool state beats full retention. Motivated `09`; `09` stopped anyway
  because the benefit is already collected by `49`.
- SWE-Pruner (2601.16746); TokenPilot (2606.17016), motivating INV-CE-B.
- GAAMA graph-augmented memory - graph beats embedding-only recall.
- Model2Vec static embeddings - <https://github.com/MinishLab/model2vec>
- `sqlite-vec` evaluated and **rejected**: C extension, this module is pure
  Go.

## 8. Defects found in shipped code during Step 0

**These are not this program's work.** They are confirmed defects in code
that has already landed, found while re-baselining. Each needs an owner.
Severity is the panel's, verified independently where marked.

### 8.1 `Capability.ResourceKey` is path-only (**verified**)

`pathCapabilityKey` (`internal/tools/tools.go:483`) unmarshals only
`{"path":...}`. Consequences:
- Two `grep` calls with **different patterns** over the same directory share
  a resource key - `pattern` is not in the key (`search.go:55`).
- Two `read_file` calls on **disjoint windows** of one file share a key -
  `offset`/`limit` are not in the key (`read.go:44`).
- Any path-less `grep`/`glob`/`list_dir` collapses onto the constant
  `"workspace:read"`.

The key feeds the concurrency scheduler (`internal/agent/loop_tools.go:408`),
so unrelated reads are **falsely serialised** today. This is a live
performance defect independent of any plan.

### 8.2 Compaction elision destroys content unrecoverably (**verified**)

`elisionNotice` (`planner_elision.go:127-131`) emits only a size bucket -
no `contentref`, by explicit design. The bytes are gone and
`l.Messages` is durably replaced (`internal/agent/context.go:47`). This
violates INV-CE-C and is the hard blocker under `10`.

### 8.3 `read_output` does not redact on load (**verified**)

`internal/cli/read_output.go` applies only `ToValidUTF8`, while the
analogous `ledger_read` calls `redact.Text` with an explicit comment on why
paging must redact first (`internal/cli/ledger_tools.go:129-131`). A tool
that does not self-redact spools raw secrets and replays them verbatim.

### 8.4 The `content` table is never reclaimed

`TestContentStoreIsNeverReclaimed` (`internal/ledger/content_retention_test.go:55`)
asserts this as intended. `Spool.MarkExpired` (`internal/remainder/spool.go:187`)
has no production caller, so `ErrExpired` is unreachable. Every truncated
result of every session accumulates on disk without bound.

### 8.5 `shape_batch` can grow a result

The status trailer (`internal/agent/shape_batch.go:264,409`) is composed
**outside** the shrink guard at `:302`, which was evaluated with an empty
trailer. A small ephemeral body plus a 35-byte notice ends larger than it
started.

### 8.6 Degraded `run_command` results can be misclassified

`toolResultBodyFailed` (`internal/agent/loop_tools.go:188-201`) scans for an
`exit=` line, but a tier-3 degrade replaces the whole body
(`shape_batch.go:409`), erasing that header. A failed command can be
reported as successful.

### 8.7 `grep` output already exceeds its byte budget

`truncationReserve` (`search.go:33`) reserves for the two truncation notices
only. The pagination trailer (`search.go:133`) and `walkErrors.notice()` -
which embeds an arbitrary path and error string - are appended after the
budget check.

### 8.8 `/compact` prices no tool schemas

`internal/cli/context_setup.go:102` and
`internal/chat/context_integration.go:467` build the manager without
`Tools`, while the turn path sets them (`:384`). Pre-existing and known;
plan `06` §6 records why it gets worse rather than better, and declines to
conceal it.

### 8.9 Float determinism is architecture-dependent

Go permits FMA fusion; arm64 fuses `sum += x*x`, amd64 at `GOAMD64=v1` does
not (<https://github.com/golang/go/issues/17895>). Any future vector or
score-ordering work cannot claim bit-identical cross-platform results
without explicit rounding or fixed-point accumulation. Recorded for `01`.
