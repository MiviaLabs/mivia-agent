# 47 - Subagent output by reference

**Status:** DESIGN
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

## 2. Current state

- `recordRunResults` (`internal/coordinator/record_results.go`) already stores
  every output/error under `ref:output:<sha256>` / `ref:error:<sha256>` and
  drops refs whose write failed (INV-AG-10), so a visible ref always resolves.
- `dispatch_tasks` declares no result budget, falling back to the 256 KiB
  dispatcher floor - yet aggregates N unbounded sub-reports.
- `MaxTokens` is deliberately unset for multi-step subagents (no mid-sentence
  truncation), so report length is unbounded upstream.
- `ledger_read` supports `ref`, `offset`, `limit` with `next_offset`
  continuation - the consumption side already exists and is root-scoped, the
  same scope that can call `dispatch_tasks`.

## 3. Design

### 3.1 Inline threshold

Add a single policy knob, `[orchestration] inline_output_bytes` (default
**4096**). Per task result:

- Body <= threshold: inline `output` exactly as today (small results stay
  cheap and ergonomic; no behavior change for short answers).
- Body > threshold: omit `output`; emit instead:

```json
{
  "task_id": "t1",
  "status": "completed",
  "output_ref": "ref:output:<sha256>",
  "output_bytes": 48213,
  "synopsis": "<= 512 bytes, see 3.2>",
  "read_hint": "use ledger_read with this ref; offset/limit paginate"
}
```

`error` follows the same rule with `error_ref` (errors are usually short and
will typically stay inline).

### 3.2 Synopsis

The synopsis must be cheap and deterministic - NOT a summarization model call:

- First `min(512, len)` bytes of the output, cut at a rune/line boundary, with
  a `…` marker. If the output is valid JSON, instead emit its top-level keys
  and sizes (`{"keys": ["findings", "files"], "bytes": 48213}`).
- Rationale: the parent needs enough to decide *whether* and *which range* to
  read, not a faithful summary. A model-generated synopsis would reintroduce
  cost and a prompt-injection surface.

### 3.3 Aggregate envelope budget

Give `dispatch_tasks`/`spawn_agent`/`join_run` a declared result budget
(`ResultBudgetBytes`) sized as `inline_output_bytes * 16 (max fan-out) +
framing`, so the envelope is bounded by construction instead of relying on the
dispatcher floor. If even the ref-only envelope exceeds budget (pathological),
degrade per-task synopses to empty strings before truncating structure -
truncated JSON must never be emitted.

### 3.4 Parent guidance

- Tool description of `dispatch_tasks`/`spawn_agent` gains one sentence:
  results above the inline threshold return `output_ref`; fetch with
  `ledger_read`, paginate with `offset`/`limit`.
- `ScopeSpawned` note: spawned agents do not get `ledger_read` (privileged);
  this is fine - only the root orchestrator consumes fan-out results. Verify
  no code path hands a ref to a scope that cannot read it; if depth>1
  orchestration is enabled, the intermediate parent has root-like session
  scope for its own run (confirm via `internal/tools/scope.go` before
  implementation; if not, refs must remain inline at that depth or
  `ledger_read` must be granted to orchestrating scopes).

## 4. Invariants

- Every emitted `output_ref` resolves (already guaranteed by INV-AG-10).
- `output` and `output_ref` are mutually exclusive above threshold; both may
  appear below threshold only if backward compatibility requires it during a
  deprecation window (prefer: below threshold emit `output` only, no ref, to
  save envelope bytes - the ref is still in the ledger and discoverable via
  `list_run_events`).
- Result envelope is always complete, valid JSON with one entry per task
  (preserves `finalizeDAG`'s one-result-per-task guarantee).
- Synopsis derivation is pure and injection-inert: raw untrusted bytes are
  clearly bounded and never interpreted.

## 5. Implementation steps

1. Add `inline_output_bytes` config with default 4096, validation >= 512.
2. Implement `synopsize(body []byte) string` (pure, tested: UTF-8 boundary,
   JSON key mode, empty, exactly-at-threshold).
3. Rework `encodeResults` + `delegateResultPayload` to the threshold rule.
4. Declare `ResultBudgetBytes` on `dispatch_tasks`, `spawn_agent`, `join_run`,
   `delegate` per 3.3.
5. Update tool descriptions; add the `read_hint` field.
6. Scope audit per 3.4.

## 6. Testing

- Envelope golden tests: below/at/above threshold, mixed fan-out, error-only
  task, missing/salvaged results.
- Budget test: 16 tasks x max synopsis stays under declared budget.
- End-to-end: parent dispatches, receives ref, `ledger_read` pages the full
  body back with `next_offset` chaining.
- Regression: `terminationReason` vocabulary and status fields unchanged.

## 7. Failure analysis

- Parent ignores the ref and re-runs the subtask: mitigated by `read_hint` and
  description text; observable via `list_run_events`.
- Ref emitted to a scope that cannot dereference it: covered by the scope
  audit in 3.4; fail closed to inline if unresolvable.
- Operator sets threshold huge: caps at declared envelope budget; envelope
  bound holds regardless.
