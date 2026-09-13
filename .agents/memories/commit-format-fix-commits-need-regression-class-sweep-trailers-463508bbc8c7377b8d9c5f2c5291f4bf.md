---
id: commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf
title: 'Commit format: fix commits need Regression/Class/Sweep trailers'
content: '`fix` commits in this repo are rejected by the commit-msg hook unless the body carries three trailers: `Regression: <test or none+reason>`, `Class: DC-N from .agents/quality/defect-taxonomy.md`, and `Sweep: <same-class search result>`. Scopes are fixed (cli, agent, workflows, ...); internal/vcs belongs to scope `workflows`, uiadapter/TUI to `cli`. `git commit --amend` is the repair path - no rebase needed.'
importance: medium
x-scope: project
x-verdict: good
tags: [git, commits, conventions, hooks]
related: [memories_auto_stage_via_pre_commit_hook_f6a8c93f27d17dda754711d718448e27, no_direct_commits_to_default_branch, pre_commit_structure_gate_strict_promotes_function_loc_warnings_on_touched_code_bb8192d7ecf41077e92a11dd41e204ec, sweep_greps_must_not_filter]
updated: 2026-09-13
---

# Commit format: fix commits need Regression/Class/Sweep trailers

## Summary
`fix` commits in this repo are rejected by the commit-msg hook unless the body carries three trailers: `Regression: <test or none+reason>`, `Class: DC-N from .agents/quality/defect-taxonomy.md`, and `Sweep: <same-class search result>`. Scopes are fixed (cli, agent, workflows, ...); internal/vcs belongs to scope `workflows`, uiadapter/TUI to `cli`.

## What worked
- `Regression: none (<reason>)` is accepted when a regression test is genuinely impossible - name the reason in parentheses.
- `Sweep: none` is accepted when the search came up empty, but the search must have been run. `Sweep:` must also state a literal count of sibling sites checked - an assertion with no count is rejected.
- `git commit --amend` repairs a rejected trailer set in place. The commit-msg hook re-runs and the commit is replaced; no rebase and no follow-up commit needed.
- `git commit -F <file>` is the reliable way to pass a multi-trailer body; multiple `-m` argv arguments break the trailer block.
- Read `.agents/quality/defect-taxonomy.md` and run its probes for any bug audit or fix - it is the mandated probe list, not just the `Class:` citation source.

## What did not work
- Writing `Class: DC-16, fix localized to one call site`. The comma breaks it - the hook wants the exact string `DC-N from .agents/quality/defect-taxonomy.md` and nothing appended.
- Composing the trailers after the rejection instead of up front, which costs a full commit cycle.

## Why
The hook error message spells this out, but composing the trailers up front avoids a failed commit cycle; taxonomy classes DC-4/DC-8/DC-16 map to common replay/retry/producer bugs. The Sweep trailer is the gate that stops one defect class producing a chain of repeat fixes, so the search must be real and its site count reported - see `sweep_greps_must_not_filter` for what counts as a real search.

## References
- .mivia/policy/commit-message.json
- .agents/quality/defect-taxonomy.md
- AGENTS.md

## History
- Merged in `fix_scope_commits_in_mivia_agent_require_regression_class_sweep_trailers_9444d8da293e3a9435aebdb64e85cffd` on 2026-09-11 (housekeeping: same rule stated twice; that copy's `content` was also truncated mid-word). Its `Class:` exact-string and `--amend` findings are folded in above.
- Merged in `fix_commits_require_regression_class_sweep_trailers_with_counts_01fb3e9a2a0640a170005c4ce2489761` on 2026-09-13 (housekeeping: same rule stated a third time). Its `Sweep:` count requirement, the `git commit -F <file>` finding, and the defect-taxonomy probe-list note are folded in above.
