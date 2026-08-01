# 09.01 - Owned-document contract

**Goal:** Reconcile existing canonical documents with the shipped agent model
and plan 08's accepted public surface.
**Depends on:** plan `08` exit gate; use the `docs-update` workflow and
`docs/OWNERS.yaml` before each document edit.

## Exact document matrix

| Canonical file | Required content | Must not claim |
|---|---|---|
| `docs/product/agent.md` | `mivia chat --agent`, `/agent`, and accepted 08 list/explain/doctor surfaces; root versus spawned tool scope; task `agent`/`skill` boundary; current `max_steps` default; a tool table reconciled against `tools.AllToolNames()` | a root model pin, a selectable compiled fallback, a root tool list as complete authority, or skill policy on arbitrary direct prompt/slash activation |
| `docs/product/config.md` | exact agent-file schema, filename/name match, inheritance/deltas, nil/empty semantics, source precedence, failure behavior, and copy destinations; user-only `[agents]` gate and its limited effect | inline agent definitions in `mivia.toml`, workspace authority over the gate, or a separate configuration manual |
| `docs/architecture/overview.md` | `internal/config` → `internal/agents` → CLI/runtime ownership; immutable definition snapshots; fresh invocation state; plan 08 definition/instance/model-generation ownership and non-persisted root-chat scope | a digest/path/prompt as public identity or a new architecture boundary that does not exist |
| `docs/architecture/skills.md` | distinguish active handler source precedence when the gate is on from agent-allowlist trust resolution; exact default gate setting and dispatch/spawn/resume enforcement | that the gate controls workspace agent files or that project skills silently erase a user allowlist binding |
| `docs/security/overview.md` | preserve the accepted always-loaded workspace-agent exposure; user-shadow and gate limits; no credentials in agent files; 08 identity-data purpose, owner, retention, access, deletion, and audit record | that workspace agents are gated/safe by provenance, or that default redaction hides newly documented data |

Cross-link to the canonical owner instead of repeating the same schema or
security policy. Update `.mivia/mivia.toml.example` comments only where they
point to these facts; it is configuration guidance, not a second agent schema.

## Content acceptance review

- Review every active occurrence of `role`, `[[agents.roles]]`, gated workspace
  agent discovery, root-chat resume, compiled selectable fallback, and
  per-agent root model policy in the five owned documents, `.mivia/INDEX.md`,
  and `.mivia/plans/00-agent-program-overview.md`.
- Require every command, option, schema field, example copy destination, and
  output/privacy claim to be supported by a focused test or a direct source
  inspection recorded in the delivery report. `make docs-check` validates
  ownership/duplicate titles, not factual Markdown claims.
- Keep raw prompts out of prose and fixtures. Describe prompt provenance and
  redaction limits without reproducing prompt text.
