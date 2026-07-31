# 33.3 — `internal/hooks/exec.go`: the execution path and wire protocol

**Status:** DESIGN — ready.
**Date:** 2026-08-01
**Parent:** [`00-overview.md`](00-overview.md) §3c, §7, §8, §8a, §11a
**Depends on:** `02`.
**Blocks:** `06`.
**Blast radius:** HIGH — this slice is the one that runs arbitrary code. It is
reachable only from tests until slice `06` wires it, and slices `04`/`05` gate it
before that happens. That ordering is deliberate (`00-overview.md` §15).

---

## 1. Why hooks get their own exec path

`00-overview.md` §3c records that `run_command`'s path structurally rejects hooks:
path-shaped `argv[0]` is refused (`internal/tools/run.go:207-211`), the program must
be on the run allowlist, cwd is pinned, secret-like paths in argv are blocked, and
output is redacted and wrapped in a `command:/cwd:/exit=` header that would corrupt
the JSON protocol. Reusing it is not a simplification, it is a non-starter.

Adding hook scripts to `run_allowlist` to make them fit would be worse: it hands the
**model** the ability to invoke them.

## 2. The contract

1. `argv[0]` is a **filesystem path**, resolved relative to the directory of the
   config file that declared the hook; absolute allowed. **No `PATH` lookup** — a hook
   must not resolve to a different binary because `PATH` changed.
2. **No shell.** `exec.CommandContext(ctx, argv[0], argv[1:]...)`. No `sh -c`, no
   `shellwords`, no `$VAR` interpolation anywhere.
3. cwd is the workspace root; env is the filtered env plus a fixed `MIVIA_*` set:
   `MIVIA_HOOK_EVENT`, `MIVIA_TOOL`, `MIVIA_FILE`, `MIVIA_SESSION_ID`,
   `MIVIA_WORKSPACE_ROOT`. Tool-derived values reach the hook **only** here and in the
   stdin JSON — never as command-line syntax. This is Grok's model (`GROK_HOOK_EVENT`
   et al., `00-overview.md` §1a) and it is why the injection row in §11 holds.
4. stdout/stderr are captured under an explicit byte bound and are **not** redacted or
   reformatted — they are a machine protocol, not model-visible tool output.
5. Hooks never construct a `runtime.Request` and never call `Dispatcher.Invoke`
   (`00-overview.md` §11a).

## 3. Wire protocol

stdin, one JSON object per `00-overview.md` §8. Control by exit code:

- `0` → success; **stdout is parsed as JSON** (and only at exit 0, matching Claude and
  Codex).
- `2` → block; **JSON is ignored**, stderr is the reason.
- other → non-blocking warning; stderr surfaced to the user; execution continues.

Structured stdout, per event (`00-overview.md` §8 — the first draft used the wrong
event's shape, and the mismatch failed *open*):

- `PreToolUse` → `hookSpecificOutput.permissionDecision` ∈ {`allow`, `deny`}, with
  `permissionDecisionReason`. `ask` and `defer` are **parse errors** — mivia has no
  dispatcher-layer prompt for them to escalate to, and mapping an unknown decision
  onto the permissive branch is exactly the drift this avoids.
- `PostToolUse` / `Stop` → flat `{ "decision", "reason", "additionalContext" }`.
- `updatedInput` present → rejected (`00-overview.md` §8a).
- **Unparseable stdout at exit 0 falls back to exit-code semantics with a warning.**
  It is never read as a decision.

## 4. Timeouts

Per-handler `timeout`, defaults per `00-overview.md` §7. `on_timeout` decides the
verdict: **`block` for `PreToolUse`**, `allow` elsewhere. A hung gate must not be an
open gate — that is the correction this slice implements, and it is the difference
between a security control and a suggestion.

Context lineage matters and is easy to get wrong:

- the timeout context derives from the **dispatcher's incoming `ctx`**, not from
  `execute`'s `callCtx`, which does not exist yet at the `PreToolUse` point
  (`internal/runtime/dispatcher.go:347-351`);
- at the `PostToolUse` point that `callCtx`'s deferred `cancel()` may already have
  fired, so a hook run on it would silently never execute.

Hook time is **not** charged against `req.Timeout`. A tool granted 300s by
`run_command`'s `Capability` (`internal/tools/run.go:37-46`) must not lose it to hooks.

Killed hooks are reported, never silently dropped, and the process group is cleaned up
the way `run_command` already does (`waitCommand`, `internal/tools/run.go:154-179`) —
reuse that *shape*, not that path.

## 5. Output bound

Total hook stdout for one invocation is bounded by a package constant (proposed 8 KiB,
not configurable in v1) and **truncated with a notice** when exceeded — not refused.
Unlike tool output, which the dispatcher destroys (INV-AG-25), hook stdout is advisory;
destroying a tool result because its formatter was chatty is the worse failure. Slice
`06` owns where those bytes land.

## 6. Verification

`go test ./internal/hooks/...`:

- `argv[0]` resolves against the config dir; a bare name is **not** found via `PATH`
- no shell metacharacter is ever interpreted: a hook whose argv contains `;`, `&&`,
  backticks, or `$(...)` receives them as literal argv elements
- a filename containing shell syntax passed through `MIVIA_FILE` does not execute
- exit 0 parses JSON; exit 2 reads stderr and **ignores** stdout JSON; other exits warn
- unknown `permissionDecision` is a parse error, not `allow`
- unparseable stdout at exit 0 falls back to exit code, with a warning
- timeout kills the process and its group; `on_timeout="block"` denies, `"allow"` warns
- stdout past the bound is truncated with a notice, not dropped and not refused
- a hook run on an already-canceled context is detected rather than silently skipped

## 7. Done when

The exec path can be handed a hostile hook definition and a hostile tool argument and
neither becomes command syntax — proven by test, not by inspection.
