---
id: docs_are_references_only
title: docs/ are references only - no plans, dev logs, or decision records
content: docs/ is strictly present-tense reference documentation - no proposals, status lines, research logs, amendment history, or decision ledgers; verify every behavioral claim against code before writing it.
importance: medium
tags: [docs, conventions, ownership, verification]
related: [prompt_lines_require_observed_failures]
updated: 2026-09-11
---

docs/ is published reference documentation. Write how the product works now,
in the present tense. Do not add plans, proposals, status lines, research
logs, iteration framing, amendment history, or decision ledgers. Plans live
outside docs/.

Practice that held up during the 2026-09 docs cleanup (~20 docs rewritten or
deleted):

- Rewrite decision ledgers (D1-D15 items, P0-P4 work items) into
  Overview / Behavior / Known-limitations sections, and verify every claim
  against current code first. One proposal doc listed work as planned that
  the code never implemented (`failed_pr_policy`), and one "deferred to v2"
  feature was fully shipped.
- Preserve section and rule numbers when rewriting design docs: code
  comments cite them (ux-rules 6.x/11.x/12.x, wireframes-panes sections).
- Docs drift from code. Theme docs still described a palette the code had
  replaced (mivia-dark hexes changed in commit 583559ff without a doc
  update). Reconcile against the shipped artifact
  (internal/ui/theme/themes/*.json), not against an older doc revision.

Ownership mechanics (one canonical path per topic, no ADRs) are rule
40-docs-ownership and scripts/check_docs_ownership.py; the prose policy
statement lives in docs/README.md.
