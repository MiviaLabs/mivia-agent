---
id: commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf
title: 'Commit format: fix commits need Regression/Class/Sweep trailers'
content: '`fix` commits in this repo are rejected by the commit-msg hook unless the body carries three trailers: `Regression: <test or none+reason>`, `Class: DC-N from .agents/quality/defect-taxonomy.md`, and `Sweep: <same-class search result>`. Scopes are fixed (cli, agent, workflows, ...); internal/vcs belongs to scope `workflows`, uiadapter/TUI to `cli`.'
importance: medium
x-scope: project
x-verdict: good
tags: [git, commits, conventions, hooks]
updated: 2026-09-08
---

# Commit format: fix commits need Regression/Class/Sweep trailers

## Summary
`fix` commits in this repo are rejected by the commit-msg hook unless the body carries three trailers: `Regression: <test or none+reason>`, `Class: DC-N from .agents/quality/defect-taxonomy.md`, and `Sweep: <same-class search result>`. Scopes are fixed (cli, agent, workflows, ...); internal/vcs belongs to scope `workflows`, uiadapter/TUI to `cli`.

## What worked
- none

## What did not work
- none

## Why
The hook error message spells this out, but composing the trailers up front avoids a failed commit cycle; taxonomy classes DC-4/DC-8/DC-16 map to common replay/retry/producer bugs.

## References
- none
