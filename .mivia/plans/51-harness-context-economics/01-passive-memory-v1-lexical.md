# 51.01 - Addon 1 v1: passive memory, recall seam and lexical retrieval

**Status:** DESIGN - ADLC Step 0 not run. **This is the highest-risk plan in
program `51`** and should be sequenced last.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** a stable `contextmgr.Plan`. Should land after `04`, `05`,
`06`, which all change planner accounting.
**Supersedes nothing.** `02` upgrades this plan's retriever only.
**Blast radius:** HIGH - planner admission, a new durable data model, and
content the model receives without asking for it.

## 1. Goal

Every turn, retrieve context relevant to the current objective from a
durable memory graph and inject it into the request automatically - with no
model-initiated tool call, and therefore no tokens spent deciding to
remember.

v1 establishes the **seam, the budget discipline, the data model, and the
graph**, using a retriever that needs no model weights and no network. v2
(`02`) swaps in a better retriever behind the same interface.

## 2. Verified baseline

- `contextmgr.Plan` is pure: no provider, storage, or filesystem effects,
  with a deterministic `IdempotencyKey` over its inputs
  (`internal/contextmgr/planner.go:63`). Everything durable happens in
  `commit_request.go` / `internal/storage/context_store.go` afterwards.
- `retainMessages` selects a mandatory set (system, current objective,
  latest tool unit) and then admits optional units newest-first under
  `target` (`planner.go:166`).
- `latestUserObjective` already extracts the current objective in the agent
  loop (`internal/agent/context.go`).
- Hook events shipped in v1 are `PreToolUse`, `PostToolUse`, `Stop`.
  **`UserPromptSubmit` is explicitly deferred** as "not implemented in v1"
  (`internal/hooks/config.go:115`).
- A completed turn commits the active context into an immutable checkpoint
  and the next turn's planner sees it (`commit_request.go`,
  `internal/storage/context_store.go`). A summarizer already exists
  (`internal/contextmgr/summarizer.go`, `summary.go`).
- Storage is `modernc.org/sqlite` - **pure Go, no cgo**.
- `docs/architecture/embedded-persistence.md:46` already requires derived
  artifacts to be keyed by workspace revision, source event range,
  tool/config version, pack algorithm, **and model/embedding version**.
  That constraint predates this plan and binds it.

## 3. Why passive, and why a graph

**Passive.** A memory *tool* costs tokens three times: the schema on every
turn, the model's decision to call it, and the call/result round trip. It
also fires only when the model already suspects it has forgotten something -
which is exactly when it is least able to know. Retrieval driven by the
harness on every turn has none of those properties.

**Graph, not pure cosine.** The published failure mode of passive semantic
stores (Mem0, MemGPT, LangMem and similar) is *retrieval bias*: they miss
causally essential anchors that lack textual similarity to the query.
Graph-augmented associative memory (GAAMA and related work) beats
embedding-only retrieval on long-horizon reasoning for exactly this reason.
The edges are not decoration - they are the mitigation for the known defect
of the mechanism this plan is built on.

## 4. Design

### 4.1 Injection point: inside `Plan`, not around it

`PlanInput` gains `Recall []Recalled`. A pure retriever computes candidates
*before* `Plan`; `Plan` admits them under a dedicated sub-budget.

This is the load-bearing design decision. Injecting recall anywhere else -
a `UserPromptSubmit` hook, the loop, the provider adapter - puts tokens in
the request that `provider.EstimatePromptCost` never priced. The compaction
trigger, the hard-budget rejection, and the calibration ratio would all be
computed over a request that is not the request being sent (INV-CE-A). The
planner's purity is preserved because retrieval happens outside it and its
result is an input.

### 4.2 Admission order and sub-budget

Recall is admitted after the mandatory set and **before** the recent tail,
under a sub-budget expressed as a fraction of `Budget` (default small -
5% is the proposed starting point, §6.2). Recall never displaces a
mandatory message and never causes a budget rejection: if the sub-budget
cannot be met, less recall is admitted, down to none.

### 4.3 Placement: tail, never head

Recalled content is placed immediately **before** the current objective, not
at the top of the request.

Fresh per-turn content at the head of the prompt changes the stable prefix
every turn and invalidates prompt caching for the entire request. On a
long session that cost plausibly exceeds everything this feature saves.
Placement at the tail keeps the prefix intact (INV-CE-B). This is not a
detail - it is the difference between a saving and a regression, and it must
be measured, not assumed (§6.5).

### 4.4 The retriever (v1: lexical)

`internal/memory` exposes:

```go
type Embedder interface {
    Embed(text string) Vector   // deterministic, offline
}
```

v1's implementation is lexical: hashed character n-grams with BM25-style
term weighting, L2-normalised. No model weights, no network, no new
dependency, and fully deterministic - which means it works inside offline
`make verify` (INV-CE-E) and its tests are exact rather than statistical.

It is a genuinely weaker retriever than a neural embedding. That is
accepted: v1's purpose is to prove the seam, the budget discipline, the
graph, and the write path, all of which are independent of retriever
quality. `02` replaces this one type.

### 4.5 Storage and search

Vectors as `float32` BLOBs in the existing SQLite store, brute-force cosine
in Go. 10k memories at 256 dimensions is ~10 MB and a sub-millisecond scan.
No ANN index below roughly 100k memories.

`sqlite-vec` was evaluated and **rejected**: it is a C extension and this
module is pure Go on `modernc.org/sqlite`.

Per `docs/architecture/embedded-persistence.md:46`, every stored vector is
keyed by embedding version. A version change invalidates vectors rather than
silently comparing incompatible ones.

