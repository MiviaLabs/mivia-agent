# Coding Agent Mode

`mivia chat` opens a session with tool access: read, search, and edit files;
run allowed commands; search the web. `mivia chat -p "question"` runs one
turn and exits. `--no-tools` disables file and command access.

## Chat modes

```bash
mivia chat                    # tools on, interactive
mivia chat --no-tools         # pure chat, no tools
mivia chat -p "fix the test"  # one-shot task
mivia chat --workspace /path/to/repo
mivia chat --agent reviewer -p "review the last commit"
```

`--plain` uses the classic terminal UI. Use it when the modern UI misbehaves.

`--quiet` suppresses the informational startup notices (the limits summary,
the lifecycle-hooks armed notice, the diagnostics-commands line, the session
banner, and the workflow session-recovery notices). Genuine warnings - a hook
that fails to load, a config that signals a misconfiguration - still print.
`--quiet` does not disable tools or change behavior.

Ctrl-C at the prompt exits. Ctrl-C during a reply stops the reply.

## File tools

mivia can read, search, and edit files with these tools:

| Tool | What it does |
|------|-------------|
| `read_file` | Read a file with optional line range |
| `list_dir` | List a directory |
| `grep` | Search file contents by pattern |
| `glob` | Find files by name pattern |
| `find_references` | Find where a symbol is used |
| `list_symbols` | List the declarations in a file |
| `go_to_definition` | Jump to where a symbol is declared |
| `write_file` | Create or overwrite a file |
| `search_replace` | Replace exact text in a file |
| `multi_edit` | Apply several exact-text edits to one file, all or nothing |
| `read_skill_resource` | Read one declared text resource for the active skill |

`find_references`, `list_symbols`, and `go_to_definition` share one workspace analysis of function, variable, and type declarations. mivia loads it on the first call of a session. Later calls are fast. mivia checks the analysis against the files on every call. It reloads when anything differs. A query never reports a position from a file as it used to be.

`list_symbols` with a `path` reads and parses that one file. It needs no workspace analysis. It works while the analysis is cold and in projects that do not compile.

## run_command

| Tool | What it does |
|------|-------------|
| `run_command` | Run an allowed program in the workspace |

`run_command` runs one program with a fixed argv list. There is no shell: no `;`, `&&`, or `$(...)` expansion.

`run_command` runs a curated built-in program allowlist out of the box: common compilers/interpreters, their package managers, git, and read-only Unix utilities. Configuration or a CLI override can extend or replace that allowlist. The recommended broader configuration includes shells and network clients — trim it to the least permission your workspace needs. Child-process environment variables are controlled by a separate, empty-by-default allowlist. See [Configuration](config.md) for the persistent policy.

The `get_diagnostics` tool runs a workspace-declared diagnostics command and returns a normalized JSON envelope of findings, each with `file`, `line`, `severity`, and `message` fields. It is configured through `[tools] diagnostics_commands`, a map of command names to argv: for example `vet = ["go", "vet", "./..."]`, `lint = ["npm", "run", "lint"]`, or `check = ["pytest", "--output", "json"]`. The agent selects one command with the `command` argument. When the argument is omitted, the tool runs the entry named `default`, or the sole entry when only one exists. With several commands and no `default`, an omitted `command` is refused with an explanatory envelope error; an unknown command name is refused the same way. The envelope names the command that ran (`command_name`) and the exact argv (`command`). The v1 key `[tools] diagnostics_command` still loads: it is a deprecated alias that folds into the `default` entry. Setting both keys is a configuration error.

The tool is registered only when at least one command is configured and its argv[0] is on the effective run allowlist; an unset or empty configuration leaves the tool unregistered. The tool's entire captured output (stdout and stderr) is redacted before parsing per the workspace privacy policy, so credentials hidden in raw output can never reach a result row. The result envelope is bounded by a 256 KiB default budget that feeds the derived per-tool output ceiling without raising the global cap; an over-budget result is refused with a bounded error envelope naming the bound — never tail-cut, never invalid JSON.

## Web research tools

| Tool | What it does |
|------|-------------|
| `search` | Search the web; uses Tavily when configured, otherwise tries free search engines |
| `fetch_url` | Fetch and read a public web page; private and internal addresses are blocked |
| `extract` | Extract structured page content with Tavily; requires `TAVILY_API_KEY` |

