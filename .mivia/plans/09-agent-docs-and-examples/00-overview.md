# 09 — Agent documentation, examples, and program closeout

**Status:** DESIGN — follows plans `05`, `07`, and `08`.
**Goal:** Document the one-file-per-agent model and ship examples that parse, validate, and enforce exactly as described.
**Depends on:** `02`, `08`.
**Blast radius:** MODERATE — inaccurate examples can create unsafe privilege configuration.

This is a multi-surface documentation task, so it is split into product/security
docs, parser-backed examples, and final verification.

## Required honesty

1. One agent TOML file is one immutable definition; many disposable instances
   may run from it concurrently.
2. `skills` is enforced at the root fan-out boundary in v1 unless plan `06`
   proves a broader reachable boundary.
3. Tool-name inclusion is not privilege ordering; `run_command` can exceed the
   apparent power of several file tools once configured.
4. There is no per-agent provider credential or command-argument scope in v1.
5. Renaming/changing a definition can invalidate in-flight resume or
   idempotency; resume fails closed rather than restoring authority from
   workspace state.
6. Workspace agent files are gated by trusted user config, and
   `.mivia/agent-prompt.md` is a separate repo-owned surface.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — product and security docs](01-product-and-security-docs.md) | Explain definitions, trust, authority, lifecycle, and observability | `05`, `07`, `08` |
| [02 — parser-backed examples](02-parser-backed-examples.md) | Ship separate global config and agent TOML examples that pass real validation | `01`, `05` |
| [03 — verification and closeout](03-verification-and-closeout.md) | Check ownership, links, safety claims, and all repository gates | `02`, `08` |
