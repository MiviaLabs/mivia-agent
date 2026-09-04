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

When no settings file exists, mivia bootstraps `~/.mivia/mivia.toml`
automatically on first run. It never auto-creates a project-level
`./.mivia/mivia.toml` — that file is always an explicit, deliberate choice.

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
| Anthropic example | `claude-sonnet-5` (declare it under `providers.anthropic`) |

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
context_window_tokens = 1000000
max_output_tokens = 128000
reasoning = "high"
reasoning_efforts = ["low", "medium", "high", "max"]
```

A proxy that translates OpenAI-compatible requests to Anthropic's real API
cannot always deliver Anthropic's own request-shape constraints - most
commonly, Anthropic rejects a non-default `temperature` outright, which
bites any config with a global `[chat] temperature` set alongside an active
reasoning level. If your proxy also exposes Anthropic's native Messages API
(`POST /v1/messages`, same host) alongside its OpenAI-compatible endpoint,
add `reasoning_dialect = "anthropic_adaptive"` to a Claude model entry to
route that specific model's requests through mivia's native Anthropic wire
format instead - reusing this provider's own `base_url` and
`CLIPROXY_API_KEY`, no separate `[providers.anthropic]` block needed:

```toml
[[providers.llmproxycli.models]]
name = "claude-sonnet-5"
context_window_tokens = 1000000
max_output_tokens = 128000
reasoning = "high"
reasoning_efforts = ["low", "medium", "high", "xhigh", "max"]
reasoning_dialect = "anthropic_adaptive"
```

Every other model on `llmproxycli` (and this same model if you omit the
override) keeps speaking OpenAI-compatible chat/completions unchanged.
`reasoning_dialect = "anthropic_adaptive"` is rejected at config-load time
on any provider that cannot actually deliver it (only `anthropic` and
`llmproxycli` can today).

### Anthropic

The `anthropic` provider speaks Anthropic's native Messages API
(`https://api.anthropic.com/v1`) directly - unlike every other built-in
provider, it does not go through the OpenAI-compatible transport, because
Anthropic's request and response shapes are structurally different (system
prompt at the top level, content blocks instead of a flat message string,
thinking blocks instead of a `reasoning_content` field).

```toml
[providers.anthropic]
models = [
  { name = "claude-sonnet-5", context_window_tokens = 1000000, max_output_tokens = 128000, reasoning_efforts = ["low", "medium", "high", "xhigh", "max"], reasoning = "high" },
]
default_model = "claude-sonnet-5"
api_key_env = "ANTHROPIC_API_KEY"
base_url = "https://api.anthropic.com/v1"
```

Reasoning works differently here than on every other provider: Anthropic's
`claude-sonnet-5` rejects the manual `budget_tokens` thinking shape outright
(HTTP 400). mivia sends `thinking: {"type": "adaptive"}` plus a graded
`output_config.effort` instead - Claude decides how much to think per turn,
and `effort` (`low` through `max`) is the depth dial. A refusal (Anthropic's
safety classifiers declining a request) is not an error: it surfaces as an
ordinary turn whose finish reason is `refusal`, with empty or partial
content depending on whether the decline happened before or during output.

## Provider support

mivia currently supports `anthropic`, `deepseek`, `openrouter`, `zai`, `ollama`, `llmgateway`, `llmproxycli`, and `minimax`. Do not add an
arbitrary OpenAI-compatible provider name. The provider registry rejects names
that it does not support.

z.ai serves two OpenAI-compatible endpoints. A key works on exactly one of them. Pay-as-you-go keys use `https://api.z.ai/api/paas/v4`. GLM Coding Plan keys use `https://api.z.ai/api/coding/paas/v4`. A Coding Plan key on the pay-as-you-go endpoint fails every request with code `1113`. mivia reports the code and what it means. It never forwards z.ai's own error text.

## Explicit model catalog

Every provider must declare a non-empty `models` list. Each entry has a provider-local `name`, a `context_window_tokens` value, and an optional positive `max_output_tokens` value. The list is the complete catalog. `--model`, `/model`, the TUI picker, and resumed sessions may select only its entries. `default_model` sets the startup default and must be in `models`. An invalid value is rejected.

