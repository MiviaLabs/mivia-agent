# 44 — Lifecycle Hooks Enhancement: ADLC Task Catalog

**Plan directory**: `44-lifecycle-hooks/`
**Phases**: 4 (P0→P1→P2→P3)

## Phase 1 — Hook Output Framing (P0)

| ID | Task | Scope | Est. LOC | Gate |
|----|------|-------|----------|------|
| 1.1 | Design framing delimiter (XML tags vs fenced block vs JSON) | Design doc | 0 | Decision recorded |
| 1.2 | Implement framing in `appendHookContext` | `internal/agent/loop_tools.go` | ~15 | Unit test |
| 1.3 | Update existing tests for new delimiter | `internal/agent/hook_context_test.go`, `internal/runtime/hooks_test.go` | ~20 | `go test` pass |
| 1.4 | Add model-facing docs for hook output framing | `docs/development/lifecycle-hooks.md` | ~15 | `make docs-check` |
| 1.5 | Update agent prompt guidance (if applicable) | System prompt / `AGENTS.md` | ~5 | Review |

## Phase 2 — Defense-in-Depth and Docs (P1)

| ID | Task | Scope | Est. LOC | Gate |
|----|------|-------|----------|------|
| 2.1 | Create `gofmt-check.sh` example PostToolUse hook | New file + `docs/development/lifecycle-hooks.md` | ~40 | Example runs |
| 2.2 | Document gofmt example in lifecycle-hooks.md | `docs/development/lifecycle-hooks.md` | ~25 | `make docs-check` |
| 2.3 | Create `block-destructive-git.sh` example PreToolUse hook | New file + `docs/development/lifecycle-hooks.md` | ~40 | Example blocks |
| 2.4 | (Optional) Extend `agent-hook-bypass.json` with destructive git patterns | `.mivia/policy/agent-hook-bypass.json` | ~5 | `make agent-hook-test` |
| 2.5 | Document destructive-git example in lifecycle-hooks.md | `docs/development/lifecycle-hooks.md` | ~25 | `make docs-check` |
| 2.6 | Add "Hook layers" section to `docs/development/hooks.md` | `docs/development/hooks.md` | ~30 | `make docs-check` |
| 2.7 | Add cross-reference in `docs/development/lifecycle-hooks.md` | `docs/development/lifecycle-hooks.md` | ~5 | `make docs-check` |

## Phase 3 — UX, Testing, and Docs (P2)

| ID | Task | Scope | Est. LOC | Gate |
|----|------|-------|----------|------|
| 3.1 | Integration test: subagent PreToolUse hook inheritance | `internal/subagents/` | ~80 | `go test` pass |
| 3.2 | Test `sessionHookState` reads from subagent goroutines | `internal/subagents/` or `internal/cli/` | ~60 | `go test` pass |
| 3.3 | Add `Store.Remove(group)` method | `internal/hooks/trust.go` | ~15 | Unit test |
| 3.4 | Add `/hooks untrust <number>` handler | `internal/cli/hooks_command.go` | ~25 | Unit test |
| 3.5 | Tests for `/hooks untrust` | `internal/cli/hooks_command_test.go` | ~50 | `go test` pass |
| 3.6 | (Optional) Add `/hooks prune` | `internal/cli/hooks_command.go`, `internal/hooks/trust.go` | ~30 | Unit test |
| 3.7 | Create `turn-log.sh` example Stop hook | New file + `docs/development/lifecycle-hooks.md` | ~25 | Example runs |
| 3.8 | Document Stop hook example with caveat | `docs/development/lifecycle-hooks.md` | ~20 | `make docs-check` |
| 3.9 | Document script-content trust limitation | `docs/development/lifecycle-hooks.md` | ~30 | `make docs-check` |

## Phase 4 — v2 Lifecycle Events (P3, Deferred)

| ID | Task | Scope | Est. LOC | Gate |
|----|------|-------|----------|------|
| 4.1 | Design session event payload | Design doc | 0 | Decision recorded |
| 4.2 | Wire `SessionStart` publish site to hook execution | `internal/cli/hooks_runner.go` + new wiring | ~40 | Integration test |
| 4.3 | Update parser: promote `SessionStart` from deferred | `internal/hooks/config.go` | ~10 | `go test` pass |
| 4.4 | Tests for `SessionStart` hooks | `internal/cli/hooks_command_test.go` | ~60 | `go test` pass |
| 4.5 | Design subagent event payload | Design doc | 0 | Decision recorded |
| 4.6 | Add publish points in `MultiStepHandler.Invoke` | `internal/subagents/multi_step.go` | ~30 | Integration test |
| 4.7 | Update parser: promote `SubagentStart`/`SubagentStop` from deferred | `internal/hooks/config.go` | ~10 | `go test` pass |
| 4.8 | Tests for `SubagentStart`/`SubagentStop` hooks | `internal/subagents/` | ~80 | `go test` pass |
| 4.9 | Design skill hook activation model | Design doc | 0 | Decision recorded |
| 4.10 | Define `SKILL.md` `hooks:` frontmatter schema | `.mivia/skills/*/SKILL.md` | ~20 | Schema validated |
| 4.11 | Wire skill hooks into hook runner | `internal/hooks/` + `internal/agents/skill_policy.go` | ~60 | Integration test |
| 4.12 | Tests for skill-level hooks | `internal/hooks/` + `internal/agents/` | ~80 | `go test` pass |

## Wave Planning

### Wave 1 (Phase 1 only — P0, no dependencies)
- Tasks 1.1–1.5 sequentially
- Single `delegate` for 1.1 (design decision), then `spawn_agent` for 1.2–1.5

### Wave 2 (Phase 2 — P1, after Phase 1)
- Tasks 2.1–2.7 in parallel batches:
  - Batch A: 2.1, 2.3 (example scripts, independent)
  - Batch B: 2.2, 2.5, 2.6, 2.7 (docs, independent)
  - Batch C: 2.4 (optional, after A)

### Wave 3 (Phase 3 — P2, after Phase 2)
- Tasks 3.1–3.2 in parallel (testing, independent)
- Tasks 3.3–3.6 sequentially (code changes, cumulative)
- Tasks 3.7–3.9 in parallel with 3.3–3.6 (docs, independent)

### Wave 4 (Phase 4 — P3, deferred, after Phases 1–3)
- Tasks 4.1–4.4 (SessionStart) as one wave
- Tasks 4.5–4.8 (SubagentStart/Stop) as one wave, after 4.1–4.4
- Tasks 4.9–4.12 (Skill hooks) as one wave, after 4.1–4.8
- Each wave requires a design decision before implementation

## Fast-Path Eligibility

Phase 2 (R2, R3, R4) is **documentation + example scripts only** — no production code changes. Each individual task is ≤5 lines of production Go (or zero). Eligible for Fast Path per ADLC if treated as separate commits.

Phase 3 tasks 3.7–3.9 are **docs only**. Fast Path eligible.

All other tasks require full ADLC.
