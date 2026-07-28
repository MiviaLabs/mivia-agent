# Plan: bug-audit-all-phases (v1 — locked)
Template-Version: v1

## Goal
Run comprehensive hostile bug audit across ALL code from Phases 0-4, fix all confirmed bugs, loop until zero findings.

## Scope
- **In scope**: All production and test code in internal/{ledger,coordinator,events,subagents,cli,storage}/ created during Phases 0-4.
- **Out of scope**: Code outside those packages (provider, chat, tools, runtime, config, etc. — those are pre-existing, not part of the orchestration phases).
- **Boundary**: Audit only files listed in evidence/ledger.md.

## Files to Create
- `.ai/plan/bug-audit-all-phases/audit/round-*.md` — audit reports
- Fixes applied directly to source files in `internal/`

## Files to Modify
Any file in scope that has a confirmed bug.

## Test Strategy
| Test Name | Type | Scenario |
|-----------|------|----------|
| (existing) | all | All existing tests must still pass after each fix |

## Dependency Graph
```
Wave 1: [t1, t2, t3, t4] — Parallel hostile audit of 4 areas
Wave 2: [t5]              — Fix all confirmed bugs
Wave 3: [t6, t7, t8, t9] — Re-audit after fixes (loop until zero)
Wave 4: [t10]             — Final verify + commit
```

## Plan Scorecard
| Criterion | Score |
|-----------|-------|
| 1. All existing tests will still pass | PASS |
| 2. No new import cycles | PASS |
| 3. No breaking changes to existing public API | PASS |
| 4. New code is testable in isolation | N/A — audit only |
| 5. Config changes are backward-compatible | N/A — no config changes |
| 6. Every new public function has ≥1 named test scenario | N/A — audit only |
| 7. Integration test path identified | PASS — go test -race ./... |
| 8. No file touched by >1 wave | PASS — each audit wave is read-only |

## Rollback Criterion
If any fix breaks existing tests, revert and re-analyse. Do not push through.
