# 44 — Lifecycle Hooks Enhancement Plan

**Status**: Analysis complete — ready for phased implementation
**Date**: 2025-07-15
**Scope**: All three hook layers (Git hooks, Python agent guard, Go lifecycle hooks), 8 skills, agent routing
**Parent plan**: `44-lifecycle-hooks-analysis-and-recommendations.md` (top-level summary moved here as overview)

## Phases

| Phase | File | Items | Status |
|-------|------|-------|--------|
| 0 | `00-overview.md` | Architecture audit, security audit, corrections | Done |
| 1 | `01-hook-output-framing.md` | R1 — structural framing for model-visible hook output | Planned |
| 2 | `02-defense-in-depth-and-docs.md` | R2, R3, R4 — gofmt example, destructive-git guard, adapter docs | Planned |
| 3 | `03-ux-testing-and-docs.md` | R5, R6, R7, R8 — subagent test, prune command, stop example, trust docs | Planned |
| 4 | `04-v2-lifecycle-events.md` | R9, R10, R11 — SessionStart, SubagentStart/Stop, skill-level hooks | Planned |

## Residual Risk

1. **S5** (hook output as prompt injection): Open design gap. Mitigated by `[lifecycle hook output]` prefix and user trust confirmation, but a structural XML/system-message delimiter would be stronger. Phase 1 (R1).
2. **S6** (script-content trust vs agent exec): Documented, accepted limitation. In mivia's agent-with-exec context, `run_command` can rewrite a trusted hook script. Phase 3 (R8 — documentation only).
3. **Stop hook fires only in interactive TUI**: `-p` one-shot and `--plain` REPL have no turn-end publish point. Documented as "a seam gap, not a design choice" in `hooks_runner.go:52-54`. Phase 3 (R7 — example with caveat).
