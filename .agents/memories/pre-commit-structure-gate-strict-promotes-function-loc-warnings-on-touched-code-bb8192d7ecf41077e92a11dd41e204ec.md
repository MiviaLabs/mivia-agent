---
id: pre_commit_structure_gate_strict_promotes_function_loc_warnings_on_touched_code_bb8192d7ecf41077e92a11dd41e204ec
title: 'Pre-commit structure gate strict-promotes function-LOC warnings on touched code'
content: 'The pre-commit check_go_structure gate runs in strict mode over the whole repo, not just as advisory: a staged change that pushes a FUNCTION over the soft 80-LOC cap (or a file over its hard cap) fails the commit itself. Observed on commit 35163fd9 (C7 child tree): +9 lines to handleTurnEventFrom (76->85) failed the commit; +4 lines to subagent.go (800->804) crossed the hard 800 file cap.'
importance: medium
x-scope: project
x-verdict: neutral
tags: [go-structure, pre-commit, loc-caps, gates, commits]
updated: 2026-09-11
---

# Pre-commit structure gate strict-promotes function-LOC warnings on touched code

## Summary
The pre-commit check_go_structure gate runs in strict mode over the whole repo, not just as advisory: a staged change that pushes a FUNCTION over the soft 80-LOC cap (or a file over its hard cap) fails the commit itself. Observed on commit 35163fd9 (C7 child tree): +9 lines to handleTurnEventFrom (76->85) failed the commit; +4 lines to subagent.go (800->804) crossed the hard 800 file cap.

## What worked
["TDD slices (RED test -> GREEN) per slice kept each step verifiable", "Moving the coordinator route half of subagent.go (RegisterTaskRoute/cancels/resolveTaskRoute) verbatim into subagent_taskroute.go cleared the hard cap with zero behavior change", "Extracting handleTurnEventFrom's per-kind switch into applyTurnEventSideEffects (flushCmd passed as *tea.Cmd) cleared the promoted warning"]

## What did not work
- none

## Why
The standalone gate prints those as warnings ("never strict-promote"), which reads as safe, but the commit path enforces them for code you touch. Split before committing: move a cohesive sub-area to a sibling file (file cap) or extract a helper (function cap); do not raise baselines.

## References
- none
