# Configuration

## Files

| Kind | Purpose | Secrets? |
|------|---------|----------|
| TOML config | Provider, model, paths | No |
| Env file / process env | API keys | Yes |

### Config search order

1. `$MIVIA_CONFIG`
2. `./.mivia/mivia.toml`
3. `~/.mivia/mivia.toml`

### Env file search order (if root-level `env_file` is unset)

1. `./.env`
2. `~/.mivia/.env`

Process environment variables always override values from the env file.

The workspace `.env` deliberately stays beside the repository files at
`./.env`: direnv, Docker Compose, and other dotenv tooling already use that
convention. Only the user-level env file lives inside the `~/.mivia/` namespace.

## Defaults

| Setting | Default |
|---------|---------|
| Provider | `deepseek` |
| DeepSeek example model | `deepseek-v4-flash` (declare it explicitly) |
| DeepSeek advanced example | `deepseek-v4-pro` (declare it, then use `default_model` or `--model`) |
| OpenRouter example | `openai/gpt-4o-mini` (declare it under `providers.openrouter`) |
| ZAI example | `glm-5.2` (declare it under `providers.zai`) |

## Set up a provider

Set the provider API key in the process environment or an env file, then use
`mivia doctor` to confirm that mivia can find it. `doctor` prints key presence,
never its value.

```bash
export DEEPSEEK_API_KEY=...
mivia doctor
mivia chat -p "hi"
```

### From a source checkout

For a source checkout, keep the project config in the workspace namespace and
the workspace credentials file at the repository root:

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
models = [{ name = "glm-5.2", context_window_tokens = 1000000, max_output_tokens = 128000 }]
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"
```

z.ai serves two OpenAI-compatible endpoints and a key works on exactly one of
them. Pay-as-you-go keys use `https://api.z.ai/api/paas/v4`; GLM Coding Plan
keys use `https://api.z.ai/api/coding/paas/v4`. A Coding Plan key on the
pay-as-you-go endpoint has no balance to spend, so every request fails with
`code 1113` regardless of the model, and models the plan does not serve on that
endpoint fail earlier with `code 1211` or `1212`. mivia reports the code and
what it means; it never forwards z.ai's own error text, which echoes request
content back.

### Explicit model catalog

Every provider must declare a non-empty `models` array. Each entry is an object
with a provider-local `name`, `context_window_tokens`, and optional positive
`max_output_tokens`. The array is the
complete selectable catalog: `--model`, `/model`, the TUI picker, and resumed
sessions may select only its entries. `default_model` sets the startup default
and must be in `models`; otherwise the first entry is used.

Omitting `models`, using an empty array, or relying on a provider registry
default is invalid. mivia does not discover models remotely or accept arbitrary
model names. Model IDs are kept intact, including slash-containing IDs such as
`openai/gpt-4o-mini`; duplicate IDs are allowed across providers but not within
one provider. Providers without credentials remain visible in the catalog and
are disabled for selection.

`context_window_tokens` is the model's physical prompt-plus-completion limit.
When set, `max_output_tokens` is the model's response ceiling; it must be below
the context window. The usable prompt budget reserves the tighter of this value
and `[chat].max_tokens`, further limited by `max_prompt_tokens` when set.
`config show` and `doctor` display
each catalog entry as `provider/model:context_window_tokens` and show the
active usable prompt budget.

```bash
DEEPSEEK_API_KEY=...
OPENROUTER_API_KEY=...
ZAI_API_KEY=...
```

### Installed binary

Create `~/.mivia/mivia.toml` and, if desired, `~/.mivia/.env` using the
settings above. Leave the root-level `env_file` unset to use the default
`~/.mivia/.env`, or set it explicitly to that path. Alternatively, set the API
key in the process environment and run with the built-in defaults. There is no
`config init` command.

## File-backed agent definitions

Named agents are separate TOML files, one definition per file:

- user-owned definitions: `~/.mivia/agents/<name>.toml`
- workspace definitions: `<workspace>/.mivia/agents/<name>.toml`

Create those two directories as needed and copy definitions into the matching
destination. The filename is canonical: `<name>.toml` must contain the same
lowercase `name`. Agent files are not inline `[agents]` configuration.

The accepted schema is:

| Field | Meaning and omission semantics |
|---|---|
| `name` | Required; must match the filename and pass the lowercase name rules. |
| `description` | Optional bounded display text. |
| `inherits` | Optional same-origin parent; only file-backed agents may be parents. |
| `tools` | Optional full allowlist; mutually exclusive with `tools_add`/`tools_remove`. |
| `tools_add`, `tools_remove` | Optional deltas applied to the inherited tool list. |
| `disallowed_tools` | Optional additional denylist applied before the allowlist. |
| `skills` | Omitted = all trusted skills; `[]` = none; a list = only those names. |
| `model` | Optional model identifier for spawned tasks, validated against the active provider catalog; it is not root model selection or a provider catalog. |
| `max_turns` | Omitted = caller/session default; `0` = unlimited; positive = cap. |
| `system_prompt` | Optional authored prompt text; workspace-origin prompt text remains subject to the user gate. |

