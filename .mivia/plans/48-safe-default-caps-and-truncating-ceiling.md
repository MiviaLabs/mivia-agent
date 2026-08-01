# 48 - Safe default caps and truncating output ceiling

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** nothing.
**Blocks:** makes plan `49` (summarizing compaction) cheaper by bounding what
enters history in the first place.
**Blast radius:** MEDIUM-HIGH - default config values (behavior change for
every install), dispatcher ceiling semantics, tool truncation paths,
contextstate persistence compatibility.

## 1. Problem

Out of the box nearly every content bound is disabled and the enforcement
that remains is destructive:

- `internal/config/defaults.go` ships `MaxReadBytes: 0`, `MaxWriteKB: 0`,
  `MaxOutputBytes: 0`, `MaxListDirEntries: 0`, `MaxToolResultBytes: 0`.
  Read-class tools fall back to a 256 MiB OOM backstop, not a context guard.
- The dispatcher output ceiling (`internal/runtime/output_ceiling.go`)
  **destroys** over-ceiling results whole (`overCeilingError`): the agent pays
  for the tool run, gets an error, and retries - maximally token-wasteful for
  honest overshoot.
- `contextstate.MaxSourceEventBytes = 64 KiB` hard-rejects oversized source
  events, so an uncapped tool result that made it into history may be
  unpersistable - the two subsystems' limits are unreconciled (the last "fix"
  was commit `16512b2` bumping the constant).
- A single huge tool result also forces plan-`41` compaction to evict its
  whole unit (units are atomic), taking sibling context with it.

## 2. Goal

Ship bounded-by-default behavior where every cap is (a) non-zero, (b)
mutually consistent from tool -> loop -> dispatcher -> durable store, and (c)
enforced by truncation-with-notice rather than destruction wherever the
overshoot is honest.

## 3. Design

### 3.1 New defaults (operator-overridable, 0 still means "explicitly unlimited")

| Knob | Current | New default | Rationale |
|---|---|---|---|
| `max_tool_result_bytes` | 0 | 49152 (48 KiB) | fits under `MaxSourceEventBytes` (64 KiB) with framing/hook headroom |
| `max_read_bytes` | 0 | 262144 (256 KiB) | window reads already self-truncate honestly; read tool clamps to `max_tool_result_bytes` reserve as today |
| `max_output_bytes` (run_command) | 0 | 131072 (128 KiB) | build logs: tail matters, see 3.3 |
| `max_list_dir_entries` | 0 | 2000 | ample; byte cap still applies |
| `max_write_kb` | 0 | 4096 (4 MiB) | generous input bound; not a context cost |

Changing the meaning of `0` is NOT proposed: `0` stays "unlimited" for
backward compatibility, but `DefaultToolsConfig` stops emitting zeros. A
loud startup log line when any cap is explicitly 0 makes opt-out visible.

Consistency rule, enforced by a config-load validation: effective per-tool
result bound + `runtime.MaxHookContextBytes` + event framing must be
<= `contextstate.MaxSourceEventBytes`, else load warns and clamps. This kills
the live mismatch permanently instead of chasing it with constant bumps.

### 3.2 Ceiling: truncate honest overshoot, destroy only runaways

Split the dispatcher ceiling into two thresholds:

- **Soft bound** = declared/effective result budget. Between soft and hard:
  tail-truncate with the standard `... (truncated N bytes)` notice at a rune
  boundary (reusing `trimPartialRune`), preserving the head. Result survives.
- **Hard bound** = current ceiling formula (`max(budget, 256 KiB) + input +
  slack`) x 2. Above hard: destroy as today - this remains the runaway-tool
  backstop the ceiling was designed to be.

Tools that already self-truncate honestly (declare `ResultBudgetBytes`)
should rarely hit the soft band; when they do, their own notice is preserved
because the soft cut reserves space for an outer notice the same way tool
budgets already reserve notice bytes.

### 3.3 run_command keeps head+tail

For `run_command` specifically, blind head-cuts lose the failure (compilers
print errors last). Change `dualCapture` to retain head `1/3` + tail `2/3` of
`max_output_bytes` with an elision marker `... (M bytes elided) ...`. The
`exit=` header line is always preserved.

### 3.4 Dereferenceable remainder (bounded scope here)

Full "page the truncated remainder via contentref" is deferred to its own
plan; this plan only requires that truncation notices state the total size
(`truncated: kept X of Y bytes`) so the model can re-request a window
deliberately (`read_file` offset/limit already supports this; `grep` should
narrow). No new storage.

## 4. Invariants

- No emitted result exceeds its declared claim: notices are paid out of the
  budget (existing reserve accounting extended to the new soft cut).
- Truncation never splits a UTF-8 rune or an assistant/tool pairing.
- Persistence never rejects a result the loop accepted (the 3.1 consistency
  validation is the guarantee).
- `search_replace` gains a declared result budget and a file-size guard
  (currently the only mutating tool with neither), and preserves file mode on
  write (`os.WriteFile(0644)` bug) - included here because the guard depends
  on `max_read_bytes` gaining a real default.

## 5. Implementation steps

1. New defaults in `DefaultToolsConfig` + startup warning for explicit zeros.
2. Config-load consistency validation vs `MaxSourceEventBytes` (clamp+warn).
3. Soft/hard ceiling split in `runtime` dispatcher; extend
   `result_cap_registry_test.go` gates.
4. `dualCapture` head+tail retention for `run_command`.
5. `search_replace`: size guard, declared budget, mode-preserving write
   (stat then `WriteFile` with original perm).
6. Truncation notices gain total-size figures across tools.
7. Changelog/docs entry: behavior change for default installs.

## 6. Testing

- Matrix test: each tool at cap-1 / cap / cap+1 / hard+1 asserting
  survive-truncated vs destroyed.
- Consistency validation unit tests (explicit zero, over-64KiB config, clamp).
- run_command: failing build fixture asserts the error tail survives.
- Persistence round-trip: max-size tool result commits as a source event
  without `MaxSourceEventBytes` rejection.
- `search_replace` on an executable file preserves `+x`.

## 7. Failure analysis

- Operators relying on unlimited defaults see new truncation: mitigated by
  startup logging, honest notices with totals, and `0` opt-out unchanged.
- Soft-truncated JSON tool output becomes invalid JSON: tools that emit JSON
  (`find_references`) already self-truncate to valid JSON under their own
  budget and stay below the soft band by construction; the soft cut is a
  last-resort for non-declaring tools.
