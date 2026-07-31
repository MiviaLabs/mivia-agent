# 33.6 — Dispatcher integration: `PreToolUse` and `PostToolUse`

**Status:** DESIGN — ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §4b, §9, §9a–§9d, §11a
**Depends on:** `03`, **`04`, `05`** — the gate lands before the call site, by
design (`00-overview.md` §15).
**Blast radius:** HIGH — touches the shared invocation boundary and three pinned
invariants (INV-AG-25/26/27).

---

## 1. Where

`internal/runtime/dispatcher.go:251-254`, between the `!res.allowed` check and
`return d.execute(...)`. `PostToolUse` runs on the return path at `:254`.

## 2. Coupling — funcs on `Policy`, not on `Dispatcher`

`internal/runtime` must not import `internal/hooks` (import cycle, and every test
binary would link the exec path). `Policy` gains optional `PreInvokeHook` /
`PostInvokeHook` func fields; CLI wiring sets them when hooks are configured **and
trusted**, nil otherwise. Nil = one nil compare = today's behaviour exactly.

Placement is load-bearing, not incidental (`00-overview.md` §9d). `Sink` is already a
`Policy` field (`dispatcher.go:71`), and `Policy` is copied to derived dispatchers by
`Dispatcher.Policy()` (`:170-176`), which clears only `Allow`. So hook funcs on
`Policy` **propagate to scoped subagent dispatchers** — deliberately. A `PreToolUse`
gate a subagent escapes is not a gate; subagents run the same tools against the same
workspace. This must be asserted by test, because today it would fall out of a struct
copy rather than out of a decision.

## 3. Which kinds fire

**`Kind == runtime.Tool` only.** `Invoke` also dispatches `Skill` and `Subagent`
(`dispatcher.go:18-22`). An event named `PreToolUse` that fires on subagent dispatch
is a lie in a security-relevant name, and a `matcher` regex written against tool names
would match subagent names by coincidence.

Two existing behaviours to pin, both consequences of `Invoke`'s structure rather than
of anything this slice writes:

- **Deduplicated invocations fire no hook.** A repeat `req.ID` returns the cached
  result from `reserve` (`:291-293`) and returns at `:240-247`, before the hook point.
  Correct — the tool did not run — but a hook author will assume one fire per model
  tool call unless it is asserted.
- **A block happens after `reserve` charged the budget** (`:305`) and installed the
  active marker. The deferred cleanup at `:234` still runs and `failResult` still
  delivers to waiters (`:462-467`), so a blocked call does not wedge a duplicate
  waiter. Not obvious from the hook code alone.

## 4. Blocked is not failed

`fail()` routes to `failResult`, which stamps `meta.Status = "failed"` (`:410`). A
policy block and a broken tool would be indistinguishable in the audit sink, the TUI,
and `internal/cli`'s status classification.

Add a distinct `"blocked"` status through the same machinery (so waiters are still
released):

- `meta.Status = "blocked"`;
- payload `{"status":"blocked","error":"<hook reason>"}` — the reason must reach the
  model, that is the entire point of a block;
- per INV-AG-27's existing note, the message **avoids the substrings
  `internal/cli`'s `statusFromErr` matches** for canceled/timed-out, so a block is not
  misreported as a cancellation.

## 5. `PostToolUse` output must not breach the ceiling

`00-overview.md` §9b is a blocker resolved here. Appending hook stdout to
`result.Output` after `execute` returns bypasses the ceiling check at `:383` entirely
— that check is INV-AG-25/26/27, three of the most heavily pinned invariants in the
repo — and would leave `meta.OutputHash` (`:387`) describing bytes the model never
received, which is INV-AG-10's defect one layer over.

**Hook context is never spliced into `Result.Output`.** It travels in a new
`Result.HookContext` field with its own bound (slice `03` §5). The agent loop renders
it as an attributed block, distinct from tool output. This also keeps hook bytes out
of `meta.OutputHash` and `meta.OutputPreview`, so the audit record keeps describing the
tool's bytes — which is what it is for.

## 6. Re-entrancy

Hooks execute out-of-band (slice `03`) and never call `Invoke`. Belt-and-braces: a
process-wide re-entrancy flag so that if a hook ever does reach `Invoke` — via a future
handler type, or a bug — the nested `PreToolUse` is skipped rather than recursing.
`MaxDepth` (`:267`) would not catch it, because hook execution carries no depth.

## 7. Verification

`go test ./internal/runtime/...`:

- nil hook fields → byte-identical results to today
- deny returns status **`"blocked"`**, carries the reason to the model, and **does not
  call the handler**
- a blocked call releases its waiter and clears its active marker
- hooks fire for `Kind == Tool` only — not `Skill`, not `Subagent`
- a deduplicated invocation fires no hook
- **a scoped subagent dispatcher inherits the hook funcs via `Policy()`**
- `PostToolUse` context lands in `Result.HookContext`, is bounded, and
  `meta.OutputHash` / `OutputPreview` still describe the tool's bytes only
- oversized hook stdout is truncated with a notice and does **not** destroy the result
- a `PostToolUse` hook still runs after `execute`'s `callCtx` was canceled
- a `PreToolUse` hook matching `run_command` does not recurse

`go test -race ./...` — the hook funcs are read on every invocation across goroutines.

## 8. Done when

The no-hook path is provably unchanged, a blocked call is distinguishable from a failed
one everywhere it surfaces, and no hook byte can reach the model outside its own bound.