An omitted root `tools` field resolves to the complete known workspace-tool
catalogue unless trusted `require_explicit_tools` is enabled. `tools = []` is
an explicit empty allowlist. The default `fail_on_empty_toolset` guardrail
rejects an empty effective set. Unknown keys, unknown tools or skills,
filename/name mismatches, invalid inheritance, and unsafe file boundaries fail
closed rather than producing a selectable definition.

User definitions win when a workspace definition has the same name. Workspace
agent files are still discovered when `load_workspace_config = false`; that
trusted user-only gate controls workspace `[chat]`/`[subagents]` prompts and
project skill handlers, not agent-file discovery. Inheritance cannot cross the
user/workspace trust boundary.

## Commands

```bash
mivia doctor          # paths + key presence (no secret values)
mivia config show     # resolved non-secret settings
mivia chat -p "hi"
mivia chat --model deepseek-v4-pro -p "harder question"
mivia chat --provider openrouter --model openai/gpt-4o-mini -p "hi"
mivia chat --provider openrouter -p "hi"
mivia chat --provider zai -p "hi"
```

## Tool safety policy

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only
source of the file-tool secret filter - nothing is compiled into the binary, so
an unconfigured workspace filters nothing. Recommended starting values ship in
`.mivia/mivia.toml.example`. Patterns match case-insensitively as substrings of
the workspace-relative path; exceptions take precedence.

This guards against accidental exposure, not against a determined agent:
`run_command` can build a path at runtime and reach the file anyway. With these
patterns unset, no paths are filtered.

## Allowlists are configuration-only

Neither the `run_command` program allowlist nor the child-process environment
allowlist is compiled into the binary. `[tools].run_allowlist` and
`[tools].env_allowlist` are the only sources: **with them unset, `run_command`
executes nothing and child processes inherit no environment.**

Recommended multi-ecosystem values ship in `.mivia/mivia.toml.example` - copy it
and trim it to what your project actually needs. The example includes powerful
programs, including shells and network clients; remove anything your workspace
does not need. In `env_allowlist`, a trailing
`*` declares a prefix rule (`"GIT_*"`). Because there is no built-in list to
extend or replace, `run_allowlist_only` and `env_allowlist_only` behave
identically to their plain counterparts.

`[tools].env_allow_keyword_blocklist` is the companion subtractive filter:
a variable admitted by a `*` prefix rule is dropped when its name contains any
listed substring (`SECRET`, `TOKEN`, `PASSWORD`, `API_KEY` in the example).
Exact `env_allowlist` entries are never dropped, so a build that genuinely
needs `FOO_TOKEN` names it outright. Unset means prefix rules admit everything
they match.

## Redaction and persisted orchestration history

`[privacy].redaction_patterns` and `[privacy].redaction_key_names` control
redaction in displayed tool previews, output, and event bodies. They do not
redact SQLite task inputs or result content at rest. The example configuration
provides starting patterns; adapt and test them for your workspace.

`[subagents].store_backend = "sqlite"` persists orchestration state. By
default the database is in the current user's cache directory under a
workspace-derived name. Set `store_path` to choose a different location.

That state includes **each task's full input payload**, recorded for recovery
support and execution history.

Two consequences worth knowing before enabling it:

- Task inputs and results are written unredacted at rest, even when `[privacy]`
  patterns are configured. Treat the chosen store location as sensitive
  workspace data and do not put secrets in task prompts.
- Authority is deliberately *not* stored. Permissions, scopes, roles and caller
  identity are never written to the ledger and are never restored from it: a
  resumed run runs under the identity and permissions of whoever resumes it, so
  editing the store file cannot grant privilege. Resource limits (timeout,
  budget, depth) are restored but clamped to your current configuration.

## Interactive turn ceiling

`[chat] max_steps` bounds one turn's agent loop. Unset uses the built-in default
of 100 steps. **`0` means unlimited** (this is the default when unset) `/steps` overrides it for the current session.

## Tool result ceiling

`[tools] max_tool_result_bytes` caps each tool result stored in agent-loop
history, in bytes. **Default is 0 = uncapped**: the per-tool budgets
(`max_read_bytes`, `max_output_bytes`, tool-declared limits) are the bound.
The one knob governs both the interactive session loop and nested sub-agent
loops, so a sub-agent never sees a different ceiling than the session that
spawned it.

