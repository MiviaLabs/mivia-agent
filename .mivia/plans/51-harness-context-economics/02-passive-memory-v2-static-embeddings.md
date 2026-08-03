# 51.02 - CLOSED (deferred): passive memory with static embeddings

**Status:** **CLOSED - DEFERRED.** ADLC Step 0 ran 2026-08-03. The panel
returned PARTIAL with the recommendation "demote to a deferred note inside
plan `01`", and that is what happened: the four surviving contributions live
in `01` §8. **Do not implement from this document.**
**Date:** 2026-08-02, closed 2026-08-03
**Part of:** program `51` (`00-overview.md`).
**Superseded by:** `01-passive-memory-v1-lexical.md` §8.

## 0. Step 0 disposition

Panel verdict: **PARTIAL**, recommendation **demote**.

| Finding | Severity | Disposition |
|---------|----------|-------------|
| **Step 0 cannot be closed for this plan.** All five of its open decisions depend on evidence only `01` can produce; §6.5 explicitly defers to "`01`'s measurement harness", and `01` is now itself deferred-blocked with four missing foundations | BLOCK | **Accepted.** A plan whose every open decision is unanswerable is a design note wearing a plan's structure. |
| INV-CE-02-A ("bit-identical vector on every platform") is **unachievable in plain Go float32**. The spec permits fusing multiple float operations into one; arm64 has guaranteed FMA and fuses `sum += x*x`, amd64 at `GOAMD64=v1` does not (<https://github.com/golang/go/issues/17895>) | BLOCK | **Accepted.** And it applies to `01`'s lexical BM25 path too, so it is recorded as overview §8.9, not as an `02`-only concern. |
| §6.2 (licence/provenance) is a genuine pre-Step-1 blocker **that this plan cannot close, because it never names a table**. Model2Vec the library is MIT; each distilled table inherits its teacher's terms | BLOCK | **Accepted.** Carried to `01` §8.3. Naming one concrete table collapses §6.1, §6.2 and the distribution question at once. |
| Dependency hygiene fails on **cost**: zero `go:embed` directives in the repo, largest tracked file 72 KB, 25.9 MB binary, no release/artifact CI job. An 8-30 MB blob is a 30-115% size regression and a new distribution concern | MEDIUM | **Accepted.** Carried to `01` §8.2. |
| §4.2 asserts a hand-rolled pure-Go tokenizer without evaluating the existing option (`sugarme/tokenizer`). Hand-rolling a parser for an untrusted third-party format is a **missing-foundation** finding under the architecture-review skill | MEDIUM | **Accepted.** Carried to `01` §8.4. |
| INV-CE-02-D ("every `01` invariant test still passes unchanged") is **false**: `01`'s graph test is calibrated to the lexical retriever's score distribution and can flip purely from swapping the embedder. The drop-in claim holds for schema, sub-budget and edge derivation, but not for the test suite | MEDIUM | **Accepted.** `01`'s retrieval-quality fixtures must be marked retriever-scoped from the outset. |
| INV-CE-02-B (silent fallback to lexical on a corrupt artifact) **contradicts** INV-CE-02-A and `01`'s INV-CE-01-E: two hosts, one with a bad checksum, produce different recall for identical input | MEDIUM | **Accepted.** Carried to `01` §8: the active embedder's identity must be part of the embedding version, so fallback is a visible version change, not a silent divergence. |
| §7's slices are mis-ordered: slices 1-3 (tokenizer, loader, `Embedder`) are independently landable **before** slice 5 proves any benefit. The skill requires blocking an independently landable stage when a later stage is what justifies it | LOW | **Accepted.** Any future attempt measures on a throwaway prototype first. |
| Repo constraint claims check out: no cgo (`grep -rn 'import "C"'` → 0 hits), `modernc.org/sqlite v1.54.0`, `embedded-persistence.md:46` cited exactly | - | Baseline clean. The plan was accurate about the *constraints* and wrong about the *schedulability*. |

### Deleted as redundant

- §4.4 "Rejected alternatives" - all four already settled program-wide in
  `00-overview.md` §6 and INV-CE-E.
- §3's constraint bullets - verbatim restatement of the overview.
- §7 delivery slices - unbuildable and mis-sequenced.

## 1. Why this document is kept

As the record of a Step 0 that concluded a plan should not be a plan.

`02` was well-formed: correct constraints, honest open decisions, a clear
statement of what it bought. It was still the wrong artifact, because every
question it asked could only be answered by work that had not happened. The
signal to watch for is a plan whose open-decisions section defers **all** of
its decisions to another plan's measurement - that is a note, not a plan,
and turning it into a document invites it to be scheduled.

The four things it genuinely contributed - the vocabulary-miss failure case,
the artifact cost, the licence blocker, and the silent-tokenizer-mismatch
hazard - are preserved in `01` §8, where they will be read at the moment
they become actionable.
