---
id: cli_workflow_seam_gate_and_stack_driver_home_decision
title: internal/cli seam gate exists; stack driver home decision and slice plan for the seam removal
content: scripts/check_seams.py + .mivia/policy/seam-baseline.json now ratchet internal/cli nil-seam counts (workflow 47, chat 13, agents 8, orchestrate 1, cli 1, worktree 1) and chat's internal/workflows fan-out (21) down only; the stack driver stays out of internal/cli/chat by moving internal/cli/chat/stack_*.go into internal/cli/workflow itself, not into a new internal/workflows/stack.
importance: high
tags: [refactor, seams, gates, cli, import-layers, stack-driver]
related: [cli_package_regroup_relative_path_fixtures_and_nil_test_seams_are_the_recurring_breakage_8260d34edda2cde3df75fdf5bb6795d6]
updated: 2026-09-18
---

# Seam gate + stack-driver home decision (cli workflow seam removal)

## The gate

`scripts/check_seams.py` (contract tests `scripts/test_check_seams.py`,
wired into `make verify` as `seam-check`) counts package-level nil seam
vars (`var X func(...)` and `var X error`) per package under
`internal/cli/**` from non-test files, and fails on: a count above
baseline, a seam never assigned anywhere (assignments matched per-file
against that file's own import bindings, so a shadowed local cannot
clear a seam), or chat's file fan-out over `internal/workflows/*`
rising. Comments, string/rune literals, and raw strings are blanked
before parsing. Dot imports count toward fan-out but do NOT authorize
bare assignments (fail closed). Baselines at gate creation
(commit d79be280): workflow 47, chat 13, agents 8, orchestrate 1, cli 1,
worktree 1; fan-out 21. Counts may only go down; regenerate with
`--generate` in the same change that removes seams.

## Home decision (D1)

The stack driver (`internal/cli/chat/stack_*.go`, ~16 files) moves INTO
`internal/cli/workflow` itself. Rejected: `internal/workflows/stack`,
because the stack code takes `*cliworkflow.PreparedWorkflowRun` in ~30
signatures, so that package would import `internal/cli/workflow`,
putting `internal/workflows` above `internal/cli` — worse layering.
Deferred (recorded): relocating `PreparedWorkflowRun` (or extracting the
narrow interface the driver needs) into `internal/workflows`, which
would let the driver live below both cli packages. Reopen trigger: a
second non-cli consumer of the stack driver.

## Slice plan for the removal

1. Gate + baseline (done, d79be280). 2. Move stack files + their tests
into `internal/cli/workflow`; rewrite the three `errors.Is` sites
(workflow_resume.go, workflow_run.go, workflow_tool_engine_reconcile.go)
to the package-local `errStackAwaitsGrant` BEFORE deleting the
`ErrStackAwaitsGrant` seam; rewrite `stack_command_helpers.go`'s
`applyPrivacyPolicy` call to the seam; add a TestMain in workflow that
installs test defaults so moved tests never see nil seams. 3. Class-b
chat-session helpers (each destination needs a `go list -deps` cycle
check; anything reaching internal/cli/orchestrate becomes an explicit
parameter instead). 4. Leftover cli-root helpers become explicit
parameters; seams.go shrinks to the options struct or is deleted.
