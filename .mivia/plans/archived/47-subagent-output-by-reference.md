# 47 - Subagent output by reference

**Status:** IMPLEMENTED
**Date:** 2026-08-02
**Depends on:** existing ledger contentref machinery (`internal/ledger`,
`internal/contentref`), `ledger_read` pagination (`internal/cli/ledger_tools.go`).
**Blocks:** nothing.
**Blast radius:** MEDIUM - model-facing result envelope for `dispatch_tasks`,
`spawn_agent`, `delegate`, `join_run`; parent-agent prompting; no storage
schema changes.

## 1. Goal

Stop duplicating full subagent output into the parent transcript. Today
`encodeResults` (`internal/cli/dispatch.go`) and `delegateResultPayload` emit
both `output` (entire body inline) **and** `output_ref`. On a fan-out of N
long reports this is the largest single token cost in the orchestration path.
The content-addressed ref machinery and `ledger_read`'s offset/limit
pagination already exist precisely to make the parent hold a pointer.

## 2. Implementation Summary

### Config
- Added `InlineOutputBytes` to `SubagentConfig` (default 4096, resolved in
  `resolveSubagentConfig` when unset).
- Added `defaultInlineOutputBytes = 4096` constant in `internal/config/defaults.go`.

### Synopsis (`internal/cli/synopsis.go`, NEW)
- `synopsize(body []byte) string` — bounded, injection-inert preview.
  - JSON objects: emits `{"keys":["k1","k2"],"bytes":N}` (truncated to 512).
  - Everything else: first min(512, len) bytes, UTF-8 boundary safe, `…` suffix.
- `belowInlineThreshold(body, threshold, ref) bool` — the core switch logic.
  INV-AG-10 safe: if no ref is available, always inlines regardless of size.
- `readHint(threshold, bodyLen, ref)` — returns hint string or empty.

### Emitters modified
All four result emitter functions now apply the threshold:
1. `encodeResults` (dispatch_tasks) — method on `dispatchTasksTool`, uses `cfg.InlineOutputBytes`.
2. `delegateResultPayload` (delegate) — takes `threshold int` parameter.
3. `modelTaskResults` (spawn_agent/join_run) — takes `threshold int` parameter.
4. `spawnResultPayload` (spawn_agent) — takes `threshold int`, passes to `runTaskResults`.
5. `joinRunTool.Execute` — passes `cfg.InlineOutputBytes` to `runTaskResults`.

Below threshold: inline `output` + optional `output_ref`. Above threshold with ref:
`output_ref` + `output_bytes` + `synopsis` + `read_hint`. Above threshold without
ref: forced inline (INV-AG-10 safety).

### Struct additions (backward compatible, omitempty)
- `taskResult` (dispatch): `OutputBytes`, `Synopsis`, `ReadHint`.
- `modelTaskResult` (spawn/join): `OutputBytes`, `Synopsis`, `ReadHint`.

### Tool descriptions updated
`dispatch_tasks`, `spawn_agent`, `delegate`, `join_run` — added sentence about
`output_ref` and `ledger_read`.

### Not implemented (YAGNI)
- Aggregate envelope budget (section 3.3): per-task threshold already bounds
  the aggregate linearly (max 16 × 4096 = 65 KiB, well within dispatcher floor).
- Scope audit (section 3.4): `ledger_read` is NOT `PrivilegedTool`, so spawned
  agents already have access. No change needed.

## 3. Invariants preserved
- Every emitted `output_ref` resolves (INV-AG-10).
- `output` and `output_ref` are both present below threshold when ref available.
- `output` alone when below threshold with no ref.
- Ref+synopsis only when above threshold with ref.
- Inline forced when above threshold with no ref (INV-AG-10 safety).
- Result envelope is always complete, valid JSON with one entry per task.

## 4. Testing
- `synopsis_test.go`: 14 tests covering empty, short, exact boundary, above,
  UTF-8 boundaries (2-byte and 3-byte runes), JSON objects/arrays/strings/invalid,
  large JSON key truncation, single byte at boundary, threshold logic.
- `output_by_ref_test.go`: 10 tests covering small inlined, large ref+synopsis,
  INV-AG-10 no-ref forced inline, mixed fan-out, JSON synopsis, modelTaskResults
  threshold, boundary conditions, zero threshold, error threshold.
- All pre-existing tests pass unchanged (small outputs stay backward compatible).

## 5. Files changed
- `internal/config/types.go` — added `InlineOutputBytes` field
- `internal/config/defaults.go` — added `defaultInlineOutputBytes` constant
- `internal/config/load.go` — resolve `InlineOutputBytes` when unset
- `internal/cli/synopsis.go` (NEW) — synopsize, belowInlineThreshold, readHint
- `internal/cli/synopsis_test.go` (NEW) — 14 synopsis tests
- `internal/cli/output_by_ref_test.go` (NEW) — 10 integration tests
- `internal/cli/dispatch.go` — encodeResults threshold logic + new fields
- `internal/cli/delegate.go` — delegateResultPayload threshold logic + new fields
- `internal/cli/orchestrate_lifecycle.go` — modelTaskResults/runTaskResults threshold
- `internal/cli/orchestrate.go` — spawnResultPayload threshold + description update