The built-in tool catalog is `read_file`, `list_dir`, `grep`, `glob`, `write_file`, `search_replace`, `multi_edit`, `run_command`, `get_diagnostics`, `search`, `fetch_url`, `extract`, `find_references`, `list_symbols`, `go_to_definition`, and `read_skill_resource`. When memory is enabled, `memory_save` and `memory_search` are also available. Configured MCP servers add scoped remote tools after discovery. Workflow tools are a separate surface; see the [Workflow guide](workflows-guide.md).

Session tools and run-record tools are separate surfaces. They are not valid agent-file allowlist names.

`search` and `extract` never truncate what they fetch. Their output is bounded by `[tools] max_tavily_response_bytes` (default 4 MiB). A result over the bound is refused with an explicit error. It is never cut short and never quietly replaced by fallback results. See [Configuration](config.md).

Tool names, descriptions, and schemas are project- and language-generic. mivia works as a coding agent in any workspace.

## Deferred tool loading

Every advertised tool costs schema bytes on every request, whether the model uses it or not. `[tools] core` (or per-agent `tools_core`) names the tools that stay advertised. The rest of the agent's authorized set is deferred. A deferred tool's advertised description is shortened to a one-line summary; its parameter schema still ships in full, since that is what the model needs to invoke it correctly once loaded. The same one-liner also appears in a one-line index injected into the prompt. The full description is sent once, when the tool is actually admitted, as the result of the `load_tools` call that loads it.

- Unset is the default and is fully inert. Every authorized tool is core. No `load_tools` tool is registered.
- Loading takes effect on the model's next turn. The current turn's tool list was already sent to the provider.
- Loading never widens permission. The core list and every `load_tools` request are intersected with the agent's effective tool set.
- Loaded tools persist for the rest of the agent binding and across save and load of the session. An `/agent` switch resets the surface to the new agent's core tier.
- `/tools` reports the advertised schema mass and how much the deferred tier is withholding.

## Named agents and skill binding

Named agents are file-backed definitions. They live in two places:

- user definitions: `~/.agents/agents/<name>.md`
- workspace definitions: `<workspace>/.agents/agents/<name>.md`

Select an agent with `mivia chat --agent <name>` or `/agent <name>`. If a file-backed `mivia` definition exists, it is selected as the root session when no agent is specified. Otherwise mivia uses a built-in default agent. The built-in default is not a file-backed definition and cannot be selected with `--agent`.

Each filename is `<name>.toml`. The in-file `name` must match the lowercase filename. The parser rejects unknown keys and malformed or unsafe names. Definitions may inherit only from another definition of the same source, user or workspace. Cross-source inheritance is not allowed. The authored fields are:

| Field | Role |
|-------|------|
| `description` | Bounded display description |
| `inherits` | Same-origin parent definition |
| `tools` | Full tool allowlist; mutually exclusive with `tools_add`/`tools_remove` |
| `tools_add`, `tools_remove` | Ordered deltas applied to inherited tools |
| `disallowed_tools` | Additional denylist applied before the final allowlist |
| `tools_core` | Always-advertised tool tier; the rest of `tools` is deferred behind `load_tools`. Omitted = inherit `[tools] core` |
| `skills` | Which skill handlers this agent may invoke |
| `mcp_servers` | Exact MCP server IDs; omit for the default server scope, `[]` for none |
| `model` | Spawned-task model identifier, validated against the active provider catalog; it does not change root model selection |
| `max_turns` | Omitted = session default; `0` = unlimited; positive = cap |
| `system_prompt` | Optional user-owned prompt; workspace prompt text is gate-controlled |

When `tools` is omitted, a root definition receives the complete known workspace-tool catalog unless the trusted `require_explicit_tools` safety setting is enabled. `tools = []` is an explicit empty set. `skills` keeps the same distinction: omitted means all trusted skills, while `skills = []` means none. An empty effective toolset is refused by the default `fail_on_empty_toolset` safety setting.

`mcp_servers` is a separate server scope. It does not list dynamic MCP tool names. A root agent that omits the field receives enabled servers with `global = true`. A user-owned root can name a non-global server. A workspace root can name only global servers. A child that omits the field inherits its parent list; it can only keep or narrow that list. Set `mcp_servers = []` to deny all MCP tools.