Set a positive value (minimum 1024; smaller positive values are a config
error) when running small-context models that cannot afford large tool
outputs in history. When a cap is set, `read_file` pre-clamps its own byte
budget below it so its `… lines X–Y of Z` window header stays honest, and
`find_references` tightens its JSON budget to fit.

Rollback: `max_tool_result_bytes = 4000` restores the previous hardcoded
interactive-loop ceiling.

## Per-batch tool result budget

`[tools] batch_result_budget_bytes` bounds what **one tool batch** adds to
history, across all of its parallel calls together. **Default is 0 = off.**

`max_tool_result_bytes` bounds each call in isolation and cannot see the
others: when the model issues ten calls in one step, ten results each honestly
under the per-call ceiling still land in the context together. This key is the
only bound that sees the batch as a whole.

- `0` (default) - off. History is byte-for-byte what it is without the key.
- `-1` - derive it from the model's prompt budget (a quarter of it in bytes,
  never below 256 KiB). Inert when there is no prompt budget configured.
- a positive value - the literal byte budget. Minimum 16384; smaller positive
  values are a config error, because the first oversized result is re-cut to
  that floor regardless and the bound would be a fiction.

Over-budget results are **degraded, never failed**. The call already ran and
its side effects already happened, so its result is re-cut - or, once the
budget is spent, replaced by a truncation notice - naming the content
reference that holds the full body. The model pages it back with `read_output`
when it actually needs it. The last degraded result carries a one-line status
saying how much budget was left and how many results were degraded.

What the budget does **not** charge: lifecycle-hook advisory context (it has
its own 8 KiB bound), and truncation notices themselves. Results whose whole
body is already smaller than the notice that would replace them are kept
intact - error explanations are not worth trading for a pointer to
themselves. Results from tools whose output is scrubbed after the turn
(skill resources) are charged but never put behind a reference.

The key governs the interactive session loop and nested sub-agent loops
alike, and the budget resets per batch: cross-batch growth remains context
compaction's job.

## Web research response bound

`[tools] max_tavily_response_bytes` bounds a Tavily API response body, in
bytes. It governs the `search` tool's Tavily path and the `extract` tool.
Default 4194304 (4 MiB).

**Responses are never truncated.** This is not a cap on how much content
reaches the model: nothing fetched is ever cut. The bound exists so that the
maximum size of these tools' results is a known, finite number, which is what
lets the dispatcher's runaway-output backstop (below) be derived high enough to
clear an honest result. Before it existed, a single extracted page larger than
331776 bytes was destroyed wholesale - the request was made, the credit was
spent, and the model received `output budget exceeded` instead of the content.

The number is enforced in two places and declared once:

- on the response body, so the read is finite;
- on **every composed result** - the Tavily search and extract results, the
  extract empty-content echo, and the free-engine fallback - because
  composition does not always shrink the body it came from. `search` writes a
  bullet per result and formats the query into a header with Go's `%q`, both of
  which can outgrow their source bytes, and the extract echo is sized by the
  request rather than the response.

A result over the bound is **refused with an explicit error naming the bound
and this key**, never silently cut short and never quietly replaced by
fallback search-engine results. Raise the key if you hit it.

Nothing on these paths is truncated. Each search result's own content reaches
the model whole.

Unset, `0` and negative all mean "use the default". There is deliberately no
unlimited setting: an unlimited response could not be declared to the backstop,
and an undeclared result is exactly what the backstop destroys. Values outside
`1024`-`67108864` are rejected at load - below the floor every response fails,
and above the ceiling the backstop arithmetic can overflow and silently fall
back to its 256 KiB floor while the read stayed effectively unbounded.

This bound is **not** clamped by `max_tool_result_bytes`. That key caps what
the agent loop stores; clamping the wire bound to it would turn a soft ceiling
on stored results into a hard failure of every web search.

Installs with no Tavily API key are unaffected: neither tool can reach the
provider, so neither declares a provider-sized budget and the backstop stays
where it was.

Separately from these budgets, the tool dispatcher keeps a **runaway-output
backstop**: a result larger than the backstop fails outright rather than being
truncated. It is not a knob - it is derived so it can never bind below an
honest tool result: the largest tool-declared result budget (`max_read_bytes`,
`max_output_bytes`, `max_tavily_response_bytes`, `find_references`' JSON
budget) plus an input allowance
(64 KiB, covering results that echo request input such as `run_command`'s
argv header) plus 4096 bytes of slack for fixed tool framing (window headers,
truncation notices), floored at 256 KiB. Raising a per-tool budget raises the
backstop with it; only a tool exceeding the budgets it was actually granted
can trip it.

## See also

- [Product overview](overview.md)
- [Coding agent mode](agent.md)
- [Security and privacy](../security/overview.md)
