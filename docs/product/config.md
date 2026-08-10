# Configuration

Configuration selects the AI provider and model, and controls tool policy,
privacy, and orchestration limits. Two inputs: a settings file (`mivia.toml`)
and an API key, kept out of the settings file, in your environment.

```bash
export DEEPSEEK_API_KEY=sk-REPLACE-ME
mivia doctor   # confirms mivia can find the key; never prints it
mivia chat
```

Default provider: DeepSeek, model `deepseek-v4-flash`. Run `mivia setup` or
copy the example settings file before `doctor` or `chat`; an API key alone
does not create the required model catalog.

## Where mivia looks for settings

mivia reads the first existing settings file it finds, in this order:

1. The file named by `$MIVIA_CONFIG`.
2. `./.mivia/mivia.toml` (the project folder).
3. `~/.mivia/mivia.toml` (your home folder).

## Where mivia looks for the API key

API keys live in an env file (`NAME=value` lines) or in the process environment. Search order:

1. `./.env`
2. `~/.mivia/.env`

Non-empty process environment variables win over values in the env file.

The workspace `.env` stays beside the project files at `./.env`. Tools such as direnv and Docker Compose already use that location. Only the user-level env file lives in `~/.mivia/`.

## Defaults

| Setting | Default |
|---------|---------|
| Provider | `deepseek` |
| DeepSeek example model | `deepseek-v4-flash` (declare it in your settings) |
| DeepSeek advanced example | `deepseek-v4-pro` (declare it, then set `default_model` or use `--model`) |
| OpenRouter example | `openai/gpt-4o-mini` (declare it under `providers.openrouter`) |
| ZAI example | `glm-5.2` (declare it under `providers.zai`) |

## Set up a provider

Set the provider API key in the process environment or an env file. Then run `mivia doctor` to confirm that mivia can find it.

```bash
export DEEPSEEK_API_KEY=sk-REPLACE-ME
mivia doctor
mivia chat -p "hi"
```

For a source checkout, keep the project settings in the workspace namespace. Keep the workspace credentials file at the repository root.

```bash
mkdir -p .mivia
cp .mivia/mivia.toml.example .mivia/mivia.toml
cp .env.example .env
# edit .env with real keys
```

```toml
env_file = "./.env"

[provider]
name = "deepseek"

[providers.deepseek]
models = [
  { name = "deepseek-v4-flash", context_window_tokens = 1000000, max_output_tokens = 384000 },
  { name = "deepseek-v4-pro", context_window_tokens = 1000000, max_output_tokens = 384000 },
]
default_model = "deepseek-v4-flash"
# For harder tasks:
# default_model = "deepseek-v4-pro"

[providers.openrouter]
models = [{ name = "openai/gpt-4o-mini", context_window_tokens = 128000 }]
default_model = "openai/gpt-4o-mini"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 200000, max_output_tokens = 128000 }]
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"
```

## Provider support

mivia currently supports `deepseek`, `openrouter`, and `zai`. Do not add an
arbitrary OpenAI-compatible provider name. The provider registry rejects names
that it does not support.

z.ai serves two OpenAI-compatible endpoints. A key works on exactly one of them. Pay-as-you-go keys use `https://api.z.ai/api/paas/v4`. GLM Coding Plan keys use `https://api.z.ai/api/coding/paas/v4`. A Coding Plan key on the pay-as-you-go endpoint fails every request with code `1113`. mivia reports the code and what it means. It never forwards z.ai's own error text.

## Explicit model catalog

Every provider must declare a non-empty `models` list. Each entry has a provider-local `name`, a `context_window_tokens` value, and an optional positive `max_output_tokens` value. The list is the complete catalog. `--model`, `/model`, the TUI picker, and resumed sessions may select only its entries. `default_model` sets the startup default and must be in `models`. An invalid value is rejected.

An empty list, a missing list, or a remote model registry is invalid. mivia does not discover models remotely and does not accept arbitrary model names. Model IDs stay intact, including slash-containing IDs such as `openai/gpt-4o-mini`. Duplicate IDs are allowed across providers but not within one provider. Providers without credentials stay visible in the catalog but are disabled for selection.

