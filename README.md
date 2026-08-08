# mivia

mivia is a helper program for your computer. It runs in a terminal window.

mivia uses an AI model to help you with a project. A project is a folder of files. mivia can read the files, search them, and edit them. It can run commands, such as tests.

mivia runs on your computer. Your files stay on your computer. mivia sends your questions to an AI provider that you choose. A provider is a company that runs an AI service. You give mivia an API key. An API key is a secret code. The key tells the provider who is asking.

## Quick start

You need three things:

1. The mivia program on your computer.
2. An API key for an AI provider.
3. A terminal window.

Step 1: set your API key.

```bash
export DEEPSEEK_API_KEY=sk-REPLACE-ME
```

Step 2: check that mivia is ready.

```bash
mivia doctor
```

`mivia doctor` prints whether mivia can find your key. It never prints the key itself.

Step 3: start a chat.

```bash
mivia chat
```

Type a question about your project. mivia reads the project and answers.

Ask one question and stop with `-p`:

```bash
mivia chat -p "what does this project do?"
```

To build mivia from source, see [Contributing](docs/contributing.md). For the full setup guide, see [Configuration](docs/product/config.md).

## What mivia can do

- Answer questions about a project.
- Read, search, and edit files.
- Run allowed commands, such as tests.
- Search the web.
- Run fixed step-by-step processes, called workflows.
- Work in a separate copy of the project, called a worktree.
- Use named specialists, called agents.

## How the pieces fit

```mermaid
flowchart LR
    You["You"] --> Chat["mivia chat"]
    Chat --> Files["Your project files"]
    Chat --> Config["Your settings"]
    Chat --> Agents["Agents and skills"]
    Chat --> Workflows["Workflows"]
    Workflows --> Worktree["Worktree"]
    Workflows --> Ledger["Run record"]
    Chat --> Provider["AI provider"]
```

Look at the arrows that point away from mivia chat. Each arrow is one thing mivia can reach. Your files and settings stay on your computer. Only your questions go to the AI provider.

## Guides

| Guide | What it covers |
|-------|----------------|
| [Product overview](docs/product/overview.md) | What mivia is and how the pieces fit |
| [Configuration](docs/product/config.md) | Providers, keys, and settings |
| [Coding agent mode](docs/product/agent.md) | Chat, tools, agents, and skills |
| [Workflows](docs/product/workflows.md) | Step-by-step processes |
| [Workflow guide](docs/product/workflows-guide.md) | Workflow commands and the built-in workflow |
| [Security and privacy](docs/security/overview.md) | How mivia protects your data |
| [Architecture](docs/architecture/overview.md) | How mivia is built |
| [Contributing](docs/contributing.md) | For people who build mivia |

## License

[MIT](LICENSE)
