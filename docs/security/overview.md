# Security Overview

Four rules cover almost everything:

1. Your API key lives in your environment. It never goes into your project files and never into git.
2. Tools that can run other programs are off until you turn them on.
3. Secret filtering and redaction are off until you configure them.
4. Treat a project you did not write like untrusted code.

## Secrets

mivia never stores credentials in the settings file. API keys live in the process environment or an env file (`NAME=value` lines). Nothing that looks like a secret is compiled into the binary. No credential pattern, key name, or value prefix is built in.

A secret scan runs on every commit. It checks staged and tracked files. If a real key appears in the tree, the commit is blocked.

## MCP server authority

MCP server definitions contain only environment variable names. mivia passes only named variables to a stdio server. HTTP header values also come only from named environment variables. A project MCP server is explicit project authority. It can start its configured executable or call its configured endpoint. A project definition with the same ID replaces the complete user definition. Review project MCP configuration before use.

MCP tool descriptions, schemas, errors, and results are untrusted server data. mivia bounds metadata and results before model exposure. It exposes text result content only. It does not treat MCP data as host instructions.

## Deny by default

Powerful tools stay off until you configure them.

- `run_command`, the tool that runs other programs, executes nothing until `[tools].run_allowlist` names the programs.
- Child processes inherit no environment until `[tools].env_allowlist` names the variables.
- These allowlists are configuration-only. There is no built-in list to extend or replace.

With nothing configured, nothing is filtered and nothing is redacted. This fails open on purpose for the operator: the operator sees tool previews, `run_command` output, event bodies, and audit metadata intact.

`prompt` and `reasoning` are never redacted. They are the agent's own instructions and deliberation, not the user's secrets. Eliding them made audit metadata useless for reconstructing agent behavior while protecting nothing.

## Secret path filtering

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only source of the file-tool secret filter. Recommended values ship in `.mivia/mivia.toml.example`. With neither configured, no paths are filtered.

The filter keeps credentials out of model context by accident. It is not a security boundary. A shell invocation that builds a path at runtime reaches the file regardless.

## Redaction

Redaction is configuration-only and off by default. `[privacy].redaction_patterns` and `[privacy].redaction_key_names` are the sole source. Recommended values ship in `.mivia/mivia.toml.example`. A workspace that configures neither redacts nothing.

Tool argument redaction is opt-in. Set `[privacy] redact_tool_args = true` in TOML or `MIVIA_REDACT_TOOL_ARGS=1` in the environment. When off, `run_command` shows argv and event previews keep argument bodies, still size-capped.

## Persisted history is raw at rest

`[subagents].store_backend = "sqlite"` persists orchestration state. The store includes each task's full input payload. Task inputs and results are written unredacted at rest, even when `[privacy]` patterns are configured. Treat the chosen store location as sensitive workspace data. Do not put secrets in task prompts.

Authority is deliberately not stored. Permissions, scopes, roles, and caller identity are never written to the ledger and never restored from it. A resumed run runs under the identity and permissions of whoever resumes it. Editing the store file cannot grant privilege.

## Workspace agent files load unconditionally

`.mivia/agents/*.toml` files always load as agent definitions. When a definition named `mivia` exists, it is auto-selected as the root session, prompt and tool allowlist included. User files with the same name take precedence over workspace files. The workspace file remains a diagnostic shadow row. Malformed files, unsafe paths, unknown fields, and cross-origin inheritance fail closed and do not become selectable agents.

Consequence: cloning a repository and running `mivia chat` in it hands that repository authorship of your root agent's system prompt and tool scope. A hostile agent file shapes every turn of that session. Agent files must not contain credentials, provider catalogs, raw secrets, or environment-specific absolute paths.

Two mitigations are yours to apply:

- Treat an unfamiliar repository the way you would treat any untrusted code. Read `.mivia/agents/` before running `mivia chat` in it.
- `mivia chat --no-tools` limits what a hostile prompt can direct. The tool surface is what turns prompt influence into filesystem or command access.

## The config file is workspace-sourced in a checkout

The session provider, base URL, API-key env name, and model catalog come from the config file. The search order is `$MIVIA_CONFIG`, `./.mivia/mivia.toml`, `~/.mivia/mivia.toml`. The first existing file wins.

A checkout that ships `.mivia/mivia.toml` makes that file authoritative for the session endpoint and key-env name. This is independent of `load_workspace_config`, which gates only workspace prompts and project skills, never `[provider]`. A hostile checkout can declare `api_key_env` for a variable you have set and point `base_url` at its own endpoint. Key lookup falls back to process environment variables, so the value is sent to that endpoint.

Treat `.mivia/mivia.toml` like the agent files. Review it before running, or point `$MIVIA_CONFIG` at a config you own to make your user file authoritative. This is a known exposure, accepted deliberately. Project agent definitions exist so a repo can orient the agent.

## Credential-routing protection

A workspace-declared `provider` or `model` selection in an agent definition is ignored at resolve time unless the operator opted in. The agent then inherits the session provider. This stops a definition from routing your prompts, tool results, and file contents to a foreign vendor's endpoint on your own credentials. The definition still loads, so its prompt, tools, and skills survive. Only the binding is stripped. Operators who accept the multi-vendor risk restore the old behavior with `allow_workspace_agent_providers = true` under `[agents]` in the user-only `~/.mivia/mivia.toml`. Workspace `[agents]` can never authorize it.

## Agent skill allowlists

Agent definitions may restrict skill invocation with `skills = [...]`. Omit = all trusted skills. `[]` = none. The allowlist is enforced at the selected task-agent boundary (`dispatch_tasks`, `spawn_agent`, skill resume). It is not enforced by trusting skill Markdown. See [Skill System Architecture](../architecture/skills.md#agent-skill-binding).

## Typed runtime identity

Lifecycle events may carry an allowlisted identity payload. It has the selected definition name and source, an opaque disposable instance ID, and a session-local model-generation number. Its purpose is operator correlation without exposing definition paths, digests, prompts, tool sets, user or model content, or raw errors.

The identity is for local correlation only. Its owner is the active CLI session. Retention is the in-process event bus. Closing the session or bus drops it. It is not a durable identity record and it does not resume a saved root chat. Only the separate, explicitly confirmed task-ledger resume flow re-executes interrupted work.

The provider-independent `mivia agents list`, `mivia agents explain`, and `mivia doctor` views expose source and bounded diagnostics without provider credentials. None of these views prints prompts, digests, credentials, or agent content. Runtime events omit source paths as well as tool and content payloads.

## No PII

mivia does not collect personal data. There is no general PII collection without explicit design approval. mivia never logs credentials or raw provider payloads containing secrets.

## See also

- [Configuration](../product/config.md#tool-safety-policy)
- [Coding agent mode](../product/agent.md#safety-and-limits)
- [Product overview](../product/overview.md)
