# 44 — Lifecycle Hooks Enhancement Plan

**Status**: **COMPLETE** — implemented 2026-08-01, archived
**Planned**: 2025-07-15
**Scope**: All three hook layers (Git hooks, Python agent guard, Go lifecycle hooks)

## Phases

| Phase | File | Items | Status |
|-------|------|-------|--------|
| 0 | `00-overview.md` | Architecture audit, security audit, corrections | Done |
| 1 | `01-hook-output-framing.md` | R1 — structural framing for model-visible hook output | Done |
| 2 | `02-defense-in-depth-and-docs.md` | R2, R3, R4 — gofmt hook, destructive-command guard, hook-layer docs | Done |
| 3 | `03-ux-testing-and-docs.md` | R5, R7, R8 — subagent test, Stop example, agent-with-exec threat model | Done |
| — | — | R6 — `/hooks untrust`/`prune` | **Obsolete**, not built: phase 1 deleted the trust store it operated on |

v2 lifecycle events (`SessionStart`, `SubagentStart`/`SubagentStop`, skill-level
hooks) were phase 4 here and are now **`.mivia/plans/45-v2-lifecycle-events.md`**
— separate work, not a tail of this plan. That file records which of its
assumptions this plan invalidated; read it before starting there.

## What shipped beyond the written plan

Two changes came from the operator mid-implementation and are not in the phase
files, so they are recorded here:

- **Hook trust confirmation was removed entirely.** No trust store, no
  definition hash, no `/hooks trust`, no `--bypass-hook-trust` (accepted and
  ignored with a notice so CI configs still start). A declared hook runs,
  interactively and headless alike. Disclosure replaces the prompt: startup
  names every armed hook, `/hooks` lists them, every execution gets a row.
- **Project-scoped hooks.** A workspace `.mivia/mivia.toml` supplies hooks,
  additively with the user config and ordered after it. Previously a workspace
  hook table was stripped with a warning. This is why a cloned repository can now
  execute commands on first launch — chosen deliberately, disclosed everywhere.

Two defects were found by the work rather than predicted by the plan:

- `PreToolUse` `additionalContext` on the ALLOW path reached nothing. It was
  dropped twice over: `parsePreToolUse` returned an empty verdict on `"allow"`,
  and the dispatcher read `verdict.Context` and discarded it.
- Hook execution was invisible. `Result.HookRuns`, `agent.EventHook` and the TUI
  banner exist because of that, including rows for runs that produce no output.

## Residual Risk

1. **S5 (hook output as prompt injection): closed as far as framing can close
   it.** Output is wrapped in `<lifecycle-hook-output>` tags the payload cannot
   forge — either case, either direction, with attributes, split across lines —
   and neutralization only ever shrinks the payload. Residual: framing is
   attribution, not a sandbox. A model may still be persuaded by framed text.
2. **S6 (script content vs agent exec): accepted, documented, and now larger.**
   An agent can rewrite a hook script. A *project* hook's script sits inside the
   workspace where `write_file` reaches it directly; a *user* hook's is reachable
   only through an allowlisted `run_command`. Moving a hook to the user config is
   the mitigation. See the threat-model section in
   `docs/development/lifecycle-hooks.md`.
3. **A cloned repository's hooks run unprompted.** The deliberate cost of project
   hooks. Bounded by: hooks load from exactly two fixed paths, a faulty workspace
   config never fails startup, and every armed hook is named at startup and
   marked `[project]` in `/hooks`.
4. **`Stop` fires in the interactive TUI only.** `KindTurnEnd` has one publish
   site. `--plain` and `-p` never publish it, so `Stop` is silent there. A seam
   gap rather than a decision; closing it needs a turn-end publish site on those
   surfaces, which is its own change.
5. **The destructive-command guard stops at the mivia layer.**
   `scripts/agent_hook_guard.py` was deliberately not widened: it is shared by
   every adapter agent in the tree, and broadening it unilaterally could block a
   recovery another agent is mid-way through.

## Where the result lives

| Surface | Path |
|---|---|
| Invariant | `INV-AG-34` in `.mivia/invariants.md` |
| Docs | `docs/development/lifecycle-hooks.md`, `docs/development/hooks.md` (hook layers) |
| Config example | `.mivia/mivia.toml.example` |
| This repo's own hooks | `.mivia/mivia.toml` `[[hooks]]`, scripts in `.mivia/hooks/` |
| Policy | `.mivia/policy/agent-hook-bypass.json`, `.mivia/policy/destructive-commands.json` |
