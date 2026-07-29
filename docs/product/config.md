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

## Examples

Copy repo examples:

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

```bash
DEEPSEEK_API_KEY=...
OPENROUTER_API_KEY=...
ZAI_API_KEY=...
```

## Commands

```bash
mivia doctor          # paths + key presence (no secret values)
mivia config show     # resolved non-secret settings
mivia chat -p "hi"
mivia chat --model deepseek-v4-pro -p "harder question"
mivia chat --provider openrouter -p "hi"
mivia chat --provider zai -p "hi"
```

## Secret path filter

`[tools].secret_path_patterns` and `[tools].secret_path_exceptions` are the only
source of the file-tool secret filter — nothing is compiled into the binary, so
an unconfigured workspace filters nothing. Recommended starting values ship in
`.mivia/mivia.toml.example`. Patterns match case-insensitively as substrings of
the workspace-relative path; exceptions take precedence.

This guards against accidents, not against a determined agent: `run_command` can
build a path at runtime and reach the file anyway.
