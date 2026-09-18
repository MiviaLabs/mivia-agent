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

## Slice outcome (executed 2026-09-18, branch cli/workflow-seam-removal)

1. Gate + baseline: d79be280. 2. Stack driver + session_delivery_repair
moved into `internal/cli/workflow`; the 24 stack seams became initialized
overrides over the moved impls (ErrStackAwaitsGrant = errStackAwaitsGrant,
so the three `errors.Is` sites kept working); 18492118. 3. Nine
self-contained helpers moved (ContextStorePath family, ApplyPrivacyPolicy,
LogMCPWarnings, MessagingDisallowed, InjectSkillResourceTool, SliceErrors,
FlagValue/FlagVar); b7775a7c. Final baselines: workflow 11, chat 9,
fan-out 2. Remaining five seams are deliberate (AR-3): their impls reach
internal/cli/orchestrate or chat internals - InstallHookSessionFunc,
LoadChatSkillsFunc, NewSessionDispatcherFunc, InitCoordinatorFunc,
InjectBaselineMessagingFunc. Lesson that cost a debug round: workflow's
TestMain kept overriding the moved seams with simplified locals, which
broke the moved integration tests (the Local repair loop read
WorkflowAutoDeliveryAttemptTimeout, unstubbed); the fix was deleting the
overrides so the test binary runs the production impls.
