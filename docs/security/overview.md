# Security Overview

Four rules cover almost everything:

1. Your API key lives in the process environment or an env file. Keep env files out of git.
2. Tools that can run other programs are off until you turn them on.
3. Secret filtering and redaction are off until you configure them.
4. Treat a project you did not write like untrusted code.

## Secrets

mivia never stores credentials in the settings file. API keys live in the process environment or an env file (`NAME=value` lines). Nothing that looks like a secret is compiled into the binary. No credential pattern, key name, or value prefix is built in.

A secret scan runs on every commit. It checks staged and tracked files. If a real key appears in the tree, the commit is blocked.

## MCP server permissions

MCP server definitions contain only environment variable names. mivia passes only named variables to a stdio server. HTTP header values also come only from named environment variables. A project MCP server can start its configured executable or call its configured endpoint. A project definition with the same ID replaces the complete user definition. Review project MCP configuration before use.

MCP tool descriptions, schemas, errors, and results are untrusted server data. mivia bounds metadata and results before it sends them to the model. It exposes text result content only. It does not treat MCP data as local instructions.

## Allowlists: one open by default, one closed

- `run_command`, the tool that runs other programs, can already execute a curated built-in list with no configuration: common compilers/interpreters, their package managers, git, and read-only Unix utilities. It excludes shells, file-mutating programs, `find`, and networking/container/infra tools by default — see `[tools].run_allowlist` in [Configuration](../product/config.md#allowlists) for the full list and rationale. `[tools].run_allowlist` extends it; `[tools].run_allowlist_only` replaces it for a closed allowlist.
- Child processes inherit no environment until `[tools].env_allowlist` names the variables. This allowlist is configuration-only — there is no built-in list to extend or replace.

With nothing configured, nothing is filtered and nothing is redacted. The user then sees tool previews, `run_command` output, event bodies, and audit metadata intact.

`prompt` and `reasoning` are never redacted. They are the agent's own instructions and deliberation, not the user's secrets. Eliding them made audit metadata useless for reconstructing agent behavior while protecting nothing.

## Tool approval policies and YOLO mode

Tool approval is policy-gated (`[approvals] default_mode`, formerly `policy`):

- `always` (default): executes tool calls without interactive confirmation.
- `once` (formerly `write-only`): requests interactive confirmation for mutating file and command tools.
- `deny`: auto-rejects every gated tool call without interactive confirmation.

YOLO mode (`--yolo`, `--approval-policy auto`, or `[approvals] default_mode = "always"` - the shipped default) disables interactive prompts only. It does not bypass path boundaries, Git hook guards, command allowlists, secret redaction, or verifier sandboxes.

**`--approval-policy` uses a different vocabulary than `default_mode`.** The CLI flag accepts the legacy `write-only` / `auto` / `always` values, where `always` means "prompt for every call, including reads" (paranoid mode) - the opposite of the config/TUI `always` above, which means "accept every call". Use `--yolo` or `--approval-policy auto` (not `--approval-policy always`) to accept everything from the command line.

## Secret path filtering

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only source of the file-tool secret filter. Recommended values ship in `.mivia/mivia.toml.example`. With neither configured, no paths are filtered.

The filter keeps credentials out of model context by accident. It is not a security boundary. A shell invocation that builds a path at runtime reaches the file regardless.

## Redaction

Redaction is configuration-only and off by default. `[privacy].redaction_patterns` and `[privacy].redaction_key_names` are the sole source. Recommended values ship in `.mivia/mivia.toml.example`. A workspace that configures neither redacts nothing. This fails open by design. What counts as a secret is a property of a workspace. Four compiled lists guessing on the user's behalf drifted apart and were wrong in both directions.

- **One engine.** New code that needs redaction calls `internal/redact`; it does not write its own regex. A `regexp.MustCompile` containing a credential keyword outside `internal/redact` is a defect, and `TestNoCompiledRedactionPatterns` fails the build for it.
- **No runtime backstop.** Redaction is off unless the workspace configures it. The authoring rules (do not log secrets, keep error messages scrubbed, keep excerpts short) are the first line of defence, not the second. Write as though nothing downstream will clean up after you, because by default nothing will.
- **`run_command` output is model-visible.** Its body is the tool result. The policy decides what the model reads, not only what an operator reads.
- Redaction protects what a third party learns about the user. Limiting what a reader learns about mivia is a different problem. Do not solve it here.

Tool argument redaction is opt-in. Set `[privacy] redact_tool_args = true` in TOML or `MIVIA_REDACT_TOOL_ARGS=1` in the environment. When off, `run_command` shows argv and event previews keep argument bodies, still size-capped.

## Persisted history is raw at rest

`[subagents].store_backend = "sqlite"` persists orchestration state. The store includes each task's full input payload. Task inputs and results are written unredacted at rest, even when `[privacy]` patterns are configured. Treat the chosen store location as sensitive workspace data. Do not put secrets in task prompts.

Permissions, scopes, roles, and caller identity are deliberately not stored. They are never written to the run record or restored from it. A resumed run uses the permissions of whoever resumes it. Editing the store file cannot grant new permissions.

## Workspace agent files load unconditionally

`.agents/agents/*.md` files always load as agent definitions. When a definition named `mivia` exists, it is auto-selected as the root session, prompt and tool allowlist included. User files with the same name take precedence over workspace files. The workspace file remains a diagnostic shadow row. Malformed files, unsafe paths, unknown fields, and cross-origin inheritance fail closed and do not become selectable agents.

Consequence: cloning a repository and running `mivia chat` in it hands that repository authorship of your root agent's system prompt and tool scope. A hostile agent file shapes every turn of that session. Agent files must not contain credentials, provider catalogs, raw secrets, or environment-specific absolute paths.

Two mitigations are yours to apply:

- Treat an unfamiliar repository the way you would treat any untrusted code. Read `.agents/agents/` before running `mivia chat` in it.
- `mivia chat --no-tools` limits what a hostile prompt can direct. The tool surface is what turns prompt influence into filesystem or command access.

## The config file is workspace-sourced in a checkout

The session provider, base URL, API-key env name, and model catalog come from the config file. The search order is `$MIVIA_CONFIG`, `./.mivia/mivia.toml`, `~/.mivia/mivia.toml`. The first existing file wins.

A checkout that ships `.mivia/mivia.toml` makes that file authoritative for the session endpoint and key-env name. This is independent of `load_workspace_config`, which gates only workspace prompts and project skills, never `[provider]`. A hostile checkout can declare `api_key_env` for a variable you have set and point `base_url` at its own endpoint. Key lookup falls back to process environment variables, so the value is sent to that endpoint.

Treat `.mivia/mivia.toml` like the agent files. Review it before running, or point `$MIVIA_CONFIG` at a config you own to make your user file authoritative. This is a known exposure, accepted deliberately. Project agent definitions exist so a repo can orient the agent.

## Credential-routing protection

A workspace-declared `provider` or `model` selection in an agent definition is ignored unless the user opts in. The agent then inherits the session provider. This stops a definition from routing prompts, tool results, and file contents to another provider with your credentials. The definition still loads, so its prompt, tools, and skills survive. Users who accept the risk of several providers can restore the old behavior with `allow_workspace_agent_providers = true` under `[agents]` in the user-only `~/.mivia/mivia.toml`. Workspace `[agents]` cannot enable it.

## Agent skill allowlists

Agent definitions may restrict skill invocation with `skills = [...]`. Omit = all trusted skills. `[]` = none. The allowlist is enforced at the selected task-agent boundary (`dispatch_tasks`, `spawn_agent`, skill resume). It is not enforced by trusting skill Markdown. See [Skill System Architecture](../architecture/skills.md#agent-skill-binding).

## Runtime identity

Lifecycle events may carry an allowlisted identity payload. It has the selected definition name and source, an opaque disposable instance ID, and a session-local model-generation number. Its purpose is local event correlation without exposing definition paths, digests, prompts, tool sets, user or model content, or raw errors.

The identity is for local correlation only. The active CLI session owns it. The process drops it when the session or event bus closes. It is not a durable identity record and it does not resume a saved root chat. Only the separate, explicitly confirmed task resume flow re-executes interrupted work.

The provider-independent `mivia agents list`, `mivia agents explain`, and `mivia doctor` views expose source and bounded diagnostics without provider credentials. None of these views prints prompts, digests, credentials, or agent content. Runtime events omit source paths as well as tool and content payloads.

## Panel review (agent_panel workflow steps)

A workflow `agent_panel` step (for example `feature-delivery.toml`'s `review_panel`) fans a review
out to several independent `panel-reviewer` members, each bound to a distinct provider/model pair,
then synthesizes their reports with a separate `review-synthesizer` agent.

**Purpose.** Independent, differently-provider-backed reviewers reduce the chance that one
provider's blind spot silently passes a defective change; the host, not any model, computes the
final verdict from the member reports.

**Data owner.** The workflow run's operator owns the task, plan, test plan, and implementation
content sent to every panel member and to the synthesizer. Each panel member report and the
synthesis output are owned by the same run and stored in the workflow ledger like any other step
output.

**Provider transfers.** Each panel member sends the same review context (task, plan, test plan,
implementation summary, prior findings) to its own bound provider. The locked feature-delivery
bindings use DeepSeek, OpenRouter, and Z.AI; every member sees the same source content its
provider's credentials are configured for. `review-synthesizer` receives only the bounded, already
-terminal member reports (never raw source), routed to the session's admitted provider.

**Retention.** Panel member and synthesis content follow the same workflow-ledger retention as
every other step's input/output: durable for the life of the run record, subject to the same
`workflow delete` and ledger-cleanup paths as non-panel steps. No panel-specific retention exists.

**Access.** Panel and synthesis content is readable through the same `workflow_inspect` /
`workflow_status` surfaces as any other step, gated by the same run-scoped access control. No
panel-only viewer exists.

**Deletion.** `mivia workflow delete` removes a panel run's ledger record exactly like any other
run; no separate deletion path exists for panel member or synthesis content.

**Audit.** Every panel member report and the synthesis output are appended to the workflow ledger
as durable step attempts with the same lifecycle events as any other step, so a panel run's full
history is auditable the same way.

**Bearer access.** `review-synthesizer` never sees source, workspace paths, or credentials: it
declares `allow_empty_tools = true` and an empty final tool registry (locked, exact-match
enforced), so it cannot read anything beyond the bounded member-report envelope it receives as
input. `panel-reviewer` members are read-only (`read_file`, `list_dir`, `grep`, `glob`,
`find_references`; `post_message` disallowed): a member cannot write, execute commands, or contact
another agent.

**Equality-oracle risk.** The host, not a model, computes the panel's final verdict
(`ComputeHostVerdict`): any member reporting `changes_requested` or a nonempty findings list forces
the gate closed. A member or the synthesizer cannot approve a change by claiming success; the host
verdict is derived from bounded, strictly-decoded member reports, never trusted from free-form
model text.

## Personal data

mivia does not collect personal data as a general product feature. mivia never logs credentials or raw provider payloads that contain secrets.

## See also

- [Configuration](../product/config.md#tool-safety-policy)
- [Coding agent mode](../product/agent.md#safety-and-limits)
- [Product overview](../product/overview.md)
