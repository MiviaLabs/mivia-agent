# Configuration

Configuration selects the AI provider and model, and controls tool policy,
privacy, and orchestration limits. Two inputs: a settings file (`mivia.toml`)
and an API key, kept out of the settings file, in your environment.

```bash
export OPENROUTER_API_KEY=sk-REPLACE-ME
mivia doctor   # confirms mivia can find the key; never prints it
mivia chat
```

Default provider: OpenRouter, model `openai/gpt-5.6-luna`. Run `mivia setup`
or copy the example settings file before `doctor` or `chat`; an API key alone
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
| Provider | `openrouter` |
| OpenRouter model | `openai/gpt-5.6-luna` (declare it under `providers.openrouter`) |
| OpenRouter cheaper alternative | `openai/gpt-4o-mini` (declare it under `providers.openrouter`) |
| DeepSeek example model | `deepseek-v4-flash` (declare it under `providers.deepseek`) |
| DeepSeek advanced example | `deepseek-v4-pro` (declare it, then set `default_model` or use `--model`) |
| ZAI example | `glm-5.2` (declare it under `providers.zai`) |
| Ollama example | `gpt-oss:120b` (declare it under `providers.ollama`) |
| MiniMax example | `MiniMax-M3` (declare it under `providers.minimax`) |

## Set up a provider

Set the provider API key in the process environment or an env file. Then run `mivia doctor` to confirm that mivia can find it.

```bash
export OPENROUTER_API_KEY=sk-REPLACE-ME
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
name = "openrouter"

[providers.openrouter]
models = [
  { name = "openai/gpt-5.6-luna", context_window_tokens = 400000, max_output_tokens = 128000 },
  { name = "openai/gpt-4o-mini", context_window_tokens = 128000 },
]
default_model = "openai/gpt-5.6-luna"

[providers.deepseek]
models = [
  { name = "deepseek-v4-flash", context_window_tokens = 1000000, max_output_tokens = 384000 },
  { name = "deepseek-v4-pro", context_window_tokens = 1000000, max_output_tokens = 384000 },
]
default_model = "deepseek-v4-flash"
# For harder tasks:
# default_model = "deepseek-v4-pro"

[providers.zai]
models = [{ name = "glm-5.2", context_window_tokens = 200000, max_output_tokens = 128000 }]
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"
```

### Ollama

