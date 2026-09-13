---
id: fix_scope_commits_in_mivia_agent_require_regression_class_sweep_trailers_9444d8da293e3a9435aebdb64e85cffd
title: 'fix(scope) commits in mivia-agent require Regression/Class/Sweep trailers'
content: 'In mivia-agent, `fix(...)` commits are rejected by the commit-msg hook unless the body carries three trailers: `Regression:` (a test that fails before and passes after, or `none (<reason>)`), `Class:` (a DC-n id from .agents/quality/defect-taxonomy.md), and `Sweep:` (what you searched for other sites of the same class and how many you found). Scope is required and drawn from a fixed list (cli, age'
importance: medium
x-scope: project
x-verdict: good
tags: [project]
archived_on: 2026-09-11
merged_into: commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf
updated: 2026-09-11
---

# fix(scope) commits in mivia-agent require Regression/Class/Sweep trailers

## Summary
In mivia-agent, `fix(...)` commits are rejected by the commit-msg hook unless the body carries three trailers: `Regression:` (a test that fails before and passes after, or `none (<reason>)`), `Class:` (a DC-n id from .agents/quality/defect-taxonomy.md), and `Sweep:` (what you searched for other sites of the same class and how many you found). Scope is required and drawn from a fixed list (cli, age

## What worked
- none

## What did not work
- none

## Why
The Sweep trailer is the gate that stops one defect class producing a chain of repeat fixes, so the search must be real and its site count reported. Discovered by a commit being rejected, then passing once the trailers were added.

## References
- .mivia/policy/commit-message.json
- .agents/quality/defect-taxonomy.md
- AGENTS.md

## Archive note
Archived by the `memories-housekeeping` audit on 2026-09-11 as a near-duplicate of
`commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf`,
which states the same three-trailer rule and additionally carries the scope mapping
(`internal/vcs` -> `workflows`, `uiadapter`/TUI -> `cli`) that this file omitted.
This file was created by a `memory_save` call that failed to detect the existing
entry. Its unique findings - the `Class:` exact-string requirement (a comma fails the
hook) and `git commit --amend` as the repair path - were folded into that memory
before this copy was moved here. The `content` field above is preserved verbatim
including its truncation mid-word at "a fixed list (cli, age", which is recorded as
the reason a merge was preferred to a keep-both.
