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
- `.mivia/policy/docs-ownership.json`
- `AGENTS.md`
- `.mivia/templates/agent-report-v1.md`
- Target docs paths named by the user

## Method

1. Search for existing coverage before adding a new doc; prefer editing the existing canonical path.
2. For an existing topic, resolve its owner and canonical path from `docs/OWNERS.yaml`; stop if the registry is incomplete or contradictory.
3. For a genuinely new topic, first add exactly one `docs/OWNERS.yaml` entry, then create only its registered canonical path. Do not invent an owner or create an unregistered document.
4. Reject duplicate topics across `docs/**`, `README.md`, and `.mivia/**` unless one is the canonical pointer.
5. Enforce naming:
   - Brand: **MiviaLabs** (one word; never the two-word spaced form)
   - Binary: **`mivia`** (product CLI is not the old agentkit binary name)
   - Allowed historical: `mivia-agentkit`, repo `github.com/MiviaLabs/mivia-agent`
6. Do not instruct hook bypass, wildcard Bash allows, or free-form skill Output headings.
7. Keep changes minimal; update indexes/links in the same change when paths move.
8. Run `make docs-check`. When changed documentation includes a runnable command, flag, config example, or expected output, validate it with the narrowest safe evidence (for example, `mivia --help`, the named Make target, or a focused test). For touched links, run an available lightweight checker or inspect local targets; report any check that cannot run.

## Rules

- Never create a second doc that restates an existing owned page; link instead.
- Never invent owners. A missing entry for an existing topic is `BLOCK`; a new topic must be registered before its document is created.
- No unresolved drift markers in committed docs.
- Severity never gates approval.

## Required Report

Always emit the compact `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`. Do not claim links or examples were verified unless the relevant check ran.

Result semantics:

- `PASS` — docs updated under OWNERS, no duplicates, naming correct, and all required validation passed.
- `BLOCK` — missing owner, duplicate topic, wrong brand/binary name, or broken canonical link.
- `PARTIAL` — useful edit but a named owner decision or gated path remains.
- `NOT_RUN` — plan only or ownership map unavailable.
