---
id: fix_commits_require_regression_class_sweep_trailers_with_counts_01fb3e9a2a0640a170005c4ce2489761
title: 'fix commits require Regression/Class/Sweep trailers with counts'
content: 'The repo''s commit-msg hook requires fix commits to carry Regression:, Class: (DC-n from .agents/quality/defect-taxonomy.md), and Sweep: trailers; Sweep must state a literal count of sibling sites checked (an assertion without a count is rejected). Use `git commit -F <file>` — multi -m argv breaks. Read the taxonomy and run its probes for any bug audit or fix.'
importance: medium
x-scope: project
x-verdict: neutral
tags: [commit-msg, defect-taxonomy, sweep, workflow]
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
