---
id: fix_commits_require_regression_class_sweep_trailers_with_counts_01fb3e9a2a0640a170005c4ce2489761
title: 'fix commits require Regression/Class/Sweep trailers with counts'
content: 'The repo''s commit-msg hook requires fix commits to carry Regression:, Class: (DC-n from .agents/quality/defect-taxonomy.md), and Sweep: trailers; Sweep must state a literal count of sibling sites checked (an assertion without a count is rejected). Use `git commit -F <file>` — multi -m argv breaks. Read the taxonomy and run its probes for any bug audit or fix.'
importance: medium
x-scope: project
x-verdict: neutral
tags: [commit-msg, defect-taxonomy, sweep, workflow]
archived_on: 2026-09-13
merged_into: commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf
updated: 2026-09-12
---

# fix commits require Regression/Class/Sweep trailers with counts

## Summary
The repo's commit-msg hook requires fix commits to carry Regression:, Class: (DC-n from .agents/quality/defect-taxonomy.md), and Sweep: trailers; Sweep must state a literal count of sibling sites checked (an assertion without a count is rejected). Use `git commit -F <file>` — multi -m argv breaks. Read the taxonomy and run its probes for any bug audit or fix.

## What worked
- none

## What did not work
- none

## Why
Learned while committing audit fixes: three rejections (missing Class, Sweep without count). The defect taxonomy at .agents/quality/defect-taxonomy.md is the mandated probe list for audits and the Class trailer must cite it.

## References
- none

## Archive note
Archived by the `memories-housekeeping` audit on 2026-09-13 as a near-duplicate of
`commit_format_fix_commits_need_regression_class_sweep_trailers_463508bbc8c7377b8d9c5f2c5291f4bf`,
which already stated the same three-trailer rule (and had already absorbed one prior
duplicate on 2026-09-11). This file's unique findings - the `Sweep:` literal-count
requirement, the `git commit -F <file>` fix for broken multi-`-m` trailer bodies, and
the note that the defect taxonomy is a mandated probe list, not just a citation
source - were folded into that memory before this copy was moved here.