`context_window_tokens` is the model's physical prompt-plus-completion limit. `max_output_tokens` is the response ceiling and must stay below the context window. The usable prompt budget keeps the tighter of this value and `[chat].max_tokens`, further limited by `max_prompt_tokens` when set. `config show` shows each catalog entry as `provider/model:context_window_tokens`.

```bash
DEEPSEEK_API_KEY=sk-REPLACE-ME
OPENROUTER_API_KEY=sk-REPLACE-ME
ZAI_API_KEY=sk-REPLACE-ME
```

## Installed binary

Create `~/.mivia/mivia.toml` and, if you want, `~/.mivia/.env` with the settings above. Leave the root-level `env_file` unset to use the default `~/.mivia/.env`. There is no `config init` command.

## Worktree branches

mivia creates linked worktree branches with the `[worktrees].branch_prefix` setting. The default is `"mivia/"`. For example, `mivia worktree create fix` creates the branch `mivia/fix`.

```toml
[worktrees]
branch_prefix = "mivia/"
```

The prefix must end with `/` and form a valid Git branch name when mivia adds a worktree name. The prefix must not be empty. Do not use spaces, control characters, or Git ref characters such as `~`, `^`, `:`, `?`, `*`, `[`, or `\`. Do not use invalid ref sequences such as `..`, `//`, and `@{`. Each path component must be non-empty and must not start with `.` or end with `.lock`.

The CLI and TUI always read this setting from `<main-repository>/.mivia/mivia.toml`. A command run in a linked worktree uses the main repository setting. It does not use a config file in the linked worktree or `MIVIA_CONFIG` for worktree branch operations.

mivia preserves a removed worktree branch. This avoids destructive branch deletion when other worktrees use the branch. If you create a worktree with the same name again, mivia reuses the retained branch that has the current configured prefix. mivia does not reset that branch.

If you change the prefix, branches with the old prefix remain. Remove them manually only after you confirm that no worktree needs them.

## Named agents

