# 08.04 — Verification and closeout

**Goal:** Audit every inspection surface for truthful, safe, provider-independent output.
**Depends on:** [03](03-identity-and-observability.md).

## Checks

- Review CLI, config, events, agent loop, subagents, chat, REPL, and TUI paths
  for identity conflation, raw-prompt leakage, and provider coupling.
- Verify unknown, gated, malformed, renamed, and changed definitions fail closed.
- Reconcile `05`, `07`, `09`, `.mivia/INDEX.md`, and the invariant manifest.
- Run `make verify`, `make test`, `make race`, `make invariants`, `make
  validate-invariants`, `make structure-check`, `make secret-scan`, and
  `make docs-check`.
- Complete the hostile bug-audit loop and produce `mivia-report/v1`.
