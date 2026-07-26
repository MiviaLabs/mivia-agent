# ADR 0004: Provider Config and Credentials

## Status

Accepted

## Context

mivia needs pluggable LLM providers. DeepSeek is the default; OpenRouter is second.
Secrets must never live in committed config.

## Decision

1. **Non-secret config** in TOML (`mivia.toml`), search order:
   - `$MIVIA_CONFIG`
   - `./mivia.toml`
   - `~/.config/mivia/config.toml`
2. **Secrets** only via process environment and/or an env file referenced by config
   (default: `./.env` then `~/.config/mivia/.env`). Process env wins over file.
3. **Provider adapters** implement a shared OpenAI-compatible chat client with per-provider presets.
4. **Defaults:**
   - Provider: `deepseek`
   - Model: `deepseek-v4-flash`
   - Alternate DeepSeek model for harder work: `deepseek-v4-pro` (user selects via config or `--model`)

## Consequences

- Clear separation of config vs credentials
- Easy to add more OpenAI-compatible providers
- Operator must manage `.env` outside git (enforced by ignore + secret scan)
