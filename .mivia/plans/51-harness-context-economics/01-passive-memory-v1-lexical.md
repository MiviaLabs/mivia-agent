# 51.01 - Passive memory: DEFERRED-BLOCKED on four missing foundations

**Status:** **DEFERRED-BLOCKED.** ADLC Step 0 ran 2026-08-03 with two
independent lenses; both returned BLOCK / DO-NOT-BUILD. The feature is not
refuted in principle, but **all four seams it assumes are absent**, and
three of them are larger than this plan. Do not implement.
**Date:** 2026-08-02, blocked 2026-08-03
**Part of:** program `51` (`00-overview.md`).
**Absorbs:** `02` (static embeddings), closed into §11.

## 0. Step 0 disposition (hostile challenge)

Panel: structural lens (DO-NOT-BUILD), correctness lens (BLOCK). They
disagreed about wording and agreed about every substantive point.

### 0.1 Baseline errors that changed the outcome

| Claim in §2 | Reality |
|-------------|---------|
| "`Plan` ... admits recall under a sub-budget in `retainMessages`" | **`retainMessages` is reached only from `planCompact`** (`planner_elision.go:23`). Below the trigger `Plan` returns its input untouched (`planner.go:100`). **There is no per-turn admission stage to hook.** |
| "A summarizer already exists" | True but misleading: `NewSummarizer`/`Summarize` have **zero non-test callers** and `Summarize` is a **provider network call** gated on `Policy.NetworkEnabled` (`summarizer.go:33-56,73`). It is not a host-side, token-free, offline write path. |
| "A completed turn commits the active context into an immutable checkpoint" | **Unverified in production.** `BuildCommitRequest` has no non-test caller; the live path is `StructuralPreparationManager`, which "owns no storage and never publishes a checkpoint" (`structural.go:13-19`). |
| "deterministic `IdempotencyKey` over its inputs" | `planIdempotencyKey` returns a **caller-supplied key verbatim** (`planner.go:298-303`) and is computed only on the compaction path; below the trigger the key is `""`. |
| `planner.go:63` / `:166` cites | Stale: `Plan` is at `:68`, `retainMessages` at `:136`. |

### 0.2 Findings

| Finding | Severity | Disposition |
|---------|----------|-------------|
| **No injection seam exists.** Recall on a normal turn requires a *new unconditional mutation stage* in `Plan`, not a new `PlanInput` field. That is a far larger change than the plan scoped | BLOCK | **Accepted.** Foundation F1, §2. |
| **Recall would become durable history.** `Plan`'s output replaces the loop's live history (`internal/agent/context.go:48`), so injected recall accumulates across turns, is re-priced every turn, feeds objective detection as if it were real history, and is marshalled into `CheckpointCandidate.ActiveContext` (`structural.go:46-52`). Untrusted harness text becomes committed session state - and §4.7 then re-mints memories from checkpoints containing recall, a self-feeding loop | BLOCK | **Accepted.** This is the finding that most clearly kills the current design. |
| **`PlanInput.Recall` alone is not priced.** `EstimatePromptCost` costs `input.Messages` only (`planner.go:85`), so INV-CE-01-A and program INV-CE-A fail by construction unless recall is merged into `Messages` first - which *is* the previous finding | BLOCK | **Accepted.** |
| **No shape-legal untrusted role.** Only user, or assistant-with-content, is legal at an arbitrary tail position (`provider/context.go:148,154-160,189-201`). A recall block is therefore either a **forged user instruction** (violating INV-CE-01-F) or words put in the model's own mouth. There is no harness role | BLOCK | **Accepted.** Foundation F3, §2. |
| **No redaction seam on the mint path.** `active_context` is written raw (`planner_elision.go:40-49`, `storage/context_store.go:184`), bypassing `SanitizeSourcePayload` entirely; and source payloads are metadata-only without a configured policy (`contextstate/sanitize.go:64,80-82`). Minting from checkpoints mints unredacted secrets; minting from source events mints nothing | BLOCK | **Accepted.** Foundation F4, §2. |
| **INV-CE-01-C fails within a multi-step turn.** `prepareStep` runs per step; the objective is not the tail once assistant/tool messages accumulate after it. Inserting before the objective at step *n* rewrites history already sent at steps 1..n-1 - the exact prefix-cache regression §4.3 claimed to avoid | BLOCK | **Accepted.** |
| **INV-CE-01-E is near-vacuous and also unholdable.** Vacuous because a caller-supplied key wins and no key exists below the trigger; unholdable because retrieval reads a mutable store while the cancellation replan (`agent/context.go:33-42`) can call `Prepare` twice in one turn | MEDIUM | **Accepted.** Restated in §3. |
| **`internal/memory` would ship with zero consumers.** The only `Plan` caller owns no storage (`structural.go:15-19`) and `PrepareInput` has no storage handle. Slices 2-4 must invent a storage-owning `PreparationManager`, inverting the current dependency direction | MEDIUM | **Accepted.** Slice 1 as drafted is speculative generality. |
| §6.7 thrash: **no unbounded loop** (post-compaction ~50% + 5% stays under the 80% trigger), but recall **accretes**: it lands in `retained`, becomes committed `ActiveContext`, and a non-tool recall message is permanently inelidable (`planner_elision.go:98-107` touches `RoleTool` only) | MEDIUM | **Accepted.** Every compaction would add a block no compaction can remove. |
| "Delimit and label" is an in-prompt advisory control over content the harness injects unbidden, derived from prior tool output - a **durable cross-session injection channel** with no model-side opt-out. `.mivia/skills/secure-change/SKILL.md:100` already says the model is not a trusted executor | MEDIUM | **Accepted.** Until F3 exists, recall carries no free text. |
| Float accumulation is architecture-dependent (Go permits FMA fusion; arm64 fuses, amd64 v1 does not) so **even the lexical BM25 path** cannot claim bit-identical cross-platform scores | MEDIUM | **Accepted.** Overview §8.9. |
| Third schema owner is **not** a novel violation - `storage/sqlite.go:43` and `context_schema.go:19` are already two migration owners, and workspace scoping already exists | LOW | Noted. Extend the numbered migrations rather than adding an owner. |

