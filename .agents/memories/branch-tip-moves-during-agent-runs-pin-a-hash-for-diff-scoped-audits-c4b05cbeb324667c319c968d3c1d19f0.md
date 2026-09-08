---
id: branch_tip_moves_during_agent_runs_pin_a_hash_for_diff_scoped_audits_c4b05cbeb324667c319c968d3c1d19f0
title: 'Branch tip moves during agent runs — pin a hash for diff-scoped audits'
content: 'This repo''s branch receives commits and amends from parallel sessions while agent tasks run: during a bug audit, HEAD moved from d6e3a067 to 23eb758c then was amended into 63ecfdc1, silently shifting every relative range (HEAD~N..HEAD) the dispatched auditors used.'
importance: medium
x-scope: project
x-verdict: neutral
tags: [git, audits, parallel-sessions, dispatch]
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