An empty list, a missing list, or a remote model registry is invalid. mivia does not discover models remotely and does not accept arbitrary model names. Model IDs stay intact, including slash-containing IDs such as `openai/gpt-4o-mini`. Duplicate IDs are allowed across providers but not within one provider. Providers without credentials stay visible in the catalog but are disabled for selection.

`context_window_tokens` is the model's physical prompt-plus-completion limit. `max_output_tokens` is the response ceiling and must stay below the context window. The usable prompt budget keeps the tighter of this value and `[chat].max_tokens`, further limited by `max_prompt_tokens` when set. `config show` shows each catalog entry as `provider/model:context_window_tokens`.

An explicit `[chat] max_tokens` is authoritative about how much **answer** you want. It is not authoritative about how much a model spends thinking before it writes one, so on a reasoning model it is raised to that model's reasoning reserve when it sits below it: `max` and `xhigh` reserve 65536, `high` 32768, `medium` 16384, `low` and `minimal` 8192. Below the reserve the request is not a smaller answer, it is no answer - an always-thinking model (z.ai's GLM-5.3 family, where `thinking.type` accepts only `enabled`) spends the whole allowance on reasoning tokens, returns `finish_reason: length` with empty content, and the turn fails with `agent: turn produced no assistant text`. The raise never exceeds `max_output_tokens` or the context window, and a model with no reasoning level, or `off`, keeps your value exactly.

This matters most for a user-level `~/.mivia/mivia.toml`: `loadFile` layers a workspace file over the base file per key, so a modest `max_tokens` there applies to every workspace that does not set its own - including one bound to a hard-thinking model the value was never chosen for.

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

## Approval mode

