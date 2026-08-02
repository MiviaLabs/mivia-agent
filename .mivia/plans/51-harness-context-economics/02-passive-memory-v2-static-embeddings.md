# 51.02 - Addon 1 v2: passive memory with static embeddings

**Status:** DESIGN - ADLC Step 0 not run. **Do not start before `01` has
shipped and has measurement evidence.**
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** `01` (the seam, the graph, the store, the write path, the
privacy boundary, and the measurement harness).
**Blast radius:** MEDIUM - one interface implementation, plus a shipped
weights artifact and its supply chain. Every invariant in `01` continues to
bind unchanged.

## 1. Goal

Replace `01`'s lexical retriever with a semantic one, without touching the
injection seam, the budget discipline, the graph, or the data model.

The entire scope of this plan is: a second implementation of

```go
type Embedder interface {
    Embed(text string) Vector
}
```

If this plan needs to change anything else in `01`, `01`'s abstraction was
wrong and that is the finding.

## 2. Why a change of retriever is worth a plan

`01`'s lexical retriever matches tokens. It cannot connect "the compaction
trigger fires too early" to a memory recorded as "planner threshold drifts
under tool-heavy turns" - no shared terms, same subject. That is the exact
class of recall the feature exists to provide, and it is the class lexical
retrieval structurally cannot deliver.

The graph in `01` mitigates *causal* misses; it does not mitigate
*vocabulary* misses. Only a semantic embedding does.

## 3. Constraints that pick the technique

- **No cgo.** Rules out ONNX Runtime bindings, llama.cpp, and every
  transformer encoder that ships as a C library. This is not a preference;
  it is what `modernc.org/sqlite` and the current `go.mod` mean.
- **Offline `make verify`** (INV-CE-E). Rules out a provider embedding API
  as the normal path.
- **Deterministic.** Retrieval feeds `IdempotencyKey` stability
  (INV-CE-01-E). A retriever with nondeterministic output breaks replanning.
- **Per-turn latency.** This runs on the critical path of every turn. A
  transformer forward pass per turn is not obviously affordable; a table
  lookup is.

## 4. Design

### 4.1 Static embeddings

Model2Vec-style static embeddings: a distilled token to vector table,
mean-pooled over the input's tokens. Published characteristics - ~8-30 MB on
disk, and CPU inference up to ~500x faster than the sentence-transformer it
distils, because inference is a table lookup and an average, with no
attention and no matrix multiplication.

Implementable in pure Go: tokenize, look up, average, normalise. No cgo, no
network, fully deterministic, and testable with exact expected vectors.

Quality sits below a live transformer encoder and far above `01`'s lexical
baseline. That is the trade this plan buys.

### 4.2 What must be shipped, and how

The weights table is a **binary artifact this project must distribute**,
and that is the real cost of this plan:

- Where it lives (embedded in the binary, downloaded on first use, or an
  operator-supplied path) is an open decision (§6.1). Download-on-first-use
  conflicts with the offline posture; embedding 30 MB in the binary is a
  large size regression for a CLI.
- Whatever the answer, the artifact needs a pinned version, a checksum
  verified before use, and a documented licence and provenance. A model
  file is a supply-chain dependency with the same review requirements as a
  Go module.
- The tokenizer must be reproduced in pure Go and must match the one used
  to build the table. A tokenizer mismatch degrades retrieval silently -
  vectors are still produced, they are just wrong. This is the plan's most
  likely defect and needs a fixture-based conformance test.

### 4.3 Versioning and migration

`01` already keys vectors by embedding version
(`docs/architecture/embedded-persistence.md:46`, INV-CE-01-I). Switching
retrievers is therefore a version bump, and existing vectors are
invalidated rather than reinterpreted.

Re-embedding an existing store is a bounded batch job over stored memory
text. Whether it runs eagerly on upgrade, lazily on access, or not at all
(memories simply become unreachable until re-minted) is §6.3.

### 4.4 Rejected alternatives

- **Provider embedding API.** Network on the critical path, breaks offline
  verification, exports memory text to a third party - which is a privacy
  change, not just a latency one.
- **ONNX / llama.cpp via cgo.** Excluded by the build constraint.
- **Training or fine-tuning anything.** Far outside this product.
- **Pure-Go transformer inference.** Feasible to write, not feasible to
  maintain or to run per turn.

## 5. Invariants

All of `01`'s invariants (INV-CE-01-A through -J) continue to bind. Added:

- **INV-CE-02-A.** `Embed` is deterministic: identical input yields a
  bit-identical vector on every platform the project supports. Float
  accumulation order is fixed, not left to the compiler or to map
  iteration.
- **INV-CE-02-B.** The weights artifact is checksum-verified before use. A
  missing or corrupt artifact degrades to `01`'s lexical retriever with a
  visible warning; it never fails a turn and never silently produces
  garbage vectors.
- **INV-CE-02-C.** The Go tokenizer matches the table's tokenizer on a
  committed conformance fixture. Drift is a build failure, not a quality
  regression discovered in production.
- **INV-CE-02-D.** Changing the retriever changes only the `Embedder`
  implementation and the embedding version. No change to admission,
  placement, the graph, the schema, or the write path.
- **INV-CE-02-E.** No network access on any turn path.

## 6. Open decisions for Step 0

1. **How is the artifact distributed?** Embedded (binary size), downloaded
   (offline posture, first-run failure mode), or operator-supplied
   (nobody configures it, so the feature is effectively off). None is
   clean.
2. **Licence and provenance.** Which specific table, under what licence,
   and is redistribution permitted? This is a blocking legal question, not
   an implementation detail.
3. **Migration.** Eager re-embed, lazy re-embed, or drop-and-remint?
4. **Is bit-identical cross-platform determinism actually achievable** with
   float32 accumulation, or does INV-CE-02-A need to relax to a quantised
   or fixed-point representation? If it relaxes, `01`'s idempotency
   guarantee is affected and that must be traced.
5. **Does measured recall quality justify all of the above** over `01`?
   `01`'s measurement harness (§6.5 there) is what answers this. If the
   delta is small, this plan should not be built.

## 7. Delivery slices

1. Pure-Go tokenizer plus conformance fixtures. No retrieval wiring.
2. Table loader, checksum verification, and lexical fallback.
3. `Embedder` implementation and embedding-version bump.
4. Migration path (§6.3).
5. A/B measurement against `01` on the same harness.

## 8. Required tests

- Determinism: repeated and cross-platform `Embed` calls produce identical
  vectors.
- Tokenizer conformance against committed fixtures.
- Corrupt, truncated, and absent artifacts each fall back to lexical with a
  warning and no failed turn.
- Version isolation: v1 and v2 vectors are never compared.
- Retrieval quality: a fixture pair with **no shared vocabulary** but the
  same subject is retrieved by v2 and not by v1. This is the test that
  justifies the whole plan.
- Latency: per-turn embedding cost stays within a stated bound on the
  reference workload.
- Every `01` invariant test still passes unchanged with v2 installed
  (INV-CE-02-D).

## 9. Out of scope

- Any change to `01`'s seam, graph, schema, or write path.
- Reranking, hybrid sparse/dense fusion, or ANN indexes.
- Provider-hosted embeddings.
- Fine-tuning or distilling a table ourselves.
