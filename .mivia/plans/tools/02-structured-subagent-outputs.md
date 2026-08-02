# tools/02 - Structured, schema-validated subagent outputs

**Status:** DESIGN VALIDATED (2026-08-02) - baselines verified against HEAD
`23c8980`; open decisions resolved below. Ready for ADLC Step 0 hostile
challenge, then implementation.
**Date:** 2026-08-02 (revised after code validation)
**Depends on:** plan `47` - **already landed** (`dispatchTaskResult` carries
`output_ref`/`output_bytes`/`synopsis`/`read_hint`; threshold
`cfg.InlineOutputBytes` default 4096 in `internal/config/defaults.go:20`).
**Blast radius:** MEDIUM-HIGH - subagents handler, dispatch/spawn task shape
(6+ edit sites), coordinator fingerprint, skills loader (both read AND write
side), config agent spec, **one new external dependency**. `internal/agent`
loop is NOT touched (decision D1).

## 1. Problem (verified)

- `skills.Definition.InputSchema/OutputSchema` exist at
  `internal/skills/skills.go:25` and **no code at all touches them** - not
  even the SKILL.md loader populates them. Wiring them means both the write
  side (frontmatter parsing) and the read side.
- Subagent replies are free prose: `buildResult`
  (`internal/subagents/multi_step.go:196-232`) emits
  `{output, steps, elapsed, step_count, status}`; `modelVisibleOutput`
  (`internal/cli/delegate.go:220-225`) passes JSON-if-valid with no contract.
- Task shape (`dispatch.go:112-121`, `task_routing.go:79-95`) has no schema
  field; parents consume prose and re-read files the child already read.

## 2. Goal

A task may demand an output schema; the subagent's final output is validated
before the run counts as completed, with bounded retry-on-violation inside
the child (cheap) instead of re-dispatch at the parent (expensive - the only
retry today is whole-task, `internal/coordinator/retry.go`).

## 3. Resolved decisions

**D1 - enforcement seam: handler re-entry, not a loop hook.** The "no
pending tool calls" decision lives in `internal/agent/loop.go:311-315`, not
in `MultiStepHandler`; the handler only sees the returned reply string.
Rather than adding an `agent.Options` validation hook (grows blast radius
into the loop package), `MultiStepHandler.run` wraps `loop.Run` in a
re-entry loop: validate the reply; on violation, call `loop.Run` again with
a bounded corrective **user-role** message (`loop.Messages` persists across
re-entry, so context is retained). Consequence: the correction is a user
turn, not tool-role - acceptable; `RequireFinalText` (loop.go:133) is the
precedent for terminal-shape enforcement staying outside the handler.

**D2 - validator: adopt `github.com/santhosh-tekuri/jsonschema` (v6).**
`go.mod` has no schema library; this is the module's first validation
dependency and a deliberate policy call: maintained, pure Go, no transitive
sprawl, supports compiling with remote `$ref` resolution *disabled* (we
require fail-closed). The hand-rolled `internal/tools` `validateSchema`
stays separate and unchanged - it already enforces
minimum/maximum/minItems/maxItems/enum (tools.go:263-299); its remaining
gaps (nested-object recursion, pattern) are out of scope here.

**D3 - corrective messages are user turns** (from D1), bounded to first 5
errors / 1 KiB, passed through `redact.Text` (`internal/redact/redact.go:137`
- string entry point verified; no []byte variant exists).

## 4. Design

### 4.1 Schema sources, precedence

1. `Task.OutputSchema` (new optional field) - highest.
2. Active skill's `OutputSchema` - requires loader write-side (4.5).
3. Agent definition `output_schema` (new optional TOML field).
4. None -> today's behavior byte-for-byte.

`InputSchema` symmetrically validates `Task.Input` at admission.

### 4.2 Task field plumbing (verified edit sites)

`Task.OutputSchema map[string]any` touches, minimum:

- `dispatch.go:112` (param struct) and `dispatch.go:200-207` (`buildTasks`
  duplicate literal) - strict decoding (`decodeStrictTaskJSON`,
  `DisallowUnknownFields`) means both or the field is rejected;
- `task_routing.go:79-95` (`taskItemSchema`, `additionalProperties:false`);
- `internal/subagents/subagents.go:14` (`Task` struct - carries the
  "work-defining fields" warning comment);
- `internal/coordinator/spawn.go:102-117` (`fingerprintTask`) **and** the
  literal in `requestFingerprint` (:120-135). Omitting either silently
  collapses schema-differing tasks into one idempotency digest - the
  invariant failure this plan names. Add a test that marshals a Task via
  reflection and fails if a field exists that `fingerprintTask` ignores
  without an explicit exclusion list.