## 1. Locked conclusion

The design is not wrong about what it wants. It is wrong about what exists.

Every load-bearing claim in its baseline - an admission stage on normal
turns, a live checkpoint-commit path, a host-side summarizer, a redaction
seam on the mint path - describes machinery that is either absent, has no
production caller, or does the opposite of what the plan needed. The plan
was assembled from a reading of the code that was optimistic at every
single seam.

**Deferred-blocked, not rejected.** The value argument (a memory tool costs
tokens three times and fires only when the model already suspects it has
forgotten) still stands, and so does the graph rationale (passive semantic
stores miss causal anchors with low textual similarity; graph augmentation
is the published mitigation).

## 2. Unblock preconditions

All four must exist before this plan is re-challenged. **Three are outside
program `51` entirely**, which is itself the finding: `01` is the only
member whose foundation lies outside its own program.

| # | Foundation | Why it is not this plan's work |
|---|-----------|-------------------------------|
| **F1** | A per-turn context-preparation stage that runs below the compaction trigger, prices what it injects, and does **not** write its output back into `l.Messages` as durable history | Changes `Plan`'s contract and the loop's history ownership. A planner plan, not a memory plan. |
| **F2** | A live checkpoint-commit path with a production caller, and a **deterministic, offline** extractor to mint from it | `BuildCommitRequest` and the summarizer both lack production callers, and the summarizer is a network call. Building this is plan `42`/`49` territory. |
| **F3** | A non-conversational message kind that is neither user nor assistant, so harness-supplied untrusted content is structurally distinguishable | Changes `provider.Message` and `ValidateToolPairing` - a provider-layer plan with its own blast radius. |
| **F4** | A redaction seam that fails **closed** on the mint path, and refuses to mint when no policy is configured | `active_context` currently bypasses sanitisation entirely. A storage/privacy plan. |

## 3. Invariants, restated for a future attempt

Kept so the next attempt does not re-derive them. Every one below is a
correction of the original.

- **INV-CE-01-D** (was "`Plan` stays pure"). Recall arrives as
  fully-materialised `provider.Message` values, validated by
  `validateMessageShape` **before** costing and fingerprinting, so shape
  validation, pricing and the idempotency key all see one array.
- **INV-CE-01-E** (was "deterministic retrieval"). Retrieval executes
  **exactly once per turn in the loop**; the resulting `[]Recalled` is an
  immutable field of `PrepareInput` and the cancellation replan reuses it
  verbatim. Scores are not claimed bit-identical across architectures.
