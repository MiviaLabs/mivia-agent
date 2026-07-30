# Product Overview

Mivia is a local CLI AI agent for software engineering. The shipped binary is `mivia`.

## Available now

- Interactive chat with DeepSeek (the default), OpenRouter, or ZAI
- Workspace-aware coding tools and optional web research tools
- Concurrent subagent orchestration with task status, results, and cancellation
- TOML configuration with provider credentials supplied through an env file or process environment
- Local quality gates for contributors (hooks, scans, and tests)

MCP integration is not part of the current product surface.

## First run

```bash
# Set DEEPSEEK_API_KEY in an env file or the process environment first.
mivia doctor
mivia chat -p "Help me understand this repository"
# For a harder DeepSeek task:
mivia chat --model deepseek-v4-pro -p "Explain this carefully"
```

Defaults: provider `deepseek`, model `deepseek-v4-flash`. Follow the [configuration guide](config.md) before the first run; it covers config locations, credentials, and safety policy.

## Non-goals (initial)

- Hosted multi-tenant control plane (see go-mivia platform separately)
- Replacing every vendor coding agent
- One OS process per subagent as the default runtime model

## Relationship to agentkit

`mivia-agentkit` was the MVP control-surface and CLI experiment (its CLI used a
different name). This repository is the product successor: binary `mivia`,
stricter docs ownership, ported production skills from `mivia-agent-skills`, and
leaner always-on gates.

## Guides

- [Configuration](config.md): providers, credentials, and policy controls
- [Coding agent mode](agent.md): tools, orchestration, and limits
- [Security and privacy](../security/overview.md): risk model and protections
- [Architecture](../architecture/overview.md) and [concurrency](../architecture/concurrency.md): implementation design