Admission caps enforced in `resolveTaskRoute`/`validateTasks`: schema
<= 16 KiB marshaled, max depth 32, compile must succeed with remote `$ref`
disabled - reject at `dispatch_tasks` argument time, before spawn cost.

### 4.3 Child-side enforcement (per D1)

In `MultiStepHandler.run` around `loop.Run`:

1. Strip at most one well-formed code fence (deterministic, tested).
2. Validate against the compiled schema.
3. Valid -> `buildResult` gains `output` as the parsed object and
   `schema: "ok"`.
4. Invalid and attempts < `schema_retry_max` (default 2, config) ->
   re-enter `loop.Run` with the redacted, bounded corrective user message.
   Re-entry consumes the existing step/wall-clock budgets
   (`MaxSteps` enforced at loop.go:119; `timeoutContext` never extends past
   parent) - no budget is ever extended.
5. Exhausted -> status `failed`; `reason: "schema_violation"` joins the
   fixed `terminationReason` vocabulary (`dispatch.go:361-388`, which
   deliberately never carries error text); last candidate goes out as
   `error_ref` only - known-malformed output is never inlined.

### 4.4 Parent-side contract

- `dispatchTaskResult` (dispatch.go:237-259) gains `schema: ok|violation|
  none`; mirror in `mergeOutputFields` (delegate.go:196-208).
- Schema-valid output below `InlineOutputBytes` is inlined as an object;
  above it, existing plan-47 ref path applies unchanged (`synopsize`
  already handles JSON via top-level-keys mode, synopsis.go).
- Host appends a deterministic "return JSON matching this schema"
  instruction to the task prompt when a schema is in force.

### 4.5 Skills and agent definitions

- Skills loader: parse `input_schema`/`output_schema` from SKILL.md
  frontmatter into the existing dead fields (write side), then read them in
  the handler's schema-resolution chain. Same admission caps apply at load
  time; a skill with an uncompilable schema is skipped with a bounded
  warning (matching the loader's existing resilience pattern).
- `config.AgentFileSpec` (`internal/config/agents.go:48-73`) gains
  `OutputSchema` (pointer, presence-preserving for inheritance, go-toml
  default key naming - the struct has no toml tags). `ResolvedAgent` gains
  the resolved copy; `Clone()` must deep-copy it. Free win: the field feeds
  `DefinitionDigest()`, so an agent-level schema change is automatically a
  new digest -> new fingerprint.

## 5. Invariants

- No schema anywhere in the chain -> behavior byte-for-byte unchanged.
- Re-entry never exceeds existing step or wall-clock budgets.
- `completed` + `schema: ok` guarantees the object validates; parents may
  consume without re-checking.
- Malformed output never inlined into the parent transcript.
- `fingerprintTask` covers `OutputSchema` (reflection guard test).
- Remote `$ref` resolution disabled; compile failures fail closed at
  admission/load, never at run time.
- Corrective text is redacted and bounded before entering the child
  transcript.

## 6. Implementation steps

1. Add `santhosh-tekuri/jsonschema` dep + thin wrapper package
   (compile-with-caps, validate, bounded error formatting).
2. Task field plumbing per 4.2 incl. fingerprint + reflection guard test.
3. Handler re-entry loop per 4.3; `schema_retry_max` config.
4. Envelope `schema` field + `schema_violation` reason (dispatch + delegate
   paths).
5. Skills frontmatter write side + read side; agent TOML field + resolve/
   Clone/digest.
6. Host prompt-instruction appendix; docs for schema authors.

## 7. Testing

- Matrix: no schema / valid first try / valid after 1 retry / exhausted /
  uncompilable at admission / oversized schema / remote $ref rejected.
- Budgets: re-entry cannot exceed MaxSteps or wall clock (fixture with
  retry_max 2 and MaxSteps already nearly consumed).
- Fence-strip: exactly one fence, nested fences, no fence.
- Fingerprint: same task ± schema -> different digests; reflection guard.
- Inheritance: agent schema inherited, overridden by skill, overridden by
  task; Clone deep-copy (mutate parent map, child unaffected).
- End-to-end: parent dispatches with schema, consumes `output.files[]`
  with zero re-read tool calls in the parent trace.

## 8. Failure analysis

- Prose-padded JSON: single-fence strip only; anything looser is a
  validator bypass.
- Chronically failing schema: `schema_violation` is countable in run
  events; authors see it immediately.
- Injection via schema error text: bounded, redacted, user-role framed -
  and `terminationReason` never carries it.
- Dependency risk (D2): wrapper package isolates the library; swapping
  validators later touches one package.
