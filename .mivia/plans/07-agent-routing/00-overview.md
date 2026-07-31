# 07 — Agent routing and invocation registration

**Status:** IMPLEMENTED (2026-08-01) — explicit task agent routing,
per-definition invocation handlers, snapshot-bound resume, and idempotency.
**Goal:** Route every task through exactly one authorized named agent definition
and preserve that identity across concurrent execution, retry, and resume.
**Depends on:** plans `02` and `05`; coordinates with the shipped skill policy
from plan `06`.
**Blocks:** `08`.
**Blast radius:** HIGH — routing selects prompt, tools, and skill authority.

## Task contract

Each file-backed agent definition is registered once under its canonical name as
an internal `Kind=Subagent` handler. The registration is reusable; every
invocation creates a fresh loop, dispatcher, and scoped registry from the
immutable definition snapshot. Ten concurrent tasks selecting `researcher` are
ten isolated instances of that definition.

Every task in `dispatch_tasks` and `spawn_agent` must contain one required
`agent` field. `agent` is the only model-facing selector for an agent definition.
If the task invokes a skill, it carries a separate explicit `skill` field; the
selected agent remains the authority owner and the skill is checked against that
agent's immutable policy.

The model-facing task schema and decoder reject `handler`, `name`, `role`,
`multi_step`, and all other unknown selector fields. They do not translate,
default, or infer an agent from those fields. A missing, unknown, renamed, or
unauthorized agent fails closed before task creation and reports available
sanitized names where appropriate. Built-in runner names and runtime handler
objects remain private implementation details.

The private generic root-session startup context, if used by plan `05`, is not a
task target and cannot satisfy the required `agent` field. No task may gain
authority merely because the caller omitted its agent selection.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [01 — agent binding and namespace](01-agent-binding-and-namespace.md) | Register immutable definitions and enforce the strict `agent`/`skill` task contract | `05`, shipped `06` policy |
| [02 — resume and idempotency](02-agent-resume-and-idempotency.md) | Re-authorize the canonical target on retry/resume and bind fingerprints to its snapshot | `01`, plan `12` |
| [03 — routing verification](03-agent-routing-verification.md) | Prove isolation, fail-closed selection, lifecycle behavior, and closeout | `01`, `02` |

## Required invariants

- Every task has exactly one canonical `agent`; missing and unknown values fail
  closed before a handler or ledger task is created.
- `handler`, `name`, `role`, and built-in runner names cannot select an agent,
  bypass the selected agent, or trigger an alternate execution path.
- A selected agent is resolved from the caller's authorized registry. Task
  scope is bounded by the caller's dispatch authority; selecting a name is not
  itself an authority grant.
- A skill is an explicit secondary target. It cannot replace, widen, or
  override the selected agent's immutable tool and skill policy.
- Published definitions and registries are immutable; each invocation receives
  its own derived state and cannot mutate another instance's prompt, tools, or
  model binding.
- Resume restores work metadata, never authority. The resuming caller must
  re-establish access to the canonical agent and any skill, and the effective
  definition digest must still match.
- Idempotency fingerprints include canonical agent identity, effective
  definition digest, and explicit skill identity when present. A caller can
  never receive another agent's live handle.

Plan `05` owns collection, trust, resolution, and root handler construction.
Plan `06` owns skill metadata and policy. This plan owns task selection and
invocation routing; the three seams must land as one enforced boundary.
