# 08 — Agent CLI surface and observability

**Status:** DESIGN — follows shipped plans `05` and `07`.
**Goal:** Make named-agent definitions, runtime instances, and model generations auditable without conflating their identities.
**Depends on:** `07` and the immutable registry from `05`.
**Blast radius:** MODERATE — diagnostics and auditability of a privilege surface.

Plan 08 is large enough to split into catalog/diagnostics, identity/observability,
and closeout phases. It must not require provider credentials or construct a
dispatcher merely to inspect configuration.

## Identity contract

- `agent_definition` is the canonical name, source path, provenance, and
  immutable effective snapshot.
- `agent_instance` is one disposable execution created from that snapshot;
  many instances can share one definition concurrently.
- `model_generation` identifies a model binding or `/model` switch. A model
  switch never mutates the definition, prompt provenance, effective tools, or
  active agent identity. The file's `model` is a default; `/model` may override
  it for the current instance only, and there is no unimplemented pin field.
- The private compiled root fallback is shown separately as `root fallback`; it
  is never a selectable file-backed agent and never appears in `--agent` enums.
- Events publish agent identity and instance/run identity once at lifecycle
  boundaries; they do not duplicate the full effective tool set on every tool
  event or add a second authority field.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — agent catalog CLI](01-agent-catalog-cli.md) | List and explain selectable definitions and private fallback | `05` |
| [02 — diagnostics](02-doctor-and-config-diagnostics.md) | Report loaded/gated/not-loaded states without provider setup | `01` |
| [03 — identity and observability](03-identity-and-observability.md) | Preserve identities across REPL, TUI, events, and model switches | `01`, `02`, `07` |
| [04 — verification and closeout](04-verification-and-closeout.md) | Audit all surfaces and run repository gates | `03` |

## Required behavior

- `mivia agents list` and `mivia agents explain <name>` load the immutable
  registry directly and work without provider credentials.
- `mivia doctor` reports loaded, gated, malformed, or not-loaded agent state;
  missing credentials must not erase the useful configuration diagnosis.
- `/agents` works in both the plain REPL and the TUI slash router, or the
  limitation is explicitly removed from the command surface and docs.
- Unknown names list available selectable definitions and source provenance.
- Raw system prompts and secrets are never printed by list, explain, doctor,
  events, or reports.
- Resume/model-switch/definition-change behavior is fail-closed and retains
  the selected agent identity and effective scope.

## Verification contract

Focused tests cover catalog, diagnostics, events, `internal/agent`,
`internal/subagents`, REPL, and TUI behavior. The closeout also runs `make
verify`, `make test`, `make race`, `make invariants`, `make
validate-invariants`, `make structure-check`, `make secret-scan`, and
`make docs-check`, followed by a hostile bug-audit round and a
`mivia-report/v1` record.
