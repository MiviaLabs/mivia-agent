# 09 — Agent documentation, examples, and program closeout

**Status:** IMPLEMENTED (2026-08-02) — owned documentation, isolated
loader-backed examples, and closeout verification are complete; this plan is
archived with the shipped contract.
**Goal:** Document the shipped one-file-per-agent model and supply isolated,
loader-backed examples that describe exactly the enforced authority boundary.
**Depends on:** shipped `05`–`07`; implementation-complete `08`.
**Blast radius:** MODERATE — inaccurate examples can create unsafe privilege configuration.

## Locked scope and decisions

1. This plan edits existing owned documents only: `docs/product/agent.md`,
   `docs/product/config.md`, `docs/security/overview.md`,
   `docs/architecture/overview.md`, and `docs/architecture/skills.md`.
   `docs/architecture/concurrency.md` is out of scope: its canonical topic is
   resource caps, not definition trust. `docs/development/agent-workflow.md`
   stays untouched unless the implementation changes its repo-maintainer
   instructions.
2. Reusable examples are test-only, not live workspace definitions and not new
   Markdown docs: `internal/config/testdata/agent-examples/user-mivia.toml`,
   `.../user-agents/{researcher,engineer}.toml`, and
   `.../workspace-agents/reviewer.toml`. Tests copy them to temporary
   `~/.mivia/` and `<workspace>/.mivia/` roots. Do not add generic examples to
   this repository's live `.mivia/agents/`, which would change its selectable
   registry, and do not create an unowned `docs/**` examples directory.
3. Retain `.mivia/mivia.toml.example` as the full product configuration sample;
   update only its agent comments. The user-owned `[agents]` gate belongs in
   `~/.mivia/mivia.toml`, never as workspace authority. Fixture global config
   is deliberately separate from the shipped full sample.
4. Examples contain no `system_prompt`, credentials, PII, real paths, or
   provider/model-specific fields. They use only fake names and the current
   generic tool/skill catalogue. A bare agent-file `model`, if documented at
   all after 08 ships, is a spawned-task default—not a root model pin or
   per-agent model policy.
5. Documentation consumes, but does not redefine, plan 08's final contract:
   `mivia agents list|explain`, `/agent` selection, `/agents` listing,
   provider-independent doctor diagnostics, definition source, opaque instance
   ID, session-local model generation, and their privacy limits. Root saved
   chat is not a task resume.

## Required honesty

1. One file-backed definition has a canonical filename/name match and resolves
   to one immutable snapshot; many isolated instances may use it concurrently.
2. The schema and its presence semantics are exact: `name`, `description`,
   `inherits`, `tools`, `tools_add`, `tools_remove`, `disallowed_tools`,
   `skills`, `model`, `max_turns`, and `system_prompt`; unknown fields fail.
   `tools` is mutually exclusive with deltas, `max_turns = 0` is unlimited,
   and omission/empty semantics for tools and skills are stated precisely.
3. Workspace agent files always load with workspace provenance; user files win
   same-name shadows. The trusted user `load_workspace_config` gate controls
   only project skill handlers and workspace `[chat]`/`[subagents]` prompts.
   Path-safety, malformed, and inheritance/source-boundary failures are
   fail-closed.
4. A root tool allowlist is not a complete privilege model: root delegation
   tools remain available by design, while spawned instances lose privileged
   delegation tools and the mandatory denylist. `run_command` additionally
   needs its independent program policy.
5. A task has required `agent` and optional `skill`; the selected task-agent
   allowlist and required tools are checked at dispatch, spawn, and
   resume/retry. Nested agents cannot synthesize such task fan-out. This does
   not claim arbitrary slash/prompt activation is covered by the task allowlist.
6. List/explain/doctor/event identity never exposes a system prompt, secret,
   source path in an event/report, digest, tool payload, user/model content, or
   arbitrary metadata. The security document owns purpose, owner, retention,
   access, deletion, and audit wording for any 08 identity data.

## Phase map

| Phase | Goal | Depends on |
|---|---|---|
| [02 — loader-backed examples](02-parser-backed-examples.md) | Prove isolated user/workspace examples through parse, discovery, and resolve | shipped `05`–`07` |
| [01 — owned-document contract](01-product-and-security-docs.md) | Reconcile the exact shipped CLI, schema, trust, and privacy semantics only after fixture GREEN | `02`, 08 exit gate |
| [03 — verification and closeout](03-verification-and-closeout.md) | Check ownership, links, fixture safety, and bounded control-surface reconciliation | `01`, `02` |

## Delivery graph and rollback criterion

`08 behavior/API lock` → `fixture tests (RED)` → `fixture files (GREEN)` →
`owned-doc updates` → `docs and cross-surface audit`. Return to Step 0 if a
user-facing example needs to be placed in a runtime-discovered directory, if
the docs require an unimplemented model or identity field, or if 08's accepted
output/privacy contract differs from this plan.