- **INV-CE-01-F** (was "delimited and labelled"). Recall is carried in a
  dedicated non-conversational message kind (F3). **Until that exists,
  recall carries no free text** - metadata, file paths and digests only.
- **INV-CE-01-C** (was "tail, never head"). Recall is inserted only on the
  **first** planning step of a turn, and never after any message already
  sent.
- **INV-CE-01-H** (was "same redaction rules"). Minting runs only when a
  redaction policy is configured and every minted body passes the policy
  before write. Fails closed.
- **New: INV-CE-01-K.** Recall never enters `CheckpointCandidate.
  ActiveContext`, and recall-derived content is never a mint source.
  Without this, memory feeds on itself.

## 4. Deleted from the design

- The `Embedder` interface and vector BLOBs in v1. A lexical BM25 retriever
  has no embedding; the interface existed solely for `02`, which is now
  closed. An extension point with no second implementor.
- INV-CE-01-I (embedding versioning) - nothing to version in a lexical v1.
- §4.6's one-hop graph expansion **as a v1 deliverable**: its only
  justifying test ("reaches an item cosine ranks below the cutoff") is
  untestable without cosine. The graph rationale survives; the v1 slice
  does not.
- Slice 1 as a standalone deliverable - a package with no consumer and no
  measurement.

## 5. What survives for a future attempt

1. The value argument: passive beats a tool because a tool costs tokens
   three times and fires at the wrong moment.
2. The graph argument: embedding-only recall misses causal anchors; graph
   augmentation is the mitigation (GAAMA and related work).
3. The budget discipline: whatever injects must be priced by
   `EstimatePromptCost` before admission (INV-CE-A).
4. The placement argument: tail, not head, to preserve the prefix - now
   qualified by INV-CE-01-C's first-step restriction.
5. The measurement requirement (original §6.5): honest accounting must
   include the prompt-cache effect, the recall tokens, and turns where
   recall displaced tail context the model then had to re-derive. **No
   evaluation harness exists in this repo**, which is a fifth precondition
   in practice.

## 6. Reopening criteria

F1-F4 all exist and have production callers; an evaluation harness exists;
and a measurement on real sessions shows recall's net token effect is
favourable **including** cache invalidation. Re-run Step 0 from scratch
against HEAD - not against this document.

## 7. Out of scope, permanently

- A model-facing memory tool.
- Cross-workspace or cross-user memory.
- Network retrieval of any kind.
- Model-authored memories or model-directed forgetting.

## 8. Absorbed: static embeddings (was plan `02`)

`02` is closed. Its four surviving contributions, for whenever F1-F4 exist:

1. **The vocabulary-miss case.** Lexical retrieval cannot connect "the
   compaction trigger fires too early" to "planner threshold drifts under
   tool-heavy turns" - no shared terms, same subject. The graph mitigates
   *causal* misses; only a semantic embedding mitigates *vocabulary*
   misses.
2. **The artifact cost.** A Model2Vec-style table is 8-30 MB. This repo has
   **zero** `go:embed` directives, a 25.9 MB binary, and no release or
   artifact CI job - so embedding one is a 30-115% size regression and
   introduces checksum, mirroring and distribution concerns the project has
   never had. `.mivia/rules/30-go-standards.md:56` permits `go:embed` for
   static templates and fixtures, which a model table is not.
3. **The licence blocker.** Model2Vec the library is MIT, but each distilled
   table inherits its teacher's terms; redistribution inside an Apache-2.0
   binary needs a named model, a named licence, and attribution. This is a
   legal question, answerable only by naming one table - which `02` never
   did. It blocks scheduling, not just implementation.
4. **The tokenizer hazard.** A hand-rolled tokenizer that does not match the
   table's degrades retrieval **silently** - vectors are still produced,
   they are just wrong. An existing pure-Go option (`sugarme/tokenizer`)
   should be evaluated against the hand-roll rather than assumed away;
   hand-rolling a parser for an untrusted third-party format is a
   missing-foundation finding under the architecture-review skill.

A future embedding upgrade must also treat float determinism honestly
(overview §8.9): a checksum-fallback design where one host uses the table
and another falls back to lexical produces different recall for identical
input, which is exactly what INV-CE-01-E exists to prevent. The active
embedder's identity must be part of the version, so a fallback is a visible
version change rather than a silent divergence.
