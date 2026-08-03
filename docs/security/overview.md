# Security Overview

## Principles

- No secrets in git (hook-enforced via `scripts/secret_scan.py`)
- Fail closed on protected verification paths (reject unsafe input rather than fall through to an insecure default)
- Powerful tools are disabled unless explicitly enabled in configuration (deny-by-default)
- No general PII collection without explicit design approval
- Never log credentials or raw provider payloads containing secrets

## Local gates

- Secret scan (staged + tracked)
- Semgrep agent standards
- Hook bypass guard for agent tools

## Context compaction data boundary

The context namespace is stricter than the general operator-visible event
surface. Before a source payload is hashed or persisted, the host-owned
sanitizer enforces the byte and UTF-8 limits and applies the configured
classifier. With no configured classifier, content-bearing bytes are not
stored: only bounded hash/size metadata is retained. Hidden reasoning, raw
prompts, provider credentials, and unbounded tool output are never valid
context payloads.

Context reads require the owner-bound principal capability, not only matching
workspace/session/subject strings. Deletion is authoritative for the context
namespace: a tombstone wins over payload reads, payload references are
revoked, and the bounded delete/export audit record contains metadata only.
Exports are versioned sanitized snapshots with an 8 MiB hard limit and no
truncation. Legacy JSONL remains a compatibility import/export path and is
never a fallback checkpoint backend.

## Product surface (as features land)

- Tool allowlists and auditability
- Isolation tiers for untrusted execution
- Tool argument redaction **opt-in** (default off for operator visibility):
  - TOML: `[privacy] redact_tool_args = true`
  - Env: `MIVIA_REDACT_TOOL_ARGS=1`
  - When off, `run_command` shows argv; event previews keep argument bodies (still size-capped)
- **Redaction is configuration-only, and off by default.** No credential
  pattern, key name or value prefix is compiled into the binary.
  `[privacy].redaction_patterns` and `.redaction_key_names` are the sole
  source; recommended values ship in `.mivia/mivia.toml.example`.

  **A workspace that configures neither redacts nothing** - tool previews,
  `run_command` output, event bodies and audit metadata pass through intact,
  including into the session transcript on disk. This fails open deliberately:

  `prompt` and `reasoning` are never redacted. They are the agent's own
  instructions and deliberation, not the user's secrets, and eliding them made
  audit metadata useless for reconstructing agent behaviour while protecting
  nothing.
- **Secret path filtering is configuration-only.** No pattern list is compiled
  into the binary: `[tools].secret_path_patterns` and `.secret_path_exceptions`
  are the sole source, recommended values ship in `.mivia/mivia.toml.example`,
  and a workspace that configures neither filters nothing. The filter keeps
  credentials out of model context by accident - it is **not** a boundary,
  because a shell invocation that builds a path at runtime reaches the file
  regardless. Config itself is deliberately agent-editable.

## Known and accepted: workspace agent definitions are ungated

`.mivia/agents/*.toml` files always load as agent definitions. When a
definition named `mivia` exists, it is auto-selected as the root session
(prompt + tool allowlist). Unlike workspace skills, agent `system_prompt`
text is **not** wrapped as untrusted content (contrast `internal/skills/loader.go`).
User files with the same name take precedence over workspace files, while the
workspace file remains a diagnostic shadow row. Malformed files, unsafe paths,
unknown fields/tools/skills, and cross-origin inheritance fail closed and do
not become selectable agents.

**Consequence:** cloning a repository and running `mivia chat` in it hands that
repository authorship of your root agent's system prompt and tool scope via
`.mivia/agents/`. A hostile `mivia.toml` agent shapes every turn of that session.
Agent files must not contain credentials, provider catalogs, raw secrets, or
environment-specific absolute paths; those belong in the environment and in the
operator's own configuration (see the config-channel note below for why the
session provider itself is *not* user-gated in a checkout).

