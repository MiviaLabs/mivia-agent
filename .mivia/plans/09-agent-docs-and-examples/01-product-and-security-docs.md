# 09.01 — Product and security documentation

**Goal:** Update owned docs to describe named agent definitions, trusted sources, and many-instance execution without role terminology.
**Depends on:** plans `05`, `07`, and `08`.

## Files

- `docs/product/agent.md` — one TOML file per named agent and many runtime instances;
- `docs/product/config.md` — `~/.mivia/mivia.toml` global settings and
  `~/.mivia/agents/<name>.toml` schema;
- `docs/security/overview.md` — tool-name sets are not privilege ordering,
  workspace trust is not self-authorizing, and `run_command` remains total
  privilege when configured;
- `docs/architecture/overview.md` and, if needed,
  `docs/architecture/concurrency.md` — `internal/agents` boundary, immutable
  snapshots, fresh per-instance state, and model-switch behavior.

## Guard

Update only files owned by the relevant entries in `docs/OWNERS.yaml`. Do not
create parallel policy docs or describe raw prompts, secrets, or unimplemented
fields.
