---
name: docs-update
description: Update mivia product docs using docs/OWNERS.yaml ownership. No duplicate doc paths, correct MiviaLabs brand, binary name mivia. Report with mivia-report/v1.
triggers:
  - docs update
  - update documentation
  - fix docs
  - OWNERS docs
---

# Docs Update

## Read First

- `docs/OWNERS.yaml` (required ownership SoT; do not invent owners)
- `.mivia/rules/40-docs-ownership.md`
- `AGENTS.md`
- `.mivia/templates/agent-report-v1.md`
- Target docs paths named by the user

## Method

1. Resolve ownership from `docs/OWNERS.yaml` for every path you will edit. Stop if a path has no owner entry.
2. Search for existing coverage before adding a new doc; prefer edit over new file.
3. Reject duplicate topics across `docs/**`, `README.md`, and `.mivia/**` unless one is the canonical pointer.
4. Enforce naming:
   - Brand: **MiviaLabs** (one word; never the two-word spaced form)
   - Binary: **`mivia`** (product CLI is not the old agentkit binary name)
   - Allowed historical: `mivia-agentkit`, repo `github.com/MiviaLabs/mivia-agent`
5. Do not instruct hook bypass, wildcard Bash allows, or free-form skill Output headings.
6. Keep changes minimal; update indexes/links in the same change when paths move.

## Rules

- Never create a second doc that restates an existing owned page; link instead.
- Never invent owners; missing `docs/OWNERS.yaml` entry is `BLOCK`.
- No unresolved drift markers in committed docs.
- Severity never gates approval.

## Required Report

Always emit `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — docs updated under OWNERS, no duplicates, naming correct, links checked.
- `BLOCK` — missing owner, duplicate topic, wrong brand/binary name, or broken canonical link.
- `PARTIAL` — useful edit but a named owner decision or gated path remains.
- `NOT_RUN` — plan only or ownership map unavailable.

```md
ReportFormat: mivia-report/v1
Skill: docs-update
Result: PASS|BLOCK|PARTIAL|NOT_RUN
Scope: <exact files/packages>
Baseline: <branch/commit/diff>
Summary: <one sentence>

| ID | Severity | Status | File:Line | Finding | Required Fix | Required Test | Mutation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| none | none | closed | none | none | none | none | none |

| Command | Result | Notes |
| --- | --- | --- |
| none | NOT_RUN | none |

ResidualRisk: none|<short exact risk>
NextAction: none|<exact task>
```
