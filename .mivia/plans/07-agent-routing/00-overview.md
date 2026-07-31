# 07 — Agent routing and handler registration

**Status:** DESIGN — plan `05` shipped the agent-aware dispatcher construction seam (`agentSessionContext` / `attachSessionDispatcher` / `buildModelBinding`); this plan owns task-field `agent` binding, handler registration per definition, and resume/idempotency.
**Goal:** Route tasks to named agent definitions and safely spawn any number of instances from one definition.
**Depends on:** plans `02` and `05`.
**Blocks:** `08`.
**Blast radius:** HIGH — routing selects prompt and tool authority.

## Model

Each file-backed agent definition is registered under its canonical agent name
as a `Kind=Subagent` handler. The handler is reusable; every invocation creates
a fresh loop, dispatcher, and scoped registry. Ten concurrent tasks selecting
`researcher` are ten instances of the same immutable definition.

There is one explicit `agent` field for selecting a named definition in
`dispatch_tasks` and `spawn_agent`. There is no configuration-level `role`
field. The existing `handler` field remains only for built-in/legacy handler
compatibility and must not override an explicit `agent` selection.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — agent binding and namespace](01-agent-binding-and-namespace.md) | Register immutable definitions and select them with one explicit field | `05` |
| [02 — resume and idempotency](02-agent-resume-and-idempotency.md) | Prevent authority changes or cross-agent handle reuse across resume/retry | `01`, plan `12` |
| [03 — concurrency and closeout](03-agent-routing-verification.md) | Prove many-instance behavior, update pointers, and run gates | `01`, `02` |

## Required invariants

- An unknown agent name fails closed and lists available names.
- A skill, built-in handler, agent, and tool name collision is rejected with
  source paths before dispatcher registration.
- Published agent definitions and registries are immutable; each invocation
  receives its own derived state.
- Resume restores work, never an authority grant written into workspace state.
  The resuming caller must re-establish access to the selected agent or resume
  fails closed.
- Idempotency fingerprints include the requested agent identity and never let a
  caller receive another agent's live handle.

Plan `05` owns the collection and safe definition loading. This plan owns task
selection and handler routing; neither plan may ship its half independently.
