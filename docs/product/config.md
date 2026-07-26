# Configuration

## Files

| Kind | Purpose | Secrets? |
|------|---------|----------|
| TOML config | Provider, model, paths | No |
| Env file / process env | API keys | Yes |

### Config search order

1. `$MIVIA_CONFIG`
2. `./mivia.toml`
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

## Examples

Copy repo examples:

```bash
mkdir -p ~/.config/mivia
cp mivia.toml.example ~/.config/mivia/config.toml
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
```

```bash
DEEPSEEK_API_KEY=...
OPENROUTER_API_KEY=...
```

## Commands

```bash
mivia doctor          # paths + key presence (no secret values)
mivia config show     # resolved non-secret settings
mivia chat -p "hi"
mivia chat --model deepseek-v4-pro -p "harder question"
mivia chat --provider openrouter -p "hi"
```