`[approvals] default_mode` sets how mivia handles tool-call approval. The
default is `"always"`: mivia accepts every tool call automatically, with no
prompt. Set it to `"once"` to prompt for every write or external tool call,
or `"deny"` to auto-deny every gated tool call. This key controls the TUI
settings screen's approval choice; the CLI flag and legacy `[approvals]
policy` key use a separate write-only/auto/always/deny vocabulary for the
same underlying policies.

## Allowlists

The `run_command` program allowlist has a built-in default; the child-process environment allowlist does not.

`run_command` can already execute a curated built-in list with `[tools].run_allowlist` unset: common compilers/interpreters, their package managers, git, and read-only Unix utilities. It deliberately excludes shells (`sh`, `bash` — unrestricted execution defeats the allowlist), file-mutating programs (`rm`, `cp`, `mv`, `mkdir`, and similar — `run_command` is not gated by the write-path blocklist, so a mutating program here would bypass it entirely), `find` (its `-exec`/`-delete` flags run arbitrary commands and delete files), and networking/container/infra tools (`curl`, `wget`, `ssh`, `docker`, `kubectl`, `terraform`). `[tools].run_allowlist` extends the built-in list; `[tools].run_allowlist_only` replaces it entirely, for a closed allowlist.

The child-process environment allowlist has no compiled default: `[tools].env_allowlist` is the only source, and with it unset, child processes inherit no environment.

A fuller, opt-in `run_allowlist` (including shells and network clients, extending the built-in list) and a starting `env_allowlist` ship in `.mivia/mivia.toml.example`. Copy it and trim it to what your project needs. In `env_allowlist`, a trailing `*` declares a prefix rule (for example, `"GIT_*"`). Because there is no built-in environment list to extend or replace, `env_allowlist_only` behaves identically to `env_allowlist`; `run_allowlist_only` differs from `run_allowlist` in that it replaces the built-in `run_command` default instead of extending it.

`[tools].env_allow_keyword_blocklist` is the companion subtractive filter. A variable admitted by a `*` prefix rule is dropped when its name contains any listed substring. The example lists `SECRET`, `TOKEN`, `PASSWORD`, and `API_KEY`. Exact `env_allowlist` entries are never dropped, so a build that needs `FOO_TOKEN` names it outright. Unset means prefix rules admit everything they match.

## Write-path blocklist

`[tools].write_path_blocklist` names workspace-relative paths or directories that the write tools of workflow agent steps (`write_file`, `search_replace`, `multi_edit`, `delete_file`) refuse to change. It applies to workflow runs only, not to the interactive session.

Nothing is blocked by default: protection is opt-in, not a built-in set a project must opt out of. `[tools].write_path_blocklist_remove` removes an entry from the effective set — an entry a project (or a layer above it) added, never a compiled-in default, since there is none. An entry in both keys is a config error.

Entries use forward slashes. At load, mivia trims whitespace and cleans each entry, so `" go.mod/ "` becomes `"go.mod"`. An entry that is empty, that resolves to the workspace root, or that is absolute is a config error: mivia refuses to start rather than silently ignore a blocklist entry that can never match.

This key is a project decision. A project that omits it leaves every path writable by workflow agents. That includes `.git`, the live Git hooks, this blocklist, and the workflow definition the run executes. The recommended starting values are `.git`, `.githooks`, `scripts`, `Makefile`, `.mivia/hooks`, `.claude`, the config file itself, and `.mivia/policy`. install_git_hooks.sh points the hooks path at `.githooks`. The hooks therefore live outside `.git`. Blocking `.git` alone does not protect the hooks. `scripts` covers both the hook implementations and the gate scripts those hooks run: with `scripts` writable, an agent rewrites a gate to exit 0 and every check the protected hook invokes passes. `Makefile` decides which gates run at all. The config file must block itself. An agent that can edit it can empty this key and give itself write access to every other entry. `.mivia/policy` holds the pattern files the hook guard reads. `.mivia/hooks` holds the guard script itself, and `.claude` holds `settings.json`, which registers that guard as a PreToolUse handler; either one writable lets an agent silently disable enforcement. A project that also wants workflow-run protection for its control-surface trees (`.mivia/`, `.agents/`, `.claude/`), its Go module files, or its workflow definitions lists them here too. `.mivia/mivia.toml.example` ships this set, and this repository's own `.mivia/mivia.toml` uses it. `scripts/verify_agent_config.py` fails the build when the live config stops covering it.

## Redaction and persisted orchestration history

`[privacy].redaction_patterns` and `[privacy].redaction_key_names` control redaction in displayed tool previews, output, and event bodies. They do not redact SQLite task inputs or result content at rest. The example configuration provides starting patterns. Adapt and test them for your workspace.

`[subagents].store_backend = "sqlite"` persists orchestration state. By default the database is in the current user's cache directory under a workspace-derived name. Set `store_path` to choose a different location.

That state includes each task's full input payload, recorded for recovery support and execution history.

Two consequences worth knowing before you enable it:

- Task inputs and results are written unredacted at rest, even when `[privacy]` patterns are configured. Treat the chosen store location as sensitive workspace data. Do not put secrets in task prompts.
- Permissions, scopes, roles, and caller identity are deliberately not stored. They are never written to the run record or restored from it. A resumed run uses the permissions of whoever resumes it. Editing the store file cannot grant new permissions. Resource limits (timeout, budget, depth) are restored but clamped to your current configuration.

## Interactive turn ceiling

`[chat] max_steps` bounds one turn's agent loop. `0` means unlimited, and this is the default when unset. `/steps` overrides it for the current session.

## Unacted-turn continuation

`[chat] max_unacted_continuations` bounds how many times one turn is continued after it announced work and then ended without calling a single tool - the "I am going to dispatch four agents", no tool call, turn over shape. `0`, the default, disables the mechanism, so a fresh install behaves exactly as before.

Set it to `1` for a model that narrates its plan instead of acting on it. Whether a model needs this is a property of the model, which is why it is an operator switch and not a default. Values above `3` are clamped to `3`: every continuation is a full extra provider call on a turn that already answered.

A continuation appends a short bracket-labelled notice to the turn's own history and keeps the same loop running, so the model keeps what it said and continues from its plan rather than restarting. Each continuation costs one extra provider call and counts as an ordinary step of the turn, so `max_steps` stays the exact ceiling on one turn's provider calls, continuations included. The notice persists in session history, labelled `[mivia: …]` so a later turn cannot read it as the user's own words.

A caller that disabled provider replays (subagent and workflow paths that set `DisableProviderReplay`) never continues, whatever this key says: a continuation is a replay.

A turn is continued only when **all** of these hold: the run ended with no tool calls; the turn called no tool at all; tools were advertised; the answer was not empty; and the text reads as a promise of tool work. The zero-tool-call rule is what makes it safe - nothing ran, so nothing can run twice. A turn that called one tool and then narrated the next step is never continued.

Text that defers to the user - "let me know if you'd like me to run the tests", "I need to check with you first" - is never continued, even though it matches the promise pattern. That is the one false positive that would cost more than a wasted call: it would run a tool the model deliberately handed back for approval.

Root chat turns only. Sub-agent loops never continue themselves, whatever this key says.

The last condition is a best-effort, English-oriented text heuristic. It will miss promises in other languages and in unusual phrasing; a miss costs nothing, because the turn then ends exactly as it does today. A false positive costs one provider call whose notice explicitly allows the model to answer that no further work is needed. A message ending in a question is never continued.

## Turn request deadline

`[chat] request_timeout_seconds` bounds one LLM request in a root chat turn, tools on or off. Unset or `0` resolves to the built-in default of `900` seconds (15 minutes). The deadline rides the request context, so a spent budget reports as a terminal deadline, not a transient transport fault.

The bound covers the LLM request only. Context preparation, the summarizer call, and the durable commit keep their own budgets.

A deadline that fires mid-stream ends the turn as an interrupt, not as an error: the text already streamed stays in history and is persisted, and the partial answer comes back as the reply. Raise this value for models that think for a long time before they answer.

A deadline interrupt and a `Ctrl+C` interrupt differ in one way after the turn ends. `Ctrl+C` cancels the caller's context, which suppresses the turn-boundary compaction pass. A deadline fires on the provider's own request context and leaves the caller's context live, so the compaction pass runs, and it can make a summary call to the provider that just timed out. That call carries the summarizer's own 20-second bound and degrades to structural-only compaction when it fails, so the cost is one bounded attempt.

## Bounded prompt budget

`[chat] max_prompt_tokens` caps the per-request prompt budget in tokens. It has no default, and leaving it unset is the normal setting.

When unset, the prompt budget is the model window minus the output reserve (for example, `616000` on `deepseek-v4-flash`), so each model runs to its own capacity: a 1M-window model gets a 1M budget and a 200k one gets 200k. One cap applied over a mixed catalogue instead holds every model to the smallest, which is why no value is recommended here.

Set it when you want compaction to fire earlier than the model's own window would cause. History compacts at 80% of the budget, targeting 50%, so a smaller budget means more frequent and cheaper compactions, and a larger one means fewer and larger summarizer calls that invalidate more of the prefix cache. The dial is recall versus price: larger values keep more history in the prompt at higher cost. Any explicit value up to `10000000` (10M) is accepted.

`mivia doctor` and `mivia config show` always report the active budget as `prompt_budget`, the number the context gauge divides by and compaction measures against, which is stated nowhere else. It names where the budget came from: the model window, or an explicit cap. When a cap holds the budget below half the model's declared window, the line names the window too, because a large model held to a small budget otherwise reads as a small model in every surface that shows it.

## Tool result ceiling

`[tools] max_tool_result_bytes` caps each tool result stored in agent-loop history, in bytes. Default is `0`, which means uncapped. The per-tool budgets (`max_read_bytes`, `max_output_bytes`, tool-declared limits) are the bound. The one knob governs both the interactive session loop and nested sub-agent loops, so a sub-agent never sees a different ceiling than the session that spawned it.

Set a positive value (minimum 1024; smaller positive values are a config error) when running small-context models. When a cap is set, `read_file` pre-clamps its own byte budget below it, and the code-navigation tools (`find_references`, `list_symbols`, `go_to_definition`) tighten their JSON budgets to fit.

Set it to `4000` to match the fixed ceiling small-context models need.

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

`[context.summary] enabled` (default `true`) turns on the bounded provider call that summarizes what context compaction dropped. The call uses the session's provider and model. On auto compaction, the validated summary is injected into the next request as a host-authored `context-summary` message. A manual `/compact` requests the same summary: the reply is appended to the live session history as the `context-summary` message, and a bounded form is stored on the durable checkpoint. A session resumed from storage replays the structural history; the stored summary is not re-rendered on load.

Two more conditions must hold, or the summary stays off: a configured `[privacy]` redaction policy, and a resolved provider endpoint. A summary the redaction policy refuses is dropped, never sent or stored.

Any summary failure - transport error, malformed reply, redaction refusal, over-budget reply - degrades silently to structural-only compaction. A turn never fails because of the summary call.

The summarize request carries bounded quotes of the dropped messages' real content (user and assistant text plus truncated tool results, at most 16 KiB, newest first). An excerpt the `[privacy]` policy flags is dropped from the request; tool-call arguments and assistant reasoning are never included.

## Provider stream watchdogs

`[provider]` carries three process-wide watchdog bounds for provider reads. Each key takes seconds. An unset or non-positive value keeps the compiled default.

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `stream_first_byte_timeout_seconds` | int | `240` | Wait for the first byte of a provider read. Raise it for hard-thinking models |
| `stream_idle_timeout_seconds` | int | `100` | Gap between successive bytes once the first byte arrived |
| `stream_content_idle_timeout_seconds` | int | `90` | Gap between successive content chunks on an SSE stream. A keepalive trickle does not reset it |

Above the watchdogs sits the derived HTTP client wall, the absolute per-attempt transport bound: the maximum of the 15-minute floor and every configured per-request budget (`[chat] request_timeout_seconds`, `[subagents] default_request_timeout_seconds`) plus a 60-second margin. Because the wall derives from the budgets, a spent budget always reports as its own terminal deadline, never as a transport fault.

## Provider wire dump

`MIVIA_PROVIDER_AUDIT_DIR` names a directory that receives one JSONL file per session (`<session-id>.jsonl`), with one line per agent-loop iteration: the request this host built (model, reasoning level and dialect, `tool_choice`, advertised tool names, the full replayed history) and the response the provider returned (finish reason, content, reasoning, tool calls, token and cache usage). Unset (the default) wires no hook, so the seam costs nothing when it is off.

Use it when a turn ends with no visible work: the dump is the only way to tell a model that answered with nothing from a request this host built wrong.

The file holds prompts and model output **in cleartext unless you configured redaction**. Every captured string passes through the process-wide redaction policy, and tool-call arguments additionally go through key-name elision - but that policy comes from `[privacy] redaction_patterns` / `redaction_key_names`, which are empty by default. A workspace that configured neither redacts nothing, and the dump is then a plaintext prompt log. Each field is capped at 32 KiB. Paths this code creates are made `0700`/`0600`; an existing directory keeps whatever permissions it already has, so name a private one. Writing through a symlink is refused outright.

There is no size cap or rotation. Every iteration writes the whole replayed history, so growth is `iterations × history` - a long session produces a large file quickly. Point the directory outside the workspace, so a dump can never be committed, and delete it when the investigation ends.

One file per session id. A run built without a session id writes to `session.jsonl`, which several such runs in one process share.

A dump target that cannot be written is reported once and then latched off for the rest of the process: the debugging aid never fails the turn it is observing, and never retries a target it already knows is broken.

## Subagent knobs

| Key | Type | Default | Meaning |
|-----|------|---------|---------|
| `max_workers` | int | `0` (unlimited) | Goroutines for concurrent tasks |
| `max_depth` | int | `0` (unlimited) | Nesting depth |
| `max_fanout` | int | `0` (unlimited) | Parallel sub-tasks per level |
| `nested_steps` | int | `0` (unlimited) | Sub-agent loop steps per turn |
| `default_timeout_seconds` | int | `0` | Per-task orchestration timeout; `0` = safety bound (12 hours) |
| `default_request_timeout_seconds` | int | `0` | Per-LLM-request timeout for subagent turns; `0` = built-in 30-minute default (1800s). The derived HTTP client wall stays above this budget plus a 60-second margin, so the budget itself ends an overlong call |
| `default_total_timeout_seconds` | int | `0` | Whole-subagent wall-clock budget and last-resort termination guarantee; `0` = built-in 60-minute default (3600s); negative = off. A trickling provider connection cannot pin a run past this bound. A tighter per-task timeout from the caller wins. Negative: an unset-timeout run has no handler-level bound and stays bounded only by workflow policy |
| `wire_stream` | bool | `true` | Nested subagent LLM calls go out with `stream:true` to the provider's SSE endpoint; the full answer is assembled before the call returns, so the non-stream contract holds. See the paragraph below |
| `default_budget` | int | `0` | Per-task admission-control cost cap (not a token meter - never enforced against actual usage); `0` = built-in safe default (1,000,000 total per batch); negative = unlimited |
| `store_backend` | string | `"memory"` | Outside chat: `"memory"` (ephemeral) or `"sqlite"` (durable) |
| `store_path` | string | `~/.mivia/context.db` (chat); platform cache dir (non-chat orchestration) | One SQLite file for chat sessions, context, worktree routes, and runs |

`wire_stream` (default `true`) changes the transport of nested subagent LLM calls, not their contract: the request goes to the provider's SSE endpoint with `stream:true`, and the full answer is assembled before the call returns. The change exists because a provider connection can trickle keepalive bytes forever while the model answer never advances; byte-level idle watchdogs cannot tell that apart from model thinking. The content-idle bound closes this gap: a turn that receives no chunk which would advance the answer within the bound (default 90 seconds, `[provider] stream_content_idle_timeout_seconds`) is a stall. A stalled call aborts, retries at once on a fresh connection (2 retries), then falls back to one plain non-stream request. A provider that rejects the stream request with a JSON error, or stalls a stream attempt without ever sending one data line, is remembered for the life of the process: later calls skip the stream endpoint. Set `wire_stream = false` to keep every nested call on the plain non-stream endpoint. The content-idle bound is independent of `[provider] stream_idle_timeout_seconds`, which still governs plain byte-idle on live streaming turns. Workflow child runs inherit the behavior through their handlers.

(This default was briefly flipped off, then restored, during investigation of a real incident where dispatch_tasks batches never completed - see `default_budget` above and internal/subagents/subagents.go's `DefaultMaxBudget`: the actual cause was an unrelated admission-control default rejecting realistic task budgets before any provider call was made, confirmed by live reproduction. A dedicated concurrency+stall stress test against wire_stream found no hang: internal/provider/openai_compat_turnstream_concurrency_test.go.)

For `mivia chat`, mivia uses one SQLite file for all durable chat state: sessions, context, worktree routes, and runs. When `store_path` is unset, mivia uses the shared global path `~/.mivia/context.db`, so every workspace on the machine has the same chat history by default. Sessions stay isolated inside that shared file by workspace ID: two projects never see each other's sessions even though they share one file. A worktree never creates another chat database. Set `store_path` to give one workspace its own separate file instead of the shared default.

Workflow child runs register for `inspect_agents`, `cancel_run`, and `join_run` only when a chat session owns them. Runs started without an owning session (CLI commands, review panels, one-shot catalog flows) stay manageable through the workflow tools instead; this fail-closed skip is by design and logs one line per skipped registration.

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

Rendering is directional today. Line-mode `--json` renders turns received from other processes as `external_*` NDJSON events. The classic REPL and line mode publish their own turns to the hub but do not yet render turns received from other processes. The TUI publishes nothing: it never joins the hub, and its session is constructed with no event bus. The full event vocabulary is specified in [Wire schema](wire-schema.md).

`default_request_timeout_seconds` never needs to be set below `default_timeout_seconds`. The outer orchestration timeout cancels the turn first. The HTTP client wall is derived from the configured request budgets: it is the maximum of the 15-minute floor and every configured per-request budget plus a 60-second margin. The wall therefore never cuts a request before its own budget does; a spent budget reports as a terminal deadline, not a transport fault. The stream watchdogs stop a hung provider call long before either bound.

## See also

- [Product overview](overview.md)
- [Coding agent mode](agent.md)
- [Workflow guide](workflows-guide.md)
- [Security and privacy](../security/overview.md)