```toml
# Specialist: only repository engineering skills
skills = ["bug-audit", "verify-change", "architecture-review"]
```

- Omit `skills` to allow all trusted skills.
- Set `skills = []` to allow none.
- Skill names are validated against the loaded skill catalog.
- Workspace agent files always load. The user-owned `load_workspace_config` gate defaults to enabled. It controls only workspace prompt and project-skill surfaces. Set it to `false` to exclude project skills and workspace `[chat]`/`[subagents]` prompts from runtime activation.

Every `dispatch_tasks` task may select a named `agent` and an optional separate `skill`. Omitting `agent` runs the task as a bare one-shot LLM call on the caller's own model, with no tools; setting `skill` without `agent` is rejected. mivia rejects the call if a selected agent's tool list does not allow the skill. Nested agents cannot dispatch tasks; extra tools are removed. See [Skill System Architecture](../architecture/skills.md#agent-skill-binding).

The task agent setting is separate from direct user-invoked skill slash handlers and prompt turns.

## Skills

A skill is a reusable task template. It is a `SKILL.md` file with optional YAML frontmatter. Skills live in `~/.mivia/skills/` (user) or `.agents/skills/` (workspace).

Pre-built skills include:

| Skill | Purpose |
|-------|---------|
| `bug-audit` | Find confirmed bugs, no false positives |
| `verify-code-change` | Check a change after edits |
| `verify-change` | Mechanical gates and reports for scoped changes |
| `docs-update` | OWNERS-safe documentation edits |
| `secure-change` | Secrets, auth, network, and tool isolation review |
| `architecture-review` | Boundaries, dependencies, and abstraction cost |
| `concurrency-review` | Subagent caps, race conditions, cancel safety |
| `feature-delivery` | Bounded feature slice with verification |

## Subagent orchestration

mivia can run several sub-agents at the same time. A sub-agent is a helper agent that works on part of a task. The model can spawn them, inspect their progress, block on results, or cancel them.

For the workflow agent tools (`workflow_run`, `workflow_status`, `workflow_events`, `workflow_inspect`, `workflow_list_runs`, `workflow_deliver`, `workflow_cancel`, `workflow_delete`), see the [Workflow Guide](workflows-guide.md).

```mermaid
flowchart LR
    dispatch_tasks -->|"tasks with dependencies"| run_handle["run handle"]
    inspect_agents --> run_snapshot["run snapshot"]
    join_run --> block_until["block until done"]
    block_until --> results["results"]
    cancel_run --> two_phase["two-phase cancel"]
```

Look at the arrows out of `dispatch_tasks`. One run can hold many tasks. `join_run` waits until all tasks finish.

| Tool | Purpose |
|------|---------|
| `dispatch_tasks` | Dispatch sub-tasks with optional dependencies. Supports `wait` (`run`/`none`/`task`, default `run`), `wait_task_id`, and per-task `timeout_seconds`/`output_schema`. With `wait=run` it blocks for the whole batch and returns one result per task |
| `inspect_agents` | Returns a snapshot of a run: status, per-task states, and a live progress block for running tasks (tool-call counter, task age, last tool call and its age) |
| `join_run` | Block until a run completes; returns per-task results |
| `cancel_run` | Cancel a running orchestration run, in two phases |

The root agent's workspace-tool allowlist is not the complete permission model. Root coordination and run-record tools remain available by design. Spawned instances lose delegation tools. Coordination tools are removed from nested agents. `run_command` has a separate program and environment allowlist. Naming it in an agent file does not authorize arbitrary process execution.

#### How tasks run

Tasks can declare `depends_on` for dependency ordering. The scheduler:

1. Runs all tasks with no dependencies at the same time.
2. Schedules a task only after all its dependencies complete.
3. Marks tasks whose dependencies fail as `blocked`.
4. Ends the run when a task fails or times out.

#### Idempotency

`dispatch_tasks` is idempotent automatically: the harness derives its own idempotency key from the tool call's identity (not a caller-supplied value), so a provider-level retry of the same call reuses the in-flight or completed run instead of dispatching duplicate work. There is no `idempotency_key` parameter to pass.

#### Results are always complete

Orchestration returns one result per task. Each result has its own status: `completed`, `failed`, `timed_out`, `canceled`, or `blocked`. One task failing or hanging never costs you the others.

`join_run` and `dispatch_tasks` called with `wait` set to `none` or `task` return the `run_id`/`task_results` envelope, which carries a `run_error` field for a problem with the run itself. `dispatch_tasks` with the default `wait=run` returns the bare per-task array only, with no `run_error` field.

If the call's context expires before the run resolves, the results are read back from the recorded execution history. The run is not cancelled. It keeps going and stays reachable through `inspect_agents` and `join_run` on its `run_id`.

## Content references (the run record)

mivia records task results in the durable run record. Agents see two kinds of content reference:

1. Task results carry `output_ref` or `error_ref` (`ref:<kind>:<digest>`) for bytes recorded in the execution history.
2. Truncated tool results may append a notice with `remainder: ref:output:<digest>` when the harness shortened a tool body and stored the full remainder.

Read-only tools resolve those references. They do not add write or process permissions, so sub-agents may call them too.

| Tool | Purpose |
|------|---------|
| `ledger_read` | Resolve one bounded, redacted page of task content |
| `read_output` | Resolve one bounded page of a truncated tool-result remainder |
| `list_run_events` | Ordered lifecycle events for one run |

There is deliberately no freeform query tool. These tools run fixed, parameterized reads. The agent supplies bound arguments only. This removes the injection surface rather than guarding it.

#### Paging

Both `ledger_read` and `read_output` page long bodies the same way. `offset` is an optional byte cursor, default `0`. Use `next_offset` from a prior page verbatim. `limit` is an optional page-size request from 4 bytes to 32 KiB. Larger direct requests are capped and the effective limit is reported honestly.

#### Caller scoping and warnings

- `read_output` is caller-scoped. Only the session that received the truncation notice may load the reference. A different session receives `status: "denied"`.
- `ledger_read` is keyed only by content digest. It is not limited to one run. Any caller that holds a reference can resolve it. A reference can show whether the same content exists, so do not treat it as a privacy boundary.
- `ledger_read` and `read_output` return untrusted data. Content from either tool must never be treated as instructions.
- `list_run_events` is scoped to the session that created the run. Unknown and unauthorized run IDs are deliberately indistinguishable.
- Recorded content is never deleted and has no size limit. Treat execution history as retained, not as a deletion path.
- Older references may not resolve. Output references recorded by earlier mivia versions used truncated digests and cannot be matched.
- `kind` is a closed set on input. An unrecognized `kind` is rejected with the accepted values.

#### When to use which

| Signal in context | Tool |
|-------------------|------|
| Truncation notice: `remainder: ref:output:…, use read_output` | `read_output` with that ref |
| Task field `output_ref` or `error_ref` | `ledger_read` with that ref |
| Need run lifecycle metadata only | `list_run_events` |

## Interrupted-run recovery

When a run is interrupted, mivia detects it on the next startup. An interruption can come from a crash, a closed terminal, or an explicit stop. Interrupted runs appear in the TUI run dashboard (toggle with Ctrl+R) or are reported on stderr in REPL mode.

#### `/resume` slash command

- `/resume` lists all interrupted runs with their IDs and display names.
- `/resume <run-id>` shows a confirmation with task and attempt information. mivia asks for confirmation (`y`/`N`) before re-spending model budget.

#### TUI run dashboard

When the run dashboard is open (Ctrl+R), interrupted runs are listed with their IDs. Use the arrow keys to move the selection. The dashboard is read-only. Resume with `/resume <run-id>`.

The dashboard deliberately binds no bare letter keys. It sits above the composer and consumes keys before them.

#### Refusal causes

Resume can be refused for three reasons, each with its own message:

1. Held by another executor: another mivia process has claimed the run.
2. Already terminal: the run completed, failed, or was canceled.
3. Cannot be resumed: the run was created by an older mivia version that did not persist task inputs.

#### Re-spend disclosure

Resume re-executes tasks that were interrupted. That re-spends model budget. Before resuming, mivia shows what will re-run and requires explicit confirmation. This prevents accidental re-spending on work that may have partially completed.

## Slash commands

Slash commands work inside the chat. Type `/` followed by the command name.

| Command | What it does |
|---------|-------------|
| `/help`, `/h`, `/?` | Show help |
| `/clear` | Clear chat history |
| `/new` | Start a new session |
| `/status` | Show session status |
| `/worktrees` | Manage git worktrees (TUI) |
| `/sessions` | Manage saved sessions (TUI) |
| `/list` | List saved sessions |
| `/session` | Show current session |
| `/title [text]` | Set the session title (TUI) |
| `/tools` | Show available tools |
| `/plain` | Explain classic UI (TUI) |
| `/select` | Toggle select mode (TUI) |
| `/model [model]` | Choose model |
| `/agent [name]` | Choose root agent |
| `/agents` | List root agents |
| `/hooks` | List the lifecycle hooks this session runs |
| `/budget [tokens]` | Set context budget |
| `/effort [level\|unset]` | Choose reasoning effort |
| `/compact` | Compact context now |
| `/steps [n]` | Set maximum steps |
| `/save <name>` | Save session |
| `/load <name>` | Load session |
| `/delete <name>` | Delete session |
| `/resume [run-id]` | Resume an interrupted run |
| `/queue` | Manage queued messages |
| `/search` | Search the web |
| `/workflows` | Show workflow runs (TUI) |
| `/exit`, `/quit`, `/q` | Exit (classic terminal) |
| `/provider` | Show provider (classic terminal) |
| `/workspace` | Show workspace (classic terminal) |

## Lifecycle hooks

Hooks are safety scripts that a project can set. They run at fixed moments during a session. For example, a `PreToolUse` hook can block a tool call before it runs. Hooks come from `~/.mivia/mivia.toml` and the workspace's `.mivia/mivia.toml`. They add rather than replace. A repository you cloned can run its hooks on first launch. Every session names what it armed and marks which hooks came with the repo. For the full reference, see [Lifecycle hooks](../development/lifecycle-hooks.md).

## Safety and limits

- Paths must stay under `--workspace` (default: current directory), unless `--full-disk` is passed by the operator.
- File-tool secret filtering is controlled by `[tools].secret_path_patterns` and `[tools].secret_path_exceptions`. With no patterns, secret-like paths are not filtered.
- `run_command` receives an argv array, not a shell command string, and needs a configured program allowlist.
- Redaction is also configuration-controlled. Do not put secrets in prompts. Do not rely on tool filtering as a security boundary.
- Run results are stored by content reference and exposed to the model through bounded references. Stored content is raw at rest, even when a privacy policy redacts displayed content. Protect the store and keep secrets out of prompts.

`--full-disk` lifts the workspace confinement: file tools (`read_file`, `write_file`, `edit`, `list_dir`, `grep`, `glob`, etc.) may operate anywhere on the filesystem. This is an **operator-invocation flag only** — it cannot be set from workspace config (`.mivia/mivia.toml`), so a cloned repository cannot grant itself full disk access. The program allowlist, env allowlists, and the write-path denylist (`.git`, `.mivia/mivia.toml`) still apply to in-workspace paths even when `--full-disk` is active.

By default, one interactive turn has no step ceiling. Set `[chat] max_steps` to a positive number to cap turns, or use `/steps`. Ctrl-C cancels a reply in progress.

Named agents can be inspected without provider credentials:

- `mivia agents list [--workspace DIR]` lists selectable definitions, their source, resolved tool scope, spawned-task model default, and turn budget.
- `mivia agents explain NAME [--workspace DIR]` shows the bounded local path and resolution trace for one definition.
- `mivia doctor [--config PATH] [--workspace DIR]` reports agent discovery, malformed or shadowed files, and the workspace prompt and project-skill gate before returning provider-readiness errors.

In chat, `/agent NAME` remains the selector and `/agents` is a read-only list. Runtime events identify only definition name and source, an opaque instance ID, and the session-local model generation. They do not contain paths, digests, prompts, tools, or content.

## See also

- [Configuration](config.md)
- [Workflow guide](workflows-guide.md)
- [Security and privacy](../security/overview.md)
- [Architecture](../architecture/overview.md) and [concurrency](../architecture/concurrency.md)
- Tool names, descriptions, and schemas are project- and language-generic so mivia works as a host agent for any workspace.
