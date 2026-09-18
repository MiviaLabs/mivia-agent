---
id: doc_reference_gate_and_docs_tree_reconciliation_release_v0_2_4_70c0c40300876882e2aba5b165ce0c77
title: 'doc-reference gate and docs/tree reconciliation (release/v0.2.4)'
content: 'Doc-reference gate shipped (commit b058bea9, strict wired 0cebfa7e): scripts/check_doc_refs.py --strict fails on Go comments citing missing docs paths, docless internal/ packages, and new plan-label comments; baseline shrink-only. Architecture docs reconciled to the tree on release/v0.2.4 (commits b058bea9..0cebfa7e, reviewer-approved zero findings).'
importance: medium
x-scope: project
x-verdict: good
tags: [docs, gate, check_doc_refs, package-docs, ste100]
updated: 2026-09-18
---

# doc-reference gate and docs/tree reconciliation (release/v0.2.4)

## Summary
Doc-reference gate shipped (commit b058bea9, strict wired 0cebfa7e): scripts/check_doc_refs.py --strict fails on Go comments citing missing docs paths, docless internal/ packages, and new plan-label comments; baseline shrink-only. Architecture docs reconciled to the tree on release/v0.2.4 (commits b058bea9..0cebfa7e, reviewer-approved zero findings).

## What worked
["scripts/check_doc_refs.py + test + scripts/doc_refs_baseline.txt (shrink-only) wired into make agent-hook-test with --strict", "Strict gate catches: Go comments citing nonexistent docs/ or .agents/ paths, docless internal/ packages, and plan-label regexes (§N, plan DN, Stage 0, Phase N, B.N #, round N)", "Legitimate §citations of existing owned docs (sdk-backend-field-mapping, ux-rules) are kept and baselined - the regex over-matches them; do not strip owned-doc section refs", "RunStackDrive lives in internal/cli/workflow (moved from internal/cli/chat); docs must name internal/cli/workflow for `mivia stack drive`", "Makefile agent-hook-test currently fails at check_test_skips.py on HEAD (pre-existing stale ledger entry internal/agent/audit_dump_test.go, renamed by commit 5beeb702) - unrelated to docs work"]

## What did not work
- none

## Why
Architecture review found docs describing a stale tree (missing package families, wrong stack-drive location, plan-label comments, dangling doc cites). The gate stops each class from returning.

## References
- scripts/check_doc_refs.py
- scripts/doc_refs_baseline.txt
- docs/architecture/overview.md
