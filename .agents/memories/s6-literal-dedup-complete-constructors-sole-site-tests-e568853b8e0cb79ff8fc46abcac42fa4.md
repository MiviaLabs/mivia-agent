---
id: s6_literal_dedup_complete_constructors_sole_site_tests_e568853b8e0cb79ff8fc46abcac42fa4
title: 'S6 literal-dedup complete: constructors + sole-site tests'
content: 'S6 (layer-2 literal dedup) landed on release/v0.2.3: b91162cb newRefOnlyShim, 1bd796b4 newTurnShapeWrapper. Each shim struct now has one construction site, enforced by AST sole-site tests and sentinel field-coverage tests. S7 (nil-cliReg guard at sdk_shaping.go:324) and S8 (shapeEnv/spool-rotation unification) remain deferred.'
importance: medium
x-scope: project
x-verdict: good
tags: [adlc, s6, refactor, sdk-shaping, constructors]
updated: 2026-09-17
---

# S6 literal-dedup complete: constructors + sole-site tests

## Summary
S6 (layer-2 literal dedup) landed on release/v0.2.3: b91162cb newRefOnlyShim, 1bd796b4 newTurnShapeWrapper. Each shim struct now has one construction site, enforced by AST sole-site tests and sentinel field-coverage tests. S7 (nil-cliReg guard at sdk_shaping.go:324) and S8 (shapeEnv/spool-rotation unification) remain deferred.

## What worked
- none

## What did not work
- none

## Why
Closes the S6 residual identified by the S5 no-op verdict. The sole-site AST test pattern (non-test files, enclosing FuncDecl binding) is the reusable guard against future duplicate literals; the pre-commit check_go_structure gate strict-promotes new >80-line functions in changed files, so test funcs must stay under 80 lines.

## References
- internal/agent/refonly_shim.go
- internal/agent/sdk_shaping.go
- internal/agent/refonly_shim_constructor_test.go
- internal/agent/sdk_shaping_constructor_test.go
