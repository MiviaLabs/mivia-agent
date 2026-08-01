# 44 — Lifecycle Hooks Enhancement Plan

**Status**: Analysis complete — ready for phased implementation
**Date**: 2025-07-15
**Scope**: All three hook layers (Git hooks, Python agent guard, Go lifecycle hooks), 8 skills, agent routing
**Parent plan**: `44-lifecycle-hooks-analysis-and-recommendations.md` (top-level summary moved here as overview)

## Phases

| Phase | File | Items | Status |
|-------|------|-------|--------|
| 0 | `00-overview.md` | Architecture audit, security audit, corrections | Done |
| 1 | `01-hook-output-framing.md` | R1 — structural framing for model-visible hook output | Done |
| 2 | `02-defense-in-depth-and-docs.md` | R2, R3, R4 — gofmt hook, destructive-command guard, hook-layer docs | Done |
| 3 | `03-ux-testing-and-docs.md` | R5, R7, R8 — subagent test, Stop example, agent-with-exec threat model (R6 obsolete: no trust store) | Done |
| 4 | `04-v2-lifecycle-events.md` | R9, R10, R11 — SessionStart, SubagentStart/Stop, skill-level hooks | Planned |

## Residual Risk

1. **S5** (hook output as prompt injection): Closed in Phase 1. Hook output is wrapped in `<lifecycle-hook-output>`…`</lifecycle-hook-output>` with an in-band advisory notice, and tags the hook's own bytes wrote are neutralized so the payload cannot close its own frame. Residual: framing is attribution, not a sandbox — a model may still be persuaded by framed text, and user confirmation remains the control over whether a script runs.
2. **S6** (script-content vs agent exec): Documented, accepted limitation. An agent can rewrite a hook script - a PROJECT hook's script directly, since it sits inside the workspace, and a user hook's through `run_command`. Framing, attribution and per-run transcript rows bound the damage; moving a hook to the user config is the mitigation. Phase 3 (R8).
3. **Stop hook fires only in interactive TUI**: `-p` one-shot and `--plain` REPL have no turn-end publish point. Still open. Documented as a seam gap rather than a design choice, in `hooks_runner.go` and now in the Stop example's caveat. Phase 3 (R7) shipped the example; closing the gap needs a turn-end publish site on those surfaces, which is its own change.
