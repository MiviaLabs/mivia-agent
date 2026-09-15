---
id: check_test_skips_gate_test_skips_json_entries_must_map_to_live_t_skip_lines_ff3f60302cefa91358ed863a0438e662
title: 'check_test_skips gate: test-skips.json entries must map to live t.Skip lines'
content: 'New gate scripts/check_test_skips.py holds every .mivia/policy/test-skips.json knownSkips entry to a live t.Skip at its recorded file:line, wired into Makefile agent-hook-test (part of make verify). The SDK v0.7.0 adoption left 18 stale entries; all fixed.'
importance: medium
x-scope: project
x-verdict: good
tags: [gates, test-skips, check_test_skips, verification, make-verify]
updated: 2026-09-15
---

# check_test_skips gate: test-skips.json entries must map to live t.Skip lines

## Summary
New gate scripts/check_test_skips.py holds every .mivia/policy/test-skips.json knownSkips entry to a live t.Skip at its recorded file:line, wired into Makefile agent-hook-test (part of make verify). The SDK v0.7.0 adoption left 18 stale entries; all fixed.

## What worked
["scripts/check_test_skips.py validates every knownSkips entry against a live t.Skip/t.Skipf/t.SkipNow at the exact file:line; wired into make agent-hook-test (so make verify) with contract tests in scripts/test_check_test_skips.py", "Initial run caught 18 stale entries (dead skips, drifted lines) in .mivia/policy/test-skips.json - all fixed in the same change", "Gate is one-directional by design: environmental t.Skip guards (pty, chmod, platform) need no ledger entry; the ledger tracks semantic skips only", "verify_common.fail prints to stderr and raises bare SystemExit(1) - contract tests must capture stderr, not str(exc)"]

## What did not work
- none

## Why
The ledger previously went stale silently - entries outlived their skips because only new-skips-in-diff were checked. Now any skip removal, unskip, or line drift fails make verify, forcing ledger updates in the same change.

## References
- none
