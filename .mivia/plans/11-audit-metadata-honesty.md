# 11 — Audit metadata: name it for what it is, or stop computing it

**Status:** Design-ready; one open decision (§3).
**Date:** 2026-07-30
**Depends on:** `10` (implemented). **Blocks:** nothing.
**Blast radius:** LOW — smaller than it looks. See §2.

---

## 1. Problem

`runtime.Metadata` carries two fields whose names assert a guarantee the code no
longer makes:

```go
// internal/runtime/dispatcher.go:48
RedactedInput, RedactedOutput string
```

Since plan `10`, redaction is configuration-only and off unless a workspace
opts in. A field called `RedactedInput` that contains the raw input is worse
than an unnamed one: a reader who wires a sink, ships these to a log
aggregator, or writes them to disk will reasonably assume the name is a
statement of fact. It is now a statement of *intent at best*, and false at
defaults.

This is the exact failure mode rule 10 warns about — a floor that is not a
floor — expressed as a variable name.

### The larger finding: they are computed and never read

Nothing in the shipped product consumes them. Re-derived at HEAD 2026-07-30:

| | |
|---|---|
| Written | `dispatcher.go:345,346` (success path), `:370,372,375` (failure path) |
| Read by production code | **none** |
| Read by tests | `internal/runtime/dispatcher_test.go` only |
| Reachable externally | via `Policy.Sink` (`dispatcher.go:61`), which **no production code ever sets** — `grep -rn "Sink" --include=*.go` outside tests returns only the field declaration and its own nil check |

`Metadata` does travel further — into `subagents.Result.Provenance`
(`subagents.go:36,218`) — but the only field anyone reads there is `.Kind`
(`cli/orchestrate_lifecycle.go:118`).

So on **every dispatch**, both paths pay for `redactMeta` twice: a JSON
unmarshal, a full recursive policy walk, a re-marshal, and a 256-byte cap —
and throw the result away. The misleading name is the visible defect; the dead
work behind it is the larger one.

> Note the two are entangled. Renaming alone keeps paying for output nobody
> reads. Deleting alone discards a genuine observability hook. §3 is the choice.

## 2. Why the blast radius is small

An earlier estimate (mine) said this "touches the dispatcher's public-ish
surface and every consumer". That was wrong, and re-deriving it is what turned
this from a rename into a real question:

- 5 write sites, all in one file.
- 0 production read sites.
- Test references are confined to `internal/runtime/dispatcher_test.go`.
- The `internal/coordinator` tests named `TestCoordinator_RedactedOutput` and
  `TestIntegration_RedactedOutputRefNotRaw` do **not** touch these fields
  despite their names — they assert the coordinator's own `OutputRef` bounding.
  Do not rename those tests as part of this; their names are separately
  misleading, which is worth a follow-up but is not this plan.

`internal/runtime` is not an exported package (`internal/`), so there is no
external API compatibility constraint. This is a mechanical change.

## 3. Options — DECISION REQUIRED

### A. Rename only

`RedactedInput`/`RedactedOutput` → `InputPreview`/`OutputPreview`.

Honest: a preview is bounded (256 bytes) and passed through whatever policy is
configured, which is exactly what these are. Keeps the observability hook for
an embedder who wires a `Sink`. Cost: keeps paying for the redaction pass on
every dispatch for output nobody currently reads.

### B. Delete the fields

Removes the dead work and the false name together. Cost: an embedder wiring a
`Sink` loses the payload preview and gets only hashes, status and timing —
`InputHash`/`OutputHash` remain, so correlation still works, but the content is
gone. Also removes the only thing that would make a `Sink` worth wiring.

### C. Rename and compute lazily

Rename per A, and populate only when `policy.Sink != nil`. Keeps the hook,
removes the cost when unused — which today is always.

**Recommendation: C.** It is the only option that fixes both defects without
discarding a capability. The guard is one condition at each of the five write
sites, or better, one helper that the sites call. B is defensible if the view
is that an unwired hook should not exist at all; A is not recommended, because
it fixes the name while leaving the reason the name mattered.

Whichever is chosen, state it in this file before implementing.

## 4. Changes (assuming C)

| Site | File | Change |
|---|---|---|
| Field | `internal/runtime/dispatcher.go:48` | rename to `InputPreview`, `OutputPreview` |
| Success path | `:345-346` | populate only when a sink is attached |
| Failure path | `:370-375` | same |
| Helper | `dispatcher.go` | `func (d *Dispatcher) previewFor(b []byte) string` returning `""` when `d.policy.Sink == nil` — one place to hold the condition, so a future write site cannot forget it |
| Doc comment | `Metadata` | state plainly: bounded preview, redacted **only** per the configured policy, empty when no sink is attached |

`redactMeta` keeps its name — it is genuinely the metadata redaction path — but
its comment should stop implying it guarantees anything.

## 5. Verification

```bash
go build ./... && go vet ./...
go test ./internal/... ./cmd/... -race
make verify && make invariants
```

**Tests:**

- `TestMetadataPreviewEmptyWithoutSink` — the fields are empty when no sink is
  attached, proving the work is skipped rather than merely unread.
- `TestMetadataPreviewPopulatedWithSink` — with a sink attached, previews are
  present and bounded to 256 bytes.
- Retarget the existing `internal/runtime/dispatcher_test.go` cases (they
  currently assert on `RedactedInput`/`RedactedOutput` and must attach a sink,
  since under C the fields are empty otherwise). **Do not delete them** — they
  carry the plan-10 assertions that an unconfigured policy redacts nothing and
  that `prompt`/`reasoning` survive.

**Mutation proofs:**

| # | Mutation | Test that MUST fail |
|---|---|---|
| M1 | Populate previews unconditionally again | `TestMetadataPreviewEmptyWithoutSink` |
| M2 | Drop the 256-byte cap | `TestMetadataPreviewPopulatedWithSink` |
| M3 | Reintroduce `prompt`/`reasoning` elision | `TestDispatcherNeverRedactsPromptOrReasoning` (exists) |

**Docs:** none required — `internal/runtime` is not user-facing and no doc
describes these fields. If §3 selects B, `docs/architecture/overview.md`'s
dispatch description should be checked for a claim about emitted payloads.

## 6. Rollback criterion

If an embedder needs previews unconditionally (a sink attached after first
dispatch, for instance), the fix is to make sink attachment explicit at
construction rather than to restore unconditional computation — a field
populated for a consumer that does not exist is the defect this plan removes.