### 4.6 Retrieval

Top-k by cosine over the seed set, then **one-hop expansion** along graph
edges, then a hard cap on total admitted items. One hop, not transitive
closure: bounded, cheap, and testable.

### 4.7 The write path - where "no tokens" actually comes from

Memories are minted by the host from **committed checkpoints**, using the
existing `CheckpointCandidate` and summarizer machinery. The model is never
asked to write a memory. Edges are derived cheaply and deterministically:

- co-occurrence within one checkpoint,
- shared file paths touched,
- shared `contentref` digests.

Minting happens after commit, off the turn's critical path.

### 4.8 What the model sees

A single clearly-delimited block, labelled as harness-supplied recall from
prior sessions, never as the user's words and never as tool output. Each
item carries its provenance. Recalled content is **untrusted data**: it was
derived from prior sessions which may have contained tool output and fetched
content. `.mivia/skills/secure-change/SKILL.md:100` already treats tool
output and fetched content as a prompt-injection vector; recall inherits
that status and must be framed accordingly (§5, INV-CE-01-F).

## 5. Invariants

- **INV-CE-01-A.** Recall tokens are priced by
  `provider.EstimatePromptCost` before any admission decision (INV-CE-A).
- **INV-CE-01-B.** Recall never displaces a mandatory message and never
  causes a prompt-budget rejection. Under pressure, recall is what gives.
- **INV-CE-01-C.** Recall is placed at the tail, preserving the request
  prefix (INV-CE-B).
- **INV-CE-01-D.** `Plan` stays pure. Retrieval is an input, never a call
  from inside the planner.
- **INV-CE-01-E.** Retrieval is deterministic for identical inputs, so
  `IdempotencyKey` is stable and a replanned turn recalls identically.
- **INV-CE-01-F.** Recalled content is delimited and labelled as untrusted
  harness-supplied context. It is never presented as user instruction or as
  tool output.
- **INV-CE-01-G.** Memory is workspace-scoped. No cross-workspace recall.
- **INV-CE-01-H.** Stored memories are subject to the same redaction and
  secret-path rules as any other persisted content. A memory store that
  accumulates secrets across sessions is a durable privacy breach, not a
  cache.
- **INV-CE-01-I.** Vectors are keyed by embedding version; a version change
  invalidates rather than reinterprets
  (`docs/architecture/embedded-persistence.md:46`).
- **INV-CE-01-J.** The feature is **off by default** until §6.5's
  measurement exists.

## 6. Open decisions for Step 0

1. **Does this belong in the product at all?** It is the largest new
   surface in the program: a durable store, a retrieval algorithm, an
   injection path, and a privacy boundary. Everything else in `51` makes
   existing behaviour cheaper; this adds behaviour. Step 0 should be
   genuinely willing to answer no.
2. **Sub-budget size and shape.** 5% is a guess. Fixed fraction, or a
   fraction that decays as the request approaches the trigger?
3. **Is lexical retrieval good enough to evaluate the seam?** If v1's
   recall quality is so low that the feature looks worthless, the
   evaluation measures the retriever, not the design - and `02` gets judged
   by v1's failure. Consider gating v1 on evaluation-only use.
4. **Deletion and correction.** A wrong memory recalled every turn is worse
   than no memory. What is the user-facing surface for inspecting and
   deleting memories, and does it exist before the feature ships?
5. **Measurement.** The claim is "fewer tokens overall". The honest
   accounting must include the prompt-cache effect of tail injection, the
   recall tokens themselves, and any turns where recall displaced tail
   context the model then had to re-derive. Without this measurement the
   feature cannot be evaluated at all.
6. **`UserPromptSubmit`.** This plan deliberately does not need it. If a
   future consumer does, that is a separate plan against
   `internal/hooks/config.go:115`.
7. **Interaction with compaction.** After compaction the model's context is
   already a reduced view. Does recall then re-inject content that
   compaction just removed? A guard against recall/compaction thrash is
   needed.

## 7. Delivery slices

1. `internal/memory` with the `Embedder` interface, the lexical
   implementation, the graph model, and pure retrieval - **no wiring**.
   Standalone tests only.
2. Durable schema, redaction, versioning, and the checkpoint-driven write
   path. Still no injection.
3. Inspection and deletion surface (§6.4).
4. `PlanInput.Recall` and sub-budget admission, off by default.
5. Measurement harness (§6.5), then a default-on decision.

## 8. Required tests

- Determinism: identical input yields identical retrieval and an unchanged
  `IdempotencyKey`.
- Budget: recall never pushes `AfterTokens` past `target`; under pressure
  recall shrinks to zero before any mandatory message is touched.
- Placement: recall appears immediately before the objective, and the
  request prefix is byte-identical to a no-recall request up to that point.
- Purity: `Plan` performs no I/O with recall present (enforced by
  construction and by test).
- Redaction: a secret-bearing checkpoint mints no secret-bearing memory.
- Scope: memories from workspace A are unreachable from workspace B.
- Version: a vector written under embedding version 1 is not compared
  against version 2.
- Graph: one-hop expansion is bounded and reaches an item that cosine alone
  ranks below the cutoff - the test that justifies the graph existing.
- Off-by-default: with the feature disabled, every planner test's output is
  byte-identical to today's.

## 9. Out of scope

- Any model-facing memory tool.
- Neural embeddings (that is `02`).
- Cross-workspace or cross-user memory.
- Network retrieval of any kind.
- Model-authored memories or model-directed forgetting.
