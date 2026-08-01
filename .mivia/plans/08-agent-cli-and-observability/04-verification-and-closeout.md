# 08.04 — Verification and closeout

**Goal:** Audit every inspection surface for truthful, safe, provider-independent output.
**Depends on:** [03](03-identity-and-observability.md).

## Checks

- Review CLI routing/formatting, config discovery, resolver traces, events,
  agent loop adapters, task handlers, chat bindings, REPL, and TUI for identity
  conflation, provider coupling, raw-prompt leakage, non-atomic root switches,
  and public digest/path/content emission.
- Verify valid user/workspace definitions, user-wins shadowing, malformed and
  unreadable files, empty collections, unknown names, missing credentials, and
  independent workspace prompt/project-skill gate states. Do not test a gated
  workspace agent collection: that contradicts `INV-AG-29`.
- Verify task resume still fails closed for missing, renamed, changed, or
  unauthorized definitions under `INV-AG-31`; verify only root-session runtime
  identity here, not nonexistent root-chat resume persistence.
- Update the canonical security documentation through its owner before shipping
  event/report data: purpose, data owner, retention, access model, deletion
  path, and audit trail for the typed identity payload. Reconcile plan `09`
  and `.mivia/INDEX.md` so they state that workspace agents always load and
  distinguish the prompt/project-skill gate.
- Run focused package tests and race tests first, then `make verify`, `make
  test`, `make race`, `make invariants`, `make secret-scan`, and `make
  docs-check`. Run `make validate-invariants` separately only if
  `.mivia/invariants.md` changed; `make verify` already includes it and the
  structure/Semgrep/hook gates.
- Complete the hostile bug-audit loop and produce `mivia-report/v1`. Add or
  extend invariant rows only for new durable safety guarantees and allocate IDs
  at landing time.
