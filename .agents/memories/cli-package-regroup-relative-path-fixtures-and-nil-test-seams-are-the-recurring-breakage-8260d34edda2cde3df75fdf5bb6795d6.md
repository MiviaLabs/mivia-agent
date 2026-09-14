---
id: cli_package_regroup_relative_path_fixtures_and_nil_test_seams_are_the_recurring_breakage_8260d34edda2cde3df75fdf5bb6795d6
title: 'cli* package regroup: relative-path fixtures and nil test seams are the recurring breakage'
content: 'Regrouped internal/{cliworktree,cliagents,cliorchestrate,cliworkflow,clichat,cliautomations} into internal/cli/{worktree,agents,orchestrate,workflow,chat,automations} on release/v0.2.2 (commits e4605642..138c4be5). Two recurring pitfalls: (1) test fixtures that compute the repo root from the package dir (filepath.Abs("../.."), Join("..",".."), Dir(Dir(cwd))) break on every depth change — DC-22; (2'
importance: medium
x-scope: project
x-verdict: good
tags: [refactor, package-moves, relative-paths, test-seams, pre-commit-gates]
updated: 2026-09-14
---

# cli* package regroup: relative-path fixtures and nil test seams are the recurring breakage

## Summary
Regrouped internal/{cliworktree,cliagents,cliorchestrate,cliworkflow,clichat,cliautomations} into internal/cli/{worktree,agents,orchestrate,workflow,chat,automations} on release/v0.2.2 (commits e4605642..138c4be5). Two recurring pitfalls: (1) test fixtures that compute the repo root from the package dir (filepath.Abs("../.."), Join("..",".."), Dir(Dir(cwd))) break on every depth change — DC-22; (2

## What worked
- none

## What did not work
- none

## Why
Same class broke twice in a row (TUI move then CLI move) because the repo relies on ../-relative fixture paths in tests and nil-able global seam vars. Future package moves should sweep for these patterns first, and pre-commit gates build only the staged tree, so commits must be ordered so dependent fixes land with or after the move.

## References
- internal/automation/executor_test.go
- internal/cli/workflow/seams.go
- scripts/verify_agent_config.py
