<p align="center">
  <img src="docs/mivia-logo.png" alt="mivia" width="120">
</p>

<h1 align="center">mivia</h1>

<p align="center">A local CLI coding agent. Chat, tools, workflows, and multi-agent orchestration, in your terminal.</p>

<p align="center">
  <a href="https://github.com/MiviaLabs/mivia-agent/actions/workflows/ci.yml"><img src="https://github.com/MiviaLabs/mivia-agent/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8.svg" alt="Go 1.25+">
</p>

mivia reads, searches, and edits files in your project. It runs commands, such as your test suite. It can also run multi-step workflows in an isolated worktree, with a durable run record for every step.

Your files stay on your machine. mivia sends your prompts and the context it selects to the AI provider you configure; nothing else leaves your machine.

## Quick start

Requires Go 1.25+ and an API key for a supported provider (DeepSeek by default). mivia ships as source; there is no prebuilt binary yet.

```bash
git clone https://github.com/MiviaLabs/mivia-agent.git
cd mivia-agent
make build              # produces ./mivia

export DEEPSEEK_API_KEY=sk-...
./mivia doctor          # verify the key is visible; never prints it
./mivia chat
```

One-shot mode:

```bash
./mivia chat -p "what does this project do?"
```

Full dev setup (hooks, tests, verify gates): see [Contributing](docs/contributing.md). Provider and config options: see [Configuration](docs/product/config.md).

<p align="center">
  <img src="docs/mivia-welcome.png" alt="mivia TUI welcome screen" width="32%">
  <img src="docs/mivia-help.png" alt="mivia TUI help dialog" width="32%">
  <img src="docs/mivia-models.png" alt="mivia TUI model selection dialog" width="32%">
</p>

## What it does

- Chat with tool access: read, search, edit files; run allowed commands.
- Web search.
- Workflows: durable, multi-step processes with retries and evidence gates.
- Worktrees: isolated checkouts for a workflow run, so your working tree stays clean.
- Agents and skills: named specialists you can route work to.

## Architecture

```mermaid
flowchart LR
    You["you"] --> Chat["mivia chat"]
    Chat --> Files["project files"]
    Chat --> Config["config"]
    Chat --> Agents["agents & skills"]
    Chat --> Workflows["workflows"]
    Workflows --> Worktree["worktree"]
    Workflows --> Ledger["run ledger"]
    Chat --> Provider["AI provider"]
```

Everything under `mivia chat` runs locally except the `AI provider` edge, which carries only your prompt and selected context.

## Docs

| Guide | Covers |
|-------|--------|
| [Product overview](docs/product/overview.md) | What mivia is, plain-language walkthrough |
| [Configuration](docs/product/config.md) | Providers, keys, settings |
| [Coding agent mode](docs/product/agent.md) | Chat, tools, agents, skills |
| [Workflows](docs/product/workflows.md) | Step-by-step processes |
| [Workflow guide](docs/product/workflows-guide.md) | Workflow commands, the built-in workflow |
| [Security and privacy](docs/security/overview.md) | Data handling |
| [Architecture](docs/architecture/overview.md) | System design |
| [Contributing](docs/contributing.md) | Build, test, and PR process |

## License

[MIT](LICENSE)
