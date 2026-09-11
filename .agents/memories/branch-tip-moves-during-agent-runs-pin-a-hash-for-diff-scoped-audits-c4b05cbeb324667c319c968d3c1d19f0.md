---
id: branch_tip_moves_during_agent_runs_pin_a_hash_for_diff_scoped_audits_c4b05cbeb324667c319c968d3c1d19f0
title: 'Branch tip moves during agent runs — pin a hash for diff-scoped audits'
content: 'This repo''s branch receives commits and amends from parallel sessions while agent tasks run: during a bug audit, HEAD moved from d6e3a067 to 23eb758c then was amended into 63ecfdc1, silently shifting every relative range (HEAD~N..HEAD) the dispatched auditors used.'
importance: medium
x-scope: project
x-verdict: neutral
tags: [git, audits, parallel-sessions, dispatch]
related: [diff_coverage_py_reads_committed_staged_blobs_not_the_working_tree_385a7d442c5857b8cfed502c9ed6a566, dispatch_tasks_must_name_an_agent_children_die_on_cumulative_token_cap_c6d9f683ce7a2dfcce668c98ce17fc19, prefer_fast_bug_audit_for_speed]
updated: 2026-09-08
---

# Branch tip moves during agent runs — pin a hash for diff-scoped audits

## Summary
This repo's branch receives commits and amends from parallel sessions while agent tasks run: during a bug audit, HEAD moved from d6e3a067 to 23eb758c then was amended into 63ecfdc1, silently shifting every relative range (HEAD~N..HEAD) the dispatched auditors used.

## What worked
Pin the audit base: run auditors on an explicit hash range or a detached-worktree copy; re-verify hash-referenced findings against the current tip before reporting.

## What did not work
Dispatching auditors with a relative range (HEAD~15..HEAD) while the tip is hot gives them a different window than the one you scoped.

## Why
Reflog showed commit+amend mid-audit; two auditors cited a commit (23eb758c) that stopped existing as an ancestor. Findings remained valid only because they were re-verified against the new tip by hand.

## References
- none