Named agents are separate TOML files, one definition per file. User-owned definitions live in `~/.mivia/agents/<name>.toml`. Workspace definitions live in `<workspace>/.mivia/agents/<name>.toml`. Create those two directories as needed. The filename is canonical: `<name>.toml` must contain the same lowercase `name`. Agent files are not inline `[agents]` configuration. Read [Coding agent mode](agent.md#named-agents-and-skill-binding) for the full schema.

## MCP servers

Configure Model Context Protocol (MCP) servers in `~/.mivia/mivia.toml` or in the project `.mivia/mivia.toml`. A project server can start a local process or call the configured HTTP endpoint. Review it before use.

When user and project files define the same server ID, the project definition replaces the complete user definition. mivia does not merge command arguments, environment names, URLs, or headers between definitions.

```toml
[mcp]
# MCP is disabled unless this is true.
enabled = true

[[mcp.servers]]
id = "repository"
transport = "stdio"
command = "/usr/local/bin/repository-mcp"
args = ["serve"]
env = ["REPOSITORY_MCP_TOKEN"]
global = true

[[mcp.servers]]
id = "issues"
transport = "streamable_http"
url = "https://mcp.example.test/mcp"
headers = [{ name = "Authorization", value_env = "ISSUES_MCP_TOKEN" }]
```

The configuration stores only environment variable names. It never stores secret values. A global server is available to a root agent that omits `mcp_servers`. A named agent can set `mcp_servers = []` to deny all MCP servers, or list exact server IDs. Workspace agents can select only global servers. A child that omits `mcp_servers` inherits its parent server list; it can only narrow that list.

An invalid server definition refuses configuration load. An invalid server definition has a bad ID, unsafe transport fields, or oversized bounds. Stdio commands must use absolute paths. HTTP URLs and transport fields must pass the configured validation rules. A server is connected lazily when an allowed agent selects it. If connection or discovery fails, that selection fails and names the server. MCP tools are never silently absent. This is deliberate fail-closed behavior. It is not configurable.

`startup_timeout_seconds` bounds initial MCP connection work. Each server's
`timeout_seconds` bounds discovery and calls. Server count, tool count,
description size, schema size, and result size have configured limits. Stdio
passes only named environment variables that exist. HTTP headers are omitted
when their named environment variables do not exist. MCP exposes text result
content only. It bounds result content and refuses or truncates content when
the configured limit requires it. Remote error details are not passed through.

Every MCP tool description sent to the model states the remote tool name and the server ID. The description is never empty. If a server provides no description, mivia uses the sentence 'The server provides no description.' mivia removes control and format characters, applies the configured [privacy] redaction patterns, and bounds the description by `max_tool_description_bytes`. A description that exceeds the cap is refused.

## Tool safety policy

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only source of the file-tool secret filter. Nothing is compiled into the binary, so an unconfigured workspace filters nothing. Recommended starting values ship in `.mivia/mivia.toml.example`. Patterns match case-insensitively as substrings of the workspace-relative path. Exceptions take precedence.

This guards against accidental exposure, not against a determined agent. `run_command` can build a path at runtime and reach the file anyway. With these patterns unset, no paths are filtered.

## Allowlists are configuration-only

Neither the `run_command` program allowlist nor the child-process environment allowlist is compiled into the binary. `[tools].run_allowlist` and `[tools].env_allowlist` are the only sources. With them unset, `run_command` executes nothing and child processes inherit no environment.

Recommended values ship in `.mivia/mivia.toml.example`. Copy it and trim it to what your project needs. The example includes powerful programs, including shells and network clients. Remove anything your workspace does not need. In `env_allowlist`, a trailing `*` declares a prefix rule (for example, `"GIT_*"`). Because there is no built-in list to extend or replace, `run_allowlist_only` and `env_allowlist_only` behave identically to their plain counterparts.

`[tools].env_allow_keyword_blocklist` is the companion subtractive filter. A variable admitted by a `*` prefix rule is dropped when its name contains any listed substring. The example lists `SECRET`, `TOKEN`, `PASSWORD`, and `API_KEY`. Exact `env_allowlist` entries are never dropped, so a build that needs `FOO_TOKEN` names it outright. Unset means prefix rules admit everything they match.

## Redaction and persisted orchestration history

`[privacy].redaction_patterns` and `[privacy].redaction_key_names` control redaction in displayed tool previews, output, and event bodies. They do not redact SQLite task inputs or result content at rest. The example configuration provides starting patterns. Adapt and test them for your workspace.

`[subagents].store_backend = "sqlite"` persists orchestration state. By default the database is in the current user's cache directory under a workspace-derived name. Set `store_path` to choose a different location.

That state includes each task's full input payload, recorded for recovery support and execution history.

Two consequences worth knowing before you enable it:

- Task inputs and results are written unredacted at rest, even when `[privacy]` patterns are configured. Treat the chosen store location as sensitive workspace data. Do not put secrets in task prompts.
- Permissions, scopes, roles, and caller identity are deliberately not stored. They are never written to the run record or restored from it. A resumed run uses the permissions of whoever resumes it. Editing the store file cannot grant new permissions. Resource limits (timeout, budget, depth) are restored but clamped to your current configuration.

## Interactive turn ceiling

`[chat] max_steps` bounds one turn's agent loop. `0` means unlimited, and this is the default when unset. `/steps` overrides it for the current session.

## Tool result ceiling

`[tools] max_tool_result_bytes` caps each tool result stored in agent-loop history, in bytes. Default is `0`, which means uncapped. The per-tool budgets (`max_read_bytes`, `max_output_bytes`, tool-declared limits) are the bound. The one knob governs both the interactive session loop and nested sub-agent loops, so a sub-agent never sees a different ceiling than the session that spawned it.

Set a positive value (minimum 1024; smaller positive values are a config error) when running small-context models. When a cap is set, `read_file` pre-clamps its own byte budget below it, and the code-navigation tools (`find_references`, `list_symbols`, `go_to_definition`) tighten their JSON budgets to fit.

Rollback: `max_tool_result_bytes = 4000` restores the previous hardcoded interactive-loop ceiling.

## Per-batch tool result budget

`[tools] batch_result_budget_bytes` bounds what one tool batch adds to history, across all of its parallel calls together. Default is `0`, which means off.

- `0` (default): off. History is byte-for-byte what it is without the key.
- `-1`: derive it from the model's prompt budget (a quarter of it in bytes, never below 256 KiB). Inert when there is no prompt budget configured.
- a positive value: the literal byte budget. Minimum 16384; smaller positive values are a config error.

Over-budget results are degraded, never failed. The call already ran and its side effects already happened, so its result is re-cut. Once the budget is spent, the result is replaced by a truncation notice that names the content reference holding the full body. The model pages it back with `read_output` when it needs it.

What the budget does not charge: lifecycle-hook advisory context (it has its own 8 KiB bound) and truncation notices themselves.

## Web research response bound

`[tools] max_tavily_response_bytes` bounds a Tavily API response body, in bytes. It governs the `search` tool's Tavily path and the `extract` tool. Default is 4194304 (4 MiB).

Responses are never truncated. Nothing fetched is ever cut. The bound exists so that the maximum size of these tools' results is a known, finite number. A result over the bound is refused with an explicit error naming the bound and this key. Raise the key if you hit it.

Unset, `0`, and negative all mean "use the default". There is deliberately no unlimited setting. Values outside `1024`–`67108864` are rejected at load.

This bound is not clamped by `max_tool_result_bytes`. That key caps what the agent loop stores. Installs with no Tavily API key are unaffected.

## Memory OOM backstop

`[tools] memory_backstop_mb` (default `256`) is the out-of-memory guard for tools that may load whole files when volume caps are uncapped. It is not a context-cost cap. `0` or negative resolves to the default 256 so the guard cannot be accidentally disabled.

## Bounded `run_command` capture

When `max_output_bytes` is a positive bound, stdout and stderr capture keeps roughly one-third head and two-thirds tail of the shared budget, with an elision marker between. Compiler error tails survive.

## Durable source payloads (chunking)

`[context] max_source_event_bytes` is the chunk size for durable source event payloads, not a whole-payload reject. `0` uses a built-in default chunk size (64 KiB). Large payloads store as an ordered chunk sequence under one content ref (SHA-256 of the full payload). ReadPayload reassembles byte-identical and fails closed on digest mismatch.

## Subagent knobs

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `max_workers` | int | `0` (unlimited) | Goroutines for concurrent tasks |
| `max_depth` | int | `0` (unlimited) | Nesting depth |
| `max_fanout` | int | `0` (unlimited) | Parallel sub-tasks per level |
| `nested_steps` | int | `0` (unlimited) | Sub-agent loop steps per turn |
| `default_timeout_seconds` | int | `0` | Per-task orchestration timeout; `0` = safety bound (12 hours) |
| `default_request_timeout_seconds` | int | `0` | Per-LLM-request timeout for subagent turns; `0` = fall back to the effective orchestration default |
| `default_budget` | int | `0` (unlimited) | Per-task token budget |
| `store_backend` | string | `"memory"` | Outside chat: `"memory"` (ephemeral) or `"sqlite"` (durable) |
| `store_path` | string | platform default | One SQLite file for chat sessions, context, worktree routes, and runs |

For `mivia chat`, mivia uses one SQLite file for all durable chat state. It resolves `store_path` from the main repository configuration. A worktree never creates another chat database. When `store_path` is unset, mivia uses the main repository cache path.

`default_request_timeout_seconds` never needs to be set below `default_timeout_seconds`. The outer orchestration timeout cancels the turn first. The internal 15-minute HTTP transport timeout is the hard per-request ceiling. It stops a single hung provider call from blocking a sub-agent beyond that limit.

## See also

- [Product overview](overview.md)
- [Coding agent mode](agent.md)
- [Workflow guide](workflows-guide.md)
- [Security and privacy](../security/overview.md)