The `ollama` provider supports two modes. The provider name is always
`ollama`; mode is inferred from `base_url` via the loopback predicate —
literal loopback hostnames (`127.0.0.1`, `::1`, `localhost`) mean local
daemon mode with **no API key**; any other `base_url` means cloud mode
and requires `OLLAMA_API_KEY`. localhost is trusted as loopback per the
design (matching internal/config/loopback.go's doc comment); environments
where localhost does not resolve to loopback should use 127.0.0.1 instead.
The client additionally resolves the host once at construction and pins the
connection to the verified loopback address, so keyless local mode fails
closed (with a clear error) if localhost resolves to a non-loopback address.

**Cloud profile** (Ollama Cloud, API key required):

```toml
[providers.ollama]
models = [{ name = "gpt-oss:120b", context_window_tokens = 131072 }]
default_model = "gpt-oss:120b"
api_key_env = "OLLAMA_API_KEY"
base_url = "https://ollama.com/v1"
```

**Local-daemon profile** (local Ollama, no key needed):

```toml
[providers.ollama]
models = [{ name = "gpt-oss:120b", context_window_tokens = 131072 }]
default_model = "gpt-oss:120b"
base_url = "http://127.0.0.1:11434/v1"
```

Local daemon model names must be declared in `models` and match the
output of `ollama list`. The local profile needs no key in the env file.

### LLM Gateway

The `llmgateway` provider sends requests to the OpenAI-compatible endpoint of
LLM Gateway (`https://api.llmgateway.io/v1`). One factory serves both key
types: a fixed-price DevPass coding-plan key and a pay-as-you-go key. Both
key types use the same endpoint, the same `Authorization: Bearer <key>`
header, and the same error format.

```toml
[providers.llmgateway]
models = [
  { name = "deepseek-v4-pro", context_window_tokens = 1100000, reasoning_efforts = ["low", "medium", "high", "max"], reasoning = "high" },
  { name = "muse-spark-1.2", context_window_tokens = 1000000 },
  { name = "glm-5.2", context_window_tokens = 1000000 },
]
default_model = "deepseek-v4-pro"
api_key_env = "LLMGATEWAY_API_KEY"
base_url = "https://api.llmgateway.io/v1"
```

A DevPass coding-plan key accepts root model IDs only, for example
`deepseek-v4-pro`. A provider-prefixed model ID, for example
`openai/gpt-4o`, gets a `403` response on DevPass. A `403` with a
provider-prefixed model ID means: use the root ID. It does not mean the
key is wrong. A pay-as-you-go key accepts both root and provider-prefixed
model IDs.

The gateway sends the top-level `reasoning_effort` field to the upstream
model. mivia's vetted default dialect for `llmgateway` is `openai`. A model
entry may set `reasoning_dialect` to override this default.

The gateway adds its own cache markers on the request. mivia does not send
its own cache markers to this provider.

### LLM Proxy CLI

The `llmproxycli` provider connects to a local CLI proxy server (such as `llm-proxy-cli` or LiteLLM) running on localhost (default `http://127.0.0.1:8317/v1`). It supports standard OpenAI-compatible completions and reasoning dialects.

```toml
[providers.llmproxycli]
default_model = "claude-sonnet-5"
api_key_env = "CLIPROXY_API_KEY"
base_url = "http://127.0.0.1:8317/v1"

[[providers.llmproxycli.models]]
name = "claude-sonnet-5"
context_window_tokens = 200000
max_output_tokens = 128000
reasoning = "high"
reasoning_efforts = ["low", "medium", "high", "max"]
```

## Provider support

mivia currently supports `deepseek`, `openrouter`, `zai`, `ollama`, `llmgateway`, `llmproxycli`, and `minimax`. Do not add an
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
OLLAMA_API_KEY=ollama-REPLACE-ME  # required for cloud mode only; local daemon needs no key
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

Named agents are separate TOML files, one definition per file. User-owned definitions live in `~/.agents/agents/<name>.md`. Workspace definitions live in `<workspace>/.agents/agents/<name>.md`. Create those two directories as needed. The filename is canonical: `<name>.toml` must contain the same lowercase `name`. Agent files are not inline `[agents]` configuration. Read [Coding agent mode](agent.md#named-agents-and-skill-binding) for the full schema.

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

## Write-path blocklist

`[tools].write_path_blocklist` names workspace-relative paths or directories that the write tools of workflow agent steps (`write_file`, `search_replace`, `multi_edit`, `delete_file`) refuse to change. It applies to workflow runs only, not to the interactive session.

Two paths are blocked by default: `.git` and `.mivia/mivia.toml`. The key adds to that default set. `[tools].write_path_blocklist_remove` removes entries from the effective set — a default entry or an addition — and is the only way to unblock the two defaults. Removing a default is a trust decision: `.mivia/mivia.toml` carries this very blocklist, so an agent that can edit it can remove its own restrictions, and `.git` carries commit history and hooks that a workflow agent could rewrite or bypass. An entry in both keys is a config error.

Entries use forward slashes. At load, mivia trims whitespace and cleans each entry, so `" go.mod/ "` becomes `"go.mod"`. An entry that is empty, that resolves to the workspace root, or that is absolute is a config error: mivia refuses to start rather than silently ignore a blocklist entry that can never match.

This key is a project decision. A project that omits it leaves paths such as `.agents/agents`, `.mivia/policy`, `.mivia/skills`, `.agents/rules`, `.agents/skills`, `.mivia/workflows`, `go.mod`, `go.sum`, and `go.work` writable by workflow agents. That includes the workflow definition the run executes. Recommended starting values ship in `.mivia/mivia.toml.example` and in this repository's own `.mivia/mivia.toml`.

## Redaction and persisted orchestration history

`[privacy].redaction_patterns` and `[privacy].redaction_key_names` control redaction in displayed tool previews, output, and event bodies. They do not redact SQLite task inputs or result content at rest. The example configuration provides starting patterns. Adapt and test them for your workspace.

`[subagents].store_backend = "sqlite"` persists orchestration state. By default the database is in the current user's cache directory under a workspace-derived name. Set `store_path` to choose a different location.

That state includes each task's full input payload, recorded for recovery support and execution history.

Two consequences worth knowing before you enable it:

- Task inputs and results are written unredacted at rest, even when `[privacy]` patterns are configured. Treat the chosen store location as sensitive workspace data. Do not put secrets in task prompts.
- Permissions, scopes, roles, and caller identity are deliberately not stored. They are never written to the run record or restored from it. A resumed run uses the permissions of whoever resumes it. Editing the store file cannot grant new permissions. Resource limits (timeout, budget, depth) are restored but clamped to your current configuration.

## Interactive turn ceiling

`[chat] max_steps` bounds one turn's agent loop. `0` means unlimited, and this is the default when unset. `/steps` overrides it for the current session.

## Bounded prompt budget

`[chat] max_prompt_tokens` caps the per-request prompt budget in tokens. The recommended value is `200000`.

When unset, the prompt budget is the model window minus the output reserve (for example, `616000` on `deepseek-v4-flash`). A bounded budget makes history compaction fire earlier - at 80% of the budget, targeting 50% - and cuts token cost on long sessions.

The recall-versus-price dial works as follows: larger values keep more history in the prompt at higher cost; smaller values compact sooner and spend fewer tokens. The escape hatch is any explicit value up to `10000000` (10M).

When the knob is unset and the active budget exceeds `200000`, `mivia doctor` and `mivia config show` print a `prompt_budget_advisory` suggesting the recommended cap. Set `[chat] max_prompt_tokens = 200000` to suppress the advisory.

## Tool result ceiling

`[tools] max_tool_result_bytes` caps each tool result stored in agent-loop history, in bytes. Default is `0`, which means uncapped. The per-tool budgets (`max_read_bytes`, `max_output_bytes`, tool-declared limits) are the bound. The one knob governs both the interactive session loop and nested sub-agent loops, so a sub-agent never sees a different ceiling than the session that spawned it.

Set a positive value (minimum 1024; smaller positive values are a config error) when running small-context models. When a cap is set, `read_file` pre-clamps its own byte budget below it, and the code-navigation tools (`find_references`, `list_symbols`, `go_to_definition`) tighten their JSON budgets to fit.

Rollback: `max_tool_result_bytes = 4000` restores the previous hardcoded interactive-loop ceiling.

## Per-batch tool result budget

`[tools] batch_result_budget_bytes` bounds what one tool batch adds to history, across all of its parallel calls together. The recommended value is `-1` (derived).

- `-1` (recommended): derive it from the model's prompt budget. The derived value is a quarter of the prompt budget in bytes, with a 256 KiB floor (so `200000` tokens yields `262144` B). Inert when there is no prompt budget configured.
- `0` (off): history is byte-for-byte what it is without the key.
- a positive value: the literal byte budget. Minimum 16384; smaller positive values are a config error.

Over-budget results are degraded to recoverable content references (`read_output`), never failed. The call already ran and its side effects already happened, so its result is re-cut. Once the budget is spent, the result is replaced by a truncation notice that names the content reference holding the full body. The model pages it back with `read_output` when it needs it.

What the budget does not charge: lifecycle-hook advisory context (it has its own 8 KiB bound) and truncation notices themselves.

Contrast with `[tools] max_tool_result_bytes = 0` (uncapped per-call). Per-call results are already bounded by each tool's declared `ResultBudgetBytes` - see the per-tool budgets (`max_read_bytes`, `max_output_bytes`, tool-declared limits). The batch budget is the only knob that bounds a group of parallel calls together.

## Ref-only tools

`[tools] ref_only_tools` is an opt-in list of tool names whose results are never inlined into the model context when they exceed the batch degrade floor (16 KiB, `BatchDegradeFloorBytes`). Instead the whole body is spooled to the remainder store and the result is replaced by a notice naming a remainder ref the model can fetch with `read_output`. Only the notice's bytes are charged (notice-only token charge). Ephemeral tools are never spooled. The default is empty (off).

When the spool succeeds the notice names the ref:

```
[tool result for <name> elided to a remainder ref (original ~N KiB): <ref> — use read_output to fetch the full body]
```

When the spool is nil, the principal is empty, or the store fails the notice omits the ref and the body is lost:

```
[tool result for <name> elided; original ~N KiB]
```

No ref is ever invented on a failed spool. The config key is `ref_only_tools` (TOML), defaulting to an empty list (off). Entries are matched exactly (case-sensitive) by tool name.

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

## LLM compaction summaries

`[context.summary] enabled` (default `false`) turns on the bounded provider call that summarizes what context compaction dropped. The call uses the session's provider and model. On auto compaction, the validated summary is injected into the next request as a host-authored `context-summary` message. A manual `/compact` requests the same summary: the reply is appended to the live session history as the `context-summary` message, and a bounded form is stored on the durable checkpoint. A session resumed from storage replays the structural history; the stored summary is not re-rendered on load.

Two more conditions must hold, or the summary stays off: a configured `[privacy]` redaction policy, and a resolved provider endpoint. A summary the redaction policy refuses is dropped, never sent or stored.

Any summary failure - transport error, malformed reply, redaction refusal, over-budget reply - degrades silently to structural-only compaction. A turn never fails because of the summary call.

The summarize request carries bounded quotes of the dropped messages' real content (user and assistant text plus truncated tool results, at most 16 KiB, newest first). An excerpt the `[privacy]` policy flags is dropped from the request; tool-call arguments and assistant reasoning are never included.

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
| `store_path` | string | `~/.mivia/context.db` (chat); platform cache dir (non-chat orchestration) | One SQLite file for chat sessions, context, worktree routes, and runs |

For `mivia chat`, mivia uses one SQLite file for all durable chat state: sessions, context, worktree routes, and runs. When `store_path` is unset, mivia uses the shared global path `~/.mivia/context.db`, so every workspace on the machine has the same chat history by default. Sessions stay isolated inside that shared file by workspace ID: two projects never see each other's sessions even though they share one file. A worktree never creates another chat database. Set `store_path` to give one workspace its own separate file instead of the shared default.

## Workflow panel limits

`[workflows.panels]` overrides the per-child-agent bounds every `agent_panel`
step's member and synthesis children run under. Each key is optional; an
unset key keeps mivia's compiled default, so an empty or absent
`[workflows.panels]` table reproduces today's behavior exactly.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `member_max_output_per_call` | int | `8192` | Output-token ceiling for one panel member's provider call |
| `member_max_tool_calls` | int | `64` | Cumulative tool calls one panel member may make |
| `synthesis_max_output_per_call` | int | `8192` | Output-token ceiling for one synthesis child's provider call |
| `synthesis_max_tool_calls` | int | `16` | Cumulative tool calls the synthesis child may make |
| `member_deadline_default_seconds` | int | `86400` (24h) | Wall-clock default for a panel member attempt when the workflow declares no run deadline (`max_duration_seconds = 0`); a workflow's own deadline always wins when it is earlier |

A step's own `max_turns` (see the workflow definition's step syntax, not this
file) always bounds turns per child; it is not part of `[workflows.panels]`
because it is a per-step knob, not a host-wide default. Cumulative prompt and
output token totals are deliberately unbounded for panel children regardless
of this table - only the per-call ceiling and the cumulative tool-call count
are host-configurable.

```toml
[workflows.panels]
member_max_tool_calls = 128
member_deadline_default_seconds = 43200
```

## Live cross-process relay

Mivia processes that resolve the same store directory share a live event hub. `hub.lock` and `hub.sock` sit beside `context.db`: one process owns the hub and every other process joins it as a client, so a turn published by one process is relayed to all hub members. Two surfaces relay only when their effective configs agree on the store directory - which happens automatically today, since every workspace defaults to the same shared `~/.mivia/context.db`.

If a workspace pins its own `store_path` (see above), its processes relay only with other processes that resolve that same path:

```toml
[subagents]
store_backend = "sqlite"
store_path = "~/.mivia/my-project/context.db"
```

The hub is keyed to the store directory, not the workspace, so anything that moves `store_path` (for example a picked project's own `.mivia/mivia.toml` overriding the shared default) moves the process to a different hub.

Rendering is directional today. Line-mode `--json` renders turns received from other processes as `external_*` NDJSON events. The TUI and classic REPL publish their own turns to the hub but do not yet render turns received from other processes. The full event vocabulary is specified in [Wire schema](wire-schema.md).

`default_request_timeout_seconds` never needs to be set below `default_timeout_seconds`. The outer orchestration timeout cancels the turn first. The internal 15-minute HTTP transport timeout is the hard per-request ceiling. It stops a single hung provider call from blocking a sub-agent beyond that limit.

## See also

- [Product overview](overview.md)
- [Coding agent mode](agent.md)
- [Workflow guide](workflows-guide.md)
- [Security and privacy](../security/overview.md)