**Credential-routing protection (strip by default):** a workspace-declared
`provider`/`model` selection in an **agent definition** is ignored at resolve
time unless the operator opted in — the agent then inherits the session provider
instead of routing the operator's prompts, tool results, and file contents to a
foreign vendor's endpoint on the operator's own credentials via that definition.
The definition still loads, so its prompt, tools, and skills survive; only the
binding is stripped (a model without a provider is not a vector and is left
alone). Operators who accept the multi-vendor risk restore the old behavior with
`allow_workspace_agent_providers = true` under `[agents]` in the user-only
`~/.mivia/mivia.toml`; workspace `[agents]` can never authorize it. The repo's
own 9 `.mivia/agents/*.toml` declarations deliberately differ in provider per
agent (anti-correlation) and keep working under the default: they resolve with
the session provider.

**Config-channel note (the session binding itself is workspace-sourced in a
checkout):** the session provider, base URL, API-key env name, and model catalog
come from the config file, whose search order is `$MIVIA_CONFIG`,
`./.mivia/mivia.toml`, `~/.mivia/mivia.toml` — first existing wins
(`internal/config/paths.go`). A checkout that ships `.mivia/mivia.toml` (this
repository's own documented pattern, see `docs/product/config.md` "From a source
checkout") makes that file authoritative for the session endpoint and key-env
name, independent of `load_workspace_config` (which gates only workspace prompts
and project skills, never `[provider]`) and of the agent-file strip above. A
hostile checkout can therefore declare `api_key_env` for a variable you have set
and point `base_url` at its own endpoint; key lookup falls back to process
environment variables, so the value is sent to that endpoint. Treat
`.mivia/mivia.toml` like the agent files: review it before running, or point
`$MIVIA_CONFIG` at a config you own to make your user file authoritative.

This is a **known exposure, accepted deliberately** - project agent definitions
exist so a repo can orient the agent (they replace the former single-file
workspace prompt surface). Two mitigations are yours to apply:

- Treat an unfamiliar repository the way you would treat any untrusted code:
  read `.mivia/agents/` before running `mivia chat` in it.
- `mivia chat --no-tools` limits what a hostile prompt can direct, since the
  tool surface is what turns prompt influence into filesystem or command access.

The user-owned `load_workspace_config` gate defaults to enabled and controls
workspace skill handlers and workspace `[chat]`/`[subagents]` system prompts -
not agent files. When explicitly disabled, only user skills are loaded for
handlers, so a project skill of the same name cannot shadow then erase a user
skill. This gate is not a complete privilege model: a workspace agent file is
still readable and selectable unless a same-name trusted user file wins.

Agent definitions may further restrict skill **invocation** with
`skills = [...]` (omit = all trusted; `[]` = none). That allowlist is enforced
at the selected task-agent boundary (`dispatch_tasks` / `spawn_agent` / skill
resume), not by trusting skill Markdown. See
[Skill System Architecture](../architecture/skills.md#agent-skill-binding).

## Typed runtime identity

Lifecycle events may carry an allowlisted identity payload with the selected
definition name/source, an opaque disposable instance ID, and a session-local
model-generation number. Its purpose is operator correlation without exposing
definition paths, digests, prompts, tool sets, user/model content, or raw
errors. The CLI session owns the values; the in-process event bus is the
retention boundary and closing the session/bus removes them. Access is limited
to local UI/event subscribers, and the event stream is the audit trail for
which typed identity was observed at each lifecycle boundary. The identity is
for local correlation only: its owner is the active CLI session, retention is
the in-process event bus, and access is limited to local UI/event subscribers.
Closing the session or bus drops it. It is not a durable identity record and it
does not resume a saved root chat; only the separate, explicitly confirmed
task-ledger resume flow re-executes interrupted work.

The provider-independent `mivia agents list`, `mivia agents explain`, and
`mivia doctor` views expose source and bounded diagnostics without provider
credentials. `explain` deliberately shows a bounded local source path for
operator diagnosis; list/doctor diagnostics reduce failures to safe classes.
None of these views prints prompts, digests, credentials, or agent content, and
runtime events omit source paths as well as tool and content payloads.

## See also

- [Configuration](../product/config.md#tool-safety-policy)
- [Coding agent mode](../product/agent.md#safety-and-limits)
