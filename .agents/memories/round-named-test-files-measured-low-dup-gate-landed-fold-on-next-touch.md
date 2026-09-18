---
id: round_named_test_files_measured_low_dup_gate_landed_fold_on_next_touch
title: 'Round-named test files: duplication measured low; new ones gated; fold on next touch'
content: Measured tree-wide, round-named Go test files hold ZERO exact duplicate bodies (an all-file scan later found 8, all removed in dca848ca), so consolidation means renaming to behaviour-owned files, not deleting copies; a gate in scripts/check_test_quality.py rejects new round-named test files outside the shrink-only baseline .mivia/policy/round-named-tests.json, and BOTH policy baselines (test-skips.json and round-named-tests.json) are read from the committed reference only, never the working tree.
importance: medium
tags: [testing, gates, check_test_quality, test-organization, measurement]
updated: 2026-09-18
---

# Round-named test files: duplication measured low; new ones gated; fold on next touch

## Summary
An architecture review claimed heavy duplication across round-named test files
(regex `coverage|pass[0-9]|round[0-9]|audit|wave`). Measurement with a
`go/ast` throwaway script (body hash exact, plus a loose hash with every
identifier, selector tail, and literal elided) says otherwise: zero
exact-duplicate bodies tree-wide; the worst packages hold at most 4
near-duplicate bodies each, and those pairs assert different error substrings,
so none were redundant. The real defect is naming and ownership, not copy-paste.

## Per-package measurement (round-named files only)
| Package | Round files | Test funcs | Exact-dup funcs | Loose-dup funcs |
|---|---|---|---|---|
| internal/config | 6 | 25 | 0 | 4 |
| internal/workflows/controller | 4 | 33 | 0 | 4 |
| internal/coordinator | 9 | 43 | 0 | 4 |
| internal/provider | 7 | 47 | 0 | 4 |
| internal/workflows/localengine | 4 | 49 | 0 | 4 |
| internal/vcs | 3 | 44 | 0 | 4 |
| internal/agent | 7 | 54 | 0 | 2 |
| internal/cli/chat | 25 | 186 | 0 | 4 |
| all other packages | 1-13 | 1-91 | 0 | 0 |

## What landed
- Gate: `feat(quality)` commit de738fe4. `check_test_quality.py` rejects any
  `_test.go` whose stem matches the round regex unless the path is in
  `.mivia/policy/round-named-tests.json` (121 entries, may only shrink; the
  policy is read as of HEAD when it is modified in the change, so a new file
  cannot allowlist itself in the same commit). Contract tests in
  `scripts/test_check_test_quality.py`.
- Worked example: `refactor(test)` commit b598eb73 renamed all six
  internal/config round files to behaviour names (`ollama_credentials_test.go`,
  `ollama_loopback_test.go`, `ollama_loopback_edges_test.go`,
  `hooks_provider_choices_test.go`, `load_runtime_mcp_test.go`,
  `provider_resolution_test.go`) with byte-identical bodies; old Test names
  went into `allowedDeletions`.

## Refined measurement (all files, not only round-named)
`scripts/check_test_duplication.py` hashes every Test function three ways:
exact body, elided body shape, and an assertion-surface hash (>=3
assertions, argument shapes elided) that catches duplicates written
differently. The all-file scan surfaced 8 exact duplicates the round-only
pass had missed - 3 host-backend prompt-too-long retry tests duplicated by
agentloop_retry_test.go, and one ledger branch test duplicated by
storage_runs_test.go - all removed in dca848ca with the surviving twins
verified green. Surface and loose counts elsewhere are lead generators, not
proof: table-driven tests legitimately share shapes; only exact-hash groups
justify deletion without reading the code.

## Rule for later work
Fold a package's round files when that package is next touched for any other
reason: `git mv` to one file per behaviour, rename round-encoded Test names,
add the old names to `allowedDeletions`, keep every body verbatim, and verify
with package-scoped `go vet` plus `go test ./internal/<pkg>/ -count=1 -race`
and a mutation check against one guarded fix. Do not schedule a bulk rewrite;
measurement shows there is no duplicate bulk to remove.

## What did not work
- The review's premise ("measure duplication before consolidating") turned out
  right in direction and wrong in magnitude: a bulk dedup pass would have
  deleted almost nothing.

## References
- scripts/check_test_quality.py
- .mivia/policy/round-named-tests.json
- .mivia/policy/test-skips.json (allowedDeletions)
