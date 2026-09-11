---
id: diff_coverage_py_reads_committed_staged_blobs_not_the_working_tree_385a7d442c5857b8cfed502c9ed6a566
title: 'diff_coverage.py reads committed/staged blobs, not the working tree'
content: 'scripts/diff_coverage.py''s --base mode reads tip_file_text via git show at a ref; uncommitted working-tree edits (new tests, new .mivia/policy/diff-coverage.json entries) are invisible until staged or committed. A same-diff policy-exemption addition is also explicitly rejected - the excusing entry must land in an earlier commit than the code it excuses.'
importance: medium
x-scope: project
x-verdict: good
tags: [diff-coverage, testing, sqlite, go]
related: [branch_tip_moves_during_agent_runs_pin_a_hash_for_diff_scoped_audits_c4b05cbeb324667c319c968d3c1d19f0, pre_commit_structure_gate_strict_promotes_function_loc_warnings_on_touched_code_bb8192d7ecf41077e92a11dd41e204ec, workflow_rules_no_big_test_suites_absolute_local_sdk_replace_shared_tree_etiquette_c599e6279a4ba3aa4848f4cb798b67d8]
updated: 2026-09-10
---

# diff_coverage.py reads committed/staged blobs, not the working tree

## Summary
scripts/diff_coverage.py's --base mode reads tip_file_text via git show at a ref; uncommitted working-tree edits (new tests, new .mivia/policy/diff-coverage.json entries) are invisible until staged or committed. A same-diff policy-exemption addition is also explicitly rejected - the excusing entry must land in an earlier commit than the code it excuses.

## What worked
- git add -A -- paths && python3 scripts/diff_coverage.py --staged gives the real current gap list.
- For a genuinely OS-unreachable branch (live-fd Write/Sync/Close, toml.Marshal on a validated struct), match the repo's own diff-coverage.json convention: name the exact reason class, cite existing precedent entries, keep it minimal.
- Prefer making dead code reachable over excusing it: e.g. an error branch with no real validation became reachable via an EXISTING test fixture once a genuine validation was added, needing no exemption at all.
- SQLite: CREATE TABLE/INDEX share one namespace, so a colliding INDEX name is a clean deterministic way to fail CREATE TABLE IF NOT EXISTS; substituting a VIEW for a TABLE makes CREATE TABLE IF NOT EXISTS no-op and the next ALTER TABLE fail for real.</parameter>
<parameter name="importance">high

## What did not work
- Trusting make verify-fast diff-coverage output without checking what ref it diffs against, while working directly on a branch mid multi-commit chunk plan (no PR/worktree isolation).
- Adding a policy-exemption entry in the SAME git add as the code it excuses always fails the gate by design - must split into two commits.

## Why
Spent significant effort chasing a stale uncovered-lines report from make verify-fast before realizing it was diffing against an old HEAD commit, not the working tree. Fix: git add -A then python3 scripts/diff_coverage.py --staged to see real state, and split commits (coverage-fix-for-prior-chunk first, new-chunk-code second) whenever a genuine gap needs a policy entry.

## References
- scripts/diff_coverage.py
- .mivia/policy/diff-coverage.json
- docs/design/automations.md
