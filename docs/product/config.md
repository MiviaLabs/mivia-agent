# Configuration

## Files

| Kind | Purpose | Secrets? |
|------|---------|----------|
| TOML config | Provider, model, paths | No |
| Env file / process env | API keys | Yes |

### Config search order

1. `$MIVIA_CONFIG`
2. `./.mivia/mivia.toml`
3. `~/.config/mivia/config.toml`

### Env file search order (if `env_file` unset)

1. `./.env`
2. `~/.config/mivia/.env`

Process environment variables always override values from the env file.

## Defaults

| Setting | Default |
|---------|---------|
| Provider | `deepseek` |
| DeepSeek model | `deepseek-v4-flash` |
| DeepSeek advanced model | `deepseek-v4-pro` (set via `model` or `--model`) |
| OpenRouter model | `openai/gpt-4o-mini` (when provider is openrouter) |
| ZAI model | `glm-5.2` (when provider is zai) |

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

Copy the examples into the standard user configuration location:

```bash
mkdir -p ~/.config/mivia
cp .mivia/mivia.toml.example ~/.config/mivia/config.toml
cp .env.example ~/.config/mivia/.env
# edit ~/.config/mivia/.env with real keys
```

```toml
[provider]
name = "deepseek"
env_file = "~/.config/mivia/.env"

[providers.deepseek]
model = "deepseek-v4-flash"
# For harder tasks:
# model = "deepseek-v4-pro"

[providers.openrouter]
model = "openai/gpt-4o-mini"

[providers.zai]
model = "glm-5.2"
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

```bash
DEEPSEEK_API_KEY=...
OPENROUTER_API_KEY=...
ZAI_API_KEY=...
```

### Installed binary

Create `~/.config/mivia/config.toml` and, if desired,
`~/.config/mivia/.env` using the settings above. Alternatively, set the API
key in the process environment and run with the built-in defaults. There is no
`config init` command.

## Commands

```bash
mivia doctor          # paths + key presence (no secret values)
mivia config show     # resolved non-secret settings
mivia chat -p "hi"
mivia chat --model deepseek-v4-pro -p "harder question"
mivia chat --provider openrouter -p "hi"
mivia chat --provider zai -p "hi"
```

## Tool safety policy

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only
source of the file-tool secret filter — nothing is compiled into the binary, so
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

Recommended multi-ecosystem values ship in `.mivia/mivia.toml.example` — copy it
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
of 100 steps. **`0` means unlimited** — a model stuck emitting tool calls will
run until you interrupt it — so the key is only absent, not zero, when you want
the default. `/steps` overrides it for the current session.

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
budget below it so its `… lines X–Y` window header always matches the lines
actually delivered, and `find_references` tightens its JSON budget to fit.

Rollback: `max_tool_result_bytes = 4000` restores the previous hardcoded
interactive-loop ceiling.

## See also

- [Product overview](overview.md)
- [Coding agent mode](agent.md)
- [Security and privacy](../security/overview.md)
